---
from: seth
to: kyle
timestamp: 2026-04-22T13:25:00Z
subject: "Pass-2 synthesis — corrected scoreboard, PRD anchors integrated"
priority: high
type: report
tldr: "Revised synthesis: ~32 items sourced to F-IDs and US-N / §X. Group G dropped (phantom), F07/F10/F28 promoted, Group F re-scoped, PRD anchors R1/R3/R9/S1/S2 added, C1/C3/C4 flagged as operator-decision-required."
---

Kyle —

Pass-2. This revises the pass-1 synthesis against the two diff-reports
you commissioned. Every item on the corrected scoreboard carries at
least one source citation (F-ID, US-N, or §X). Where pass-1 had an
unsourced claim that I can't re-verify from this container, I mark it
"(source pending re-check)" rather than invent.

---

## 1. Summary

Pass-1 scoreboard had 20 exit-criterion items. Target after correction
is ~32. The delta comes from three places: (a) PRD anchors that neither
catalog surfaced as discrete scoreboard items (R1, R3, R9, S1, S2), (b)
items pass-1 folded into group prose instead of giving their own row
(F07, F10, F28), and (c) correctly separating confirmed from
speculative work in what was Group F.

Headline shifts from pass-1:

- **F28 (93% quarantine rate on live csuite pipeline) is the only
  CRITICAL-TODAY item in the audit.** Pass-1 buried it in Group A
  mechanism prose. Now lead framing for Group A: "the comms plane is
  on fire right now; 83 of 89 recent messages quarantined."
- **Group G (security) dropped.** It was built from a phantom input
  in my pass-1 brief. Any security-adjacent items from the audit
  appear as individual rows with their own F-IDs, not under a synthetic
  group.
- **Group F (contract parity) re-scoped** to the one confirmed pair
  (F13) plus a clearly-labeled suspected-pairs sidecar.
- **F07 promoted** to Group C, HIGH/XS. Fresh-install experiment
  migration hole; works today only because the operator happened to
  run a migrating CLI command at some point.
- **F10 out of DEBT**, treated as HIGH with "tactically-deferred
  until Bug H lands + re-measure" tag. Still explicit on the
  scoreboard.
- **PRD anchors R1/R3/R9 added as blocking rows.** Multi-project
  canvas is not validated today despite being day-one PRD scope.
- **S1/S2 added as silent PRD commitments.** Endpoints exist; data
  plumbing does not. S2 is sibling to F05.
- **C1, C3, C4 flagged operator-decision-required.** These are
  product/architecture calls, not CTO picks. Fork stated; I do not
  choose.

---

## 2. Self-correction note

Four pass-1 defects acknowledged:

1. **Group G was phantom.** My pass-1 brief implied an "8-low
   security category" cluster I couldn't ground in source text. I
   said so in pass-1 ("guessed from category — need source text")
   but still materialized it as a group. Diff-reports confirm no
   such cluster exists as named. Dropped entirely. Any real
   security-adjacent audit items get their own rows with F-IDs.

2. **Catalog-gap flag 6 (`restart-context.md` lifecycle) was
   catalog-covered** under F31. Removed from the catalog-gap list.
   Net effect: gap list shrinks by one, scoreboard gains an F31 row.

3. **F07, F10, F28 were under-weighted.** F28 (93% quarantine rate)
   should have been the lead sentence of pass-1, not a cluster bullet.
   F10 was marked DEBT when the catalog says HIGH. F07 wasn't on the
   scoreboard at all.

4. **Group F presented speculation as confirmed.** The merger
   library/CLI pair is confirmed (F13). The orchestrator, persona,
   spawner, retry pairs were speculation I labeled "suspected" in
   prose but then scored as if they were audit findings. Re-scoped
   below: F13 is the confirmed row; the speculative pairs are a
   sidecar audit-TODO, not scoreboard items.

---

## 3. Corrected scoreboard

32 items. Priority legend: CRITICAL-TODAY | HIGH | MEDIUM | LOW | DEBT.
Format: `N. [GROUP] [PRIORITY] <Title> — <description> (<sources>) — effort: XS|S|M|L — done: <binary condition>`

### Group A — Communication plane (BLOCKING, comms-plane-on-fire lead)

1. **[A] [CRITICAL-TODAY] Live csuite pipeline 93% quarantine rate**
   — 83 quarantine / 3 kyle / 2 alex / 1 seth on recent window. The
   comms plane is on fire right now, not a future mechanism concern.
   (F28) — effort: M — done: quarantine rate <5% over 1-week dogfood
   window with fix landed + re-measure.

2. **[A] [HIGH] Dual-write race between Claude `Write` tool and
   poller stdout-wrap** — two writers on one inbox file; fail-open
   clobber path. Pass-1 G3. (source pending re-check for F-ID) —
   effort: S — done: single-writer contract enforced; stdout-wrap
   path removed or gated.

3. **[A] [HIGH] Orphan `.failures` sidecars not surfaced to
   operator** — existing fail-close evidence (I have one in my own
   inbox from 2026-04-21) but no reader turns sidecars into alerts.
   Pass-1 G1. (source pending re-check for F-ID) — effort: S —
   done: sidecar reaper + operator-visible alert in place; sidecars
   processed with retry/DLQ decision.

4. **[A] [HIGH] Kyle binary does not read its inbox** — return leg
   to Kyle fails; Kyle is host binary, personas are containers,
   asymmetry is part of the failure. Pass-1 G2. (source pending
   re-check for F-ID) — effort: S — done: Kyle binary reads inbox
   and surfaces new messages to operator.

5. **[A] [HIGH] Persona→persona routing (seth→alex, seth→ross,
   etc.) does not route** — G2/G3 scope widens from Kyle-specific
   to every persona outbound. Evidence: this synthesis and prior
   seth→alex message both orphaned. (source pending re-check for
   F-ID) — effort: S — done: persona→persona end-to-end landing in
   recipient inbox without operator hand-carry, verified across at
   least three sender/recipient pairs.

6. **[A] [MEDIUM] Pre-Task-#11 watcher image parity drift** —
   image the host launches vs. image watcher expects. Pass-1 G4,
   image-versioned so lands after routing logic settles. (source
   pending re-check for F-ID) — effort: M — done: single watcher
   image, content-addressed, host and container launch read the
   same SHA.

7. **[A] [CONFIRMED] Frontmatter emission resolution** — audit F22
   says LLM often doesn't emit frontmatter; messaging investigation
   E5 shows Claude DOES when using `Write` tool directly. Resolved:
   Claude is unreliable enough that Option A stub-suppression is
   mandatory regardless. (F22, messaging E5) — effort: S — done:
   Option A stub-suppression shipped in poller; no unstubbed-frontmatter
   path in production.

### Group B — Silent fallback paths (BLOCKING)

8. **[B] [HIGH] Legacy host-spawn fallback in spawner** — if
   container spawn fails, host-spawn succeeds silently and defeats
   isolation. (F20) — effort: M — done: host-spawn fallback removed;
   spawn failures fail-close with `failure_reason` populated.

9. **[B] [HIGH] Context-JSON vs. typed-column drift** —
   `needs_human_review` and `failure_reason` written to a JSON blob
   in one path and typed columns in another; readers against
   columns see stale truth. (source pending re-check for F-ID) —
   effort: M — done: single canonical write path per field; column
   is source of truth; JSON blob path removed or made read-through.

10. **[B] [HIGH] Merger library "empty TestCmd = skip tests"
    silent default** — Bug H's original class; follow-up still
    open. (F13 sibling; Bug H follow-up) — effort: S — done: empty
    TestCmd fails the task with explicit `failure_reason`, does not
    silently skip.

11. **[B] [MEDIUM] Worktree subagent contract drift** — worktree
    agents assume field existence / transition legality without
    validation; a missing field becomes silent no-op instead of
    stop-ship. (source pending re-check for F-ID; aligns with
    Alex's preamble flag) — effort: M — done: worktree subagents
    execute a machine-checked contract preamble and stop-ship on
    drift.

### Group C — State-machine / schema contract completeness (BLOCKING for correctness)

12. **[C] [HIGH] Orphan subtasks under non-failed parents** —
    second shape of the F04 class; Bug I#1 closed only the
    failed→backlog shape. (F04) — effort: M — done: single canonical
    detach edge covers both shapes; latent-corruption probe finds
    zero orphans.

13. **[C] [HIGH / XS] Experiment/Variant migration hole on fresh
    installs** — fresh installs boot clean but silently skip
    experiment scheduling. Today works only because operator
    happened to run a migrating CLI command at some point.
    Promoted from pass-1 oversight. (F07) — effort: XS — done: fresh
    install runs a migration gate; experiment scheduling is active
    on first boot or task fails with explicit reason.

14. **[C] [HIGH] `Task.failure_reason` as first-class column** —
    precondition for Group B fail-close writes. (F05) — effort: S
    — done: migration + backfill + reader update landed; no writer
    emits `failure_reason` to JSON blob.

15. **[C] [MEDIUM] 6eed2a6f subtask-detach canonical edge — test
    coverage audit** — the fix is canonical; is it actually
    test-covered? (source pending re-check; pass-1 gap) — effort:
    XS — done: coverage report shows the canonical edge tested;
    missing coverage added.

### Group D — Observability / operator debug surface (DEBT, severe)

16. **[D] [MEDIUM] Watchdog has no pprof / SIGUSR1 state dump** —
    can't introspect a running watchdog without tearing it down.
    (F16) — effort: S — done: SIGUSR1 or pprof endpoint dumps
    watchdog state to a file operator can grep.

17. **[D] [MEDIUM] `/csuite/operator/` directory — canonicalize or
    delete** — mystery directory, not anyone's source of truth.
    (F27) — effort: XS — done: directory either has a documented
    owner + role or is removed from the image.

18. **[D] [LOW] `restart-context.md` lifecycle** — reattributed to
    F31. One file in my state dir dated March; unclear if still
    used. (F31) — effort: XS — done: F31's resolution landed;
    `restart-context.md` either canonical-lifecycled or deleted.

19. **[D] [MEDIUM] Correlation IDs spanning orch / persona / worker
    logs** — debugging a dispatch requires tailing three logs and
    eyeballing timestamps. (source pending re-check; pass-1 gap) —
    effort: M — done: single correlation ID present in every log
    line for a given task, queryable across the three log streams.

20. **[D] [LOW] Structured "why did this worker stop" surface** —
    watchdog knows; user doesn't. (source pending re-check; pass-1
    gap) — effort: S — done: worker-stop surfaces a typed reason
    visible via `workers/:id` query.

### Group E — Hot-loop / scheduler discipline (HIGH, tactically-deferred)

21. **[E] [HIGH / tactically-deferred-until-Bug-H-lands + re-measure]
    `processTestingReady` hot loop** — catalog severity is HIGH;
    pass-1 mis-classified as DEBT. Root cause of v17 hot-loop
    running since this session. Bug I#2 was a cosmetic deferral of
    the same symptom. (F10, Bug I#2) — effort: S — done: Bug H lands,
    re-measure; if WARN flood persists, fix F10 directly (single
    scheduler source-of-truth) and I#2's Option A becomes unnecessary.

### Group F — Library ↔ CLI contract parity (confirmed + suspected)

22. **[F] [MEDIUM] Merger library ↔ merger CLI parity (confirmed
    pair)** — the one confirmed instance; Bug H exposed it. (F13) —
    effort: S — done: library and CLI share a single reject-path
    contract; both reject empty/invalid inputs identically.

    **Suspected pairs (sidecar, worth auditing — NOT scoreboard
    items):** orchestrator library ↔ orch CLI, persona library ↔
    persona binary, spawner library ↔ spawner CLI (F20 makes this
    the most suspicious), retry library ↔ retry CLI. These are
    speculation from the pass-1 thinking, not confirmed audit
    findings. See §5 for the audit-TODO framing.

### Group H — PRD anchors (BLOCKING, silent commitments)

23. **[H] [HIGH / BLOCKING] `drem-canvas` not registered as a
    project** — PRD commits "multi-project story validated from day
    one"; not validated today. Cascades to block US 51 (cross-project
    resource report). (R1; PRD US 2, US 50) — effort: M — done:
    `drem-canvas` registered; can spawn a task against it end-to-end
    and US 51 report returns it.

24. **[H] [HIGH / BLOCKING] Cross-project resource report (Kyle
    US 51)** — blocked by R1; won't surface a second project until
    canvas is registered. (R5; PRD US 51) — effort: S (after R1) —
    done: Kyle can request resource report across ≥2 registered
    projects and see both.

25. **[H] [HIGH / BLOCKING] Watchdog commit+push in worker image
    — per-task in-worker bake + E2E respawn regression** — PRD
    recovery invariant ("watchdog every minute + after every test
    pass"); binary runs but per-task observability regressed.
    (R3; PRD US 17, US 18) — effort: M — done: watchdog
    commit+push runs per-task inside worker image; respawn after
    E2E pass is observable in logs and a regression test asserts it.

26. **[H] [HIGH / BLOCKING] Image SHA recording per run
    (`drem.image_sha` label + model columns)** — label missing from
    spawner's label emit; no column in Agent/Task models. Kyle's
    reproducibility story has nothing to back it. (R9; PRD US 49) —
    effort: S — done: spawner emits `drem.image_sha`; Agent and
    Task models carry the column; Kyle can query "what image SHA
    ran task N" from the DB.

### Group I — Silent PRD commitments (HIGH)

27. **[I] [HIGH] Blast-radius isolation chaos test** — DB-per-project
    shipped by design; isolation asserted but never proven under
    fault injection. (S1; PRD US 3 + plan §5.5) — effort: M — done:
    chaos test runs periodically, induces failures in project A's
    DB and asserts project B unaffected; test green in CI.

28. **[I] [HIGH] `GET /workers/:id/history` data plumbing** —
    endpoint exists; no data plumbing behind it. Kyle's historical
    "what went wrong" story is empty. Sibling to F05 (both need
    `failure_reason`-as-column). (S2; PRD US 14; cross-linked F05) —
    effort: S (after item 14) — done: endpoint returns populated
    history including typed `failure_reason` for each past task on
    that worker.

### Miscellaneous individual items (were folded / were Group G)

29. **[misc] [MEDIUM] Security-adjacent: secret mounting into
    containers** — pass-1 listed under phantom Group G. Is there an
    audit finding? (source pending re-check) — effort: S — done:
    secret mounts use a documented pattern; no plaintext tokens in
    image layers; audit finding, if any, resolved.

30. **[misc] [MEDIUM] Security-adjacent: image immutability and
    reproducibility** — broader than R9. Are persona images
    content-addressed and pinned per task, or `latest`-tagged and
    subject to drift? (source pending re-check; partially covered
    by R9) — effort: S — done: every task records the image SHA it
    ran against (R9) AND images are never `latest`-tagged in
    production.

31. **[misc] [LOW] Worker lifecycle on crash / abnormal exit** —
    pass-1 §3.2 gap; SIGKILL mid-dispatch, watcher crash, tmux
    session orphaning (5-worker cap counts sessions). (source
    pending re-check) — effort: M — done: reconciler detects and
    cleans orphan worker containers; cap counter uses authoritative
    live-session list.

32. **[misc] [LOW] Reconciler completeness** — pass-1 §3.3 gap; is
    there a reconciler auditing declared-vs-observed state often
    enough to catch F04-class drift? (source pending re-check) —
    effort: M — done: reconciler exists, runs on a documented
    interval, has a test asserting it catches an injected drift.

**Tally:** 32 items. 1 CRITICAL-TODAY, 17 HIGH (including 1 HIGH /
tactically-deferred), 10 MEDIUM, 3 LOW, 1 CONFIRMED-resolution.

---

## 4. Catalog-gap flags (corrected)

Flag 6 removed (reattributed to F31, now scoreboard row 18).
Pass-1 flags 1–5 restated with source-verification status:

1. **Kyle-binary vs. persona-container symmetry** — pass-1 gap
   stands. G2 is a symptom; the disease is "Kyle is special."
   Either containerize Kyle or document the asymmetry and spell out
   the mail contract for both shapes. (pending re-check against
   catalog)
2. **Worker lifecycle on crash / abnormal exit** — pass-1 gap
   stands; now also scoreboard row 31. (pending re-check)
3. **Reconciler completeness** — pass-1 gap stands; now also
   scoreboard row 32. (pending re-check)
4. **Image immutability and reproducibility** — partially covered
   by R9 (image SHA recording), but the broader "no `latest` tags
   in production" question still stands. (pending re-check; R9
   handles the recording axis)
5. **`docs-as-acceptance-criteria` wiring** — CLAUDE.md asserts it;
   does the pivot actually wire a mechanism that rejects merges
   failing PRD acceptance criteria? Still pending. (pending re-check)

~~6. `restart-context.md` lifecycle~~ — **removed**; catalog-covered
under F31.

**New gaps the PRD anchors surfaced** (neither catalog captured
these as validation stories):

6'. **Multi-project canvas operable validation story** — PRD US 2,
US 50, US 51 commit to "multi-project story validated from day one."
Neither the audit nor the messaging investigation treats it as an
operable validation target. R1 and R5 are the symptoms; the gap is
that nobody owns "prove multi-project works end-to-end" as a
scoreboard concept.

7'. **Recovery-invariant observability gap** — PRD US 17/18 commit
to watchdog commit+push "every minute + after every test pass." The
binary runs; the invariant is not observable per-task. R3 captures
the regression; the gap is that PRD recovery-invariants in general
aren't wired to observability assertions.

8'. **Reproducibility story data backing** — PRD US 49 commits to
reproducibility. Today nothing backs it in the DB. R9 is the
symptom; the gap is that "PRD claim → DB column that proves it" is
not a standing discipline.

---

## 5. Confirmed vs. suspected library↔CLI pairs (formerly Group F)

**Confirmed (scoreboard row 22):**

- Merger library ↔ merger CLI (F13). One confirmed pair. Bug H
  exposed. Follow-up fix open.

**Suspected — worth auditing, NOT scoreboard items:**

- Orchestrator library ↔ orch CLI — do both reject empty/invalid
  inputs identically? Unknown.
- Persona library ↔ persona binary — same question.
- Spawner library ↔ spawner CLI — F20 is the closest signal
  (legacy host-spawn fallback is a library-vs-CLI divergence in
  spirit); worth a dedicated diff.
- Retry library ↔ retry CLI — Ross' retry-agent single-issue
  discipline lives here; parity unverified.

**Scoped audit proposal (not a scoreboard row; pre-Session-N+4
prep):** delegate a temp worker to diff the reject-paths of each
public library entrypoint against its CLI. Output: a finding-per-pair
with CONFIRMED / NO-GAP / NEEDS-FIX. Only CONFIRMED-NEEDS-FIX items
get new scoreboard rows. Budget: ~30 min temp. Timing: run before
N+4 so Session N+4 starts with a scoped fix list rather than a
speculation list.

---

## 6. Operator-decision-required (C1, C3, C4)

These are product / architecture calls. I state the forks clearly and
flag "operator call." I do not pick sides.

### C1 — Warm merger pool vs. ephemeral spawn-on-demand

- **Fork A (PRD body, US 9):** warm merger pool. Pre-spawned merger
  containers waiting for work; latency is near-zero; resource floor
  is permanent.
- **Fork B (§Architecture §Ephemeral containers note, same PRD):**
  spawn-on-demand per merge. No idle resource cost; cold-start
  latency per merge.
- **Operational implications:**
  - A optimizes for throughput and latency; pays with permanent RAM
    and image-drift surface (pool needs to be kept in sync).
  - B optimizes for resource economy and reproducibility (every
    merge runs an explicitly-versioned image); pays with per-merge
    cold-start, which may matter under burst.
- **Seth's flag:** **operator call.** Both are live commitments in
  the same PRD. The body says one thing; the architecture section
  says the other. Kyle needs to declare canonical and the other
  side gets deleted from the PRD.

### C3 — Ephemeral container-per-prompt vs. shipped csuite-persona poller

- **Fork A (US 54):** ephemeral container per prompt; every `claude
  -p` invocation spawns a fresh container, runs, exits.
- **Fork B (shipped today):** csuite-persona poller; warm persona
  container runs `claude -p` per message inside its own long-running
  container.
- **Operational implications:**
  - A provides maximum isolation and reproducibility per prompt; adds
    spawn latency per message; every prompt has a clean slate (no
    session memory bleed).
  - B provides lower latency and a persistent session feel; accepts
    that a persona container accumulates state across messages and
    that crash recovery is container-level not prompt-level.
- **Seth's flag:** **operator call.** Either reconcile US 54 to
  match what shipped (document the deviation, update AC), or commit
  to migrating to US 54's shape and treat the shipped poller as a
  transition artifact. Don't leave the PRD asserting A while B ships.

### C4 — Docker socket location (§Networking vs. agentmon direct mount)

- **Fork A (§Networking, PRD):** Docker socket mounted only in the
  spawner container. Everything else reaches Docker through the
  spawner's API surface.
- **Fork B (shipped today):** agentmon mounts the Docker socket
  directly. Documented as a deviation in `remaining-work.md`.
- **Operational implications:**
  - A is the stricter isolation posture: only one process sees the
    socket; escape surface is minimized; agentmon goes through
    spawner RPC to do anything Docker-shaped.
  - B is the simpler posture: agentmon talks to Docker directly,
    skipping a hop; escape surface is wider; `remaining-work.md`
    already flags it.
- **Seth's flag:** **operator call.** Two live paths. Pick one:
  either tighten agentmon to go through spawner (close the
  deviation), or update §Networking to match shipped reality
  (formalize the deviation). Middle ground is the current state and
  is what `remaining-work.md` is asking to resolve.

---

## 7. Session N+ sequencing (revised)

Pass-1 sequence still largely holds, with one reorder: F28's
critical-today framing reinforces Group A as session-N lead, not
revises it. F07 lifts into Session N+2 (XS effort, clean fit with
Group C). F10 stays tactically-deferred until Bug H re-measures.

- **Session N — Group A, live-fire comms plane.** Items 1, 2, 3, 4,
  5. F28 is the headline: quarantine rate must come down. G3
  dual-write is the fail-open bug inside the group and gets priority.
  Persona→persona routing included because scope widened this
  session. Success gate: seth→alex, seth→ross land in recipient
  inboxes; quarantine rate drops below 10% on the week-of
  measurement.
- **Session N+1 — Bug H fallout + F10 re-measure + observability
  precondition.** Bug H lands; immediately re-measure F10 (item 21).
  If hot loop persists, fix F10 directly. Then F05 (item 14) as
  precondition for Group B writes. Then F20 (item 8) and merger
  empty-TestCmd (item 10). Success gate: no silent-skip paths; every
  failure lands in `failure_reason` column.
- **Session N+2 — State-machine completeness.** F04 (item 12), F07
  (item 13), worktree preamble (item 11), 6eed2a6f coverage (item
  15). Success gate: every transition has one canonical edge; fresh
  install schedules experiments without operator CLI intervention.
- **Session N+3 — PRD anchors.** R1/R5 (items 23, 24), R3 (item 25),
  R9 (item 26), S1 (item 27), S2 (item 28). This session is mostly
  PRD-unblock. Success gate: multi-project canvas demonstrable; Kyle
  can query image SHA per task; chaos test is green.
- **Session N+4 — Observability polish + contract-parity audit +
  mop-up.** F16 (item 16), F27 (item 17), F31 (item 18), correlation
  IDs (item 19), worker-stop surface (item 20), merger parity fix
  (item 22), plus the scoped library/CLI audit from §5 — fixes from
  the audit get appended here. Misc items 29–32 fold in as they
  surface from source re-check. Success gate: all scoreboard rows
  green; exit criteria met.

Velocity envelope: 32 items across 5 sessions at 3–5 items per
session. Fits if session N+3 is PRD-heavy and session N+4 absorbs
long-tail misc.

---

## 8. Source-doc resolutions

- **Quarantine count — 104.** F30 says 104; messaging investigation
  G5 says 102 or 103. Per CEO directive, Seth picks 104 (worst-case
  framing drives action). Scoreboard item 1 phrased "93% quarantine
  rate" uses the 83/89-ish recent-window framing from F28; the 104
  is the cumulative count that surfaces in audit F30. Same
  phenomenon, two measurements; 104 is the canonical figure.
- **Frontmatter emission (F22 vs. messaging E5) — Option A
  mandatory.** Confirmed. Claude's `Write`-tool path emits
  frontmatter reliably; other paths do not. Reliability is mixed
  enough that stub-suppression is mandatory regardless of which path
  the LLM happens to use. Scoreboard row 7 captures the resolution.

---

## 9. Exit criteria per item — how to call pass-2 complete

Every scoreboard row above carries:

- **A source citation** (F-ID, US-N, R-anchor, S-anchor, or
  "pending re-check" flag).
- **An effort tag** (XS, S, M, L).
- **A binary done-state** (the "done:" clause in each row).

Owner personas default as follows, subject to override:

- Group A (1–7): seth / kyle joint (comms plane touches Kyle binary).
- Group B (8–11): seth (spawner, merger, DB writes).
- Group C (12–15): seth (models, migrations, state machine).
- Group D (16–20): seth (observability).
- Group E (21): seth, blocked-on-ross (retry/Bug-H context).
- Group F (22): seth (merger contract).
- Group H (23–26): seth / alex joint (PRD anchors; alex owns PRD
  citation truth).
- Group I (27–28): seth (chaos + endpoint plumbing).
- Misc (29–32): seth, with source re-check dependency on a temp-worker
  delegation (see §10).

**Pass-2 is complete when:** all 32 rows have (a) owner persona
assigned, (b) effort tag confirmed against source text where pending,
(c) binary done-state unambiguous. Current state of this document:
(a) defaults assigned above, (b) effort tags are my estimate — will
firm up as source re-check lands, (c) done-states are unambiguous per
row.

---

## 10. What still requires source re-check

Several rows carry "(source pending re-check)" because I don't have
bare-repo access from this container. Delegation proposal (same as
pass-1 §7.1, restated for completeness):

1. Temp worker reads the full 673-line audit and 344-line messaging
   investigation. Output: F-IDs for scoreboard rows 2, 3, 4, 5, 6,
   9, 11, 15, 19, 20, 29, 30, 31, 32. Budget: ~15 min temp.
2. Temp worker reads `docs/prd-containerization.md` and
   `plans/containerization.md`. Output: verification that R1/R3/R9/S1/S2
   and US-N citations are accurate; any additional PRD anchors I
   missed. Budget: ~10 min temp.
3. Scoped library/CLI contract-parity audit (see §5). Budget: ~30
   min temp.

If Kyle wants any of these kicked off before Session N, say the word.
Otherwise default ordering is: fix comms plane first (Session N),
then run the delegations between N and N+1.

---

## Routing note

This message lands in my outbox. Per the live F28 finding (item 1 on
this scoreboard) persona→Kyle routing does not work — you'll hand-read
until Session N closes that gap. Self-reinforcing irony acknowledged.

— seth
