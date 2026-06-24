package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"goxidized/pkg/goxidized"
)

type Request struct {
	Job    goxidized.Job
	Target goxidized.Target
}

type Handler func(context.Context, Request) goxidized.JobResult

type Config struct {
	QueueSize                  int
	MaxGlobalConcurrency       int
	MaxPerVendorConcurrency    int
	MaxPerGroupConcurrency     int
	MaxPerSiteConcurrency      int
	MaxNewConnectionsPerSecond float64
	MaxAttempts                int
	BackoffInitial             time.Duration
	BackoffMax                 time.Duration
	CircuitFailureThreshold    int
	CircuitOpenDuration        time.Duration
	WorkerID                   string
	LeaseStore                 LeaseStore
	LeaseTTL                   time.Duration
	LeaseRenewInterval         time.Duration
	Clock                      func() time.Time
}

type Manager struct {
	cfg     Config
	handler Handler
	limiter *rate.Limiter
	queue   chan Request

	global chan struct{}
	mu     sync.Mutex
	vendor map[string]chan struct{}
	group  map[string]chan struct{}
	site   map[string]chan struct{}
	active map[string]struct{}
	cb     map[string]circuit

	workerID           string
	leases             LeaseStore
	leaseTTL           time.Duration
	leaseRenewInterval time.Duration
	leased             map[string]WorkerLease
	renewerOnce        sync.Once
	clock              func() time.Time
}

type circuit struct {
	failures  int
	openUntil time.Time
}

func New(cfg Config, handler Handler) *Manager {
	cfg = normalize(cfg)
	return &Manager{
		cfg: cfg, handler: handler, limiter: rate.NewLimiter(rate.Limit(cfg.MaxNewConnectionsPerSecond), int(math.Ceil(cfg.MaxNewConnectionsPerSecond))),
		queue: make(chan Request, cfg.QueueSize), global: make(chan struct{}, cfg.MaxGlobalConcurrency),
		vendor: map[string]chan struct{}{}, group: map[string]chan struct{}{}, site: map[string]chan struct{}{},
		active: map[string]struct{}{}, cb: map[string]circuit{},
		workerID: cfg.WorkerID, leases: cfg.LeaseStore, leaseTTL: cfg.LeaseTTL, leaseRenewInterval: cfg.LeaseRenewInterval,
		leased: map[string]WorkerLease{}, clock: cfg.Clock,
	}
}

func (m *Manager) Enqueue(ctx context.Context, req Request) error {
	var err error
	req, err = m.prepare(req)
	if err != nil {
		return err
	}
	if delay := req.Job.QueuedAt.Sub(m.now()); delay > 0 {
		next := req
		go func() {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
			case <-timer.C:
				next.Job.QueuedAt = m.now()
				_ = m.Enqueue(ctx, next)
			}
		}()
		return nil
	}
	if err := m.reserve(ctx, req); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		m.releaseReservation(context.Background(), req)
		return ctx.Err()
	case m.queue <- req:
		return nil
	}
}

func (m *Manager) prepare(req Request) (Request, error) {
	if req.Target.ID == "" {
		return Request{}, errors.New("target id is required")
	}
	if req.Job.TargetID == "" {
		req.Job.TargetID = req.Target.ID
	}
	if req.Job.Group == "" {
		req.Job.Group = req.Target.Group
	}
	if req.Job.Status == "" {
		req.Job.Status = goxidized.StatusQueued
	}
	if req.Job.Attempt == 0 {
		req.Job.Attempt = 1
	}
	if req.Job.ID == "" {
		req.Job.ID = newJobID()
	}
	if req.Job.QueuedAt.IsZero() {
		req.Job.QueuedAt = m.now()
	}
	return req, nil
}

func (m *Manager) reserve(ctx context.Context, req Request) error {
	now := m.now()
	m.mu.Lock()
	if _, ok := m.active[req.Target.ID]; ok {
		m.mu.Unlock()
		return fmt.Errorf("%w: target %s", ErrDuplicateActive, req.Target.ID)
	}
	if c := m.cb[req.Target.ID]; now.Before(c.openUntil) {
		m.mu.Unlock()
		return fmt.Errorf("%w: target %s circuit open until %s", ErrCircuitOpen, req.Target.ID, c.openUntil.Format(time.RFC3339))
	}
	m.active[req.Target.ID] = struct{}{}
	m.mu.Unlock()

	if m.leases == nil {
		return nil
	}
	lease := WorkerLease{
		TargetID:  req.Target.ID,
		WorkerID:  m.workerID,
		JobID:     req.Job.ID,
		Now:       now,
		ExpiresAt: now.Add(m.leaseTTL),
	}
	ok, err := m.leases.TryAcquireLease(ctx, lease)
	if err != nil {
		m.clearActive(req.Target.ID)
		return err
	}
	if !ok {
		m.clearActive(req.Target.ID)
		return fmt.Errorf("%w: target %s", ErrLeaseHeld, req.Target.ID)
	}
	m.mu.Lock()
	m.leased[req.Target.ID] = lease
	m.mu.Unlock()
	return nil
}

var ErrCircuitOpen = errors.New("circuit open")
var ErrDuplicateActive = errors.New("target already has an active job")
var ErrLeaseHeld = errors.New("target lease is held")

func (m *Manager) Start(ctx context.Context, workers int) {
	if workers <= 0 {
		workers = m.cfg.MaxGlobalConcurrency
	}
	m.startLeaseRenewer(ctx)
	for i := 0; i < workers; i++ {
		go m.worker(ctx)
	}
}

func (m *Manager) QueueDepth() int {
	return len(m.queue)
}

func (m *Manager) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-m.queue:
			m.run(ctx, req)
		}
	}
}

func (m *Manager) run(ctx context.Context, req Request) {
	defer m.releaseReservation(context.Background(), req)
	if err := m.acquire(ctx, req.Target); err != nil {
		return
	}
	defer m.release(req.Target)
	if err := m.limiter.Wait(ctx); err != nil {
		return
	}
	result := m.handler(ctx, req)
	if isSuccess(result.Status) {
		m.recordSuccess(req.Target.ID)
		return
	}
	m.recordFailure(req.Target.ID)
	if req.Job.Attempt < m.cfg.MaxAttempts {
		next := req
		next.Job.Attempt++
		next.Job.ID = ""
		next.Job.QueuedAt = m.now().Add(backoff(m.cfg, req.Job.Attempt))
		go func() {
			timer := time.NewTimer(next.Job.QueuedAt.Sub(m.now()))
			defer timer.Stop()
			select {
			case <-ctx.Done():
			case <-timer.C:
				_ = m.Enqueue(ctx, next)
			}
		}()
	}
}

func (m *Manager) acquire(ctx context.Context, t goxidized.Target) error {
	keys := []chan struct{}{m.global, m.sem(m.vendor, t.Vendor, m.cfg.MaxPerVendorConcurrency), m.sem(m.group, t.Group, m.cfg.MaxPerGroupConcurrency), m.sem(m.site, t.Site, m.cfg.MaxPerSiteConcurrency)}
	acquired := make([]chan struct{}, 0, len(keys))
	for _, sem := range keys {
		select {
		case <-ctx.Done():
			for i := len(acquired) - 1; i >= 0; i-- {
				<-acquired[i]
			}
			return ctx.Err()
		case sem <- struct{}{}:
			acquired = append(acquired, sem)
		}
	}
	return nil
}

func (m *Manager) release(t goxidized.Target) {
	sems := []chan struct{}{m.sem(m.site, t.Site, m.cfg.MaxPerSiteConcurrency), m.sem(m.group, t.Group, m.cfg.MaxPerGroupConcurrency), m.sem(m.vendor, t.Vendor, m.cfg.MaxPerVendorConcurrency), m.global}
	for _, sem := range sems {
		select {
		case <-sem:
		default:
		}
	}
}

func (m *Manager) sem(bucket map[string]chan struct{}, key string, cap int) chan struct{} {
	if key == "" {
		key = "default"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sem, ok := bucket[key]
	if !ok {
		sem = make(chan struct{}, cap)
		bucket[key] = sem
	}
	return sem
}

func (m *Manager) clearActive(targetID string) {
	m.mu.Lock()
	delete(m.active, targetID)
	m.mu.Unlock()
}

func (m *Manager) recordSuccess(targetID string) {
	m.mu.Lock()
	delete(m.cb, targetID)
	m.mu.Unlock()
}

func (m *Manager) recordFailure(targetID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.cb[targetID]
	c.failures++
	if c.failures >= m.cfg.CircuitFailureThreshold {
		c.openUntil = m.now().Add(m.cfg.CircuitOpenDuration)
	}
	m.cb[targetID] = c
}

func (m *Manager) releaseReservation(ctx context.Context, req Request) {
	if m.leases != nil {
		lease, ok := m.takeLease(req.Target.ID)
		if ok {
			_ = m.leases.ReleaseLease(ctx, lease)
		}
	}
	m.clearActive(req.Target.ID)
}

func (m *Manager) takeLease(targetID string) (WorkerLease, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lease, ok := m.leased[targetID]
	if ok {
		delete(m.leased, targetID)
	}
	return lease, ok
}

func (m *Manager) snapshotLeases() []WorkerLease {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]WorkerLease, 0, len(m.leased))
	for _, lease := range m.leased {
		out = append(out, lease)
	}
	return out
}

func (m *Manager) startLeaseRenewer(ctx context.Context) {
	if m.leases == nil {
		return
	}
	m.renewerOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(m.leaseRenewInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					m.renewLeases(ctx)
				}
			}
		}()
	})
}

func (m *Manager) renewLeases(ctx context.Context) {
	now := m.now()
	for _, lease := range m.snapshotLeases() {
		lease.Now = now
		lease.ExpiresAt = now.Add(m.leaseTTL)
		if ok, err := m.leases.RenewLease(ctx, lease); err == nil && ok {
			m.mu.Lock()
			if current, exists := m.leased[lease.TargetID]; exists && current.JobID == lease.JobID && current.WorkerID == lease.WorkerID {
				m.leased[lease.TargetID] = lease
			}
			m.mu.Unlock()
		}
	}
}

func (m *Manager) now() time.Time {
	if m.clock == nil {
		return time.Now().UTC()
	}
	return m.clock().UTC()
}

func normalize(cfg Config) Config {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 10000
	}
	if cfg.MaxGlobalConcurrency <= 0 {
		cfg.MaxGlobalConcurrency = 1
	}
	if cfg.MaxPerVendorConcurrency <= 0 {
		cfg.MaxPerVendorConcurrency = cfg.MaxGlobalConcurrency
	}
	if cfg.MaxPerGroupConcurrency <= 0 {
		cfg.MaxPerGroupConcurrency = cfg.MaxGlobalConcurrency
	}
	if cfg.MaxPerSiteConcurrency <= 0 {
		cfg.MaxPerSiteConcurrency = cfg.MaxGlobalConcurrency
	}
	if cfg.MaxNewConnectionsPerSecond <= 0 {
		cfg.MaxNewConnectionsPerSecond = 1
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 1
	}
	if cfg.BackoffInitial <= 0 {
		cfg.BackoffInitial = time.Second
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = time.Minute
	}
	if cfg.CircuitFailureThreshold <= 0 {
		cfg.CircuitFailureThreshold = 5
	}
	if cfg.CircuitOpenDuration <= 0 {
		cfg.CircuitOpenDuration = 30 * time.Minute
	}
	if cfg.WorkerID == "" {
		cfg.WorkerID = defaultWorkerID()
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = 15 * time.Minute
	}
	if cfg.LeaseRenewInterval <= 0 {
		cfg.LeaseRenewInterval = cfg.LeaseTTL / 3
		if cfg.LeaseRenewInterval <= 0 {
			cfg.LeaseRenewInterval = time.Minute
		}
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	return cfg
}

func backoff(cfg Config, attempt int) time.Duration {
	d := cfg.BackoffInitial
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= cfg.BackoffMax {
			return cfg.BackoffMax
		}
	}
	return d
}

func isSuccess(status goxidized.JobStatus) bool {
	return status == goxidized.StatusSuccessChanged || status == goxidized.StatusSuccessNoChange
}

func defaultWorkerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

func newJobID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("job-%d", time.Now().UnixNano())
	}
	return "job-" + hex.EncodeToString(b[:])
}
