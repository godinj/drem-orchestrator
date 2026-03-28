package watcher_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/internal/watcher"
)

// runnerMockRunner is a controllable CommandRunner for Runner tests.
// It records call counts and returns canned output.
type runnerMockRunner struct {
	output   string
	exitCode int
}

func (r *runnerMockRunner) Run(_ context.Context, _ string) ([]byte, int, error) {
	return []byte(r.output), r.exitCode, nil
}

// runnerCfg returns a RunnerConfig suitable for tests with short intervals.
func runnerCfg(t *testing.T) watcher.RunnerConfig {
	t.Helper()
	return watcher.RunnerConfig{
		AllowedAgents:     []string{"mike", "alex", "ross", "seth"},
		InboxBaseDir:      t.TempDir(),
		InboxPollInterval: 10 * time.Millisecond,
		SafetyInterval:    100 * time.Millisecond,
		HeartbeatInterval: 15 * time.Millisecond,
	}
}

// TestRunner_StartsAndStops verifies that Run returns cleanly when the context
// is cancelled without any trigger activity.
func TestRunner_StartsAndStops(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{}, &watcher.Heartbeat{})
	mock := &runnerMockRunner{output: claudeJSON(10, 5)}
	cfg := runnerCfg(t)

	r := watcher.NewRunner(cfg, db, mock)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx)
	}()

	// Give the loop time to start all components.
	time.Sleep(50 * time.Millisecond)

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s after context cancellation")
	}
}

// TestRunner_HeartbeatWritten verifies that the runner writes heartbeat rows
// to the database while running.
func TestRunner_HeartbeatWritten(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{}, &watcher.Heartbeat{})
	mock := &runnerMockRunner{output: claudeJSON(10, 5)}
	cfg := runnerCfg(t)

	r := watcher.NewRunner(cfg, db, mock)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx)
	}()

	// Wait for several heartbeat intervals.
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	var count int64
	if err := db.Model(&watcher.Heartbeat{}).Count(&count).Error; err != nil {
		t.Fatalf("count heartbeats: %v", err)
	}
	if count < 2 {
		t.Fatalf("expected at least 2 heartbeats, got %d", count)
	}
}

// TestRunner_InboxTriggerRoutesToLifecycle verifies the full path: a .signal
// file placed in an agent inbox directory causes the lifecycle manager to
// receive a TriggerAgent call (observable via turn_metrics row).
func TestRunner_InboxTriggerRoutesToLifecycle(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{}, &watcher.Heartbeat{})
	mock := &runnerMockRunner{output: claudeJSON(100, 50)}
	cfg := runnerCfg(t)

	// Create an inbox directory for mike.
	inbox := mkInbox(t, cfg.InboxBaseDir, "mike")

	r := watcher.NewRunner(cfg, db, mock)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx)
	}()

	// Give the loop time to start.
	time.Sleep(50 * time.Millisecond)

	// Write a signal file.
	writeSignal(t, inbox)

	// Wait for the turn to complete and metrics to be recorded.
	waitUntil(t, func() bool {
		var count int64
		db.Model(&watcher.TurnMetric{}).Where("agent = ?", "mike").Count(&count)
		return count >= 1
	}, 2*time.Second)

	cancel()
	<-done
}

// TestRunner_EventDeliveryRoutesToLifecycle verifies that pushing agent names
// onto the Notifications channel causes the lifecycle manager to trigger turns.
func TestRunner_EventDeliveryRoutesToLifecycle(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{}, &watcher.Heartbeat{})
	mock := &runnerMockRunner{output: claudeJSON(100, 50)}
	cfg := runnerCfg(t)

	r := watcher.NewRunner(cfg, db, mock)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx)
	}()

	// Give the loop time to start.
	time.Sleep(50 * time.Millisecond)

	// Push a notification for alex.
	r.Notifications() <- []string{"alex"}

	// Wait for the turn to complete.
	waitUntil(t, func() bool {
		var count int64
		db.Model(&watcher.TurnMetric{}).Where("agent = ?", "alex").Count(&count)
		return count >= 1
	}, 2*time.Second)

	cancel()
	<-done
}

// TestRunner_SafetyTimerDisabled verifies that the safety timer no longer
// wakes mike — no turn_metrics rows are produced from the timer alone.
func TestRunner_SafetyTimerDisabled(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{}, &watcher.Heartbeat{})
	mock := &runnerMockRunner{output: claudeJSON(10, 5)}
	cfg := runnerCfg(t)
	cfg.SafetyInterval = 20 * time.Millisecond

	r := watcher.NewRunner(cfg, db, mock)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx)
	}()

	// Wait long enough that the old timer would have fired several times.
	time.Sleep(200 * time.Millisecond)

	var count int64
	db.Model(&watcher.TurnMetric{}).Where("agent = ?", "mike").Count(&count)
	if count != 0 {
		t.Fatalf("safety timer should be disabled, but got %d turn_metrics for mike", count)
	}

	cancel()
	<-done
}

// TestRunner_NoInboxBaseDir verifies that the runner starts cleanly even when
// InboxBaseDir is empty (inbox trigger is skipped).
func TestRunner_NoInboxBaseDir(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{}, &watcher.Heartbeat{})
	mock := &runnerMockRunner{output: claudeJSON(10, 5)}
	cfg := runnerCfg(t)
	cfg.InboxBaseDir = "" // no inbox trigger

	r := watcher.NewRunner(cfg, db, mock)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx)
	}()

	// Just verify it starts and stops cleanly.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s")
	}
}

// TestRunner_RefusedAgentNoMetrics verifies that pushing a non-allowed agent
// onto the notifications channel does not produce a turn_metrics row.
func TestRunner_RefusedAgentNoMetrics(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{}, &watcher.Heartbeat{})
	mock := &runnerMockRunner{output: claudeJSON(10, 5)}
	cfg := runnerCfg(t)
	cfg.AllowedAgents = []string{"mike"}
	cfg.SafetyInterval = time.Hour // disable safety timer for this test

	r := watcher.NewRunner(cfg, db, mock)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	r.Notifications() <- []string{"kyle"}

	// Use a sentinel notification to confirm the kyle one was processed.
	r.Notifications() <- []string{"mike"}
	waitUntil(t, func() bool {
		var count int64
		db.Model(&watcher.TurnMetric{}).Where("agent = ?", "mike").Count(&count)
		return count >= 1
	}, 2*time.Second)

	// kyle must not have any turn_metrics row.
	var kyleCount int64
	db.Model(&watcher.TurnMetric{}).Where("agent = ?", "kyle").Count(&kyleCount)
	if kyleCount != 0 {
		t.Fatalf("expected 0 turn_metrics for kyle, got %d", kyleCount)
	}

	cancel()
	<-done
}

// TestRunner_SignalFileCleanedUp verifies that after the inbox trigger detects
// a .signal file and routes it to the lifecycle manager, the signal file is
// removed from disk.
func TestRunner_SignalFileCleanedUp(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{}, &watcher.Heartbeat{})
	mock := &runnerMockRunner{output: claudeJSON(10, 5)}
	cfg := runnerCfg(t)

	inbox := mkInbox(t, cfg.InboxBaseDir, "alex")
	signalPath := writeSignal(t, inbox)

	r := watcher.NewRunner(cfg, db, mock)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx)
	}()

	// Wait for the signal to be detected and cleaned up.
	waitUntil(t, func() bool {
		_, err := os.Stat(signalPath)
		return os.IsNotExist(err)
	}, 2*time.Second)

	cancel()
	<-done
}
