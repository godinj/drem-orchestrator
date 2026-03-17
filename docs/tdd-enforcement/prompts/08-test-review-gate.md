# Agent: TEST_REVIEW Human Gate & Rejection Flow

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent orchestrator that manages Claude Code agents via tmux in git bare repos with worktrees.
Your task is to implement the `TEST_REVIEW` human gate: approve/reject test subtasks, rejection cloning with feedback, 3-round diagnostic limit, and TUI integration.

## Context

Read these before starting:
- `docs/tdd-enforcement/prd-tdd-enforcement.md` (sections 4.4.3, 4.4.4)
- `internal/orchestrator/orchestrator.go` — read these sections:
  - `HandlePlanApproved` / `HandlePlanRejected` (around line 2272) — pattern for approve/reject handlers
  - `HandleTestPassed` / `HandleTestFailed` (around line 2412) — existing TESTING_READY handlers
  - `scheduleSubtasks` (around line 1810) — scheduling pattern
- `internal/tui/app.go` — read:
  - `handleApprove` and `handleReject` methods (around line 556) — TUI approve/reject dispatch
  - `handleBoardKeys` and `handleDetailKeys` — key handling
- `internal/tui/keys.go` — key binding definitions
- `internal/model/enums.go` (`StatusTestWriting`, `StatusTestReview`, `StatusRejected`)
- `internal/model/models.go` (Task struct with `Phase` field)

Key facts:
- The TUI already has `a` (approve) and `r` (reject) keybindings that dispatch to `handleApprove`/`handleReject`
- Those methods currently check for `StatusPlanReview` and `StatusTestingReady` — they need `StatusTestReview` added
- Rejection feedback is collected via the existing `FeedbackModel` dialog (used for plan rejection)

## Dependencies

This agent depends on Agent 06 (test-writing flow). The `processTestWriting`, `StatusTestWriting`, `StatusTestReview`, and phase-aware scheduling must exist.

## Deliverables

### 1. Modify `internal/orchestrator/orchestrator.go`

**a) Add `HandleTestReviewApproved`:**

```go
// HandleTestReviewApproved transitions from TEST_REVIEW to IN_PROGRESS,
// enabling implementation subtask scheduling.
func (o *Orchestrator) HandleTestReviewApproved(taskID uuid.UUID) error
```

Logic:
1. Load task, verify status is `TEST_REVIEW`
2. Transition to `IN_PROGRESS`
3. Save and emit event
4. Log: "test review approved, scheduling implementation"

**b) Add `HandleTestReviewRejected`:**

```go
// HandleTestReviewRejected marks rejected test subtasks as REJECTED, clones
// them with feedback, and transitions the parent back to TEST_WRITING.
// After 3 rejection rounds, pauses the task and spawns a diagnostic agent.
func (o *Orchestrator) HandleTestReviewRejected(taskID uuid.UUID, feedback string) error
```

Logic per §4.4.3:

1. Load task, verify status is `TEST_REVIEW`
2. Track rejection count: increment `task.Context["test_rejection_count"]` (default 0 → 1)
3. If rejection count reaches 3:
   - Transition task to `PAUSED` (via `TEST_WRITING` first if needed, or add `TEST_REVIEW → PAUSED` transition — check valid transitions)
   - Spawn a diagnostic agent (see step 6 below)
   - Return early
4. Load all test-phase subtasks in DONE state for this parent
5. For each:
   - Transition to `REJECTED` (mark as terminal)
   - Create a replacement subtask cloned from the rejected one:
     - Same title, but with " (revision N)" suffix
     - Description = original description + "\n\n## Rejection Feedback\n\n" + feedback
     - Same `Phase`, `TestsFor`, `Context` (copy `estimated_files`, `agent_type`)
     - Status: `BACKLOG`
     - `DependencyIDs`: same as original
6. Transition parent back to `TEST_WRITING`
7. Save and emit events

**c) Add diagnostic agent spawning for 3rd rejection:**

```go
// spawnDiagnosticAgent creates a diagnostic agent to help the human understand
// repeated test rejection patterns.
func (o *Orchestrator) spawnDiagnosticAgent(parent *model.Task) error
```

Logic:
1. Gather all test subtask history: for each rejection round, collect the subtask diffs and feedback
2. Build a diagnostic prompt:
   ```
   The tests for this task have been rejected 3 times. Help the human understand why.

   Task: <parent title and description>

   Round 1: <test subtask titles, feedback>
   Round 2: <test subtask titles, feedback>
   Round 3: <test subtask titles, feedback>

   Summarize the pattern of rejections and suggest a path forward.
   Either the test premise is wrong, the acceptance criteria are ambiguous,
   or there's a misunderstanding.
   ```
3. Spawn a reviewer-type agent on the integration branch worktree
4. The diagnostic agent writes its output to a file; the orchestrator doesn't wait for it — the task is paused and the diagnostic is advisory

If the diagnostic agent exhausts its context window before producing output, the orchestrator logs a warning and writes a fallback diagnostic to the task context: "Diagnostic agent was unable to complete analysis. Manual review of the 3 rejection rounds is required."

**d) Handle `REJECTED` status in `checkFeatureCompletion`:**

Update the terminal-state check to include `REJECTED`:

```go
// A subtask is terminal if it's DONE, FAILED, or REJECTED
func isTerminal(status model.TaskStatus) bool {
    return status == model.StatusDone || status == model.StatusFailed || status == model.StatusRejected
}
```

Use this helper wherever terminal-state checks exist.

### 2. Modify `internal/tui/app.go`

**a) Update `handleApprove`** to handle TEST_REVIEW:

```go
case model.StatusTestReview:
    err = m.orch.HandleTestReviewApproved(taskID)
```

**b) Update `handleReject`** to handle TEST_REVIEW with feedback:

For TEST_REVIEW rejection, the user needs to provide feedback. Use the existing `FeedbackModel` dialog pattern (same as plan rejection). The rejection handler should:
1. Open the feedback dialog
2. On submit, call `m.orch.HandleTestReviewRejected(taskID, feedbackText)`

Look at how plan rejection feedback works and replicate the same pattern.

**c) Update `IsHumanGate` usage in the TUI** — the status should show that TEST_REVIEW requires human action. Since `IsHumanGate()` already returns true for `StatusTestReview` (done by Agent 02), the TUI status display should already work. Verify that the approve/reject keybinding help text includes TEST_REVIEW.

### 3. Modify `internal/state/machine.go` (if needed)

If `TEST_REVIEW → PAUSED` is needed for the 3-round diagnostic flow, add it to ValidTransitions. Check if the current transitions allow this path. If not, the simplest approach is:
- `TEST_REVIEW → TEST_WRITING` (reject) → then from `TEST_WRITING → PAUSED` (which is already valid)

So the 3rd rejection would: transition to TEST_WRITING first, then immediately to PAUSED. This avoids adding a new transition.

### 4. Add tests

**`internal/orchestrator/test_review_test.go`** (new file):

- **HandleTestReviewApproved — happy path**: Parent in TEST_REVIEW → transitions to IN_PROGRESS
- **HandleTestReviewApproved — wrong status**: Parent not in TEST_REVIEW → error
- **HandleTestReviewRejected — first rejection**: Test subtasks marked REJECTED, replacements created with feedback in description, parent back to TEST_WRITING
- **HandleTestReviewRejected — rejection count increments**: First rejection → count=1, second → count=2
- **HandleTestReviewRejected — 3rd rejection pauses**: Count reaches 3, parent paused, diagnostic context set
- **Replacement subtask preserves Phase and TestsFor**: Cloned subtask has same Phase and TestsFor as original
- **REJECTED subtasks excluded from scheduling**: Parent in TEST_WRITING with REJECTED and BACKLOG test subtasks → only BACKLOG ones get scheduled

## Scope Limitation

ONLY modify:
- `internal/orchestrator/orchestrator.go`
- `internal/tui/app.go`
- `internal/state/machine.go` (only if a new transition is absolutely required)
- New test files in `internal/orchestrator/`

Do NOT modify: `internal/model/`, `internal/prompt/`, `internal/tui/keys.go`, `internal/tui/styles.go`.

## Conventions

- Go 1.22+ with standard library
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./...`
