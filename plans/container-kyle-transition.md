# Container-Kyle transition plan

**Status:** v2, 2026-04-22. v1 reviewed by Seth
(`~/.drem-csuite/kyle/inbox/2026-04-23T04:45:52Z-seth-68d9582f.md`) —
**PASS with 6 adjustments, all applied in this revision**.
Grep audits Seth requested (#6) also run on host; findings
inlined below. Operator has not yet greenlit.

## Context

The C-Suite now runs 3 containerized personas (Mike, Alex, Seth) via
`csuite-persona` binaries that poll inbox, invoke `claude -p`, and
write outbox files routed by `csuite-watcher`. Kyle-the-persona (the
CEO) runs instead as an interactive Claude Code CLI instance on the
host, invoked by the operator ad-hoc.

Kyle-the-persona is **NOT** the same thing as the `cmd/drem-kyle` Go
binary (HTTP/WS world-state API). The Go binary stays untouched; this
plan is only about the CEO persona runtime.

## Why now

Session 2026-04-22 closed four Pod 1 items (Ross retirement, watcher
audit-token fix, Bug-J merger preserve-workdir, Pod 1–7 ratification)
and added a kyle-as-source fix (commit `c076700`). The only remaining
asymmetry between Kyle and the other personas is the host-vs-container
runtime split. Containerizing Kyle now:

- subsumes the `drem csuite send` host CLI I was about to spec (≈4–5h
  of work eliminated);
- solves the Q12 kyle-context-save-restart investigation by making
  each turn stateless (canonical docs + `state.md` + inbox message);
- unblocks ~2s host-Kyle→persona latency via the same POST /deliver
  mechanism Mike/Alex/Seth already use;
- removes the `ClassKyle` special-case in `internal/deliver/classify.go`
  — Kyle becomes `ClassPersona` like the others.

## Non-goals

- NOT touching `cmd/drem-kyle` (Go binary, separate concern).
- NOT removing the operator's ability to invoke `claude` interactively
  against Kyle's prompt (still a valid escape hatch for exploratory
  work, major strategic pivots, or operator-sitting-next-to-Kyle
  review).
- NOT changing Kyle's prompt content beyond the coexistence rules
  added in Phase 4.

## Current state inventory

| Artifact | Status |
|---|---|
| `docs/csuite-agents/prompts/kyle.md` | Exists, scrubbed of Ross references this session (commit `cef269e`). Already formatted for csuite-persona consumption. |
| `~/.drem-csuite/kyle/` | Exists (inbox, outbox, archive, `state.md`, `restart-context.md`, `heartbeat`). Identical shape to other personas. |
| `validPersonas` / `rescanPersonas` | Includes `kyle` (commit `c076700`). |
| `classify.go::ClassKyle` | Separate class; routes to inbox the same way `ClassPersona` does but with a distinct identity. |
| `cmd/csuite-persona/` | Persona-agnostic; parameterized by `$CSUITE_AGENT` env var. Kyle would run the same binary. |
| `deploy/docker/csuite-{mike,alex,seth}.Dockerfile` | Thin per-persona images built on `csuite-base`. Same pattern for Kyle. |
| `internal/projects/templates/project-compose.yml.tmpl` | Three near-identical persona service blocks. Fourth one for Kyle would mirror the shape. |

## Phase 1 — Image + compose (estimated 1–2h, ~200 LOC)

Pattern-match the three existing personas:

1. **`deploy/docker/csuite-kyle.Dockerfile`** — copy from
   `csuite-mike.Dockerfile`, substitute `mike` → `kyle` in every
   `ARG`/`LABEL`/`ENTRYPOINT`. No functional change.

2. **`deploy/docker/build-csuite.sh`** — add `kyle` to the build+push
   loop (`for persona in mike alex seth` becomes `... mike alex seth
   kyle`). Verify the `build-csuite.sh` variable `PERSONAS` (if one
   exists) or the inline list is updated.

3. **`internal/projects/templates/project-compose.yml.tmpl`** — insert
   `csuite-kyle` service block after `csuite-seth`. Body mirrors other
   personas with ONE privileged exception (per Seth adjustment #2):
   - `image: localhost:5000/drem-csuite-kyle:latest`
   - `environment`: `DREM_PROJECT`, `DREM_ORCH_URL`,
     `CSUITE_AGENT=kyle`, `CSUITE_WATCHER_TOKEN_FILE`
   - `volumes`:
     - creds (ro)
     - prompts (ro)
     - **orch-plans mounted `:rw` (Kyle-only privilege)** — Kyle is
       the plan-author in the C-Suite; other personas keep the `:ro`
       mount. Breaks symmetry intentionally to avoid plan-authorship
       regression.
     - `~/.drem-csuite/kyle` rw
     - watcher-token (ro)
   - `networks`, `labels`, `depends_on`, `restart`

4. **`internal/images/default.go`** — add `drem-csuite-kyle` to the
   registry.

5. **`internal/csuite/persona/persona.go::AllowedPersonas`** — append
   `"kyle"`. Scan for related constants (`csuiteAgents`,
   `knownAgents`).

6. **`deploy/docker/context/csuite-entrypoint.sh` +
   `csuite-run.sh`** — add `kyle` to the persona validation switch.
   Specifically `csuite-run.sh:38–46` where the case statement reads
   `mike|alex|seth` — extend to `mike|alex|seth|kyle`. The prompt
   path resolution (`/opt/csuite/prompts/${CSUITE_AGENT}.md`) is
   already generic, so once `kyle.md` is staged by `build-csuite.sh`
   into `/opt/csuite/prompts/kyle.md` the container can load it.

7. **`deploy/docker/csuite-kyle.Dockerfile`** in Phase 1 step 1
   already sets `ENV CSUITE_AGENT=kyle`, matching the pattern of the
   three existing persona Dockerfiles.

8. **Template test (`internal/projects/template_test.go`)** — extend
   `TestRender_CsuiteWatcherTokenPathIsWired` (line 217) to include
   `kyle` in the persona list it iterates. Add a
   `TestRender_CsuiteKyleServicePresent` asserting the csuite-kyle
   service block renders with the expected mounts + env, including
   the `:rw` orch-plans mount (Kyle-only privilege).

9. **Do NOT add `kyle` to `cmd/csuite-watcher/main.go:270`
   `AllowedAgents` default.** That slice governs the legacy
   agentd-style `LifecycleManager.RunTurn` path, which has a hard
   `ErrKyleException` in `internal/watcher/lifecycle.go:72–75`. The
   legacy path is not wired into the running watcher binary (grep
   confirms `RunTurn` has zero call-sites under `cmd/csuite-watcher/`;
   README references it as a future Phase 4 task). Treat the legacy
   watcher code as dead-code retirement material per world-state §2a,
   same category as OpenCode / `internal/worktreehost/` — out of
   scope for this plan but noted.

## Phase 1.5 — csuite-persona multi-outbox signal (conditional)

**Per Seth adjustment #3.** If inspection of `cmd/csuite-persona/main.go`
confirms csuite-persona only POSTs /deliver for files IT writes
(not for files `claude -p` writes via the Write tool during the turn),
then the ~2s latency claim in Phase 3 smoke-test #2 is wrong and
container-Kyle would ride the 5-min rescan path just like host-Kyle.

**Verification step (add before Phase 3):** read
`cmd/csuite-persona/main.go`. Find the `claude -p` invocation and
look for:
- (option α) A before/after `os.ReadDir(outbox)` diff that iterates
  newly-created files and POSTs /deliver for each. Good — no Phase 1.5
  needed.
- (option β) A single POST /deliver tied to csuite-persona's own
  explicit outbox writes. Problem — Phase 1.5 needed.

**If β:** add Phase 1.5 commit: csuite-persona snapshots the outbox
dir mtimes before spawning claude, and after claude exits enumerates
files newer than the snapshot, POSTing /deliver for each. Strictly
additive, fixes latency for ALL four personas, not just Kyle.
Estimated ~60 LOC + test.

**If α:** skip Phase 1.5 entirely.

## Phase 2 — Simplify classification (estimated 30min, ~40 LOC)

1. **`internal/deliver/classify.go`** — collapse
   ```go
   case "mike", "alex", "seth":
       return Classification{Class: ClassPersona, Dest: dest}, nil
   case "kyle":
       return Classification{Class: ClassKyle, Dest: "kyle"}, nil
   ```
   into a single arm listing all four personas returning
   `ClassPersona`.

2. **Audit callers of `ClassKyle`** — grep for it. Likely candidates:
   - `internal/deliver/rescan.go::rescanPersona` switch on
     `class.Class`; merge the `ClassKyle` arm into `ClassPersona` arm
     (same handling already via `deliverToInbox`).
   - `classify_test.go` — update tests.
   - Any audit-queue handler or metrics tag keyed on `ClassKyle`.

3. **Keep `ClassKyle` const removed** or deprecated with a comment.
   Post-transition, there is nothing persona-like about Kyle that the
   other three don't share.

## Phase 3 — Deploy + smoke (estimated 30min)

1. `bash deploy/docker/build-csuite.sh` → pushes
   `localhost:5000/drem-csuite-kyle:latest`.

2. `drem project register --update --force drem-orchestrator` →
   regenerates compose.yml with csuite-kyle service.

3. `docker compose up -d --no-deps csuite-kyle` → container-Kyle goes
   live. **Do not recreate the other 3 personas** — they are
   undisturbed.

4. **Smoke-test #1 (inbox consumption):** drop a verify message in
   `~/.drem-csuite/kyle/inbox/`, expect `claude -p` turn within ~30s,
   reply in `~/.drem-csuite/kyle/outbox/` with frontmatter.

5. **Smoke-test #2 (outbox routing via POST /deliver):** container
   Kyle sends to Mike. Expect watcher log
   `source=kyle dest=mike` within ~2s (not the 5-min rescan). This
   proves the POST /deliver path works for containerized Kyle — the
   full Path #3 symmetry.

## Phase 4 — Coexistence rules (estimated 15min, ~60 LOC docs)

**Per Seth adjustments #1 and #5.** Expanded scope from v1 — the
shared-state surface between interactive-Kyle and container-Kyle is
the whole `~/.drem-csuite/kyle/` tree, not just outbox. Also
explicitly names the operator → Kyle channel.

Add a preamble block to `docs/csuite-agents/prompts/kyle.md`:

```
## Runtime mode

Kyle-the-CEO runs in TWO modes depending on invocation context:

1. **Container mode** (`csuite-kyle` service): default, always-on,
   processes inbox messages 24/7 via csuite-persona poller. This is
   the canonical Kyle for persona→Kyle and orch-event→Kyle traffic.

2. **Interactive mode** (operator runs `claude` against this prompt):
   canonical channel for operator→Kyle dialogue, exploratory work,
   strategic pivots, plan authorship, subagent dispatch (see below).

### Shared-state discipline

The whole `~/.drem-csuite/kyle/` tree is shared between the two
instances. To prevent state corruption:

**Interactive-Kyle MUST NOT write any file under
`~/.drem-csuite/kyle/` while container-Kyle is running.** That
includes `state.md`, `restart-context.md`, `heartbeat`, `outbox/`,
`inbox/archive/`, and `inbox/` itself. Writing is container-Kyle's
exclusive domain while it is up.

**Clean hand-off protocol** (if interactive-Kyle needs to
write — e.g. restart-context.md after a save/restart cycle):

```bash
sg docker -c "docker compose -f ~/.drem/projects/drem-orchestrator/compose.yml stop csuite-kyle"
# ... interactive-Kyle does its writes ...
sg docker -c "docker compose -f ~/.drem/projects/drem-orchestrator/compose.yml up -d --no-deps csuite-kyle"
```

### Channel assignments

- **operator → Kyle:** ALWAYS interactive mode. Container-Kyle does
  not process operator messages. This matches current reality —
  operator runs `claude` to talk to Kyle — and requires no new
  infrastructure. (If operator is willing, they can drop markdown
  files into `~/.drem-csuite/kyle/inbox/` and container-Kyle will
  process them, but that is not the canonical channel.)
- **persona → Kyle** (mike/alex/seth): container-Kyle via watcher
  rescan routing. This is the big win of this plan.
- **orch events → Kyle:** container-Kyle via event bus / inbox
  delivery (already working; see world-state §2a).
- **Kyle → persona:** container-Kyle via csuite-persona POST
  /deliver (<2s, conditional on Phase 1.5 verification) or periodic
  rescan (≤5min). Interactive-Kyle falls back to direct-inbox
  filesystem drop while container-Kyle is stopped.
- **Kyle → plan docs:** container-Kyle via the :rw orch-plans mount.
  Interactive-Kyle via direct filesystem write. Do NOT have both
  editing the same plan doc concurrently.

### Subagent dispatch

Kyle subagent dispatch (e.g. `Agent({subagent_type: 'general-purpose',
prompt: ...})`) is **interactive-only** until further notice. The
`claude -p` runtime used by container-Kyle does not expose the
Claude Code Task tool, and the `drem` CLI is not currently
installed in `csuite-base` (grep-audited 2026-04-22). Container-Kyle
CAN still route work to Mike via inbox messages, which is the
canonical coordination path anyway. If container-Kyle encounters a
situation that requires spawning a subagent directly, it should
outbox-message the operator asking them to run interactive-Kyle.

When a container-Kyle is running (detect via `docker ps --format
'{{.Names}}' | grep -q csuite-kyle`), the interactive instance is
for OPERATOR INTERACTION, PLAN AUTHORSHIP, AND SUBAGENT DISPATCH
only. When no container-Kyle is running, the interactive instance IS
the sole Kyle and normal operations apply.
```

Commit the prompt update. Rebuild csuite-kyle image (the prompt is
COPY'd into the image at build time). Recreate csuite-kyle.

## Phase 5 — Housekeeping (estimated 15min)

1. `plans/kyle-context-save-restart-investigation.md` — add a
   RESOLVED note at top: "Resolved by container-Kyle transition
   (plans/container-kyle-transition.md). Each container turn is
   stateless; context-growth no longer a concern."

2. `plans/c-suite-world-state-2026-04-22.md` §2a — update the
   containerization paragraph: "4 containerized personas (Kyle, Mike,
   Alex, Seth)" instead of "3 containerized personas".

3. `plans/c-suite-world-state-2026-04-22.md` §7 — if this lands before
   Pod 1 closes, add to Pod 1 progress list. Otherwise fold into Pod
   1.5 spin-out.

## Risks + mitigations

Updated per Seth review. Adjustments #1, #2, #3, #4 added explicit
rows; prior rows updated where Seth flagged.

| Risk | Severity | Mitigation |
|---|---|---|
| Multi-CEO (container + interactive) stomping each other | Medium | Coexistence rules (Phase 4) expanded to the whole `~/.drem-csuite/kyle/` tree, not just outbox. Hand-off protocol via `docker compose stop csuite-kyle`. |
| Container-Kyle cannot write orch-plans | Medium | **Fixed in Phase 1.** csuite-kyle service gets `:rw` mount on orch-plans (Kyle-only privilege). Other personas unchanged. |
| Container-Kyle hot-loops on malformed inbox messages | Low | Same quarantine / reaper patterns already in csuite-persona. Watcher already quarantines malformed input. |
| `state.md` growth in container-Kyle | Low-Medium | Add compaction discipline to the prompt: "When state.md > 5 KB, summarize and truncate prior sections." Same pattern other personas already use. |
| Interactive-Kyle overwrites state.md / restart-context.md mid-turn | Low-Medium | Phase 4 hand-off protocol: interactive-Kyle stops csuite-kyle before writing anything under `~/.drem-csuite/kyle/`. |
| Operator muscle-memory (`claude` for Kyle) breaks | Low | Interactive mode stays the canonical operator→Kyle channel. Phase 4 explicitly names it. |
| Kyle prompt drift between interactive + container | Low | Both read `docs/csuite-agents/prompts/kyle.md`. Rebuilds synchronize automatically. |
| 4 active csuite containers burn more host memory | Low | Each csuite-persona container is small (~200 MB RSS steady-state). 4× that is still trivial on the dogfood host. |
| Container-Kyle multi-outbox-per-turn latency gap | Medium | Phase 1.5 conditional. Verify csuite-persona POSTs /deliver for every file created during `claude -p` turn, not just its own explicit writes. If not, add Phase 1.5 scaffold commit (strictly additive, helps all personas). |
| Container-Kyle can't dispatch subagents | **Confirmed: interactive-only, deferred** | Seth audit #4 + host grep-audit #5 confirmed: `drem` CLI is NOT installed in `csuite-base`, and `claude -p` has no Task tool. Container-Kyle coordinates via inbox; subagent dispatch stays interactive-only until post-Q2 (post-core could revisit installing drem + giving container-Kyle orch API access via network, NOT via docker.sock). |
| drem CLI missing from csuite-kyle image | Confirmed gap | See previous row. Not addressed in this plan. Revisit in Pod 3 or later if container-Kyle spawning becomes load-bearing. |

## Test plan

1. **Phase 1 template tests** — assert compose renders csuite-kyle
   with expected shape.
2. **Phase 2 classify tests** — kyle resolves to `ClassPersona`.
3. **Phase 3 live smoke-tests (#1 + #2)** — inbox processing + POST
   /deliver path.
4. **Phase 4 prompt test** — manual: interactive session reads prompt
   and internalizes coexistence rules; grep-check passes.

## Rollback

If container-Kyle misbehaves in prod, operator runs:
```bash
sg docker -c "docker compose -f ~/.drem/projects/drem-orchestrator/compose.yml stop csuite-kyle"
```
State (inbox, outbox, state.md) is preserved in `~/.drem-csuite/kyle/`.
Interactive Kyle remains fully functional. No data loss path.

## Seth's grep audit findings (run 2026-04-22 on host)

Per Seth adjustment #6. All five audits run:

**#1 `grep -rn ClassKyle internal/`** — 6 hits, all in
`internal/deliver/` as expected. Both real call-sites
(`rescan.go:190`, `deliver.go:235`) already joint-arm with
`ClassPersona`. Phase 2 collapse is truly cosmetic.

**#2 `grep -rn '"kyle"' internal/ cmd/`** — LOAD-BEARING FINDING:
`internal/watcher/{lifecycle.go, dedup.go, event_delivery.go,
types.go}` has an `ErrKyleException` code path that refuses to run
turns for agent="kyle". This is legacy agentd-style launcher code,
NOT wired into the running `cmd/csuite-watcher/` binary (grep
confirmed zero call-sites of `LifecycleManager.RunTurn` under `cmd/
csuite-watcher/`; README mentions it only as a future "Phase 4 task 4"
plan). Same dead-code category as OpenCode / `internal/worktreehost/`
(world-state §2a). Document in Phase 1 step 9 that we do NOT add
kyle to the legacy `AllowedAgents` default. Retirement of the legacy
watcher code goes with Pod 1's dead-code sweep, not this plan.

**#3 `grep -rn 'mike.*alex.*seth' ...`** — confirmed list in Phase 1:
`AllowedPersonas` (persona.go:69, needs kyle), `CsuiteImages` map
default (cmd/drem/project.go:455, needs kyle — already Ross-free per
commit 9b03a1a), template.go:41 comment (cosmetic), csuite-run.sh
validation switch, csuite-base.Dockerfile comment. No unexpected
hits.

**#4 `grep -rn CSUITE_AGENT cmd/ deploy/ internal/`** — confirmed
pattern. `deploy/docker/csuite-{mike,alex,seth}.Dockerfile` each set
`ENV CSUITE_AGENT=<name>` — Phase 1 step 1 adds csuite-kyle.Dockerfile
with `ENV CSUITE_AGENT=kyle`. `csuite-run.sh:38–46` validation switch
needs kyle added — Phase 1 step 6. Prompt path
`/opt/csuite/prompts/${CSUITE_AGENT}.md` is generic; kyle.md gets
staged by `build-csuite.sh` already since commit cef269e.

**#5 `grep -n 'drem' deploy/docker/csuite-base.Dockerfile`** —
**confirmed: drem CLI is NOT installed in csuite-base.** Rebuilding
the image with a `RUN curl -L drem > /usr/local/bin/drem` step is
possible but deferred — see the "subagent dispatch" risk row.
Container-Kyle will not be able to invoke `drem spawn worker` or
`drem cli kyle inbox` or `drem csuite audit` directly. These all
become interactive-Kyle workflows or operator-side actions.

## Out of scope (Kyle-persona-only)

- `cmd/drem-kyle` Go binary (untouched).
- `drem csuite send` host CLI (subsumed — delete the
  not-yet-implemented plan).
- Pod 3 gate-delegation work (independent; container-Kyle makes it
  easier but not a blocker).

## Decision requested

Seth: DONE. PASS with 6 adjustments, all applied in this v2.

Operator: A/B/C per prior session output.
- **A (recommended):** Implement Phases 1 + 1.5 (if β) + 2 + 3 + 4 +
  5. ~3–4h if Phase 1.5 needed, ~3h if not. Closes Q12.
- **B:** Phase 1 only now, 1.5/2/3/4/5 later. ~1.5–2h.
- **C:** Defer; stick with `drem csuite send` CLI approach (~4–5h).
  Note: option C does NOT close Q12 (interactive-Kyle context growth
  continues).

Estimated totals with Seth's adjustments:

| Phase | LOC | Time |
|---|---|---|
| 1. Image + compose + kyle.Dockerfile + csuite-run.sh + `:rw` plans mount | ~220 | 1.5–2 h |
| 1.5. csuite-persona multi-outbox signal (conditional on β) | ~60 | 45 min |
| 2. classify simplification | ~40 | 30 min |
| 3. Deploy + smoke #1 + smoke #2 | ~0 code, operational | 30 min |
| 4. Coexistence rules (expanded) | ~60 | 20 min |
| 5. Housekeeping (world-state §2a, Q12 resolution, Pod 1 progress) | ~30 | 15 min |
| **Total (with 1.5)** | **~410 LOC** | **~3.5–4 h** |
| **Total (without 1.5)** | **~350 LOC** | **~3 h** |
