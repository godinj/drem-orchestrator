## 2026-03-21T22:15 — on_demand_session

- **Task**: Replace one-shot classifier with classifier agent (d105c55a-eb63-4da1-a623-4a7ea3a288a5)
- **Problem**: The plan stored in the database was completely wrong — it contained 3 subtasks about "inline ctx display" (rendering context percentage in the TUI activity line), which is from a different task/branch. The correct 6-subtask plan for the classifier agent feature existed in plan.json on disk (committed in a9ca0bf) but was never synced to the DB. The task was in `plan_review` with the wrong plan, meaning human review would have approved an unrelated plan.
- **Actions Taken**: Used `sqlite3 readfile('plan.json')` to overwrite the task's `plan` column with the correct plan.json from disk. Verified the update by checking subtask count (6) and first subtask title matches.
- **Root Cause**: The planner agent committed the correct plan to plan.json on disk and the orchestrator transitioned the task to `plan_review`, but the plan stored in the database was stale/from a different task. This is likely a race condition or a bug where the orchestrator reads plan.json but the DB update uses a cached/previous version. Since plan.json was removed from git tracking (commit 4308b57 "stop tracking plan.json to prevent cross-branch merge conflicts"), the planner agent may have written it, the orchestrator read a stale copy, and then the disk file was updated — or the plan.json on disk belongs to the current branch but the DB plan was written from a different branch's context.
- **Suggested Improvement**: The orchestrator should validate that the plan it stores in the DB matches the plan.json on disk after transitioning to `plan_review`. A simple checksum comparison (or reading plan.json directly from the integration worktree at commit time) would catch this divergence. Additionally, when transitioning to plan_review, the orchestrator should log the first subtask title as a sanity check — "inline ctx display" would have been an obvious red flag for a task titled "Replace one-shot classifier."
- **Outcome**: DB plan now matches disk plan.json with all 6 correct subtasks. Task remains in `plan_review` for human approval of the correct plan.

---
## 2026-03-21T22:30 — on_demand_session (continued)

- **Task**: Replace one-shot classifier with classifier agent (d105c55a-eb63-4da1-a623-4a7ea3a288a5)
- **Problem**: All 6 subtasks stuck in `backlog` with no agents being spawned. Parent task was in `test_writing` status.
- **Actions Taken**: 
  1. Synced plan.json from disk to DB (previous intervention — plan was stale/wrong)
  2. Ran manual plan reviewer agent via `claude --print` which produced review.json recommending "revise" with 3 issues (user approved anyway)
  3. Diagnosed scheduling deadlock: `test_writing` calls `scheduleSubtasks(parent, "test")` which filters to test-phase only, but the wave schedule requires group 0 (implementation subtask "Add shared types") to complete first. Implementation subtasks are skipped by the phase filter, creating a deadlock where nothing can ever be scheduled.
  4. Transitioned parent from `test_writing` to `in_progress` via direct DB update.
- **Root Cause**: The `test_writing` state assumes test-phase subtasks can run independently, but this plan has test subtasks that depend on an implementation subtask (shared types). The wave schedule correctly puts the implementation subtask in group 0, but `test_writing`'s phase filter prevents it from being scheduled. This is a design conflict between the TDD-oriented `test_writing` state and plans where test subtasks have implementation dependencies.
- **Suggested Improvement**: When the orchestrator enters `test_writing`, it should check whether the wave schedule's current group contains any non-test subtasks. If it does, either: (a) allow scheduling of implementation subtasks that are dependencies of test subtasks, or (b) skip `test_writing` and go straight to `in_progress` for plans where test subtasks have cross-phase dependencies. Alternatively, the planner could be instructed to ensure test subtasks never depend on implementation subtasks when TDD ordering is desired.
- **Outcome**: Parent task transitioned to `in_progress`. Orchestrator should now schedule group 0 (shared types implementation) on next tick, unblocking the entire pipeline.

---
