package scheduler

import (
	"testing"
	"time"

	"goxidized/pkg/goxidized"
)

func TestPlannerIntervalWindowAndBlackout(t *testing.T) {
	loc := time.UTC
	planner, err := NewPlanner(SchedulePolicy{
		Interval:      24 * time.Hour,
		Location:      loc,
		JitterPercent: 0,
		Windows: []ScheduleWindow{{
			Name: "core-business-hours", Groups: []string{"core"}, Days: []time.Weekday{time.Monday},
			Start: 9 * time.Hour, End: 17 * time.Hour, Location: loc,
		}},
		Blackouts: []ScheduleWindow{{
			Name: "dc1-maintenance", Sites: []string{"dc1"}, Days: []time.Weekday{time.Monday},
			Start: 10 * time.Hour, End: 11 * time.Hour, Location: loc,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := goxidized.Target{ID: "r1", Group: "core", Site: "dc1"}

	beforeWindow := time.Date(2026, 6, 22, 8, 30, 0, 0, time.UTC)
	decision, err := planner.QueueTime(beforeWindow, target)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	if !decision.QueueAt.Equal(want) || !decision.DelayedByWindow {
		t.Fatalf("queue_at=%s delayed_window=%v, want %s and delayed by window", decision.QueueAt, decision.DelayedByWindow, want)
	}

	duringBlackout := time.Date(2026, 6, 22, 10, 15, 0, 0, time.UTC)
	decision, err = planner.QueueTime(duringBlackout, target)
	if err != nil {
		t.Fatal(err)
	}
	want = time.Date(2026, 6, 22, 11, 0, 0, 0, time.UTC)
	if !decision.QueueAt.Equal(want) || !decision.DelayedByBlackout {
		t.Fatalf("queue_at=%s delayed_blackout=%v, want %s and delayed by blackout", decision.QueueAt, decision.DelayedByBlackout, want)
	}
}

func TestPlannerCronNextBase(t *testing.T) {
	planner, err := NewPlanner(SchedulePolicy{Cron: "*/15 9-10 * * mon-fri", Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	next, err := planner.NextBase(time.Date(2026, 6, 22, 9, 7, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 6, 22, 9, 15, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next=%s, want %s", next, want)
	}

	next, err = planner.NextBase(time.Date(2026, 6, 26, 10, 50, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want = time.Date(2026, 6, 29, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next after weekend=%s, want %s", next, want)
	}
}

func TestPlannerInjectedJitter(t *testing.T) {
	base := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	planner, err := NewPlanner(SchedulePolicy{
		Interval:      time.Hour,
		JitterPercent: 10,
		Jitter: func(_ string, max time.Duration) time.Duration {
			return max / 2
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := planner.QueueTime(base, goxidized.Target{ID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Jitter != 3*time.Minute {
		t.Fatalf("jitter=%s, want 3m", decision.Jitter)
	}
	if !decision.QueueAt.Equal(base.Add(3 * time.Minute)) {
		t.Fatalf("queue_at=%s, want %s", decision.QueueAt, base.Add(3*time.Minute))
	}
}

func TestOvernightWindow(t *testing.T) {
	window := ScheduleWindow{Days: []time.Weekday{time.Monday}, Start: 22 * time.Hour, End: 2 * time.Hour, Location: time.UTC}
	if !window.Contains(time.Date(2026, 6, 22, 23, 0, 0, 0, time.UTC)) {
		t.Fatalf("expected Monday 23:00 inside overnight window")
	}
	if !window.Contains(time.Date(2026, 6, 23, 1, 30, 0, 0, time.UTC)) {
		t.Fatalf("expected Tuesday 01:30 inside Monday overnight window")
	}
	if window.Contains(time.Date(2026, 6, 23, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("did not expect Tuesday 03:00 inside overnight window")
	}
}
