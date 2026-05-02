# DREM Pipeline Reliability Policy

Status: proposed policy and implementation plan
Scope: pipeline failure-state semantics, canary/watchdog reporting, and staged reliability work

## Decision

DREM pipeline reliability should be judged by fresh, task-correlated evidence from supported surfaces, not by aggregate historical noise. Current canaries and watchdogs must track exact task IDs, current event timestamps, terminal parent outcomes, and normalized failure reasons. Historical contaminated tasks remain retained evidence for systemic follow-up, but they must not reopen closed lanes or poison current health unless the same failure recurs in a new supported watch window.

## Parent Failure Semantics

Standard parent tasks should use one terminal or blocked parent outcome per pipeline edge case, with a durable reason that distinguishes deterministic blockers from transient worker churn.

Proposed semantics:

- Child failure: parent moves to `failed` only when the child failure is terminal and unrecoverable or retry budget is exhausted. Reason: `child_failed` with child task ID, child role, worker/attempt ID, and last supported event timestamp. Transient child worker death may requeue or respawn without marking the parent terminal.
- Test-writing failure: parent moves to `failed` when the test-writer lane cannot produce required test artifacts after retry budget or preflight failure. Reason: `test_writing_failed`. Evidence must include test-writer child ID and artifact/log reference when available.
- Test-review rejection: parent returns to the appropriate implementation or test-writing lane when review rejection is actionable. Parent moves to `rejected` or `failed` only when the rejection is final, out of scope, or retry budget is exhausted. Reason: `test_review_rejected` with reviewer evidence and required remediation summary.
- `testing_ready` failure: parent moves to `failed` when gate execution cannot run or test gate fails after allowed remediation attempts. Reason: `testing_ready_failed` for route/tooling failure or `tests_failed` for actual test failure. Evidence must include command, exit code, and task-correlated output reference.
- Merger `tests_failed`: parent moves out of `merging` and into the remediation lane when merge-time tests fail and remediation budget remains. Parent moves to `failed` when remediation or merger retry budget is exhausted. Reason: `merger_tests_failed` with merger attempt ID, command, exit code, and test output reference.
- Merge conflict: parent moves to terminal `failed` or a future explicit `merge_blocked` state when conflict is deterministic after existing conflict-resolution attempts. Reason: `merge_conflict`. Reconciler must not resurrect the parent to `in_progress` or `testing_ready` after terminal conflict.
- Push rejected: parent remains in `merging` only while a bounded retry/preflight fix is available. Parent moves to `failed` when push rejection is deterministic or retry budget is exhausted. Reason: `push_rejected` or `push_failed_after_attempts`, with remote, ref, stderr, and merger attempt ID.
- Watchdog timeout: watchdog marks the exact watched task as `failed` or `stalled` when no fresh supported event or state transition appears before timeout. Reason: `watchdog_timeout`. Timeout evidence must include watch start, deadline, last observed status/event timestamp, and polling surface.
- Historical contaminated tasks: historical zero-UUID attribution, stale logs/history, dead workers, old failed tasks, and prior push/build/crash evidence remain systemic evidence only. They do not affect current health or reopen terminal canaries unless they recur on a fresh task ID inside an active canary/watchdog window.

## Current vs Historical Health Policy

Current health is based on fresh supported surfaces for the active project and exact task IDs:

- `dremctl status` for service reachability, project status, current task counts, and current project worker count.
- `dremctl tasks --limit N` and focused status queries for current task IDs, titles, states, and assigned workers.
- `dremctl events --limit N` for recent task-correlated state transitions and worker churn.
- Exact canary/watchdog task IDs, not aggregate queue deltas alone.

Historical health evidence is retained but separated:

- Aggregate failed/dead/archived worker counts are retention evidence unless tied to a current task and fresh event timestamp.
- Closed canaries remain closed when their parent task is terminal `done` on supported surfaces and no fresh recurrence appears.
- Historical contaminated tasks should be tagged as `historical_contamination` or equivalent in reports, with a link to the systemic follow-up lane rather than a current-health failure.
- A current health report may be `healthy_with_historical_noise` when supported surfaces are reachable and active canaries/watchdogs are clean, even if retained failed/dead worker totals are nonzero.

## Canary Tagging and Reporting Policy

Canaries must be precise enough to distinguish live reliability from background noise.

- Tag each canary with a stable title prefix such as `[canary:pipeline-reliability]`, the exact parent task ID, filing surface, filing timestamp, and operator-approved scope.
- Record the intended pipeline coverage: classification, planning, test writing, review, implementation, `testing_ready`, merger, push, and terminal parent state.
- Report only exact canary task state, fresh events, worker assignments, merger attempts, and terminal evidence as canary pass/fail evidence.
- Classify outcomes as `pass`, `fail`, `blocked`, or `inconclusive`. `inconclusive` is for unsupported surfaces, missing creation route, missing current events, or ambiguous stale evidence.
- Do not use canary runs to clean up backlog, archive failed tasks, retry unrelated work, restart services, or mutate lifecycle state beyond the scoped canary action.
- A canary passes only when the exact parent reaches terminal `done` with task-correlated evidence through the intended gate and merge path.
- A canary fails when the exact parent reaches a terminal failure state, the watchdog times out, or a deterministic blocker appears on the exact canary lane.

## Watchdog Plan

The watchdog should be a bounded observer first and a lifecycle mutator only when explicitly authorized by policy.

- Watch exact task IDs and expected parent/child edges, not global queue counts.
- Poll supported surfaces at a fixed interval and record last observed state, event timestamp, worker ID, and attempt ID.
- Treat fresh worker churn as liveness when the task continues to receive new task-correlated events.
- Treat no fresh task-correlated event before deadline as `watchdog_timeout`.
- Emit a concise report containing task ID, start time, deadline, last status, last event, classification, and recommended owner lane.
- Do not restart `drem-sglang`, do not run broad `docker compose up`, and do not use raw DB/log routes as normal watchdog recovery.
- Scoped service recreates, when separately approved, must use `docker compose up --no-deps <service>` or equivalent to avoid cascading through SGLang.

## Runtime Constraints

Reliability implementation and reviews must preserve current operational constraints:

- No `drem-sglang` restart as part of canary, watchdog, or reliability verification.
- No broad compose recreation. Scoped service recreates require `--no-deps`.
- The `orch` runtime currently needs Go, CGO, and Git identity available until task execution is moved out of the orchestrator runtime.
- Git identity should be command-local or runtime-local where possible; do not rely on global Git config as the correctness mechanism.
- Documentation and reports must distinguish supported surfaces from break-glass host/Docker/DB/log access.

## Staged Implementation Plan

Commit-sized slices:

1. Add failure-reason taxonomy and parent edge-case policy docs. Verification: doc review against known edge cases and existing T2/merger evidence.
2. Normalize parent terminal failure reasons for child failure, test-writing failure, test-review final rejection, `testing_ready` failure, merger test failure, merge conflict, push rejection, and watchdog timeout. Verification: targeted lifecycle/state-machine tests.
3. Add task-correlated merger and push failure evidence with attempt IDs, command, exit code, stderr/log reference, and parent task ID. Verification: induced conflict, push rejection, and no zero-UUID-only evidence tests.
4. Add watchdog exact-task observer and report format without broad lifecycle mutation. Verification: fake clock or controlled timeout test plus live read-only dry run against a known task.
5. Add canary tagging/reporting support and a confirm-gated canary runner if a supported filing surface is available. Verification: dry-run report, then one operator-approved scoped canary.
6. Split current-health reports from historical contamination reports. Verification: fixtures with retained failed/dead workers still report `healthy_with_historical_noise` when current canary/watchdog evidence is clean.
7. Move task execution dependencies out of `orch` runtime or explicitly package Go/CGO/Git identity until that move is complete. Verification: container/runtime preflight test proves required tools and identity are present before execution.

## Verification Strategy

Use layered verification so reliability work does not depend on manual archaeology.

- Unit tests for state transitions and failure reason normalization.
- Integration tests for parent/child lifecycle edges, merger conflicts, push rejection, and reconciler non-resurrection after terminal merge failure.
- Watchdog tests for liveness, timeout, and report classification.
- Canary dry-run that validates filing surface, tags, polling plan, and report path without creating tasks.
- One operator-approved scoped canary that files or watches exact task IDs and records before/after supported-surface evidence.
- Constitution and relevant package tests before merge readiness.

## Acceptance Criteria

- Every standard parent edge case has one proposed parent outcome and normalized reason.
- Current health and historical contamination are explicitly separated in reports.
- Canary reports are exact-task, tagged, and bounded to scoped action.
- Watchdog timeout behavior is task-correlated and does not authorize broad recovery by itself.
- Operational constraints around SGLang, scoped recreates, and current `orch` runtime dependencies are recorded for implementation and review.
