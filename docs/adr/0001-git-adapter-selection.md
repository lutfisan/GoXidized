# ADR 0001: Git Adapter Selection

Status: Accepted for production-core, benchmark-gated for production v1

Date: 2026-06-24

## Context

GoXidized stores normalized and redacted device configuration as versioned Git content. The PRD requires an explicit decision between the pure-Go `go-git` library and invoking the native `git` binary through fixed arguments. The system must handle 26,000+ devices, shard repositories, avoid shell injection, and remain easy to ship in containers.

Context7 documentation for `go-git` confirms the adapter supports opening/initializing repositories, adding paths through a worktree, committing with author metadata, and iterating commit logs. That covers the production-core storage interface. Native `git` may still outperform `go-git` in very large repositories and has richer maintenance commands.

## Decision

Use `go-git` as the default production-core adapter.

Keep the shell-`git` path as a benchmark and emergency fallback only. It must remain behind the same storage boundary and must use fixed argument arrays, never shell-composed commands.

## Consequences

Positive:

- No external Git binary is required in the minimal container.
- There is no shell command surface in the default write path.
- Commit metadata, file history, and path sanitization stay inside Go code.

Tradeoffs:

- Very large repositories may require native `git gc`, repack, or maintenance windows.
- Production v1 must retain benchmark evidence before claiming `go-git` is sufficient at full scale.

## Benchmark Gate

Before production v1, run `goxidized-bench` and the shell-`git` fallback against representative repository size and filesystem class:

```powershell
$env:GOCACHE = "$PWD\.tmp\gocache"
go run ./cmd/goxidized-bench --git-devices 26000 --git-shards 128 --git-workers 64
```

Acceptance requires zero failed commits, stable p95/p99 write latency under the configured backup window, and documented Git maintenance settings.

