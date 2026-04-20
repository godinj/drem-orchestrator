# Warm Planner Pivot — Implementation Plan

Status: **in progress.** 2026-04-20. Commits 1-4 landed (httpserver + drem-planner HTTP + real claude subprocess + orch plan_client).

Supersedes the spawn-on-demand planner that landed earlier today
(commits `c279f32`..`b2024ee`). The shipped design polled
`InspectWorker` for 60-180s from an orch goroutine per task — the
exact pressure we moved the classifier OUT of orch to avoid. Warm
is the correct shape; we pivot before the first live T2 canary.

## 0. What changes, what stays

Keep:
- Plan JSON schema + `validatePlanJSON` from `plan_dispatch.go`.
- `processPlanning` provider-branch (`provider=claude` routes to the
  new HTTP client; legacy runner path remains fallback).
- `MaxPlannerRetries`, `total_planner_spawns` task-context counter.
- `drem-planner` image concept — BUT rewritten from one-shot
  claude invocation to long-lived HTTP server that `exec`s claude
  per request.

Replace:
- `plan_dispatch.go` spawner+poll flow → HTTP client
  (`plan_client.go`), shape matches `classifyViaContainer`.
- `cmd/drem-planner/` from absent (was just a Dockerfile +
  entrypoint script) → Go HTTP server (mirrors `cmd/drem-classifier/`).
- `deploy/docker/planner.Dockerfile` from claude-CLI-entrypoint to
  multi-stage Go build + node + `@anthropic-ai/claude-code` in the
  runtime layer.
- `deploy/docker/context/planner-entrypoint.sh` — **delete.** The
  Go server handles request lifecycle inline.

Remove entirely:
- **All `ANTHROPIC_API_KEY` plumbing.** Per operator direction
  2026-04-20: "no API-key fallback — the whole reason we're using
  local inference is to limit Anthropic inference as much as we can
  and get more done." Planner auths via the operator's Claude Max
  subscription, **only**.
- `planner-template` stub in the per-project compose (not a per-task
  container anymore).

Add:
- **`internal/agent/httpserver/` package** — shared auth middleware
  (`Authorization: Bearer <DREM_AGENTMON_TOKEN>`), `/healthz` + `/metrics`
  handlers, `expvar` registration. Classifier + planner both adopt
  it. Prep will adopt when it lands.
- `drem-planner` as a long-lived service in
  `deploy/compose/global.yml` + per-project compose (no template-
  stub — it runs).
- Subscription-auth bind mount:
  `${HOME}/.claude/.credentials.json:/home/drem/.claude/.credentials.json:ro`.
  Same pattern csuite agents already use (see
  `~/.drem/projects/drem-orchestrator/compose.override.yml`). Fail
  loud at orch startup if the host file is missing.

## 1. Subscription-auth policy (strict)

Claude Max subscription = shared rate-limit pool across all consumers
(operator's interactive sessions, csuite agents, planner). Planner
competes for that pool; no separate API budget.

Implications baked into this plan:

- drem.toml `[agents.planner]` has **no `auth` knob.** Auth is
  always subscription, full stop.
- Planner container runs as UID 1000 (matches worker-base + host
  operator), home at `/home/drem`, so `~/.claude/.credentials.json`
  resolves without `CLAUDE_CONFIG_DIR` overrides.
- Bind-mount is **read-only.** Prevents the container from writing
  refresh tokens that the host then has to reconcile. Host claude
  CLI (interactive sessions) owns refresh; container reads the file
  fresh on each request.
- Startup validation: planner binary probes for
  `${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.credentials.json` on boot;
  if absent, log a loud error and exit 1. Compose restart-policy
  will keep retrying — operator sees the loop in `docker ps` and
  runs `claude login` on host to fix.
- Dispatch-side validation: `plan_client.Dispatch` checks container
  `/healthz` returns 200 before POSTing. Healthz only returns 200
  when credentials file is readable. Dispatch fails fast when creds
  are missing rather than hanging on an Anthropic-side 401.

No fallback. If you really want API-key access for an ad-hoc test,
set `ANTHROPIC_API_KEY` in the container's env manually and the CLI
picks it up — but the generated compose template never sets it, and
the binary doesn't probe for it. Out of the happy path by design.

## 2. Architecture

```
orch tick loop
  (tasks in status=planning, provider=claude)
    │
    │  POST /plan {task_id, task, project, worktree_path, comments, …}
    │  (Bearer DREM_AGENTMON_TOKEN)
    ▼
drem-planner container (drem-net :8090, warm, 1 replica)
    │
    │  (handler execs `claude -p --output-format json ...` as subprocess)
    │  subprocess reads ~/.claude/.credentials.json (RO bind-mount)
    │
    ▼
api.anthropic.com — Claude Opus (claude-opus-4-6), billed to subscription
    │
    │  plan.json in response (JSON mode)
    ▼
drem-planner handler (parses, validates against schema)
    │
    │  200 { task_id, plan: {subtasks, coverage, tdd_exceptions, …},
    │        tokens_in, tokens_out, duration_ms }
    │
    ▼
orch persists plan to task.Plan, advances toward plan_review
```

One container, serialized requests by default (no concurrent claude
subprocesses). Concurrency can be raised via a `-concurrency` flag
once rate-limit behavior is observed.

## 3. Shared `internal/agent/httpserver/` package

The classifier hand-rolled its auth middleware, healthz handler, and
expvar registration in `cmd/drem-classifier/server.go`. Planner needs
the same. Extract once, use twice. When prep lands, same substrate.

Shape:

```go
// internal/agent/httpserver/server.go
package httpserver

type Config struct {
    ListenAddr   string
    BearerToken  string               // DREM_AGENTMON_TOKEN
    HealthProbe  func(ctx) error      // what /healthz runs
    MetricsNS    string               // "drem_classifier" / "drem_planner"
}

type Server struct { /* …handlers map, mux, metrics… */ }

func New(cfg Config) *Server
func (s *Server) Handle(method, path string, h http.HandlerFunc)
func (s *Server) Run(ctx context.Context) error

// Standard counters published under MetricsNS:
//   {ns}_requests_total{result="ok|4xx|5xx"}
//   {ns}_duration_seconds_bucket
//   {ns}_upstream_up (0/1 from the cached health probe)
```

Migration: classifier commit 1 below extracts + adopts. Zero
behavior change. Planner builds on top in commit 2.

## 4. Files touched

### New files
- `internal/agent/httpserver/server.go`, `server_test.go` — shared
  HTTP framework.
- `cmd/drem-planner/main.go`, `server.go`, `server_test.go` — HTTP
  server, /plan handler that execs claude.
- `internal/orchestrator/plan_client.go`, `plan_client_test.go` —
  HTTP client for orch → planner. Replaces
  `plan_dispatch.go` spawner-poll logic.
- `plans/warm-planner-pivot.md` — this file.

### Modified files
- `cmd/drem-classifier/server.go`, `main.go` — adopt
  `internal/agent/httpserver/` (delete the inline auth + expvar).
- `cmd/drem-classifier/server_test.go` — fewer cases now; shared
  framework has its own tests.
- `deploy/docker/planner.Dockerfile` — rewritten. Multi-stage:
  stage 1 compiles the Go binary; stage 2 is
  `debian:bookworm-slim` + node + `@anthropic-ai/claude-code` +
  the Go binary + non-root `drem` user (UID 1000) +
  `safe.directory='*'`.
- `deploy/docker/context/planner-entrypoint.sh` — **deleted.**
- `deploy/compose/global.yml` — add `drem-planner` as a long-lived
  service, depends_on: nothing (Anthropic-only; no sglang link);
  healthcheck on `/healthz` every 30s; restart: unless-stopped;
  mount `${HOME}/.claude/.credentials.json:ro`.
- `internal/orchestrator/task_processing.go::processPlanning` —
  rewrite the container-path branch to call the new HTTP client.
  Legacy runner path stays.
- `internal/orchestrator/plan_dispatch.go` — **deleted.** Its
  validation logic moves to `plan_client.go`. Tests migrate.
- `internal/orchestrator/plan_dispatch_test.go`,
  `plan_routing_test.go` — partially delete (argv / exit-code /
  spawner tests gone), partially migrate (validation, routing
  branch tests keep their shape).
- `internal/spawner/images.go` — **remove** the `"planner"` entry
  (no longer spawned by the spawner). Add test asserting it's
  absent.
- `internal/spawner/images_test.go` — flip assertions.
- `internal/projects/templates/project-drem.toml.tmpl` — drop the
  `model`, `provider`, `effort` trio from
  `[agents.planner]` — they still belong (planner IS always opus +
  claude), but remove the `endpoint` default from the drem.toml if
  present; the compose `DREM_PLANNER_URL` env is the source of
  truth.
- `internal/projects/templates/project-compose.yml.tmpl` —
  - Remove `ANTHROPIC_API_KEY` passthrough on orch (gone entirely).
  - Remove `planner-template` stub (the actual service is in
    `deploy/compose/global.yml` now).
  - Add `DREM_PLANNER_URL: "http://drem-planner:8090/plan"` on orch.
- `internal/projects/template.go` — drop `PlannerImage` field if
  unused; drop `DefaultPlannerImage` const if unused.
- `cmd/drem/project.go` — drop `PlannerImage` plumbing.
- `cmd/drem/config.go` — new `[agents.planner].endpoint` TOML key
  + `DREM_PLANNER_URL` env override. Mirrors the classifier pattern
  from `c7528c7`.
- `docs/containerization/install.md` — restructure:
  - "Spawn-on-demand agents" section shrinks to merger-only.
  - "Warm direct agents" section gains planner.
  - New "Claude subscription auth" subsection: documents the
    credentials bind-mount, `claude login` prerequisite, the
    absence of API-key fallback and why.
  - Catalog count stays 20 (drem-planner is still a built image).
- `plans/containerization.md` — flip the planner acceptance-
  criteria bullet wording from spawn-on-demand to warm HTTP.
- `plans/warm-direct-planner.md` — status line flipped to
  "superseded by plans/warm-planner-pivot.md". Don't delete the
  doc; the spawn-on-demand analysis is still useful reference for
  a future heavy-compute role that does suit that pattern.

## 5. HTTP surface of `drem-planner`

### POST /plan

Request:
```json
{
  "task_id": "...",
  "task":         { /* full Task row */ },
  "project":      { /* full Project row */ },
  "worktree_path": "/home/godinj/git/drem-orchestrator.git/feature/<feature>/integration",
  "comments":     [...],
  "target_coder": {
    "provider": "sglang-direct",
    "model":    "gemma4-26b"
  },
  "effort": "high"
}
```

Response 200:
```json
{
  "task_id":     "...",
  "plan":        { "subtasks": [...], "coverage": [...], "tdd_exceptions": [...], "assumptions": [...] },
  "tokens_in":   14328,
  "tokens_out":  4802,
  "duration_ms": 84312
}
```

Errors:
- 400 — bad request shape, worktree_path escape, missing fields.
- 401 — bad/missing bearer token.
- 409 — plan.json validation failed after retries.
- 502 — Anthropic upstream failure.
- 504 — claude CLI timed out (default 5 min per request).
- 500 — planner bug.

Plan is returned **inline** in the response body (no plan.json on
shared filesystem). Different from the shipped spawn-on-demand shape.
Reason: warm containers shouldn't couple via worktree files when an
HTTP response carries the payload cleanly.

### GET /healthz

200 when all of:
- credentials file readable.
- claude binary in PATH and `claude --version` returns in <2s.

503 otherwise. Orch's dispatch gates on `/healthz` before POSTing.

### GET /metrics

Via `internal/agent/httpserver/`'s expvar registration. Counters:
- `drem_planner_requests_total{result="ok|4xx|5xx"}`
- `drem_planner_duration_seconds_bucket`
- `drem_planner_claude_tokens_total{direction="in|out"}`
- `drem_planner_credentials_readable` (0/1)

## 6. Plan validation (unchanged from shipped)

The shipped `validatePlanJSON` logic is fine. Migrate to
`plan_client.go` as `validatePlan`. Rules:
- Top-level shape parses.
- `subtasks` non-empty.
- Every `tests_for` index is within `subtasks` bounds.
- Every `dependencies` index is within `subtasks` bounds.

TDD pairing (one test per impl, exactly one `tests_for`) happens
downstream in `plan_validation.go::validateTDD` when orch persists.
No change there.

## 7. Commit sequence

1. **`refactor(agent): extract shared httpserver package`** —
   `internal/agent/httpserver/` + classifier adoption. Zero behavior
   change. Tests migrate from classifier into the shared package.
2. **`feat(cmd): add cmd/drem-planner HTTP server`** — binary
   + handler + tests. Uses httpserver. Doesn't exec claude yet —
   stub returns a fixed plan.json from the request body's `task`
   so orch wiring can test without claude installed.
3. **`feat(cmd/drem-planner): exec claude CLI for plan generation`** —
   real subprocess invocation, timeout, token-count parsing, JSON
   extraction. Tests stub `exec.Command` so they don't need
   Anthropic creds.
4. **`feat(orch): replace plan_dispatch with HTTP plan_client`** —
   delete `plan_dispatch.go`, add `plan_client.go`, migrate
   `validatePlan`. `processPlanning` calls the HTTP client.
   `plan_routing_test.go` updated (spawner-path tests removed,
   HTTP-path tests added). `plan_dispatch_test.go` partially
   migrated.
5. **`feat(deploy): rewrite planner Dockerfile as long-lived server`** —
   multi-stage build, debian-slim runtime, node + claude-code,
   UID 1000 user, healthcheck. Delete
   `deploy/docker/context/planner-entrypoint.sh`.
6. **`feat(deploy): drem-planner in global compose with creds mount`** —
   add the service, `:ro` bind-mount of
   `${HOME}/.claude/.credentials.json`. Smoke assertion in
   `deploy/compose/compose_test.go`.
7. **`refactor(spawner): drop planner from images map`** — no
   longer a spawner-driven role. Test asserts map shape.
8. **`feat(projects): point drem.toml + compose at warm planner`** —
   remove `ANTHROPIC_API_KEY` env from orch compose, remove
   `planner-template` stub, add `DREM_PLANNER_URL` env, update
   `[agents.planner]`. Template tests updated.
9. **`docs(containerization): warm planner + subscription auth walkthrough`** —
   restructure install.md, document `claude login` prerequisite,
   explain the no-API-key stance.
10. **`docs(plans): supersede spawn-on-demand planner, mark pivot done`**.

~10 commits, ~1400 LOC net (a lot of deletion). ~2 days focused.

## 8. Rollout

1. Land commits 1-9 serially. Each commit builds + tests clean.
2. On host: `claude login` (one-time setup; operator already has this
   done since csuite agents use it).
3. Rebuild + push `drem-classifier:latest`, `drem-planner:latest`.
4. Re-register project: `drem project register --update drem-orchestrator`
   regenerates compose + drem.toml from new templates.
5. `docker compose -f deploy/compose/global.yml up -d drem-planner`
   brings planner up warm.
6. T2 canary: insert a classifying task, verify it completes
   through classifier → planner → plan_review (parks there per
   frozen gate).
7. Mark Phase 8 planner criterion complete.

## 9. Open questions

1. **Concurrency on the planner container.** Claude CLI is one
   process per invocation; the HTTP handler serializes by default.
   If throughput matters, raise concurrency via a server-side
   semaphore. Defer until queue buildup is observed.
2. **Claude CLI flags exact set.** `-p` / `--print` for non-
   interactive, `--output-format json`, `--model claude-opus-4-6`,
   `--max-tokens`. Verify against the CLI help at implementation
   time; pin in a constant in `server.go` with a comment naming the
   CLI version we tested.
3. **Credentials file validation is host-dependent.** On a fresh
   machine without `claude login` run, the operator sees a
   crashloop. Install.md walks them through `claude login` before
   `docker compose up`. Detect + printable error message in the
   planner binary itself (don't just silent-exit-1).
4. **Opus rate limits.** Claude Max gives ~900 messages / 5h on
   the highest tier. If drem runs a planner call every 3-5 min
   during heavy work, that's 60-100 calls / 5h — within cap but
   not comfortable. Metric + alert on
   `drem_planner_anthropic_429_total` so the operator sees before
   hitting the wall.
5. **What about other Claude-backed roles?** Reviewer + fixer
   might want the same warm pattern (their legacy path uses claude
   CLI too). Out of scope for this plan; file
   `plans/warm-reviewer.md` / `plans/warm-fixer.md` separately
   when those roles become blocking.

## 10. What this doesn't solve

- Prep container — still `plans/warm-direct-prep.md`, still using
  sglang.
- Gate freeze still parks tasks at `plan_review`. T2 canary stops
  there by design.
- `researcher` agent type — orthogonal.
- Multi-project concurrent planner calls — single-replica serializes
  them. Fine for now; scale decision deferred.
