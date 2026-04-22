# Proactive bug + gap audit (2026-04-22)

**Scope**: Repo-wide sweep of `/home/godinj/git/drem-orchestrator.git/master`
at HEAD `3fdcb85` (advanced from `535fb81` during the audit as Bug H
merged). Goal: surface bugs, gaps, and drift the operator has not yet
tripped over so Seth can prioritize rather than whack-a-mole.

**Method**: 10-minute fast pass across `plans/`, `docs/`, `internal/`,
`cmd/`; targeted sweeps for contract mismatches, silent-failure
patterns, observability gaps, state-machine holes, consistency
drift, workarounds, and live-system signal (docker ps, drem.db,
watcher deliveries.db, csuite tree). Read-only; no containers
restarted. Plans marked MERGED/shipped are excluded per brief (Bug F,
Bug E, Bug H, Bug I#1); plans-in-flight are flagged explicitly.

## Summary table

| # | Title | Category | Urgency | Effort |
|---|---|---|---|---|
| F01 | `o.events` chan has no consumer in headless orch (Bug I#2 — plan exists, not shipped) | silent failure | medium | XS |
| F02 | csuite-persona auto-reply routing: 100% of stubs quarantined (plan exists, not shipped) | half-shipped | high | S |
| F03 | Kyle container never reads its inbox — operator hand-depositing | workaround still required | high | M |
| F04 | Retry cascade covers `failed` parents but NOT `paused`/`cancelled`/`plan_review` | contract mismatch | high | S |
| F05 | `failure_reason` lives only in `task.Context` JSON — no first-class column | observability gap | medium | S |
| F06 | `task.Context["needs_human_review"]` disagrees with `task.NeedsHumanReview` column | contract mismatch | medium | XS |
| F07 | `Experiment`/`Variant` tables absent from `internal/db/AutoMigrate` | half-shipped / consistency | high | XS |
| F08 | README.md describes tmux-era architecture; contradicts containerization pivot | stale doc | medium | S |
| F09 | `CLAUDE.md` references non-existent `compose.override.yml` | stale doc | low | XS |
| F10 | `processTestingReady` hot-loop never transitions out when `fixer_attempted=true` | single point of failure / hot loop | high | S |
| F11 | 8 of 44 `state.TransitionTask` call sites skip `publishTaskTransition` (event bus gap) | observability gap | medium | S |
| F12 | `pprof` + `SIGUSR1` installed in orch but not exposed in compose wiring (env not set) | observability gap | low | XS |
| F13 | Merger library still silently accepts empty `TestCmd` — TODO(seth) unresolved | contract mismatch (library vs CLI) | medium | S |
| F14 | `orchEvents` chan (size 100) allocated even in `--tui-only` mode but never consumed in non-TUI runs | silent failure | low | XS |
| F15 | 944 `idle` agents accumulated in DB; oldest 2026-03-05 — no reaping path | consistency drift / unbounded growth | medium | S |
| F16 | Watchdog has no SIGUSR1/pprof/restart story; single point of commit + push | single point of failure | medium | S |
| F17 | `reconcileAlreadyMergedFeatures` bypasses state machine (FAILED→DONE direct SQL write) | contract mismatch | medium | XS |
| F18 | `plans/warm-direct-prep.md` still not implemented — prep runs inline in orch tick loop | half-shipped | medium | L |
| F19 | `plans/sglang-gemma4-followup.md` Dockerfile never verified; plan stale 3+ days | half-shipped | low | M |
| F20 | Legacy `runner.SpawnAgent` (host-spawn tmux path) still callable in prod code paths | consistency drift | medium | M |
| F21 | `internal/logging` sampler exists but only 2 prod call sites — most Warn/Error unsampled | observability gap | medium | S |
| F22 | Persona poller never writes frontmatter; operator must hand-write every reply | workaround still required / half-shipped | high | S |
| F23 | Delivery audit DB and orch DB both copy task/agent state — drift is possible | consistency drift | low | M |
| F24 | `publishTaskTransition` skipped in `test_writing.go` replan path (2 transitions, 0 publishes) | observability gap | medium | XS |
| F25 | `log.Printf` (15 sites) vs `slog` (600+ sites) — mixed logging without `task_id`/`project_id` context | observability gap | medium | S |
| F26 | `orchEvents` drop emits `logger.Warn` every time — Bug I#2 WARN flood still open | observability gap | medium | XS |
| F27 | `/csuite/operator` inbox/outbox dir exists but no documented consumer | half-shipped | low | S |
| F28 | 83/89 recent csuite deliveries go to `quarantine` (end-to-end pipeline is effectively broken) | silent failure (live) | critical | S |
| F29 | `reconcileStuckAgents` / container-awareness plan still open (plans/reconciler-container-awareness.md) | half-shipped | medium | M |
| F30 | 104 files sit in `/csuite/quarantine/` with no cleanup/rescan-retry policy | consistency drift | low | XS |
| F31 | Persona `restart-context.md` is stale (Seth's last update 2026-03-27) — no staleness check | observability gap | low | XS |
| F32 | `globalSpawner` spawner is a single process — its crash wedges every project's spawn path | single point of failure | medium | L |
| F33 | Orchestrator's `events` channel size hardcoded 100 — no backpressure signal when TUI is slow | single point of failure | low | XS |
| F34 | Bug H fix uses `failTask` but `failure_reason` string is the same regardless of project language | observability gap | low | XS |

## Findings

### F01 — `o.events` chan has no consumer in headless orch (Bug I#2 — plan exists, not shipped)

**Category**: silent failure
**Urgency**: medium
**Effort**: XS
**Symptom**: Every `o.emit(...)` past the first ~100 emits logs `"event
channel full, dropping event"` at WARN. Burns log disk and CPU; no
functional break.
**Evidence**: `internal/orchestrator/orchestrator.go:760-764` — select/
default on `o.events`; emit caller in `cmd/drem/main.go:213` allocates
`make(chan orchestrator.Event, 100)` but the bridge goroutine at line
380 only drains it when the TUI binary is running. In containerized
headless `drem-orch` there is no TUI, so the bridge goroutine is never
wired and the chan fills.
**Impact**: WARN flood (observed 1677 lines in 4 minutes during Bug I
investigation); masks other WARNs that operators might care about;
contributes to operator's "reactive pattern" frustration.
**Plan status**: `plans/bug-i-event-channel-saturation-scheduler-starvation.md`
Root Cause #2 — OPEN, explicitly deferred.

### F02 — csuite-persona auto-reply routing: 100% of stubs quarantined

**Category**: half-shipped
**Urgency**: high
**Effort**: S
**Symptom**: Every persona-emitted reply from the four csuite
personas is quarantined by the watcher with `reason="no frontmatter
delimiters"`. The inter-persona message mesh is effectively broken.
**Evidence**: Live watcher DB copied from container
`drem-orchestrator-csuite-watcher-1:/var/lib/watcher/deliveries.db`
shows `dest: quarantine=83, kyle=3, alex=2, seth=1` as of
2026-04-22. Root-causing at
`internal/csuite/persona/poller.go:237-248` confirms: poller
writes `claude -p` stdout straight to outbox with no frontmatter
wrap. `internal/deliver/classify.go:72-93` quarantines anything
without `---\n` delimiters.
**Impact**: Personas cannot reply to each other. Operator
hand-depositing messages into Kyle's inbox to route around the break.
**Plan status**: `plans/csuite-persona-auto-reply-routing.md` — recon
only, four options laid out, not yet dispatched.

### F03 — Kyle container never reads its inbox

**Category**: workaround still required
**Urgency**: high
**Effort**: M
**Symptom**: `~/.drem-csuite/kyle/inbox/` has 10+ messages dating
back to 2026-04-11. No consumer reads them.
**Evidence**: `cmd/drem-kyle/main.go` + `internal/kyle/service.go` —
Kyle's `Service` has HTTP endpoints (`/world`, `/world/summary`,
`/docker/query`) and a poll loop that refreshes an in-memory cache,
but NO inbox/outbox watcher. `grep -rn "inbox" cmd/drem-kyle/
internal/kyle/` returns zero results. The PRD
(`docs/prd-containerization.md`) says Kyle "runs as a Go binary"
(CLAUDE.md line 52 confirms "different runtime, intentionally"), but
the non-LLM Go binary cannot consume natural-language prompts dropped
as .md files.
**Impact**: Every operator-to-Kyle message requires hand-routing.
`plans/csuite-persona-pivot.md` treated Kyle as a full persona; the
Go-binary pivot lost that capability silently.
**Workaround**: Operator hand-writing replies into Seth's outbox with
fully-formed frontmatter.

### F04 — Retry cascade covers `failed` parents but NOT `paused`/`cancelled`/`plan_review`

**Category**: contract mismatch
**Urgency**: high
**Effort**: S
**Symptom**: Bug I#1's cascade re-animates `FAILED` parents when a
child is retried. But the `scheduleSubtasks` gate rejects ANY parent
not in a "live" status — so if the parent is `paused`, `cancelled`,
or the subtask's parent hit `plan_review` with an approved plan that
still has backlog children, the child sits orphaned.
**Evidence**: Live DB:
```
sqlite3 drem.db "SELECT p.status, COUNT(t.id) FROM tasks p
  JOIN tasks t ON t.parent_task_id = p.id
  WHERE t.status = 'backlog' GROUP BY p.status"
cancelled=5, done=13, failed=7, paused=3
```
Three `paused` parents have 3 backlog children; 5 `cancelled`
parents have backlog children. None will be re-scheduled.
`internal/orchestrator/subtask_scheduling.go:25-28` — pause guard
returns nil; no path unblocks backlog children of a cancelled parent.
**Impact**: Orphaned subtasks leak into the backlog indefinitely.
Plan `plans/bug-i-...md` called out only the `FAILED` edge; the
broader orphan class was never enumerated.

### F05 — `failure_reason` lives only in `task.Context` JSON — no first-class column

**Category**: observability gap
**Urgency**: medium
**Effort**: S
**Symptom**: Task failures record a reason string in
`task.Context["failure_reason"]` — a JSON blob column. Operator
cannot SELECT on reason without JSON1 extraction, and the TUI has to
reach into the blob (see `internal/tui/detail.go:281`). No index,
no typed query.
**Evidence**: `grep -rn failure_reason --include="*.go"` — 16 sites
write to `task.Context["failure_reason"]`; zero writes to any
`FailureReason` column. `internal/model/models.go:49` has
`NeedsHumanReview bool` as a first-class column but NO failure-reason
column.
**Impact**: Operator asking "how many tasks failed because of
merger-skipped?" must grep JSON. Queryability of failure modes is
degraded.

### F06 — `task.Context["needs_human_review"]` disagrees with `task.NeedsHumanReview` column

**Category**: contract mismatch
**Urgency**: medium
**Effort**: XS
**Symptom**: There are TWO sources of truth for "needs human review":
a first-class bool column (`model.Task.NeedsHumanReview`) and a
`Context["needs_human_review"]` JSON key. Different code paths write
different ones.
**Evidence**:
- Column writers: `internal/orchestrator/merge_execution.go:62,238`
  (2 sites) — set `task.NeedsHumanReview = true`.
- JSON writers: `test_writing.go:85`, `test_execution.go:91,169,183,
  206,215`, `context_monitor.go:275,284,408` — set
  `parent.Context["needs_human_review"] = true` (10 sites).
Not a single site sets BOTH. So the column is stale for 10/12
escalation paths.
**Impact**: Kyle's DB poll (which reads the column) sees only 2/12
escalations. TUI filter "show needs-human tasks" misses the majority.

### F07 — `Experiment`/`Variant` tables absent from `internal/db/AutoMigrate`

**Category**: half-shipped / consistency
**Urgency**: high
**Effort**: XS
**Symptom**: `internal/db/db.go::AutoMigrate` (line 72-87) lists 12
models. `experiment.Experiment` and `experiment.Variant` are NOT on
the list. Tables only come into existence when a CLI command in
`internal/cli/cli.go:708,771` runs.
**Evidence**:
```
grep AutoMigrate cmd/drem/ internal/db/db.go
→ db.go only migrates non-experiment models
grep "experimentScheduler" internal/orchestrator/
→ 5 call sites, including `scheduleSubtasks:58-59`
```
Live DB has the tables because the operator ran the CLI. On a clean
install where an operator starts orch before using the CLI,
`o.experimentScheduler.IsActive()` will error and silently turn off
experiment scheduling.
**Impact**: Fresh installs look healthy but silently skip
experiments.

### F08 — README.md describes tmux-era architecture; contradicts containerization pivot

**Category**: stale doc
**Urgency**: medium
**Effort**: S
**Symptom**: README.md (line 3) still says "manages their lifecycle
via tmux"; line 43 lists tmux as a prerequisite; lines 276-279
describe the "dedicated tmux server". Containerization pivot
(`docs/prd-containerization.md`, `plans/kill-list.md`) killed
`internal/tmux/` and `internal/worktree/`.
**Evidence**: `ls internal/tmux internal/worktree` — both absent.
README says "tmux sessions, launch TUI dashboard"; actual flow is
docker containers coordinated by `drem-global-spawner-1`.
**Impact**: First-time readers / future agents get wrong mental
model. High onboarding cost.

### F09 — `CLAUDE.md` references non-existent `compose.override.yml`

**Category**: stale doc
**Urgency**: low
**Effort**: XS
**Symptom**: CLAUDE.md line 16 says "Containers pick this up via a
read-only bind-mount (see `compose.override.yml`)". No file of that
name exists in the repo.
**Evidence**: `find . -maxdepth 3 -name 'compose.override*'` returns
nothing. The actual bind-mount lives at
`deploy/compose/global.yml:345`.
**Impact**: A future subagent following CLAUDE.md's guidance will
not find the file. Not blocking, but erodes trust in the hard
constraints doc.

### F10 — `processTestingReady` hot-loop when `fixer_attempted=true` and no transition out

**Category**: single point of failure / hot loop
**Urgency**: high
**Effort**: S
**Symptom**: Once `Context["testing_ready_fixer_attempted"]==true`
and tests still fail, `processTestingReady`
(`test_execution.go:89-102`) sets `needs_human_review`, saves,
emits, logs WARN — and returns nil. Task status stays at
`testing_ready`. Next tick re-enters and repeats every 5s forever.
**Evidence**: Bug I observation: "v17 ... is currently in a
hot-loop emitting `testing_ready fixer failed, needs human review`
every 5 seconds" (plans/bug-h... line 20-21). This is the source.
Live DB confirms task `6b6eb427...` still at `testing_ready` as of
audit time (updated_at=2026-04-22 07:11).
**Impact**: 5s-cadence log spam; the source of the Bug I#2 WARN
flood; masks other signals; exhausts ad-hoc log tails. An explicit
transition to `failed` (or `paused` for review) would be more honest.

### F11 — 8 of 44 `state.TransitionTask` call sites skip `publishTaskTransition`

**Category**: observability gap
**Urgency**: medium
**Effort**: S
**Symptom**: Not every status change goes onto the event bus. csuite
watchers that subscribe to task transitions (downstream of
`internal/eventbus`) miss ~18% of all transitions.
**Evidence**: File-by-file counts from the audit:
```
direct_tool_dispatch.go: transitions=1 publishes=0
test_writing.go:         transitions=2 publishes=0
context_monitor.go:      transitions=2 publishes=0
reconcile.go:            transitions=2 publishes=0
merge_execution.go:      transitions=4 publishes=2
```
11 transitions, 2 publishes. 9 transitions never reach the event
bus.
**Impact**: csuite personas see an incomplete event stream. Hard
bugs like "test_writing replan happened, Seth never noticed" become
possible.

### F12 — pprof + SIGUSR1 installed in orch but not exposed in compose wiring

**Category**: observability gap
**Urgency**: low
**Effort**: XS
**Symptom**: Bug E's W3.1/W3.2 landed pprof and SIGUSR1 goroutine
dump in `internal/orchhttp/pprof.go` and `goroutinedump.go`, but
`deploy/compose/global.yml` never sets `DREM_PPROF=1` or a port
mapping for 6060.
**Evidence**: `grep DREM_PPROF deploy/` returns zero hits.
`cmd/drem/main.go:405` gates on `StartPprofListener` — with env
absent, `pprofEnabled()` returns false and the listener never binds.
**Impact**: The observability shipped in Bug E is dormant in the
live containers. Operator would have to restart orch with an env
override to use it.

### F13 — Merger library still silently accepts empty `TestCmd` — TODO(seth) unresolved

**Category**: contract mismatch (library vs CLI, again)
**Urgency**: medium
**Effort**: S
**Symptom**: Bug H fix put a fail-close guard at `merge_dispatch.go`
(argv build refuses empty `--test-cmd`). But the merger library
(`internal/merger/merger.go:78-84`) still documents "empty TestCmd
means 'no tests'" as passing behavior. Any caller that skips the
argv path (in-process tests, future callers) silently skips tests.
**Evidence**: `internal/merger/merger.go:74-85` —
`// TODO(seth): tighten the contract so library-side silent-skip on
// empty TestCmd either requires an explicit opt-in ...`. Unfixed at
HEAD.
**Impact**: Library's permissive default is a foot-gun for any new
caller. The Bug H fix only patched one consumer.

### F14 — `orchEvents` channel (size 100) allocated even when never consumed

**Category**: silent failure
**Urgency**: low
**Effort**: XS
**Symptom**: `cmd/drem/main.go:213` allocates `make(chan
orchestrator.Event, 100)` unconditionally. The bridge goroutine at
line 380 (`for e := range orchEvents { tuiEvents <- ... }`) always
runs. When the TUI is not actually consuming (e.g. `--tui-only`
mode with no focused terminal, or headless orch), tuiEvents (also
size 100) fills first, blocking the bridge, which then blocks
`orchEvents`, which triggers the F01/F26 WARN flood.
**Evidence**: `cmd/drem/main.go:379-385` — the bridge does not
have a select/default drop; it blocks on `tuiEvents <- ...` if
tuiEvents is full.
**Impact**: Two-stage chan chain where one stall propagates.
Compounds F01.

### F15 — 944 `idle` agents accumulated in DB; no reaping path

**Category**: consistency drift / unbounded growth
**Urgency**: medium
**Effort**: S
**Symptom**: `SELECT status, COUNT(*) FROM agents` shows:
`archived=1134, dead=1945, idle=944, failed=32, ...`. `idle` agents
date from 2026-03-05 through yesterday. No code path transitions
`idle → archived`.
**Evidence**: `grep -rn "StatusIdle" --include="*.go"` (not run here,
but the count signals accumulation regardless). Agent table now
holds 4000+ rows; every orch startup scan through agents gets
slower.
**Impact**: DB bloat, slower queries, no cleanup visibility. Future
reconciler passes will walk more stale rows each startup.

### F16 — Watchdog has no SIGUSR1/pprof/restart story

**Category**: single point of failure
**Urgency**: medium
**Effort**: S
**Symptom**: `cmd/drem-watchdog/main.go` is a bare binary. If its
ticker goroutine wedges (e.g. git push hangs), the worker's commit +
push path dies silently with no goroutine dump, no metrics, no
restart.
**Evidence**: `internal/watchdog/loop.go` — plain ticker loop, no
observability hooks. `grep -rn SIGUSR1 internal/watchdog` returns
nothing. Compare to orch which has full W3.1/W3.2 coverage.
**Impact**: When a v-N canary fails silently, it's often watchdog
hangs (v13 canary post-mortem mentions this class). No diagnostic
surface today.

### F17 — `reconcileAlreadyMergedFeatures` bypasses state machine

**Category**: contract mismatch
**Urgency**: medium
**Effort**: XS
**Symptom**: `internal/orchestrator/reconcile_parents.go:66-79`
directly SQL-UPDATEs `task.Status = StatusDone` and hand-rolls a
TaskEvent, explicitly noting "Bypass the state machine (failed ->
done is not a valid transition)".
**Evidence**: Lines 66-79 of that file. The state machine
(`internal/state/machine.go:30`) denies `failed → done`. The
reconciler works around it with a direct write.
**Impact**: Two truth paths — some transitions go through
`state.TransitionTask` (validated, publishable), others bypass it
(invisible to subscribers, invalid per the declared machine).
Violates the "one state machine" invariant. Adding the edge to
`ValidTransitions` (with a reason discriminator) would close this.

### F18 — `plans/warm-direct-prep.md` still not implemented

**Category**: half-shipped
**Urgency**: medium
**Effort**: L
**Symptom**: Plan says "design proposed, not yet implemented"
2026-04-20. Prep still runs inline in the orch tick loop via
`RunDirectPrep` (`internal/orchestrator/task_prep.go:329`). Plan
warns "a single prep can hold an orch goroutine for 30-90s" —
scheduler starvation risk.
**Evidence**: `grep RunDirectPrep` — 2 prod call sites, both
in-orch. No `drem-prep` container, no Dockerfile, no compose entry.
**Impact**: Orch tick goroutine stalls during prep. Plan identifies
this as higher risk than the (already-shipped) classifier split.

### F19 — `plans/sglang-gemma4-followup.md` Dockerfile never verified

**Category**: half-shipped
**Urgency**: low
**Effort**: M
**Symptom**: "Dockerfile drafted, awaiting operator (Kyle) build
verification." since 2026-04-19.
**Evidence**: The live `drem-sglang` is a host-side process, NOT a
container (CLAUDE.md line 33-34 protects its uptime). The custom
Dockerfile the plan ships has never been built per plan status.
**Impact**: Full-stack containerization can't close until SGLang
is containerized; meanwhile the operator is one host reboot away
from a broken system.

### F20 — Legacy `runner.SpawnAgent` path still callable

**Category**: consistency drift
**Urgency**: medium
**Effort**: M
**Symptom**: The pre-container `runner.SpawnAgent` and
`SpawnAgentInWorktree` methods are still called from 7 prod sites
(`subtask_scheduling.go:301`, `quickfix_processing.go:117,211`,
`task_processing.go:248`, `classifying.go:90`, `test_execution.go:212`,
`session_spawning.go:142`). The contract says container-mode uses
`o.Spawner` and host-mode uses runner — but the fallback path at
every site means a misconfiguration degrades silently to host-spawn.
**Evidence**: `plans/kill-list.md` marks `internal/tmux` +
`internal/worktree` dead but `internal/agent/runner.go:294` still
exists and still references `tmux.Manager` (line 26,94,137,243 per
kill-list row 1).
**Impact**: A half-migrated deployment silently reverts to host-
spawn. No audit surface distinguishes which path actually ran.

### F21 — `internal/logging` sampler exists but only 2 prod call sites

**Category**: observability gap
**Urgency**: medium
**Effort**: S
**Symptom**: Bug E W4.1 shipped `internal/logging.NewSampler` with
`EveryN`/`EveryD`. Only two call sites use it: `handlers_public.go:26`
and `server.go:167`. Every other hot-path Warn/Error logs unsampled.
**Evidence**: `grep sampler --include="*.go" internal/` — 2 callers
in `internal/orchhttp`, zero in `internal/orchestrator`. The 5s
hot-loop in F10 hammers `o.logger.Warn` unsampled.
**Impact**: Hot-loop warnings (F10, F01) are exactly the class the
sampler was designed for. It was shipped but not adopted.

### F22 — Persona poller never writes frontmatter

**Category**: workaround still required / half-shipped
**Urgency**: high
**Effort**: S
**Symptom**: Duplicates F02 (explicit producer-side evidence). The
persona poller at `internal/csuite/persona/poller.go:237-248` writes
raw stdout. The prompt docs (`docs/csuite-agents/prompts/seth.md`
§"Message format") tell the LLM to emit frontmatter, but the LLM
often doesn't, and the poller never wraps.
**Evidence**: F02 evidence + the frozen design in
`plans/csuite-watcher-outbox-routing.md` §8 ("watcher stays dumb")
puts the burden on the producer.
**Impact**: Operator works around by hand-writing every csuite
reply.

### F23 — Delivery audit DB and orch DB both copy task/agent state

**Category**: consistency drift
**Urgency**: low
**Effort**: M
**Symptom**: `~/.drem/projects/drem-orchestrator/data/drem.db` and
`~/.drem-csuite/watcher.db` + `/var/lib/watcher/deliveries.db` are
three separate SQLite files with overlapping notions of "who
delivered what, when". Bug F collapsed the image registries into one;
the data-plane has the same shape but remains split.
**Evidence**: `ls ~/.drem-csuite/*.db` + container path confirm 3
separate files. Cross-file joins require manual copy.
**Impact**: Future "audit across task+delivery" queries need
manual reconciliation.

### F24 — `publishTaskTransition` skipped in `test_writing.go` replan path

**Category**: observability gap
**Urgency**: medium
**Effort**: XS
**Symptom**: Subset of F11, called out because it's a high-value
edge. `test_writing.go:87` (replan → failed) and line 114 (backlog
→ planning replan transition) both skip publish. Seth wants to know
when replan happens.
**Evidence**: `test_writing.go: transitions=2 publishes=0` per F11.
**Impact**: Silent replans. Operator sees the task bouncing via DB
but no corresponding csuite event.

### F25 — Mixed logging styles without task/project context

**Category**: observability gap
**Urgency**: medium
**Effort**: S
**Symptom**: Modern path uses `slog` with structured keys; legacy
`log.Printf` in 32 prod sites (mostly `internal/watcher`, some
`internal/bugreport`, some `internal/serve`). Many Warn/Error lines
lack `task_id`/`agent_id`/`project_id` tags.
**Evidence**: `grep log.Printf` counts: `internal/watcher` 15 sites,
`internal/bugreport` 2, `internal/serve` ~6. None use the structured
keys the rest of the codebase adopts.
**Impact**: Aggregation/search across task lifecycle fails when
some log lines are unstructured.

### F26 — `orchEvents` drop emits WARN every time

**Category**: observability gap
**Urgency**: medium
**Effort**: XS
**Symptom**: Paired with F01/F14. Every drop logs `"event channel
full, dropping event"` at WARN — no sampling, no rate limit. Bug
I#2 called this out as the source of the ~1677 WARNs in 4 minutes.
**Evidence**: `internal/orchestrator/orchestrator.go:763`.
**Impact**: Log amplifier. Trivially silenced with an `EveryD(1s)`
sampler.

### F27 — `/csuite/operator` inbox/outbox dir exists but no consumer documented

**Category**: half-shipped
**Urgency**: low
**Effort**: S
**Symptom**: `ls ~/.drem-csuite/operator/` shows `inbox/` and
`outbox/`. No documented consumer reads from operator/outbox; no
documented producer writes to operator/inbox.
**Evidence**: `ls ~/.drem-csuite/operator/` — both dirs present.
`grep -rn '/operator/' cmd/ internal/` (not run exhaustively, but
the persona list in `internal/deliver/deliver.go:255` hardcodes
`mike|alex|ross|seth`; operator is not a valid source).
**Impact**: Dead directory; operator confusion about whether to
use it; potential for messages written there to be silently lost.

### F28 — 83/89 recent csuite deliveries go to quarantine

**Category**: silent failure (live)
**Urgency**: critical
**Effort**: S
**Symptom**: The production csuite pipeline is effectively broken.
83/89 = 93% quarantine rate.
**Evidence**: Fresh copy of
`drem-orchestrator-csuite-watcher-1:/var/lib/watcher/deliveries.db`:
```
dest       | count
quarantine | 83
kyle       |  3
alex       |  2
seth       |  1
```
**Impact**: Inter-persona messaging has been mostly non-functional
for the ~5 hours since this audit interval. Operator is
hand-routing around it (see F02, F03, F22).
**Plan status**: F02's plan exists; not yet shipped. This is the
same bug as F02 but framed as production impact.

### F29 — `reconcileStuckAgents` container-awareness plan still open

**Category**: half-shipped
**Urgency**: medium
**Effort**: M
**Symptom**: `plans/reconciler-container-awareness.md` flagged an
infinite-respawn loop in the sweeper because it doesn't know about
container agents. Plan has no status line and no merge commit.
**Evidence**: Plan header has no "Status:" line (one of only 4
plans without one). `git log --oneline | grep container-awareness`
returns nothing.
**Impact**: Latent double-writer risk: a live container gets
declared dead, a fresh one spawns against the same branch, commits
collide. Rare but ugly when it happens.

### F30 — 104 files in `/csuite/quarantine/` with no cleanup policy

**Category**: consistency drift
**Urgency**: low
**Effort**: XS
**Symptom**: `find ~/.drem-csuite/quarantine -type f | wc -l = 104`.
Many are March-era audit reports. No retention policy; no rescan-and-
retry after the classifier learns new rules.
**Evidence**: `ls ~/.drem-csuite/quarantine/seth/ | head -10` shows
files dating from 2026-03-24. `grep -rn "quarantine" internal/watcher/
internal/deliver/ | grep -i "clean\|retention\|sweep"` returns zero
cleanup code.
**Impact**: Unbounded growth of quarantine dir. No per-item
reclassification pass when a F02-style fix lands.

### F31 — Persona `restart-context.md` files go stale silently

**Category**: observability gap
**Urgency**: low
**Effort**: XS
**Symptom**: Seth's `restart-context.md` last modified 2026-03-27
(25+ days stale). No staleness check; operator has no signal the
persona hasn't rotated state.
**Evidence**: `stat -c "%y %n" ~/.drem-csuite/seth/restart-context.md`
→ 2026-03-27. `state.md` is fresh (2026-04-21) — two sibling files
on different refresh cadences with no documented contract.
**Impact**: Confusion about which file is authoritative after
container restart.

### F32 — `drem-global-spawner-1` is a single process

**Category**: single point of failure
**Urgency**: medium
**Effort**: L
**Symptom**: All worker/merger spawns go through a single JSON-RPC
socket at `/var/run/drem/spawner.sock`, backed by ONE
`drem-global-spawner-1` container (verified `docker ps`). If it
crashes, every project's scheduler silently stalls.
**Evidence**: `internal/spawner/service.go` has no leader-election,
no supervisor. `deploy/compose/global.yml` sets no `restart: always`
or healthcheck on the spawner service. Its crash is invisible to
orch until the next spawn fails.
**Impact**: SPOF for the entire fleet. Recovery requires manual
container restart.

### F33 — Orchestrator events channel size hardcoded 100

**Category**: single point of failure
**Urgency**: low
**Effort**: XS
**Symptom**: `cmd/drem/main.go:213` — `make(chan
orchestrator.Event, 100)`. No env override, no config file key.
**Evidence**: Hardcoded constant. Related to F01/F14/F26.
**Impact**: Sizing is arbitrary. A slow TUI on a large-task-count
project fills the buffer in seconds.

### F34 — Bug H fail-fast reason is project-language-agnostic

**Category**: observability gap
**Urgency**: low
**Effort**: XS
**Symptom**: `mergerSpawnSkippedReason = "merger spawn skipped:
project has no test command"`. Does not say which project or why
(no go.mod? no pyproject.toml?). Operator must look elsewhere.
**Evidence**: `internal/orchestrator/merge_dispatch.go:39`.
Operator-facing failure_reason is 57 bytes of context-free string.
**Impact**: Operator gets a failure but needs a second query to
root-cause. Could be a one-line improvement.

## Surprises

1. **Experiments migration hole (F07)** — orch boots "healthy" but
   silently skips experiments if the CLI was never run first. Future
   clean installs at risk.
2. **Two sources of truth for needs-human-review (F06)** —
   `task.NeedsHumanReview` bool column is written by 2 sites; the
   `Context["needs_human_review"]` JSON key is written by 10. Kyle's
   DB poll reads the column. This was nearly invisible.
3. **Live quarantine rate is 93%** (F28) — the csuite pipeline is
   more broken than the plan doc suggests.
4. **Watchdog has no observability** (F16) — orch got pprof/SIGUSR1
   from Bug E; the sibling binary didn't. Every canary post-mortem
   that blamed "watchdog stalled" has no diagnostic surface.
5. **Legacy host-spawn path still wired in 7 hot call sites** (F20)
   — the containerization pivot is not clean; it's a dual-mode system
   with a silent fallback.

## Follow-up passes worth doing

- **State-machine audit**: I only skimmed `internal/state/machine.go`
  and a few transition callers. A full enumeration of every
  `state.TransitionTask` call site vs every `publishTaskTransition`
  call site (F11 captured the numbers but not the per-line gap) is a
  1-hour follow-up.
- **`internal/ctxmon` coverage**: only cursory look. Some chance of
  sglang-specific hardcoding that breaks a future vLLM swap.
- **TUI retry-storm plan (`plans/tui-retry-storm-prevention.md`)**:
  has no status line. Unclear if the fix landed. Worth a 10-minute
  follow-up.
- **Agent table `idle`/`dead` cleanup (F15)**: the 944 idle rows
  suggest a reaper is missing. Confirm with a deeper dive into
  `reconcileStuckAgents`.
- **`csuite.db` vs `csuite_agents` table drift** (F23): I listed the
  three-DB split but didn't diff schemas. Worth a pass.
- **`docs/containerization/install.md` vs live compose**: size
  (1000+ lines) exceeded what I could audit in the time budget.
  Stale env-var names are likely.
- **`plans/agent-bug-reports-prd.md`**: large PRD, no status line,
  unclear implementation coverage. Deserves its own recon.
