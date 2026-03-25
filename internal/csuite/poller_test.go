package csuite_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/csuite"
)

// ---------------------------------------------------------------------------
// Fake SnapshotSource for controlling query results and injecting errors.
// ---------------------------------------------------------------------------

type fakeSource struct {
	mu                sync.Mutex
	agents            []csuite.AgentSummary
	counts            map[string]int
	agentSummariesErr error
	unreadCountsErr   error
	callCount         int
}

func (f *fakeSource) AgentSummaries() ([]csuite.AgentSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	if f.agentSummariesErr != nil {
		return nil, f.agentSummariesErr
	}
	// Return a copy to avoid races.
	out := make([]csuite.AgentSummary, len(f.agents))
	copy(out, f.agents)
	return out, nil
}

func (f *fakeSource) UnreadMessageCounts() (map[string]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unreadCountsErr != nil {
		return nil, f.unreadCountsErr
	}
	out := make(map[string]int, len(f.counts))
	for k, v := range f.counts {
		out[k] = v
	}
	return out, nil
}

func (f *fakeSource) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCount
}

func (f *fakeSource) setAgentSummariesErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agentSummariesErr = err
}

func (f *fakeSource) setUnreadCountsErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unreadCountsErr = err
}

func discardLogger() *log.Logger {
	return log.New(os.Stderr, "poller-test: ", log.LstdFlags)
}

// ---------------------------------------------------------------------------
// 1. Snapshot construction — BuildSnapshot
// ---------------------------------------------------------------------------

func TestBuildSnapshot_PopulatesAgentSummaries(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	src := &fakeSource{
		agents: []csuite.AgentSummary{
			{CsuiteAgent: csuite.CsuiteAgent{Name: "planner", Status: csuite.AgentMonOnline, HeartbeatAt: &now}, UnreadCount: 2},
			{CsuiteAgent: csuite.CsuiteAgent{Name: "coder", Status: csuite.AgentMonOffline}, UnreadCount: 0},
		},
		counts: map[string]int{"planner": 2},
	}

	p := csuite.NewPoller(src, csuite.DefaultPollInterval, discardLogger())
	snap, err := p.BuildSnapshot()
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	if len(snap.AgentSummaries) != 2 {
		t.Fatalf("got %d agent summaries, want 2", len(snap.AgentSummaries))
	}
	if snap.AgentSummaries[0].Name != "planner" {
		t.Errorf("AgentSummaries[0].Name = %q, want %q", snap.AgentSummaries[0].Name, "planner")
	}
	if snap.AgentSummaries[1].Name != "coder" {
		t.Errorf("AgentSummaries[1].Name = %q, want %q", snap.AgentSummaries[1].Name, "coder")
	}
}

func TestBuildSnapshot_PopulatesInboxCounts(t *testing.T) {
	src := &fakeSource{
		agents: []csuite.AgentSummary{},
		counts: map[string]int{"planner": 3, "coder": 1},
	}

	p := csuite.NewPoller(src, csuite.DefaultPollInterval, discardLogger())
	snap, err := p.BuildSnapshot()
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	if snap.InboxCounts["planner"] != 3 {
		t.Errorf("InboxCounts[planner] = %d, want 3", snap.InboxCounts["planner"])
	}
	if snap.InboxCounts["coder"] != 1 {
		t.Errorf("InboxCounts[coder] = %d, want 1", snap.InboxCounts["coder"])
	}
}

func TestBuildSnapshot_SetsTimestamp(t *testing.T) {
	src := &fakeSource{
		agents: []csuite.AgentSummary{},
		counts: map[string]int{},
	}

	before := time.Now()
	p := csuite.NewPoller(src, csuite.DefaultPollInterval, discardLogger())
	snap, err := p.BuildSnapshot()
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	after := time.Now()

	if snap.Timestamp.Before(before) || snap.Timestamp.After(after) {
		t.Errorf("Timestamp %v not between %v and %v", snap.Timestamp, before, after)
	}
}

func TestBuildSnapshot_EmptySource(t *testing.T) {
	src := &fakeSource{
		agents: []csuite.AgentSummary{},
		counts: map[string]int{},
	}

	p := csuite.NewPoller(src, csuite.DefaultPollInterval, discardLogger())
	snap, err := p.BuildSnapshot()
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	if len(snap.AgentSummaries) != 0 {
		t.Errorf("got %d agent summaries, want 0", len(snap.AgentSummaries))
	}
	if len(snap.InboxCounts) != 0 {
		t.Errorf("got %d inbox counts, want 0", len(snap.InboxCounts))
	}
}

func TestBuildSnapshot_AgentSummariesError(t *testing.T) {
	src := &fakeSource{
		agentSummariesErr: fmt.Errorf("db connection lost"),
		counts:            map[string]int{},
	}

	p := csuite.NewPoller(src, csuite.DefaultPollInterval, discardLogger())
	_, err := p.BuildSnapshot()
	if err == nil {
		t.Fatal("BuildSnapshot should return error when AgentSummaries fails")
	}
}

func TestBuildSnapshot_UnreadCountsError(t *testing.T) {
	src := &fakeSource{
		agents:          []csuite.AgentSummary{},
		unreadCountsErr: fmt.Errorf("db connection lost"),
	}

	p := csuite.NewPoller(src, csuite.DefaultPollInterval, discardLogger())
	_, err := p.BuildSnapshot()
	if err == nil {
		t.Fatal("BuildSnapshot should return error when UnreadMessageCounts fails")
	}
}

// ---------------------------------------------------------------------------
// 2. Channel delivery — Start sends snapshots at configured interval
// ---------------------------------------------------------------------------

func TestStart_DeliversSnapshotsOnChannel(t *testing.T) {
	src := &fakeSource{
		agents: []csuite.AgentSummary{
			{CsuiteAgent: csuite.CsuiteAgent{Name: "planner"}, UnreadCount: 1},
		},
		counts: map[string]int{"planner": 1},
	}

	p := csuite.NewPoller(src, 10*time.Millisecond, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go p.Start(ctx)

	select {
	case snap := <-p.Snapshots():
		if len(snap.AgentSummaries) != 1 {
			t.Errorf("got %d agent summaries, want 1", len(snap.AgentSummaries))
		}
		if snap.AgentSummaries[0].Name != "planner" {
			t.Errorf("agent name = %q, want %q", snap.AgentSummaries[0].Name, "planner")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for snapshot")
	}
}

func TestStart_DeliversMultipleSnapshots(t *testing.T) {
	src := &fakeSource{
		agents: []csuite.AgentSummary{},
		counts: map[string]int{},
	}

	p := csuite.NewPoller(src, 10*time.Millisecond, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go p.Start(ctx)

	// Drain at least 3 snapshots.
	for i := 0; i < 3; i++ {
		select {
		case <-p.Snapshots():
			// ok
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for snapshot %d", i+1)
		}
	}
}

func TestStart_SnapshotTimestampsIncrease(t *testing.T) {
	src := &fakeSource{
		agents: []csuite.AgentSummary{},
		counts: map[string]int{},
	}

	p := csuite.NewPoller(src, 10*time.Millisecond, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go p.Start(ctx)

	var prev time.Time
	for i := 0; i < 3; i++ {
		select {
		case snap := <-p.Snapshots():
			if !snap.Timestamp.After(prev) && i > 0 {
				t.Errorf("snapshot %d timestamp %v not after previous %v", i, snap.Timestamp, prev)
			}
			prev = snap.Timestamp
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for snapshot %d", i+1)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. Pause/Resume
// ---------------------------------------------------------------------------

func TestPause_StopsSnapshotEmission(t *testing.T) {
	src := &fakeSource{
		agents: []csuite.AgentSummary{},
		counts: map[string]int{},
	}

	p := csuite.NewPoller(src, 10*time.Millisecond, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go p.Start(ctx)

	// Wait for at least one snapshot to confirm the poller is running.
	select {
	case <-p.Snapshots():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial snapshot")
	}

	p.Pause()

	// After pausing, no snapshots should arrive within a reasonable window.
	select {
	case <-p.Snapshots():
		t.Error("received snapshot after Pause — should be paused")
	case <-time.After(100 * time.Millisecond):
		// Expected — no snapshot while paused.
	}
}

func TestResume_RestartsSnapshotEmission(t *testing.T) {
	src := &fakeSource{
		agents: []csuite.AgentSummary{},
		counts: map[string]int{},
	}

	p := csuite.NewPoller(src, 10*time.Millisecond, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go p.Start(ctx)

	// Wait for initial snapshot.
	select {
	case <-p.Snapshots():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial snapshot")
	}

	p.Pause()
	time.Sleep(50 * time.Millisecond) // Let pause take effect.
	p.Resume()

	// After resuming, snapshots should arrive again.
	select {
	case <-p.Snapshots():
		// ok — resumed successfully
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for snapshot after Resume")
	}
}

func TestPause_Idempotent(t *testing.T) {
	src := &fakeSource{
		agents: []csuite.AgentSummary{},
		counts: map[string]int{},
	}

	p := csuite.NewPoller(src, 10*time.Millisecond, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go p.Start(ctx)

	select {
	case <-p.Snapshots():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial snapshot")
	}

	// Multiple pauses should not panic or deadlock.
	p.Pause()
	p.Pause()

	select {
	case <-p.Snapshots():
		t.Error("received snapshot after double Pause")
	case <-time.After(100 * time.Millisecond):
		// ok
	}
}

func TestResume_WithoutPause_IsNoOp(t *testing.T) {
	src := &fakeSource{
		agents: []csuite.AgentSummary{},
		counts: map[string]int{},
	}

	p := csuite.NewPoller(src, 10*time.Millisecond, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go p.Start(ctx)

	// Resume without prior Pause should not panic.
	p.Resume()

	select {
	case <-p.Snapshots():
		// ok — still delivers snapshots
	case <-time.After(2 * time.Second):
		t.Fatal("timed out — Resume without Pause should not break delivery")
	}
}

// ---------------------------------------------------------------------------
// 4. Graceful shutdown
// ---------------------------------------------------------------------------

func TestStart_ContextCancelStopsPoller(t *testing.T) {
	src := &fakeSource{
		agents: []csuite.AgentSummary{},
		counts: map[string]int{},
	}

	p := csuite.NewPoller(src, 10*time.Millisecond, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		p.Start(ctx)
		close(done)
	}()

	// Wait for at least one snapshot.
	select {
	case <-p.Snapshots():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial snapshot")
	}

	cancel()

	select {
	case <-done:
		// Start returned — good.
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

func TestStart_ChannelClosedAfterShutdown(t *testing.T) {
	src := &fakeSource{
		agents: []csuite.AgentSummary{},
		counts: map[string]int{},
	}

	p := csuite.NewPoller(src, 10*time.Millisecond, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		p.Start(ctx)
		close(done)
	}()

	// Confirm running.
	select {
	case <-p.Snapshots():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial snapshot")
	}

	cancel()
	<-done

	// Channel should be closed after shutdown.
	_, ok := <-p.Snapshots()
	if ok {
		t.Error("snapshot channel should be closed after shutdown")
	}
}

// ---------------------------------------------------------------------------
// 5. Error resilience — query failures don't crash or close the channel
// ---------------------------------------------------------------------------

func TestStart_ContinuesOnQueryError(t *testing.T) {
	src := &fakeSource{
		agents:            []csuite.AgentSummary{},
		counts:            map[string]int{},
		agentSummariesErr: fmt.Errorf("transient db error"),
	}

	p := csuite.NewPoller(src, 10*time.Millisecond, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go p.Start(ctx)

	// Even with errors, the poller should keep running. Wait and then
	// clear the error — the poller should resume delivering snapshots.
	time.Sleep(50 * time.Millisecond)

	src.setAgentSummariesErr(nil)
	src.setUnreadCountsErr(nil)

	select {
	case snap := <-p.Snapshots():
		if snap.Timestamp.IsZero() {
			t.Error("snapshot has zero timestamp")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("poller did not recover after error cleared")
	}
}

func TestStart_DoesNotCloseChannelOnError(t *testing.T) {
	src := &fakeSource{
		agents:            []csuite.AgentSummary{},
		counts:            map[string]int{},
		agentSummariesErr: fmt.Errorf("connection refused"),
	}

	p := csuite.NewPoller(src, 10*time.Millisecond, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go p.Start(ctx)

	// Wait several intervals with an error active.
	time.Sleep(100 * time.Millisecond)

	// Clear the error.
	src.setAgentSummariesErr(nil)

	// Channel should still be open and delivering.
	select {
	case <-p.Snapshots():
		// ok — channel still open
	case <-time.After(2 * time.Second):
		t.Fatal("channel appears closed or stuck after transient errors")
	}
}

func TestStart_SkipsSnapshotOnPartialError(t *testing.T) {
	// When UnreadMessageCounts fails but AgentSummaries succeeds,
	// the poller should skip the snapshot (not send partial data).
	src := &fakeSource{
		agents: []csuite.AgentSummary{
			{CsuiteAgent: csuite.CsuiteAgent{Name: "planner"}, UnreadCount: 1},
		},
		counts:          map[string]int{},
		unreadCountsErr: fmt.Errorf("partial failure"),
	}

	p := csuite.NewPoller(src, 10*time.Millisecond, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go p.Start(ctx)

	// No snapshot should arrive while there's an error.
	select {
	case <-p.Snapshots():
		t.Error("should not emit snapshot when a query fails")
	case <-time.After(100 * time.Millisecond):
		// ok — skipped as expected
	}

	// Clear error, snapshots should resume.
	src.setUnreadCountsErr(nil)

	select {
	case <-p.Snapshots():
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("poller did not recover after partial error cleared")
	}
}

// ---------------------------------------------------------------------------
// 6. Backoff on repeated failures
// ---------------------------------------------------------------------------

func TestStart_BackoffOnConsecutiveFailures(t *testing.T) {
	src := &fakeSource{
		agents:            []csuite.AgentSummary{},
		counts:            map[string]int{},
		agentSummariesErr: fmt.Errorf("persistent failure"),
	}

	interval := 10 * time.Millisecond
	p := csuite.NewPoller(src, interval, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go p.Start(ctx)

	// With a 10ms interval and no backoff, ~10 calls would happen in 100ms.
	// With backoff, significantly fewer calls should occur.
	time.Sleep(200 * time.Millisecond)

	callsWithBackoff := src.calls()

	// A naive poller without backoff at 10ms interval would make ~20 calls
	// in 200ms. With exponential backoff the count should be notably lower.
	// We use a generous threshold to avoid flakiness.
	maxExpected := 15 // backoff should keep it well below 20
	if callsWithBackoff > maxExpected {
		t.Errorf("expected backoff to reduce call count below %d, got %d", maxExpected, callsWithBackoff)
	}

	// Verify at least some calls were made (poller didn't just stop).
	if callsWithBackoff < 2 {
		t.Errorf("expected at least 2 calls during backoff, got %d", callsWithBackoff)
	}
}

func TestStart_BackoffResetsOnSuccess(t *testing.T) {
	src := &fakeSource{
		agents:            []csuite.AgentSummary{},
		counts:            map[string]int{},
		agentSummariesErr: fmt.Errorf("temporary failure"),
	}

	p := csuite.NewPoller(src, 10*time.Millisecond, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go p.Start(ctx)

	// Let backoff build up.
	time.Sleep(100 * time.Millisecond)

	// Clear the error — backoff should reset.
	src.setAgentSummariesErr(nil)

	// After recovery, snapshots should arrive at the normal interval,
	// not at the backed-off interval.
	start := time.Now()
	select {
	case <-p.Snapshots():
		elapsed := time.Since(start)
		// Should arrive within a few intervals, not the backed-off delay.
		if elapsed > 500*time.Millisecond {
			t.Errorf("snapshot arrived after %v — backoff did not reset", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for snapshot after error cleared")
	}
}

// ---------------------------------------------------------------------------
// Constructor / defaults
// ---------------------------------------------------------------------------

func TestNewPoller_SetsDefaults(t *testing.T) {
	src := &fakeSource{}
	p := csuite.NewPoller(src, csuite.DefaultPollInterval, discardLogger())

	if p.Snapshots() == nil {
		t.Error("Snapshots channel should not be nil")
	}
}

func TestDefaultPollInterval_Value(t *testing.T) {
	if csuite.DefaultPollInterval != 5*time.Second {
		t.Errorf("DefaultPollInterval = %v, want 5s", csuite.DefaultPollInterval)
	}
}
