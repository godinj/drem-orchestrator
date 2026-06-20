# Full Portability and Multi-Project Isolation PRD

## Problem Statement

Drem Orchestrator has made substantial progress toward containerized operation: per-project compose generation exists, worker and merger containers can be spawned through the spawner service, the project registry exists, Kyle can inspect registered orchestrators, metrics are persisted, and Grafana dashboards exist for operator visibility.

The system is still not proven fully portable or confidently isolated for multiple simultaneous projects. Running two active projects at once can still be confused by shared host paths, shared C-Suite state, global service names, shared Grafana datasource assumptions, ambiguous labels, singleton relay cursors, default ports, registry bind-mount foot-guns, and operator/Kyle mental-model drift.

The goal is to make Drem portable enough to run two or more Drem-managed projects simultaneously, experiment across them, and see clearly what is happening in each project and in the shared orchestration layer.

Project boundaries must become explicit and durable:

- Each project has isolated runtime identity, state roots, data roots, prompts, logs, tasks, agents, attempts, metrics, relays, and C-Suite project context.
- Global services remain global only when intentionally shared.
- Metrics and Grafana views support both per-project diagnosis and cross-project comparison.
- Kyle, Mike, Seth, and Alex can reason about multiple live projects without mixing missions, events, or failures.
- Install, bootstrap, teardown, migration, and canary validation prove the model end to end.

Two active Kyle-driven missions are related but narrower than this PRD:

- `8af09a24`: registry bind-mount guard for Kyle startup safety.
- `31858ad9`: mission-scoped relay so Kyle receives bounded lifecycle wakeups.

This PRD defines the broader portability and isolation destination.

## Solution

Build a full multi-project runtime model around Drem's existing containerization foundation.

Every component must be classified as either global or project-scoped. Every project-scoped component must carry project identity through configuration, container labels, filesystem roots, database rows, metrics labels, relay messages, dashboards, and CLI/API surfaces.

The solution has ten parts:

1. The project runtime registry becomes authoritative for local deployments. It records each project's name, stable ID, bare repository, language, orchestrator URL, allocated ports, state root, data root, prompt root, compose identity, C-Suite root policy, metric identity, and image overrides.
2. Port, name, and compose allocation become deterministic and collision-safe. Registration allocates project-specific ports, compose project names, container-name prefixes, volume names, network aliases, and labels.
3. Per-project state/data roots become explicit and complete. Each project receives isolated roots for orchestrator DB, WAL files, logs, prompts, artifacts, worker attempt records, relay cursor state, C-Suite watcher state, and project-owned C-Suite memory/state.
4. Global and per-project services are formally separated. Global services include only intentionally shared infrastructure such as image registry, SGLang, GQ/admission control, spawner, docker-query proxy, Kyle world API, and optionally shared Grafana/Prometheus surfaces.
5. C-Suite/persona isolation becomes project-aware. Shared executive identity can remain, but project-specific state and routing must be isolated by default.
6. Worker, spawner, and merger isolation is validated for simultaneous projects. Every worker and merger spawn carries stable project ID and human-readable project name.
7. Event relay isolation becomes mission- and project-scoped. Relays maintain per-project/per-recipient cursors and include project identity, mission identity, task identity, status, and bounded instructions in each message.
8. Metrics and dashboards become multi-project clear. Metrics include bounded, low-cardinality labels for project, project ID, agent role, model, provider, attempt status, task status, service, and global/per-project scope.
9. Install/bootstrap/teardown/migration become repeatable. Operators can register, start, stop, update, validate, and remove each project without hand-editing generated files or affecting other projects.
10. A simultaneous-project canary validates the architecture: two projects run at once, each drives work through the orchestrator pipeline, and metrics, relays, dashboards, worker attempts, C-Suite routing, and teardown remain clearly isolated.

## User Stories

1. As the operator, I want to register a second Drem-managed project while the first remains running, so that I can work on multiple systems at once.
2. As the operator, I want project registration to allocate unique ports, names, roots, compose identity, and labels automatically, so that I do not hand-resolve collisions.
3. As the operator, I want each project to have its own data root, so that SQLite files, logs, prompts, and artifacts cannot overwrite another project.
4. As the operator, I want each project to have a stable project ID and a human-readable name, so that renames do not break metrics, labels, and historical records.
5. As the operator, I want per-project compose generation to be deterministic, so that I can regenerate files safely and review drift.
6. As the operator, I want `register --update` to preserve tokens, ports, image overrides, and operator-owned overrides.
7. As the operator, I want a clear list of global services versus per-project services, so that I know what can be restarted without affecting all projects.
8. As the operator, I want teardown for one project to leave global services and other projects untouched.
9. As the operator, I want a dry-run teardown plan showing containers, volumes, files, and registry entries before removal.
10. As the operator, I want project-specific health checks, so that global health does not hide a single unhealthy project.
11. As the operator, I want a cross-project world view and per-project world views.
12. As Kyle, I want project identity included in every task, worker, event, attempt, and metric I receive.
13. As Kyle, I want mission-scoped events routed only when they match my mission ownership or sponsorship.
14. As Mike, I want broad operational events grouped by project, so that I can manage system health across projects without confusing failure domains.
15. As Seth, I want quality-gate evidence scoped to the project under review.
16. As Alex, I want product-scope reports to distinguish project, mission, and experiment context.
17. As a C-Suite persona, I want inbox/outbox/state to be project-aware, so that messages do not accidentally cross project threads.
18. As a worker, I want my prompt, repository mount, branch, auth mount, environment, and labels generated from one project runtime context.
19. As a worker, I want my container labels to include project name, project ID, task ID, worker ID, agent type, branch, image, provider, model, and attempt ID where appropriate.
20. As the spawner, I want a project runtime descriptor rather than loose string fields, so that spawn requests cannot omit required isolation data.
21. As the spawner, I want to reject spawn requests with missing or inconsistent project identity.
22. As the merger, I want merge parameters to include project identity and orchestrator callback identity.
23. As the merger, I want to mount only the target project's bare repository.
24. As the orchestrator, I want worker attempts recorded with project ID, task ID, attempt ID, container ID, image, model, provider, status, and failure classification.
25. As the orchestrator, I want stale worker reconciliation to be project-scoped.
26. As agentmon or the log collector, I want to ingest only containers labeled for my project.
27. As the event relay, I want per-project and per-recipient cursor state.
28. As the event relay, I want each message to include project, task, mission, event type, current status, and bounded next action.
29. As the operator, I want Grafana dashboards with a project selector.
30. As the operator, I want Grafana dashboards with cross-project comparison panels.
31. As the operator, I want metrics to label project and scope consistently.
32. As the operator, I want high-cardinality details like task ID, attempt ID, and container ID available through drill-down tables or logs, not every top-level time series.
33. As the operator, I want global service metrics distinguished from per-project service metrics.
34. As the operator, I want install docs for bootstrapping project one, project two, and the global stack.
35. As the operator, I want migration guidance for existing projects.
36. As the operator, I want a simultaneous-project canary that covers task execution, worker spawn, merge, C-Suite routing, relays, metrics, dashboard checks, and teardown.
37. As the operator, I want one project's induced failure to leave the other project healthy.
38. As the operator, I want generated files to fail fast when required host files are missing, so Docker does not silently create directories in place of files.
39. As the operator, I want project registration to validate bare repo config, auth files, token files, prompt roots, and data roots.
40. As the operator, I want a local portability profile, so the stack can be moved to another Linux host by copying project roots, registry state, image tags, and documented auth prerequisites.

## Implementation Decisions

- Treat this as a second-stage portability and isolation layer on top of existing containerization, not a rewrite of the task lifecycle.
- Preserve one orchestrator per project unless a later design proves a multi-project orchestrator is safer.
- Strengthen the project runtime registry as the source of truth for stable project ID, display name, slug, language, bare repo, orchestrator URL, host port, compose identity, roots, C-Suite policy, auth/token paths, image overrides, and lifecycle status.
- Separate configured project state from runtime inventory of running project services.
- Add a project slug/name allocation interface for Docker compose names, container prefixes, volume names, labels, and network aliases.
- Add a persisted, deterministic, collision-aware port allocation interface.
- Make compose generation consume a runtime descriptor instead of ad hoc template fields.
- Keep only intentionally shared services in global compose.
- Require project name and project ID labels on every per-project service, worker, and merger.
- Build worker and merger spawns from a single project runtime context object.
- Maintain dual identity: human-readable project name for operators and stable project ID for joins, labels, attempts, and metrics.
- Add isolated project sub-roots for data, prompts, logs, artifacts, relay cursors, C-Suite project state, and generated config.
- Make C-Suite roots project-specific by default, with explicit shared-root overrides reserved for deliberate operator topology choices.
- Scope relay cursors by project and recipient.
- Promote mission metadata to durable task metadata: mission owner/governor, mission correlation ID, mission kind, and project.
- Define a canonical low-cardinality metrics label vocabulary: `project`, `project_id`, `scope`, `service`, `agent_type`, `provider`, `model`, `status`, `failure_class`, `language`, and `experiment`.
- Keep task ID, attempt ID, worker ID, and container ID out of high-volume default series; preserve them in SQLite rows, events, logs, and drill-down tables.
- Revisit Grafana datasource strategy because a single mounted DB path does not naturally represent multiple project databases.
- Evaluate one datasource per project, a read-only aggregate query service, or Prometheus scrape endpoints for live health plus SQLite dashboards for historical data.
- Label global service metrics with `scope=global` and per-project service metrics with `scope=project`.
- Keep SGLang and GQ global unless GPU/resource isolation becomes a later requirement; requests should carry project attribution where feasible.
- Decide whether warm planner/classifier services are global shared services with project labels or per-project services.
- Harden agentmon/docker-query access so project filtering is strict and tested.
- Add bootstrap, update, validation, teardown, and migration flows that operate on one project at a time without disturbing others.
- Require the first canary to run two active projects simultaneously and induce a failure in one while proving the other remains healthy.

## Testing Decisions

Tests should assert externally visible behavior at module boundaries: runtime descriptors, compose/service identity, spawn requests, labels, mounts, API responses, metrics labels, relay messages, dashboard provisioning, and canary outcomes.

Required coverage:

- Project runtime registry tests for multiple registration, stable IDs, unique slug/name allocation, unique ports, update preservation, unsafe-name rejection, and required host-file validation.
- Compose generation tests proving per-project labels, unique resources, global service non-duplication, and explicit missing-file validation.
- Spawner tests requiring project name and project ID, preventing caller label override, and proving mounts/prompts/auth come from the correct project runtime context.
- Merger tests proving project-bound mounts, callbacks, and merge result events.
- Orchestrator/API tests proving per-project task/worker/event isolation, Kyle aggregation separation, project-scoped stale worker reconciliation, and project identity on attempts.
- Relay tests proving broad ops to Mike remains intact, mission-scoped events route to Kyle only when matching mission/project metadata, cursors are per project/recipient, and messages include bounded-turn instructions.
- Metrics tests proving canonical project labels, no high-cardinality default series, correct cross-project aggregation, and `scope=global` versus `scope=project` behavior.
- Grafana/provisioning tests proving project filters, multi-project datasource handling, valid dashboards, and no accidental first-project-only panels.
- Install/teardown tests proving two-project bootstrap in a sandbox home, update preservation, one-project teardown safety, and actionable missing-file failures.
- A simultaneous-project canary covering two registered projects, concurrent project stacks, task execution or controlled failure in each, Kyle visibility, Grafana filtering/comparison, relay correctness, induced failure isolation, and safe teardown.

## Out of Scope

- Multi-host orchestration or cluster scheduling.
- Multi-user or hostile multi-tenant security guarantees.
- Vault/KMS-grade secrets management.
- GPU partitioning per project beyond shared SGLang/GQ attribution.
- Replacing Docker Compose with Kubernetes.
- Replacing SQLite as the per-project historical store.
- Rewriting the TUI as a web UI.
- Changing the core task lifecycle state machine solely for portability.
- Retiring existing C-Suite personas or changing their product roles.
- Full cloud backup/restore automation.
- Remote image registry publishing, except ensuring this design does not block it later.

## Further Notes

The repo already has meaningful foundations: project registry, per-project compose templates, host-port allocation concepts, dual project labels in spawner types, worker attempt recording, Kyle polling, event relay, SQLite metrics, and Grafana dashboards.

The remaining issue is confidence and completeness. Current behavior is multi-project-shaped, but not yet proven as a complete simultaneous multi-project runtime. Known pressure points include single mounted DB assumptions, C-Suite host tools that may still default to legacy global roots unless configured, singleton Grafana datasource paths, global relay cursor defaults, and registry/host bind-mount safety.

This PRD should be treated as the portability finish line. The system is not done until a two-project canary proves task execution, observability, relays, C-Suite routing, metrics, dashboards, and teardown all work concurrently with clear attribution.
