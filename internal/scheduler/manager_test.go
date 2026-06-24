package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"goxidized/pkg/goxidized"
)

func TestEnqueueDedupesActiveTarget(t *testing.T) {
	m := New(Config{QueueSize: 1, MaxGlobalConcurrency: 1, MaxNewConnectionsPerSecond: 1}, func(context.Context, Request) goxidized.JobResult {
		return goxidized.JobResult{Status: goxidized.StatusSuccessNoChange}
	})
	target := goxidized.Target{ID: "r1", Vendor: "cisco_iosxe", Group: "core", Site: "dc1"}
	if err := m.Enqueue(context.Background(), Request{Target: target}); err != nil {
		t.Fatal(err)
	}
	if err := m.Enqueue(context.Background(), Request{Target: target}); err == nil {
		t.Fatalf("expected duplicate active job error")
	}
}

func TestEnqueueHonorsFutureQueuedAt(t *testing.T) {
	m := New(Config{QueueSize: 1, MaxGlobalConcurrency: 1, MaxNewConnectionsPerSecond: 100}, func(context.Context, Request) goxidized.JobResult {
		return goxidized.JobResult{Status: goxidized.StatusSuccessNoChange}
	})
	target := goxidized.Target{ID: "r1", Vendor: "cisco_iosxe", Group: "core", Site: "dc1"}
	req := Request{Target: target, Job: goxidized.Job{QueuedAt: time.Now().Add(40 * time.Millisecond)}}
	if err := m.Enqueue(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if m.QueueDepth() != 0 {
		t.Fatalf("queue depth=%d, want 0 before queued_at", m.QueueDepth())
	}
	time.Sleep(80 * time.Millisecond)
	if m.QueueDepth() != 1 {
		t.Fatalf("queue depth=%d, want 1 after queued_at", m.QueueDepth())
	}
}

func TestLeaseDedupesAcrossManagersUntilExpiry(t *testing.T) {
	now := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	store := newMemoryLeaseStore()
	handler := func(context.Context, Request) goxidized.JobResult {
		return goxidized.JobResult{Status: goxidized.StatusSuccessNoChange}
	}
	cfg := Config{
		QueueSize: 1, MaxGlobalConcurrency: 1, MaxNewConnectionsPerSecond: 100,
		LeaseStore: store, LeaseTTL: time.Minute, Clock: func() time.Time { return now },
	}
	m1 := New(withWorkerID(cfg, "worker-1"), handler)
	m2 := New(withWorkerID(cfg, "worker-2"), handler)
	target := goxidized.Target{ID: "r1", Vendor: "cisco_iosxe", Group: "core", Site: "dc1"}

	if err := m1.Enqueue(context.Background(), Request{Target: target}); err != nil {
		t.Fatal(err)
	}
	if err := m2.Enqueue(context.Background(), Request{Target: target}); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("second enqueue error=%v, want ErrLeaseHeld", err)
	}
	now = now.Add(time.Minute + time.Nanosecond)
	if err := m2.Enqueue(context.Background(), Request{Target: target}); err != nil {
		t.Fatalf("enqueue after lease expiry: %v", err)
	}
}

func TestLeaseReleasedAfterHandlerFailure(t *testing.T) {
	now := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	store := newMemoryLeaseStore()
	done := make(chan struct{})
	m := New(Config{
		QueueSize: 4, MaxGlobalConcurrency: 1, MaxNewConnectionsPerSecond: 100, MaxAttempts: 1,
		WorkerID: "worker-1", LeaseStore: store, LeaseTTL: time.Minute, Clock: func() time.Time { return now },
	}, func(context.Context, Request) goxidized.JobResult {
		defer close(done)
		return goxidized.JobResult{Status: goxidized.StatusFailedCommand}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx, 1)
	target := goxidized.Target{ID: "r1", Vendor: "cisco_iosxe", Group: "core", Site: "dc1"}

	if err := m.Enqueue(ctx, Request{Target: target}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not run")
	}
	waitFor(t, time.Second, func() bool {
		return !store.hasActive("r1", now)
	})
	if err := m.Enqueue(ctx, Request{Target: target}); err != nil {
		t.Fatalf("enqueue after failed job released lease: %v", err)
	}
}

func TestConcurrencyCapsReleaseAfterFailures(t *testing.T) {
	stats := newCapStats()
	done := make(chan struct{}, 12)
	m := New(Config{
		QueueSize: 12, MaxGlobalConcurrency: 2, MaxPerVendorConcurrency: 1, MaxPerGroupConcurrency: 1, MaxPerSiteConcurrency: 1,
		MaxNewConnectionsPerSecond: 1000, MaxAttempts: 1,
	}, func(_ context.Context, req Request) goxidized.JobResult {
		stats.enter(req.Target)
		time.Sleep(5 * time.Millisecond)
		stats.leave(req.Target)
		done <- struct{}{}
		return goxidized.JobResult{Status: goxidized.StatusFailedCommand}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx, 8)

	for i := 0; i < 12; i++ {
		target := goxidized.Target{
			ID: fmt.Sprintf("r%d", i), Vendor: fmt.Sprintf("vendor-%d", i%2),
			Group: fmt.Sprintf("group-%d", i%2), Site: fmt.Sprintf("site-%d", i%2),
		}
		if err := m.Enqueue(ctx, Request{Target: target}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 12; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for capped jobs; a semaphore may not have been released")
		}
	}
	if stats.maxGlobal > 2 {
		t.Fatalf("max global concurrency=%d, want <=2", stats.maxGlobal)
	}
	if got := stats.maxValue(stats.maxVendor); got > 1 {
		t.Fatalf("max vendor concurrency=%d, want <=1", got)
	}
	if got := stats.maxValue(stats.maxGroup); got > 1 {
		t.Fatalf("max group concurrency=%d, want <=1", got)
	}
	if got := stats.maxValue(stats.maxSite); got > 1 {
		t.Fatalf("max site concurrency=%d, want <=1", got)
	}
}

func TestMemoryLeaseStoreSemantics(t *testing.T) {
	store := newMemoryLeaseStore()
	now := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	lease1 := WorkerLease{TargetID: "r1", WorkerID: "worker-1", JobID: "job-1", Now: now, ExpiresAt: now.Add(time.Minute)}
	ok, err := store.TryAcquireLease(context.Background(), lease1)
	if err != nil || !ok {
		t.Fatalf("first acquire ok=%v err=%v, want ok", ok, err)
	}
	lease2 := WorkerLease{TargetID: "r1", WorkerID: "worker-2", JobID: "job-2", Now: now.Add(30 * time.Second), ExpiresAt: now.Add(90 * time.Second)}
	ok, err = store.TryAcquireLease(context.Background(), lease2)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("second worker acquired unexpired lease")
	}
	renewed := lease1
	renewed.Now = now.Add(45 * time.Second)
	renewed.ExpiresAt = now.Add(2 * time.Minute)
	ok, err = store.RenewLease(context.Background(), renewed)
	if err != nil || !ok {
		t.Fatalf("renew ok=%v err=%v, want ok", ok, err)
	}
	lease2.Now = now.Add(2*time.Minute + time.Nanosecond)
	lease2.ExpiresAt = lease2.Now.Add(time.Minute)
	ok, err = store.TryAcquireLease(context.Background(), lease2)
	if err != nil || !ok {
		t.Fatalf("acquire after expiry ok=%v err=%v, want ok", ok, err)
	}
	if err := store.ReleaseLease(context.Background(), lease1); err != nil {
		t.Fatal(err)
	}
	if !store.hasActive("r1", lease2.Now) {
		t.Fatalf("stale release removed another worker's lease")
	}
	if err := store.ReleaseLease(context.Background(), lease2); err != nil {
		t.Fatal(err)
	}
	if store.hasActive("r1", lease2.Now) {
		t.Fatalf("release did not remove active lease")
	}
}

func withWorkerID(cfg Config, workerID string) Config {
	cfg.WorkerID = workerID
	return cfg
}

func waitFor(t *testing.T, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

type memoryLeaseStore struct {
	mu     sync.Mutex
	leases map[string]WorkerLease
}

func newMemoryLeaseStore() *memoryLeaseStore {
	return &memoryLeaseStore{leases: map[string]WorkerLease{}}
}

func (s *memoryLeaseStore) TryAcquireLease(_ context.Context, lease WorkerLease) (bool, error) {
	if err := lease.Validate(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.leases[lease.TargetID]
	if exists && current.ExpiresAt.After(lease.Now) && (current.WorkerID != lease.WorkerID || current.JobID != lease.JobID) {
		return false, nil
	}
	s.leases[lease.TargetID] = lease
	return true, nil
}

func (s *memoryLeaseStore) RenewLease(_ context.Context, lease WorkerLease) (bool, error) {
	if err := lease.Validate(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.leases[lease.TargetID]
	if !exists || current.WorkerID != lease.WorkerID || current.JobID != lease.JobID || !current.ExpiresAt.After(lease.Now) {
		return false, nil
	}
	s.leases[lease.TargetID] = lease
	return true, nil
}

func (s *memoryLeaseStore) ReleaseLease(_ context.Context, lease WorkerLease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.leases[lease.TargetID]
	if exists && current.WorkerID == lease.WorkerID && current.JobID == lease.JobID {
		delete(s.leases, lease.TargetID)
	}
	return nil
}

func (s *memoryLeaseStore) hasActive(targetID string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, ok := s.leases[targetID]
	return ok && lease.ExpiresAt.After(now)
}

type capStats struct {
	mu        sync.Mutex
	global    int
	maxGlobal int
	vendor    map[string]int
	group     map[string]int
	site      map[string]int
	maxVendor map[string]int
	maxGroup  map[string]int
	maxSite   map[string]int
}

func newCapStats() *capStats {
	return &capStats{
		vendor: map[string]int{}, group: map[string]int{}, site: map[string]int{},
		maxVendor: map[string]int{}, maxGroup: map[string]int{}, maxSite: map[string]int{},
	}
}

func (s *capStats) enter(t goxidized.Target) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.global++
	if s.global > s.maxGlobal {
		s.maxGlobal = s.global
	}
	incCap(s.vendor, s.maxVendor, t.Vendor)
	incCap(s.group, s.maxGroup, t.Group)
	incCap(s.site, s.maxSite, t.Site)
}

func (s *capStats) leave(t goxidized.Target) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.global--
	s.vendor[t.Vendor]--
	s.group[t.Group]--
	s.site[t.Site]--
}

func (s *capStats) maxValue(values map[string]int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	max := 0
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}

func incCap(current, max map[string]int, key string) {
	current[key]++
	if current[key] > max[key] {
		max[key] = current[key]
	}
}
