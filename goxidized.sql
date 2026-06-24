-- GoXidized PostgreSQL bootstrap schema.
--
-- Usage:
--   createdb goxidized
--   psql -v ON_ERROR_STOP=1 -d goxidized -f goxidized.sql
--
-- This file mirrors the embedded application migration and can be used by
-- operators who prefer to initialize the database outside the GoXidized
-- startup path. It does not create the database or database role because those
-- usually require site-specific ownership, password, TLS, and privilege policy.

BEGIN;

CREATE TABLE IF NOT EXISTS schema_migrations (
    version text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS devices (
    id text PRIMARY KEY,
    hostname text NOT NULL,
    ip_address text NOT NULL,
    port integer NOT NULL DEFAULT 22,
    vendor text NOT NULL,
    device_group text NOT NULL,
    site text NOT NULL DEFAULT '',
    role text NOT NULL DEFAULT '',
    tags jsonb NOT NULL DEFAULT '[]'::jsonb,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    jump_host text NOT NULL DEFAULT '',
    credential_ref text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    telnet_enabled boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS devices_vendor_idx ON devices (vendor);
CREATE INDEX IF NOT EXISTS devices_group_idx ON devices (device_group);
CREATE INDEX IF NOT EXISTS devices_site_idx ON devices (site);
CREATE INDEX IF NOT EXISTS devices_role_idx ON devices (role);
CREATE INDEX IF NOT EXISTS devices_enabled_idx ON devices (enabled);

CREATE TABLE IF NOT EXISTS inventory_sources (
    name text PRIMARY KEY,
    source_type text NOT NULL,
    path text NOT NULL,
    last_loaded_at timestamptz,
    last_error text
);

CREATE TABLE IF NOT EXISTS credential_refs (
    id text PRIMARY KEY,
    provider text NOT NULL,
    ref_hash text NOT NULL,
    display_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    auth_type text NOT NULL DEFAULT '',
    last_outcome text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS backup_jobs (
    id text NOT NULL,
    target_id text NOT NULL,
    device_group text NOT NULL DEFAULT '',
    trigger text NOT NULL,
    actor text NOT NULL DEFAULT '',
    status text NOT NULL,
    attempt integer NOT NULL DEFAULT 1,
    queued_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now()
) PARTITION BY RANGE (created_at);

CREATE TABLE IF NOT EXISTS backup_jobs_default PARTITION OF backup_jobs DEFAULT;
CREATE INDEX IF NOT EXISTS backup_jobs_id_idx ON backup_jobs (id);
CREATE INDEX IF NOT EXISTS backup_jobs_target_idx ON backup_jobs (target_id, created_at DESC);
CREATE INDEX IF NOT EXISTS backup_jobs_status_idx ON backup_jobs (status, created_at DESC);

CREATE TABLE IF NOT EXISTS backup_results (
    job_id text NOT NULL,
    target_id text NOT NULL,
    status text NOT NULL,
    attempt integer NOT NULL,
    error_text text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL,
    finished_at timestamptz NOT NULL,
    duration_ms bigint NOT NULL,
    revision_id text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
) PARTITION BY RANGE (created_at);

CREATE TABLE IF NOT EXISTS backup_results_default PARTITION OF backup_results DEFAULT;
CREATE INDEX IF NOT EXISTS backup_results_job_idx ON backup_results (job_id);
CREATE INDEX IF NOT EXISTS backup_results_target_idx ON backup_results (target_id, created_at DESC);
CREATE INDEX IF NOT EXISTS backup_results_status_idx ON backup_results (status, created_at DESC);

CREATE TABLE IF NOT EXISTS config_versions (
    id text PRIMARY KEY,
    target_id text NOT NULL,
    shard text NOT NULL,
    path text NOT NULL,
    content_sha256 text NOT NULL,
    commit_sha text NOT NULL,
    parent_commit text NOT NULL DEFAULT '',
    changed boolean NOT NULL,
    commit_trailers jsonb NOT NULL DEFAULT '{}'::jsonb,
    commit_meta jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS config_versions_target_idx ON config_versions (target_id, created_at DESC);
CREATE INDEX IF NOT EXISTS config_versions_commit_idx ON config_versions (commit_sha);
CREATE INDEX IF NOT EXISTS config_versions_content_idx ON config_versions (content_sha256);
CREATE INDEX IF NOT EXISTS config_versions_target_commit_idx ON config_versions (target_id, commit_sha);

CREATE TABLE IF NOT EXISTS config_diffs (
    id bigserial NOT NULL,
    target_id text NOT NULL,
    from_revision text NOT NULL DEFAULT '',
    to_revision text NOT NULL,
    added_lines integer NOT NULL DEFAULT 0,
    removed_lines integer NOT NULL DEFAULT 0,
    risk_level text NOT NULL,
    categories jsonb NOT NULL DEFAULT '[]'::jsonb,
    rule_hits jsonb NOT NULL DEFAULT '[]'::jsonb,
    diff_preview text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
) PARTITION BY RANGE (created_at);

CREATE TABLE IF NOT EXISTS config_diffs_default PARTITION OF config_diffs DEFAULT;
CREATE INDEX IF NOT EXISTS config_diffs_target_idx ON config_diffs (target_id, created_at DESC);
CREATE INDEX IF NOT EXISTS config_diffs_risk_idx ON config_diffs (risk_level, created_at DESC);

CREATE TABLE IF NOT EXISTS audit_events (
    id text NOT NULL,
    actor_type text NOT NULL,
    actor_id text NOT NULL,
    action text NOT NULL,
    target_id text NOT NULL DEFAULT '',
    credential_ref text NOT NULL DEFAULT '',
    job_id text NOT NULL DEFAULT '',
    request_id text NOT NULL DEFAULT '',
    source_ip text NOT NULL DEFAULT '',
    outcome text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
) PARTITION BY RANGE (created_at);

CREATE TABLE IF NOT EXISTS audit_events_default PARTITION OF audit_events DEFAULT;
CREATE INDEX IF NOT EXISTS audit_events_action_idx ON audit_events (action, created_at DESC);
CREATE INDEX IF NOT EXISTS audit_events_actor_idx ON audit_events (actor_type, actor_id, created_at DESC);
CREATE INDEX IF NOT EXISTS audit_events_target_idx ON audit_events (target_id, created_at DESC);

CREATE TABLE IF NOT EXISTS worker_leases (
    target_id text PRIMARY KEY,
    worker_id text NOT NULL,
    job_id text NOT NULL,
    expires_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS worker_leases_expires_idx ON worker_leases (expires_at);

CREATE TABLE IF NOT EXISTS circuit_breakers (
    target_id text PRIMARY KEY,
    state text NOT NULL,
    failures integer NOT NULL DEFAULT 0,
    opened_at timestamptz,
    next_attempt_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS driver_versions (
    vendor text PRIMARY KEY,
    version text NOT NULL,
    changelog text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS notification_events (
    id text NOT NULL,
    event_type text NOT NULL,
    target_id text NOT NULL DEFAULT '',
    notifier text NOT NULL,
    outcome text NOT NULL,
    message text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
) PARTITION BY RANGE (created_at);

CREATE TABLE IF NOT EXISTS notification_events_default PARTITION OF notification_events DEFAULT;
CREATE INDEX IF NOT EXISTS notification_events_type_idx ON notification_events (event_type, created_at DESC);

CREATE TABLE IF NOT EXISTS users (
    id text PRIMARY KEY,
    subject text NOT NULL UNIQUE,
    display_name text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS roles (
    id text PRIMARY KEY,
    name text NOT NULL UNIQUE,
    permissions jsonb NOT NULL DEFAULT '[]'::jsonb
);

CREATE TABLE IF NOT EXISTS user_roles (
    user_id text NOT NULL,
    role_id text NOT NULL,
    PRIMARY KEY (user_id, role_id)
);

CREATE TABLE IF NOT EXISTS api_tokens (
    id text PRIMARY KEY,
    token_hash text NOT NULL UNIQUE,
    actor_id text NOT NULL,
    role_id text NOT NULL,
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);

INSERT INTO roles (id, name, permissions) VALUES
    ('admin', 'Admin', '["*"]'::jsonb),
    ('operator', 'Operator', '["devices:read","jobs:read","backups:run","inventory:reload"]'::jsonb),
    ('security-auditor', 'Security Auditor', '["devices:read","jobs:read","configs:diff","audit:read"]'::jsonb),
    ('config-viewer', 'Config Viewer', '["devices:read","configs:read","configs:diff"]'::jsonb),
    ('read-only', 'Read Only', '["devices:read","jobs:read"]'::jsonb)
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name,
    permissions = EXCLUDED.permissions;

INSERT INTO schema_migrations (version)
VALUES ('0001_init.sql'), ('0002_worker_leases.sql')
ON CONFLICT (version) DO NOTHING;

COMMIT;
