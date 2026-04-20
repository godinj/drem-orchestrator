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
max_dispatch_rate     = 3
dispatch_window       = "60s"
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
| `max_dispatch_rate` | Maximum agent dispatches allowed within the dispatch window (default 3) |
| `dispatch_window` | Sliding window duration for dispatch rate limiting (default `60s`) |

### Model Profiles

Profiles let you override model and effort settings per agent role for a named experiment or run configuration. Define `[profiles.<name>]` sections in `drem.toml`:

```toml
[profiles.fast.planner]
model  = "claude-opus-4-6"
effort = "high"

[profiles.fast.coder]
model  = "claude-opus-4-6"
effort = "high"

[profiles.cheap.planner]
model  = "claude-haiku-4-5-20251001"
effort = "low"

[profiles.cheap.coder]
model  = "claude-haiku-4-5-20251001"
effort = "low"
```

**Profile name constraints:** Names must be non-empty and contain only alphanumeric characters, hyphens (`-`), and underscores (`_`).

**Partial overrides:** You only need to specify the roles you want to change. Any role not listed in a profile inherits from the corresponding `[agents.<role>]` section (or the hardcoded default if that is also absent).

**Resolution order (highest priority wins):**
1. Profile override for the specific agent role
2. Default agent config from `[agents.<role>]`
3. Hardcoded defaults (`claude-sonnet-4-6`, effort `normal`)

Use `ForAgentTypeWithProfile(agentType, profileName)` in the config API to resolve the effective `AgentCLIConfig` for a given role and profile. If the profile name is unknown, an error is returned — referencing a non-existent profile is always an error, not a silent fallback.

### Direct API Agents

Drem can run selected agent roles as synchronous calls to a local SGLang OpenAI-compatible endpoint instead of spawning an OpenCode subprocess. This bypasses the ~20K-token tool-definition overhead OpenCode loads by default and eliminates process startup cost, cutting typical token usage from ~40K to a few thousand per run.

Supported roles:

- **Classifier** — enable with `Orchestrator.SetDirectClassifierConfig(&agent.DirectClassifierConfig{...})`. Handles all `CLASSIFYING` tasks.
- **Plan reviewer** — enable with `Orchestrator.SetDirectPlanReviewerConfig(&agent.DirectPlanReviewerConfig{...})`. Engages only when `SpawnReviewerSession` runs with a task in `plan_review`; feature review (`testing_ready`) continues to use the subprocess path unchanged.
- **Prep** — enable via `[agents.prep]` TOML stanza or auto-enabled when the classifier is direct. Read-only recon role for task preparation.
- **Coder / Reviewer / Fixer** — see "Direct SGLang Tool Agents" section below.

Defaults target `http://localhost:8081/v1/chat/completions` with `gemma4-26b`. Pass `nil` to either setter to fall back to the subprocess path. Direct agents produce the same output artifacts (`classification-<task>.json`, `review.json`) the subprocess agents emit, so downstream completion handlers are unchanged.

## Direct SGLang Tool Agents

Coder, reviewer, and fixer roles can bypass the Claude Code / OpenCode
subprocess path and call an SGLang-served local model directly over its
OpenAI-compatible `/v1/chat/completions` endpoint. Subprocess tool
definitions consume roughly 20 000 tokens per agent; the direct path emits
a compact role-specific tool list (about 460 tokens), which keeps a 43 000
token context budget usable for local models such as Gemma 4.

When enabled, the orchestrator creates a lightweight agent DB record for
audit and duplicate-dispatch guards, then runs a synchronous tool-call
loop (`read`, `edit`, `write`, `bash`, `grep`, `glob` — reviewers are
restricted to read-only tools). On success the completion is funneled
through the existing `onAgentCompleted` / `onReviewerCompleted` /
`onFixerCompleted` handlers so merge, review.json parsing, and fixer
bookkeeping stay unchanged. SGLang is started with
`--tool-call-parser gemma4` (hardcoded in `deploy/compose/global.yml`)
so OpenAI tool calls are converted to and from the model's native
tool tokens. The containerized image reproduces the host's
customized SGLang build (upstream git + 6 gemma4 patches) so the
`gemma4` parser is natively supported — see
`plans/sglang-gemma4-followup.md` for the build approach.

**Enable the direct path** in `drem.toml`:

```toml
[direct_tool_agent]
enabled        = true
endpoint       = "http://localhost:8081/v1/chat/completions"
model          = "gemma4-26b"
max_tokens     = 2048
temperature    = 0.1
timeout        = "120s"
max_iterations = 20
bash_timeout   = "30s"
```

All fields are optional when `enabled = true`; omitted values fall back
to `agent.DefaultDirectToolAgentConfig()`. The feature also auto-enables
when any of the coder, reviewer, or fixer agents declare
`provider = "sglang-direct"`:

```toml
[agents.coder]
provider = "sglang-direct"
```

| Role     | Tools                                     | Output                          |
|----------|-------------------------------------------|---------------------------------|
| coder    | read, edit, write, bash, grep, glob       | commits on the agent's branch   |
| reviewer | read, bash, grep, glob (read-only)        | `review.json` in the worktree   |
| fixer    | read, edit, write, bash, grep, glob       | commits a minimal fix           |

The direct path does not replace Claude Code for high-complexity work —
it is intended for local, latency-sensitive routing on models that
struggle with the full Claude Code prompt surface. Leaving
`[direct_tool_agent].enabled = false` (the default) preserves the
existing subprocess behavior for every role.

### Context window monitoring

Direct tool agents do not run inside the OpenCode subprocess, so they
bypass the `ctxmon` context monitor that watches Claude Code agents.
`RunDirectToolAgent` instead does its own per-iteration check by reading
the `prompt_tokens` field returned by the SGLang API and comparing it
against `DirectToolAgentConfig.ContextLimit` (the model's input window
in tokens — e.g. 131072 for `gemma4-26b`). When `ContextLimit` is `0`
the monitor is disabled.

| Threshold | Default | Behavior |
|-----------|---------|----------|
| `ContextWarnPct` | 85 | Append a one-shot system message asking the model to wrap up. Fires once per run. |
| `ContextStopPct` | 95 | Halt the loop and return `StopReason="context_limit"`. Prevents runaway token spend on stuck agents. |

A natural `finish_reason="stop"` always wins over the stop threshold —
if the model produced a final answer on its own, the run is treated as
a successful completion regardless of context usage.

The orchestrator forwards its repo-wide `context_warn_pct` /
`context_stop_pct` settings onto each direct-tool dispatch, so the
direct path uses the same thresholds as the subprocess monitor. After
each run the result's `FinalContextPct` is persisted onto the agent
record's `Config["context_used_pct"]` field, making the value visible
to the TUI agent panel and the C-Suite dashboards alongside Claude Code
agents.

## Dispatch Throttling

To prevent API quota exhaustion when many tasks advance simultaneously, the orchestrator rate-limits agent dispatch using a sliding window algorithm.

**How it works:**

- The orchestrator tracks the timestamp of each agent dispatch in a sliding window of configurable duration (`dispatch_window`, default 60s).
- Before spawning a new agent, it checks whether the number of dispatches within the current window has reached `max_dispatch_rate` (default 3). If so, the dispatch is deferred.
- Tasks that cannot be dispatched immediately are **not dropped** — they remain in their current state and are retried on the next orchestrator tick (every `tick_interval`).
- Random jitter (up to 5s) is added to retry timing to prevent thundering-herd effects when the window resets.

**Configuration:**

```toml
max_dispatch_rate = 3    # max agents dispatched per window
dispatch_window   = "60s" # sliding window duration
```

With the defaults, no more than 3 agents will be dispatched in any rolling 60-second period. Increase `max_dispatch_rate` if your API quota allows higher concurrency, or widen `dispatch_window` to spread dispatches further apart.

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

**Sort Order:** Tasks are organized by status priority to keep actionable items visible:
1. **Active** — tasks currently being worked on (`in_progress`, `planning`, `test_writing`, `merging`, `classifying`, `backlog`)
2. **Gates** — tasks awaiting human approval (`plan_review`, `test_review`, `testing_ready`, `needs_clarification`)
3. **Failed** — tasks that encountered errors (visible without scrolling for quick action)
4. **Paused/Rejected** — suspended or rejected tasks
5. **Done** — completed tasks

Within each status group, tasks are sorted by priority (higher first), ensuring failed tasks appear above the large done list for immediate visibility.

### Agent Panel

Lists all agents with their type, status, current task, and last heartbeat.

#### Phase 1: Agent Enrichment Fields

The agent panel displays two enrichment fields for each agent:

**Model ID** — The Claude model being used by the agent (e.g., `claude-opus-4`, `claude-haiku`).
- Populated immediately after the agent spawns
- Shows as "-" for agents created before this feature or if the model is not yet assigned
- Useful for understanding which agents are using which models

**Cost** — The cumulative cost (in USD) of API calls made by the agent during its execution.
- Updated continuously as the agent runs
- Shows as "$X.XX" (e.g., `$1.50`, `$0.05`)
- Shows as "-" if no cost data is available yet
- Shows as `$0.00` for agents that have started but made no API calls
- Costs >$0.50 appear in yellow, >$1.00 appear in red (visual warning for expensive agents)

**Example Agent Panel Display:**

```
> agent-1   spawned
    session: tmux-4
    branch: feature/xyz
    model: claude-opus-4
    cost: $2.15

> agent-2   working
    session: tmux-5
    branch: feature/abc
    model: claude-haiku
    cost: $0.42
    activity: ⊸ compile cmd/drem/main.go | implemented

> agent-3   dead [timeout]
    session: (no session)
    branch: (no branch)
    model: claude-sonnet
    cost: $1.87
    exit_reason: context limit exceeded
```

**When are these fields populated?**

- **Model ID**: Set immediately when the agent is spawned, from the agent configuration
- **Cost**: Updated every 30 seconds (or at heartbeat interval) by the context monitor, pulling cost data from the Claude API usage tracker

**Querying agents by model via SQL:**

You can query the orchestrator database directly to find agents by model. `model_id` and `total_cost_usd` are first-class columns on the `agents` table (not stored in the JSON config blob):

```sql
-- Find all agents using a specific model
SELECT id, agent_type, status, created_at FROM agents
WHERE model_id = 'claude-opus-4';

-- Sum costs by model
SELECT
  model_id,
  COUNT(*) as agent_count,
  SUM(total_cost_usd) as total_cost
FROM agents
GROUP BY model_id;

-- Find expensive completed agents
SELECT name, model_id, total_cost_usd, exit_reason, completed_at
FROM agents
WHERE total_cost_usd > 1.0
ORDER BY total_cost_usd DESC;
```

**Terminal Width:**

All agent panel lines respect standard terminal widths:
- Agent header: ~19 characters (e.g., `> agent-1   working`)
- Model line: ~24-33 characters (e.g., `    model: claude-opus-4`)
- Cost line: ~17 characters (e.g., `    cost: $123.45`)
- Maximum observed: 84 characters (for long branch paths)
- Safe on 120+ column terminals ✓

**Phase 1 Context:**

These fields are part of Phase 1 of the Metrics & Experiments plan. The agent enrichment enables:
- Real-time cost tracking per agent
- Model usage analytics across the fleet
- Cost-aware scheduling and agent placement in future phases
- Retrospective analysis of model performance and cost efficiency

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
| `w` | Open C-Suite Dashboard (agent monitoring & messaging) |
| `b` | Open bug report screen |
| `?` | Toggle context-aware help overlay |

> **Exiting:** The TUI does not have a quit key. To exit, kill the tmux session (e.g. `tmux kill-session`).

### C-Suite Dashboard

The C-Suite Dashboard provides real-time monitoring of orchestrator agents and inter-agent messaging.

**Accessing the Dashboard:**
Press `w` from the main task board to open the C-Suite Dashboard.

**Dashboard Features:**

- **Agent Health Table** — Lists all registered C-Suite agents with:
  - Status (online, stale, offline)
  - Last heartbeat time
  - Context usage percentage (colors: gray ≤75%, yellow >75%, red >90%)
  - Inbox count (messages waiting for agent)
  - Current activity description

- **Pipeline Summary** — Aggregate view of all agents:
  - Total agent count (online, stale, offline)
  - Total unread messages across all agents

**Message Management:**

When viewing an agent's messages, use these keybindings:

| Key | Action |
|-----|--------|
| `j/k` | Navigate message list |
| `enter` | Open message detail view |
| `c` | Compose new message |
| `a` | Toggle archive (show/hide archived messages) |
| `esc` | Return to agent list or previous view |

**Message Detail View:**

View the full content of a single message including:
- Sender and recipient
- Subject, priority, and message type
- Timestamp
- Full message body

| Key | Action |
|-----|--------|
| `r` | Quick reply (pre-fills recipient and "Re: " subject) |
| `esc` | Return to message list |

**Compose View:**

Create a new message with the following fields:
- **To** — Recipient agent name
- **Subject** — Message subject line
- **Priority** — Low, Normal, High, or Critical
- **Type** — Status, Request, Alert, or Decision
- **Body** — Message content

| Key | Action |
|-----|--------|
| `Tab` / `Shift+Tab` | Navigate between fields |
| `Ctrl+S` or `Enter` | Submit and send message |
| `esc` | Cancel and return to previous view |

**Features:**

- Messages are stored in the orchestrator database and persist across sessions
- Archived messages are hidden by default; press `a` in the list view to toggle visibility
- Quick reply automatically fills the recipient and adds "Re: " prefix to the subject
- Priority color-coding: Critical (red), High (yellow), Normal (blue), Low (gray)
- All messages include metadata (from, to, type, priority, timestamp)

## CLI Commands

The orchestrator provides a headless CLI for programmatic task and agent control. This is primarily used by C-Suite agents and temp workers that need to approve plans, provide feedback, or report test results without human operator intervention.

### Subcommand Overview

**Task queries** (read-only):
- `drem cli tasks` — List all tasks with summary information
- `drem cli task <id>` — Get detailed information about a single task
- `drem cli agents` — List all Claude Code agents and their status
- `drem cli failures` — List task failures and their root causes
- `drem cli stats` — Display orchestrator statistics
- `drem cli file-task <path>` — Create a task from a markdown file

**Task mutations**:
- `drem cli comment <task-id> --body '...'` — Add a comment to a task (allowed in any status)
- `drem cli approve <task-id>` — Approve a plan or test review (gate command)
- `drem cli reject <task-id> [--reason '...']` — Reject a plan or test review (gate command)
- `drem cli answer <task-id> --body '...'` — Answer a clarification question (gate command)
- `drem cli pass <task-id>` — Pass a final test review (gate command)
- `drem cli fail <task-id>` — Fail a final test review (gate command)

### Gate Commands

Gate commands are actions taken at task review points. They require an orchestrator instance with proper task state; headless agents use these to drive task progression without human interaction.

#### `drem cli approve <task-id>`

Approves a task at a review gate (either plan_review or test_review).

**Valid statuses:** `plan_review`, `test_review`
- **plan_review** → approves plan and moves task to test_writing
- **test_review** → approves tests and moves task to in_progress

**Usage:**
```bash
# Approve a plan awaiting review
drem cli approve 12345678

# Using full UUID
drem cli approve 12345678-1234-1234-1234-123456789012
```

**Error cases:**
```bash
# Task not found
$ drem cli approve abcdefgh
error: task not found

# Wrong status (task not at a gate)
$ drem cli approve 12345678
error: task 12345678 is in wrong status "in_progress" for approval (expected plan_review or test_review)
```

#### `drem cli reject <task-id> [--reason '...']`

Rejects a task at a review gate and transitions it back for revision.

**Valid statuses:** `plan_review`, `test_review`
- **plan_review** → rejects plan and moves task back to planning
- **test_review** → rejects tests with feedback and moves task back to test_writing

**Usage:**
```bash
# Reject a plan (no reason required)
drem cli reject 12345678

# Reject tests with feedback (recommended)
drem cli reject 12345678 --reason "Incomplete test coverage; add tests for error cases"

# Alternative syntax
drem cli reject 12345678 --reason='Add validation for negative numbers'
```

**Error cases:**
```bash
# Task not found
$ drem cli reject abcdefgh
error: task not found

# Wrong status
$ drem cli reject 12345678
error: task 12345678 is in wrong status "done" for rejection (expected plan_review or test_review)

# Missing task ID
$ drem cli reject
error: usage: drem cli reject <task-id> [--reason=REASON]
```

#### `drem cli answer <task-id> --body '...'`

Answers a clarification question when a task is blocked waiting for additional information.

**Valid statuses:** `needs_clarification`

**Usage:**
```bash
# Answer a clarification question
drem cli answer 12345678 --body 'The API endpoint is /api/v2/users/{id}'

# Multi-line answer (shell quoting)
drem cli answer 12345678 --body 'Requirements:
1. Must support batch operations
2. Should be backward compatible
3. Add performance benchmarks'
```

**Error cases:**
```bash
# Missing body
$ drem cli answer 12345678
error: usage: drem cli answer <task-id> --body=BODY

# Empty body
$ drem cli answer 12345678 --body ''
error: --body is required

# Wrong status
$ drem cli answer 12345678 --body 'Answer'
error: task 12345678 is in wrong status "plan_review" for answering clarification (expected needs_clarification)

# Task not found
$ drem cli answer abcdefgh --body 'Answer'
error: task not found
```

#### `drem cli pass <task-id>`

Passes the final test review, marking a task as ready for merge into main.

**Valid statuses:** `testing_ready`

Transitions task to merging and begins the merge process.

**Usage:**
```bash
# Pass final testing
drem cli pass 12345678

# Full UUID
drem cli pass 12345678-1234-1234-1234-123456789012
```

**Error cases:**
```bash
# Wrong status (not at testing_ready gate)
$ drem cli pass 12345678
error: task 12345678 is in wrong status "in_progress" for passing (expected testing_ready)

# Task not found
$ drem cli pass abcdefgh
error: task not found

# Missing task ID
$ drem cli pass
error: usage: drem cli pass <task-id>
```

#### `drem cli fail <task-id>`

Fails the final test review, sending a task back for fixes.

**Valid statuses:** `testing_ready`

Transitions task back to in_progress so the coder agent can address the failures.

**Usage:**
```bash
# Fail and send back to coder
drem cli fail 12345678

# Full UUID
drem cli fail 12345678-1234-1234-1234-123456789012
```

**Error cases:**
```bash
# Wrong status
$ drem cli fail 12345678
error: task 12345678 is in wrong status "done" for failing (expected testing_ready)

# Task not found
$ drem cli fail abcdefgh
error: task not found

# Missing task ID
$ drem cli fail
error: usage: drem cli fail <task-id>
```

#### `drem cli comment <task-id> --body '...'`

Adds a comment to a task. Comments are allowed on tasks in **any status** — including `backlog`, `planning`, `in_progress`, and all other lifecycle states. This enables programmatic annotation by C-Suite agents and temp workers without requiring the task to be at a human-gate status.

> **Note:** Prior to this change, `AddComment` restricted comments to human-gate statuses (`plan_review`, `test_review`, `needs_clarification`, `testing_ready`). That restriction has been removed.

**Valid statuses:** any

**Usage:**
```bash
# Add a comment to a task in any status
drem cli comment 12345678 --body 'Blocked on upstream API change'

# Works during active development
drem cli comment 12345678 --body 'Reviewer noted: check edge case in line 42'

# Full UUID also accepted
drem cli comment 12345678-1234-1234-1234-123456789012 --body 'See related task 87654321'
```

**Error cases:**
```bash
# Task not found
$ drem cli comment abcdefgh --body 'hello'
error: task not found

# Missing body
$ drem cli comment 12345678
error: usage: drem cli comment <task-id> --body=BODY
```

### Task ID Resolution

All CLI commands that take a task ID accept either:
- **Short form** (first 8 characters of UUID): `drem cli approve 12345678`
- **Full UUID**: `drem cli approve 12345678-1234-1234-1234-123456789012`

The CLI resolves the task by prefix matching against the database. If the prefix matches multiple tasks, an error is returned.

### JSON Output

Most read-only subcommands support `--json` for structured output. Example:

```bash
drem cli tasks --json | jq '.[] | select(.status == "plan_review")'
drem cli task 12345678 --json
drem cli agents --json
```

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

**Depth scoring criteria**: The depth score evaluates three equally-weighted criteria: (1) at least one subtask defines valid `module_boundaries` with package and description, (2) at least one subtask specifies `interface_shapes` with functions or types, (3) all boundary-defining subtasks keep `exports` in (0, 20]. Plans that define no depth metadata score 0% and are flagged for rejection.

**Shallow vs deep plans**: A shallow plan wraps a few lines of logic in a thin wrapper or pass-through function with no module boundaries, no interface shapes, and no meaningful internal logic — this scores 0%. A deep plan creates modules with real internal decision-making (policy logic, state machines, validation), clear boundaries, and narrow export surfaces — this scores 100%.

**Planner self-check**: Before submitting a plan, the planner evaluates whether each subtask has `module_boundaries` and `interface_shapes`, whether modules contain meaningful internal logic rather than just delegating, and whether there are opportunities to unify duplicated concerns into shared infrastructure. Subtasks that fail this self-check are restructured before submission.

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
| **prep** | Read-only recon pass before the coder writes: reads target files and emits a structured tactical brief (target files, insertion points, patterns to follow, warnings, constructors) |

## Direct SGLang Agents

For roles that talk to a local SGLang-served model (typically `gemma4-26b`), drem supports a "direct" path that calls the OpenAI-compatible chat completions endpoint synchronously instead of spawning an OpenCode subprocess. The direct path avoids roughly 20K tokens of tool-definition overhead that OpenCode injects on every turn, which matters for smaller context windows.

Two roles currently have a direct path:

| Role | Tools | Use |
|------|-------|-----|
| **classifier** | none | Single-shot task classification via JSON output |
| **prep** | read, grep, glob | Read-only reconnaissance agent that gathers context for the coder |

The prep role is deliberately restricted to **read, grep, and glob** — it has no edit, write, or bash tool. Its job is to gather signal, not to modify code. The model returns a `PrepOutput` JSON document (target files with relevant definitions and methods, insertion points, patterns to follow, warnings, and affected constructors) which the orchestrator stores in the subtask context. The coder agent that runs next sees that context in its prompt and writes code with better situational awareness.

### Enabling the direct path

Direct classifier and prep are auto-enabled when the classifier role is configured against an SGLang provider. You can also enable either role explicitly:

```toml
[agents.classifier]
provider = "opencode"
model    = "sglang/gemma4-26b"
direct   = true            # explicit opt-in

[agents.prep]
provider = "opencode"
model    = "sglang/gemma4-26b"
direct   = true            # explicit opt-in; otherwise piggybacks on classifier
```

When enabled, prep shares the classifier's SGLang endpoint (`http://localhost:8081/v1/chat/completions`) but uses a larger token budget (`MaxTokens = 4096`) because its tool loop needs room for several iterations of tool calls plus a final JSON payload. The prep role falls back to the OpenCode subprocess path if `SetDirectPrepConfig` is left unset, preserving existing behavior.

### Token savings

A subprocess OpenCode classifier burns roughly 20K context tokens on tool definitions alone before seeing the user prompt. The direct path uses only the tools declared in `ToolsForRole("prep")` — read, grep, glob — whose combined JSON schema is under 2K tokens. That keeps prep well within the context budget of a 26B local model without triggering context-exhaustion warnings.

### Failure modes

If the SGLang endpoint errors or returns malformed JSON, the orchestrator degrades gracefully: the subtask is marked `prep_complete = true` and `prep_failed = true`, and the coder dispatches normally without the enrichment context. No retry storm, no dropped tasks.

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
cmd/csuite-watcher/    C-Suite bridge: event publisher, watcher loop, HTTP serve subcommand
internal/
├── model/             GORM models (Task, Agent, Project, Memory, TaskEvent, TaskComment, BugReport)
├── csuite/            C-Suite monitoring models (CsuiteAgent, CsuiteInboxMessage) and enums
├── serve/             Bridge HTTP server: bearer auth middleware, /api/health and /api/agents handlers
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

### Per-Constraint Delta Gate

The integration gate uses **per-constraint delta comparison** rather than total failure count. This prevents pre-existing violations (grandfathered baselines) from permanently blocking all tasks.

When the integration gate runs, it evaluates the feature worktree against master on a constraint-by-constraint basis. Each constraint is classified by its state transition:

| Transition | Outcome | Action |
|------------|---------|--------|
| PASS → PASS | No regression | Allow |
| PASS → FAIL | New violation introduced | Block |
| FAIL → PASS | Violation fixed | Allow |
| FAIL → FAIL (not worse) | Pre-existing, unchanged | Allow |
| FAIL → FAIL (worse) | Worsened violation | Block |

For FAIL→FAIL transitions, "worse" is measured by violation message count: more messages means the violation grew (e.g., a file that was 10 lines over the ceiling is now 50 lines over).

**What this means in practice:**

- A task that fixes one violation without touching others will be allowed even if other violations remain.
- A task that adds a new import to an already-over-ceiling package is blocked because the violation worsened.
- Pre-existing violations (grandfathered baselines like `orchestrator.go` line count) do not block unrelated tasks.

**Retry and backoff:** When the gate blocks, the orchestrator retries with exponential backoff (base 30s, up to 10 minutes, max 5 retries). Early termination triggers if the magnitude (number of blocked constraints) does not decrease between attempts.

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

## C-Suite Bridge Server (csuite-watcher serve)

The `csuite-watcher` binary (`cmd/csuite-watcher/`) provides a lightweight HTTP bridge server for the C-Suite mobile client. It exposes a REST API over bearer token authentication so the mobile app can query live agent status and inbox data.

```bash
# Build
go build -o csuite-watcher ./cmd/csuite-watcher

# Start the bridge server (reads [serve] section from drem.toml)
csuite-watcher serve --config /path/to/drem.toml
```

### Configuration

Add a `[serve]` section to your `drem.toml`:

```toml
[serve]
listen_addr   = ":8080"       # Address and port to listen on (default: ":8080")
bearer_token  = "secret"      # Required: token clients must supply in Authorization header
db_path       = "./drem.db"   # Path to the SQLite database (default: "./drem.db")
```

If `bearer_token` is empty the server starts but rejects all requests with 401 Unauthorized.

### Endpoints

| Method | Path             | Description                                                   |
|--------|------------------|---------------------------------------------------------------|
| GET    | `/api/health`    | Returns `{"status":"ok"}`. Requires auth.                     |
| GET    | `/api/agents`    | Returns JSON array of agent dashboard rows. Requires auth.    |
| GET    | `/api/messages`  | Returns paginated messages between two agents. Requires auth. |
| POST   | `/api/messages`  | Creates a new inbox message. Requires auth.                   |

All endpoints require a valid `Authorization: Bearer <token>` header. Missing or incorrect tokens return HTTP 401.

#### `GET /api/agents` response

```json
[
  {
    "name":             "agent-abc123",
    "status":           "in_progress",
    "context_percent":  42,
    "current_activity": "implementing foo.go",
    "unread_count":     2,
    "latest_inbox":     "2026-03-29T18:00:00Z"
  }
]
```

Fields map directly to `internal/csuite.AgentDashboardRow`. An empty agent list returns `[]`.

#### `GET /api/messages` query parameters

| Parameter   | Required | Description                                                                  |
|-------------|----------|------------------------------------------------------------------------------|
| `from`      | yes      | Agent name — one side of the conversation                                    |
| `to`        | yes      | Agent name — other side of the conversation                                  |
| `limit`     | no       | Max messages to return (default: 50)                                         |
| `before_id` | no       | UUID of a message — return only messages older than this (cursor pagination) |

Messages are returned newest-first. To page backwards, pass the `id` of the oldest message in the last response as `before_id`.

```json
[
  {
    "id":         "b3d2e1f0-...",
    "from_agent": "operator",
    "to_agent":   "ceo",
    "subject":    "Status update",
    "body":       "All systems nominal.",
    "priority":   "normal",
    "type":       "status",
    "archived":   false,
    "created_at": "2026-03-29T18:00:00Z"
  }
]
```

#### `POST /api/messages` request body

```json
{
  "from_agent": "operator",
  "to_agent":   "ceo",
  "subject":    "Status update",
  "body":       "All systems nominal.",
  "priority":   "normal",
  "type":       "status"
}
```

`priority` must be one of `low`, `normal`, `high` (default: `normal`). `type` must be one of `status`, `request`, `alert` (default: `status`). `from_agent`, `to_agent`, and `subject` are required. Returns HTTP 201 with the created message on success.

### Graceful shutdown

The server honours `SIGTERM` and `SIGINT` — send either signal to stop it cleanly.

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
