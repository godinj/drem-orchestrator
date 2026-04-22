# Bug I — Event-channel saturation + orphan-subtask scheduler starvation

**Status**: Root Cause #1 **MERGED** (2026-04-21, retry-endpoint
cascade landed at `internal/orchhttp/gate_handlers.go`
`handleRetryTask`). Root Cause #2 **OPEN** — deferred per Seth's
recommendation until Bug H lands and we re-measure the WARN cadence.

## Root Cause #1 — Final shape (MERGED)

Seth (CTO) rejected the original Option A framing below (direct
`failed → in_progress` parent edge). Shipped shape, per his
correction:

- Retry HTTP handler at `internal/orchhttp/gate_handlers.go`
  detects `task.ParentTaskID != nil && parent.Status == FAILED`
  on entry.
- Cascades via the **existing** `RetryTask` primitive — first
  `RetryTask(parent)`, then `RetryTask(child)`. No new state-machine
  edge. Both calls ride the already-allowed `FAILED → BACKLOG`
  transition in `internal/state/machine.go::ValidTransitions`.
- Scheduler's `processBacklog` promotes `BACKLOG → IN_PROGRESS` on
  the parent on the next tick via the canonical live-promotion path.
  With the parent live, `scheduleSubtasks(parent)` resumes and picks
  up the retried child naturally — no custom reconciler pass.

**Rationale (Seth's framing, preserved):**
- Any subtask-detach / stale-agent-unlink logic added in task
  `6eed2a6f` lives on the `FAILED → BACKLOG` edge. Reusing the same
  edge for the parent means no drift.
- Single retry primitive — no divergent code paths to maintain.
- Orphan-subtask pickup is a natural consequence of parent
  re-animation, not a reconciler quirk.

**Scope guards shipped with the cascade:**
- DONE parents are not re-animated (only `FAILED` parents trigger
  cascade) — covered by `TestRetrySubtask_DoneParentLeftAlone`.
- A dangling `ParentTaskID` (parent row deleted out-of-band) falls
  through to single-task retry — covered by
  `TestRetrySubtask_MissingParentFallsThrough`.
- Happy-path cascade covered by
  `TestRetrySubtask_ReAnimatesFailedParent` (both rows transition to
  `BACKLOG`, parent-retry call ordered before child-retry call).

---

Original investigation notes (pre-fix) preserved below for audit
trail.

**Status (pre-fix)**: OPEN. Investigation complete; fix not yet
greenlit. Plan filed to capture the **two separate root causes** the
retry-agent dogfood conflated into one report.

**Origin**: 2026-04-22. Retry-CLI dogfood flipped v15 subtask
`8a7616f1-49c2-4622-8f07-b8b423273ee7` from `failed → backlog` at
05:32:17Z via the new `POST /projects/{name}/tasks/{id}/retry`
endpoint. As of 06:38Z the subtask was still at `backlog` — no worker
spawn, no scheduler log lines. Retry-agent flagged `event channel
full, dropping event` WARNs in orch logs and speculated scheduler
starvation from event-channel backpressure.

Investigation disentangles the two signals.

## Root cause #1 — Orphan subtask: parent is `failed`, scheduler never visits

`scheduleSubtasks(parent)` is the **only** code path that picks up a
BACKLOG subtask and dispatches a worker
(`internal/orchestrator/subtask_scheduling.go::22`). It is called
from parent-handling sites: `processTestWriting`, `processTesting`,
`processCoding`, each of which runs **only when the parent is in a
live status** (`in_progress`, `testing_ready`, `test_review`, etc.).

Current DB state:
```
id        | status  | updated_at
3ddba802  | failed  | 03:19:09Z   ← v15 parent
4e1318b3  | failed  | 03:19:09Z   ← v16 parent
8a7616f1  | backlog | 05:32:17Z   ← v15 subtask (retried, orphaned)
```

Parent `3ddba802` transitioned `test_review → failed` at 03:19:09
("subtasks failed: Implement CanaryV15Marker struct"). The retry
endpoint transitioned subtask `8a7616f1` **only**. Parent stays
`failed`. Tick loop never invokes `scheduleSubtasks(3ddba802)`
because the parent-state gate rejects it. Subtask sits at backlog
forever.

**This is the real scheduler starvation.** It has nothing to do with
the event channel. The retry CLI's fix was incomplete: retrying a
child does not re-animate its parent.

### Why the retry-agent missed this

The retry PR's live dogfood waited ~12ms and confirmed the status
flip succeeded. The agent moved on before the scheduler's 5-second
tick had a chance to NOT pick up the task. Only the save-state
context flagged "1h later, still at backlog" — by then the agent was
done. Root-causing the orphaning required looking at parent state,
which the retry PR's scope did not cover.

## Root cause #2 — Event channel full: TUI feed has no consumer in headless mode

`internal/orchestrator/orchestrator.go::emit` (line 758) sends to
`o.events chan<- Event` with a `select … default: drop` pattern. The
channel's documented purpose is "TUI channel" (line 758 comment).

In the headless containerized pivot, no one reads from `o.events`.
The channel was sized for a TUI consumer that does not exist in the
container. First ~16 emits fill the buffer, all subsequent emits
drop.

The 5-second `testing_ready fixer failed, needs human review`
reconciler loop on v17 `6b6eb427` hammers `o.emit("testing_ready_
needs_human", …)` every tick. Buffer fills within the first minute
of the hot-loop; every subsequent emit fires the WARN. 1677 WARNs in
a ~4-minute window == expected given the hot-loop cadence plus other
emit sites.

**This is a cosmetic WARN, not a scheduler impact.** The scheduler
does not consume from `o.events`. The drops do not block anything;
they just fill the log with noise.

## Impact

- **Orphan subtasks** (root cause #1): v15 subtask stuck. If v15/v16
  retries go through (post-Bug-H fix), each parent-level retry needs
  to handle the parent too — otherwise retries produce zombie
  backlog rows.
- **Event-channel WARN flood** (root cause #2): log noise. 1677 WARN
  lines in ~4 minutes, which interferes with grep-driven debugging.
  Related to Bug E W4.1 (log sampling) — same class of problem,
  different channel.

## Fix options

### Fix for root cause #1 — Orphan subtask scheduler pickup — MERGED

Seth's correction superseded the options below. The shipped shape is
documented at the top of this doc ("Root Cause #1 — Final shape").
Retained below for audit trail only — do not re-implement.

**Option A (preferred, ORIGINAL FRAMING — SUPERSEDED) — Retry-endpoint cascade to parent.**
When the retry endpoint is called on a subtask, also check the parent
task's status. If parent is `failed`, transition it to the earliest
appropriate live state (`in_progress` if it had coded work,
`testing_ready` if all siblings are done, etc.). Re-animate the tree
so the tick loop resumes scheduling.

- LOC: ~50 in `internal/orchestrator/handlers.go` + test.
- Regression proof: extend retry endpoint tests with a failed-parent
  case.
- Risk: re-animating a failed parent is a state-machine-adjacent
  change. Needs the same "does this transition exist in
  `state.TransitionTask`?" check that the retry endpoint already
  does. If no valid transition exists, the endpoint returns a clear
  error instead of silently orphaning.

**Option B — Reconciler catches orphan subtasks.**
Add a reconciler pass that queries "subtasks in BACKLOG whose parent
is in a terminal state". For each match, either (a) transition the
parent back to a live state, or (b) fail the subtask with a clear
reason (`"parent terminal: retry parent to re-animate"`).

- LOC: ~80 in `internal/orchestrator/reconcile.go` + test.
- Regression proof: dispatch-stall test with orphan subtask shape.
- Trade-off: reconciler runs async, so the retry response returns
  before the parent is re-animated. User experience is "retry
  succeeds, then task sits at backlog for one reconciler tick, then
  worker spawns" — a few seconds of lag. Simpler transition logic
  than A.

**Option C — CLI-side pre-flight check.**
`drem cli retry` rejects a retry-on-subtask if the parent is in a
terminal state, and instructs the operator to retry the parent
instead. Does not address the orphaning — just surfaces the
limitation.

- LOC: ~20 in `internal/cli/gate_commands.go`.
- Downside: does not fix the bug; pushes it back to the operator.

**Recommendation for #1**: **Option A + a dispatch-stall regression
test.** Makes the retry endpoint self-healing for the common case.
Skip B as a second layer unless we see orphans produced by a path
other than retry.

### Fix for root cause #2 — Event channel saturation

**Option A (preferred) — Drop the TUI channel in headless mode.**
Make `o.events` optional on the orchestrator. When constructor gets
`nil`, `emit` becomes a no-op. Drop the WARN.

- LOC: ~15 in `orchestrator.go` + wiring in `cmd/drem/main.go` to
  pass `nil` in containerized mode.
- Regression proof: existing `eventbus_integration_test.go::
  TestSetEventBus_NilIsNoOp` already tests nil channel behavior;
  extend to the `emit` method.

**Option B — Per-site log sampling.**
Keep the channel, but wrap `emit`'s drop-path WARN with the same
per-site sampler introduced by Bug E W4.1
(`internal/logging/sampler.go`). Log the first drop per type per
minute, count the rest.

- LOC: ~20 in `orchestrator.go`.
- Preserves the signal (we still learn "channel is full") without
  flooding the log.

**Option C — Increase buffer size to 1024.**
Kick the can. Buys time against sustained 1-per-5s emit rates. Does
not fix the underlying "no consumer" problem.

- LOC: 1.
- Downside: does not address the root cause. Also masks future
  hot-loop regressions (which Bug E W4.1 sampling would catch).

**Recommendation for #2**: **Option A** — drop the dead channel in
headless mode. Option B as a fallback if we want to preserve the
TUI path for later. Skip C.

## Impact ordering

- **#1 blocks v15/v16/v17 progress.** Fix first.
- **#2 is log-cosmetic.** Fix when Bug H unblocks the hot-loop and
  the WARN flood goes away on its own; may not need fixing at all if
  Bug H's resolution removes the upstream hot-loop.

## Constitution notes

- `internal/orchestrator/handlers.go`: 610 lines today; +50 for
  retry-cascade keeps it under 800 cap.
- `internal/orchestrator/reconcile.go`: shrink-only file. Avoid
  Option B for #1 unless we can offset the growth elsewhere.
- `internal/orchestrator/orchestrator.go`: 845 lines — at cap. Any
  change to `emit` must be a wash (nil-check + no-op is zero net
  lines).

## Regression proofs (what catches this next time)

- **#1**: `TestRetrySubtask_ReAnimatesFailedParent` in
  `handlers_test.go`.
- **#2**: `TestEmit_NilChannelIsNoOp` in
  `eventbus_integration_test.go` (already exists for SetEventBus;
  extend to cover emit directly).

## Out of scope

- **Bug H (merger --test-cmd crash)**: separate plan doc. Its fix
  removes the hot-loop upstream, which reduces WARN volume for #2
  but does not fix #2 structurally.
- **Reconciler rolling failed tasks back to testing_ready**: not
  covered here. The reconciler's "all subtasks done, recovering
  parent" path fired on v17 at 06:31:08Z and re-entered the hot-
  loop. Likely correct behavior in isolation (recover failed parents
  when siblings are done), but combined with Bug H creates the
  hot-loop. Revisit after Bug H lands.
- **`drem-kyle` polling cadence**: 30s poll of `/tasks` is not
  related.

## Operator decision point

Two independent fixes. For #1, pick A (preferred) or B. For #2,
pick A (preferred), B, or defer until after Bug H lands. C options
for both are last-resort.
