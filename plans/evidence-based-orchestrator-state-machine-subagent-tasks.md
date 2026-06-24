# Plan: Evidence-Based Orchestrator State Machine Subagent Tasks

> Source PRD: `docs/prd-evidence-based-orchestrator-state-machine.md`

This plan breaks the PRD into subagent-sized implementation briefs. Each task should be implemented in its own worktree or branch unless the operator directs otherwise. Subagents should preserve existing prior art around `WorkerAttempt`, spawn reservation, attempt-scoped Docker events, stale recovery, scheduler gating, and health reporting instead of replacing it wholesale.

## Durable Architectural Decisions

- **Execution truth**: `WorkerAttempt` is the canonical execution record. `Task.AssignedAgentID` remains only a pointer to the current attempt's agent.
- **Attempt states**: support `reserved`, `running`, `completed`, `failed`, `aborted`, and `superseded` as explicit lifecycle states.
- **Active lease invariant**: at most one active attempt exists for a given task, role, and branch.
- **Evidence envelope**: every guarded transition and recovery mutation records structured evidence with task ID, attempt ID when relevant, actor, source, timestamp, normalized reason, and references to logs, branch refs, gate reports, or test output.
- **Evidence precedence**: current-attempt evidence outranks stale-attempt evidence; container exit evidence outranks heartbeat freshness for the same attempt; audited operator recovery outranks automated retry but must remain compare-and-swap guarded.
- **Branch gate**: worker completion is not accepted until branch hygiene and expected-base checks pass.
- **Parent readiness**: parent status is derived from all children, their dependencies, and required gates. Phase-local checks must not move a parent past blocked children.
- **Recovery surface**: direct SQLite repair becomes break-glass only; supported API and `dremctl` commands handle known stuck states.
- **Verification constraint**: do not restart `drem-sglang`; do not run broad `docker compose up` without `--no-deps`.

## Suggested Wave Order

- **Wave 1**: Tasks 1, 2, and 3 establish contracts and attempt lifecycle.
- **Wave 2**: Tasks 4, 5, 6, and 7 can proceed after Wave 1 interfaces stabilize.
- **Wave 3**: Tasks 8, 9, 10, and 11 expose recovery, observability, and UI/API surfaces.
- **Wave 4**: Task 12 ties the feature together with cascade replay and docs updates.

## Implementation Status

- **Wave 1 complete in working tree**: Task 1 added the guarded evidence transition contract, Task 2 completed branch-aware `WorkerAttempt` lifecycle and active lease uniqueness, and Task 3 made exit-zero Docker events actionable completion evidence for the current attempt.
- **Task 4 complete in working tree**: branch preflight and branch acceptance now run through a narrow `internal/branchpolicy` module. Worker spawns preflight branch metadata before reservation/container launch, and worker completion must pass branch acceptance before fast-tracking to `done` and marking the attempt completed.
- **Known Task 4 follow-up**: persisted attempt-start base SHA is not implemented yet; branch acceptance uses the current default branch comparison plus optional `estimated_files` scope.

---

## Task 1: Evidence Contract And Guarded Transitions

**Subagent brief**: Define the evidence envelope and a small guarded transition module that wraps task status changes. Keep guard logic pure where possible and integrate without a broad orchestration rewrite.

**User stories covered**: 1, 19, 20, 35, 40.

**Likely seams**: `internal/state`, `internal/model`, `internal/orchestrator`, `internal/orchhttp`.

**What to build**

Create a transition API that accepts current task state, target state, actor, observed task version or equivalent freshness check, and evidence. The API should validate status legality, required evidence fields, and optional attempt matching before producing the task mutation and `TaskEvent` details. Existing `state.TransitionTask` call sites can be migrated incrementally, starting with worker completion, Docker death, recovery, parent readiness, and gate mutations.

**Acceptance criteria**

- [ ] A canonical evidence type exists and is used in transition event details for newly migrated transitions.
- [ ] Guard functions are unit-testable without Docker, Git, or HTTP setup.
- [ ] Missing required evidence refuses the transition with a reason that can be shown through API/CLI surfaces.
- [ ] Stale observed-version or stale assignment mutations return a conflict instead of overwriting newer state.
- [ ] Existing valid transitions still pass current lifecycle tests.

**Verification**

- Run `go test ./internal/state ./internal/orchestrator ./internal/orchhttp`.

---

## Task 2: Complete WorkerAttempt Lifecycle And Lease Invariant

**Subagent brief**: Finish attempt states and enforce one active lease per task, role, and branch using `WorkerAttempt` rather than scattered agent/task fields.

**User stories covered**: 4, 7, 8, 9, 26, 27, 44.

**Likely seams**: `internal/model`, `internal/db`, `internal/workeridentity`, `internal/orchestrator/worker_spawn.go`, `internal/orchestrator/docker_events.go`.

**What to build**

Extend the attempt model with the full lifecycle and branch-aware active uniqueness. Replace implicit stale closure with explicit `superseded` or `aborted` semantics. Ensure reservation, spawn finalization, spawn failure, Docker death, and recovery classify the exact current attempt before mutating task assignment or attempt state.

**Acceptance criteria**

- [ ] Attempt constants include `reserved`, `running`, `completed`, `failed`, `aborted`, and `superseded`.
- [ ] The active-attempt uniqueness invariant includes task, role, and branch.
- [ ] Reservation refuses or explicitly supersedes active current attempts before replacement spawn.
- [ ] Stale attempts cannot clear the current task assignment.
- [ ] Migration/backfill leaves existing project databases usable.

**Verification**

- Run `go test ./internal/workeridentity ./internal/db ./internal/orchestrator`.

---

## Task 3: Positive Worker Completion Path

**Subagent brief**: Make successful worker completion an explicit evidence path from watchdog push, `DONE`, final metadata, and exit-zero reconciliation through branch gate and task transition.

**User stories covered**: 2, 22, 23, 28.

**Likely seams**: `cmd/drem-watchdog`, `internal/watchdog`, `internal/orchestrator/docker_events.go`, `internal/orchestrator/agent_results.go`, `internal/workeridentity`.

**What to build**

Emit a positive completion signal after a successful watchdog push and synthesize canonical completion evidence. Treat exit-zero Docker events as actionable evidence for the matching current attempt. The completion path must mark the attempt completed only after branch hygiene accepts the diff and the task reaches the right next state.

**Acceptance criteria**

- [ ] A successful watchdog push emits completion evidence with attempt and branch identifiers.
- [ ] Exit-zero for the current attempt triggers completion reconciliation rather than becoming a no-op.
- [ ] Exit-zero for stale attempts records ignored evidence and does not mutate current assignment.
- [ ] Attempt state changes to `completed` only after branch acceptance passes.
- [ ] Failed completion synthesis records a normalized reason on the attempt or task evidence.

**Verification**

- Run `go test ./internal/watchdog ./internal/orchestrator`.

---

## Task 4: Branch Preflight And Branch Acceptance Deep Module

**Subagent brief**: Create a narrow branch policy module for preflight and acceptance checks, then route worker, reviewer, fixer, and merger completion through it.

**User stories covered**: 13, 14, 21, 24, 30, 31, 32, 43.

**Likely seams**: `internal/gitexec`, `internal/gitref`, `internal/orchestrator/worker_spawn.go`, `internal/orchestrator/agent_results.go`, `internal/orchestrator/merge_execution.go`, `internal/orchestrator/constraint_gate_policy.go`.

**What to build**

Implement a deep module with a small interface for branch preflight and branch acceptance. Preflight validates refs, reflogs, ownership, writeability, remote pushability, and expected base ancestry before spawn. Acceptance compares against the correct base branch and rejects worker traces, prompt artifacts, plan artifacts, credentials/config deletions, unrelated deletions, and files outside accepted scope unless explicitly allowed.

**Acceptance criteria**

- [ ] Spawn preflight fails before launching a worker when branch refs or reflogs are not writeable.
- [ ] Preflight returns normalized `branch_permission` or `branch_ownership` reasons instead of generic git errors.
- [ ] Completion acceptance rejects worker trace files, prompt edits, plan artifacts, credentials/config changes, and unrelated deletions.
- [ ] Accepted evidence packets include accepted diffs and rejected artifacts.
- [ ] The module can be unit-tested with temporary git repositories.

**Verification**

- Run `go test ./internal/gitexec ./internal/gitref ./internal/orchestrator`.

---

## Task 5: Docker Exit And Heartbeat Evidence Precedence

**Subagent brief**: Reconcile container exit truth ahead of heartbeat freshness and make death/recovery matching attempt-scoped end to end.

**User stories covered**: 2, 3, 4, 26, 27, 28, 29.

**Likely seams**: `internal/orchestrator/docker_events.go`, `internal/orchhttp/health_recovery.go`, `internal/orchestrator/reconcile.go`, `internal/container`.

**What to build**

Update Docker event handling and stale-assignment classification so known container exit for the current attempt outranks fresh heartbeat state. Nonzero exits should include normalized model/tool/infra failure details. Docker events from stale attempts should remain visible as evidence but must not mutate the current assignment.

**Acceptance criteria**

- [ ] Current-attempt exit-zero completion is processed promptly even when the agent heartbeat is fresh.
- [ ] Current-attempt nonzero exit records normalized failure evidence and respects retry budgets.
- [ ] Stale Docker events do not mutate current assignment, task status, or active attempt.
- [ ] Stale-assignment recovery classifies the current attempt rather than only the assigned agent row.
- [ ] Health and recovery refusal messages state whether the blocker is active worker, missing evidence, or safety policy.

**Verification**

- Run `go test ./internal/orchestrator ./internal/orchhttp ./internal/container`.

---

## Task 6: Retry Budgets And Failure Reason Normalization

**Subagent brief**: Record retry limits per task edge and normalize worker/model/tool failures so retry storms stop as actionable failures.

**User stories covered**: 10, 25, 29, 39.

**Likely seams**: `internal/orchestrator/docker_events.go`, `internal/orchestrator/agent_results.go`, `internal/orchestrator/plan_client.go`, `internal/orchhttp/public_read_model.go`, `cmd/dremctl`.

**What to build**

Introduce a retry budget keyed by task, transition edge, and failure class. Normalize reasons such as model truncation, tool loop, infra timeout, branch permission, branch contamination, and test failure. Public task DTOs and health issues should show current failure evidence for supported failed states without reporting false missing-evidence noise.

**Acceptance criteria**

- [ ] Repeated model/tool failures exhaust a per-edge retry budget and fail with actionable evidence.
- [ ] Failure classifications are machine-readable and bounded in public DTOs.
- [ ] `dremctl health issues` separates current failures from historical noise.
- [ ] Failed tasks with supported evidence no longer appear as `missing_failure_evidence`.
- [ ] Retry budget state is persisted or otherwise durable across orchestrator restarts.

**Verification**

- Run `go test ./internal/orchestrator ./internal/orchhttp ./cmd/dremctl`.

---

## Task 7: Parent Readiness And Mixed-Phase Dependency Guard

**Subagent brief**: Replace phase-local parent advancement with derived readiness over all children and detect unschedulable mixed-phase dependencies before approval.

**User stories covered**: 16, 17, 18, 34, 35, 42.

**Likely seams**: `internal/orchestrator/test_writing.go`, `internal/orchestrator/task_processing.go`, `internal/orchestrator/scheduler.go`, planner/reviewer prompts and validation.

**What to build**

Create a parent-readiness function that evaluates every child, dependency edge, phase, and required gate before parent transitions to `test_review`, `testing_ready`, `merging`, or `done`. Add plan/scheduler validation for mixed test/implementation dependencies. Either explicitly schedule required implementation work during `test_writing` when supported, or reject/block the plan with actionable evidence when the TDD phase model cannot execute it.

**Acceptance criteria**

- [ ] A parent cannot enter `test_review` while required children remain backlog, blocked, in progress, failed, or dependency-blocked.
- [ ] Blocked child dependencies are visible on parent evidence.
- [ ] Plans with impossible phase/dependency ordering are rejected or marked blocked before approval.
- [ ] Mixed-phase scheduling behavior is explicit and tested.
- [ ] Existing valid TDD flows still progress.

**Verification**

- Run `go test ./internal/orchestrator ./internal/prompt ./cmd/drem-planner`.

---

## Task 8: Blocked-State Evidence And Event Validation

**Subagent brief**: Make scheduler blocked evidence stateful and harden event ingestion so malformed or uncorrelated events cannot break read APIs or pollute health reports.

**User stories covered**: 36, 37, 38.

**Likely seams**: `internal/orchestrator/scheduler.go`, `internal/orchestrator/task_processing.go`, `internal/model`, `internal/orchhttp`, `internal/agentmon`.

**What to build**

Replace repeated tick-local blocked events with durable blocked-state evidence that is updated only when blocker sets change. Validate event details before persistence. Malformed JSON or zero-UUID events should be rejected or quarantined and remain visible for diagnostics without breaking `/events`.

**Acceptance criteria**

- [ ] Unchanged blocker sets do not emit duplicate events every tick.
- [ ] Changed blocker sets update durable evidence and emit a single meaningful event.
- [ ] Malformed event details cannot break `GET /events`.
- [ ] Zero-UUID task events are rejected or quarantined with a clear diagnostic.
- [ ] Health reports ignore quarantined uncorrelated failures unless explicitly requested.

**Verification**

- Run `go test ./internal/orchestrator ./internal/orchhttp ./internal/agentmon`.

---

## Task 9: Supported Recovery APIs And CLI Commands

**Subagent brief**: Add supported recovery surfaces for the break-glass repairs named in the PRD, with audit comments and compare-and-swap safety.

**User stories covered**: 5, 6, 15, 33, 40.

**Likely seams**: `internal/orchhttp/health_recovery.go`, `internal/orchhttp/gate_handlers.go`, `pkg/orchclient`, `cmd/dremctl`, `internal/orchestrator`.

**What to build**

Extend recovery APIs and CLI commands for exited-container-with-fresh-heartbeat, contaminated branch reset, duplicate active attempts, stuck parent phase, and accepted-child-work adoption. Every apply path should be audited with a task comment or event and guarded by observed task version or equivalent current-state check.

**Acceptance criteria**

- [ ] Dry-run recovery explains why it would apply or refuse.
- [ ] Apply recovery records an audit event and task comment with actor, policy, evidence, action, and result.
- [ ] Recovery refuses stale observed state with conflict instead of mutating.
- [ ] Existing stale-assignment recovery continues to work under the new attempt-scoped classification.
- [ ] Raw DB edits used in the motivating run have supported API equivalents.

**Verification**

- Run `go test ./internal/orchhttp ./pkg/orchclient ./cmd/dremctl`.

---

## Task 10: Public API, TUI, And Health Visibility

**Subagent brief**: Expose active attempts, leases, blocked dependencies, and branch hygiene failures through supported operator surfaces.

**User stories covered**: 11, 12, 39, 41, 42, 43.

**Likely seams**: `pkg/orchdto`, `internal/orchhttp/public_read_model.go`, `internal/orchhttp/health_recovery.go`, `cmd/dremctl`, TUI packages.

**What to build**

Add public projections for active attempt/lease state, duplicate-worker diagnostics with task, role, branch, and attempt IDs, blocked parent dependencies, and branch hygiene gate failures. Keep current failures separate from historical noise in health outputs.

**Acceptance criteria**

- [ ] Task DTOs include enough current-attempt and lease data to tell running from merely assigned.
- [ ] Duplicate-worker detection reports task, role, branch, and attempt IDs.
- [ ] Parent DTO or health issue output includes blocked child dependency details.
- [ ] Branch hygiene failures appear as gate failures, not generic build errors.
- [ ] TUI and `dremctl` render the new fields without raw DB inspection.

**Verification**

- Run `go test ./pkg/orchdto ./internal/orchhttp ./cmd/dremctl` plus the relevant TUI package tests after locating the TUI package.

---

## Task 11: Scheduler And Spawn Race Integration Tests

**Status**: Implemented in current working tree. Coverage extends existing worker identity, worker spawn, Docker event, branch preflight, event validation, and recovery/API health suites. Focused tests cover concurrent active-attempt uniqueness, duplicate spawn refusal before second container launch, stale exit-zero immunity, exit-zero completion through branch acceptance, branch permission preflight, parent blocked readiness DTOs, event validation/quarantine, and recovery API refusal/apply DTOs.

**Subagent brief**: Add focused tests for concurrent scheduling, Docker death races, duplicate spawn prevention, and branch preflight failure paths.

**User stories covered**: 8, 9, 26, 31, 45.

**Likely seams**: `internal/orchestrator/*_test.go`, `internal/workeridentity/*_test.go`, `internal/projects/bare_repo_test.go`, `internal/container/fake.go`.

**What to build**

Build the regression suite that proves active attempt uniqueness, stale event immunity, no overlapping containers on the same branch, branch permission preflight, and exit-zero completion reconciliation. Extend existing lifecycle, worker spawn, Docker event, stale recovery, scheduling, test-writing, test-review, and reconciliation tests rather than creating a separate harness unless a new pure transition package needs isolated tests.

**Acceptance criteria**

- [x] Concurrent scheduling cannot create two active attempts for the same task, role, and branch.
- [x] Docker death races do not mutate a newer current attempt.
- [x] Duplicate worker overlap is refused or supersedes the prior attempt before spawn.
- [x] Root-owned or non-writeable refs fail preflight before worker launch.
- [x] Exit-zero completion advances through branch hygiene without stale-assignment recovery.

**Verification**

- Run `go test ./internal/orchestrator ./internal/workeridentity ./internal/projects`.

---

## Task 12: Drem-Canvas Cascade Replay And Documentation

**Status**: Implemented in current working tree. `TestDremCanvasCascadeReplaySurfacesEvidenceThroughAPIs` models the motivating cascade without real Docker by seeding supported task/attempt/event/comment state through existing fake HTTP/orchestrator utilities. The replay asserts task DTOs, active attempt leases, attempt history, health issues, recovery DTOs, events, comments, and branch acceptance evidence.

**Subagent brief**: Add an end-to-end regression replay for the observed `drem-canvas` cascade and update docs so the new evidence model is operator-understandable.

**User stories covered**: 1 through 45 as integration acceptance, with emphasis on 2, 3, 8, 13, 17, 30, 36, 45.

**Likely seams**: integration test packages, `docs/`, `plans/`, operator runbooks.

**What to build**

Create a replay that models exited-zero worker, stale assignment refusal, duplicate respawns, branch contamination, cross-phase dependency blockage, and failed implementation attempt. The test should assert externally visible state-machine behavior: task status, attempt lifecycle, events, comments, branch refs, and API responses. Update operator docs for evidence precedence, supported recovery commands, branch acceptance policy, and attempt lifecycle.

**Acceptance criteria**

- [x] The replay fails against old behavior and passes with the evidence-backed state machine.
- [x] The replay asserts supported surfaces rather than direct SQLite-only state.
- [x] Docs explain how to inspect transition evidence, active attempts, recovery refusals, and branch hygiene failures.
- [x] Docs include migration/backfill notes for existing project databases.
- [x] The PRD and this task plan remain consistent with implemented behavior.

**Verification**

- Run the new replay test and `go test ./...` if feasible in the current environment.

## Cross-Task Coordination Rules

- Do not change Claude auth to token or API-key based flows.
- Do not restart `drem-sglang` during implementation or verification.
- Do not use broad Docker Compose restarts as a reliability fix.
- Preserve existing behavior until the new guarded path covers it with tests.
- Every subagent should leave task evidence understandable through API/CLI, not only through logs.
- Every subagent should update relevant docs or this plan when implementation changes the contract.
