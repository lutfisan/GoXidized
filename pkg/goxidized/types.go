package goxidized

import (
	"net"
	"strconv"
	"time"
)

type Target struct {
	ID            string            `json:"id"`
	Hostname      string            `json:"hostname"`
	IPAddress     string            `json:"ip_address"`
	Port          int               `json:"port"`
	Vendor        string            `json:"vendor"`
	Group         string            `json:"group"`
	Site          string            `json:"site,omitempty"`
	Role          string            `json:"role,omitempty"`
	Tags          []string          `json:"tags,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	JumpHost      string            `json:"jump_host,omitempty"`
	CredentialRef string            `json:"credential_ref"`
	Enabled       bool              `json:"enabled"`
	TelnetEnabled bool              `json:"telnet_enabled,omitempty"`
}

func (t Target) Address() string {
	if t.Port == 0 {
		return net.JoinHostPort(t.IPAddress, "22")
	}
	return net.JoinHostPort(t.IPAddress, strconv.Itoa(t.Port))
}

type Credentials struct {
	Username      string       `json:"username"`
	Password      SecretString `json:"password,omitempty"`
	PrivateKeyPEM SecretBytes  `json:"private_key_pem,omitempty"`
	EnableSecret  SecretString `json:"enable_secret,omitempty"`
	Source        string       `json:"source"`
	AuthType      string       `json:"auth_type,omitempty"`
}

type ConfigResult struct {
	TargetID   string            `json:"target_id"`
	FetchedAt  time.Time         `json:"fetched_at"`
	RawConfig  []byte            `json:"-"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	DurationMS int64             `json:"duration_ms"`
}

type RedactedConfig struct {
	TargetID string `json:"target_id"`
	Content  []byte `json:"content"`
}

type RedactionReport struct {
	SecretsFound int               `json:"secrets_found"`
	Categories   []string          `json:"categories,omitempty"`
	Fingerprints map[string]string `json:"fingerprints,omitempty"`
}

type RiskLevel string

const (
	RiskNone     RiskLevel = "none"
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

type DiffResult struct {
	TargetID     string    `json:"target_id"`
	FromRevision string    `json:"from_revision,omitempty"`
	ToRevision   string    `json:"to_revision,omitempty"`
	UnifiedDiff  string    `json:"unified_diff,omitempty"`
	AddedLines   int       `json:"added_lines"`
	RemovedLines int       `json:"removed_lines"`
	Risk         RiskLevel `json:"risk"`
	Categories   []string  `json:"categories,omitempty"`
	RuleHits     []string  `json:"rule_hits,omitempty"`
}

type CommitMeta struct {
	JobID            string            `json:"job_id"`
	Trigger          string            `json:"trigger"`
	Actor            string            `json:"actor"`
	RulesetVersion   string            `json:"ruleset_version"`
	RedactionReport  RedactionReport   `json:"redaction_report"`
	Risk             RiskLevel         `json:"risk"`
	DeviceMetadata   map[string]string `json:"device_metadata,omitempty"`
	AdditionalTrails map[string]string `json:"additional_trails,omitempty"`
}

type Revision struct {
	ID             string            `json:"id"`
	TargetID       string            `json:"target_id"`
	Shard          string            `json:"shard"`
	Path           string            `json:"path"`
	ContentSHA256  string            `json:"content_sha256"`
	CommitSHA      string            `json:"commit_sha"`
	ParentCommit   string            `json:"parent_commit,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	Changed        bool              `json:"changed"`
	CommitTrailers map[string]string `json:"commit_trailers,omitempty"`
}

type ShardStrategy string

const (
	ShardByRegion ShardStrategy = "region"
	ShardBySite   ShardStrategy = "site"
	ShardByVendor ShardStrategy = "vendor"
	ShardByRole   ShardStrategy = "role"
	ShardByHash   ShardStrategy = "hash"
)

type JobStatus string

const (
	StatusQueued                   JobStatus = "queued"
	StatusLeased                   JobStatus = "leased"
	StatusRunning                  JobStatus = "running"
	StatusSuccessNoChange          JobStatus = "success_no_change"
	StatusSuccessChanged           JobStatus = "success_changed"
	StatusFailedConnect            JobStatus = "failed_connect"
	StatusFailedAuth               JobStatus = "failed_auth"
	StatusFailedPrivilege          JobStatus = "failed_privilege"
	StatusFailedCommand            JobStatus = "failed_command"
	StatusFailedTimeout            JobStatus = "failed_timeout"
	StatusFailedNormalization      JobStatus = "failed_normalization"
	StatusFailedRedaction          JobStatus = "failed_redaction"
	StatusFailedStorage            JobStatus = "failed_storage"
	StatusFailedCredentialProvider JobStatus = "failed_credential_provider"
	StatusSkippedDisabled          JobStatus = "skipped_disabled"
	StatusSkippedBlackout          JobStatus = "skipped_blackout"
	StatusSkippedCircuitOpen       JobStatus = "skipped_circuit_open"
	StatusCancelled                JobStatus = "cancelled"
)

type Job struct {
	ID        string    `json:"id"`
	TargetID  string    `json:"target_id"`
	Group     string    `json:"group,omitempty"`
	Trigger   string    `json:"trigger"`
	Actor     string    `json:"actor"`
	Status    JobStatus `json:"status"`
	Attempt   int       `json:"attempt"`
	QueuedAt  time.Time `json:"queued_at"`
	StartedAt time.Time `json:"started_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type JobResult struct {
	JobID      string        `json:"job_id"`
	TargetID   string        `json:"target_id"`
	Status     JobStatus     `json:"status"`
	Attempt    int           `json:"attempt"`
	Err        error         `json:"-"`
	ErrorText  string        `json:"error_text,omitempty"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at"`
	Duration   time.Duration `json:"duration"`
	RevisionID string        `json:"revision_id,omitempty"`
}

type EventType string

const (
	EventConfigChanged            EventType = "config_changed"
	EventBackupFailed             EventType = "backup_failed"
	EventDeviceUnreachableChronic EventType = "device_unreachable_chronic"
	EventDriverError              EventType = "driver_error"
	EventCredentialProviderError  EventType = "credential_provider_error"
	EventStorageError             EventType = "storage_error"
	EventSchedulerDegraded        EventType = "scheduler_degraded"
	EventHighRiskDiffDetected     EventType = "high_risk_diff_detected"
)

type Event struct {
	Type      EventType `json:"type"`
	TargetID  string    `json:"target_id,omitempty"`
	Message   string    `json:"message"`
	Diff      string    `json:"diff,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

type AuditEvent struct {
	ID            string            `json:"id"`
	ActorType     string            `json:"actor_type"`
	ActorID       string            `json:"actor_id"`
	Action        string            `json:"action"`
	TargetID      string            `json:"target_id,omitempty"`
	CredentialRef string            `json:"credential_ref,omitempty"`
	JobID         string            `json:"job_id,omitempty"`
	RequestID     string            `json:"request_id,omitempty"`
	SourceIP      string            `json:"source_ip,omitempty"`
	Outcome       string            `json:"outcome"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
}
