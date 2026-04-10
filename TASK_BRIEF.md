# Task: Auto-merge Default Variant & Challenger Promotion (5c)

## Goal
Hook experiment-aware logic into the EXISTING task completion flow. Do NOT rewrite existing functions.

## CRITICAL RULES
- **DO NOT rewrite `onAgentCompleted` or `onAgentFailed`.** Hook INTO them, don't replace them.
- **DO NOT shadow package names with variables.** Use `exp` not `experiment` for variables.
- **DO NOT call methods that don't exist.** Check with grep before calling any method.
- Import `"github.com/godinj/drem-orchestrator/internal/experiment"` if you use it.

## What to build

### 1. Add experiment check in onAgentCompleted (agent_results.go:59)
After the existing merge logic succeeds (around line 163, after `handleAgentMergeFailure`), add:
```go
// Check if this task belongs to an experiment variant
if task.ParentTaskID == nil {
    exp, expErr := experiment.FindVariantByTaskID(o.db, task.ID)
    if expErr == nil && exp != nil {
        if err := o.handleExperimentVariantCompleted(task, exp); err != nil {
            o.logger.Warn("experiment variant completion handling failed", "error", err)
        }
    }
}
```

### 2. Add experiment check in onAgentFailed (agent_failure.go)
At the END of the existing function (before final return), add similar logic:
```go
exp, expErr := experiment.FindVariantByTaskID(o.db, task.ID)
if expErr == nil && exp != nil {
    if err := o.handleExperimentVariantFailed(task, exp); err != nil {
        o.logger.Warn("experiment variant failure handling failed", "error", err)
    }
}
```

### 3. Create handleExperimentVariantCompleted and handleExperimentVariantFailed
New file: `internal/orchestrator/experiment_hooks.go` (~80-120 lines)
- `handleExperimentVariantCompleted`: If variant.IsDefault, trigger merge. Check if all variants done → transition experiment to "review".
- `handleExperimentVariantFailed`: If default failed, check if any challenger passed → auto-promote winner. Check all done → transition to "review".

### 4. Write tests
New file: `internal/orchestrator/experiment_hooks_test.go`
Test: default passes (auto-merge), default fails + challenger passes (promote), both fail (review), all done (experiment completes).

## Key files (DO NOT read others unless absolutely needed)
- `internal/orchestrator/agent_results.go:59` — onAgentCompleted
- `internal/orchestrator/agent_failure.go` — onAgentFailed (DO NOT REWRITE)
- `internal/experiment/experiment.go` — experiment model and methods
- `internal/model/models.go:64` — Task and Agent models

## BEFORE YOU EXIT
**ALWAYS commit your work before the session ends.** Run:
```bash
git add -A && git commit -m "feat: auto-merge default variant and challenger promotion (5c)"
```
Even if tests fail, commit what you have with a WIP note. Uncommitted work WILL BE LOST.

## Verification
Run `go vet ./... && go test ./internal/orchestrator/...` in ONE command. Max 2 fix cycles.
