# ADR 0002: Raw Transcript Storage Policy

Status: Accepted

Date: 2026-06-24

## Context

Drivers need raw transcripts for conformance, troubleshooting, and vendor regression analysis. The PRD also requires secrets to never reach Git, APIs, notifications, or logs in clear text. Raw device output can include passwords, SNMP communities, keys, local users, and AAA material.

## Decision

Production storage commits only normalized and redacted configuration.

Raw transcripts are not persisted by default. Test fixtures may contain faithful synthetic transcripts and must use seeded dummy secrets only. If operators enable raw transcript retention later, it must be encrypted at rest, excluded from Git history, bounded by retention, and inaccessible through normal config APIs.

## Required Controls

- `redaction.raw_storage_enabled` defaults to `false`.
- Driver fixtures that include secrets must use obvious seeded values and expected redaction outputs.
- Logs must not include raw command output.
- Any future raw-retention feature must emit audit events on write/read/delete.
- Any future raw-retention feature must document key management and deletion semantics.

## Consequences

Positive:

- Git remains sanitized and safe to replicate.
- Diffs, notifications, and API responses share one redacted source of truth.

Tradeoffs:

- Some field debugging requires reproducing the issue or collecting an explicitly approved encrypted transcript bundle.

