# Agent: Lifecycle Routing — Quick Fix Task Processing in Orchestrator

You are working on the `master` branch of Drem Orchestrator, a terminal-based task orchestrator that coordinates multiple Claude Code agents to work on software projects in parallel.
Your task is lifecycle routing: modify the orchestrator tick loop so that quickfix tasks follow the lightweight lifecycle (BACKLOG → IN_PROGRESS → MERGING → DONE), skipping planning and TDD gates.

## Context

Read these specs before starting:
- `docs/quickfix-tasks/prd-quickfix-tasks.md` (sections 4.2, 4.6, 4.7 — Quick Fix Lifecycle, Agent Assignment, Scheduling)
- `internal/orchestrator/task_processing.go` (`processBacklog`, `scheduleSubtasks`, `executeMerge`, `checkFeatureCompletion`)
- `internal/orchestrator/orchestrator.go` (`doTick` function — the main tick loop, lines 220–364)
- `internal/model/enums.go` (the `TaskCategory` type — `CategoryStandard`, `CategoryQuickFix`, `IsQuickFix()`)
- `internal/state/machine.go` (`ValidTransitions` — `backlog → in_progress` is now valid)
- `internal/prompt/prompt.go` (how prompts are generated for agents — `prompt.Generate` with `prompt.Opts`)
- `internal/orchestrator/lifecycle_test.go` (existing lifecycle test patterns)
- `internal/orchestrator/bugreport_test.go` (test setup patterns — `setupOrchestratorWithBugReports`)

## Dependencies

This agent depends on Agent 01 (Task Model). If `model.TaskCategory`, `model.CategoryQuickFix`, or `TaskCategory.IsQuickFix()` don't exist yet, create stubs in `internal/model/enums.go` matching these signatures:

```go
type TaskCategory string
const CategoryStandard TaskCategory = "standard"
const CategoryQuickFix TaskCategory = "quickfix"
func (c TaskCategory) IsQuickFix() bool { return c == CategoryQuickFix }
```

## Deliverables

### Modified files

#### 1. `internal/orchestrator/task_processing.go`

**Modify `processBacklog`** to handle quickfix tasks differently. Currently it always transitions to `StatusPlanning`. For quickfix tasks, it should:

- Skip planning entirely
- Create a feature worktree (same as standard tasks — reuse the worktree creation logic from `processPlanning`)
- Transition directly: `backlog → in_progress`
- Spawn a coder agent with the task's description and any target file hints from `task.Context["target_files"]`

Implementation approach — add a quickfix check at the top of `processBacklog`:

```go
func (o *Orchestrator) processBacklog(task *model.Task) error {
    if task.Category.IsQuickFix() {
        return o.processQuickFix(task)
    }
    // ... existing processBacklog logic unchanged ...
}
```

**Add `processQuickFix`** as a new method on `*Orchestrator`:

```go
func (o *Orchestrator) processQuickFix(task *model.Task) error
```

This method should:
1. Check if an agent is already assigned — if so, return nil (agent is working)
2. If agent is assigned but dead/idle, clean up worktree, clear assignment, increment retry count (max 3 retries, then fail)
3. Check `o.runner.CanSpawn()` — if no capacity, return nil
4. Create feature worktree if `task.WorktreeBranch == ""` (use `o.worktree.CreateFeature(taskFeatureName(task))`)
5. Transition `backlog → in_progress` via `state.TransitionTask`
6. Load project, generate coder prompt via `prompt.Generate(prompt.Opts{...})` with `AgentType: model.AgentCoder`
7. Spawn agent via `o.runner.SpawnAgent(task, featureName, model.AgentCoder, coderPrompt)`
8. Save task with `AssignedAgentID`
9. Emit `"quickfix_started"` event

**Modify `executeMerge`** for quickfix merge failure handling. Currently, failed merges call `o.failTask()` and optionally spawn a fixer agent. For quickfix tasks:

- On merge failure, set `task.NeedsHumanReview = true` on the task
- Do NOT spawn a fixer agent
- Still call `o.failTask()` with a descriptive message
- Emit `"quickfix_merge_failed"` event

Add this check near the top of the merge failure branch (after `if !result.Success`):

```go
if task.Category.IsQuickFix() {
    task.NeedsHumanReview = true
    if err := o.failTask(task, "quick fix merge failed — flagged for human review"); err != nil {
        return err
    }
    o.emit("quickfix_merge_failed", map[string]any{"task_id": task.ID, "conflicts": result.Conflicts})
    return nil
}
```

#### 2. `internal/orchestrator/orchestrator.go`

**Modify the `doTick` function** (step 1, processing BACKLOG tasks). The existing code already calls `o.processBacklog(task)` for each backlog task — no changes needed here since the routing happens inside `processBacklog`.

**Modify the `doTick` function** (step 4, processing IN_PROGRESS tasks). Currently, `scheduleSubtasks` and `checkFeatureCompletion` are called for all IN_PROGRESS parent tasks. For quickfix tasks (which are top-level with no subtasks), the behavior is:

- `scheduleSubtasks` should be skipped (quickfix tasks have no subtasks)
- Instead, check if the quickfix task's agent has completed and handle the result

Add a quickfix-specific check in the IN_PROGRESS loop:

```go
for i := range inProgressTasks {
    task := &inProgressTasks[i]
    if task.Category.IsQuickFix() {
        // Quick fix tasks are top-level with no subtasks.
        // Agent completion is handled by processAgentResult (step 2).
        // If agent is done and task is still in_progress, transition to merging.
        if task.AssignedAgentID == nil {
            // Agent finished — transition to merging
            if err := o.transitionQuickFixToMerging(task); err != nil {
                o.logger.Error("quickfix to merging", "task_id", task.ID, "error", err)
            }
        }
        continue
    }
    if err := o.scheduleSubtasks(task); err != nil {
        o.logger.Error("schedule subtasks", "task_id", task.ID, "error", err)
    }
    if err := o.checkFeatureCompletion(task); err != nil {
        o.logger.Error("check feature completion", "task_id", task.ID, "error", err)
    }
}
```

**Add `transitionQuickFixToMerging`**:

```go
func (o *Orchestrator) transitionQuickFixToMerging(task *model.Task) error
```

This method should:
1. Run constraint checks on the feature worktree (reuse the constraint evaluation pattern from `checkFeatureCompletion`)
2. If constraints fail, set `task.NeedsHumanReview = true` and emit `"quickfix_constraint_failed"`; do NOT transition
3. If constraints pass, transition `in_progress → testing_ready → merging` (fast-track through testing_ready since quickfix tasks skip human test review)
4. Emit `"quickfix_merging"` event

Wait — per the PRD, quickfix lifecycle is `BACKLOG → IN_PROGRESS → MERGING → DONE`. The `testing_ready` state is skipped. But the state machine requires `in_progress → testing_ready → merging`. To handle this:

- Fast-track through `testing_ready` automatically (no human gate): transition `in_progress → testing_ready`, then immediately `testing_ready → merging`
- Both transitions happen in `transitionQuickFixToMerging`, not waiting for the next tick

### New test file

#### 3. `internal/orchestrator/quickfix_test.go`

Test quickfix lifecycle routing. Follow the test setup pattern from `lifecycle_test.go` (use `setupLifecycleTest`).

**Tests to write:**

- `TestProcessBacklog_QuickFix_TransitionsToInProgress` — Create a quickfix task in backlog. Call `processBacklog`. Verify task transitions to `in_progress` (not `planning`).
- `TestProcessBacklog_Standard_StillGoesToPlanning` — Create a standard task in backlog. Call `processBacklog`. Verify it still transitions to `planning`.
- `TestQuickFix_SkipsSubtaskScheduling` — Create a quickfix task in `in_progress`. Run the IN_PROGRESS processing. Verify `scheduleSubtasks` is not called for it.
- `TestQuickFix_MergeFailure_FlagsForHumanReview` — Create a quickfix task in `merging` state. Simulate merge failure. Verify `NeedsHumanReview` is true and no fixer agent is spawned.
- `TestQuickFix_NeverEntersPlanningStates` — Create a quickfix task, run it through the full lifecycle. Verify events never include `planning`, `plan_review`, `test_writing`, or `test_review`.

**Test setup notes:**
- Use `testutil.NewTestDB(t)` for database
- Use `testutil.CreateQuickFixTask(t, db, projectID, title, status)` (from Agent 01)
- If `CreateQuickFixTask` doesn't exist yet, create quickfix tasks manually:
  ```go
  task := &model.Task{ID: uuid.New(), ProjectID: projectID, Title: "fix typo", Status: model.StatusBacklog, Category: model.CategoryQuickFix}
  db.Create(task)
  ```
- The processBacklog/processQuickFix functions need a runner that can spawn — use the mock runner pattern from `lifecycle_test.go`

## Scope Limitation

- Do NOT modify `internal/model/` or `internal/state/` — those belong to Agent 01
- Do NOT modify `internal/tui/` — that belongs to Agent 04
- Do NOT modify `ingestBugReports()` — that belongs to Agent 05
- Do NOT create `internal/orchestrator/classifier.go` — that belongs to Agent 03
- You own: `task_processing.go` (processBacklog, processQuickFix, executeMerge), `orchestrator.go` (doTick IN_PROGRESS loop, transitionQuickFixToMerging), and `quickfix_test.go`

## Conventions

- Namespace: `package orchestrator`
- Error wrapping: `fmt.Errorf("process quick fix: %w", err)`
- Logging: `o.logger.Info("quickfix started", "task_id", task.ID)`
- Events: `o.emit("quickfix_started", map[string]any{"task_id": task.ID})`
- Build verification: `go build ./... && go test ./internal/orchestrator/...`
- Format: `gofmt -w .`
