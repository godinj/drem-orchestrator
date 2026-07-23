# PRD: Evidence-Based Orchestrator State Machine

## Problem Statement

The orchestrator has received multiple rounds of state-machine hardening, but real pipeline runs still fail through combinations of worker lifecycle drift, branch contamination, premature parent transitions, and recovery gaps. The recurring issue is broader than any one edge case: task state is not consistently derived from explicit, trusted evidence.

From the operator's perspective, this makes Drem unreliable at exactly the point where it should reduce supervision. A task can have an exited-zero container while remaining `in_progress`; a fresh heartbeat can outrank Docker's exited-container truth; a parent can move to a review gate while dependency-blocked children remain; multiple replacement workers can overlap on the same branch; and branch artifacts can be accepted as useful work. When this happens, the operator has to use Docker, Git, and SQLite directly to stop retry loops and repair state.

Prior hardening improved specific failure modes such as spawn reservation, attempt-scoped Docker death events, reconciler completion detection, and stale-assignment recovery. Those fixes are necessary but not sufficient because the state machine still lacks a single evidence contract that every transition must satisfy.

## Solution

Reframe orchestration around evidence-backed attempts and explicit transition contracts. A task should move only when the orchestrator has durable evidence for that transition, and recovery should reconcile conflicting evidence according to a documented precedence order.

The core product change is to make worker attempts the unit of execution truth. Each active task has at most one current attempt for a role and branch. Worker lifecycle signals, container exit state, watchdog pushes, branch hygiene checks, test results, merge results, and parent child-state aggregation produce structured evidence. The state machine consumes that evidence through guarded transitions instead of inferring progress from stale task fields or partial absence checks.

The operator should be able to trust supported surfaces (`dremctl`, HTTP API, TUI, and task comments/events) to explain why a task is running, blocked, failed, retrying, or done without inspecting raw Docker, Git, or SQLite state.

## User Stories

1. As the operator, I want every task transition to cite the evidence that allowed it, so that I can understand why the orchestrator moved the task.
2. As the operator, I want a worker container exit to be reconciled promptly, so that exited-zero workers do not leave tasks stuck `in_progress`.
3. As the operator, I want Docker container state to outrank heartbeat freshness when a container has exited, so that recovery does not refuse a known-dead worker.
4. As the operator, I want stale-assignment recovery to classify the exact current worker attempt, so that recovery does not race with a new assignment.
5. As the operator, I want supported recovery commands for stuck execution states, so that direct SQLite edits are not required.
6. As the operator, I want recovery commands to explain why they refuse, so that I know whether the blocker is safety policy, missing evidence, or an active worker.
7. As the operator, I want worker attempts to have a clear lifecycle, so that `reserved`, `running`, `completed`, `failed`, `aborted`, and `superseded` are not inferred from scattered fields.
8. As the operator, I want one active worker lease per task and role, so that overlapping workers cannot write to the same task branch.
9. As the operator, I want stale workers to be superseded before replacements start, so that replacement attempts cannot collide with previous pushes.
10. As the operator, I want retry limits to be recorded per task edge, so that repeated model/tool failures stop as actionable failures instead of retry storms.
11. As Mike, I want health reports to separate current failures from historical noise, so that operations work focuses on active risk.
12. As Mike, I want duplicate-worker detection to show the task, role, branch, and attempt IDs, so that I can diagnose overlap quickly.
13. As Mike, I want branch contamination to be reported before review gates, so that unrelated artifacts do not reach human review.
14. As Mike, I want branch ownership and permission problems to be surfaced as first-class failures, so that push rejections are not misclassified as generic build errors.
15. As Mike, I want recovery to create audit comments automatically, so that later reviewers can see what was repaired and why.
16. As Alex, I want plan review to reject plans with impossible phase/dependency ordering, so that implementation subtasks are not blocked behind test-only scheduling.
17. As Alex, I want parent task readiness to be derived from all children, so that a parent cannot enter `test_review` while children remain backlog, blocked, or in progress.
18. As Alex, I want the planner to be warned when test subtasks depend on implementation subtasks in a way the scheduler cannot execute, so that the plan is fixed before approval.
19. As Seth, I want a small transition engine with explicit guards, so that state-machine behavior can be reviewed independently from Docker and Git plumbing.
20. As Seth, I want transition guards to be pure and testable where possible, so that edge cases do not require full container integration tests.
21. As Seth, I want branch acceptance to be a deep module with a narrow interface, so that artifact policy is enforced consistently for worker, reviewer, fixer, and merger outputs.
22. As a worker agent, I want a clear completion contract, so that `DONE`, pushed commits, and final result metadata are converted into one canonical completion event.
23. As a worker agent, I want the watchdog to emit a positive completion signal after successful push, so that the orchestrator does not rely only on death/staleness reconciliation.
24. As a reviewer agent, I want evidence packets to include accepted diffs and rejected artifacts, so that review decisions do not require branch archaeology.
25. As a fixer agent, I want failed attempts to include normalized failure reasons, so that retries address the real blocker instead of repeating the same failure.
26. As the orchestrator, I want container lifecycle events matched to worker attempts, so that stale Docker events cannot mutate the current assignment.
27. As the orchestrator, I want heartbeat ingestion scoped to the current attempt, so that stale containers cannot keep a task artificially fresh.
28. As the orchestrator, I want exit-zero events to be actionable evidence, so that normal worker completion flows through branch hygiene and completion synthesis.
29. As the orchestrator, I want nonzero exits to include model/tool failure details, so that retry budgets can distinguish transient infra from bad agent behavior.
30. As the orchestrator, I want branch hygiene checks before accepting completion, so that `agent-trace` files, prompt changes, credentials changes, plan artifacts, and unrelated deletions are rejected.
31. As the orchestrator, I want branch refs and reflogs preflighted for ownership/writeability before spawning a worker, so that push failures are caught before work starts.
32. As the orchestrator, I want branch acceptance to compare against the correct base branch, so that parent and child branch contamination is not inherited silently.
33. As the orchestrator, I want child work adoption to be a supported transition, so that useful preserved worker commits can be accepted without raw DB repair.
34. As the orchestrator, I want parent status to be recomputed from child states and dependency graph evidence, so that parent gates are not advanced by phase-local checks.
35. As the orchestrator, I want `test_writing`, `in_progress`, `test_review`, `testing_ready`, and `merging` to have explicit entry and exit criteria, so that ambiguous mixed-phase plans fail fast.
36. As the orchestrator, I want scheduler blocked events to be rate-limited or stateful, so that repeated identical blocked events do not flood the event stream.
37. As the orchestrator, I want event details to be schema-validated before insertion, so that malformed JSON cannot break `/events`.
38. As the orchestrator, I want zero-UUID events to be rejected or quarantined, so that health reports are not polluted by uncorrelated failures.
39. As the API client, I want failed tasks to include current failure evidence, so that `dremctl health issues` does not report missing evidence for supported failures.
40. As the API client, I want task status mutations to be conditional on the observed task version, so that manual recovery and orchestrator ticks do not race.
41. As the TUI user, I want active attempts and leases visible on the task, so that I can tell whether a task is genuinely running or only assigned.
42. As the TUI user, I want blocked child dependencies visible on the parent, so that a stuck parent has an actionable explanation.
43. As the TUI user, I want branch hygiene failures visible as gate failures, so that source contamination is not hidden behind generic build errors.
44. As a future maintainer, I want migration/backfill rules for existing agents and attempts, so that current project databases remain usable after the redesign.
45. As a future maintainer, I want integration tests that replay the observed failure cascade, so that the state machine cannot regress to overlapping workers and contaminated branches.

## Implementation Decisions

- Treat `WorkerAttempt` as the canonical execution record for worker lifecycle, not just auxiliary audit metadata.
- Add or complete explicit attempt states for reserved, running, completed, failed, aborted, and superseded attempts.
- Enforce a database-level invariant that prevents more than one active attempt for the same task, role, and branch.
- Keep task assignment as a pointer to the current attempt's agent, but require transition guards to verify the assignment still matches the current active attempt.
- Define an evidence envelope for state transitions. Evidence should include task ID, attempt ID when relevant, actor, source, timestamp, normalized reason, and references to logs, branch refs, test output, or gate reports.
- Define a lifecycle evidence precedence order. Container exit evidence should outrank heartbeat freshness for the same attempt. Current-attempt evidence should outrank stale attempt evidence. Explicit operator recovery should outrank automated retries but must remain audited.
- Make worker completion a positive path. A successful worker should produce explicit completion evidence after pushing, and exit-zero should trigger completion reconciliation rather than becoming a no-op.
- Make branch acceptance an explicit gate before a worker attempt is treated as completed. The gate should reject worker traces, prompt artifacts, plan artifacts, credentials/config deletions, unrelated source deletions, and files outside the task's accepted scope unless explicitly allowed.
- Add a branch preflight before worker spawn that verifies branch refs, reflogs, ownership, writeability, remote pushability, and expected base ancestry.
- Make parent readiness a derived function over all child tasks, their dependency graph, and required gates. Parent transition to review or done must require every required child to be terminal in the right state.
- Make mixed-phase dependency handling explicit. Either schedule implementation dependencies during `test_writing` when required by the plan, or fail/reject plans whose dependency graph cannot be executed by the TDD phase model.
- Replace repeated tick-local blocked events with durable blocked-state evidence that is updated only when the blocker set changes.
- Harden event ingestion so structured event fields are validated before persistence and malformed events are quarantined instead of breaking read APIs.
- Extend recovery APIs to cover exited-container-with-fresh-heartbeat, contaminated branch reset, duplicate active attempts, stuck parent phase, and accepted-child-work adoption.
- Use guarded compare-and-swap mutations for recovery and gate transitions so a stale operator/API action cannot overwrite a newer orchestrator assignment.
- Keep raw DB repair as break-glass only. Every manual repair performed during the observed run should have a supported API equivalent after this work.
- Preserve existing spawn reservation and attempt-scoped Docker event work, but audit them against the new evidence contract because the run showed remaining gaps after those hardening waves.
- Avoid restarting `drem-sglang` or using broad Docker Compose restarts as part of this redesign or its verification.

## Testing Decisions

- Tests should assert externally visible state-machine behavior: task status, attempt lifecycle, events, comments, branch refs, and API responses.
- Tests should avoid coupling to internal helper names where possible. The transition guard module can have direct unit tests because it should be a deep, pure module.
- Add tests for one active attempt per task/role/branch under concurrent scheduling and Docker death races.
- Add tests where a worker container exits zero and the task advances through branch hygiene and completion synthesis without waiting for stale-assignment recovery.
- Add tests where a worker exits one after a fresh heartbeat and recovery treats container exit as authoritative for that attempt.
- Add tests for stale Docker events from old attempts proving they do not mutate the current assigned attempt.
- Add tests for duplicate worker overlap proving the second attempt is refused or supersedes the first before spawn, with no overlapping containers on the same branch.
- Add tests for root-owned or non-writeable bare branch refs proving spawn preflight fails before worker launch with a normalized branch-permission reason.
- Add tests for branch hygiene rejecting worker trace files, prompt edits, plan artifacts, and unrelated deletions before completion acceptance.
- Add tests for clean branch acceptance proving intended task files pass and artifact-only diffs fail.
- Add tests for parent readiness with blocked backlog children proving parent cannot enter `test_review` prematurely.
- Add tests for mixed-phase dependency plans proving the scheduler either dispatches required implementation work or marks the plan/schedule blocked with actionable evidence.
- Add tests for blocked-event rate limiting proving unchanged blocker sets do not emit duplicate events every tick.
- Add tests for event JSON validation proving malformed details cannot break `/events`.
- Add tests for supported recovery APIs replacing the break-glass operations used in the observed run.
- Add an integration replay for the observed `drem-canvas` cascade: exited-zero worker, stale assignment refusal, duplicate respawns, branch contamination, cross-phase dependency blockage, and failed implementation attempt.
- Existing prior-art tests live around lifecycle, worker spawn, Docker events, stale recovery, scheduling, test writing, test review, and reconciliation. New tests should extend those suites rather than create a parallel harness unless a new transition-engine package needs isolated tests.

## Operator Contract

### Evidence Precedence

- Current attempt evidence wins over stale attempt evidence. Docker or watchdog records must match the task's current assigned agent/attempt before they can mutate task state.
- Container exit evidence wins over heartbeat freshness for the same current attempt. A fresh heartbeat cannot keep an already-exited container alive for recovery classification.
- Exit-zero is positive completion evidence, not a no-op. It triggers completion synthesis, branch acceptance, attempt completion, and task fast-track only when the event matches the current attempt.
- Explicit operator recovery wins over automated retry only through supported, audited APIs. Unsafe or stale recovery requests must refuse with a DTO explaining `active worker`, `missing evidence`, or `safety policy`.

### Supported Recovery Surfaces

- `POST /projects/{project}/tasks/{task}/recover/stale-assignment` classifies or clears stale assigned workers. Dry-run is the default diagnostic path; apply clears only evidence-safe stale assignments.
- `POST /projects/{project}/tasks/{task}/recover/contaminated-branch-fail-gate` marks a task failed when branch hygiene evidence shows contaminated worker output. This replaces direct DB edits for branch-contamination gates.
- `POST /projects/{project}/tasks/{task}/recovery-audit` records Kyle/operator recovery decisions with policy, evidence, action, result, and whether the path was supported or break-glass.
- Unsupported recovery classes return refusal DTOs instead of mutating state. Duplicate active attempts and stuck parent phases are surfaced through `/health/issues`; only allowlisted mutation endpoints may repair them.

### Branch Acceptance Policy

- Worker spawn preflights branch metadata before reservation/container launch. Non-writeable or incorrectly owned branch refs/reflogs fail before any worker starts.
- Worker completion must pass branch acceptance before the attempt becomes `completed` and before the task becomes `done`.
- Branch acceptance compares the feature worktree against the configured base branch and optional `estimated_files` scope. Accepted evidence is stored as `branch_acceptance_accepted`; rejected evidence is stored as `branch_acceptance_rejected` and the active attempt is failed.
- Rejections cover worker traces, prompt artifacts, plan artifacts, credentials/config edits, unrelated deletions, and files outside the accepted task scope unless a future task explicitly expands the allowlist.

### Attempt Lifecycle

- `reserved`: identity row exists and the task is claimed before container launch.
- `running`: the container launched and the attempt has a concrete container ID.
- `completed`: the current attempt exited zero, completion synthesis succeeded, and branch acceptance passed.
- `failed`: the current attempt failed through nonzero exit, branch acceptance rejection, or retry-budget failure.
- `aborted`: reservation or spawn finalization failed before the attempt became usable.
- `superseded`: a stale unassigned active attempt was closed before a replacement could reserve the same task, role, and branch.

### Migration And Backfill Notes

- Existing project databases remain usable when `WorkerAttempt` rows are missing because public attempt history can still project legacy `worker_spawned` events and agent rows.
- New databases and migrated test databases must carry the partial unique index `idx_worker_attempt_active_task_role_branch` on `(task_id, agent_type, branch) WHERE completed_at IS NULL`.
- Backfill should create or infer attempt rows only when a historical spawn has a clear task, role, worker/container identity, and branch. Ambiguous historical records should remain event-only rather than creating fake active leases.
- Pre-existing active duplicates are health issues, not silent migrations. Operators should inspect `/health/issues`, archive/retry through supported endpoints where available, and avoid raw DB repair except as documented break-glass.

## Out of Scope

- Rewriting all task orchestration as a full event-sourced system in one pass.
- Replacing the current database or Docker runtime abstraction.
- Changing model providers, SGLang configuration, or GQ behavior except where model/tool failure evidence is normalized.
- Solving planner quality broadly beyond detecting unschedulable phase/dependency graphs.
- Building a new UI. TUI/API visibility improvements are limited to exposing the new evidence and recovery state.
- Bulk cleanup of historical failed/dead workers except where required for migration safety.
- Restarting core services as a reliability fix.

## Further Notes

This PRD intentionally consolidates lessons from prior state-machine hardening plans rather than replacing them. Several earlier plans were directionally correct: spawn reservation, attempt-scoped Docker events, watchdog completion signals, stale-recovery TOCTOU fixes, and current-vs-historical health separation. The new requirement is to make those pieces part of one transition contract so they cannot disagree silently.

The `drem-canvas` run that motivated this PRD produced these acceptance-driving examples:

- `dremctl logs` failed because the orchestrator log endpoint could not reach Docker.
- An exited-zero worker remained `in_progress` long enough to trigger retries.
- Stale-assignment recovery refused repair because heartbeat freshness outranked known container exit.
- Multiple replacement workers overlapped on the same subtask branch.
- Worker branches accumulated traces, prompt/doc edits, `plan.json` deletion, and unrelated test deletions.
- A root-owned bare branch ref/reflog caused push failures and blocked branch cleanup.
- Parent `test_writing` moved to `test_review` while dependency-blocked children remained backlog.
- A mixed test/implementation dependency graph required manual phase intervention.
- A worker model/tool loop hit max-token truncation and produced branch contamination.
- A red-test worker replaced a shared CMake manifest with C++ test text and a later retry invented headers instead of exercising the planned seam.
- One failed parallel child immediately cancelled an independent sibling after that sibling had already paid for and produced a useful model checkpoint.
- A killed worker without a terminal summary left zero token telemetry even though several paid responses had completed.

The first implementation slice should be small and measurable: define the evidence contract, enforce active-attempt uniqueness, and make exit-zero completion reconciliation pass branch hygiene before advancing a subtask. Subsequent slices can add parent readiness, mixed-phase scheduling, recovery APIs, and event validation.
