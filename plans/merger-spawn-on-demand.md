# Merger: spawn-on-demand wiring

Status: implemented 2026-04-19 on worktree-agent-ab9c5911 — see
`plans/merger-spawn-on-demand-impl.md` for commit list, test surface,
and exit-code → state-machine routing. Acceptance criteria 1–3, 6, 7
land with that branch; 4 (per-project serialization) and 5 (cross-
project parallelism) are properties of the existing tick loop (one
tick-goroutine per orchestrator process, one orchestrator per project)
and require no new code.

## Problem statement

The per-project compose template previously declared a `merger-pool` service
with `replicas: 2`, restart policy `unless-stopped`, and no command-line
arguments. The intent (per `docs/prd-containerization.md` US 9 and the
Phase 6 plan in `plans/containerization.md`) was a "warm pool" of merger
containers ready to pick up merges as soon as a worker finishes.

The actual `drem-merger` binary (`cmd/drem-merger/main.go`) does not match
that intent. It is a per-task one-shot:

- Hard-required flags: `--feature-branch`, `--project`, `--task-id`,
  `--test-cmd`, `--orch-url`, `--agentmon-token`.
- Exits non-zero immediately if any flag is missing.
- Performs exactly one merge per process invocation, then exits with a
  status code documenting the outcome (see `exitCodeFor` for the table).

Running this binary as a long-lived `replicas: 2` service with no argv:

1. Container starts.
2. `parseFlags` returns `missing required flags: [--feature-branch ...]`.
3. Process exits 1.
4. Compose `restart: unless-stopped` respawns it.
5. Goto 1, forever.

This produces the observed crash loop: `drem-orchestrator-merger-pool-{1,2}`
with stderr `drem-merger: missing required flags: [...]`.

## Current state

- `internal/projects/templates/project-compose.yml.tmpl` no longer
  declares a running `merger-pool` service. It keeps a `merger-template`
  stub gated behind `profiles: ["never"]` so `docker compose pull` still
  primes the merger image, mirroring the pre-existing `worker-template`
  pattern.
- `cmd/drem-merger`, `internal/merger/`, and `deploy/docker/merger.Dockerfile`
  are unchanged. The one-shot binary contract is correct; only the
  compose service definition was wrong.
- No code path currently spawns a merger container on task completion.
  Merges that previously would have been handled by the warm pool are
  now silently dropped.

## Proposed fix: spawner-driven merger invocation

Mirror the existing worker-spawn pattern:

1. Orchestrator detects "merge needed" — typically on a worker reporting
   a successful test run on its feature branch (or however the existing
   merge-trigger logic in `internal/orchestrator/` decides to merge today).
2. Orchestrator constructs a `spawner.SpawnWorkerParams` payload with
   `AgentType: "merger"` (the spawner image table in
   `internal/spawner/images.go` already maps `merger` to
   `localhost:5000/drem-merger:latest`).
3. Orchestrator passes the merger's required flags via the container
   command (or via env vars the merger main reads as fallback). Flags
   needed:
   - `--feature-branch <branch>`
   - `--project <project name>`
   - `--task-id <task id>`
   - `--test-cmd <project test command>`
   - `--orch-url http://orch:8080`
   - `--agentmon-token <SharedToken>`
   - Optional: `--integration-branch <branch>` if the project's default
     differs from `master`.
   - Optional: `--gitref-db <DSN>` if the gitref registry is wired up.
4. Spawner creates a one-shot container on `drem-net` with the bare
   repository bind-mounted at `/bare:rw` (the merger needs write access
   to push the integration branch and delete the feature branch).
5. Container runs the merge, POSTs the structured `merge_result` record
   to `/internal/logs`, and exits with one of the documented status codes
   (0 success, 2 conflict, 3 tests failed, 4 push failed, 1 misc).
6. Spawner detects exit via the existing Docker-event subscription and
   removes the container from the worker registry. There is no restart.
7. Orchestrator records the outcome from the `merge_result` log and
   advances the task state machine.

## References

- Worker spawn entry point: `internal/spawner/methods.go::SpawnWorker`
- Spawner RPC contract: `internal/spawner/types.go`
- Spawner image table (already includes `merger`):
  `internal/spawner/images.go`
- Spawner client used by orchestrator: `internal/spawner/client.go` and
  call sites under `internal/orchestrator/`
- Merger binary and exit code map: `cmd/drem-merger/main.go`
- Merger package (the merge operation itself): `internal/merger/merger.go`
- PRD and topology: `docs/prd-containerization.md` (US 9, US 22–24)

## Constraints

- The merger needs `/bare:rw`, unlike workers which mount `/bare:ro`.
  The spawner already supports this via the `BareRepoMount` field on
  `SpawnWorkerParams` (currently always read-only — see
  `internal/spawner/methods.go` where it sets `ReadOnly: true`). The
  spawner contract will need either:
  - a new `BareRepoReadWrite bool` field on `SpawnWorkerParams`, or
  - a separate `SpawnMerger` RPC that defaults to read-write `/bare`.
  Pick one in the implementation phase; do not silently flip the global
  default.
- The merger writes to the bare repository. Concurrent mergers on the
  same project would race on the integration branch. The orchestrator
  must serialize merge invocations per project (a per-project mutex
  on the merge dispatcher).
- The merger needs the project's test command. This is currently
  carried in the orchestrator's task record. Confirm the field name
  and pipe it through.

## Acceptance criteria for the future implementation

1. Removing or recreating the per-project compose stack does not
   produce a crash-looping merger container.
2. On a task whose worker pushes a green feature branch, the
   orchestrator spawns exactly one merger container, the container
   runs to completion, exits, and is removed from the spawner
   registry. No restart.
3. The merge result (success, conflict, tests-failed, push-failed)
   appears as a `merge_result` record at `/internal/logs` for the
   correct task ID.
4. Two simultaneous merge triggers on the same project are serialized
   — the second waits for the first to finish.
5. Two simultaneous merge triggers on *different* projects run in
   parallel.
6. The spawner's `/bare` mount mode is read-write only for merger
   containers; worker containers keep their read-only `/bare`.
7. Tests cover: spawn parameters built correctly from a task record;
   per-project serialization; non-zero merger exit propagated to the
   task state machine.
