# Agent: Post-Agent Constraint Gate

You are working on the `master` branch of Drem Orchestrator, a Go-based agent orchestration system.
Your task is to add a constraint evaluation step after agent completion — before the agent's branch is merged into the integration branch. If constraints are violated, the subtask fails with feedback.

## Context

Read these before starting:
- `docs/constraints-system/design.md` (section 4.5 — Post-Agent Constraint Gate)
- `internal/orchestrator/agent_results.go` (the full file — understand `onAgentCompleted` at line 47, especially the merge flow at lines 77-142 and the post-merge success path at lines 144-199)
- `internal/constraints/config.go` (`LoadConfig`, `Config` types)
- `internal/constraints/evaluate.go` (`EvaluateFiles` function — evaluates only file-based constraints, no commands)
- `internal/constraints/report.go` (`Report`, `Result`, `FormatReport`)

## Dependencies

This agent depends on Agent 01 (constraints-package). The `internal/constraints/` package must exist with `LoadConfig`, `EvaluateFiles`, `Report`, `Result`, and `FormatReport`.

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
func EvaluateFiles(cfg *Config, worktreeRoot string, files []string) (*Report, error) {
    return &Report{}, nil
}

// internal/constraints/report.go
func FormatReport(report *Report) string { return "" }
```

## Deliverables

### Modified file: `internal/orchestrator/agent_results.go`

#### 1. Add constraint check after successful merge

In `onAgentCompleted()`, after the merge succeeds (line ~113, the `merged = true` line) but **before** the agent worktree is cleaned up (line ~145), add a constraint evaluation step.

The insertion point is between the end of the merge block and the worktree cleanup. The logic:

```go
// After merge succeeded, check constraints on the feature worktree.
// Use file-based constraints only (no commands) for speed.
if merged && featureBranch != "" {
    fn := strings.TrimPrefix(featureBranch, "feature/")
    featureDir := o.worktree.FeatureWorktreePath(fn)

    constraintCfg, cfgErr := constraints.LoadConfig(featureDir)
    if cfgErr != nil {
        o.logger.Warn("constraint config load failed after merge",
            "agent_id", ag.ID, "error", cfgErr)
    } else if constraintCfg != nil {
        // Get the files this agent changed.
        changedFiles, chErr := worktree.GetChangedFiles(featureDir, o.worktree.DefaultBranch)
        if chErr != nil {
            o.logger.Warn("failed to get changed files for constraint check",
                "agent_id", ag.ID, "error", chErr)
        } else if len(changedFiles) > 0 {
            report, evalErr := constraints.EvaluateFiles(constraintCfg, featureDir, changedFiles)
            if evalErr != nil {
                o.logger.Warn("constraint evaluation failed",
                    "agent_id", ag.ID, "error", evalErr)
            } else if report.Failed > 0 {
                // Constraint violation — fail the subtask with feedback.
                o.logger.Warn("constraint violations after agent merge",
                    "agent_id", ag.ID, "task_id", task.ID,
                    "failed", report.Failed)

                // Store violations in task context for visibility.
                if task.Context == nil {
                    task.Context = make(model.JSONField)
                }
                task.Context["constraint_violations"] = constraints.FormatReport(report)

                // Fail the subtask so it can be retried with constraint feedback.
                ag.Status = model.AgentIdle
                ag.CurrentTaskID = nil
                if err := o.db.Save(ag).Error; err != nil {
                    return fmt.Errorf("on agent completed: save agent after constraint fail: %w", err)
                }
                evt, err := state.TransitionTask(task, model.StatusFailed, "orchestrator",
                    map[string]any{
                        "reason": "constraint violations after merge",
                        "violations": constraints.FormatReport(report),
                    })
                if err != nil {
                    o.logger.Warn("failed to transition task after constraint violation",
                        "task_id", task.ID, "error", err)
                    return nil
                }
                if err := o.db.Save(task).Error; err != nil {
                    return fmt.Errorf("on agent completed: save task after constraint fail: %w", err)
                }
                if err := o.db.Create(evt).Error; err != nil {
                    return fmt.Errorf("on agent completed: save constraint-fail event: %w", err)
                }
                return nil
            }
        }
    }
}
```

**Important placement notes**:
- This goes AFTER `merged = true` is confirmed but BEFORE the worktree cleanup at line ~145.
- If constraints fail, the function returns early — it does NOT proceed to clean up the worktree or fast-track the subtask to DONE.
- The agent worktree is preserved on constraint failure (same pattern as merge failure) so the work isn't lost.
- The constraint report is stored in both `task.Context` (for TUI display) and the event details (for history).

#### 2. Add import for constraints package

Add `"github.com/godinj/drem-orchestrator/internal/constraints"` to the import block.

### New file: `internal/orchestrator/post_agent_constraint_test.go`

Tests for the post-agent constraint gate:

1. **No constraints config — agent completes normally**: Set up an orchestrator with a worktree that has no `.drem/constraints.toml`. Run a coder agent to completion (simulate with `processAgentResult`). Verify the subtask transitions to DONE normally.

2. **Constraints pass — agent completes normally**: Set up a worktree with a `.drem/constraints.toml` that has a `max_lines` constraint with limit=1000. The agent's changed files are all under the limit. Verify subtask transitions to DONE.

3. **Constraints fail — subtask fails with feedback**: Set up constraints that will fail (e.g., `max_lines` limit=10 on a file the agent modified that has 50 lines). Verify:
   - Subtask status is `failed`.
   - `task.Context["constraint_violations"]` is set.
   - A task event with `reason: "constraint violations after merge"` exists.
   - Agent status is `idle`.

Since these tests require simulating the full agent completion flow (including merge), use the existing test patterns from `agent_result_test.go`. Use `testutil` helpers for DB and git setup. You'll need:
- A bare repo with a feature branch and an agent branch.
- The agent branch should have a committed file that either passes or fails constraints.
- A `.drem/constraints.toml` in the repo.

If the full integration test is too complex, test at a lower level by:
1. Setting up the database state (agent, task, worktree records).
2. Creating real git worktrees with test files.
3. Calling `processAgentResult` directly.

## Scope Limitation

- Only modify `internal/orchestrator/agent_results.go`.
- Only create `internal/orchestrator/post_agent_constraint_test.go`.
- Do NOT modify `internal/constraints/`, `internal/prompt/`, or other packages.
- Do NOT modify the merge logic itself — only add a check after a successful merge.
- Do NOT modify `onPlannerCompleted` — that's Agent 03's scope.

## Conventions

- Go 1.22+ with standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./internal/orchestrator/...`
