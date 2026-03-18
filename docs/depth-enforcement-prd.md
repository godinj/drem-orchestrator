# PRD: Enforce Module Depth Through Orchestration Pipeline

> GitHub Issue: https://github.com/godinj/drem-orchestrator/issues/1

## Problem Statement

The orchestrator's architecture constitution currently enforces only structural constraints — line counts, import ceilings, formatting, placement of test helpers. These are useful guardrails, but they measure the shape of code without evaluating whether modules are well-designed. A codebase can pass all 9 existing rules and still be full of shallow modules: many small files with pass-through interfaces that redistribute complexity rather than encapsulating it.

Meanwhile, the planning skills (`write-a-prd`, `design-an-interface`) already reason about module depth — "extract deep modules that can be tested in isolation," "a lot of functionality behind a simple, testable interface which rarely changes" — but none of that understanding flows into the orchestrator's enforcement loop. The gap is: planning skills reason about depth, but the enforcement machinery can't verify it.

## Solution

Introduce module depth enforcement across the entire orchestration pipeline — from planning through implementation to merge — using four complementary approaches:

1. **Static depth constraints** added to the constitution that approximate depth through measurable heuristics
2. **Supervisor architectural review** that uses LLM judgment to evaluate depth when static checks fail or plans need review
3. **Prompt-level enforcement** that carries depth expectations into agent prompts so agents build deep modules from the start
4. **Plan depth scoring** that gates plan approval on whether the plan defines deep module boundaries

## User Stories

1. As an orchestrator operator, I want the constitution to detect shallow modules automatically, so that agents can't merge code that spreads complexity across pass-through interfaces
2. As an orchestrator operator, I want the planner agent to specify module boundaries and interface shapes in its plans, so that depth expectations are explicit before coding begins
3. As an orchestrator operator, I want a depth score on plans, so that I can quickly see whether a plan is architecturally sound without reading every subtask
4. As an orchestrator operator, I want the depth score to gate plan approval, so that shallow plans are caught before coding starts
5. As an orchestrator operator, I want the supervisor to review plans that fail the depth score, so that I get actionable guidance on whether the plan can be adjusted or the task concept is flawed
6. As an orchestrator operator, I want coder agents to receive task-specific depth guidance from the plan's module decisions, so that they implement code that conforms to the planned architecture
7. As an orchestrator operator, I want coder agents to receive constraint-derived depth guidance when no plan exists, so that depth is enforced even for ad-hoc tasks
8. As an orchestrator operator, I want depth constraints to run on the integration worktree alongside existing constitution checks, so that violations are caught before feature-to-main merge
9. As an orchestrator operator, I want the supervisor to diagnose depth constraint failures on the integration worktree, so that I get specific feedback about where violations occurred and what to do next
10. As an orchestrator operator, I want the supervisor to reject plans or implementations with comments on the task, so that the feedback loop is visible and auditable
11. As an orchestrator operator, I want depth constraints to use the same model as existing constraints (hard ceilings with grandfathered exceptions), so that enforcement is consistent
12. As an orchestrator operator, I want `tui/` grandfathered for depth constraints, so that boundary/adapter code isn't penalized for having a large export surface
13. As an orchestrator operator, I want `orchestrator/` to NOT be grandfathered for depth constraints, so that this initiative forces it to improve toward conformance
14. As an orchestrator operator, I want export ratio measured (exported symbols / total LOC), so that I can detect packages with disproportionately large public interfaces
15. As an orchestrator operator, I want interface growth rate tracked, so that I can detect when a package's exported surface area grows faster than its implementation
16. As an orchestrator operator, I want pass-through functions detected, so that I can identify functions that just delegate to another package without adding logic
17. As an orchestrator operator, I want depth constraints to exclude `_test.go` files from measurement, so that test stubs and doubles don't inflate the exported surface area
18. As an orchestrator operator, I want the depth score to evaluate whether plans specify module boundaries, interface shapes, and deep subtask decomposition, so that plan quality is assessed on architectural merit
19. As an orchestrator operator, I want the depth score to act as a fast gate before supervisor review, so that acceptable plans proceed without LLM cost and only failing plans trigger supervisor escalation
20. As an orchestrator operator, I want the plan depth score and supervisor review to work together: score gates first, supervisor escalates only on failure, so that the system is both fast and thorough
21. As an orchestrator operator, I want the supervisor's role to be advisory — diagnosing problems and recommending next steps — not automatically re-spawning agents, so that humans stay in the loop for architectural decisions
22. As an orchestrator operator, I want plan/subtask models to carry module boundary and interface shape metadata, so that depth expectations persist from planning through implementation

## Implementation Decisions

### Module Structure

- **`internal/constraints/depth/`** — New sub-package nested under the existing constraints package. This is the depth analysis engine: computes depth metrics for Go packages (export ratio, interface growth rate, pass-through detection). Simple interface (`Analyze(pkgPath) -> DepthReport`), hides AST parsing and heuristic logic internally. Testable in isolation against fixture packages, and also exercised through the parent `constraints/` package.

- **`internal/constraints/`** (modified) — Add depth constraint types alongside existing structural constraints. Wire depth checks into the constitution checking pipeline. Add grandfathering support: `tui/` is grandfathered, `orchestrator/` is not.

- **`internal/score/`** (modified) — Extend plan scoring with a depth dimension. Evaluate whether plans specify module boundaries, interface shapes, and deep decomposition. Return a score and pass/fail against a configurable threshold.

- **`internal/supervisor/`** (modified) — Add two new evaluation modes:
  - **Role A (Plan depth review)**: Reviews plans that fail the depth score. Determines whether the plan can be adjusted or the task concept is fundamentally flawed. Rejects with comments on the task.
  - **Role B (Depth constraint failure diagnosis)**: When static depth constraints fail on the integration worktree, navigates the diff, identifies where violations occurred, and rejects with actionable comments.
  - Both roles are advisory — they recommend next steps but do not automatically re-spawn agents.

- **`internal/prompt/`** (modified) — Planner prompts updated to explicitly instruct designing for depth and to require specifying module boundaries and interface shapes. Coder prompts updated to carry task-specific depth guidance from the plan's module decisions (priority), falling back to constraint-derived guidance (e.g., "package X has export ratio Y, keep below Z") when no plan-level guidance exists. Heavy-handed prescriptive style.

- **`internal/orchestrator/`** (modified) — Wire the depth score gate into the plan approval flow: after scoring, if acceptable proceed to human review; if unacceptable, escalate to supervisor. Wire depth constraint checks into the integration worktree gate alongside existing constitution checks.

- **`internal/model/`** (modified) — Extend plan and subtask models to carry module boundary and interface shape metadata.

### Constraint Configuration

- Depth constraints added to `.drem/constraints.toml` using the same model as existing constraints: hard ceilings with grandfathered exceptions.
- `_test.go` files excluded from depth metric calculations.
- Depth constraints run at the integration worktree gate (when subtasks merge onto the integration worktree), not at the per-agent level.

### Enforcement Flow

1. **Planner agent** receives prompt with heavy-handed depth guidance. Produces plan with subtasks, module boundaries, and interface shapes.
2. **Plan depth score** evaluates the plan as a fast heuristic gate.
3. **If score acceptable** → plan proceeds to human review.
4. **If score unacceptable** → supervisor (Role A) reviews, determines next steps, rejects with comments.
5. **Coder agents** receive prompts with depth guidance: task-specific from plan (priority) or constraint-derived (fallback).
6. **Subtasks merge to integration worktree** → depth constraints run alongside existing constitution checks.
7. **If constraints pass** → proceed to feature-to-main merge.
8. **If constraints fail** → supervisor (Role B) navigates diff, identifies violations, rejects with comments.

### Three Static Depth Heuristics

1. **Export ratio**: exported symbols / total LOC per package. A deep module has a low ratio (few exports relative to implementation size).
2. **Interface growth rate**: flag packages where exported surface area grows faster than implementation across the diff.
3. **Pass-through detection**: flag functions that delegate to another package without adding meaningful logic.

These form a layered detection approach — any single heuristic can miss cases, but together they provide strong coverage.

## Testing Decisions

- Tests should verify external behavior, not implementation details. A good test for depth analysis exercises the public `Analyze()` interface against fixture Go packages with known depth characteristics.
- **`internal/constraints/depth/`** should be tested in isolation with fixture packages that represent deep modules, shallow modules, pass-through patterns, and edge cases.
- **`internal/constraints/`** should have integration tests that exercise depth checks through the parent constraint-checking pipeline, ensuring depth violations are correctly reported alongside existing constraint violations.
- **`internal/score/`** depth scoring should be tested with fixture plans that have/lack module boundaries and interface shapes.
- **`internal/supervisor/`** new evaluation modes should be tested to verify they produce rejection comments with actionable feedback.
- **`internal/prompt/`** should be tested to verify depth guidance appears in generated prompts under both conditions (plan-derived and constraint-derived fallback).
- Prior art: existing constraint tests in `internal/constraints/` and score tests in `internal/score/` follow the same patterns.

## Out of Scope

- **Refactoring `orchestrator/` to conform to depth constraints.** This PRD introduces the enforcement machinery. The work to actually fix `orchestrator/`'s depth violations is a separate effort — this PRD just ensures it won't be grandfathered.
- **Depth enforcement for non-Go languages.** The heuristics are Go-specific (AST parsing, exported symbols). Extending to other languages is future work.
- **Automatic agent re-spawning on supervisor rejection.** The supervisor is advisory only. Automating the retry loop is a separate concern.
- **TUI changes for depth display.** The depth score will be visible in the existing plan scoring display. No new TUI panels or views are in scope.
- **Changes to the `/swarm` skill or `ctxmon`.** These are unaffected by depth enforcement.

## Further Notes

- The `orchestrator/` package is already grandfathered for file length (2,250 lines, must shrink) and internal imports (35, ceiling 6). Not grandfathering it for depth constraints is a deliberate choice to increase pressure toward decomposition.
- The depth heuristics may need tuning after initial rollout. The hard-ceiling model with grandfathering provides a natural path: start with generous ceilings, tighten over time, and grandfather packages that need migration work.
- The "heavy-handed" prescriptive style for prompts is a deliberate choice for the initial rollout. It can be relaxed later if agents demonstrate consistent depth-aware design.
