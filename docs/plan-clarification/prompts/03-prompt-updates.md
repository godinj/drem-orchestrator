# Agent: Prompt Updates — Assumptions & Supervisor Cross-Check

You are working on the `master` branch of Drem Orchestrator, a multi-agent task orchestration system built in Go.
Your task is to update the planner prompt to instruct planners to populate an `assumptions` field in plan.json, update the supervisor to cross-check for missed assumptions, and add a clarification context section for replans.

## Context

Read these before starting:
- `docs/plan-clarification-prd.md` (§ "Assumptions field in plan.json", § "Prompt changes")
- `internal/prompt/prompt.go` (planner prompt generation — `plannerInstructions()` function, and `Generate()` for the clarification context injection point)
- `internal/supervisor/prompts.go` (existing supervisor prompt patterns — `FailureDiagnosisPrompt`, `PlanDepthReviewPrompt`)

## Deliverables

### Modified files

#### 1. `internal/prompt/prompt.go`

**A. Update `plannerInstructions()`** — add an `assumptions` field to the plan.json schema and instructions for the planner to populate it.

After the existing `"coverage"` field in the JSON schema example (around line 220-225), add:

```json
"assumptions": [
  {
    "decision": "what you decided",
    "alternatives": ["other option 1", "other option 2"],
    "why_chosen": "why you picked this over the alternatives"
  }
]
```

After the existing "Coverage Verification" section (around line 310), add a new section:

```
## Assumption Reporting

For each decision in your plan, evaluate whether the task description explicitly specified
it or whether you inferred it. Report ALL inferred decisions in the `assumptions` field.

An assumption is any decision where:
- The task description did not specify the approach and you chose one
- You selected a specific technology, library, or pattern that wasn't mentioned
- You made a scoping decision (included or excluded something not explicitly addressed)
- You interpreted an ambiguous requirement in a specific way

For each assumption, provide:
- `decision`: what you decided (e.g., "Using Redis for the cache layer")
- `alternatives`: other reasonable options you considered (at least one)
- `why_chosen`: your reasoning for this choice over the alternatives

If the task description is fully specified with no room for interpretation, the
assumptions array may be empty. But err on the side of reporting — if in doubt,
it's an assumption.
```

**B. Add clarification context injection in `Generate()`** — after the `prompt_adjustment` section (around line 84), add handling for `clarification_context`:

```go
// Clarification context from prior clarification loop.
if opts.Task.Context != nil {
    if clarCtx, ok := opts.Task.Context["clarification_context"].(string); ok && clarCtx != "" {
        sections = append(sections, clarCtx, "")
    }
}
```

Also add `"clarification_context"` and `"clarification_session"` to the skip list in the Additional Context loop (line 70-72) so these keys don't get dumped as raw context:

```go
switch key {
case "prompt_adjustment", "clarification_context", "clarification_session":
    continue
}
```

#### 2. `internal/supervisor/prompts.go`

Add a new prompt function for supervisor assumption cross-checking:

```go
// AssumptionCrossCheckPrompt builds a prompt for the supervisor to review a plan
// and identify assumptions the planner may have missed.
func AssumptionCrossCheckPrompt(taskTitle, taskDesc, planJSON string, reportedAssumptions string) string
```

The prompt should:
- Provide the task title, description, plan JSON, and planner's self-reported assumptions
- Ask the supervisor to identify decisions in the plan that the planner made without explicit specification from the task description but did NOT report as assumptions
- Instruct the supervisor to return ONLY a JSON object:

```json
{
  "missed_assumptions": [
    {
      "decision": "what the planner decided without being told to",
      "reasoning": "why this is an assumption the planner missed"
    }
  ],
  "assessment": "thorough|some_gaps|many_gaps"
}
```

- Use the same truncation patterns as existing prompts: `truncateForPrompt(taskDesc, truncTaskDesc)` for description, `truncateForPrompt(planJSON, truncDiffOutput)` for plan JSON
- If `reportedAssumptions` is empty, note that the planner reported no assumptions and the supervisor should be extra thorough

Follow the exact style of `PlanDepthReviewPrompt` — same format string approach, same truncation calls.

Add a corresponding response type:

```go
// AssumptionCrossCheck holds the supervisor's assessment of missed assumptions.
type AssumptionCrossCheck struct {
    MissedAssumptions []struct {
        Decision  string `json:"decision"`
        Reasoning string `json:"reasoning"`
    } `json:"missed_assumptions"`
    Assessment string `json:"assessment"`
}
```

Place this type near the existing `FailureDiagnosis` and other response types. If types are in a separate file (`types.go`), add it there; otherwise add it in `prompts.go`.

## Scope Limitation

Do NOT modify the orchestrator, TUI, state machine, or model packages. This agent only touches prompt generation and supervisor prompt text. The orchestrator integration agent will wire these prompts into the processing loop.

## Conventions

- Package: `prompt` and `supervisor`
- Module path: `github.com/godinj/drem-orchestrator`
- Follow existing formatting patterns in both files
- Run `gofmt` on all modified files
- Build verification: `go build ./internal/prompt/... ./internal/supervisor/... && go test ./internal/prompt/... ./internal/supervisor/...`
