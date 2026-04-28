# Task Cancel/Archive Mutation Follow-Up

Opened: 2026-04-27
Source: Mike report in reply to operator corrid `0f4a2c91`

## Current State

Mike verified that `dremctl` and the orchestrator HTTP task routes support normal lifecycle actions only: `approve`, `reject`, `pass`, `fail`, `answer`, `retry`, and `comment`. There is no supported `archive`, `remove`, or `cancel` mutation for obsolete tasks.

The immediate obsolete plan-review cleanup is contained: the 17 IDs tracked in `orch-plans/drem-task-disposition.md` are terminal `failed`, there are no project workers assigned to them, and project workers are currently `0`. The least-risk operational rule is to avoid `retry`, `approve`, or any other action that revives those obsolete IDs.

## Follow-Up Need

Add a supported task `cancel` or `archive` mutation for obsolete non-running tasks. This should be an auditable no-spawn terminalization path, not a planner/spawner transition.

Acceptance criteria for the design:

- Exposed through both the orchestrator HTTP API and `dremctl`.
- Requires actor and reason, and emits an audit/status event with previous state and final state.
- Refuses or explicitly guards running/assigned work so it cannot kill active workers accidentally.
- Does not enqueue planner, coder, reviewer, merger, or retry work.
- Is idempotent for already-terminal tasks and safe for batch use on obsolete task sets.
- Preserves enough metadata for future audits while allowing obsolete tasks to be hidden or excluded from active operating views.

## Owner

Kyle routed the design/audit follow-up to Seth on 2026-04-27. Mike remains the ops owner for monitoring that the contained obsolete IDs are not revived.

## Implementation Routing - 2026-04-27T22:24Z

Mike reported that the current persona `dremctl` surface has no supported task-create or arbitrary cold-worker spawn mutation, so he cannot directly file the implementation task from his runtime. Kyle accepted the blocker, reinforced the accepted archive contract, and routed Alex to file/own the implementation request for the HTTP and CLI mutation path.

Current coordination split:

- Alex owns filing or otherwise initiating the implementation task for `POST /tasks/{id}/archive` and `dremctl archive <task-id-prefix> --reason TEXT [--actor TEXT]`.
- Mike owns monitoring that obsolete terminal IDs are not revived and that default active views continue to exclude cancelled/obsolete rows.
- Kyle owns operator synthesis and keeping this plan current until the implementation task exists or Alex reports a concrete blocker.

## Filing Attempt - 2026-04-27T22:28Z

Kyle attempted to file the implementation task from the container runtime after Alex reported that her persona container has no task-create surface.

Result: the implementation task was not created because no supported filing path was available from Kyle's runtime.

Checked surfaces:

- `dremctl --help` exposes status, tasks, workers, history, events, logs, gate mutations, retry, and comment only. It has no task-create/task-filing command.
- Raw orchestrator HTTP fallback at `http://orch:8080` returned 404 for `/`, `/healthz`, and `/openapi.json`; no create route surfaced.
- Host-exec `drem` is allowlisted, and the host worktree is present at `/home/godinj/git/drem-orchestrator.git/master`, but the old `drem cli` path is not usable as a safe filing path from this container turn. Without a temporary config it opened `./drem.db`/`./drem.log` relative to the host-exec daemon's read-only working directory; with absolute DB/log paths it timed out rather than returning a task-create help or task ID.

Current status: preserve the accepted archive/cancel implementation contract here, but do not treat the task as filed. Next required action is to expose or authorize a supported task filing surface for C-Suite, such as `dremctl file/create`, a documented HTTP `POST /projects/{project}/tasks` route, or an explicitly scoped operator-approved host-side filing command.

## Filing Surface Blocker Decision - 2026-04-27T22:35Z

Alex accepted that the archive-mutation implementation task does not exist yet and that this is not an archive-mutation scope problem. The immediate blocker is now the lack of a supported task-filing surface available from the containerized C-Suite runtime.

Product priority: Tier 3 -- Pipeline Blocker. The preferred unblock is a supported `dremctl file/create` command backed by an orchestrator HTTP create route, returning the created task ID. Acceptable alternatives are a documented HTTP create endpoint or an explicitly scoped operator-approved host-side filing command that returns a task ID.

Coordination state:

- Archive mutation implementation remains not filed and has no task ID.
- Mike should continue no-spawn/no-revival monitoring on the assumption that no archive-mutation implementation task exists.
- Kyle escalated the filing-surface blocker to the operator and routed Mike's monitoring instruction on 2026-04-27.

## Accepted Contract - 2026-04-27

Seth returned the recommended smallest contract in reply to operator thread `95d9d64e`, and Kyle accepted it as the implementation target.

- HTTP API: add `POST /tasks/{id}/archive` with JSON `{ "actor": "...", "reason": "...", "mode": "obsolete" }`. The operator-facing verb is `archive`, even if the canonical stored status remains `cancelled`.
- CLI: add `dremctl archive <task-id-prefix> --reason TEXT [--actor TEXT]`. Batch mode can follow later via `--file`; the first acceptable implementation can be single-task and script-loopable.
- Safety guard: allow only non-running, non-assigned work. The update must be atomic and guarded by `worker_id IS NULL` plus an allowed non-running status set. Assigned, leased, or active lifecycle statuses must be rejected.
- Audit: emit `status_change` or `task_archived` with actor, reason, old status, new status, previous worker ID, archived timestamp, and `obsolete=true`. This mutation must not enqueue planner, coder, reviewer, merger, or retry work.
- Idempotency: archiving an already archived/cancelled task succeeds with `changed=false`, never revives or reassigns work, and preserves existing history.
- Views: default active views (`dremctl tasks`, TUI active lanes, C-Suite summaries) should hide archived/cancelled obsolete tasks unless an explicit status, include-archived flag, or direct ID lookup requests them. Historical task and event detail remains queryable.

Implementation acceptance gates:

- Tests must prove HTTP, route, status, and CLI semantics align.
- Tests must cover the assignment race guard and refusal of running/assigned work.
- Visibility changes must hide obsolete work from active views without deleting rows or suppressing history/audit queries.
- Avoid the verb `remove`; it implies deletion and audit loss.

## Runtime Verification After Direct Repo Implementation - 2026-04-27T22:39Z

Operator reported direct repo implementation of the accepted archive mutation contract and successful package tests for `./internal/orchhttp`, `./pkg/orchclient`, and `./cmd/dremctl`.

Kyle verified the currently deployed C-Suite/orchestrator runtime surface and found the implementation is not yet visible there:

- `dremctl --help` still does not list `archive` or an `include_archived` task flag.
- `dremctl archive --help` returns `unknown command "archive"`.
- `POST http://orch:8080/projects/drem-orchestrator/tasks/00000000-0000-0000-0000-000000000000/archive` returns HTTP 404.
- `dremctl tasks --status cancelled --limit 20` does expose cancelled rows explicitly, and default `dremctl tasks --limit 80` continues to omit cancelled rows.

Decision: the repo-level contract appears implemented per operator report, but the live runtime contract is not resolved until the orchestrator/dremctl surface running in C-Suite is rebuilt/redeployed and exposes the archive endpoint and CLI command.

## Alex Filing-Surface Alignment - 2026-04-27T22:40Z

Alex acknowledged and aligned with the owner split: the immediate Tier 3 pipeline blocker is the supported task-filing surface, not archive-mutation scope. The archive-mutation implementation task remains absent until C-Suite has either `dremctl file/create` backed by orchestrator HTTP task creation, a documented HTTP create endpoint, or an explicitly operator-approved bounded host-side filing command that returns a real task ID synchronously.

Alex-owned filing requirements are now part of the blocker definition: create a real orchestrator task through a supported API path; return the created task ID synchronously; accept enough metadata for product triage such as title, body, category/lifecycle path, priority, and rationale; and avoid persona-container direct DB mutation or host-exec as the normal path.

Kyle runtime recheck at 2026-04-27T22:41Z still shows `dremctl` exposing only status/list/history/events/logs plus gate/lifecycle mutations, with no task-create or file command. Mike should report only material changes: a newly available supported filing path, an operator-approved bounded filing command, or unexpected revival/movement of obsolete tasks.

## Mike No-Movement Check - 2026-04-27T22:45Z

Mike completed a supported-surface read-only check after the latest ACK and found no unexpected archive-mutation movement: `dremctl status` is reachable, project workers remain `0`, there are no visible `planning`, `in_progress`, or `classifying` active tasks, and recent archive/mutation keyword hits are historical failed/cancelled work only. Current gates are unrelated (`caca7002` T2 roundtrip canary at `plan_review`, `56fa181f` reconciler optimistic concurrency at `test_review`). The archive-mutation implementation lane remains unfiled until a supported filing surface or explicitly scoped operator-approved filing command returns a real task ID.

## Runtime Verification After Operator Fixes - 2026-04-27T22:47Z

Operator reported direct implementation of both recent fixes and successful package tests for `./internal/orchestrator`, `./internal/orchhttp`, `./pkg/orchclient`, and `./cmd/dremctl`.

Kyle rechecked the live C-Suite/orchestrator runtime and found the archive contract is still not visible there:

- `dremctl --help` still does not list `archive`.
- `dremctl archive --help` still returns `unknown command "archive"`.
- `dremctl tasks --include-archived --limit 5` returns `flag provided but not defined: -include-archived`.
- `POST http://orch:8080/projects/drem-orchestrator/tasks/00000000-0000-0000-0000-000000000000/archive` still returns HTTP 404.
- Default `dremctl tasks --limit 80` still includes `cancelled` rows, so the active/default hiding behavior is not live.

Decision: do not call archive mutation finished until the deployed `orch`/`dremctl` runtime exposes the new endpoint, command, include-archived query behavior, and default cancelled-row hiding semantics.

## Final Live Runtime Verification - 2026-04-27T22:55Z

Operator rebuilt and redeployed the orchestrator plus refreshed C-Suite persona images. Kyle verified the live runtime from the container surface and accepts the archive mutation contract as live.

Evidence:

- `dremctl --help` now lists `tasks [--status STATUS] [--limit N] [--offset N] [--include-archived]` and `archive <task-id-prefix> --reason TEXT [--actor TEXT]`.
- `dremctl archive --help` reaches archive command parsing and returns `--reason is required`, not `unknown command`.
- `dremctl archive 00000000-0000-0000-0000-000000000000 --reason final-live-verify --actor kyle` reaches the orchestrator route and returns task-not-found through `orchclient`.
- `POST http://orch:8080/projects/drem-orchestrator/tasks/00000000-0000-0000-0000-000000000000/archive` returns JSON `{"error":"task not found"}` from the archive route, not a route-missing response.
- `dremctl status` now reports 849 tasks, consistent with default hiding of cancelled rows.
- `dremctl tasks --include-archived --limit 10` succeeds, default `dremctl tasks --limit 80` exposes no cancelled rows, and `dremctl tasks --status cancelled --limit 20` explicitly exposes cancelled rows.

Decision: archive/cancel tooling gap is closed in the live runtime. Cancelled/obsolete rows remain queryable by explicit request and hidden from active/default views.
