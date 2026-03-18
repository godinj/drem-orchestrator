# Agent: Plan Depth Scoring

You are working on the `master` branch of drem-orchestrator, a Go-based multi-agent orchestrator with TDD enforcement and constitution-based quality constraints.
Your task is to add a depth scoring dimension to the plan scoring system, so that plans are evaluated on whether they define deep module boundaries and interface shapes.

## Context

Read these specs before starting:
- `docs/depth-enforcement-prd.md` (User stories 3, 4, 18-20, Plan depth scoring)
- `internal/score/score.go` (StepScore, PlanEntry, ScorePlan, ScoreImplementation, FormatScores, ScoresToMap)
- `internal/score/score_test.go` (existing test patterns, approxEqual)
- `internal/model/models.go` (SubtaskPlan with ModuleBoundary and InterfaceShape — added by Agent 02)

## Dependencies

This agent depends on Agent 02 (model-depth-metadata). If `ModuleBoundary` and `InterfaceShape` types don't exist in `internal/model/` yet, use these definitions in your scoring logic:

```go
// Depth metadata fields on SubtaskPlan (added by Agent 02)
// ModuleBoundaries []ModuleBoundary `json:"module_boundaries,omitempty"`
// InterfaceShapes  []InterfaceShape `json:"interface_shapes,omitempty"`

type ModuleBoundary struct {
    Package     string `json:"package"`
    Description string `json:"description"`
    Exports     int    `json:"exports"`
}

type InterfaceShape struct {
    Package   string `json:"package"`
    Functions []string `json:"functions"`
    Types     []string `json:"types"`
}
```

## Deliverables

### Migration (internal/score/)

#### 1. score.go

Add a `Depth` dimension to `StepScore` and extend scoring logic:

**Modify `StepScore`:**

```go
type StepScore struct {
    TDD           float64
    Constitution  float64
    Documentation float64
    Depth         float64 // NEW: 0.0–1.0
}
```

**Add depth metadata to `PlanEntry`** (mirroring model types for decoupling):

```go
// DepthMeta carries module boundary and interface shape info for depth scoring.
type DepthMeta struct {
    ModuleBoundaries []ModuleBoundary
    InterfaceShapes  []InterfaceShape
}

type ModuleBoundary struct {
    Package     string
    Description string
    Exports     int
}

type InterfaceShape struct {
    Package   string
    Functions []string
    Types     []string
}
```

**Add `DepthMeta` to `PlanEntry`:**

```go
type PlanEntry struct {
    Title          string
    AgentType      string
    Phase          string
    EstimatedFiles []string
    TestsFor       []int
    Dependencies   []int
    DepthMeta      *DepthMeta // NEW: nil for plans without depth metadata
}
```

**Add `scorePlanDepth(entries []PlanEntry) float64`:**

Score plan depth on three sub-criteria (equal weight):

1. **Module boundaries defined** (0.0 or 1.0): At least one subtask has `ModuleBoundaries` with non-empty entries. Each boundary must have a non-empty `Package` and `Description`.
2. **Interface shapes specified** (0.0 or 1.0): At least one subtask has `InterfaceShapes` with non-empty entries. Each shape must have a non-empty `Package` and at least one function or type.
3. **Deep decomposition** (0.0 or 1.0): For subtasks that define module boundaries, check that `Exports` count is specified and ≤ 20 (a deep module has few exports). Score 1.0 if all boundary-defining subtasks meet this, 0.0 if any don't.

Final depth score = average of the three sub-criteria.

**Modify `ScorePlan`** to include depth:

```go
func ScorePlan(input PlanScoreInput) StepScore {
    return StepScore{
        TDD:           scorePlanTDD(input.Entries, input.TDDExceptions),
        Constitution:  scorePlanConstitution(input.ValidationResult),
        Documentation: scorePlanDocumentation(input.Entries),
        Depth:         scorePlanDepth(input.Entries), // NEW
    }
}
```

**Modify `FormatScores`** to include depth:

```go
func FormatScores(s StepScore) string {
    return fmt.Sprintf("TDD: %d%% | Constitution: %d%% | Docs: %d%% | Depth: %d%%",
        int(s.TDD*100+0.5),
        int(s.Constitution*100+0.5),
        int(s.Documentation*100+0.5),
        int(s.Depth*100+0.5),
    )
}
```

**Modify `ScoresToMap`** to include depth:

```go
func ScoresToMap(s StepScore) map[string]any {
    return map[string]any{
        "tdd":           s.TDD,
        "constitution":  s.Constitution,
        "documentation": s.Documentation,
        "depth":         s.Depth,
        "formatted":     FormatScores(s),
    }
}
```

**Implementation scoring:** `ScoreImplementation` does NOT need a depth dimension — depth is enforced at plan time and via constraints at integration time. Set `Depth: 0.0` in `ScoreImplementation` (it's not evaluated at that gate).

#### 2. score_test.go

Add tests for depth scoring. Follow the existing test patterns with `approxEqual`:

- **Plan with full depth metadata**: subtasks have module boundaries (Package, Description, Exports ≤ 20) and interface shapes (Package, Functions, Types) → depth score 1.0
- **Plan with no depth metadata**: no subtask has `DepthMeta` → depth score 0.0
- **Plan with boundaries but no interface shapes**: only module boundaries defined → depth score ~0.33 (1 of 3 criteria)
- **Plan with interface shapes but no boundaries**: only interface shapes → depth score ~0.33
- **Plan with boundaries but excessive exports**: `Exports > 20` on a boundary → deep decomposition fails → depth score ~0.67
- **Plan with mixed subtasks**: some subtasks have depth metadata, some don't → score based on whether ANY subtask provides it
- **Existing tests still pass**: verify TDD, Constitution, Documentation scoring is unchanged
- Test `FormatScores` includes depth percentage
- Test `ScoresToMap` includes depth key

## Scope Limitation

- Do NOT import `internal/model` — duplicate the depth metadata types in the score package to maintain the existing decoupling pattern (score package uses its own `PlanEntry` type, not `model.SubtaskPlan`)
- Do NOT add a depth threshold or pass/fail gate — the orchestrator decides what score triggers supervisor review
- Do NOT modify `ScoreImplementation` beyond adding `Depth: 0.0`
- Do NOT modify any files outside `internal/score/`

## Conventions

- Package: `package score`
- Go 1.22+ with standard library where possible
- `gofmt` for formatting
- Exported types have doc comments
- `approxEqual(a, b float64) bool` with tolerance 0.001 for float comparisons in tests
- Table-driven tests with `t.Run()` sub-tests
- Build verification: `go build ./internal/score/ && go test ./internal/score/ -v`
