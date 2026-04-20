# Merger Spawn-on-Demand — Implementation Plan (Phase 6 wiring)

Status: **implemented on worktree-agent-ab9c5911, 2026-04-19.** All
five commits from §8 landed. Tests added: 2 in
`internal/spawner/service_test.go` (Cmd forwarding, BareRepoReadWrite
toggles mount RO flag), 5 in `internal/orchestrator/merge_dispatch_test.go`
(argv composition, rw flag, default-branch elision, non-default branch
flag, exit-code → FailureReason mapping table), 4 in
`internal/orchestrator/merge_execution_test.go` (tests_failed /
push_failed / misc / unknown exit-code routing), 1 in
`internal/projects/template_test.go` (orch gets `DREM_ORCH_URL`).
Awaiting review before master merge.

Companion to the design doc `plans/merger-spawn-on-demand.md` (the PRD
source of truth). This file is the surgical implementation plan:
files, LOC, tests, and commit sequence.

## 0. Scope and key finding

The Phase-6 "merger spawn-on-demand" wiring is **~80% already
implemented** in the tree. The missing pieces are narrow:

- `internal/orchestrator/merge_dispatch.go` already contains
  `dispatchMerge`, which calls `o.Spawner.SpawnWorker` with
  `AgentType: "merger"` and polls `InspectWorker` until exit.
- `internal/spawner/images.go` already maps
  `merger → localhost:5000/drem-merger:latest`.
- `internal/state/machine.go` already has
  `StatusMerging → {StatusDone, StatusFailed}`.
- `internal/orchestrator/merge_execution.go::dispatchMerges` is called
  once per tick (see `orchestrator.go:504`) and iterates MERGING tasks
  **synchronously** — per-project merge serialization is therefore a
  property of the tick loop, not something we need to bolt on.
- `internal/container/runtime.go::Spec.Cmd []string` already exists.

**What is broken (confirmed by reading `dispatchMerge` line-by-line):**

1. `SpawnWorkerParams` has no `Cmd` field, so the required
   `--feature-branch / --task-id / --test-cmd / --orch-url /
   --agentmon-token` flags cannot be passed. The merger container
   would launch with empty argv and exit 1 via `parseFlags`.
2. `SpawnWorker` in `internal/spawner/methods.go:51` hard-codes
   `ReadOnly: true` on the `/bare` mount. The merger MUST have
   `/bare:rw` for `git push` / `git push --delete`.
3. `dispatchMerge` never passes test command, orchestrator URL, or
   agentmon token — those values don't currently reach the
   orchestrator process either.
4. Exit-code interpretation is binary (`ExitCode == 0`). Codes 2/3/4
   are conflated with "transient failure" and routed to
   `retry_policy.go`, which for a tests-failed merge just burns the
   retry budget before failing.
5. No tests cover the argv-building side of the dispatch contract.

Everything else in the design-doc acceptance criteria — "no
crash-looping merger in compose", "exactly one merger per task with no
restart", "spawner cleans up via Destroy" — is already satisfied by
the `dispatchMerge` + reconcile_containers + docker_events path.

## 1. Interface decision: extend `SpawnWorkerParams`, do NOT add `SpawnMerger`

**Pick A (extend `SpawnWorkerParams`) over B (new `SpawnMerger` RPC).**

- The spawner contract is framed in the PRD as a four-method surface.
  Adding a fifth RPC (`SpawnMerger`) requires: a new JSON-RPC method in
  `invoke()`, a new method on `Client`, a new method on the
  `WorkerSpawner` interface, a new stub in every fake, and a new
  matrix row of `ListWorkers / InspectWorker / DestroyWorker` coupling
  (mergers are still tracked in the same registry). The PRD explicitly
  frames the spawner as a single uniform lifecycle surface — splitting
  by agent type is exactly what the abstraction is meant to prevent.
- Two new fields on `SpawnWorkerParams` are enough: `Cmd []string`
  (argv to the container entrypoint) and `BareRepoReadWrite bool`
  (flip the default `/bare:ro` to `/bare:rw`). Both are generally
  useful: `Cmd` future-proofs any per-invocation binary (e.g.
  containerized C-Suite one-shots in Phase 7.4), and
  `BareRepoReadWrite` is the minimum expressive change that keeps
  worker containers read-only and mergers read-write without a
  separate RPC.
- Backward compatibility: both fields zero-default to today's
  behavior. Existing callers and tests pass unchanged.

Name choice: `BareRepoReadWrite` (not `BareRepoRW` or inverting to
`BareRepoReadOnly`) because inverting would reverse the default
behavior of every worker spawn — silently flipping the global default
is forbidden by the PRD's "Constraints" section.

## 2. Exact file list with LOC estimate

| File | Change | LOC |
|---|---|---|
| `internal/spawner/types.go` | Add `Cmd []string` and `BareRepoReadWrite bool` to `SpawnWorkerParams`; extend doc comment | +6 |
| `internal/spawner/methods.go` | Plumb `Cmd` into `container.Spec`; flip `ReadOnly` based on `p.BareRepoReadWrite` | +4 |
| `internal/spawner/service_test.go` | Two new tests: Cmd forwarded; BareRepoReadWrite toggles mount RO flag | +50 |
| `internal/orchestrator/merge_dispatch.go` | Build `[]string` argv from task+project+testGate+env; set `BareRepoReadWrite: true`; populate `Cmd`; read `DREM_ORCH_URL`/`DREM_AGENTMON_TOKEN` from orchestrator fields (new) with env-var fallback; honour typed exit codes (0/2/3/4/1) when mapping to `MergeResult.FailureReason` | +70 |
| `internal/orchestrator/orchestrator.go` | Add `orchURL string` and `agentmonToken string` fields to `Orchestrator`; add `SetInternalEndpoints(url, token string)` setter | +20 |
| `internal/orchestrator/merge_execution.go` | Teach `executeMerge` to distinguish conflict (exit 2) vs test-fail (exit 3) vs push-fail (exit 4) vs transient (other) by reading `result.ExitCode` before the `len(result.Conflicts) == 0` branch; feed exit-code → `FailureReason` into the existing retry / fixer logic; no state-machine changes | +40 |
| `internal/orchestrator/merge_execution_test.go` | Extend `stubMerger` with `ExitCode` field; add 4 new tests covering each exit code's target terminal state | +120 |
| `internal/orchestrator/worker_spawn_test.go` | No code change needed — `fakeWorkerSpawner` already accepts any params | 0 |
| `internal/orchestrator/merge_dispatch_test.go` (**new file**) | Unit-level coverage of argv composition (no state-machine involvement): feature-branch, task-id, test-cmd, orch-url, agentmon-token all present; integration-branch defaulting; `BareRepoReadWrite: true` | +130 |
| `cmd/drem-orchestrator/main.go` | Read `DREM_ORCH_URL` and `DREM_AGENTMON_TOKEN` env and call new `SetInternalEndpoints` | +8 |
| `internal/projects/templates/project-compose.yml.tmpl` | Add `DREM_ORCH_URL: "http://orch:8080"` to the orch service env block (already has `DREM_AGENTMON_TOKEN`). Adjust bare-repo mount on orch from `:rw` to keep it; no change to merger-template (still profiles: never) | +1 |
| `internal/projects/template_test.go` | Assert orch gets `DREM_ORCH_URL` | +5 |

**Deleted**: none. No shell scripts, no new Go packages.

**Totals**: ~454 LOC touched across 11 files (3 net-new tests files /
8 modified), all Go / one YAML template.

## 3. Orchestrator wiring point

**State-machine transition that triggers the spawn**: the existing
`StatusTestingReady → StatusMerging` transition. This is already wired
by existing logic in `internal/orchestrator/test_execution.go` and
`internal/orchestrator/merge_execution.go::transitionQuickFixToMerging`.
No new transition is needed; `state.ValidTransitions` already allows
`Merging → {Done, Failed}`.

**Handler function**: `Orchestrator.dispatchMerge` in
`internal/orchestrator/merge_dispatch.go` (existing). Invocation chain:

```
Run() tick → dispatchMerges() → executeMerge(task)
            → mergeDispatch(ctx, task)
            → dispatchMerge(ctx, task)
            → o.Spawner.SpawnWorker(...)
```

All extension lives **inline in `merge_dispatch.go`** — no new file.
Changes:

- Build `[]string{"--feature-branch", task.WorktreeBranch, "--project",
  o.projectID.String(), "--task-id", task.ID.String(), "--test-cmd",
  o.testGate.TestCommand, "--orch-url", o.orchURL, "--agentmon-token",
  o.agentmonToken}` and append `--integration-branch` + `--gitref-db`
  when non-default.
- Set `params.Cmd = argv`.
- Set `params.BareRepoReadWrite = true`.
- After `awaitMergerExit`, map `finalState.ExitCode` to
  `result.FailureReason`: `0 → ""`, `2 → "conflict"`,
  `3 → "tests_failed"`, `4 → "push_failed"`, `1 → "misc"`,
  other → `"unknown"`. Keep the existing agentmon-context-pull logic
  for `MergeCommit` / `Conflicts`; the `merge_result` POST the merger
  container issues still supplies those via `/internal/logs`.

## 4. Per-project serialization

**Decision: no new mutex. Do nothing.**

`dispatchMerges` at `merge_execution.go:17` iterates
`model.StatusMerging` tasks *synchronously* within one tick. There is
exactly one tick loop per orchestrator process, and exactly one
orchestrator process per project. Therefore:

- Two merges **on the same project** are already serialized (by the
  tick loop).
- Two merges **on different projects** already run in parallel
  (separate orch containers).

A `sync.Mutex` per project would be redundant; a DB-backed lock would
be overkill. The acceptance-criteria tests in the PRD (items 4 and 5)
are satisfied by the existing loop shape — adding a test that proves
it is cheap and useful.

If we ever move to `go o.executeMerge(task)` (async per task inside a
tick), *then* we'd want a `sync.Mutex` embedded in `Orchestrator` —
process-local is correct because there's one orch per project. That
change is out of scope for Phase 6.

## 5. Exit-code → state transition table

| Merger exit | `MergeResult.FailureReason` | Terminal task state | `executeMerge` branch (where added) |
|---|---|---|---|
| **0** (success) | `""` | `StatusDone` | Existing success branch — `state.TransitionTask(task, StatusDone, ...)` at `merge_execution.go:42`. |
| **2** (conflict) | `"conflict"` | `StatusFailed` (with fixer spawn when supervisor present) | Existing conflict branch — `result.Conflicts != nil` path triggers `SpawnFixerSession` or `failTask`. The `merge_result` POST from the merger populates `task.Context["merge_conflicts"]` which `dispatchMerge` already hydrates into `result.Conflicts`. |
| **3** (tests failed) | `"tests_failed"` | `StatusFailed` (immediate, no retry) | **New branch**: before the `len(result.Conflicts) == 0` transient check, `if result.FailureReason == "tests_failed" { return o.failTask(task, "merge pre-push tests failed") }`. Tests-failed is not a conflict (len==0) but is also not transient — it must bypass retry. |
| **4** (push failed) | `"push_failed"` | `StatusMerging` → retry up to `MaxMergeRetries`, then `StatusFailed` | Treated as transient. Push failure typically means the remote advanced; retry is the right call. Falls into the existing `len(result.Conflicts) == 0` retry path. |
| **1** (misc / config error) | `"misc"` | `StatusFailed` (immediate) | **New branch**: after tests_failed, `if result.FailureReason == "misc"` return `failTask` with the captured err. Config errors never heal by retry. |
| **other** | `"unknown"` | `StatusFailed` (immediate) | Same as misc; safer to fail loud than to retry an unrecognized signal. |

The state machine itself (`internal/state/machine.go`) needs **zero
changes** — `StatusMerging` already transitions only to `Done` or
`Failed`. All differentiation is in `executeMerge`'s failure-mode
routing.

## 6. Test surface

### New unit tests

**`internal/spawner/service_test.go`** (extend):

- `TestService_SpawnWorker_CmdForwardedToSpec` — call with
  `Cmd: []string{"--foo", "bar"}`; assert `FakeRuntime` Spawn call
  captured `spec.Cmd == ["--foo","bar"]`.
- `TestService_SpawnWorker_BareRepoReadWriteFlipsMount` — call with
  `BareRepoReadWrite: true`; assert `spec.Mounts[0].ReadOnly == false`.
  Call without (zero) → `ReadOnly == true` (default unchanged).

**`internal/orchestrator/merge_dispatch_test.go`** (new file, fake
spawner):

- `TestDispatchMerge_BuildsRequiredArgv` — captures
  `spawner.SpawnWorkerParams`, asserts `params.Cmd` contains all six
  required flags in pair order and that `--test-cmd` reflects
  `testGate.TestCommand`.
- `TestDispatchMerge_SetsBareRepoReadWrite` — asserts
  `params.BareRepoReadWrite == true`.
- `TestDispatchMerge_DefaultIntegrationBranch` — no
  `--integration-branch` flag when worktree's default branch is plain
  `main`/`master`; flag present otherwise.
- `TestDispatchMerge_ExitCodeMapping` — table-driven: feed
  `fakeWorkerSpawner.inspectResult.ExitCode` ∈ {0,1,2,3,4,99} and
  assert `result.FailureReason` ∈
  {"", "misc", "conflict", "tests_failed", "push_failed", "unknown"}.

**`internal/orchestrator/merge_execution_test.go`** (extend existing):

- `TestExecuteMerge_TestsFailedFailsImmediately` —
  `FailureReason:"tests_failed"`, `Conflicts:nil`. Assert terminal
  status `StatusFailed`, attempt count 0.
- `TestExecuteMerge_PushFailedRetries` —
  `FailureReason:"push_failed"`. Assert stays in `StatusMerging`,
  attempt count 1.
- `TestExecuteMerge_MiscExitFailsImmediately` —
  `FailureReason:"misc"`. Assert `StatusFailed`, attempt count 0.
- `TestExecuteMerge_SerializesSameProjectMerges` — two tasks both in
  `StatusMerging` in the same DB; single call to `dispatchMerges`
  processes them sequentially (the stub counts calls and checks they
  do not overlap).

### Existing fakes to extend

- `internal/spawner/service_test.go` → `container.FakeRuntime` already
  captures `spec.Cmd` and `spec.Mounts`; no extension needed.
- `internal/orchestrator/worker_spawn_test.go` → `fakeWorkerSpawner`
  already has `inspectResult spawner.InspectWorkerResult` with
  `ExitCode int` — no extension needed.
- `internal/orchestrator/merge_execution_test.go` → `stubMerger`
  returns `*MergeResult` directly; the new `FailureReason`-bearing
  results tests need one line each.

### Integration test

**No new Docker-using integration test.** The existing
`internal/container/docker_integration_test.go` already proves the
runtime loop. The merger-specific wiring is entirely covered by unit
tests against the fake. Operators doing a manual smoke (PRD
acceptance criterion 2) run a real task on a registered project and
inspect `docker ps -a --filter label=drem.agent_type=merger` — no
scripted check needed.

## 7. MVP scope cut

**The explore-agent suggestion ("one merge per project, no
concurrency") is already the current behaviour.** There is no smaller
slice; the MVP *is* the plan above. Specifically:

- Concurrency: the tick loop already serializes per-project merges
  (see §4).
- Single merge: the `dispatchMerge` code path already handles one
  task per invocation with one container.
- No warm pool: compose template already has `profiles: ["never"]` on
  the merger stub.

What the "MVP" reduces to in practice is: **do not skip any of the
file edits in §2**. Every one is load-bearing:

- Skip `Cmd` plumbing → merger crash-loops on empty argv. Not
  shippable.
- Skip `BareRepoReadWrite` → merger can't push. Not shippable.
- Skip exit-code branching → tests_failed merges burn retries
  (wasteful but not broken). **This is the only optional piece.** A
  one-commit "even smaller slice" could defer the exit-code table to
  a follow-up.

**Smallest shippable slice (suggested single-commit MVP):** everything
in §2 *except* the merge_execution.go exit-code branching and its
tests. Merger runs, mergers succeed on clean paths, merger-failures
all route through the existing transient/conflict retry logic
(suboptimal but correct). Ship it, then refine exit-code handling in
the next commit.

## 8. Commits (≤ 5)

| # | Subject (conventional-commits style) | Files |
|---|---|---|
| 1 | `feat(spawner): add Cmd and BareRepoReadWrite to SpawnWorkerParams` | `internal/spawner/types.go`, `internal/spawner/methods.go`, `internal/spawner/service_test.go` |
| 2 | `feat(orch): plumb orch URL and agentmon token into Orchestrator` | `internal/orchestrator/orchestrator.go`, `cmd/drem-orchestrator/main.go`, `internal/projects/templates/project-compose.yml.tmpl`, `internal/projects/template_test.go` |
| 3 | `feat(orch): wire merger spawn-on-demand argv and rw bare mount` | `internal/orchestrator/merge_dispatch.go`, `internal/orchestrator/merge_dispatch_test.go` (new) |
| 4 | `feat(orch): route merger exit codes to typed merge failure reasons` | `internal/orchestrator/merge_execution.go`, `internal/orchestrator/merge_execution_test.go` |
| 5 | `docs(plans): mark merger-spawn-on-demand done; update containerization phase 6` | `plans/merger-spawn-on-demand.md`, `plans/containerization.md` (status lines only) |

Commits 1-2 can land independently and are covered by existing
`go build ./...`. Commit 3 depends on commit 1 (new field). Commit 4
depends on commit 3 (new FailureReason values). Commit 5 is
documentation.

## 9. Hard-constraint compliance

- **No shell scripts**: all argv construction is `[]string` Go slices
  passed via `container.Spec.Cmd`. Existing
  `deploy/docker/merger.Dockerfile` already has
  `ENTRYPOINT ["/usr/local/bin/drem-merger"]` in exec form.
- **`go build ./...` and test suite**: additive changes, zero-valued
  defaults preserve existing behavior, every touched test file has
  its coverage extended not broken.
- **Spawner-RPC boundary**: all Docker lifecycle flows through
  `o.Spawner.SpawnWorker` / `InspectWorker` / `DestroyWorker`.
  Orchestrator never sees `/var/run/docker.sock`. The
  `internal/container.Runtime` on the orch side is only consumed by
  `watchDockerEvents` for the read-only event subscription, which is
  the pattern the PRD explicitly permits.
- **`/bare:rw` for merger, `/bare:ro` for workers**: new
  `BareRepoReadWrite bool` field on `SpawnWorkerParams`, set `true`
  only in `dispatchMerge`; worker spawn sites in `worker_spawn.go`
  leave it zero-valued.

## Critical files for implementation

- `internal/spawner/types.go`
- `internal/spawner/methods.go`
- `internal/orchestrator/merge_dispatch.go`
- `internal/orchestrator/merge_execution.go`
- `internal/orchestrator/orchestrator.go`
