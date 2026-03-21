# Agent: Add Plan Clarification Section to README

You are working on the `master` branch of drem-orchestrator, a Go-based task orchestrator.
Your task is to add a user-facing "Plan Clarification" section to README.md.

## Context

Read these before starting:
- `README.md` (the file to update — find the "Task Lifecycle" section)
- `docs/plan-clarification-prd.md` (design background — extract user-relevant details)
- `internal/clarification/` (all `.go` files — understand the API and data flow)
- `internal/orchestrator/handlers.go` (lines 500-620 — the TUI handler for clarification answers)
- `internal/orchestrator/task_processing.go` (lines 58-100 — where clarification triggers)
- `internal/tui/` (search for "clarification" — find the TUI interaction pattern)

## Dependencies

This agent depends on Agent 08 (ARCHITECTURE.md update) for the updated lifecycle diagram. If it hasn't landed, create the section independently — it will be consistent.

## Deliverables

### Add Plan Clarification section to `README.md`

#### 1. New section: `## Plan Clarification`

Insert after the "Task Lifecycle" section (after the state descriptions, before "Step Scores").

Content must cover (in operator-friendly language, not developer jargon):

1. **What it is**: After a planner agent generates a plan, the system evaluates the plan's assumptions to determine if any are risky or unclear. If so, it enters a clarification loop where the user is asked targeted questions before the plan proceeds to review.

2. **When it triggers**: After `planning` completes, before `plan_review`. The task enters `needs_clarification` state. If no assumptions need clarification, the task skips directly to `plan_review`.

3. **How to interact in the TUI**:
   - When a task is in `needs_clarification`, the detail panel shows the current question
   - Press `c` to answer the current question
   - Type your answer and press Enter to submit
   - Type `/done` to finish the clarification round early
   - The system may ask follow-up questions based on your answers
   - Once all questions are answered, the plan either proceeds to `plan_review` or goes back for replanning with your clarification context

4. **What happens behind the scenes** (brief): The supervisor evaluates each assumption's risk level. High-risk assumptions with user-facing impact generate questions. Answers are deduplicated and fed back into the planning context.

#### 2. Update task lifecycle diagram

Update the ASCII flow diagram to include `needs_clarification` between `planning` and `plan_review`:

```
planning ──► needs_clarification ──► plan_review
                    │
                    ▼
                 planning (replan with clarification context)
```

#### 3. Update state descriptions

Add to the state description list:
- **needs_clarification** — Plan assumptions need human input; the TUI shows clarification questions

### Scope Limitation

- Only modify `README.md`
- Write for project operators, not developers
- Keep the same style as existing sections (concise, with examples where helpful)
- Do NOT duplicate the full PRD — extract only what a user needs to know

## Verification

```bash
# Section exists:
grep -n "Plan Clarification" README.md

# State mentioned:
grep "needs_clarification" README.md

# README still renders (no broken markdown):
# Visual check — ensure no unclosed code blocks or broken tables
```
