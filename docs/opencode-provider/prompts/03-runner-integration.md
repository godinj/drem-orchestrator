# Agent: Runner Integration — Provider Dispatch

You are working on the `master` branch of drem-orchestrator, a Go-based orchestrator that spawns Claude Code agents as subprocesses. Your task is wiring provider-aware dispatch through the runner, exit info enrichment, context monitoring, and main.go.

## Context

Read these specs before starting:
- `opencode-plan.md` (sections: Changes 5–7, 9 — Runner, Exit info, Context monitoring, Wiring)
- `internal/agent/runner.go` (current `Runner` struct, `NewRunner`, `startAgent`, `monitorAgent`)
- `internal/agent/hook.go` (current `enrichCompletion` — add OpenCode equivalent)
- `internal/agent/monitor.go` (current `contextMonitorLoop` — add provider branching)
- `internal/agent/process.go` (`StartOpenCodeProcess` — added by Agent 02, if not present create a stub)
- `internal/model/agentconfig.go` (`ProviderType`, `EffectiveProvider()` — added by Agent 01, if not present create a stub)
- `cmd/drem/main.go` (line ~189: `NewRunner` call — wire `OpenCodeBin`)

## Dependencies

This agent depends on Agent 01 (Model & Config) and Agent 02 (OpenCode Process). If `ProviderType`/`EffectiveProvider()` don't exist in `internal/model/agentconfig.go`, add these stubs:

```go
type ProviderType string
const (
    ProviderClaude   ProviderType = "claude"
    ProviderOpenCode ProviderType = "opencode"
)
func (c AgentCLIConfig) EffectiveProvider() ProviderType {
    if c.Provider == "" { return ProviderClaude }
    return c.Provider
}
```

If `StartOpenCodeProcess` doesn't exist in `internal/agent/process.go`, add a stub that returns `nil, fmt.Errorf("StartOpenCodeProcess: not yet implemented")`.

## Deliverables

### Modified files

#### 1. `internal/agent/runner.go`

**Runner struct changes:**
- Add `openCodeBin string` field to `Runner`

**NewRunner changes:**
- Add `openCodeBin string` parameter after `claudeBin`
- Store it in the Runner struct
- Updated signature: `func NewRunner(db *gorm.DB, tm *tmux.Manager, wt *worktree.Manager, claudeBin, openCodeBin string, maxConcurrent int, agentConfigs func(model.AgentType) model.AgentCLIConfig) *Runner`

**RunningAgent changes:**
- Add `Provider model.ProviderType` field to `RunningAgent`

**startAgent refactor:**
Split `startAgent` into provider dispatch. The current `startAgent` method becomes the Claude path. Add an OpenCode path.

- At the top of `startAgent`, resolve `cliConfig := r.agentConfigs(agentType)` and check `cliConfig.EffectiveProvider()`
- If `ProviderClaude`: execute existing body (settings.json, hooks, status scripts, exit-log script, `StartAgentProcess`)
- If `ProviderOpenCode`: call new `startOpenCodeAgent` method

**New `startOpenCodeAgent` method:**
```go
func (r *Runner) startOpenCodeAgent(agentID, taskID uuid.UUID, worktreePath, branch, sessionName, prompt string, agentType model.AgentType) error
```

Implementation:
1. Create `.opencode` directory in worktreePath
2. Write prompt to `.opencode/agent-prompt.md` (for reference/debugging)
3. Write agent metadata JSON to `.opencode/agent-metadata.json` (reuse `writeAgentMetadata` but target `.opencode` dir)
4. Remove stale idle signal: `os.Remove(filepath.Join(worktreePath, ".opencode", "agent-idle"))` — ignore error
5. Call `StartOpenCodeProcess(ctx, r.openCodeBin, promptPath, worktreePath, cliConfig.CLIArgs())`
6. Create `RunningAgent` with `Provider: model.ProviderOpenCode` set
7. Store in `r.running` map
8. Launch `monitorAgent`, `heartbeatLoop`, `contextMonitorLoop` goroutines — same as Claude path

**What OpenCode agents skip** (do NOT include in `startOpenCodeAgent`):
- `.claude/settings.json`
- Claude hooks (Notification, PreCompact, Stop)
- Context status line script (`context-status.sh`)
- Exit-log hook script (`exit-log.sh`)

**spawnNewAgent changes:**
- After resolving `cliConfig`, set `agent.Provider = string(cliConfig.EffectiveProvider())` before `r.db.Create(agent)` — this populates the new DB column from Agent 01

#### 2. `internal/agent/hook.go`

Add `enrichOpenCodeCompletion` function.

```go
func enrichOpenCodeCompletion(comp *Completion, logPath string)
```

Implementation:
1. Read the JSONL file at `logPath` (this is `.opencode/agent-output.jsonl`)
2. Scan lines in reverse order looking for the last `step_finish` event
3. Parse the JSON line — look for `"type": "step_finish"` 
4. Extract from the `part` object:
   - `part.reason` → `ExitInfo.ExitReason` (e.g., "stop")
   - `part.tokens.input` → could store in ExitInfo but currently ExitInfo doesn't have token fields, so just set ExitReason
5. Set `comp.ExitInfo` with the extracted data
6. If no `step_finish` found or file doesn't exist, return without setting ExitInfo (same graceful behavior as `enrichCompletion`)

JSON structure to parse (from the plan):
```json
{
  "type": "step_finish",
  "part": {
    "reason": "stop",
    "tokens": {"total": 12103, "input": 12100, "output": 3, "reasoning": 0}
  }
}
```

Use a simple struct for unmarshaling:
```go
type openCodeEvent struct {
    Type string `json:"type"`
    Part struct {
        Reason string `json:"reason"`
        Tokens struct {
            Total     int `json:"total"`
            Input     int `json:"input"`
            Output    int `json:"output"`
            Reasoning int `json:"reasoning"`
        } `json:"tokens"`
    } `json:"part"`
}
```

#### 3. `internal/agent/monitor.go`

**contextMonitorLoop changes:**

At the start of the loop body (after the ticker fires), check the provider of the running agent:

```go
r.mu.Lock()
ra, ok := r.running[agentID]
var provider model.ProviderType
if ok {
    provider = ra.Provider
}
r.mu.Unlock()
if !ok {
    return
}
```

Then branch:

- **Claude path** (default, `provider != model.ProviderOpenCode`): existing code — reads usage file, transcript fallback, compaction signal
- **OpenCode path** (`provider == model.ProviderOpenCode`): 
  1. Read `.opencode/agent-output.jsonl` and scan for `step_finish` events
  2. Accumulate token counts from all `step_finish` events into a `ctxmon.Usage` struct:
     - `TotalInputTokens` = sum of all `part.tokens.input`
     - `TotalOutputTokens` = sum of all `part.tokens.output`
     - `UsedPercent` = 0 (no context window tracking for OpenCode yet)
     - `TotalCostUSD` = 0 (local model, no cost)
  3. Skip compaction detection entirely (no equivalent for OpenCode)
  4. Activity monitoring (`actMon.Scan()`) runs unchanged for both providers — it scans git state, not Claude-specific output

**monitorAgent changes:**

In the `done:` label section, branch enrichment by provider:

```go
r.mu.Lock()
ra, raOk := r.running[agentID]
var provider model.ProviderType
if raOk {
    provider = ra.Provider
}
r.mu.Unlock()

comp := Completion{AgentID: agentID, ReturnCode: exitCode}
if provider == model.ProviderOpenCode {
    logPath := filepath.Join(worktreePath, ".opencode", "agent-output.jsonl")
    enrichOpenCodeCompletion(&comp, logPath)
} else {
    homeDir, _ := os.UserHomeDir()
    if homeDir != "" {
        projectDir := filepath.Join(homeDir, ".claude", "projects", ctxmon.ProjectDirName(worktreePath))
        enrichCompletion(&comp, projectDir)
    }
}
```

**Important:** The `monitorAgent` function currently doesn't know the provider. You need to look it up from `r.running[agentID]` at the `done:` label, similar to the pattern above.

Also update `monitorAgent` idle signal path — for OpenCode agents, the idle signal would be at `.opencode/agent-idle` instead of `.claude/agent-idle`. However, OpenCode has no idle detection mechanism (deferred per the plan), so for now just use the `.claude/agent-idle` path for Claude agents only. For OpenCode agents, skip the idle signal polling entirely — just wait for process exit.

#### 4. `cmd/drem/main.go`

Update the `NewRunner` call (currently at line ~189):

```go
// Before:
runner := agent.NewRunner(database, tmux, wt, cfg.ClaudeBin, cfg.MaxConcurrentAgents, cfg.Agents.ForAgentType)

// After:
runner := agent.NewRunner(database, tmux, wt, cfg.ClaudeBin, cfg.OpenCodeBin, cfg.MaxConcurrentAgents, cfg.Agents.ForAgentType)
```

#### 5. `internal/agent/runner.go` — GetAgentOutput update

Update `GetAgentOutput` to check provider for the log path:

For agents not in the running map (finished agents), check `agent.Provider`:
- If `"opencode"`: use `.opencode/agent-output.jsonl`
- Otherwise (empty or `"claude"`): use `.claude/agent-output.log` (existing behavior)

## Scope Limitation

Do NOT modify `agentconfig.go`, `config.go`, `config_profiles.go`, or `models.go` — those are owned by Agent 01. Do NOT modify `StartAgentProcess` or add `StartOpenCodeProcess` — that's owned by Agent 02 (use it if it exists, stub if not).

## Conventions

- Package: `agent` (for runner/hook/monitor), `main` (for cmd/drem)
- Error wrapping: `fmt.Errorf("start opencode agent: <step>: %w", err)`
- Log with `slog.Info`/`slog.Warn` matching existing patterns
- Build verification: `cd /home/godinj/git/drem-orchestrator.git/master && go vet ./... && go test ./...`
