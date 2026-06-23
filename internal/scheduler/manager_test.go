package scheduler

import (
	"context"
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
