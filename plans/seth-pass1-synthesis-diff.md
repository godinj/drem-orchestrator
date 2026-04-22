# Seth pass-1 synthesis — diff report (2026-04-22)

**Purpose:** Seth's pass-1 containerization-pivot gap synthesis
(`~/.drem-csuite/seth/outbox/20260422T073000Z-seth-to-kyle-containerization-pivot-gap-synthesis.md`)
was written from Kyle-summary inputs, not from the source catalogs
directly — Seth's container does not have bare-repo access. This
diff compares Seth's synthesis against the two source documents:

1. `plans/proactive-bug-gap-audit.md` — 673 lines, 34 findings
   (F01–F34), urgency + effort labels.
2. `plans/csuite-messaging-workaround-investigation.md` — 344 lines,
   gaps G1–G4 (narrative; G5 also appears mid-doc).

Seth's synthesis covers seven component groups (A–G), a 20-item
exit scoreboard, and six catalog-gap flags (§3, items 1–6). This
diff surfaces what he should know for pass-2. No re-prioritization;
just what the catalogs actually contain vs. what Seth wrote.

---

## 1. Under-weighted in pass-1

### 1a. CRITICAL / HIGH findings not explicitly named

**F28 — 93% quarantine rate in production (CRITICAL)**
- Catalog urgency: `critical` (the only `critical` in the entire
  34-finding audit). Audit line 47, detailed §F28 line 528–548.
- Seth's placement: implicit in Group A (comms plane) via G3/G4
  framing. The **critical** urgency label is never surfaced.
- Proposed correction: F28 is the live-production framing of F02
  (same root cause). Seth's Group A covers the mechanism (dual-
  write, frontmatter) but the **"93%, right now, today"** framing
  does not appear in his synthesis. The scoreboard should include
  a green-gate item that the live quarantine rate drops under 10%
  for 24h before the comms plane is declared done.
- Evidence: audit §F28 line 528–548 with live watcher DB counts
  (`quarantine=83, kyle=3, alex=2, seth=1`).

**F02 — auto-reply routing plan exists, not shipped (HIGH)**
- Catalog urgency: `high`. Audit line 21, §F02 line 77–96.
- Seth's placement: not named by ID. G3's framing "dual-write"
  overlaps in mechanism, but F02 is the **plan-status** framing
  (`plans/csuite-persona-auto-reply-routing.md` recon-only, four
  options A–D not yet dispatched).
- Proposed correction: F02 belongs in Group A as the explicit plan
  identifier. Seth's "fix it first, everything else gets cheaper"
  narrative reads well but never ties back to the already-filed
  plan and its Option A/B/C/D shortlist.

**F03 — Kyle container never reads its inbox (HIGH)**
- Catalog urgency: `high`. Audit line 22, §F03 line 98–118.
- Seth's placement: covered as G2 in Group A. NOT miscategorized.
  Flagging here only because Seth's §3 item #1 ("Kyle-binary vs
  persona-container asymmetry") claims the audit is missing this
  — it is not. F03 is the audit-side finding; G2 is the
  messaging-investigation side of the same issue.

**F07 — `Experiment`/`Variant` absent from `AutoMigrate` (HIGH)**
- Catalog urgency: `high`, effort `XS`. Audit line 26, §F07 line
  184–205, and audit "Surprises" §1 line 638–641.
- Seth's placement: **not mentioned anywhere** in the synthesis.
  No group, no scoreboard item, no catalog-gap flag.
- Proposed correction: This is a latent-fresh-install bug with
  XS effort and HIGH urgency. Belongs in Group C (schema
  contract completeness) — it is schema-drift, same family as
  F04 and the Task.failure_reason column framing. A clean install
  will silently skip experiments until the CLI is run.
- Evidence: audit lines 184–205 (migration list missing experiment
  tables) + "Surprises" #1.

**F10 — `processTestingReady` hot-loop (HIGH)**
- Catalog urgency: `high`. Audit line 29, §F10 line 238–255.
- Seth's placement: Group E (DEBT, symptomatic). Seth explicitly
  labels it DEBT.
- Proposed correction: The catalog labels F10 **HIGH**, not debt.
  Seth's "not blocking — orch works" framing conflicts with the
  audit's urgency classification. The audit characterizes this as
  "the source of the Bug I#2 WARN flood; masks other signals;
  exhausts ad-hoc log tails" (line 252–254). A HIGH-urgency
  finding placed in DEBT is a priority demotion.
- Recommendation to Seth: either escalate F10 to BLOCKING (consistent
  with catalog), or document why it stays DEBT despite HIGH label.

**F22 — persona poller never writes frontmatter (HIGH)**
- Catalog urgency: `high`. Audit line 41, §F22 line 443–456.
- Seth's placement: subsumed into Group A via G3 framing.
- Proposed correction: F22 is cited by the audit as the **producer-
  side evidence** for F02. It is the "single writer" target Seth
  names in his Group A session plan (G3: "Single writer, single
  file, single truth"). Call out F22 by ID so the fix dispatch
  has a direct file/line anchor (`poller.go:237-248`).

### 1b. Findings mis-grouped

**F20 placement — Group B is correct, but F20's evidence broader**
- Catalog §F20 line 409–426: the "7 prod sites" that still call
  `runner.SpawnAgent` are named (`subtask_scheduling.go:301`,
  `quickfix_processing.go:117,211`, `task_processing.go:248`,
  `classifying.go:90`, `test_execution.go:212`,
  `session_spawning.go:142`).
- Seth's framing: "legacy host-spawn fallback in spawner." That's
  accurate for the spawner, but the audit evidence shows the fallback
  is wired in **orchestrator dispatch paths**, not only in the
  spawner binary. Seth's fix dispatch should scope the removal to
  all 7 sites, not just the spawner service.

**F06 placement — Group B is right, but needs ID callout**
- Catalog §F06 line 164–183: two writers for needs-human-review,
  **10 JSON-writers vs 2 column-writers**. Kyle's DB poll reads
  the column and sees only 2/12 escalations.
- Seth's framing: "Context-JSON vs. column drift — `needs_human_review`
  and `failure_reason`." Correct direction, but F06's specific
  finding is that **Kyle's current poll is observing a minority of
  escalations**. That's not just future-debt; it's a live
  observability regression affecting today's Kyle binary. Worth
  surfacing as a current-impact bullet, not only future-state.

### 1c. Findings Seth classified as DEBT that the catalog grades as BLOCKING

**None strictly** — the catalog uses urgency labels (`critical`,
`high`, `medium`, `low`), not a BLOCKING/DEBT split. The closest
mismatches are:

- **F10** (HIGH → DEBT in Seth). See §1a above.
- **F22** (HIGH → implicit in Group A but not ID-named). See §1a.
- **F02** (HIGH → mechanism-covered, plan-status not named). See
  §1a.

All other HIGH findings are either in Seth's Groups A–C (blocking)
or unnamed entirely (see §5).

---

## 2. Over-weighted in pass-1

**Group G — security (8 low items)**
- Seth frames Group G as "DEBT if audit's 8-low count is accurate."
- Reality check: the audit **does not have a "security" category
  with 8 low items.** That number does not appear in
  `proactive-bug-gap-audit.md`. The summary table's categories are:
  `silent failure`, `half-shipped`, `contract mismatch`,
  `observability gap`, `single point of failure`, `consistency
  drift`, `stale doc`, `workarounds`.
- Proposed correction: Seth's Group G is built on a phantom input.
  The audit has no dedicated security section. If Kyle's inline
  summary referenced an "8 low" count, it was not from this audit.
  Seth should either drop Group G or re-source it.

**Group F — contract-first / library↔CLI parity "debt, systemic"**
- Catalog evidence: F13 (merger library empty TestCmd) is the
  **only** confirmed library/CLI parity finding in the audit. It's
  labeled `medium` (line 32).
- Seth's framing: suspects four more pairs (orchestrator lib/CLI,
  persona lib/CLI, spawner lib/CLI, retry lib/CLI). These are
  speculative per Seth's own text ("Suspected pairs I'd audit
  (not yet confirmed)").
- Proposed correction: Group F is over-weighted relative to catalog
  evidence. Only one confirmed pair; four speculative. Worth flagging
  as a recon task, not a standing group — OR explicitly marking the
  speculative pairs as "unverified" so pass-2 readers don't assume
  confirmed findings.

---

## 3. Gaps between the two source docs

### 3a. Where the docs agree (redundancies)

- **F02 ≡ F22 ≡ F28** (audit) ≡ **G3 narrative** (messaging
  investigation). All four describe the same mechanism (persona
  poller writes raw stdout; watcher quarantines) from different
  angles: plan-status (F02), producer-side evidence (F22), live
  production impact (F28), and curated-vs-stub dual-write (G3).
  Redundancy is useful for triangulation — each angle points to
  the same fix site (`poller.go:237-248`).

- **F03** (audit) ≡ **G2 narrative** (messaging investigation).
  Kyle binary has no inbox reader. Same finding, confirmed from
  both catalogs.

- **F30** (audit) ≡ **G5 narrative** (messaging investigation, line
  165–169). Both docs note 102–104 files in quarantine; agreement
  on count within the hour of observation.

### 3b. Where the docs add distinct weight

**Messaging investigation adds (not in audit):**
- **G1: orphan `.failures` sidecars** — the audit does NOT cover
  the Claude-subprocess exit=-1 path that leaves sidecars pointing
  at `.md` files that have already been moved to `.archive`. Two
  occurrences named (2026-04-21T22:13:44Z and 2026-04-22T00:00:12Z;
  messaging investigation line 54–59 and E7 line 293–304). Seth
  correctly puts G1 in Group A.
- **G3: curated+stub dual-write** — the audit's F22 covers only
  the stub side. The investigation adds that Claude ALSO writes
  a curated `*-to-<dest>-*.md` via its Write tool in the same
  turn. So Option A from the plan would route BOTH files and Kyle
  sees duplicates. Audit misses this. Seth names G3 in Group A.
- **G4: watcher image parity** — the audit does not flag image
  parity at all. Investigation line 151–162 shows the watcher is
  running a pre-Task-#11 image and the `drem csuite audit
  {list,queue}` commands don't exist in the live container yet.

**Audit adds (not in messaging investigation):**
- **Everything state-machine, observability, schema, reconciler,
  logging, agent reaping, spawner SPOF, SGLang containerization,
  warm-direct-prep** — the audit covers 30+ findings outside the
  messaging mesh. The investigation is scoped to csuite messaging
  only.

### 3c. Contradictions between the docs

- **Quarantine file count**: audit F30 says `104` (line 570);
  investigation G5 says `102` then `103` in E8 line 305–313. Same
  filesystem state observed within an hour; likely delta caused by
  a new quarantine between counts. Not a real contradiction, but
  pass-2 should pick one number.
- **Does Claude hand-write frontmatter?** Messaging investigation
  line 272–282 (E5) says "the `-to-` one is Claude's Write-tool
  output; the `-reply-` one is stdout wrapped by the poller" —
  implying Claude DOES write frontmatter when it uses Write
  directly. Audit F22 line 451 says "the prompt docs … tell the
  LLM to emit frontmatter, but the LLM often doesn't." These are
  compatible — Claude often forgets BUT when it uses Write
  explicitly it does — but pass-2 should resolve this as "Claude
  is unreliable on frontmatter" and treat Option A as mandatory
  regardless.

---

## 4. Seth's six catalog-gap flags — verified or refuted

Seth claimed six items ARE NOT in the catalogs. Verification
below.

### Flag 1: Kyle-binary vs persona-container asymmetry
- **Status: partially refuted.** The audit covers the mechanism
  symptom (F03 line 98–118: Kyle container never reads inbox) and
  the investigation covers it again (G2 line 116–137). Neither doc
  explicitly frames it as "Kyle is special; asymmetry needs design
  decision" at the level Seth articulates. So the **symptom** is in
  both catalogs; the **architectural framing** Seth adds is novel.
- Recommendation: credit the catalogs with the symptom (F03, G2)
  but keep the framing as Seth's value-add.

### Flag 2: Worker lifecycle on crash / SIGKILL
- **Status: partially covered.** Audit F29 line 550–563 covers
  `reconcileStuckAgents` container-awareness plan ("infinite-
  respawn loop in the sweeper because it doesn't know about
  container agents"). Audit F32 line 594–609 covers spawner
  single-process SPOF.
- What's NOT covered: worker SIGKILL mid-dispatch reconciliation,
  watcher crash leaving orphan worker containers, tmux session
  orphaning mis-reading the 5-worker cap.
- Recommendation: Seth's flag is mostly valid. F29 addresses the
  sweeper side partially; the "dispatch mid-flight" and "orphan
  container" pieces are genuinely missing.

### Flag 3: Reconciler completeness
- **Status: partially covered.** F29 (line 550–563) and F17 (line
  362–379, `reconcileAlreadyMergedFeatures` bypasses state machine)
  are the two audit findings about reconcilers. Neither is a
  comprehensive "does a reconciler exist, is it complete, does it
  run often enough" framing.
- Recommendation: Seth's flag valid. Catalog has two narrow
  findings; the systemic question is not asked.

### Flag 4: Image immutability / reproducibility
- **Status: partially covered.** G4 (messaging investigation line
  151–162) names watcher image drift. F19 (audit line 395–407)
  names the SGLang Dockerfile "never verified" status. Neither is
  a systemic "are all container images content-addressed and pinned
  per task" framing.
- Recommendation: Seth's flag valid as a systemic question;
  catalog has point-instances only.

### Flag 5: docs-as-acceptance-criteria wiring
- **Status: confirmed missing.** `docs` and `acceptance` appear in
  neither catalog's body. Audit mentions `docs/prd-containerization.md`
  in passing (line 109, describing Kyle's runtime) and
  `docs/containerization/install.md` in §Follow-ups (line 670) as
  "size exceeded audit budget." Investigation does not touch it.
- Recommendation: Seth's flag confirmed. Neither catalog tests
  whether CLAUDE.md's standing constraint is actually wired into
  merge gates. Keep as novel pass-2 addition.

### Flag 6: `restart-context.md` convention lifecycle
- **Status: refuted — it IS in the audit.** F31 line 581–592:
  "Persona `restart-context.md` is stale (Seth's last update
  2026-03-27) — no staleness check". Exactly Seth's concern.
- Recommendation: re-attribute flag 6 to F31 rather than listing
  as novel catalog gap. Seth may have been working from Kyle's
  summary which omitted F31.

**Summary**: 4 of 6 flags hold up as genuinely novel architectural
framings on top of catalog point-findings. Flags 1, 2, 3, 4 stand
as architectural framings even though the catalogs cover specific
symptoms. Flag 5 is fully novel. Flag 6 is directly in the audit
(F31) and should be re-attributed.

---

## 5. Items the catalogs cover that Seth didn't touch at all

Findings not mentioned in any Group A–G, any scoreboard item, or
any catalog-gap flag. Ordered by urgency.

| Finding | Urgency | One-liner |
|---|---|---|
| F07 | high | `Experiment`/`Variant` missing from `AutoMigrate` — fresh installs silently skip experiments |
| F01 | medium | `o.events` chan has no consumer in headless orch — source of WARN flood (mentioned via Bug I#2 only) |
| F08 | medium | README.md still describes tmux-era architecture; contradicts the whole pivot |
| F11 | medium | 8 of 44 `state.TransitionTask` call sites skip `publishTaskTransition` — event bus gap |
| F13 | medium | Merger library's empty TestCmd contract TODO still unresolved (Seth named it but not by ID) |
| F15 | medium | 944 `idle` agents in DB, oldest 2026-03-05, no reaping path |
| F17 | medium | `reconcileAlreadyMergedFeatures` bypasses state machine (FAILED→DONE direct SQL) |
| F18 | medium | `plans/warm-direct-prep.md` not implemented — prep runs inline in orch tick loop, 30–90s stalls |
| F21 | medium | `internal/logging` sampler shipped but only 2 of many hot-path sites use it |
| F24 | medium | `test_writing.go` replan transitions never publish — Seth specifically wants to see replan events |
| F25 | medium | 32 `log.Printf` sites lack structured `task_id`/`project_id` — aggregation fails |
| F26 | medium | `orchEvents` drop emits WARN every time — no sampling (paired with F01) |
| F29 | medium | `reconcileStuckAgents` container-awareness plan open — latent double-writer risk |
| F32 | medium | `drem-global-spawner-1` is a single process; crash wedges every project |
| F09 | low | CLAUDE.md references nonexistent `compose.override.yml` |
| F12 | low | pprof + SIGUSR1 installed in orch but compose never sets `DREM_PPROF=1` |
| F14 | low | `orchEvents` allocated even in `--tui-only`; two-stage chan chain |
| F19 | low | SGLang containerization Dockerfile never verified |
| F23 | low | Three SQLite DBs (drem.db, watcher.db, deliveries.db) with overlapping state |
| F27 | low | `/csuite/operator/` inbox/outbox — Seth DID name this in Group D; not unmentioned |
| F30 | low | 104 files in quarantine with no cleanup/rescan policy |
| F31 | low | `restart-context.md` stale — this IS Seth's flag #6 (re-attribute) |
| F33 | low | Orchestrator event channel size hardcoded 100 |
| F34 | low | Bug H fail-fast reason is project-language-agnostic |

**Correction on F27**: Seth did name F27 in Group D. Included in
table above for completeness; strike from "unmentioned" list.

**Correction on F31**: Seth's flag #6 IS F31. Re-attribute.

**Net unmentioned**: 22 findings not surfaced in Seth's synthesis
(after removing F27 and F31). Most are medium or low urgency, but
F07 (HIGH) stands out as absent.

---

## 6. Proposed scoreboard corrections

Seth's 20-item scoreboard is tidy but the ratio of narrative-
items to catalog-items is skewed toward narrative. Proposed
additions, removals, revisions.

### Additions (from catalog, not on scoreboard)

| # | Proposed item | Source |
|---|---|---|
| +1 | Live csuite quarantine rate drops <10% sustained 24h | F28 |
| +2 | Fresh-install path migrates `Experiment`/`Variant` tables automatically | F07 |
| +3 | README.md and CLAUDE.md reflect post-pivot architecture | F08, F09 |
| +4 | All `state.TransitionTask` call sites publish to event bus | F11, F24 |
| +5 | `log.Printf` sites migrated to `slog` with task/project context | F25 |
| +6 | `idle` agent reaper path exists and runs | F15 |
| +7 | `reconcileAlreadyMergedFeatures` routes through state machine (no SQL bypass) | F17 |
| +8 | `plans/warm-direct-prep.md` shipped or explicitly abandoned | F18 |
| +9 | `internal/logging` sampler adopted at every Warn/Error hot path | F21 |
| +10 | Three-SQLite-DB drift reconciled or documented as permanent | F23 |
| +11 | Quarantine dir has cleanup / rescan-retry policy | F30 |
| +12 | Spawner has leader-election, healthcheck, or failover | F32 |

### Revisions (Seth's existing items that need catalog anchors)

- **Item 2** (watcher image parity) — anchor to G4 explicitly.
- **Item 3** (persona→Kyle mail routing) — anchor to F02 + F22 + G2.
- **Item 5** (persona→persona mail routing) — note: this is Seth's
  novel observation from his current session, NOT catalog-sourced.
  Keep; flag as "session-originated, to add to pass-2 catalog."
- **Item 8** (spawner no host-spawn fallback) — anchor to F20,
  note that scope is 7 call sites, not just the spawner binary.
- **Item 9** (`failure_reason` first-class column) — anchor to
  F05; note this is also a precondition for F06 resolution.
- **Item 11** (single canonical edge per transition) — anchor to
  F04 AND F17 (the `reconcileAlreadyMergedFeatures` bypass is
  another canonical-edge violation the audit explicitly calls out).
- **Item 13** (`processTestingReady` not hot-looping) — anchor to
  F10; note that catalog labels this HIGH, not DEBT.
- **Item 14** (pprof/SIGUSR1 on watchdog) — anchor to F16. Also
  note F12 (pprof installed in orch but compose env not set) as
  a sibling item.
- **Item 15** (`/csuite/operator/` canonical or deleted) — anchor
  to F27.

### Removals (over-weighted per §2)

- **Item 18** (security 8 low items) — the "8 low" input appears
  fabricated relative to the audit's actual category structure.
  Either replace with explicit security-item list if one surfaces,
  or drop.

### New unknowns worth promoting to explicit items

- Orchestrator event-bus publish coverage (F11: 18% gap).
- Agent table cleanup / reaping (F15, F30).

**Revised scoreboard size**: ~30 items after additions. Still
reasonable for a "what does pivot-done look like" checklist.

---

## Summary of corrections

- **Under-weighted**: 5 HIGH findings (F02, F03, F07, F10, F22)
  deserve explicit ID callouts. F07 is fully absent. F10 is
  classified DEBT despite HIGH label.
- **Over-weighted**: Group G (security, "8 low") is built on a
  phantom input. Group F has one confirmed pair and four
  speculative.
- **Docs-vs-docs**: Four redundancies (all reinforce each other),
  three weights unique to the messaging investigation (G1, G3,
  G4), twenty-plus findings unique to the audit. One minor count
  discrepancy (quarantine 102 vs 103 vs 104).
- **Catalog-gap flags**: 4 of 6 hold up as novel architectural
  framings. Flag 6 is directly in the audit as F31 —
  re-attribute. Flag 5 (docs-as-acceptance-criteria wiring) is
  confirmed novel.
- **Unmentioned findings**: 22 of 34 findings do not appear in
  the synthesis (most medium/low). F07 (HIGH) is the standout
  absence.
- **Scoreboard**: +12 new items from catalog; 9 existing items
  need catalog-ID anchors; 1 item likely fabricated.

Ready for Seth's pass-2 integration.
