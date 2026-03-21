# PRD: Plan Clarification Loop

## Problem Statement

When a task description is ambiguous or underspecified, the planner agent has no way to ask the user for clarification. It receives the task description, generates a plan, and silently fills in gaps with assumptions. The user only discovers these assumptions during plan review, at which point the only options are to approve a plan built on guesses or reject it and hope the retry is better — but no rejection reason is passed back to the planner.

This leads to wasted planning cycles and plans that don't match user intent. The planner needs a way to surface its uncertainties and have an interactive conversation with the user before the plan is finalized.

## Solution

Introduce a clarification loop between PLANNING and PLAN_REVIEW that detects when a plan contains unresolved assumptions and lets the user answer questions before the plan is finalized.

The approach uses two layers of assumption detection:

1. **Planner self-reporting** — the planner includes an `assumptions` field in plan.json listing every choice it made that wasn't explicitly specified in the task description.
2. **Supervisor cross-checking** — the supervisor reviews the plan specifically looking for unstated assumptions the planner missed, since it has an outsider's perspective on the plan.

When assumptions are detected, the task enters a NEEDS_CLARIFICATION state where the user answers questions through the TUI. The planner then replans with the Q&A transcript as additional context. This loops until the planner is satisfied or the user signals `/done` to accept the plan as-is.

All clarification logic is encapsulated in a deep module (`internal/clarification/`) with a narrow interface that the orchestrator calls without knowing the internals.

## User Stories

1. As an orchestrator operator, I want the planner to self-report its assumptions alongside the plan, so that I can see where the planner filled in gaps rather than working from explicit requirements
2. As an orchestrator operator, I want the supervisor to cross-check plans for assumptions the planner missed, so that unstated decisions are caught even when the planner doesn't realize it's guessing
3. As an orchestrator operator, I want assumption detection to merge and deduplicate findings from both the planner and supervisor, so that I see a single consolidated list of questions
4. As an orchestrator operator, I want the task to enter a NEEDS_CLARIFICATION state when assumptions are detected, so that I can answer questions before the plan is finalized
5. As an orchestrator operator, I want to answer clarification questions through the TUI, so that the interaction is tracked and visible on the task
6. As an orchestrator operator, I want the planner to replan with the previous plan attempt and Q&A transcript as context, so that it refines its approach rather than starting from scratch
7. As an orchestrator operator, I want the clarification loop to continue for multiple rounds if needed, so that complex features can be fully explored before planning finalizes
8. As an orchestrator operator, I want to type `/done` to accept the current plan as-is and skip remaining questions, so that I can end the loop when I'm satisfied or when further questions aren't productive
9. As an orchestrator operator, I want the Q&A transcript compacted when passed back to the planner, so that context stays manageable across multiple rounds
10. As an orchestrator operator, I want tasks with simple, unambiguous descriptions to skip clarification entirely, so that trivial tasks don't incur unnecessary round-trips
11. As an orchestrator operator, I want the clarification history preserved on the task, so that if the plan still needs adjustments post-supervisor analysis the full context is available
12. As an orchestrator operator, I want the assumptions field in plan.json to include the decision made, alternatives considered, and reasoning, so that I have enough context to evaluate each assumption
13. As an orchestrator operator, I want the clarification module to be a deep module with a simple interface, so that the orchestrator's state machine stays clean and the clarification internals can evolve independently

## Implementation Decisions

### New task status: NEEDS_CLARIFICATION

- Sits between PLANNING and PLAN_REVIEW in the state machine
- The orchestrator transitions to this state when `clarification.Evaluate()` returns questions
- The orchestrator transitions out when `clarification.ProcessAnswer()` returns done (either no more questions or user sent `/done`)
- On exit, the task transitions back to PLANNING with replan context injected

### Assumptions field in plan.json

```json
"assumptions": [
  {
    "decision": "Using Redis for the cache layer",
    "alternatives": ["in-process LRU cache", "memcached"],
    "why_chosen": "task mentions shared across services"
  }
]
```

The planner prompt must explicitly instruct the planner to populate this field, distinguishing between what the task description specified and what the planner inferred.

### Deep module: `internal/clarification/`

Public interface:

- `Evaluate(plan, assumptions, supervisorAnalysis) → *Result` — merges planner-reported assumptions with supervisor-detected ones, deduplicates, and determines whether clarification is needed
- `ProcessAnswer(session, answer) → done bool` — records a user answer, detects `/done`, determines whether another round is needed
- `ReplanContext(session) → string` — returns the compacted Q&A transcript and previous plan for the planner's next attempt

Hidden internals:

- Merging and deduplication of planner + supervisor assumptions
- Question prioritization and ordering
- Transcript compaction logic across multiple rounds
- `/done` detection and round tracking
- Session state management

The `Session` type is internal to the package. The orchestrator stores it serialized in `task.Context` (consistent with existing patterns like `prompt_adjustment` and `plan_validation`).

### State machine transition

```
PLANNING → [plan received] → clarification.Evaluate()
  → needs clarification → NEEDS_CLARIFICATION
  → no clarification    → PLAN_REVIEW

NEEDS_CLARIFICATION → [user answers] → clarification.ProcessAnswer()
  → done     → PLANNING (with clarification.ReplanContext() injected)
  → not done → NEEDS_CLARIFICATION (next round)
```

### Prompt changes

- The planner prompt gains instructions to populate the `assumptions` field, with guidance like: "for each decision in your plan, note whether the task description specified it or you inferred it"
- The supervisor analysis prompt gains instructions to look for unstated assumptions the planner may have missed
- On replan, the prompt includes a "## Clarification Context" section with the compacted Q&A transcript from `ReplanContext()`

### TUI changes

- NEEDS_CLARIFICATION tasks display the current questions in the task detail view
- The user responds using the existing comment input (keybinding `c`), with answers routed to `clarification.ProcessAnswer()`
- `/done` in a response ends the loop and transitions the task back to PLANNING for a final replan

## Testing Decisions

A good test for this module verifies external behavior — given a plan with certain assumptions and supervisor feedback, does the module produce the right questions? Given a sequence of answers, does it correctly determine when clarification is complete?

The `internal/clarification/` package should be tested in isolation:

- `Evaluate`: given various combinations of planner assumptions and supervisor flags, verify correct question sets (merged, deduplicated, ordered)
- `ProcessAnswer`: given answer sequences including `/done`, verify correct done/not-done signals
- `ReplanContext`: given multi-round Q&A history, verify the output is compacted and includes the previous plan

Integration tests at the orchestrator level should verify state transitions:

- A plan with assumptions triggers NEEDS_CLARIFICATION
- A plan with no assumptions skips to PLAN_REVIEW
- User answers cycle through rounds correctly
- `/done` terminates the loop
- Replan context is injected into the planner prompt on retry

Prior art: existing plan validation tests in the orchestrator test suite.

## Out of Scope

- Changing the plan rejection flow (PLAN_REVIEW → reject → PLANNING). That existing loop is separate from pre-review clarification.
- Automated resolution of assumptions (e.g., the supervisor answering questions on behalf of the user). All clarification goes through the human.
- Clarification for non-planning phases (e.g., coder agents asking questions during implementation).
- Changes to the grill-me or write-a-prd skills themselves. This feature is inspired by their interview pattern but is implemented independently in the orchestrator.

## Further Notes

- The dual-layer detection (planner self-report + supervisor cross-check) is deliberate. LLMs have a known blind spot where they fill in gaps with "reasonable defaults" without recognizing they made a choice. The supervisor's outside perspective catches assumptions the planner normalized. Neither layer alone is sufficient.
- The `/done` escape valve is important for usability. Some tasks have genuinely ambiguous aspects that the user is comfortable leaving to the planner's judgment. Forcing resolution of every question would be counterproductive.
- Transcript compaction on replan keeps context manageable. Without it, multi-round clarifications could bloat the planner prompt and degrade plan quality.
