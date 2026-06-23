package worker

import (
	"context"
	"errors"
	"time"

	"goxidized/internal/pipeline"
	"goxidized/internal/scheduler"
	"goxidized/pkg/goxidized"
)

type Dialer interface {
	Dial(context.Context, goxidized.Target, goxidized.Credentials) (goxidized.Session, error)
}

type DriverLookup func(string) (goxidized.Driver, error)

type BackupRunner struct {
	Metadata     goxidized.MetadataStore
	Storage      goxidized.Storage
	Credentials  goxidized.CredentialProvider
	SSHDialer    Dialer
	TelnetDialer Dialer
	Drivers      DriverLookup
	Notifier     goxidized.Notifier
}

func (r *BackupRunner) Handle(ctx context.Context, req scheduler.Request) goxidized.JobResult {
	start := time.Now().UTC()
	job, err := r.Metadata.RecordJobStart(ctx, req.Job)
	if err != nil {
		return goxidized.JobResult{JobID: req.Job.ID, TargetID: req.Target.ID, Status: goxidized.StatusFailedStorage, Err: err, ErrorText: err.Error(), StartedAt: start, FinishedAt: time.Now().UTC()}
	}
	fail := func(err error) goxidized.JobResult {
		status := goxidized.StatusForFailure(goxidized.ClassifyError(err))
		res := goxidized.JobResult{JobID: job.ID, TargetID: req.Target.ID, Status: status, Attempt: job.Attempt, Err: err, ErrorText: err.Error(), StartedAt: start, FinishedAt: time.Now().UTC()}
		res.Duration = res.FinishedAt.Sub(start)
		_ = r.Metadata.RecordJobFinish(ctx, res)
		_ = r.audit(ctx, "backup_failed", req, job.ID, "failure", map[string]string{"status": string(status)})
		return res
	}

	creds, err := r.Credentials.Resolve(ctx, req.Target.CredentialRef)
	if err != nil {
		_ = r.audit(ctx, "credential_resolve", req, job.ID, "failure", nil)
		return fail(err)
	}
	_ = r.audit(ctx, "credential_resolve", req, job.ID, "success", map[string]string{"provider": creds.Source, "auth_type": creds.AuthType})

	driver, err := r.Drivers(req.Target.Vendor)
	if err != nil {
		return fail(&goxidized.BackupError{Category: goxidized.FailureCommand, Op: "driver lookup", Err: err})
	}
	dialer := r.SSHDialer
	if req.Target.TelnetEnabled || (req.Target.Metadata != nil && req.Target.Metadata["transport"] == "telnet") {
		dialer = r.TelnetDialer
	}
	if dialer == nil {
		return fail(&goxidized.BackupError{Category: goxidized.FailureConnect, Op: "transport", Err: errors.New("dialer not configured")})
	}
	sess, err := dialer.Dial(ctx, req.Target, creds)
	if err != nil {
		return fail(err)
	}
	defer sess.Close()

	if err := driver.Prepare(ctx, sess); err != nil {
		return fail(err)
	}
	cfg, err := driver.FetchConfig(ctx, req.Target, sess)
	if err != nil {
		return fail(err)
	}
	norm, err := driver.Normalize(ctx, cfg)
	if err != nil {
		return fail(err)
	}
	redacted, report, err := driver.Redact(ctx, norm)
	if err != nil {
		return fail(err)
	}

	previous, previousRev, _ := r.Storage.Latest(ctx, req.Target.ID)
	meta := goxidized.CommitMeta{
		JobID: job.ID, Trigger: job.Trigger, Actor: job.Actor, RulesetVersion: pipeline.RulesetVersion,
		RedactionReport: report, DeviceMetadata: cfg.Metadata,
	}
	rev, err := r.Storage.Save(ctx, req.Target, *redacted, meta)
	if err != nil {
		return fail(&goxidized.BackupError{Category: goxidized.FailureStorage, Op: "git save", Err: err})
	}
	diff := goxidized.DiffResult{TargetID: req.Target.ID, ToRevision: rev.ID, Risk: goxidized.RiskLow}
	if previousRev.ID != "" && rev.Changed {
		diff, err = pipeline.UnifiedDiff(ctx, req.Target.ID, previousRev.ID, rev.ID, previous.Content, redacted.Content)
		if err != nil {
			return fail(&goxidized.BackupError{Category: goxidized.FailureStorage, Op: "diff", Err: err})
		}
	}
	meta.Risk = diff.Risk
	if err := r.Metadata.SaveRevision(ctx, rev, meta); err != nil {
		return fail(&goxidized.BackupError{Category: goxidized.FailureStorage, Op: "metadata revision", Err: err})
	}
	if rev.Changed {
		if err := r.Metadata.SaveDiff(ctx, diff); err != nil {
			return fail(&goxidized.BackupError{Category: goxidized.FailureStorage, Op: "metadata diff", Err: err})
		}
	}
	status := goxidized.StatusSuccessNoChange
	if rev.Changed {
		status = goxidized.StatusSuccessChanged
	}
	res := goxidized.JobResult{JobID: job.ID, TargetID: req.Target.ID, Status: status, Attempt: job.Attempt, StartedAt: start, FinishedAt: time.Now().UTC(), RevisionID: rev.ID}
	res.Duration = res.FinishedAt.Sub(start)
	_ = r.Metadata.RecordJobFinish(ctx, res)
	_ = r.audit(ctx, "backup_complete", req, job.ID, "success", map[string]string{"status": string(status), "revision": rev.ID})
	if r.Notifier != nil && rev.Changed {
		_ = r.Notifier.Notify(ctx, goxidized.Event{Type: goxidized.EventConfigChanged, TargetID: req.Target.ID, Message: "configuration changed", Diff: diff.UnifiedDiff, Timestamp: time.Now().UTC()})
		if diff.Risk == goxidized.RiskHigh || diff.Risk == goxidized.RiskCritical {
			_ = r.Notifier.Notify(ctx, goxidized.Event{Type: goxidized.EventHighRiskDiffDetected, TargetID: req.Target.ID, Message: "high-risk configuration change detected", Diff: diff.UnifiedDiff, Timestamp: time.Now().UTC()})
		}
	}
	return res
}

func (r *BackupRunner) audit(ctx context.Context, action string, req scheduler.Request, jobID string, outcome string, metadata map[string]string) error {
	return r.Metadata.Audit(ctx, goxidized.AuditEvent{
		ActorType: "system", ActorID: req.Job.Actor, Action: action, TargetID: req.Target.ID,
		CredentialRef: req.Target.CredentialRef, JobID: jobID, Outcome: outcome, Metadata: metadata, CreatedAt: time.Now().UTC(),
	})
}
