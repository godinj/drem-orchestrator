# Agent: Plan Schema & Model Extensions for TDD

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent orchestrator that manages Claude Code agents via tmux in git bare repos with worktrees.
Your task is to extend the data model and plan parsing to support TDD phases, test-to-implementation mapping, and TDD exceptions.

## Context

Read these before starting:
- `docs/tdd-enforcement/prd-tdd-enforcement.md` (sections 4.2, 4.2.1, 4.2.2, 4.2.3, Phase 1d, 1e)
- `internal/model/models.go` (Task struct, SubtaskPlan struct, JSONField/JSONArray types)
- `internal/model/json_types.go` (JSONField, JSONArray implementations)
- `internal/orchestrator/orchestrator.go` — search for `planEntry` struct and `parsePlan` function (near end of file, around line 2949)

Key facts:
- `planEntry` is the internal struct used to parse plan JSON from planner agents
- `SubtaskPlan` in `models.go` is an older struct — it should be updated to match
- The Task model uses GORM AutoMigrate — adding new fields automatically adds DB columns
- `JSONField` is `map[string]any` with custom JSON scanning; `JSONArray` is `[]string`

## Deliverables

### 1. Modify `internal/model/models.go`

**a) Add new fields to the `Task` struct:**

```go
type Task struct {
    // ... existing fields ...

    // TDD fields (used for subtasks)
    Phase    string    `gorm:"default:''"` // "test", "implementation", "integration", or ""
    TestsFor JSONArray `gorm:"type:text"`  // indices of impl subtasks this test covers (test-phase only)

    // TDD fields (used for parent tasks)
    TDDExceptions    JSONField `gorm:"type:text"` // planner-declared TDD exceptions
    NeedsHumanReview bool      `gorm:"default:false"` // set when fixer escalates to human

    // ... existing relations ...
}
```

`Phase` is a plain string rather than an enum because it only applies to subtasks created from TDD plans, and existing subtasks will have an empty string (backward compatible).

`TestsFor` uses `JSONArray` (which stores as `["0", "1"]` text in SQLite) for consistency with `DependencyIDs`. The orchestrator converts to `int` at read time.

**b) Update `SubtaskPlan` to include the new fields:**

```go
type SubtaskPlan struct {
    Title          string   `json:"title"`
    Description    string   `json:"description"`
    AgentType      string   `json:"agent_type"`
    EstimatedFiles []string `json:"estimated_files"`
    Phase          string   `json:"phase,omitempty"`
    TestsFor       []int    `json:"tests_for,omitempty"`
}
```

### 2. Modify `internal/orchestrator/orchestrator.go` — plan parsing only

**a) Extend `planEntry` struct:**

```go
type planEntry struct {
    Title          string   `json:"title"`
    Description    string   `json:"description"`
    AgentType      string   `json:"agent_type"`
    EstimatedFiles []string `json:"estimated_files"`
    Files          []string `json:"files"`
    Dependencies   []int    `json:"dependencies"`
    Priority       int      `json:"priority"`
    IsTest         bool     `json:"is_test,omitempty"`
    Phase          string   `json:"phase,omitempty"`
    TestsFor       []int    `json:"tests_for,omitempty"`
}
```

**b) Extend `parsePlan` to also extract `tdd_exceptions`:**

The current `parsePlan` extracts the `subtasks` array from plan JSON. Extend it to also return TDD exceptions. Change the signature to:

```go
type tddException struct {
    SubtaskIndex int    `json:"subtask_index"`
    Reason       string `json:"reason"`
}

// parsePlanResult holds the full parsed plan output.
type parsePlanResult struct {
    Subtasks      []planEntry
    TDDExceptions []tddException
}

func parsePlan(plan model.JSONField) (*parsePlanResult, error)
```

Update the JSON unmarshaling to handle this structure:

```json
{
    "subtasks": [...],
    "tdd_exceptions": [
        {"subtask_index": 2, "reason": "Integration wiring only"}
    ]
}
```

If `tdd_exceptions` is absent, return an empty slice (not an error — backward compatible with old plans).

**c) Update all callers of `parsePlan`** to use the new return type. There are multiple call sites — search for `parsePlan(` to find them all. Each currently does:

```go
subtaskPlans, err := parsePlan(task.Plan)
```

Update to:

```go
planResult, err := parsePlan(task.Plan)
// ... then use planResult.Subtasks where subtaskPlans was used
```

Do a careful search-and-replace across the file. The callers are in `HandlePlanApproved`, `processPlanning`, and possibly others.

### 3. Add tests

**a) `internal/model/models_test.go`** — verify the new fields:

- A Task with `Phase: "test"` and `TestsFor: ["0"]` round-trips through GORM correctly
- A Task with `TDDExceptions` JSON round-trips correctly
- `NeedsHumanReview` defaults to false

**b) `internal/orchestrator/plan_parse_test.go`** (new file) — test plan parsing:

- Plan JSON with `phase` and `tests_for` fields parses correctly
- Plan JSON with `tdd_exceptions` parses correctly
- Plan JSON without `tdd_exceptions` (old format) parses without error (backward compat)
- Plan JSON without `phase` fields (old format) parses with empty phases (backward compat)
- `tests_for: [1]` on a test subtask is preserved in the parsed result

## Scope Limitation

ONLY modify:
- `internal/model/models.go`
- `internal/orchestrator/orchestrator.go` (plan parsing section only — `planEntry`, `parsePlan`, and their callers)
- New test files

Do NOT modify: `internal/state/`, `internal/prompt/`, `internal/tui/`, `internal/orchestrator/plan_validation.go`.

## Conventions

- Go 1.22+ with standard library
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./...`
