# Capacity Health Verification - 2026-04-30

Owner: Kyle
Status: Slice 4 runbook drafted
Scope: read-only verification and controlled capacity canary requirements

## Decision

This is a standalone runbook for capacity health verification. It records the current baseline from stored artifacts and live supported checks, defines admissible snapshot evidence, and sets the requirements for any future controlled capacity canary.

This runbook intentionally does not edit `docs/containerization/install.md`.

## Current Baseline

Stored artifact baseline:

- `plans/t2-roundtrip-canary-2026-04-28.md` closes T2 roundtrip canary `caca7002-0002-4000-8000-000000000002` as end-to-end orchestrator pipeline success.
- T2 closure evidence includes terminal `done`, successful merger `merger-caca-0ce9`, merged SHA `cdad9dd27fce1908394398a0eb6ab94c09bfd9cd`, and service/world summary health OK.
- `plans/preflight-readiness-2026-04-28.md` clears only one scoped real-work task, not broad lifecycle cleanup or unconstrained launch activity.
- Known baseline noise remains: `backlog=1`, `in_progress=2`, historical failed/dead/archived workers, zero-UUID heartbeat attribution, stale history/log gaps, and failed task history.
- Historical worker totals are retention evidence, not live capacity. Do not confuse worker count history with currently running project worker capacity.

Live supported-surface check from this workspace on 2026-04-30:

- `./deploy/docker/context/dremctl --orch-url http://127.0.0.1:8080 --project drem-orchestrator status` reached the orchestrator and reported project health/status OK enough for read-only checks.
- Live status reported one registered project, `drem-orchestrator`, with `workers=0` in the project line.
- Live task counts were `tasks: 826 {backlog=1, done=774, failed=48, in_progress=2, rejected=1}`.
- Live worker counts were historical/retained: `workers: 3882 {archived=1134, completed=1, dead=1958, failed=30, finished=1, idle=756, terminated=2}`.
- `dremctl tasks --limit 30` showed `04b03b0c` as `done`, `0e79d985` and `772aad4b` as `in_progress` with `worker=-`, recent failed tasks, and T2-related tasks still terminal `done`.
- Recent events show the latest controlled task `04b03b0c-a8db-4c0d-b890-5da9b0b02325` reached `done` after worker churn and reconcile recovery: `in_progress -> failed` because the agent session died without commits, followed immediately by `failed -> done` with reason `reconcile-already-merged-to-default`.

## Read-Only Snapshot Evidence

Use only supported read-only surfaces for baseline snapshots unless the operator separately approves a deeper route.

Required evidence:

- `dremctl status` for project health, task counts, project worker count, historical worker count, and recent event timestamps.
- `dremctl tasks --limit N` for recent task IDs, statuses, titles, and assigned worker IDs.
- Focused task-status queries with `dremctl tasks --status backlog --limit N`, `dremctl tasks --status in_progress --limit N`, and `dremctl tasks --status failed --limit N`.
- `dremctl events --limit N` for recent state transitions and worker churn evidence.
- Exact task IDs for canary monitoring. Aggregate queue counts alone are not sufficient because known backlog, in-progress, failed, and dead-worker noise is part of the accepted baseline.

Snapshot helper:

```bash
DREM_ORCH_URL=http://127.0.0.1:8080 \
DREM_PROJECT=drem-orchestrator \
bash scripts/drem-capacity-snapshot.sh
```

Controlled canary runner, once available:

```bash
DREM_ORCH_URL=http://127.0.0.1:8080 \
DREM_PROJECT=drem-orchestrator \
DREM_CAPACITY_CANARY_CONFIRM=yes \
bash scripts/drem-capacity-canary.sh
```

`scripts/drem-capacity-canary.sh` is a mutating runner and must require `DREM_CAPACITY_CANARY_CONFIRM=yes` before it files any task. Its scope is limited to filing scoped canary tasks and observing those exact tasks through supported `dremctl` surfaces; it must not approve, retry, archive, restart, or clean up tasks/workers/services.

Focused `dremctl` checks:

```bash
./deploy/docker/context/dremctl --orch-url http://127.0.0.1:8080 --project drem-orchestrator status
./deploy/docker/context/dremctl --orch-url http://127.0.0.1:8080 --project drem-orchestrator tasks --limit 30
./deploy/docker/context/dremctl --orch-url http://127.0.0.1:8080 --project drem-orchestrator tasks --status backlog --limit 20
./deploy/docker/context/dremctl --orch-url http://127.0.0.1:8080 --project drem-orchestrator tasks --status in_progress --limit 20
./deploy/docker/context/dremctl --orch-url http://127.0.0.1:8080 --project drem-orchestrator tasks --status failed --limit 20
./deploy/docker/context/dremctl --orch-url http://127.0.0.1:8080 --project drem-orchestrator events --limit 50
```

## Artifact Registry Prerequisite

When artifact registry validation is available in the installed `drem` binary, run it before a controlled canary and treat validation errors as a prerequisite blocker for using registered artifacts as current authority.

Expected source-level CLI surface is `artifact-registry validate`, but the live `./drem` binary in this workspace returned `unknown subcommand: "artifact-registry"` on 2026-04-30. Until the installed binary exposes that command, record artifact registry validation as unavailable rather than claiming it passed.

Candidate command once available:

```bash
./drem cli artifact-registry validate
```

If validation is unavailable, the canary may still proceed only if the operator explicitly accepts repo-file artifacts plus live `dremctl` evidence as sufficient for that run.

## Controlled Capacity Canary Requirements

A capacity canary is not just a task count delta. It must prove that a precisely identified scoped task can be filed, observed through worker assignment/churn, and reach a terminal state without broad cleanup or unsupported recovery.

Prerequisites:

- Service health OK on supported `dremctl status`.
- Baseline snapshot captured before filing or observing the canary.
- Artifact registry validation passed when available, or an explicit operator note records that registry validation is unavailable and repo-file evidence is accepted for this canary only.
- Known baseline noise recorded: backlog/in-progress/failed counts, dead/archived worker retention, and systemic observability gaps.
- A supported filing surface is operator-cleared for the run, or exact pre-created task IDs are provided by the operator.

Task filing boundary:

- Do not claim there is a supported task creation command unless the active `dremctl --help` output exposes one in the same runtime where the canary will be filed.
- The deployed host `dremctl` currently exposes `create`, `create-task`, and `file-task`, but the active runtime for the canary must still be checked immediately before filing.
- If `dremctl` in the active runtime does not expose a creation command, capacity canary execution requires either an operator-cleared supported filing surface or pre-created exact task IDs.

Monitoring requirements:

- Record exact task ID, title, filing timestamp, and filing surface.
- Capture before/after `dremctl status`, `dremctl tasks --limit 30`, and `dremctl events --limit 50`.
- Watch exact task ID and fresh event timestamps, not aggregate queue deltas alone.
- Classify worker churn as live churn if the exact task receives replacement workers and fresh events continue.
- Treat terminal `done` after reconcile recovery as success only when events show a supported recovery reason such as `reconcile-already-merged-to-default` and the task remains terminal on `dremctl tasks`.
- Treat terminal `failed`, missing fresh events, repeated churn without terminal movement, or route/tooling failure as a canary blocker requiring operator review.

Confirm-gated runner:

```bash
DREM_ORCH_URL=http://127.0.0.1:8080 \
DREM_PROJECT=drem-orchestrator \
DREM_CAPACITY_CANARY_CONFIRM=yes \
bash scripts/drem-capacity-canary.sh --count 1 --timeout 20m --interval 15s
```

The runner is mutating because it files canary tasks. Its mutation scope is limited to `dremctl create --title ... --description ...`; after filing, it only polls `dremctl tasks` and `dremctl events` for the exact created IDs.

## Forbidden Actions

These actions are outside Slice 4 and require separate explicit approval:

- No `drem-sglang` restart.
- No broad `docker compose up`; scoped service recreation must use `--no-deps`.
- No raw DB route.
- No raw log route.
- No broad lifecycle cleanup.
- No broad archive/retry/pass/fail mutations to reduce baseline noise.
- No canary-runner approvals, retries, archives, restarts, or cleanup beyond filing and observing the scoped canary tasks.
- No legacy temp-worker/tmux route as a substitute for supported `dremctl` or operator-cleared surfaces.

## Acceptance Checklist

- Standalone runbook exists at `plans/capacity-health-verification-2026-04-30.md`.
- Snapshot helper exists at `scripts/drem-capacity-snapshot.sh` and uses read-only health/status checks.
- Canary runner exists at `scripts/drem-capacity-canary.sh`: mutating, confirm-gated, file-and-observe only.
- Current baseline cites stored artifacts and live checks.
- T2 roundtrip remains closed.
- Preflight clearance remains limited to one scoped real-work task.
- Historical worker count is explicitly separated from live capacity.
- Latest controlled task `04b03b0c` is recorded as `done` after worker churn and reconcile recovery.
- Capacity canary requirements avoid claiming unsupported task creation surfaces without checking the active runtime.
- Forbidden actions are explicit.
