# ADR 0004: Production-Core Threat Model

Status: Accepted

Date: 2026-06-24

## Context

GoXidized connects to privileged network infrastructure, retrieves sensitive configuration, stores change history, and exposes APIs. The PRD requires a concrete threat model before production v1.

## Threat Model

| Threat | Impact | Control |
| --- | --- | --- |
| Device credentials leak through logs or errors | Network compromise | Secret wrapper types, redacted logs, provider refs only, secret-leak tests |
| Embedded config secrets reach Git | Credential disclosure through repository replication | Strict redaction before storage, raw storage disabled, redaction reports |
| SSH host impersonation | Config exfiltration and credential theft | Strict known_hosts by default, TOFU available, insecure mode warning |
| Silent SSH-to-Telnet downgrade | Credential exposure in cleartext | Telnet requires global and target opt-in; SSH failure is never retried as Telnet |
| Command/path injection | Host compromise or data overwrite | No shell in default Git path, sanitized storage paths, fixed-argument fallback |
| Duplicate active backups | AAA overload and inconsistent metadata | Active target dedupe, concurrency caps, DB-backed worker leases |
| API token abuse | Unauthorized config access or backup runs | SHA-256 token hashes, RBAC roles, audit events, token revocation |
| Postgres/Git split brain | Missing history or incorrect API state | Commit trailers, reconciliation job, idempotent metadata writes |
| Malicious fixture or attachment | Code execution or secret injection in tests | Fixtures are data only; no fixture-driven code execution |
| Oversized output | Memory pressure and worker starvation | Per-command deadlines and max output byte limits |

## Security Defaults

- SSH is the default transport.
- Telnet defaults to disabled.
- Host key mode defaults to strict.
- Raw transcript persistence defaults to disabled.
- API-token auth defaults to enabled.
- OIDC, Vault, and CyberArk remain integration milestones requiring deployment-specific details.

