# Agent: Test-Writing Flow & Orchestrator Lifecycle

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent orchestrator that manages Claude Code agents via tmux in git bare repos with worktrees.
Your task is to implement the `TEST_WRITING` orchestrator flow: plan approval creates TDD-aware subtasks, only test-phase subtasks are scheduled during `TEST_WRITING`, and the parent transitions to `TEST_REVIEW` when all tests complete.

## Context

Read these before starting:
- `docs/tdd-enforcement/prd-tdd-enforcement.md` (sections 4.4.1, 4.4.2, 4.4.4, 4.5.2, 4.3.5, 4.8.2, 4.10)
- `internal/orchestrator/orchestrator.go` — read these sections:
  - `HandlePlanApproved` (around line 2272) — creates subtasks from plan, transitions to IN_PROGRESS
  - `scheduleSubtasks` (around line 1810) — wave-based scheduling with dependency checking
  - `doTick` (around line 120) — the 8-step tick loop
  - `processPlanning` (around line 850) — plan parsing and review spawning
  - `planEntry` struct and `parsePlan` function (around line 2949)
  - `checkFeatureCompletion` (around line 2036)
- `internal/orchestrator/plan_validation.go` (`ValidatePlan`, `MergeTDDDependencies`)
- `cmd/drem/config.go` (Config struct)
- `internal/model/enums.go` (new `StatusTestWriting`, `StatusTestReview`, `StatusRejected`)
- `internal/model/models.go` (Task struct with new `Phase`, `TestsFor`, `TDDExceptions` fields)

## Dependencies

This agent depends on Agents 02 (state machine), 03 (plan schema), and 04 (plan validation). The new states (`StatusTestWriting`, `StatusTestReview`), model fields (`Phase`, `TestsFor`, `TDDExceptions`), extended `planEntry`/`parsePlan`, and `MergeTDDDependencies` must exist.

## Deliverables

### 1. Modify `internal/orchestrator/orchestrator.go`

**a) Update `HandlePlanApproved` — TDD-aware subtask creation:**

After parsing the plan, store TDD-related fields on each subtask:

```go
// In the subtask creation loop, add Phase and TestsFor to the context
// and to the Task model fields:
sub := model.Task{
    // ... existing fields ...
    Phase:    sp.Phase,  // "test", "implementation", "integration", or ""
    // TestsFor is set below after all subtasks are created
}

ctx := model.JSONField{
    "agent_type":      sp.AgentType,
    "estimated_files": sp.EstimatedFiles,
    "phase":           sp.Phase,
}
```

After creating all subtasks, apply `MergeTDDDependencies` from plan_validation.go to compute auto-generated reverse dependencies from `tests_for`, then update both `DependencyIDs` and `TestsFor` fields:

```go
// Auto-generate TDD reverse dependencies
merged := MergeTDDDependencies(planResult.Subtasks)

// Second pass: set dependency IDs (including auto-generated TDD deps)
for i, sp := range merged {
    // ... existing dependency mapping code, but use merged[i].Dependencies ...
}

// Third pass: set TestsFor on test-phase subtasks
for i, sp := range planResult.Subtasks {
    if len(sp.TestsFor) > 0 {
        var testsForIDs model.JSONArray
        for _, idx := range sp.TestsFor {
            if idx >= 0 && idx < len(createdIDs) {
                testsForIDs = append(testsForIDs, createdIDs[idx].String())
            }
        }
        o.db.Model(&model.Task{}).Where("id = ?", createdIDs[i]).
            Update("tests_for", testsForIDs)
    }
}
```

Store `tdd_exceptions` on the parent task:

```go
if len(planResult.TDDExceptions) > 0 {
    exceptionsJSON, _ := json.Marshal(planResult.TDDExceptions)
    var exceptionsField any
    json.Unmarshal(exceptionsJSON, &exceptionsField)
    task.TDDExceptions = model.JSONField{"exceptions": exceptionsField}
}
```

**Change the transition target**: If the plan has any test-phase subtasks, transition to `TEST_WRITING` instead of `IN_PROGRESS`. If the plan has no test-phase subtasks (old-format plan or all exceptions), transition to `IN_PROGRESS` as before:

```go
hasTestPhase := false
for _, sp := range planResult.Subtasks {
    if sp.Phase == "test" {
        hasTestPhase = true
        break
    }
}

var targetStatus model.TaskStatus
if hasTestPhase {
    targetStatus = model.StatusTestWriting
} else {
    targetStatus = model.StatusInProgress
}
evt, err := state.TransitionTask(&task, targetStatus, "user", map[string]any{"action": "plan_approved"})
```

**b) Add `processTestWriting` method:**

```go
// processTestWriting schedules test-phase subtasks and checks for completion.
// When all test-phase subtasks are done, transitions the parent to TEST_REVIEW.
func (o *Orchestrator) processTestWriting(parent *model.Task) error
```

Logic:
1. Query subtasks: `WHERE parent_task_id = ? AND phase = 'test'`
2. Schedule unstarted test subtasks using the existing scheduling logic (dependency check, wave scheduling, capacity check). Reuse `scheduleSubtasks` but filter to only test-phase. The simplest approach: add a `phaseFilter` parameter or check `sub.Phase` inside the scheduling loop.
3. Check if all test-phase subtasks are in a terminal state (DONE or FAILED or REJECTED):
   - If all done → transition parent to `TEST_REVIEW`
   - If any failed and the rest are terminal → transition parent to `FAILED`

**c) Add phase-aware scheduling** in `scheduleSubtasks`:

Add a check in the scheduling loop to skip subtasks that don't match the parent's current state:

```go
// During TEST_WRITING, only schedule test-phase subtasks
if parent.Status == model.StatusTestWriting && sub.Phase != "test" {
    continue
}

// During IN_PROGRESS, only schedule implementation and integration subtasks
if parent.Status == model.StatusInProgress && sub.Phase == "test" {
    continue
}
```

**d) Update `doTick`** — add a step to process TEST_WRITING tasks:

After step 4 (process PLANNING tasks) and before step 5 (process IN_PROGRESS), add:

```go
// 4b. Process TEST_WRITING parent tasks.
var testWritingTasks []model.Task
if err := o.db.Where("project_id = ? AND status = ? AND parent_task_id IS NULL",
    o.projectID, model.StatusTestWriting).Find(&testWritingTasks).Error; err != nil {
    o.logger.Error("doTick: query test_writing tasks", "error", err)
} else {
    for i := range testWritingTasks {
        if err := o.processTestWriting(&testWritingTasks[i]); err != nil {
            o.logger.Error("doTick: processTestWriting", "task_id", testWritingTasks[i].ID, "error", err)
        }
    }
}
```

**e) Add test file tracking** (§4.8.2):

When a test-phase agent completes (in `processAgentResult`), extract the actual test files from the agent's diff and store them:

```go
// After a test-phase subtask's agent completes successfully:
if sub.Phase == "test" {
    actualTestFiles := o.extractTestFiles(agent.WorktreePath, featureBranch)
    if len(actualTestFiles) > 0 {
        if sub.Context == nil {
            sub.Context = make(model.JSONField)
        }
        sub.Context["actual_test_files"] = actualTestFiles
        o.db.Save(sub)
    }
}
```

Add the helper:

```go
// extractTestFiles runs git diff --name-only on the agent's worktree and
// returns files matching test patterns.
func (o *Orchestrator) extractTestFiles(worktreePath, baseBranch string) []string
```

Use `worktree.RunGit` or equivalent to run `git diff --name-only <baseBranch>...HEAD` in the worktree. Filter results for test patterns: `*_test.go`, `*_test.py`, `test_*.py`, `*.test.ts`, `*.test.js`, `*.spec.ts`, `*.spec.js`.

**f) Add baseline test health check** (§4.5.2):

Before scheduling the first test subtask in `processTestWriting`, check if the test suite passes on the integration branch. This runs once, when the parent first enters TEST_WRITING:

```go
// Check baseline test health (once per task)
if parent.Context == nil {
    parent.Context = make(model.JSONField)
}
if _, checked := parent.Context["baseline_tests_checked"]; !checked {
    testCmd := o.getTestCommand(parent)
    if testCmd != "" {
        featureName := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
        featureDir := o.worktree.FeatureWorktreePath(featureName)
        result := o.runCommand(featureDir, testCmd)
        parent.Context["baseline_tests_checked"] = true
        if result.ExitCode != 0 {
            parent.Context["baseline_tests_failed"] = true
            parent.Context["baseline_test_output"] = truncate(result.Stdout+result.Stderr, 5000)
            // Block: don't schedule anything until pre-existing failures resolved
            o.logger.Warn("baseline tests fail on integration branch",
                "task_id", parent.ID, "exit_code", result.ExitCode)
            o.db.Save(parent)
            return nil
        }
        o.db.Save(parent)
    }
}
```

If `getTestCommand` doesn't exist yet, add it — see §4.10 for test command discovery:

```go
// getTestCommand returns the test command for the project. Checks drem.toml
// first, falls back to CLAUDE.md. Returns empty string if not found.
func (o *Orchestrator) getTestCommand(task *model.Task) string
```

### 2. Modify `cmd/drem/config.go`

Add test-related configuration fields:

```go
type Config struct {
    // ... existing fields ...
    TestCommand     string        `toml:"test_command"`
    CompileCommand  string        `toml:"compile_command"`
    ScopedTests     *bool         `toml:"scoped_tests"`     // pointer for default-true detection
    TestTimeout     time.Duration `toml:"test_timeout"`
}
```

Defaults:

```go
func DefaultConfig() Config {
    scopedDefault := true
    return Config{
        // ... existing ...
        TestTimeout: 5 * time.Minute,
        ScopedTests: &scopedDefault,
    }
}
```

### 3. Add tests

**`internal/orchestrator/test_writing_test.go`** (new file):

- **processTestWriting schedules only test-phase subtasks**: Create a parent in TEST_WRITING with 2 test and 2 impl subtasks in BACKLOG. Call processTestWriting. Assert only the test subtasks get scheduled (status changes), impl subtasks remain in BACKLOG.
- **All test subtasks done → TEST_REVIEW**: Create a parent in TEST_WRITING with all test subtasks in DONE. Call processTestWriting. Assert parent transitions to TEST_REVIEW.
- **HandlePlanApproved with TDD plan → TEST_WRITING**: Create a task with a plan containing phase-annotated subtasks. Call HandlePlanApproved. Assert parent is in TEST_WRITING (not IN_PROGRESS).
- **HandlePlanApproved with old-format plan → IN_PROGRESS**: Plan without phases. Assert parent is in IN_PROGRESS (backward compat).
- **Phase-aware scheduling**: Parent in IN_PROGRESS with test and impl subtasks. Only impl subtasks get scheduled.
- **Test file extraction**: Mock a git diff output, verify test file pattern matching.
- **TDD reverse dependencies**: Plan with `tests_for` generates correct dependency IDs on subtasks.

## Scope Limitation

ONLY modify:
- `internal/orchestrator/orchestrator.go`
- `cmd/drem/config.go`
- New test files in `internal/orchestrator/`

Do NOT modify: `internal/model/`, `internal/state/`, `internal/prompt/`, `internal/tui/`, `internal/orchestrator/plan_validation.go`.

## Conventions

- Go 1.22+ with standard library
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./...`
