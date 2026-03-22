## 2026-03-21T18:30 — on_demand_session

- **Task**: del-plan (823e4f12-22b4-4791-932e-1e379f7e1f8f)
- **Problem**: Task was stuck in `planning` status. The planner had produced a 5-subtask plan to create a `RemoveEphemeralArtifacts` module, but the depth review scored it 0% ("architecturally trivial, should be 3-5 lines per call site"), and the user's feedback said "ephemeral files shouldn't be committed or tracked." The plan addressed the symptom (removing plan.json at merge time) rather than the root cause (plan.json being committed in the first place).
- **Actions Taken**:
  1. Analyzed the codebase: confirmed plan.json IS tracked in existing feature worktrees (verified via `git ls-files` in feature/467c28f1), and git history shows past merge conflicts (`eb2afde Merge master into ctrl-c-tui feature (resolve plan.json conflict)`).
  2. Identified root cause: `HandlePlanApproved()` in `handlers.go` writes plan.json and commits it via `CommitUnstagedChanges`. Once tracked, `.gitignore` no longer prevents it from being staged, leading to cross-branch conflicts.
  3. Implemented the fix directly (4 files changed):
     - **`internal/worktree/git.go`**: Added `UntrackEphemeralFiles()` — checks if plan.json is tracked via `git ls-files`, runs `git rm --cached` to untrack it while keeping the file on disk, and commits the removal (~20 lines).
     - **`internal/orchestrator/handlers.go`**: Changed `HandlePlanApproved()` to write plan.json to disk as an untracked file (removed the `CommitUnstagedChanges` call, replaced with `UntrackEphemeralFiles` to handle legacy tracked state).
     - **`internal/merge/merge.go`**: Added `UntrackEphemeralFiles` calls in both `MergeAgentIntoFeature` (after auto-commit, before merge) and `MergeFeatureIntoMain` (before merge, on the feature worktree) as safety nets.
     - **Tests**: Added 3 unit tests for `UntrackEphemeralFiles` (tracked, untracked, absent) in `git_test.go`. Updated `TestHandlePlanApproved_CommitsPlanJSON` → `TestHandlePlanApproved_WritesPlanJSONUntracked` to verify plan.json is written to disk but NOT tracked. Added `TestHandlePlanApproved_UntracksLegacyPlanJSON` for the migration path.
  4. Full test suite passes (`go test ./...` — all green).
  5. Marked subtasks as done and parent task as `testing_ready`.
- **Root Cause**: The planner over-engineered the decomposition (5 subtasks for ~30 lines of actual change) and addressed the symptom (remove at merge time) rather than the root cause (stop committing plan.json). The depth review caught this but the replanning loop didn't converge on the simpler approach.
- **Suggested Improvement**: When the depth review scores a plan 0% and says "implement directly as part of existing functions," the orchestrator should either (a) auto-reject and replan with the depth feedback as a hard constraint, or (b) flag for supervisor intervention instead of proceeding to `plan_review`. The current flow let a trivially-scored plan sit waiting for human approval.
- **Outcome**: Changes implemented and tested on master. Task moved to `testing_ready`. The fix prevents plan.json from being committed to git (root cause) and adds safety-net untracking at both merge paths (defense in depth).

---
