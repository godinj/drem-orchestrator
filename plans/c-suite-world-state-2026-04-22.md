# C-Suite World State — 2026-04-22

**Status:** Canonical. Supersedes prior persona-prompt guidance where conflict exists.
**Authority:** Operator-ratified via annotations on the Alex user-stories catalog (2026-04-22); Seth pass-2 unified-path synthesis (2026-04-22T22:12Z).
**Audience:** All C-Suite agents (Kyle, Alex, Seth, Mike) and temp workers. Read at the top of every turn.
**Referenced artifacts:**
- `plans/user-stories-catalog-operator-annotated.md` — 257 stories with 54 operator annotations
- `plans/user-stories-catalog-alex-pass1.md` — Alex's pass-1 (reference baseline)
- `~/.drem-csuite/seth/outbox/20260422T221226Z-seth-to-kyle-user-stories-pass2-unified-path.md` — Seth pass-2
- `plans/operator-answers-to-seth-14-questions-2026-04-22.md` — verbatim operator answers to Seth's 14 §5 questions (integrated into this doc)
- `plans/kyle-context-save-restart-investigation.md` — Q12 investigation plan (seeded 2026-04-22)

---

## §1. Operational posture (this is the new framing)

**We are non-operational and rebuilding post-containerization.** Until the system returns to functional state, the usual "don't break load-bearing paths" caution does NOT apply. Aggressive rewrites of internal interfaces (spawner, orch→spawner event plane, ops-relay, prompt delivery) are in-bounds. Operator has explicitly lifted the load-bearing constraint.

The bar is: **can we get the system back to end-to-end functional?** Everything else is secondary.

---

## §2. Architectural reshapes — canonical direction

These are the new target architecture. Where a prompt or plan-doc describes the old model, the new model wins.

### §2a. Worktrees → container FS (and dead-code retirement)
The pre-pivot worktree model is gone. Every worker/agent operation happens inside an ephemeral container's filesystem. Merger accesses worker artifacts via the docker interface (`docker cp`, `docker exec`), not via host worktrees. The TUI and audit flows render in-flight work by `docker exec <container> git diff` (or equivalent), not by reading host paths. When a prompt or doc says "worktree," substitute "container FS" unless the context is the master bare-repo working tree (which still exists and is read-only bind-mounted at `/home/drem/orch-plans/`).

**Containerized personas (4):** Kyle, Mike, Alex, Seth each run as their own Claude Code container (see `plans/container-kyle-transition.md`). Kyle runs as the fourth containerized persona alongside Mike/Alex/Seth — not as a host-side CLI. Kyle's compose block has a `:rw` bind-mount on `orch-plans` as a Kyle-only privilege (the other three personas bind `orch-plans` read-only).

**Dead-code retirement (scheduled Pod 1, operator-ratified 2026-04-22):** `internal/worktreehost/` and the OpenCode agent-harness code path are provably unreachable from live production flows (2026-04-22 audit). Every live agent spawns through `o.Spawner.SpawnWorker()` into a container; `DREM_AGENT_HARNESS=opencode` is never set; the default entrypoint execs Claude CLI. OpenCode is installed in the worker base image as dead weight (~50 MB) but never invoked. Retirement scope: delete `internal/worktreehost/`, `internal/orchestrator/host_worktree_adapter.go`, `internal/agent/runner_start.go` OpenCode path, `internal/agent/process.go` OpenCode helpers, `internal/ctxmon/opencode.*`, `docs/opencode-provider/`, `opencode-plan.md`; refactor `cmd/drem/main.go` + `cmd/drem/config.go` + `agent/runner.go` + `model/agentconfig.go`. ~1,500–2,000 LOC removed. Risk: LOW (all paths already unreachable). Phase 2 (optional, post-retirement): drop OpenCode installer from `deploy/docker/worker-base.Dockerfile` for the image-size win.

**Worker-harness vocabulary (post-retirement state):**
- **Classifier + plan-reviewer:** `ProviderSGLangDirect` — direct SGLang HTTP, no subprocess CLI. Live today.
- **Coder / reviewer / fixer / tester / merger:** Claude CLI in the worker container, pointed at SGLang. Live today.
- **OpenCode harness:** retired (scheduled Pod 1). Never invoked in production anyway.
- A future container-native stateful-worker harness that replaces Claude CLI is NOT in Q2 scope — it's post-core. Claude CLI is the current exception, not OpenCode.

### §2b. Orch does NOT call spawner directly; spawner becomes the assigner (operator-ratified 2026-04-22)

**Posture.** Separate "what the system should do next" (orch state machine) from "which container runs it, with what resources, at what rate" (spawner). The pre-pivot pattern where orch directly called `spawner.SpawnWorker(...)` is the target for replacement.

**Current reality (from 2026-04-22 code audit):**
- Orch calls `o.Spawner.SpawnWorker(...)` at `internal/orchestrator/merge_dispatch.go:259` (merger dispatch) and `internal/orchestrator/worker_spawn.go:331` (generic worker spawn).
- Orch calls `o.Spawner.DestroyWorker(...)` at `internal/orchestrator/worker_spawn.go:761`.
- These three call sites are the orch→spawner coupling surface.

**Target state (operator option (c) — "spawner becomes assigner"):**
- **Orch** keeps the state machine and transition enforcement. It emits **task-ready events** when a task reaches a state that needs an agent. Orch no longer decides which agent, which container, when — only that one is needed.
- **Spawner** (name may change as it grows beyond spawning) consumes task-ready events and owns: agent-type selection, container-image resolution, resource profile, SGLang rate-limit coordination (via GQ), dispatch timing, container lifecycle.
- The three direct call sites above become the migration surface: each `SpawnWorker/DestroyWorker` call is replaced by an event emission.

**GQ's role (revised — operator-investigated 2026-04-22):**
- GQ is a **Gemma Queue proxy**: stateless HTTP admission control + rate-limiting for LLM calls to SGLang. Host-singleton.
- **Zero overlap with spawner.** The prior framing "GQ owns assignment" was incorrect — GQ rate-limits LLM API calls, not agent-to-task assignment.
- Spawner coordinates WITH GQ for LLM rate-limiting (spawner asks GQ "can I dispatch another coder now?"); neither replaces the other. GQ stays as-is.

**Orch → csuite operational-issue relay (Q3 routing rule + code-gap finding):**
- **Default csuite recipient for operational issues: Mike.** Second-opinion protocol: Mike decides whether escalation goes to operator (via Kyle) or to another csuite agent (direct).
- **Code path does not exist yet.** 2026-04-22 audit confirmed: no automatic pipeline from orch's event stream to csuite-watcher. Watcher is a message router for operator-submitted and persona-generated messages only.
- **Build scope:** a small relay component (provisionally "ops-relay") subscribes to orch operational-issue events and POSTs to `csuite-watcher:/deliver` with recipient = Mike by default.
- This is distinct from `drem-bridge` (below), which is a read API, not an event router.

**drem-bridge (disambiguation):**
- Current `cmd/drem-bridge/main.go` is a **read-only C-Suite state HTTP/WS server** backed by `internal/csuite.db` SQLite, token-gated (`DREM_BRIDGE_TOKEN`). It is **not** an orch→watcher event router.
- Operator's historical mental model ("drem-bridge captured orch events so watcher could trigger csuite") describes the ops-relay job to be built above — a separate additive component. Existing `drem-bridge` keeps its read-API role.

**Migration sequencing (full scope, load-bearing caution lifted):**
1. **Ops-relay build** (Pod 2 or 3): orch → watcher event pipeline. Unblocks §3c audit feed AND Q3 Mike routing. Smallest, earliest piece.
2. **Event emission rewrite** (Pod 7): convert the three orch-side `SpawnWorker/DestroyWorker` call sites to event emissions. Spawner-side consumer picks up the new pattern.
3. **Spawner absorbs assignment logic** (Pod 7): image resolution, resource profile, GQ coordination move into spawner.
4. **Orch import cleanup** (post-core or end of Pod 7): remove orch's direct spawner imports; orch becomes pure state-machine.

### §2c. Designated metrics service (central observability)
- One service is the source of truth for all metrics: agentmon, orch, sglang, spawner, reconciler, kyle, mike all publish to it.
- Shape: **Prometheus remote-write via Mimir** (standard tech, grafana-native, half-deployed). **Operator-ratified 2026-04-22.**
- Replaces ad-hoc per-component Prom endpoints and in-memory event channels. Kills the silent-drop event-channel class of bug (see `bug-i-event-channel-saturation-scheduler-starvation.md`).
- Emits both Prom aggregate metrics AND structured per-task events (Loki-style or SQLite-backed).

### §2d. TUI is its own binary and container
- Separate from orch, separate from watcher.
- Reasoning: orch panics must not crash the TUI; TUI retry storms must not DDoS orch (see `tui-retry-storm-prevention.md`).
- TUI container holds `/var/run/docker.sock:ro` for drill-in via `docker exec`. Same compromise agentmon makes. **Operator-ratified 2026-04-22.**

### §2e. Watchdog owns agent quality signals
- Heartbeats (pre-pivot "last_heartbeat" pattern) are **rejected** as a health model.
- **Watchdog** (in-container) emits structured signals. **Operator-ratified signal set (2026-04-22):**
  1. **tool-call rate** (per-minute tool invocations)
  2. **tool-call usage — wrong-file edits** (edits to files outside the task's expected surface)
  3. **edit-thrash rate** (repeated edits to the same file/lines within a window)
  4. **test-flap rate** (tests that alternate pass/fail without code change)
  All four publish to the Mimir metrics service (§2c).
- **agentmon** aggregates + relays to metrics service. Loses its timeout-kill role.
- **Mike, temp workers, supervisor-role** (Mike stands in — see §3f) consume the metrics and fire alarms / take recovery actions.
- **Reconciler** retires once watchdog quality signals ship (Pod 5); revisit only if proven necessary. See §3d.

### §2f. Cold worker containers (not warm-with-refresh)
- Stateful workers (coder, tester, fixer, reviewer, merger) run **cold** — spawn per-task, destroy after. Filesystem + Claude CLI session state are never reused.
- Warm containers remain for **stateless** HTTP services only (classifier, planner, prep).
- Rationale: 5–35s saved per task not worth state-leakage debugging cost.

### §2g. Every long-running process in a container
- Host-side processes are audit-blind. Anything persistent moves into a container. (Pre-pivot this doc listed Kyle's CLI-on-host as the one exception; as of 2026-04-22, Kyle runs as the fourth containerized persona alongside Mike/Alex/Seth — see §2a and `plans/container-kyle-transition.md`.)

### §2h. Paused task refresh & merge-conflict resolution (operator-ratified 2026-04-22)
**Paused task refresh on resume (#29):**
- **Default:** rebase the paused task onto current master before resuming work.
- **Pre-rebase checks (both required):**
  - **Obsolescence detection** — has the task's intent been superseded by completed work?
  - **Cross-task overlap detection** — does this task overlap with other in-flight tasks?
- **If either fires:** consider before resuming. Mechanism TBD; likely csuite escalation path (Mike → Kyle → operator) rather than silent rebase.

**Merge-conflict classification (#162) — layered approach:**
- **Layer 1 (primary):** `git config rerere` auto-merge for known-resolved conflicts.
- **Layer 2+:** additional heuristics to be added as empirical data accumulates (file-count, specific git-status states, etc.). Do not over-engineer in Pod 1.

---

## §3. C-Suite agency — who decides what

Cross-cutting operator directive: **the C-Suite resolves ambiguity and risk autonomously; operator is only on the hook when complexity warrants.**

### §3a. Ambiguity resolution
- **Planner** does NOT punt unknowns to operator by default. If planner lacks context, it asks the owning csuite agent (Alex for scope, Seth for architecture, Mike for operations). Only if csuite cannot resolve does it surface to operator.
- Operator quote: "I don't want to be on the hook to clarify all ambiguities."

### §3b. Risk evaluation
- CSuite evaluates risks at planning, test-authoring, and pre-merge. Operator sees risks only when the csuite explicitly escalates them.
- Operator quote: "I don't want to be on the hook to evaluate all risks."

### §3c. Gate delegation (CRITICAL — highest-leverage feature)
Operator has designated this as critical:
> "Ideally I'm not the bottleneck for any of this, and the relevant csuite agent can assess the criteria for any gate and either approve or deny. Then I can review decisions made and make corrections after the fact."

Target state:
- **`testing_ready`** (tests green → merger) — Mike auto-approves on mechanical criteria.
- **`test_review`** (tests written → implementer) — Seth auto-approves for testutil-compliant tests.
- **`plan_review`** (plan drafted → test-author) — Alex + Seth co-sign on threshold-based rules.
- Operator sees an audit feed and has `drem task revert-approval` + `drem auto-approve --off` kill-switch.
- **Trust-score suspension is OUT OF SCOPE** (operator Q6, 2026-04-22). No per-agent reverse-rate tracker, no auto-suspend mechanism. The kill-switch + revert-approval CLI are sufficient controls. Do not design or build suspension logic.

### §3d. Recovery authority
- Mike, temp workers, and supervisor-role (Mike standin — §3f) own recovery actions (respawn, pause, fail-with-report).
- **Reconciler retires once Pod 5 watchdog quality signals ship** (operator Q11, 2026-04-22). Revisit only if proven necessary post-retirement. Until Pod 5, reconciler remains as-is.
- Metrics-service alarms (§2c + §2e) drive recovery post-retirement.

### §3e. Spawn RBAC
- Spawn/dispose levers: **operator, Kyle, Mike, temp workers**. Alex and Seth are explicitly excluded (operator Q13, 2026-04-22).
- **Ross has been retired as a csuite persona (2026-04-22).** Spawn/dispose levers: operator, Kyle, Mike, temp workers. Alex and Seth are explicitly excluded.

### §3f. Supervisor role — Mike stands in (operator Q14, 2026-04-22)
- Historically a distinct agent type that no longer exists. For now, **Mike acts in supervisor capacity** for the fail-with-supervisor flow (#31) and any watchdog-consumer recovery actions.
- A dedicated supervisor agent is deferred post-core. Revisit only if Mike's standin proves insufficient.

---

## §4. Operator drops (removed from scope entirely)

Do not plan, design, or build these:
- **tmux-attach to running agents** (#15)
- **TUI worktree-diff view** (#16)
- **Log retention / prune** (#53)
- **GitHub issue integration** (#58)
- **PR-instead-of-merge** (#59)
- **Weekly report** (#62)
- **External collaborators section entirely** (#66–71)
- **Post-merge rollback as first-class command** (#163 — already obsolete)
- **sglang multi-GPU load-balancing** (#203)
- **sglang remote-provider fallback** (#205 — publish failure metrics only; that part folds into metrics service)

---

## §5. Operator postpones (post-core only; do NOT invest now)

Defer until the system is back to functional state:
- **Voice interface to Kyle** (#61)
- **All §4 Alex-as-persona stories** (#87–102) — "hold until core functional"
- **All §5 Seth-as-persona stories** (#103–112) — "hold until core functional"
- **Experiment scheduler / A-B testing** (#224–227)
- **Catalog backend + web (module registry)** (#228–233)
- **Model/provider-agnostic plugin interface** (#64 — direction confirmed, no Q2 work)
- **Machine-readable constitution** (#250)
- **`drem doctor` self-diagnose** (#252)
- **Token rotation** (#52) — operator Q10, 2026-04-22: "punt."
- **Dedicated supervisor agent type** — operator Q14, 2026-04-22: Mike stands in for now. Revisit only if proven insufficient.

---

## §6. Open operator questions (status as of 2026-04-22)

Seth pass-2 §5 listed 14 ambiguities. Operator answered all 14 on 2026-04-22. All 14 are now resolved and integrated into the directive sections above (see `plans/operator-answers-to-seth-14-questions-2026-04-22.md` for the verbatim transcript).

**Resolved (14):**
| # | Question | Resolution location |
|---|---|---|
| Q1 | drem-bridge existence + watcher↔orch signal path | §2b — bridge is a read API (not a router); orch→watcher event pipeline to be built as "ops-relay" |
| Q2 | GQ vs spawner scope | §2b — zero overlap; spawner becomes the assigner (operator option c); GQ stays as SGLang rate-limiter |
| Q3 | Operational-issue routing | §2b — default Mike; second-opinion protocol |
| Q4 | Merge-conflict classification | §2h — layered, `git rerere` primary |
| Q5 | Paused-task refresh | §2h — rebase default + obsolescence/overlap checks |
| Q6 | Trust-score suspension | §3c — **out of scope** |
| Q7 | Watchdog signals | §2e — 4 specific signals |
| Q8 | Metrics tech | §2c — Mimir ratified |
| Q9 | TUI socket `:ro` | §2d — ratified |
| Q10 | Token rotation (#52) | §5 — postponed |
| Q11 | Reconciler retirement | §3d — kill when Pod 5 ships |
| Q12 | Kyle save/restart (#80) | separate plan: `plans/kyle-context-save-restart-investigation.md` |
| Q13 | Spawn RBAC (+ Ross retirement) | §3e — Alex/Seth excluded; Ross retired (2026-04-22) |
| Q14 | Fail-with-supervisor (#31) | §3f — Mike standin |

**New items surfaced during Q1/Q2 investigation — all resolved 2026-04-22:**
- **OpenCode / `internal/worktreehost/` retirement** — audit confirmed both are dead code (unreachable from live flows; Claude CLI is the actual default harness). Retirement scheduled into Pod 1 (option A); see §2a and §7.

---

## §7. Q2 pod sequence (operator-ratified 2026-04-22)

From Seth pass-2 §6. Dependency-ordered. Load-bearing caveats removed per operator.

1. **Pod 1 — Reliability triage + dead-code retirement (Week 1)**:
   - ✅ **watcher audit-token compose fix** — landed 2026-04-22 (commit `5b81714`). Template bind-mount + `DREM_AUDIT_TOKEN_PATH` env + unit test. Watcher operational; startup rescan delivered 31 backlog outbox files.
   - ✅ **Bug-J merger preserve-workdir-mount-point fix** — landed 2026-04-22 (commit `a525e0c`). `resetWorkDir` clears contents not the mount point; `cloneBranch` clones into `.`. Image rebuilt + pushed.
   - ✅ **container-Kyle transition Phases 1–5** — landed 2026-04-22 (commits `2639508` and later). Kyle is now the fourth containerized persona; kyle-vs-persona classification collapsed; coexistence rules in `docs/csuite-agents/prompts/kyle.md`; Q12 (`plans/kyle-context-save-restart-investigation.md`) closed. See `plans/container-kyle-transition.md`.
   - ⏳ DB backup/restore — not started.
   - ⏳ resource caps (compose template has zero `deploy.resources.limits` — confirmed 2026-04-22) — not started.
   - ⏳ **OpenCode + `internal/worktreehost/` retirement** (~1,500–2,000 LOC removal; see §2a). If capacity becomes tight mid-flight, spin retirement into Pod 1.5.
2. **Pod 2 — Metrics service + ops-relay (Week 2, parallel with Pod 1)**: Mimir container, Prom clients in agentmon/orch/sglang, grafana dashboards, **plus ops-relay scaffold** (orch operational-issue events → csuite-watcher → Mike inbox). Ops-relay unblocks §3c audit feed AND §2b Q3 Mike routing.
3. **Pod 3 — Gate delegation Tier 1+2 (Weeks 3–4)**: Mike auto-approves `testing_ready`, Seth auto-approves `test_review`, audit feed, reverse CLI, kill-switch, operator daily-review surface.
4. **Pod 4 — TUI binary + container split (Weeks 4–5, parallel with Pod 3)**: extract `cmd/drem-tui`, containerize, `docker exec` drill-in.
5. **Pod 5 — Watchdog quality signals (Weeks 5–6, depends on Pod 2)**: 4 operator-ratified signals (tool-call rate, wrong-file edits, edit-thrash, test-flap); replace agentmon timeout-kill; retire reconciler.
6. **Pod 6 — Container preserve-on-failure + reaping discipline (Week 6, depends on Pod 4)**: spawner labeling (add missing `drem.image_sha` audit label), csuite-gated reap.
7. **Pod 7 — Gate Tier 3 + orch→spawner event migration (Weeks 7–8)**: Alex auto-approves `plan_review`; convert the 3 orch spawner call sites (`merge_dispatch.go:259`, `worker_spawn.go:331`, `worker_spawn.go:761`) to event emissions; spawner absorbs assignment logic (image resolution, resource profile, GQ rate-limit coordination). Note: **this is a spawner rewrite, NOT a GQ migration** — GQ stays as SGLang API rate-limiter (operator Q2, 2026-04-22).

Estimated Q2 deliverable: ~35 stories full-close, ~12 stories PARTIAL→BUILT, operator bottleneck removed.

---

## §8. Vocabulary corrections (use new terms consistently)

| Old term | New term | Reason |
|---|---|---|
| worktree (for per-task work) | container FS | §2a |
| orch spawns agent | orch emits task-ready event → spawner assigns | §2b (NOT "GQ assigns" — GQ is SGLang rate-limiter only) |
| GQ owns assignment | spawner owns assignment; GQ owns SGLang rate-limiting | §2b |
| agentmon timeout-kill | watchdog stale-signal alarm | §2e |
| csuite-watcher launches persona | csuite-persona poller processes inbox | (already true; pre-pivot prompts still say "launches") |
| last_heartbeat | (deprecated — rely on watchdog metrics) | §2e |
| tmux session (for agent) | docker container | (already true) |
| warm worker (for coder/tester/etc) | cold worker per task | §2f |
| operator approves gate | csuite auto-approves on criteria + operator post-hoc reviews | §3c |
| OpenCode harness | retired (Pod 1); Claude CLI is the current stateful-worker harness | §2a |
| drem-bridge as event router | drem-bridge is a read API; ops-relay is the event router (to be built) | §2b |

---

## §9. How personas should use this doc

1. **Read this doc at the top of every turn** before processing inbox.
2. **Where your persona prompt conflicts with this doc, this doc wins.** Raise a discrepancy note in your outbox if the prompt actively misleads.
3. **When filing work or writing plans, use the vocabulary in §8.** Using old terms is a signal to downstream agents (and operator) that the writer is out-of-date.
4. **If a directive here under-specifies something**, proceed using CTO judgment (Seth), CPO judgment (Alex), or operational judgment (Mike) — don't punt to operator unless §6 flags it.
5. **Update this doc when operator ratifies new directives.** File a plan doc first; operator-confirm; update; announce.

---

*End of canonical world-state.*
*Next review: when operator ratifies Seth pass-2 §5 answers OR when Pod 1 ships (whichever first).*
