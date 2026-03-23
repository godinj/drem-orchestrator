# PRD: Classifier Agent

## Problem Statement

The orchestrator's current bug report classifier is a one-shot LLM call (`supervisor.EvaluateJSON`) that receives the bug report text and must guess whether a task is a quick fix or standard-complexity change. It has no access to the codebase — it cannot grep for relevant code, count affected files, or assess the scope of a change. This works for obvious cases (constraint violations, typo fixes) but fails when:

- A bug report is vague ("the output looks wrong") and complexity depends on how many files are actually involved
- A human-created task has a sparse description and the classifier cannot assess scope without exploration
- The boundary between quick fix and standard is ambiguous without seeing the code

Additionally, the system has two separate classification paths: automatic classification for promoted bug reports (one-shot LLM) and manual category selection for human-created tasks (checkbox in TUI). This split means human-created tasks bypass any intelligent classification, and the enrichment of sparse descriptions (`enrichQuickFixDescription`) is a separate, disconnected supervisor call.

## Solution

Replace the one-shot classifier with a **classifier agent** that runs as the first phase of every task's lifecycle. The classifier agent is a full Claude Code session running against the main worktree (read-only) that can explore the codebase, assess the scope of changes needed, and produce an evidence-based classification.

Key properties:
- **Unified entry point**: Both promoted bug reports and human-created tasks go through the same classifier agent. The TUI quick fix checkbox is removed.
- **New `CLASSIFYING` state**: All new tasks start in `CLASSIFYING` before entering `BACKLOG`. Classification is an explicit lifecycle phase.
- **Codebase exploration**: The agent can read files, grep for patterns, and assess how many files a change touches — producing a classification grounded in evidence, not just description parsing.
- **Complexity scoring**: Beyond the binary quickfix/standard category, the agent produces an explicit complexity estimate.
- **Clarification requests**: If the agent cannot determine scope even after exploring, it can request clarification from the user rather than guessing.
- **Read-only, no worktree overhead**: The classifier runs against the main worktree and is prompt-constrained to read-only operations. No worktree creation or teardown.
- **No concurrency limits**: Classifier agents do not count against `maxConcurrent` since they are read-only and do not compete for worktree resources.

## User Stories

1. As an orchestrator operator, I want all new tasks (both promoted bug reports and human-created) to be automatically classified by an agent that can explore the codebase, so that classification is based on evidence rather than description parsing.
2. As an orchestrator operator, I want a `CLASSIFYING` state that appears in the TUI, so that I can see which tasks are being analyzed and track their progress.
3. As an orchestrator operator, I want the classifier agent to produce a complexity score alongside the quickfix/standard category, so that I have a more nuanced view of task difficulty.
4. As an orchestrator operator, I want the classifier agent to enrich sparse task descriptions with target file hints and expanded context, so that downstream agents (planner, coder) receive high-quality input regardless of how the task was created.
5. As an orchestrator operator, I want the classifier agent to request clarification when it cannot determine scope, so that ambiguous tasks are resolved through human input rather than incorrect guesses.
6. As an orchestrator operator, I want clarification questions to appear in the TUI, so that I can answer them and unblock classification.
7. As an orchestrator operator, I want to be able to override the classifier's result before the task leaves `CLASSIFYING`, so that human judgment takes precedence when I disagree with the agent.
8. As an orchestrator operator, I want the classifier agent to run at medium effort with no time limit, so that it can thoroughly explore the codebase for complex tasks without being cut short.
9. As an orchestrator operator, I want classifier agents to not count against the `maxConcurrent` agent limit, so that classification never starves implementation or planning work.
10. As an orchestrator operator, I want the TUI task creation form to no longer have a quick fix checkbox, so that all tasks go through the unified classification path.
11. As an orchestrator operator, I want failed classifier agents to park the task for human triage rather than guessing a default, so that misclassification is avoided.
12. As an orchestrator operator, I want bug report ingestion to create tasks in `CLASSIFYING` rather than classifying inline during the tick loop, so that the tick loop is not blocked by classification.
13. As an orchestrator operator, I want the classifier agent to be read-only (enforced by prompt), so that it cannot accidentally modify the codebase while exploring.
14. As an orchestrator operator, I want the classifier's structured output (category, complexity score, enriched description, target files, rationale) stored on the task, so that downstream processing and human review have full context.
15. As an orchestrator operator, I want multiple classifier agents to be able to run concurrently without interference, so that a batch of new tasks does not create a bottleneck.

## Implementation Decisions

### New Agent Type and Task Status

- Add `AgentClassifier` to the `AgentType` enum alongside Planner, Coder, Reviewer, Fixer, Researcher.
- Add `StatusClassifying` to the `TaskStatus` enum, ordered before `StatusBacklog`.
- Add a `ComplexityScore` field (integer, 1-10 scale) to the Task model for the classifier's complexity estimate.
- The state machine allows: `CLASSIFYING` -> `BACKLOG` (on success or human override), and self-loops for clarification retries.

### Classifier Agent Execution Model

- The classifier agent is a full Claude Code session spawned via the agent runner.
- It runs against the **main worktree** (read-only) — no feature worktree is created at classification time.
- The agent runs at `--effort medium` with no timeout.
- The agent prompt instructs it to not modify any files (read-only enforcement by prompt, not tooling).
- Classifier agents are tracked separately from other agents and do **not** count against `maxConcurrent`.

### Classifier Agent Prompt

The prompt instructs the agent to:
1. Read the task title and description (or bug report context).
2. Explore the codebase: grep for relevant identifiers, read candidate files, assess how many files would need to change.
3. Produce structured JSON output with: `category` (quickfix or standard), `complexity_score` (1-10), `title` (refined), `description` (enriched with specifics from code exploration), `target_files` (concrete paths found during exploration), `rationale` (evidence-based explanation).
4. If the description is too ambiguous to classify even after exploration, output a `needs_clarification` status with specific questions.

### Unified Entry Point

- **Bug report promotion**: `classifyNewBugReports()` is simplified — it ingests reports, creates tasks in `CLASSIFYING` status, links the bug report. No inline LLM call.
- **Human task creation**: TUI creates tasks in `CLASSIFYING` status. The quick fix checkbox is removed.
- Both paths converge: all tasks in `CLASSIFYING` are picked up by `processClassifying()` in the tick loop.

### Result Handling (Three Outcomes)

1. **Classified successfully**: Agent returns structured JSON with category and complexity score. Task is updated with enriched fields and transitions to `BACKLOG`.
2. **Needs clarification**: Agent returns questions. Task stays in `CLASSIFYING`. Questions are surfaced in TUI. When the user answers, the classifier agent is re-spawned with answers as additional context.
3. **Agent failure** (crash, timeout, malformed output): Task stays in `CLASSIFYING` and is flagged for human triage in the TUI. No fallback to one-shot classification — if the agent can't figure it out, a human should look.

### Human Override

- From the TUI, the user can manually set a task's category and complexity score while it is in `CLASSIFYING`, and push it to `BACKLOG` without waiting for or agreeing with the agent's result.
- If the classifier has already completed, the user can review and override the result before advancing.

### Removed Code

- `classifyBugReport()` — replaced by the classifier agent.
- `enrichQuickFixDescription()` — subsumed by the classifier agent's description enrichment.
- `ClassificationResult` and `enrichmentResult` structs in `classifier.go` — replaced by a new result struct that includes complexity score and clarification fields.
- TUI quick fix checkbox — removed from task creation form.

### Tick Loop Integration

- `doTick()` gains a new phase: `processClassifyingTasks()`, which runs before `processBacklog`.
- This phase queries tasks in `CLASSIFYING` status without an assigned agent and spawns classifier agents for them.
- Classifier agent completions are drained alongside other agent completions in the existing completion-handling phase.

### Concurrency Model

- Classifier agents do not count against `maxConcurrent`. They are read-only and run against the shared main worktree.
- No concurrency limit on classifier agents. Multiple can run in parallel without interference since they only read.
- The agent runner needs a mechanism to distinguish classifier agents from other agents for concurrency accounting purposes.

## Testing Decisions

A good test for this feature verifies observable behavior through the module's public interface — state transitions, structured output parsing, and orchestrator routing — without mocking internal implementation details like prompt construction or agent internals.

### Modules to test

**`processClassifying()` orchestrator logic**: Test that tasks in `CLASSIFYING` get classifier agents spawned, that successful results transition to `BACKLOG` with correct fields populated, that clarification results keep the task in `CLASSIFYING`, and that failures park for human triage. Prior art: `quickfix_test.go` and the existing orchestrator state transition tests, which use a mock runner with `maxConcurrent=0` to verify transitions without spawning real agents.

**Classifier agent result handling**: Test the new `onClassifierCompleted` / `onClassifierFailed` handlers in `agent_results.go`. Verify that structured JSON output is correctly parsed and applied to the task, that malformed output is treated as failure, and that the complexity score is stored. Prior art: existing agent result handler tests.

**Bug report ingestion refactor**: Test that `classifyNewBugReports()` creates tasks in `CLASSIFYING` status (not `BACKLOG`), links the bug report, and does not make any supervisor/LLM calls. Prior art: `classifier_test.go` (which will be substantially rewritten).

## Out of Scope

- **Reclassification**: Once a task leaves `CLASSIFYING` and enters `BACKLOG`, it is not re-classified. If the planner later discovers the task is more complex than expected, that is handled by existing replanning mechanisms.
- **Classifier learning/feedback loop**: The classifier does not learn from past classification accuracy. Each classification is independent.
- **Category expansion beyond quickfix/standard**: The binary category remains. The complexity score provides the nuance; adding new categories (e.g., "medium") is a future consideration.
- **Write access for the classifier agent**: The classifier is strictly read-only. If future needs require the classifier to create scaffolding or prototype code, that would be a separate feature.
- **Remote/external codebase analysis**: The classifier only explores the local repository available in the main worktree.

## Further Notes

- The complexity score (1-10) is informational for the operator in this iteration. It is stored on the task and displayed in the TUI but does not drive any automated routing decisions beyond the quickfix/standard category. Future work could use it to adjust planning depth or agent effort levels.
- The clarification mechanism for the classifier is simpler than the existing plan clarification module (`internal/clarification/`). It does not need multi-round Q&A or assumption merging — just a list of questions from the agent and answers from the user, injected into the re-run prompt.
- Since the classifier runs against the main worktree, it sees the latest state of the default branch. If a task relates to code that only exists on a feature branch, the classifier may not find it. This is acceptable — such tasks are likely standard-complexity by definition.
