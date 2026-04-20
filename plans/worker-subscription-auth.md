# Worker Subscription-Auth — Implementation Plan

Status: **implemented, 2026-04-20.** All 7 implementation commits
(`66be7f0..3900029`) plus this plan-status update landed on
worktree-agent-a5c0c6b4. Follows the warm-planner pivot
(`plans/warm-planner-pivot.md`, commits `101f739..1f554b1`, merged
2026-04-20) and extends its subscription-only policy to every worker
role that runs the `claude` CLI harness.

Tests added:

- `internal/spawner/service_test.go` — 2 (CredsMount produces a
  read-only file mount at /home/drem/.claude/.credentials.json;
  missing host file fails SpawnWorker fast without reaching the
  runtime).
- `internal/spawner/client_test.go` — extended round-trip to carry
  CredsMount through the JSON wire format.
- `internal/agent/spawn_test.go` — extended to assert CredsMount
  propagates through Manager.Spawn into WorkerSpawnParams.
- `internal/agent/spawn_integration_test.go` — rpcAdapter carries
  CredsMount across the real JSON-RPC boundary.
- `internal/orchestrator/worker_spawn_test.go` — 6 new
  (credsMountRequired table, every claude role populates CredsMount,
  supervisor populates CredsMount, merger omits CredsMount, missing
  env + missing $HOME fails-closed with a worker_spawn_failed event,
  rejectAPIKeyInEnv table + recordSpawnFailureEventWithReason
  carries the reason classifier).
- `internal/projects/template_test.go` — 3 new
  (DREM_WORKER_CREDS_PATH is wired from HostHome, default derivation
  when caller leaves WorkerCredsPath zero, explicit override wins
  over the HostHome default).

All tests pass on `go test -count=1 ./...`; `go vet ./...` is clean.

Rollout steps (§7) remain unchanged; the T2.5 canary is operator
work post-merge.

## 0. What changes, what stays

Keep:
- Worker-clone pattern: spawner bind-mounts `/bare` RO, worker clones
  into `/home/drem/work`, watchdog pushes back. No host-side worktree.
- Worker harness selection: `DREM_AGENT_HARNESS=claude|opencode`, default
  `claude`. `worker-entrypoint.sh` execs `claude --dangerously-skip-permissions`.
- Per-language worker images (`drem-worker-go`, `drem-worker-cpp`).
  The `@anthropic-ai/claude-code` npm install stays in worker-base.
- Spawn path: `orch.spawnTypedWorker` → `spawner.Service.SpawnWorker` →
  `container.Runtime.Spawn`. Nothing about the shape of the task-→-worker
  pipeline changes.

Replace:
- **Auth path for Claude-backed workers.** Today there is no auth path —
  if a worker spawned, `claude` inside would hit an unauth'd prompt and
  exit. (Workers have not been spawned since containerization began;
  this is why the gap hasn't bitten.) Going forward: auth is subscription-
  only, via a read-only bind-mount of `~/.claude/.credentials.json` from
  host into the container. Same pattern csuite agents already use
  (`~/.drem/projects/drem-orchestrator/compose.override.yml`) and the
  planner adopted in the pivot.

Remove:
- Any lingering `ANTHROPIC_API_KEY` forwarding. Grep shows the only
  current references are negative assertions (template tests, compose
  tests) and superseded-plan prose. No runtime code actually plumbs
  the env var anywhere a worker could see it. Add a regression test
  at the orch-→-spawner boundary that rejects `ANTHROPIC_API_KEY` in
  `SpawnWorkerParams.Env` so a future change can't silently re-introduce it.

Add:
- **`SpawnWorkerParams.CredsMount string`** — a new field (host path)
  that the spawner translates into a read-only bind-mount at
  `/home/drem/.claude/.credentials.json`. Narrow, purpose-named, mirrors
  the existing `BareRepoMount`. No generic `Mounts []Mount` field;
  YAGNI until a second kind of per-worker mount shows up.
- `agent.WorkerSpawnParams.CredsMount` — mirror field in the agent
  package so `agent.Manager.Spawn` can pass it through.
- Orch wiring: `buildSpawnContext` populates `CredsMount` for every
  Claude-backed agent type (coder, reviewer, fixer, tester, supervisor).
  Merger does NOT need it — merger is a Go binary (`drem-merger`), does
  not exec claude.
- Worker-base Dockerfile: pre-create `/home/drem/.claude/` with
  `drem:drem` ownership so the bind-mount's auto-created parent doesn't
  land as root (claude CLI writes session state under `~/.claude/` and
  needs the parent writable).
- Per-project compose template: pass the host operator's creds path to
  orch via `DREM_WORKER_CREDS_PATH` env. Source of truth for the mount
  source at spawn time.
- Install docs: new "Worker subscription auth" subsection mirroring the
  planner walkthrough, plus the combined prerequisite ("one `claude
  login` on host covers csuite + planner + workers").

## 1. Subscription-auth policy (strict)

Same policy as the planner pivot (§1 of `plans/warm-planner-pivot.md`),
applied to every Claude-backed worker role:

- drem.toml `[agents.<role>]` has **no `auth` knob.** Auth is always
  subscription.
- Worker container runs as UID 1000 `drem`, home at `/home/drem`, so
  `${HOME}/.claude/.credentials.json` resolves without `CLAUDE_CONFIG_DIR`.
- Bind-mount is **read-only.** Host operator's `claude login` sessions
  own OAuth refresh; container reads each invocation. Prevents the
  container from writing refresh tokens the host then has to reconcile.
- Startup validation: spawner pre-checks `CredsMount` file exists on
  host before creating the container. If missing, SpawnWorker fails with
  a loud error identifying the missing path; orch surfaces this as a
  `worker_spawn_failed` event (already exists via `recordSpawnFailureEvent`)
  so the operator sees exactly which agent was unspawnable and why.
- Dispatch-side validation: unchanged. Workers are ephemeral per-task;
  there's no /healthz to poll. The pre-spawn file check is the whole
  validation path.
- Rate-limit implication: workers share the same Claude Max pool as
  csuite, planner, and the operator's interactive sessions. Per the
  planner plan §9.4 (~900 msgs / 5h), this matters more now. Add
  per-worker-type request counting to the existing
  `worker_spawned` event metadata so the operator can observe the
  workload pattern as workers come online.

**No fallback.** If you really want API-key access for an ad-hoc test,
set `ANTHROPIC_API_KEY` manually on the container via `docker run`. The
orch spawn path never sets it; the Dockerfile never probes for it; the
template tests forbid it in generated compose.

## 2. Architecture

```
orch tick loop
  (tasks transitioning through coding/review/fix/testing_ready)
    │
    │  spawnTypedWorker(agent_type)
    ▼
orchestrator/worker_spawn.go
    │  buildSpawnContext() populates:
    │    - Env:           DREM_TASK_ID, DREM_PROJECT, DREM_AGENT, …
    │    - BareRepoMount: /path/to/bare-repo (RO for workers, RW for merger)
    │    - CredsMount:    /home/godinj/.claude/.credentials.json  ← NEW
    │  (CredsMount empty for merger; populated for claude-harness roles)
    ▼
spawner JSON-RPC (Unix socket)
    │  SpawnWorkerParams{CredsMount: "..."}
    ▼
spawner.Service.SpawnWorker
    │  pre-checks file exists on host (fail-closed if missing)
    │  translates CredsMount into container.Mount{
    │    Source: "<host>/.claude/.credentials.json",
    │    Target: "/home/drem/.claude/.credentials.json",
    │    ReadOnly: true,
    │  }
    ▼
container.Runtime.Spawn
    │  docker creates container, parent dir /home/drem/.claude already
    │  exists (pre-created in worker-base.Dockerfile with drem:drem),
    │  bind target file materializes RO.
    ▼
worker-entrypoint.sh
    │  claude CLI starts; reads ~/.claude/.credentials.json;
    │  OAuth token present; api.anthropic.com request works;
    │  subscription pool charged.
```

One bind-mount per worker, read-only, single file (not the `.claude/`
directory). Matches the planner + csuite pattern.

## 3. Shape of the new field

### `spawner.SpawnWorkerParams`

```go
type SpawnWorkerParams struct {
    // ...existing fields...

    // CredsMount, when non-empty, is bind-mounted read-only at
    // /home/drem/.claude/.credentials.json so the worker's claude CLI
    // can read the host operator's subscription credentials. The
    // spawner validates the host path exists before creating the
    // container; missing file fails SpawnWorker fast. Leave empty for
    // agent types that do not run claude (merger).
    CredsMount string `json:"creds_mount,omitempty"`
}
```

### `agent.WorkerSpawnParams`

Mirror field. `agent.Manager.Spawn` populates from a new
`SpawnRequest.CredsMount string`, which upstream callers
(`internal/orchestrator/worker_spawn.go`) pass through.

### `spawner.Service.SpawnWorker`

Insertion point right after the existing `BareRepoMount` mount append:

```go
if p.CredsMount != "" {
    if _, err := os.Stat(p.CredsMount); err != nil {
        return SpawnWorkerResult{}, fmt.Errorf(
            "creds file not found at %s: run `claude login` on host: %w",
            p.CredsMount, err)
    }
    mounts = append(mounts, container.Mount{
        Source:   p.CredsMount,
        Target:   "/home/drem/.claude/.credentials.json",
        ReadOnly: true,
    })
}
```

No `ReadWrite` knob — workers must not be able to overwrite the host
operator's creds.

## 4. Files touched

### New files
- `plans/worker-subscription-auth.md` — this file.

### Modified files
- `internal/spawner/types.go` — add `CredsMount` field.
- `internal/spawner/methods.go::SpawnWorker` — pre-flight stat + mount
  append.
- `internal/spawner/service_test.go` — two new tests:
  (a) `CredsMount` produces a Mount in `Spec.Mounts` with correct
      source/target/readonly;
  (b) missing file fails SpawnWorker with a clear error.
- `internal/spawner/client_test.go` — extend round-trip test so the
  JSON field serializes.
- `internal/agent/spawn.go` — add `CredsMount` to `WorkerSpawnParams`,
  `SpawnRequest`; populate in `Manager.Spawn`.
- `internal/agent/spawn_integration_test.go` — adapter carries the field
  through the RPC boundary.
- `internal/agent/spawn_test.go` — fake spawner asserts `CredsMount`
  received.
- `deploy/docker/worker-base.Dockerfile` — mkdir + chown
  `/home/drem/.claude/` with drem:drem ownership. One RUN layer,
  adjacent to the existing `mkdir -p /home/drem/work`.
- `internal/orchestrator/worker_spawn.go::buildSpawnContext` — populate
  `CredsMount` for Claude-backed agent types via a small table
  (`credsMountRequired(agentType string) bool`); source path read from
  `DREM_WORKER_CREDS_PATH` env with fallback to
  `${HOME}/.claude/.credentials.json`. Host path passed to orch at
  container start — orch doesn't introspect `$HOME` (inside the
  container `$HOME` is `/root`, wrong).
- `internal/orchestrator/worker_spawn.go` — add regression check:
  reject `ANTHROPIC_API_KEY` appearing in any
  `buildSpawnContext.envVars` (paranoia; currently impossible, but
  codifies the policy at the boundary).
- `internal/orchestrator/worker_spawn_test.go` — new tests:
  (a) coder spawn carries `CredsMount` = configured host path;
  (b) merger spawn carries `CredsMount` = ""
      (merger is still dispatched via `merge_dispatch.go`, not the
      typed-worker path; ensure that path also does NOT set CredsMount);
  (c) env map never contains `ANTHROPIC_API_KEY`;
  (d) missing `DREM_WORKER_CREDS_PATH` env + missing default path fails
      spawn with a clear error (fail-closed, not silent-empty).
- `internal/orchestrator/merge_dispatch.go` — explicit comment that
  merger does NOT set `CredsMount` (Go binary, no claude CLI). No code
  change; prose only.
- `internal/projects/templates/project-compose.yml.tmpl` — add
  `DREM_WORKER_CREDS_PATH: "{{.WorkerCredsPath}}"` to orch env, default
  rendered as `{{.HostHome}}/.claude/.credentials.json`.
- `internal/projects/template.go` — add `WorkerCredsPath` and
  `HostHome` fields to the template struct; populate from
  `os.UserHomeDir()` at render time.
- `internal/projects/template_test.go` — assert `DREM_WORKER_CREDS_PATH`
  appears in generated compose with a non-empty value; assert the
  negative ANTHROPIC_API_KEY check still passes (unchanged).
- `internal/projects/templates/project-drem.toml.tmpl` — comment block
  noting subscription-auth policy covers workers in addition to planner.
  No new TOML keys.
- `docs/containerization/install.md` — new subsection "Worker
  subscription auth" alongside the existing planner one, or fold the
  two under a unified "Claude subscription auth" parent (recommended:
  unified; prerequisites are identical).
- `plans/containerization.md` — tick a new acceptance-criteria row
  under Phase 6 or Phase 8 documenting the worker auth path.

## 5. Agent-type → CredsMount-required table

Defined in `internal/orchestrator/worker_spawn.go`:

```go
// credsMountRequired reports whether the agent_type runs the claude CLI
// harness inside the container and therefore needs the subscription
// credentials file bind-mounted. Roles not in this set (notably merger,
// which is a Go binary) get an empty CredsMount and the spawner omits
// the mount entry.
func credsMountRequired(agentType string) bool {
    switch agentType {
    case string(model.AgentCoder),
        string(model.AgentReviewer),
        string(model.AgentFixer),
        string(model.AgentTester),
        "supervisor":
        return true
    case string(model.AgentMerger):
        return false
    }
    // Default fail-closed: unknown agent types do not get creds
    // (prevents accidental leaks when a future type is added without
    // reviewing auth).
    return false
}
```

A new agent type (e.g. future `researcher`) requires a deliberate
addition to the switch AND a test covering it. This is the auth
equivalent of the deny-by-default policy.

## 6. Commit sequence

1. **`feat(spawner): add CredsMount to SpawnWorkerParams`** —
   wire-type extension, stat-check in `SpawnWorker`, mount append,
   tests. Classifier/planner unaffected (they're not spawner-driven
   paths). Zero behavior change for merger (CredsMount empty).
2. **`feat(agent): propagate CredsMount through agent package mirror`** —
   `WorkerSpawnParams.CredsMount`, `SpawnRequest.CredsMount`,
   `Manager.Spawn` pass-through. Adapter + integration tests.
3. **`feat(worker): pre-create /home/drem/.claude with drem ownership`** —
   one-line `mkdir -p && chown` in worker-base.Dockerfile, adjacent to
   the existing `/home/drem/work` setup. No runtime code change.
4. **`feat(orch): populate CredsMount for claude-backed workers`** —
   `credsMountRequired` table, `buildSpawnContext` populates
   `CredsMount`, env-var-driven host path (`DREM_WORKER_CREDS_PATH`),
   fail-closed when required + missing. Tests cover every agent type
   in the table plus the unknown-type default-deny case.
5. **`refactor(orch): reject ANTHROPIC_API_KEY in worker env at spawn`** —
   defensive check in `spawnTypedWorker` (and symmetrically in
   `dispatchMerge`). Emits a `worker_spawn_failed` event with reason
   `policy_violation_api_key` and refuses to spawn. No current caller
   sets this env; the check codifies the policy so a future one can't
   regress it silently.
6. **`feat(projects): pass worker creds path through compose template`** —
   `WorkerCredsPath` template field, `DREM_WORKER_CREDS_PATH` env on
   orch, rendered from host `$HOME`. Template + compose smoke tests.
7. **`docs(containerization): unify claude subscription auth walkthrough`** —
   install.md restructure: one "Claude subscription auth" section
   covering csuite + planner + workers. Single prerequisite
   (`claude login` once), three bind-mounts documented. Also note
   which roles do NOT get creds (merger) and why.
8. **`docs(plans): mark worker-subscription-auth done + tick containerization`** —
   update this plan's status header; tick the relevant
   `plans/containerization.md` acceptance row.

~8 commits, ~500-700 LOC net (most of it tests + doc). ~1-1.5 days
focused.

## 7. Rollout

1. Land commits 1-7 serially. Each commit builds + tests clean on its
   own — commit 1 ships `CredsMount` plumbing with no caller, commits
   2-3 prepare the caller + container, commit 4 wires it, commit 5
   locks the policy, commit 6 hooks up the template, commit 7 documents.
2. Rebuild + push:
   - `drem-worker-base:latest` (commit 3 changes the Dockerfile).
   - `drem-worker-go:latest`, `drem-worker-cpp:latest` (rebuilt on top
     of the new base).
   - `drem-orch:latest` (commits 1, 2, 4, 5 touch orch code).
3. Re-register project:
   `drem project register --update drem-orchestrator` regenerates the
   per-project compose + drem.toml with the new
   `DREM_WORKER_CREDS_PATH` env.
4. Restart orch: `docker compose -f ~/.drem/projects/drem-orchestrator/compose.yml up -d orch`.
   Picks up the new env + updated image.
5. **T2.5 canary** (the whole point of this plan):
   - Insert a task in `plan_review` and manually advance it to `coding`
     (or unfreeze the `plan_review` gate briefly for one task; restore
     freeze afterward).
   - Watch the orch spawn a coder worker. Expect:
     - `worker_spawned` event with `agent_type=coder`, non-empty
       `container_id`.
     - `docker inspect <container-id>` shows the creds mount read-only
       at `/home/drem/.claude/.credentials.json`.
     - Worker container's `docker logs` shows `claude` starting and
       making an Anthropic API call successfully (not the interactive
       login prompt).
     - Subscription pool ticks (observable via the claude CLI's own
       usage surface on host, or by counting Anthropic 200s in
       container logs).
   - Tear down the worker via `DestroyWorker` once the canary proves
     auth works. Don't let it run a real coding task yet — that's
     outside this plan's scope (next plan: worker prompt delivery +
     harness wiring for the real coding loop).
6. Park. T3 blockers (gate unfreezes, worker prompt delivery, etc.) are
   separate plans.

**T2.5 canary timeline:** ~1-2 hrs of ops work once commits land,
assuming the images build clean and the `plan_review` unfreeze
procedure is straightforward (it's a DB update; see §"Force-cancel /
DB-write procedure" in the restart context).

## 8. Open questions

1. **What about `opencode` harness?** `worker-entrypoint.sh` supports
   `DREM_AGENT_HARNESS=opencode` as an alternate. Opencode uses a
   different auth story (env-var API key to the upstream provider).
   Out of scope for this plan — opencode is not the default, and no
   current agent-type defaults to it. When/if an opencode-driven role
   comes online, file a sibling `plans/worker-opencode-auth.md`.

2. **`/home/drem/.claude/` permissions when docker auto-creates the
   parent.** If commit 3's `mkdir -p && chown` in worker-base is skipped
   or if a descendant image (cpp, go) USER-switches before the chown
   runs, docker may auto-create the parent as root during bind mount,
   blocking claude's own writes under `~/.claude/`. Verify at the T2.5
   canary by `exec`ing into a running worker and `ls -la /home/drem/.claude/`.
   If the parent is root-owned, revisit commit 3 — most likely fix is
   to move the `mkdir` ABOVE the first `USER drem` in worker-base.

3. **Credentials file rotation.** `claude login` on host refreshes the
   file in-place. The RO bind-mount is live — next worker spawn picks up
   the refreshed file naturally. But an ALREADY-RUNNING worker would
   have the old file open; if the refresh overwrites via
   `rename(2)` (atomic), the running worker keeps the old inode and
   eventually hits a 401 on token expiry. Current worker lifecycle is
   short (per-task, minutes to low-hours), so this is unlikely to bite.
   Document in install.md as a known limitation. A real fix would be
   the worker re-opens the file on each claude invocation — which is
   the CLI's default behavior anyway. So this is more of a worry than a
   bug. Note it; don't fix it preemptively.

4. **Shared rate-limit pool stress.** Once coder/reviewer/fixer/tester
   all can spawn with creds, a busy day could fire N×4 claude calls.
   Claude Max gives ~900 msgs / 5h. Add a metric
   `drem_worker_claude_spawns_total{agent_type}` to the
   `worker_spawned` event stream, and a derived alert in the Grafana
   dashboard at >500 spawns/hr. Not a blocker — observability punt is
   fine.

5. **Spawner's host-path stat check races with rotation.** Between the
   stat and the container's `open(2)`, the host could be in the middle
   of a `claude login` that truncates the file. Window is microseconds;
   the docker daemon re-reads the file at bind-mount time. Very small
   blast radius (one failed /plan or /coder spawn; retries work). Don't
   engineer around it.

6. **DREM_WORKER_CREDS_PATH semantics when orch runs across hosts.**
   Currently orch is single-host; the env var is a host path passed
   through compose. If orch ever moves to a different host than the
   operator's `claude login`, the pattern breaks — the path must point
   to a file docker can read, which means a host-local file. Fine for
   now (single operator, single host per the standing instructions).
   Revisit when/if multi-host becomes real.

## 9. What this doesn't solve

- Gate freezes (`plan_review`, `test_review`, `testing_ready`, `paused`)
  remain. T2.5 canary requires manually unfreezing one task briefly.
- ~~Worker prompt delivery — `DREM_PROMPT_PATH` exists but the wiring
  from orch plan → prompt file on host is a separate plan. This plan
  proves auth works; the next plan proves coding works.~~ **Done**
  2026-04-20 in `plans/worker-prompt-delivery.md` (commits
  `4b6f1f3..17b2523`).
- Reviewer/fixer/tester harness specifics (what prompt, what tools,
  what success criteria) — separate plans per role.
- Rate-limit governance beyond observability (back-pressure, budget
  caps, per-project quotas) — file when observed pressure warrants.
- Opencode alternate harness — see §8.1.

## 10. Related plans

- `plans/warm-planner-pivot.md` — the auth policy originated here; worker
  auth is a direct extension.
- `plans/containerization.md` — acceptance criteria referenced in §4.
- `plans/warm-direct-classifier.md` — classifier is sglang-backed, no
  Claude auth involved; listed for completeness.
- `plans/merger-spawn-on-demand-impl.md` — merger explicitly opts OUT
  of CredsMount; this plan documents the reason.
- `plans/warm-direct-prep.md` — prep agent, TBD whether it runs the
  claude harness; if yes, it joins the `credsMountRequired` table
  when that plan lands.
