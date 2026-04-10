<<<<<<< HEAD
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
=======
# Task: TUI Experiment Summary View (7a)

## Goal
Add a read-only experiment summary view to the TUI following existing Bubble Tea patterns.

## CRITICAL RULES
- **NEVER do DB queries in View().** View() must be a pure render function. Load data via tea.Cmd in Init/Update.
- **Use pointer receivers for SetSize** — `func (v *ExperimentView) SetSize(...)` not value receiver.
- **Follow existing sub-model patterns.** Look at how BoardModel or AgentsModel work — they don't implement full tea.Model interface. Match that pattern exactly.
- **Add a key binding** to navigate to and from the experiments view (e.g., "x" for experiments).
- **Check go.mod** — if you need `charmbracelet/bubbles/table`, run `go get` first.

## What to build

### 1. Experiment view component
**New file:** `internal/tui/experiment_view.go`

Follow the EXISTING pattern in `internal/tui/` for sub-models:
- Struct with width, height, db, and cached experiment data
- `Init() tea.Cmd` — returns a Cmd that loads experiments from DB
- `Update(msg tea.Msg) tea.Cmd` — handles the load result message, stores in struct
- `View() string` — pure render from cached data, NO DB calls
- `SetSize(w, h int)` — POINTER receiver

Display format:
```
ID (short)  | Status   | Created    | Variants | Winner
a1b2c3d4    | running  | 2026-04-09 | 3/3 done | default
```

### 2. Wire into TUI navigation
- Add `FocusExperiments` constant to the focus enum
- Add key binding (e.g., "x") in `keyhandlers.go` to switch to experiments
- Add Escape handling to return from experiments to board
- Add `renderExperimentsScreen()` in the main render switch

### 3. Key files to read (check repo-map.md FIRST)
- `internal/tui/app.go` — main Model struct, View() switch, focus constants
- `internal/tui/keyhandlers.go` — key handling, navigation patterns
- ONE existing sub-model (e.g., agents or board) — for the pattern to follow
- `internal/experiment/experiment.go` — experiment data model
>>>>>>> 2476ed2 (feat: worker-023 qwen3-coder round 2 output)

## BEFORE YOU EXIT
**ALWAYS commit your work before the session ends.** Run:
```bash
<<<<<<< HEAD
git add -A && git commit -m "feat: auto-merge default variant and challenger promotion (5c)"
=======
git add -A && git commit -m "feat: add TUI experiment summary view (7a)"
>>>>>>> 2476ed2 (feat: worker-023 qwen3-coder round 2 output)
```
Even if tests fail, commit what you have with a WIP note. Uncommitted work WILL BE LOST.

## Verification
<<<<<<< HEAD
Run `go vet ./... && go test ./internal/orchestrator/...` in ONE command. Max 2 fix cycles.
=======
Run `go vet ./... && go test ./internal/tui/...` in ONE command. Max 2 fix cycles.
>>>>>>> 2476ed2 (feat: worker-023 qwen3-coder round 2 output)
