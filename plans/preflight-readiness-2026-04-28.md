# Preflight Readiness - 2026-04-28

Owner: Kyle
Status: confirmed for one scoped real-work task
Operator corrid: `preflight-cleanup-200359`
Updated: 2026-04-28T20:07:24Z

## Decision

CONFIRMED: proceed with one scoped real-work task.

## Supported Surface Snapshot

- Kyle world summary: drem-orchestrator health OK, zero running workers, no recent commits/merge-success/crashes in the summary window.
- `dremctl status`: tasks `backlog=1`, `done=773`, `failed=48`, `in_progress=2`, `rejected=1`; workers zero running with historical failed/dead/archived workers retained.
- `0e79d985` is `in_progress` with `worker=-` for Git identity / merge failure telemetry.
- `772aad4b` is `in_progress` with `worker=-` for worker-attempt observability.
- The only backlog item is `44d99768`, `Write test for non-constraint failure recovery`, with stale assigned worker `7eee5d97-1a7d-4cfe-b8ae-2564dab2927a`.

## Classifications

- `0e79d985`: active systemic parent lane, acceptable baseline, no cleanup or lifecycle mutation before the next scoped task.
- `772aad4b`: active systemic parent lane, acceptable baseline, no cleanup or lifecycle mutation before the next scoped task.
- `44d99768`: stale/non-running backlog debt. It can make aggregate backlog counts noisy, but it has not moved since April 17, its assigned worker lookup returns 404, and there are zero running workers. Kyle attempted the smallest supported cleanup, `dremctl archive 44d99768 --reason "preflight cleanup: stale sole backlog item outside current scoped readiness lanes" --actor kyle`; the orchestrator refused it because archive requires non-running unassigned work and this row still carries a stale assignment. Leave it alone for the first controlled task and track it as a future stale-assignment/archive cleanup gap.
- Recent internal-import and file-length ceiling failures are real current blockers for the `0e79d985`/`772aad4b` systemic acceptance lanes, but they are not a global blocker for one unrelated controlled task if the task is scoped and monitored by task ID.
- Zero-UUID and event-delivery noise remains observability debt, not a launch blocker, provided the first real-work task is monitored by explicit task ID, worker ID, and fresh event timestamps.

## Communication Baseline

- inbox: 1 current file message
- acks: 14
- outbox: 0 pending before Kyle's reply
- db_unread: 10 for Kyle, 47 across all agents
- event_unacked: 4134 for Kyle/all current unacked event deliveries

## Monitoring Rule For Next Task

Treat `backlog=1`, `in_progress=2`, `db_unread_kyle=10`, and `event_unacked=4134` as the known baseline. The next task should be monitored by its exact task ID and fresh events after launch, not by aggregate queue deltas alone.

## Live Cleanup Task Watch 2026-04-28T20:18:49Z

- Mike reported that scoped real-work task `04b03b0c-a8db-4c0d-b890-5da9b0b02325` remained `in_progress`; original worker `41d161ac-9e59-409f-8c97-63b3594376ed` was dead; replacement worker `aa318a12-6de4-45a9-98c8-fc6d3e1102a2` was working; and no terminal state or Mike-side mutation had occurred.
- Kyle rechecked supported surfaces after Mike's sample. `dremctl tasks --limit 10` still shows `04b03b0c` as `in_progress`, but worker assignment advanced again to `0df49d73-2418-4bbb-b4b5-164dc3fa73e0`; `dremctl worker 0df49d73-2418-4bbb-b4b5-164dc3fa73e0` shows it `working` on the same branch and task, started at `2026-04-28T20:18:32Z` with last heartbeat at `2026-04-28T20:18:32Z`.
- Recent events show repeated `quickfix-direct` `backlog -> in_progress` transitions and coder spawns for the same task at `20:15:34Z`, `20:16:52Z`, and `20:18:32Z`. The zero-UUID heartbeat attribution remains observability debt.
- Decision: treat this as live worker churn on the controlled documentation task, not a terminal result and not a clearance for lifecycle mutation. Mike owns bounded supported-surface watch until terminal `done` or `failed`, a fresh blocker, or another material worker-churn recurrence.
