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
state, successfully mutated paths, unresolved failed edits, and tool-call
count. A replacement container resumes only when the prompt fingerprint
matches; completed or stale journals are ignored.

The durable event history remains lossless. The model-facing view is bounded
by workload: read-only reviewers retain the aggressive compact view, while
mutation-capable coders and fixers retain the four newest complete tool
exchanges until 70% live-context pressure. Older observations are folded to a
content hash, byte count, and short prefix. This preserves the most recent
writable source through the read-denial-mutation transition without increasing
every reviewer request. Telemetry distinguishes peak per-request input from
cumulative replay input and records resumed turns and folded bytes.

## Budgets and recovery

Static phase values are floors. At dispatch, the worker derives its minimum
viable cumulative budget, mutation threshold, iterations, and tool calls from
the rendered prompt size, writable-scope size, role, and expected turns. A
mutation turn always remains reserved.

The semantic loop detector recognizes identical observations reached through
different actions and alternating ABAB cycles. Once discovery closes, all
prose-only and denied-read recovery paths share one non-stackable forced
mutation allowance; after it is consumed, another non-mutating response fails
closed. Blind retry count is zero. A valid Git checkpoint is preserved for
deterministic admission or bounded adoption.

A token, tool, context, or natural-stop boundary is not completion merely
because one scoped file changed. Test and implementation workers receive an
explicit required-mutation set equal to their declared writable contract.
Every required path must have a successful mutation, and every failed mutation
must be repaired, before the harness may return success. Otherwise the
watchdog preserves the partial commit, the durable journal remains incomplete,
and the orchestrator resumes the same child from that checkpoint. Integration
workers retain read-only completion because their assembly inputs may already
be correct after dependency merges.

## Regression corpus

Run the focused operational corpus before a real Canvas pilot:

```bash
scripts/drem-canvas-orchestration-regressions.sh
```

It covers empty/length plan review, planned-contract and scope admission,
large inherited artifacts, pre/post-mutation budget exits, crash/resume,
checkpoint handoff, branch-acceptance persistence, marker-shaped history
retention and forced mutation, partial multi-file checkpoints, failed-edit
journal continuation, required output coverage, non-stackable recovery,
sibling draining, integration scope, artifact freezing, native verification
failure, and repeated Computer Use rework. The real Qwen worker canary remains
the final preflight; the corpus does not replace an exact-artifact host
verification or Computer Use run.

Production incidents also have data fixtures under
`internal/orchestrator/testdata/incidents/`. A fixture preserves the relevant
persisted task, attempt, rejection, and expected retry classification without
retaining a worker checkout. The marker-v9 replay covers both JSON shapes that
matter: the named in-memory `model.JSONField` seen before persistence and the
ordinary nested maps produced by SQLite reload. It must classify the missing
`marker.add` assertion as `test_contract`, dispatch one bounded correction,
accept the repaired checkpoint, and reach a versioned frozen artifact.

Registry-action red tests have a two-stage admission contract. Task filing
must explicitly instruct an executable assertion, enumeration, resolution, or
execution of the exact action ID. Branch admission then requires that token on
an added executable test line; a test name, label, or comment is insufficient.
The worker prompt requires source-backed registry APIs and fails closed when
the verified source pack does not provide one.

The production orchestrator Dockerfile runs the marker incident replay and
its spec/admission tests in the Linux/CGO build stage. Local host tests alone
therefore cannot authorize an image whose builder runtime fails the replay.

## Benchmark acceptance boundary

`bench/canvasbench-v2/manifest-orchestrated.json` is the model/harness
qualification suite for this contract. It tests the worker-facing result of
compiled contracts, scoped artifact handoffs, ownership-union repair, and
deterministic rework without requiring one model session to implement an
entire cross-phase feature. `manifest-focused.json` remains a raw agent
discriminator and `manifest.json` remains an adversarial stress suite; neither
is a substitute for the orchestrated production acceptance boundary.

Compiled worker contracts are executable contracts, not topic summaries. They
name exact destination paths, allowed callable expressions, positive versus
no-op side effects, and a bounded artifact count. The harness repeats the
read/write projection in the model-visible system prompt while enforcing it in
the outer container. A worker that must rediscover a type, choose a destination,
or infer whether an operation is undoable indicates an upstream compilation
defect rather than useful model difficulty.

For sufficiently narrow skeleton-completion phases, the compiled contract may
be enforced as the worker's entire tool surface instead of repeated as prose.
The `pi_fixed_slots_v1` contract binds one target and bounded replacement
slots; the Pi adapter exposes only that generated tool, the trusted proxy
forces the exact strict function call, and the tool validates the whole edit
before writing and terminating. This lane is intentionally narrower than a
general implementation worker. Its one-case CanvasBench manifest compares the
same task under generic Pi and enforced Pi so improvements are attributable to
the contract rather than a changed fixture or prompt.
