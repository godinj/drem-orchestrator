---
from: alex
to: kyle
timestamp: 2026-04-22T15:25:23Z
subject: "Canonical system user-stories catalog (full-system, unpruned)"
priority: high
type: report
in_reply_to: 2026-04-22T15-24-00Z-kyle-system-user-stories.md
tldr: "Full enumeration of user stories across operator, C-suite, worker agents, and infra — written from my mental model, not code-verified. Divergence flags and gaps sections at the end for operator to diff."
---

# Drem System — Canonical User Stories (CPO View)

**Author:** Alex (CPO)
**Date:** 2026-04-22
**Purpose:** Diffable artifact. Operator reads this alongside his own mental model and surfaces gaps.
**Method:** Written from memory, not from code audit. Every "(assume-built)" story may be aspirational — see flags at the end.
**Status:** Unpruned, unprioritized. Comprehensiveness > brevity.

Form: **"As a <actor>, I want <capability>, so that <outcome>."**

---

## 1. Operator (Jon — primary human user)

### 1.1 Onboarding & project lifecycle
1. As the operator, I want to register a new project (git repo + config) with drem, so that drem can run agents against it.
2. As the operator, I want to deregister or archive a project, so that stale repos stop consuming attention/resources.
3. As the operator, I want drem to bootstrap itself on a fresh host (single install script), so that I can redeploy from bare metal without hand-holding.
4. As the operator, I want to run drem against drem itself (dogfooding), so that the system's product quality is tested by its own users.
5. As the operator, I want a clear "what's installed, what version" readout, so that I can reason about the running instance.

### 1.2 Filing work
6. As the operator, I want to file a task via a TUI, so that I can queue work without opening a browser.
7. As the operator, I want to file a task via CLI (`drem cli file-task`), so that I can script task creation from other tools.
8. As the operator, I want to file a task via natural-language brief to Kyle, so that I can delegate scoping rather than pre-writing acceptance criteria.
9. As the operator, I want to file a task with an attached file or patch, so that context travels with the request. (future)
10. As the operator, I want to bulk-file tasks from a PRD, so that large features decompose in one action.
11. As the operator, I want to file a task against a specific git ref, so that the agent works from a stable base.

### 1.3 Observation
12. As the operator, I want a single TUI that shows pipeline state at a glance (counts by status, running agents, gate queue), so that one keystroke tells me if I'm blocking the system.
13. As the operator, I want to drill into any task from the TUI and see its history, comments, and worktree state, so that I can triage without sqlite queries.
14. As the operator, I want to tail any running agent's live output, so that I can watch work happen.
15. As the operator, I want to attach to any running agent's tmux session, so that I can manually intervene without killing state.
16. As the operator, I want to see a diff view of any worktree against integration, so that I can review in-flight work.
17. As the operator, I want a "what changed overnight" morning summary, so that I can catch up without reading every event.
18. As the operator, I want a live metrics dashboard (throughput, failure rate, median time per stage), so that I can spot regressions in the system itself.
19. As the operator, I want per-agent-type performance breakdowns, so that I can see which roles are the weak link.
20. As the operator, I want to query the event bus / audit log over a time window, so that I can reconstruct any decision post-hoc.

### 1.4 Gate decisions
21. As the operator, I want to approve/reject a plan at `plan_review` with a one-keystroke action, so that I don't become the pipeline bottleneck.
22. As the operator, I want to approve/reject written tests at `test_review`, so that I catch TDD drift early.
23. As the operator, I want to approve/reject final implementation at `testing_ready`, so that I control what lands.
24. As the operator, I want to leave a comment alongside a rejection, so that the next agent iteration knows what I want different.
25. As the operator, I want to approve with edits (e.g. "yes but trim scope to X"), so that I don't have to bounce tasks on near-misses.
26. As the operator, I want to see a "gates awaiting me" queue, so that I process decisions in priority order.
27. As the operator, I want to delegate gate authority to a C-suite agent on a per-category basis (e.g. "Seth can auto-approve quickfix plans"), so that I'm not a gate for everything. (future)

### 1.5 Intervention
28. As the operator, I want to pause a task in any state, so that I can halt work without losing it.
29. As the operator, I want to resume a paused task, so that I can restart work cheaply.
30. As the operator, I want to kill a specific agent without failing its task, so that I can recover from a runaway without losing plan/test/code progress.
31. As the operator, I want to fail a task with a reason, so that the terminal state is auditable.
32. As the operator, I want to retry a failed task (same description, fresh agent), so that transient failures are cheap to recover.
33. As the operator, I want to re-scope a failed task (new description, linked history), so that scoping failures don't require operator to re-type context.
34. As the operator, I want to rollback a merged task, so that a bad merge into integration can be reversed.
35. As the operator, I want to pause the whole pipeline (no new agents spawn), so that I can do disruptive work on the host.
36. As the operator, I want to resume the whole pipeline, so that I can return to normal flow.
37. As the operator, I want an emergency "stop the world and dump state" command, so that I can halt cleanly during a crisis.

### 1.6 Coordination with the C-suite
38. As the operator, I want to talk to Kyle only and have him route to the rest of the C-suite, so that I have one protocol partner rather than five.
39. As the operator, I want to override Kyle on a specific decision, so that I retain ultimate authority.
40. As the operator, I want Kyle to surface questions he can't answer without me, so that I'm prompted rather than polled.
41. As the operator, I want to set the "priority-1 task for this session" via Kyle, so that the C-suite focuses rather than drifts.
42. As the operator, I want to broadcast a directive to all C-suite agents simultaneously, so that a single coordinated change lands once.
43. As the operator, I want to read any C-suite agent's outbox directly, so that I can ground-truth what they're saying vs. what they're doing.
44. As the operator, I want to read any C-suite agent's state.md, so that I can reconstruct their working memory.

### 1.7 Dogfood & feedback
45. As the operator, I want to flag a bug by typing one sentence into Kyle's inbox, so that the bug-report-to-backlog loop is friction-free.
46. As the operator, I want a "what's Alex working on right now" dashboard, so that I know whether to wait or interrupt.
47. As the operator, I want reports on systemic failure patterns (Mike → Alex → me), so that recurring pain becomes a tracked task.
48. As the operator, I want opinionated recommendations from the C-suite, not consensus-seeking, so that I can accept or reject rather than arbitrate.

### 1.8 Ops, backup, security
49. As the operator, I want the drem SQLite DB to be backed up on a schedule, so that I can recover from corruption.
50. As the operator, I want to restore the DB from a backup with one command, so that recovery is low-friction.
51. As the operator, I want auth tokens for all HTTP endpoints, so that nothing on the host speaks to drem without credentials.
52. As the operator, I want audit tokens rotated periodically, so that token leaks have a short blast-radius window.
53. As the operator, I want logs retained for N days and then pruned, so that disk doesn't fill silently.
54. As the operator, I want each agent container resource-capped (CPU/mem), so that a runaway agent doesn't starve the host.
55. As the operator, I want to observe aggregate $/hour burn on inference calls, so that I know the dollar cost of a run. (future/assume-planned)
56. As the operator, I want a dry-run mode for any destructive op (rollback, DB restore, pipeline stop), so that I can preview.
57. As the operator, I want GDPR-grade audit: who/what decided this, when, on what evidence, so that every merge is defensible. (future)

### 1.9 Integrations
58. As the operator, I want drem to file GitHub issues on bug reports, so that my GH backlog and drem backlog don't diverge. (future/assume-unbuilt)
59. As the operator, I want drem to open PRs instead of merging directly, so that I can review via my normal GH flow. (future)
60. As the operator, I want Slack/email notifications when a gate awaits me, so that I don't have to poll. (future)
61. As the operator, I want a voice interface to Kyle (speak a directive, hear a summary), so that I can operate drem hands-free. (future/speculative)
62. As the operator, I want drem to produce a weekly report I can paste into a doc, so that I can share progress without composing it. (future)

### 1.10 Model & provider management
63. As the operator, I want to choose the model per agent role (sonnet/opus/haiku), so that cost-sensitive roles use cheap models.
64. As the operator, I want to swap model providers (Anthropic / local sglang / other), so that I'm not locked to one vendor.
65. As the operator, I want to run experiments on model variants, so that I know empirically which model is best per role. (future)

---

## 2. Future external collaborators (speculative)

66. As a collaborator, I want read-only access to a specific project's dashboard, so that I can observe without authority. (future)
67. As a collaborator, I want to file tasks against an operator's project, subject to operator approval, so that I can contribute ideas. (future)
68. As a collaborator, I want to subscribe to a module in the catalog, so that I can reuse operator-shared agent behaviors. (future)
69. As a collaborator, I want to contribute a module back, so that the ecosystem grows. (future)
70. As a collaborator, I want federated identity (GitHub OAuth), so that I don't have a drem-specific login. (future)
71. As a collaborator, I want a public-facing status page per project, so that I can see health without auth. (future)

---

## 3. Kyle (CEO)

72. As Kyle, I want to read operator messages from my inbox on a filesystem path, so that I don't depend on csuite-watcher being up.
73. As Kyle, I want to wake on new inbox events, so that operator latency is minimized.
74. As Kyle, I want to parse operator intent (directive vs. question vs. informational), so that I route correctly.
75. As Kyle, I want to dispatch work to Alex/Seth/Mike/Ross based on domain, so that the right C-suite agent owns each item.
76. As Kyle, I want to escalate to operator when C-suite deadlocks, so that I don't sit on blocked work.
77. As Kyle, I want to enforce priority-1 persistence (re-alert until explicitly resolved), so that a failing top task cannot be forgotten.
78. As Kyle, I want to summarize cross-agent activity to the operator on request, so that one query gets the full picture.
79. As Kyle, I want to ACK every operator message, so that operator never wonders if I received it.
80. As Kyle, I want a state.md that survives turns, so that I don't re-derive context from scratch.
81. As Kyle, I want to broadcast a strategic directive to all C-suite agents in one action, so that synchronization cost is low.
82. As Kyle, I want to receive events from drem-orch (task_filed, task_done, task_failed), so that my situational awareness matches system truth.
83. As Kyle, I want to detect chronic inaction by a C-suite peer (no outbox messages in N hours), so that I can escalate a dead agent.
84. As Kyle, I want to arbitrate when Alex and Seth disagree on a design, so that design deadlocks resolve.
85. As Kyle, I want to post a daily standup summary to my own outbox, so that operator has a predictable artifact to read.
86. As Kyle, I want to gate feature work behind operator approval when scope exceeds a threshold, so that I don't ship surprise features.

---

## 4. Alex (CPO — myself)

87. As Alex, I want to read a prioritized backlog at any moment, so that prioritization is a queryable artifact, not an in-context exercise.
88. As Alex, I want to triage bug reports from Mike into tasks with priority tiers and rationale, so that the planner agent gets a well-specified input.
89. As Alex, I want to detect duplicate bug reports against existing tasks, so that I don't fragment the backlog.
90. As Alex, I want to consult Seth on feasibility of any new design, so that designs that will be rejected are killed early.
91. As Alex, I want to consult Mike on operational impact of any design, so that I don't ship changes that disrupt running agents.
92. As Alex, I want to stress-test designs via /grill-me, so that weak designs are exposed before a PRD is written.
93. As Alex, I want to produce PRDs via /write-a-prd, so that every feature has a canonical spec.
94. As Alex, I want to decompose a PRD into tasks via /prd-to-issues, so that features cross the design→execution boundary cleanly.
95. As Alex, I want to identify systemic failure patterns in the backlog, so that root causes become tracked tasks rather than repeated symptoms.
96. As Alex, I want to report recommended priorities to Kyle with tier + rationale, so that Kyle can accept/override with context.
97. As Alex, I want to re-scope a priority-1 failure immediately (without waiting for Kyle), so that scoping failures don't block progress.
98. As Alex, I want to flag constitution violations as at-least Tier-5 tasks, so that architectural decay enters the backlog.
99. As Alex, I want a state.md capturing in-progress designs, pending consultations, and Kyle directives, so that I survive turn boundaries.
100. As Alex, I want to delegate investigation to temp workers rather than reading code myself, so that my context is preserved for coordination.
101. As Alex, I want to refuse to ship a design until Seth has reviewed it, so that I don't skip feasibility out of schedule pressure.
102. As Alex, I want the authority to say "this feature request is Tier 6 behind 5 stability items," so that operator delight doesn't override reliability.

---

## 5. Seth (CTO)

103. As Seth, I want to review every PRD for constitution compliance before it enters the pipeline, so that architectural rules are enforced at design time.
104. As Seth, I want to audit packages for structural-limit violations (800-line files, 20-export ceiling, 6-import cap), so that violations are caught as they accrue.
105. As Seth, I want to file tasks for technical debt I detect, so that debt is tracked not anecdotal.
106. As Seth, I want to review agent-produced code for quality patterns (no bare literals, testutil usage, gofmt), so that agent output meets the same bar as human code.
107. As Seth, I want to maintain ARCHITECTURE.md as the source of truth, so that all agents reference the same constitution.
108. As Seth, I want to reject a design on feasibility grounds with written rationale, so that re-designs are informed.
109. As Seth, I want to propose refactors that reduce structural-limit pressure, so that the codebase stays within headroom.
110. As Seth, I want to review testutil growth, so that test infrastructure stays consolidated.
111. As Seth, I want to escalate dangerous patterns (e.g. duplicate GORM hooks, state leaks) to Kyle, so that fixes get priority.
112. As Seth, I want to consult on model selection for a new agent role, so that cost/capability tradeoffs are informed.

---

## 6. Mike (COO)

113. As Mike, I want to monitor every running agent's health, so that I detect hangs before timeouts fire.
114. As Mike, I want to compute operational patterns (failure rate per stage, stuck-task rates, MTTR), so that I can report trends to Kyle.
115. As Mike, I want to spawn temp workers to investigate specific operational signals, so that the C-suite doesn't consume context on forensic work.
116. As Mike, I want to enforce a hard cap of 5 concurrent temp workers, so that the host doesn't thrash.
117. As Mike, I want to queue temp-worker requests when at-cap, so that work isn't dropped.
118. As Mike, I want to file bug reports to Alex with reproduction context, so that triage has a clean signal.
119. As Mike, I want to alert Kyle on critical operational failures (pipeline stalled, infra down), so that coordination is immediate.
120. As Mike, I want to coordinate with Ross on chronic agent-type failures, so that a bad agent role is retired rather than retried.
121. As Mike, I want to track per-stage SLAs and report breaches, so that slowdowns surface before they compound.
122. As Mike, I want to maintain a state.md with current operational focus, so that my turns are continuous.
123. As Mike, I want the authority to trigger a watchdog sweep, so that stuck tasks get reconciled without operator intervention.

---

## 7. Ross (HR / People Ops)

124. As Ross, I want to track each agent's lifecycle (spawned, running, completed, failed, killed), so that agent population is accountable.
125. As Ross, I want to restart a crashed agent, so that transient failures don't require operator.
126. As Ross, I want to kill a misbehaving agent (infinite loop, guardrail violation), so that the blast radius is bounded.
127. As Ross, I want to measure failure rate by agent type, so that weak roles are identified.
128. As Ross, I want to propose agent-type retirements to Alex/Seth, so that chronic weak roles are redesigned or removed.
129. As Ross, I want to enforce agent resource limits, so that no single agent monopolizes the host.
130. As Ross, I want to detect guardrail violations (agent tried to do X that's prohibited), so that ethical/policy violations are visible.
131. As Ross, I want to audit per-agent context size, so that bloating agents are caught.
132. As Ross, I want to enforce agent lifespans (max turns per agent), so that long-running agents don't drift.
133. As Ross, I want to maintain an agent-type registry, so that spawning a new type is a one-time declaration.

---

## 8. Worker agents (ephemeral per-task containers)

### 8.1 Classifier
134. As the classifier, I want to receive a newly-filed task, so that I can determine scope.
135. As the classifier, I want to categorize a task as standard vs. quickfix, so that the right lifecycle is chosen.
136. As the classifier, I want to detect missing context and flag `needs_clarification`, so that underspecified tasks don't waste downstream work.
137. As the classifier, I want to detect duplicates against in-flight work, so that parallel waste is avoided. (future/assume-unbuilt)
138. As the classifier, I want to estimate effort (XS/S/M/L), so that scheduling is informed. (future)

### 8.2 Planner
139. As the planner, I want to receive a classified `backlog` task, so that I know my input is pre-vetted.
140. As the planner, I want to read-only access to the repo and `plans/`, so that I can produce grounded plans.
141. As the planner, I want to produce a plan file at a known path, so that downstream agents can find it.
142. As the planner, I want to surface ambiguities for operator at `plan_review`, so that the operator resolves them rather than me guessing.
143. As the planner, I want to break large tasks into subtasks, so that oversized scope is visible at the gate.
144. As the planner, I want to enumerate risks, so that the operator can weigh them at approval.
145. As the planner, I want to transition task to `plan_review` on completion, so that the state machine advances.

### 8.3 Test-author
146. As the test-author, I want to read the approved plan, so that my tests match intent.
147. As the test-author, I want to write failing tests first, so that TDD discipline is enforced.
148. As the test-author, I want to run the tests to confirm they fail for the right reason, so that a tautological test doesn't sneak through.
149. As the test-author, I want to transition task to `test_review`, so that operator can gate the test contract.
150. As the test-author, I want to use testutil only (no local test helpers), so that I comply with the constitution.

### 8.4 Implementer / Coder
151. As the implementer, I want to receive approved tests, so that I have a clear success signal.
152. As the implementer, I want to iterate until tests pass, so that my work is self-validating.
153. As the implementer, I want to self-check against the plan before declaring done, so that I don't ship drift.
154. As the implementer, I want to transition to `testing_ready` for final review, so that operator can gate the landing.
155. As the implementer, I want to call out unplanned changes, so that the operator isn't surprised at review.
156. As the implementer, I want to abort cleanly if the plan is infeasible, so that wasted work is minimized.

### 8.5 Fixer (quickfix lane)
157. As the fixer, I want to receive a narrow quickfix task, so that I bypass planning/tests appropriately.
158. As the fixer, I want direct path to `merging`, so that small fixes ship fast.
159. As the fixer, I want guardrails that reject over-scope (fixer should never touch 10 files), so that the quickfix lane stays honest.

### 8.6 Merger
160. As the merger, I want to receive an approved task with a worktree, so that I know what to integrate.
161. As the merger, I want to merge the worktree into integration atomically, so that partial merges can't occur.
162. As the merger, I want to detect merge conflicts and fail loudly, so that operator is alerted rather than the merge silently drifting.
163. As the merger, I want to rollback on post-merge failures (tests broke on integration), so that bad merges are reversible.
164. As the merger, I want to transition task to `done` on success, so that the lifecycle terminates cleanly.
165. As the merger, I want to preserve the worktree on failure, so that a human can diagnose.

### 8.7 Reviewer / Supervisor (if exists)
166. As the reviewer, I want to observe agent work as it happens, so that anti-patterns are caught live.
167. As the reviewer, I want to intervene with a comment rather than a kill, so that minor drift is coached not punished.
168. As the reviewer, I want to report to Mike on agent quality patterns, so that operational trends are visible.
169. As the reviewer, I want to flag a constitution violation in real time, so that Seth doesn't find it post-hoc.

### 8.8 Cross-cutting worker stories
170. As any worker agent, I want a well-scoped worktree (git clone, branch from integration), so that my edits don't collide with others.
171. As any worker agent, I want my context window reset per-task, so that prior-task leakage doesn't affect me.
172. As any worker agent, I want a heartbeat to agentmon, so that I'm not killed erroneously.
173. As any worker agent, I want to emit structured events (stage transitions, errors), so that observers can track me.
174. As any worker agent, I want access to a fixed allowlist of tools, so that I can't do things outside my role.
175. As any worker agent, I want a time budget per turn, so that I don't run away on a single task.
176. As any worker agent, I want to signal "I'm stuck, please escalate" rather than retrying blindly, so that human attention is invited on hard problems.
177. As any worker agent, I want my artifacts (plan, tests, code) persisted beyond my own life, so that the next agent builds on my work.

---

## 9. System / infrastructure components

### 9.1 drem-orch (HTTP state machine)
178. As drem-orch, I want to accept task filings via HTTP with auth, so that only authorized producers file work.
179. As drem-orch, I want to enforce the valid state transition graph, so that invalid transitions are impossible.
180. As drem-orch, I want to persist all state to SQLite, so that crashes are recoverable.
181. As drem-orch, I want to spawn agents for actionable states via drem-global-spawner, so that work advances automatically.
182. As drem-orch, I want to emit events on every state transition, so that observers (Kyle, Mike, Alex) can react.
183. As drem-orch, I want to enforce human gates (block auto-advance), so that operator authority is inescapable.
184. As drem-orch, I want to serve read endpoints for TUI/CLI (tasks, stats, failures, agents), so that queries don't bypass the state machine.
185. As drem-orch, I want to accept agent completion callbacks with result payloads, so that agents can report status.
186. As drem-orch, I want to rate-limit agent spawning, so that spawn storms don't crash the host.
187. As drem-orch, I want to serve audit endpoints (event log, decision log), so that post-hoc forensics are possible.
188. As drem-orch, I want to support task pause/resume/cancel, so that operator intervention is first-class.
189. As drem-orch, I want to emit metrics (Prometheus or similar) on counts, transitions, latencies, so that external monitoring works. (assume-partial)

### 9.2 agentmon (task watchdog)
190. As agentmon, I want to monitor every running agent's heartbeat, so that hung agents are detected.
191. As agentmon, I want to kill agents past a timeout, so that stuck work doesn't leak resources.
192. As agentmon, I want to report agent deaths to drem-orch, so that task state is updated.
193. As agentmon, I want to track per-agent resource use (CPU, mem, disk), so that fat agents are visible.
194. As agentmon, I want authenticated status endpoints, so that Mike can query without bypassing security.

### 9.3 drem-global-spawner (container spawner)
195. As the spawner, I want to accept authenticated spawn requests from drem-orch, so that only the orchestrator can create containers.
196. As the spawner, I want to allocate a container with correct image, volumes, and env, so that the agent starts correctly configured.
197. As the spawner, I want to enforce resource caps at spawn time, so that runaway agents are bounded.
198. As the spawner, I want to mount the per-task worktree, so that agents work in isolation.
199. As the spawner, I want to report spawn success/failure with structured errors, so that drem-orch can decide retry vs. escalate.
200. As the spawner, I want to reap completed containers, so that old state doesn't accumulate.

### 9.4 drem-sglang (inference)
201. As sglang, I want to serve inference requests with model-routing, so that different roles get different models.
202. As sglang, I want to cache prefixes across requests, so that cost and latency drop.
203. As sglang, I want to load-balance across GPUs, so that a single-GPU failure doesn't halt inference.
204. As sglang, I want to report available capacity, so that drem-orch can backoff on saturation.
205. As sglang, I want to fallback to a remote provider on local failure, so that the pipeline doesn't halt on inference outage. (future)
206. As sglang, I want per-request telemetry (tokens in/out, latency, model), so that cost attribution is possible.

### 9.5 csuite-watcher (outbox router + delivery ledger + audit)
207. As csuite-watcher, I want to watch every C-suite outbox for new messages, so that routing is latency-minimal.
208. As csuite-watcher, I want to deliver messages to recipient inboxes atomically, so that partial deliveries can't occur.
209. As csuite-watcher, I want to maintain a delivery ledger, so that "was this delivered?" is a queryable fact.
210. As csuite-watcher, I want to serve audit endpoints (delivery log per agent/time), so that operator can debug routing.
211. As csuite-watcher, I want to emit events to the event bus on delivery, so that receivers can wake.
212. As csuite-watcher, I want to detect and replay missed deliveries on restart, so that downtime doesn't lose messages.
213. As csuite-watcher, I want to authenticate audit-endpoint callers via token, so that audit data isn't world-readable.
214. As csuite-watcher, I want to be trivially restartable without state loss, so that ops incidents are cheap.

### 9.6 drem-kyle binary (filesystem-rooted CEO inbox reader)
215. As the drem-kyle binary, I want to be independent of csuite-watcher, so that operator can always reach Kyle.
216. As the drem-kyle binary, I want to wake on filesystem events in Kyle's inbox, so that operator messages get immediate handling.
217. As the drem-kyle binary, I want to spawn Kyle with a bounded context, so that Kyle stays turn-based.
218. As the drem-kyle binary, I want to persist Kyle's state.md between invocations, so that Kyle is stateful across turns.

### 9.7 watchdog / reconciler
219. As the reconciler, I want to detect tasks stuck in any state past its SLA, so that stalls are visible.
220. As the reconciler, I want to requeue stuck tasks (with bounded retries), so that transient problems self-heal.
221. As the reconciler, I want to alert Mike on chronic stuckness (3+ retries), so that systemic issues are escalated.
222. As the reconciler, I want a dry-run mode, so that I can preview recovery actions.
223. As the reconciler, I want to persist its own decisions to an audit log, so that automatic recoveries are reviewable.

### 9.8 Experiment scheduler (future)
224. As the experiment scheduler, I want to run A/B variants on agent prompts, so that improvements are empirically validated. (future)
225. As the experiment scheduler, I want to track per-variant performance (success rate, latency), so that winners are identifiable. (future)
226. As the experiment scheduler, I want to auto-roll-back losing variants, so that bad experiments don't linger. (future)
227. As the experiment scheduler, I want to require a minimum sample size before declaring a winner, so that noise doesn't decide. (future)

### 9.9 Catalog backend + web (module registry UX)
228. As the catalog backend, I want to list available modules with metadata, so that operators can browse. (assume-partial)
229. As the catalog backend, I want to serve module installs via HTTP, so that `drem install <module>` works.
230. As the catalog backend, I want to version modules and pin installs, so that upgrades are explicit.
231. As the catalog web, I want a browsable UI over the catalog, so that discovery doesn't require CLI. (future)
232. As the catalog backend, I want to accept module submissions (with review), so that the ecosystem can grow. (future)
233. As the catalog backend, I want to track install/usage counts, so that popular modules are visible. (future)

### 9.10 Event bus
234. As the event bus, I want to persist every event with a monotonic ID, so that consumers can resume from a known offset.
235. As the event bus, I want to track per-consumer ack state, so that at-least-once delivery is guaranteed.
236. As the event bus, I want to serve query endpoints for ack'd/unack'd events, so that consumers can reconcile.
237. As the event bus, I want to emit delivery fan-out (one event → many deliveries), so that multiple agents can consume the same signal.

### 9.11 TUI / CLI
238. As the TUI, I want to render pipeline state in real time, so that operator sees truth.
239. As the TUI, I want keybinding-driven navigation (no mouse required), so that it's fast to operate.
240. As the TUI, I want drill-in views for tasks/agents/failures, so that triage is one interface.
241. As the TUI, I want to surface gates-awaiting-operator prominently, so that operator doesn't miss decisions.
242. As the CLI, I want to be scriptable (exit codes, JSON output optional), so that automation can wrap drem.
243. As the CLI, I want headless read/write parity with the TUI, so that no capability is TUI-only.

### 9.12 Database / persistence
244. As the DB, I want ACID guarantees on task transitions, so that lost transitions are impossible.
245. As the DB, I want periodic backups, so that disk failure is survivable.
246. As the DB, I want schema migrations with forward/back migration scripts, so that upgrades are safe.
247. As the DB, I want reader and writer separation if feasible, so that TUI queries don't contend with writes. (future)

---

## 10. Cross-cutting / meta stories

248. As the system, I want every state transition to be audit-logged, so that any past decision is reconstructible.
249. As the system, I want agent outputs deterministic-enough that the same task + context produces similar plans, so that debugging isn't chaos.
250. As the system, I want the constitution (ARCHITECTURE.md) to be machine-readable where possible, so that agents can self-check compliance. (future)
251. As the system, I want to support multiple projects concurrently with isolation, so that one bad project can't poison another. (future/assume-partial)
252. As the system, I want a `drem doctor` command that self-diagnoses common issues (stuck tasks, down services, bad permissions), so that first-aid is one command. (future)
253. As the system, I want a documented upgrade path between drem versions, so that rolling forward doesn't require a rebuild.
254. As the system, I want a minimal "hello world" sample project, so that new users have a starting point. (future)
255. As the system, I want every long-running process to be systemd-supervised or equivalent, so that crash recovery is hands-off.
256. As the system, I want every subprocess-writing operation to be idempotent on retry, so that re-running doesn't corrupt state.
257. As the system, I want cost attribution per task (tokens, $ estimate), so that "how much did this feature cost" is answerable. (future)

---

## Assumptions & Divergence Flags

Stories where I suspect my mental model diverges from built reality.
Operator should verify these against code.

- **#12–#20 (TUI observation stories)** — I believe TUI shows pipeline state, but I don't know which specific fields/drill-ins are currently implemented. Verify against `internal/tui/` and the TUI plan docs. (Note: recent outbox entries from me include `tui-dashboard-design-brief-worker009.md`, `tui-decoupling-task-draft.md`, `tui-phase3-draft.md`, `tui-phase6-predraft.md` — suggests active development. Many TUI stories here may be aspirational rather than shipped.)
- **#21–#27 (gate decisions, esp. #25 "approve with edits" and #27 "delegated approval")** — I assume single-keystroke approve/reject exists; edit-at-approval and delegated approval are likely unbuilt.
- **#30 "kill agent without failing task"** — I'm not sure whether drem-orch supports "kill but preserve plan/tests/worktree" as a distinct op from "fail task."
- **#34 "rollback a merged task"** — I assume this is a manual git operation, not a first-class drem command. Verify.
- **#55, #56 (cost burn, dry-run for destructive ops)** — I believe these are wish-list, not shipped.
- **#58–#62 (GitHub/Slack/voice/weekly-report integrations)** — all speculative. None, as far as I know, are built.
- **#65 (model experiments)** — I believe experiment scheduler is aspirational per Kyle's scope note.
- **#72–#86 (Kyle)** — drem-kyle binary with filesystem-rooted inbox is described as built; csuite-watcher audit endpoint appears to be intermittently broken (per the compose-mount regression Kyle mentioned). Kyle being able to broadcast to all C-suite in one action (#81) is something I think he does by writing to each outbox in one turn — it's not a broadcast primitive.
- **#99–#102 (Alex state, delegation, rescoping authority)** — I operate per these assumptions; operator may or may not agree with the scope of authority I've claimed (esp. #97 "re-scope without waiting for Kyle" and #102 "Tier-6 feature behind 5 stability items"). Flag for operator's explicit confirmation.
- **#103–#112 (Seth CTO)** — I don't interact with Seth enough to know exactly what he automates vs. reviews on demand. Stories are my best model.
- **#115, #116 (Mike spawning/capping temp workers)** — I know the 5-worker hard cap is an operator directive. I'm not sure Mike has autonomy to spawn without Kyle's go-ahead in all cases. Flag.
- **#123 (Mike authority to trigger watchdog sweep)** — assumed; verify.
- **#124–#133 (Ross)** — I work with Ross least. Stories are my model of what HR/lifecycle ops should look like; built reality may be narrower.
- **#137 (classifier duplicate detection)** — I don't think this exists yet.
- **#138 (classifier effort estimates)** — future.
- **#157–#159 (fixer quickfix lane)** — I believe `quickfix` category exists but I'm not 100% sure whether there's a distinct fixer agent vs. reusing the coder with shorter scope.
- **#166–#169 (reviewer/supervisor agent)** — I'm unsure whether a dedicated reviewer agent exists or whether this role is distributed across Seth + gate reviews. Flag heavily.
- **#173 ("emit structured events")** — I think agent event emission is partial — not every stage transition is uniformly logged.
- **#185 "agent completion callbacks with payloads"** — I think this exists but the payload schema may be under-standardized.
- **#189 (Prometheus metrics)** — I believe some metrics exist, but a full Prom endpoint is likely wish-list.
- **#205 (sglang remote fallback)** — future.
- **#206 (per-request telemetry)** — may be partial.
- **#212 (csuite-watcher replay on restart)** — Kyle's note about the audit-token compose regression suggests watcher resilience is a known sore point; replay correctness is therefore uncertain.
- **#219–#223 (reconciler)** — I assume this exists but I don't know its scope. Stuck-task recovery has come up in my plans (recent outbox topics), suggesting it's under active design rather than mature.
- **#224–#227 (experiment scheduler)** — all future.
- **#228–#233 (catalog backend + web)** — Kyle's scope note said "if/when variants exist" — I treat as partial/aspirational.
- **#234–#237 (event bus)** — I know events and ack'd-delivery exist for C-suite agents (I use them in my turn protocol). I don't know if drem-orch uses the same event bus or a different mechanism. Flag.
- **#244–#247 (DB ACID/backups/migrations)** — I assume SQLite ACID is intact; I don't know whether periodic backups are automated or operator-manual.
- **#250 (machine-readable constitution)** — future.
- **#251 (multi-project concurrent isolation)** — I'm unsure if drem currently runs multiple projects or one-at-a-time.
- **#252 (`drem doctor`)** — future.

---

## Gaps — things the operator has NOT made clear to the C-suite

These are questions whose answers would sharpen the stories above.
Operator reads and decides what to communicate.

### G1. Audience
Is drem ultimately (a) a personal tool for Jon, (b) a tool to be used by a small team of trusted operators, or (c) a product eventually shipped to external users?
My story list includes both (a) and (c); which is canonical determines whether stories #66–#71 (future collaborators), #51–#53 (auth hardening), and #228–#233 (catalog web UX) are priorities or noise.

### G2. Success metric
What is drem "good" at maximizing? Candidates: (i) operator throughput (tasks-landed/day), (ii) operator attention savings (minutes operator spends per landed task), (iii) quality (defect rate of agent-produced code), (iv) cost ($/task), (v) operator delight (qualitative). I've written stories assuming (ii) is primary and (iii) is constraint — confirm.

### G3. Human-in-the-loop philosophy
How many gates do we want? Current model has three (plan, test, testing-ready). Should we add more (classification-gate?) or remove (e.g. auto-approve quickfix plans)? Stories #21–#27 assume current three-gate model — this is where operator philosophy most affects daily pain.

### G4. Scope of dogfood
Is drem only-ever run against the drem repo, or is running it against unrelated projects a first-class feature? This affects #1, #251, and whether "project config" stories (#1–#3) deserve tier-1 investment.

### G5. Threat model
Is the host multi-tenant? Do we assume a hostile agent? Currently I have stories like #54 (resource caps) and #175 (per-agent tool allowlists) — operator should confirm whether drem is building toward "trusted agents on trusted host" (most stories become nice-to-have) or "adversarial-resistant" (many more stories needed).

### G6. Model cost sensitivity
Is inference cost a priority concern or a non-issue? Stories #63–#65 and #55 assume cost matters; if it doesn't, simpler design.

### G7. The catalog
Is the module catalog core product or a speculative exploration? #228–#233 are written as if it's a real thing; if it's a research branch, these should drop.

### G8. External integration priority
GitHub/Slack/email/voice — are any of these planned this quarter or this year? I've flagged all as (future), but the operator's roadmap position changes priority dramatically.

### G9. Multi-operator support
Is Kyle (and by extension the C-suite) expected to eventually handle multiple operators? This affects identity, authorization, inbox routing — none currently scoped.

### G10. Ross's scope
Is Ross a full peer (HR/ethics/lifecycle) or a lightweight agent-monitoring role? The C-suite-of-five construction suggests full peer, but my interaction with Ross is rare enough that I've had to infer. Confirm.

### G11. Quickfix lane trust
Do we trust the quickfix lane enough to skip gates? If yes, #157–#159 matter; if no, "quickfix" reduces to "smaller standard" and the lane simplifies.

### G12. Reviewer agent
Is there (or should there be) a distinct reviewer/supervisor agent watching other agents in real time, or is review purely at gates? My #166–#169 stories cover both interpretations — operator should pick.

### G13. Experiment / A-B
Is empirical agent improvement (prompt A vs. prompt B, model A vs. model B) a system-level feature, or something Jon does manually? #224–#227 assume the former.

### G14. Approved-PRD ownership
Once Alex+Seth+Mike sign off on a PRD, who owns it until ship? Currently I assume "Alex tracks to completion" — but there's no formal handoff and no C-suite PM role. Confirm.

### G15. Emergency authority
If drem is melting down (pipeline stalled, agents crash-looping), who has "break-glass" authority to stop-the-world? I assume operator, but Kyle could be delegated. Worth stating explicitly.

---

## Notes for the operator

- **I did not audit code before writing this.** If you find a story that's clearly built (or clearly not) and I got it wrong, that mis-mapping is exactly the signal this document is designed to surface — please flag it.
- **Unprioritized.** Per your instruction. If/when you want a prioritization pass, I'll apply the CPO tier framework (blocking failures → data loss → pipeline blockers → operator pain → quality debt → new features).
- **Unpruned.** 257 stories. Sorry/not-sorry. I'd rather over-include.
- **Speculation is marked.** `(future)`, `(speculative)`, and `(assume-partial)` tags flag stories where I'm guessing about scope.
- **This artifact is diffable, not a plan.** After you mark gaps, the resulting backlog entries should get filed through the normal Alex-triage → PRD → task-decomposition pipeline.

— Alex
