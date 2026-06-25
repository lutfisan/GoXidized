ALTER TABLE api_tokens
    ADD COLUMN IF NOT EXISTS token_type text NOT NULL DEFAULT 'api_token',
    ADD COLUMN IF NOT EXISTS last_used_at timestamptz;

CREATE INDEX IF NOT EXISTS api_tokens_type_idx ON api_tokens (token_type);
CREATE INDEX IF NOT EXISTS api_tokens_actor_idx ON api_tokens (actor_id);
CREATE INDEX IF NOT EXISTS user_roles_role_idx ON user_roles (role_id);

INSERT INTO roles (id, name, permissions) VALUES
    ('admin', 'Admin', '["*"]'::jsonb),
    ('operator', 'Operator', '["devices:read","jobs:read","backups:run","inventory:reload","drivers:read","drivers:test","configs:read","configs:diff"]'::jsonb),
    ('security-auditor', 'Security Auditor', '["devices:read","jobs:read","configs:read","configs:diff","audit:read","drivers:read"]'::jsonb),
    ('config-viewer', 'Config Viewer', '["devices:read","jobs:read","configs:read","configs:diff","drivers:read"]'::jsonb),
    ('read-only', 'Read Only', '["devices:read","jobs:read","drivers:read"]'::jsonb)
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name,
    permissions = EXCLUDED.permissions;
