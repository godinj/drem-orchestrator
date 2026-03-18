# Agent: Model Depth Metadata

You are working on the `master` branch of drem-orchestrator, a Go-based multi-agent orchestrator with TDD enforcement and constitution-based quality constraints.
Your task is to extend the plan and subtask model types with module boundary and interface shape metadata, so that depth expectations persist from planning through implementation.

## Context

Read these specs before starting:
- `docs/depth-enforcement-prd.md` (User story 22, Implementation Decisions — `internal/model/`)
- `internal/model/models.go` (current Task and SubtaskPlan types)
- `internal/model/json.go` (JSONField and JSONArray custom types)
- `internal/model/models_test.go` (existing test patterns)

## Deliverables

### Migration (internal/model/)

#### 1. models.go

Extend `SubtaskPlan` with depth metadata fields. These fields are optional — plans produced before this change will unmarshal with zero values.

Add to `SubtaskPlan`:

```go
type SubtaskPlan struct {
    Title          string   `json:"title"`
    Description    string   `json:"description"`
    AgentType      string   `json:"agent_type"`
    EstimatedFiles []string `json:"estimated_files"`
    Phase          string   `json:"phase,omitempty"`
    TestsFor       []int    `json:"tests_for,omitempty"`

    // Depth metadata (populated by planner when designing for depth)
    ModuleBoundaries []ModuleBoundary `json:"module_boundaries,omitempty"`
    InterfaceShapes  []InterfaceShape `json:"interface_shapes,omitempty"`
}
```

#### 2. models.go (new types)

Add two new types for depth metadata:

```go
// ModuleBoundary describes a module boundary defined in a plan subtask.
// It captures the planner's intent about what a module encapsulates and
// where its boundary lies.
type ModuleBoundary struct {
    Package     string `json:"package"`      // e.g., "internal/constraints/depth"
    Description string `json:"description"`  // what this module encapsulates
    Exports     int    `json:"exports"`      // expected number of exported symbols
}

// InterfaceShape describes the intended public interface of a module.
// It captures the planner's commitment to a specific API surface.
type InterfaceShape struct {
    Package   string   `json:"package"`   // e.g., "internal/constraints/depth"
    Functions []string `json:"functions"` // expected exported function signatures
    Types     []string `json:"types"`     // expected exported type names
}
```

#### 3. models_test.go

Add tests to verify:

- `SubtaskPlan` with depth metadata marshals/unmarshals correctly via JSON
- `SubtaskPlan` without depth metadata (legacy plans) unmarshals with nil slices — backward compatible
- `ModuleBoundary` and `InterfaceShape` round-trip through JSON correctly
- A plan stored in `Task.Plan` (as `JSONField`) preserves depth metadata through GORM serialization

Use table-driven tests following the existing patterns in `models_test.go`.

## Scope Limitation

- Do NOT modify `Task` struct fields — depth metadata lives on `SubtaskPlan`, not `Task`
- Do NOT modify `enums.go` or `json.go`
- Do NOT add database migrations — `SubtaskPlan` is stored as JSON inside `Task.Plan`, so no schema change is needed
- Do NOT change any existing JSON tags — only add new fields with `omitempty`

## Conventions

- Package: `package model`
- Go 1.22+ with standard library where possible
- `gofmt` for formatting
- Exported types have doc comments
- Table-driven tests with `t.Run()` sub-tests
- Build verification: `go build ./internal/model/ && go test ./internal/model/ -v`
