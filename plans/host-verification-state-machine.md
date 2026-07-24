# Host-authoritative delivery, verification, and Codex iteration state machine

Status: the exact-SHA artifact, verification, authorization, merge-preflight,
typed Cubase task ingestion, and actor-owned host-rework loop are complete. A
non-integrating Canvas delivery canary also passed. The preliminary gate
consumes typed branch acceptance, runs in a disposable exact-SHA worktree,
records typed pass/failure evidence (including setup, ref-validation, checkout,
and cleanup failures), and never launches a generic fixer.
Computer Use results are retained per acceptance criterion and every host edit
returns through a new commit, gate run, artifact version, and verification.
Mutation attribution and replay are uniform across task creation, legacy gates,
comments/audits, archive, recovery apply, delivery verification/rework, and
integration.
Merge completion no longer depends on telemetry: an immutable intent is
created before `merging`, and reconciliation proves the accepted artifact is
contained in the authoritative target ref before it writes `done`.

This specification also defines two local Codex responsibilities that the
original protocol left implicit: turning Cubase observations into durable
tasks, and performing repeated Computer Use verification/tweak cycles without
bypassing orchestration. The read-only remote-inference canary remains
externally blocked: on 2026-07-22 no loopback tunnel was listening at
`127.0.0.1:18090`. A read-only remote audit found GQ metrics healthy but
`/v1/models` returning 502, with no SGLang listener/container upstream. The
canary is now an executable repository-free probe with deterministic JSON
evidence. A live remote-inference Canvas writer remains disabled until it
passes.

## Problem

The current task lifecycle assumes that the orchestrator can both verify and
integrate work on its own host:

```text
in_progress -> testing_ready -> merging -> done
```

That sequence is ambiguous in the current implementation:

- `testing_ready` is classified as a human-only gate, but
  `processTestingReady` runs it as an automated action state.
- The automated gate and the manual `pass` command both transition directly
  to `merging`.
- `merging` is the first state that mutates the default branch, so there is no
  durable state for a prepared branch that must be verified elsewhere.
- `done` means both "work completed" and "accepted commit merged into the
  default branch."
- Test, branch, and merge evidence is primarily stored in `Task.Context` and
  event detail maps rather than a typed delivery contract.

For Canvas, container workers can perform preliminary checks, but the exact
application must be built and exercised natively on the Mac. The remote GPU
host supplies inference only and must never own Git refs.

## Lifecycle contract

Keep the existing early planning and implementation phases. Replace the final
ambiguous segment with explicit delivery phases:

```text
in_progress
  -> testing_ready
  -> verification_ready
  -> host_rework -> testing_ready  # optional local Codex repair loop
  -> integration_ready
  -> merging
  -> done
```

| State | Owner | Entry evidence | Exit condition |
| --- | --- | --- | --- |
| `testing_ready` | deterministic gate runner | an accepted branch/head/base candidate exists | disposable exact-SHA preliminary gates pass and a delivery artifact is frozen |
| `verification_ready` | Codex verifier/operator | immutable branch, commit, base, and preliminary-gate record | native verification for that exact commit passes, or rework is requested |
| `host_rework` | one identified local Codex task | current artifact was invalidated and a host-rework session owns the branch | an exact replacement commit is submitted to `testing_ready`, or the task is returned to orchestrated rework/cancelled |
| `integration_ready` | orchestrator | accepted verification record referencing the current delivery artifact | integration preflight confirms the artifact is still current and the target is safe |
| `merging` | merger | exact accepted artifact and target preflight | target branch contains the accepted change, or integration fails |
| `done` | none | merged commit and accepted artifact are durably linked | terminal |

State classification becomes unambiguous:

- `testing_ready` and `merging` are always actionable states.
- `integration_ready` is policy-actionable: `auto_merge` advances after a
  successful exact-SHA preflight, while `prepare_branch` remains parked until
  an explicit integration authorization is recorded.
- `verification_ready` is an external-evidence state, not a human gate. An
  authorized Codex task normally owns Computer Use verification and submits
  evidence through the supported CLI.
- `host_rework` is deliberately non-actionable to the scheduler. It prevents a
  container worker from racing a local Codex task that is making a bounded
  verification-driven repair.
- `done` is terminal and means integration completed, not merely that a worker
  stopped or a feature branch exists.

## Rework and invalidation

- Preliminary gate code failure: `testing_ready -> in_progress` with a typed
  gate run and normalized failure class. It does not automatically spawn a
  generic fixer.
- Preliminary gate infrastructure, configuration, or timeout failure:
  `testing_ready -> paused` with the typed gate run retained. An explicit retry
  returns to `testing_ready`; no model tokens are spent on runner failures.
- Native verification failure or requested changes:
  invalidate the delivery artifact and retain the failed verification record,
  then take one explicitly selected route:
  - `verification_ready -> in_progress` for orchestrated rework; or
  - `verification_ready -> host_rework` for a bounded repair owned by the
    current local Codex task.
- Rework requested after verification but before integration follows the same
  explicit routing from `integration_ready`.
- A host repair submission records the new exact branch commit and returns
  `host_rework -> testing_ready`. It never edits an existing artifact in place;
  successful gates freeze the replacement as the next artifact version.
- Archiving parked `verification_ready` or `integration_ready` work transitions
  it to `cancelled` and invalidates the current artifact in the same transaction.
- Target branch drift after acceptance: `integration_ready -> in_progress`
  with reason `target_drift_requires_reverification`; never merge an artifact
  verified against a different base silently.
- Merge failure: `merging -> failed` under the existing typed merger failure
  policy.
- Any new commit on the delivery branch invalidates prior verification and
  acceptance. SHA equality is mandatory; branch-name equality is insufficient.

Quick fixes may skip planning and TDD review as they do today, but they must not
skip `verification_ready` when the project verification policy requires an
external/native acknowledgement.

## Typed evidence

Do not add more stringly typed keys to `Task.Context` for this protocol.

Add a delivery artifact record with at least:

- task ID and monotonically increasing artifact version;
- branch and exact commit SHA;
- base branch and exact base SHA;
- preliminary gate commands, results, and timestamps;
- invalidation reason and timestamp;
- creator actor/source.

Add an append-only preliminary gate run with at least:

- task ID, accepted branch/head/base, and isolated workspace identity;
- exact commands, exit codes, bounded output, and timestamps;
- closed result/failure class (`pass`, `code`, `infra`, `configuration`,
  `timeout`, or `cancelled`);
- runner environment fingerprint and actor/source.

The gate runner materializes a fresh detached worktree directly from the bare
repository at the accepted head SHA. It never resets, cleans, or executes in a
long-lived integration, main, or user-owned worktree. The artifact uses the
base SHA captured by branch acceptance, not whatever the default branch happens
to name when the gate finishes; default-branch drift is therefore visible and
forces reverification instead of being silently absorbed.

Promote successful and rejected branch acceptance into append-only typed
records before the gate runner consumes it. `Task.Context["branch_acceptance"]`
is a compatibility mirror during migration, not delivery authority. The typed
record identifies the worker attempt, branch, accepted head, accepted base,
admitted paths, rejection details, actor/source, and observation time.

Record the feature branch's creation SHA on the task when its integration
worktree is created. This is distinct from the artifact's later base SHA: it
lets recovery prove that a feature branch advanced before treating an ancestor
relationship as out-of-band integration. Legacy tasks without this provenance
fail closed instead of guessing whether an equal branch was empty or merged.

Add a verification record with at least:

- delivery artifact ID and exact commit SHA;
- verifier identity and environment fingerprint;
- commands and structured results;
- exact application binary SHA-256 when a binary is produced;
- pass/fail result, notes, and timestamp.

Computer Use verification also records structured interaction evidence:

- acceptance-criterion ID and scenario name;
- ordered input/action steps and observed result;
- local screenshot/video artifact IDs with content hashes, not mutable paths;
- Canvas binary SHA-256, application version, host environment, and run PID;
- per-criterion pass/fail plus any discovered discrepancy.

Add an integration authorization record with the artifact and verification
record IDs, exact commit/base SHAs, authorizing actor/source, idempotency key,
and timestamp. This is required even for `auto_merge`; policy changes who
creates the authorization, not whether it is auditable.

Explicit rework decisions and merge completion are also typed records. Rework
records retain the actor, reason, exact artifact, and idempotency contract.
Merge completion links the artifact, passing verification, integration
authorization, verified base SHA, and resulting merge commit in the same
transaction that advances the task to `done`.

Merger completion must be recoverable from authoritative Git state plus the
typed merge intent. The `/internal/logs`/Agentmon reporting path is telemetry,
not a correctness dependency: losing a success report after the default ref
advances must not strand the task in `merging` or prevent creation of the typed
completion record.

Add a host-rework session record with the prior artifact/SHA, actor, reason,
allowed scope, idempotency key, start time, and terminal disposition. A
successful submission adds the replacement commit SHA and proves the canonical
feature ref names it. At most one active host-rework session and no active
worker attempt may exist for a task.

The transition event should reference these record IDs. It should not duplicate
their payload into an unvalidated event map.

## Project policy

Use explicit project policy instead of inferring behavior from operating
system, endpoint URL, or repository name:

```toml
[delivery]
integration_policy = "prepare_branch" # or "auto_merge"
verification_policy = "external_ack"  # or "local_automated"
```

The policies are orthogonal:

| Verification | Integration | Behavior |
| --- | --- | --- |
| `external_ack` | `prepare_branch` | freeze artifact, wait for native evidence, then wait for explicit integration authorization |
| `external_ack` | `auto_merge` | freeze artifact, wait for native evidence, then exact-SHA preflight and merge automatically |
| `local_automated` | `prepare_branch` | record automated verification, then wait for explicit integration authorization |
| `local_automated` | `auto_merge` | record automated verification, exact-SHA preflight, and merge automatically |

`external_ack` therefore stops at `verification_ready`. `prepare_branch`
stops later at `integration_ready`; the distinction is intentional. A native
verifier can accept an artifact without also authorizing mutation of the
default branch. The normal all-in-one Linux deployment uses `auto_merge` plus
`local_automated`, but still creates the same typed artifact and verification
records.

Unknown explicit policy values fail closed at startup. Omitted policy blocks
retain the compatibility default `auto_merge` plus `local_automated`; every
newly generated project configuration writes those values explicitly. Canvas
must be registered as `prepare_branch` plus `external_ack`.

## Supported control surfaces

The existing HTTP-only CLI is the canonical approval surface. Codex tasks use
the same commands as operators; they never edit SQLite directly:

- `dremctl approve <id>` advances `plan_review` and `test_review`. It does not
  approve `testing_ready`, which is an automated preparation state.
- `dremctl reject <id> --reason ...` returns the current approval gate for
  rework.
- `dremctl answer <id> --body ...` resolves `needs_clarification`.
- `dremctl accept-assumptions <id>` preserves the current plan and advances it
  to deterministic/SGLang review when Codex has already inspected the plan; it
  replaces a manual plan-review approval without bypassing review evidence.
- `dremctl adopt <failed-child> --commit <sha>` is a specialized recovery edge
  for a failed child and failed parent. It requires an idle child, exact
  canonical branch-head equality, and fresh immutable-base scope admission;
  accepted work resumes the parent without another inference attempt.
- `pass` and `fail` remain recognized compatibility commands but fail closed
  for delivery states with a pointer to `verify`; they cannot manufacture the
  missing exact-SHA evidence.

Mutation endpoints require the project bearer token and a stable identity in
`X-Drem-Actor`; generic identities are refused. Existing-task mutations also
carry `X-Drem-Observed-State-Version` and `Idempotency-Key`. Delivery request
bodies must match the claimed actor and carry their typed state/artifact
guards. This is attribution within the single-user project trust boundary,
not per-thread cryptographic identity.

Extend that surface with guarded, compare-and-swap delivery operations:

- `dremctl artifact <id>` shows the branch, exact SHA, base, and preliminary
  evidence to verify.
- `dremctl verify <id> --result pass|fail --actor <identity>
  --environment <fingerprint> --command <command> ...` reads the current
  artifact envelope and records native evidence for its observed task version,
  artifact version, and exact commit. A pass advances to
  `integration_ready`; a failure advances to `in_progress` and invalidates the
  artifact in the same transaction.
- `dremctl request-rework <id> --mode orchestrated|host-direct --reason ...`
  invalidates the artifact and atomically selects `in_progress` or
  `host_rework`. The mode is required; the server never guesses from diff size
  or actor name.
- `dremctl submit-rework <id> --commit <sha>` is valid only for the actor that
  owns the active host-rework session. It verifies the exact canonical feature
  ref, records the submission, and advances to `testing_ready` for a fresh gate
  and artifact version.
- `dremctl integrate <id> --actor <identity>` reads the current artifact and
  passing verification, then authorizes a `prepare_branch` task already in
  `integration_ready`. It performs the exact-SHA/base preflight and advances
  atomically to `merging`; it does not merge inside the HTTP request.
- The existing authenticated archive operation may cancel non-running parked
  delivery work. It invalidates the current artifact atomically; it never
  treats cancellation as successful verification or integration.

Every existing-task mutation carries an actor, idempotency key, and observed
task state version; delivery mutations additionally carry artifact version and
(where applicable) exact commit SHA. Task creation has no prior state version,
but atomically records its actor, idempotency key, task, event, and original
response. Replaying the same key returns the original result. Reusing it with a
different payload is a conflict. Stale acknowledgements return a conflict and
do not mutate state. A pending replay claim after a crash fails closed for
operator reconciliation. The authenticated actor claim must match any body
actor; generic identities such as `user`, `operator`, `csuite`, and `dremctl`
are rejected. Archive, comments, recovery audit/apply, and task creation are no
longer privileged exceptions.

## Codex-facing operating model

The ChatGPT/Codex task on the Mac is part of the control plane, but it is not a
privileged second database writer. It uses authenticated HTTP/CLI operations
for task creation, verification, rework routing, and integration. Git writes
are permitted only inside an explicitly owned implementation or host-rework
worktree. Computer Use never changes task state by implication.

### Creating tasks from Cubase observations

A Codex discovery task may inspect Cubase with Computer Use and create Canvas
work through `dremctl create --spec <json>`. The specification is typed rather
than a prose-only title/description and contains:

- Cubase product/version, OS/display environment, observation timestamp, and
  observer actor;
- the reference workflow's preconditions and ordered interaction steps;
- expected visible/interactive behavior, including negative behavior;
- local content-addressed screenshot/video evidence IDs and hashes;
- one or more independently verifiable acceptance criteria;
- proposed Canvas scope, explicit exclusions, dependencies, uncertainty, and
  open questions.

The local artifact store retains Cubase media. Task prompts receive only the
admitted textual description and explicit artifact references unless project
policy authorizes sending selected media as inference input. Repository data,
credentials, native build artifacts, and verification authority remain local.

Observation does not equal implementation authorization. The create endpoint
deduplicates against active tasks and may return `needs_clarification` when the
workflow or acceptance criteria are incomplete. A broad Cubase workflow is
split into tasks only at independently demonstrable behavioral boundaries;
Codex does not manufacture a large task graph merely because one observation
contains many UI steps.

### Computer Use verification and repeated tweaks

The verifier checks out and builds the exact current artifact SHA, records the
binary hash, launches that binary, and exercises each acceptance criterion with
Computer Use. It then chooses exactly one outcome through the API:

1. **Pass** — append passing interaction evidence and advance to
   `integration_ready`.
2. **Fail to orchestrated rework** — use when the acceptance contract changes,
   architecture or data ownership is implicated, tests/design need expansion,
   the repair crosses the bounded task scope, or the verifier cannot state a
   deterministic edit. The task returns to `in_progress` with the discrepancy
   evidence partitioned across immutable repair children cloned from the
   active completed test, implementation, and integration owners. Every repair
   inherits only its owner's `writable_files`; original dependency and
   `tests_for` edges are remapped to the repair generation. The original
   children stay terminal as audit history, integration cannot widen its write
   authority to its read/merge scope, and `testing_ready`/artifact re-freeze is
   blocked until every scoped repair succeeds.
3. **Fail to host-direct rework** — use for a deterministic, bounded correction
   that preserves the existing task intent and acceptance criteria. The same
   Codex task acquires `host_rework`, edits only its isolated worktree, commits,
   and submits the exact replacement SHA. Preliminary gates and full Computer
   Use verification run again; prior failures and artifacts remain immutable.

Host-direct is a workflow optimization, not a bypass. It is refused when a
worker attempt or another host-rework session is active. It may not change task
acceptance criteria, dependency shape, persistence/schema, security/auth,
cross-process ownership, build/release policy, or unrelated files. Any such
discovery is routed back through orchestrated rework. File or line counts are
diagnostic only; the route is selected from semantic risk and ownership.

The verifier may perform several host-direct cycles when every cycle continues
to satisfy those constraints. Each cycle creates a new commit, gate run,
artifact version, and verification record. Integration can consume only the
latest non-invalidated artifact, so an earlier visually passing build can never
be merged after a later tweak.

An inconclusive UI observation that changes no source, resource, build input,
commit, or binary may be repeated against the same artifact and binary; it
appends another interaction record without returning through implementation.
Any edit or binary-hash change is rework and must create a new commit, gate run,
artifact version, build, and verification. This boundary keeps ordinary
Computer Use retries cheap while making every code tweak pass through the
deterministic orchestration checkpoints.

## Transaction and ownership invariants

- A task has a monotonically increasing state version. Every status mutation
  is a compare-and-swap on `(task_id, status, state_version)`.
- The task row, typed evidence rows, and transition event commit in one
  database transaction. None may exist without the others.
- Ordinary task transitions use one guarded CAS persistence boundary. The two
  child-only exceptions are named, guarded operations: accepting independently
  proven existing work as complete, and superseding a completed test-writing
  child after parent test-review rejection. Neither can complete or reopen a
  top-level task.
- Exactly one non-invalidated delivery artifact may be current for a task.
  Artifact versions are unique and monotonically increasing per task.
- Verification records are append-only. Failure and rework invalidate the
  referenced artifact but never delete verification history.
- Gate callers are attributed identities, never the literal actor `user`.
- Only the orchestrator process writes the database. HTTP clients, Codex
  tasks, and host verifiers use authenticated mutation endpoints.
- `merging` is the only state whose handler may mutate the default branch.
  Entry requires a current artifact, a passing verification record for its
  exact SHA, and durable integration authorization when policy requires it.
- Merge completion records the accepted artifact ID, verification ID, target
  preflight SHA, and resulting merge SHA before transitioning to `done`.
- Cubase observations, acceptance criteria, preliminary gate runs, interaction
  evidence, and host-rework sessions are append-only typed records or typed
  artifact-registry entries; they are not hidden in `Task.Context`.
- `host_rework` has exactly one actor-owned session and zero active worker
  attempts. Only that actor can submit or abandon it.

## Implementation sequence

1. Add the two states, pure transition rules, classification tests, and public
   read-model support without changing runtime routing.
2. Add delivery policy plus typed artifact and verification models with
   migration/backfill behavior.
3. Split `processTestingReady` so it freezes an artifact and transitions to
   `verification_ready` instead of `merging` for `prepare_branch` projects.
4. Add guarded HTTP/CLI verification, rework, and integration operations.
5. Make merge dispatch consume an accepted artifact by exact SHA and refuse
   default-branch or artifact drift.
6. Route quick-fix, reconciliation, recovery, health, TUI, and C-Suite policy
   through the same states; centralize ordinary status persistence behind the
   guarded CAS transaction and name any child-only lifecycle exceptions.
7. Replace integration-worktree synchronization in `testing_ready` with a
   disposable exact-SHA gate workspace and typed gate-run evidence.
8. Add typed Cubase observation/acceptance-criterion ingestion plus
   `dremctl create --spec` and local artifact references.
9. Add `host_rework`, actor-owned host-rework sessions, explicit routing, and
   exact-SHA submission; extend verification records with Computer Use
   interaction evidence.
10. Remove automatic generic-fixer routing for gate failures and apply the
    closed deterministic failure policy.
11. Make merger completion recover from typed merge intent plus authoritative
    target-ref state; make telemetry/report delivery optional for correctness.
12. Apply actor/idempotency/version requirements uniformly to archive and all
    remaining mutations. **Complete.**
13. Run a read-only inference canary, then a no-op branch-delivery canary, and
    finally a non-integrating multi-tweak Computer Use canary before authorizing
    a Canvas writer.

The exact-SHA delivery core within steps 1–6 is implemented, and step 7 now
uses typed branch acceptance plus an orchestrator-owned disposable worktree;
passing gate evidence and the artifact freeze commit atomically. Step 10 now
routes deterministic command failures without launching a generic fixer: code
returns to `in_progress`, while configuration and infrastructure classes park.
Step 8 is implemented through the authenticated task endpoint and
`dremctl create --spec`: normalized reference observations and independently
addressable criteria are retained as typed records, media stays behind
content-addressed references, replay is idempotent, equivalent active work is
deduplicated, and unresolved questions enter `needs_clarification`.
Step 9 is implemented through explicit orchestrated/host-direct routing,
single-owner host-rework sessions, semantic and path-scope attestation,
canonical-ref exact-SHA submission, fresh artifact creation, and structured
Computer Use interaction evidence. Host rework is non-actionable and refuses
active worker attempts; its owner may submit, abandon, or cancel it without
discarding prior evidence.
Step 11 is implemented through an immutable merge intent linked to the exact
artifact, passing verification, authorization, feature ref, target ref, and
verified base. The orchestrator reconciles target Git state before and after
each merger dispatch, completes already-pushed work without redispatch, and
refuses unrelated target advances. Agentmon reporting is optional telemetry;
historical completion rows remain migration-compatible. Step 12 is implemented
through a shared HTTP mutation ledger: actor/header agreement, optimistic task
version checks, exact-response replay, payload-conflict refusal, and
fail-closed pending claims apply to all remaining writes. Comments, audits,
and recovery repairs now increment the task state version; simple task creation
records its replay result in the same transaction. The recovery implementation
was split below the repository's file ceiling. Delivery-gate execution failures
after exact candidate resolution now close as typed `configuration`, `infra`,
or `code` gate runs: operator/configuration failures pause without spending
model tokens, while an accepted checkout mutated by the gate returns to normal
implementation without freezing an artifact.
Reconciliation no longer infers `done`: even when a
recorded branch base proves the feature advanced and Git proves it is already
on the default branch, the task returns through `testing_ready` for an exact
artifact freeze and verification. The full ordered step-13 protocol passed
live on 2026-07-22. Repository-free inference traversed the local loopback
tunnel and remote GQ/SGLang worker with `repo_data_sent=false`. A
non-integrating delivery canary froze and verified a single metadata-file
artifact. The native multi-tweak canary then produced three exact Canvas
artifacts and binary hashes: two Computer Use failures created two distinct
actor-owned host-rework sessions, submissions, fresh preliminary gates, and
replacement artifacts; artifact version 3 passed into `integration_ready`.
The task was archived without integration and Canvas `master` remained at its
recorded base SHA.

That live run found and closed two state-machine defects. Candidate resolution
now treats the newest typed host-rework submission as the authoritative head
when it is newer than worker branch acceptance, retaining the accepted base.
Paused infrastructure/configuration/timeout gates now have a supported
`dremctl retry` edge back to `testing_ready` that preserves the feature branch.
An explicit retry idempotency key permits a new operator intention after a
durably replayed failure without falsifying actor identity. Regression tests
cover all three contracts.

## Operational hardening discovered by the canary

The disposable canary was intentionally retained as a fault-finding exercise.
It exposed and closed these boundary defects before any real Canvas task:

- local service Dockerfiles now honor BuildKit `TARGETOS`/`TARGETARCH` instead
  of producing amd64 binaries inside arm64 images;
- project identity honors explicit `DREM_PROJECT`, so a Canvas bare repository
  can be registered under a distinct local-control-plane name;
- worker images trust only the fixed `/bare` mount for cross-UID macOS Git
  access, rather than disabling ownership checks globally;
- PID 1 supervises the harness and waits for the watchdog's final commit/push;
  the final flush also pushes a clean tree when the harness already committed;
- the Codex prompt redirect is applied to the actual backgrounded command, not
  its supervising shell function;
- the canary-era integration-worktree synchronizer refuses local edits and ref
  drift, but it is transitional. Step 7 removes that worktree from preliminary
  gate authority entirely rather than making reset heuristics more elaborate;
- direct-agent traces are written under a runtime trace directory outside the
  repository, so observability output cannot become a source commit; and
- obsolete, unassigned `testing_ready` tasks can be archived through the
  authenticated API, while any live assignment still blocks cancellation.

## Pre-live canary protocol and results

The protocol below was run in order and retained as the repeatable admission
check for future control-plane changes. Do not combine it with a real Canvas
integration.

1. **Read-only inference canary.** After the operator separately starts the
   loopback SSH tunnel and local control plane, record the Canvas default-branch
   SHA and porcelain status. Send a fixed, repository-free prompt directly
   through the configured OpenAI-compatible endpoint and require a fixed
   response token. Capture endpoint identity, model, latency, and token counts;
   do not include a repository path, source, credentials, tools, or artifacts.
   Prove the Canvas SHA/status and local worktree list are byte-for-byte
   unchanged afterward.

   **2026-07-22 final result:** after restoring the existing remote SGLang
   worker and GQ route, request `0e3cc9a42a104bfaa6ab1c3cd8866da8`
   completed through the local loopback tunnel. The response hash was
   `33b613bbe848774c8f35f52b41ad15a4c1340ec27423a138fd316795598f3fba`;
   the probe recorded `repo_data_sent=false` and no orchestration state. The
   earlier 502 remains useful typed availability evidence, not the final
   outcome.
2. **Non-integrating Canvas delivery canary.** Record the Canvas default SHA
   and dirty-worktree inventory again. Use one explicitly named disposable
   feature branch whose only change is a canary metadata file; never reuse an
   active feature worktree. Require `prepare_branch` plus `external_ack`, wait
   for the exact artifact at `verification_ready`, verify that exact SHA on the
   Mac, and submit the evidence through `dremctl verify`. Stop at
   `integration_ready`: do not call `integrate`. Prove the default SHA and all
   pre-existing dirty worktrees are unchanged, then archive the parked delivery
   task (which atomically invalidates its artifact) and remove only the
   disposable canary branch/worktree.

   **2026-07-22 result:** task `59f89aac-876e-438e-a380-8beed71b7b6e`
   produced artifact `100a9c14e63a202aff5cf7e67c4a6ff089f996c3` over base
   `b53d312e1b75d1cd59ff6d515e74f85d057c7bfd`. A fresh detached macOS/arm64
   worktree proved the exact parent, sole `.drem-canary.json` change, exact JSON
   payload, clean diff, and clean status. Verification record
   `37a24f83-78a6-4d6f-bb0a-0617ed3ded5e` advanced the task to
   `integration_ready`; no integrate call or merger event occurred. Archive
   advanced it to `cancelled`, invalidated the artifact, and the disposable
   ref/worktrees/container were removed. Canvas `master` remained the base
   SHA. Concurrent local writers changed two already-dirty worktree
   fingerprints during the long run; canary cleanup preserved their
   contemporaneous fingerprints exactly and never reset or staged them.

   **2026-07-22 ordered rerun:** task
   `26b2ad4b-c8ed-49fc-86b0-cca6cdefd35d` produced artifact
   `3803667e-0113-4952-8e45-35383c02d2e5` at commit
   `2bb7f13643e1ad219fdc5a585e608053d786d174`, verified it, stopped at
   `integration_ready`, and was archived. No integrate call occurred.
3. **Non-integrating multi-tweak Computer Use canary.** Verify an exact Canvas
   artifact, submit a failing interaction result into `host_rework`, make one
   bounded local Codex correction, submit the replacement SHA, and prove that a
   new preliminary gate, artifact version, binary hash, and Computer Use record
   are required. Stop again at `integration_ready`, archive the task, and prove
   the default ref and unrelated worktrees never changed.

   **2026-07-22 result:** quick-fix task
   `98e14e0d-be5e-4721-bed1-2a5577fc88bb` produced artifacts v1/v2/v3 at
   commits `7d81ff11ceffe329063e9086c5b13865e0afb59f`,
   `b645d2c4e982a0a40344857a55afd91f5d244cfd`, and
   `773fef60b8560a8775b05184f0f31bece0cd9957`. The corresponding native
   binary hashes were `1f57dfa17c21c8c9d40aed5f513e51f5604f19d8c90fc31b3abd7e07c7454eb2`,
   `69099d722fb41faf5da3f7b69c3ba9a34af8dca810dbaddc0e753ab682646775`,
   and `2a2f24ef9162e58ead46971337acb3148df7739eaa9a8ca9d4e44ffacf9c92e5`.
   Computer Use failed v1 and v2, then passed v3 with the readable title
   `[HV:A3|SGLang:remote|orch:local] Drem Canvas
   [feature/98e14e0d-add-opt-...]`. The ledger contains three artifacts,
   three verification records, three interaction records, two host-rework
   sessions, and two submissions. The task reached `integration_ready`, was
   archived, and never invoked integration.
4. A live Canvas writer is authorized only after all evidence bundles are
   retained and the canaries demonstrate that no default ref, active worktree,
   or unrelated task changed.

## Acceptance criteria

- A Canvas task can finish container work without advancing `master`.
- The operator can identify one exact branch commit to build on the Mac.
- A passing acknowledgement is rejected if its commit or artifact version is
  stale.
- Native verification failure produces a supported rework transition with its
  evidence retained.
- Only `merging` may mutate the default branch, and it consumes the exact
  accepted artifact.
- The remote inference host receives prompts and responses only; it never
  receives Git credentials, repository mounts, refs, build artifacts, or
  verification authority.
- Existing all-in-one deployments retain an automated path through the same
  evidence contract.
- A Codex task can turn a Cubase Computer Use observation into typed,
  deduplicated acceptance criteria without granting the remote inference host
  repository or verification authority.
- Computer Use verification identifies the exact Canvas binary and artifact
  SHA and retains per-criterion interaction evidence.
- A bounded visual/interaction correction can remain with the current Codex
  task through `host_rework`, while semantic or architectural changes return to
  orchestrated processing through an explicit decision.
- Every tweak cycle produces a new commit, preliminary gate run, artifact
  version, and verification record; no edit-in-place or stale-artifact merge is
  possible.
