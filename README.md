# Drem Orchestrator

Drem Orchestrator is a local system for coordinating AI coding agents across software projects.

In plain English: you give Drem a project and a goal, and it helps turn that goal into planned work, agent tasks, review steps, merges, and status reports. It is built for a solo developer who wants several AI agents working in parallel without losing track of what each agent is doing.

## Who This Is For

Drem is useful if you want:

- One place to see what your AI coding agents are doing.
- Safer parallel work on the same codebase.
- Repeatable agent environments instead of relying on whatever is installed on the host machine.
- A clear record of tasks, plans, worker runs, logs, failures, and merges.
- A way for helper personas such as Kyle, Mike, Alex, and Seth to inspect project state without guessing from scattered terminal panes.

You do not need to understand every internal service to understand the project. At a high level, Drem is a control room for AI-assisted software work.

## Current State

The repository has moved from the older tmux-and-host-worktree design to a containerized design.

What is current today:

- The main program is written in Go.
- Long-running infrastructure runs in Docker containers.
- Worker agents run in containers and use branches in a bare Git repository as the source of truth.
- The TUI reads from the orchestrator HTTP API instead of directly owning the whole world.
- Per-project setup is managed through `drem project ...` commands and files under `~/.drem/projects/`.
- Global shared services include a local image registry, SGLang, GQ, a spawner, agentmon, and Kyle.
- Per-project services include an orchestrator, a C-Suite watcher, and warm C-Suite persona containers.
- Host-side tmux orchestration and host-side Git worktree management have been removed from the production path.

Important caveat: this is an operator-grade local automation system, not a polished end-user desktop app. It expects Linux, Docker, Git, local model infrastructure, and careful setup.

## The Big Picture

```text
You
  |
  | use the TUI or CLI
  v
Drem orchestrator for one project
  |
  | plans tasks, tracks state, exposes an HTTP API
  v
Spawner service
  |
  | starts isolated containers
  v
Worker, reviewer, fixer, planner, and merger containers
  |
  | commit and push branches to the project's bare Git repo
  v
Merger and orchestrator record the result
```

Kyle and the C-Suite services sit beside this flow. Their job is to report on the system, answer operational questions, and coordinate messages through project-specific inboxes and outboxes.

## Main Parts

| Part | What it means in everyday language |
|------|------------------------------------|
| `drem` | The main command. Starts the TUI, runs the orchestrator, imports tasks, and manages projects. |
| Orchestrator | The project manager. It tracks tasks, state transitions, approvals, worker runs, and events. |
| TUI | The terminal dashboard for watching and controlling work. |
| CLI | Scriptable commands for listing tasks, approving plans, retrying work, sending C-Suite messages, and other operations. |
| Spawner | The service allowed to create and destroy worker containers. |
| Workers | Short-lived containers where coding, reviewing, fixing, and merging happen. |
| Agentmon | Watches container output and turns logs into structured events. |
| GQ | A local gateway/queue in front of model calls. |
| SGLang | The local model server used by direct/local model paths. |
| Kyle | A global status reporter that reads registered project state. |
| C-Suite | Warm persona containers such as Mike, Alex, and Seth for review, planning, and coordination. |

## Repository Layout

| Path | Purpose |
|------|---------|
| `cmd/drem` | Main CLI, TUI entry point, project registration, and orchestrator startup. |
| `cmd/drem-spawner` | Container spawner service. |
| `cmd/drem-agentmon` | Container log/event monitor. |
| `cmd/drem-kyle` | Global project status service. |
| `cmd/drem-merger` | One-shot merge worker. |
| `cmd/drem-watchdog` | Worker-side checkpoint helper. |
| `cmd/csuite-*` | C-Suite inbox, watcher, and persona processes. |
| `internal/orchestrator` | Core task state machine and coordination logic. |
| `internal/container` | Docker runtime abstraction. |
| `internal/spawner` | Spawner RPC implementation and client support. |
| `internal/projects` | Host-wide project registry and per-project config generation. |
| `internal/orchhttp` | Orchestrator HTTP API. |
| `internal/tui` | Terminal dashboard. |
| `deploy/docker` | Dockerfiles for orchestrator, workers, C-Suite, Kyle, SGLang, GQ, and support services. |
| `deploy/compose` | Global Docker Compose stack for shared services. |
| `docs/containerization` | Install runbook and migration notes. |
| `plans` | Implementation plans, investigations, and operational notes. |

## Requirements

For the all-in-one containerized path, expect:

- Linux.
- Docker Engine and Docker Compose V2.
- Git.
- Go 1.25+ if building host binaries directly.
- NVIDIA GPU support and `nvidia-container-toolkit` if running the SGLang service locally.
- Local model weights for SGLang, by default under `$HOME/sglang-models`.
- Subscription-based Claude/OpenCode/Codex authentication mounted as local credentials where the containers expect it. Do not configure Claude API tokens for this repo.

A bounded Docker Desktop/macOS control-plane topology is also supported: build
the service and worker images locally for Linux/arm64, keep project Git and
native verification on the Mac, and route only inference through an SSH tunnel
to a remote GQ/SGLang host. See the install guide's remote-inference section.

The exact install steps are in `docs/containerization/install.md`.

## Quick Start For An Existing Operator Machine

Build the main binary:

```bash
go build -o drem ./cmd/drem
```

Create the shared Docker network once:

```bash
bash deploy/compose/network-setup.sh
```

Start the global shared services:

```bash
docker compose -f deploy/compose/global.yml up -d
```

Register a project:

```bash
./drem project register \
  --name my-project \
  --bare /path/to/my-project.git \
  --language go \
  --orch-url http://127.0.0.1:8080 \
  --integration-policy auto_merge \
  --verification-policy local_automated
```

Run the TUI against a project orchestrator:

```bash
./drem --repo /path/to/my-project.git
```

Or run only the dashboard against an already-running orchestrator API:

```bash
./drem --tui-only --orch-url http://127.0.0.1:8080 --repo /path/to/my-project.git
```

For a fresh machine, use `docs/containerization/install.md` instead of treating this section as complete installation documentation.

## TUI Dashboard

The TUI Dashboard shows project tasks, workers, events, and health through the orchestrator HTTP API. It is designed to be run inside an operator-owned terminal or tmux session.

To exit the dashboard, close or kill the tmux pane/session that owns it. The `q` key is not a quit keybinding because the dashboard is intended to stay attached to the project while work continues.

## Common Commands

| Command | What it does |
|---------|--------------|
| `./drem --repo /path/to/project.git` | Start Drem for a bare Git repo. |
| `./drem --repo /path/to/project.git --import tasks.md` | Import tasks from a Markdown file. |
| `./drem --headless --repo /path/to/project.git` | Run the orchestrator and HTTP API without opening the TUI. |
| `./drem --tui-only --orch-url URL --repo /path/to/project.git` | Open only the dashboard, reading from an existing orchestrator API. |
| `./drem project register ...` | Add a project to the host registry and generate per-project config. |
| `./drem project list` | Show registered projects. |
| `./drem project show NAME` | Show one registered project's generated settings. |
| `./drem project remove NAME` | Remove a project from the registry. |
| `./drem cli tasks` | List tasks in script-friendly form. |
| `./drem cli approve TASK_ID` | Approve a gate such as a reviewed plan. |
| `./drem cli reject TASK_ID --reason=...` | Reject a gate and give a reason. |
| `./drem cli retry TASK_ID` | Retry failed work. |
| `dremctl accept-assumptions TASK_ID` | Preserve the current deterministic plan and advance it to SGLang review without a manual approval gate. |
| `dremctl revise-plan TASK_ID --spec task.json --reason TEXT` | Replace a reviewer-rejected adapter plan in place, revalidate immutable scope, and request one fresh SGLang review without invoking the planner. |
| `dremctl adopt CHILD_ID --commit SHA` | Admit a bounded Codex repair for a failed child after re-running branch/scope checks, without another inference attempt. |
| `dremctl artifact TASK_ID` | Show the immutable branch/commit/base delivery envelope. |
| `dremctl report TASK_ID` | Correlate phase timing, SGLang tokens, attempts, artifact versions, host rework, native verification, and Computer Use evidence. |
| `dremctl codex-usage TASK_ID ...` | Attach final explicit Codex goal tokens and elapsed time without mixing them into SGLang inference totals. |
| `dremctl verify TASK_ID ...` | Record evidence for the exact current artifact. |
| `dremctl request-rework TASK_ID --mode orchestrated --reason ...` | Invalidate the current artifact and return it to an orchestrated worker. |
| `dremctl request-rework TASK_ID --mode host-direct --scope PATH ...` | Reserve a bounded repair for the current Codex verifier. |
| `dremctl submit-rework TASK_ID --session UUID --commit SHA` | Submit the host-owned replacement commit for fresh deterministic gates. |
| `dremctl abandon-rework TASK_ID --session UUID --reason ...` | Release host ownership and return the task to orchestration. |
| `dremctl integrate TASK_ID` | Authorize an externally verified prepared branch for integration. |

### Local Canvas Codex adapter

For a project registered with `verification_policy = "external_ack"`, Linux
workers stop at an immutable delivery artifact. The control plane validates the
Git candidate but deliberately does not run Canvas build commands inside its
container; the Mac host owns native compilation and Computer Use evidence.

The helper below checks out the exact frozen SHA in an isolated detached
worktree and runs Canvas's native verification contract:

```bash
scripts/drem-canvas-pilot.sh doctor --base <canvas-base-sha> --min-free-gib 8
scripts/drem-canvas-pilot.sh start --spec plans/canvas-canary-task-spec.json
scripts/drem-canvas-pilot.sh revise <task-id-prefix> --spec plans/canvas-canary-task-spec.json --reason "address reviewer findings"
scripts/drem-canvas-pilot.sh await <task-id-prefix> --timeout 30m
scripts/drem-canvas-pilot.sh prepare <task-id-prefix>
scripts/drem-canvas-pilot.sh build <task-id-prefix>
scripts/drem-canvas-pilot.sh verify <task-id-prefix> \
  --worktree <exact-artifact-worktree> \
  --binary <exact-native-binary> \
  --interactions <computer-use-evidence.json>
scripts/drem-canvas-pilot.sh goal-usage <task-id-prefix> \
  --goal-objective "supervise Canvas task" --goal-status complete \
  --tokens-used <final-goal-tokens> --elapsed-ms <final-goal-elapsed-ms>
scripts/drem-canvas-pilot.sh report <task-id-prefix> --output canary-report.md
scripts/drem-canvas-pilot.sh report <task-id-prefix> --json --output canary-report.json
```

Run `doctor` before activating the supervising Codex goal. It checks the exact
base, local control plane, writable evidence roots, shared Skia cache,
toolchain, and free disk before subscription inference starts. The goal
objective is to supervise the run to a measured terminal report; if the Canvas
implementation fails, producing that report still completes the supervisory
goal. This avoids spending extra turns converting an already measured worker
failure into a Codex `blocked` audit.

For an apples-to-apples comparison, `experiment-init` freezes identical spec
bytes and base commit, `direct-prepare` creates the Drem-owned direct-arm
worktree, and `experiment-record` appends each arm exactly once. The resulting
`experiment-report` includes direct-arm commits, binary/evidence hashes, Codex
tokens, and elapsed time instead of incorrectly reporting those artifacts as
zero because they did not pass through an orchestrated task.

`verify` fails closed unless that worktree is clean, belongs to the registered
Canvas bare repository, and has the exact current artifact commit checked out.
It also requires the supplied binary to live inside that exact worktree. This
prevents a stale build or a convenient binary from another Canvas task from
being attached to otherwise valid Computer Use evidence.

`start` preserves the attributed task-spec contract, and `await` stops at the
next state where the Codex task must inspect evidence rather than hiding that
decision in a shell loop. `report` is a single orchestrator-owned snapshot: it
joins the parent and child attempts with artifact/rework/verification records,
and marks historical zero-token inference as unmeasured. For a measured pilot,
the delegating prompt explicitly instructs the supervising Codex thread to
create a supervisory goal after `doctor` passes and before filing the task.
After the run has a terminal measured outcome, Codex completes that goal,
submits the final returned tokens/time with `goal-usage`,
and regenerates the report. Codex goal usage stays separate from SGLang totals.

If a worker result is scope-rejected but the repair is deterministic, repair
the isolated child worktree, commit it, and use `dremctl adopt`. If native or
visual verification finds a bounded implementation issue, use the existing
`request-rework --mode host-direct` / `submit-rework` cycle. Any edit creates a
new artifact version; an inconclusive UI read with no edit can be repeated
against the same binary.

For direct SGLang coder/fixer workers, a task with planner-declared files is a
bounded run: the exact list is passed to the harness and repository discovery
tools are omitted. Unscoped work and reviewers retain discovery tools. This is
an inference control, not the correctness boundary; whole-branch scope
admission still rejects any out-of-scope diff.

Direct-worker token limits bound cumulative replay input across model requests;
they are not the SGLang model's live context-window size. Generated project
configuration uses phase-aware ceilings: 65k for tests, 90k for implementation,
75k for integration, and 30k for review, with a 60k generic fallback. A response
that has already produced repository mutations is preserved as a checkpoint
when a cumulative-input ceiling is crossed, then deterministic gates decide
whether it is usable. Empty budget-exhausted runs still fail closed.

Scoped coder/fixer runs also enforce a 12-call run-wide ceiling and a smaller
pre-mutation input budget (18k test, 30k implementation, 24k integration).
Reads, structured searches, and discovery-like shell commands share the same
reconnaissance budget; all shell commands are rejected before the first mutation.
Older large tool results are compacted in replay history. Test subtasks receive
an automatically materialized semantic interface contract from the paired
implementation plan. C++ functions/types, registry actions, keymap routes, and
call edges are distinct contract kinds, so only genuinely missing C++ symbols
may use compile-red; runtime seams require active behavioral assertions. Every
child also receives the immutable, hash-checked source excerpts, acceptance
criteria, and paired TDD file list supplied by the adapter.

Worker checkpoints pass deterministic admission before review. The gate rejects
out-of-scope files, destructive rewrites of existing files, placeholder tests,
comment-only contract references, and language-mismatched manifest content. A
failed attempt that already pushed a commit is parked as an `artifact_handoff`
instead of being retried with the same prompt. When one parallel child fails,
not-yet-started dependents are cancelled while already-running independent
siblings drain and preserve their checkpoints.

Terminal direct-worker summaries are copied from the bounded Docker log tail
into both the durable worker attempt and public agent rows. The harness also
emits an incremental usage checkpoint after every model response; if a process
dies before its terminal summary, the last checkpoint preserves token and
context utilization instead of reporting an unmeasured zero. New pilots can
therefore compare `tokens_in` / `tokens_out` without scraping container logs.
Before container creation, the spawner inspects the selected image and performs
one serialized pull when it is absent. An unavailable registry fails the task
with `worker_image_unavailable` instead of retrying the same spawn every tick.
After an accepted child merge, the orchestrator deletes only the exact
Git-reference-registry-owned child branch; checked-out branches, parent feature
branches, and unowned refs fail closed.

## Project Registration

Drem stores the list of known projects at:

```text
~/.drem/projects.toml
```

Each registered project gets generated files under:

```text
~/.drem/projects/<project-name>/
```

Registration records the project's name, bare repository path, language,
orchestrator URL, and optional external inference endpoint. It also generates
per-project Compose and Drem configuration files.

Supported project languages currently include:

- `go`
- `cpp`

These languages select the default worker image, such as `drem-worker-go` or `drem-worker-cpp`.

## Configuration

The main config file is `drem.toml`. Most operators should use the generated per-project config from `drem project register` rather than hand-writing one.

Common settings include:

| Setting | Meaning |
|---------|---------|
| `database_path` | SQLite database for this project. |
| `bare_repo_path` | Bare Git repository Drem works against. |
| `default_branch` | Branch considered the base branch, usually `master` or `main`. |
| `orch_http_port` | Port for the orchestrator HTTP API. |
| `agentmon_token` | Shared secret for agentmon to submit structured events. |
| `[project].language` | Project language used to choose worker images. |
| `[agents.*]` | Per-agent provider, model, effort, endpoint, or container image overrides. |
| `[direct_tool_agent]` | Optional direct local-model tool-agent path, including generic and phase-specific cumulative replay/read-before-mutation limits. |
| `inference_endpoint` in the project registry | Optional OpenAI-compatible endpoint injected into generated direct-tool configuration. |

Older tmux settings may still be accepted by the config loader for compatibility, but they are ignored by the current production path.

## Module Depth Planning

Drem planner prompts ask agents to design for module depth before work begins. A plan should describe `module_boundaries`, typed `interface_contracts` (or legacy `interface_shapes`), and export pressure so reviewers can tell whether the design creates meaningful internal logic or only moves code around.

Planner self-checks should reject shallow designs. A shallow plan often creates a thin wrapper, pass-through package, or exported function with no real decision-making behind it. A deep plan puts policy, state transitions, validation, or orchestration behind a small interface and keeps exports proportional to the module's responsibility.

## What Changed From The Older README

The older README described Drem as a tmux-based system that spawned Claude Code agents in host Git worktrees. That is no longer the current architecture.

The current architecture is container-first:

- Containers are the execution boundary for workers and services.
- Bare Git repositories are the durable source of truth.
- The orchestrator exposes project state over HTTP.
- The dashboard can run as an HTTP client.
- The spawner owns Docker lifecycle actions.
- The C-Suite and Kyle are part of the operational surface.

## Documentation Map

- `docs/containerization/install.md`: step-by-step install and bring-up runbook.
- `docs/containerization/remaining-work.md`: completion log and known deviations from the containerization migration.
- `docs/prd-containerization.md`: product requirements and architecture rationale for the containerized system.
- `plans/containerization.md`: phased implementation plan and acceptance criteria history.
- `deploy/compose/README.md`: global shared-service Compose notes.
- `docs/README.md`: older documentation index.

## Development Checks

Build everything:

```bash
go build ./...
```

Run tests:

```bash
go test ./...
```

Some historical docs mention pre-existing test failures from earlier migration points. Check `docs/containerization/remaining-work.md` for the latest recorded baseline before assuming a failure is caused by your current change.

## Safety Notes

- Do not run `docker compose up` for a subset of services without `--no-deps`; dependent services can cascade into expensive restarts.
- Do not restart SGLang casually; model warmup and CUDA graph capture are slow.
- Do not configure Claude API tokens for this repo. The expected path is subscription credentials mounted from the host.
- Do not push to origin unless explicitly requested by the operator.
