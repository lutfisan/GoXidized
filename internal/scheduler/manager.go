package scheduler

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	}
}

func (m *Manager) Enqueue(ctx context.Context, req Request) error {
	if req.Target.ID == "" {
		return errors.New("target id is required")
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
	if req.Job.QueuedAt.IsZero() {
		req.Job.QueuedAt = time.Now().UTC()
	}
	if delay := time.Until(req.Job.QueuedAt); delay > 0 {
		next := req
		go func() {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
			case <-timer.C:
				next.Job.QueuedAt = time.Now().UTC()
				_ = m.Enqueue(ctx, next)
			}
		}()
		return nil
	}
	m.mu.Lock()
	if _, ok := m.active[req.Target.ID]; ok {
		m.mu.Unlock()
		return fmt.Errorf("target %s already has an active job", req.Target.ID)
	}
	if c := m.cb[req.Target.ID]; time.Now().Before(c.openUntil) {
		m.mu.Unlock()
		return fmt.Errorf("%w: target %s circuit open until %s", ErrCircuitOpen, req.Target.ID, c.openUntil.Format(time.RFC3339))
	}
	m.active[req.Target.ID] = struct{}{}
	m.mu.Unlock()

	select {
	case <-ctx.Done():
		m.clearActive(req.Target.ID)
		return ctx.Err()
	case m.queue <- req:
		return nil
	}
}

var ErrCircuitOpen = errors.New("circuit open")

func (m *Manager) Start(ctx context.Context, workers int) {
	if workers <= 0 {
		workers = m.cfg.MaxGlobalConcurrency
	}
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
	defer m.clearActive(req.Target.ID)
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
		next.Job.QueuedAt = time.Now().UTC().Add(backoff(m.cfg, req.Job.Attempt))
		go func() {
			timer := time.NewTimer(time.Until(next.Job.QueuedAt))
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
		c.openUntil = time.Now().Add(m.cfg.CircuitOpenDuration)
	}
	m.cb[targetID] = c
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
