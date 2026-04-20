# Warm Direct-Classifier Container — Implementation Plan

Status: **in progress.** 2026-04-20. Commits 1-2/6 landed: `agent.Classify` extracted and `cmd/drem-classifier` HTTP server wired with handler + /healthz + /metrics tests.

Follow-up to T1 canary (commit `aca8bf3`) which proved the direct
classifier roundtrip works inline in the orch process. The next move
is to lift that work out of orch into a dedicated long-lived container.

## 0. Scope and motivation

Today `direct_classifier` runs **inside the orch process**:
`internal/orchestrator/classifying.go:187` calls
`agent.RunDirectClassifier(...)` synchronously from the tick loop. This
creates four problems:

1. **Tick starvation.** Classifier LLM calls sit in the same goroutine
   family as DB I/O. `maxDirectClassifiersPerTick = 3` is a kludge to
   stop classifier dispatch from monopolizing ticks.
2. **Thread exhaustion.** T1 surfaced a `fatal error: thread exhaustion`
   under polling pressure; SQLite+cgo connections and classifier HTTP
   both contend for Go's 10K-thread ceiling. Moving classifier HTTP out
   of orch frees a bounded number of threads.
3. **No isolation.** A classifier hang, a bad prompt template, or a
   slow sglang response stalls orch. Container-level restart policy
   + healthcheck catch these.
4. **Pattern inconsistency.** Worker, merger, C-Suite, watcher are all
   containerized. Classifier is the lone inline role. Planner + prep
   follow the same pattern; fixing classifier first lets us template
   the refactor.

Operator decisions (2026-04-20 session):
- **Single replica.** Start with one classifier container; add scaling
  knobs later if we see queue buildup.
- **Push model.** Orch POSTs classify jobs to the container. Lower
  latency than pull and keeps orch as the source of truth for which
  tasks are classifiable.
- **Role-dedicated binary.** New `cmd/drem-classifier` rather than a
  shared `drem-direct-agent --role=classifier`. Planner and prep get
  their own binaries when they land.

## 1. Target architecture

```
orch tick loop
  (tasks in status=classifying)
    │
    │  POST /classify {task_id, title, description, ctx}
    │  (HTTP, bearer-auth via shared token)
    ▼
drem-classifier container (drem-net :8090)
    │
    │  POST /v1/chat/completions
    ▼
drem-gq container (drem-net :8090)
    │
    ▼
drem-sglang container (drem-net :8081)
    │
    ▼
gemma4-26b
    │  classification JSON
    ▼
drem-classifier (parses, validates)
    │  POST /projects/<p>/tasks/<id>/classification  (OR direct DB write)
    ▼
orch persists result, transitions classifying → backlog
```

Orch keeps the transition authority; the classifier container is a
stateless compute worker that returns a classification decision.

## 2. Interface decision

Two concrete options for how classifier returns its result to orch:

| Option | Orch surface | Classifier surface | Trade-off |
|---|---|---|---|
| **A. Response body** | Existing orch tick polls the HTTP call; on 200, writes category/complexity directly to DB and transitions state. | 1 endpoint: `POST /classify → 200 {category, complexity, …}`. | Simplest. Keeps DB write authority in orch. Classifier stateless. |
| **B. Callback** | Orch enqueues a classify job, classifier POSTs back to orch `/projects/../tasks/../classification` on completion. | 2 endpoints on classifier; orch needs a new webhook handler. | Lower orch-side blocking. More moving parts. Needed only if we scale to N classifiers with concurrent jobs. |

**Recommend A for phase 1.** Single replica + push = orch's dispatch
goroutine can block on the HTTP call; each goroutine holds one open
connection, bounded by `MaxDirectClassifiersPerTick`. The orch tick
loop is NOT blocked because `RunDirectClassifier` was already run
off-tick in its own goroutine (see
`internal/orchestrator/classifying.go:172`). Option B is a clean
upgrade path if queue pressure appears.

## 3. Files touched

### New files

- `cmd/drem-classifier/main.go` — HTTP server + shutdown handling.
- `cmd/drem-classifier/server.go` — handlers (`POST /classify`,
  `GET /healthz`, `GET /metrics`).
- `cmd/drem-classifier/server_test.go` — table-driven handler tests.
- `deploy/docker/classifier.Dockerfile` — multi-stage Go build, debian
  runtime, `safe.directory='*'` consistency.
- `plans/warm-direct-classifier.md` — this file.

### Modified files

- `internal/agent/direct_classifier.go` — keep `RunDirectClassifier`
  and the classifier logic; both orch (until cutover) and the new
  binary import it. Thin export of `Classify(ctx, cfg, input)
  (Result, error)` so the new binary doesn't re-implement prompt
  assembly or JSON parsing.
- `internal/orchestrator/classifying.go` — add an HTTP-client path
  (`classifyViaContainer`) guarded by `cfg.Classifier.Endpoint`. When
  the endpoint is set, orch POSTs to the container; otherwise falls
  back to the inline path (rollback safety).
- `cmd/drem/config.go` — new `[agents.classifier].endpoint` TOML key
  + `DREM_CLASSIFIER_URL` env override.
- `deploy/compose/global.yml` — add `drem-classifier` service on
  drem-net, depends_on sglang+gq, healthcheck on `/healthz`.
- `internal/projects/templates/project-drem.toml.tmpl` — set
  `endpoint = "http://drem-classifier:8090/classify"` so per-project
  orch talks to the shared classifier.
- `internal/projects/templates/project-compose.yml.tmpl` — orch env
  gets `DREM_CLASSIFIER_URL` for clarity (redundant with TOML but
  matches the `DREM_ORCH_URL` pattern from merger commit `d288c6c`).
- `docs/containerization/install.md` — new section: "Warm direct
  agents", document the classifier lifecycle + how to scale.
- `plans/containerization.md` — add a Phase 8 row for warm direct
  agents.

## 4. HTTP surface of `drem-classifier`

### POST /classify

Request (JSON):
```json
{
  "task_id": "caca7001-0001-4000-8000-000000000001",
  "title": "CANARY-T1 direct-classifier smoke",
  "description": "Smoke task ...",
  "context": {"human_triage": false}
}
```

Response 200 (JSON):
```json
{
  "task_id": "caca7001-...",
  "category": "quickfix",
  "complexity_score": 2,
  "tokens_in": 812,
  "tokens_out": 48,
  "duration_ms": 947
}
```

Errors:
- 400 — bad request shape.
- 502 — sglang/gq upstream failure.
- 504 — LLM call timed out.
- 500 — classifier bug; logs stack; orch surfaces as a normal classify
  failure and retries on the next tick.

Auth: `Authorization: Bearer <shared-token>` matching
`DREM_AGENTMON_TOKEN`. Token plumbed via env in compose, never logged.

### GET /healthz

Returns 200 when (a) the binary is running and (b) the classifier
can reach the gq endpoint (single probe every 30s, cached).

### GET /metrics

Prometheus-shaped counters for visibility:
- `drem_classifier_requests_total{result="ok|4xx|5xx"}`
- `drem_classifier_duration_seconds_bucket`
- `drem_classifier_llm_tokens_total{direction="in|out"}`
- `drem_classifier_upstream_up` (0/1 from the cached probe)

## 5. Orch-side changes

`processClassifyingTasksDirect` becomes the shared dispatch entry; it
already runs off-tick. Add a small branch:

```go
if cfg.Endpoint != "" {
    // POST to drem-classifier container; parse response; write result.
    result, err = classifyViaContainer(ctx, cfg, task)
} else {
    // Legacy inline path — kept for rollback + tests.
    result, err = agent.RunDirectClassifier(cfg.DirectClassifierConfig, ...)
}
```

`cfg.Endpoint` threads through `DirectClassifierConfig` (already a
struct) so the interface change is local. Existing callers that don't
set `Endpoint` get the inline behavior — T1 still passes.

## 6. Commit sequence

1. **`refactor(agent): extract classifier core to agent.Classify`** —
   split `RunDirectClassifier` so the LLM call + parse logic is
   reusable from the new binary. Zero behavior change; tests move with
   the refactor.
2. **`feat(cmd): add cmd/drem-classifier HTTP server`** — minimal
   server that wraps `agent.Classify`. Unit tests: happy path, bad
   JSON, upstream 502, auth missing. No Docker yet.
3. **`feat(deploy): drem-classifier Dockerfile + global compose entry`** —
   image builds, container boots, healthcheck green. Separate commit
   so image prep can happen in parallel with orch wiring.
4. **`feat(orch): route direct classify via drem-classifier when Endpoint set`** —
   add `classifyViaContainer`. Inline path stays as fallback.
   Regression test: when Endpoint is empty, inline path still fires.
5. **`feat(projects): point generated drem.toml at drem-classifier`** —
   template update + test that the endpoint lands in the rendered file.
6. **`docs(containerization): warm direct agents walkthrough`** — update
   install.md §"Classifier", phase 8 row in containerization.md.

~7-10 files per commit, ~800 LOC total. 2 days focused work.

## 7. Test surface

| Area | Tests |
|---|---|
| `agent.Classify` | existing `RunDirectClassifier` tests move; add one for the extracted function boundary |
| `cmd/drem-classifier/server_test.go` | 200 happy, 400 bad JSON, 401 missing token, 502 upstream fail, 504 timeout |
| `internal/orchestrator/classifying_test.go` | `classifyViaContainer` success, endpoint-empty falls through to inline, HTTP 5xx surfaces as classify-failed + retry |
| `internal/projects/template_test.go` | drem.toml contains `endpoint = "http://drem-classifier:8090/classify"` |
| `deploy/compose/global.yml` | YAML-parse + service-present smoke test (existing pattern from global-compose-test) |

## 8. Rollout

1. Merge commit 1 (refactor) + 2 (binary). No runtime change.
2. Merge commit 3 (Dockerfile + compose). Image builds; container can
   be up-ed but orch still uses inline path.
3. Merge commit 4 (orch wiring). Toggle per-project by flipping
   `endpoint` in drem.toml. Test on one project before globalizing.
4. Merge commits 5+6 (template + docs).
5. T1-equivalent canary against the new container path — insert a
   classifying task, verify it completes through the container, not
   inline.
6. Mark Phase 8 complete.

## 9. Open questions

1. **Model per role** — classifier defaults to `gemma4-26b` today
   because that's what sglang serves. Do we want `[agents.classifier]`
   to pin a *different* model for cost/speed, routed through gq to a
   separate sglang or a separate model on the same sglang? Defer until
   the planner warm container, which has different latency needs.
2. **Container image tag scheme** — follow the pattern in
   `plans/sglang-gemma4-followup.md` "Open follow-ups" and tag
   classifier images with the gq-version + model-version they were
   validated against? Yes if sglang model churns; not urgent day 1.
3. **Circuit breaker** — the classifier container already does its
   own `/healthz` probe of gq. Do we still need `endpointHealth.IsHealthy`
   in orch, or does orch trust the container's 502? Recommend dropping
   the orch-side breaker once cutover is clean.
4. **Planner + prep follow-up** — same pattern applies. File companion
   plans `plans/warm-direct-planner.md` and `plans/warm-direct-prep.md`
   once classifier is merged and we've learned what we got wrong.

## 10. What this doesn't solve

- Thread exhaustion was also driven by stale TUI clients (today's
  finding). That's orthogonal; fix tracked in a separate issue.
- Gate freeze still in effect: frozen-gate tasks don't move even with
  a warm classifier.
- Merger spawn-on-demand just landed (commits `89d6ed7`..`f7a48aa`).
  Still works the same way; mergers remain short-lived one-shot
  containers. Warm-vs-one-shot is per-role.
