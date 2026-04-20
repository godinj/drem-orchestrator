# Worker Prompt Delivery — Implementation Plan

Status: **implemented, 2026-04-20.** All 6 implementation commits
(`4b6f1f3..17b2523`) plus this plan-status update landed on
worktree-agent-a5fb3e7b. Sibling plan to
`plans/worker-subscription-auth.md` (commits `66be7f0..4f3f9fc`, landed
earlier today); called out as a follow-up in that plan's §9. Unblocks
the T3 canary — the first real coder task driven end-to-end through
the containerized worker path.

Tests added:

- `internal/spawner/service_test.go` — 3 new
  (PromptMount produces a read-only file mount + sets DREM_PROMPT_PATH
  deterministically in env; missing host file fails SpawnWorker fast;
  caller-set DREM_PROMPT_PATH in Env is overwritten by the canonical
  target).
- `internal/spawner/client_test.go` — extended round-trip carries
  PromptMount through the JSON wire format.
- `internal/agent/spawn_test.go` — extended to assert PromptMount
  propagates through Manager.Spawn into WorkerSpawnParams.
- `internal/agent/spawn_integration_test.go` — rpcAdapter carries
  PromptMount across the real JSON-RPC boundary.
- `internal/orchestrator/worker_spawn_test.go` — 3 new
  (promptRequired table, spawnCoder writes the prompt file before the
  spawner is called and leaves no tmp residue, missing prompt root
  fails closed with reason=prompt_render_failed).
- `internal/orchestrator/merge_dispatch_test.go` — 1 new
  (merger dispatch omits both CredsMount and PromptMount).
- `internal/projects/template_test.go` — 3 new
  (DREM_PROMPT_ROOT_HOST wired on orch env + rw bind-mount at
  host-identical target; default derivation from HostHome +
  ProjectName when caller leaves WorkerPromptRoot zero; explicit
  override wins over the default).

All tests pass on `go test -count=1 ./...`; `go vet ./...` is clean.

Rollout steps (§7) remain unchanged; the T3 canary is operator work
post-merge.

## 0. What changes, what stays

Keep:
- Worker-clone pattern, harness selection, per-language images, spawn
  path (orch → spawner → runtime). Untouched by this plan.
- The `worker-entrypoint.sh` prompt contract already in place:
  `deploy/docker/context/worker-entrypoint.sh:132-160` reads
  `DREM_PROMPT_PATH` and execs `claude --dangerously-skip-permissions
  "$(cat "${DREM_PROMPT_PATH}")"` when the file exists. No change to
  the consumer side; this plan is entirely producer.
- Subscription-only auth for Claude-backed roles (`CredsMount` /
  `credsMountRequired` / ANTHROPIC_API_KEY regression guard). This
  plan inherits the same role table without modification.

Replace:
- **Prompt delivery.** Today there is no delivery path. Containerized
  workers spawn, `DREM_PROMPT_PATH` is unset (or a stale leftover
  from nothing), and the entrypoint falls through to interactive
  claude — which has no TTY, no user, and exits. The legacy host
  runner in `internal/agent/runner_start.go:29-30` writes
  `.claude/agent-prompt.md` inside the host-side feature worktree and
  passes it positionally to `claude`; the containerized path has no
  equivalent yet.

Remove:
- Nothing. The legacy host runner's prompt-writing code (in
  `runner_start.go` and `session_spawning.go`) stays in place for
  the duration — it remains the only path used by csuite agents and
  the supervisor-via-host-tmux fallback, neither of which this plan
  migrates.

Add:
- **`SpawnWorkerParams.PromptMount string`** — a new field (host file
  path) that the spawner translates into a read-only bind-mount at
  `/home/drem/.drem/prompt.md` inside the worker, with
  `DREM_PROMPT_PATH=/home/drem/.drem/prompt.md` injected into the
  worker's env automatically. Narrow, purpose-named, mirrors
  `CredsMount`. No generic `Mounts []Mount` field — same YAGNI line
  worker-subscription-auth drew.
- `agent.WorkerSpawnParams.PromptMount` + `SpawnRequest.PromptPath`
  wire-through (the `PromptPath` field already exists on
  `SpawnRequest` — this plan actually connects it).
- Orch wiring: `buildSpawnContext` renders the prompt via
  `internal/prompt.Generate`, writes it atomically under
  `~/.drem/projects/<project>/prompts/<task-id>.md` on host,
  populates `PromptMount` with the absolute host path. Policy table
  `promptRequired(agentType)` mirrors `credsMountRequired` — every
  claude-backed role gets a prompt; merger does not.
- Per-project compose template: pass the host prompt-root to orch via
  `DREM_PROMPT_ROOT_HOST` env so orch's write path lands on a path the
  spawner (running in its own container) can bind-mount back in.
- Worker-base Dockerfile: pre-create `/home/drem/.drem/` with
  `drem:drem` ownership so the read-only bind target's parent exists.
- Install docs: new "Worker prompt delivery" subsection next to the
  subscription-auth one, walking the operator through the end-to-end
  path (host write → bind-mount → entrypoint read).

## 1. Prompt delivery policy

- drem.toml has **no new knobs.** Prompt delivery is mandatory for
  every claude-backed worker role; the policy is not per-project.
- Worker entrypoint contract is **unchanged**: when `DREM_PROMPT_PATH`
  is set AND the file exists, claude runs with the prompt as its
  positional arg; otherwise falls through (currently: interactive,
  which exits fast in a TTY-less container). The container-side
  guarantee flips from "prompt-path happens to exist" to "prompt
  always present for claude-backed roles".
- Bind-mount is **read-only.** The worker must not be able to
  overwrite its own prompt. If the worker's process wants scratch
  space it writes under `/home/drem/work/` (already read-write; the
  git clone lives there).
- Startup validation: spawner pre-checks the host prompt file exists
  before creating the container (same `os.Stat(2)` pattern as
  `CredsMount`). Missing file fails `SpawnWorker` fast with a loud
  error; orch surfaces this as a `worker_spawn_failed` event with
  reason `prompt_missing`.
- Dispatch-side validation: unchanged. Ephemeral workers have no
  /healthz; the pre-spawn stat is the whole validation path.
- Atomicity: orch writes to a temp file in the same directory and
  `rename(2)`s into place. The worker container cannot start before
  rename returns — spawn call is a synchronous follow-up on the same
  goroutine.

**No fallback.** If the render step fails (nil task, nil project,
prompt.Generate returns empty, write fails), the spawn fails closed
with a `worker_spawn_failed` event. The worker never starts against
a blank prompt.

## 2. Architecture

```
orch tick loop (task transitions to coding / review / fix / testing_ready)
    │
    │  spawnTypedWorker(agent_type)
    ▼
orchestrator/worker_spawn.go
    │  buildSpawnContext() now ALSO:
    │    - calls prompt.Generate(Opts{Task, Project, AgentType, …})
    │    - writes rendered markdown to
    │      <promptRootHost>/<task-id>.md atomically (tmp + rename)
    │    - populates swc.promptMount = that host path
    │
    │  spawnTypedWorker builds SpawnWorkerParams:
    │    - Env:           DREM_TASK_ID, DREM_PROJECT, … (unchanged)
    │    - BareRepoMount: /path/to/bare-repo (unchanged)
    │    - CredsMount:    /home/op/.claude/.credentials.json (unchanged)
    │    - PromptMount:   <promptRootHost>/<task-id>.md   ← NEW
    ▼
spawner JSON-RPC (Unix socket)
    │  SpawnWorkerParams{PromptMount: "..."}
    ▼
spawner.Service.SpawnWorker
    │  pre-stat(PromptMount); fail-closed if missing.
    │  append container.Mount{
    │    Source: PromptMount,
    │    Target: "/home/drem/.drem/prompt.md",
    │    ReadOnly: true,
    │  }
    │  overwrite Spec.Env["DREM_PROMPT_PATH"] = "/home/drem/.drem/prompt.md"
    │  (set deterministically by the spawner so callers can't regress it).
    ▼
container.Runtime.Spawn
    │  parent dir /home/drem/.drem already exists (pre-created in
    │  worker-base.Dockerfile), bind target file materializes RO.
    ▼
worker-entrypoint.sh
    │  DREM_PROMPT_PATH=/home/drem/.drem/prompt.md, file exists,
    │  harness=claude → exec claude --dangerously-skip-permissions
    │  "$(cat /home/drem/.drem/prompt.md)".
    │  Agent session starts against the rendered prompt.
```

Host filesystem layout:

```
$HOME/.drem/projects/<project>/prompts/
    └── <task-uuid>.md            # one file per task
```

The `prompts/` dir is already created by `Manager.PromptDir`
(internal/agent/spawn.go:207); this plan reuses that helper so the
layout agrees with the legacy-runner expectation.

## 3. Shape of the new field

### `spawner.SpawnWorkerParams`

```go
type SpawnWorkerParams struct {
    // ...existing fields (Project/AgentType/WorkerID/Branch/Labels/Image/
    //    Env/BareRepoMount/BareRepoReadWrite/Cmd/CredsMount)...

    // PromptMount, when non-empty, is a host file path bind-mounted
    // read-only into the worker at /home/drem/.drem/prompt.md. The
    // spawner also sets DREM_PROMPT_PATH=/home/drem/.drem/prompt.md in
    // the worker's env so the entrypoint's `claude -p` path finds it
    // without the caller having to plumb the env key explicitly. The
    // spawner validates the host path exists before creating the
    // container; missing file fails SpawnWorker fast. Leave empty for
    // agent types that do not run claude (merger).
    PromptMount string `json:"prompt_mount,omitempty"`
}
```

### `agent.WorkerSpawnParams`

Mirror field. `agent.Manager.Spawn` populates
`WorkerSpawnParams.PromptMount` from the existing
`SpawnRequest.PromptPath` (already on the struct, currently
unwired — this plan connects it). The agent package keeps a single
field name on both sides.

### `spawner.Service.SpawnWorker`

Insertion point right after the existing `CredsMount` block:

```go
if p.PromptMount != "" {
    if _, err := os.Stat(p.PromptMount); err != nil {
        return SpawnWorkerResult{}, fmt.Errorf(
            "prompt file not found at %s: orch must write the prompt "+
                "before SpawnWorker: %w", p.PromptMount, err)
    }
    mounts = append(mounts, container.Mount{
        Source:   p.PromptMount,
        Target:   promptMountPath,
        ReadOnly: true,
    })
    // DREM_PROMPT_PATH is deterministic — overwrite any caller-set
    // value so the container-side path always agrees with the mount
    // target. The caller cannot regress this via Env map collisions.
    if env == nil { env = map[string]string{} }
    env[workerPromptPathEnv] = promptMountPath
}
```

No `ReadWrite` knob — workers must not overwrite their own prompt.
No trailing cleanup — the prompt is a host artifact the operator can
inspect for post-mortem; §8.3 discusses GC.

## 4. Files touched

### New files
- `plans/worker-prompt-delivery.md` — this file.

### Modified files
- `internal/spawner/types.go` — add `PromptMount` field + godoc.
- `internal/spawner/methods.go` — pre-flight stat, mount append, env
  injection. Introduces `promptMountPath` + `workerPromptPathEnv`
  constants alongside `credsMountPath`.
- `internal/spawner/service_test.go` — three new tests:
  (a) `PromptMount` produces a read-only mount at
      `/home/drem/.drem/prompt.md` AND sets
      `DREM_PROMPT_PATH=/home/drem/.drem/prompt.md` in the Spec env;
  (b) missing host file fails `SpawnWorker` with a clear error;
  (c) caller-set `DREM_PROMPT_PATH` in `Env` is overwritten by the
      spawner's deterministic value (no trust placed in caller env).
- `internal/spawner/client_test.go` — extend JSON round-trip test so
  `PromptMount` survives the wire format.
- `internal/agent/spawn.go` — add `PromptMount` to
  `WorkerSpawnParams`; populate from `SpawnRequest.PromptPath` in
  `Manager.Spawn`.
- `internal/agent/spawn_test.go` — extend fake-spawner assertion to
  cover `PromptMount`.
- `internal/agent/spawn_integration_test.go` — rpcAdapter carries
  `PromptMount` across the RPC boundary.
- `internal/orchestrator/worker_spawn.go` —
  - `promptRequired(agentType string) bool` table mirroring
    `credsMountRequired` (same roles; merger excluded).
  - `resolveWorkerPromptRoot()` — returns `DREM_PROMPT_ROOT_HOST` env
    with fallback to `$HOME/.drem/projects/<project>/prompts`.
  - `renderAndWritePrompt(task, project, agentType) (string, error)`
    — calls `prompt.Generate`, writes to `<root>/<task-uuid>.md`
    atomically (tmp + rename), returns the absolute host path.
  - `buildSpawnContext` populates `swc.promptMount` for every role in
    the `promptRequired` table. Fail-closed when required but render
    fails: emit `worker_spawn_failed` with
    `reason=prompt_render_failed`.
- `internal/orchestrator/worker_spawn_test.go` — 5 new tests:
  (a) `promptRequired` table covers every claude-backed role + merger;
  (b) `spawnCoder` writes the prompt file before calling SpawnWorker
      (assertion: file exists on disk with non-empty content at the
      `PromptMount` path in the spawner's observed call);
  (c) `spawnTypedWorker` sets `PromptMount` to the resolved host
      path for every claude role;
  (d) merger dispatch does NOT set `PromptMount`
      (assertion reads `merge_dispatch.go`-produced SpawnWorkerParams);
  (e) missing `$HOME` + missing `DREM_PROMPT_ROOT_HOST` fails closed
      with a `worker_spawn_failed` event.
- `internal/orchestrator/merge_dispatch.go` — explicit code comment
  that merger does not set `PromptMount` (Go binary). No code change;
  the merger already leaves the field at its zero value by omission.
- `internal/projects/templates/project-compose.yml.tmpl` — add
  `DREM_PROMPT_ROOT_HOST: "{{.WorkerPromptRoot}}"` under `orch.env`
  AND add a `promptRoot` bind-mount entry `-
  {{.WorkerPromptRoot}}:{{.WorkerPromptRoot}}:rw` so the orch
  container can WRITE to that host path (host-identical path, matching
  the bare-repo pattern already in the template). The spawner
  bind-mounts the same host path read-only into the worker at spawn
  time.
- `internal/projects/template.go` — add `WorkerPromptRoot` field to
  `TemplateData`; populate from
  `filepath.Join(HostHome, ".drem", "projects", ProjectName, "prompts")`
  when empty. Also create the directory on disk during
  `WriteProjectComposeAt` (best-effort `os.MkdirAll`) so a fresh
  `drem project register` has a working destination before the first
  spawn.
- `internal/projects/template_test.go` — 3 new tests:
  (a) `DREM_PROMPT_ROOT_HOST` appears in orch env with a non-empty
      value derived from HostHome + ProjectName;
  (b) the prompts dir is bind-mounted on orch rw;
  (c) explicit `WorkerPromptRoot` override wins over the derived
      default.
- `deploy/docker/worker-base.Dockerfile` — `mkdir -p
  /home/drem/.drem && chown drem:drem /home/drem/.drem` adjacent to
  the existing `/home/drem/.claude` setup from worker-subscription-auth.
- `docs/containerization/install.md` — new subsection "Worker prompt
  delivery" under Step 7, tracing the host write → bind-mount →
  entrypoint read loop. Include debug tips (`ls
  ~/.drem/projects/<p>/prompts/`, `docker exec <worker> cat
  /home/drem/.drem/prompt.md`).
- `plans/containerization.md` — tick a new acceptance-criteria row
  under Phase 6 documenting the worker prompt path.
- `plans/worker-subscription-auth.md:446` — strike the follow-up TODO.

## 5. Agent-type → prompt-required table

Defined in `internal/orchestrator/worker_spawn.go`:

```go
// promptRequired reports whether the agent_type runs the claude CLI
// in a mode that needs a prompt file. Every role that is claude-backed
// (same set as credsMountRequired) gets a prompt; merger, a Go binary,
// does not. The table is separate from credsMountRequired only so
// future roles can opt out of one or the other independently — in
// practice the two will stay in lock-step for Claude-harness agents.
func promptRequired(agentType string) bool {
    switch agentType {
    case string(model.AgentCoder),
        string(model.AgentReviewer),
        string(model.AgentFixer),
        "tester",
        "supervisor":
        return true
    case "merger":
        return false
    }
    return false  // unknown types fail closed — no prompt, no spawn (combined w/ credsMountRequired)
}
```

A new agent type must add an entry here AND a test, same policy
discipline as `credsMountRequired`.

## 6. Commit sequence

1. **`feat(spawner): add PromptMount to SpawnWorkerParams`** — wire
   type extension, stat-check in `SpawnWorker`, mount append, env
   injection, tests. Zero behavior change for any current caller
   (all leave `PromptMount` empty until commit 4).
2. **`feat(agent): propagate PromptMount through agent mirror`** —
   `WorkerSpawnParams.PromptMount`, populate from
   `SpawnRequest.PromptPath` in `Manager.Spawn`. Adapter +
   integration tests.
3. **`feat(worker): pre-create /home/drem/.drem with drem ownership`** —
   one-line `mkdir -p && chown` in `worker-base.Dockerfile`, adjacent
   to the existing `/home/drem/.claude` setup. No runtime code change.
4. **`feat(orch): render and mount worker prompts`** —
   `promptRequired` table, `renderAndWritePrompt` helper,
   `buildSpawnContext` populates `promptMount`, env-var-driven host
   root (`DREM_PROMPT_ROOT_HOST`), fail-closed when required but
   render fails. Tests cover every agent type + missing-root fallback.
5. **`feat(projects): pass worker prompt root through compose template`** —
   `WorkerPromptRoot` template field, `DREM_PROMPT_ROOT_HOST` env on
   orch, bind-mount entry for orch, directory pre-creation in
   `WriteProjectComposeAt`. Template + compose smoke tests.
6. **`docs(containerization): document worker prompt delivery`** —
   new "Worker prompt delivery" subsection in `install.md` next to
   the subscription-auth one; debug runbook + the rotation / GC
   caveat.
7. **`docs(plans): mark worker-prompt-delivery done + tick
   containerization`** — update this plan's status header; tick the
   relevant `plans/containerization.md` row; strike
   `plans/worker-subscription-auth.md:446`.

~7 commits, ~400-600 LOC net (tests + docs dominate). ~1 day focused.

## 7. Rollout

1. Land commits 1-7 serially. Each commit builds + tests clean on its
   own — commit 1 ships `PromptMount` plumbing with no caller, commits
   2-3 prepare caller + container, commit 4 wires it, commit 5 hooks
   up the template, commit 6 documents.
2. Rebuild + push (operator):
   - `drem-worker-base:latest` (commit 3 changes the Dockerfile).
   - `drem-worker-go:latest`, `drem-worker-cpp:latest` (rebuilt on top).
   - `drem-orch:latest` (commits 1-2, 4 touch orch code).
3. Re-register project:
   `drem project register --update drem-orchestrator` regenerates the
   per-project compose + drem.toml with the new `DREM_PROMPT_ROOT_HOST`
   env and the prompts bind-mount.
4. Restart orch: `docker compose -f
   ~/.drem/projects/drem-orchestrator/compose.yml up -d orch`. Picks
   up the new env + updated image.
5. **T3 canary** (the whole point):
   - Insert a task in `plan_review` and manually advance it to
     `coding` (DB update per the restart-context procedure).
   - Watch the orch spawn a coder worker. Expect:
     - `~/.drem/projects/drem-orchestrator/prompts/<task-id>.md` appears
       on host before `worker_spawned` fires (inotify / `ls -la`
       check).
     - `worker_spawned` event with `agent_type=coder`, non-empty
       `container_id`.
     - `docker inspect <container-id>` shows the prompt mount
       read-only at `/home/drem/.drem/prompt.md`.
     - `docker logs <container-id>` shows the entrypoint log line
       `execing claude with prompt from /home/drem/.drem/prompt.md`.
     - Claude session starts, makes tool calls against the worktree,
       and eventually commits + pushes via the watchdog.
   - Tear down the worker via `DestroyWorker` once the canary proves
     prompt delivery works. Observe the prompt file persists on host
     (artifact for post-mortem).
6. Park. T3 completion requires more pieces (test execution wiring,
   review / fix loop end-to-end, merger conflict UX) — those are
   separate plans.

**T3 canary timeline:** ~1-2 hrs of ops work once commits land,
assuming clean image rebuilds. Gate unfreezes are the same DB-write
procedure used for the T2.5 canary.

## 8. Open questions (decided in this plan)

1. **Where does the prompt live on the host?** —
   `$HOME/.drem/projects/<project>/prompts/<task-uuid>.md`. Per-task,
   per-project. Matches the `Manager.PromptDir` helper already in
   `internal/agent/spawn.go`. Alternatives considered:
   - Per-subtask path (`/<task>/<subtask-idx>.md`): rejected because
     current worker spawn is one-per-task, not one-per-subtask (see
     §8.4). A subtask's prompt content is baked into the task's
     prompt via `task.Context["current_subtask"]` or similar; the
     file-level identity stays at task-uuid.
   - Volume-based (`drem-<project>-prompts`): rejected because the
     spawner (separate container) needs to bind-mount the file into
     a worker, which means it has to be a host-visible path, which
     rules out a named docker volume.

2. **Does merger need prompts?** — No. Merger is a Go binary
   (`cmd/drem-merger`), takes argv flags, does not run the claude
   CLI. `promptRequired("merger") == false`, same as
   `credsMountRequired`. The comment in `merge_dispatch.go`
   explicitly notes this (matches the subscription-auth comment
   commit 4f3f9fc already added there).

3. **What's the prompt content?** — The same markdown
   `internal/prompt.Generate(Opts)` produces for the legacy host
   runner. Single source of truth for what an agent sees. Opts
   populated from `(task, project, agentType, worktreePath=the
   host-identical bare path which becomes `/home/op/git/<repo>.git`
   on both sides via the existing bare-repo bind-mount trick)`.
   Rendering is pure — no fs access required at render time — so
   we render in-process in orch and write the result atomically.
   Plan JSON and subtask indexing flow through the prompt
   generator's existing Opts hooks (`Task.Plan`, `Task.Context`),
   not via a second file.

4. **Multi-subtask tasks: one prompt or per-subtask?** — One prompt
   per task, regenerated per spawn. Worker-spawn granularity is
   per-task today; changing that is out-of-scope. The prompt rendering
   pipeline already handles per-phase variation (test vs
   implementation) via `Task.Phase`. A future
   plans/worker-per-subtask-spawns.md can revisit file-level identity.

5. **Cleanup: when/who deletes old prompts?** — Keep as artifact.
   Writer (orch) never deletes. The operator can `rm -rf
   ~/.drem/projects/*/prompts/` between canary runs if they want a
   clean slate; a future GC plan can wire this to
   `task_terminal_transition` events if it becomes load-bearing. The
   prompt files are small (<100KB) and tasks finish on the order of
   1-100/day, so size pressure is not a concern in the T3 horizon.
   The `.md` files are also useful post-mortem evidence for any
   agent whose session fails — deleting them eagerly would throw
   away debugging context.

6. **Policy table: reuse or add?** — Add. `promptRequired` is
   nominally the same set as `credsMountRequired` today (every
   claude role needs both), but the two concerns are independent:
   a future opencode-harness role might need a prompt without
   subscription creds, or a future `prep` worker role might need
   creds but take its input via argv. Keeping the tables distinct
   lets each policy evolve without the other dragging it along.

## 9. Observability punt

Telemetry additions out of scope for this plan. If we want numbers
later:
- `drem_worker_prompt_bytes{agent_type}` histogram on each render
  (cheap, rendered size already known).
- `drem_worker_prompt_render_duration_ms{agent_type}` histogram.
- `worker_spawned` event detail gains `prompt_size_bytes` + `prompt_path`
  keys.

Punt until the T3 canary has produced enough data to know whether
prompt-size distribution matters (e.g. hitting claude context
window caps). The prompt-render path is deterministic-ish; adding
counters adds churn without a current consumer.

## 10. Follow-ups / what this doesn't solve

- **Per-subtask spawn granularity.** One task → one worker → one
  prompt. Mixing test-phase + impl-phase into a single prompt is
  fine because only one is active at a time per spawn, but future
  work that truly parallelizes subtasks will need to revisit.
- **Prompt rotation mid-session.** If orch regenerates a prompt
  after the worker has started, the worker still holds the old
  inode. Acceptable — claude CLI reads the prompt once at exec
  time and doesn't poll. A genuine "prompt update mid-task" flow
  would require IPC back to the watchdog, which is a separate plan.
- **Prompt-content reviewer / fixer / tester nuance.** The current
  `prompt.Generate` dispatcher has reviewer and fixer branches;
  tester has a placeholder-default branch. This plan delivers
  whatever Generate produces without adding new branches. Per-role
  prompt quality is a separate iteration (probably one plan per
  role, matching the subscription-auth §9.3 pattern).
- **Opencode harness prompt.** `worker-entrypoint.sh:149-159`
  already reads `DREM_PROMPT_PATH` for the opencode path and
  errors when unset. This plan's producer side works for opencode
  workers too (any role that lands in `promptRequired` gets a
  prompt); opencode-specific harness wiring stays in its own plan.
- **Prompt GC.** §8.5. File if observed pressure warrants.
- **Multi-host operation.** §8.1's conclusion — host-local path —
  rules out multi-host orch. If that changes, the prompt root
  becomes a shared volume or object store, not a host path. Not
  a near-term concern.

## 11. Related plans

- `plans/worker-subscription-auth.md` — the layering template and
  the source of the `credsMountRequired` policy pattern this plan
  mirrors. Also the plan that called out prompt delivery as a TODO
  (§9.2).
- `plans/containerization.md` — acceptance criteria referenced in §4.
- `plans/merger-spawn-on-demand-impl.md` — merger explicitly opts
  OUT of `promptRequired`; this plan documents the reason.
- `plans/warm-planner-pivot.md` — planner takes its input over HTTP
  not via a file. This plan does NOT change that; the producer side
  covered here is only for ephemeral workers spawned through the
  spawner.
