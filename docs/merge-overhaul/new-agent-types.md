# New Agent Types — Design Notes

**Date**: 2026-03-07
**Status**: Draft (future follow-up)
**Related**: [Merge Reliability PRD](prd-merge-reliability.md), [Planner Decomposition Strategy](planner-decomposition-strategy.md)
**Scope**: `internal/model/enums.go`, `internal/prompt/prompt.go`, `internal/orchestrator/orchestrator.go`, `internal/agent/runner.go`

---

## Current Agent Types

The orchestrator has 4 agent types (`model/enums.go:76-80`), but only 3 have distinct prompt instructions:

| Type | Prompt | Lifecycle | Produces |
|------|--------|-----------|----------|
| `planner` | Decompose task into subtasks | Spawned in `processPlanning()`, result parsed in `onPlannerCompleted()` | `plan.json` |
| `coder` | Implement the described task | Spawned in `scheduleSubtasks()`, branch merged in `onAgentCompleted()` | Git commits |
| `researcher` | Investigate and document findings | Same lifecycle as coder | `research-report.md` |
| `orchestrator` | (unused — the Go process is the orchestrator) | N/A | N/A |

The supervisor is not an agent type — it's a separate system (`internal/supervisor/`) that uses the Claude API for JSON-structured evaluations and interactive tmux sessions. It does not go through the agent runner.

---

## Proposed New Agent Types

### 1. Merge Resolver

**Gap addressed**: When `MergeAgentIntoFeature()` or rebase fails due to conflicts, the orchestrator currently marks the subtask as `failed` and waits for a supervisor. The merge reliability findings show these conflicts are almost always mechanically resolvable — additive changes to the same file section (end of include lists, end of method blocks, duplicated additions).

**What it does**: Receives a merge conflict description and resolves it in the integration worktree.

**Lifecycle**:
```
MergeAgentIntoFeature() fails with conflicts
  → orchestrator checks conflict severity (via supervisor.MergeConflictAnalysis)
  → if severity is "trivial" or "moderate": spawn merge_resolver agent
  → agent works in the integration worktree (not a new worktree)
  → agent resolves conflicts, runs build, commits the merge
  → on success: subtask transitions to done
  → on failure: subtask transitions to failed (escalate to supervisor)
```

**Key design decisions**:

- **Runs in the integration worktree**, not its own worktree. It needs to see the conflict markers in-place. This is different from coder/researcher agents which get isolated worktrees.
- **No new branch**. The merge is in-progress (conflict state) in the integration worktree. The resolver commits the merge resolution directly.
- **Short-lived**. Should complete in under 2 minutes. Has a tight timeout (5 min) since it's blocking the merge pipeline.
- **Receives conflict diff as context**. The prompt includes the conflicting files, the diff from both sides, and the resolution hints from `MergeConflictAnalysis`.

**Prompt sketch**:
```markdown
You are a merge resolver agent. A merge of `<agent-branch>` into
`<feature-branch>` produced conflicts in the following files:

<list of conflicting files with diff hunks>

## Resolution Guidance
<supervisor's resolution_hints from MergeConflictAnalysis>

## Instructions
1. Resolve all conflict markers in the listed files
2. For additive conflicts (both sides adding code), keep both additions
3. For duplicate additions, deduplicate
4. Run the build command to verify the resolution compiles
5. Stage resolved files and complete the merge commit
6. Do NOT modify any files that are not in the conflict list
```

**Integration points**:
- `internal/model/enums.go`: Add `AgentMergeResolver AgentType = "merge_resolver"`
- `internal/prompt/prompt.go`: Add `mergeResolverInstructions()` with conflict context
- `internal/orchestrator/orchestrator.go`: In `onAgentCompleted()`, when merge fails and supervisor says severity is trivial/moderate, spawn resolver instead of failing
- `internal/agent/runner.go`: `SpawnMergeResolver()` variant that works in an existing worktree (no `CreateAgentWorktree()` call)

---

### 2. Reviewer

**Gap addressed**: The `plan_review` and `testing_ready` states are human gates. The human reviewer has to manually read the plan or test the feature. A reviewer agent can pre-screen plans and run automated acceptance checks, surfacing issues before the human looks at it.

**What it does**: Performs automated review of plans (at `plan_review`) or completed features (at `testing_ready`), producing a structured review report.

**Two sub-modes**:

#### 2a. Plan Reviewer

Triggered when a task enters `plan_review`. Runs before the human sees the plan.

```markdown
You are a plan reviewer agent. A planner has produced the following
plan for task "<title>":

<plan JSON>

## Parent Task
<task description with acceptance criteria>

## Instructions
Evaluate this plan against the acceptance criteria. Produce a
`review.json` file:

{
  "coverage": "full|partial|none",
  "uncovered_criteria": ["criterion not addressed by any subtask"],
  "file_overlap_risk": "low|medium|high",
  "overlapping_pairs": [{"a": 0, "b": 2, "files": ["shared.go"]}],
  "integration_gap": true/false,
  "issues": ["issue description"],
  "recommendation": "approve|revise|reject"
}
```

The orchestrator presents this review alongside the plan in the TUI at plan_review, giving the human reviewer a head start.

#### 2b. Feature Reviewer

Triggered when a task enters `testing_ready`. Runs acceptance checks before the human tests.

```markdown
You are a feature reviewer agent. All subtasks for "<title>" have been
merged into the integration branch.

## Acceptance Criteria
<from task description>

## Changes
<git diff of integration branch vs default branch>

## Instructions
1. Read the acceptance criteria carefully
2. Examine the code changes
3. Run the build and any tests
4. For each acceptance criterion, verify it is addressed by the code
5. Produce a `review.json` file:

{
  "build_passes": true/false,
  "tests_pass": true/false,
  "criteria_results": [
    {"criterion": "...", "met": true/false, "evidence": "file:line"}
  ],
  "issues": ["missing wiring between X and Y"],
  "recommendation": "approve|needs_work"
}
```

**Lifecycle**:
- Spawned automatically on `plan_review` or `testing_ready` entry
- Runs in the integration worktree (read-only — should not commit)
- Result stored in `task.Context["review"]`
- Does NOT auto-approve — the review is advisory for the human gate
- TUI displays the review summary on the plan_review/testing_ready screen

**Key design decision**: The reviewer does not replace human gates. It augments them. The recommendation is informational. This avoids the risk of automated approval of bad plans or broken features.

**Integration points**:
- `internal/model/enums.go`: Add `AgentReviewer AgentType = "reviewer"`
- `internal/prompt/prompt.go`: Add `planReviewerInstructions()` and `featureReviewerInstructions()`
- `internal/orchestrator/orchestrator.go`: Spawn reviewer when entering `plan_review` or `testing_ready`; parse `review.json` on completion; store in task context
- `internal/tui/`: Display review summary in plan_review and testing_ready views

---

### 3. Fixer

**Gap addressed**: When a merge succeeds but the build fails, `MergeFeatureIntoMain()` rolls back with `git reset --hard HEAD~1` and marks the task `failed`. The supervisor can diagnose the build failure (`BuildFailureDiagnosis`) and even determine `can_auto_fix: true`, but there's no agent to actually apply the fix. Similarly, after `testing_ready` rejection with feedback, the current path is to replan or manually intervene.

**What it does**: Receives a diagnosis of what's broken (build failure, test failure, or user feedback) and applies a targeted fix in the integration worktree.

**Lifecycle**:
```
Build failure after merge → supervisor diagnoses → can_auto_fix: true
  → spawn fixer agent in integration worktree
  → agent applies fix, builds, tests, commits
  → on success: retry merge
  → on failure: task fails (escalate)

OR:

testing_ready rejected with feedback
  → supervisor synthesizes feedback (FeedbackIntegration)
  → spawn fixer agent in integration worktree
  → agent addresses feedback, builds, tests, commits
  → on success: re-enter testing_ready
  → on failure: task fails
```

**Key design decisions**:

- **Works in the integration worktree**, like the merge resolver. Fixes need to be applied to the merged state, not a side branch.
- **Scoped fix, not reimplementation**. The prompt explicitly constrains the agent to the diagnosed issue. It should not refactor, add features, or touch unrelated code.
- **Single attempt**. If the fixer fails, the task goes to `failed` for human intervention. No retry loop — the diagnosis was already the best the supervisor could produce.

**Prompt sketch**:
```markdown
You are a fixer agent. The integration branch has a specific issue
that needs a targeted fix.

## Diagnosis
<root_cause from BuildFailureDiagnosis or FeedbackIntegration>

## Affected Files
<affected_files list>

## Suggested Fix
<suggested_fix from diagnosis>

## Instructions
1. Apply ONLY the fix described above — do not refactor or change
   anything else
2. Run the build command to verify the fix works
3. Run tests if applicable
4. Commit with a message describing the fix
5. The fix should be minimal — the smallest change that resolves the issue
```

**Integration points**:
- `internal/model/enums.go`: Add `AgentFixer AgentType = "fixer"`
- `internal/prompt/prompt.go`: Add `fixerInstructions()` with diagnosis context
- `internal/orchestrator/orchestrator.go`: In `executeMerge()` when build fails and `can_auto_fix`, spawn fixer instead of failing. In `HandleTestingRejected()`, optionally spawn fixer for feedback-based fixes.

---

### 4. Integrator

**Gap addressed**: The planner decomposition strategy doc (P-2) proposes a mandatory integration subtask. But an integration subtask planned at decomposition time may not capture the actual wiring needed after agents produce their work. An integrator agent type that runs *after all subtask merges* and can see the full integrated codebase would be more effective.

**What it does**: After all subtasks are merged into the integration branch, examines the combined code for missing wiring, dead imports, unconnected components, and end-to-end flow gaps. Applies the glue code.

**Lifecycle**:
```
All subtasks merged → checkFeatureCompletion()
  → before transitioning to testing_ready
  → spawn integrator agent in integration worktree
  → agent analyzes cross-cutting concerns
  → agent wires components, fixes imports, adds registrations
  → agent builds and runs tests
  → on completion: transition parent to testing_ready
  → on failure: transition parent to failed
```

**Key design decisions**:

- **Runs after all subtask work is merged**, so it sees the full picture. This is fundamentally different from a subtask — it's a post-merge pass, not a parallel work unit.
- **Has access to the parent task's acceptance criteria** and all subtask descriptions. This lets it verify that the pieces are wired together to deliver the feature, not just compile.
- **Produces commits in the integration branch** directly. Its work is part of the feature, not a separate branch.
- **Optional**. Some features (single-file changes, independent additions) don't need integration. The planner can indicate this in the plan: `"needs_integration": false`.

**Prompt sketch**:
```markdown
You are an integrator agent. All subtasks for "<title>" have been
implemented and merged into the integration branch. Your job is to
wire the pieces together.

## Feature Goal
<parent task description + acceptance criteria>

## Subtasks Completed
<list of subtask titles and descriptions>

## Instructions
1. Read the acceptance criteria and understand the end-to-end flow
2. Examine the code added by each subtask
3. Identify missing connections:
   - Components created but never instantiated or registered
   - Functions defined but never called from the main flow
   - Types added to one module but not imported where needed
   - State changes that need to propagate to the UI/rendering layer
   - Configuration or registration entries that wire subsystems together
4. Apply the minimal wiring to connect the pieces
5. Build and run tests
6. Commit your changes with a message describing the integration wiring

Do NOT reimplement subtask work. Do NOT refactor code. Only add
the minimal glue needed to make the feature work end-to-end.
```

**Integration points**:
- `internal/model/enums.go`: Add `AgentIntegrator AgentType = "integrator"`
- `internal/prompt/prompt.go`: Add `integratorInstructions()` with parent context and subtask list
- `internal/orchestrator/orchestrator.go`: In `checkFeatureCompletion()`, when all subtasks are done and plan has `needs_integration: true` (or default), spawn integrator before transitioning to `testing_ready`

---

## Priority and Dependency Order

```
                    ┌─────────────────┐
                    │ Merge Resolver   │  ← Highest ROI: directly fixes
                    │ (addresses 70%   │     the #1 failure mode
                    │  of supervisor   │
                    │  interventions)  │
                    └────────┬────────┘
                             │
                    ┌────────▼────────┐
                    │ Fixer            │  ← Handles build failures and
                    │ (addresses post- │     test feedback automatically
                    │  merge failures) │
                    └────────┬────────┘
                             │
                    ┌────────▼────────┐
                    │ Integrator       │  ← Catches wiring gaps that
                    │ (addresses cross-│     caused Automation Lanes
                    │  cutting gaps)   │     to ship non-functional
                    └────────┬────────┘
                             │
                    ┌────────▼────────┐
                    │ Reviewer         │  ← Augments human gates,
                    │ (advisory, not   │     reduces review burden
                    │  blocking)       │
                    └─────────────────┘
```

**Merge Resolver** should be implemented first — it directly addresses the problem that caused 70%+ of supervisor interventions in the findings report. It also has the simplest lifecycle (short-lived, deterministic success criteria, no new state transitions needed).

**Fixer** is second — it closes the loop on build failures that the supervisor can already diagnose but nobody can act on.

**Integrator** is third — it addresses the Automation Lanes class of failure where isolated subtasks work individually but the feature is non-functional.

**Reviewer** is lowest priority — it's quality-of-life for the human reviewer, not a reliability fix. The human gate is working; it just takes time.

---

## Implementation Considerations

### Agent Runner Changes

The current `SpawnAgent()` always creates a new worktree. Merge resolver, fixer, and integrator all work in the integration worktree. A new spawn variant is needed:

```go
// SpawnAgentInWorktree starts an agent in an existing worktree
// without creating a new branch. Used for merge resolution, fixing,
// and integration tasks that operate on the integration branch directly.
func (r *Runner) SpawnAgentInWorktree(
    task *model.Task,
    worktreePath string,
    agentType model.AgentType,
    prompt string,
) (*model.Agent, error)
```

This skips `CreateAgentWorktree()` and uses the provided path directly. The agent commits to whatever branch the worktree is on (the feature/integration branch).

### Concurrency Constraints

- **Merge resolver**: Must be the only agent operating on the integration worktree. No other subtask merges should happen while a resolver is running.
- **Fixer**: Same constraint — exclusive access to integration worktree.
- **Integrator**: Same constraint. Additionally, should not run until all subtask agent worktrees are cleaned up (to avoid ref confusion).
- **Reviewer**: Read-only, so can run concurrently with other agents. But should not run concurrently with another reviewer on the same task.

This suggests a per-feature-worktree lock:

```go
type Runner struct {
    // ...
    featureLocks map[string]*sync.Mutex // keyed by feature name
}
```

### Prompt Context Differences

Unlike coder agents that receive a subtask description, these agents need richer context:

| Agent Type | Needs | Source |
|-----------|-------|--------|
| Merge resolver | Conflict diff, resolution hints | `MergeResult.Conflicts`, `MergeConflictAnalysis` |
| Fixer | Build/test failure diagnosis, affected files | `BuildFailureDiagnosis`, `FeedbackIntegration` |
| Integrator | Parent task + acceptance criteria, all subtask descriptions, combined diff | Task model, subtask query, `git diff` |
| Reviewer | Plan JSON or combined diff, acceptance criteria | Task model, `git diff` |

The `prompt.Opts` struct needs extension to carry this additional context, or each agent type gets its own opts struct.

### State Machine Impact

No new states are needed. The new agents operate within existing transitions:

| Agent Type | Operates During | State Before | State After (success) | State After (failure) |
|-----------|----------------|-------------|----------------------|---------------------|
| Merge resolver | Subtask merge step | `in_progress` (subtask) | `done` (subtask) | `failed` (subtask) |
| Fixer | Feature merge or test rejection | `merging` or `testing_ready` | Retry merge or re-enter `testing_ready` | `failed` |
| Integrator | Post-subtask-merge | `in_progress` (parent, all subtasks done) | `testing_ready` | `failed` |
| Reviewer | Human gate states | `plan_review` or `testing_ready` | Same state (advisory) | Same state (ignored) |

### Cleanup

Merge resolver, fixer, and integrator don't create worktrees, so `RemoveAgentWorktree()` is not needed on completion. The agent DB record should still be cleaned up (status → idle, current_task_id → nil).

---

## Open Questions

1. **Merge resolver scope**: Should it handle only trivial/moderate conflicts (as assessed by the supervisor), or attempt all conflicts? Starting conservative (trivial/moderate only) avoids making things worse on complex conflicts.

2. **Fixer for test feedback**: When `testing_ready` is rejected with user feedback, should the fixer always be spawned, or only when the feedback is clearly a bug (vs. a design change)? The `FeedbackIntegration` analysis could include a `fixable` flag.

3. **Integrator trigger**: Should the integrator always run, or only when the plan says `needs_integration: true`? Running it always adds latency but catches gaps the planner missed. A compromise: run it, but with a short timeout (3 min) — if there's nothing to wire, it finishes quickly.

4. **Reviewer blocking**: Should `plan_review` wait for the reviewer to finish before showing the plan to the human? Or show the plan immediately and append the review when it completes? The latter is better UX — the human can start reading while the reviewer works.

5. **Cost**: Each new agent type spawns a Claude session. Merge resolver and fixer are short-lived (1-3 min), so cost is bounded. Integrator and reviewer could be longer. Should there be a cost budget per task that limits how many auxiliary agents can be spawned?
