# Drem Orchestrator Through the Lens of *Accelerate*

An analysis of how the drem-orchestrator design maps to the findings from *Accelerate: The Science of Lean Software and DevOps* (Forsgren, Humble, Kim, 2018).

---

## 1. Trunk-Based Development — Deeply Embodied

**Accelerate's finding:** Teams with fewer than 3 active branches, branch lifetimes under a day, and no code freezes are high performers.

**Drem's design directly enables this.** The worktree architecture creates short-lived agent branches (`feature/<name>/agent-<uuid>/`) that are merged into an integration branch as soon as work completes. The `BuildSchedule()` algorithm with file-overlap conflict graphs means multiple agents work in parallel on non-conflicting files, then merge quickly — exactly the "short-lived branches merged at least daily" pattern Accelerate found correlates with performance.

The rebase-before-merge strategy in `MergeAgentIntoFeature()` keeps history clean and reduces integration pain — the book specifically notes that short integration times (< 1 day) combined with short merging periods produce better delivery performance.

## 2. Loosely Coupled Architecture — The Biggest Win

**Accelerate's finding:** Loosely coupled architecture was the **single biggest contributor to continuous delivery** in the 2017 analysis — bigger than test automation or deployment automation.

The key test from the book: "Can teams make large-scale changes to their system without the permission of somebody outside the team?" and "Can they deploy independently?"

**Drem operationalizes this through worktree isolation.** Each agent gets its own worktree, branch, and tmux session. Agents can test, build, and commit independently. The `scheduler.go` file-overlap analysis is essentially an automated version of the book's architectural ideal: ensuring work units don't create coupling at the implementation level.

The package structure itself (`internal/agent/`, `internal/merge/`, `internal/state/`, etc.) follows Conway's Law — each concern is encapsulated, and the orchestrator coordinates between them without tight coupling. The constitution enforcing max 6 internal imports per package is a concrete enforcement of loose coupling.

## 3. Version Control — Everything in Version Control

**Accelerate's finding:** Keeping **system and application configuration** in version control was *more correlated* with performance than keeping code in version control.

Drem puts everything in version control:
- Agent prompts are generated from code, not stored externally
- Configuration is TOML-based and versionable
- The `ARCHITECTURE.md` constitution is version-controlled
- Build/test commands are read from `CLAUDE.md` in the worktree
- The `check_constitution.sh` enforcement script is versioned

## 4. Test Automation — Built Into the Pipeline

**Accelerate's finding:** Tests must be **reliable** (when they pass, you're confident the software is releasable), and **developers** should create and maintain them, not a separate QA team.

Drem has a `testing_ready` state where automated gates run (`compile_command`, `test_command` with optional `scoped_tests`). The state machine enforces that work cannot reach `merging` without passing through `testing_ready`. This is exactly the deployment pipeline pattern the book advocates.

The constitution's test infrastructure rules (all test factories in `internal/testutil/`, minimize real I/O in unit tests) align with the book's emphasis on test reliability and maintainability.

## 5. Continuous Integration — The Deployment Pipeline

**Accelerate's finding:** Every commit should trigger a build and run fast, automated tests.

Drem's orchestrator tick loop (every 5 seconds) continuously monitors agent work, triggers test gates on completion, and manages the merge pipeline. The `testing_ready -> merging -> done` progression IS a deployment pipeline — just for agent-produced code rather than human-produced code.

## 6. Lean Management — WIP Limits, Visual Management, Feedback

**Accelerate's finding:** WIP limits alone don't predict performance — they must be combined with visual displays and production monitoring feedback.

**Drem implements all three:**
- **WIP limits**: `max_concurrent_agents` (default 5) + semaphore-based throttling
- **Visual management**: The Bubble Tea TUI dashboard shows task status, agent health, context window usage, and progress in real-time
- **Production feedback**: Context window monitoring (`ctxmon`), heartbeat monitoring, stale agent detection, and reconciliation audits are continuous feedback loops

The book specifically says these three must work *together* to be effective. Drem's design tightly integrates them — the TUI shows the WIP state, the monitoring feeds back into scheduling decisions, and the reconciliation loop audits consistency.

## 7. Lightweight Change Approvals — The Most Surprising Finding

**Accelerate's finding:** **External approval bodies (CABs) do not improve stability and actively slow things down.** Peer review + deployment pipelines outperform all other approaches.

Drem implements this with nuance: there ARE human gates (`plan_review`, `test_review`), but they're **intra-team peer review**, not external CAB approvals. The human operator in the TUI is the peer reviewer. The deployment pipeline (automated test gates) catches quality issues. The optional `supervisor` (LLM-powered evaluation) provides additional analysis without being a blocking gate.

This is exactly what the book recommends: lightweight peer review combined with automated validation.

## 8. Shift Left on Security & Quality

**Accelerate's finding:** High-performing teams integrate security into the delivery process rather than bolting it on at the end.

The constitution (`ARCHITECTURE.md` + `check_constitution.sh`) is a "shift left" mechanism — quality rules are defined as code, enforced by scripts, and checked early. The pattern of making rules graduated from `[not yet enforced]` to `[enforced]` mirrors the book's recommendation of making quality practices part of daily work rather than a separate phase.

## 9. Work in Small Batches — The Core Design

**Accelerate's finding:** Decompose work into features deliverable in less than a week; use MVPs.

This is arguably the **foundational design decision** of drem-orchestrator. The planner agent decomposes tasks into 3-8 subtasks with explicit file lists. Each subtask is a small batch of work. The scheduler groups them into concurrent waves. This is small-batch thinking applied to software development at the agent level.

## 10. Making Flow Visible

**Accelerate's finding:** Teams should understand the full value stream and have visibility into flow.

The TUI board panel shows the full flow: `backlog -> planning -> plan_review -> in_progress -> testing_ready -> merging -> done`. The task tree with expandable subtasks makes the decomposition visible. The agent panel shows who's working on what. This is a kanban board for agent work.

## 11. Team Experimentation

**Accelerate's finding:** Teams must be able to try new ideas without external approval.

Agents can work autonomously within their worktrees. The supervisor is optional. The human gates are approval/rejection, not prescription — the planner proposes, the human disposes. This preserves agent autonomy while maintaining human oversight.

## 12. Deployment Pain — Designing It Away

**Accelerate's finding:** Where deployments are most painful, you find the worst outcomes. Causes: manual changes, configuration drift, multi-team handoffs.

Drem's merge orchestration specifically addresses this:
- Rebase-before-merge reduces conflict pain
- Automated merge with retry (up to 3 times with backoff)
- Reconciliation loop catches orphaned work and stuck agents
- The entire merge process is automated, not manual

## 13. Burnout Prevention — Sustainable Pace

**Accelerate's finding:** Technical and lean practices reduce burnout. Key factor: giving employees authority over decisions and tools.

Drem automates the toil (scheduling, merging, monitoring) while keeping the human in control of high-level decisions (approvals, task creation, feedback). The TUI provides rich feedback without requiring constant attention. This is sustainable orchestration.

## 14. Architecture Scaling — The Deploys-per-Developer Graph

**Accelerate's most striking finding:** High performers deploy at **increasing frequency** as they add developers, while low performers decrease.

Drem's `BuildSchedule()` with graph coloring enables this exact pattern: adding more agents doesn't slow things down because file-overlap analysis prevents conflicts. The loose coupling through worktree isolation means more agents = more parallel work = faster delivery, not more coordination overhead.

---

## Where Drem Could Go Further

A few areas where the book's findings suggest opportunities:

### Monitoring & Proactive Notification (Ch. 7)

The context monitoring is good, but the book emphasizes using monitoring data for *business decisions*. Adding metrics like "average time from backlog to done" or "merge conflict rate over time" would close this loop.

### Customer Feedback Integration (Ch. 8)

The `TaskComment` system enables human feedback, but the book emphasizes systematic customer feedback loops. For drem, this could mean tracking which agent-produced code gets revised by humans post-merge.

### Memory as Organizational Learning (Ch. 3, Westrum culture)

The memory system captures `lesson_learned` and `decision_log`, but the book's generative culture emphasizes *cross-team* learning. The `GetProjectMemories()` function exists but could be more prominent in prompt generation.

### Measuring the 4 DORA Metrics for Agent Work

The four DORA metrics map naturally to agent orchestration:

| DORA Metric | Drem Equivalent |
|-------------|-----------------|
| Deploy frequency | Merge frequency (merges per day) |
| Lead time for changes | Backlog-to-done duration |
| Change fail rate | Merge failure rate |
| Mean time to restore | Time from failure to fix-agent respawn |

These are all trackable from `TaskEvent` records already in the database.

---

## Summary

The alignment is striking. Drem isn't just inspired by these principles — it *operationalizes* them as a coordination system. The book says "loosely coupled architecture + trunk-based development + small batches + automated testing + visual management = high performance." Drem is infrastructure that makes those practices the default path for agent-based development.
