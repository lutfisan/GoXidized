# GoXidized

GoXidized is a Go-native network configuration backup and change-governance platform based on `GoXidized_PRD_v1.2.md`.

It is designed as a production-core backend for large network fleets: CSV/router.db inventory, SSH collection, explicitly gated Telnet, vendor drivers, normalization, redaction, risk-classified diffs, Git-backed sanitized configuration history, PostgreSQL metadata/audit storage, a REST API, and a CLI.

## Current Status

Implemented:

- `goxidized` CLI: server, inventory, backup, device, config, diff, driver, storage, admin, and version commands.
- REST API with health, readiness, metrics, devices, jobs, config history, diffs, manual backup triggers, inventory reload, drivers, and audit events.
- Public extension contracts in `pkg/goxidized`.
- CSV and router.db-style inventory loading.
- `.env` and AES-GCM encrypted-file credential providers.
- SSH transport with `strict`, `tofu`, and `insecure` host-key modes.
- Telnet transport is opt-in only and never used as SSH fallback.
- Driver keys for Cisco IOS XE, Cisco IOS XR, Huawei VRP, Juniper Junos, Ericsson IPOS, and ZTE ZXR10.
- Git storage using `go-git/v5`.
- PostgreSQL schema for devices, jobs, results, revisions, diffs, audit events, RBAC, API tokens, worker leases, circuit breakers, and notifications.
- API-token auth, OIDC browser login, DB-backed session tokens, route-level RBAC, and sensitive-action audit events.
- Embedded minimal Web UI at `/ui/` with device status grid, job/config/diff detail views, manual backup triggers, driver/inventory status, OIDC login, token fallback, and RBAC-aware controls.
- OpenAPI starter file at `docs/openapi.yaml`.

Future work:

- CyberArk and Vault live providers.
- Distributed queue / multi-worker deployment.

## Repository Layout

```text
cmd/goxidized/                         CLI entrypoint
internal/api/                          REST API
internal/app/                          application composition
internal/config/                       YAML/env config
internal/credentials/                  credential provider routing
internal/drivers/                      vendor driver registry
internal/inventory/csv/                CSV/router.db inventory source
internal/metadata/postgres/            PostgreSQL store and embedded migrations
internal/pipeline/                     normalize, redact, diff, risk classify
internal/scheduler/                    queue, caps, retry, rate limit
internal/storage/gitstore/             Git-backed sanitized config storage
internal/transport/                    SSH and Telnet transport adapters
pkg/goxidized/                         public interfaces and shared types
pkg/conformance/                       driver fixture replay harness
config.example.yaml                    example application config
.env.example                           example environment/credential file
devices.example.csv                    example device inventory
goxidized.sql                          standalone PostgreSQL bootstrap schema
```

## Requirements

- Go 1.26 or newer.
- PostgreSQL 14 or newer recommended.
- Git.
- Linux, Windows, or container runtime.
- Network reachability from the GoXidized host/container to device management addresses.

## Quick Start From Source

```bash
git clone <repo-url> GoXidized
cd GoXidized
cp config.example.yaml config.yaml
cp .env.example .env
cp devices.example.csv devices.csv
go mod download
go test ./...
go run ./cmd/goxidized version
```

Create a PostgreSQL database and load the schema:

```bash
createdb goxidized
psql -v ON_ERROR_STOP=1 -d goxidized -f goxidized.sql
```

Edit `.env` and set `GOXIDIZED_POSTGRES_DSN`, `GOXIDIZED_BOOTSTRAP_TOKEN`, and device credentials. Then start the server:

```bash
go run ./cmd/goxidized --config config.yaml server start
```

On Windows PowerShell, use:

```powershell
Copy-Item config.example.yaml config.yaml
Copy-Item .env.example .env
Copy-Item devices.example.csv devices.csv
go test ./...
go run ./cmd/goxidized --config config.yaml server start
```

## Database Setup

The application runs embedded migrations automatically at startup. Operators who prefer explicit database bootstrap can use `goxidized.sql`.

Create a role and database:

```bash
sudo -u postgres createuser --pwprompt goxidized
sudo -u postgres createdb --owner=goxidized goxidized
psql -U goxidized -d goxidized -f goxidized.sql
```

Set the DSN:

```env
GOXIDIZED_POSTGRES_DSN=postgres://goxidized:password@127.0.0.1:5432/goxidized?sslmode=disable
```

For production, use your site’s TLS, password, backup, and role policy. Back up PostgreSQL before deploying schema-changing updates.

## Configuration

Start from the examples:

```bash
cp config.example.yaml config.yaml
cp .env.example .env
cp devices.example.csv devices.csv
```

Important `config.yaml` fields:

- `server.listen_address`: API bind address.
- `server.tls_enabled`: enable TLS only when `tls_cert_file` and `tls_key_file` are set.
- `server.auth.api_tokens_enabled`: keep enabled outside local experiments.
- `server.auth.oidc.enabled`: enable OIDC browser login and DB-backed session tokens.
- `server.auth.oidc.issuer_url`, `client_id`, `client_secret_env`, and `redirect_url`: provider settings for the authorization-code flow.
- `server.auth.oidc.scopes`: must include `openid`; `profile` and `email` are recommended.
- `server.auth.oidc.session_ttl`: short-lived session duration for OIDC logins.
- `inventory.sources[].path`: inventory CSV path.
- `credentials.default_provider`: `dotenv` or `encrypted-file`.
- `credentials.dotenv.file_path`: path to the `.env` credential file. Set to empty in Docker if credentials come from environment variables.
- `storage.metadata.dsn_env`: environment variable containing the PostgreSQL DSN.
- `storage.config.base_path`: directory for Git shard repositories.
- `storage.config.shard_strategy`: `region`, `site`, `vendor`, `role`, or `hash`.
- `transport.ssh.host_key_mode`: `strict` for production, `tofu` for controlled onboarding, `insecure` only for labs.
- `transport.telnet.enabled`: global Telnet opt-in. Per-device opt-in is also required.

Important `.env` fields:

```env
GOXIDIZED_POSTGRES_DSN=postgres://goxidized:goxidized@localhost:5432/goxidized?sslmode=disable
GOXIDIZED_BOOTSTRAP_TOKEN=replace-with-a-long-random-token
GOXIDIZED_REDACTION_HMAC_KEY=replace-with-a-long-random-secret
GOXIDIZED_OIDC_CLIENT_SECRET=replace-with-oidc-client-secret

LAB_XE_1_USERNAME=backup
LAB_XE_1_PASSWORD=change-me
LAB_XE_1_ENABLE_SECRET=change-me-enable
```

Inventory example:

```csv
id,hostname,ip_address,port,vendor,group,site,role,tags,jump_host,credential_ref,enabled,telnet_enabled
lab-xe-1,lab-xe-1,192.0.2.10,22,cisco_iosxe,lab,dc1,core,lab|iosxe,,dotenv://LAB_XE_1,true,false
```

Supported vendor keys:

- `cisco_iosxe`
- `cisco_iosxr`
- `huawei_vrp`
- `juniper_junos`
- `ericsson_ipos`
- `zte_zxr10`

## Linux Install With systemd

The default Linux layout is under `/opt/goxidized`:

```text
/opt/goxidized/bin/goxidized
/opt/goxidized/config/config.yaml
/opt/goxidized/config/devices.csv
/opt/goxidized/config/goxidized.env
/opt/goxidized/data/repos/
/opt/goxidized/src/
```

Install dependencies:

```bash
sudo apt-get update
sudo apt-get install -y git postgresql-client ca-certificates
```

Install Go 1.26+ from your OS package manager or the official Go distribution, then build:

```bash
sudo install -d /opt/goxidized/src
sudo chown "$USER":"$USER" /opt/goxidized/src
git clone <repo-url> /opt/goxidized/src
cd /opt/goxidized/src
go mod download
go test ./...
go build -o goxidized ./cmd/goxidized
```

Create the runtime user and directories:

```bash
sudo useradd --system --home /opt/goxidized --shell /usr/sbin/nologin goxidized
sudo install -d -o goxidized -g goxidized /opt/goxidized/bin /opt/goxidized/config /opt/goxidized/data /opt/goxidized/data/repos
sudo install -m 0755 goxidized /opt/goxidized/bin/goxidized
sudo install -m 0644 config.example.yaml /opt/goxidized/config/config.yaml
sudo install -m 0644 devices.example.csv /opt/goxidized/config/devices.csv
sudo install -m 0600 .env.example /opt/goxidized/config/goxidized.env
sudo chown goxidized:goxidized /opt/goxidized/config/goxidized.env /opt/goxidized/config/devices.csv
```

Edit `/opt/goxidized/config/config.yaml`:

```yaml
inventory:
  sources:
    - name: primary-csv
      type: csv
      path: /opt/goxidized/config/devices.csv

credentials:
  dotenv:
    file_path: /opt/goxidized/config/goxidized.env

storage:
  config:
    base_path: /opt/goxidized/data/repos

transport:
  ssh:
    known_hosts_path: /opt/goxidized/config/known_hosts
    tofu_path: /opt/goxidized/config/known_hosts.tofu
```

Edit `/opt/goxidized/config/goxidized.env`:

```env
GOXIDIZED_POSTGRES_DSN=postgres://goxidized:goxidized@127.0.0.1:5432/goxidized?sslmode=disable
GOXIDIZED_BOOTSTRAP_TOKEN=replace-with-a-long-random-token
GOXIDIZED_REDACTION_HMAC_KEY=replace-with-a-long-random-secret
```

Create `/etc/systemd/system/goxidized.service`:

```ini
[Unit]
Description=GoXidized network configuration backup service
Wants=network-online.target
After=network-online.target postgresql.service

[Service]
Type=simple
User=goxidized
Group=goxidized
WorkingDirectory=/opt/goxidized
EnvironmentFile=/opt/goxidized/config/goxidized.env
ExecStart=/opt/goxidized/bin/goxidized --config /opt/goxidized/config/config.yaml server start
Restart=on-failure
RestartSec=10s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=/opt/goxidized

[Install]
WantedBy=multi-user.target
```

Enable, start, and inspect logs:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now goxidized
sudo systemctl status goxidized
journalctl -u goxidized -f
```

Validate the API:

```bash
TOKEN="$(sudo sed -n 's/^GOXIDIZED_BOOTSTRAP_TOKEN=//p' /opt/goxidized/config/goxidized.env)"
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/healthz
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/api/v1/devices
```

Update a systemd install:

```bash
cd /opt/goxidized/src
git pull --ff-only
go test ./...
go build -o goxidized ./cmd/goxidized
sudo systemctl stop goxidized
sudo install -m 0755 goxidized /opt/goxidized/bin/goxidized
sudo systemctl start goxidized
```

## Docker / Compose

Use Docker when you want a self-contained runtime. Persist configuration, PostgreSQL data, and Git shard repositories.

Create `Dockerfile`:

```dockerfile
FROM golang:1.26-alpine AS builder
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/goxidized ./cmd/goxidized

FROM alpine:3.22
RUN apk add --no-cache ca-certificates git && adduser -S -D -H -h /opt/goxidized goxidized
WORKDIR /opt/goxidized
COPY --from=builder /out/goxidized /opt/goxidized/bin/goxidized
USER goxidized
EXPOSE 8080
ENTRYPOINT ["/opt/goxidized/bin/goxidized"]
CMD ["--config", "/opt/goxidized/config/config.yaml", "server", "start"]
```

Create `compose.yaml`:

```yaml
services:
  postgres:
    image: postgres:17-alpine
    environment:
      POSTGRES_DB: goxidized
      POSTGRES_USER: goxidized
      POSTGRES_PASSWORD: goxidized
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U goxidized -d goxidized"]
      interval: 10s
      timeout: 5s
      retries: 5

  goxidized:
    build:
      context: .
    depends_on:
      postgres:
        condition: service_healthy
    ports:
      - "8080:8080"
    env_file:
      - .env
    volumes:
      - ./config.yaml:/opt/goxidized/config/config.yaml:ro
      - ./devices.csv:/opt/goxidized/config/devices.csv:ro
      - goxidized-data:/opt/goxidized/data
    restart: unless-stopped

volumes:
  pgdata:
  goxidized-data:
```

Use container-friendly config values:

```yaml
server:
  listen_address: 0.0.0.0:8080

inventory:
  sources:
    - name: primary-csv
      type: csv
      path: /opt/goxidized/config/devices.csv

credentials:
  dotenv:
    file_path: ""

storage:
  metadata:
    dsn_env: GOXIDIZED_POSTGRES_DSN
  config:
    base_path: /opt/goxidized/data/repos
```

Set `.env` for Compose:

```env
GOXIDIZED_POSTGRES_DSN=postgres://goxidized:goxidized@postgres:5432/goxidized?sslmode=disable
GOXIDIZED_BOOTSTRAP_TOKEN=replace-with-a-long-random-token
GOXIDIZED_REDACTION_HMAC_KEY=replace-with-a-long-random-secret
LAB_XE_1_USERNAME=backup
LAB_XE_1_PASSWORD=change-me
```

Start and inspect:

```bash
cp config.example.yaml config.yaml
cp devices.example.csv devices.csv
cp .env.example .env
docker compose up --build -d
docker compose logs -f goxidized
```

Validate:

```bash
TOKEN="$(grep GOXIDIZED_BOOTSTRAP_TOKEN .env | cut -d= -f2-)"
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/healthz
```

Update a Docker install:

```bash
git pull --ff-only
docker compose build --pull goxidized
docker compose up -d
docker compose logs -f goxidized
```

## CLI Operations

Validate inventory:

```bash
goxidized --config config.yaml inventory validate --file devices.csv
```

List registered drivers:

```bash
goxidized driver list
```

Run a fixture through a driver:

```bash
goxidized driver test --vendor cisco_iosxe --fixture testdata/fixtures/cisco_iosxe/running.cfg
```

Verify Git storage:

```bash
goxidized --config config.yaml storage verify
```

Run an immediate backup:

```bash
goxidized --config config.yaml backup run --device lab-xe-1
goxidized --config config.yaml backup run --group lab
```

Show latest sanitized config:

```bash
goxidized --config config.yaml config show lab-xe-1 --latest
```

## API Usage

Health and readiness:

```bash
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:8080/readyz
```

Authenticated API request:

```bash
curl -H "Authorization: Bearer $GOXIDIZED_BOOTSTRAP_TOKEN" http://127.0.0.1:8080/api/v1/devices
```

Manual backup trigger:

```bash
curl -X POST -H "Authorization: Bearer $GOXIDIZED_BOOTSTRAP_TOKEN" http://127.0.0.1:8080/api/v1/devices/lab-xe-1/backup
```

## Web UI

The embedded Web UI is served by the GoXidized API process:

```bash
open http://127.0.0.1:8080/ui/
```

It uses the same RBAC-gated API routes as CLI and API clients. OIDC sessions work through the HttpOnly session cookie, and local/bootstrap access can use the token fallback form. Static UI files are public, but device data, config content, diffs, backup triggers, driver tests, inventory reload, and audit reads remain protected by API permissions.

The approved launch UI concept is kept at `docs/ui/goxidized-web-ui-concept.png`; the implementation uses the generated `network-backup-empty.png` asset only for login and empty states.

## API Tokens

For production-style API access, create a token and store only its SHA-256 hash:

```bash
goxidized admin create-token
```

Insert the returned `token_sha256` into `api_tokens.token_hash` with an `actor_id` and `role_id`. Keep the plaintext token only in your secret manager.

`GOXIDIZED_BOOTSTRAP_TOKEN` is intended for first startup and recovery. Long-lived access should use rows in `api_tokens`.

Default role permissions:

- `admin`: `*`
- `operator`: device/job reads, manual backups, inventory reload, driver read/test, config read/diff
- `security-auditor`: device/job/config/audit reads and driver reads
- `config-viewer`: device/job/config reads and diffs plus driver reads
- `read-only`: device/job/driver reads

## OIDC Login And RBAC

OIDC is optional and fail-closed. Users must be preprovisioned in PostgreSQL; claim-to-role mapping is intentionally not automatic.

Enable OIDC in `config.yaml`:

```yaml
server:
  auth:
    oidc:
      enabled: true
      issuer_url: https://issuer.example
      client_id: goxidized
      client_secret_env: GOXIDIZED_OIDC_CLIENT_SECRET
      redirect_url: https://goxidized.example.com/auth/oidc/callback
      scopes: [openid, profile, email]
      session_ttl: 8h
      cookie_name: goxidized_session
      require_email_verified: true
```

Preprovision an OIDC user and assign a DB role:

```sql
INSERT INTO users (id, subject, display_name)
VALUES ('user-alice', 'oidc:https://issuer.example#provider-subject', 'Alice')
ON CONFLICT (id) DO UPDATE SET
    subject = EXCLUDED.subject,
    display_name = EXCLUDED.display_name;

INSERT INTO user_roles (user_id, role_id)
VALUES ('user-alice', 'operator')
ON CONFLICT DO NOTHING;
```

Login starts at `GET /auth/oidc/login`. The callback verifies state and nonce, exchanges the code, verifies the ID token, requires `email_verified` when configured, resolves `users.subject`, creates a short-lived `api_tokens` row with `token_type='oidc_session'`, sets an HttpOnly SameSite cookie, and redirects the browser back to `/ui/`.

Check the current principal:

```bash
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/api/v1/auth/me
```

Logout revokes the current token:

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/auth/logout
```

## Security Notes

- Do not commit `.env`, real device output, private keys, or production inventory.
- Redaction is enabled by default. Keep strict mode enabled in production.
- Use `transport.ssh.host_key_mode: strict` in production.
- Use `tofu` only for controlled onboarding and review the generated known-hosts file.
- Use `insecure` only in labs.
- Telnet requires global and per-device opt-in and is never an automatic SSH fallback.
- Only normalized and redacted configs are stored by default.

## Backup And Update Checklist

Before updating a running deployment:

- Back up PostgreSQL.
- Back up `/opt/goxidized/config`.
- Back up `/opt/goxidized/data/repos` or your configured Git storage root.
- Review migration and release notes.
- Run `go test ./...`.
- Test in a lab or staging environment when changing drivers, redaction rules, scheduler limits, or storage layout.

Generic source update:

```bash
git pull --ff-only
go mod download
go test ./...
go build -o goxidized ./cmd/goxidized
```

## Troubleshooting

Server will not start:

- Check `GOXIDIZED_POSTGRES_DSN`.
- Confirm PostgreSQL is reachable.
- Run `psql -d "$GOXIDIZED_POSTGRES_DSN" -c 'select 1'` if your shell supports DSN connection strings.
- Check logs with `journalctl -u goxidized -f` for systemd or `docker compose logs -f goxidized` for Docker.

API returns unauthorized:

- Confirm `Authorization: Bearer <token>` is present.
- Confirm `GOXIDIZED_BOOTSTRAP_TOKEN` is set, or insert a SHA-256 token hash into `api_tokens`.
- For OIDC, confirm the user exists with subject `oidc:<issuer_url>#<sub>` and has at least one row in `user_roles`.

Inventory validation fails:

- Confirm required fields: `id`, `hostname`, `ip_address`, `vendor`, `group`, `credential_ref`, `enabled`.
- Confirm `port` is between `1` and `65535`.
- Confirm `vendor` is one of the supported driver keys.

Backups fail at authentication:

- Confirm `credential_ref` matches the `.env` prefix.
- Example: `dotenv://LAB_XE_1` uses `LAB_XE_1_USERNAME`, `LAB_XE_1_PASSWORD`, and optionally `LAB_XE_1_ENABLE_SECRET`.

Git storage fails:

- Confirm `storage.config.base_path` exists and is writable by the service user.
- Confirm the container or host has Git-compatible filesystem permissions.

## Contribute

Pull requests are welcome.

Recommended workflow:

```bash
git checkout -b feature/my-change
go test ./...
go vet ./...
gofmt -w cmd internal pkg
```

Contribution expectations:

- Keep secrets out of commits. Use fake values in examples and fixtures.
- Add tests for behavior changes.
- Add or update driver fixtures for driver changes.
- Update `README.md`, `docs/openapi.yaml`, `config.example.yaml`, `.env.example`, `devices.example.csv`, or `goxidized.sql` when public behavior changes.
- Keep vendor drivers behind the `Driver` interface; do not special-case scheduler or storage for a vendor.
- Keep raw device output in memory only unless a future encrypted raw-storage feature is explicitly implemented.

Driver PRs should include:

- Vendor key.
- Commands used to disable paging and fetch config.
- Raw fixture with fake/sanitized data.
- Expected normalized/redacted output behavior.
- Error, timeout, or unusual prompt cases when known.

Security-sensitive PRs should include:

- Why the change is safe.
- What secrets could be exposed if it fails.
- Tests proving logs, Git, API responses, and metadata do not expose seeded secrets.

Open a pull request with a short summary, test output, and operational notes.
