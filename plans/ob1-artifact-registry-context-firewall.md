## Problem Statement

The OB1/Open Brain operating-system plan defines the durable operating layer Drem needs: agent-readable files, persona and agent contracts, artifact taxonomy, workflow recipes, handoffs, decisions, contradiction records, confidence labels, and operating-artifact validation. That plan intentionally keeps repo files as durable, reviewable operating artifacts and avoids making OB1 a runtime dependency.

The remaining risk is that Drem's persona agents and worker agents still need a machine-accessible way to know which artifacts are current, accepted, admissible, stale, superseded, legacy, relevant, or forbidden for a specific turn. Repo files alone cannot reliably answer that question. Old plans, generated reports, archived C-Suite messages, historical prompts, task comments, memory summaries, raw logs, and stale PRDs can remain useful as history, but they can also pollute persona context if they are retrieved or injected without status and relevance metadata.

This creates three concrete failure modes:

- Agents can treat stale or legacy artifacts as current protocol because the artifact body is readable and plausible.
- Persona agents can receive polluted context from artifacts that are valid somewhere in the system but irrelevant to the current persona, task, workflow, project, or directive.
- Larger operator directives, strategic goals, architecture goals, incident directives, and operating priorities can be buried in prose instead of participating directly in artifact selection and context assembly.

Drem needs an authoritative artifact metadata layer that controls artifact status, admissibility, currentness, supersession, evidence trust, persona visibility, relevance, and goal alignment. This metadata must be accessible to persona agents and orchestrator flows before they rely on artifacts. The content bodies can remain in repo files, task records, event streams, generated JSON, and existing storage, but metadata about whether those bodies may be trusted or admitted into context needs to be authoritative and queryable.

## Solution

Add an SQLite-backed artifact authority registry and context firewall for Drem Orchestrator. The registry is the authoritative source for artifact metadata. Existing artifact bodies remain in their native storage locations.

The core rule is:

The artifact registry decides whether an artifact is current, accepted, admissible, superseded, legacy, scratch, relevant, visible, or usable as evidence. The artifact body remains the authoritative source for the artifact's content.

This keeps Markdown plans, PRDs, contracts, recipes, decision records, contradiction records, handoffs, task events, C-Suite messages, generated reports, test results, and logs durable and inspectable while giving agents a deterministic trust layer.

The solution has five parts:

1. Artifact authority registry. Add SQLite tables that register operating artifacts, their storage locations, lifecycle status, authority class, admissibility, evidence trust, owner, scope, visibility, currentness, validation state, and supersession relationships.

2. Context firewall. Add a context admission service that receives a persona or agent role, task, workflow stage, project, active goals, and candidate artifacts, then returns a minimal accepted context packet plus explicit exclusion reasons. Persona agents and worker agents should not assemble broad context directly from old files, memory summaries, generated reports, or historical messages.

3. Goal and directive metadata. Represent larger goals and directives as first-class registry entities. Artifacts should declare which active goals, directives, operating principles, product goals, architecture goals, task goals, or incident directives they support, implement, constrain, contradict, or provide evidence for.

4. Registry-aware validation. Extend operating-artifact validation so stale, superseded, unregistered, dangling, ownerless, low-trust, or incorrectly scoped artifacts are detected. Validation should also check that active artifacts have suitable directive links where the artifact type requires them.

5. Gradual enforcement. Start in report-only mode, then enforce registry admission on high-risk persona and worker context paths once coverage is adequate. Enforcement should prefer excluding irrelevant artifacts over injecting everything with caveats.

The registry should act as a context firewall, not as a broad retrieval engine. Its job is to prevent agent context pollution by admitting the smallest authoritative context set needed for the current turn.

## User Stories

1. As the operator, I want SQLite metadata to determine whether an artifact is current, accepted, stale, superseded, legacy, rejected, scratch, or admissible, so that agents do not accidentally trust old repo files.

2. As the operator, I want repo files and existing storage systems to remain authoritative for artifact bodies, so that Drem keeps durable, reviewable, human-readable operating artifacts.

3. As the operator, I want the registry to be authoritative for artifact status and trust, so that historical files can remain in the repo without confusing persona agents.

4. As the operator, I want persona agents to receive the smallest relevant context set for the current turn, so that unrelated active artifacts do not pollute reasoning.

5. As the operator, I want larger goals and directives tracked as first-class metadata, so that artifact relevance is determined by current strategic intent rather than buried prose.

6. As Kyle, I want executive status briefings to cite only current and admissible artifacts, so that current facts are not mixed with stale historical guidance.

7. As Kyle, I want superseded artifacts to point to their replacement artifacts, so that I can route attention to the current source instead of re-litigating old context.

8. As Mike, I want incident context filtered by task, incident, evidence trust, and currentness, so that recovery work does not inherit obsolete or unrelated failure reports.

9. As Mike, I want raw logs and generated reports labeled by evidence trust and scope, so that weak evidence does not get promoted into operational truth.

10. As Alex, I want product goals, prioritization directives, rejected alternatives, and accepted plans represented in the registry, so that planning work can be aligned with current priorities.

11. As Alex, I want artifacts that conflict with active product goals to be visible as conflicts rather than silently ignored, so that prioritization remains explicit.

12. As Seth, I want architecture goals, constitution constraints, repo-wide quality directives, and audit evidence represented in the registry, so that audits distinguish authoritative constraints from stale commentary.

13. As Seth, I want context admission to exclude artifacts outside the current audit scope unless explicitly admitted as historical background, so that review output is not contaminated by unrelated plans.

14. As a planner agent, I want only active PRDs, accepted directives, relevant decisions, and current workflow recipes injected, so that the plan does not chase obsolete requirements.

15. As a coder agent, I want a minimal context packet containing required inputs, current constraints, relevant decisions, and task-specific evidence, so that I do not read or apply unrelated persona history.

16. As a reviewer agent, I want evidence trust metadata attached to test results, build results, logs, review outputs, and task events, so that approval decisions are grounded in admissible evidence.

17. As a fixer agent, I want stale and superseded artifacts excluded by default, so that bug fixes do not accidentally restore rejected behavior.

18. As a merger or recovery flow, I want supersession and handoff metadata to be queryable, so that retry and recovery decisions remain auditable.

19. As the orchestrator, I want context admission decisions recorded, so that later audits can explain which artifacts were included, excluded, or escalated for a persona turn.

20. As a maintainer, I want unregistered operating artifacts and dangling content pointers flagged, so that the registry does not drift away from the repo.

21. As a future agent, I want the registry to tell me whether an artifact is active protocol, admissible evidence, historical background, or scratch, so that I do not infer trust from prose alone.

22. As a future agent, I want artifacts linked to the directives they support or contradict, so that I can reason from current goals instead of reconstructing intent from long documents.

23. As a C-Suite persona, I want my context assembled through persona visibility rules, so that I do not receive confidential, irrelevant, or role-inappropriate artifacts.

24. As the operator, I want report-only rollout before hard enforcement, so that we can classify legacy artifacts without breaking active orchestration.

## Implementation Decisions

- Add an SQLite-backed artifact authority registry as the authoritative source for artifact metadata.

- Keep artifact content bodies in their native locations: repo files, task state, task events, generated JSON artifacts, C-Suite messages, review outputs, test results, logs, and existing memory storage.

- Treat the registry as authoritative for status, admissibility, currentness, supersession, evidence trust, persona visibility, context relevance, and goal alignment.

- Treat the artifact body as authoritative for the content of the artifact once the registry admits it.

- Do not make active artifact status equivalent to automatic context injection. Active artifacts can still be irrelevant to a persona turn.

- Make context admission persona-aware, task-aware, workflow-aware, project-aware, directive-aware, and evidence-aware.

- Implement the context firewall as a deep module with a small interface: given turn context and candidate artifacts, return admitted artifacts, excluded artifacts, escalation reasons, and trust annotations.

- Prefer deterministic filtering over broad retrieval. The context firewall should admit a small context packet rather than dump all plausible artifacts into the prompt.

- Record context admission decisions for auditability. A later reviewer should be able to see why a stale PRD, old C-Suite message, raw log, or generated report was excluded or admitted as historical context.

- Add directive and goal records as first-class registry entities. Operator directives, strategic goals, product goals, architecture goals, operating principles, task goals, and incident directives should be queryable.

- Link artifacts to directives with explicit alignment types: supports, implements, constrains, conflicts with, supersedes because of, provides evidence for, explains, or historical background for.

- Use status values that distinguish artifact lifecycle from admissibility. Suggested artifact statuses are candidate, draft, accepted, active, superseded, legacy, archived, rejected, scratch, stale, and unknown.

- Use admission outcomes that distinguish trust decisions. Suggested outcomes are admit, admit minimal, admit as historical context, admit as weak evidence, exclude stale, exclude superseded, exclude visibility, exclude irrelevant, exclude inadmissible, and escalate conflict.

- Use evidence trust labels that remain simple at first: high, medium, low, unknown, and invalidated.

- Include a content digest for file-backed artifacts so the registry can detect when metadata may be stale relative to the artifact body.

- Include owner and validation fields for active artifacts, unresolved contradictions, active directives, accepted decisions, and authoritative protocols.

- Register legacy artifacts without forcing immediate cleanup. Unknown artifacts should be distrusted by default until classified.

- Preserve superseded and legacy artifacts for audit and historical context. Do not delete them as part of this work.

- Add migration and indexing support for common query dimensions: artifact id, content URI, artifact type, status, project, task, persona, agent role, workflow stage, directive, validation status, and supersession target.

- The first implementation should focus on metadata registration, context admission, validation, and report-only integration. It should not rewrite the orchestration loop.

- Hard enforcement should begin only after registry coverage is good enough for the target path. Candidate enforcement paths are C-Suite persona context assembly, worker task prompts, review prompts, and recovery prompts.

Suggested registry entities:

- Artifact: a registered operating artifact or evidence artifact.
- ArtifactLink: a typed relationship between artifacts, such as supersedes, contradicts, depends on, cites, derived from, or implements.
- Directive: a larger goal, operator directive, strategic goal, architecture goal, product goal, incident directive, or operating principle.
- ArtifactDirectiveLink: a typed relationship between an artifact and a directive.
- ContextAdmissionDecision: a recorded decision to include, exclude, downgrade, or escalate an artifact for a specific persona or agent turn.
- ContextPacket: the assembled, minimal context set delivered to a persona or agent.

Suggested artifact metadata fields:

- Artifact id.
- Artifact type.
- Content URI.
- Content hash.
- Title.
- Owner.
- Project id.
- Task id.
- Persona scope.
- Agent role scope.
- Workflow scope.
- Topic tags.
- Status.
- Authority class.
- Admissibility.
- Evidence trust.
- Confidence.
- Visibility scope.
- Valid from.
- Valid until.
- Last seen at.
- Last validated at.
- Validation status.
- Validation errors.
- Canonical artifact id.
- Supersedes artifact id.
- Superseded by artifact id.
- Staleness reason.
- Legacy reason.

Suggested directive metadata fields:

- Directive id.
- Directive type.
- Title.
- Description.
- Owner.
- Status.
- Priority.
- Authority rank.
- Scope type.
- Scope key.
- Accepted by.
- Accepted at.
- Valid from.
- Valid until.
- Success criteria.
- Non-goals.
- Superseded by directive id.
- Source artifact id.

Suggested context firewall input:

- Persona or agent role.
- Task id.
- Project id.
- Workflow stage.
- Active incident id when applicable.
- Current operator request summary when available.
- Active directive ids.
- Candidate artifact ids or candidate artifact query.
- Required artifact types.
- Evidence trust threshold.

Suggested context firewall output:

- Admitted artifacts.
- Required artifacts.
- Optional artifacts.
- Artifacts admitted only as historical context.
- Artifacts admitted only as weak evidence.
- Excluded artifacts and exclusion reasons.
- Supersession replacements.
- Unresolved conflicts requiring escalation.
- Active directives used for filtering.
- Confidence and evidence trust annotations.

Phased implementation guidance:

1. Define registry vocabulary and semantics. Document statuses, authority classes, admissibility outcomes, evidence trust labels, directive types, artifact types, visibility scopes, and context admission rules.

2. Add SQLite schema and migrations. Create the artifact registry, artifact links, directives, artifact-directive links, context admission decisions, and context packet records.

3. Add registration APIs. Support registering repo-backed artifacts, generated artifacts, task events, review outputs, test results, C-Suite messages, and memory summaries without moving their content bodies into SQLite.

4. Add a registry validator. Validate required fields, valid enum values, dangling content URIs, stale content hashes, invalid supersession chains, missing owners, missing directive links, unresolved contradictions, and visibility conflicts.

5. Seed canonical artifacts. Register the OB1 operating-system plan, this PRD, current project guidance, active architecture constraints, live persona contracts when available, and active workflow recipes when available. Mark obvious historical artifacts as legacy or superseded where replacement is known.

6. Build the context firewall. Implement admission logic for currentness, supersession, status, visibility, evidence trust, persona relevance, workflow relevance, task relevance, project relevance, and active directive relevance.

7. Add report-only integration. Produce context admission reports for representative C-Suite persona turns and worker task prompts without changing delivered context yet.

8. Integrate with persona context assembly. Route Kyle, Mike, Alex, and Seth context through the firewall, initially in report-only mode, then enforced mode once the reports are stable.

9. Integrate with worker and reviewer context assembly. Route planner, coder, reviewer, fixer, merger, and recovery prompts through the firewall where candidate artifacts are known.

10. Add audit surfaces. Provide CLI or validation output that explains active artifacts, superseded artifacts, unknown artifacts, admitted artifacts, excluded artifacts, and directive alignment gaps.

11. Tighten enforcement. Move from warnings to hard failures for unregistered active protocol, stale canonical artifacts, missing supersession targets, forbidden persona visibility, and inadmissible evidence in high-risk gates.

## Testing Decisions

- Good tests should validate externally observable behavior: given registered artifacts, active directives, and a persona turn, does Drem admit the right context and exclude polluted context with clear reasons?

- Test registry validation with representative fixtures for active, draft, accepted, superseded, legacy, archived, rejected, scratch, stale, and unknown artifacts.

- Test that repo-backed artifact bodies remain outside SQLite while registry metadata can still validate content URI and content digest.

- Test that unknown or unregistered artifacts are distrusted by default when considered for authoritative context.

- Test supersession chains by asserting that a superseded artifact resolves to the correct active replacement.

- Test that supersession cycles are rejected.

- Test that archived, legacy, scratch, stale, rejected, and superseded artifacts are excluded from normal context unless explicitly admitted as historical context or weak evidence.

- Test persona visibility rules by asserting that Kyle, Mike, Alex, Seth, worker agents, reviewer agents, and merger flows receive only artifacts allowed for their roles.

- Test context pollution prevention by creating multiple valid active artifacts where only a subset is relevant to the current persona, task, workflow, project, and directive.

- Test goal and directive filtering by asserting that artifacts linked to active directives are preferred over otherwise similar artifacts linked only to inactive, superseded, or unrelated directives.

- Test conflict handling by asserting that artifacts conflicting with active directives are excluded or escalated rather than silently injected.

- Test evidence trust thresholds by asserting that raw logs, generated reports, test results, review outputs, and task events are admitted, downgraded, or excluded according to trust metadata.

- Test context admission audit records by asserting that included and excluded artifacts are recorded with reasons.

- Test validator behavior for dangling content URIs, stale content digests, invalid enum values, missing owners, missing validation status, invalid visibility scopes, and missing directive links.

- Test report-only integration by verifying that existing prompt assembly can generate admission reports without changing the delivered context.

- Test enforced integration with one representative persona flow and one representative worker or reviewer flow after report-only behavior is stable.

- Avoid tests that depend on exact historical file contents. Use small fixtures that represent PRDs, plans, persona contracts, workflow recipes, generated reports, task events, C-Suite messages, and logs.

## Out of Scope

- Replacing repo files with SQLite-stored artifact bodies.

- Rewriting the orchestrator task lifecycle.

- Replacing existing task event storage, C-Suite message storage, memory storage, or generated artifact storage.

- Making OB1 or an MCP shared-memory server a required runtime dependency.

- Building a new UI for registry browsing.

- Automatically rewriting or deleting stale repo documents.

- Automatically resolving contradictions between active artifacts.

- Treating vector similarity retrieval as sufficient for context admission.

- Capturing full conversations or raw logs as durable memory by default.

- Hard-enforcing registry admission across every path before report-only coverage is understood.

- Reclassifying every historical artifact in the repo before delivering the first useful slice.

## Further Notes

This PRD refines the OB1/Open Brain operating-system plan by making the registry an explicit authority boundary. The original plan identifies the need for operating artifacts, taxonomy, contracts, recipes, decisions, contradictions, confidence labels, and validation. This follow-on PRD clarifies that agents also need a queryable artifact metadata authority to decide whether those artifacts are accepted, current, admissible, relevant, visible, and aligned with active goals.

The registry should not become an opaque documentation store. Its value is that it makes artifact trust and context relevance mechanically inspectable. If the SQLite database is lost, Drem should be able to rebuild much of the registry from repo-backed operating files and existing runtime records, though manually classified status and directive alignment may require backup, migration, or reseeding.

The first successful version is one where a persona turn can answer these questions before receiving context:

- Which artifacts are authoritative for this turn?
- Which artifacts are relevant to this persona, task, workflow, project, and directive?
- Which artifacts are current and which are superseded?
- Which artifacts are admissible as evidence and at what trust level?
- Which artifacts are excluded because they are stale, legacy, scratch, irrelevant, forbidden, or unknown?
- Which larger goals or directives justify the included context?
- Which contradictions or conflicts must be escalated instead of hidden?

The strongest implementation bias should be toward a small, deterministic, testable admission layer. The goal is not to make agents read more context. The goal is to make agents read less context, but make that context current, accepted, relevant, and auditable.
