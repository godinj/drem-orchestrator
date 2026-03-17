# Accelerate Gaps: Where Drem Falls Short

Gaps between what *Accelerate* prescribes and how drem-orchestrator currently operates. Organized by severity and whether the gap is a genuine omission vs. a scope boundary.

---

## Genuine Gaps (things drem should do but doesn't)

### 1. No DORA Metrics — "You can't improve what you can't measure" (Ch. 2)

**The finding:** Accelerate's entire thesis rests on four metrics: deployment frequency, lead time for changes, change fail rate, and mean time to restore service. The book repeatedly emphasizes that measuring these is what separates high performers from the rest.

**The gap:** Drem has all the raw data but doesn't compute or surface any of these metrics. `TaskEvent` records every state transition with timestamps. The merge pipeline tracks successes and failures. Agent failures are logged with categories. But none of this is aggregated into trend lines or summary statistics.

**What exists today:**
- `TaskEvent` table: timestamps for every `backlog → planning → ... → done` transition
- Merge success/failure logged in `slog` output
- Agent failure categories from supervisor diagnosis (transient, prompt_issue, code_error, environment)
- Post-merge fix commits trackable via git history

**What's missing:**
- Lead time calculation (time from `backlog` to `done` per task)
- Change fail rate (failed merges / total merge attempts)
- Deploy frequency (successful merges per day/hour)
- MTTR (time from `failed` to next `done` for the same task)
- Any historical trending or visualization of these
- No TUI panel for metrics (the dashboard shows current state, not trends)

**Accelerate relevance:** HIGH — this is the foundation of the book's measurement framework. Without metrics, there's no way to know if the system is improving or degrading over time.

**Suggested approach:** Query `TaskEvent` records to compute DORA equivalents. Add a metrics TUI panel or periodic log summary. The data is already there — this is a reporting gap, not a data gap.

---

### 2. No Post-Merge Rollback — "Time to restore service" (Ch. 2)

**The finding:** Mean time to restore (MTTR) is one of the four key metrics. High performers restore service in under an hour. The ability to quickly roll back is a prerequisite.

**The gap:** `StatusDone` is a terminal state in the state machine — there are no outbound transitions. Once code merges to the default branch, drem has no mechanism to revert it.

**What exists today:**
- Pre-merge rollback: if build verification fails after merge to main, `git reset --hard HEAD~1` rolls back immediately (merge.go:369)
- Feature branch sync: remaining features are rebased after main merge
- Merge conflict detection prevents bad merges from landing

**What's missing:**
- No `done → failed` or `done → reverting` transition in the state machine
- No automated revert if a subsequent task's tests fail due to a prior merge
- No "revert task" command in the TUI
- No tracking of which commits belong to which task (for surgical reverts)
- No canary or progressive rollout concept

**Accelerate relevance:** HIGH — the book explicitly measures time to restore service as a key differentiator. High performers recover in under an hour; this requires rollback infrastructure.

**Suggested approach:** Add a `StatusReverted` state with `done → reverting → reverted` transitions. Track the merge commit SHA on the task record so `git revert <sha>` can be automated. Add a TUI keybinding for "revert task."

---

### 3. Change Fail Rate Is Invisible (Ch. 2, Ch. 4)

**The finding:** Change fail rate — the percentage of changes that result in degraded service or require remediation — is a key stability metric. The book found that high performers have change fail rates of 0-15%.

**The gap:** Every tier of the recent refactor required a post-merge fix commit. This is a 100% change fail rate for that work, but the system doesn't surface this. Failures are handled (retry, supervisor diagnosis, fixer agents) but never counted as a rate.

**What exists today:**
- Supervisor categorizes failures: transient, prompt_issue, code_error, environment, unknown
- Retry logic: planners retry up to 3 times, empty work retries up to 2 times
- Fixer agents spawned for test failures
- `needs_human_review` flag for unresolvable failures

**What's missing:**
- Ratio of tasks that reach `done` on first attempt vs. those requiring retries or fixes
- Ratio of merges that succeed vs. fail
- Tracking whether fixer agents succeed (and how often)
- Any trending of failure rates to detect regression

**Accelerate relevance:** HIGH — without this metric you can't tell if architectural or prompt improvements are actually reducing failure rates.

---

### 4. Approval Gates Have No Time Tracking (Ch. 7 — Lean Management)

**The finding:** Accelerate found that WIP limits expose obstacles to flow. One of the biggest obstacles is work sitting in approval queues. The book recommends making approval wait times visible.

**The gap:** `plan_review` and `test_review` are manual gates with no time tracking. A task could sit in `plan_review` for hours or days, and the system wouldn't notice or alert anyone.

**What exists today:**
- `TaskEvent` records the timestamp when a task enters `plan_review`
- `TaskEvent` records the timestamp when it transitions out (approved/rejected)
- The TUI shows tasks in the `plan_review` column

**What's missing:**
- No calculation of time-in-review
- No alerting if a task has been waiting for approval beyond a threshold
- No visibility into approval queue depth or wait times
- No SLA or escalation policy for stale reviews
- No tracking of approval throughput (approvals per hour/day)

**Accelerate relevance:** MEDIUM — the book's lean management chapter explicitly warns that approval queues are a major source of waste and deployment pain. The data to compute wait times already exists in `TaskEvent`.

---

### 5. Memory System Doesn't Close the Learning Loop (Ch. 3 — Westrum Culture)

**The finding:** Generative (high-performance) cultures actively seek information, train messengers, and share lessons across teams. Failures lead to inquiry, not punishment. Novel ideas are welcomed.

**The gap:** The memory system captures `lesson_learned` and `decision_log` entries via regex extraction from agent output, but there's no evidence these memories measurably improve future agent performance. The learning loop is open-ended — information goes in but its impact is never validated.

**What exists today:**
- `ExtractMemoriesFromOutput()` parses agent output for decisions, lessons, blockers, file changes
- `BuildAgentContext()` injects up to ~8000 tokens of prior memories into new agent prompts
- `CompactAgentMemory()` summarizes old memories to manage token budget
- `GetProjectMemories()` retrieves cross-agent lessons

**What's missing:**
- No measurement of whether agents with prior context perform better than those without
- No feedback signal from the human (e.g., "this memory was helpful" or "this was noise")
- No cross-project memory sharing (memories are scoped to a single project)
- Regex-based extraction misses nuanced lessons (e.g., "the approach in file X didn't work because Y")
- No curation mechanism — stale or wrong memories accumulate without pruning
- The post-merge fix pattern (Tier 3 agent made unauthorized changes) generated a lesson in the commit message, but it's unclear if that lesson reached subsequent agents' prompts

**Accelerate relevance:** MEDIUM — the book's Westrum culture model emphasizes organizational learning as a distinguishing capability. The infrastructure exists but the feedback loop isn't closed.

---

### 6. No Security in the Pipeline (Ch. 6 — Integrating Infosec)

**The finding:** High-performing teams "shift left" on security — integrating infosec into the delivery process rather than bolting it on at the end. They spend 50% less time remediating security issues. The book prescribes: security reviews for all major features, security testing as part of the automated suite, and easy-to-consume preapproved security libraries.

**The gap:** Drem has zero security integration. No secret scanning, no dependency vulnerability checks, no SAST/DAST, no security-focused review stage.

**What exists today:**
- Reviewer agents can review code, but have no security-specific prompting
- The constitution enforces structural quality rules, but none are security-related
- No `go vet` security analyzers (gosec, staticcheck security rules) in the test gate

**What's missing:**
- No secret scanning before agent code is committed (agents could accidentally hardcode credentials)
- No dependency vulnerability scanning (agents could introduce vulnerable dependencies)
- No security-focused constitution rules (e.g., "no raw SQL", "no eval", "no hardcoded secrets")
- No security review stage in the state machine
- No integration with `gosec`, `trivy`, or similar tools in the test gate

**Accelerate relevance:** HIGH — the book devotes an entire chapter (Ch. 6) to this. The key insight is that security should be easy and built-in, not a separate gate. Adding security checks to the existing `compile_command`/`test_command` gate would be low-friction.

**Suggested approach:** Add `gosec ./...` to the test gate. Add constitution rules for common security anti-patterns. Add a "security scan" check to `check_constitution.sh`. These are incremental additions to existing infrastructure.

---

### 7. Constitution Enforcement Is Not Yet Automated (Ch. 4 — CD Practices)

**The finding:** Accelerate emphasizes that the deployment pipeline should automatically reject bad changes. Manual compliance checking is a form of the "risk management theater" the book warns against.

**The gap:** The constitution (`ARCHITECTURE.md`) has 10 rules. The enforcement script (`check_constitution.sh`) checks 9 of them. But the script is not integrated into the pipeline — it's not run as part of the test gate, and none of the rules are marked `[enforced]` in the document (all are `[not yet enforced]`).

**Current state:** 3 of 9 checks pass. The script exists but isn't a gate.

**What's missing:**
- `check_constitution.sh` is not called by `compile_command` or `test_command`
- No pre-commit hook runs the constitution checks
- The graduation path ("when a rule can be reliably detected by a script, mark it [enforced]") hasn't been exercised — no rules have graduated
- Agents can violate the constitution and their work will still merge

**Accelerate relevance:** HIGH — the book is explicit that automated pipelines that detect and reject bad changes are a key capability. Having the script but not running it is the "we have a process but don't follow it" anti-pattern.

**Suggested approach:** Add `bash scripts/check_constitution.sh` to the `compile_command` or as a pre-merge check. Graduate passing rules to `[enforced]`. Fix the 6 failing checks iteratively.

---

## Scope Boundaries (capabilities that belong outside drem)

These are Accelerate capabilities that drem intentionally doesn't implement because they belong to the surrounding infrastructure:

### Deployment to Production (Ch. 4)

Drem merges to the default branch — what happens after that (CI/CD, container builds, Kubernetes deploys) is outside its scope. Drem is a code production tool, not a deployment tool. The book's "deployment automation" capability applies to the CI/CD layer that consumes drem's output.

### Infrastructure Monitoring (Ch. 7)

The book's "feedback from production" capability refers to application performance monitoring (APM), error tracking, and infrastructure metrics. Drem monitors its own agents (heartbeats, context windows) but doesn't monitor the software it produces. This is appropriate — runtime monitoring belongs to the deployed application, not the orchestrator that wrote it.

### Customer Feedback Collection (Ch. 8)

The book emphasizes gathering customer feedback and incorporating it into product design. Drem provides the `TaskComment` mechanism for human input, but systematic customer feedback collection (surveys, analytics, A/B testing) is a product management concern, not an orchestrator concern.

---

## Summary

| Gap | Severity | Data Available? | Effort |
|-----|----------|----------------|--------|
| No DORA metrics | HIGH | Yes (TaskEvent) | Low — query/reporting only |
| No post-merge rollback | HIGH | Partial (merge SHAs) | Medium — state machine + git revert |
| Change fail rate invisible | HIGH | Yes (logs, events) | Low — aggregate existing data |
| No security in pipeline | HIGH | N/A | Low — add gosec to test gate |
| Constitution not enforced | HIGH | Yes (script exists) | Low — wire script into test gate |
| Approval time not tracked | MEDIUM | Yes (TaskEvent) | Low — compute from existing timestamps |
| Memory loop not closed | MEDIUM | Partial | Medium — needs effectiveness metrics |

The pattern: most gaps are **reporting and enforcement gaps, not infrastructure gaps**. The data and mechanisms exist — they're just not wired together. This is consistent with Accelerate's finding that the biggest improvements come from making existing capabilities visible and measurable.
