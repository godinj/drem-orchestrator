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

For the current containerized path, expect:

- Linux.
- Docker Engine and Docker Compose V2.
- Git.
- Go 1.25+ if building host binaries directly.
- NVIDIA GPU support and `nvidia-container-toolkit` if running the SGLang service locally.
- Local model weights for SGLang, by default under `$HOME/sglang-models`.
- Subscription-based Claude/OpenCode/Codex authentication mounted as local credentials where the containers expect it. Do not configure Claude API tokens for this repo.

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
  --orch-url http://127.0.0.1:8080
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

## Project Registration

Drem stores the list of known projects at:

```text
~/.drem/projects.toml
```

Each registered project gets generated files under:

```text
~/.drem/projects/<project-name>/
```

Registration records the project's name, bare repository path, language, and orchestrator URL. It also generates per-project Compose and Drem configuration files.

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
| `[direct_tool_agent]` | Optional direct local-model tool-agent path. |

Older tmux settings may still be accepted by the config loader for compatibility, but they are ignored by the current production path.

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
