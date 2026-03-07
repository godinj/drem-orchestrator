# Reconcile Overhaul

**Status:** Disabled (pending redesign)

The `Reconcile` function in `internal/orchestrator/orchestrator.go` has been disabled
in both the periodic tick and the TUI keybinding (`X`). The existing implementation
remains in the codebase but is not called.

## Current Sub-functions

1. **reconcileStaleSubtasks** — Resets DONE subtasks to BACKLOG when the parent
   feature branch has no file changes.
2. **reconcileOrphanedSubtasks** — Recovers IN_PROGRESS subtasks whose agent is
   idle/dead by merging leftover work and fast-tracking to DONE, or resetting to
   BACKLOG.
3. **reconcileEmptyFeatures** — Fails TESTING_READY parent tasks whose feature
   branch has no changes.
4. **reconcileOrphanWorktrees** — Removes agent worktrees with no commits and no
   active agent.

## Why It Was Disabled

The function needs a design review before being re-enabled. Areas to revisit:

- Determine which checks are still valuable vs. causing unintended side effects.
- Evaluate whether the "no file changes" heuristic is too aggressive (e.g. subtasks
  that only modify existing lines may not be detected correctly).
- Consider whether orphaned subtask fast-tracking should require explicit confirmation
  rather than running automatically.
- Review the interaction between reconcile and the merge pipeline to avoid race
  conditions.

## Re-enabling

1. Revisit and overhaul the sub-functions listed above.
2. Uncomment the periodic call in `orchestrator.go` `doTick`.
3. Restore the `handleReconcile()` call for the `X` keybinding in `tui/app.go`.
