# Agent: Supervisor Depth Roles

You are working on the `master` branch of drem-orchestrator, a Go-based multi-agent orchestrator with TDD enforcement and constitution-based quality constraints.
Your task is to add two new supervisor evaluation modes for depth enforcement: plan depth review (Role A) and depth constraint failure diagnosis (Role B).

## Context

Read these specs before starting:
- `docs/depth-enforcement-prd.md` (Supervisor roles A and B, User stories 5, 9-10, 21)
- `internal/supervisor/supervisor.go` (Supervisor struct, Evaluate, EvaluateJSON, extractJSON, truncateForPrompt)
- `internal/supervisor/types.go` (FailureDiagnosis, MergeConflictAnalysis, BuildFailureDiagnosis)
- `internal/supervisor/prompts.go` (FailureDiagnosisPrompt, MergeConflictPrompt, BuildFailurePrompt — patterns to follow)
- `internal/constraints/depth/depth.go` (DepthReport, PassThrough — created by Agent 01)
- `internal/score/score.go` (StepScore with Depth field — modified by Agent 04)

## Dependencies

This agent depends on:
- Agent 01 (depth-analysis-engine): `DepthReport` and `PassThrough` types for constraint failure diagnosis prompts
- Agent 04 (plan-depth-scoring): `StepScore.Depth` field for plan depth review prompts

If these types don't exist yet, use the type definitions from the PRD and other agent prompts as reference. The supervisor package only uses these types as prompt input (string formatting), not as imports — so no compile-time dependency is needed.

## Deliverables

### Migration (internal/supervisor/)

#### 1. types.go

Add two new evaluation result types:

```go
// PlanDepthReview is the supervisor's evaluation of a plan that failed
// the depth score. It determines whether the plan can be adjusted or
// the task concept is fundamentally flawed.
type PlanDepthReview struct {
    Assessment     string   `json:"assessment"`      // "adjustable" or "fundamentally_shallow"
    ShallowAreas   []string `json:"shallow_areas"`   // specific subtasks or modules that lack depth
    Recommendations []string `json:"recommendations"` // actionable steps to improve depth
    RejectionReason string  `json:"rejection_reason"` // human-readable explanation for the task comment
}

// DepthConstraintDiagnosis is the supervisor's evaluation of depth constraint
// failures on the integration worktree. It identifies where violations occurred
// and recommends next steps.
type DepthConstraintDiagnosis struct {
    Violations []DepthViolation `json:"violations"`
    RootCause  string           `json:"root_cause"`   // why depth constraints failed
    Recommendation string       `json:"recommendation"` // what to do next
    RejectionReason string      `json:"rejection_reason"` // for the task comment
}

// DepthViolation describes a single depth constraint violation.
type DepthViolation struct {
    Package       string `json:"package"`        // e.g., "internal/orchestrator"
    Metric        string `json:"metric"`         // "export_ratio" or "pass_through_count"
    ActualValue   string `json:"actual_value"`   // e.g., "0.25" or "7"
    Limit         string `json:"limit"`          // e.g., "0.15" or "3"
    Suggestion    string `json:"suggestion"`     // specific fix suggestion
}
```

#### 2. prompts.go

Add two new prompt-building functions following the existing patterns:

**`PlanDepthReviewPrompt`:**

```go
// PlanDepthReviewPrompt builds a prompt for reviewing a plan that failed the depth score.
func PlanDepthReviewPrompt(taskTitle, taskDesc, planJSON string, depthScore float64) string
```

The prompt should:
- Provide the task title, description, and full plan JSON
- State the depth score and that it failed the threshold
- Ask the supervisor to evaluate whether the plan can be improved for depth or whether the task concept is inherently shallow
- Request specific identification of which subtasks/modules lack depth
- Request actionable recommendations
- Instruct JSON-only output matching `PlanDepthReview` schema

**`DepthConstraintDiagnosisPrompt`:**

```go
// DepthConstraintDiagnosisPrompt builds a prompt for diagnosing depth constraint failures.
func DepthConstraintDiagnosisPrompt(taskTitle string, constraintReport string, diffOutput string) string
```

The prompt should:
- Provide the task title, the constraint report (from `FormatReport`), and the git diff
- Ask the supervisor to identify which packages violated depth constraints and why
- Request per-violation analysis with specific fix suggestions (e.g., "internalize function X", "merge packages Y and Z")
- Instruct JSON-only output matching `DepthConstraintDiagnosis` schema
- Use `truncateForPrompt` for diff output (use `truncDiffOutput` constant)

Both prompts should follow the exact pattern of existing prompts in `prompts.go`: raw `fmt.Sprintf` with the prompt template, truncation of large fields, JSON schema in the instructions.

#### 3. supervisor_test.go (or depth_supervisor_test.go)

Test the new prompt functions and types:

- **PlanDepthReviewPrompt contains required fields**: verify prompt output includes task title, description, plan JSON, depth score, and JSON schema
- **DepthConstraintDiagnosisPrompt contains required fields**: verify prompt output includes task title, constraint report, diff, and JSON schema
- **PlanDepthReview unmarshals correctly**: test JSON round-trip for `PlanDepthReview` with all fields populated
- **DepthConstraintDiagnosis unmarshals correctly**: test JSON round-trip for `DepthConstraintDiagnosis` with violations
- **Prompt truncation**: verify large diff inputs are truncated appropriately
- **Empty/edge cases**: empty plan JSON, empty diff, zero depth score

## Scope Limitation

- Do NOT modify `Supervisor.Evaluate` or `Supervisor.EvaluateJSON` — they are generic and work with any prompt
- Do NOT import `internal/constraints/depth` or `internal/score` — the supervisor package takes string/float inputs, not typed imports
- Do NOT implement automatic agent re-spawning — the supervisor is advisory only (per PRD user story 21)
- Do NOT modify existing prompt functions — only add new ones
- Do NOT modify any files outside `internal/supervisor/`

## Conventions

- Package: `package supervisor`
- Go 1.22+ with standard library where possible
- `gofmt` for formatting
- Exported functions and types have doc comments
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Table-driven tests with `t.Run()` sub-tests
- Build verification: `go build ./internal/supervisor/ && go test ./internal/supervisor/ -v`
