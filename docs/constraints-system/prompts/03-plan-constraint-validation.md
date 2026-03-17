# Agent: Plan Constraint Validation

You are working on the `master` branch of Drem Orchestrator, a Go-based agent orchestration system.
Your task is to add constraint-aware validation to the plan validation pipeline, so that plans targeting grandfathered or constrained files produce warnings before reaching `plan_review`.

## Context

Read these before starting:
- `docs/constraints-system/design.md` (section 4.4 — Plan Validation Integration)
- `internal/orchestrator/plan_validation.go` (the full file — understand `ValidatePlan`, `PlanValidationResult`, `planEntry`, `allFiles()`)
- `internal/orchestrator/agent_results.go` (lines 202-305 — `onPlannerCompleted`, specifically the validation block at lines 254-280 where `ValidatePlan` is called)
- `internal/constraints/config.go` (the `Config`, `MaxLinesConstraint`, `MaxMatchesConstraint`, `LinesException`, `MatchesException` types)
- `internal/constraints/evaluate.go` (the `EvaluateFiles` function signature)

## Dependencies

This agent depends on Agent 01 (constraints-package). The `internal/constraints/` package must exist with `LoadConfig`, `Config`, `EvaluateFiles`, and all constraint/exception types.

If `internal/constraints/` doesn't exist yet, create a minimal stub in `internal/constraints/config.go`:

```go
package constraints

type Config struct {
    ContextFiles []string             `toml:"context_files"`
    MaxLines     []MaxLinesConstraint  `toml:"max_lines"`
    MaxMatches   []MaxMatchesConstraint `toml:"max_matches"`
}
type MaxLinesConstraint struct {
    Name       string           `toml:"name"`
    Glob       string           `toml:"glob"`
    Exclude    []string         `toml:"exclude"`
    Limit      int              `toml:"limit"`
    Exceptions []LinesException `toml:"exception"`
}
type LinesException struct {
    Path          string `toml:"path"`
    Rule          string `toml:"rule"`
    BaselineLines int    `toml:"baseline_lines"`
}
type MaxMatchesConstraint struct {
    Name       string             `toml:"name"`
    Glob       string             `toml:"glob"`
    Exclude    []string           `toml:"exclude"`
    Pattern    string             `toml:"pattern"`
    Limit      int                `toml:"limit"`
    Scope      string             `toml:"scope"`
    Exceptions []MatchesException `toml:"exception"`
}
type MatchesException struct {
    Path          string `toml:"path"`
    Rule          string `toml:"rule"`
    BaselineCount int    `toml:"baseline_count"`
}
func LoadConfig(worktreeRoot string) (*Config, error) { return nil, nil }
```

## Deliverables

### Modified file: `internal/orchestrator/plan_validation.go`

#### 1. Add `ValidatePlanConstraints` function

```go
// ValidatePlanConstraints checks a plan's estimated_files against the
// constraint config. Returns warnings for files that target constrained
// or grandfathered paths. Returns an empty result if cfg is nil.
func ValidatePlanConstraints(subtasks []planEntry, cfg *constraints.Config) PlanValidationResult
```

Implementation:

1. If `cfg` is nil, return an empty (valid) `PlanValidationResult`.
2. Collect all file paths from each subtask using `allFiles()` (already exists — returns `files` or `estimated_files`).
3. **Check max_lines exceptions**: For each `MaxLinesConstraint` in `cfg.MaxLines`, iterate over its `Exceptions`. For each exception with `Rule == "shrink-only"`, check if any subtask's files match the exception `Path` (use `filepath.Match` or string prefix matching for directory paths). If a match is found, add a warning:
   ```
   Subtask N ("<title>") targets grandfathered file <path> (shrink-only, baseline: <baseline_lines> lines) — changes must not increase line count
   ```
4. **Check max_matches exceptions**: Same logic for `MaxMatchesConstraint` exceptions. Warning message:
   ```
   Subtask N ("<title>") targets grandfathered file <path> (shrink-only, baseline: <baseline_count> matches for "<constraint_name>") — changes must not increase count
   ```
5. **Check files near limit**: For each `MaxLinesConstraint` without a matching exception, check if any targeted file exists on disk (in the worktree) and is already >90% of the limit. This requires the worktree path, so add it as a parameter:
   ```go
   func ValidatePlanConstraints(subtasks []planEntry, cfg *constraints.Config, worktreeRoot string) PlanValidationResult
   ```
   If `worktreeRoot` is empty, skip the near-limit check. If the file exists and `lineCount > limit * 9 / 10`, warn:
   ```
   Subtask N ("<title>") targets file <path> which is already at <count>/<limit> lines (90%+ of ceiling)
   ```

6. These are all **warnings**, not errors — the plan should still be valid. The human reviewer at `plan_review` decides whether to approve.

#### 2. Add import for constraints package

Add `"github.com/godinj/drem-orchestrator/internal/constraints"` to the import block. Also add `"os"` and `"bytes"` if needed for line counting.

### Modified file: `internal/orchestrator/agent_results.go`

#### 3. Wire `ValidatePlanConstraints` into `onPlannerCompleted`

In `onPlannerCompleted()`, after the existing `ValidatePlan` call (around line 259), add:

```go
// Check plan against project constraints (grandfathered files, ceilings).
constraintCfg, cfgErr := constraints.LoadConfig(ag.WorktreePath)
if cfgErr != nil {
    o.logger.Warn("constraint config load failed", "task_id", task.ID, "error", cfgErr)
} else if constraintCfg != nil {
    // Use feature worktree for near-limit checks (integration branch has latest state).
    featureDir := ""
    if task.WorktreeBranch != "" {
        fn := strings.TrimPrefix(task.WorktreeBranch, "feature/")
        featureDir = o.worktree.FeatureWorktreePath(fn)
    }
    constraintResult := ValidatePlanConstraints(planResult.Subtasks, constraintCfg, featureDir)
    // Merge constraint warnings into the existing validation result.
    validation.Warnings = append(validation.Warnings, constraintResult.Warnings...)
    validation.Errors = append(validation.Errors, constraintResult.Errors...)
    if len(constraintResult.Errors) > 0 {
        validation.Valid = false
    }
}
```

This must go **after** the `ValidatePlan` call but **before** the `!validation.Valid` check that decides whether to retry or proceed.

Also add `"github.com/godinj/drem-orchestrator/internal/constraints"` to the import block.

### New file: `internal/orchestrator/plan_constraint_test.go`

Tests for `ValidatePlanConstraints`:

1. **Nil config returns empty result**: Pass `nil` config, verify result is valid with no warnings.

2. **Subtask targets shrink-only file (max_lines)**: Config has a `max_lines` constraint with an exception for `"internal/orchestrator/orchestrator.go"` with `rule = "shrink-only"`. Subtask has `estimated_files = ["internal/orchestrator/orchestrator.go"]`. Verify a warning is produced mentioning "shrink-only" and the baseline.

3. **Subtask targets shrink-only file (max_matches)**: Same pattern but for a `max_matches` exception. Verify warning mentions the constraint name and baseline count.

4. **Subtask targets unconstrained file**: Config has constraints but subtask files don't match any exception. Verify no warnings.

5. **Multiple subtasks, mixed hits**: Two subtasks, one targets a grandfathered file, one doesn't. Verify exactly one warning, mentioning the correct subtask index and title.

6. **Near-limit warning**: Create a temp dir with a file at 750 lines. Config has `max_lines` with `limit = 800`. Subtask targets that file. Pass the temp dir as `worktreeRoot`. Verify a warning about >90% of ceiling.

7. **File well under limit**: Same setup but file has 100 lines. Verify no near-limit warning.

Use the existing `planEntry` type (it's in the same package, so accessible in tests). Follow the table-driven test pattern used in `plan_validation_test.go`.

## Scope Limitation

- Only modify `internal/orchestrator/plan_validation.go` and `internal/orchestrator/agent_results.go`.
- Only create `internal/orchestrator/plan_constraint_test.go`.
- Do NOT modify `internal/constraints/`, `internal/prompt/`, or any other package.
- Do NOT change the existing `ValidatePlan` function signature or behavior.

## Conventions

- Go 1.22+ with standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./internal/orchestrator/...`
