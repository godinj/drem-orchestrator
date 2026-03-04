# Go Rewrite — Agent Prompts

## Overview

Full rewrite of Drem Orchestrator from Python (FastAPI + React) to Go (Bubble Tea TUI + tmux). Agents spawn in tmux windows for direct visibility. Single binary, no web server, no Redis.

## Prompt Summary

| # | Name | Tier | Dependencies | Files Created |
|---|------|------|-------------|---------------|
| 01 | Models, DB & State Machine | 1 | None | `go.mod`, `internal/model/*.go`, `internal/db/db.go`, `internal/state/machine.go`, `cmd/drem/config.go`, `CLAUDE.md` |
| 02 | tmux Manager | 1 | None | `internal/tmux/tmux.go`, `internal/tmux/tmux_test.go` |
| 03 | Git Worktree Manager | 1 | None | `internal/worktree/manager.go`, `internal/worktree/git.go`, `internal/worktree/manager_test.go` |
| 04 | Agent Runner & Heartbeat | 2 | 01, 02, 03 | `internal/agent/runner.go`, `internal/agent/heartbeat.go` |
| 05 | Prompt Generation & Memory | 2 | 01 | `internal/prompt/prompt.go`, `internal/memory/memory.go`, `internal/memory/compaction.go` |
| 06 | Merge Orchestration | 2 | 01, 03 | `internal/merge/merge.go` |
| 07 | Orchestrator & Scheduler | 3 | 01-06 | `internal/orchestrator/orchestrator.go`, `internal/orchestrator/scheduler.go` |
| 08 | TUI Dashboard & Main | 4 | 01-07 | `internal/tui/*.go`, `cmd/drem/main.go` |

## Execution Order

```bash
# Tier 1 (parallel — no dependencies)
claude --agent docs/go-rewrite/prompts/01-models-db-state.md
claude --agent docs/go-rewrite/prompts/02-tmux-manager.md
claude --agent docs/go-rewrite/prompts/03-worktree-manager.md

# Tier 2 (parallel — after Tier 1 merges)
claude --agent docs/go-rewrite/prompts/04-agent-runner.md
claude --agent docs/go-rewrite/prompts/05-prompt-memory.md
claude --agent docs/go-rewrite/prompts/06-merge-orchestration.md

# Tier 3 (after Tier 2 merges)
claude --agent docs/go-rewrite/prompts/07-orchestrator.md

# Tier 4 (after Tier 3 merges)
claude --agent docs/go-rewrite/prompts/08-tui-main.md
```

## Dependency Graph

```
Tier 1:  [01 Models+DB]  [02 tmux]  [03 Worktree]
              │               │          │
Tier 2:  [04 Agent Runner]──┘──────────┘
         [05 Prompt+Memory]──(01)
         [06 Merge]──────────(01,03)
              │    │    │
Tier 3:  [07 Orchestrator]──(01-06)
              │
Tier 4:  [08 TUI + Main]──(01-07)
```

## Verification

After all agents complete:
```bash
go build ./...
go vet ./...
go test ./...
./drem --repo /path/to/bare-repo.git
```
