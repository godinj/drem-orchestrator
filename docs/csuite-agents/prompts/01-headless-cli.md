# Agent: Headless CLI Subcommand

You are working on the `master` branch of drem-orchestrator, a Go-based multi-agent task orchestrator.
Your task is implementing the headless CLI: a new `drem cli` subcommand that provides programmatic access to orchestrator operations for C-Suite agents and temp workers.

## Context

Read these specs before starting:
- `docs/csuite-agents/prd-csuite-agents.md` (section: "Headless Orchestrator CLI" — full command list and output requirements)
- `cmd/drem/main.go` (current CLI entry point — flag-based, no subcommands yet)
- `cmd/drem/config.go` (Config struct and LoadConfig — you need DatabasePath)
- `internal/db/db.go` (Init function — opens SQLite with WAL mode)
- `internal/model/models.go` (Task, Agent, TaskEvent, TaskComment structs — your query targets)
- `internal/model/enums.go` (TaskStatus, AgentType, AgentStatus constants — filter values)
- `internal/model/bugreport.go` (BugReport struct — for failure context)
- `internal/testutil/testutil.go` (NewTestDB, CreateProject, CreateTask, CreateAgent — use these in tests)

## Deliverables

### New files

#### 1. `internal/cli/cli.go`

Top-level CLI dispatcher. Parses the subcommand and delegates to handler functions. All handlers receive a `*gorm.DB` and write to an `io.Writer` for testability.

```go
package cli

// Run parses os.Args[1:] (after "cli" is consumed) and dispatches to the
// appropriate handler. Returns an error for unknown subcommands or handler failures.
func Run(db *gorm.DB, args []string, w io.Writer, jsonMode bool) error
```

**Read subcommands:**

- `tasks [--status=STATUS]` — List tasks. Columns: ID (first 8 chars), Status, Category, Title. If `--status` given, filter by that status (use `model.ParseTaskStatus` to validate). Order by UpdatedAt desc.
- `task <id>` — Show single task detail. Include: full ID, title, description, status, category, complexity, priority, labels, worktree branch, phase, created/updated timestamps. If task has subtasks, list them (ID prefix + status + title). If task has an assigned agent, show agent type + status. If task has comments, show them (author + timestamp + body).
- `agents [--status=STATUS]` — List agents. Columns: ID (first 8 chars), Type, Status, CurrentTaskID (first 8 or "—"), HeartbeatAt (relative, e.g., "2m ago" or "—"). Filter by status if given. Order by UpdatedAt desc.
- `failures [--since=DURATION]` — Show recent failures. Query tasks with `status = 'failed'` and agents with `status = 'dead'`. Default `--since` is 24h. For each failed task: ID prefix, title, last event details. For each dead agent: ID prefix, type, last task title. Order by most recent first.
- `stats` — Operational summary. Counts: tasks by status (only show statuses with >0 count), agents by status, total tasks, failure rate (failed / total * 100, 1 decimal). Also show: tasks completed in last 24h, agents spawned in last 24h.

**Write subcommands:**

- `file-task --title=TITLE --description=DESC [--project-id=UUID]` — Create a new task in `StatusClassifying`. If `--project-id` omitted, use the first project in the database. Print the new task ID.
- `comment <task-id> --body=BODY` — Add a TaskComment with `Author: "csuite"` to the specified task. Task ID can be a prefix (minimum 8 chars) — query with `LIKE 'prefix%'`. Print confirmation with comment ID.

**Output modes:**

- Default: human-readable aligned text (use `text/tabwriter` for tables).
- `--json`: JSON output. Tasks/agents as JSON arrays, stats as a JSON object. Use `encoding/json` with `MarshalIndent`.

#### 2. `internal/cli/cli_test.go`

Integration tests using `testutil.NewTestDB`. Seed the database with known state, run CLI handlers, verify output.

Required test cases:
- `TestTasksList` — Create 3 tasks with different statuses. Run `tasks`. Verify all 3 appear. Run `tasks --status=backlog`. Verify only matching tasks appear.
- `TestTaskDetail` — Create a task with subtasks and comments. Run `task <id>`. Verify all fields present.
- `TestAgentsList` — Create 2 agents (working, dead). Run `agents`. Verify both appear. Run `agents --status=working`. Verify filter works.
- `TestFailures` — Create a failed task and a dead agent. Run `failures`. Verify both appear. Run `failures --since=0s`. Verify empty (nothing failed in 0 seconds — this tests the duration filter).
- `TestStats` — Create mix of tasks and agents. Run `stats`. Verify counts match.
- `TestFileTask` — Run `file-task --title=X --description=Y`. Verify task created in DB with `StatusClassifying`.
- `TestComment` — Create a task. Run `comment <id> --body=Z`. Verify TaskComment in DB with author "csuite".
- `TestJSONOutput` — Run `tasks --json`. Parse output as JSON array. Verify structure.
- `TestTaskIDPrefix` — Create a task. Run `task <first-8-chars>`. Verify it resolves correctly.

All tests must use `testutil.NewTestDB`, `testutil.CreateProject`, `testutil.CreateTask`, `testutil.CreateAgent`.

#### 3. `cmd/drem/cli_cmd.go`

Thin entry point that wires the CLI into `main.go`. This file handles:
- Detecting when `os.Args[1] == "cli"` (before flag.Parse consumes args)
- Loading config (reuse `LoadConfig`)
- Opening the database via `db.Init(cfg.DatabasePath)`
- Parsing `--json` flag
- Calling `cli.Run(db, remainingArgs, os.Stdout, jsonMode)`
- Exiting with appropriate code

### Migration

#### 4. `cmd/drem/main.go`

Add a subcommand check at the very top of `main()`, before `flag.Parse()`:

```go
// Handle subcommands that bypass the flag-based TUI entry point.
if len(os.Args) > 1 && os.Args[1] == "cli" {
    runCLI()
    return
}
```

The `runCLI()` function lives in `cli_cmd.go` (same package). It loads config, opens DB, and delegates to `internal/cli`.

## Scope Limitation

- Do NOT modify any existing internal packages.
- Do NOT add new dependencies to `go.mod` — use only `text/tabwriter`, `encoding/json`, `flag`, `fmt`, `io`, `os`, `strings`, `time` from stdlib plus existing GORM/model imports.
- Do NOT implement any C-Suite agent logic — this is purely a read/write CLI for the existing database.
- The `file-task` command creates tasks in `StatusClassifying` — it does NOT trigger any orchestrator processing.

## Conventions

- Module path: `github.com/godinj/drem-orchestrator`
- Use `internal/cli/` for the new package (not `cmd/drem/` — keep the cmd package thin)
- No file may exceed 800 lines
- No more than 20 exported functions per file
- No more than 6 internal imports per package
- All test DB creation must use `testutil.NewTestDB`
- `gofmt` compliance required
- Build verification: `cd /home/godinj/git/drem-orchestrator.git/master && go build ./... && go test ./internal/cli/ && go vet ./...`
