# Operator Approval Authority - 2026-04-27

Operator corrid `fc435562` authorized direct approval movement for the current canary/orchestrator path: Mike may approve whatever he sees fit, and Kyle may approve whatever Kyle sees fit.

Scope and constraints:

- Applies to `dremctl` gate/task movement for the containerized orchestrator/cold-worker canary path.
- Does not authorize secrets disclosure, destructive git or Docker commands, force push, credential changes, or restarting sglang.
- Do not route current canary work through legacy tmux temp-worker flows.

Actions already taken:

- `4e1318b3` approved from `plan_review` to `test_writing`, then from `test_review` to `in_progress`.
- `3ddba802` approved from `plan_review` to `test_writing`, then from `test_review` to `in_progress`.
- Follow-on implementation workers observed active for V16 (`0cb31fa9`) and V15 (`14c2705e`).
