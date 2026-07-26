# Codex adapter and real Canvas pilot

Date: 2026-07-22

## Outcome

The local Docker control plane can now delegate inference to remote SGLang
while keeping Canvas Git state, native compilation, application launch,
Computer Use evidence, and integration authority on the Mac. The first real
feature pilot used task `00516fe3` against an isolated Canvas feature branch;
the dirty shared Canvas worktree and Canvas `master` were never touched.

## Operational path

1. Codex files or follows a typed task through `dremctl`.
2. `accept-assumptions` advances a satisfactory deterministic plan without a
   human approval pause.
3. If the SGLang reviewer requests a correction, `revise-plan` replaces only
   the mutable execution plan, validates it against the immutable task scope,
   increments the observed state version, and requests one fresh review
   without paying for another planner pass.
4. Remote SGLang workers produce bounded child branches.
5. A diagnostic pause resumes to its recorded phase with `dremctl resume`;
   retry remains reserved for failed work and preliminary-gate recovery.
6. If a failed child is mechanically repairable, Codex commits the repair in
   its isolated worktree and `adopt` re-runs exact-base scope admission before
   merging it. This avoids paying for another worker attempt.
7. The parent receives a whole-feature branch acceptance record. For
   `external_ack`, Linux verifies the exact Git candidate but defers Canvas
   commands to the host.
8. `drem-canvas-pilot.sh build` creates a detached exact-SHA worktree and runs
   `scripts/dev verify` on macOS.
9. Codex launches that exact binary and records Computer Use observations and
   content-addressed media per acceptance criterion.
10. After the requested Canvas outcome is complete, Codex completes its
    explicitly requested goal, submits the final goal tokens/time with
    `drem-canvas-pilot.sh goal-usage`, and regenerates the correlated report.
11. A pass reaches `integration_ready`. A bounded discrepancy uses the
   actor-owned host-rework cycle; broader changes return to orchestration.

## Pilot findings

The deterministic control-plane portions were fast. The classifier completed
in about 2.6 seconds and SGLang plan review in about 1.7 seconds. Planning took
186 seconds and was the dominant up-front latency. The test worker consumed a
large context budget (roughly 78k input tokens on its final attempt, with an
earlier attempt near 100k total context) and still produced an out-of-scope
file. Codex repaired and adopted that branch without a replacement inference
run.

The pilot found two higher-level defects that unit-only work would have missed:

- existing-work dedup mistook a production header scaffold introduced by the
  test child for completed implementation work;
- the integration worker stayed in scope but implemented a compile-time marker
  with the wrong semantics, which native contract/visual verification caught.

The scheduler now refuses existing-work fast-tracking when a production task's
declared scope overlaps a completed test child's scaffold. Whole-feature branch
acceptance also prevents individually admitted children from bypassing a final
parent-scope check.

## Native feedback loop evidence

Artifact v1 at `e7612eac8663c977d788a1e4321fedb4f7e6fdf5` compiled and passed
architecture checks, but Canvas's constitution rejected one line of growth in
the grandfathered `src/Main.cpp`. The adapter invalidated v1, opened a
host-direct session limited to `src/Main.cpp`, admitted the one-line layout-only
repair, and froze artifact v2 at
`3a1bc492f442a086309cc10b0c39a4d8a8e2477c`. No SGLang retry was used for that
native failure.

Artifact v2 passed 1,578/1,578 native tests plus architecture, constitution,
changed-file, and golden-file gates. The exact release binary hash was
`f697365595bd6f86b10ba51eb2b294b72d5c52d618da6c829a0f52a013172b5b`.
Computer Use recorded the marked and unmarked title scenarios with media hashes
`724bf6854921d4121de26918f61347be67c99325a93e0710bed93688f9e41870`
and `bd878952d28f525586184f38de87bbf410f084f5b5ea90506bb3930aaca66023`.
The parent reached `integration_ready`; integration was intentionally not
authorized, so Canvas `master` remained unchanged.

The debug executable also exposed a baseline Canvas assertion outside this
feature: `MixerWidget::rebuildIoStrips` constructs `ChannelStripWidget` from an
I/O-bus state, while `ChannelStripWidget::syncFromTrackState` wraps it as a
`Track`. The release artifact remained usable for the visual contract. This is
separate Canvas defect evidence, not a reason to expand or contaminate the
window-title task.

## Meta-analysis

The adapter reduces Codex inference when it converts failures into typed,
replayable evidence and keeps deterministic repairs local. The largest
remaining inference opportunities are earlier in the pipeline:

- reduce planner wall time and context by feeding it a smaller deterministic
  repository map and stricter task-spec contract;
- treat heuristic dedup as an optimization that must fail open to a worker,
  never as proof of semantic completion;
- retain host-native and Computer Use discrepancies as structured prompts for
  the next attempt instead of making the verifier restate them;
- prefer one whole-feature semantic review after deterministic child admission
  over more per-file heuristics.

The orchestration pipeline should own planning, parallelizable implementation,
immutable evidence, and lifecycle transitions. The active Codex task should
own native verification and small bounded corrections because it already has
the visual context; re-running the full inference pipeline for those tweaks is
both slower and less informed.

## Pilot 3: adapter-authored plan through real Canvas acceptance

Task `9a44738c` exercised the intended operational path from an adapter-authored
typed execution plan through remote SGLang workers, isolated Canvas branches,
native macOS verification, two host-direct repairs, and Computer Use evidence.
The shared Canvas `main` worktree and Canvas `master` remained untouched. The
final candidate is artifact v3 at
`a1ba6fe2d90d4f6f1c2d6a5eb5a788e57a8c14c4`; it is intentionally parked at
`integration_ready` because integration was not authorized.

The deterministic plan-review request used 1,840 input and 103 output tokens.
The worker loop was not yet inference-efficient: the implementation used about
30,114 cumulative input tokens, and four integration attempts each approached
the 30k replay budget without editing. Codex adopted one bounded integration
repair instead of purchasing another retry. The exact native build then found
two worker defects that Linux deferral could not detect:

- `std::string_view::starts_with` was generated for a C++17 target;
- the focused test used GoogleTest in Canvas's Catch2-only unit target.

Both corrections used actor-owned `host_rework` sessions, exact canonical-ref
submission, accepted-path checks, and fresh immutable artifacts. Artifact v3
then passed 1,578/1,578 native tests and all standard repository gates. The
verified release binary SHA-256 is
`023c872902162cf2d44776a23a3e07bf806547974bfc0316ab1fb41765043cb0`.
Computer Use observed exact titles in both requested build contexts:

- mainline: `Drem Canvas — Untitled`, screenshot SHA-256
  `befd96e3ca249b3de72d25baffbf47bd710ea2560d459da934c3948463d5c40d`;
- feature: `Drem Canvas — Untitled [feature/example]`, screenshot SHA-256
  `4ed11262d384afd8114179f5cecf5e8f92f6e0211eac84b01f416c740a2ab689`.

## Changes driven by pilot 3

- Adapter-authored execution plans bypass classifier/planner inference and
  begin at deterministic plan review.
- SGLang prompts are phase-aware and compact. The original 30k cumulative
  replay ceiling was later replaced by phase-specific ceilings after measured
  Canvas workers reached it while individual requests remained far below the
  model's 131k context window.
- Rejected test revisions no longer poison parent completion after a done
  revision supersedes them.
- A failed child with a bounded host repair can be adopted while its parent is
  already in the exact resume state.
- Direct coder/fixer containers now receive the planner's exact file list and
  omit `grep`/`glob` discovery tools when scope is known. This addresses the
  measured integration-worker exploration loop directly rather than adding a
  larger prompt.
- `drem-canvas-pilot verify` now refuses a stale SHA, dirty worktree, wrong Git
  repository, or binary outside the exact artifact worktree. Cleanup refuses
  dirty evidence worktrees.

## Remaining operational gaps

The first post-pilot hardening slice closed three gaps with direct evidence from
task `9a44738c`: every container attempt had zero public token counts, and four
completed/rejected child refs remained in the Canvas bare repository. Terminal
`sglang-direct` summaries are now parsed from a bounded, Docker-demultiplexed
log tail and persisted on WorkerAttempt and Agent. The spawner performs a
serialized pull-if-missing preflight and emits a terminal
`worker_image_unavailable` failure instead of a spawn storm. Child cleanup now
deletes only a non-checked-out ref whose Git reference registry row names that
exact child task; top-level deliverables and unowned refs are preserved.

Historical pilot rows and refs are intentionally not rewritten or deleted by
the migration. The adapter now exposes a repeatable measured canary protocol:
`start --spec` files the attributed task, `await` stops at the next evidence
boundary, the existing exact-artifact build/verify commands preserve Mac and
Computer Use authority, and `report` emits one JSON or Markdown snapshot across
parent/child lifecycle phases. The report includes SGLang reviewer and worker
tokens, attempt churn, artifact versions, host-rework loops, native verification,
and Computer Use runs. It explicitly reports both historical measurement gaps
and the absence of an authoritative host Codex token API.

The next real Canvas task is the measurement gate for this newly deployed path.
Remaining high-value gaps:

- detached build contexts resolve to `HEAD`; acceptance criteria that depend on
  a branch label need an explicit, recorded build context as this pilot used;
- changing a target-wide branch compile definition rebuilds most of Canvas;
  scoping it to `Main.cpp` would make dual-context title checks much cheaper;
- a completed post-instrumentation task is still required to prove nonzero
  terminal worker metrics and owned-child-ref cleanup against the live Canvas
  repository rather than only deterministic tests and the historical pilot.

## Pilot 4: operational Codex adapter and scoped build-metadata canary

Task `7d40b4da` proved the complete adapter boundary against the live Canvas
repository without touching the shared `main` worktree or default branch. The
adapter authored and revised the immutable plan, SGLang reviewed it without a
human approval gate, child repairs were adopted by exact commit, and the parent
froze artifact v1 at `7a4a2753528dcec60d8f5ee0505f3ba5d0105531`.

The frozen artifact passed architecture, constitution/workflow, golden-file,
and 1,577/1,577 native tests. A compile-database query found
`DC_GIT_BRANCH` on exactly `src/Main.cpp` and
`src/ui/transport/TransportBarWidget.cpp`; changing from detached `HEAD` to the
temporary verification branch rebuilt exactly those two objects. Computer Use
then observed `codex/verify-7d40b4da` in both the native title and transport
badge. The temporary ref was removed and the worktree returned to the frozen
SHA before evidence submission. The task is intentionally parked at
`integration_ready` pending explicit default-branch authorization.

The measured inference result exposed the next control-plane priority. Plan
review used 11,816 input tokens and test review used 2,380, while ten worker
attempts consumed 332,781 input tokens; nine attempts failed, including four
identical implementation attempts and four identical integration attempts that
reached the cumulative token budget without a useful edit. The pipeline
successfully moved this inference off Codex and onto remote SGLang, but did not
use that inference efficiently.

The post-pilot guardrails therefore enforce behavior instead of adding more
prompt prose:

- a scoped coder/fixer may execute two reads before its first successful
  `edit` or `write`; additional reads are blocked, and a second refusal ends
  the attempt as `no_progress`;
- `token_budget` and `no_progress` are durable `inference_budget` failures with
  zero automatic retries, so Codex can revise or adopt once instead of buying
  the same remote trace repeatedly;
- tracked repository `.claude/settings.json` is no longer classified as an
  orchestrator artifact; only `plan.json` is untracked before merges;
- recovered branch-acceptance failures no longer remain as current health
  warnings when `latest_failure_current` is false.

## Pilot 5: fail-closed SGLang with Codex repair

Task `f7d55b32` exercised the post-guardrail path with a useful Canvas workflow
feature: `scripts/dev context <path>...` now emits the exact revision, owning
concern, starting files, focused gate, and root-to-nearest `AGENTS.md` chain.
It accepts missing in-repository paths and rejects absolute or escaping paths.
The final candidate is artifact v2 at
`96c2126a64612730c9051fa8d77669d3978e0607`, intentionally parked at
`integration_ready`; Canvas `master` and the dirty shared `main` worktree were
not touched.

The first test-writing run predated the final guardrails and demonstrated the
failure mode they replace: three placeholder attempts consumed 90,817 input
and 12,686 output tokens before the parent paused. The new `dremctl resume`
edge restored that diagnostic pause to its recorded `test_writing` phase after
Codex repaired the test contract. SGLang test review approved the real
assertions without a manual gate.

The implementation worker then stopped once on `token_budget` after 32,679
input and 765 output tokens. The attempt was recorded as a durable
`inference_budget` failure even though it left a two-line diff; no automatic
retry was purchased. Codex completed the exact declared scope and `adopt`
rejected the first submission because it included `docs/CODEMAP.md` outside
the immutable task plan. After correcting the branch to
`docs/DEVELOPMENT.md`, adoption succeeded. The integration worker completed in
one 30,913-input/817-output attempt.

Exact-artifact inspection found that integration had documented a non-working
root-level `ctest -R context` command and omitted standard verification. This
was handled through one actor-owned `host_rework` session limited to
`docs/DEVELOPMENT.md`, producing artifact v2 without another SGLang run. The
artifact passed the focused CTest, all architecture/constitution/documentation
checks, a full app build, and 1,578/1,578 standard tests. The exact debug binary
SHA-256 is
`eeb48600c825fc857365944e1e8d5cf7c8d4e0f24ff77932eb731aa50815cf21`.
Computer Use was intentionally not required because this slice changes only
repository workflow and has no Canvas runtime behavior.

Pilot 5 validates the operational boundary but not Gemma's coding quality.
The viable policy is fail-closed SGLang delegation plus auditable Codex repair:

- maximum-token, no-progress, and timeout stops are failures even when a diff
  exists; cumulative-token and context stops preserve only the complete
  threshold-crossing response checkpoint for deterministic gates;
- automated test-review rejection fails the child and parent immediately
  instead of generating review-revision loops;
- terminal usage is re-inspected once when the event raced its final log
  flush, preserving complete new-attempt metrics;
- typed task observations accept content-addressed text and JSON as well as
  image/video evidence;
- diagnostic resume, failed-child adoption, exact-artifact verification, and
  bounded host rework are distinct state-machine edges with scope checks.

The next inference-reduction slice should consume Canvas's new deterministic
context output when constructing worker prompts and enforce a measured prompt
budget. Pilot 5's two post-resume workers still consumed 63,592 input tokens
for only 1,582 output tokens. That is now bounded to one attempt per failure,
but the context supplied to each attempt remains too large.

## Pilot 6: explicit Codex goal measurement

Task `815e61cc` validated the intended SGLang-first/Codex-repair boundary for a
real MIDI velocity defect, but its supervising Ryan thread ran as an ordinary
Codex turn. The app exposed 32m52s of turn duration but no turn token count;
`get_goal` correctly returned null because no explicit goal existed. The task
report therefore measured 98,114 SGLang input and 2,708 output tokens while
leaving subscription inference unknown.

The adapter now closes that measurement gap for future pilots. The delegating
prompt must explicitly request a Codex goal before task filing. Once the goal's
Canvas outcome is complete or genuinely blocked, Codex completes the goal,
uses the final token/time result to call `goal-usage`, and regenerates the task
report. The server persists an append-only record keyed by task, authenticated
`codex:<thread-id>` actor, and idempotency key. Reports expose Codex total goal
tokens and elapsed time separately from SGLang input/output counts; neither is
treated as a substitute for the other.

## Pilot 7: paired-run failure hardening

The transient-slicing comparison exposed four control-plane defects. A failed
required child could leave its parent active behind backlog descendants; the
cumulative-token guard stopped before applying an already-generated edit
batch; a syntactically valid scope omitted the production action-registration
caller; and the direct arm lived outside task reporting, making its real
artifact and verification evidence appear as zero.

The hardened path now:

- fails the parent immediately and cancels nonterminal sibling work when a
  required child fails or is rejected, while health reports legacy
  `dependency_failure_stall` rows;
- applies the entire paid response tool batch before returning a token/context
  checkpoint, and admits token-budget checkpoints with repository changes to
  deterministic gates;
- exposes `max_reads_before_mutation` in project TOML and sends it only to
  scoped coder/fixer workers;
- requires content-addressed source excerpts and explicit production-entrypoint
  seams for adapter-authored plans, including every missing-edge file in scope
  and in the integration subtask;
- runs `doctor` before goal activation, defines the Codex goal as supervision
  to a measured terminal report, and stores paired direct/orchestrated arm
  records under one immutable spec/base contract.

The paired experiment surface is intentionally local to the Canvas adapter:
Canvas source, native binaries, and Computer Use evidence remain Mac-owned;
remote SGLang remains inference-only; neither arm mutates Canvas `master`.

## Pilot 8: phase budgets and image-coherent checkpoints

Hardened task `ab2806ff` failed before producing a usable artifact because its
test worker reached the inherited 30k cumulative replay ceiling after 31,471
input tokens. The final request was only about 6.85k tokens, so this was not a
131k model-context overflow. The running worker and spawner images also
predated the checkpoint-safe code that had been deployed only in `drem-orch`.
The failed branch and task remain preserved; they are not retried as part of
the infrastructure correction.

The corrected policy separates phase complexity from model context capacity:

- test workers receive 65k cumulative input and may inspect eight scoped files
  before mutation so regressions use real production declarations;
- implementation and fixer workers receive 90k and six reads;
- integration workers receive 75k and six reads;
- reviewers retain a 30k ceiling and no mutation pressure;
- generic direct work falls back to 60k and four reads.

Crossing a replay ceiling after a repository mutation yields a durable
checkpoint for deterministic gates, including the previously uncovered
non-tool finish path. Crossing it without a mutation remains a typed
`inference_budget` failure. Acceptance requires focused unit/config tests plus
a disposable post-30k worker exercise before any supervised Canvas retry, and
requires attested orchestrator, spawner, and worker images built from the same
source state without restarting remote SGLang.

The independent worker exercise passed on 2026-07-23. The rebuilt
`drem-worker-cpp` processed four deterministic OpenAI-compatible responses,
crossed 30k after its second request, then checkpointed a real file mutation at
66,000 cumulative input against the configured 65,000 test ceiling. It emitted
`stop_reason=token_budget`, exited zero, and preserved content with SHA-256
`7c3ab1dd8d1819ba5e1a92dd32f060db0f9076983d2c060bb593ff7fa1d90550`.
No Canvas task or remote SGLang request was used. The Canvas orchestrator,
global spawner, and C++ worker image used for the exercise carried one shared
source-state attestation; the final evidence-only rebuild retained that
three-image coherence.

## Pilot 9: no-progress containment and red-test contracts

The next Canvas retry proved that a larger phase budget alone was insufficient.
The test worker spent 68,766 cumulative input tokens across 15 turns without a
mutation. Its live request stayed near 6k tokens. It repeatedly used shell
`ls`/`find`/`grep` commands to bypass a guard that counted only the structured
`read` tool, while the prompt asked it to inspect declarations for an API that
did not exist and omitted the implementation plan's exact interface shapes.

The corrected policy is containment-first:

- a 12-call hard run-wide budget replaces the advisory prompt-only limit;
- 18k/30k/24k pre-mutation input ceilings stop test, implementation, and
  integration workers before the larger checkpoint budget is consumed;
- all reconnaissance counts against one budget, and scoped shell commands are
  rejected before mutation;
- old large tool results are replaced in replay history by bounded,
  content-addressed summaries while the assistant/tool protocol pair remains;
- adapter and planner interface shapes survive plan parsing and are
  automatically materialized onto the paired test subtask as the owning files,
  exact planned functions/types, and expected missing-symbol red state;
- the test prompt treats that contract as authoritative and does not search for
  planned symbols that are expected not to exist yet.

A deterministic reproduction of the failed shell-discovery pattern now stops
after two blocked attempts at 11k cumulative input with `no_progress`, no
repository mutation, and no SGLang dependency. This protection is validated
before another Canvas task is filed; raising the cumulative limit again is not
an accepted repair.

A later two-file Canvas implementation pilot exposed an accounting edge at the
transition itself: the one allowed final scoped read was recorded as a blocked
response, so the first actually denied re-read terminated the worker before it
could observe the denial. The final read now selects mutation-only tools without
incrementing the denied-response counter. One genuinely denied response is sent
back for correction, while a second denied response still fails closed.

The next clean pilot found the equivalent natural-stop edge. After six
successful reconnaissance turns, the OpenAI-compatible inference backend
(SGLang in this pilot) returned a provider compaction message with
`finish_reason=stop` at 73,294 cumulative input tokens. The natural-stop
branch failed immediately at the 55k pre-mutation ceiling even though the 90k
phase budget had reserved a mutation turn. Scoped mutation workers now journal
one explicit mutation-only corrective request for an unmutated `stop` or
`end_of_turn` at the ceiling. A second unmutated stop still fails `no_progress`,
and read-only integration completion is unchanged.

## Recovery-guard follow-up: completion, continuation, and delivery ownership

The next parity pilots exposed three control-plane failures rather than Canvas
implementation failures: a dependency-cancelled child was treated as completed
after a sibling was adopted; a useful decomposed-child checkpoint could only be
handed to Codex instead of continued with its durable journal; and delivery
rework was assigned to the completed integration child even when the observed
defect belonged to a test or action owner.

The state machine now records dependency-failure cancellation provenance and
requires explicit supersession/replanning before a cancelled child can be
ignored. Token-budget/timeout/context-limit child checkpoints continue
automatically only after exact-head, immutable-prompt, durable-journal, and
file-scope admission; continuation remains partial work, never an adoption.
Delivery rework now reconstructs dependency-ordered scoped repair children from
the completed ownership graph. Each repair retains its immutable writable
scope, so a keymap-only integration child cannot receive an action or test
repair.
Generated merge subjects are neutral fixed control-plane text, so external
branch/task wording cannot cause a repository-policy failure.
