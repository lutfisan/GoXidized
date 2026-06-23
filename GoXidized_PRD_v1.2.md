# Product Requirements Document

## GoXidized — Go-Native Network Configuration Backup & Change Governance Platform
### Go rewrite of Oxidized, engineered for 26,000+ multi-vendor network devices

| Field | Value |
|---|---|
| Document Title | GoXidized PRD |
| Version | 1.2.0 (merged) |
| Status | Draft for Architecture Review |
| Prepared For | Muhammad Lutfi Santoso |
| Author | Drafted by Claude (acting as Senior Software Architect) |
| Date | 2026-06-23 |
| Target Language | Go 1.26+ |
| Target Fleet Size | 26,000+ network devices |
| Primary Deployment Target | Linux amd64/arm64, systemd and Kubernetes |
| Reference Project | [ytti/oxidized](https://github.com/ytti/oxidized) (Ruby, Apache-2.0) |

---

## 0. Revision & Merge Notes

This v1.2 merges two prior drafts: my own v1.0 (and its in-chat revisions for Telnet/credentials/Git sharding/open questions) and a v1.1 "improved" draft you supplied. Rather than picking one wholesale, this version keeps what's independently sound from each and is explicit about where the two disagreed or where a claim needs your confirmation.

**Adopted from the v1.1 draft, on its engineering merits (not contingent on facts about your environment):**
PostgreSQL metadata index and job-state machine; risk-classified diffs (§9.9); RBAC roles; `SecretString`/`SecretBytes` wrapper types; the `go-git` vs. shell-`git` ADR (instead of asserting `go-git` as settled); the sanitized-only-vs-encrypted-raw-storage decision; a dual-scenario capacity model plus a concrete p95-measurement methodology; a threat-model table; MVP-vs-production acceptance criteria; Definition of Done.

**Corrected based on the v1.1 draft's own valid critique of my v1.0:**
§17 (Technology Stack) no longer treats specific Go 1.26 release-note features as architecturally load-bearing — they're noted as useful, to be verified at implementation time, not depended upon.

**Carried forward as flagged recommendations, *not* as confirmed decisions** (the v1.1 draft presented these as already settled; you haven't told me any of this directly in our conversation, so I'm not treating it as fact):
CyberArk PAM Self-Hosted version/component specifics, "all devices in one management zone," Huawei VRP/ZTE ZXR10 as the top-priority platforms, a mandatory (not fast-follow) web UI for v1, ELK specifically as the future SIEM target. Each is marked inline where it appears and re-listed in §25.2.

**Kept from my v1.0 because it was already correct and the v1.1 draft didn't materially improve it:**
The researched vendor CLI table (§10) — including the correction that Ericsson IPOS uses `show configuration`, not Cisco-style `show running-config` — and the Oxidized background/IP-hygiene framing (§2).

**Resolved in our conversation directly (not from either draft — these are things you actually told me):** text/CSV inventory default with future CMDB/NetBox/IPAM/LibreNMS plugins; single-node v1; drift remediation out of scope; 1-year retention configurable via `.env`; Telnet explicitly opt-in and disabled by default; `.env`/encrypted-file/Vault/CyberArk as the four credential providers; configurable Git sharding by region/site/vendor/role/hash.

---

## 1. Executive Summary

GoXidized is a ground-up Go reimplementation of [Oxidized](https://github.com/ytti/oxidized), the Ruby-based "RANCID replacement" for network configuration backup. The objective is not to port Oxidized line-for-line — it's to preserve the proven product concepts (inventory-driven polling, vendor models, versioned storage, diffs, hooks, API access) while redesigning the runtime for enterprise/telecom scale, and extending it from a pure backup tool into a **configuration backup and change-governance platform**: one that doesn't just store what changed, but flags *how risky* that change was.

GoXidized must reliably back up **26,000+ devices** across six vendor operating systems — Cisco IOS XE, Cisco IOS XR, Huawei VRP, Juniper Junos, Ericsson IPOS, ZTE ZXR10 — within a predictable, boundable backup window, with graceful degradation when individual devices or vendor stacks misbehave.

The platform provides: bounded-concurrency collection at scale; secure, pluggable credential handling; a strongly-typed vendor driver framework; normalized and redacted configuration output; Git-backed version history with configurable sharding; a queryable PostgreSQL metadata/audit index; risk-classified diffs; REST API, CLI, and a status/diff web UI; notifications; full observability; and a safe migration path from an existing Oxidized deployment.

At this scale, the system is production infrastructure, not a cron job. It must protect AAA/TACACS+ infrastructure from synchronized login storms, isolate a single misbehaving device or driver from stalling the fleet, avoid noisy false diffs, prevent credential and in-config-secret leakage, and produce audit-grade history that a security/compliance review can actually trust.

> **IP note:** Oxidized is Apache-2.0 licensed, which permits using it as a *functional reference* (sources, outputs, model concepts, REST surface) for a clean-room reimplementation. GoXidized must not port Ruby source line-by-line; all Go code is original. Track non-trivial architectural choices as ADRs rather than silent decisions.

---

## 2. Background

### 2.1 What Oxidized Provides
Oxidized polls a configurable list of network devices on a schedule, logs in over SSH/Telnet, retrieves the running configuration via vendor-specific "models," diffs it against the last known version, and commits changes to a backend — typically Git. It supports CSV/SQLite/MySQL/HTTP inventory sources, File/Git/Git-Crypt/HTTP outputs, a REST API (fetch-now, reload, list nodes, diff versions), and hooks (exec, Slack, webhook-style integrations).

### 2.2 Why Rewrite in Go
- **Concurrency model.** Oxidized's Ruby/MRI runtime has a global interpreter lock; I/O-bound SSH waits are fine, but the practical thread ceiling, scheduler fairness, and per-thread memory overhead become real constraints well before 26,000 concurrent or near-concurrent sessions. Go's goroutines are KB-scale and designed for exactly this I/O-bound fan-out.
- **Operational footprint.** A single Ruby process with thousands of threads is hard to reason about under memory pressure and hard to profile. Go ships a single static binary with first-class profiling (`pprof`) built in.
- **Driver extensibility.** Ruby models rely on duck-typing; a malformed driver fails at runtime, often mid-sweep. A compiler-enforced Go interface catches this at build time.
- **Secrets handling.** Static YAML/CSV credentials are the Oxidized norm; integrating enterprise PAM (CyberArk, Vault) means bolting on custom Ruby code rather than a first-class provider abstraction.
- **Deployment.** Ruby + native gem dependencies (libssh2, sqlite3) is a heavier OS footprint than a single static Go binary.

### 2.3 IP and Licensing Note
Oxidized is Apache-2.0 licensed. GoXidized may use its documented behavior, outputs, and public REST surface as a compatibility reference, but must not mechanically port Ruby source code. Engineering guidance: write original Go implementation; preserve third-party license notices where required; track architectural decisions in ADRs; avoid copying Ruby internals into Go.

---

## 3. Problem Statement

The organization needs reliable, secure, scalable, and auditable configuration backups for 26,000+ heterogeneous-vendor devices. Current operational risks:

1. Configuration drift without timely visibility.
2. Incomplete or inconsistent backup coverage.
3. Manual troubleshooting to determine what changed and when.
4. Noisy diffs caused by timestamps, banners, counters, or generated lines.
5. Secrets accidentally persisted in plaintext configuration history.
6. High risk of overwhelming TACACS+/RADIUS/AAA infrastructure with synchronized polling.
7. Lack of vendor-driver conformance testing, so a bad driver release can silently corrupt history fleet-wide.
8. Difficulty proving audit-trail integrity to a security/compliance reviewer.
9. Migration risk from an existing Oxidized deployment (continuity of history, parity validation).
10. Limited operational introspection into job-level failures at fleet scale — "it's backing up" isn't an answer when 400 devices silently failed last night.

---

## 4. Product Goals

| ID | Goal |
|---|---|
| G1 | Back up 26,000+ devices within a predictable, configurable backup window (see §11 for how this depends on *measured*, not assumed, session time). |
| G2 | Support Cisco IOS XE, Cisco IOS XR, Huawei VRP, Juniper Junos, Ericsson IPOS, and ZTE ZXR10 in production v1. |
| G3 | Oxidized parity-plus: inventory, drivers, Git history, diffs, hooks, API, web status — plus risk-classified change governance (G10). |
| G4 | Single Go 1.26+ static binary and container image; no cgo dependency in the default build. |
| G5 | Strongly-typed, pluggable `Driver`, `InventorySource`, `CredentialProvider`, `Storage`, and `Notifier` interfaces, each with a conformance test harness. |
| G6 | Pluggable credential providers — `.env`/environment variable (default local fallback), encrypted local file, HashiCorp Vault, and CyberArk PAM as the priority enterprise integration — with both SSH-key and username/password authentication. |
| G7 | Redact in-config secrets and reduce noisy diffs before anything is persisted, displayed, or notified. |
| G8 | Audit-grade metadata: a queryable PostgreSQL index of jobs, diffs, and access events — not just per-device Git history. |
| G9 | Operational safety at scale: layered concurrency caps (global/group/vendor/site), connection-rate limiting, retry/backoff, circuit breaking. |
| G10 | Classify configuration changes by risk severity (Critical/High/Medium/Low) so security-relevant changes are distinguishable from routine ones. |
| G11 | Safe, group-by-group migration from an existing Oxidized deployment. |
| G12 | Remain extensible for future structured transports (NETCONF/gNMI) where they reduce fragility — not a v1 blocker. |

---

## 5. Non-Goals (v1)

- Configuration push, remediation, or rollback automation.
- SNMP fault/performance polling — not a full NMS, SIEM, IPAM, CMDB, or AAA replacement.
- Auto-discovery by scanning network ranges.
- AI-based remediation.
- A full semantic policy-compliance engine (rule-based risk *tagging*, §9.9, is in scope; a general policy engine is not).
- Multi-tenant SaaS billing/customer management.
- Exact Ruby/Oxidized plugin compatibility.
- Automatic Telnet fallback after an SSH failure — Telnet is opt-in only, never an automatic downgrade (§9.3).
- Mandatory NETCONF/gNMI support for every vendor in v1.
- A full SPA web application as a v1 blocker (see the scope flag in §9.13).

---

## 6. Success Metrics & SLOs

| Metric | Target |
|---|---|
| Full-fleet sweep duration | Default target ≤ 1 hour, configurable — contingent on measured session time, not assumed (§11) |
| Per-cycle backup success rate | ≥ 99.5%, excluding confirmed down/decommissioned devices |
| API/control-plane availability | 99.9% |
| Duplicate active jobs per device per schedule window | 0 |
| Credential leakage incidents | 0 |
| Raw secret presence in logs | 0, enforced by automated tests |
| New vendor driver lead time | ≤ 3 engineer-days given representative fixtures |
| Noisy false-positive diffs | Trending toward zero per device-group after scrub-rule tuning |
| Mean time to detect a systemic backup failure | < 5 minutes via alerting/metrics |

---

## 7. Target Users & Stakeholders

| Persona | Primary Need |
|---|---|
| NOC Engineer | Backup status, retry failed jobs, inspect latest known-good config |
| Network Security Engineer | Detect risky changes, verify audit evidence |
| Network Automation Engineer | Add/maintain drivers without learning the whole codebase |
| SOC Analyst | High-signal alerts for dangerous config changes, not noise |
| Compliance/Audit Team | Historical config and access evidence for review |
| Platform/SRE Team | Operate, scale, monitor, upgrade, and recover the system |
| Engineering Lead | Plan roadmap, assign driver work, enforce test/release quality |

---

## 8. Scope Summary

**In scope (v1):** text/CSV inventory (pluggable for future CMDB/NetBox/IPAM/LibreNMS); SSHv2 transport with explicitly-opt-in Telnet; jump-host/bastion support; six vendor drivers; scheduler/queue/worker pool with layered safety controls; normalization, redaction, and risk-classified diffing; Git-backed sharded version history plus optional filesystem/S3; PostgreSQL metadata index; REST API (OpenAPI 3.0); CLI; a minimal web UI (see scope flag, §9.13); notifications; Prometheus metrics, structured logs, health/readiness endpoints; audit logging; RBAC with API-token/OIDC auth; Oxidized migration strategy.

**Out of scope (v1):** automatic remediation/push; a full SPA as a launch blocker; deep device discovery; a general policy-as-code engine beyond simple risk tagging; replacing existing monitoring systems.

---

## 9. Functional Requirements

### 9.1 Inventory Management

1. v1 default implementation: plain text/CSV (a RANCID/Oxidized-`router.db`-compatible format is a reasonable starting point).
2. Pluggable `InventorySource` interface (§14) — CMDB, NetBox, IPAM, LibreNMS, SQL, or HTTP sources are explicitly planned future plugins, not required for v1, but the seam must exist from day one.
3. Inventory refresh without process restart (configurable interval; default 5 minutes); add/remove/change events logged.
4. Validate inventory before scheduling; reject or quarantine invalid records with a reason code rather than silently dropping or crashing.
5. Device grouping (`group`, `site`, `role`) drives scheduling windows and layered concurrency caps (§9.4).

**Canonical Target fields:**

| Field | Required | Description |
|---|---:|---|
| `id` | Yes | Stable unique device ID |
| `hostname` | Yes | Hostname |
| `ip_address` | Yes | Management IP/FQDN |
| `port` | No | Default 22 (SSH) |
| `vendor` | Yes | Driver registry key, e.g. `huawei_vrp` |
| `group` | Yes | Scheduling/concurrency group |
| `site` | No | Site/region/POP |
| `role` | No | Core, access, transport, PE, CE, etc. |
| `tags` | No | Free-form metadata |
| `jump_host` | No | Optional bastion reference |
| `credential_ref` | Yes | Opaque credential reference |
| `enabled` | Yes | Whether backups are scheduled |

### 9.2 Vendor Driver Architecture

1. Strongly-typed Go `Driver` interface (§14); drivers self-register by vendor key.
2. Each driver owns vendor-specific login, privilege escalation, paging, command sequencing, prompt/error handling, normalization, and redaction logic.
3. Adding a new driver must not require scheduler or storage changes.
4. Every driver ships with golden transcript fixtures and passes a shared conformance test suite — independent of any live lab device.
5. Drivers support command overrides per platform/firmware sub-family (important for Ericsson IPOS and ZTE ZXR10, §10).
6. Drivers return structured failure categories (`failed_connect`, `failed_auth`, `failed_privilege`, `failed_command`, `failed_timeout` — see job-state enum, §15), not opaque errors.

Required driver keys: `cisco_iosxe`, `cisco_iosxr`, `huawei_vrp`, `juniper_junos`, `ericsson_ipos`, `zte_zxr10`.

### 9.3 Transport Layer

1. SSHv2 (`golang.org/x/crypto/ssh`) is the primary and default transport.
2. **Telnet support is optional and disabled by default.** It exists only for legacy devices that genuinely lack SSH (notably some older Ericsson IPOS and ZTE ZXR10 units), and must be explicitly enabled both globally and per-device/per-group. An SSH failure is reported as a failure — it is never silently retried over Telnet.
3. Username/password and SSH private-key authentication, independent of transport.
4. Jump-host/bastion proxying (SSH-over-SSH, equivalent to `ProxyCommand`).
5. Configurable host-key verification: `strict` (production default), `tofu` (controlled onboarding), `insecure` (lab only, must log a loud warning).
6. Independent, configurable timeouts: connect, authentication, command, idle, and full-session deadline.

### 9.4 Scheduling, Queue & Worker Pool

1. Interval and cron-like schedules, per-group/per-site windows, blackout windows, and scheduling jitter (avoids every device in a group hitting AAA in the same second).
2. Manual priority ("head of line") backup trigger for a single device or group.
3. v1: bounded in-process job queue, single-node. The queue interface is designed so a Phase-2 distributed mode (external queue, DB-backed leases for multi-worker dedup) is additive, not a rewrite (§13.2).
4. **Layered concurrency caps**, acquired innermost-first: global → per-vendor → per-group → per-site. 26,000 near-simultaneous logins against a shared AAA backend is a realistic self-inflicted outage, not a hypothetical.
5. Token-bucket rate limiting on *new connection* establishment, independent of steady-state concurrency.
6. Retry with exponential backoff and jitter; per-device circuit breaker demotes chronically-failing devices to a slower retry cadence rather than hammering them at full frequency forever.
7. No duplicate active job for the same device within a schedule window (trivial in single-node v1 via an in-memory active-job set; becomes a DB-backed lease requirement only if/when multi-worker mode is adopted).

### 9.5 Credential Management

Baseline requirements, regardless of which provider backs a given deployment:

1. **Credential references, not embedded secrets** — every `Target` carries a `credential_ref` (§14) resolved at job time; no inventory record or main config file ever embeds a raw secret.
2. **Environment variable / `.env`** — default local fallback for lab/dev/small deployments. Plaintext at rest by design: ships with mandatory guardrails (`chmod 600`, `.gitignore` exclusion, a logged warning whenever GoXidized falls back to it outside a declared dev environment).
3. **Encrypted local secret file** — AES-GCM, key from environment or local KMS, never plaintext on disk; the recommended step-up from `.env` for small unattended deployments that don't want to stand up Vault/CyberArk.
4. **HashiCorp Vault** (KV v2 or dynamic secrets engine).
5. **CyberArk PAM as the priority enterprise integration** — see the proposed reference design below.
6. SSH private-key authentication (`PrivateKeyPEM`, §14).
7. Username/password authentication (`Password`, §14) — either auth mode, independent of provider.
8. **No credential material in logs, ever** — extends beyond device passwords to API tokens, SSH private keys, and raw `Authorization` headers. All such values pass through the structural redaction layer (§17) before reaching any log sink.
9. **Credential-access audit logs without exposing secrets** — every resolution and every device-config retrieval logged with who/what/when/which-target; the audit record never contains the secret value.
10. A distinct privilege-escalation secret per device/group (Cisco `enable`, Huawei `super`) as a field separate from the login credential.

**Provider priority:**

| Provider | Use case | Recommended for production |
|---|---|---:|
| CyberArk PAM | Enterprise PAM-backed fleet access, centralized audit | Yes — pending confirmation of deployed component/version, see flag below |
| HashiCorp Vault | Enterprise secret manager, alternative/complement to CyberArk | Yes |
| Encrypted local file | Small, unattended deployment | Conditional |
| Environment variable / `.env` | Lab, dev, bootstrap | No — local fallback only |

#### CyberArk PAM Integration — Proposed Reference Design

> **Flagged assumption:** a prior draft of this PRD specified **CyberArk PAM Self-Hosted 14.6+** with CCP/AAM-style REST retrieval as the production credential platform, presented as an already-confirmed decision. You haven't told me that directly in this conversation, so I'm carrying it forward here as a *recommended reference design* pending your confirmation — not as settled fact. Before implementation, confirm: which CyberArk component is actually deployed (CCP, AAM, or something PSM-managed), the version, and the access policy (AppID restrictions, reason-required, mTLS) — see §25.2.

If CyberArk's Central Credential Provider (CCP) pattern is the deployed integration, a `credential_ref` resolves to:

| Field | Required | Description |
|---|---:|---|
| `provider` | Yes | `cyberark_pam` |
| `base_url` | Yes | CCP-accessible base URL (stored in provider config, not per device) |
| `app_id` | Yes | CyberArk Application ID authorized to retrieve the account |
| `safe` | Yes | CyberArk Safe containing the network-device account |
| `object` / `account_name` | Yes | CyberArk account object name |
| `reason` | No | Access reason, if required by policy |
| `verify_tls` | Yes | Defaults to `true` |
| `client_cert_ref` | Conditional | Client cert/key reference, if mTLS is required |

```yaml
credential_ref: cyberark://safe/NETWORK-DEVICES/object/huawei-vrp-readonly?app_id=goxidized-prod
```

CyberArk-specific requirements (in addition to the general rules above):
1. No CyberArk passwords, API response bodies, `Authorization` headers, or client-certificate private keys ever appear in logs.
2. The AppID used by GoXidized is scoped only to the Safes/accounts required for network backup — never a broad/admin AppID.
3. TLS verification against an enterprise CA bundle is mandatory; mTLS supported where policy requires it.
4. A failed CyberArk lookup produces the explicit `failed_credential_provider` job status (§15), distinguishable from a device-side failure.
5. CyberArk lookup latency, success/failure counts, and circuit-breaker state are exported as Prometheus metrics, without exposing Safe/Object values.

### 9.6 Normalization & Scrub Rules

Distinct from redaction (§9.7): this stage removes *noise* (volatile, non-meaningful lines) before diffing, so a device that hasn't actually changed doesn't generate a false "changed" event.

1. Global and per-vendor regex-based normalization rules.
2. Strip command echo and pagination artifacts.
3. Preserve configuration meaning — normalization must never alter semantic content, only volatile formatting/metadata.
4. Tests for every scrub rule, per vendor.
5. Operator-defined regex overrides per driver/group, for environment-specific noise the built-in rules don't anticipate.

Examples of volatile lines: timestamps, uptime counters, last-login lines, auto-generated certificate metadata, SNMP engine values, NTP-synced clock output, rotating-value banners, paging markers.

### 9.7 Secret Redaction & Raw-Storage Policy

Distinct from §9.5 (the credentials GoXidized uses to *log in*): this governs secrets embedded *inside* a device's own configuration output — SNMP communities, local password hashes, BGP/IPsec/NTP auth keys, etc. — which must never reach storage, diffs, notifications, or the API in cleartext.

1. Redaction enabled by default; not disableable in a production profile.
2. Global + per-vendor redaction rules, with fixture tests proving each rule fires against representative output for every required vendor.
3. A redaction report (count + category, never the value) is stored alongside each revision's metadata.
4. Optional "strict mode": a pattern that looks secret-shaped but doesn't match a known category can block persistence pending manual review, rather than silently storing it unredacted.

Recognized secret categories (extend per vendor as needed): local user passwords/hashes, enable/super secrets, SNMP communities, TACACS+/RADIUS shared secrets, IPsec pre-shared keys, BGP/OSPF/NTP authentication keys, WLAN keys, incidentally-embedded API tokens. Placeholder format: `<redacted:secret-type>` — preserves line structure for diffing without exposing the value.

**Storage policy — sanitized-only by default:**
- **Default (v1), and the recommended permanent default:** only the normalized + redacted configuration is ever persisted or returned via API/notifications.
- **Optional, off by default — encrypted raw storage for forensics:** if a genuine forensic need exists to prove exactly what raw output a device returned at a point in time, raw output can be stored separately, encrypted, with its own RBAC permission (distinct from normal config viewing), its own audit log, a shorter/explicitly-approved retention, and a hard rule that it never surfaces in diffs/notifications/normal API responses. Requires explicit security sign-off before enabling in any environment.

### 9.8 Storage & Versioning

1. **Default backend: Git**, for audit-grade, content-addressed history.
2. **`go-git` vs. shell `git` — explicit ADR, not a default assumption.** `go-git` (pure Go, no external binary, simpler container, no shell-injection surface) is the natural starting point for a Go-native rewrite, but may lag native `git` in performance/feature parity at very large repo sizes. Benchmark both against a representative repo size before locking in.
3. **Configurable sharding** — region, site, vendor, device role, or a hash partition (`hash(target.ID) % shard_count`) — rather than one monolithic repository for 26,000+ devices, which degrades non-linearly in `git log`/diff/clone cost and was a known pain point in large single-repo Oxidized deployments. The shard key is a single config setting; switching strategies later is an explicit, operator-triggered migration.
4. Commits only on actual sanitized-config change (configurable: `always_commit` override available), with commit metadata trailers (timestamp, trigger source, model/serial/software-version where known).
5. Alternative backends behind the same `Storage` interface: flat filesystem (latest-only) and S3-compatible object storage with native versioning.
6. **PostgreSQL metadata index** (§15) alongside Git — Git answers "what changed on this one device"; it cannot efficiently answer "show me every AAA-related change across the fleet this week." The metadata index is what makes that query possible.
7. Scheduled repository maintenance (`git gc`-equivalent), decoupled from live backup sweeps.
8. **Retention**: default 1 year, configurable via `.env` (e.g. `RETENTION_DAYS=365`). Revisions older than the configured window become eligible for pruning during scheduled maintenance, never deleted inline during a sweep.
9. History import from an existing Oxidized Git repository where continuity matters — see §21 for why this is recommended but not made launch-blocking.

### 9.9 Diff & Risk Classification

1. Unified diffs computed against the last stored revision.
2. Diff metadata (line counts, categories touched, risk class) stored in the PostgreSQL metadata index for fleet-wide queryability.
3. Rule-based risk tagging — a **starting taxonomy meant to be tuned by your security team**, not treated as fixed:

| Risk | Example triggers |
|---|---|
| Critical | AAA/authentication disabled, new privileged/admin user, management-plane ACL opened, logging disabled |
| High | SNMP community change, TACACS+/RADIUS config change, VPN/IPsec key change, BGP auth change |
| Medium | Routing policy change, interface reconfiguration, syslog/NTP/DNS change |
| Low | Description/comment/banner changes, other non-security metadata |

4. A separate `high_risk_diff_detected` notification fires for Critical/High classifications, distinct from the generic `config_changed` event, so a SOC workflow can filter signal from routine noise.
5. SIEM/log-platform forwarding is a natural future consumer of this classification but is **not** a v1 requirement. Keep the `Notifier` interface generic (webhook JSON / syslog-style / a Beats-style shipper would all fit) so a future SIEM adapter is additive. Treat "ELK specifically" as one plausible target, not a locked decision (§25.2).

### 9.10 Notifications

Event types: `config_changed`, `backup_failed`, `device_unreachable_chronic`, `driver_error`, `credential_provider_error`, `storage_error`, `scheduler_degraded`, `high_risk_diff_detected`.

v1 notifiers: generic webhook (JSON POST), Slack, Telegram, SMTP email. A SIEM/log-platform adapter is future-ready via the same interface, not v1-mandatory.

### 9.11 REST API

OpenAPI 3.0-documented.

| Method | Path | Description |
|---|---|---|
| GET | `/api/v1/devices` | List devices, paginated/filterable |
| GET | `/api/v1/devices/{id}` | Device detail and last status |
| POST | `/api/v1/devices/{id}/backup` | Trigger immediate backup |
| POST | `/api/v1/groups/{group}/backup` | Trigger group backup |
| GET | `/api/v1/devices/{id}/configs` | Revision history |
| GET | `/api/v1/devices/{id}/configs/latest` | Latest sanitized config (RBAC-gated) |
| GET | `/api/v1/devices/{id}/configs/diff?from=&to=` | Unified diff |
| GET | `/api/v1/jobs` | Job list |
| GET | `/api/v1/jobs/{id}` | Job detail |
| POST | `/api/v1/inventory/reload` | Force inventory reload |
| GET | `/api/v1/drivers` | List registered drivers |
| POST | `/api/v1/drivers/{name}/test` | Driver dry-run, restricted |
| GET | `/api/v1/audit/events` | Audit events, restricted |
| GET | `/healthz` | Liveness |
| GET | `/readyz` | Readiness (dependencies reachable, inventory loaded) |
| GET | `/metrics` | Prometheus metrics |

### 9.12 CLI

```bash
goxidized server start
goxidized inventory validate --file devices.csv
goxidized inventory reload
goxidized backup run --device DEVICE_ID
goxidized backup run --group GROUP
goxidized device status DEVICE_ID
goxidized config show DEVICE_ID --latest
goxidized diff DEVICE_ID --from REV_A --to REV_B
goxidized driver list
goxidized driver test --vendor huawei_vrp --fixture fixture.txt
goxidized storage verify
goxidized admin create-token
goxidized version
```

### 9.13 Web UI

> **Scope flag:** a prior draft treated a minimal web UI as a v1 *launch* requirement rather than a fast-follow, framed as already decided. That's a reasonable product call if a NOC/security team needs a visual diff viewer on day one — but it's real scope on top of the backend (status grid, filters, job detail, config viewer, diff viewer, manual trigger, RBAC-aware auth is its own mini-project). I'd recommend explicitly confirming this is launch-blocking rather than assuming it (§25.2); the REST API alone is enough to drive an external dashboard as an interim path if the UI needs to slip past v1.

If pursued for v1: device status grid (filterable by group/vendor/site/status); job detail page; sanitized-latest-config viewer (RBAC-gated); diff viewer; manual single-device and group backup trigger; driver/inventory status page; OIDC or API-token/session auth, RBAC-aware.

### 9.14 Access Control (RBAC)

1. API tokens (service-to-service, bootstrap) and OIDC (interactive/enterprise SSO).
2. RBAC separating *metadata* visibility (status, job history) from *config content* visibility (actual config/diff text) — different sensitivity levels, different permission.
3. Every config read, credential resolution, and admin action is audited (§17).

Suggested starting roles (tune to your org's actual structure):

| Role | Permissions |
|---|---|
| Admin | Full system configuration, including credential-provider and RBAC settings |
| Operator | Trigger backups, view status, manage inventory reloads |
| Security Auditor | View diffs, audit evidence, risk classifications |
| Config Viewer | View sanitized configs/diffs, no admin/trigger rights |
| Read Only | Dashboards/metadata only, no config content |

---

## 10. Vendor Driver Specifications

| Vendor / OS | Transport | Disable Paging | Primary Config-Fetch Commands | Privilege Escalation | Confidence / Notes |
|---|---|---|---|---|---|
| **Cisco IOS XE** | SSHv2 | `terminal length 0` | `show running-config` (+ `show startup-config`, `show version` for drift-vs-startup comparison and model/serial) | `enable` + enable secret | High. Must handle both unprivileged and privileged prompts. |
| **Cisco IOS XR** | SSHv2 | `terminal length 0` | `show running-config`; admin-plane config via a separate `admin show running-config` session | Admin-plane requires separate access | High, with a caveat: XR splits the main CLI from the admin/sysadmin plane — treat as **two logical configs per device** if admin-plane state matters for audit scope. |
| **Huawei VRP** | SSHv2 | `screen-length 0 temporary` | `display current-configuration` (+ `display saved-configuration`, `display version`) | AAA level / `super` password | High (matches your own domain). Use `temporary` so the paging change doesn't persist across sessions. |
| **Juniper Junos** | SSHv2 (NETCONF on port 830 as Phase-2 option) | `set cli screen-length 0` | `show configuration` (curly-brace) or `show configuration \| display set` (flat, often easier to diff) | Class-based permissions; no separate "enable" step | High. NETCONF returns structured XML — recommended Phase-2 upgrade for this driver specifically. |
| **Ericsson IPOS** (SmartEdge / Router 6000 family) | SSHv2; Telnet opt-in fallback only | Firmware-dependent — confirm per release | `show configuration` — **IPOS does not use Cisco-style `show running-config`** | `enable` on platforms with a privileged mode; some images are exec-only | **Lowest confidence in this table.** Command syntax varies materially across SEOS 6.x and IPOS 12.x/14.x/16.x firmware families. Validate against a representative lab/field unit before any production claim. |
| **ZTE ZXR10** | SSHv2; Telnet opt-in fallback only | `terminal length <n>` (0 = unlimited on most images) | `show running-config` | `enable` (privilege levels 1–15, password per level) | Medium-high. CLI closely mirrors Cisco IOS (`ZXR10>` / `ZXR10#` / `configure terminal`), but newer M6000-class platforms move toward a commit-based model similar to Junos — confirm per platform family/firmware generation before assuming classic syntax applies fleet-wide. |

**Engineering implication:** treat Ericsson IPOS and ZTE ZXR10 as the two highest-risk driver items, not because Go makes them hard, but because the vendor CLI surface itself is the least standardized of the six. Budget lab-validation time accordingly (§22, §24).

### 10.1 Vendor Validation Requirement

A driver is not production-ready until it has: at least three representative raw transcript fixtures; expected normalized-output fixture; expected redacted-output fixture; a mock-SSH-server integration test; at least one lab/field validation run; documented prompt patterns; and documented known-unsupported-firmware cases.

---

## 11. Capacity Planning

### 11.1 Formula

```
sweep_duration ≈ (device_count / effective_concurrency) × average_session_time × (1 + retry_tail_factor)
```

`effective_concurrency` is global concurrency *after* group/vendor/site caps are applied; `average_session_time` covers credential lookup + connect + auth + prepare + fetch + disconnect; `retry_tail_factor` covers timeouts, retries, slow devices, AAA delay.

### 11.2 Worked Examples — Sensitivity to the Unmeasured Variable

The single biggest unknown in this model is `average_session_time`. Two scenarios, 20% tail overhead:

**If ~12s/session (optimistic, low-latency, fast AAA):**

| Effective concurrency | Ideal | With 20% tail |
|---:|---:|---:|
| 50 | ~104 min | ~125 min |
| 100 | ~52 min | ~62 min |
| 150 | ~35 min | ~42 min |
| 250 | ~21 min | ~25 min |
| 400 | ~13 min | ~16 min |

**If ~45s/session (slower AAA/WAN path, more realistic for some telecom topologies):**

| Effective concurrency | Ideal | With 20% tail |
|---:|---:|---:|
| 100 | ~195 min | ~234 min |
| 200 | ~98 min | ~117 min |
| 300 | ~65 min | ~78 min |
| 500 | ~39 min | ~47 min |

The "≤ 1 hour" SLO (§6) is achievable in the first scenario at a modest concurrency, and requires aggressive concurrency in the second — **this is exactly why §11.4 (measuring real session time) is not optional**, it's the input that determines whether the SLO is realistic at all.

### 11.3 Starting Configuration

```yaml
scheduler:
  max_global_concurrency: 250
  max_new_connections_per_second: 10
  max_per_site_concurrency: 30
  max_per_vendor_concurrency: 100
  jitter_percent: 20
```

Tune from there based on observed AAA/TACACS+/RADIUS capacity, device CPU impact, management-WAN latency, storage throughput, failure rate, and observed p95/p99 session duration. **The limiting factor at this scale is rarely Go runtime throughput** — it's AAA capacity, the management path, slow devices, and storage contention.

### 11.4 Measuring Real Session Time Before Locking In an SLO

Run a controlled benchmark before finalizing capacity tuning:

1. Sample 30–50 representative devices per platform family where available (Huawei VRP, ZTE ZXR10, Cisco IOS XE, Cisco IOS XR, Juniper Junos, Ericsson IPOS); use all available devices and mark confidence lower where fewer exist.
2. Use the same connection path, credential-provider lookup, and commands expected in production.
3. Measure separately: credential-lookup time, TCP connect time, SSH auth time, privilege-escalation time, paging-disable time, command-execution time, total session time.
4. Record average, p50, p95, p99, timeout rate, auth-failure rate, output size — **use p95 for scheduler sizing, not average**, since slow devices dominate the tail that determines whether a sweep finishes inside its window.
5. Re-run after major network, AAA, credential-provider, or firmware changes.

| Vendor | Sample size | Avg | p50 | p95 | p99 | Timeout % | Notes |
|---|---:|---:|---:|---:|---:|---:|---|
| Huawei VRP | TBD | TBD | TBD | TBD | TBD | TBD | |
| ZTE ZXR10 | TBD | TBD | TBD | TBD | TBD | TBD | |
| Cisco IOS XE | TBD | TBD | TBD | TBD | TBD | TBD | |
| Cisco IOS XR | TBD | TBD | TBD | TBD | TBD | TBD | |
| Juniper Junos | TBD | TBD | TBD | TBD | TBD | TBD | |
| Ericsson IPOS | TBD | TBD | TBD | TBD | TBD | TBD | Highest CLI uncertainty |

---

## 12. Non-Functional Requirements

| Category | Requirement |
|---|---|
| Scalability | 26,000+ devices and 250+ effective concurrent sessions after tuning; v1 vertical (single Go process), architecture must not preclude Phase-2 distributed mode. |
| Reliability | One slow/hung device never blocks the sweep (context-bound deadlines). In-flight job state survives process restart where possible. |
| Security | TLS, RBAC, audit logging, credential references only, strict redaction defaults — see threat model, §17. |
| Maintainability | Driver SDK + conformance suite; ADRs for non-trivial decisions; semantic versioning; CI runs against fixtures, no live-lab dependency. |
| Portability | Linux amd64/arm64; static binary where practical; container image; systemd unit and Kubernetes manifests. |
| Observability | Prometheus, structured JSON logs, health/readiness, optional OpenTelemetry tracing, restricted-admin `pprof`. |
| Compliance | Default 1-year retention, configurable via `.env`; audit-grade change and access logs; no assumption of where regulatory requirements land beyond this default. |

---

## 13. System Architecture

### 13.1 Component Diagram

```
                              ┌────────────────────────────────┐
                              │        Inventory Source           │
                              │  (text/CSV v1 → future CMDB/        │
                              │   NetBox/IPAM/LibreNMS plugins)       │
                              └──────────────┬─────────────────┘
                                             │ periodic refresh
                                             ▼
 ┌────────────────┐          ┌────────────────────────────────────┐          ┌──────────────────┐
 │  REST API /      │◄───────►│       Scheduler / Controller          │─────────►│   Job Queue        │
 │  Web UI / RBAC    │         │  (group/vendor/site caps, rate limit,  │         │ (in-proc v1;       │
 └────────────────┘         │   blackout windows, jitter, leases)     │         │  NATS/Redis Phase-2)│
                              └──────────────┬─────────────────────┘          └─────────┬──────────┘
                                             │ assigns jobs                              │ dequeue
                                             ▼                                           ▼
                              ┌───────────────────────────────────────────────────────────┐
                              │                  Worker Pool (N goroutines, capped)           │
                              │  ┌───────────┐  ┌───────────┐  ┌───────────┐  ┌──────────┐    │
                              │  │ Cisco IOS  │  │ Huawei VRP │  │  Junos     │  │ ZXR10 /  │    │
                              │  │ XE / XR    │  │  Driver    │  │  Driver    │  │ IPOS     │    │
                              │  └───────────┘  └───────────┘  └───────────┘  └──────────┘    │
                              └──────────┬───────────────────────────────┬────────────────────┘
                                         │ SSH (Telnet opt-in only)      │ raw config + metadata
                                         ▼                                ▼
                              ┌───────────────────┐           ┌──────────────────────────────┐
                              │  Network Devices    │           │ Normalize → Redact →            │
                              │  (26,000+)           │           │ Diff → Risk-Classify             │
                              └───────────────────┘           └────────────┬─────────────────┘
                                                                            ▼
                                                  ┌───────────────────────────────────────────┐
                                                  │   Storage + Metadata                          │
                                                  │   Git shards (region/site/vendor/role/hash)    │
                                                  │   + FS/S3  +  PostgreSQL metadata/audit index   │
                                                  └─────────────────────┬───────────────────────┘
                                                                        ▼
                                                  ┌───────────────────────────────────────────┐
                                                  │   Notifier / Audit Log / SIEM (future)         │
                                                  │   (Slack / Telegram / Webhook / Email)           │
                                                  └───────────────────────────────────────────┘

           ┌──────────────────────────┐        ┌──────────────────────────────┐
           │  Credential Provider        │◄──────►│  Worker Pool (per-job lookup) │
           │  (CyberArk PAM / Vault /     │        └──────────────────────────────┘
           │   encrypted file / `.env`)    │
           └──────────────────────────┘

           ┌─────────────────────────────────────────────────────────────┐
           │  Metrics / Structured Logs / Tracing / Audit Events —          │
           │  emitted from every component above                            │
           └─────────────────────────────────────────────────────────────┘
```

### 13.2 Deployment Modes

| Mode | Description | Use case |
|---|---|---|
| Lab / single-node (dev) | SQLite or local files, `.env` credentials, filesystem/Git storage | Development, driver fixture work |
| Production single-node | PostgreSQL, CyberArk/Vault, Git shards, systemd or k8s replica=1 | v1 production default |
| Production HA-ready | Single active scheduler + standby, multiple worker replicas, DB-backed leases | Larger production, still one management zone |
| Distributed (Phase 2) | External queue (NATS/Redis), multiple worker pools, multi-region | Multi-region/HA scale — only if a concrete trigger emerges (see §24, "overengineering distributed mode too early") |

---

## 14. Core Go Interfaces & Data Types

```go
// SecretString and SecretBytes make accidental secret exposure structurally
// hard rather than relying on developer discipline: String()/GoString() always
// return a fixed marker, never the underlying value. Reveal() is the explicit,
// auditable unwrap used only at the point of actual use (e.g. inside the SSH
// auth call), never passed further than necessary.
type SecretString struct{ value string }
type SecretBytes struct{ value []byte }

func (s SecretString) String() string { return "[REDACTED]" }
func (s SecretString) Reveal() string { return s.value }
func (s SecretBytes) String() string  { return "[REDACTED]" }
func (s SecretBytes) Reveal() []byte  { return s.value }

// Target represents one device to be backed up.
type Target struct {
    ID            string
    Hostname      string
    IPAddress     string
    Port          int
    Vendor        string   // driver registry key, e.g. "huawei_vrp"
    Group         string
    Site          string   // optional: POP / data center / region
    Role          string   // optional: core, access, transport, PE, CE...
    Tags          []string
    JumpHost      string
    CredentialRef string
    Enabled       bool
}

// Credentials holds resolved, in-memory-only secret material for one session.
type Credentials struct {
    Username      string
    Password      SecretString
    PrivateKeyPEM SecretBytes
    EnableSecret  SecretString
    Source        string // "cyberark-pam" | "vault" | "encrypted-file" | "dotenv"
}

// Driver is the interface every vendor OS implementation must satisfy.
// Normalize and Redact are deliberately separate stages (not one "Sanitize"
// catch-all): normalization removes diff noise, redaction removes secrets —
// different concerns with different failure-handling requirements (§9.6, §9.7).
type Driver interface {
    Vendor() string
    Detect(ctx context.Context, sess Session) (bool, error) // optional auto-detection
    Connect(ctx context.Context, t Target, creds Credentials) (Session, error)
    Prepare(ctx context.Context, sess Session) error
    FetchConfig(ctx context.Context, sess Session) (*ConfigResult, error)
    Normalize(ctx context.Context, r *ConfigResult) (*ConfigResult, error)
    Redact(ctx context.Context, r *ConfigResult) (*RedactedConfig, RedactionReport, error)
    Disconnect(ctx context.Context, sess Session) error
}

// Driver self-registration, called from each driver package's init().
var registry = map[string]func() Driver{}

func RegisterDriver(name string, ctor func() Driver) { registry[name] = ctor }

func init() {
    RegisterDriver("cisco_iosxe",   func() Driver { return &ciscoIOSXE{} })
    RegisterDriver("cisco_iosxr",   func() Driver { return &ciscoIOSXR{} })
    RegisterDriver("huawei_vrp",    func() Driver { return &huaweiVRP{} })
    RegisterDriver("juniper_junos", func() Driver { return &juniperJunos{} })
    RegisterDriver("ericsson_ipos", func() Driver { return &ericssonIPOS{} })
    RegisterDriver("zte_zxr10",     func() Driver { return &zteZXR10{} })
}

type ConfigResult struct {
    TargetID   string
    FetchedAt  time.Time
    RawConfig  []byte // in memory only; persisted only if raw-storage is explicitly enabled (§9.7)
    Metadata   map[string]string
    DurationMS int64
}

type RedactedConfig struct {
    TargetID string
    Content  []byte // normalized + redacted; this is what's stored/diffed/displayed by default
}

type RedactionReport struct {
    SecretsFound int
    Categories   []string // e.g. "snmp_community", "enable_secret", "bgp_auth_key"
}

// JobStatus mirrors the state machine in §15 — string-typed for readable
// JSON/logs rather than an opaque integer.
type JobStatus string

const (
    StatusQueued                  JobStatus = "queued"
    StatusLeased                  JobStatus = "leased"
    StatusRunning                 JobStatus = "running"
    StatusSuccessNoChange         JobStatus = "success_no_change"
    StatusSuccessChanged          JobStatus = "success_changed"
    StatusFailedConnect           JobStatus = "failed_connect"
    StatusFailedAuth              JobStatus = "failed_auth"
    StatusFailedPrivilege         JobStatus = "failed_privilege"
    StatusFailedCommand           JobStatus = "failed_command"
    StatusFailedTimeout           JobStatus = "failed_timeout"
    StatusFailedNormalization     JobStatus = "failed_normalization"
    StatusFailedRedaction         JobStatus = "failed_redaction"
    StatusFailedStorage           JobStatus = "failed_storage"
    StatusFailedCredentialProvider JobStatus = "failed_credential_provider"
    StatusSkippedDisabled         JobStatus = "skipped_disabled"
    StatusSkippedBlackout         JobStatus = "skipped_blackout"
    StatusSkippedCircuitOpen      JobStatus = "skipped_circuit_open"
    StatusCancelled               JobStatus = "cancelled"
)

type JobResult struct {
    TargetID   string
    Status     JobStatus
    Attempt    int
    Err        error
    StartedAt  time.Time
    FinishedAt time.Time
}

type InventorySource interface {
    Load(ctx context.Context) ([]Target, error)
    Watch(ctx context.Context) (<-chan []Target, error) // optional, for dynamic refresh
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

// ShardStrategy determines how the Git-backed Storage implementation maps
// a Target onto one of several underlying repositories (§9.8).
type ShardStrategy string

const (
    ShardByRegion ShardStrategy = "region"
    ShardBySite   ShardStrategy = "site"
    ShardByVendor ShardStrategy = "vendor"
    ShardByRole   ShardStrategy = "role"
    ShardByHash   ShardStrategy = "hash" // hash(target.ID) % ShardCount
)

type Notifier interface {
    Notify(ctx context.Context, ev Event) error
}

type EventType string

const (
    EventConfigChanged           EventType = "config_changed"
    EventBackupFailed            EventType = "backup_failed"
    EventDeviceUnreachableChronic EventType = "device_unreachable_chronic"
    EventDriverError              EventType = "driver_error"
    EventCredentialProviderError  EventType = "credential_provider_error"
    EventStorageError             EventType = "storage_error"
    EventSchedulerDegraded        EventType = "scheduler_degraded"
    EventHighRiskDiffDetected     EventType = "high_risk_diff_detected"
)

type Event struct {
    Type      EventType
    TargetID  string
    Message   string
    Diff      string // populated for EventConfigChanged / EventHighRiskDiffDetected
    Timestamp time.Time
}
```

---

## 15. Data Model

PostgreSQL is the recommended metadata store for production (SQLite is fine for lab/MVP only — it will not hold up under 26,000-device concurrent write load).

| Table | Purpose |
|---|---|
| `devices` | Materialized current inventory state |
| `inventory_sources` | Configured inventory source definitions |
| `credential_refs` | Non-secret metadata about credential references (provider, safe/object name — never the secret itself) |
| `backup_jobs` | Job lifecycle records |
| `backup_results` | Per-job outcome detail (duration, driver, failure reason) |
| `config_versions` | Pointer to each stored Git/FS/S3 revision, with metadata |
| `config_diffs` | Diff metadata + risk classification per change |
| `schedules` | Per-group/site schedule definitions, blackout windows |
| `worker_leases` | Active job leases — duplicate-job prevention in multi-worker mode |
| `circuit_breakers` | Per-device circuit-breaker state |
| `audit_events` | Credential access, config reads, admin actions |
| `users`, `roles`, `api_tokens` | RBAC and authentication |
| `driver_versions` | Driver registry version/changelog tracking |
| `notification_events` | Notifier delivery log |

**Backup job states** (mirrors `JobStatus`, §14): `queued`, `leased`, `running`, `success_no_change`, `success_changed`, `failed_connect`, `failed_auth`, `failed_privilege`, `failed_command`, `failed_timeout`, `failed_normalization`, `failed_redaction`, `failed_storage`, `failed_credential_provider`, `skipped_disabled`, `skipped_blackout`, `skipped_circuit_open`, `cancelled`.

---

## 16. Observability

**Metrics (Prometheus):** total/enabled devices; jobs queued/running/completed; queue depth; backup/connect/command/storage duration histograms; credential-provider latency; per-vendor/per-group/per-site success/failure; circuit-breaker open count; redaction counts; config-changed count; notifier failure count; Git shard write latency; API latency.

**Logging:** structured JSON (`log/slog` or equivalent); correlation IDs across request/job/device/revision; no raw secrets, ever; clear failure-reason categories; fields friendly to downstream log/SIEM ingestion.

**Health and debug endpoints:** `/healthz` (process alive), `/readyz` (dependencies reachable, inventory loaded), `/metrics`, `/debug/pprof` (restricted to admin/debug access only).

**On Go 1.26 specifically:** the runtime/toolchain version is a hard requirement (G4); specific release-note features (e.g., the Green Tea GC, experimental goroutine-leak profiling in `runtime/pprof`) are *useful operational tooling worth evaluating during implementation*, not something the architecture depends on. Verify availability/stability at build time rather than designing around them as guaranteed.

---

## 17. Security Requirements

### 17.1 Threat Model Summary

| Threat | Control |
|---|---|
| Credential leakage in logs | `SecretString`/`SecretBytes` wrapper types, log-redaction tests, code-review gate |
| Secrets stored in config history | Redaction before persistence (§9.7), strict mode |
| Unauthorized config viewing | RBAC, audit logs, OIDC/API-token auth (§9.14) |
| SSH MITM | Strict host-key verification by default (§9.3) |
| AAA overload | Rate limiting and layered concurrency caps (§9.4) |
| Command injection via file paths | Path canonicalization; no shell execution unless ADR-approved and safely wrapped |
| Malicious/malformed device output | Parser fuzzing, output size limits, timeouts |
| Git repo tampering | Restricted access, optional signed commits, optional immutable-backup target |

### 17.2 Secure Defaults

SSH enabled by default; Telnet disabled by default; redaction enabled by default; strict host-key checking in production profile; TLS enabled for the API; config content access restricted by RBAC; the `.env` provider warns whenever used outside a declared development profile.

---

## 18. Configuration Example

```yaml
server:
  listen_address: 0.0.0.0:8080
  tls_enabled: true
  auth:
    oidc_enabled: true
    api_tokens_enabled: true

scheduler:
  default_interval: 24h
  jitter_percent: 20
  max_global_concurrency: 250
  max_new_connections_per_second: 10
  max_per_site_concurrency: 30
  max_per_vendor_concurrency: 100
  retry:
    max_attempts: 3
    backoff_initial: 30s
    backoff_max: 30m

inventory:
  refresh_interval: 5m
  sources:
    - name: primary-csv
      type: csv
      path: /etc/goxidized/devices.csv

credentials:
  default_provider: dotenv
  production_provider: cyberark_pam   # flagged assumption — confirm actual deployed component, §9.5
  fallback_provider: vault
  dotenv:
    file_path: /etc/goxidized/.env
    require_chmod_600: true
  encrypted_file:
    path: /etc/goxidized/secrets.enc
    kms_key_env: GOXIDIZED_KMS_KEY
  cyberark_pam:
    base_url_env: CYBERARK_CCP_BASE_URL
    app_id_env: CYBERARK_APP_ID
    verify_tls: true
    ca_bundle_path: /etc/goxidized/certs/cyberark-ca.pem
    request_timeout: 5s
    max_retries: 3
    rate_limit_per_second: 20

storage:
  metadata:
    type: postgres
    dsn_env: GOXIDIZED_POSTGRES_DSN
  config:
    type: git
    shard_strategy: role   # region | site | vendor | role | hash
    shard_count: 0         # only used when shard_strategy: hash
    base_path: /var/lib/goxidized/repos

redaction:
  enabled: true
  strict_mode: true
  raw_storage_enabled: false   # requires explicit security sign-off to enable, §9.7

transport:
  ssh:
    connect_timeout: 20s
    command_timeout: 60s
    idle_timeout: 30s
    host_key_mode: strict
  telnet:
    enabled: false   # global opt-in switch; per-device/group override also required

retention:
  days: 365

observability:
  metrics_enabled: true
  tracing_enabled: true
  log_format: json
```

---

## 19. Technology Stack

| Concern | Recommendation |
|---|---|
| Language | Go 1.26+ |
| HTTP/API | `net/http` with `chi` or stdlib pattern routing |
| CLI | `spf13/cobra` |
| Config | `spf13/viper` or an explicit YAML/env loader |
| SSH | `golang.org/x/crypto/ssh` |
| Git | ADR: benchmark `go-git` vs. shell `git` (§9.8) before locking in |
| Metadata database | PostgreSQL for production; SQLite only for lab/MVP |
| Migrations | `goose`, `atlas`, or equivalent |
| Metrics | `prometheus/client_golang` |
| Tracing | OpenTelemetry (optional) |
| Logging | `log/slog` (stdlib), or `zap`/`zerolog` |
| Rate limiting | `golang.org/x/time/rate` |
| Concurrency helpers | `golang.org/x/sync/errgroup`, contexts, semaphores |
| Circuit breaker | `sony/gobreaker` or a small vetted internal implementation |
| Distributed queue (Phase 2) | NATS JetStream or Redis Streams |
| Container | `distroless`/`scratch` final image where practical |

---

## 20. Testing Strategy

**Unit:** scheduler calculations; concurrency acquire/release; rate limiting; retry/backoff; circuit-breaker state; inventory validation; credential-provider mocks; redaction rules; normalization rules; storage path sharding; diff/risk classification; RBAC checks.

**Driver tests:** raw transcript fixtures; mock-SSH-server replay tests; golden normalized-output tests; golden redacted-output tests; error-prompt tests; timeout tests; oversized-output tests.

**Integration:** PostgreSQL metadata writes; Git commit workflow; S3-compatible storage if enabled; API/CLI tests; Vault mock; CyberArk mock; notification mock receivers; multi-worker lease tests (once that mode exists).

**Performance/load:** 2,000–5,000 mock SSH endpoints before a full-scale test; a simulated 26,000-device inventory; slow/unreachable devices; auth failures; credential-provider latency; storage contention; scheduler/worker restart.

**Security:** log scanning for known secret values; fuzz vendor-output parsers; host-key policy tests; RBAC tests; API-token redaction tests; `.env` permission-guard tests.

---

## 21. Migration Strategy from Oxidized

1. **Inventory compatibility** — import Oxidized/`router.db`-style inventory; map model names to GoXidized driver keys; validate every device before enabling its schedule.
2. **Shadow run** — run GoXidized alongside the existing Oxidized instance, writing to separate storage; compare config parity device-by-device, vendor-by-vendor.
3. **History import** (recommended, not launch-blocking) — import existing Oxidized Git history where continuity matters, re-sharded into GoXidized's layout. I'm recommending this be optional rather than mandatory on my own judgment, not because it was confirmed to me: making it mandatory risks turning a clean, low-risk migration into one blocked on data-cleanliness issues in years-old Git history that have nothing to do with whether GoXidized itself works correctly.
4. **Group-by-group cutover**, not a flag day. Suggested gate: ≥ 4 consecutive clean shadow sweeps, ≥ 99% config parity for the target group, 0 unresolved critical driver bugs, 0 secret-leakage findings.
5. **Decommission legacy** — freeze Oxidized for migrated groups, keep a read-only archive, update runbooks/alerts/dashboards.

---

## 22. Roadmap

*Durations assume a small dedicated team (2–4 senior Go engineers); adjust to actual team size/availability.*

| Phase | Duration | Scope |
|---|---:|---|
| Phase 0: Discovery & ADRs | 2–3 weeks | Architecture decisions (`go-git` vs. `git`, raw-storage policy), vendor fixture collection, the §11.4 benchmark plan, threat model. |
| Phase 1: Core MVP | 6–8 weeks | Interfaces, scheduler, CLI, CSV inventory, SSH transport, Git/filesystem storage, drivers for Cisco IOS XE + Huawei VRP. |
| Phase 2: Driver Expansion | 8–12 weeks | IOS XR, Junos, ZTE ZXR10, Ericsson IPOS; conformance suite; normalization/redaction engine. Start lab validation for IPOS/ZXR10 early — they're the long pole. |
| Phase 3: Enterprise Security | 6–8 weeks | CyberArk PAM integration (pending confirmation of exact component, §9.5), Vault, RBAC, audit logs, TLS, host-key policy, secret-leak tests. |
| Phase 4: Scale & Observability | 6–8 weeks | PostgreSQL metadata, leases, metrics, tracing, rate limits, circuit breakers, load tests. |
| Phase 5: API / UI / Notifications | 4–6 weeks | REST API + OpenAPI; web UI (scope to confirm, §9.13); Slack/Telegram/webhook/email notifications. |
| Phase 6: Migration & Production Hardening | 6–8 weeks | Shadow run, optional history import, HA runbooks, systemd/k8s deployment artifacts, production-readiness review. |

---

## 23. Acceptance Criteria

### 23.1 MVP Acceptance
Builds with Go 1.26+; starts as daemon and CLI; loads and validates CSV inventory; backs up Cisco IOS XE and Huawei VRP from fixtures and lab targets; normalizes and redacts output; stores changed configs in Git or filesystem; exposes metrics and health endpoints; basic API for device status and manual backup; unit and driver-fixture tests pass.

### 23.2 Production v1 Acceptance
Supports all six required vendor OSes; handles 26,000+ inventory records; completes a full sweep within the agreed backup window under realistic test conditions (per §11.4's measured numbers, not the optimistic table); supports 250+ effective concurrent sessions after safety caps; no duplicate jobs demonstrated even in multi-worker/lease mode; redacts known secrets for all required NOS families; supports Git sharding; records metadata in PostgreSQL; provides RBAC and audit logs; integrates with CyberArk (component TBD, §9.5) and `.env` as the default bootstrap provider; provides OpenAPI documentation; provides systemd and Kubernetes deployment artifacts; passes unit/integration/race/lint/security/scale tests; completes the shadow-run migration gate for the first rollout group; provides the web UI scope agreed in §9.13.

---

## 24. Risks & Mitigations

| Risk | Impact | Mitigation |
|---|---:|---|
| Ericsson IPOS CLI variance across firmware families | High | Collect real fixtures early; validate per firmware; support command overrides (§9.2, §10). |
| ZTE ZXR10 platform differences (classic vs. M6000-class) | High | Split driver by platform family if needed; lab validation before production claim. |
| AAA overload from synchronized fleet logins | Critical | Connection rate limiting, layered concurrency caps, jitter, staged rollout (§9.4, §11). |
| Git performance bottleneck at 26,000-device scale | High | Configurable sharding, the `go-git`/`git` ADR benchmark, S3 alternative, scheduled maintenance windows (§9.8). |
| Secret leakage (in-config or credential) | Critical | Redaction + strict mode, `SecretString`/`SecretBytes` wrapper types, automated log scanning (§9.5, §9.7, §17). |
| Noisy diffs | Medium | Scrub rules, per-vendor tests, operator overrides (§9.6). |
| Driver bugs causing fleet-wide failures | High | Circuit breakers, fixture tests, canary/group-by-group enablement. |
| Migration history loss | Medium | Optional history-import step + read-only archive (§21). |
| Overengineering distributed mode too early | Medium | Single-node-capable v1; queue interface keeps distributed mode additive, not a rewrite (§13.2). |
| Underestimating real session time, breaking the backup-window SLO | High | Capacity model uses measured p95/p99, not assumed averages (§11.4) — this is the single highest-leverage unknown in the whole plan. |
| CyberArk integration details assumed rather than confirmed | Medium | Explicitly flagged in §9.5/§25.2; do not begin CyberArk implementation work until the actual deployed component/version/policy is confirmed. |

---

## 25. Decisions, Assumptions & Open Questions

### 25.1 Decided in our conversation directly
- **Inventory source of truth (v1): text/CSV file**, with `InventorySource` pluggable for future CMDB/NetBox/IPAM/LibreNMS integrations.
- **Deployment topology (v1): single-node, single-process.**
- **Configuration push/remediation: out of scope for v1**, confirmed (§5).
- **Historical-configuration retention: 1 year by default**, configurable via `.env`.
- **Telnet: explicitly opt-in, disabled by default**, never an automatic SSH-failure fallback.
- **Four credential providers**: `.env` (default local fallback), encrypted local file, HashiCorp Vault, CyberArk PAM (priority enterprise integration) — both SSH-key and username/password auth supported throughout.
- **Git sharding is operator-configurable**: region, site, vendor, role, or hash partition — not fixed to any one strategy.

### 25.2 Carried forward as recommendations — please confirm, not yet treated as fact
These appeared as "confirmed decisions" in a prior draft you shared; I'm flagging them because I have no record of you telling me this directly, not because I think they're wrong:

| # | Carried-forward claim | What I need from you |
|---:|---|---|
| 1 | CyberArk PAM Self-Hosted (version ~14.6+) with CCP/AAM REST retrieval is the production credential platform. | Confirm the actual deployed component, version, and access policy (AppID scope, mTLS, reason-required) — §9.5. |
| 2 | All devices are reachable from a single management zone. | Confirm — this affects whether a single-node v1 is really sufficient or whether multi-region reachability is already a constraint. |
| 3 | Huawei VRP and ZTE ZXR10 are the current top-priority platforms by device count. | Confirm, so Phase 1/2 driver sequencing (§22) reflects your actual fleet composition rather than an assumed default. |
| 4 | A minimal web UI is mandatory for v1 launch, not a fast-follow. | Confirm — this is real added scope (§9.13); worth deciding deliberately rather than by default. |
| 5 | The future SIEM target is specifically ELK. | Not blocking — the `Notifier` interface stays generic either way — but worth confirming so the eventual adapter is built once, correctly, rather than retrofitted. |

### 25.3 Remaining open questions
1. What are the actual AAA/TACACS+/RADIUS capacity limits for the management zone(s) involved? Determines safe connection-rate and concurrency caps (§9.4, §11).
2. What are the real p95/p99 SSH session durations for the top platforms? Drives whether the 1-hour backup-window target is realistic (§11.4).
3. Exact CyberArk integration component and policy deployed — see §25.2, item 1.
4. Is configuration push/remediation truly out of scope indefinitely, or a stated future phase? (Currently a firm v1 non-goal, §5; worth revisiting only if priorities shift.)

---

## 26. Definition of Done

A feature is complete only when: code is implemented; unit tests are added; integration tests are added where applicable; fixtures are added for driver behavior; logs do not expose secrets; metrics are added for operational behavior; API/CLI docs are updated; runbook notes are updated; failure modes are documented; security implications are reviewed; lint/race/unit/integration/security checks pass; an ADR exists for any major architecture choice.

---

## 27. Appendix A: Glossary

| Term | Meaning |
|---|---|
| AAA | Authentication, Authorization, Accounting — e.g. TACACS+/RADIUS |
| CCP | CyberArk Central Credential Provider |
| Circuit breaker | Pattern that reduces repeated calls to a chronically-failing dependency/device |
| Driver | GoXidized's vendor-OS-specific retrieval implementation (Oxidized calls the equivalent a "model") |
| Scrub rule | Rule that removes/normalizes volatile output before diffing |
| Sweep | One full scheduled pass over in-scope devices |
| Revision | One stored version of a sanitized configuration |
| Shard | One of multiple storage repositories/partitions |

## 28. Appendix B: References

- Oxidized: https://github.com/ytti/oxidized
- Go 1.26 release notes: https://go.dev/doc/go1.26 (reference only — see §16 on not over-depending on specific release-note features)
- `golang.org/x/crypto/ssh`: https://pkg.go.dev/golang.org/x/crypto/ssh
- `go-git`: https://github.com/go-git/go-git
- CyberArk PAM Self-Hosted — refer to current official CyberArk documentation for the specific deployed version/component at implementation time, once confirmed (§25.2)
- HashiCorp Vault API — refer to current official HashiCorp documentation during implementation

---

*End of document.*
