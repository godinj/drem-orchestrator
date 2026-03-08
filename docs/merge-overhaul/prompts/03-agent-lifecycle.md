# Agent: Agent Lifecycle Hardening

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent orchestrator that manages Claude Code agents via tmux in git bare repos with worktrees.
Your task is to harden the agent lifecycle in the runner: add spawn verification to detect immediately-dead agents, and fix prompt delivery to avoid shell truncation.

## Context

Read these before starting:
- `docs/merge-overhaul/prd-merge-reliability.md` (sections 4.8.1, 4.9)
- `internal/agent/runner.go` (SpawnAgent, Spawn, startAgent, monitorAgent, buildAgentNames, RunningAgent struct, Completion struct)
- `internal/tmux/manager.go` (tmux session management — understand how sessions are created and checked)

Key details about the current implementation:
- `SpawnAgent()` creates a worktree, DB record, writes the prompt, creates a tmux session, and starts monitor/heartbeat goroutines
- `startAgent()` is the common setup: writes `.claude/agent-prompt.md`, writes `settings.json` (with idle_prompt hook), creates tmux session, launches monitor goroutine
- The prompt is currently delivered via: `claude --dangerously-skip-permissions "$(cat agent-prompt.md)"` — shell command substitution can truncate on special characters or large prompts
- `monitorAgent()` watches for an idle signal file (`.claude/agent-idle`) and sends a `Completion` when detected
- The runner tracks running agents in a `running` map keyed by agent UUID

## Deliverables

### 1. Spawn Verification (`internal/agent/runner.go`)

After spawning an agent, verify the tmux session is actually alive after a short delay. This catches cases where the session fails to start or dies immediately.

Add a new method:

```go
// verifySpawn checks that the agent's tmux session is alive after a
// short delay. If the session doesn't exist, it sends a failure
// completion so the orchestrator can handle the dead agent.
func (r *Runner) verifySpawn(agentID uuid.UUID, sessionName string, delay time.Duration)
```

Implementation:
1. `time.Sleep(delay)` (use 10 seconds)
2. Lock `r.mu`, check if agentID is still in `r.running`. If not, return (already completed or stopped)
3. Unlock `r.mu`
4. Check if the tmux session is alive — use `r.tmux` to verify the session exists
5. If not alive:
   - Log an error: "agent failed spawn verification"
   - Send `Completion{AgentID: agentID, ReturnCode: 1}` to `r.completions`

Call `go r.verifySpawn(agentID, sessionName, 10*time.Second)` at the end of `startAgent()`, after the monitor goroutine is launched.

### 2. Prompt Delivery Fix (`internal/agent/runner.go`)

The current prompt delivery uses shell command substitution:
```
claude --dangerously-skip-permissions "$(cat agent-prompt.md)"
```

This is fragile: `$(cat ...)` can truncate if the prompt contains shell special characters, null bytes, or exceeds `ARG_MAX`.

Change to pipe-based delivery:
```
cat <prompt-path> | claude --dangerously-skip-permissions -p -
```

If `claude` doesn't support `-p -` for stdin, use `--prompt-file`:
```
claude --dangerously-skip-permissions --prompt-file <prompt-path>
```

Find the line in `startAgent()` where the Claude command string is built and update it. The prompt file path is already being written — you just need to change how it's passed to Claude.

### 3. Prompt Write Verification (`internal/agent/runner.go`)

After writing the prompt file in `startAgent()`, verify the write was complete:

```go
// After writing prompt file
written, err := os.ReadFile(promptPath)
if err != nil || len(written) != len(prompt) {
    return fmt.Errorf("prompt write verification failed: wrote %d of %d bytes",
        len(written), len(prompt))
}
```

Add this right after the existing `os.WriteFile` call for the prompt.

### 4. Log Prompt Size on Spawn (`internal/agent/runner.go`)

In `SpawnAgent()`, after the agent is successfully started, log the prompt byte count:

```go
slog.Info("agent spawned",
    "agent_id", agent.ID,
    "task", task.Title,
    "agent_type", agentType,
    "prompt_bytes", len(prompt),
)
```

If there's already a spawn log line, add `prompt_bytes` to it rather than creating a new one.

### 5. Tests

Add tests for:

- **verifySpawn — session alive**: Mock/stub tmux to report session alive. Verify no completion is sent.
- **verifySpawn — session dead**: Mock/stub tmux to report session dead. Verify a `Completion` with `ReturnCode: 1` is sent to the completions channel.
- **verifySpawn — already completed**: Remove the agent from the running map before verify runs. Verify no completion is sent (no double-complete).
- **Prompt write verification**: Write a prompt, verify readback matches. Write to a read-only path, verify error is returned.

## Scope Limitation

ONLY modify files in `internal/agent/`. Do NOT touch `internal/orchestrator/`, `internal/worktree/`, `internal/merge/`, or `internal/prompt/`. The orchestrator-side stuck agent detection is handled by a separate agent.

## Conventions

- Go 1.22+ with standard library
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./...`
