# Agent: Model & State Machine — NEEDS_CLARIFICATION Status

You are working on the `master` branch of Drem Orchestrator, a multi-agent task orchestration system built in Go.
Your task is to add the `NEEDS_CLARIFICATION` task status to the model layer and state machine, enabling tasks to pause for user clarification between PLANNING and PLAN_REVIEW.

## Context

Read these before starting:
- `docs/plan-clarification-prd.md` (§ "New task status: NEEDS_CLARIFICATION" and § "State machine transition")
- `internal/model/enums.go` (current TaskStatus enum, `IsActionable()`, `IsHumanGate()`, `allTaskStatuses`)
- `internal/state/machine.go` (ValidTransitions map, `ValidateTransition()`, `TransitionTask()`)
- `internal/state/machine_test.go` (existing transition tests — follow the same patterns)

## Deliverables

### Modified files

#### 1. `internal/model/enums.go`

Add the new status constant and update related methods.

- Add `StatusNeedsClarification TaskStatus = "needs_clarification"` between `StatusPlanning` and `StatusPlanReview` in the const block
- Add it to `allTaskStatuses` in the same position
- Add `StatusNeedsClarification` to `IsHumanGate()` — this status requires the user to answer questions before proceeding

Do NOT add it to `IsActionable()` — the orchestrator does not take automated action on this status; it waits for user input via the TUI.

#### 2. `internal/state/machine.go`

Update `ValidTransitions` to include the new status and modify existing transitions:

Current `planning` transitions: `{plan_review, failed, paused}`
New `planning` transitions: `{needs_clarification, plan_review, failed, paused}`

New entry for `needs_clarification`:
```go
model.StatusNeedsClarification: {model.StatusPlanning, model.StatusPlanReview},
```

Rationale for the two outbound transitions:
- `needs_clarification → planning` — user answered questions, replan with clarification context
- `needs_clarification → plan_review` — no clarification needed (skip case) or user sent `/done`

Also add `needs_clarification` to the `paused` outbound list so paused tasks can resume to this state:
```go
model.StatusPaused: {model.StatusBacklog, model.StatusPlanning, model.StatusInProgress, model.StatusTestWriting, model.StatusNeedsClarification},
```

#### 3. `internal/state/machine_test.go`

Add tests that verify:

- `planning → needs_clarification` is valid
- `needs_clarification → planning` is valid (replan after clarification)
- `needs_clarification → plan_review` is valid (skip/done)
- `needs_clarification → in_progress` is invalid (can't skip plan_review)
- `needs_clarification → failed` is invalid (must go through planning first)
- `paused → needs_clarification` is valid
- `IsHumanGate()` returns true for `needs_clarification`
- `IsActionable()` returns false for `needs_clarification`
- `ParseTaskStatus("needs_clarification")` returns `StatusNeedsClarification`

Follow the existing test style in the file.

## Conventions

- Package: `model` (enums) and `state` (machine)
- Module path: `github.com/godinj/drem-orchestrator`
- Run `gofmt` on all modified files
- Build verification: `go build ./cmd/drem && go test ./internal/model/... ./internal/state/...`
