# Architecture Constitution

Concrete, falsifiable rules to prevent specific categories of codebase decay.
These are not principles or aspirations — each rule can be verified with a grep,
a line count, or a single targeted question.

**Origin:** Every rule here addresses a problem documented in
`~/Documents/drem-canvas-docs/reference/drem-orchestrator-code-quality-report.md`.
New rules are added only when a new category of decay is observed. Rules are
removed when hook enforcement makes them redundant (see Graduation Path below).

**Enforcement status:**
- `[enforced]` — a hook or CI check blocks violations
- `[not yet enforced]` — manual compliance only; infrastructure pending

---

## Package Map

### `cmd/` — CLI entry points

- `cmd/drem/` — Main orchestrator CLI: project init, task import, agent lifecycle, TUI dashboard
- `cmd/check-constraints/` — CLI wrapper for running constitution constraint checks (`bash scripts/check_constitution.sh`)
- `cmd/ctxmon/` — CLI for setting up and querying context window monitoring in agent worktrees

### `internal/` — Core packages

- `agent/` — Agent lifecycle management: spawning, heartbeat, status tracking, and teardown of Claude Code agents (planner, coder, researcher, reviewer, fixer, classifier)
- `agentmon/` — Agent transcript monitoring: tails Claude conversation JSONL, extracts test results, build errors, git operations, and context usage signals
- `constraints/` — Constitution constraint engine: loads `.drem/constraints.toml`, evaluates `command`, `max_lines`, `max_matches`, `no_match`, and `depth` rules
- `ctxmon/` — Context window monitoring: tracks agent token usage, triggers compaction and fixer escalation
- `csuite/` — C-Suite agent monitoring models: CsuiteAgent (status, heartbeat, context usage), CsuiteInboxMessage (inter-agent messaging), and enums (AgentMonStatus, InboxPriority, InboxMessageType)
- `db/` — Database initialization and migration helpers for the Drem Orchestrator
- `memory/` — Agent memory persistence, retrieval, compaction, and extraction from agent output
- `merge/` — Worktree merge logic: merges agent worktree branches back into the integration branch
- `model/` — GORM models, enums (task statuses, agent types), and custom JSON types
- `orchestrator/` — Core orchestrator loop: tick-based scheduling, state transitions, agent dispatch
  - `orchestrator.go` — Main orchestrator struct, configuration, tick loop, and agent lifecycle coordination
  - `agent_results.go` — Agent result processing: routes completed agent success/failure, manages retries
  - `classifying.go` — Classifier output parsing, bug report ingestion, and classification of new bug reports
  - `context_monitor.go` — Context usage monitoring, fixer spawning, and failure recovery for agents
  - `dedup.go` — Duplicate work detection: checks integration branch for existing file changes and commit keyword overlap
  - `handlers.go` — TUI interaction handlers: plan approval/rejection, test review, task status transitions
  - `lifecycle.go` — Lifecycle CRUD: comment deletion, plan step management
  - `merge_attempt_state.go` — MergeAttemptState: typed context access for merge conflict state during retries
  - `merge_execution.go` — Merge execution, dispatch, retry logic, mergerClient interface, and quick-fix-to-merging transition
  - `plan_validation.go` — Plan validation: file overlap checks, TDD coverage ratios, constraint warnings
  - `retry_policy.go` — RetryPolicy: exponential backoff with jitter for merge retries (base=10s, cap=5min, max=5)
  - `reconcile.go` — State reconciliation: recovers stale subtasks, orphaned worktrees, stuck agents
  - `reconcile_parents.go` — Parent task reconciliation: already-merged feature detection, completed/failed parent recovery via policy pattern
  - `scheduler.go` — SchedulingPolicy: dependency resolution, wave group ordering, file-conflict scoring, and dispatch gating; BuildSchedule for graph-coloring-based subtask grouping
  - `score_bridge.go` — Inlined quality scoring (TDD, constitution, documentation, depth) to avoid import ceiling
  - `session_spawning.go` — Spawning reviewer, fixer, and supervisor Claude Code sessions
  - `task_api.go` — Task management API: pause, resume, retry, create, comment, and override operations
  - `task_processing.go` — Core task processing: backlog→planning transitions, quick-fix dispatch, agent spawning
  - `test_execution.go` — Test suite execution, compilation verification, and test scoping logic
- `prompt/` — Prompt builder: constructs markdown prompt strings piped to Claude Code agent sessions
- `score/` — Quality scoring: computes TDD, Constitution, Documentation, and Depth scores for plans and implementations
- `state/` — Task status state machine: defines valid transitions between task lifecycle states
- `supervisor/` — Agent supervisor: monitors running agents, detects stalls, triggers recovery
- `taskimport/` — Markdown task file parser: bulk-creates tasks from structured Markdown
- `testutil/` — Shared test infrastructure: database factories, git repo setup, mock supervisor
- `tmux/` — Go wrapper around the tmux CLI for managing sessions and windows used to host agents
- `tui/` — Bubble Tea TUI dashboard: real-time view of projects, tasks, agents, and logs
- `worktree/` — Git worktree management: creation, cleanup, and branch tracking for agent workspaces

---

## Task Lifecycle

Tasks move through the following states (defined in `internal/model/enums.go`):

```
                                    ┌──────────────────────────────────────────┐
                                    │              rejected                   │
                                    │            (terminal)                   │
                                    └──────────────────────────────────────────┘
                                          ▲              ▲
                                          │              │
classifying ──► backlog ──► planning ──► plan_review ──► test_writing ──► test_review ──► in_progress ──► testing_ready ──► merging ──► done
                                                                                              │                               │
                                                                                              ▼                               ▼
                                                                                           paused                          failed
```

| Status          | Description                                                                 |
|-----------------|-----------------------------------------------------------------------------|
| `classifying`   | Classifier agent is analyzing task scope and complexity                      |
| `backlog`       | Task created, not yet started                                               |
| `planning`      | Planner agent is generating a plan                                          |
| `plan_review`   | Human gate: plan awaits approval before proceeding                          |
| `test_writing`  | TDD phase: test agent is writing tests based on the approved plan           |
| `test_review`   | Human gate: written tests await approval before implementation              |
| `in_progress`   | Coder agent is implementing the task                                        |
| `testing_ready` | Human gate: implementation complete, awaiting final test/review             |
| `merging`       | Orchestrator is merging the agent worktree into the integration branch      |
| `paused`        | Task suspended by user or orchestrator                                      |
| `done`          | Task completed and merged successfully                                      |
| `failed`        | Task encountered an unrecoverable error                                     |
| `rejected`      | Task rejected at a review gate                                              |

**Actionable states** (orchestrator can take automated action): `classifying`, `backlog`, `planning`, `test_writing`, `in_progress`, `merging`.

**Human gates** (require human approval to proceed): `plan_review`, `test_review`, `testing_ready`.

---

## Configuration

Configuration is loaded from a TOML file (see `cmd/drem/config.go`). Missing values
use the defaults shown below.

| Option                  | TOML key                | Type       | Default        | Description                                        |
|-------------------------|-------------------------|------------|----------------|----------------------------------------------------|
| Database path           | `database_path`         | string     | `./drem.db`    | Path to the SQLite database file                   |
| Bare repo path          | `bare_repo_path`        | string     | `""`           | Path to the bare git repository                    |
| Default branch          | `default_branch`        | string     | `master`       | Default git branch name                            |
| Claude binary           | `claude_bin`            | string     | `claude`       | Path to the Claude Code CLI binary                 |
| Max concurrent agents   | `max_concurrent_agents` | int        | `5`            | Maximum number of agents running simultaneously    |
| Tick interval           | `tick_interval`         | duration   | `5s`           | Orchestrator main loop tick interval               |
| Heartbeat interval      | `heartbeat_interval`    | duration   | `30s`          | How often agents send heartbeats                   |
| Stale timeout           | `stale_timeout`         | duration   | `5m`           | Time before a silent agent is considered stale     |
| Supervisor enabled      | `supervisor_enabled`    | bool       | `true`         | Whether the agent supervisor is active             |
| Supervisor timeout      | `supervisor_timeout`    | duration   | `2m`           | Timeout for supervisor health checks               |
| Context warn percent    | `context_warn_percent`  | int        | `75`           | Context usage % that triggers a warning            |
| Context stop percent    | `context_stop_percent`  | int        | `90`           | Context usage % that triggers a hard stop          |
| Context fixer percent   | `context_fixer_percent` | int        | `85`           | Context usage % that triggers fixer escalation     |
| Log path                | `log_path`              | string     | `./drem.log`   | Path to the orchestrator log file                  |
| Test command            | `test_command`          | string     | `""`           | Build/test command for the project                 |
| Compile command         | `compile_command`       | string     | `""`           | Compile command for the project                    |
| Scoped tests            | `scoped_tests`          | bool       | `true`         | Whether to run tests scoped to subtask file changes|
| Test timeout            | `test_timeout`          | duration   | `5m`           | Timeout for test command execution                 |
| Tmux socket             | `tmux_socket`           | string     | `drem`         | Dedicated tmux server socket name (-L flag)        |
| Tmux config file        | `tmux_config_file`      | string     | `master/.tmux.conf` | Repo-local tmux config file path (relative to bare repo) |
| Max dispatch rate       | `max_dispatch_rate`     | int        | `3`            | Max agent dispatches allowed within the dispatch window   |
| Dispatch window         | `dispatch_window`       | duration   | `60s`          | Sliding window duration for dispatch rate limiting        |
| Model profiles          | `[profiles.<name>.*]`   | map        | `{}`           | Named per-role model/effort overrides; see docs/prd-metrics-and-experiments.md#profiles |

### Agent configuration

Per-agent CLI flags are set under `[agents.<type>]` sections. Supported agent
types: `classifier`, `planner`, `coder`, `reviewer`, `fixer`, `researcher`,
`supervisor`, `interactive_supervisor`.

| Field    | TOML key | Type   | Default    | Description                          |
|----------|----------|--------|------------|--------------------------------------|
| Model    | `model`  | string | `""`       | Claude model ID passed via `--model` |
| Effort   | `effort` | string | `"medium"` | Effort level: `low`, `medium`, `high`|

Example:

```toml
[agents.coder]
  model  = "claude-sonnet-4-6"
  effort = "medium"

[agents.supervisor]
  effort = "low"
```

### Profile configuration

Named profiles allow task-specific overrides layered on top of the base agent
configuration. Profiles are defined under `[profiles.<name>.agents.<type>]`.
Only explicitly set (non-empty) fields override their base counterparts —
omitted fields inherit from `[agents.<type>]`.

The three-layer resolution order (highest to lowest priority):
1. Profile override — fields set in `[profiles.<name>.agents.<type>]`
2. Base agent config — fields set in `[agents.<type>]`
3. Hardcoded default — `effort = "medium"`

Supported profile agent types match the base agent types above (excludes
`supervisor` and `interactive_supervisor`).

Example:

```toml
[agents.coder]
  model  = "claude-sonnet-4-6"
  effort = "medium"

# "fast" profile: keep the base model, run at low effort
[profiles.fast.agents.coder]
  effort = "low"

# "quality" profile: override both model and effort
[profiles.quality.agents.coder]
  model  = "claude-opus-4-6"
  effort = "high"
```

An empty profile name falls through to the base agent config. A non-empty name
that does not match any `[profiles.*]` entry is a configuration error.

---

## Structural Limits

### File length ceiling: 800 lines `[enforced]`

No `.go` source file (non-test) may exceed 800 lines. If adding code would
breach this limit, extract into a separate file in the same package first.

`orchestrator.go` (2,249 lines, down from 4,567) is grandfathered but must
shrink, not grow. Any change to a grandfathered file that increases its line
count is a violation.

**Compliance test:** `wc -l` on the changed file; must be <= 800 (or <= previous
count for grandfathered files).

### Function count ceiling: 20 exported functions per file `[enforced]`

No single file may define more than 20 exported functions or methods. If
exceeded, split into a focused file within the same package.

`orchestrator.go` (2 exported functions, down from 84) is grandfathered under
the same shrink-only rule.

**Compliance test:** `grep -c '^func ' file.go`; must be <= 20 (or <= previous
count for grandfathered files).

### Package import ceiling: 6 internal imports `[enforced]`

No package may import more than 6 other `internal/` packages. If exceeded,
the package is accumulating too many responsibilities — extract a sub-concern
into its own package or push logic down to dependencies.

`orchestrator` (11 unique internal imports, grandfathered at baseline 11) must not add more.

**Methodology:** The ceiling counts *unique import paths* per directory, not total
occurrences across files. This is equivalent to `grep "internal/" *.go | sort -u | wc -l`.
Splitting a file into multiple focused files within the same package does not increase the
import count if the same import paths are reused — only genuinely new dependencies raise it.

**Current baselines (unique paths, non-test files):**

| Package            | Unique internal imports |
|--------------------|------------------------|
| `internal/agent/`  | 5                      |
| `internal/tui/`    | 6 (at ceiling)         |
| `internal/orchestrator/` | 10 (grandfathered, baseline 11) |

**Compliance test:** Count distinct `internal/` import lines across a package's
non-test source files (`grep "internal/" *.go | sort -u | wc -l`);
must be <= 6 (or <= previous count for grandfathered packages).

---

## Formatting

### gofmt compliance: 100% `[enforced]`

All `.go` files must pass `gofmt -l` with no output. Do not commit
unformatted code.

**Compliance test:** `gofmt -l ./internal/ ./cmd/` returns no results.

---

## Duplication

### Search before creating `[not yet enforced]`

Before writing any helper function in a test file, check `internal/testutil/`
for an existing implementation. If one exists, import it rather than creating
a local copy.

**Compliance test:** No two test files contain the same helper function body
(e.g., `setupBareRepo`, `addWorktree`, `commitFile`, `newTestDB`).

### Three-copy threshold `[not yet enforced]`

If the same pattern (function body, struct layout, boilerplate block) exists in
3+ locations, extract it before adding a 4th. The extraction must happen in the
same commit as the new usage.

**Compliance test:** `grep` for the pattern across the codebase; count must stay
below 3.

### testutil is the single source for test infrastructure `[enforced]`

All test database creation must use `testutil.NewTestDB` or
`testutil.NewSharedTestDB`. All git repo setup must use `testutil.SetupBareRepo`,
`testutil.AddWorktree`, `testutil.CommitFile`. Do not define local versions of
these functions in test files.

**Compliance test:**
```bash
# DB init outside testutil
grep -rn 'gorm.Open(sqlite' internal/ --include='*_test.go' | grep -v testutil/
# Git helpers outside testutil
grep -rn 'func setupBareRepo\|func initBareRepo\|func addWorktree\|func commitFile' \
  internal/ --include='*_test.go' | grep -v testutil/
```
Both must return no results.

---

## Interfaces & Coupling

### Interfaces at consumption sites `[not yet enforced]`

When a package depends on a collaborator that it needs to mock or swap in tests,
define an interface in the consuming package, not the providing package. Do not
depend on concrete types from other packages when an interface would suffice.

**Compliance test:** Count interfaces in the codebase; any new external
dependency (runner, manager, supervisor) should have a corresponding interface
at the consumption site.

**Current state:** 2 interfaces exist:
- `mergeWorktreeClient` in `merge.go`
- `TUIOrchestrator` in `tui/orchestrator.go` — 21-method interface
  decoupling the TUI dashboard from `*orchestrator.Orchestrator`

---

## Constants & Magic Numbers

### No bare numeric literals in business logic `[not yet enforced]`

Thresholds, timeouts, retry counts, and percentage values must be defined as
named constants with a comment explaining the choice. Do not use bare numeric
literals for these values.

**Compliance test:** `grep` for bare numbers used as timeouts, retry limits, or
thresholds in non-test `.go` files; new occurrences must use named constants.

Context thresholds (`contextFixerPct`, `contextStopPct`, `contextWarnPct`,
`fixerEscalatePct`) are now named constants/fields. Remaining cases are
file-permission literals and display thresholds.

---

## Models

### No duplicate GORM hooks `[enforced]`

GORM lifecycle hooks (BeforeCreate, BeforeUpdate, etc.) that share identical
logic across model types must be consolidated. Use either a shared embedded
struct with a single hook or a GORM callback registered once at DB init time.

**Compliance test:** `grep -c 'func.*BeforeCreate' internal/model/models.go`;
should be 1 (shared) not 6 (per-type).

All GORM hooks are now consolidated.

---

## Test Infrastructure

### Test factory functions in testutil `[enforced]`

Common test entity creation (projects, tasks, agents) must use shared factory
functions from `internal/testutil/`. Do not define `createTestProject`,
`createTestTask`, or equivalent functions in individual test files.

**Compliance test:**
```bash
grep -rn 'func createTest\|func newTest' internal/ --include='*_test.go' | grep -v testutil/
```
Must return no results.

### Minimize real I/O in unit tests `[not yet enforced]`

Tests that only assert on database state should not create real git worktrees.
Reserve git worktree setup for integration tests that exercise actual git
operations.

**Compliance test:** Manual review — if a test's assertions only check DB
records, it should not call `SetupBareRepo` or `AddWorktree`.

---

## Graduation Path

When a constitution rule can be reliably detected:

1. Add the constraint to `.drem/constraints.toml` using the appropriate type
   (`command`, `max_lines`, `max_matches`, `no_match`, or `depth`)
2. Mark the rule in this document as `[enforced]`
3. The constraint system automatically enforces the rule at:
   - **Plan validation** -- warns when plans target constrained files
   - **Post-agent gate** -- checks file-based constraints after each agent merge
   - **Integration gate** -- runs all constraints before testing_ready
4. Run manually: `bash scripts/check_constitution.sh`

The rule stays in this document for context, but `.drem/constraints.toml` is now
the authoritative enforcement definition.
