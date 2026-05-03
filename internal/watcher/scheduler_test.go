package watcher

import (
	"testing"
	"time"
)

func newTestScheduler(allowed []string, cooldown time.Duration, duplicate TriggerResult) (*turnScheduler, *time.Time) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	scheduler := newTurnScheduler(allowed, cooldown, duplicate)
	scheduler.now = func() time.Time { return now }
	return scheduler, &now
}

func requireAction(t *testing.T, action schedulerAction, want schedulerActionKind) {
	t.Helper()
	if action.kind != want {
		t.Fatalf("action kind: got %v, want %v", action.kind, want)
	}
}

func TestTurnScheduler_Started(t *testing.T) {
	scheduler, _ := newTestScheduler([]string{"mike"}, 0, Dropped)

	result, action := scheduler.Trigger("mike")
	if result != Started {
		t.Fatalf("trigger: got %v, want Started", result)
	}
	requireAction(t, action, schedulerStartNow)
}

func TestTurnScheduler_Queued(t *testing.T) {
	scheduler, _ := newTestScheduler([]string{"mike"}, 0, Dropped)
	scheduler.Trigger("mike")

	result, action := scheduler.Trigger("mike")
	if result != Queued {
		t.Fatalf("trigger while running: got %v, want Queued", result)
	}
	requireAction(t, action, schedulerNoop)
}

func TestTurnScheduler_DuplicateQueue(t *testing.T) {
	scheduler, _ := newTestScheduler([]string{"mike"}, 0, Dropped)
	scheduler.Trigger("mike")
	scheduler.Trigger("mike")

	result, action := scheduler.Trigger("mike")
	if result != Dropped {
		t.Fatalf("duplicate queued trigger: got %v, want Dropped", result)
	}
	requireAction(t, action, schedulerNoop)
}

func TestTurnScheduler_RefusedAgent(t *testing.T) {
	scheduler, _ := newTestScheduler([]string{"mike"}, 0, Dropped)

	result, action := scheduler.Trigger("kyle")
	if result != Refused {
		t.Fatalf("refused trigger: got %v, want Refused", result)
	}
	requireAction(t, action, schedulerNoop)
}

func TestTurnScheduler_Cooldown(t *testing.T) {
	scheduler, now := newTestScheduler([]string{"mike"}, 100*time.Millisecond, Dropped)
	scheduler.Trigger("mike")
	scheduler.Complete("mike")
	*now = now.Add(25 * time.Millisecond)

	result, action := scheduler.Trigger("mike")
	if result != Cooldown {
		t.Fatalf("cooldown trigger: got %v, want Cooldown", result)
	}
	requireAction(t, action, schedulerStartAfter)
	if action.delay != 75*time.Millisecond {
		t.Fatalf("cooldown delay: got %v, want 75ms", action.delay)
	}
}

func TestTurnScheduler_QueuedAutoStart(t *testing.T) {
	scheduler, _ := newTestScheduler([]string{"mike"}, 0, Dropped)
	scheduler.Trigger("mike")
	scheduler.Trigger("mike")

	action := scheduler.Complete("mike")
	requireAction(t, action, schedulerStartNow)
}

func TestTurnScheduler_IdleCleanup(t *testing.T) {
	scheduler, _ := newTestScheduler([]string{"mike"}, 0, Dropped)
	scheduler.Trigger("mike")
	scheduler.Complete("mike")

	result, action := scheduler.Trigger("mike")
	if result != Started {
		t.Fatalf("trigger after drain: got %v, want Started", result)
	}
	requireAction(t, action, schedulerStartNow)
}

func TestTurnScheduler_IndependentAgents(t *testing.T) {
	scheduler, _ := newTestScheduler([]string{"mike", "alex"}, 0, Dropped)

	mikeResult, mikeAction := scheduler.Trigger("mike")
	alexResult, alexAction := scheduler.Trigger("alex")
	if mikeResult != Started || alexResult != Started {
		t.Fatalf("independent triggers: got mike=%v alex=%v, want Started/Started", mikeResult, alexResult)
	}
	requireAction(t, mikeAction, schedulerStartNow)
	requireAction(t, alexAction, schedulerStartNow)

	queuedResult, queuedAction := scheduler.Trigger("mike")
	if queuedResult != Queued {
		t.Fatalf("mike second trigger: got %v, want Queued", queuedResult)
	}
	requireAction(t, queuedAction, schedulerNoop)
}
