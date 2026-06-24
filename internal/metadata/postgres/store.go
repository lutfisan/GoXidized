package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goxidized/internal/scheduler"
	"goxidized/pkg/goxidized"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Store struct {
	pool *pgxpool.Pool
}

func (s *Store) ValidateAPIToken(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", pgx.ErrNoRows
	}
	sum := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(sum[:])
	var actorID string
	err := s.pool.QueryRow(ctx, `
SELECT actor_id FROM api_tokens
WHERE token_hash=$1
  AND revoked_at IS NULL
  AND (expires_at IS NULL OR expires_at > now())
LIMIT 1`, tokenHash).Scan(&actorID)
	return actorID, err
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	if dsn == "" {
		return nil, errors.New("postgres DSN is required")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

func (s *Store) Migrate(ctx context.Context) error {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version := entry.Name()
		var exists bool
		_ = s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&exists)
		if exists {
			continue
		}
		data, err := migrationFS.ReadFile("migrations/" + version)
		if err != nil {
			return err
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(data)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", version, err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES ($1) ON CONFLICT DO NOTHING`, version); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) UpsertDevices(ctx context.Context, devices []goxidized.Target) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	for _, d := range devices {
		tags, _ := json.Marshal(d.Tags)
		meta, _ := json.Marshal(d.Metadata)
		if _, err := tx.Exec(ctx, `
INSERT INTO devices (id, hostname, ip_address, port, vendor, device_group, site, role, tags, metadata, jump_host, credential_ref, enabled, telnet_enabled, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,now())
ON CONFLICT (id) DO UPDATE SET
hostname=EXCLUDED.hostname, ip_address=EXCLUDED.ip_address, port=EXCLUDED.port, vendor=EXCLUDED.vendor,
device_group=EXCLUDED.device_group, site=EXCLUDED.site, role=EXCLUDED.role, tags=EXCLUDED.tags,
metadata=EXCLUDED.metadata, jump_host=EXCLUDED.jump_host, credential_ref=EXCLUDED.credential_ref,
enabled=EXCLUDED.enabled, telnet_enabled=EXCLUDED.telnet_enabled, updated_at=now()`,
			d.ID, d.Hostname, d.IPAddress, d.Port, d.Vendor, d.Group, d.Site, d.Role, tags, meta, d.JumpHost, d.CredentialRef, d.Enabled, d.TelnetEnabled); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ListDevices(ctx context.Context) ([]goxidized.Target, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, hostname, ip_address, port, vendor, device_group, site, role, tags, metadata, jump_host, credential_ref, enabled, telnet_enabled
FROM devices ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []goxidized.Target
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetDevice(ctx context.Context, id string) (goxidized.Target, error) {
	row := s.pool.QueryRow(ctx, `
SELECT id, hostname, ip_address, port, vendor, device_group, site, role, tags, metadata, jump_host, credential_ref, enabled, telnet_enabled
FROM devices WHERE id = $1`, id)
	return scanTarget(row)
}

func (s *Store) RecordJobStart(ctx context.Context, job goxidized.Job) (goxidized.Job, error) {
	if job.ID == "" {
		job.ID = newID("job")
	}
	now := time.Now().UTC()
	if job.QueuedAt.IsZero() {
		job.QueuedAt = now
	}
	job.StartedAt = now
	job.UpdatedAt = now
	if job.Status == "" {
		job.Status = goxidized.StatusRunning
	}
	if job.Attempt == 0 {
		job.Attempt = 1
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO backup_jobs (id,target_id,device_group,trigger,actor,status,attempt,queued_at,started_at,updated_at,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		job.ID, job.TargetID, job.Group, job.Trigger, job.Actor, job.Status, job.Attempt, job.QueuedAt, job.StartedAt, job.UpdatedAt, now)
	return job, err
}

func (s *Store) RecordJobFinish(ctx context.Context, result goxidized.JobResult) error {
	if result.FinishedAt.IsZero() {
		result.FinishedAt = time.Now().UTC()
	}
	if result.ErrorText == "" && result.Err != nil {
		result.ErrorText = result.Err.Error()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	if _, err := tx.Exec(ctx, `UPDATE backup_jobs SET status=$2, updated_at=$3 WHERE id=$1`, result.JobID, result.Status, result.FinishedAt); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO backup_results (job_id,target_id,status,attempt,error_text,started_at,finished_at,duration_ms,revision_id,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$7)`,
		result.JobID, result.TargetID, result.Status, result.Attempt, result.ErrorText,
		result.StartedAt, result.FinishedAt, result.Duration.Milliseconds(), result.RevisionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) TryAcquireLease(ctx context.Context, lease scheduler.WorkerLease) (bool, error) {
	if err := lease.Validate(); err != nil {
		return false, err
	}
	var targetID string
	err := s.pool.QueryRow(ctx, `
INSERT INTO worker_leases (target_id, worker_id, job_id, expires_at, updated_at)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (target_id) DO UPDATE SET
    worker_id=EXCLUDED.worker_id,
    job_id=EXCLUDED.job_id,
    expires_at=EXCLUDED.expires_at,
    updated_at=EXCLUDED.updated_at
WHERE worker_leases.expires_at <= $5
   OR (worker_leases.worker_id = $2 AND worker_leases.job_id = $3)
RETURNING target_id`,
		lease.TargetID, lease.WorkerID, lease.JobID, lease.ExpiresAt, lease.Now).Scan(&targetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) RenewLease(ctx context.Context, lease scheduler.WorkerLease) (bool, error) {
	if err := lease.Validate(); err != nil {
		return false, err
	}
	tag, err := s.pool.Exec(ctx, `
UPDATE worker_leases
SET expires_at=$4, updated_at=$5
WHERE target_id=$1 AND worker_id=$2 AND job_id=$3 AND expires_at > $5`,
		lease.TargetID, lease.WorkerID, lease.JobID, lease.ExpiresAt, lease.Now)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) ReleaseLease(ctx context.Context, lease scheduler.WorkerLease) error {
	if lease.TargetID == "" || lease.WorkerID == "" || lease.JobID == "" {
		return errors.New("lease target id, worker id, and job id are required")
	}
	_, err := s.pool.Exec(ctx, `
DELETE FROM worker_leases
WHERE target_id=$1 AND worker_id=$2 AND job_id=$3`,
		lease.TargetID, lease.WorkerID, lease.JobID)
	return err
}

func (s *Store) SaveRevision(ctx context.Context, rev goxidized.Revision, meta goxidized.CommitMeta) error {
	rowID := newID("rev")
	trailers, _ := json.Marshal(rev.CommitTrailers)
	metaJSON, _ := json.Marshal(meta)
	_, err := s.pool.Exec(ctx, `
INSERT INTO config_versions (id,target_id,shard,path,content_sha256,commit_sha,parent_commit,changed,commit_trailers,commit_meta,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		rowID, rev.TargetID, rev.Shard, rev.Path, rev.ContentSHA256, rev.CommitSHA, rev.ParentCommit, rev.Changed, trailers, metaJSON, rev.CreatedAt)
	return err
}

func (s *Store) SaveDiff(ctx context.Context, diff goxidized.DiffResult) error {
	cats, _ := json.Marshal(diff.Categories)
	hits, _ := json.Marshal(diff.RuleHits)
	preview := diff.UnifiedDiff
	if len(preview) > 65535 {
		preview = preview[:65535]
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO config_diffs (target_id,from_revision,to_revision,added_lines,removed_lines,risk_level,categories,rule_hits,diff_preview,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now())`,
		diff.TargetID, diff.FromRevision, diff.ToRevision, diff.AddedLines, diff.RemovedLines, diff.Risk, cats, hits, preview)
	return err
}

func (s *Store) ListJobs(ctx context.Context, limit int) ([]goxidized.Job, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
SELECT id,target_id,device_group,trigger,actor,status,attempt,queued_at,started_at,updated_at
FROM backup_jobs ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []goxidized.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) GetJob(ctx context.Context, id string) (goxidized.Job, error) {
	return scanJob(s.pool.QueryRow(ctx, `
SELECT id,target_id,device_group,trigger,actor,status,attempt,queued_at,started_at,updated_at
FROM backup_jobs WHERE id=$1 ORDER BY created_at DESC LIMIT 1`, id))
}

func (s *Store) ListRevisions(ctx context.Context, targetID string, limit int) ([]goxidized.Revision, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
SELECT id,target_id,shard,path,content_sha256,commit_sha,parent_commit,changed,commit_trailers,created_at
FROM config_versions WHERE target_id=$1 ORDER BY created_at DESC LIMIT $2`, targetID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []goxidized.Revision
	for rows.Next() {
		rev, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rev)
	}
	return out, rows.Err()
}

func (s *Store) LatestRevision(ctx context.Context, targetID string) (goxidized.Revision, error) {
	return scanRevision(s.pool.QueryRow(ctx, `
SELECT id,target_id,shard,path,content_sha256,commit_sha,parent_commit,changed,commit_trailers,created_at
FROM config_versions WHERE target_id=$1 ORDER BY created_at DESC LIMIT 1`, targetID))
}

func (s *Store) Audit(ctx context.Context, ev goxidized.AuditEvent) error {
	if ev.ID == "" {
		ev.ID = newID("audit")
	}
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now().UTC()
	}
	meta, _ := json.Marshal(ev.Metadata)
	_, err := s.pool.Exec(ctx, `
INSERT INTO audit_events (id,actor_type,actor_id,action,target_id,credential_ref,job_id,request_id,source_ip,outcome,metadata,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		ev.ID, ev.ActorType, ev.ActorID, ev.Action, ev.TargetID, ev.CredentialRef, ev.JobID, ev.RequestID, ev.SourceIP, ev.Outcome, meta, ev.CreatedAt)
	return err
}

func (s *Store) ListAuditEvents(ctx context.Context, limit int) ([]goxidized.AuditEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
SELECT id,actor_type,actor_id,action,target_id,credential_ref,job_id,request_id,source_ip,outcome,metadata,created_at
FROM audit_events ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []goxidized.AuditEvent
	for rows.Next() {
		ev, err := scanAudit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func scanTarget(row pgx.Row) (goxidized.Target, error) {
	var t goxidized.Target
	var tagsRaw, metaRaw []byte
	err := row.Scan(&t.ID, &t.Hostname, &t.IPAddress, &t.Port, &t.Vendor, &t.Group, &t.Site, &t.Role, &tagsRaw, &metaRaw, &t.JumpHost, &t.CredentialRef, &t.Enabled, &t.TelnetEnabled)
	if err != nil {
		return t, err
	}
	_ = json.Unmarshal(tagsRaw, &t.Tags)
	_ = json.Unmarshal(metaRaw, &t.Metadata)
	return t, nil
}

func scanJob(row pgx.Row) (goxidized.Job, error) {
	var j goxidized.Job
	err := row.Scan(&j.ID, &j.TargetID, &j.Group, &j.Trigger, &j.Actor, &j.Status, &j.Attempt, &j.QueuedAt, &j.StartedAt, &j.UpdatedAt)
	return j, err
}

func scanRevision(row pgx.Row) (goxidized.Revision, error) {
	var rev goxidized.Revision
	var trailersRaw []byte
	err := row.Scan(&rev.ID, &rev.TargetID, &rev.Shard, &rev.Path, &rev.ContentSHA256, &rev.CommitSHA, &rev.ParentCommit, &rev.Changed, &trailersRaw, &rev.CreatedAt)
	if err != nil {
		return rev, err
	}
	_ = json.Unmarshal(trailersRaw, &rev.CommitTrailers)
	return rev, nil
}

func scanAudit(row pgx.Row) (goxidized.AuditEvent, error) {
	var ev goxidized.AuditEvent
	var metaRaw []byte
	err := row.Scan(&ev.ID, &ev.ActorType, &ev.ActorID, &ev.Action, &ev.TargetID, &ev.CredentialRef, &ev.JobID, &ev.RequestID, &ev.SourceIP, &ev.Outcome, &metaRaw, &ev.CreatedAt)
	if err != nil {
		return ev, err
	}
	_ = json.Unmarshal(metaRaw, &ev.Metadata)
	return ev, nil
}

func rollback(ctx context.Context, tx pgx.Tx) {
	_ = tx.Rollback(ctx)
}

func newID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}
