# Agent: OpenCode Process Starter

You are working on the `master` branch of drem-orchestrator, a Go-based orchestrator that spawns Claude Code agents as subprocesses in git worktrees. Your task is adding a `StartOpenCodeProcess` function that launches OpenCode (a local LLM CLI) as an alternative to Claude Code.

## Context

Read these specs before starting:
- `opencode-plan.md` (sections: OpenCode CLI Contract, Differences from Claude Code, Changes 4 — Process layer)
- `internal/agent/process.go` (current `StartAgentProcess` and `AgentProcess` — reuse the same return type and lifecycle)

Key differences between Claude Code and OpenCode process startup:

| Concern | Claude Code (`StartAgentProcess`) | OpenCode (`StartOpenCodeProcess`) |
|---------|-----------------------------------|-----------------------------------|
| Binary | `claudeBin` | `openCodeBin` |
| Permission bypass | `--dangerously-skip-permissions` | Not needed — `opencode run` auto-approves |
| Prompt delivery | stdin pipe (`-p -`) | Positional arg (last argument) |
| Working dir | `cmd.Dir = cwd` | `--dir <cwd>` flag |
| Stdout | Redirected to `.claude/agent-output.log` | Redirected to `.opencode/agent-output.jsonl` |
| Extra args | Inserted between `--dangerously-skip-permissions` and `-p -` | Inserted before `--dir` and prompt |

## Deliverables

### Modified files

#### 1. `internal/agent/process.go`

Add `StartOpenCodeProcess` function alongside the existing `StartAgentProcess`. Do NOT modify `StartAgentProcess` or `ProcessStarter` type.

**New function signature:**
```go
func StartOpenCodeProcess(ctx context.Context, openCodeBin, promptPath, cwd string, extraArgs []string) (*AgentProcess, error)
```

**Implementation details:**

1. Create `.opencode` directory in `cwd` (like `.claude` for Claude agents)
2. Create log file at `<cwd>/.opencode/agent-output.jsonl`
3. Read prompt content from `promptPath` into a string
4. Build command: `opencode run <extraArgs...> --dir <cwd> <prompt-content>`
   - `extraArgs` carries pre-built flags like `["--model", "ollama/qwen3-coder-iq4xs-128k", "--variant", "minimal", "--format", "json", "--agent", "build"]`
   - `--dir` flag sets the working directory for OpenCode
   - Prompt text is the final positional argument
5. Set `cmd.Dir = cwd`
6. Redirect stdout to the log file (this is the JSONL event stream)
7. Redirect stderr to the same log file
8. No stdin pipe needed — prompt is a positional arg, not piped
9. Start the process
10. Return an `*AgentProcess` with the same fields populated: `cmd`, `logFile`, `logPath`, `pid`, `done` channel
11. Launch the same wait goroutine pattern as `StartAgentProcess` (wait for exit, capture exit code, close done channel)

**Important:** The prompt content may be large (multi-KB). Pass it as a single positional argument — OpenCode handles this correctly. Read the prompt from the file at `promptPath`.

## Scope Limitation

Do NOT modify `runner.go`, `hook.go`, `monitor.go`, or any config files. Only add to `process.go`. Do not change the existing `StartAgentProcess` function or `ProcessStarter` type.

## Conventions

- Package: `agent`
- Error wrapping: `fmt.Errorf("start opencode process: <step>: %w", err)` — match the existing style in `StartAgentProcess`
- Build verification: `cd /home/godinj/git/drem-orchestrator.git/master && go vet ./... && go test ./...`
