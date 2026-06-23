package goxidized

import "context"

type Session interface {
	Run(ctx context.Context, command string) ([]byte, error)
	Close() error
}

type Driver interface {
	Vendor() string
	Prepare(ctx context.Context, sess Session) error
	FetchConfig(ctx context.Context, t Target, sess Session) (*ConfigResult, error)
	Normalize(ctx context.Context, r *ConfigResult) (*ConfigResult, error)
	Redact(ctx context.Context, r *ConfigResult) (*RedactedConfig, RedactionReport, error)
}

type InventorySource interface {
	Load(ctx context.Context) ([]Target, error)
	Watch(ctx context.Context) (<-chan []Target, error)
}

type CredentialProvider interface {
	Resolve(ctx context.Context, ref string) (Credentials, error)
}

type Storage interface {
	Save(ctx context.Context, t Target, cfg RedactedConfig, meta CommitMeta) (Revision, error)
	Latest(ctx context.Context, targetID string) (RedactedConfig, Revision, error)
	History(ctx context.Context, targetID string, limit int) ([]Revision, error)
	Diff(ctx context.Context, targetID, fromRev, toRev string) (string, error)
}

type MetadataStore interface {
	UpsertDevices(ctx context.Context, devices []Target) error
	ListDevices(ctx context.Context) ([]Target, error)
	GetDevice(ctx context.Context, id string) (Target, error)
	RecordJobStart(ctx context.Context, job Job) (Job, error)
	RecordJobFinish(ctx context.Context, result JobResult) error
	SaveRevision(ctx context.Context, rev Revision, meta CommitMeta) error
	SaveDiff(ctx context.Context, diff DiffResult) error
	ListJobs(ctx context.Context, limit int) ([]Job, error)
	GetJob(ctx context.Context, id string) (Job, error)
	ListRevisions(ctx context.Context, targetID string, limit int) ([]Revision, error)
	LatestRevision(ctx context.Context, targetID string) (Revision, error)
	Audit(ctx context.Context, ev AuditEvent) error
	ListAuditEvents(ctx context.Context, limit int) ([]AuditEvent, error)
}

type Notifier interface {
	Notify(ctx context.Context, ev Event) error
}
