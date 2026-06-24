CREATE TABLE IF NOT EXISTS worker_leases (
    target_id text PRIMARY KEY,
    worker_id text NOT NULL,
    job_id text NOT NULL,
    expires_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS worker_leases_expires_idx ON worker_leases (expires_at);
