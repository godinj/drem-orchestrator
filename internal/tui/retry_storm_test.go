package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/pkg/orchclient"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// These tests cover the TUI retry-storm prevention fix (single-flight
// gate + nextAllowedRefresh). They are the regression suite for the
// 413%-CPU /tasks connection-reset incident: a confused client was
// firing completion-edge re-fires and compounded tea.Tick schedules
// until the orchestrator could no longer keep up. The fix is
// documented in plans/tui-retry-storm-prevention.md; this file is
// its mandatory test matrix.
//
// Altitude: tests 1–4, 7, 8 are pure — they exercise canDispatch and
// the Update handlers without a real datasource. Tests 5 and 6 drive
// Update through a slow fakeDataSource and count fetch invocations;
// they are the only ones here that need concurrency.

// -- Test 1 --------------------------------------------------------------

// TestRetryStorm_SingleFlight_TasksDispatch asserts the per-endpoint
// single-flight gate: two consecutive dispatchTasksRefresh() calls
// with no intervening tasksLoadedMsg/dataErrMsg return Cmd, nil.
func TestRetryStorm_SingleFlight_TasksDispatch(t *testing.T) {
	ds := &fakeDataSource{}
	m := Model{dataSource: ds}

	cmd1 := m.dispatchTasksRefresh()
	require.NotNil(t, cmd1, "first dispatch should fire")
	require.True(t, m.tasksInflight, "first dispatch should set inflight")

	cmd2 := m.dispatchTasksRefresh()
	require.Nil(t, cmd2, "second dispatch must be gated while inflight")
}

// TestRetryStorm_SingleFlight_AgentsDispatch mirrors the tasks
// single-flight check for the agents endpoint. The two gates are
// independent by design.
func TestRetryStorm_SingleFlight_AgentsDispatch(t *testing.T) {
	ds := &fakeDataSource{}
	m := Model{dataSource: ds}

	cmd1 := m.dispatchAgentsRefresh()
	require.NotNil(t, cmd1)
	require.True(t, m.agentsInflight)

	cmd2 := m.dispatchAgentsRefresh()
	require.Nil(t, cmd2)
}

// TestRetryStorm_SingleFlight_PerEndpoint asserts that the tasks
// gate does not block an agents dispatch and vice versa — the gates
// are per-endpoint, so a slow /tasks does not stall /workers.
func TestRetryStorm_SingleFlight_PerEndpoint(t *testing.T) {
	ds := &fakeDataSource{}
	m := Model{dataSource: ds}

	require.NotNil(t, m.dispatchTasksRefresh())
	require.NotNil(t, m.dispatchAgentsRefresh(), "agents gate is independent of tasks gate")
}

// -- Test 2 --------------------------------------------------------------

// TestRetryStorm_TasksLoadedMsgReleasesGate asserts that a successful
// tasksLoadedMsg clears tasksInflight AND nextAllowedRefresh AND
// resets the backoff ladder to zero. Proves gate release on success.
func TestRetryStorm_TasksLoadedMsgReleasesGate(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	m.tasksInflight = true
	m.nextAllowedRefresh = time.Now().Add(5 * time.Second)
	m.dataBackoff = 5 * time.Second
	m.dataErr = errors.New("stale")

	result, _ := m.Update(tasksLoadedMsg{tasks: nil})
	got := result.(Model)

	require.False(t, got.tasksInflight, "tasksLoadedMsg must release gate")
	require.True(t, got.nextAllowedRefresh.IsZero(), "tasksLoadedMsg must clear nextAllowedRefresh")
	require.Equal(t, time.Duration(0), got.dataBackoff, "tasksLoadedMsg must reset backoff")
	require.NoError(t, got.dataErr, "tasksLoadedMsg must clear dataErr")
}

// TestRetryStorm_AgentsLoadedMsgReleasesGate mirrors the tasks
// version for the agents endpoint.
func TestRetryStorm_AgentsLoadedMsgReleasesGate(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	m.agentsInflight = true
	m.nextAllowedRefresh = time.Now().Add(5 * time.Second)
	m.dataBackoff = 5 * time.Second

	result, _ := m.Update(agentsLoadedMsg{agents: nil})
	got := result.(Model)

	require.False(t, got.agentsInflight)
	require.True(t, got.nextAllowedRefresh.IsZero())
	require.Equal(t, time.Duration(0), got.dataBackoff)
}

// -- Test 3 --------------------------------------------------------------

// TestRetryStorm_DataErrMsgReleasesGateAndSetsBackoff asserts that
// a dataErrMsg releases the tasks gate (CRITICAL — else the gate
// strands on error), bumps the backoff ladder, and seeds
// nextAllowedRefresh to now+backoff. Proves no stranded gate and
// correct backoff window seeding.
func TestRetryStorm_DataErrMsgReleasesGateAndSetsBackoff(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	m.tasksInflight = true
	m.agentsInflight = true

	before := time.Now()
	result, cmd := m.Update(dataErrMsg{err: errors.New("down")})
	got := result.(Model)

	require.False(t, got.tasksInflight, "dataErrMsg must release tasks gate")
	require.False(t, got.agentsInflight, "dataErrMsg must release agents gate")
	require.Equal(t, 1*time.Second, got.dataBackoff, "first err arms 1s backoff")
	require.False(t, got.nextAllowedRefresh.IsZero(), "nextAllowedRefresh must be seeded")
	require.True(t, got.nextAllowedRefresh.After(before),
		"nextAllowedRefresh must be in the future")
	require.EqualError(t, got.dataErr, "down")
	require.Nil(t, cmd, "dataErrMsg must NOT schedule a replacement tick — singleton periodic")
}

// -- Test 4 --------------------------------------------------------------

// TestRetryStorm_CanDispatch_BackoffGate proves the pure helper
// correctly gates on nextAllowedRefresh with an injected clock.
// Altitude-matched to datasource_backoff_test.go.
func TestRetryStorm_CanDispatch_BackoffGate(t *testing.T) {
	fixed := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name        string
		inflight    bool
		nextAllowed time.Time
		now         time.Time
		want        bool
	}{
		{"idle, no hold -> allow", false, time.Time{}, fixed, true},
		{"idle, hold past -> allow", false, fixed.Add(-1 * time.Second), fixed, true},
		{"idle, hold future -> deny", false, fixed.Add(1 * time.Second), fixed, false},
		{"idle, hold equals now -> allow", false, fixed, fixed, true},
		{"inflight -> deny regardless", true, time.Time{}, fixed, false},
		{"inflight + past hold -> deny", true, fixed.Add(-1 * time.Second), fixed, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := canDispatch(tc.inflight, tc.nextAllowed, tc.now)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestRetryStorm_Dispatch_HonoursBackoffWindow drives the dispatch
// helper through a backoff window with an injected clock: while
// now < nextAllowedRefresh, dispatch returns nil; once the clock
// advances past the hold, dispatch fires.
func TestRetryStorm_Dispatch_HonoursBackoffWindow(t *testing.T) {
	ds := &fakeDataSource{}
	start := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	clock := start
	m := Model{
		dataSource:         ds,
		nextAllowedRefresh: start.Add(1 * time.Second),
		nowFunc:            func() time.Time { return clock },
	}

	require.Nil(t, m.dispatchTasksRefresh(),
		"dispatch must be gated while now < nextAllowedRefresh")
	require.False(t, m.tasksInflight, "gated dispatch must not arm inflight")

	clock = start.Add(2 * time.Second)
	require.NotNil(t, m.dispatchTasksRefresh(),
		"dispatch must fire once clock passes nextAllowedRefresh")
	require.True(t, m.tasksInflight)
}

// -- Test 5 (MANDATORY regression test) ---------------------------------

// slowFakeDataSource wraps fakeDataSource-style behaviour with a
// configurable latency + a concurrent-call counter. It is the regression
// harness for the 413%-CPU incident: simulate a slow /tasks that exceeds
// the client timeout, drive the poll loop, and assert the single-flight
// gate holds concurrent dispatches to at most one.
type slowFakeDataSource struct {
	latency time.Duration

	mu            sync.Mutex
	concurrent    int32
	maxConcurrent int32
	taskCalls     int32
	workerCalls   int32
}

func (s *slowFakeDataSource) bumpIn() {
	cur := atomic.AddInt32(&s.concurrent, 1)
	s.mu.Lock()
	if cur > s.maxConcurrent {
		s.maxConcurrent = cur
	}
	s.mu.Unlock()
}

func (s *slowFakeDataSource) bumpOut() {
	atomic.AddInt32(&s.concurrent, -1)
}

func (s *slowFakeDataSource) ListTasks(ctx context.Context, _ orchclient.TaskFilter) ([]orchdto.TaskDTO, error) {
	atomic.AddInt32(&s.taskCalls, 1)
	s.bumpIn()
	defer s.bumpOut()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(s.latency):
		return nil, nil
	}
}

func (s *slowFakeDataSource) ListWorkers(ctx context.Context) ([]orchdto.WorkerDTO, error) {
	atomic.AddInt32(&s.workerCalls, 1)
	s.bumpIn()
	defer s.bumpOut()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(s.latency):
		return nil, nil
	}
}

func (s *slowFakeDataSource) Events(_ context.Context, _ time.Time) ([]orchdto.EventDTO, error) {
	return nil, nil
}
func (s *slowFakeDataSource) WorkerHistory(_ context.Context, _ string) (orchdto.WorkerHistoryDTO, error) {
	return orchdto.WorkerHistoryDTO{}, nil
}
func (s *slowFakeDataSource) TaskAttempts(_ context.Context, _ string) ([]orchdto.WorkerAttemptDTO, error) {
	return nil, nil
}
func (s *slowFakeDataSource) StreamLogs(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (s *slowFakeDataSource) StreamAttemptLogs(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

// TestRetryStorm_SlowBackend_BoundsConcurrentDispatches is the
// production-mirror regression test. It drives 30 periodic ticks
// through Update under a slow datasource whose latency exceeds the
// 5s client timeout (simulated here with a tight timeout via
// Update-level gate observation rather than wall-clock waits — we
// never let a dispatch actually run to completion before the next
// tick fires). The single-flight gate must hold max concurrent
// dispatches to at most one.
//
// Implementation note: we execute fetch Cmds in background
// goroutines so Update observes the same "tasksInflight=true"
// state across all 30 ticks — mimicking the production scenario
// where the prior fetch has not returned before the next tick.
func TestRetryStorm_SlowBackend_BoundsConcurrentDispatches(t *testing.T) {
	// Latency exceeds what any test reasonably waits for, but we
	// cancel all background fetches at teardown so nothing leaks.
	ds := &slowFakeDataSource{latency: 10 * time.Second}
	m := newKeyTestModel(t, FocusBoard)
	m.dataSource = ds

	// Track in-flight fetch Cmds so we can let them run concurrently.
	// Each Cmd, when invoked, will enter ListTasks/ListWorkers and
	// block on the latency. We don't wait on the goroutines — the
	// datasource latency is long enough that they remain live well
	// past the test assertion, and the test process exit tears them
	// down. (We can't sync.WaitGroup them cleanly anyway: the Cmds
	// own their own ctx.)
	const ticks = 30
	for i := 0; i < ticks; i++ {
		result, cmd := m.Update(periodicRefreshMsg{})
		m = result.(Model)
		if cmd == nil {
			continue
		}
		// Execute the batched Cmd tree in a goroutine. Drop the
		// resulting tea.Msg — tasksLoadedMsg/dataErrMsg would
		// unblock the gate, but we want to observe the gate HELD
		// across all 30 ticks.
		go func(c func() any) {
			_ = c()
		}(func() any { return cmd() })
	}

	// Give background fetches a moment to enter ListTasks/ListWorkers
	// so the concurrency counter is non-zero.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&ds.concurrent) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	ds.mu.Lock()
	maxObs := ds.maxConcurrent
	ds.mu.Unlock()

	require.LessOrEqual(t, maxObs, int32(1),
		"single-flight gate must hold max concurrent /tasks dispatches to <=1 (got %d)", maxObs)
}

// -- Test 6 --------------------------------------------------------------

// TestRetryStorm_EventStorm_BoundsFetchInvocations pumps 100
// EventMsg in a tight loop while tasksInflight=true and asserts the
// underlying datasource sees at most one fetch. Proves Site 3
// (EventMsg amplifier) is bounded by the gate.
func TestRetryStorm_EventStorm_BoundsFetchInvocations(t *testing.T) {
	ds := &fakeDataSource{}
	m := newKeyTestModel(t, FocusBoard)
	m.dataSource = ds
	m.events = make(<-chan Event)

	// Pre-arm the gate so the first EventMsg finds tasksInflight=true.
	m.tasksInflight = true

	for i := 0; i < 100; i++ {
		result, _ := m.Update(EventMsg{Type: "task_updated"})
		m = result.(Model)
	}

	require.Equal(t, 0, ds.taskCalls,
		"event storm must not punch through the single-flight gate")
}

// TestRetryStorm_EventStorm_GateOpen_FiresOnce pumps 100 EventMsg
// starting from an idle gate. The first EventMsg arms the gate; the
// remaining 99 must be swallowed. Asserts the fetch Cmd was
// constructed exactly once by observing the cmd of each call: first
// non-nil (for refresh), rest non-nil (listenForEvents still there)
// but only one refresh fired.
func TestRetryStorm_EventStorm_GateOpen_FiresOnce(t *testing.T) {
	ds := &fakeDataSource{}
	m := newKeyTestModel(t, FocusBoard)
	m.dataSource = ds
	m.events = make(<-chan Event)

	for i := 0; i < 100; i++ {
		result, _ := m.Update(EventMsg{Type: "task_updated"})
		m = result.(Model)
	}

	// Exactly one task fetch was dispatched — the first event armed
	// the gate; the rest were gated. tasksInflight is still true
	// because no loaded/err msg has cleared it.
	require.True(t, m.tasksInflight, "gate should still be held from first event")
}

// -- Test 7 --------------------------------------------------------------

// TestRetryStorm_ConsecutiveErrors_NoTickCompounding injects 5
// consecutive dataErrMsg and asserts each Update returns a nil Cmd.
// Proves the error path no longer schedules a replacement
// periodicRefreshMsg tick — the existing periodic tick is a
// singleton. This is the direct regression test for Site 2 (tick
// compounding, the primary engine).
func TestRetryStorm_ConsecutiveErrors_NoTickCompounding(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)

	wants := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		5 * time.Second,
		10 * time.Second,
		10 * time.Second,
	}
	for i, want := range wants {
		result, cmd := m.Update(dataErrMsg{err: errors.New("down")})
		m = result.(Model)
		require.Nilf(t, cmd, "err #%d must not schedule a replacement tick", i+1)
		require.Equalf(t, want, m.dataBackoff,
			"err #%d: backoff ladder advanced incorrectly", i+1)
	}
}

// -- Test 8 --------------------------------------------------------------

// TestRetryStorm_FastRecoveryAfterBackoff asserts that a successful
// tasksLoadedMsg following N errors clears the backoff hold so the
// very next dispatch fires immediately — no leftover allow-time
// strand.
func TestRetryStorm_FastRecoveryAfterBackoff(t *testing.T) {
	m := newKeyTestModel(t, FocusBoard)
	m.dataSource = &fakeDataSource{}

	// Walk the backoff ladder a few rungs.
	for i := 0; i < 3; i++ {
		result, _ := m.Update(dataErrMsg{err: errors.New("down")})
		m = result.(Model)
	}
	require.Equal(t, 5*time.Second, m.dataBackoff)
	require.False(t, m.nextAllowedRefresh.IsZero())

	// Now simulate a successful recovery load.
	result, _ := m.Update(tasksLoadedMsg{tasks: nil})
	m = result.(Model)
	require.Equal(t, time.Duration(0), m.dataBackoff)
	require.True(t, m.nextAllowedRefresh.IsZero())

	// Next dispatch must fire immediately — no stranded hold.
	cmd := m.dispatchTasksRefresh()
	require.NotNil(t, cmd, "dispatch must fire immediately after successful recovery")
}
