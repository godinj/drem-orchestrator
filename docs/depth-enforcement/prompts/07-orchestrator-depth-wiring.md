# Agent: Orchestrator Depth Wiring

You are working on the `master` branch of drem-orchestrator, a Go-based multi-agent orchestrator with TDD enforcement and constitution-based quality constraints.
Your task is to wire depth enforcement into the orchestrator's plan approval flow and integration worktree gate.

## Context

Read these specs before starting:
- `docs/depth-enforcement-prd.md` (Enforcement Flow, User stories 4, 8-10, 19-20)
- `internal/orchestrator/orchestrator.go` (Tick loop, plan approval flow, integration gate)
- `internal/orchestrator/plan_validation.go` (ValidatePlan, plan approval pipeline)
- `internal/orchestrator/score_bridge.go` (inline scoring that mirrors internal/score)
- `internal/constraints/config.go` (Config with Depth field — modified by Agent 03)
- `internal/constraints/evaluate.go` (Evaluate now handles depth constraints — modified by Agent 03)
- `internal/score/score.go` (StepScore with Depth field, scorePlanDepth — modified by Agent 04)
- `internal/supervisor/types.go` (PlanDepthReview, DepthConstraintDiagnosis — added by Agent 06)
- `internal/supervisor/prompts.go` (PlanDepthReviewPrompt, DepthConstraintDiagnosisPrompt — added by Agent 06)

## Dependencies

This agent depends on all previous agents:
- Agent 03 (constraints-depth-integration): depth constraints in the evaluation pipeline
- Agent 04 (plan-depth-scoring): `StepScore.Depth` field and `scorePlanDepth`
- Agent 06 (supervisor-depth-roles): `PlanDepthReview`, `DepthConstraintDiagnosis`, prompt functions

If any of these don't exist yet, implement against the interfaces documented in those agent prompts.

## Deliverables

### Migration (internal/orchestrator/)

#### 1. score_bridge.go

The orchestrator has an inline `score_bridge.go` that mirrors `internal/score` types. Update it to include the `Depth` field:

- Add `Depth float64` to the local `StepScore` equivalent (if it exists)
- Ensure `FormatScores` and `ScoresToMap` include the depth dimension
- If `score_bridge.go` uses the `score` package directly, just ensure the new field flows through

#### 2. orchestrator.go — Plan approval flow

Find where plans transition from `plan_review` to `in_progress` (or where plan scoring happens). Add depth score gating:

After the existing plan scoring logic:

1. Check `scores.Depth` against a threshold (use `0.5` as the initial threshold — plans must score at least 50% on depth)
2. If depth score ≥ threshold: proceed normally (plan goes to human review)
3. If depth score < threshold: escalate to supervisor for plan depth review:
   - Call `supervisor.EvaluateJSON(ctx, supervisor.PlanDepthReviewPrompt(task.Title, task.Description, planJSON, scores.Depth), &review)`
   - Add the supervisor's `RejectionReason` as a system comment on the task
   - Store the `PlanDepthReview` in `task.Context["depth_review"]`
   - Log the escalation

The depth score gate should run AFTER existing scoring and BEFORE human review. It is a fast pre-filter: acceptable plans proceed to human review, unacceptable plans get supervisor diagnosis first (then still proceed to human review with the diagnosis attached).

Important: The supervisor review does NOT block the plan from reaching human review. It adds diagnostic information. The human still makes the final decision.

#### 3. orchestrator.go — Integration worktree gate

Find where the integration worktree constraint check runs (typically when all subtasks are done and the parent transitions toward `testing_ready`). Depth constraints are already wired into `constraints.Evaluate` by Agent 03, so they run automatically.

Add depth-specific failure handling:

1. After `constraints.Evaluate` runs, check if any depth constraint results failed
2. If depth constraints failed: escalate to supervisor for diagnosis:
   - Call `supervisor.EvaluateJSON(ctx, supervisor.DepthConstraintDiagnosisPrompt(task.Title, constraintReport, diff), &diagnosis)`
   - Add the supervisor's `RejectionReason` as a system comment on the task
   - Store the `DepthConstraintDiagnosis` in `task.Context["depth_diagnosis"]`
   - Log the diagnosis
3. The existing constraint failure handling continues as normal — depth failures are treated the same as any other constraint failure

To identify depth-specific failures in the constraint report, check for `Result` entries where `Type == "depth"` (or check the result `Name` against known depth constraint names from the config).

#### 4. orchestrator_test.go or depth_wiring_test.go

Test the depth wiring:

- **Plan depth score above threshold passes**: plan with depth score ≥ 0.5 proceeds to plan_review without supervisor call
- **Plan depth score below threshold triggers supervisor**: plan with depth score < 0.5 triggers `PlanDepthReviewPrompt` call, system comment added to task
- **Depth constraint failure triggers supervisor diagnosis**: constraint report with depth failure triggers `DepthConstraintDiagnosisPrompt`, system comment added
- **Depth constraint pass proceeds normally**: constraint report with all-pass depth results proceeds without supervisor call
- **Supervisor failure is graceful**: if supervisor call fails (timeout, parse error), log and continue — don't block the pipeline

Use the existing test patterns from `plan_validation_test.go` and `post_agent_constraint_test.go`. Mock the supervisor by using a test helper that returns canned JSON responses.

## Scope Limitation

- Do NOT modify the constraint evaluation pipeline — depth constraints are already wired by Agent 03
- Do NOT modify the scoring logic — depth scoring is already implemented by Agent 04
- Do NOT modify supervisor prompt templates — they are owned by Agent 06
- Do NOT implement automatic re-planning on depth failure — the supervisor is advisory only
- Do NOT change the depth score threshold without documenting the change — the initial threshold of 0.5 is a starting point
- Minimize changes to `orchestrator.go` — this file is already at 2,250 lines with a shrink-only constraint. Add the minimum code necessary and consider extracting depth-specific logic into a helper function or a small new file (e.g., `depth_gate.go`) if it helps stay under the baseline

## Conventions

- Package: `package orchestrator`
- Go 1.22+ with standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Table-driven tests with `t.Run()` sub-tests
- Build verification: `go build ./internal/orchestrator/ && go test ./internal/orchestrator/ -v`
- CRITICAL: `orchestrator.go` has a shrink-only baseline of 2,250 lines. Do NOT increase the line count. If you need to add logic, extract it into a separate file or replace existing code.
