# Agent: Wave-Based Scheduling

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent orchestrator that manages Claude Code agents via tmux in git bare repos with worktrees.
Your task is to implement file-overlap-aware scheduling that groups subtasks into waves. Subtasks within a wave run in parallel; waves run sequentially. This prevents merge conflicts from concurrent agents touching the same files.

## Context

Read these before starting:
- `docs/merge-overhaul/prd-merge-reliability.md` (section 4.1 — all subsections)
- `internal/orchestrator/scheduling.go` (existing scheduler functions: GetScheduleSummary, GetAssignableTasks, DependenciesMet, GetBlockingTasks)
- `internal/orchestrator/orchestrator.go` (scheduleSubtasks function ~line 1268 — understand how subtasks are currently scheduled. Also read HandlePlanApproved ~line 1600 to understand plan approval flow)
- `internal/model/models.go` (Task model — Context JSONField, Plan field, DependencyIDs)

The orchestrator currently schedules ALL backlog subtasks simultaneously up to agent capacity. This causes merge conflicts when agents touch overlapping files. The planner prompt (updated by agent 04) now asks for a `files` list per subtask and the plan validation (agent 04) checks for file overlap.

## Dependencies

This agent depends on:
- Agent 02 (orchestrator-reliability) — orchestrator.go has been modified with safety checks. Your changes to `scheduleSubtasks()` must be compatible.
- Agent 04 (planner-and-validation) — plan validation now checks for file overlap, and the planner prompt requests `files` lists. The plan JSON now reliably contains `files` arrays.

Read the current state of `orchestrator.go` and `scheduling.go` carefully before making changes, as they may have been modified by prior agents.

## Deliverables

### 1. Schedule Data Types (`internal/orchestrator/scheduling.go`)

Add to the existing `scheduling.go` file:

```go
// SubtaskGroup represents a set of subtasks that can run concurrently.
// All subtasks within a group have no file overlap.
type SubtaskGroup struct {
    Order    int
    TaskIDs  []uuid.UUID
}

// Schedule represents the ordered execution plan for a set of subtasks.
type Schedule struct {
    Groups []SubtaskGroup
}
```

### 2. BuildSchedule Function (`internal/orchestrator/scheduling.go`)

```go
// BuildSchedule analyzes file overlap between subtasks and produces
// an ordered list of groups. Subtasks within a group have no file
// overlap and can run concurrently. Groups run sequentially — group
// N+1 starts only after all subtasks in group N are merged.
//
// If no subtasks have file data, returns a single group containing
// all subtasks (backward-compatible behavior).
func BuildSchedule(subtasks []model.Task) Schedule
```

Algorithm — greedy graph coloring on the file-overlap conflict graph:

1. Extract the `files` list from each subtask. The planner stores this in the plan JSON, and the orchestrator copies it to `subtask.Context["estimated_files"]` or similar when creating subtasks in `onPlannerCompleted()`. Check what field is actually used — look at how subtasks are created from plan entries.

2. Build an undirected conflict graph: each subtask is a node, edges connect subtasks whose file lists overlap (any file in common).

3. Also honor explicit `DependencyIDs`: if subtask B depends on subtask A, they MUST be in different groups (B's group must come after A's group).

4. Greedy-color the graph:
   - Order nodes by degree descending (most conflicts first)
   - Assign the lowest color number that doesn't conflict with neighbors
   - Respect dependency ordering: if B depends on A, B's color must be > A's color

5. Each color becomes a `SubtaskGroup`. Groups are ordered by color index.

6. **Fallback**: If no subtask has file data, return a single group with all subtask IDs (preserves current behavior).

### 3. Store Schedule on Plan Approval (`internal/orchestrator/orchestrator.go`)

In `HandlePlanApproved()`, after creating subtasks from the plan, call `BuildSchedule()` and store the result:

```go
// After subtasks are created from plan entries:
schedule := BuildSchedule(subtasks)
scheduleJSON, _ := json.Marshal(schedule)
task.Context["schedule"] = json.RawMessage(scheduleJSON)
o.db.Save(task)
```

Find the exact location in `HandlePlanApproved()` where subtasks are created and the task is saved. Add the schedule computation right before the final save.

### 4. Wave-Based Scheduling in scheduleSubtasks (`internal/orchestrator/orchestrator.go`)

Modify `scheduleSubtasks()` to respect the wave schedule:

```go
func (o *Orchestrator) scheduleSubtasks(parent *model.Task) error {
    // Check for wave schedule
    scheduleRaw, hasSchedule := parent.Context["schedule"]
    if !hasSchedule {
        // Legacy: no schedule — use existing logic (schedule all at once)
        // ... keep existing code path ...
    }

    // Parse schedule
    var schedule Schedule
    if err := json.Unmarshal(scheduleRaw, &schedule); err != nil {
        // Fallback to legacy on parse error
        // ... keep existing code path ...
    }

    // Find the current group — the earliest group with unfinished subtasks
    currentGroup := o.findCurrentGroup(parent, schedule)
    if currentGroup == nil {
        return nil // all groups finished
    }

    // Only schedule subtasks in the current group
    for _, taskID := range currentGroup.TaskIDs {
        var subtask model.Task
        if err := o.db.First(&subtask, taskID).Error; err != nil {
            continue
        }
        if subtask.Status == model.StatusBacklog {
            // ... existing spawn logic for this subtask ...
        }
    }

    return nil
}
```

Add a helper:

```go
// findCurrentGroup returns the earliest group that has subtasks not
// yet in a terminal state (done or failed). Returns nil if all groups
// are complete.
func (o *Orchestrator) findCurrentGroup(parent *model.Task, schedule Schedule) *SubtaskGroup
```

A group is "finished" when all its subtask IDs resolve to tasks in `done` or `failed` status. When a group finishes, the next group becomes current.

### 5. Tests (`internal/orchestrator/scheduling_test.go`)

Add to the existing test file or create if needed:

- **No file overlap**: 4 subtasks, no shared files -> single group with all 4
- **Full overlap**: 3 subtasks all touching the same file -> 3 groups of 1 each (sequential)
- **Partial overlap**: A overlaps B, B overlaps C, A doesn't overlap C -> 2 groups: {A, C} and {B} (or similar valid coloring)
- **No file data (fallback)**: Subtasks with empty file lists -> single group with all subtasks
- **Explicit dependencies**: A has no file overlap with B, but B depends on A -> B is in a later group
- **Mixed overlap + dependencies**: File overlap and explicit deps both present -> both are respected
- **findCurrentGroup**: First group all done -> returns second group. First group has in_progress -> returns first group. All groups done -> returns nil.

## Scope Limitation

ONLY modify `internal/orchestrator/scheduling.go` and `internal/orchestrator/orchestrator.go`. In orchestrator.go, limit changes to `scheduleSubtasks()` and `HandlePlanApproved()`. Do NOT modify any other functions in orchestrator.go. Do NOT touch `internal/worktree/`, `internal/merge/`, `internal/agent/`, or `internal/prompt/`.

## Conventions

- Go 1.22+ with standard library
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./...`
