# Drem Task Disposition — Containerization Pivot

Snapshot taken 2026-04-18T22:07Z from `~/git/drem-orchestrator.git/master/drem.db`.

## Legend

- **KEEP** — drains to `done` naturally on old system; work is still valuable after pivot.
- **RESCOPE** — survives in spirit but re-implemented in the new architecture (spawner, gitref, HTTP API, watchdog). Original drem task cancelled post-drain.
- **FREEZE** — leave stuck; pattern evidence for post-pivot audit or future bug work.
- **DROP** — obsoleted by the rearch. Cancel post-drain. Includes anything touching `internal/tmux/`, `internal/worktree/`, or host-side filesystem ops the new arch eliminates.

Principle (Kyle pivot §): nothing new enters drem. Existing in-flight drains to `done` or `failed`. Bugs found get docs (GitHub issues or plan entries), not drem tasks.

## In-flight (active states)

| ID | Title | State | Disposition | Reason |
|---|---|---|---|---|
| 20e6d5d3 | Integration verification of CAS reconciliation | in_progress | KEEP-if-drains / FREEZE-if-not | Drain preferred — reconciliation logic carries forward. If stalls, freeze as pattern evidence for skip-gates (20e6d5d3 already flagged same-tick backlog→in_progress). Do not force. |
| 3c2e67b9 | Retry suppression for constraint-gate failures in reconcile | test_writing | KEEP-if-drains / FREEZE | State-machine behavior survives the rearch. If tests finish pre-halt, let it reach `done`. Otherwise freeze. |
| 56fa181f | Fix lost-update race in task reconciler (optimistic concurrency) | test_review | FREEZE | Do not approve (gate freeze dir). DB concurrency is still a concern in new arch — log finding as GH issue, re-file outside drem post-pivot. |

## Stuck testing_ready (resurrection trio — frozen per dir #6)

| ID | Title | State | Disposition | Reason |
|---|---|---|---|---|
| 1ec2e26b | Fix dispatch scheduler stall during test_review → in_progress | testing_ready | FREEZE | Pattern evidence for skip-gates audit. Dispatch layer replaced by spawner + Docker events in new arch → the specific fix becomes moot; the lesson feeds spawner design. |
| 781e9ca4 | Audit constraint-gate bypass mechanism | testing_ready | FREEZE | Resurrection-family evidence. Re-file as GH issue for new-arch state-machine port. |
| 21650b21 | Refactor direct_tool_agent.go: extract TraceWriter + Loop | testing_ready | DROP | `direct_tool_agent.go` disappears in new arch (agents run inside containers via existing `claude` CLI / `opencode`). Refactor obsoleted. |

## Stuck plan_review (17 tasks — FREEZE all, individual disposition)

| ID | Title | Disposition | Reason |
|---|---|---|---|
| 4bfa2460 | touchTask helper + audit-trail integrity | DROP-from-drem / RESCOPE | cd83396a (commit 2e832c4) already landed a narrow `touchTask(task)` helper. 9-site audit + richer `touchTask(task, eventType, reason, details)` API unimplemented. Do NOT land via drem. Move remaining scope (site audit + richer event typing) to new-arch orchestrator-HTTP-API module scope — DB observer contract needs it anyway. See §cd83396a-diff below. |
| 7a17c213 | Self-termination logic for stalled planning tasks | DROP | Stall detection replaced by Docker events + spawner watchdog in new arch. Timer-polling approach reframed entirely. |
| db164be6 | Prevent agent trace file leakage via .gitignore + pre-merge hook | DROP-from-drem / GH-issue | Trace hygiene still relevant in new arch but mechanism (worker clones bare repo into container fs, merger runs in container) changes. File as GH issue: "worker-container image must include trace-file gitignore + pre-push hook". |
| feeb26a8 | Fix missing status_change event emission in post-merge failure | DROP-from-drem / RESCOPE | Event emission is HTTP-API concern in new arch (agentmon POST → orchestrator). Port the bug finding to new-arch `extraction` package tests. |
| e0a7fdaa | Triage auto-escalation with configurable timeout | RESCOPE | Triage UI is a TUI/HTTP-API concern — Phase 3 scope. |
| 66e68959 | Consolidate constitution enforcement path + cleanup | KEEP-as-doc | Constitution enforcement carries forward (ARCHITECTURE.md invariants). Finding stays as design input for new-arch merger module. |
| 8a261bdd | `drem triage approve` CLI command | RESCOPE | CLI rebuilt against HTTP API (Phase 3). Original implementation against direct DB access obsoleted. |
| 54466ad6 | "Needs Attention" badge + Triage View in TUI | RESCOPE | TUI Phase 3 (HTTP API). Draft remains design input. |
| 16f6144b | `drem triage dismiss` CLI command | RESCOPE | Same as 8a261bdd. |
| f48401c6 | `drem task retry <id>` CLI command | RESCOPE | Same. |
| 2684d2d0 | `drem triage` + `triage show <id>` CLI | RESCOPE | Same. |
| e369ee4b | `drem task cancel <id>` CLI command | RESCOPE | Same. |
| 8ae71ed7 | Unified task mutation helper in CLI package | DROP | CLI architecture changes — package-level helper obsoleted when CLI calls HTTP API. |
| 771a9643 | `drem task resolve <id> --reason` CLI | RESCOPE | Same as 8a261bdd. |
| 8de3f477 | `GetTriageTasks` DB method in new `internal/triage` package | DROP | Direct DB access from CLI forbidden per PRD §decision "Orchestrator is single state surface". Package stillborn. |
| dcfbeebb | `drem task resume <id>` CLI command | RESCOPE | Same as 8a261bdd. |
| 862e4063 | `drem task pause <id>` CLI command | RESCOPE | Same. |

## Stuck classifying (classify pair — frozen per pivot §classify pair CANCEL)

| ID | Title | Disposition | Reason |
|---|---|---|---|
| e742f63b | Wave 2.5: `drem task clear-context <id> <key>` CLI | DROP | Task-CLI completion deferred until post-rearch per pivot. Pair at 92min+ is auto-cancel-not-firing evidence — log to GH issue, don't replace. |
| 657e7ec8 | Wave 2.6: `drem task transition --force` CLI | DROP | Same. |

## Paused (4 tasks)

| ID | Title | Disposition | Reason |
|---|---|---|---|
| c7328749 | CLI: `drem experiment from-task` with plan reuse | DROP | Experiments package scoped out pre-pivot, definitely out post-pivot. |
| 9bea5d32 | Move `createTestOpenCodeDB` to testutil package | KEEP-as-doc | Testutil consolidation is correct direction — fold into new-arch testutil cleanup without drem task. |
| a263be29 | Migrate Inbox Protocol to SQL-based SSoT | RESCOPE | Inbox design carries forward but implementation site changes (Kyle-global container reads cross-project). Redesign as new-arch module. |
| a46c8a83 | Tests for retry suppression in reconcileFailedParents | KEEP-as-doc | Orchestrator state-machine port should include these tests. Finding survives, task drops. |

## Backlog (33 tasks — grouped by theme)

### Dispatch package refactor (6 tasks)

| IDs | Theme | Disposition |
|---|---|---|
| e1694dee / 9e549eb3 | Define dispatch package types + interface (dupes) | DROP — dispatch layer replaced by spawner RPC |
| 9baaa3fe / c1f39e20 | Rewire orchestrator to use dispatch interface (dupes) | DROP — same |
| 3a1055cb / 5bbe2d21 | Wrap fast-track transition loops in transactions (dupes) | DROP — fast-track pathway obsoleted |
| 09a5a688 | Add fast-track status transitions to `processCoderDirect` | DROP — `processCoderDirect` replaced by spawner |

### Compaction engine (4 tasks)

| IDs | Theme | Disposition |
|---|---|---|
| 4f076a0e | Shared compaction types + config | RESCOPE — compaction is a `claude` CLI / agent-harness concern; port to new-arch agent image build |
| 3e98ec12 | Implement message compaction engine | RESCOPE — same |
| 403e909f | Integrate compaction into `RunDirectToolAgent` loop | DROP — `RunDirectToolAgent` gone in new arch |
| da3ec34d | Integration wiring + config propagation | RESCOPE — wiring target is now the agent image + orchestrator HTTP API |

### Constraint-gate exhaustion (4 tasks)

| IDs | Theme | Disposition |
|---|---|---|
| a7b2e670 | Record exhaustion state in `evaluateConstraintGate` | KEEP-as-doc — state-machine port includes this |
| b6792b7b | Suppression in `reconcileParentsBy` | KEEP-as-doc |
| 149149eb | Suppression in `recoverFailedParent` | KEEP-as-doc |
| 7b80fb38 | Fix test failures + harden status filter | KEEP-as-doc |

### Watchdog / systemd / supervisor (4 tasks)

| IDs | Theme | Disposition |
|---|---|---|
| 2c9d4b6f | `SafetyTimer` watchdog with Pet/expire/dump | RESCOPE — new-arch worker watchdog (commit + push every minute) supersedes, different design |
| 0a6f1be8 | systemd notifier + unit file | DROP — orchestrator moves to Docker, systemd replaced by compose |
| 73fa8321 | Dead agent reaping | DROP — Docker events + OOM handling replaces polling-based reaping |
| 7356701c | Integration: wire watchdog + sd_notify into runner | DROP — sd_notify path obsoleted |

### Gate atomicity / reconciliation (5 tasks)

| IDs | Theme | Disposition |
|---|---|---|
| 5d64106e / b09c7b13 | Gate approval atomicity + processTestingReady race (dupes) | KEEP-as-doc — state-machine port must address |
| 621d139a | Tests for unassigned in-progress recovery | KEEP-as-doc |
| d2d06929 | Implement unassigned in-progress recovery | RESCOPE — behavior preserved; implementation site moves to spawner event loop |
| 0ad87919 | Wire recovery into `Reconcile()` end-to-end | KEEP-as-doc |
| 44d99768 | Test for non-constraint failure recovery | KEEP-as-doc |

### Inbox / CLI / misc (5 tasks)

| IDs | Theme | Disposition |
|---|---|---|
| 27d90b23 | Implement inbox package | RESCOPE — inbox moves to SQL-SSoT inside Kyle container |
| 4f8b6b4a | Implement `csuite-inbox` CLI | RESCOPE — Kyle-container tooling |
| 9d0f6c08 | No-op (behavior already exists) | DROP |
| ce95faea | Integration: scripts, migration, docs, prompts | KEEP-as-doc — migration scripts get refactored for container cutover |
| b5d8be5a / 3667f916 | Integration: constraints baseline + delete old test files (dupes) | KEEP-as-doc — test-layout hygiene carries forward |

### Other integration (5 tasks)

| IDs | Theme | Disposition |
|---|---|---|
| 3051cf89 | Integration: gate approval + build verify | KEEP-as-doc |
| 56a8ae7a | Integration: direct-dispatched subtasks not re-scheduled | DROP — direct-dispatch path gone |

## Summary counts

| Disposition | Count | Notes |
|---|---|---|
| KEEP-if-drains | 2 | 20e6d5d3, 3c2e67b9 — only if they naturally reach `done` before HALT completes |
| KEEP-as-doc | 12 | Findings survive as design input; drem task cancelled post-drain |
| RESCOPE | 15 | Work carries forward in new arch at different site |
| FREEZE | 3 | 56fa181f, 1ec2e26b, 781e9ca4 — stuck pattern evidence |
| DROP | 25 | Obsoleted by rearch (tmux/worktree/dispatch/CLI-direct-DB/systemd) |
| DROP-from-drem / RESCOPE | 2 | 4bfa2460, feeb26a8 — scope moves out of drem but survives as new-arch module concern |
| DROP-from-drem / GH-issue | 1 | db164be6 |

Total 60 non-terminal tasks surveyed (25 in-flight/stuck + 33 backlog + 4 paused, minus 2 KEEP-if-drains counted under in-flight).

## cd83396a vs 4bfa2460 diff (snapshot)

Per pivot: keep this analysis as input for the gitref rewrite. Will not land 4bfa2460 in drem either way.

- **cd83396a** (done, commit `2e832c4`): added `touchTask(task *model.Task) error` — 1-arg helper, calls `o.db.Save(task)` then `o.emit("task_updated", task)`. Used at merge-retry sites in `merge_execution.go`. +11 LOC in `orchestrator.go`.
- **4bfa2460** (plan_review): proposed `touchTask(task, eventType, reason, details)` — 4-arg helper with typed events. Refactor of 9 sites across `test_execution.go`, `reconcile.go`, `constraint_gate_policy.go`, `merge_execution.go`, `task_processing.go`. Plus `processTestingReady` short-circuit guard.

Overlap: helper name + single-step save+emit pattern. Gaps vs plan: (a) richer event-type parameterization not implemented, (b) 9-site audit not done, (c) `processTestingReady` short-circuit not implemented.

**Recommendation for new arch**: the orchestrator HTTP API package (per PRD §RPC and HTTP contracts → internal agentmon ingestion endpoint) needs structured event records anyway — fold 4bfa2460's richer API + site-audit work into the HTTP API module design. `processTestingReady` short-circuit reframes as a worker-death Docker-event handler guard in the spawner, not an orchestrator loop gate.

— alex
