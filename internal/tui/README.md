# internal/tui

Bubble Tea dashboard for the Drem Orchestrator. Renders tasks, agents,
task detail, bug reports, experiments, and C-Suite snapshots.

## Data sources

The TUI consumes two distinct surfaces:

1. `DataSource` (`datasource.go`) — the read-only HTTP view of the
   orchestrator provided by `pkg/orchclient`. Populates the main
   dashboard (task list, agent list, event refresh, log stream, worker
   history). The production implementation is `HTTPDataSource`.

2. `*gorm.DB` — the local SQLite database. Still used for feature
   surfaces whose public HTTP projection has not yet landed:

   - Bug reports screen (`bugreports.go`, `bugreport_actions.go`)
   - Experiments screen (`experiment_view.go`)
   - Task detail enrichment: subtasks, comments, dependency titles, and
     the assigned agent snapshot (`data_cmds.go`, `refreshData`)

The `--orch-url` flag on `drem` (see `cmd/drem/main.go`) picks the HTTP
endpoint that `HTTPDataSource` talks to. Default: `http://127.0.0.1:<orch_http_port>`
where `orch_http_port` comes from `drem.toml`.

## Connection loss

When a DataSource call fails, the Update loop records the error in
`Model.dataErr` and schedules a retry on an exponential-backoff ladder
(`1s → 2s → 5s → 10s` cap; see `nextDataBackoff`). The last-good
snapshot stays on screen and a "connection lost — retrying" banner
appears beneath the detail panel.

## Mutating actions (pause, resume, retry, comment, approve plan)

The orchestrator's public HTTP API is read-only. Mutating actions
triggered from the TUI still go through the local path (in-process
calls into `internal/orchestrator` via the `TUIOrchestrator` interface,
or `drem cli <subcommand>` shell-outs). Remote-orchestrator support
for these actions is a follow-up that requires authenticated write
endpoints on the HTTP API — tracked in `docs/prd-containerization.md`.

## Tests

- `datasource_test.go` exercises `HTTPDataSource` against an
  `httptest.NewServer` with canned JSON.
- `datasource_backoff_test.go` covers the `nextDataBackoff` ladder.
- `model_datasource_test.go` wires a fake `DataSource` into the Model
  and asserts that tasks/agents flow through, that errors surface as
  `dataErrMsg`, and that banners don't clobber the last-good snapshot.
- The pre-existing sub-model tests (`board_test.go`, `agents_test.go`,
  `detail_test.go`, …) continue to pass because they operate on the
  model.Task/model.Agent types the DTO adapter produces.
