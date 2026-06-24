package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"goxidized/internal/storage/gitstore"
	"goxidized/pkg/goxidized"
)

type result struct {
	Name       string         `json:"name"`
	Count      int            `json:"count"`
	DurationMS int64          `json:"duration_ms"`
	P50MS      int64          `json:"p50_ms,omitempty"`
	P95MS      int64          `json:"p95_ms,omitempty"`
	P99MS      int64          `json:"p99_ms,omitempty"`
	Failures   int64          `json:"failures,omitempty"`
	Skipped    bool           `json:"skipped,omitempty"`
	Reason     string         `json:"reason,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
}

func main() {
	var (
		devices      = flag.Int("devices", 26000, "device count for full-sweep simulation")
		concurrency  = flag.Int("concurrency", 250, "global concurrency for full-sweep simulation")
		newConnRate  = flag.Float64("new-connection-rate", 10, "new SSH connection start rate per second")
		gitDevices   = flag.Int("git-devices", 200, "device commits for git contention benchmark")
		gitShards    = flag.Int("git-shards", 32, "hash shards for git contention benchmark")
		gitWorkers   = flag.Int("git-workers", 32, "parallel git workers")
		pgRows       = flag.Int("postgres-rows", 5000, "rows for optional Postgres insert benchmark")
		sshSamples   = flag.Int("ssh-samples", 2000, "synthetic SSH timing samples")
		outputFormat = flag.String("format", "markdown", "output format: markdown or json")
	)
	flag.Parse()

	ctx := context.Background()
	results := []result{
		fullSweep(*devices, *concurrency, *newConnRate),
		gitContention(ctx, *gitDevices, *gitShards, *gitWorkers),
		postgresWrites(ctx, *pgRows),
		sshTiming(*sshSamples),
	}

	switch *outputFormat {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			panic(err)
		}
	default:
		printMarkdown(results)
	}
}

func fullSweep(devices, concurrency int, newConnRate float64) result {
	start := time.Now()
	if devices <= 0 {
		devices = 1
	}
	if concurrency <= 0 {
		concurrency = 1
	}
	if newConnRate <= 0 {
		newConnRate = 1
	}
	workerReady := make([]float64, concurrency)
	completions := make([]int64, 0, devices)
	for i := 0; i < devices; i++ {
		w := minWorker(workerReady)
		startGate := float64(i) / newConnRate
		startAt := workerReady[w]
		if startGate > startAt {
			startAt = startGate
		}
		duration := syntheticSSHDurationMS(i) / 1000.0
		workerReady[w] = startAt + duration
		completions = append(completions, int64(workerReady[w]*1000))
	}
	sort.Slice(completions, func(i, j int) bool { return completions[i] < completions[j] })
	return result{
		Name:       "full_sweep_simulation",
		Count:      devices,
		DurationMS: time.Since(start).Milliseconds(),
		P50MS:      percentile(completions, 50),
		P95MS:      percentile(completions, 95),
		P99MS:      percentile(completions, 99),
		Meta: map[string]any{
			"concurrency":                 concurrency,
			"new_connection_rate_per_sec": newConnRate,
			"estimated_sweep_wall_ms":     completions[len(completions)-1],
			"model":                       "deterministic synthetic SSH duration distribution; validate with lab timing before production acceptance",
		},
	}
}

func gitContention(ctx context.Context, devices, shards, workers int) result {
	start := time.Now()
	base, err := os.MkdirTemp("", "goxidized-git-bench-*")
	if err != nil {
		return result{Name: "git_contention", Skipped: true, Reason: err.Error()}
	}
	defer os.RemoveAll(base)
	store := gitstore.New(filepath.Join(base, "repos"), goxidized.ShardByHash, shards, "GoXidized Bench", "bench@example.invalid")
	if workers <= 0 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var failures int64
	durations := make([]int64, devices)
	for i := 0; i < devices; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			target := goxidized.Target{ID: fmt.Sprintf("bench-%05d", i), Hostname: fmt.Sprintf("bench-%05d", i), Vendor: vendorFor(i), Role: fmt.Sprintf("role-%02d", i%8), Enabled: true}
			content := []byte(fmt.Sprintf("hostname %s\ninterface Loopback0\n description bench-%d\n", target.Hostname, i))
			sum := sha256.Sum256(content)
			cfg := goxidized.RedactedConfig{TargetID: target.ID, Content: content}
			meta := goxidized.CommitMeta{JobID: fmt.Sprintf("job-%05d", i), Trigger: "benchmark", Actor: "goxidized-bench", AdditionalTrails: map[string]string{"content_sha256": hex.EncodeToString(sum[:])}}
			writeStart := time.Now()
			if _, err := store.Save(ctx, target, cfg, meta); err != nil {
				atomic.AddInt64(&failures, 1)
			}
			durations[i] = time.Since(writeStart).Milliseconds()
		}()
	}
	wg.Wait()
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return result{
		Name:       "git_contention",
		Count:      devices,
		DurationMS: time.Since(start).Milliseconds(),
		P50MS:      percentile(durations, 50),
		P95MS:      percentile(durations, 95),
		P99MS:      percentile(durations, 99),
		Failures:   failures,
		Meta: map[string]any{
			"shards":  shards,
			"workers": workers,
			"adapter": "go-git",
		},
	}
}

func postgresWrites(ctx context.Context, rows int) result {
	dsn := os.Getenv("GOXIDIZED_BENCH_POSTGRES_DSN")
	if dsn == "" {
		return result{Name: "postgres_writes", Count: rows, Skipped: true, Reason: "GOXIDIZED_BENCH_POSTGRES_DSN not set"}
	}
	start := time.Now()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return result{Name: "postgres_writes", Count: rows, Skipped: true, Reason: err.Error()}
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `CREATE TEMP TABLE goxidized_bench_jobs (id text PRIMARY KEY, target_id text NOT NULL, status text NOT NULL, created_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return result{Name: "postgres_writes", Count: rows, Skipped: true, Reason: err.Error()}
	}
	durations := make([]int64, rows)
	var failures int64
	for i := 0; i < rows; i++ {
		rowStart := time.Now()
		_, err := pool.Exec(ctx, `INSERT INTO goxidized_bench_jobs(id,target_id,status) VALUES ($1,$2,$3)`, fmt.Sprintf("job-%d", i), fmt.Sprintf("device-%d", i), "success_no_change")
		if err != nil {
			failures++
		}
		durations[i] = time.Since(rowStart).Milliseconds()
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return result{Name: "postgres_writes", Count: rows, DurationMS: time.Since(start).Milliseconds(), P50MS: percentile(durations, 50), P95MS: percentile(durations, 95), P99MS: percentile(durations, 99), Failures: failures}
}

func sshTiming(samples int) result {
	start := time.Now()
	if samples <= 0 {
		samples = 1
	}
	durations := make([]int64, samples)
	for i := 0; i < samples; i++ {
		durations[i] = int64(syntheticSSHDurationMS(i))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return result{
		Name:       "ssh_session_timing",
		Count:      samples,
		DurationMS: time.Since(start).Milliseconds(),
		P50MS:      percentile(durations, 50),
		P95MS:      percentile(durations, 95),
		P99MS:      percentile(durations, 99),
		Meta: map[string]any{
			"source": "synthetic deterministic distribution calibrated for benchmark plumbing only; replace with lab CSV for production acceptance",
		},
	}
}

func syntheticSSHDurationMS(i int) float64 {
	base := 2800 + float64((i*7919)%3500)
	if i%37 == 0 {
		base += 8000
	}
	if i%211 == 0 {
		base += 22000
	}
	return base
}

func vendorFor(i int) string {
	vendors := []string{"cisco_iosxe", "huawei_vrp", "cisco_iosxr", "juniper_junos", "zte_zxr10", "ericsson_ipos"}
	return vendors[i%len(vendors)]
}

func minWorker(values []float64) int {
	minIdx := 0
	for i := 1; i < len(values); i++ {
		if values[i] < values[minIdx] {
			minIdx = i
		}
	}
	return minIdx
}

func percentile(values []int64, p int) int64 {
	if len(values) == 0 {
		return 0
	}
	idx := (len(values)*p + 99) / 100
	if idx <= 0 {
		idx = 1
	}
	if idx > len(values) {
		idx = len(values)
	}
	return values[idx-1]
}

func printMarkdown(results []result) {
	fmt.Println("# GoXidized Production-Core Benchmark Evidence")
	fmt.Println()
	fmt.Printf("Generated: %s\n\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Println("| Benchmark | Count | Duration ms | p50 ms | p95 ms | p99 ms | Failures | Notes |")
	fmt.Println("| --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |")
	for _, r := range results {
		note := ""
		if r.Skipped {
			note = "SKIPPED: " + r.Reason
		} else if len(r.Meta) > 0 {
			data, _ := json.Marshal(r.Meta)
			note = string(data)
		}
		fmt.Printf("| %s | %d | %d | %d | %d | %d | %d | %s |\n", r.Name, r.Count, r.DurationMS, r.P50MS, r.P95MS, r.P99MS, r.Failures, note)
	}
}
