# Drem Orchestrator

A terminal-based task orchestrator that coordinates multiple Claude Code agents to work on software projects in parallel. It decomposes features into subtasks, spawns specialized agents in isolated git worktrees, manages their lifecycle via tmux, merges their work, and provides a real-time TUI dashboard for monitoring and control.

## How It Works

```
                         ┌──────────────┐
                         │   You (TUI)  │
                         └──────┬───────┘
                                │ create tasks, approve plans, give feedback
                         ┌──────▼───────┐
                         │ Orchestrator │  tick loop (5s default)
                         └──────┬───────┘
               ┌────────────────┼────────────────┐
               ▼                ▼                ▼
          ┌──────────┐    ┌─────────┐       ┌─────────┐
          │ Planner  │    │ Coder   │  ...  │ Coder   │
          │  Agent   │    │ Agent 1 │       │ Agent N │
          └────┬─────┘    └────┬────┘       └────┬────┘
               │               │                 │
          plan JSON      feature/foo/        feature/foo/
                         agent-<uuid>/       agent-<uuid>/
                               │                 │
                               └────────┬────────┘
                                        ▼
                                feature/foo/integration
                                        │
                                        ▼  merge into main
                                      done
```

1. **Create a task** in the TUI dashboard describing a feature or bug fix
2. The orchestrator spawns a **planner agent** that decomposes the task into subtasks
3. You **review and approve** the plan (or provide feedback for revision)
4. **Coder agents** are spawned in parallel, each working in an isolated git worktree
5. Completed work is **merged** back into the feature integration branch
6. Once all subtasks pass, the feature is ready for promotion to main

## Prerequisites

- **Go 1.22+**
- **tmux** (agent sessions run inside tmux windows)
- **Claude Code CLI** (`claude` binary on PATH)
- **Git** with a **bare repository** for the target project
- **SQLite** (bundled via go-sqlite3, requires CGo)

## Installation

```bash
git clone https://github.com/godinj/drem-orchestrator.git
cd drem-orchestrator
go build -o drem ./cmd/drem
```

## Quick Start

```bash
# Point drem at a bare git repo
./drem --repo /path/to/your-project.git

# Or import tasks from a markdown file
./drem --repo /path/to/your-project.git --import tasks.md
```

Drem will create a tmux session, launch the TUI dashboard, and begin orchestrating. If you're already inside tmux, it switches to the drem session; otherwise it attaches.

## Configuration

Configuration is read from `drem.toml` (or specify `--config <path>`). All values have sensible defaults:

```toml
database_path         = "./drem.db"
bare_repo_path        = "/path/to/repo.git"
default_branch        = "master"
claude_bin            = "claude"
max_concurrent_agents = 5
tick_interval         = "5s"
heartbeat_interval    = "30s"
stale_timeout         = "5m"
supervisor_enabled    = true
supervisor_timeout    = "2m"
log_path              = "./drem.log"
```

| Setting | Description |
|---------|-------------|
| `database_path` | SQLite database file location |
| `bare_repo_path` | Path to the bare git repo (also settable via `--repo`) |
| `default_branch` | Branch to merge features into |
| `claude_bin` | Path to the Claude Code CLI binary |
| `max_concurrent_agents` | Maximum agents running simultaneously |
| `tick_interval` | How often the orchestrator checks for work |
| `heartbeat_interval` | How often agents report liveness |
| `stale_timeout` | Time without heartbeat before an agent is marked dead |
| `supervisor_enabled` | Enable LLM-powered decision layer for plan validation and failure diagnosis |
| `supervisor_timeout` | Timeout for supervisor LLM calls |
| `log_path` | Log file (kept separate from TUI output) |

## TUI Dashboard

The dashboard has three main panels switched with `Tab`:

### Task Board

Displays tasks in a tree view (parent tasks with expandable subtasks), color-coded by status.

### Agent Panel

Lists all agents with their type, status, current task, and last heartbeat.

### Keybindings

| Key | Action |
|-----|--------|
| `j/k` or `Up/Down` | Navigate |
| `Tab` | Switch panel |
| `Enter` | Expand/collapse task, or select |
| `n` | Create new task |
| `a` | Approve plan or test |
| `r` | Reject plan (send back for revision) |
| `t` | Pass test |
| `f` | Fail test |
| `c` | Add comment / feedback |
| `d` | Delete last comment |
| `g` | Jump to agent's tmux window |
| `l` | View agent log |
| `p` | Pause / resume task |
| `R` | Retry failed task |
| `v` | Spawn reviewer agent |
| `x` | Spawn fixer agent |
| `S` | Trigger supervisor evaluation |
| `A` | Toggle archived agents |
| `F` | Toggle task filter |
| `C` | Clean up dead tmux sessions |
| `q` | Quit |

## Task Lifecycle

```
backlog ──► planning ──► plan_review ──► in_progress ──► testing_ready ──► merging ──► done
              │              │               │                │               │
              ▼              ▼               ▼                ▼               ▼
           failed         planning       failed/paused     in_progress     failed
                        (revise plan)                    (needs changes)
```

- **backlog** -- Waiting for dependencies to be met
- **planning** -- Planner agent is decomposing the task
- **plan_review** -- Human gate: approve the subtask plan or send it back
- **in_progress** -- Coder/researcher agents are working on subtasks
- **testing_ready** -- Human gate: verify the work meets acceptance criteria
- **merging** -- Agent branches are being merged into the feature integration branch
- **done** -- All work merged successfully
- **failed** -- Something went wrong (can be retried back to backlog)
- **paused** -- Manually paused by user

## Agent Types

| Type | Purpose |
|------|---------|
| **planner** | Decomposes a root task into 3-8 subtasks with file lists, dependencies, and agent type assignments |
| **coder** | Implements a subtask in an isolated worktree |
| **researcher** | Investigates questions, reads code, gathers information |
| **reviewer** | Reviews plans or diffs; approves or requests changes |
| **fixer** | Diagnoses and fixes broken merges or failed agent work |

## Git Worktree Layout

Drem uses a structured worktree hierarchy under the bare repo:

```
your-project.git/              # bare repo
├── main/                      # default branch worktree
└── feature/
    └── my-feature/
        ├── integration/       # feature integration branch
        ├── agent-<uuid-1>/    # agent 1's isolated worktree
        └── agent-<uuid-2>/    # agent 2's isolated worktree
```

Each agent works in its own worktree and branch. Completed work is rebased onto the integration branch, then merged with `--no-ff` for clean history.

## Task Import

You can bulk-import tasks from a Markdown file:

```bash
./drem --repo /path/to/repo.git --import tasks.md
```

The expected format is one task per heading:

```markdown
# Task title

Task description and acceptance criteria here.

# Another task

More details.
```

## Architecture

```
cmd/drem/              Entry point, config parsing, tmux session bootstrap
internal/
├── model/             GORM models (Task, Agent, Project, Memory, TaskEvent, TaskComment)
├── db/                SQLite init, WAL mode, auto-migrations
├── state/             Task status state machine with validated transitions
├── orchestrator/      Main tick loop, task scheduling, dependency resolution
├── agent/             Agent spawning, heartbeat monitoring, completion tracking
├── tmux/              tmux session/window management wrapper
├── worktree/          Git worktree lifecycle (create, merge, cleanup)
├── merge/             Rebase-before-merge orchestration, conflict detection
├── prompt/            Agent prompt generation (markdown, per agent type)
├── memory/            Agent memory persistence and compaction
├── supervisor/        Optional LLM-powered decision layer (plan validation, failure diagnosis)
├── taskimport/        Markdown task parsing and import
└── tui/               Bubble Tea dashboard (board, agents, detail, create, feedback panels)
```

### Key Dependencies

- [Bubble Tea](https://github.com/charmbracelet/bubbletea) + [Lipgloss](https://github.com/charmbracelet/lipgloss) -- TUI framework
- [GORM](https://gorm.io/) + [go-sqlite3](https://github.com/mattn/go-sqlite3) -- ORM and database
- [BurntSushi/toml](https://github.com/BurntSushi/toml) -- Configuration parsing

## Development

```bash
# Run tests
go test ./...

# Build
go build -o drem ./cmd/drem

# Format
gofmt -w .
```

### Conventions

- Standard library preferred where possible
- Error wrapping with `fmt.Errorf("context: %w", err)`
- `context.Context` for cancellation
- Table-driven tests
- Exported functions have doc comments

## License

See [LICENSE](LICENSE) for details.
