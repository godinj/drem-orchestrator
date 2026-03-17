# Agent: State Machine & Enums for TDD Enforcement

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent orchestrator that manages Claude Code agents via tmux in git bare repos with worktrees.
Your task is to add the `TEST_WRITING`, `TEST_REVIEW`, and `REJECTED` states to the task lifecycle, and update the state machine transitions.

## Context

Read these before starting:
- `docs/tdd-enforcement/prd-tdd-enforcement.md` (sections 4.1.1, 4.1.2)
- `internal/model/enums.go` (TaskStatus constants, `allTaskStatuses`, `IsActionable`, `IsHumanGate`)
- `internal/state/machine.go` (ValidTransitions map, `TransitionTask`)

## Deliverables

### 1. Modify `internal/model/enums.go`

**a) Add three new TaskStatus constants:**

```go
StatusTestWriting TaskStatus = "test_writing"
StatusTestReview  TaskStatus = "test_review"
StatusRejected    TaskStatus = "rejected"
```

**b) Add them to `allTaskStatuses`** so `ParseTaskStatus` recognizes them.

**c) Update `IsActionable()`** to include `StatusTestWriting` — the orchestrator schedules test-phase subtasks in this state:

```go
case StatusBacklog, StatusPlanning, StatusInProgress, StatusTestWriting, StatusMerging:
    return true
```

**d) Update `IsHumanGate()`** to include `StatusTestReview` — this is the new human review gate for test quality:

```go
case StatusPlanReview, StatusTestReview, StatusTestingReady:
    return true
```

### 2. Modify `internal/state/machine.go`

**Update `ValidTransitions` to match the PRD's updated state machine (§4.1.2):**

```go
var ValidTransitions = map[model.TaskStatus][]model.TaskStatus{
    model.StatusBacklog:       {model.StatusPlanning, model.StatusPaused},
    model.StatusPlanning:      {model.StatusPlanReview, model.StatusFailed, model.StatusPaused},
    model.StatusPlanReview:    {model.StatusTestWriting, model.StatusPlanning},
    model.StatusTestWriting:   {model.StatusTestReview, model.StatusFailed, model.StatusPaused},
    model.StatusTestReview:    {model.StatusInProgress, model.StatusTestWriting},
    model.StatusInProgress:    {model.StatusTestingReady, model.StatusFailed, model.StatusPaused},
    model.StatusTestingReady:  {model.StatusMerging, model.StatusInProgress, model.StatusPlanning},
    model.StatusMerging:       {model.StatusDone, model.StatusFailed},
    model.StatusPaused:        {model.StatusBacklog, model.StatusPlanning, model.StatusInProgress, model.StatusTestWriting},
    model.StatusDone:          {},
    model.StatusFailed:        {model.StatusBacklog, model.StatusInProgress},
    model.StatusRejected:      {},
}
```

Key changes from the current map:
- `PLAN_REVIEW` now transitions to `TEST_WRITING` instead of `IN_PROGRESS`
- `TEST_WRITING` is new: can go to `TEST_REVIEW`, `FAILED`, or `PAUSED`
- `TEST_REVIEW` is new: can go to `IN_PROGRESS` (approve) or `TEST_WRITING` (reject)
- `PAUSED` gains `TEST_WRITING` as a valid target
- `REJECTED` is terminal (empty targets) — used for subtasks rejected at TEST_REVIEW

### 3. Add/update tests

**a) `internal/model/enums_test.go`** — add test cases (or create the file if it doesn't exist):

- `ParseTaskStatus` recognizes `"test_writing"`, `"test_review"`, `"rejected"`
- `IsActionable` returns true for `StatusTestWriting`, false for `StatusTestReview`, false for `StatusRejected`
- `IsHumanGate` returns true for `StatusTestReview`

**b) `internal/state/machine_test.go`** — add table-driven test cases (or extend existing):

- `PLAN_REVIEW → TEST_WRITING` is valid
- `PLAN_REVIEW → IN_PROGRESS` is now INVALID (this is a breaking change from the old flow)
- `TEST_WRITING → TEST_REVIEW` is valid
- `TEST_WRITING → FAILED` is valid
- `TEST_WRITING → PAUSED` is valid
- `TEST_WRITING → IN_PROGRESS` is INVALID (must go through TEST_REVIEW)
- `TEST_REVIEW → IN_PROGRESS` is valid (approve)
- `TEST_REVIEW → TEST_WRITING` is valid (reject)
- `TEST_REVIEW → MERGING` is INVALID
- `PAUSED → TEST_WRITING` is valid
- `REJECTED → *` is invalid (terminal state)
- `TransitionTask` succeeds for `TEST_WRITING → TEST_REVIEW` and populates event fields correctly

## Scope Limitation

ONLY modify files in `internal/model/` and `internal/state/`. Do NOT touch `internal/orchestrator/`, `internal/prompt/`, `internal/tui/`, or any other package.

## Conventions

- Go 1.22+ with standard library
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./...`
