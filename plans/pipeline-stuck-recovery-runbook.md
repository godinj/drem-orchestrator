# Pipeline Stuck Recovery Runbook

Use supported dremctl/API surfaces first. Do not edit the project SQLite DB
directly unless the operator explicitly authorizes break-glass repair after the
supported surfaces fail.

## Detect

Run the supported health scan from the host or an operator shell with
`DREM_ORCH_URL` and `DREM_PROJECT` set:

```bash
dremctl health issues
dremctl --json health issues
```

The scan reports these stuck-pipeline classes when visible through the
orchestrator API:

- `stale_assigned_worker`: task has an assigned worker that is missing, dead,
  non-working, or has a stale heartbeat.
- `active_task_no_fresh_events`: active lifecycle state has no recent task
  event or update.
- `orphan_backlog_child`: backlog child is parked under a terminal parent.
- `planner_capacity_exhausted`: planner spawn cap has blocked progress.
- `missing_failure_evidence`: failed task has no supported failure evidence
  event for normal recovery triage.

For a single suspected assignment, classify without mutation:

```bash
dremctl recover stale-assignment <task-id-prefix> --dry-run
```

## Safe Recovery

Only clear stale assignments through the supported recovery command:

```bash
dremctl recover stale-assignment <task-id-prefix> --apply
```

The command refuses unsafe live assignments. A worker is treated as unsafe when
it is still `working` and has a fresh heartbeat. Safe cases include a missing
assigned worker row, `dead` worker, non-working worker, or stale heartbeat.

On apply, the orchestrator clears the task assignment, clears the worker's
`current_task_id` when applicable, writes a `stale_assignment_recovered` task
event, and appends an audit comment to the task.

Kyle's existing recovery command remains scoped to testing-ready failure retry:

```bash
dremctl kyle recover --dry-run
dremctl kyle recover --apply
```

Use `dremctl recover stale-assignment` for assignment repair rather than asking
Kyle to perform direct DB or host-level cleanup.

## Escalate

Escalate to the operator when:

- `--dry-run` reports a live working worker with fresh heartbeat.
- Health issues persist after supported stale-assignment recovery.
- The issue is `orphan_backlog_child`, `planner_capacity_exhausted`, or
  `missing_failure_evidence` and there is no supported targeted recovery
  command for the specific condition.
- Any suggested fix requires direct DB writes, Docker/container surgery, or
  lifecycle/scheduler internals.

## Break Glass

Direct SQLite writes are break-glass only. Before touching the DB, capture the
health JSON, task ID, worker ID, current task row, worker row, and the reason the
supported API could not repair the condition. Prefer adding a supported
orchestrator mutation over repeating manual DB repair.
