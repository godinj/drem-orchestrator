# Spawn-on-Demand Planner Container — Implementation Plan

Status: **implementation in progress on worktree-agent-af401951, 2026-04-19.**
Commits 1 (Dockerfile + entrypoint), 2 (spawner image mapping), 3
(dispatchPlan + plan.json parse + validation), 4 (processPlanning
provider-based routing), 5 (drem.toml planner defaults), 6
(planner-template compose image-prime stub), and 7 (install.md
spawn-on-demand agents section + ANTHROPIC_API_KEY compose
passthrough) landed.

**Revised from the earlier warm/direct-LLM draft after operator
direction 2026-04-20: "planner should always be opus for now."**

Opus = Anthropic Claude API. The `claude` CLI (`@anthropic-ai/claude-
code` npm) is the canonical client; it already ships inside
`drem-worker-base.Dockerfile` (line ~83). Running planner via the
existing direct-tool-agent sglang path does NOT apply here. Instead,
planner becomes a **per-task spawn-on-demand container** exactly like
merger (plan `plans/merger-spawn-on-demand-impl.md`, landed commits
`89d6ed7`..`f7a48aa`).

This supersedes the earlier draft of this file (warm container +
DirectToolAgent + gemma4-26b). The sibling prep plan
(`plans/warm-direct-prep.md`) is unaffected and still uses the
sglang direct path.

## 0. Scope

Replace the legacy `o.runner.SpawnAgent(... AgentPlanner ...)` call
site in `internal/orchestrator/task_processing.go:234` with a
`o.Spawner.SpawnWorker(... AgentType: "planner" ...)` spawn, matching
how the merger call site works. The merger spawner plumbing landed
this session — `SpawnWorkerParams.Cmd []string` and
`BareRepoReadWrite bool` are already there (commit `89d6ed7`). We
reuse them.

Key constraints from operator direction:
- **Always Opus.** `[agents.planner].model` defaults to
  `claude-opus-4-6` (or whatever the current Opus tag is — confirm
  against `internal/capability/capability.go`).
- **Always Anthropic.** `[agents.planner].provider` defaults to
  `claude`. No sglang routing for planner.
- **Per-task lifecycle.** Planner runs once per task, ~60-180s,
  produces `plan.json`, exits. Warm pool is overkill for this
  frequency.

## 1. Why spawn-on-demand instead of warm

| Consideration | Warm | **Spawn-on-demand (chosen)** |
|---|---|---|
| Planner frequency | rare (once per backlog task) | rare — cold-start cost amortized |
| LLM behind it | Anthropic HTTPS | Anthropic HTTPS |
| Tool-loop substrate | would need new code | existing claude CLI does tool loops natively |
| Idle resource cost | ~100MB RAM sitting idle | 0 between runs |
| Failure isolation | container-level | container-level (same) |
| Pattern with merger | divergent | identical |
| State between runs | none needed | none needed |

Warm wins on cold-start latency; claude CLI starts in <1s, tiny cost
relative to a 60-180s plan run. Not worth the design complexity.

## 2. Interface decision

`SpawnWorkerParams` already has `Cmd []string` — planner's argv goes
there. The planner container runs claude in headless JSON mode against
the worktree, writes `plan.json` to the worktree root, exits.

Orch detects completion by polling `InspectWorker` (same pattern as
`dispatchMerge`). Exit code 0 with a valid `plan.json` on disk = success.

No callback, no HTTP server on the planner side. Keep it identical to
merger's shape.

## 3. Files touched

### New files
- `deploy/docker/planner.Dockerfile` — based on `debian:bookworm-slim`,
  layers node + `@anthropic-ai/claude-code`, a small entrypoint
  script, and `git config --system --add safe.directory '*'`. Same
  shape as merger's Dockerfile minus the Go build stage. ~200-300MB
  image.
- `deploy/docker/context/planner-entrypoint.sh` — 30-line shell
  entrypoint: parses flags (`--task-id`, `--worktree`, `--prompt-file`,
  `--model`, `--effort`), invokes claude in headless mode, waits for
  `plan.json` to land, exits with the CLI's exit code.
- `internal/orchestrator/plan_dispatch.go` — planner-side analogue of
  `merge_dispatch.go`. Exposes `dispatchPlan(ctx, task) (*PlanResult,
  error)`, builds `SpawnWorkerParams.Cmd`, polls `InspectWorker`,
  reads `plan.json` from the worktree, validates + returns.
- `internal/orchestrator/plan_dispatch_test.go` — argv composition,
  exit-code routing, JSON parse errors, validation failure modes.
- `plans/warm-direct-planner.md` — this file.

### Modified files
- `internal/spawner/images.go` — add
  `"planner": "localhost:5000/drem-planner:latest"` to
  `defaultImages`.
- `internal/orchestrator/task_processing.go::processPlanning` —
  `if cfg.Planner.Provider == "claude" || cfg.Planner.Provider == ""`
  branch calls `dispatchPlan` via the spawner; else falls through to
  the legacy runner path for rollback safety.
- `internal/projects/templates/project-drem.toml.tmpl` — add
  `[agents.planner]\n  provider = "claude"\n  model = "claude-opus-4-6"\n  effort = "high"`.
- `deploy/compose/global.yml` — the planner image just needs to be
  pushed to the registry (like drem-merger); no long-lived service
  entry. A `profiles: ["never"]` image-prime stub can be added to
  the per-project compose template mirroring `merger-template`.
- `internal/projects/templates/project-compose.yml.tmpl` — add a
  `planner-template` service entry with `profiles: ["never"]` so
  `docker compose pull` primes the image on first up. Matches the
  existing `merger-template` convention.
- `docs/containerization/install.md` — new "Spawn-on-demand agents"
  subsection covering merger AND planner, since they share the
  pattern. Document the `ANTHROPIC_API_KEY` plumbing.
- `plans/containerization.md` — tick the planner row in Phase 6.

## 4. ANTHROPIC_API_KEY plumbing

The planner container needs Anthropic credentials. Unlike merger
(which calls internal orch HTTP) planner calls out to
`api.anthropic.com`. Options:

1. **Env var through compose.** Host operator sets
   `ANTHROPIC_API_KEY` in their shell; `deploy/compose/global.yml`
   passes it to the spawner; spawner forwards it to planner-container
   env on spawn. Fail closed if unset.
2. **Anthropic Bedrock-equivalent / credentials file.** Overkill for
   a single-operator stack.

Recommend (1). Add `SpawnWorkerParams.Env` already exists as a
passthrough map — populate it in `dispatchPlan` from the orch's own
env:

```go
params.Env = map[string]string{
    "ANTHROPIC_API_KEY": os.Getenv("ANTHROPIC_API_KEY"),
}
```

Orch's env gets `ANTHROPIC_API_KEY` from `deploy/compose/global.yml`
(passed through from host) and the per-project compose template.

Fail closed: if the orch env doesn't have the key at
`SetInternalEndpoints` time, `processPlanning` logs a loud error,
leaves the task in PLANNING (orch retries next tick), and emits a
`planner_missing_api_key` event. Do NOT advance to plan_review on a
blank result.

## 5. Commit sequence

1. **`feat(deploy): add drem-planner image (claude CLI + entrypoint)`** —
   new Dockerfile + entrypoint script. `docker build` + registry
   push. Image inventory test confirms the tag lands. No orch wiring.
2. **`feat(spawner): map "planner" to drem-planner image`** —
   one-line addition to `defaultImages`, plus an image-resolver test.
3. **`feat(orch): dispatchPlan via spawner, parse plan.json`** —
   `plan_dispatch.go` + tests. Uses mock spawner in tests (same
   pattern as `merge_dispatch_test.go`). Covers argv, JSON parse,
   validation (subtasks non-empty, tests_for valid, etc.).
4. **`feat(orch): processPlanning routes to dispatchPlan when provider=claude`** —
   wire `processPlanning` to pick the spawner path. Fall-through to
   legacy runner stays. Unit tests for the branch decision.
5. **`feat(projects): default planner to opus in drem.toml template`** —
   template update + test asserting the generated toml pins provider
   and model.
6. **`feat(projects): add planner-template image prime to compose`** —
   mirrors merger-template, profiles: ["never"].
7. **`docs: containerization install.md — spawn-on-demand agents section`** —
   covers merger + planner. Document ANTHROPIC_API_KEY requirement.
8. **`docs: plans/containerization.md — tick planner row`**.

~8 commits, ~900 LOC, ~2 days focused work.

**T2 canary becomes feasible at commit 4 assuming
`ANTHROPIC_API_KEY` is set in the orch env.**

## 6. Plan validation

Move from the prior draft: the planner container writes `plan.json`,
orch reads it. Validation runs in `dispatchPlan` after the container
exits with code 0:

- `plan.json` parses.
- `subtasks` non-empty.
- Every `tests_for` index points to a real `subtasks` entry.
- Every `dependencies` index is valid.
- TDD pairing rule: each implementation subtask has exactly one test
  subtask whose `tests_for` contains only its index
  (`internal/prompt/prompt_planner.go:63-66`).

On validation failure, `dispatchPlan` returns an error that surfaces
as a planner retry in `processPlanning`. Budget = `MaxPlannerRetries`
(already defined). Beyond budget, fail task with a
`planner_validation_exhausted` reason.

Material improvement over legacy which had no validation.

## 7. Exit-code table

| Exit | Meaning | Orch action |
|---|---|---|
| 0 + valid plan.json | success | advance task to plan_review (after clarification check) |
| 0 + missing plan.json | silent failure from CLI | retry (MaxPlannerRetries) |
| 0 + plan.json validation fail | malformed output | retry with feedback appended |
| 1 | claude CLI error (auth, network, etc.) | retry; escalate if persistent |
| 2+ | unknown | fail task immediately |
| 124 / 137 | timeout (SIGTERM from tini) | retry once; else fail |

Same shape as merger's exit-code table (§5 of merger plan) —
consistent routing across spawn-on-demand roles.

## 8. Test surface

- `internal/orchestrator/plan_dispatch_test.go` — argv composition,
  mock spawner, exit-code routing, plan.json parse, validation
  failure, missing-plan-file, timeout.
- `internal/orchestrator/task_processing_test.go` — add cases for
  spawner-path vs runner-path selection on provider value.
- `internal/spawner/images_test.go` — planner image mapping.
- `internal/projects/template_test.go` — drem.toml has
  `provider = "claude"` and `model = "claude-opus-4-6"` under
  `[agents.planner]`; compose template has planner-template stub.
- Dockerfile: lint clean (no `hadolint` in tree today — skip).

## 9. Open questions

1. **Model string — exact tag.** `claude-opus-4-6` mirrors existing
   references in `internal/capability/capability_test.go:26` and
   `bench/scratch/...`. Verify against the Anthropic docs at
   implementation time; pick the current stable Opus tag.
2. **Claude CLI flags.** The entrypoint script needs the right flag
   set: `--headless`, `--model`, `--effort`, output routing. Cross-
   reference `agent.NewRunner` for how the legacy path invoked it so
   we match behavior.
3. **Prompt file mount.** The planner prompt (generated by
   `prompt.Generate` with `AgentType=Planner`) must reach the
   container. Two options: (a) write to the worktree before spawn
   and pass the path; (b) pass inline via env var. Option (a) is
   cleaner and already matches how merger receives config.
4. **Context window.** Opus has a 1M context; planner prompts +
   repo exploration easily exceed the 200K default. Pass
   `--context-window` explicitly in the entrypoint to avoid the
   CLI's auto-switch.
5. **Thinking budget for reasoning.** Plan quality likely benefits
   from extended thinking. Expose via config:
   `[agents.planner].thinking_tokens = 32768`. Defer unless planner
   quality is bad.

## 10. What this doesn't solve

- Gate freeze still parks tasks at `plan_review`. T2 canary stops
  there by design.
- Cost: every planner run is billable Anthropic tokens. Track via
  `drem_planner_anthropic_tokens_total` metric (exported from
  dispatchPlan based on entrypoint output parsing).
- `researcher` agent type — orthogonal.
- If Anthropic rate-limits, the retry budget in §7 chews through
  them. Worth adding a circuit-breaker on 429 in a follow-up.

## 11. Relationship to the classifier + prep plans

- Classifier + prep run on sglang (`gemma4-26b`), managed by gq.
  They warm-containerize because they're frequent and LLM-local.
- Planner runs on Opus (Anthropic). It spawn-on-demands because it's
  rare and LLM-remote.
- All three share the HTTP/auth pattern where applicable
  (classifier+prep) and the spawner pattern where applicable (merger
  + planner).
- A future role may want both a warm container AND spawn-on-demand
  behavior (e.g. coder — warm for quick fixes, spawn for heavy work).
  Defer that design until we see the need.
