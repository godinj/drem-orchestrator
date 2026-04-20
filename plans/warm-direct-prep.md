# Warm Direct-Prep Container — Implementation Plan

Status: **design proposed, not yet implemented.** 2026-04-20.

Sibling to `plans/warm-direct-classifier.md`. Follow that plan for
any shared decisions (single replica, push model, role-dedicated
binary, auth via `DREM_AGENTMON_TOKEN`, `/healthz` + `/metrics`
surface). This doc captures only the prep-specific deltas.

## 0. Scope and motivation

Direct-prep already exists as an in-orch function call:

- `internal/agent/direct_prep.go` — `RunDirectPrep(cfg, opts,
  outputPath)` reuses `RunDirectToolAgent` with read-only tools
  (`read`, `grep`, `glob` from `internal/agent/direct_tool_agent.go:185`).
- `cmd/drem/main.go:229-254` auto-enables prep-direct whenever
  classifier-direct is on. Uses `DirectPrepConfig` = `DirectToolAgent-
  Config` with `MaxTokens=4096` (larger than classifier's 1024 for
  tool loops).

Problems this plan solves (identical to classifier plan §0, but with
extra urgency for prep because its tool loops run LONGER than a
one-shot classify):

1. **Longer tick starvation.** Prep is a multi-turn tool loop; a
   single prep can hold an orch goroutine for 30-90s while reading
   files and calling sglang repeatedly. That's 6x-18x the risk of
   the classifier path.
2. **Thread exhaustion.** Prep opens more sqlite connections per run
   because its tool loop may write interim state. Moves the same
   problem as classifier except worse.
3. **Isolation.** A prep that gets stuck in a tool-loop hallucination
   is harder to kill from inside orch than a one-shot classifier.
4. **Pattern consistency.** Same as classifier plan §0.

Operator decisions carry from classifier session: single replica,
push, role-dedicated binary `cmd/drem-prep`.

## 1. Target architecture

Identical to classifier plan §1 with two substitutions:

- `drem-classifier` → `drem-prep`
- `POST /classify {task_id, title, description, ctx}` →
  `POST /prep {task_id, prep_opts}` where `prep_opts` encodes the
  `PrepPromptOpts` struct (task, project, worktree path, etc.).

## 2. Interface decision

**Option A (response body).** Orch blocks on the HTTP call; prep
returns the output file contents (or a pointer to a shared volume).
Unlike classifier, prep writes a FILE to the worktree — the classifier
just writes DB rows. So the interface is slightly different:

- **Option A1 — inline body.** Response includes the full prep output
  as a field. Orch writes it to the worktree from its side. Simple,
  but the payload can be large.
- **Option A2 — shared volume.** Prep container mounts the project's
  bare-repo volume rw (same as merger); writes the output file
  directly. Response body is just `{tokens_in, tokens_out, output_path}`.
- **Option B — callback.** Prep POSTs completion webhook to orch.

**Recommend A2.** Prep needs the worktree anyway (tool calls resolve
paths relative to the worktree). Mounting it once + having prep write
the output file matches what the legacy subprocess did. Orch's side is
a thin HTTP POST + transition; no payload copy.

Caveat: the bare-repo mount makes prep stateful in its container
filesystem during a request. Single-replica + request-scoped cleanup
handles this. If we scale to N, we need per-request worktrees or a
pool.

## 3. Files touched

### New files
- `cmd/drem-prep/main.go`, `cmd/drem-prep/server.go`,
  `cmd/drem-prep/server_test.go`.
- `deploy/docker/prep.Dockerfile` — same shape as classifier, plus
  a `/work` volume for worktree bind-mount.
- `plans/warm-direct-prep.md` — this file.

### Modified files
- `internal/agent/direct_prep.go` — refactor `RunDirectPrep` so the
  outer LLM call is reusable from the new binary; keep the function
  signature unchanged for in-orch callers (fallback).
- `internal/orchestrator/task_prep.go` (or wherever prep is
  dispatched — `grep -rn "RunDirectPrep\|AgentPrep" internal/orchestrator`
  to confirm) — add HTTP-client path; inline stays as fallback.
- `cmd/drem/config.go` — new `[agents.prep].endpoint` TOML key
  + `DREM_PREP_URL` env override.
- `deploy/compose/global.yml` — new `drem-prep` service on drem-net.
- `internal/projects/templates/project-drem.toml.tmpl` — add
  `[agents.prep]\n  endpoint = "http://drem-prep:8090/prep"`.
- `internal/projects/templates/project-compose.yml.tmpl` — orch env
  gets `DREM_PREP_URL` alongside the merger-wiring env already landed
  in `d288c6c`.
- `docs/containerization/install.md` — add prep paragraph to the
  warm-direct-agents section authored by the classifier commit.

## 4. HTTP surface of `drem-prep`

### POST /prep

Request:
```json
{
  "task_id": "...",
  "prep_opts": {
    "task": {...},
    "project": {...},
    "worktree_path": "/home/.../feature/task-xxx/agent-yyy",
    ...
  },
  "output_relative_path": "drem-prep-output.md"
}
```

Response 200:
```json
{
  "task_id": "...",
  "output_path": "/home/.../feature/task-xxx/agent-yyy/drem-prep-output.md",
  "tokens_in": 14000,
  "tokens_out": 3200,
  "duration_ms": 52000,
  "tool_calls": 12
}
```

Errors: 400/401/502/504/500 per classifier plan §4.

`worktree_path` must be inside the bind-mounted bare repo area —
validate on receive; reject paths that escape.

### GET /healthz, /metrics

Same shape as classifier plan §4. Metrics add:
- `drem_prep_tool_calls_total{tool="read|grep|glob"}`
- `drem_prep_duration_seconds_bucket` (longer buckets than classifier)

## 5. Orch-side changes

Find prep's current dispatch site (likely `internal/orchestrator/
task_prep.go` — the grep in §3 will confirm). Pattern:

```go
if cfg.Prep.Endpoint != "" {
    result, err = prepViaContainer(ctx, cfg.Prep, opts)
} else {
    result, err = agent.RunDirectPrep(cfg.DirectPrep, opts, outputPath)
}
```

Rollback behaviour identical to classifier.

## 6. Commit sequence

1. **`refactor(agent): extract prep core to agent.Prep`** — split
   `RunDirectPrep` so the LLM+tool loop is reusable. Zero behavior
   change; tests move.
2. **`feat(cmd): add cmd/drem-prep HTTP server`** — minimal server
   wrapping `agent.Prep`. Unit tests for happy path + error branches.
3. **`feat(deploy): drem-prep Dockerfile + global compose entry`** —
   image builds, container boots, healthcheck green.
4. **`feat(orch): route direct prep via drem-prep when Endpoint set`** —
   inline path as fallback.
5. **`feat(projects): point generated drem.toml at drem-prep`** —
   template update + test.
6. **`docs(containerization): warm direct-prep walkthrough`** —
   append to install.md; tick Phase 8 prep row.

~7-10 files per commit, ~700 LOC total. ~1.5 days. Faster than
classifier because the direct-prep path already exists and we're
lifting it rather than refactoring.

## 7. Test surface

Same categories as classifier plan §7, plus:

- `agent.Prep` — tool-loop behavior (mock sglang returns a pre-canned
  tool-call sequence).
- `cmd/drem-prep/server_test.go` — worktree path validation
  (`{"worktree_path": "/etc/passwd"}` must 400).

## 8. Rollout

Same as classifier plan §8, in series after the classifier container
lands. Do NOT parallelize; let classifier prove the pattern first.

## 9. Open questions

1. **Shared substrate or fork?** Classifier and prep both need an
   HTTP server, auth middleware, `/healthz`, `/metrics`. Tempting to
   factor these into `internal/agent/httpserver/` after classifier
   lands. Defer until we see actual duplication.
2. **Bare-repo mount rw vs ro.** Prep only reads; mount ro. The plan
   already implies this — explicit here.
3. **Output file location.** Legacy subprocess wrote to the worktree
   directly. Keep that; the response body advertises the absolute
   path so orch can ingest on its side.
4. **Prep + classifier share a binary?** Operator said no (role-
   dedicated). Matches Unix daemon norms; no rethink needed.

## 10. What this doesn't solve

- Planner is still subprocess-only. Tracked separately in
  `plans/warm-direct-planner.md`.
- Gate freeze still in effect.
- Prep is single-replica — concurrent projects share one prep
  container. Scale in a follow-up.
