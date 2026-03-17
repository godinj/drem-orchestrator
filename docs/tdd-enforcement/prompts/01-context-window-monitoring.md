# Agent: Context Window Monitoring

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent orchestrator that manages Claude Code agents via tmux in git bare repos with worktrees.
Your task is to add context window monitoring so the orchestrator can track each agent's token usage and intervene before an agent exhausts its context.

## Context

Read these before starting:
- `docs/tdd-enforcement/research-context-window-proxy.md` (background research)
- `internal/agent/runner.go` (agent lifecycle — `startAgent`, `RunningAgent`, `GetRunningAgents`, settings.json construction at line 278)
- `internal/orchestrator/orchestrator.go` (`doTick` loop steps, `Orchestrator` struct, `New` constructor)
- `cmd/drem/config.go` (`Config` struct, `DefaultConfig`)
- `internal/model/models.go` (`Agent` struct — note the existing `Config JSONField` on the Agent model)

Key architectural facts:
- `startAgent()` already writes `.claude/settings.json` with a `Notification` hook for idle detection
- `startAgent()` launches three goroutines: `monitorAgent`, `heartbeatLoop`, `verifySpawn`
- The orchestrator's `doTick()` runs every 5 seconds with 8 numbered steps
- The `Agent` model has a `Config JSONField` column that can store arbitrary JSON (already exists, no migration needed)
- `RunningAgent` is an in-memory struct tracking active agents
- The runner exposes `GetRunningAgents()` and `StopAgent()`

## Design

### Data source: Claude Code status line script

Claude Code passes context window data to status line scripts via stdin JSON:

```json
{
  "context_window": {
    "total_input_tokens": 15234,
    "total_output_tokens": 4521,
    "context_window_size": 200000,
    "used_percentage": 8,
    "remaining_percentage": 92,
    "current_usage": {
      "input_tokens": 8500,
      "output_tokens": 1200,
      "cache_creation_input_tokens": 5000,
      "cache_read_input_tokens": 2000
    }
  },
  "cost": {
    "total_cost_usd": 0.01234,
    "total_duration_ms": 45000,
    "total_api_duration_ms": 2300
  }
}
```

A small shell script reads this from stdin and writes it atomically to a JSON file. The orchestrator polls this file.

### Failsafe: PreCompact hook

Claude Code fires a `PreCompact` hook before auto-compaction (meaning the context is full). This is a backup signal — if the status line script fails, this still fires.

## Deliverables

### 1. New package: `internal/ctxmon/ctxmon.go`

Context monitoring types and file I/O.

```go
package ctxmon

// Usage represents the current context window state for an agent.
type Usage struct {
    TotalInputTokens    int       `json:"total_input_tokens"`
    TotalOutputTokens   int       `json:"total_output_tokens"`
    ContextWindowSize   int       `json:"context_window_size"`
    UsedPercent         int       `json:"used_percentage"`
    RemainingPercent    int       `json:"remaining_percentage"`
    TotalCostUSD        float64   `json:"total_cost_usd"`
    CompactionTriggered bool      `json:"compaction_triggered"`
    LastUpdated         time.Time `json:"-"`
}
```

Implement:

- `ReadUsageFile(path string) (*Usage, error)` — reads and parses the context usage JSON file. Returns `nil, nil` if the file does not exist yet (agent hasn't made its first API call). Returns an error only on I/O errors or corrupt JSON. Sets `LastUpdated` to `time.Now()` on successful read.
- `UsageFilePath(worktreePath string) string` — returns the canonical path: `<worktreePath>/.claude/context-usage.json`
- `CompactionSignalPath(worktreePath string) string` — returns: `<worktreePath>/.claude/compaction-triggered`

### 2. New file: `internal/ctxmon/script.go`

Generate the shell script and settings fragments.

```go
// StatusScript returns the content of a shell script that reads Claude Code's
// status line JSON from stdin and writes the context-usage.json file atomically.
// outputPath is the absolute path to the output JSON file.
func StatusScript(outputPath string) string
```

The script must:
- Read all of stdin (Claude Code passes JSON)
- Use `jq` to extract the `context_window` and `cost` fields into a flat JSON object matching the `Usage` struct
- Write to a `.tmp` file first, then `mv` to the final path (atomic on POSIX)
- Be executable (`chmod +x`)
- Handle the case where `jq` is not installed: fall back to writing the raw stdin JSON as-is (the Go reader can handle either format)

The output JSON should look like:
```json
{
  "total_input_tokens": 15234,
  "total_output_tokens": 4521,
  "context_window_size": 200000,
  "used_percentage": 8,
  "remaining_percentage": 92,
  "total_cost_usd": 0.01234
}
```

Also implement:

```go
// HooksJSON returns the PreCompact hook configuration as a map suitable for
// merging into the agent's settings.json hooks object. The hook writes a
// signal file when auto-compaction triggers.
func HooksJSON(signalPath string) map[string]any
```

This returns a structure like:
```json
{
  "PreCompact": [{
    "hooks": [{
      "type": "command",
      "command": "touch /path/to/compaction-triggered",
      "timeout": 5
    }]
  }]
}
```

### 3. New file: `internal/ctxmon/ctxmon_test.go`

Table-driven tests:

- **ReadUsageFile — valid JSON**: Write a well-formed usage JSON file, read it, verify all fields parse correctly.
- **ReadUsageFile — file not found**: Call with a nonexistent path, verify `nil, nil` return.
- **ReadUsageFile — corrupt JSON**: Write invalid JSON, verify error is returned.
- **ReadUsageFile — partial/empty file**: Write an empty file, verify error handling.
- **StatusScript**: Generate a script, verify it contains the output path, is valid shell syntax. If `jq` is available on the test machine, pipe sample JSON through the script and verify the output file matches expected structure.
- **UsageFilePath / CompactionSignalPath**: Verify path construction.

### 4. Modify `internal/agent/runner.go` — extend startAgent

In `startAgent()`, after the existing `.claude` directory creation and before the settings.json write:

**a) Write the status line script:**
```go
statusScriptPath := filepath.Join(claudeDir, "context-status.sh")
statusOutputPath := ctxmon.UsageFilePath(worktreePath)
scriptContent := ctxmon.StatusScript(statusOutputPath)
os.WriteFile(statusScriptPath, []byte(scriptContent), 0o755)
```

**b) Extend the settings.json to include both the status line script and the PreCompact hook:**

The current settings.json only has the `Notification` hook. Extend it to also include:
- `"statusLineScript": "/path/to/.claude/context-status.sh"` (top-level field)
- A `PreCompact` entry in the `hooks` object

The resulting settings.json should look like:
```json
{
    "statusLineScript": "/path/to/.claude/context-status.sh",
    "hooks": {
        "Notification": [... existing idle hook ...],
        "PreCompact": [... compaction signal hook ...]
    }
}
```

Build the settings as a `map[string]any` and marshal with `json.Marshal` instead of the current `fmt.Sprintf` approach — this is cleaner and avoids JSON escaping issues.

**c) Add `ContextUsage` to `RunningAgent`:**
```go
type RunningAgent struct {
    // ... existing fields ...
    ContextUsage *ctxmon.Usage // latest context window usage; nil until first read
}
```

Update `GetRunningAgents()` to copy the `ContextUsage` field.

**d) Add `contextMonitorLoop` goroutine:**

```go
func (r *Runner) contextMonitorLoop(ctx context.Context, agentID uuid.UUID, worktreePath string)
```

- Tick every 5 seconds
- Call `ctxmon.ReadUsageFile()` on the usage file path
- Check for the compaction signal file; if it exists, set `usage.CompactionTriggered = true` and remove the signal file
- If no usage file exists but compaction signal exists, create a synthetic `Usage{UsedPercent: 100, CompactionTriggered: true}`
- Update `r.running[agentID].ContextUsage` under the mutex
- Update the agent's DB `Config` JSONField with `context_used_pct`, `context_window_size`, `total_cost_usd`

Launch this goroutine in `startAgent()` alongside the existing three:
```go
go r.contextMonitorLoop(ctx, agentID, worktreePath)
```

**e) Add `GetContextUsage` method:**
```go
// GetContextUsage returns the latest context window usage for a running agent.
// Returns nil if the agent is not running or has no usage data yet.
func (r *Runner) GetContextUsage(agentID uuid.UUID) *ctxmon.Usage
```

### 5. Modify `cmd/drem/config.go` — add threshold config

Add two fields to `Config`:
```go
ContextWarnPercent int           `toml:"context_warn_percent"`
ContextStopPercent int           `toml:"context_stop_percent"`
```

Defaults: `ContextWarnPercent: 75`, `ContextStopPercent: 90`.

### 6. Modify `internal/orchestrator/orchestrator.go` — context window enforcement

**a) Add fields to `Orchestrator` struct:**
```go
contextWarnPct int
contextStopPct int
```

**b) Extend `New()` to accept and store these values.**

**c) Add a new method:**
```go
// checkContextUsage inspects context window usage for all running agents and
// takes action at configured thresholds.
func (o *Orchestrator) checkContextUsage()
```

Logic:
- Call `o.runner.GetRunningAgents()`
- For each agent, check `ContextUsage`
- If `UsedPercent >= contextStopPct` OR `CompactionTriggered`:
  - Log a warning with agent_id, used_pct, threshold
  - Call `o.runner.StopAgent(agentID)`
  - Find the agent's task, transition it to FAILED with reason: `"agent exhausted context window (N% used, threshold M%)"` or `"agent triggered auto-compaction"`
  - Emit event `"context_window_exceeded"`
- Else if `UsedPercent >= contextWarnPct`:
  - Log info with agent_id, used_pct
  - Emit event `"context_window_warning"` (the TUI can display this)

**d) Call `o.checkContextUsage()` in `doTick()` between step 7 (stale cleanup) and step 8 (periodic reconciliation):**
```go
// 7b. Check context window usage for running agents.
o.checkContextUsage()
```

### 7. Modify `cmd/drem/main.go` — wire config through

Pass the two new config values to `orchestrator.New()`. Find where `New()` is called and add the threshold parameters.

## Scope Limitation

ONLY create or modify these files:
- `internal/ctxmon/ctxmon.go` (NEW)
- `internal/ctxmon/script.go` (NEW)
- `internal/ctxmon/ctxmon_test.go` (NEW)
- `internal/agent/runner.go` (MODIFY)
- `internal/orchestrator/orchestrator.go` (MODIFY)
- `cmd/drem/config.go` (MODIFY)
- `cmd/drem/main.go` (MODIFY)

Do NOT modify: `internal/model/`, `internal/tui/`, `internal/tmux/`, `internal/merge/`, `internal/prompt/`, `internal/db/`.

## Conventions

- Go 1.22+ with standard library
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./...`
