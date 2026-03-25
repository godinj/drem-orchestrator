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
context_warn_percent  = 75
context_stop_percent  = 90
context_fixer_percent = 85
log_path              = "./drem.log"
test_command          = ""
compile_command       = ""
scoped_tests          = true
test_timeout          = "5m"
tmux_socket           = "drem"
tmux_config_file      = "master/.tmux.conf"
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
| `context_warn_percent` | Context usage % that triggers a warning (default 75) |
| `context_stop_percent` | Context usage % that triggers a hard stop (default 90) |
| `context_fixer_percent` | Context usage % that triggers fixer agent escalation (default 85) |
| `log_path` | Log file (kept separate from TUI output) |
| `test_command` | Command to run tests (e.g., `go test ./...`); empty = no test step |
| `compile_command` | Command to compile the project; empty = no compile step |
| `scoped_tests` | Run tests scoped to subtask file changes only (default true) |
| `test_timeout` | Timeout for test command execution (default 5m) |
| `tmux_socket` | Dedicated tmux server socket name (default `drem`) |
| `tmux_config_file` | Repo-local tmux config path, relative to bare repo (default `master/.tmux.conf`) |

## Tmux Isolation

Drem runs in a **dedicated tmux server** separate from your personal tmux sessions. This means drem's sessions, windows, and configuration never collide with your own.

- **Socket**: All tmux commands use `-L drem` (configurable via `tmux_socket`), creating an independent server.
- **Config**: A repo-local `.tmux.conf` (in the master worktree) is loaded via `-f`, so drem has its own status bar theme and settings without touching `~/.tmux.conf`.
- **Visual distinction**: The default config uses a blue/cyan status bar with a `[drem]` prefix so you can immediately tell you're in a drem session.

To interact with the drem tmux server directly:

```bash
# List drem sessions
tmux -L drem list-sessions

# Attach to the drem server
tmux -L drem attach
```

To override, set `tmux_socket` and/or `tmux_config_file` in `drem.toml`:

```toml
tmux_socket      = "my-custom-socket"
tmux_config_file = "my-tmux.conf"   # resolved relative to bare_repo_path
```

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
| `c` | Add comment / feedback, or answer a clarification question |
| `d` | Delete mode — select a comment, plan step, task, or subtask to remove (`d` → select → `y`/`Enter`). Deleting a root task cascades to all subtasks, agents, comments, and events |
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
| `b` | Open bug report screen |
| `?` | Toggle context-aware help overlay |

> **Exiting:** The TUI does not have a quit key. To exit, kill the tmux session (e.g. `tmux kill-session`).

## Bug Reports

Bug reports are structured problem reports filed by agents during their work. When an agent encounters a broken build, flaky test, unclear requirement, constraint violation, or other issue, it writes a JSON file that the orchestrator ingests and surfaces in a dedicated TUI screen. Human operators triage these reports and optionally promote them into tasks.

### Filing Bug Reports

Agents file bug reports by writing a JSON file to `.drem/bug-reports/<uuid>.json`. The orchestrator scans this directory every tick (default 5s), validates each file, inserts valid reports into the database, and deletes the source file. Invalid files are moved to `.drem/bug-reports/failed/` for debugging.

**JSON schema:**

```json
{
  "title": "Short descriptive title",
  "description": "What went wrong — be specific",
  "category": "tooling|merge_conflict|requirements|constraint_violation|upstream_code|test_failure|environment|other",
  "severity": "blocking|degraded|informational",
  "reproduction_context": "File paths, commands run, error output — enough to reproduce",
  "agent_id": "<agent UUID>",
  "task_id": "<task UUID>"
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `title` | Yes | Short descriptive title for the problem |
| `description` | Yes | What went wrong — be specific about symptoms and impact |
| `category` | Yes | Problem classification (see values below) |
| `severity` | Yes | Impact level (see values below) |
| `reproduction_context` | No | File paths, commands, error output — enough to reproduce |
| `agent_id` | No | UUID of the filing agent (read from `.claude/agent-metadata.json`) |
| `task_id` | No | UUID of the task the agent was working on |

**Category values:**

| Value | Meaning |
|-------|---------|
| `tooling` | Build tools, CLI tools, or infrastructure problems |
| `merge_conflict` | Git merge conflicts during agent work |
| `requirements` | Unclear, contradictory, or incomplete requirements |
| `constraint_violation` | Quality constraint failures the agent cannot resolve |
| `upstream_code` | Bugs or issues in existing code the agent depends on |
| `test_failure` | Flaky or unexpectedly failing tests |
| `environment` | Environment setup, dependency, or configuration problems |
| `other` | Anything not covered by the above categories |

**Severity values:**

| Value | Meaning |
|-------|---------|
| `blocking` | Agent cannot continue its work |
| `degraded` | Agent worked around the problem but it remains |
| `informational` | Observed issue with no immediate impact |

**Example:**

```json
{
  "title": "go vet fails on internal/merge package",
  "description": "Running go vet ./internal/merge/... reports 'unreachable code' in merge.go line 245. The dead code was introduced by a recent refactor. Agent worked around it by skipping the vet step but the issue should be fixed.",
  "category": "tooling",
  "severity": "degraded",
  "reproduction_context": "cd /path/to/worktree && go vet ./internal/merge/...\n\nOutput:\ninternal/merge/merge.go:245:2: unreachable code",
  "agent_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
  "task_id": "98765432-dcba-0987-6543-210fedcba987"
}
```

### Bug Report TUI Screen

Press `b` from the main dashboard to open the bug report screen. The screen shows a scrollable list of reports with columns for severity icon, category tag, title, status, and relative timestamp.

**Severity icons in the list:**
- `!` (red, bold) -- blocking
- `~` (yellow) -- degraded
- `·` (gray) -- informational

**Keybindings:**

| Key | Action |
|-----|--------|
| `b` | Toggle bug report screen (return to dashboard) |
| `j/k` or `Up/Down` | Navigate list (or scroll detail pane when open) |
| `Enter` | Toggle detail view for selected report |
| `a` | Acknowledge report |
| `p` | Promote to task (opens `$EDITOR`) |
| `D` | Dismiss report |
| `x` | Delete report (prompts `y/n` confirmation) |
| `c` | Add comment (opens feedback overlay) |
| `/` | Toggle filter mode |
| `Esc` | Return to dashboard |

### Bug Report Lifecycle

```
open ──→ acknowledged ──→ promoted
  │          │
  │          └──→ dismissed
  │
  ├──→ promoted
  └──→ dismissed
```

| Status | Meaning |
|--------|---------|
| `open` | Newly filed, not yet reviewed |
| `acknowledged` | Operator has seen the report but is not taking immediate action |
| `promoted` | Converted into a task (linked via `PromotedTaskID`) |
| `dismissed` | Hidden from the default view but retained in the database |

Hard-delete (`x` with confirmation) permanently removes the report and its comments from the database. This is separate from dismissal.

**Promotion workflow:** Press `p` on a report to start promotion. A temporary file is created pre-populated with the report's title (first line) and description (remaining lines). `$EDITOR` opens for refinement. On save, a new task is created in `backlog` status with the edited title and description. The bug report transitions to `promoted` status and stores a reference to the created task.

### Filtering

Press `/` to enter filter mode. Use `Tab` to cycle between filter dimensions and `j/k` to cycle values within a dimension. Press `Enter` to apply or `Esc` to cancel.

**Filter dimensions:**

| Dimension | Values |
|-----------|--------|
| Category | `all`, `tooling`, `merge_conflict`, `requirements`, `constraint_violation`, `upstream_code`, `test_failure`, `environment`, `other` |
| Severity | `all`, `blocking`, `degraded`, `informational` |
| Status | `all`, `open`, `acknowledged`, `promoted`, `dismissed` |
| Dismissed | `yes` / `no` (default `no` -- dismissed reports are hidden) |

Active filters are shown as badges in the header (e.g., `[cat:tooling sev:blocking]`).

## Task Lifecycle

```
                                                                        ┌─────────────────────────────────────────────────────┐
                                                                        │             (revise plan)                            │
                                                                        ▼                                                     │
classifying ──► backlog ──► planning ──► needs_clarification ──► plan_review ──► test_writing ──► test_review ──► in_progress ──► testing_ready ──► merging ──► done
                              │                  │                    │               │                │               │                │               │
                              ▼                  ▼                    ▼               ▼                ▼               ▼                ▼               ▼
                           failed             planning             planning       failed/paused    test_writing    failed/paused     in_progress     failed
                                          (replan with context)   (revise plan)                    (revise tests)                  (needs changes)

                          plan_review ─────────────────────────────────► in_progress
                                                                     (skip TDD path)

                          test_review ──► planning
                                       (full replan)

                          paused ──► classifying / backlog / planning / in_progress / test_writing
                          failed ──► classifying / backlog / in_progress / test_writing

                          rejected   (terminal — set when subtasks are rejected at a review gate)
```

New tasks start in `classifying` where a classifier agent explores the codebase to determine scope, complexity, and category. If no assumptions need clarification, the task skips `needs_clarification` and moves directly from `planning` to `plan_review`.

- **classifying** -- Classifier agent is analyzing task scope and complexity (produces category, complexity score, target files)
- **backlog** -- Waiting for dependencies to be met
- **planning** -- Planner agent is decomposing the task
- **needs_clarification** -- Plan assumptions need human input; the TUI shows clarification questions
- **plan_review** -- Human gate: approve the subtask plan or send it back
- **test_writing** -- Test agent is writing tests before implementation begins (TDD)
- **test_review** -- Human gate: verify written tests before implementation
- **in_progress** -- Coder/researcher agents are working on subtasks
- **testing_ready** -- Human gate: verify the work meets acceptance criteria
- **merging** -- Agent branches are being merged into the feature integration branch
- **done** -- All work merged successfully
- **failed** -- Something went wrong (can be retried back to classifying or backlog). If the parent failed but all subtasks eventually complete, the reconciler automatically recovers the parent (see [State Reconciliation](#state-reconciliation) #7)
- **paused** -- Manually paused by user
- **rejected** -- Task rejected at a review gate (terminal)

### Plan Clarification

After a planner agent generates a plan, the system evaluates the plan's assumptions to determine if any are risky or unclear. If so, the task enters a clarification loop where you are asked targeted questions before the plan proceeds to review. If no assumptions need clarification, the task skips straight to plan review.

**When it triggers:** After `planning` completes and before `plan_review`. The task moves to `needs_clarification` state. The supervisor evaluates each assumption's risk level; high-risk assumptions with user-facing impact generate questions.

**TUI interaction:**

- When a task is in `needs_clarification`, the detail panel shows the current question
- Press `c` to answer the current question
- Type your answer and press Enter to submit
- Type `/done` to finish the clarification round early (skipping remaining questions)
- The system may ask follow-up questions based on your answers
- Once all questions are answered, the plan either proceeds to `plan_review` or goes back to `planning` for replanning with your clarification context

**Behind the scenes:** The supervisor evaluates each assumption in the plan and assigns a risk level. High-risk assumptions that have user-facing impact generate clarification questions. Answers are deduplicated and fed back into the planning context so the planner can incorporate them. If answers reveal that the original plan was based on incorrect assumptions, the task returns to `planning` for a revised plan rather than proceeding to review.

### Step Scores

Every plan and implementation is scored on four dimensions when ready for human review:

| Dimension | Plan Review | Implementation Review |
|-----------|------------|----------------------|
| **TDD** | % of implementation subtasks covered by test subtasks | `go test -cover` average across packages |
| **Constitution** | Plan validation pass rate (errors=0%, warnings reduce score) | Constraint check pass/fail ratio |
| **Documentation** | Whether any subtask touches doc files | Whether changed files include documentation |
| **Depth** | Three-criterion evaluation of module boundaries, interface shapes, and deep decomposition | Constraint-based depth checks |

The depth score uses three equally-weighted sub-criteria when `depth_meta` is present in plan subtasks: (1) at least one subtask defines valid module boundaries, (2) at least one subtask specifies interface shapes, and (3) all boundary-defining subtasks keep exports in (0, 20]. Plans without `depth_meta` fall back to a file-coverage ratio. The scoring logic lives in `internal/orchestrator/score_bridge.go` (bridge) and `internal/score/` (canonical), with parity tests ensuring both agree.

Scores appear in the TUI board as compact badges (T:85 C:100 D:0 Dp:67) and in the detail panel as a full score line.

**Reading the badge**: Scores appear as compact badges in the task board: `T:85 C:100 D:42 Dp:67`
- `T` = TDD coverage
- `C` = Constitution compliance
- `D` = Documentation coverage
- `Dp` = Depth score

**Score ranges**:

| Range | Meaning | Action |
|-------|---------|--------|
| 80-100 | Strong | No action needed |
| 60-79 | Acceptable | Monitor — may improve as subtasks complete |
| 40-59 | Concerning | Review the plan/implementation for gaps |
| 0-39 | Weak | Address before approving |

**What to do when a score is low**:

- **Low TDD (T)**: At plan review — check that implementation subtasks have corresponding test subtasks. At testing_ready — check `go test -cover` output for the affected packages.
- **Low Constitution (C)**: Run `bash scripts/check_constitution.sh` to see which constraints are failing. Common causes: file too long, too many exports, formatting issues.
- **Low Documentation (D)**: Ensure at least one subtask touches documentation files (README, doc comments, guides). At testing_ready, check whether changed files include any `.md` updates.
- **Low Depth (Dp)**: At plan review — check that subtasks include `depth_meta` with module boundaries and interface shapes. Plans without `depth_meta` fall back to a file-coverage ratio which tends to score lower.

**Scores at different gates**:
- **Plan review**: Scores are predictive (based on plan structure, not actual code). TDD measures test subtask coverage. Depth evaluates module decomposition quality.
- **Testing ready**: Scores are measured (based on actual code). TDD uses real `go test -cover` output. Constitution runs actual constraint checks.

## Agent Types

| Type | Purpose |
|------|---------|
| **planner** | Decomposes a root task into 3-8 subtasks with file lists, dependencies, and agent type assignments |
| **coder** | Implements a subtask in an isolated worktree |
| **researcher** | Investigates questions, reads code, gathers information |
| **reviewer** | Reviews plans or diffs; approves or requests changes |
| **fixer** | Diagnoses and fixes broken merges or failed agent work |

## Git Worktree Layout

Drem uses a structured worktree hierarchy under the bare repo. The default branch can be checked out either as a linked worktree inside the bare repo or at the bare repo root itself (for non-bare clones used as the `--repo` path):

```
your-project.git/              # bare repo (root may also be the main worktree)
├── main/                      # default branch worktree (linked)
└── feature/
    └── my-feature/
        ├── integration/       # feature integration branch
        ├── agent-<uuid-1>/    # agent 1's isolated worktree
        └── agent-<uuid-2>/    # agent 2's isolated worktree
```

Each agent works in its own worktree and branch. Completed work is rebased onto the integration branch, then merged with `--no-ff` for clean history. The `MainWorktreePath()` resolver checks all worktrees — including the repo root — for the default branch checkout, so both layouts work without additional configuration.

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
├── model/             GORM models (Task, Agent, Project, Memory, TaskEvent, TaskComment, BugReport)
├── csuite/            C-Suite monitoring models (CsuiteAgent, CsuiteInboxMessage) and enums
├── db/                SQLite init, WAL mode, auto-migrations
├── state/             Task status state machine with validated transitions
├── orchestrator/      Main tick loop, task scheduling, dependency resolution
├── agent/             Agent spawning, heartbeat monitoring, completion tracking
├── bugreport/         Bug report ingestion, lifecycle management, and promotion
├── tmux/              tmux session/window management wrapper
├── worktree/          Git worktree lifecycle (create, merge, cleanup)
├── merge/             Rebase-before-merge orchestration, conflict detection
├── prompt/            Agent prompt generation (markdown, per agent type; helpers in prompt_helpers.go)
├── memory/            Agent memory persistence and compaction
├── supervisor/        Optional LLM-powered decision layer (plan validation, failure diagnosis)
├── taskimport/        Markdown task parsing and import
└── tui/               Bubble Tea dashboard (board, agents, detail, create, feedback panels)
```

### Key Dependencies

- [Bubble Tea](https://github.com/charmbracelet/bubbletea) + [Lipgloss](https://github.com/charmbracelet/lipgloss) -- TUI framework
- [GORM](https://gorm.io/) + [go-sqlite3](https://github.com/mattn/go-sqlite3) -- ORM and database
- [BurntSushi/toml](https://github.com/BurntSushi/toml) -- Configuration parsing

## Agent Lifecycle

When a subtask is scheduled, the orchestrator:

1. **Creates a git worktree** under `feature/<name>/agent-<uuid>/` with a dedicated branch
2. **Writes a prompt file** to `.claude/agent-prompt.md` in the worktree with task context, constraints, and instructions
3. **Spawns a Claude Code process** as a headless subprocess (planners, coders, reviewers, fixers) or a tmux session (supervisors, shells)
4. **Monitors heartbeats** — each agent updates `HeartbeatAt` via the context monitor. If an agent's heartbeat is older than `stale_timeout`, it is marked dead
5. **Tracks context usage** — the context monitor (`ctxmon`) reads Claude's cost/token output to detect when an agent is approaching its context window limit
6. **Handles completion** — when the subprocess exits, the orchestrator processes the agent's work (merge, test gate, or failure recovery)

Agents that exceed context thresholds trigger automatic recovery: a fixer agent is spawned for implementation agents at 85%, and a hard stop occurs at 90%. Fixer agents that hit 80% escalate to human review.

## Supervisor

The optional supervisor is an LLM-powered evaluation layer (`internal/supervisor/`). It is invoked at key decision points:

- **Plan validation** — checks that a planner's decomposition is reasonable before presenting it for human review
- **Failure diagnosis** — when a task fails, the supervisor analyzes logs and context to recommend retry, replan, or escalate
- **Manual trigger** — users can press `S` in the TUI to request a supervisor evaluation of any task

The supervisor returns structured JSON with a verdict (approve/reject/retry/escalate) and reasoning. Its output is stored in `task.Context` and displayed in the detail panel. Configure via `supervisor_enabled` and `supervisor_timeout` in `drem.toml`.

## Quality Constraints

Quality constraints are automated checks that enforce your project's structural and coding standards. They act as a machine-readable constitution: define your rules once and the orchestrator enforces them throughout the agent workflow.

### Where Constraints Are Defined

Constraints live in `.drem/constraints.toml` at the root of your bare repo. The file uses TOML array-of-tables syntax (`[[type]]`) with one entry per rule. It is committed to the repo and available in every worktree.

### Constraint Types

Five constraint types are available. Examples below are taken from this project's `.drem/constraints.toml`.

#### `[[command]]` -- Shell Command

Run a shell command in the worktree root. Passes if the exit code is 0. Set `expect = "empty_output"` to pass only when stdout is empty.

```toml
[[command]]
name   = "gofmt compliance"
run    = "gofmt -l ./internal/ ./cmd/"
expect = "empty_output"
```

#### `[[max_lines]]` -- File Length Ceiling

Enforce a maximum line count on files matching a glob pattern. Test files and other patterns can be excluded.

```toml
[[max_lines]]
name    = "File length ceiling"
glob    = "internal/**/*.go"
exclude = ["*_test.go"]
limit   = 800

[[max_lines.exception]]
path           = "internal/orchestrator/orchestrator.go"
rule           = "shrink-only"
baseline_lines = 2250
```

#### `[[max_matches]]` -- Regex Match Count

Count regex matches per file (`scope = "file"`) or per directory (`scope = "directory"`). Fails if the count exceeds `limit`.

```toml
[[max_matches]]
name    = "Exported function count"
glob    = "internal/**/*.go"
exclude = ["*_test.go"]
pattern = "^func [A-Z]"
limit   = 20
scope   = "file"
```

#### `[[no_match]]` -- Forbidden Pattern

A regex pattern that must match zero times in the target files. Use `exclude_path` to exempt specific directories.

```toml
[[no_match]]
name         = "DB init outside testutil"
glob         = "internal/**/*_test.go"
exclude_path = ["internal/testutil/", "internal/model/"]
pattern      = "gorm\\.Open\\(sqlite"
```

#### `[[depth]]` -- Module Depth Enforcement

Enforce export ratio and pass-through limits across packages. Keeps the internal API surface small.

```toml
[[depth]]
name              = "Export ratio ceiling"
glob              = "internal/**/*.go"
exclude           = ["*_test.go"]
max_export_ratio  = 0.15
max_pass_throughs = 3
```

### Running Checks Manually

```bash
bash scripts/check_constitution.sh
```

This evaluates all constraints defined in `.drem/constraints.toml` and prints a PASS/FAIL report for each rule.

### Automatic Enforcement

The orchestrator evaluates constraints at three gates during the agent workflow:

- **Plan validation** -- When a planner agent produces a plan, the validator checks whether `estimated_files` target constrained or grandfathered files. Warnings appear at `plan_review` so you can catch problems before agents start working.
- **Post-agent gate** -- After each agent completes, file-based constraints (`max_lines`, `max_matches`, `no_match`, `depth`) are evaluated on the agent's worktree before merging into the integration branch. Violations fail the subtask with feedback so the next attempt knows what to fix.
- **Integration gate** -- Before a parent task can transition to `testing_ready`, all constraints (including `command` constraints like `gofmt` and `go vet`) are evaluated on the integration worktree. Violations block the transition.

The supervisor also powers **depth enforcement gates**: plan depth scores are evaluated against a threshold, and depth-specific constraint violations receive supervisor diagnosis at the integration gate. Both are advisory only.

### Adding Exceptions

Use `[[<type>.exception]]` sub-tables to modify how a constraint applies to a specific file or directory. Two exception rules are supported:

- **`shrink-only`** -- The metric (line count, match count, export ratio) must not exceed a declared baseline. The file is exempt from the general limit but must stay at or below its baseline value. Any change that increases the metric above the baseline is a violation.

  ```toml
  [[max_lines.exception]]
  path           = "internal/orchestrator/orchestrator.go"
  rule           = "shrink-only"
  baseline_lines = 2250
  ```

- **`grandfathered`** -- The file or directory is fully exempt from the constraint. Use sparingly for legacy code that cannot be refactored yet.

  ```toml
  [[depth.exception]]
  path               = "internal/tui/"
  rule               = "grandfathered"
  baseline_ratio     = 1.0
  baseline_pass_thrus = 100
  ```

### Context Files

The top-level `context_files` key lists files that are included verbatim in planner and coder agent prompts, giving agents architectural awareness:

```toml
context_files = ["ARCHITECTURE.md"]
```

## Merge Conflict Prevention

The orchestrator uses a layered approach to prevent merge conflicts when parallel agents work in the same codebase.

### Layer 1: Plan-Time Prevention

The planner prompt requires test subtasks to list **all** files they will create or modify in `files`, including stub and interface files needed for compilation. This allows the scheduler's wave grouping (which uses `estimated_files` overlap) to detect conflicts before agents run.

**Same-package overlap warning:** Plan validation (`internal/orchestrator/plan_validation.go`) checks pairs of test-phase subtasks that share no dependency. If both target files in the same Go package, it warns that they likely need shared stubs and should either list those stubs explicitly or be sequenced with a dependency.

**Shared foundations pattern:** When multiple subtasks need the same new type or interface, the planner is guided to create a small foundational subtask that establishes those shared types first. Other subtasks depend on it, avoiding duplicate stubs and merge conflicts.

### Layer 2: Dispatch-Time File-Conflict Detection

Before dispatching a subtask, the scheduler evaluates it against all in-progress sibling tasks using `SchedulingPolicy.EvaluateDispatch()`. A task is only dispatched when all three checks pass:

1. **Dependency resolution** -- all tasks in `DependencyIDs` must be in `done` status
2. **Wave group ordering** -- if a wave schedule exists in the parent's context, earlier groups must complete before later groups start
3. **File-conflict scoring** -- the candidate's `estimated_files` are compared against every in-progress task's files. The conflict score is `|intersection| / min(|a|, |b|)`. If the score meets or exceeds the block threshold (0.3), the candidate is held back until the conflicting task finishes.

This prevents two agents from concurrently modifying the same files, which is the root cause of merge conflict cascades in parallel task execution.

**Wave scheduling** groups subtasks into concurrent batches using graph coloring on a file-overlap graph. `BuildSchedule()` assigns subtasks to groups such that no two tasks in the same group share files. Groups execute sequentially -- group N+1 starts only when all tasks in group N are terminal. The schedule is stored in `parent.Context["schedule"]` and computed once at plan approval.

**How it surfaces:** Blocked dispatches are logged at `DEBUG` level with the blocking reason (e.g., "file conflict with 2 in-progress task(s)", "wave group 2 blocked: group 1 still has active tasks", "unmet dependencies"). No user action is needed -- blocked tasks are automatically dispatched once the conflict resolves.

### Layer 3: Merge-Time Recovery

When an agent-to-feature merge fails despite plan-time and dispatch-time prevention, the supervisor diagnoses the conflict and may spawn a fixer agent to auto-resolve trivial conflicts (see Self-Healing item #6 above). This mirrors the existing feature-to-main merge conflict fixer but operates at the subtask level.

## Memory & Context

Agent memory (`internal/memory/`) provides cross-conversation persistence:

- **StoreMemory** — agents can record decisions, blockers, file changes, and completions as typed memory entries
- **ExtractMemoriesFromOutput** — automatically extracts memory-worthy facts from agent output using pattern matching
- **CompactAgentMemory** — compresses accumulated memories into a structured summary (grouped by type: decisions, file changes, blockers, completions) and archives the originals
- **BuildAgentContext** — assembles a token-budgeted context string for an agent, combining its own memory summary, recent task memories, and project-wide context from other agents

Context management tracks each agent's token usage via the `context_used_pct` field in `agent.Config`. The orchestrator uses this to trigger compaction, spawn fixers, or hard-stop agents before they exhaust their context window.

## Prompt System

Prompts are generated per agent type in `internal/prompt/`. Each prompt includes:

- Task description and acceptance criteria
- Parent task context (for subtasks)
- Git diff of existing work (truncated to 50,000 characters)
- Constraint rules from `.drem/constraints.toml`
- Agent-type-specific instructions (planners get decomposition rules, coders get implementation guidelines, reviewers get evaluation criteria)

Prompts are written to `.claude/agent-prompt.md` in the agent's worktree before the Claude process starts.

## Transcript Monitoring

The `agentmon` package (`internal/agentmon/`) tails Claude's conversation transcript (`~/.claude/projects/.../conversations/*.jsonl`) to extract structured signals:

- Test results (pass/fail with output)
- Build errors
- Git operations
- Context usage indicators

These signals feed back into the orchestrator's decision loop for automated test gating and failure recovery.

## Context Monitor (ctxmon)

The `ctxmon` binary (`cmd/ctxmon/`) is a standalone CLI for configuring and querying context window monitoring in agent worktrees. It is used by the `/swarm` skill to set up monitoring before agents start and to check usage while they run.

```bash
# Build
go build -o ctxmon ./cmd/ctxmon

# Set up monitoring in a worktree (creates .claude/ with hook scripts)
ctxmon setup /path/to/worktree

# Query current context usage (JSON output)
ctxmon status /path/to/worktree
```

The `status` command returns JSON with `used_percent`, `remaining_percent`, and a `compaction_triggered` flag. If no usage data has been recorded yet, it exits with code 2.

## Agent Exit Logging

When an agent session ends, a Claude Code **Stop hook** captures structured exit information and writes it to a JSONL log file. This gives the orchestrator visibility into why an agent stopped and what it was doing at the time.

**What it captures:**
- Exit reason (`success`, `error`, `context_limit`, `user_interrupt`, `unknown`)
- Last tool call before exit (e.g. `Edit`, `Read`, `Bash`)
- Summary of work done (files modified, commits made)
- Agent ID and task ID for correlation

**Where logs are stored:**
Exit entries are appended to `exit-log.jsonl` in the agent's Claude Code project directory (`~/.claude/projects/<project-dir>/exit-log.jsonl`). Each line is a JSON object with fields: `agent_id`, `task_id`, `timestamp`, `exit_reason`, `last_tool`, `last_target`, `files_modified`, `commits_made`, `summary`.

**How it works:**
1. At spawn time, the orchestrator writes `agent-metadata.json` (containing agent/task IDs) and an `exit-log.sh` hook script to the agent's `.claude/` directory
2. The agent's `settings.json` includes a `Stop` hook that runs `exit-log.sh` when the session ends
3. When the agent process exits, the orchestrator reads `exit-log.jsonl`, matches the latest entry for that agent, and populates `ExitInfo` on the completion record
4. `processAgentResult` stores exit info in the agent's `Config` field (`exit_reason`, `exit_last_tool`, `exit_summary`)

**How it surfaces in the TUI:**
- The agent detail view shows an "Exit:" line with the reason and last tool (e.g. `Exit: context_limit (last: Read)`) followed by the work summary
- The agent sidebar shows `exit: <reason>` for dead or idle agents

The Stop hook is configured alongside the existing Notification (idle detection) and PreCompact (context compaction) hooks.

## Database

Drem uses SQLite in WAL mode for zero-configuration persistence:

- **Location** — configured via `database_path` (default `./drem.db`)
- **Schema** — auto-migrated on startup via GORM for models: Project, Task, Agent, TaskEvent, Memory, TaskComment, BugReport, BugReportComment, CsuiteAgent, CsuiteInboxMessage
- **Inspection** — use `sqlite3 drem.db` to query directly; `.schema` shows tables, `.mode column` + `.headers on` for readable output
- **WAL mode** — enables concurrent reads during agent writes; the `-wal` and `-shm` files are expected alongside the main DB file

## Writing Tests

The `internal/testutil/` package provides shared helpers to eliminate boilerplate:

| Helper | Purpose |
|--------|---------|
| `testutil.NewTestDB(t)` | In-memory SQLite with auto-migration and unique name for isolation |
| `testutil.SetupBareRepo(t)` | Bare git repo with initial commit (auto-cleaned via `t.TempDir()`) |
| `testutil.InitBareRepoWithMainWorktree(t)` | Bare repo plus a linked main worktree |
| `testutil.AddWorktree(t, bareRepo, branch, dir)` | Create a worktree with a new branch |
| `testutil.CommitFile(t, wt, filename, content, msg)` | Write a file and commit it |
| `testutil.CreateProject(t, db, name, path, branch)` | Insert a project record |
| `testutil.CreateTask(t, db, projectID, title, status)` | Insert a task record |
| `testutil.CreateAgent(t, db, taskID, agentType, status)` | Insert an agent record |
| `testutil.CreateCsuiteAgent(t, db, name, status)` | Insert a C-Suite monitored agent record |
| `testutil.CreateCsuiteInboxMessage(t, db, from, to, subject, priority, msgType)` | Insert a C-Suite inbox message |
| `testutil.NewTestDBWithModels(t, extraModels...)` | In-memory SQLite with core + extra models (e.g., csuite) |

**Patterns to follow:**

- Use `testutil.NewTestDB(t)` instead of opening SQLite directly in tests
- Use table-driven tests for parameterized cases
- Use `t.TempDir()` for any filesystem operations (auto-cleanup)
- Keep test helpers in `testutil/` when they are needed by multiple packages; package-local helpers are fine when used by only one package

## Development

```bash
# Run tests
go test ./...

# Build
go build -o drem ./cmd/drem

# Format
gofmt -w .

# Check quality constraints
bash scripts/check_constitution.sh
```

### Conventions

- Standard library preferred where possible
- Error wrapping with `fmt.Errorf("context: %w", err)`
- `context.Context` for cancellation
- Table-driven tests
- Exported functions have doc comments

## Self-Healing & Recovery

The orchestrator automatically handles five recurring failure patterns that would otherwise require manual intervention. Each recovery is logged to the log file configured via `log_path`.

### 1. Dirty Worktree Auto-Commit

**Problem:** Orchestrator-generated artifacts (`.claude/`, `journals/`, `plan.json`) left in a feature worktree cause rebase and merge operations to fail with "dirty worktree" errors.

**How it works:** Before any merge or rebase, `MergeAgentIntoFeature()` checks whether the feature worktree is clean; if not, it auto-commits all unstaged changes with a `chore:` message. See `internal/merge/merge.go` lines 142-167.

`.claude/` files (including `settings.json`) are explicitly excluded from auto-commits even when tracked. After `git add --all`, any staged `.claude/` files are unstaged via `git reset HEAD -- .claude/`. This prevents worktree-specific paths in `.claude/settings.json` from being committed and causing merge conflicts.

As defense-in-depth, `UntrackEphemeralFiles` untracks `.claude/settings.json` (alongside `plan.json`) before merges. This ensures that even if `.claude/settings.json` was previously committed by an agent, it is removed from git tracking before any merge operation, preventing conflicts caused by divergent worktree-specific paths.

**Where you see it:** Log entries reading `auto-committed orchestrator artifacts before merge` with the feature worktree path.

### 2. Stale Agent Unlinking

**Problem:** A failed agent leaves behind its assignment on a subtask, plus failure-related context keys (`retry_count`, `last_error`), preventing the task from being properly rescheduled.

**How it works:** `RetryTask()` clears the `retry_count` and `last_error` context keys and transitions the task back to BACKLOG so it can be picked up by a fresh agent. See `internal/orchestrator/orchestrator.go`.

**Where you see it:** Log entry `task retried` with the task ID; the task reappears in BACKLOG status in the TUI.

### 3. Stuck Agent Recovery

**Problem:** An agent's tmux session dies (crash, OOM, network drop) without sending a completion signal, leaving the subtask permanently stuck in IN_PROGRESS.

**How it works:** The reconciliation loop detects IN_PROGRESS subtasks whose assigned agent is no longer in the runner's active set. If the agent branch has commits, those are routed through the normal completion path. If not, the subtask is rescheduled (up to `MaxEmptyWorkRetries = 2` attempts) before being marked FAILED. See `reconcileStuckAgents()` in `internal/orchestrator/reconcile.go`.

**Where you see it:** Log warning `detected dead agent session without completion` followed by either `auto-retrying dead agent subtask` or a failure message.

### 4. Already-Complete Fast-Track

**Problem:** The supervisor diagnoses an empty-work agent as `already_complete` (the work was already done by a prior agent or manual edit), but the default path would waste retry attempts on a task that needs no further action.

**How it works:** When the supervisor's failure diagnosis returns a category of `already_complete`, `no_changes_needed`, or `work_done`, the subtask is fast-tracked directly to DONE, bypassing retry logic. See `onAgentEmptyWork()` in `internal/orchestrator/agent_results.go`.

**Where you see it:** Supervisor journal entry with outcome `Fast-tracked to DONE — work already complete`; the task jumps to DONE in the TUI.

### 5. Merge Conflict Fixer (Feature-to-Main)

**Problem:** Merging a feature branch into the default branch fails with file conflicts that could be automatically resolved.

**How it works:** When `executeMerge()` detects conflicts, the supervisor analyzes them and returns a `resolution_strategy`. If the strategy is `spawn_agent`, a fixer agent is automatically spawned to resolve the conflicts in the feature worktree. See `internal/orchestrator/task_processing.go` lines 525-654.

**Where you see it:** Log entry `spawning resolver agent for merge conflict`; the TUI shows a new fixer agent attached to the task; a `merge_conflict` event is emitted with `fixer_spawned: true`.

### 6. Agent-to-Feature Merge Conflict Recovery

**Problem:** When parallel agents create overlapping stubs or modify the same files, merging an agent's branch into the feature integration branch fails. Previously this immediately marked the subtask as failed.

**How it works:** `handleAgentMergeFailure()` invokes the supervisor to diagnose the merge conflict before giving up. If the supervisor recommends `spawn_agent` (trivial or auto-fixable conflict), a fixer agent is spawned to resolve the conflicts in the feature worktree. If the conflict is non-trivial or no supervisor is available, the subtask falls back to failure with the agent branch preserved. See `internal/orchestrator/agent_merge.go`.

**Where you see it:** Supervisor journal entry with type `merge_conflict` and outcome `Spawning resolver agent for agent-to-feature merge conflict`; the task context gains `merge_conflict_severity`, `merge_conflict_strategy`, and `merge_conflict_files` fields.

## State Reconciliation

The `Reconcile()` method runs automatically every tick (default 5s) and audits the project for seven categories of state inconsistency. All fixes are logged and emitted as a `reconcile` event. Source: `internal/orchestrator/reconcile.go`.

| # | Category | Detected State | Fix Applied |
|---|----------|---------------|-------------|
| 1 | **Stale subtasks** | DONE subtasks whose parent is IN_PROGRESS but the feature branch has zero file changes relative to the default branch | Reset to BACKLOG for rescheduling |
| 2 | **Orphaned subtasks** | IN_PROGRESS subtasks whose assigned agent is idle or dead (completion signal was lost) | Attempt to merge remaining agent work into the feature branch and fast-track to DONE; if merge fails, mark FAILED with agent worktree preserved |
| 3 | **Empty features** | TESTING_READY parent tasks whose feature branch has no file changes relative to the default branch | Marked FAILED with `empty_feature` context flag |
| 4 | **Orphan worktrees** | Agent worktrees with no commits ahead of the feature branch and no corresponding WORKING agent in the database | Worktree removed via `git worktree remove` |
| 5 | **Stuck agents** | IN_PROGRESS subtasks whose agent tmux session is dead but still marked WORKING in the database | If agent branch has commits, route through normal completion; otherwise auto-retry (up to `MaxEmptyWorkRetries = 2`), then fail |
| 6 | **Already-merged features** | FAILED parent tasks whose feature branch is already an ancestor of the default branch HEAD | Transition directly to DONE (bypasses state machine since failed-to-done is not a normal transition); feature worktree cleaned up |
| 7 | **Failed parents with completed subtasks** | FAILED parent tasks where all subtasks have reached DONE status (e.g., parent failed due to a merge conflict but subtasks succeeded via retry) | Transition from FAILED → IN_PROGRESS via the state machine, then evaluate quality gates via `checkFeatureCompletion()` to advance to the appropriate next status |

Reconciliation results can be observed in the log file. When any fixes are applied, a `reconcile` event is emitted containing a `ReconcileResult` struct with counts for each category.

## Troubleshooting

### Running the Constitution Check

See [Quality Constraints](#quality-constraints) for details on constraint definitions, types, and enforcement gates. To run checks manually:

```bash
bash scripts/check_constitution.sh
```

See [Quality Constraints](#quality-constraints) for details on what this checks and how to configure it.


### Inspecting the Database

The SQLite database (default `drem.db`) can be queried directly:

```bash
sqlite3 drem.db
```

Useful queries:

```sql
-- Table structure
.schema

-- Task overview
SELECT id, title, status FROM tasks;

-- Active agents
SELECT id, agent_type, status FROM agents WHERE status = 'working';

-- Failed tasks with error context
SELECT id, title, context FROM tasks WHERE status = 'failed';

-- Recent task events
SELECT task_id, event_type, old_value, new_value, created_at
FROM task_events ORDER BY created_at DESC LIMIT 20;
```

### Debugging a Failed Merge

1. **Check the log file** (default `drem.log`) for merge error details -- search for `merge failed` or `merge conflicts`
2. **Inspect task context** in the database -- `SELECT context FROM tasks WHERE id = '<task-id>';` will show supervisor diagnosis fields like `merge_conflict_severity`, `merge_conflict_strategy`, and `merge_conflict_hints`
3. **Check the feature worktree** for conflict markers -- navigate to the feature integration worktree (e.g., `<bare-repo>/feature/<name>/integration/`) and look for `<<<<<<<` markers in source files

### Cleaning Up After a Failed Run

1. **Clean dead tmux sessions** -- press `C` in the TUI to reap orphaned agent sessions
2. **Remove orphan worktrees manually** -- list worktrees with `git worktree list` from the bare repo, then remove stale ones with `git worktree remove <path>`
3. **Reset stuck tasks** -- press `R` on a FAILED task in the TUI to retry it back to BACKLOG, or use the database directly to update status

### Understanding Supervisor Diagnoses

The supervisor is an LLM-powered decision layer that evaluates failures and merge conflicts. Its outputs are stored in `task.Context` and visible in the TUI detail panel.

**Failure diagnosis** (`FailureDiagnosis`):
- `category` -- classifies the failure: `transient`, `prompt_issue`, `code_error`, `environment`, `already_complete`, or `unknown`
- `should_retry` -- whether the orchestrator should retry the task
- `retry_strategy` -- `same_prompt`, `modified_prompt`, or `different_approach`

**Merge conflict analysis** (`MergeConflictAnalysis`):
- `severity` -- `trivial`, `moderate`, or `complex`
- `resolution_strategy` -- `auto_resolve`, `spawn_agent`, or `manual`
- `resolution_hints` -- free-text guidance for resolving the conflicts

**Build failure diagnosis** (`BuildFailureDiagnosis`):
- `root_cause` -- what went wrong in the build
- `can_auto_fix` -- whether a fixer agent can resolve it automatically

All supervisor evaluations are also recorded in journal files under the `journals/` directory.

### Effort Levels

The orchestrator uses two different Claude Code effort levels:

| Component | Effort Level | Rationale |
|-----------|-------------|-----------|
| Supervisor (`internal/supervisor/supervisor.go`) | `--effort low` | Fast classification and diagnosis; supervisor calls happen on every decision point so speed matters |
| Agents (`internal/agent/process.go`) | `--effort medium` | Balanced speed and quality for actual code generation and research work |

These effort levels are hardcoded and not configurable via `drem.toml`.

## License

See [LICENSE](LICENSE) for details.
