# Canvas orchestration reliability contract

Canvas tasks execute from a deterministic manifest compiled after plan
acceptance. The semantic reviewer may request a revision, but it does not own
runtime topology, file permissions, budgets, gates, or retry behavior.

## Compiled execution manifest

The manifest freezes the task/spec fingerprint, exact base SHA, ordered steps,
read and write scopes, dependencies, expected turns, required gates, and
recovery policy. Its SHA-256 is copied to the parent and every child. Plans
with disjoint write sets use the decomposed DAG lane. Plans assigning the same
writable file to multiple workers use the atomic-repair lane: one worker owns
the union, and deterministic scope, contract, native, and Computer Use gates
validate the result afterward. This prevents sequential weak-model workers
from repeatedly rediscovering and rewriting the same artifact.

## Worker turns and context

Direct workers journal every completed model/tool batch to a task-specific,
host-backed directory. The journal contains the immutable prompt fingerprint,
exact conversation replay state, last parsed turn, token totals, mutation
state, and tool-call count. A replacement container resumes only when the
prompt fingerprint matches; completed or stale journals are ignored.

The durable event history remains lossless. The model-facing view is bounded:
the two newest tool observations remain complete and older observations are
folded to a content hash, byte count, and short prefix. Telemetry distinguishes
peak per-request input from cumulative replay input and records resumed turns
and folded bytes.

## Budgets and recovery

Static phase values are floors. At dispatch, the worker derives its minimum
viable cumulative budget, mutation threshold, iterations, and tool calls from
the rendered prompt size, writable-scope size, role, and expected turns. A
mutation turn always remains reserved.

The semantic loop detector recognizes identical observations reached through
different actions and alternating ABAB cycles. It grants one explicit
rehydration turn, then requires an authorized mutation or `BLOCKED: <concrete
missing fact>`. Blind retry count is zero. A valid Git checkpoint is preserved
for deterministic admission or bounded adoption.

## Regression corpus

Run the focused operational corpus before a real Canvas pilot:

```bash
scripts/drem-canvas-orchestration-regressions.sh
```

It covers empty/length plan review, planned-contract and scope admission,
large inherited artifacts, pre/post-mutation budget exits, crash/resume,
checkpoint handoff, sibling draining, integration scope, artifact freezing,
native verification failure, and repeated Computer Use rework. The real Qwen
worker canary remains the final preflight; the corpus does not replace an
exact-artifact host verification or Computer Use run.
