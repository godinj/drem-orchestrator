# Agent: Integration Gate Constraint Check

You are working on the `master` branch of Drem Orchestrator, a Go-based agent orchestration system.
Your task is to add a full constraint evaluation at the integration gate — before a parent task transitions from `in_progress` to `testing_ready`. If constraints are violated, the transition is blocked.

## Context

Read these before starting:
- `docs/constraints-system/design.md` (section 4.6 — Integration Gate)
- `internal/orchestrator/task_processing.go` (lines 386-460 — `checkFeatureCompletion` function, especially the `allDone` path at lines 417-457 where the transition to `testing_ready` happens)
- `internal/constraints/config.go` (`LoadConfig`, `Config` types)
- `internal/constraints/evaluate.go` (`Evaluate` function — runs ALL constraints including commands)
- `internal/constraints/report.go` (`Report`, `Result`, `FormatReport`)
- `internal/worktree/manager.go` (the `FeatureWorktreePath` method used to resolve worktree paths)

## Dependencies

This agent depends on Agent 01 (constraints-package). The `internal/constraints/` package must exist with `LoadConfig`, `Evaluate`, `Report`, `Result`, and `FormatReport`.

If `internal/constraints/` doesn't exist yet, create minimal stubs:

```go
// internal/constraints/config.go
package constraints

type Config struct {
    ContextFiles []string `toml:"context_files"`
}
func LoadConfig(worktreeRoot string) (*Config, error) { return nil, nil }

// internal/constraints/evaluate.go
type Report struct {
    Results []Result
    Passed  int
    Failed  int
}
type Result struct {
    Name     string
    Type     string
    Passed   bool
    Messages []string
}
func Evaluate(cfg *Config, worktreeRoot string) (*Report, error) {
    return &Report{}, nil
}

// internal/constraints/report.go
func FormatReport(report *Report) string { return "" }
```

## Deliverables

### Modified file: `internal/orchestrator/task_processing.go`

#### 1. Add constraint check before `testing_ready` transition

In `checkFeatureCompletion()`, after the `allDone` check confirms all subtasks are done (line ~417) and after the empty-feature-branch check (lines 421-437), but **before** the `TransitionTask` to `StatusTestingReady` (line ~439), add constraint evaluation:

```go
// Run full constraint evaluation on the integration worktree before
// allowing transition to testing_ready.
if parent.WorktreeBranch != "" {
    fn := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
    featureDir := o.worktree.FeatureWorktreePath(fn)

    constraintCfg, cfgErr := constraints.LoadConfig(featureDir)
    if cfgErr != nil {
        o.logger.Warn("constraint config load failed at integration gate",
            "task_id", parent.ID, "error", cfgErr)
    } else if constraintCfg != nil {
        report, evalErr := constraints.Evaluate(constraintCfg, featureDir)
        if evalErr != nil {
            o.logger.Warn("constraint evaluation failed at integration gate",
                "task_id", parent.ID, "error", evalErr)
        } else if report.Failed > 0 {
            o.logger.Warn("constraint violations at integration gate, blocking testing_ready",
                "task_id", parent.ID, "failed", report.Failed)

            // Store violations in task context for TUI visibility.
            if parent.Context == nil {
                parent.Context = make(model.JSONField)
            }
            parent.Context["constraint_violations"] = constraints.FormatReport(report)
            if err := o.db.Save(parent).Error; err != nil {
                return fmt.Errorf("check feature completion: save constraint violations: %w", err)
            }

            // Do NOT transition to testing_ready. The parent stays in_progress.
            // The violations are visible in the TUI for the user to address.
            o.emit("constraint_violations", map[string]any{
                "task_id":    parent.ID,
                "failed":     report.Failed,
                "violations": constraints.FormatReport(report),
            })
            return nil
        }

        // Constraints passed — clear any previous violation context.
        if parent.Context != nil {
            delete(parent.Context, "constraint_violations")
        }
    }
}
```

**Important placement notes**:
- This goes AFTER the empty-feature check (which may `failTask` and return) but BEFORE the `TransitionTask` call.
- On constraint failure, the function returns `nil` (no error) — the parent simply stays in `in_progress`. This is intentional: the constraint violations are informational, not a fatal error. The user can see them in the TUI and decide how to proceed.
- Clear `constraint_violations` from context when constraints pass, so stale violation data doesn't persist.

#### 2. Add import for constraints package

Add `"github.com/godinj/drem-orchestrator/internal/constraints"` to the import block.

### New file: `internal/orchestrator/integration_gate_test.go`

Tests for the integration gate constraint check:

1. **No constraints config — transitions normally**: Parent task with all subtasks done. No `.drem/constraints.toml` in the worktree. Verify parent transitions to `testing_ready`.

2. **Constraints pass — transitions normally**: Set up a `.drem/constraints.toml` with a `max_lines` constraint (limit=1000). All files are under the limit. Verify parent transitions to `testing_ready`.

3. **Constraints fail — stays in_progress**: Set up constraints that will fail (e.g., `no_match` pattern that matches a file in the worktree). Verify:
   - Parent status remains `in_progress`.
   - `parent.Context["constraint_violations"]` is set and contains the violation details.
   - No `testing_ready` event is created.

4. **Constraints pass after previous failure — violations cleared**: Set up a parent with `Context["constraint_violations"]` already set from a prior failure. Run `checkFeatureCompletion` with constraints that now pass. Verify `constraint_violations` is removed from context.

5. **Command constraint failure blocks transition**: Set up a `command` constraint that will fail (e.g., `run = "false"`). Verify parent stays in `in_progress`.

These tests require:
- A real git repo with a feature branch (use `testutil.SetupBareRepo` and `testutil.AddWorktree`).
- A `.drem/constraints.toml` committed to the repo or placed in the worktree.
- Subtasks in DONE status.
- The parent task in `in_progress` with a `worktree_branch` set.

Use the existing test patterns from `scheduling_test.go` (which already tests `checkFeatureCompletion`). The key fixtures: `testutil.NewTestDB`, `testutil.SetupBareRepo`, create Project + parent Task + subtask Tasks in DB, then call `o.checkFeatureCompletion(&parent)`.

For the git setup, commit a `.drem/constraints.toml` file and some test source files to the feature branch before running the test.

## Scope Limitation

- Only modify `internal/orchestrator/task_processing.go`.
- Only create `internal/orchestrator/integration_gate_test.go`.
- Do NOT modify `internal/constraints/`, `internal/prompt/`, or other packages.
- Do NOT modify `agent_results.go` — that's Agents 03 and 04's scope.
- Do NOT change `checkFeatureCompletion`'s signature or the existing empty-feature check.

## Conventions

- Go 1.22+ with standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./internal/orchestrator/...`
