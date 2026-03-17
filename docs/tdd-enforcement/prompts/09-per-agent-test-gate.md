# Agent: Per-Agent Test Gate at Merge Time

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent orchestrator that manages Claude Code agents via tmux in git bare repos with worktrees.
Your task is to add automated test verification before merging agent branches: run tests, retry on failure, store results, support scoped execution, and enforce compilation-only gates for test-phase subtasks.

## Context

Read these before starting:
- `docs/tdd-enforcement/prd-tdd-enforcement.md` (sections 4.5.1 through 4.5.4, 4.3.5, 4.10, 4.11)
- `internal/orchestrator/orchestrator.go` — read these sections:
  - `processAgentResult` (around line 955) — where agent completions are handled and merges are triggered
  - The merge triggering flow: follow what happens after an agent completes successfully
  - `getTestCommand` (if it exists from Agent 06) — test command discovery
- `internal/merge/merge.go` — the merge orchestrator that handles branch merging
- `cmd/drem/config.go` (Config struct — `TestCommand`, `CompileCommand`, `ScopedTests`, `TestTimeout` fields added by Agent 06)
- `internal/model/models.go` (Task struct — `Phase` field)
- `internal/worktree/git.go` (`RunGit` helper)

Key facts:
- After an agent completes, `processAgentResult` transitions the subtask and triggers a merge
- The merge pipeline is in `internal/merge/merge.go`
- The test gate should run BETWEEN agent completion and merge
- `Config.TestCommand` is set from `drem.toml`; fallback to CLAUDE.md parsing
- `Config.CompileCommand` is for test-phase compilation gates

## Dependencies

This agent depends on Agent 06 (test-writing flow). The config fields (`TestCommand`, `CompileCommand`, `ScopedTests`, `TestTimeout`) and `getTestCommand` must exist.

## Deliverables

### 1. Modify `internal/orchestrator/orchestrator.go`

**a) Add `TestResult` type:**

```go
// TestResult stores the outcome of a test run for auditing and debugging.
type TestResult struct {
    Passed       bool      `json:"passed"`
    Output       string    `json:"output"`          // truncated to last 5000 chars
    ExitCode     int       `json:"exit_code"`
    RunAt        time.Time `json:"run_at"`
    Duration     float64   `json:"duration_seconds"`
    Command      string    `json:"command"`
    Scoped       bool      `json:"scoped"`           // true if ran scoped tests
    AttemptCount int       `json:"attempt_count"`
}
```

**b) Add `verifyTestsBeforeMerge`:**

```go
// verifyTestsBeforeMerge runs the test suite in the agent's worktree and
// returns the result. Retries up to 3 times with backoff for environmental
// flakiness. Only applies to implementation and integration phase subtasks;
// test-phase subtasks use verifyCompilationBeforeMerge instead.
func (o *Orchestrator) verifyTestsBeforeMerge(subtask *model.Task, worktreePath string) (*TestResult, error)
```

Logic (§4.5.3):
1. Get the test command via `o.getTestCommand(subtask)` — returns empty string if not configured
2. If empty, log warning and return `&TestResult{Passed: true}` (degraded mode — no automated gate)
3. Determine if scoped execution applies:
   - If `o.config.ScopedTests` is true (default), derive package list from the agent's diff:
     ```
     git diff --name-only <base>...HEAD
     ```
   - Map changed files to packages (e.g., `internal/track/foo.go` → `./internal/track/...`)
   - Build scoped command: replace `./...` in test command with specific packages
   - If no packages found or diff is empty, fall back to full test command
4. Retry loop (up to 3 attempts):
   ```go
   for attempt := 1; attempt <= 3; attempt++ {
       result := o.runCommandWithTimeout(worktreePath, testCmd, o.testTimeout)
       lastResult = &TestResult{
           Passed:       result.ExitCode == 0,
           Output:       truncate(result.Output, 5000),
           ExitCode:     result.ExitCode,
           RunAt:        time.Now(),
           Duration:     result.Duration.Seconds(),
           Command:      testCmd,
           Scoped:       scoped,
           AttemptCount: attempt,
       }
       if lastResult.Passed {
           return lastResult, nil
       }
       if attempt < 3 {
           time.Sleep(time.Duration(attempt) * 2 * time.Second)
       }
   }
   return lastResult, nil
   ```
5. Store the result on the agent record: `agent.Config["last_test_result"] = lastResult`

**c) Add `verifyCompilationBeforeMerge`** (§4.3.5):

```go
// verifyCompilationBeforeMerge runs the compilation command for test-phase
// subtasks. Test execution results are ignored — only compilation matters.
func (o *Orchestrator) verifyCompilationBeforeMerge(subtask *model.Task, worktreePath string) (*TestResult, error)
```

Logic:
1. Get compile command: check `o.config.CompileCommand` first
2. If empty, infer from language:
   - If Go files in worktree: `go vet ./...`
   - If `tsconfig.json` exists: `npx tsc --noEmit`
   - If Python files: `python -m py_compile` on changed files
   - If `Cargo.toml` exists: `cargo check`
3. If no command found, skip gate (return `&TestResult{Passed: true}`)
4. Run the command (no retries needed for compilation)
5. Return result

**d) Integrate test gate into the merge flow:**

In `processAgentResult`, after the agent completes successfully but BEFORE triggering the merge, add the test gate:

```go
// Test gate: verify tests pass before merging
if subtask.Phase == "test" {
    // Test-phase: compilation-only gate
    result, err := o.verifyCompilationBeforeMerge(subtask, agent.WorktreePath)
    if err != nil {
        return fmt.Errorf("compilation gate: %w", err)
    }
    o.storeTestResult(agent, result)
    if !result.Passed {
        o.logger.Warn("compilation failed for test-phase subtask",
            "subtask_id", subtask.ID, "exit_code", result.ExitCode)
        // Don't block — the human catches quality at TEST_REVIEW
        // Just log the warning
    }
} else if subtask.Phase == "implementation" || subtask.Phase == "integration" || subtask.Phase == "" {
    // Impl/integration: full test gate
    result, err := o.verifyTestsBeforeMerge(subtask, agent.WorktreePath)
    if err != nil {
        return fmt.Errorf("test gate: %w", err)
    }
    o.storeTestResult(agent, result)
    if !result.Passed {
        o.logger.Error("tests failed, blocking merge",
            "subtask_id", subtask.ID, "attempts", result.AttemptCount)
        // Block merge — notify via event
        o.emit("test_gate_failed", map[string]any{
            "subtask_id": subtask.ID,
            "exit_code":  result.ExitCode,
            "output":     result.Output,
        })
        // Don't proceed to merge — leave subtask in current state
        // The failure recovery (Agent 10) handles escalation
        return nil
    }
}
// ... proceed to merge ...
```

**e) Add helper for scoped test execution:**

```go
// scopeTestCommand takes a base test command and a list of changed files,
// and returns a scoped command that only tests affected packages.
// Returns the original command if scoping isn't possible.
func scopeTestCommand(baseCmd string, changedFiles []string) (string, bool)
```

For Go: map each file to its package directory, deduplicate, and replace `./...` with the package list:
```
go test ./...  →  go test ./internal/track/... ./internal/render/...
```

For other languages, return the base command unmodified (scoping is Go-specific for now).

**f) Add `runCommandWithTimeout`:**

```go
// runCommandWithTimeout executes a shell command with a timeout.
// Returns the combined output, exit code, and duration.
func (o *Orchestrator) runCommandWithTimeout(dir, cmd string, timeout time.Duration) *commandResult

type commandResult struct {
    Output   string
    ExitCode int
    Duration time.Duration
}
```

Use `exec.CommandContext` with a context deadline set to the configured `test_timeout`.

**g) Add `storeTestResult` helper:**

```go
func (o *Orchestrator) storeTestResult(ag *model.Agent, result *TestResult) {
    if ag.Config == nil {
        ag.Config = make(model.JSONField)
    }
    ag.Config["last_test_result"] = result
    o.db.Model(ag).Update("config", ag.Config)
}
```

### 2. Add tests

**`internal/orchestrator/test_gate_test.go`** (new file):

- **verifyTestsBeforeMerge — tests pass first try**: Mock a passing test command → result.Passed true, AttemptCount 1
- **verifyTestsBeforeMerge — tests pass on retry**: Fail first, pass second → AttemptCount 2
- **verifyTestsBeforeMerge — all retries fail**: Three failures → result.Passed false, AttemptCount 3
- **verifyTestsBeforeMerge — no test command**: Returns passed=true (degraded mode)
- **verifyCompilationBeforeMerge — compilation passes**: Go project → runs `go vet ./...`, passes
- **verifyCompilationBeforeMerge — no compile command, unknown language**: Returns passed=true (skip)
- **scopeTestCommand — Go packages**: `go test ./...` + files in `internal/track/` and `internal/render/` → `go test ./internal/track/... ./internal/render/...`
- **scopeTestCommand — no ./... in command**: Returns original command unchanged
- **scopeTestCommand — empty file list**: Returns original command
- **Test result stored on agent**: After verifyTestsBeforeMerge, agent.Config["last_test_result"] is populated
- **Test timeout**: Command exceeding timeout is killed, result treated as failure
- **Output truncation**: Output over 5000 chars is truncated

## Scope Limitation

ONLY modify:
- `internal/orchestrator/orchestrator.go`
- New test files in `internal/orchestrator/`

Do NOT modify: `internal/model/`, `internal/state/`, `internal/prompt/`, `internal/tui/`, `internal/merge/`, `cmd/drem/config.go`.

## Conventions

- Go 1.22+ with standard library
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./...`
