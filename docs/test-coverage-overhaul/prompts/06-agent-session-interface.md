# Agent: SessionManager Interface & Mock-Based Agent Tests

You are working on the `master` branch of drem-orchestrator, a Go TUI tool that orchestrates Claude Code agents via tmux.
Your task is to extract a `SessionManager` interface from the concrete tmux dependency in `internal/agent`, then use it to write mock-based tests for the agent lifecycle methods.

## Context

Read these before starting:
- `CLAUDE.md` (build commands, conventions)
- `docs/test-coverage-overhaul/prd-test-coverage.md` (Phase 2a and 2b sections)
- `internal/agent/runner.go` (source — 674 LOC, all methods)
- `internal/agent/runner_test.go` (existing tests)
- `internal/tmux/tmux.go` (tmux.Manager methods — understand which are used by runner.go)

The `Runner` struct currently holds `tmux *tmux.Manager` as a concrete type. The following tmux methods are called by runner.go — these form the interface:
- Session/window creation for agents
- Checking if a session is alive
- Killing a session
- Capturing pane output
- Listing sessions

Read `runner.go` carefully to identify the exact `tmux.Manager` method names and signatures used.

## Dependencies

This agent depends on Agent 05 (agent-helper-tests). The exported helper functions (`TruncateTitle`, `SanitizeSessionName`, `AgentTypeLabel`) should already exist. If not, export them yourself following the same pattern.

## Deliverables

### New files

#### 1. `internal/agent/session.go` (~30–40 LOC)

Define the `SessionManager` interface based on the actual tmux.Manager methods used in runner.go:

```go
// SessionManager abstracts tmux session operations for testing.
type SessionManager interface {
    // List the exact methods runner.go calls on r.tmux here,
    // with their actual signatures from tmux.Manager
}
```

Read `runner.go` and `tmux.go` to determine the exact method signatures. The interface should contain ONLY the methods that `runner.go` actually calls — no more, no less.

#### 2. `internal/agent/runner_mock_test.go` (~250–300 LOC)

Create a `mockSessionManager` struct that implements `SessionManager`:

```go
type mockSessionManager struct {
    // Track calls and configure return values
    createCalled    bool
    createErr       error
    aliveSessions   map[string]bool
    killedSessions  []string
    capturedOutput  map[string]string
    listedSessions  []string
    // ... fields for each interface method
}
```

**Test functions:**

```go
func TestSpawnAgentInWorktree_MockTmux(t *testing.T)
```
- Happy path: mock returns success for session creation
- Verify agent record created in DB, session name set correctly
- Verify prompt file written to worktree

```go
func TestSpawnAgentInWorktree_SessionFailure(t *testing.T)
```
- Mock returns error on session creation
- Verify error propagated, no orphaned DB record

```go
func TestStopAgent_MockTmux(t *testing.T)
```
- Set up a running agent in DB and running map
- Call StopAgent → verify mock's kill method called
- Verify DB record updated (status set to stopped/completed)

```go
func TestStopAgent_NotFound(t *testing.T)
```
- Call StopAgent with unknown agentID → verify appropriate error

```go
func TestGetAgentOutput_MockTmux(t *testing.T)
```
- Set up a running agent, configure mock to return captured output
- Verify output string returned correctly

```go
func TestGetAgentOutput_NotRunning(t *testing.T)
```
- Call GetAgentOutput for non-running agent → verify error or empty

```go
func TestCleanupStaleAgents_MockTmux(t *testing.T)
```
- Create agent records in DB with old heartbeat timestamps
- Call CleanupStaleAgents → verify stale sessions killed via mock
- Verify DB records updated

```go
func TestCleanupStaleAgents_NoneStale(t *testing.T)
```
- All agents have recent heartbeats → no kill calls

```go
func TestReapOrphanedSessions_MockTmux(t *testing.T)
```
- Mock lists sessions that have no matching DB agent records
- Call ReapOrphanedSessions → verify orphaned sessions killed
- Verify return count matches

```go
func TestReapOrphanedSessions_NoOrphans(t *testing.T)
```
- All listed sessions have matching DB records → no kills, count=0

### Modified files

#### 3. `internal/agent/runner.go`

Change the `Runner` struct to use the interface instead of the concrete type:

```go
type Runner struct {
    db            *gorm.DB
    tmux          SessionManager   // was: *tmux.Manager
    // ... rest unchanged
}
```

Update `NewRunner` signature to accept `SessionManager`:

```go
func NewRunner(db *gorm.DB, tm SessionManager, wt *worktree.Manager, claudeBin string, maxConcurrent int) *Runner
```

**Important:** Verify that all callers of `NewRunner` still compile. The `tmux.Manager` should satisfy the `SessionManager` interface, so existing callers passing `*tmux.Manager` will work if the interface methods match.

Also check if any code accesses `r.tmux` to call methods NOT on the interface (e.g., `r.TmuxManager()` accessor). If so, keep those working — the accessor can type-assert or the interface can include those methods.

## Scope Limitation

- Only modify files in `internal/agent/`
- Do NOT modify `internal/tmux/tmux.go`
- The `SessionManager` interface must be satisfied by `*tmux.Manager` without any changes to the tmux package
- All new tests must work without a real tmux binary (no `requireTmux()`)
- Use `testutil.NewTestDB(t)` for database setup

## Verification

```bash
go build ./...
go test ./...
```

All existing tests (including those requiring tmux) must still pass. New mock-based tests must pass without tmux. Coverage for `internal/agent` should reach ~40-45%.

## Conventions

- `gofmt` for formatting
- Exported types and functions have doc comments
- Table-driven tests where applicable
- `t.Helper()` on test helpers
