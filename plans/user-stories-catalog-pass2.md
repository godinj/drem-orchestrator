# Drem System — User Stories Catalog (Pass 2)

**Author:** Alex (CPO)
**Date:** 2026-04-23
**Supersedes:** `user-stories-catalog-alex-pass1.md` and `user-stories-catalog-operator-annotated.md`.
**Governing authority:** `c-suite-world-state-2026-04-22.md`. Where this catalog conflicts with that doc, the world-state doc wins. Terminology follows world-state §8.
**Purpose:** Operator-annotated catalog collapsed into prioritized, pruned, ranked pass-2 artifact. Seth's pass-2 synthesis-diff runs off this.

---

## Method

Pass-2 applies five actions to the pass-1 catalog:

1. **Fold operator annotations.** Every annotation from `user-stories-catalog-operator-annotated.md` resolves into exactly one of: story rewritten, story moved to Deferred, story moved to Dropped, or annotation captured as a dependency/constraint on a surviving story. **No loose annotations survive in pass-2.**
2. **Drop out-of-scope** per world-state §4 (operator-ratified drops).
3. **Defer post-core** per world-state §5 (operator-ratified postpones).
4. **Rank active stories** P0/P1/P2 against world-state §7 pod sequence:
   - **P0** = Pod 1 + Pod 2 work (reliability triage, dead-code retirement, metrics + ops-relay). Required to return the system to end-to-end functional.
   - **P1** = Pod 3 + Pod 4 + Pod 5 (gate delegation Tier 1+2, TUI split, watchdog quality signals).
   - **P2** = Pod 6 + Pod 7 (container preserve-on-failure, Tier 3 gate + orch→spawner event migration) and infra polish.
5. **One new story** on C-suite temp-worker spawn parity (operator-raised; see §5 of this doc).

Pass-1 story numbers are preserved in parentheses for diffability. New stories get `NEW-n` IDs.

Per-story fields: `P-tier · OWNER · AC (acceptance criteria) · DEPS (dependencies)`.

---

## Counts

- **Active (ranked):** 124 stories (43 P0, 48 P1, 33 P2)
- **Deferred (post-core):** 44 stories
- **Dropped (operator-ratified):** 25 stories
- **Backlog (not ranked, speculative):** 10 stories
- **New:** 1 story
- **Original pass-1 count:** 257

---

## 1. Active — P0 (Pod 1 + Pod 2 reliability triage, back-to-functional)

P0 = the system is non-operational until these land. See world-state §1.

### 1.1 Orch core (state machine, persistence, transitions)

- **(#178) Accept task filings via HTTP with auth** · P0 · OWNER: orch · AC: auth-gated POST; rejects anonymous; works today · DEPS: none
- **(#179) Enforce state transition graph** · P0 · OWNER: orch · AC: invalid transitions return 4xx with structured error · DEPS: none
- **(#180-REWRITTEN) Persist task/agent state to SQLite** · P0 · OWNER: orch · AC: task/agent metadata in DB; **worker artifacts (plan, tests, diffs) live in container FS, not DB** (per operator annotation); crash recovery limited to state-machine rows · DEPS: container FS artifact access (§2a)
- **(#182) Emit events on every state transition** · P0 · OWNER: orch · AC: every transition produces an event consumable by ops-relay · DEPS: ops-relay (Pod 2)
- **(#183-REWRITTEN) Human-gate behaviour** · P0 · OWNER: orch + csuite · AC: gates route to csuite auto-approve path per §3c; operator is only surfaced when complexity warrants (per operator annotation); Mike default recipient for operational-issue routing (§2b Q3) · DEPS: gate delegation (Pod 3/7), ops-relay
- **(#184) Serve read endpoints for TUI/CLI** · P0 · OWNER: orch · AC: tasks/stats/failures/agents endpoints respond in < 200ms at p95 · DEPS: none
- **(#244) ACID guarantees on task transitions** · P0 · OWNER: DB · AC: no partial transitions; all transitions are single-txn · DEPS: none

### 1.2 Event bus + ops-relay (the orch → csuite signal plane)

- **(#234) Persist events with monotonic ID** · P0 · OWNER: event bus · AC: consumers resume from a known offset after restart · DEPS: none
- **(#235) Per-consumer ack state (at-least-once delivery)** · P0 · OWNER: event bus · AC: ack ledger is queryable per agent · DEPS: none
- **(#236) Query endpoints for ack'd/unack'd events** · P0 · OWNER: event bus · AC: Kyle/Alex/Seth/Mike can query their unacked deliveries · DEPS: none
- **(#237) Delivery fan-out (one event → many deliveries)** · P0 · OWNER: event bus · AC: one `task_filed` can fan to multiple subscribers · DEPS: none
- **(#187) Serve audit endpoints (event log, decision log)** · P0 · OWNER: orch + ops-relay · AC: operator can reconstruct any past decision via the audit feed · DEPS: ops-relay (Pod 2)
- **(#189) Emit metrics to designated metrics service** · P0 · OWNER: orch + metrics · AC: orch publishes transition counts/latencies via Prom remote-write to Mimir · DEPS: Mimir (Pod 2, §2c)
- **(#248) Every state transition audit-logged** · P0 · OWNER: ops-relay · AC: cross-system audit feed covers orch + csuite-watcher events · DEPS: ops-relay + metrics service

### 1.3 csuite-watcher + messaging substrate

- **(#207) Watch every C-suite outbox for new messages** · P0 · OWNER: watcher · AC: new outbox file → delivered within 2s · DEPS: none (operational today)
- **(#208) Deliver messages atomically** · P0 · OWNER: watcher · AC: no partial-file deliveries; rename-into-place or txn · DEPS: none
- **(#209) Maintain a delivery ledger** · P0 · OWNER: watcher · AC: "was msg X delivered?" is a queryable row · DEPS: none
- **(#210) Serve audit endpoints** · P0 · OWNER: watcher · AC: per-agent/per-time-window delivery logs accessible via token-gated endpoint (landed 2026-04-22 per world-state §7) · DEPS: none
- **(#211) Emit events on delivery** · P0 · OWNER: watcher · AC: receivers can wake on delivery events · DEPS: event bus
- **(#212) Detect + replay missed deliveries on restart** · P0 · OWNER: watcher · AC: startup rescan covers backlog outbox files (confirmed 2026-04-22: 31 files rescanned) · DEPS: none
- **(#213) Authenticate audit-endpoint callers via token** · P0 · OWNER: watcher · AC: landed 2026-04-22 (commit `5b81714`) · DEPS: none
- **(#214) Trivially restartable without state loss** · P0 · OWNER: watcher · AC: restart produces no lost or duplicated deliveries · DEPS: delivery ledger

### 1.4 Metrics + observability (Pod 2, §2c)

- **(#18) Live metrics dashboard** · P0 · OWNER: metrics (Mimir) + Grafana · AC: throughput, failure rate, median time per stage visible in Grafana · DEPS: Mimir deploy (Pod 2)
- **(#19) Per-agent-type performance breakdowns** · P0 · OWNER: metrics · AC: Grafana panel: success rate + p50/p95 per agent type · DEPS: Mimir
- **(#194-REWRITTEN) Designated metrics service is single source of truth** · P0 · OWNER: metrics · AC: agentmon, orch, sglang, spawner, reconciler, Kyle, Mike all publish to Mimir (per operator annotation) · DEPS: Mimir deploy
- **(#199-REWRITTEN) Spawner publishes success/failure metrics** · P0 · OWNER: spawner + metrics · AC: spawn outcomes land in Mimir with structured error codes (per operator annotation) · DEPS: Mimir
- **(#201-REWRITTEN) sglang publishes inference metrics** · P0 · OWNER: sglang + metrics · AC: tokens in/out, latency, model, cost visible in Grafana (per operator annotation) · DEPS: Mimir
- **(#206) sglang per-request telemetry** · P0 · OWNER: sglang + metrics · AC: per-request cost attribution queryable · DEPS: Mimir

### 1.5 Reliability triage (Pod 1)

- **(#49) DB backup on schedule** · P0 · OWNER: Mike · AC: hourly backup to host FS; retention TBD in impl plan · DEPS: Pod 1 task doc
- **(#50) DB restore with one command** · P0 · OWNER: Mike · AC: `drem db restore <snapshot>` restores in < 5 min; dry-run flag supported · DEPS: #49
- **(#54) Per-agent container resource caps** · P0 · OWNER: spawner + Mike · AC: compose template gets `deploy.resources.limits` (currently zero — world-state §7) · DEPS: spawner config
- **(#255-REWRITTEN) Every long-running process in a container** · P0 · OWNER: operator + Mike · AC: no persistent host processes except init/sshd; per §2g · DEPS: Kyle container (landed), agentmon container, reconciler container
- **(#162-REWRITTEN) Merge-conflict classification** · P0 · OWNER: merger · AC: `git rerere` handles trivial conflicts (Layer 1); non-trivial surface to csuite per §2h (not operator) · DEPS: world-state §2h spec

### 1.6 C-suite + operator coordination (operational today, kept active)

- **(#4) Dogfood drem against itself** · P0 · OWNER: Kyle · AC: drem's own backlog is managed via drem · DEPS: none (current pattern)
- **(#7) CLI file-task** · P0 · OWNER: CLI · AC: `drem cli file-task` returns task ID on success · DEPS: none
- **(#8) Natural-language brief to Kyle** · P0 · OWNER: Kyle + csuite-send CLI · AC: operator can file via `drem csuite send` one-shot prompt (per operator annotation to #45) · DEPS: `drem csuite send` (see `plans/drem-csuite-send-cli.md`)
- **(#38) Operator talks to Kyle only** · P0 · OWNER: Kyle · AC: Kyle is sole operator interface; routes to peers · DEPS: none
- **(#40-REWRITTEN) Kyle surfaces questions only when complex** · P0 · OWNER: Kyle · AC: per §3a, Kyle resolves ambiguity autonomously via csuite; escalates to operator only when complexity warrants · DEPS: none
- **(#41) Priority-1 task for session** · P0 · OWNER: Kyle · AC: Kyle holds P1 directive across turns and re-alerts on failure · DEPS: Kyle state.md
- **(#43) Operator reads any C-suite outbox** · P0 · OWNER: filesystem · AC: operator has host-level read access to all outboxes · DEPS: none (works today)
- **(#44) Operator reads any C-suite state.md** · P0 · OWNER: filesystem · AC: same as #43 · DEPS: none
- **(#45) One-sentence bug flag to Kyle's inbox** · P0 · OWNER: Kyle + csuite-send CLI · AC: one-shot CLI command to Kyle's container (per operator annotation) · DEPS: `drem csuite send` CLI
- **(#47) Systemic failure reports (Mike → Alex → operator)** · P0 · OWNER: Mike + Alex · AC: Mike produces weekly-ish pattern reports; Alex folds into backlog · DEPS: Pod 2 metrics
- **(#48) Opinionated recommendations from C-suite** · P0 · OWNER: Kyle · AC: Kyle delivers decisions, not option menus · DEPS: none

### 1.7 Kyle (operational today)

- **(#72) FS-rooted inbox** · P0 · OWNER: Kyle · AC: Kyle container polls filesystem inbox independent of watcher · DEPS: container-Kyle transition (landed)
- **(#73) Wake on new inbox events** · P0 · OWNER: csuite-persona poller · AC: poll interval ≤ 2s · DEPS: none
- **(#74) Parse operator intent** · P0 · OWNER: Kyle · AC: Kyle classifies as directive / question / informational · DEPS: none
- **(#75-REWRITTEN) Dispatch to Alex/Seth/Mike based on domain** · P0 · OWNER: Kyle · AC: Ross removed from routing target set (Ross retired per §3e); Alex = product, Seth = architecture, Mike = ops · DEPS: none
- **(#77) Priority-1 persistence (re-alert until resolved)** · P0 · OWNER: Kyle · AC: Kyle re-alerts P1 failure every turn until acknowledged · DEPS: Kyle state.md
- **(#79) ACK every operator message** · P0 · OWNER: Kyle · AC: every inbound operator message gets a reply within one turn · DEPS: none
- **(#80) state.md survives turns** · P0 · OWNER: Kyle · AC: context-save-restart is low-complexity (per operator annotation); see `plans/kyle-context-save-restart-investigation.md` (world-state Q12) · DEPS: Q12 investigation
- **(#82) Receive orch events** · P0 · OWNER: ops-relay + Kyle · AC: orch task_filed/task_done/task_failed events delivered to Kyle via ops-relay (§2b Q3 default: Mike; Kyle is strategic consumer) · DEPS: ops-relay (Pod 2)

### 1.8 Mike (operational today)

- **(#114) Compute operational patterns** · P0 · OWNER: Mike · AC: failure rate per stage, stuck-task rate, MTTR published to Mimir · DEPS: Pod 2
- **(#115) Spawn temp workers** · P0 · OWNER: Mike · AC: `drem spawn temp-worker` via Mike's authority; per-task brief input · DEPS: spawner RBAC (§3e)
- **(#116) Hard cap of 5 concurrent temp workers** · P0 · OWNER: Mike · AC: 6th request blocks or queues; count is host-global · DEPS: spawner
- **(#118) File bug reports to Alex with reproduction context** · P0 · OWNER: Mike · AC: every Mike-filed bug has repro steps + context · DEPS: none
- **(#119) Alert Kyle on critical operational failures** · P0 · OWNER: Mike · AC: pipeline-stalled or infra-down events reach Kyle inbox within one poll cycle · DEPS: csuite-watcher
- **(#122) state.md with current operational focus** · P0 · OWNER: Mike · AC: persisted across turns · DEPS: Mike container (operational)

### 1.9 Worker agents — classifier + planner + merger (end-to-end pipeline)

- **(#134) Classifier receives new task** · P0 · OWNER: classifier · AC: every new task enters classifier within one poll cycle · DEPS: none
- **(#135) Classifier categorizes standard vs quickfix** · P0 · OWNER: classifier · AC: every classified task has a category; default = standard · DEPS: none
- **(#136-REWRITTEN) Classifier flags needs_clarification** · P0 · OWNER: classifier + csuite · AC: ambiguity routed to owning csuite agent first (§3a); operator surfaced only when csuite can't resolve · DEPS: §3a
- **(#139) Planner receives classified backlog task** · P0 · OWNER: planner · AC: planner consumes `backlog` tasks · DEPS: #134-#136
- **(#140) Planner has read-only repo + plans/ access** · P0 · OWNER: planner · AC: bind-mount :ro; planner cannot write source · DEPS: spawner config
- **(#141) Planner produces plan at known path** · P0 · OWNER: planner · AC: plan file at `plans/<slug>.md`; discoverable by downstream agents · DEPS: bind-mount
- **(#142-REWRITTEN) Planner surfaces ambiguities via csuite, not operator** · P0 · OWNER: planner + csuite · AC: per §3a, planner asks Alex (scope) / Seth (architecture) / Mike (ops) first; operator is last resort · DEPS: §3a routing
- **(#144-REWRITTEN) Planner enumerates risks for csuite evaluation** · P0 · OWNER: planner + Seth · AC: per §3b, csuite evaluates risks at plan time; operator sees risks only when csuite escalates · DEPS: §3b, Seth risk-evaluation protocol (deferred §5 but routing applies today)
- **(#145) Planner transitions task to plan_review** · P0 · OWNER: planner · AC: state transition on plan completion · DEPS: orch
- **(#160-REWRITTEN) Merger receives approved task with container FS handle** · P0 · OWNER: merger · AC: per §2a, merger accesses worker artifacts via `docker cp` / `docker exec`, not host worktrees · DEPS: dead-code retirement (Pod 1, §2a)
- **(#161-REWRITTEN) Merger merges container-FS artifacts atomically into integration** · P0 · OWNER: merger · AC: no partial merges; single txn · DEPS: #160
- **(#164) Merger transitions task to done on success** · P0 · OWNER: merger · AC: state-machine transition emits `task_done` · DEPS: orch
- **(#165-REWRITTEN) Merger preserves container FS on failure** · P0 · OWNER: merger + spawner · AC: failed-merge container is not reaped; diff + artifacts accessible to csuite for post-hoc review (per operator annotation) · DEPS: spawner reap discipline (Pod 6)
- **(#170-REWRITTEN) Worker agent runs in a well-scoped container FS** · P0 · OWNER: spawner · AC: per §2a, each worker is its own ephemeral container; no host worktrees · DEPS: §2a
- **(#171) Context window reset per-task** · P0 · OWNER: spawner · AC: cold containers per §2f; no state reuse across tasks for stateful roles · DEPS: §2f
- **(#177-REWRITTEN) Worker artifacts persisted beyond agent life via cold-container offload** · P0 · OWNER: spawner + merger · AC: per operator annotation + §2f, artifacts offloaded to merger-accessible location before container is reaped · DEPS: Pod 6 preserve-on-failure

### 1.10 Spawner (Pod 1 baseline: caps + container FS)

- **(#196) Spawner allocates container with correct image/volumes/env** · P0 · OWNER: spawner · AC: spawn-time config validated against role schema · DEPS: none
- **(#197) Spawner enforces resource caps at spawn time** · P0 · OWNER: spawner · AC: caps applied per role (CPU/mem); currently zero (world-state §7) · DEPS: Pod 1 task
- **(#198-REWRITTEN) Spawner mounts per-task container FS (no worktrees)** · P0 · OWNER: spawner · AC: per §2a, per-task volume is the container's own FS; host worktree mount retired (Pod 1 §2a dead-code retirement) · DEPS: `internal/worktreehost/` retirement

### 1.11 Cross-cutting

- **(#242) CLI scriptable (exit codes, JSON output optional)** · P0 · OWNER: CLI · AC: `drem cli tasks --json` works for all commands · DEPS: none

---

## 2. Active — P1 (Pod 3 gate delegation + Pod 4 TUI + Pod 5 watchdog)

### 2.1 Gate delegation (Pod 3 = CRITICAL per operator)

Per operator annotation to #27 ("this is critical. Ideally I'm not the bottleneck"). World-state §3c is governing spec.

- **(#21-REWRITTEN) testing_ready gate operator sees audit + revert** · P1 · OWNER: TUI + Mike · AC: Mike auto-approves `testing_ready` on mechanical criteria per §3c; operator sees audit feed + one-keystroke `drem task revert-approval` · DEPS: ops-relay audit feed, `drem auto-approve --off` kill-switch
- **(#22-REWRITTEN) test_review auto-approved by Seth** · P1 · OWNER: Seth + TUI · AC: Seth auto-approves testutil-compliant tests per §3c; operator gets audit + revert · DEPS: Seth auto-approve criteria (Pod 3)
- **(#23-REWRITTEN) plan_review auto-approved by Alex+Seth** · P2 · OWNER: Alex + Seth + TUI · AC: Alex + Seth co-sign on threshold rules per §3c (Pod 7) · DEPS: Alex auto-approve criteria (Pod 7), `plan_review` is Tier 3 authority
- **(#24) Comment alongside rejection** · P2 · OWNER: TUI + csuite · AC: reject-with-comment routes comment to next agent iteration · DEPS: TUI (Pod 4)
- **(#26) Gates-awaiting-me queue** · P1 · OWNER: TUI + ops-relay · AC: audit feed surfaces revertable recent approvals (not an "awaiting" queue in the gate-block sense, since auto-approve clears the gate) · DEPS: ops-relay, TUI
- **(#27-REWRITTEN) Delegated gate authority per-category** · P0 · OWNER: csuite · AC: §3c is the full spec — Mike testing_ready (P1), Seth test_review (P1), Alex+Seth plan_review (P2); trust-score suspension explicitly OUT OF SCOPE per Q6; kill-switch + revert-approval CLI are sufficient · DEPS: ops-relay, metrics, kill-switch CLI

### 2.2 TUI (Pod 4 — separate binary + container per §2d)

- **(#6) TUI file-task** · P1 · OWNER: TUI · AC: one-keystroke file with cursor in description field · DEPS: Pod 4 TUI split
- **(#12-REWRITTEN) Single TUI — pipeline state + agent performance** · P1 · OWNER: TUI · AC: per operator annotation, TUI shows WIP + agent-performance view (not just gate queue) · DEPS: Pod 4, Mimir
- **(#13-REWRITTEN) Drill into task via container FS** · P1 · OWNER: TUI · AC: per operator annotation, drill-in calls `docker exec <container> git diff` — no host worktree path (§2a) · DEPS: Pod 4, docker.sock bind-mount (§2d)
- **(#14) Tail running agent live output** · P1 · OWNER: TUI · AC: `docker logs -f` live-stream via TUI · DEPS: Pod 4, docker.sock `:ro`
- **(#20) Event-bus time-window query** · P1 · OWNER: ops-relay · AC: `drem cli events --since=<window>` returns structured events · DEPS: ops-relay
- **(#46-REWRITTEN) "What's Alex working on" dashboard** · P1 · OWNER: TUI · AC: per operator annotation, TUI is its own binary + container (§2d); dashboard view shows per-persona current focus (from state.md) · DEPS: Pod 4
- **(#238) TUI renders pipeline state in real time** · P1 · OWNER: TUI · AC: refresh ≤ 2s · DEPS: Pod 4
- **(#239) Keybinding-driven navigation** · P1 · OWNER: TUI · AC: no mouse required for any workflow · DEPS: Pod 4
- **(#240) Drill-in views for tasks/agents/failures** · P1 · OWNER: TUI · AC: per-entity detail pages · DEPS: Pod 4
- **(#241-REWRITTEN) Surface revertable auto-approvals prominently** · P1 · OWNER: TUI · AC: per §3c, TUI surfaces recent auto-approvals for operator review (not "gates awaiting operator") · DEPS: ops-relay, audit feed
- **(#243) CLI/TUI read-write parity** · P1 · OWNER: CLI + TUI · AC: every TUI action has a CLI equivalent · DEPS: Pod 4

### 2.3 Watchdog quality signals (Pod 5, §2e)

- **(#113-REWRITTEN) Watchdog monitors agent health** · P1 · OWNER: Mike + watchdog · AC: per §2e, 4 signals (tool-call rate, wrong-file edits, edit-thrash, test-flap) published to Mimir · DEPS: Pod 5, Mimir
- **(#121) Per-stage SLAs + breach reports** · P1 · OWNER: Mike · AC: Mimir alarm on p95 breach per stage · DEPS: Pod 2 metrics
- **(#123-REWRITTEN) Watchdog/Mike recovery authority** · P1 · OWNER: Mike · AC: per §3d, Mike triggers watchdog-informed recovery actions (respawn/pause/fail-with-report); reconciler retires once Pod 5 ships · DEPS: Pod 5
- **(#166-REWRITTEN) Watchdog observes agent behaviour live** · P1 · OWNER: watchdog · AC: per §2e, 4 signals emit continuously during agent run · DEPS: Pod 5
- **(#168-REWRITTEN) Watchdog reports patterns to Mike** · P1 · OWNER: watchdog + Mike · AC: Mimir alarms fire on pattern thresholds; Mike consumes · DEPS: Pod 5
- **(#172-REWRITTEN) Watchdog tracks agent behaviour (no agent heartbeat)** · P1 · OWNER: watchdog · AC: per operator annotation, agent does not emit heartbeat; watchdog observes externally · DEPS: Pod 5
- **(#173-REWRITTEN) Watchdog tracks structured agent events** · P1 · OWNER: watchdog · AC: per operator annotation, watchdog responsible for stage-transition/error tracking (not agent-emitted) · DEPS: Pod 5
- **(#175-REWRITTEN) Watchdog surfaces busy-loop / idleness / drift** · P1 · OWNER: watchdog + Mike · AC: per operator annotation, watchdog surfaces signals; Mike (supervisor-standin per §3f) intervenes · DEPS: Pod 5
- **(#176-REWRITTEN) Watchdog escalates on stuck-worker signal** · P1 · OWNER: watchdog · AC: per operator annotation, watchdog escalates automatically; agent does not need to self-report "stuck" · DEPS: Pod 5
- **(#190-REWRITTEN) agentmon aggregates watchdog metrics to Mimir** · P1 · OWNER: agentmon + watchdog · AC: per §2e, agentmon loses timeout-kill role; becomes Mimir shim · DEPS: Pod 5, Mimir
- **(#192-REWRITTEN) Watchdog surfaces death signals via Mimir** · P1 · OWNER: watchdog + Mike · AC: Mimir alarm on agent-exit signals; Mike consumes · DEPS: Pod 5
- **(#193) Track per-agent resource use** · P1 · OWNER: metrics · AC: CPU/mem/disk per container in Mimir · DEPS: Pod 2
- **(#204) sglang reports available capacity** · P1 · OWNER: sglang + GQ · AC: GQ consumes capacity signal for SGLang rate-limiting · DEPS: GQ (existing)
- **(#205-REWRITTEN) sglang publishes inference-failure metrics** · P1 · OWNER: sglang + metrics · AC: per operator annotation — failure metrics only; no remote fallback logic · DEPS: Mimir
- **(#219-REWRITTEN) Watchdog/Mimir detects stuck tasks past SLA** · P1 · OWNER: watchdog + Mimir + Mike · AC: per §3d, Mimir alarms replace reconciler stall-detection · DEPS: Pod 5
- **(#220-REWRITTEN) Mike requeues stuck tasks** · P1 · OWNER: Mike · AC: per operator annotation, Mike (+ temps + supervisor-standin) manage recovery; reconciler retires · DEPS: Pod 5
- **(#221-REWRITTEN) Mimir alarm on chronic stuckness** · P1 · OWNER: Mimir + Mike · AC: alarm threshold configurable; Mike inbox gets alert · DEPS: Pod 5
- **(#222-REWRITTEN) Audit feed replaces reconciler dry-run** · P1 · OWNER: ops-relay · AC: per operator annotation, Mike/temps/supervisor manage recovery; "dry-run" becomes post-hoc audit review (§3c) · DEPS: ops-relay

### 2.4 Agent lifecycle + worker stories (P1 pod-adjacent)

- **(#30) Kill agent without failing task** · P1 · OWNER: orch + Mike · AC: `drem kill-agent <id> --preserve-task` keeps plan/tests/container artifacts · DEPS: container preserve-on-failure (Pod 6)
- **(#31-REWRITTEN) Fail task with reason + Mike-supervisor review** · P1 · OWNER: orch + Mike · AC: per operator annotation + §3f, fail-with-reason produces a supervisor review (Mike standin per §3f); csuite ingests and adjusts · DEPS: §3f, ops-relay
- **(#146)–(#156) Test-author + implementer pipeline** · P1 · OWNER: test-author + implementer · AC: TDD discipline; tests fail for right reason; transition gates · DEPS: none new (pipeline operational)
  - **(#150-REWRITTEN) testutil-only + scaffolding-signal-to-Seth** · P1 · OWNER: test-author + Seth · AC: per operator annotation, test-author signals Seth when testutil lacks coverage · DEPS: §3a routing
- **(#157-REWRITTEN) Fixer narrow quickfix (no plan/tests context)** · P1 · OWNER: fixer · AC: per operator annotation, fixer context excludes plan/tests (reduced context window) · DEPS: fixer prompt update
- **(#158) Fixer direct path to merging** · P1 · OWNER: fixer · AC: quickfix bypasses plan/test gates · DEPS: orch quickfix category
- **(#159) Fixer rejects over-scope** · P1 · OWNER: fixer · AC: scope-check (file-count heuristic) aborts over-scoped fixes · DEPS: §2h layer-2 heuristics
- **(#188-REWRITTEN) Pause/resume/cancel accessible to operator, Kyle, Mike, temps** · P1 · OWNER: orch · AC: per §3e spawn-RBAC + operator annotation, pause/resume levers match spawn-lever RBAC · DEPS: §3e
- **(#200-REWRITTEN) Spawner preserves diff + artifacts until csuite verifies safe-to-reap** · P1 · OWNER: spawner + csuite · AC: per operator annotation + Pod 6, no reap until csuite (Mike) signals · DEPS: Pod 6

### 2.5 Orch + infra (P1)

- **(#29-REWRITTEN) Resume paused task with refresh** · P1 · OWNER: orch + csuite · AC: per §2h, rebase onto master default; pre-rebase obsolescence + cross-task overlap checks; escalation path Mike → Kyle → operator if either fires · DEPS: §2h spec
- **(#55-REWRITTEN) Operator sees $/hour + tokens-in/out burn** · P1 · OWNER: sglang + metrics · AC: per operator annotation, tokens in/out AND $ estimate in Mimir · DEPS: Pod 2
- **(#57) GDPR-grade audit per merge** · P1 · OWNER: ops-relay + metrics · AC: every merge has who/what/when/evidence in audit feed · DEPS: Pod 2, ops-relay
- **(#117) Queue temp-worker requests when at-cap** · P1 · OWNER: Mike · AC: 6th request queued; FIFO release · DEPS: Mike state.md
- **(#185) Agent completion callbacks with result payloads** · P1 · OWNER: orch · AC: standardized payload schema · DEPS: payload schema spec
- **(#257) Cost attribution per task** · P1 · OWNER: metrics · AC: Mimir query returns tokens + $ per task ID · DEPS: Pod 2

### 2.6 Security

- **(#51) Auth tokens for all HTTP endpoints** · P1 · OWNER: Seth · AC: every HTTP surface is token-gated; audit endpoint already landed · DEPS: Pod 3 parallel

---

## 3. Active — P2 (Pod 6 + Pod 7 + polish)

### 3.1 orch→spawner event migration (Pod 7, §2b)

- **(#181-REWRITTEN) orch emits task-ready events; spawner assigns** · P2 · OWNER: orch + spawner + ops-relay · AC: per §2b, three orch call sites (`merge_dispatch.go:259`, `worker_spawn.go:331`, `worker_spawn.go:761`) become event emissions; spawner absorbs assignment logic · DEPS: Pod 7, ops-relay (Pod 2)
- **(#186-REWRITTEN) Spawner coordinates with GQ for SGLang rate-limiting** · P2 · OWNER: spawner + GQ · AC: per §2b Q2, spawner asks GQ before dispatching; GQ stays as SGLang rate-limiter (not assignment) · DEPS: Pod 7
- **(#195-REWRITTEN) Spawn RBAC: operator, Kyle, Mike, temp workers only** · P2 · OWNER: spawner · AC: per §3e (Q13), Alex and Seth cannot call SpawnWorker; Ross retired · DEPS: Pod 7

### 3.2 Spawner + container discipline (Pod 6)

- **(#165/#200 reinforced)** covered above in P1; Pod 6 is their full form.

### 3.3 Bootstrap + onboarding

- **(#1) Register new project** · P2 · OWNER: orch · AC: `drem project register` works; already partially live · DEPS: none
- **(#2) Deregister/archive project** · P2 · OWNER: orch · AC: `drem project archive` · DEPS: #1
- **(#3) Bootstrap on fresh host** · P1→P2 · OWNER: Mike · AC: one install script produces working drem container stack · DEPS: install-log.md
- **(#5) Installed version readout** · P2 · OWNER: Mike · AC: `drem version` prints all component versions · DEPS: none
- **(#11) File task against specific git ref** · P2 · OWNER: orch · AC: `--ref=<sha>` flag on file-task · DEPS: none

### 3.4 Intervention (P2)

- **(#28) Pause task** · P2 · OWNER: orch · AC: `drem task pause <id>` · DEPS: none
- **(#32) Retry failed task** · P2 · OWNER: orch · AC: `drem task retry` with fresh agent · DEPS: none
- **(#33) Re-scope failed task** · P2 · OWNER: orch + Alex · AC: new description + linked history; Alex persona's re-scope authority deferred to §4, but mechanical CLI live · DEPS: §4 deferral caveat
- **(#35) Pause whole pipeline** · P2 · OWNER: orch · AC: global pause flag; no new spawns · DEPS: none
- **(#36) Resume whole pipeline** · P2 · OWNER: orch · AC: unset global pause · DEPS: #35
- **(#37) Emergency stop-the-world + state dump** · P2 · OWNER: orch + Kyle · AC: `drem emergency-stop` halts + dumps state to FS · DEPS: none

### 3.5 C-suite polish (P2)

- **(#17) "What changed overnight" morning summary** · P2 · OWNER: Kyle · AC: Kyle generates overnight diff artifact on first turn of day · DEPS: Kyle state.md
- **(#39) Operator overrides Kyle** · P2 · OWNER: Kyle · AC: `drem csuite send --override` flag bypasses Kyle's decision · DEPS: csuite-send CLI
- **(#42) Broadcast directive to all C-suite** · P2 · OWNER: Kyle · AC: one action → messages to all persona inboxes · DEPS: csuite-send CLI
- **(#56) Dry-run for destructive ops** · P2 · OWNER: orch · AC: `--dry-run` flag on rollback/restore/pipeline-stop · DEPS: none
- **(#63) Model per agent role** · P2 · OWNER: orch · AC: per-role model config (supported today); planner-aware-of-coder-model per #143 · DEPS: #143
- **(#76) Kyle escalates deadlocks** · P2 · OWNER: Kyle · AC: after N back-and-forth turns without resolution, Kyle surfaces to operator · DEPS: Kyle state.md
- **(#78) Kyle summarizes cross-agent activity on request** · P2 · OWNER: Kyle · AC: `drem csuite send kyle "summarize since <time>"` returns summary · DEPS: csuite-send CLI
- **(#81) Kyle broadcast to all C-suite** · P2 · OWNER: Kyle · AC: implemented by Kyle writing to each peer outbox in one turn (no new primitive needed) · DEPS: none
- **(#83-REWRITTEN) Kyle detects chronic peer inaction** · P2 · OWNER: Kyle + Mike · AC: per operator annotation, metric must be accurate (low false-positive) — SLA-based, not silence-based; threshold documented · DEPS: Mimir (Pod 2)
- **(#84) Kyle arbitrates Alex/Seth disagreement** · P2 · OWNER: Kyle · AC: Kyle resolves disagreement within one turn or escalates · DEPS: §3a routing
- **(#85) Kyle daily standup post** · P2 · OWNER: Kyle · AC: one outbox file per day with standup summary · DEPS: none
- **(#86) Kyle gates feature work beyond threshold** · P2 · OWNER: Kyle · AC: scope-over-threshold features route to operator for pre-approval · DEPS: none
- **(#120-REWRITTEN) Mike coordinates with Kyle/Alex on chronic agent-type failures** · P2 · OWNER: Mike · AC: Ross retired (§3e); Mike surfaces chronic weak roles to Kyle/Alex directly · DEPS: none

### 3.6 Infra polish (P2)

- **(#167) Reviewer intervenes with comment not kill** · P2 · OWNER: Mike (supervisor standin per §3f) · AC: coaching comment in-container during drift · DEPS: Pod 5
- **(#169) Flag constitution violation live** · P2 · OWNER: watchdog + Seth · AC: live signal when agent writes code violating §ARCHITECTURE.md · DEPS: Pod 5, constitution heuristics
- **(#174) Tool allowlist per worker role** · P2 · OWNER: spawner · AC: Claude CLI `settings.json` allow-list per role · DEPS: none
- **(#202) sglang caches prefixes** · P2 · OWNER: sglang · AC: measurable p95 latency improvement · DEPS: none
- **(#215)–(#218-REWRITTEN) Kyle container = fourth containerized persona** · P2 · OWNER: Kyle · AC: per §2a, container-Kyle transition landed (Phases 1–5 per §7); Kyle's `orch-plans:rw` bind-mount is a Kyle-only privilege; context save/restart via Q12 investigation · DEPS: Q12
- **(#246) DB schema migrations** · P2 · OWNER: orch · AC: up/down migration scripts for every schema change · DEPS: none
- **(#249) Agent outputs deterministic-enough** · P2 · OWNER: Seth · AC: Seth maintains prompt + sampling params stable · DEPS: none
- **(#253) Documented upgrade path between drem versions** · P2 · OWNER: Seth + Mike · AC: `UPGRADE.md` per release · DEPS: none
- **(#256) Idempotent subprocess-writing ops on retry** · P2 · OWNER: Seth · AC: retry-safe by construction (constitution rule) · DEPS: ARCHITECTURE.md update

### 3.7 Planner / test / implementer refinement (P2)

- **(#143-REWRITTEN) Planner aware of coding model for subtask granularity** · P2 · OWNER: planner · AC: per operator annotation, planner reads coder model config and tunes subtask size accordingly · DEPS: orch config surface

---

## 4. Deferred (post-core — do NOT invest now)

Per world-state §5 and operator annotations. These return to the active backlog only after the system is back to functional.

### 4.1 Alex-as-persona (#87–#102) — ALL DEFERRED
Operator annotation: "Let's hold off on this whole section until we get the core of the system functional." World-state §5 codifies.

Stories: #87 prioritized-backlog, #88 bug-triage, #89 duplicate-detection, #90 consult-Seth-on-feasibility, #91 consult-Mike-on-impact, #92 grill-me stress-test, #93 write-a-prd, #94 prd-to-issues, #95 systemic-pattern-detection, #96 report-priorities-to-Kyle, #97 re-scope-without-waiting, #98 flag-constitution-violations-as-tier-5, #99 state.md, #100 delegate-to-temp-workers (see §7 of this doc — spawn parity story refines), #101 refuse-ship-until-Seth-review, #102 Tier-6-authority.

### 4.2 Seth-as-persona (#103–#112) — ALL DEFERRED
Operator annotation + world-state §5.

Stories: #103 PRD-review, #104 structural-limit-audits, #105 file-debt-tasks, #106 review-agent-code, #107 maintain-ARCHITECTURE.md, #108 feasibility-rejections, #109 refactor-proposals, #110 testutil-review, #111 escalate-dangerous-patterns, #112 model-selection-consult.

### 4.3 Other deferred items

- **(#52-DEFERRED) Audit token rotation** — world-state §5 (operator Q10: "punt"). Operator's annotation asked whether capacity exists — answer: tokens exist, no rotation mechanism, auditable by inspecting compose file. Revisit post-core.
- **(#61-DEFERRED) Voice interface to Kyle** — operator annotation + world-state §5 ("postponed until working system").
- **(#64-DEFERRED) Swap model providers (plugin interface)** — operator annotation: "eventually I want a model/provider agnostic interface." World-state §5: direction confirmed, no Q2 work.
- **(#65-DEFERRED) Model experiments** — world-state §5.
- **(#224–#227-DEFERRED) Experiment scheduler** — operator annotation + world-state §5.
- **(#228–#233-DEFERRED) Catalog backend + web** — operator annotation + world-state §5.
- **(#250-DEFERRED) Machine-readable constitution** — world-state §5.
- **(#252-DEFERRED) `drem doctor`** — world-state §5.
- **Dedicated supervisor agent type** — world-state §5 (operator Q14: Mike stands in per §3f; revisit only if Mike insufficient).

---

## 5. Dropped (operator-ratified, world-state §4)

Do not plan, design, or build. These are closed.

### 5.1 Operator drops (annotated into pass-1)

- **(#15) Tmux-attach to running agents** — operator annotation: "no longer necessary" + world-state §4.
- **(#16) TUI worktree-diff view** — operator annotation: "not necessary" + world-state §4.
- **(#34) Rollback a merged task** — world-state §4 (post-merge rollback as first-class command, "already obsolete" per operator annotation to #163).
- **(#53) Logs retained for N days and pruned** — operator annotation: "we can drop this."
- **(#58) GitHub issue integration** — operator annotation: "we can drop."
- **(#59) PR instead of merge** — operator annotation: "we can drop."
- **(#62) Weekly report** — operator annotation: "we can drop."
- **(#66–#71) External collaborators section** — operator annotation: "this whole section is out of scope" + world-state §4.
- **(#163-MERGED-INTO-#34) Merger rollback on post-merge failures** — operator annotation folds into #34 (already obsolete).
- **(#191) agentmon kill-on-timeout** — operator annotation + world-state §2e: agentmon loses timeout-kill role; rejected as health model.
- **(#203) sglang multi-GPU load-balancing** — operator annotation + world-state §4.

### 5.2 Ross retirement (world-state §3e)

- **(#120-partial) Mike coordinates with Ross** — REWRITTEN to Mike↔Kyle/Alex (see P2 §3.5 of this doc).
- **(#124–#133) Ross persona stories** — Ross retired 2026-04-22 per §3e. All 10 stories dropped.

---

## 6. Backlog — not ranked (speculative; no commitment)

Kept for bookkeeping. Not ready to rank.

- **(#9) File task with attached file or patch** — future.
- **(#10) Bulk-file tasks from a PRD** — future; Alex's `/prd-to-issues` covers a variant but pass-through delivery from operator is not scoped.
- **(#25) Approve with edits** — future; depends on auto-approve + revert being comfortable first.
- **(#60) Slack/email notifications** — future; operator has no annotation promoting.
- **(#137) Classifier duplicate detection** — future; pass-1 flagged as unbuilt.
- **(#138) Classifier effort estimates (XS/S/M/L)** — future.
- **(#247) DB reader/writer separation** — future; optimization.
- **(#251) Multi-project concurrent isolation** — future (see open question Q-A below).
- **(#254) Hello-world sample project** — future.

---

## 7. New story — C-Suite temp-worker spawn parity

### NEW-1: Spawn parity across C-suite (via approved broker)

**Background.** Operator asked whether Kyle + C-suite peers should all be able to assign temp workers. Pass-1 story #115 names Mike as sole spawner; pass-1 story #100 gives Alex "delegate to temp workers" without explicit spawn authority. World-state §3e (operator Q13, 2026-04-22) answers the spawn RBAC question: **operator, Kyle, Mike, and temp workers may spawn. Alex and Seth are explicitly excluded.**

This story codifies the mechanism: how Alex and Seth parallelize investigation without holding spawn authority.

**Story.** As Kyle and Mike, I want to spawn temp workers directly; as Alex and Seth, I want to request a spawn through Mike (the approved broker); and as the system, I want spawn RBAC enforced at the spawner boundary — so that investigation work parallelizes across the C-suite without violating the operator-ratified spawn policy.

**Owner.** Mike (broker) + spawner (RBAC enforcement) + Kyle (one authorized peer). Alex and Seth are requesters only.

**Mechanism.**
- **Warm containers** for stateless services only — `warm-direct-classifier`, `warm-direct-planner`, and a new `warm-direct-investigator` pool (stateless by construction). Per §2f, stateful worker roles stay cold.
- **Cold containers** for any temp worker that needs Claude CLI session state (forensic reads-only is fine in warm; anything writing is cold).
- **Spawn API** takes `originating_persona` + `task_brief`. Spawner checks persona against RBAC whitelist.

**Acceptance criteria.**
1. Spawner encodes RBAC: `SpawnWorker` callers must present one of `{operator, kyle, mike, temp-worker}`. Calls from Alex or Seth fail with `PERMISSION_DENIED`.
2. Alex and Seth can send a typed `temp-worker-request` message to Mike's inbox; Mike processes (accept / block / redirect) and, on accept, spawns via his own authority. Mike's decision is audit-logged in Mimir.
3. Kyle spawns directly — no Mike broker needed for Kyle-initiated investigations.
4. The 5-concurrent-temp-workers cap (operator directive) is enforced globally. Spawns originated by Alex or Seth count toward the cap against Mike's authority.
5. Warm-direct-classifier and warm-direct-planner containers exist and are reused across spawns (stateless). Warm-direct-investigator is a new pool.
6. Stateful roles (coder, tester, fixer, reviewer, merger) remain cold per §2f.
7. Metrics: every spawn is attributed to `originating_persona` in Mimir. Queryable via `drem cli temp-spawns --by=persona`.
8. Mike has a queue for at-cap-blocked requests (existing story #117 generalized to cross-persona).

**Dependencies.**
- World-state §2b orch→spawner event migration (Pod 7) — RBAC enforcement happens in spawner once assignment logic migrates there.
- World-state §2a dead-code retirement (Pod 1) — worker-base image clean before warm pools stabilize.
- World-state §2f cold-worker baseline (ratified).
- Mimir metrics service (Pod 2) for per-persona attribution.
- Pod 3 + Pod 7 gate delegation — Alex and Seth start auto-approving gates; their investigation bandwidth becomes the bottleneck, which is the motivating problem for this story.

**Priority.** **P1** — depends on gate-delegation landing (Pod 3/7), so it is not P0, but blocks the next productivity unlock.

**Out of scope (in this story).**
- Giving Alex or Seth direct spawn authority — world-state §3e closes that door.
- Replacing Kyle's spawn authority with a Mike broker — Kyle is a peer of Mike on spawn RBAC.
- Warm containers for stateful roles — violates §2f.

---

## 8. Open questions (surfaced, not folded into body per scope discipline)

Following Kyle's scope-discipline directive: these look like missing coverage but were not action-items for pass-2. Flagged for his routing decision.

### Q-A. Multi-project isolation story gap
Pass-1 story #251 ("multi-project concurrent with isolation") is backlog-unranked. No explicit scope for whether drem is single-project today or multi-project ready. World-state doesn't address. Operator pass-1 gap G4 asked the same. Consider a future pass-1.5 on multi-project semantics if operator wants a lane.

### Q-B. Ops-relay story coverage
World-state §2b introduces ops-relay as a new component ("orch operational-issue events → csuite-watcher → Mike inbox"). Pass-1 has no story for ops-relay as a first-class component — stories #82, #211, #222 imply its functions but don't own them. If you want a discrete story set for ops-relay (its own inbox/outbox/state like a persona), I can add one in a pass-2.5.

### Q-C. GQ story coverage
World-state §2b carves GQ as a standalone service (SGLang rate-limiter, zero spawner overlap). Pass-1 has no GQ-owned story set. Same situation as Q-B — discrete story set would require a pass-2.5.

### Q-D. Mimir / metrics-service story coverage
Similar gap — Mimir is referenced as the destination by many stories (#18, #19, #114, #121, etc.) but has no "Mimir wants to…" stories of its own (retention, cardinality, sharding). Pass-2 treats it as infrastructure plumbing, not a first-class persona. Flag for future if operator wants a metrics-service design pass.

### Q-E. Alex auto-approve criteria
Story #23 (`plan_review` auto-approve by Alex+Seth) is Tier 3 per world-state §3c and is my (Alex's) most-emphasized directive. The threshold rules that Alex+Seth co-sign on are not yet written — Pod 7 needs a design doc before the story becomes actionable. I'll volunteer to draft that separately if you want to treat it as a work item independent of this pass.

### Q-F. csuite-send CLI as implicit dependency
Stories #8, #39, #42, #45, #78 all depend on a first-class `drem csuite send` CLI that is tracked in `plans/drem-csuite-send-cli.md` but has no explicit story in pass-1. Worth a discrete story if we're tracking it as a first-class capability.

### Q-G. "Reviewer agent" collapse — fully resolved?
Pass-1 gap G12 asked whether reviewer/supervisor is a distinct agent or distributed. World-state §3f answered "Mike stands in; dedicated supervisor agent deferred." Pass-2 rewrites #166–#169 accordingly. But the operator annotation to #165 ("figure out a viable approach for preserve-on-failure") and to #177 ("warm container state refresh / cold preferred") both point at container-lifecycle design rather than reviewer-agent design. Flagging that if there's a gap, it's in Pod 6 container-preserve specs, not in a new reviewer role.

### Q-H. Kyle context save/restart (story #80) duration complaint
Operator annotation on #80: "saving and restarting context takes a fairly long time, especially over the course of a longer conversation. I'd like to find a way to minimize the complexity of this operation." World-state §6 Q12 points at `plans/kyle-context-save-restart-investigation.md`. That plan doc is seeded but not closed — pass-2 treats #80 as P0 with a dependency on Q12. If the Q12 plan hits a design decision, it may promote itself into its own story set.

---

## Notes

- **Every pass-1 annotation has a resolution** above (rewrite / defer / drop / captured-as-dep). I did not leave any loose. The resolution is stated inline with the affected story.
- **Pass-1 gaps G1–G15** are mostly closed by world-state §3–§6. The still-open residual is Q-A (multi-project) above.
- **Pass-1 "Assumptions & Divergence Flags"** section is superseded — the world-state doc and operator annotations together resolve most flags. Pass-2 makes the resolutions the canonical state.
- **Vocabulary** is world-state §8 throughout. No references to "worktree" for per-task work; no references to "orch spawns agent" as current posture; no references to agent-emitted heartbeat.

— Alex (CPO)
