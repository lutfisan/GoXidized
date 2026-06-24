# ADR 0003: Git Sharding and Maintenance

Status: Accepted for production-core

Date: 2026-06-24

## Context

The PRD requires Git storage to remain predictable at 26,000+ devices. A single large repository risks write contention, slow log scans, and expensive maintenance. GoXidized stores configs under `<base>/<strategy>/<shard>/<vendor>/<device_id>.cfg`.

## Decision

Use configurable repository sharding from the start.

Default production-core sharding is role-based for readability. Full-scale deployments should use hash sharding when roles/sites are imbalanced. The recommended starting point for 26,000 devices is 64 to 256 hash shards, then tune from benchmark data.

## Maintenance Policy

- Commit only normalized and redacted content.
- Include commit trailers for target ID, job ID, content SHA, ruleset version, and redaction evidence.
- Run Git maintenance during backup-light windows.
- Keep native shell-`git` maintenance commands fixed-argument only.
- Reconciliation must rebuild Postgres metadata from Git commit trailers if Git commit succeeds and metadata write fails.

## Benchmark Gate

Run local and production-like Git contention benchmarks:

```powershell
$env:GOCACHE = "$PWD\.tmp\gocache"
go run ./cmd/goxidized-bench --git-devices 26000 --git-shards 128 --git-workers 64
```

Production v1 requires measured p95/p99 write latency, zero lost commits, and an operator runbook for shard count changes and repository maintenance.

