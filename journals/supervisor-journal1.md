## 2026-03-07 — on_demand_session

- **Task**: Clip Lifecycle (9eb98010-2a7f-4d9e-9298-0bd0073ce2a5)
- **Problem**: Parent task was in `failed` state. Two subtasks ("Implement clip split" and "Implement ripple delete") were marked `failed`, and one integration tests subtask was still in `backlog`. However, the code from both failed subtasks was already successfully merged into the integration branch (commits 6658b7e, a4ced34, 823280c, 2ff62c4). The build passes cleanly. The failure occurred because the orchestrator retried the subtasks on Mar 7 and the new agent branches had merge conflicts with the already-merged work ("merge into feature branch failed, agent branch preserved").
- **Actions Taken**: User confirmed manual testing passed. Updated DB directly:
  - Set subtasks ea82f1bd and 94250a54 from `failed` to `done` (code already merged)
  - Set subtask 04630bea from `backlog` to `done` (manual testing confirmed passing)
  - Set parent task 9eb98010 from `failed` to `merging`
- **Root Cause**: The orchestrator retried failed subtasks even though their earlier work had already been merged into the integration branch. The new agents produced conflicting changes against their own previously merged work, causing merge failures. The orchestrator doesn't check whether a subtask's work is already present in the integration branch before retrying.
- **Suggested Improvement**: Before retrying a failed subtask, the orchestrator should check if the subtask's prior agent branch was already merged into the integration branch. If so, it should either mark the subtask as done or ensure the new agent starts from the current integration HEAD (not re-do already-merged work).
- **Outcome**: Parent task set to `merging`. All subtasks marked `done`. Build passes. Orchestrator will pick up the merge on next tick.

---
