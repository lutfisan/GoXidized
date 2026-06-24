# GoXidized Production-Core Benchmark Evidence

Date: 2026-06-24

Host context: local Windows workspace under `D:\project\codex\GoXidized`.

Command:

```powershell
$env:GOCACHE = Join-Path (Get-Location) '.tmp\gocache'
go run ./cmd/goxidized-bench --devices 26000 --concurrency 250 --new-connection-rate 10 --git-devices 200 --git-shards 32 --git-workers 16 --ssh-samples 2000
```

## Results

| Benchmark | Count | Duration ms | p50 ms | p95 ms | p99 ms | Failures | Notes |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| full_sweep_simulation | 26000 | 14 | 1304610 | 2474866 | 2578727 | 0 | concurrency 250; new connection rate 10/s; estimated sweep wall time 2621907 ms |
| git_contention | 200 | 30919 | 2602 | 3576 | 3644 | 0 | go-git adapter; 32 hash shards; 16 workers |
| postgres_writes | 5000 | 0 | 0 | 0 | 0 | 0 | skipped because `GOXIDIZED_BENCH_POSTGRES_DSN` was not set |
| ssh_session_timing | 2000 | 0 | 4607 | 6230 | 13408 | 0 | deterministic synthetic SSH distribution |

## Interpretation

The benchmark command and evidence path are now in place. This run proves the measurement plumbing, full-sweep scheduling model, and bounded local go-git contention path. It is not a production acceptance benchmark because it did not use live lab devices or a reachable PostgreSQL instance.

Production v1 still requires the same command to be rerun with:

- `--git-devices 26000` and the intended production shard count.
- `GOXIDIZED_BENCH_POSTGRES_DSN` pointed at the target PostgreSQL class.
- Lab-derived SSH timing samples or live SSH benchmark collection per NOS family.
- Comparison against the fixed-argument shell-`git` fallback before closing ADR 0001's benchmark gate.

