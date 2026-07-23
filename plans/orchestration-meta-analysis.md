# Orchestration meta-analysis: simplify the control loop

Status: evidence-backed direction after ordered delivery and live three-artifact
Canvas/Computer Use canaries. Phases 1–3 are implemented and covered by
focused, full-suite, and race tests, including typed failures that occur before
a gate command starts. Phase 4 remains deliberate follow-up work; it should
delete old paths rather than layer new roles beside them. The delivery protocol
and bounded host-direct repair loop are worth keeping.

## What the canary proved

The strongest part of the system begins after implementation has produced an
immutable candidate:

- branch acceptance records an exact head and base SHA;
- preliminary evidence is attached to a typed delivery artifact;
- native verification is tied to that exact artifact and environment;
- integration can remain parked without mutating Canvas `master`;
- cancellation invalidates the artifact without pretending that it shipped.

That boundary removed inference. The control plane could answer what was
built, what was checked, and what was authorized from durable records.

The live multi-tweak canary exercised the whole local/remote split rather than
only its pure state-machine tests. One quick-fix task produced three immutable
artifacts, three exact native binary hashes, three per-criterion Computer Use
records, two actor-owned host-rework sessions, and two bounded replacement
commits. Artifact versions 1 and 2 failed for observed title discrepancies;
version 3 passed into `integration_ready`. Archiving then invalidated the
artifact without integrating it or moving Canvas `master`.

The weakest part is the path before that boundary. In the canary, a successful
worker exit was not consumed as a normal control-plane fact. The task remained
`in_progress` until lease recovery inferred completion from a dead process and
a moved Git ref. Similar ambiguity exists when a long-lived host worktree is
used as both mutable scratch space and test authority, and when any non-zero
test result is treated as a reason to spend another model attempt.

The live loop also found two control-plane invariants that deterministic tests
had previously bypassed:

- a submitted host-rework replacement must supersede the older worker branch
  acceptance when the next exact-SHA gate resolves its candidate; and
- a paused infrastructure/configuration/timeout gate needs a supported
  `retry` edge back to `testing_ready` that preserves the accepted feature
  branch and spends no model tokens.

Both are now explicit runtime behavior with regression tests. `dremctl retry`
also accepts an operator-supplied idempotency key, so a genuinely new retry
intention after a durable failed replay or control-plane upgrade does not
require changing actor identity.

## Keep, replace, retire

| Area | Decision | Reason |
| --- | --- | --- |
| Exact-SHA branch acceptance | Keep | Establishes an immutable handoff from implementation to delivery. |
| Typed artifact, verification, authorization, and merge evidence | Keep | Makes delivery auditable and fail-closed. |
| Explicit `verification_ready` and `integration_ready` states | Keep | Separates native proof from permission to mutate the target branch. |
| Actor-owned `host_rework` loop | Keep, narrowly bounded | Two live corrections reused the current Codex context while still forcing fresh gates, artifacts, hashes, and Computer Use proof. |
| Spawner ownership of Docker | Keep | Provides one authoritative process-lifecycle boundary. |
| Agentmon log ingestion | Keep as telemetry | Useful for observability, but logs must not drive task state. |
| Lease expiry as normal completion detection | Replace | It is delayed inference over facts already held by the spawner. |
| Persistent integration worktree as gate authority | Replace | Mutable ambient state can drift away from the accepted artifact. |
| Automatic fixer for generic command failure | Replace | Infrastructure, configuration, and code failures require different actions. |
| Docker event handling in two services | Retire as a control pattern | Agentmon may observe events, but only the orchestrator should advance task state from spawner facts. |
| Reconciler as a primary workflow engine | Retire gradually | Reconciliation should repair missed facts, not define the happy path. |

## Target architecture

The normal path should contain few model decisions and explicit deterministic
boundaries:

```text
optional classify/plan
  -> one model implementation attempt
  -> authoritative spawner attempt result
  -> exact-SHA branch acceptance
  -> disposable exact-SHA preliminary gate
  -> verification_ready
  -> exact-artifact native verification
  -> integration_ready
  -> deterministic exact-SHA merger
```

Four durable concepts are sufficient to explain the flow:

1. **Task** — desired outcome and delivery phase.
2. **Worker attempt** — one process execution, its role, identity, exit, and
   resource result.
3. **Delivery artifact** — immutable branch/head/base plus preliminary gates.
4. **Verification/integration evidence** — proof and authorization tied to the
   artifact.

Task state must change only from typed attempt results, gate results, or
delivery evidence. Container liveness belongs to the worker-attempt lifecycle;
it is not itself a task phase.

## Control-loop invariants

- The spawner is authoritative for container state. The orchestrator consumes
  that state during every tick; Docker event delivery is an optimization, not
  a correctness dependency.
- A zero-exit current attempt is consumed immediately and idempotently.
- A non-zero exit produces a typed attempt failure before any retry decision.
- Leases and broad reconciliation repair missed notifications only. They are
  never the expected completion path.
- Agentmon cannot approve, fail, retry, or advance a task. It emits telemetry.
- Preliminary gates run against a disposable checkout of the accepted SHA,
  never ambient changes in a persistent host worktree.
- A newer typed host-rework submission supersedes the original worker branch
  acceptance for candidate resolution while retaining the original accepted
  base SHA.
- Retrying a paused runner/configuration/timeout gate returns only to
  `testing_ready`; it never deletes the feature branch or redispatches a model.
- Gate failures are classified before policy acts: code regression may request
  rework; infrastructure/configuration failures park or retry deterministically.
- A model fixer is an explicit rework policy, not the default response to an
  unknown non-zero exit.
- Remote inference health is checked before dispatch. An unavailable endpoint
  parks the attempt without silently changing providers.

## Incremental replacement plan

### Phase 1 — authoritative worker completion

On each orchestrator tick, list project workers through the spawner. Inspect
only terminal entries and feed their exact exit state into the existing,
idempotent worker-attempt handler. Share replacement accounting with the
optional direct Docker event watcher. Keep lease recovery only as a fallback.

Success criterion: a successful worker advances from `in_progress` without
waiting for stale-agent or lease thresholds, and duplicate observation cannot
complete or respawn it twice.

### Phase 2 — exact-SHA gate runner (implemented)

Materialize a fresh detached worktree for the accepted artifact SHA, run the
configured deterministic commands there, capture structured results, and
delete it. The long-lived host integration worktree is not part of normal gate
execution.

Success criterion: changing any unrelated host worktree cannot affect the
artifact gate result.

### Phase 3 — failure routing without inference (implemented for command runs)

Give the gate runner a small closed failure taxonomy: `code`, `infra`,
`configuration`, `timeout`, and `cancelled`. Define retry/park/rework policy for
each class. Remove the generic path from command failure to fixer dispatch.

Success criterion: an unavailable tool, mount, endpoint, or runner spends no
model tokens and cannot be mislabeled as a code defect.

### Phase 4 — delete duplicate control paths

Once production evidence shows the first three phases are stable, remove
agentmon-to-state assumptions, happy-path stale-agent completion synthesis,
and role-specific recovery branches that duplicate typed attempt handling.

Success criterion: one documented normal transition source exists for each
task edge, with the reconciler exercising only recovery edges.

## Measures that determine whether this is better

Track these per project and role:

- worker exit to attempt-finalized latency;
- attempt-finalized to artifact-frozen latency;
- percentage of task transitions produced by reconciliation rather than the
  normal path;
- duplicate/stale lifecycle observations ignored;
- gate failures by class and model attempts spent after each class;
- artifact SHA mismatches and ambient-worktree drift blocks;
- model calls, tokens, and wall time per accepted artifact;
- rework success rate by explicit reason.

The key health target is not raw task throughput. It is that reconciliation,
unknown failure classes, and model-based repair become rare exceptions whose
cost is visible.
