# Agent: Orchestrator Integration — Clarification Loop Wiring

You are working on the `master` branch of Drem Orchestrator, a multi-agent task orchestration system built in Go.
Your task is to wire the clarification loop into the orchestrator's state machine: parse assumptions from plan.json, call the clarification module after planning, handle the NEEDS_CLARIFICATION state, and inject replan context on retry.

## Context

Read these before starting:
- `docs/plan-clarification-prd.md` (§ "State machine transition", § "Assumptions field in plan.json", full document for overall flow)
- `internal/orchestrator/task_processing.go` (`processPlanning()` — this is where the plan-exists check happens at line 59; `processBacklog()` for replan patterns)
- `internal/orchestrator/handlers.go` (`parsePlan()`, `planEntry` struct, `HandlePlanApproved()`, `HandlePlanRejected()`)
- `internal/orchestrator/orchestrator.go` (`doTick()` dispatch loop where processing functions are called, `Orchestrator` struct)
- `internal/orchestrator/agent_results.go` (`onAgentCompleted()` — where plan.json is read after planner finishes, and `onAgentFailed()` — supervisor diagnosis pattern)
- `internal/clarification/clarification.go` (the clarification module's public API: `Evaluate()`, `ProcessAnswer()`, `ReplanContext()`, `Assumption` type)
- `internal/supervisor/prompts.go` (`AssumptionCrossCheckPrompt()`, `AssumptionCrossCheck` type)
- `internal/supervisor/supervisor.go` (`Supervisor.EvaluateJSON()` — how to call the supervisor)
- `internal/model/enums.go` (`StatusNeedsClarification`)
- `internal/state/machine.go` (valid transitions for the new status)

## Dependencies

This agent depends on Agent 01 (Model & State Machine), Agent 02 (Clarification Package), and Agent 03 (Prompt Updates). If those files don't exist yet, create stub implementations with the interfaces described in their prompts and implement against them.

## Deliverables

### Modified files

#### 1. `internal/orchestrator/handlers.go`

**A. Parse assumptions from plan.json.**

Update `parsePlanResult` to include assumptions:

```go
type parsePlanResult struct {
    Subtasks      []planEntry
    TDDExceptions []tddException
    Assumptions   []clarification.Assumption // NEW
}
```

Update `parsePlan()` to extract the `assumptions` field (backward compatible — missing key means empty slice):

```go
// Extract assumptions if present.
var assumptions []clarification.Assumption
if assumptionsRaw, hasAssumptions := planField["assumptions"]; hasAssumptions {
    ab, err := json.Marshal(assumptionsRaw)
    if err != nil {
        return nil, fmt.Errorf("parse plan: marshal assumptions: %w", err)
    }
    if err := json.Unmarshal(ab, &assumptions); err != nil {
        return nil, fmt.Errorf("parse plan: unmarshal assumptions: %w", err)
    }
}
```

Set `result.Assumptions = assumptions` before returning.

**B. Add `HandleClarificationAnswer()` public method.**

```go
// HandleClarificationAnswer processes a user's answer to a clarification question.
// Called by the TUI when the user submits a comment on a NEEDS_CLARIFICATION task.
func (o *Orchestrator) HandleClarificationAnswer(taskID uuid.UUID, answer string) error
```

Logic:
1. Load the task, verify status is `StatusNeedsClarification`
2. Retrieve `task.Context["clarification_session"]`
3. Call `clarification.ProcessAnswer(sessionData, answer)` → `updatedSession, done, nextQuestion`
4. Store `updatedSession` back in `task.Context["clarification_session"]`
5. If done:
   a. Call `clarification.ReplanContext(updatedSession)` → replanCtx
   b. Store replanCtx in `task.Context["clarification_context"]`
   c. Clear the plan: `task.Plan = nil`
   d. Transition task to `StatusPlanning` (replan with context)
6. If not done:
   a. Store `nextQuestion` in `task.Context["clarification_current_question"]` (for TUI display)
   b. Save task
7. Emit `"task_updated"` event

#### 2. `internal/orchestrator/task_processing.go`

**A. Modify `processPlanning()`** to call clarification.Evaluate after plan is received.

Replace the current plan-exists block (lines 59-72):

```go
// 1. If plan already exists, evaluate for clarification needs.
if task.Plan != nil {
    // Parse plan to extract assumptions.
    planResult, err := parsePlan(task.Plan)
    if err != nil {
        o.logger.Warn("process planning: parse plan for assumptions failed", "task_id", task.ID, "error", err)
        // Fall through to plan_review without clarification.
    }

    var assumptions []clarification.Assumption
    if planResult != nil {
        assumptions = planResult.Assumptions
    }

    // Supervisor cross-check for missed assumptions (if supervisor available).
    var supervisorAnalysis string
    if o.supervisor != nil && planResult != nil {
        planJSON, _ := json.Marshal(task.Plan)
        assumptionsJSON, _ := json.Marshal(assumptions)
        crossCheckPrompt := supervisor.AssumptionCrossCheckPrompt(
            task.Title, task.Description, string(planJSON), string(assumptionsJSON),
        )
        var crossCheck supervisor.AssumptionCrossCheck
        if err := o.supervisor.EvaluateJSON(context.Background(), crossCheckPrompt, &crossCheck); err != nil {
            o.logger.Warn("supervisor assumption cross-check failed", "task_id", task.ID, "error", err)
        } else {
            analysisJSON, _ := json.Marshal(crossCheck.MissedAssumptions)
            supervisorAnalysis = string(analysisJSON)
        }
    }

    // Evaluate whether clarification is needed.
    planJSON, _ := json.Marshal(task.Plan)
    evalResult, evalErr := clarification.Evaluate(string(planJSON), assumptions, supervisorAnalysis)
    if evalErr != nil {
        o.logger.Warn("clarification evaluate failed", "task_id", task.ID, "error", evalErr)
    }

    if evalResult != nil && evalResult.NeedsClarification {
        // Transition to NEEDS_CLARIFICATION.
        if task.Context == nil {
            task.Context = make(model.JSONField)
        }
        task.Context["clarification_session"] = evalResult.SessionData
        task.Context["clarification_questions"] = evalResult.Questions
        if len(evalResult.Questions) > 0 {
            task.Context["clarification_current_question"] = evalResult.Questions[0]
        }

        event, err := state.TransitionTask(task, model.StatusNeedsClarification, "orchestrator", nil)
        if err != nil {
            return fmt.Errorf("process planning: transition to needs_clarification: %w", err)
        }
        if err := o.db.Save(task).Error; err != nil {
            return fmt.Errorf("process planning: save task: %w", err)
        }
        if err := o.db.Create(event).Error; err != nil {
            return fmt.Errorf("process planning: save event: %w", err)
        }
        o.emit("needs_clarification", map[string]any{"task_id": task.ID, "questions": evalResult.Questions})
        return nil
    }

    // No clarification needed — proceed to plan_review.
    event, err := state.TransitionTask(task, model.StatusPlanReview, "orchestrator", nil)
    if err != nil {
        return fmt.Errorf("process planning: transition to plan_review: %w", err)
    }
    if err := o.db.Save(task).Error; err != nil {
        return fmt.Errorf("process planning: save task: %w", err)
    }
    if err := o.db.Create(event).Error; err != nil {
        return fmt.Errorf("process planning: save event: %w", err)
    }
    o.emit("plan_ready", map[string]any{"task_id": task.ID})
    return nil
}
```

**B. Add import** for `"github.com/godinj/drem-orchestrator/internal/clarification"` at the top of the file.

#### 3. `internal/orchestrator/orchestrator.go`

**A. Add `processNeedsClarification()` to the `doTick()` dispatch.**

Find the `doTick()` method's status dispatch (switch or if-chain). Add a case for `StatusNeedsClarification`:

```go
case model.StatusNeedsClarification:
    // Human gate — no automated processing. The TUI handles user input
    // via HandleClarificationAnswer(). Nothing to do here.
```

This is a no-op in the tick loop because it's a human gate (like `plan_review`), but it needs to be present so the orchestrator doesn't log warnings about unhandled statuses.

## Important: Backward Compatibility

The `assumptions` field in plan.json is optional. Plans without it (from existing planner agents) must continue to work. The `parsePlan()` change is backward compatible because missing `assumptions` key results in an empty slice, and `clarification.Evaluate()` with empty assumptions + empty supervisor analysis returns `NeedsClarification: false`, so the task proceeds directly to `plan_review` as before.

## Conventions

- Package: `orchestrator`
- Module path: `github.com/godinj/drem-orchestrator`
- Follow existing error handling patterns (wrap with context: `fmt.Errorf("process planning: ...: %w", err)`)
- Follow existing event emission patterns (`o.emit("event_name", data)`)
- Follow existing context storage patterns (`task.Context["key"] = value`)
- Run `gofmt` on all modified files
- Build verification: `go build ./cmd/drem && go test ./internal/orchestrator/...`
