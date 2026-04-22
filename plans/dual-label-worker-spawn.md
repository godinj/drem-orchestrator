# Dual-label worker spawn (`drem.project` + `drem.project_id`)

## Problem: v13-v14 canary outage (41 hours silent)

Agentmon's Docker event filter uses
`Labels: map[string]string{"drem.project": *project}` where `*project`
is the `DREM_PROJECT` env var from the per-project compose (the
human-readable project **name**, e.g. `drem-orchestrator`).

Workers, however, were spawned with
`Project: o.projectID.String()` in
`internal/orchestrator/worker_spawn.go::buildSpawnContext`, which flowed
into `spawner.SpawnWorkerParams.Project` and then into the container
label `drem.project=<UUID>`.

Docker label filters are exact-match string comparisons. `<name>` never
equals `<UUID>`, so:

- Agentmon's event subscription matched zero containers.
- Worker stdout was never tailed.
- DB heartbeats stayed frozen at worker-start.
- The reconciler's "agent session died without producing commits" path
  fired on every live worker because `ag.HeartbeatAt` was stale.
- Live workers got killed; new workers inherited the same label
  mismatch; the loop ran 41 hours before an operator noticed.

Kyle hot-fixed the live compose by hardcoding the project UUID into
`~/.drem/projects/drem-orchestrator/compose.yml`, which works until
`drem project register --update --force` regenerates the file.

## Seth's reframe: dual labels are not redundant

`drem.project_id` (UUID) and `drem.project` (name) are a standard
stable-identifier + human-label pair, not redundancy. UUIDs are primary
keys; names are display strings. They carry different semantics — one
is stable under rename, the other is readable to humans — and every
object in our system eventually grows both. Writing both on the
container is the correct long-run shape, not a maintenance smell.

## Design

### Container labels (authoritative)

Every worker container (claude-backed or Go binary merger) carries both:

| Label             | Source                     | Consumers                            |
|-------------------|----------------------------|--------------------------------------|
| `drem.project`    | `SpawnWorkerParams.Project`   | agentmon, operator shell filters     |
| `drem.project_id` | `SpawnWorkerParams.ProjectID` | orch event watcher, reconcilers, reg |

`drem.project_id` is the stable key; orch-side filters migrate to it so
a project rename never drops events. `drem.project` is the human label;
agentmon keeps using it because `DREM_PROJECT` in the generated compose
is the name.

### Orchestrator plumbing

`Orchestrator` gains a `projectName string` field alongside the existing
`projectID uuid.UUID`. Both `New` and `NewWithExperimentScheduling`
take it as a positional parameter; `cmd/drem/main.go` passes the
already-derived `projectName` (from the bare repo basename) into the
constructor.

`buildSpawnContext` populates a new `spawnWorkerContext.projectID` field
alongside `project` (now the name), and `spawnTypedWorker` threads both
into `spawner.SpawnWorkerParams` as `Project` (name) and `ProjectID`
(UUID). `DREM_PROJECT` env on the worker is the name (matches agentmon
and the legacy convention in `deploy/docker/context/csuite-*.sh`).

### Spawner surface

`SpawnWorkerParams` grows `ProjectID string` beside `Project`.
`SpawnWorker` emits both labels. `WorkerInfo` gains `ProjectID` so
`ListWorkers` can filter without a Docker label round-trip.
`ListWorkersParams` grows `ProjectID` as a second, AND'd filter. Empty
fields are ignored so legacy operator-side name filters keep working.

### Internal filter consumers (migrated to UUID)

| File                                           | Change                                                 |
|------------------------------------------------|--------------------------------------------------------|
| `internal/orchestrator/docker_events.go`       | `drem.project` -> `drem.project_id` in `EventFilter`   |
| `internal/orchestrator/reconcile_containers.go`| `ListWorkersParams{Project:}` -> `{ProjectID:}`        |
| `internal/orchestrator/reconcile_stuck.go`     | same                                                   |
| `internal/orchestrator/merge_dispatch.go`      | `Project: UUID` -> `Project: name` + `ProjectID: UUID` |

### Template (unchanged)

`internal/projects/templates/project-compose.yml.tmpl` already has
`DREM_PROJECT: "{{.ProjectName}}"` (the human-readable name) on every
service. Before this fix that was wrong relative to the workers (which
labeled with the UUID); after this fix it is correct. No template edit
required.

### Live compose rollback

Kyle's hot-fix to `~/.drem/projects/drem-orchestrator/compose.yml` is
reverted automatically by running:

```
drem project register --update --force drem-orchestrator
```

The regenerated compose uses the standard template; agentmon's
`DREM_PROJECT` env is the name; workers spawn with the name label;
everything matches.

## Regression test (Seth's non-negotiable)

`TestSpawnCoder_BuildsExpectedParams` in
`internal/orchestrator/worker_spawn_test.go` now asserts:

- `SpawnWorkerParams.Project == "<known name>"` (human-readable)
- `SpawnWorkerParams.ProjectID == "<known UUID>"` (stable key)
- `Env["DREM_PROJECT"] == "<known name>"`

`TestService_SpawnWorker_ProducesSpawnCallWithLabelsAndMounts` in
`internal/spawner/service_test.go` now asserts both container labels
are present on the resulting `container.Spec.Labels`.

`TestService_ListWorkers_FiltersByProject` now exercises both filter
fields (`Project` by name, `ProjectID` by UUID) so the spawner's
AND-filter semantic is covered.

## Constitution guardrails verified

- `internal/orchestrator/orchestrator.go`: 809 lines before and after
  (shrink-only; net zero). The struct field + two constructor params
  are balanced by comment trims on `New` and `NewWithExperimentScheduling`.
- `internal/orchestrator/` package unique internal imports: 20 (under
  the 34 grandfathered cap; no new internal import added).
- Subscription-only auth: untouched. No `CLAUDE_CODE_OAUTH_TOKEN`,
  `ANTHROPIC_API_KEY`, or `ANTHROPIC_AUTH_TOKEN` anywhere in scope.

## Non-goals

- Project rename workflow. Adding a rename path is out of scope; the
  contract here just makes a future rename non-fatal by filtering on
  the stable UUID.
- Migrating `gitref.BranchRef.Project` semantics. It was already a
  human-readable label in tests (e.g. `"drem-orch"`) even though orch
  was passing it a UUID; the switch to the name aligns production with
  the test expectations.

## Deployment

Kyle owns the image rebuild + deploy. The dual-label fix is code-only —
no compose template edits, no image-schema migrations. Regenerating the
live compose with `drem project register --update --force` after deploy
drops the UUID-hardcoded hot-fix.
