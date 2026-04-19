# Agent: TUI HTTP API Migration

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent task orchestrator. Your task is Phase 3 integration work for the containerization initiative: migrate the Bubble Tea TUI dashboard from direct GORM/SQLite reads to orchestrator HTTP API calls via the `pkg/orchclient` library. This decouples the TUI from the database schema and lets it run against a remote orchestrator in the future.

## Context

Read these specs before starting:

- `docs/prd-containerization.md` (sections: "Orchestrator is the single state surface per project"; user stories 39, 40)
- `internal/tui/` (existing TUI — understand every place it currently touches `*gorm.DB` or model types directly)
- `pkg/orchclient/` (prompt 08 — the client you swap in)
- `pkg/orchdto/` (prompt 08 — the DTOs you render)
- `ARCHITECTURE.md`

## Dependencies

- Prompt 08 (`pkg/orchclient/` and `pkg/orchdto/`) — the client and DTOs

## Deliverables

### Migration

#### 1. TUI data fetch layer

Identify the current data-fetch seam in `internal/tui/` (likely a `dataSource`, `repo`, or `service` type the Bubble Tea model depends on). Replace its GORM-backed implementation with an `orchclient`-backed one.

- `type DataSource interface { ListTasks(ctx, filter) ([]orchdto.TaskDTO, error); ListWorkers(ctx) ([]orchdto.WorkerDTO, error); Events(ctx, since) ([]orchdto.EventDTO, error); WorkerHistory(ctx, id) (orchdto.WorkerHistoryDTO, error); StreamLogs(ctx, containerID) (io.ReadCloser, error) }`
- `type HTTPDataSource struct { Client *orchclient.Client }` — wires each method to the corresponding client call
- The existing GORM-backed data source is deleted in this prompt (unlike `internal/tmux/` and `internal/worktree/`, the TUI's direct DB path is dead code immediately after this swap; clean it up here rather than defer to prompt 17)

#### 2. Bubble Tea message plumbing

Current TUI likely fetches data in a `tea.Cmd` that calls the data source and returns a message. Keep the same pattern; only the data source changes.

Add graceful handling when the HTTP call fails (orchestrator down, network partition): show a "connection lost — retrying" banner and keep the last successful snapshot visible. Poll on a backoff (1s → 2s → 5s → 10s cap).

#### 3. Config / flag for orchestrator URL

Add a `--orch-url` flag to the TUI entry command (likely `drem tui` or `drem dashboard`). Default: `http://127.0.0.1:8080` (the local orchestrator). If the current TUI takes a `--db-path` or similar, keep it for one release but log a deprecation warning on startup.

If the project has a `drem.toml` field for the orchestrator URL (prompt 08 adds `orch_http_port`), read it as the source of truth; the flag overrides.

#### 4. Log-streaming viewer

If the TUI has a log-viewer pane today, it probably reads from local files. Swap to `DataSource.StreamLogs(ctx, containerID)` which proxies `docker logs` via the orchestrator's `GET /logs` endpoint. Handle chunked responses; cancel the stream when the user navigates away.

#### 5. Any mutating actions

If the TUI exposes buttons that mutate state (pause task, resume, retry, comment, approve plan), note that the HTTP API defined in prompt 08 is READ-ONLY. For mutations in the first cut, the TUI shells out to `drem task pause <id>` etc., which hit the local DB directly through the CLI command path (the CLI runs on the host and can still touch SQLite because it is colocated with the orchestrator).

Do not add write endpoints to the HTTP API in this prompt. Document in `internal/tui/README.md` that remote-orchestrator support for mutations is a follow-up that requires authenticated write endpoints.

### Tests

#### 6. `internal/tui/datasource_test.go`

Use `httptest.NewServer` with canned JSON responses. Assert:

- `HTTPDataSource.ListTasks` produces a request to `/projects/:name/tasks` with the expected query params and decodes the DTO correctly
- `HTTPDataSource.ListWorkers` likewise
- On HTTP 500, methods return a wrapped error that the TUI can display
- On context cancel mid-stream, `StreamLogs` closes cleanly

#### 7. `internal/tui/model_test.go`

The existing model tests should continue to pass after the data source swap. If they currently set up a real `*gorm.DB`, refactor them to use a fake `DataSource` that returns canned DTOs. The tests assert on rendered output, not on data-source internals.

## Scope Limitation

- **No UI changes.** Per PRD ("TUI rewrite" in Out of Scope): preserve the existing visual layout, key bindings, and navigation. Only the data source changes.
- **No new tabs or views.** If Kyle's `GET /world/summary` output is appealing in the TUI, that's a follow-up.
- **No authentication.** The TUI connects to a local orchestrator over `127.0.0.1`. Remote orchestrator support requires auth, which is out of scope here.
- **Do not remove `internal/worktree/` or `internal/tmux/`.** Prompt 17 handles those. If the TUI currently imports them for rendering worktree paths or tmux pane IDs, either stop rendering those columns or render a placeholder. The PRD makes worktrees and tmux panes architecturally obsolete.

## Performance

The TUI currently refreshes against a local SQLite file; HTTP round-trips to the same orchestrator process are roughly the same cost. Use a 1-second refresh by default (match existing cadence). If the TUI currently polls every 100ms, keep that cadence — the orchestrator HTTP layer is local loopback and can handle it. Profile before optimizing.

## Conventions

- Module path: `github.com/godinj/drem-orchestrator`
- Package name: `tui` (extending existing)
- File-length and function-count ceilings per `ARCHITECTURE.md`
- Tests: `testify/require`, `httptest.NewServer`, fake `DataSource`
- Build verification: `go build ./internal/tui/... ./cmd/drem/... && go test ./internal/tui/...`
- Manual verification: `go run ./cmd/drem tui --orch-url http://127.0.0.1:8080` against a running orchestrator — confirm tasks, workers, events all render
- Constitution check: `bash scripts/check_constitution.sh`
