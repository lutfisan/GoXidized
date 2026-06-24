package scheduler

import (
	"container/heap"
	"fmt"
	"sort"
	"testing"
	"time"

	"goxidized/pkg/goxidized"
)

func TestScaleSchedulingSimulation(t *testing.T) {
	base := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)
	planner, err := NewPlanner(SchedulePolicy{
		Interval:      24 * time.Hour,
		JitterPercent: 20,
		Location:      time.UTC,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{50, 500, 5000, 26000} {
		t.Run(fmt.Sprintf("%d_devices", count), func(t *testing.T) {
			targets := scaleTargets(count)
			jobs := make([]simJob, 0, len(targets))
			scheduleDeltas := make([]time.Duration, 0, len(targets))
			seen := map[string]bool{}
			for _, target := range targets {
				if seen[target.ID] {
					t.Fatalf("duplicate target id in simulation: %s", target.ID)
				}
				seen[target.ID] = true
				decision, err := planner.QueueTime(base, target)
				if err != nil {
					t.Fatal(err)
				}
				scheduleDeltas = append(scheduleDeltas, decision.QueueAt.Sub(base))
				jobs = append(jobs, simJob{targetID: target.ID, queueAt: decision.QueueAt, duration: 2 * time.Minute})
			}
			queueDelays, duplicateActive := simulateQueue(jobs, 250)
			if duplicateActive {
				t.Fatalf("simulation produced duplicate active job for a target")
			}
			scheduleP95, scheduleP99 := percentile(scheduleDeltas, 0.95), percentile(scheduleDeltas, 0.99)
			queueP95, queueP99 := percentile(queueDelays, 0.95), percentile(queueDelays, 0.99)
			t.Logf("devices=%d schedule_p95=%s schedule_p99=%s queue_p95=%s queue_p99=%s", count, scheduleP95, scheduleP99, queueP95, queueP99)
			if scheduleP99 > 5*time.Hour {
				t.Fatalf("schedule p99=%s, want <=5h with 20%% jitter over 24h", scheduleP99)
			}
			if queueP99 > 30*time.Minute {
				t.Fatalf("queue p99=%s, want <=30m for simulated 2m jobs at 250 concurrency", queueP99)
			}
		})
	}
}

type simJob struct {
	targetID string
	queueAt  time.Time
	duration time.Duration
}

func scaleTargets(count int) []goxidized.Target {
	targets := make([]goxidized.Target, 0, count)
	for i := 0; i < count; i++ {
		targets = append(targets, goxidized.Target{
			ID:     fmt.Sprintf("device-%05d", i),
			Vendor: fmt.Sprintf("vendor-%02d", i%6),
			Group:  fmt.Sprintf("group-%02d", i%20),
			Site:   fmt.Sprintf("site-%03d", i%200),
			Role:   fmt.Sprintf("role-%02d", i%8),
		})
	}
	return targets
}

func simulateQueue(jobs []simJob, concurrency int) ([]time.Duration, bool) {
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].queueAt.Equal(jobs[j].queueAt) {
			return jobs[i].targetID < jobs[j].targetID
		}
		return jobs[i].queueAt.Before(jobs[j].queueAt)
	})
	active := map[string]bool{}
	finishes := finishHeap{}
	heap.Init(&finishes)
	delays := make([]time.Duration, 0, len(jobs))
	for _, job := range jobs {
		for finishes.Len() > 0 && !finishes[0].finish.After(job.queueAt) {
			finished := heap.Pop(&finishes).(simFinish)
			delete(active, finished.targetID)
		}
		start := job.queueAt
		if finishes.Len() >= concurrency {
			finished := heap.Pop(&finishes).(simFinish)
			delete(active, finished.targetID)
			if finished.finish.After(start) {
				start = finished.finish
			}
		}
		for finishes.Len() > 0 && !finishes[0].finish.After(start) {
			finished := heap.Pop(&finishes).(simFinish)
			delete(active, finished.targetID)
		}
		if active[job.targetID] {
			return delays, true
		}
		active[job.targetID] = true
		delays = append(delays, start.Sub(job.queueAt))
		heap.Push(&finishes, simFinish{targetID: job.targetID, finish: start.Add(job.duration)})
	}
	return delays, false
}

type simFinish struct {
	targetID string
	finish   time.Time
}

type finishHeap []simFinish

func (h finishHeap) Len() int { return len(h) }
func (h finishHeap) Less(i, j int) bool {
	if h[i].finish.Equal(h[j].finish) {
		return h[i].targetID < h[j].targetID
	}
	return h[i].finish.Before(h[j].finish)
}
func (h finishHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *finishHeap) Push(x any)   { *h = append(*h, x.(simFinish)) }
func (h *finishHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

func percentile(values []time.Duration, p float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}
