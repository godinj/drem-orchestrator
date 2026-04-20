# Warm Direct-Planner Container — Implementation Plan

Status: **design proposed, not yet implemented.** 2026-04-20.

Sibling to `plans/warm-direct-classifier.md` and
`plans/warm-direct-prep.md`. Shares their auth/HTTP/healthcheck
shape.

## 0. Scope and motivation

**Planner has no direct-LLM path yet.** Unlike classifier and prep,
there is no `RunDirectPlanner` function and no `[agents.planner].direct`
config key. `internal/orchestrator/task_processing.go:234` calls
`o.runner.SpawnAgent(task, featureName, model.AgentPlanner, prompt)`,
which routes through `agent.NewRunner(..., cfg.ClaudeBin,
cfg.OpenCodeBin, ...)` — the legacy claude/opencode subprocess path.
Neither binary is in the orch container.

This plan is therefore **two phases**:

1. **Phase 1 — Add direct-planner in-orch.** Lands a
   `RunDirectPlanner` function, wires `[agents.planner].direct=true`,
   and routes `processPlanning` through it. Analogous to the
   pre-session state of direct-classifier. **Unblocks T2 canary
   immediately.**
2. **Phase 2 — Warm-containerize it.** Same shape as classifier /
   prep plans.

Operator can stop after phase 1 to land T2 canary, then resume
phase 2.

## 1. Key differences from classifier + prep

| Dimension | Classifier | Prep | **Planner** |
|---|---|---|---|
| Input | title+desc+ctx | task+project+worktree | task+project+worktree+comments+repo-map+target-coder-model |
| Output | JSON `{category, complexity}` | markdown file | JSON `plan.json` with subtasks, coverage, assumptions, tdd_exceptions, module_boundaries, interface_shapes |
| Tool use | none (one-shot chat) | read/grep/glob | **needs read/grep/glob** — planner must explore the codebase to decompose sensibly |
| Prompt length | ~800 tokens | ~2K tokens | ~4-8K tokens (richer instructions in `internal/prompt/prompt_planner.go`) |
| Output size | ~200 tokens | ~3K tokens | ~4-8K tokens (full plan) |
| Loop turns | 1 | 5-12 | 10-20 |
| Per-run duration | 1-2s | 30-90s | 60-180s |

Planner therefore uses the **`DirectToolAgent` substrate**, same as
prep, but with a different system prompt and output schema. The
substrate already exists — we add planner to the `roleTools` map
with read-only tools:

```go
// internal/agent/direct_tool_agent.go
var roleTools = map[string][]string{
    "coder":    {"read", "edit", "write", "bash", "grep", "glob"},
    "fixer":    {"read", "edit", "write", "bash", "grep", "glob"},
    "reviewer": {"read", "bash", "grep", "glob"},
    "prep":     {"read", "grep", "glob"},
    "planner":  {"read", "grep", "glob"},   // NEW
}
```

Planner does NOT need edit/write/bash — it writes `plan.json` as its
final assistant message, which the caller parses and persists to the
DB. No filesystem side-effects during tool loops.

## 2. Phase 1 — direct-planner in-orch

### 2.1 Files

- **NEW** `internal/agent/direct_planner.go` (~260 LOC). Mirrors
  `direct_prep.go`. Exposes `DirectPlannerConfig` (embedding
  `DirectToolAgentConfig`) and `RunDirectPlanner(cfg, opts,
  outputPath) (*DirectToolAgentResult, error)`.
- **NEW** `internal/agent/direct_planner_test.go` — table tests
  against a mock sglang that returns a canned plan JSON.
- **MODIFIED** `internal/agent/direct_tool_agent.go` — add
  `"planner"` to `roleTools`.
- **MODIFIED** `cmd/drem/main.go` — add `plannerDirect` gate in
  the same style as `classifierDirect` (see lines 209-221); plumb a
  `*agent.DirectPlannerConfig` onto the orchestrator via a new
  `SetDirectPlannerConfig` method.
- **MODIFIED** `internal/orchestrator/orchestrator.go` — add
  `directPlannerCfg *agent.DirectPlannerConfig` field + setter.
- **MODIFIED** `internal/orchestrator/task_processing.go` —
  `processPlanning` branches on `o.directPlannerCfg`:

  ```go
  if o.directPlannerCfg != nil {
      return o.processPlanningDirect(task)   // NEW
  }
  // fall through to existing SpawnAgent path
  ```

  `processPlanningDirect` creates a lightweight agent record
  (provider=`sglang-direct`, model from cfg), runs
  `RunDirectPlanner` synchronously in a goroutine (same pattern as
  `processClassifyingTasksDirect`), parses the resulting `plan.json`,
  writes it to `task.Plan`, and lets the existing tick advance to
  `PLAN_REVIEW`.
- **MODIFIED** `internal/prompt/prompt_planner.go` — extract a
  `plannerSystemPrompt` constant for `RunDirectPlanner` to use
  directly. The existing `plannerInstructions()` (used by
  `prompt.Generate`) keeps its subprocess shape for rollback safety.

### 2.2 Commits

1. **`feat(agent): add direct planner path with tool loop`** —
   `direct_planner.go` + `roleTools["planner"]` + tests. No orch
   wiring yet.
2. **`feat(orch): wire direct planner in processPlanning`** —
   `SetDirectPlannerConfig`, `processPlanningDirect`, fall-through to
   legacy. Tests: direct path returns valid plan, falls through when
   cfg is nil, JSON parse errors surface as planner-failed with
   retry.
3. **`feat(config): plumb [agents.planner].direct through config`** —
   main.go gate, drem.toml template adds
   `[agents.planner]\n  direct = true\n  model = "gemma4-26b"`.
4. **`docs: update install.md with direct planner section`**.

~4 commits, ~600 LOC, ~1.5 days focused work. **T2 canary becomes
feasible at commit 3.**

### 2.3 T2 canary after phase 1

```
insert task → classifying → backlog → planning → plan_review
                                          ↑
                          direct planner completes, writes plan.json
```

The `plan_review` gate is still FROZEN per standing directive, so
the canary task would park there. That's the intended T2 stopping
point: prove classify+plan roundtrip, then cancel.

## 3. Phase 2 — warm container

Directly analogous to classifier plan §§3-8. Adds:

- `cmd/drem-planner/{main,server,server_test}.go`
- `deploy/docker/planner.Dockerfile`
- `drem-planner` service in `deploy/compose/global.yml`
- `[agents.planner].endpoint` TOML key
- `DREM_PLANNER_URL` env in orch compose
- HTTP `POST /plan` / `GET /healthz` / `GET /metrics`

Refactor step: phase 1 wrote `RunDirectPlanner` with the full
DirectToolAgent dependency. For phase 2, extract the LLM + tool-loop
core into `agent.Plan(ctx, cfg, opts) (Result, error)` so both the
in-orch fallback and the new binary can import it. Same refactor
pattern as classifier plan commit 1.

4 commits for phase 2, ~500 LOC, ~1 day.

**Total planner effort: 8 commits, ~1100 LOC, 2.5 days.**

## 4. HTTP surface (phase 2)

### POST /plan

Request:
```json
{
  "task_id": "...",
  "plan_opts": {
    "task": {...},
    "project": {...},
    "worktree_path": "/home/.../feature/task-xxx",
    "comments": [...],
    "target_coder_provider": "sglang-direct",
    "target_coder_model": "gemma4-26b"
  }
}
```

Response 200 mirrors prep's shape: `{task_id, output_path,
tokens_in, tokens_out, duration_ms, tool_calls}` where `output_path`
is `<worktree_path>/plan.json`.

Errors: 400 (bad request), 401 (bad token), 409 (JSON parsed but
failed schema validation — plan has zero subtasks, for example), 502
(sglang unreachable), 504 (timeout), 500 (internal).

## 5. Plan validation

Planner output is high-risk — a malformed plan poisons every downstream
agent. The binary must validate before returning 200:

- `plan.json` parses.
- `subtasks` non-empty.
- Every `tests_for` index points to a real `subtasks` entry.
- Every `dependencies` index is valid.
- TDD pairing rule (`internal/prompt/prompt_planner.go:63-66`):
  each implementation subtask has exactly one test subtask whose
  `tests_for` contains only its index.

On validation failure, the binary retries up to `MaxValidationRetries`
(default 2) with the validation errors appended to the prompt as
feedback. Only after retries exhaust does it 409.

This is a material improvement over the legacy subprocess path,
which had no validation.

## 6. Test surface

Phase 1:
- `internal/agent/direct_planner_test.go` — canned plans, tool-loop
  behavior against mock sglang, JSON parse failures.
- `internal/orchestrator/task_processing_test.go` — add cases for
  the direct path, fall-through, validation errors.
- `cmd/drem/main_test.go` — `plannerDirect` gate tests.

Phase 2:
- `cmd/drem-planner/server_test.go` — same categories as classifier
  + validation-failure retry test.
- Compose/YAML smoke test.

## 7. Open questions

1. **Which model?** `gemma4-26b` for parity with classifier/prep is
   the default. Real-world planner quality may demand a larger
   reasoning model (operator has opinions). Parameterize via
   `[agents.planner].model` and make it easy to override per project.
2. **Validation retry budget.** 2 retries doubles planner latency
   on bad outputs. Worth metric+alert before dialing down.
3. **Does planner need the orchestrator API** (via `--orch-url`) like
   merger does? Merger POSTs merge_results back; planner writes
   `plan.json` which orch reads from the worktree. No orch call-back
   needed — planner is request/response only. **Confirm during
   phase 2 commit 2.**
4. **Integration with existing `clarification_session` path** — the
   legacy planner occasionally decides it needs clarification
   (`internal/orchestrator/clarification_handling.go`). Does the
   direct planner follow the same clarification decision protocol?
   Phase 1 commit 2 must replicate this or document the divergence.

## 8. What this doesn't solve

- Gate freeze still parks tasks at `plan_review`. T2 canary stops
  at that gate — by design.
- Planner quality — fidelity vs. cost / latency trade-offs live
  outside this plan.
- `researcher` agent type — covered by neither this plan nor the
  prep plan. Planner may emit subtasks with `agent_type: "researcher"`
  that nothing picks up. File a separate plan if researcher becomes
  a blocking role.
