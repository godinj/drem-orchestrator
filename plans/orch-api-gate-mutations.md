# Orch HTTP Gate Mutation API — Design & Plan

Status: **Phase 1+1b+1c+2 merged, 2026-04-21.** Phase 3 pending (TUI
in-process handler removal). Phase 1 endpoints landed on
`feat/orchhttp-gate-mutations`; Phase 1b added `pkg/orchclient` gate
mutation methods; Phase 1c added `orchclient.ResolveTaskID`. Phase 2
rewrote `drem cli approve/reject/pass/fail/answer` to POST through
the HTTP API and deleted `internal/orchestrator/cli.go`
(`orchestrator.NewForCLI`) — the double-writer escape hatch is now
closed at the type level.

## 1. Why

Before this change, `drem cli approve/reject/pass/fail/answer` opened
the bind-mounted `drem.db` directly from the host and spun up a
*second* orchestrator via `orchestrator.NewForCLI` (see
`cmd/drem/cli_cmd.go:65-72`). The containerized orchestrator was
already attached to the same SQLite file as a writer. Two writers on
one SQLite DB is undefined behaviour; in production on 2026-04-21 this
manifested as `drem cli approve <task-id>` silently returning exit 0
without transitioning the task — a direct `sqlite3` query confirmed
no row change.

The fix closes the escape hatch: the containerized orchestrator is
the **single writer** to the project DB, and all gate mutations flow
through its HTTP API. The read-only endpoints (Kyle, TUI) and the
write endpoints (gate mutations) share the same `*gorm.DB` handle, so
reads following writes are trivially consistent — this is the
regression-proof-point covered by `TestApproveThenListTasksReadAfter
Write` in `internal/orchhttp/gate_handlers_test.go`.

## 2. Endpoints

All five endpoints live on the orch container's public HTTP API. The
`{id}` segment is always the **full UUID** — callers (Phase 2's CLI)
resolve short-prefix → UUID by calling `GET /projects/{name}/tasks`
first, keeping the server API unambiguous.

| Method | Path | Body | On-success transitions |
|--------|------|------|------------------------|
| `POST` | `/projects/{name}/tasks/{id}/approve` | *(empty)* | `plan_review → in_progress` *(or `test_writing` when TDD subtasks exist)*; `test_review → in_progress` |
| `POST` | `/projects/{name}/tasks/{id}/reject` | `{"reason":"..."}` (optional) | `plan_review → rejected/planning`; `test_review → test_writing` with feedback persisted |
| `POST` | `/projects/{name}/tasks/{id}/pass` | *(empty)* | `testing_ready → merging` |
| `POST` | `/projects/{name}/tasks/{id}/fail` | *(empty)* | `testing_ready → in_progress` |
| `POST` | `/projects/{name}/tasks/{id}/answer` | `{"body":"..."}` (required, non-empty) | `needs_clarification → planning` (when all Qs answered) |

### Request shape

- `Content-Type: application/json` recommended but not required when
  the body is empty.
- `reject` body is optional for both gates. A missing body is
  equivalent to `{"reason":""}`.
- `answer` body is required; an empty string for `body` yields 400.

### Response shape (success)

`200 OK` with a JSON-encoded `orchdto.TaskDTO` reflecting the
post-transition row. Fields: `id`, `title`, `status`, `created_at`,
`updated_at`, `assigned_worker`. `Content-Type: application/json`.

### Error response shape

All error responses use `Content-Type: application/json` with a
uniform envelope:

```json
{"error": "human-readable description"}
```

| Status | Meaning | When |
|--------|---------|------|
| `400` | Malformed request | Bad UUID in URL; malformed JSON; missing required body field (e.g. `answer` without `body`) |
| `404` | Not found | Project name doesn't match server's project; task row doesn't exist |
| `409` | Conflict | Task exists but is in a status this verb can't transition from. Body shape: `task in status "X", expected one of [Y, Z]` |
| `500` | Internal error | Orchestrator method returned an error, or DB failure |
| `503` | Not configured | Server was constructed without `Server.Orch` wired (read-only dev mode) |

## 3. Server wiring

`internal/orchhttp/server.go` defines a package-local
`GateOrchestrator` interface (consumption-site rule) with the 7
handler methods used by the 5 endpoints. `*orchestrator.Orchestrator`
already satisfies this interface — no production-side changes to the
orchestrator package.

`Server.Orch GateOrchestrator` is optional. When nil, the gate
endpoints return 503. `cmd/drem/orchhttp_server.go` sets it to the
live in-process orchestrator at startup so the containerized
production server exposes the full write surface.

Tests inject a scripted fake that records calls and applies a
simplified status transition to the shared `*gorm.DB`. This keeps
handler tests fast (no orchestrator bootstrap) while proving the
handler → DB → handler re-fetch round-trip.

## 4. Phase boundaries

**Phase 1 (this change).** Server-side: endpoints, interface,
wiring, 15-case test matrix, docs. **Does not** touch
`cmd/drem/cli_cmd.go` or `internal/cli/gate_commands.go` — those stay
functional (if buggy) until Phase 2. The constitution's "additive
only" rule is honoured: existing routes unchanged.

**Phase 2.** Rewrite the CLI gate commands to POST against the
orchestrator HTTP API instead of opening the DB and calling
`orchestrator.NewForCLI`. Short-prefix resolution stays client-side
via `GET /tasks`. `orchestrator.NewForCLI` can then be deleted (last
caller gone). Expected commits: client HTTP helper extraction, CLI
command rewrite, removal of `NewForCLI`, removal of
`gateCommands`/`tui.TUIOrchestrator` wiring in `cli_cmd.go`.

**Phase 3.** The TUI currently calls `orch.HandlePlanApproved(...)`
etc. directly via the in-process `TUIOrchestrator` interface. In
containerized mode the TUI runs as a pure HTTP client — already true
for read endpoints (`tui.NewHTTPDataSource`), but write paths still
assume in-process. Phase 3 switches those to HTTP POSTs against the
same endpoints Phase 2 introduced, so the TUI can run against a
remote orchestrator (e.g. the C-Suite web UI).

## 5. Non-goals

- **Auth.** The HTTP API runs on localhost in compose. No token
  middleware on the gate endpoints — same security model as the
  existing read endpoints. If Phase 4 opens the orch port to
  non-localhost, all write endpoints (gate + ingest) should gate on
  the same shared token, but that is a separate change with its own
  deployment implications.
- **Prefix resolution.** The server takes full UUIDs only. Keeps the
  API unambiguous and the server stateless about client UX.
- **WebSocket / SSE.** Per `docs/prd-containerization.md` the orch
  API is HTTP-only; follow-mode is chunked HTTP on `/logs`.

## 6. Rollout

Backward-compatible and additive. Existing clients (TUI reads, Kyle,
agentmon ingest) unaffected. The next Docker image build includes
the new endpoints; until Phase 2 CLI ships, the legacy `drem cli
approve` code path still exists but can be fixed incrementally or
left in place as a transition tool.

## 7. Test coverage summary

`internal/orchhttp/gate_handlers_test.go` covers:

1. `approve` happy path `plan_review → in_progress`
2. `approve` happy path `test_review → in_progress`
3. `approve` wrong status (409)
4. `approve` unknown task (404)
5. `approve` malformed UUID (400)
6. `reject` plan_review happy (no reason)
7. `reject` test_review happy (with reason forwarded)
8. `reject` missing body is 200 with empty reason
9. `answer` happy with body
10. `answer` missing/empty body → 400
11. `pass` happy
12. `fail` happy
13. Wrong project name → 404
14. Orchestrator-returned error → 500
15. End-to-end POST /approve then GET /tasks (read-after-write)

Plus `reject`/`pass`/`answer` 409 variants, malformed-JSON 400, and
the 503 path when `Server.Orch` is nil.
