# Worker /bare Mount Read-Write — Implementation Plan

Status: **implemented, 2026-04-20.** One-line fix + test + this plan.
Closes the T3 canary v9 regression where the worker cloned its branch,
execed claude, and committed work — but the watchdog's `git push origin`
failed with `remote unpack failed: unable to create temporary object
directory`.

## 1. Problem

`internal/spawner/methods.go:72` flips the `/bare` bind-mount read-only
unless `SpawnWorkerParams.BareRepoReadWrite == true`. Merger dispatch in
`internal/orchestrator/merge_dispatch.go:200` sets the flag. Worker
dispatch (coder/reviewer/fixer/tester/supervisor) via
`spawnTypedWorker` in `internal/orchestrator/worker_spawn.go` did not,
so every container worker ended up with a read-only view of the bare
repo. The worker's `drem-watchdog` runs `git push origin <branch>` on
each commit tick; the push refuses to create temporary objects under
`/bare/objects/incoming-*/` because the filesystem is mounted ro.

Observable symptom inside the worker:

```
watchdog: commit tick error: watchdog: push origin feature/...: exit 1:
error: remote unpack failed: unable to create temporary object directory
To /bare
 ! [remote rejected] feature/... -> feature/... (unpacker error)
error: failed to push some refs to '/bare'
```

The design intent was always rw for workers. `worker-entrypoint.sh`'s
header comment makes it explicit:

> Re-point origin at /bare so that git push from inside the watchdog
> lands back in the bare repo. /bare is mounted read-write for workers
> precisely because the watchdog needs to push (PRD §Lifecycle and
> recovery, user stories 17, 18).

The spawner's default of `ReadOnly: true` was chosen to make merger
(which is the only path that needs rw for bulk fetch+merge+push)
opt-in. Workers were meant to opt in too, but the orch-side wiring
never did.

## 2. Fix

`internal/orchestrator/worker_spawn.go` — set `BareRepoReadWrite: true`
on the `SpawnWorkerParams` built inside `spawnTypedWorker`. One field
assignment, covers every typed-worker dispatch site (coder, reviewer,
fixer, tester, supervisor).

Merger keeps its own explicit `BareRepoReadWrite: true` in
`merge_dispatch.go` — the two paths construct `SpawnWorkerParams`
independently, so the flag has to live in both. Duplication is
acceptable here: the two sites are already divergent in several other
fields (merger uses Cmd, workers use Env; merger skips creds, workers
require creds) and hoisting the flag would obscure that divergence.

## 3. Test

`internal/orchestrator/worker_spawn_test.go::TestSpawnCoder_BuildsExpectedParams`
grew one assertion:

```go
require.True(t, p.BareRepoReadWrite,
    "workers need /bare mounted rw so the watchdog can push commits")
```

No new test file — the assertion rides inside the existing "build
expected params" test alongside the other contract assertions
(`CredsMount`, `PromptMount`, no API-key in env, etc.). If a future
change accidentally flips the default back to read-only, this
assertion fails at test time rather than silently regressing at
canary time.

## 4. Security stance

Workers already hold subscription-auth creds and a writable view of
their own container filesystem; rw access to the bare repo is a
smaller privilege delta than those. The bare repo is the canonical
source of truth and the whole point of the worker is to push commits
to it. A rogue worker could force-push any branch — mitigated at the
`gitref.EnsureBranch` boundary (which refuses to force-reset existing
branches) and at the merger boundary (which re-validates integration
branches before merging). The rw mount does not broaden the
worker-compromise blast radius meaningfully.

## 5. Not in scope

- Per-branch push restriction. Could be added via a git update-hook
  in the bare repo that rejects force-pushes from worker identity.
  Worth a separate conversation; not a canary unblocker.
- Moving the flag into `buildSpawnContext`. Keeping it at the
  `SpawnWorkerParams` construction site makes the divergence from
  merger visible; hiding it inside `swc` would require a new field
  on `spawnWorkerContext` that's always `true` for workers, which is
  worse-of-both.

## 6. Rollout

1. `go vet ./...` + `go test -count=1 ./...` green.
2. Rebuild `drem-orch:latest`, push, `docker compose up -d --no-deps
   --force-recreate --pull=always orch`.
3. Re-run the T3 canary and confirm the worker's watchdog can push
   commits to the bare repo.
