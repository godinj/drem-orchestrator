# Operator Answers to Seth's 14 §5 Questions — 2026-04-22

**Context:** Seth (CTO) delivered his pass-2 unified-path synthesis on 2026-04-22T22:12Z at
`~/.drem-csuite/seth/outbox/20260422T221226Z-seth-to-kyle-user-stories-pass2-unified-path.md`.
Section §5 listed 14 architectural ambiguities needing operator input. Operator answered them
live in the Kyle session on 2026-04-22 evening. This doc captures the answers verbatim for audit
trail and for integration into `plans/c-suite-world-state-2026-04-22.md`.

**Status of each answer is annotated.** Twelve answers are directive and go straight into the
world-state doc. Two (Q1 + Q2) require code-reality-check investigation before they can be
finalized — a background temp worker was dispatched to answer them.

---

## Q1 — drem-bridge

> this was a component that captured events from orchestration that the watcher used to trigger
> csuite. not sure if this still exists or how watcher interacts with orch now.

**Status:** INVESTIGATION IN FLIGHT. Explore agent dispatched 2026-04-22 to determine whether
`drem-bridge` exists, what it does if so, and how csuite-watcher currently receives orch signals
if the bridge is gone.

---

## Q2 — GQ scope

> How does the spawner exist right now? I would imagine there's enough overlap to warrant
> combining these into one service.

**Status:** INVESTIGATION IN FLIGHT. Same Explore agent will enumerate spawner call sites,
current GQ shape, and the overlap surface. Operator's prior is "combine" — final direction
pending overlap map.

---

## Q3 — Operational-issue relay routing

> Mike. If Mike needs a second opinion, he has to determine if that's me (which he then
> notifies Kyle), or another csuite agent.

**Status:** RESOLVED. Mike is the default csuite recipient for operational issues emitted by
orch. Mike's second-opinion protocol: Mike decides whether the second opinion needs operator
(in which case Mike routes via Kyle) or another csuite agent (direct).

---

## Q4 — Trivial vs non-trivial merge conflict classification

> this is nuanced, it might need to be a layered approach starting with auto-merge with
> git config rerere

**Status:** RESOLVED (direction). Layered approach, starting with `git config rerere`
auto-merge. Additional heuristics to be added as empirical data accumulates.

---

## Q5 — Paused-task refresh state

> again this is nuanced. rebase should work most of the time, but if the task is obsolete or
> has overlap with other tasks in flight, we should consider before resuming.

**Status:** RESOLVED (direction). Default: rebase. Pre-rebase checks: (a) obsolescence
detection, (b) overlap detection against in-flight tasks. If either fires, consider before
resuming (mechanism TBD — likely csuite escalation or operator confirm).

---

## Q6 — Auto-approve trust-score thresholds

> I'm not worried about reversing decisions right now. more complexity than we need right now.

**Status:** RESOLVED. **Trust-score suspension is out of scope.** The auto-approve authorities
themselves (Mike Tier 1, Seth Tier 2, Alex Tier 3) remain. But the per-agent reverse-rate
tracker + auto-suspend mechanism is NOT built. Operator retains `drem task revert-approval` and
`drem auto-approve --off` kill switch as sufficient controls.

---

## Q7 — Watchdog drift-signal definitions

> tool-call rate, tool-call usage (editing the wrong files), edit-thrash, test-flap. these
> should all be captured as metrics.

**Status:** RESOLVED. Four signals, all captured as metrics via the Mimir metrics service:
1. tool-call rate
2. tool-call usage (editing the wrong files) — wrong-file detection
3. edit-thrash rate
4. test-flap rate

(Drops Seth's proposed "token-burn-without-progress," "busy-loop detection," and "idleness
detection" — operator's list is the complete set.)

---

## Q8 — Metrics service tech

> Mimir sounds good.

**Status:** RESOLVED. Prometheus remote-write via Mimir is the sanctioned metrics tech.
Ratifies Seth's §2c recommendation.

---

## Q9 — TUI container Docker socket

> that's fine.

**Status:** RESOLVED. TUI container may mount `/var/run/docker.sock:ro` for `docker exec`
drill-in. Same compromise agentmon already makes.

---

## Q10 — Token rotation (#52)

> punt.

**Status:** RESOLVED (postponed). Moves to `plans/c-suite-world-state-2026-04-22.md` §5
postpones. Post-core only.

---

## Q11 — Reconciler retirement timing

> kill it once watchdog quality signal lands, we can revisit later if it's truly needed.

**Status:** RESOLVED. Reconciler (`reconcileStuckAgents`) is retired once Pod 5 ships watchdog
quality signals. Revisit only if proven necessary post-retirement.

---

## Q12 — Kyle context save/restart slowness (#80)

> agreed, needs it's own plan doc / investigation

**Status:** RESOLVED (direction). Separate investigation plan doc seeded at
`plans/kyle-context-save-restart-investigation.md`. Candidate bottlenecks: state.md size,
claude-cli context-window rendering, watcher-restart latency. Investigation work not scheduled
into Q2 pods until root cause known.

---

## Q13 — Spawn RBAC

> let's get rid of Ross entirely, otherwise yes.

**Status:** RESOLVED — with major ripple. **Ross is being retired entirely as a csuite
persona.** Spawn RBAC confirmed for the remaining set: operator / Kyle / Mike / temp workers
only. Alex and Seth are explicitly excluded from spawn authority.

**Ross retirement scope (operator-confirmed):**
1. Stop + remove the Ross container; delete the compose service entry.
2. Delete `docs/csuite-agents/prompts/ross.md`.
3. Scrub Ross references from the other 4 prompts (kyle.md, alex.md, mike.md, seth.md) and
   from `plans/c-suite-world-state-2026-04-22.md`.
4. Redistribute Ross's responsibilities:
   - Container-lifecycle monitoring → Mike (already owns recovery authority).
   - People-ops framing → dropped.
   - Outbox/inbox reaping housekeeping (if any was Ross's) → orchestrator or Mike.

Executed as a dedicated edit pass; tracked separately from the other 13 answers.

---

## Q14 — Fail-with-supervisor flow (#31)

> supervisor is an agent type that used to exist, but let's just have Mike be the standin for
> this for now.

**Status:** RESOLVED. Mike acts in supervisor capacity for the fail-with-supervisor flow. A
dedicated "supervisor" agent type is deferred post-core; revisit if Mike's standin proves
insufficient.

---

## Summary of world-state doc changes

| Section | Change |
|---|---|
| §2b Orch→GQ | Add Q3 routing rule (Mike + second-opinion protocol) |
| §2c Metrics | Mark Mimir as operator-ratified (Q8) |
| §2d TUI | Mark socket `:ro` as operator-ratified (Q9) |
| §2e Watchdog | Replace signal list with operator's exact 4 signals (Q7) |
| §2h (new) | Paused task refresh & merge recovery (Q4 + Q5) |
| §3c Gate delegation | **Remove trust-score suspension mechanism** (Q6) |
| §3d Recovery | Strengthen reconciler-retirement timing (Q11) |
| §3e Spawn RBAC | Note Ross retirement pending; reaffirm Alex/Seth exclusion (Q13) |
| §3f Supervisor | Mike is standin; dedicated agent deferred (Q14) |
| §5 Postpones | Add token rotation (#52) (Q10) |
| §6 Open Qs | Strike 12 resolved items; retain Q1 + Q2 as "investigation in flight" |
| Referenced artifacts | Add this doc + Q12 plan doc |

## Ross retirement sequence

Separate edit pass covering:
- `docker compose rm -sf csuite-ross` + compose file edit
- Delete `docs/csuite-agents/prompts/ross.md`
- Scrub references in `docs/csuite-agents/prompts/{kyle,alex,mike,seth}.md`
- Scrub references in `plans/c-suite-world-state-2026-04-22.md`
- Rebuild csuite images (prompts changed)
- Restart 4 remaining personas
