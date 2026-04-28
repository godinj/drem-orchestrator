## Problem Statement

Drem Orchestrator already coordinates multi-agent software work: it classifies tasks, plans decomposition, routes work to planner/coder/reviewer/fixer/prep/merger roles, runs human gates, tracks task events, validates architecture constraints, and supports C-Suite personas for operations and product/technical oversight. However, its higher-level operating knowledge is spread across project guidance, architecture rules, PRDs, plan files, persona prompts, runtime state, task comments, event streams, generated JSON artifacts, and C-Suite inbox/outbox messages.

That spread creates avoidable operational ambiguity:

- Agents do not have one canonical hierarchy for resolving conflicts between operator directives, architecture rules, plans, persona prompts, runtime observations, and historical notes.
- Persona and agent responsibilities exist, but their contracts are partly embedded in prompts and historical docs rather than expressed as durable, versioned operating artifacts.
- Task artifacts exist, but their lifecycle is not consistently classified as source of truth, derived evidence, transient scratch, review output, handoff, decision, or historical context.
- C-Suite and worker handoffs rely on prose conventions that are hard to validate mechanically.
- Durable decisions and contradictions can be lost inside long plans, comments, logs, or memory summaries.
- Validation currently focuses on code constraints, tests, and task state transitions, but not on whether meta-plumbing artifacts remain coherent, current, and agent-readable.

OB1/Open Brain provides useful patterns for this problem: durable memory substrate, canonical skill/persona contracts, recipe-based workflows, explicit handoffs, confidence labels, contradiction preservation, durable-output-only capture, and agent-readable operating files. Drem should adopt those patterns as an internal operating-system layer around its existing artifacts and personas, without making OB1 itself a required dependency unless shared-memory MCP persistence is explicitly needed later.

## Solution

Create an OB1-inspired operating layer for Drem Orchestrator that makes the repo's meta-plumbing explicit, durable, and enforceable. This is not a rewrite of the orchestrator loop, task lifecycle, agent spawning, C-Suite runtime, or merge pipeline. It is a structured layer around the existing system concepts.

The solution has six parts:

1. Agent-readable operating files. Add a small set of durable operating documents that define how agents should resolve authority, classify artifacts, perform workflows, write handoffs, preserve contradictions, and label confidence. These files should be written for agents first and humans second.

2. Canonical persona and agent contracts. Define versioned contracts for the live C-Suite personas and orchestrator agent roles. Each contract states ownership, allowed decisions, forbidden decisions, required inputs, required outputs, escalation triggers, and expected handoff format.

3. Decision and source-of-truth hierarchy. Establish a repo-specific authority order so agents know which artifact wins when instructions conflict.

Highest authority: current operator instruction in the active conversation or task.

Next: standing project guidance and safety constraints.

Next: architecture constitution and project constraint definitions.

Next: active PRD or approved implementation plan for the current work.

Next: current task state, task comments, review gates, and orchestrator event records.

Next: persona or agent contract for the role doing the work.

Next: workflow recipes for the activity being performed.

Next: durable decisions and contradiction records.

Next: historical PRDs, historical plans, generated reports, logs, and archived C-Suite messages.

Lowest authority: transient scratch, stale generated artifacts, raw logs without corroborating task state, and copied historical text that conflicts with current operating files.

4. Artifact taxonomy. Classify Drem artifacts into durable operating categories.

Source-of-truth artifacts: standing guidance, architecture constitution, project constraints, approved PRDs, approved plans, task state, event records, registered project metadata, persona contracts, workflow recipes.

Decision artifacts: explicit decisions, supersession records, rejected alternatives, owner, date, scope, confidence, and links to affected tasks or plans.

Workflow artifacts: recipes for PRD creation, task triage, plan review, test review, merge recovery, C-Suite delegation, bug-report ingestion, incident response, and repo audits.

Handoff artifacts: structured baton passes between operator, Kyle, Mike, Alex, Seth, worker agents, reviewers, fixers, merger, and watchdog/recovery flows.

Evidence artifacts: test results, build results, task events, agent lifecycle events, container lifecycle events, review results, classification outputs, task-prep outputs, audit reports.

Memory artifacts: durable lessons, project-wide decisions, agent summaries, and compacted context intended to survive across sessions.

Contradiction artifacts: known conflicts between docs, prompts, plans, runtime behavior, and task state, preserved until resolved rather than silently normalized.

Transient artifacts: raw logs, scratch files, temporary reports, stale local generated outputs, and intermediate worker output that should not become durable memory unless promoted.

5. Recipe-based workflows. Define compact recipes for recurring Drem operations. Each recipe should state trigger, actor, prerequisites, steps, required artifacts, validation, confidence labels, handoff target, and exit criteria.

Initial recipes should cover PRD to plan, plan to implementation, human gate review, C-Suite delegation, issue/bug triage, merge conflict recovery, stalled worker recovery, repo audit, contradiction resolution, and durable-memory promotion.

6. Validation and enforcement. Extend the existing quality posture so operating artifacts are validated alongside code. Enforcement should start with lightweight checks and become stricter only after the operating layer stabilizes.

Required validations should include authority hierarchy presence, persona contract completeness, workflow recipe schema compliance, artifact taxonomy coverage, no orphaned active decisions, no unresolved contradiction without owner/status, handoff format compliance, and stale-doc detection when a newer canonical operating file supersedes older guidance.

OB1 integration should remain optional. The implementation should be OB1-inspired by default, using normal repo files, existing database-backed memory, task events, C-Suite inbox/outbox conventions, and existing validation infrastructure. A direct OB1 dependency should only be introduced if Drem needs a shared-memory MCP server or external durable memory substrate that cannot be cleanly served by the existing database, event stream, and repo-tracked operating files.

## User Stories

1. As the operator, I want Drem agents to know which instructions win when docs, plans, prompts, and runtime state conflict, so that I do not need to restate the same priority rules in every task.
2. As the operator, I want durable decisions separated from transient logs and generated artifacts, so that future agents can recover the reason for a choice without reading thousands of lines of historical plan text.
3. As Kyle, I want a canonical source-of-truth hierarchy, so that status briefings can distinguish current facts from stale historical guidance.
4. As Mike, I want an incident and recovery recipe, so that worker/container failures produce consistent evidence, escalation, and handoff artifacts.
5. As Alex, I want PRD and prioritization recipes, so that product decisions preserve rejected alternatives, confidence, and open contradictions.
6. As Seth, I want architecture and quality recipes tied to the existing constitution and constraints system, so that audits can check both code health and operating-artifact health.
7. As a planner agent, I want a canonical artifact taxonomy, so that I know which outputs must be preserved, which can be ignored, and which need promotion into durable memory.
8. As a coder agent, I want a role contract that states my required inputs and outputs, so that I do not invent handoff formats or mutate artifacts outside my scope.
9. As a reviewer agent, I want confidence labels and evidence requirements, so that approval or rejection is grounded in observed behavior rather than vague prose.
10. As a fixer agent, I want contradiction preservation rules, so that conflicting requirements are escalated or recorded instead of silently resolved in code.
11. As a merger/recovery flow, I want explicit handoff records, so that merge failures, retries, and supersession decisions remain auditable.
12. As a C-Suite persona, I want my persona contract to identify decision boundaries and escalation triggers, so that I can act autonomously where safe and defer where required.
13. As a future agent reading the repo, I want agent-readable operating files, so that I can bootstrap correct behavior without reconstructing norms from old conversations.
14. As the operator, I want stale or conflicting persona prompts flagged, so that historical prompt drift does not undermine current operating behavior.
15. As the orchestrator, I want workflow recipes to define required artifacts at each gate, so that validation can detect incomplete or malformed handoffs.
16. As a maintainer, I want OB1 to be inspiration rather than a mandatory runtime dependency, so that Drem stays self-contained unless shared-memory MCP persistence becomes necessary.

## Implementation Decisions

- Treat OB1 as a pattern library for operating structure, not as a required service dependency.
- Keep the first implementation file-based and repo-native, using existing Drem concepts: task state, task events, memory records, project guidance, architecture constitution, constraints, C-Suite messages, PRDs, plans, review artifacts, and generated classification/prep outputs.
- Add a small operating layer rather than changing the core task lifecycle. The current lifecycle from classification through planning, clarification, gates, implementation, review, merge, and completion should remain intact.
- Define canonical contracts for live personas: Kyle, Mike, Alex, and Seth. Historical or retired personas should be represented only as superseded context when relevant.
- Define canonical contracts for orchestrator-managed agent roles: classifier, prep, planner, coder, reviewer, fixer, researcher if still used, merger, supervisor, and direct tool agents.
- Each contract should include role purpose, ownership, allowed decisions, forbidden decisions, required input artifacts, required output artifacts, confidence expectations, escalation triggers, and handoff rules.
- Introduce a decision record format that captures decision, owner, date, scope, confidence, evidence, alternatives rejected, affected artifacts, supersedes, superseded by, and open contradictions.
- Introduce a contradiction record format that captures the conflicting sources, observed conflict, current handling rule, owner, status, and resolution criteria.
- Introduce handoff records for cross-agent and cross-persona transitions. Handoffs should identify sender, receiver, task or incident, current state, completed work, evidence, blockers, recommended next action, confidence, and expiry/staleness conditions.
- Add workflow recipes for recurring operations instead of embedding process only in prompts. Initial recipes should cover PRD writing, PRD-to-plan, task classification, plan review, clarification, test review, implementation review, merge recovery, stalled worker recovery, C-Suite delegation, bug-report triage, repo audit, and contradiction resolution.
- Add a durable-memory promotion rule: only decisions, lessons, unresolved contradictions, stable workflow changes, role-contract changes, and validated source-of-truth updates should become durable memory. Raw logs, speculative analysis, failed intermediate attempts, and temporary generated files should remain evidence or scratch unless explicitly promoted.
- Preserve contradictions instead of forcing immediate synthesis. If two active sources disagree and the correct winner is not clear from the hierarchy, agents must record the contradiction and escalate rather than overwrite or silently ignore one side.
- Add confidence labels for claims in decision, review, handoff, and incident artifacts. Suggested labels are high, medium, low, and unknown. Confidence should be tied to evidence quality.
- Keep existing project constraints and architecture constitution as primary quality sources. OB1-style validations should complement, not replace, current code constraints.
- Extend existing validation concepts to operating artifacts. This can start as a command-line validator and later become part of repo-wide constitution checks and Drem gate validation.

Phased implementation guidance:

1. Create the operating taxonomy, source-of-truth hierarchy, contract templates, decision template, contradiction template, handoff template, and first workflow recipes.
2. Update live persona and agent contracts to reference the hierarchy and artifact taxonomy. Mark older conflicting guidance as superseded where appropriate.
3. Add lightweight validation for required operating files, schema-like required fields, known persona names, known agent roles, valid confidence labels, and unresolved contradiction ownership.
4. Integrate operating-artifact validation into existing quality checks and C-Suite audit workflows.
5. Teach relevant prompts and recipes to emit structured decisions, handoffs, confidence labels, and contradiction records at task gates.
6. Evaluate whether existing database memory and repo files are enough. Only consider optional OB1/MCP shared-memory integration if cross-agent persistence needs exceed current repo/database/event capabilities.

## Testing Decisions

- Good tests should validate external behavior: given a set of operating artifacts, does the validator accept valid artifacts and reject ambiguous, stale, incomplete, or contradictory ones?
- Add tests for the operating-artifact validator using fixture files that represent persona contracts, agent contracts, decisions, contradictions, handoffs, and recipes.
- Test source-of-truth hierarchy parsing by asserting that conflicts produce deterministic outcomes when the hierarchy clearly resolves them and unresolved contradiction records when it does not.
- Test persona contract validation for required fields, known persona names, ownership boundaries, escalation triggers, and output artifact requirements.
- Test agent contract validation for known orchestrator roles, required inputs, required outputs, and forbidden actions.
- Test decision records for required owner, scope, confidence, evidence, supersession metadata, and contradiction linkage.
- Test contradiction records for both unresolved and resolved states, ensuring unresolved contradictions require an owner and resolution criteria.
- Test workflow recipes for required trigger, actor, prerequisites, validation, handoff, and exit criteria.
- Test durable-memory promotion rules with examples of logs, review outputs, decisions, lessons, and contradictions to ensure only durable outputs are promoted.
- Add integration coverage where existing repo-wide quality checks can run the operating validator as one more check without needing live Claude, OpenCode, Docker, SGLang, or C-Suite services.
- Avoid tests that depend on exact historical file paths or long generated plan contents. Tests should use small representative fixtures and existing Drem concepts.

## Out of Scope

- Rewriting the orchestrator task lifecycle.
- Replacing the existing SQLite-backed memory, task event, or C-Suite message systems.
- Replacing current PRDs, plans, architecture constitution, or project guidance wholesale.
- Making OB1 a required runtime dependency.
- Introducing shared-memory MCP persistence in the first implementation.
- Changing Claude/OpenCode authentication behavior.
- Changing container orchestration, worker spawning, merger behavior, or SGLang deployment.
- Building a new UI for operating artifacts.
- Automatically resolving contradictions without operator or owner review.
- Capturing raw logs or full conversations as durable memory by default.

## Further Notes

The repo already has many OB1-adjacent pieces: memory persistence and compaction, task events, structured agent roles, C-Suite personas, disk-based persona messaging, architecture constraints, PRDs, implementation plans, review artifacts, classifier outputs, task-prep artifacts, and gate commands. The missing piece is a canonical operating structure that tells agents how those pieces relate.

This PRD should bias toward small, durable, agent-readable files and validation over new runtime machinery. The first successful version is one where a new Drem agent can answer: what am I allowed to decide, what artifacts matter, what source wins in a conflict, what recipe am I following, what must I hand off, what confidence do I have, and what contradictions must remain visible.
