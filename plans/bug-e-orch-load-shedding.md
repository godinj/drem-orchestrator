# Bug E — Orchestrator load-shedding + observability hardening

**Status**: W1 (load-shedding middleware + /tasks DB-query timeout) and
W2.1 (tasks composite index) merged 2026-04-21 per §Sequencing item 1.
W3 (pprof + SIGUSR1), W4 (log sampling), and W5 (healthcheck +
sightingProbe) still pending.

**Origin**: 2026-04-21. Seth (CTO) produced this prevention list during his RCA
of the apparent "orch wedge" incident
(`~/.drem-csuite/seth/outbox/20260421T224500Z-seth-to-kyle-orch-wedge-investigation.md`).
His primary hypothesis (H1 — `watchDockerEvents` hot-loop) was wrong — the real
cause was a client-side TUI retry storm, captured in
`plans/tui-retry-storm-prevention.md` and fixed in commit series
`25f2d95..c97667a`. But Seth's broader prevention recommendations are still valid:
**a well-behaved server should not be DDoS-able by one confused client**, and
today's orch was. The TUI fix prevents the specific client from re-offending;
Bug E makes the server itself DDoS-resistant and observable next time.

## Problem statement

On 2026-04-21 a single misbehaving TUI client (pid 1240083, 441% CPU) pushed
the orchestrator into a state where:

- `/tasks` returned HTTP 500 with ~5–28 s latency, climbing to connection-reset
- orch sustained 3.8 cores of CPU burn and 7.2 GiB RAM
- `drem.log` ballooned from normal to 1.37 GiB, dominated by 1406
  `GET /tasks status=500 duration_ms=~7000` entries per 200 KB of tail
- request rate peaked at **495 req/s** at 23:01:54Z from a single localhost
  client
- the scheduler tick was starved enough that v17 canary subtasks sat in
  `backlog` for minutes despite workers being free

The system recovered instantly once the client was killed. That recovery
behaviour is exactly what we want — the pathology is that **one client can
starve the scheduler in the first place**. No request budget, no timeout on
the DB query path, no observability into which endpoint was under load, no
healthcheck that would have auto-restarted a wedged orch.

## Goals

1. **Load-shedding** — orch rejects overflow requests fast rather than
   queueing them into unbounded latency. One confused client cannot starve
   any other consumer.
2. **Timeouts** — no single DB query or handler can hold a goroutine
   indefinitely.
3. **Observability** — distroless orch has zero live-debugging surface today.
   Fix that without compromising image size discipline.
4. **Log hygiene** — errors inside for-loops should not produce gigabyte log
   files in minutes.
5. **Ops** — missing docker healthcheck + restart policy is a foot-gun every
   time we hit a wedge class that today's changes don't prevent.

## Non-goals

- Multi-tenant auth or token-scoped rate limits. Localhost-bound single-operator
  orch is the current model; per-tenant quotas are a future-work pivot.
- General metrics+dashboard build-out. We scope latency and error-rate
  counters for the mutated endpoints only; a full metrics surface is Alex's
  territory (see §Alex-task below).
- Log aggregation system. Sampling at the source is enough; shipping logs
  somewhere fancy is later.

## Workstream

The list breaks into five workstreams. Each is independently landable; within
a workstream, the items are roughly ordered by impact.

### W1 — Load-shedding (highest priority) — MERGED 2026-04-21

These items make a confused client harmless. All three shipped as
commit series `test → feat → feat → feat → docs` on top of master tip
`7a1babb`.

1. **HTTP server-side max-in-flight middleware** — global cap on concurrent
   in-flight requests. Default `N=64`. Overflow returns HTTP 503 immediately
   with `Retry-After: 1`. Counter is atomic. Middleware wraps the top-level
   mux. Prevents any single endpoint from soaking up all goroutines.
   Env: `DREM_ORCH_MAX_INFLIGHT`. Lives in
   `internal/orchhttp/middleware.go`.
2. **Per-endpoint concurrency cap on `/tasks`** — tighter sub-budget
   (default `8` concurrent). Same 503 response on overflow. Applied because
   `/tasks` is the most-polled expensive endpoint and today's incident proved
   it the right chokepoint. Applies BEFORE the middleware counter so
   `/tasks`-overflow doesn't consume global budget. Env:
   `DREM_ORCH_TASKS_MAX_INFLIGHT`.
3. **`context.WithTimeout(5s)` on the `/tasks` DB query** — hard ceiling. If
   SQLite takes more than 5s to list tasks, return 503 with a clear log line
   rather than stretching handler latency to 28 s. Matches the TUI's
   client-side `dataFetchTimeout` — a handler slower than its client's
   timeout is never useful. Env: `DREM_ORCH_TASKS_QUERY_TIMEOUT_MS`
   (milliseconds; unset or `0` = 5000). Lives in `handleListTasks` in
   `internal/orchhttp/handlers_public.go`.

### W2 — DB hygiene

1. **Composite index on `(project_id, created_at DESC)` on `tasks` table**
   — MERGED 2026-04-21. Today's 4.3 GiB DB's cold scan is the reason the
   `/tasks` query is expensive. Migration:
   `CREATE INDEX IF NOT EXISTS idx_tasks_project_created ON tasks(project_id, created_at DESC)`.
   Scope delta vs original plan: this repo has no `internal/db/migrations/`
   directory — the convention in `internal/db/db.go` is raw `db.Exec`
   statements appended after `AutoMigrate` (see the existing `UPDATE agents
   SET tmux_session` back-fill). The index migration follows that
   pattern. Test: `TestTasksProjectCreatedIndexUsed` in
   `internal/db/db_index_test.go` EXPLAIN-QUERY-PLANs the ListTasks shape
   and asserts `idx_tasks_project_created` appears; before the migration
   the plan was `SEARCH tasks USING INDEX idx_tasks_project_id` +
   `USE TEMP B-TREE FOR ORDER BY`, now it's a direct index walk.
2. **`PRAGMA wal_checkpoint(TRUNCATE)` on orch shutdown** — add to the
   graceful-shutdown path in `cmd/drem/main.go` (or wherever orch's signal
   handler lives). Today's WAL grew to 782 KB which is fine, but across many
   deploys + crashes the WAL has been observed much larger. Truncate on
   clean shutdown; crash path is unchanged.

### W3 — Observability (distroless-compatible)

1. **Always-on pprof, gated to localhost + env flag** — mount `net/http/pprof`
   handlers on a separate listener bound to `127.0.0.1:6060` (different port
   from the main API). Gate with `DREM_PPROF=1` env var. Zero-dep: stdlib
   only. Distroless can run the handler; the blocker today is there's no
   shell in the container to curl it, so also:
2. **`SIGUSR1` handler → `runtime.Stack()` to a file** — register a signal
   handler that writes a goroutine dump to `/tmp/drem-goroutines-<ts>.log`
   whenever SIGUSR1 arrives. Host operator can `docker kill --signal=USR1
   drem-orchestrator-orch-1` and then inspect the file via bind-mount.
   No shell needed inside the container. Zero-dep: stdlib.
3. **Per-endpoint latency + error-rate counters — Alex task** — owned by
   Alex (CPO) per his operator-metrics surface responsibility. Basic
   counters: `orch_request_total{endpoint, status_bucket}`,
   `orch_request_duration_seconds_bucket{endpoint}`. Expose on the pprof
   listener above for minimum viable metrics. This entry is a hand-off
   placeholder; Alex drafts the spec.

### W4 — Log hygiene

1. **Log sampling inside for-loops** — wrap error-rate-prone loop logs with
   a sampling wrapper that emits at most once per N occurrences or per M
   seconds. Today's `/tasks status=500` repeated >1000× per 200 KB of tail
   is the canonical anti-pattern. Apply to `orchhttp` handlers first, then
   sweep the rest of `internal/` for `for { ... log.X(...) }` shapes.
2. **Bounded `drem.log` via log rotation** — outside scope of Go code;
   document in install docs that `/var/lib/drem/data/drem.log` should rotate
   (`logrotate` config template or a systemd service template). Minimum
   viable guidance.

### W5 — Ops

1. **Docker healthcheck** in compose for `drem-orchestrator-orch-1`: `curl
   --fail http://127.0.0.1:8080/projects || exit 1`, every 30s, 3 retries.
   Paired with `restart: on-failure` in the compose service. An orch that
   stops responding for 90 s gets restarted automatically, which would have
   papered over today's incident entirely (well, until the client came back
   and wedged the replacement — which is why W1 is the real fix, not just
   this).
2. **Cross-process wiring of `sightingProbe`** — carried over from Bug D
   (`plans/agentmon-observability-hardening.md`? — check location). The
   `ContainerSightingProbe` hook is plumbed but `sightingProbe` stays `nil`
   until something wires agentmon's `HasSeen` across process boundaries.
   Not strictly load-shedding, but groups cleanly here as "latent gap in
   observability wiring."

## Sequencing

Recommended order:

1. **W1 + W2.1 (the tasks index)** together as a single PR. Load-shedding + the
   query that load-shedding is protecting. High-confidence, ~200-300 LOC.
   **DONE 2026-04-21.** Landed as five commits (test → feat(db) → feat(middleware)
   → feat(timeout) → docs), ~360 LOC prod + 390 LOC test. Env knobs
   finalised as `DREM_ORCH_MAX_INFLIGHT`, `DREM_ORCH_TASKS_MAX_INFLIGHT`,
   and `DREM_ORCH_TASKS_QUERY_TIMEOUT_MS`.
2. **W3.1 + W3.2 (pprof + SIGUSR1)** as a small observability PR. ~80 LOC.
   Low risk, high debugging value next time something goes sideways.
3. **W4.1 (log sampling)** as a focused PR across `orchhttp` + any obvious
   offenders. ~100 LOC including tests. Low risk.
4. **W5.1 (healthcheck + restart policy)** as a compose-config PR. Tiny.
   Ships whenever.
5. **W5.2 (sightingProbe wiring)** separately — revisit the original Bug D
   plan doc; this is its natural follow-up.
6. **W3.3 (latency metrics)** — Alex drafts spec, Kyle dispatches impl. No
   Bug E dependency; runs on Alex's cadence.

## Priority after Phase 1+2 of the containerization pivot

Bug E ships AFTER `plans/orch-api-gate-mutations.md` Phase 1+2 merge, because:

- Bug E's W1 (load-shedding middleware) wraps the HTTP mux. Phase 1 adds new
  POST endpoints to the same mux. Sequencing Phase 1 first avoids churn in
  the middleware code.
- The `/tasks` endpoint concurrency cap (W1.2) is the most-impactful
  chokepoint — it protects the mutation endpoints Phase 1 adds, so landing
  Phase 1 first means those mutations benefit from the load-shedding from
  day one.
- Both W1.3 (DB query timeout) and W2.1 (tasks index) have zero coupling to
  Phase 1 and could move earlier if we had cycle pressure.

## Regression-proof tests

Each workstream MUST include a test that would have caught today's incident
in isolation:

- **W1**: a test that fires 200 concurrent `/tasks` requests and asserts
  overflow returns 503 within 100 ms, not stretched latency. Count goroutines
  at steady state to prove they don't accumulate.
- **W2.1**: a bench test on `ListTasks` that proves the query uses the new
  index (via `EXPLAIN QUERY PLAN`) and runs in < 50 ms at 100k row scale.
- **W3.1**: a smoke test that pprof endpoint returns 200 when `DREM_PPROF=1`
  and 404 when unset.
- **W3.2**: a unit test that fires SIGUSR1 to a test orch process and asserts
  the dump file appears at the expected path.
- **W4.1**: a unit test of the sampling wrapper proving at-most-N-per-interval
  behaviour.
- **W5.1**: no test — compose-config change.

## Constitution notes

- Load-shedding middleware lives in `internal/orchhttp/` — check import-cap
  headroom before landing. Don't pull in a third-party rate-limit library;
  stdlib + atomic counter is enough for the shape we need.
- The pprof listener lives in `cmd/drem/main.go` or a new
  `internal/orchhttp/pprof.go` — either works; prefer the latter if adding
  lines to `main.go` pushes file size.
- Log sampling wrapper goes in `internal/logging/` if that package exists,
  else `internal/orchhttp/` as a handler-only utility.

## What this plan does NOT do

- Does not introduce server-side retry or queueing. Overflow is fast-fail.
  Retry is the client's job (see `plans/tui-retry-storm-prevention.md`).
- Does not add authentication. Localhost binding remains the only trust
  boundary. Revisit when orch leaves single-operator land.
- Does not fix the `drem cli` silent-fail foot-gun discovered during the
  2026-04-21 v17 approval attempt. That bug disappears entirely once Phase 2
  of the API pivot lands (the CLI becomes a thin HTTP client and the
  `orchestrator.NewForCLI` dead code is removed).

## Task tracking

Originally intended to be filed as task #13 via `drem cli create-task`. That
path shares the same double-writer foot-gun we're pivoting away from; the
task will be filed via the new `POST /tasks` API after Phase 1+2 land. For
now, tracked as an entry in the restart-context carryover list.

— kyle, 2026-04-21
