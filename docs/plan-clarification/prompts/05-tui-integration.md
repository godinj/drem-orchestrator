# Agent: TUI Integration — Clarification Question Display & Input

You are working on the `master` branch of Drem Orchestrator, a multi-agent task orchestration system built in Go.
Your task is to update the TUI to display clarification questions for tasks in NEEDS_CLARIFICATION status and route user answers to the orchestrator.

## Context

Read these before starting:
- `docs/plan-clarification-prd.md` (§ "TUI changes")
- `internal/tui/detail.go` (`DetailModel`, `View()`, `availableActions()` — how task details are rendered and actions listed per status)
- `internal/tui/model.go` (main `Model` struct, `Update()` message handling, comment input flow — search for `StatusPlanReview` to see how human-gate actions are dispatched)
- `internal/tui/styles.go` (color constants and style helpers used in detail view)
- `internal/model/enums.go` (`StatusNeedsClarification` — the new status constant)
- `internal/orchestrator/orchestrator.go` (`HandleClarificationAnswer()` — the method the TUI calls)

## Dependencies

This agent depends on Agent 01 (Model & State Machine) for `StatusNeedsClarification` and Agent 04 (Orchestrator Integration) for `HandleClarificationAnswer()`. If `HandleClarificationAnswer` doesn't exist yet, implement against this signature:

```go
func (o *Orchestrator) HandleClarificationAnswer(taskID uuid.UUID, answer string) error
```

## Deliverables

### Modified files

#### 1. `internal/tui/detail.go`

**A. Display clarification questions in `View()`.**

After the plan subtasks section (around line 198) and before the review results section, add a new section for clarification questions:

```go
// Clarification questions (for needs_clarification status).
if d.task.Status == model.StatusNeedsClarification && d.task.Context != nil {
    clarStyle := lipgloss.NewStyle().Foreground(colorWarning)
    sections = append(sections, clarStyle.Render("Clarification Needed:"))

    // Show all questions with answered/pending indicators.
    if questions, ok := d.task.Context["clarification_questions"].([]any); ok {
        for i, q := range questions {
            if qs, ok := q.(string); ok {
                prefix := "  ? "
                sections = append(sections, fmt.Sprintf("%s%d. %s", prefix, i+1, qs))
            }
        }
    }

    // Highlight the current question.
    if current, ok := d.task.Context["clarification_current_question"].(string); ok {
        currentStyle := lipgloss.NewStyle().Foreground(colorInfo).Bold(true)
        sections = append(sections, "")
        sections = append(sections, currentStyle.Render("Current question:"))
        sections = append(sections, currentStyle.Render("  "+current))
        sections = append(sections, subtitleStyle.Render("  Press [c] to answer, or type /done to accept plan as-is"))
    }
}
```

**B. Update `availableActions()` for the new status.**

Add a case to the switch statement in `availableActions()`:

```go
case model.StatusNeedsClarification:
    parts = append(parts, "[c]larify (answer question)", "[p]ause")
```

#### 2. `internal/tui/model.go`

**A. Route comment input for NEEDS_CLARIFICATION tasks.**

Find the comment submission handler — the code path where a user presses `c`, types a comment, and submits it. This likely involves a text input model and a submission action.

When the selected task's status is `StatusNeedsClarification`, the submitted comment text should be routed to `HandleClarificationAnswer()` instead of (or in addition to) the normal comment creation flow:

```go
if d.task.Status == model.StatusNeedsClarification {
    // Route to clarification handler instead of normal comment.
    if err := m.orchestrator.HandleClarificationAnswer(d.task.ID, commentText); err != nil {
        // Handle error (log or show in status bar).
    }
} else {
    // Normal comment flow.
    // ... existing code ...
}
```

The comment text should ALSO be saved as a normal TaskComment (author: "user") so it appears in the comment thread for visibility. This way the clarification Q&A is visible in the task's comment history.

**B. Handle `/done` display.** No special TUI handling needed — `/done` is detected by `clarification.ProcessAnswer()` on the orchestrator side. The TUI just submits whatever the user types.

## Scope Limitation

Do NOT modify the clarification package, orchestrator processing logic, state machine, or prompt generation. This agent only touches TUI rendering and input routing. The orchestrator handles all state transitions.

Only modify `detail.go` and the main TUI model file. Do not restructure the TUI or add new files.

## Conventions

- Package: `tui`
- Module path: `github.com/godinj/drem-orchestrator`
- Follow existing Bubble Tea patterns in the file
- Use existing style constants (`colorWarning`, `colorInfo`, `subtitleStyle`, etc.)
- Follow existing comment input patterns for the answer routing
- Run `gofmt` on all modified files
- Build verification: `go build ./cmd/drem && go test ./internal/tui/...`
