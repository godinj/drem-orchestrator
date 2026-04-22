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

## Mutating actions

The TUI splits its mutating actions into two groups based on what
Phase 3 of the orch API gate-mutation pivot converted to HTTP:

1. **Gate mutations** (approve plan, reject plan, test-review approve /
   reject, test pass / fail, clarification answer) route through
   `pkg/orchclient` over HTTP. The Phase-3 adapter `*tui.HTTPOrchestrator`
   satisfies `TUIOrchestrator`'s seven `Handle*` methods by POSTing to
   `/projects/{name}/tasks/{id}/{approve,reject,pass,fail,answer}` on the
   orchestrator's HTTP API. Typed errors from `orchclient`
   (`ErrWrongStatus`, `ErrNotFound`, `ErrServer`, transport errors) are
   surfaced verbatim so the TUI's status line can pattern-match via
   `errors.As`. The containerized orchestrator is the single writer to
   the project DB — this closes the double-writer escape hatch that the
   old in-process `drem cli approve` path exposed. See
   `plans/orch-api-gate-mutations.md`.

2. **Non-gate mutations** (pause, resume, retry, comment, delete,
   `Spawn*Session`, `IntegrationWorktreePath`, …) still go through the
   local path: `*tui.HTTPOrchestrator` delegates these to an in-process
   `*orchestrator.Orchestrator` via `WithFallback`. Remote-orchestrator
   support for these actions is a follow-up that requires authenticated
   write endpoints on the HTTP API — tracked in
   `docs/prd-containerization.md`.

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
