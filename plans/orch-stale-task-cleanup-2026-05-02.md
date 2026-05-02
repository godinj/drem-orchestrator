# Orchestrator Stale Task Cleanup - 2026-05-02

Status: complete
Owner: Kyle
Delegate: Mike
Correlation ID: orch-stale-cleanup-2026-05-02

## Scope

Clean up stale, pre-fix DREM orchestrator pipeline rows that still appear in Kyle attention or in-flight counts after the successful standard parent proof on `2e75708a-1f93-4d51-a83c-2585a4dfbf5c`.

## Baseline

Kyle world summary at 2026-05-02T14:01Z reported `health: OK` but still showed historical attention from stale tasks:

- `39536776-49aa-4670-8e12-b1962b8ede8b` - `testing_ready`, contaminated by pre-fix runtime test gate evidence.
- `c2caa6ef-1978-41c2-a253-512318410924` - `testing_ready`, contaminated by pre-fix validation canary evidence.
- `1f9bd806-66f3-441f-84d4-006fcf201306` - `paused`, capacity canary duplicate.
- `8e54eae8-8235-429d-a585-b29453784aac` - `paused`, capacity canary duplicate.
- `daf89cd9-aa1f-4ebf-bec3-f7e3f4451b14` - `paused`, capacity canary duplicate.
- `d72fe085-b7bd-4762-8dc6-9bcc2032bfa6` - `paused`, obsolete capacity metadata attempt.
- `c5f5d7c4-8df9-4e60-91f5-66ef8152bf74` - `paused`, obsolete capacity metadata attempt.
- `44d99768-5b55-4f83-9d3b-7858581a6a2c` - stale assigned backlog row from April 17.
- `0e79d985-0d32-419f-97c2-371cda0ea87c` - stale `in_progress` row, current failure flag false.
- `772aad4b-50fa-46f5-9d2a-8e4c4a835fa2` - stale `in_progress` row, current failure flag false.

## Delegation

Kyle delegates operational cleanup evidence collection to Mike. Mike must exercise the legacy temp-worker workflow once for this bounded cleanup, with the worker limited to read-only inspection and a written report. The operator-approved cleanup mutations remain Kyle/Mike directed and must be recorded here.

## Cleanup Rules

- Prefer supported `dremctl archive` for stale unassigned `paused`, `failed`, `rejected`, backlog, planning, clarification, plan-review, and test-review rows.
- Do not retry, pass, or otherwise revive contaminated canary rows.
- If a stale row is not archiveable through the supported endpoint because it is `testing_ready`, `in_progress`, or assigned to a dead worker, record the unsupported-surface gap before any break-glass disposition.
- Break-glass disposition may only set stale rows to `cancelled` with `task_archived` audit events and must not delete task, worker, event, or comment history.

## Acceptance

- Mike's temp worker report exists under `~/.drem-csuite/temp-workers/` or Mike's inbox/outbox evidence.
- Stale rows above no longer appear in active `dremctl tasks` output except when `--include-archived` is requested.
- Kyle `/world/summary` reports `health: OK` with no stale-task attention from the cleaned rows.
- Artifact registry seed metadata includes this cleanup record.

## Outcome

Completed at 2026-05-02T14:10Z.

- Kyle delegated cleanup to Mike via `~/.drem-csuite/mike/inbox/20260502-140511-kyle.md`.
- Mike exercised the legacy temp-worker workflow with `worker-orch-cleanup-20260502`.
- The worker completed with `Status: DONE` in `~/.drem-csuite/temp-workers/worker-orch-cleanup-20260502/state.md`.
- The worker reported back to Mike at `~/.drem-csuite/mike/inbox/20260502-141002-worker-orch-cleanup-20260502.md`.
- Supported `dremctl archive` dispositioned all unassigned stale `paused` and `failed` rows.
- Break-glass disposition marked stale `testing_ready`, `in_progress`, assigned `backlog`, assigned `rejected`, and assigned `failed` rows as `cancelled` with `task_archived` audit events. No task, worker, event, or comment history was deleted.
- `go run ./cmd/dremctl --orch-url http://127.0.0.1:8080 --project drem-orchestrator status` reported `tasks: 795 {done=795}` after cleanup.
- SQLite status counts after cleanup were `done=795` and `cancelled=378`.
- Kyle `/world/summary` at 2026-05-02T14:09:21Z reported `tasks: 0 in-flight` and `health: OK`.
- Follow-up verification at 2026-05-02T14:33Z found `39536776-49aa-4670-8e12-b1962b8ede8b` and `c2caa6ef-1978-41c2-a253-512318410924` back in `testing_ready` because their dead agent rows still referenced them. Mike repeated the audited break-glass cancellation and cleared `current_task_id` from dead agents `0477bd52-79e6-46e2-ad26-e9b78d6d70f4` and `a7823be1-79d0-40b7-952b-72c7b1e15bec` without changing agent history.
- Final verification at 2026-05-02T14:35Z showed `dremctl status` reporting `tasks: 795 {done=795}`, DB counts `done=795` and `cancelled=378`, and Kyle `/world/summary` reporting `tasks: 0 in-flight` with `health: OK`.

Kyle accepts the cleanup as done because the live Kyle summary no longer reports stale task attention or in-flight rows, and the successful standard proof task `2e75708a-1f93-4d51-a83c-2585a4dfbf5c` remains `done` with merge commit `ff981df1cfa6d561ba0fed7a9b27df147e8085fe`.

## Follow-Up Notes

- `drem cli stats` without an explicit live config reads a different/default database view in this workspace and disagreed with Kyle plus the live project DB. Use `dremctl --orch-url http://127.0.0.1:8080 --project drem-orchestrator status` or the active project config for live operational verification.
- The legacy temp-worker environment did not have `dremctl` on `PATH`; the worker used the repository-local `drem` binary and direct live DB reads as fallback evidence. This is an ops-surface consistency gap, not a blocker for this cleanup.
- Follow-up resolution: `scripts/csuite-spawn-worker.sh` now creates repo-local `dremctl` and `drem` wrappers in each legacy temp worker's `bin/` directory and prepends that directory to the launched harness `PATH`. The temp-worker prompt now directs live cleanup verification to `dremctl --orch-url ... --project ...` and warns against bare `drem cli stats`.
- Follow-up resolution: `drem cli stats` now warns when it is invoked without an explicit `--config` while using the implicit `./drem.db` fallback, so live operators are pointed at `dremctl ... status` or `drem cli --config <active project config> stats` before trusting the output.
- Kyle still reports `30 failed` workers. This remains historical worker-retention noise under `plans/drem-pipeline-reliability-policy.md` because there are `0 running` workers, no live task references, and `health: OK`.
