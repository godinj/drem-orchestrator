package agent

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// mockLimiter is a test double implementing dispatchLimiter.
type mockLimiter struct {
	allowResult bool
	retryAfter  time.Duration
	allowCalls  atomic.Int32
	recordCalls atomic.Int32
}

func (m *mockLimiter) Allow() (bool, time.Duration) {
	m.allowCalls.Add(1)
	return m.allowResult, m.retryAfter
}

func (m *mockLimiter) Record() {
	m.recordCalls.Add(1)
}

// TestCanSpawn_LimiterDenies verifies that CanSpawn returns false when the
// dispatch limiter denies the request, even though the semaphore has capacity.
func TestCanSpawn_LimiterDenies(t *testing.T) {
	r := &Runner{
		maxConcurrent: 4,
		running:       make(map[uuid.UUID]*RunningAgent),
		completions:   make(chan Completion, 4),
		semaphore:     make(chan struct{}, 4),
	}

	lim := &mockLimiter{allowResult: false, retryAfter: 5 * time.Second}
	r.SetDispatchLimiter(lim)

	if r.CanSpawn() {
		t.Error("CanSpawn() = true, want false when limiter denies dispatch")
	}
	if lim.allowCalls.Load() == 0 {
		t.Error("limiter.Allow() was never called by CanSpawn")
	}
}

// TestCanSpawn_LimiterAllows verifies that CanSpawn returns true when both
// the semaphore has capacity and the limiter permits the dispatch.
func TestCanSpawn_LimiterAllows(t *testing.T) {
	r := &Runner{
		maxConcurrent: 4,
		running:       make(map[uuid.UUID]*RunningAgent),
		completions:   make(chan Completion, 4),
		semaphore:     make(chan struct{}, 4),
	}

	lim := &mockLimiter{allowResult: true}
	r.SetDispatchLimiter(lim)

	if !r.CanSpawn() {
		t.Error("CanSpawn() = false, want true when limiter allows and capacity available")
	}
}

// TestCanSpawn_LimiterDenies_SemaphoreHasCapacity ensures the limiter
// takes precedence over raw semaphore capacity. Even with zero running
// agents, a denying limiter must block spawning.
func TestCanSpawn_LimiterDenies_SemaphoreHasCapacity(t *testing.T) {
	r := &Runner{
		maxConcurrent: 10,
		running:       make(map[uuid.UUID]*RunningAgent),
		completions:   make(chan Completion, 10),
		semaphore:     make(chan struct{}, 10),
	}

	lim := &mockLimiter{allowResult: false, retryAfter: 30 * time.Second}
	r.SetDispatchLimiter(lim)

	if r.CanSpawn() {
		t.Error("CanSpawn() = true, want false: limiter should override semaphore capacity")
	}
}

// TestCanSpawn_NilLimiter_BackwardCompatible verifies that when no dispatch
// limiter is set (nil), CanSpawn behaves exactly as before — gated only by
// the running map count vs maxConcurrent.
func TestCanSpawn_NilLimiter_BackwardCompatible(t *testing.T) {
	t.Run("capacity available", func(t *testing.T) {
		r := &Runner{
			maxConcurrent: 2,
			running:       make(map[uuid.UUID]*RunningAgent),
			completions:   make(chan Completion, 2),
			semaphore:     make(chan struct{}, 2),
		}
		// No limiter set — dispatchLimiter is nil.
		if !r.CanSpawn() {
			t.Error("CanSpawn() = false, want true with nil limiter and capacity available")
		}
	})

	t.Run("at capacity", func(t *testing.T) {
		r := &Runner{
			maxConcurrent: 1,
			running:       make(map[uuid.UUID]*RunningAgent),
			completions:   make(chan Completion, 1),
			semaphore:     make(chan struct{}, 1),
		}
		id := uuid.New()
		r.running[id] = &RunningAgent{AgentID: id}

		if r.CanSpawn() {
			t.Error("CanSpawn() = true, want false with nil limiter at capacity")
		}
	})
}

// TestSpawnNewAgent_RecordsDispatch verifies that a successful spawn calls
// limiter.Record() to register the dispatch in the sliding window.
func TestSpawnNewAgent_RecordsDispatch(t *testing.T) {
	db := testutil.NewTestDB(t)

	binDir := t.TempDir()
	claudeBin := writeFakeClaudeBin(t, binDir, 0)
	r := newMockRunner(t, db, claudeBin)

	lim := &mockLimiter{allowResult: true}
	r.SetDispatchLimiter(lim)

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")
	task := testutil.CreateTask(t, db, project.ID, "Rate limit task", model.StatusInProgress)

	worktreeDir := t.TempDir()
	agent, err := r.SpawnAgentInWorktree(&task, worktreeDir, model.AgentCoder, "test prompt")
	if err != nil {
		t.Fatalf("SpawnAgentInWorktree: %v", err)
	}

	if lim.recordCalls.Load() != 1 {
		t.Errorf("limiter.Record() called %d times, want 1", lim.recordCalls.Load())
	}

	// Cleanup.
	_ = r.StopAgent(agent.ID)
}

// TestSpawnNewAgent_NoRecord_OnFailure verifies that limiter.Record() is NOT
// called when the spawn fails (e.g., process start error), so the sliding
// window is not polluted by failed attempts.
func TestSpawnNewAgent_NoRecord_OnFailure(t *testing.T) {
	db := testutil.NewTestDB(t)

	starter := mockProcessStarter(0, fmt.Errorf("process start failed"))
	r := newMockRunnerWithStarter(t, db, starter)

	lim := &mockLimiter{allowResult: true}
	r.SetDispatchLimiter(lim)

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")
	task := testutil.CreateTask(t, db, project.ID, "Failing task", model.StatusInProgress)

	worktreeDir := t.TempDir()
	_, err := r.SpawnAgentInWorktree(&task, worktreeDir, model.AgentCoder, "test prompt")
	if err == nil {
		t.Fatal("expected spawn to fail")
	}

	if lim.recordCalls.Load() != 0 {
		t.Errorf("limiter.Record() called %d times on failure, want 0", lim.recordCalls.Load())
	}
}

// TestSpawnNewAgent_NilLimiter_NoRecord verifies that spawn succeeds without
// calling any limiter methods when no limiter is configured (nil).
func TestSpawnNewAgent_NilLimiter_NoRecord(t *testing.T) {
	db := testutil.NewTestDB(t)

	binDir := t.TempDir()
	claudeBin := writeFakeClaudeBin(t, binDir, 0)
	r := newMockRunner(t, db, claudeBin)
	// No limiter set — dispatchLimiter is nil.

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")
	task := testutil.CreateTask(t, db, project.ID, "No limiter task", model.StatusInProgress)

	worktreeDir := t.TempDir()
	agent, err := r.SpawnAgentInWorktree(&task, worktreeDir, model.AgentCoder, "test prompt")
	if err != nil {
		t.Fatalf("SpawnAgentInWorktree should succeed with nil limiter: %v", err)
	}

	// Cleanup.
	_ = r.StopAgent(agent.ID)
}

// TestSetDispatchLimiter_NilRemoves verifies that passing nil to
// SetDispatchLimiter clears a previously set limiter.
func TestSetDispatchLimiter_NilRemoves(t *testing.T) {
	r := &Runner{
		maxConcurrent: 2,
		running:       make(map[uuid.UUID]*RunningAgent),
		completions:   make(chan Completion, 2),
		semaphore:     make(chan struct{}, 2),
	}

	// Set a denying limiter.
	lim := &mockLimiter{allowResult: false, retryAfter: 10 * time.Second}
	r.SetDispatchLimiter(lim)
	if r.CanSpawn() {
		t.Fatal("CanSpawn() should be false with denying limiter")
	}

	// Remove the limiter.
	r.SetDispatchLimiter(nil)
	if !r.CanSpawn() {
		t.Error("CanSpawn() = false after removing limiter, want true")
	}
}
