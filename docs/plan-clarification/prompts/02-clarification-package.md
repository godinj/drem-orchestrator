# Agent: Clarification Package — Deep Module

You are working on the `master` branch of Drem Orchestrator, a multi-agent task orchestration system built in Go.
Your task is to create the `internal/clarification/` package — a deep module that encapsulates all clarification loop logic behind a narrow public interface.

## Context

Read these before starting:
- `docs/plan-clarification-prd.md` (§ "Deep module: internal/clarification/", § "Assumptions field in plan.json", § "Testing Decisions")
- `internal/model/json.go` (JSONField type — `map[string]any` with SQL serialization)
- `internal/model/models.go` (Task struct — note `Context JSONField` where session state will be stored)
- `internal/supervisor/supervisor.go` (pattern for a deep module with narrow public interface)

## Deliverables

### New files (`internal/clarification/`)

#### 1. `clarification.go`

The public interface of the clarification module. All internal types and logic are unexported.

**Exported types:**

```go
// Assumption represents a single planner-reported assumption.
type Assumption struct {
    Decision     string   `json:"decision"`
    Alternatives []string `json:"alternatives"`
    WhyChosen    string   `json:"why_chosen"`
}

// Result is returned by Evaluate to indicate whether clarification is needed.
type Result struct {
    NeedsClarification bool
    Questions          []string  // consolidated, deduplicated, ordered
    SessionData        any       // opaque — store in task.Context["clarification_session"]
}
```

**Exported functions:**

```go
// Evaluate merges planner-reported assumptions with supervisor-detected ones,
// deduplicates, prioritizes, and determines whether clarification is needed.
// supervisorAnalysis is the raw string from the supervisor's cross-check.
// Returns a Result with NeedsClarification=false if assumptions is empty and
// supervisorAnalysis finds nothing.
func Evaluate(planJSON string, assumptions []Assumption, supervisorAnalysis string) (*Result, error)

// ProcessAnswer records a user answer for the current question, detects the
// /done command, and determines whether another round of questions is needed.
// sessionData is the opaque value from Result.SessionData or a previous
// ProcessAnswer call. Returns updated session data, whether the loop is done,
// and the next question (empty if done).
func ProcessAnswer(sessionData any, answer string) (updatedSession any, done bool, nextQuestion string, err error)

// ReplanContext builds the compacted Q&A transcript and previous plan summary
// for injection into the planner prompt on retry. The returned string is
// markdown-formatted for direct inclusion in the prompt.
func ReplanContext(sessionData any) (string, error)
```

**Hidden internals (unexported):**

- `session` struct — tracks rounds, Q&A pairs, current question index, previous plan JSON, original assumptions
- `mergeAssumptions(planner []Assumption, supervisorRaw string) []assumption` — parses supervisor output, merges with planner assumptions, deduplicates by semantic similarity (simple string-distance or substring match)
- `prioritizeQuestions(merged []assumption) []string` — converts assumptions to user-facing questions, orders by impact (decisions with more alternatives first, then alphabetical)
- `compactTranscript(rounds []round) string` — summarizes multi-round Q&A into a concise markdown block, dropping redundant context
- `isDone(answer string) bool` — detects `/done` command (case-insensitive, with optional trailing whitespace)

**Session serialization:**

The `session` struct must be JSON-serializable so the orchestrator can store it in `task.Context["clarification_session"]`. The `SessionData` field in `Result` and `ProcessAnswer` uses `any` so callers don't depend on the internal type. Internally, marshal/unmarshal to `session` struct.

Pattern to follow: `task.Context["plan_validation"]` stores a `PlanValidationResult` as a JSON-serialized `any` in the existing codebase.

**Evaluate logic:**

1. Parse `supervisorAnalysis` to extract any assumptions the supervisor identified (expect a JSON array of `{"decision": "...", "reasoning": "..."}` objects, but handle malformed input gracefully — log and skip)
2. Merge planner assumptions with supervisor-detected ones
3. Deduplicate: if two assumptions reference the same decision (case-insensitive substring match on the `decision` field), keep the one with more detail (longer alternatives list)
4. If merged list is empty, return `NeedsClarification: false`
5. Convert each merged assumption to a question: `"You chose: <decision>. Alternatives considered: <alternatives>. Reason: <why>. Is this the right call?"` (but phrased more naturally)
6. Create a new session, return it as SessionData

**ProcessAnswer logic:**

1. Deserialize sessionData to internal session
2. Record the answer for the current question
3. Check `isDone(answer)` — if true, mark session as done
4. Advance to next question — if no more questions, mark done
5. Return updated serialized session, done flag, and next question text

**ReplanContext logic:**

1. Deserialize sessionData to internal session
2. Build markdown:
   ```
   ## Clarification Context

   The user answered the following questions about your previous plan:

   **Q:** <question>
   **A:** <answer>

   ...

   Incorporate these answers into your revised plan. Do not re-ask resolved questions.
   ```
3. If more than 5 Q&A pairs, compact: group by theme, summarize answers

#### 2. `clarification_test.go`

Test the public interface thoroughly:

**Evaluate tests:**
- Empty assumptions + empty supervisor analysis → `NeedsClarification: false`
- Planner assumptions only → questions generated from each assumption
- Supervisor analysis only → questions generated from supervisor findings
- Both planner and supervisor → merged, deduplicated (test with overlapping decisions)
- Malformed supervisor JSON → graceful handling, planner assumptions still work
- Single assumption → exactly one question

**ProcessAnswer tests:**
- Answer first question → not done, next question returned
- Answer all questions sequentially → done after last
- `/done` on first question → done immediately
- `/Done` and `/DONE` → case-insensitive detection
- `/done ` with trailing whitespace → still detected
- Answer then `/done` → done, previous answers preserved
- Calling ProcessAnswer after done → error or no-op

**ReplanContext tests:**
- Single round Q&A → markdown contains question and answer
- Multi-round Q&A → all rounds present
- Session with `/done` before all questions answered → only answered questions in output
- Output contains `## Clarification Context` header
- Output contains instruction to incorporate answers

**Integration flow test:**
- `Evaluate` → get session → `ProcessAnswer` in loop → `ReplanContext` — verify end-to-end round-trip

## Scope Limitation

This package has NO dependencies on the orchestrator, TUI, database, or any other internal package. It is a pure logic module that operates on data structures. The only imports should be from the standard library (`encoding/json`, `fmt`, `strings`, `sort`, `testing`).

Do NOT import `internal/model`, `internal/supervisor`, or any other project package. The `Assumption` type is defined locally in this package.

## Conventions

- Package: `clarification`
- Module path: `github.com/godinj/drem-orchestrator/internal/clarification`
- All internal types and functions are unexported
- Exported surface: 2 types (`Assumption`, `Result`) + 3 functions (`Evaluate`, `ProcessAnswer`, `ReplanContext`)
- Run `gofmt` on all files
- Build verification: `go build ./internal/clarification/... && go test ./internal/clarification/...`
