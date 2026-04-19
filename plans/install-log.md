# Containerization Install Log — Session Record

Chronological record of the containerization build effort, written so a
future install script can be derived from it. Covers the work executed by
the swarm over `docs/prd-containerization.md` prompts plus the cleanup
and cutover steps handled directly.

This document is not the install script. It is the source material for
writing one. Steps are grouped into phases matching the install order; each
phase cites the artifact that implements it.

---

## 0. Target outcome (what "installed" means)

When the install is complete on a fresh host, the following will be true:

- A Docker registry listening on `127.0.0.1:5000` holds the full set of
  `drem-*` images (18 today: registry, sglang, gq, spawner, worker-base,
  worker-go, worker-cpp, agentmon, orch, orch-dev, csuite-base, csuite-{mike,
  alex,ross,seth}, csuite-watcher, merger, kyle, docker-query-proxy).
- The global compose stack (`deploy/compose/global.yml`) is up with
  registry, sglang, gq, spawner, kyle, and docker-query-proxy healthy.
- The host-side SGLang + GQ processes have been retired.
- `drem project register <name>` can register a project and start its
  per-project compose.

Phase 7 graduation items (delete `internal/tmux/` and `internal/worktree/`)
have already landed via commits on `master`; the install does not need to
repeat them.

---

## 1. Host prerequisites

### 1.1 OS and kernel

- Linux host. Tested on the operator's workstation (Debian-family).
- Kernel recent enough to support cgroup v2 and nvidia-container-toolkit.

### 1.2 Installed packages

- `docker` (engine + CLI) version >= 20.10 with BuildKit. Daemon must be
  active (`systemctl is-active docker`). Installed on this host as Docker
  29.2.1.
- `nvidia-container-toolkit` — required for SGLang GPU passthrough. The
  toolkit must be wired into the Docker daemon's runtime configuration.
- A Go toolchain matching the `go.mod` directive for building the
  watchdog binary statically into the worker build context. Multi-stage
  builds carry their own Go inside the image, but `build-workers.sh`
  compiles `cmd/drem-watchdog` on the host before the docker build.
- `curl` and `sqlite3` for operator-side smoke checks.
- `git` (already required for the bare repo).

### 1.3 User and group membership

- The interactive user must be in the `docker` group to talk to the
  socket without `sudo`. This session required:

  ```bash
  sudo usermod -aG docker "$USER"
  ```

  Docker-facing commands inside a shell that predates the `usermod` work
  via `sg docker -c '<cmd>'` until the user starts a fresh login.

### 1.4 GPU

- A CUDA-capable GPU visible to the nvidia runtime. SGLang requires
  `--gpus all` (compose translates this via
  `deploy.resources.reservations.devices`).

---

## 2. Host-side directory layout

The install expects / creates the following paths under `$HOME`:

| Path | Purpose | Created by |
|---|---|---|
| `~/.drem-csuite/` | C-Suite state dir (watcher.db, csuite.db, per-agent inboxes) | orchestrator at first run; can pre-create |
| `~/.drem-csuite/gq.toml` | Optional GQ config override | operator; absent is fine |
| `~/.drem/` | Host-wide project registry root | `drem project register` |
| `~/.drem/projects.toml` | Project-registry TOML | `drem project register` |
| `~/.drem/projects/<name>/compose.yml` | Per-project compose | `drem project register` |
| `~/models/<model-dir>/` | SGLang model weights (Hugging Face layout) | operator |
| `deploy/compose/.env` | Operator-local compose overrides (gitignored) | operator, from `.env.example` |

Model-dir location and weight-subdir name both vary per host. Compose
parameterizes both via `.env`:

- `SGLANG_MODEL_DIR` — absolute host path bind-mounted read-only into the
  container at `/models`. Default: `$HOME/sglang-models`.
- `SGLANG_MODEL_SUBDIR` — subdirectory under `/models` passed as
  `--model-path`. Default: `gemma4-26b`.
- `SGLANG_SERVED_NAME` — OpenAI-API model name drem clients reference.
  Default: `gemma4-26b`. Must match `[agents.classifier].model` and
  `[direct_tool_agent].model` in `drem.toml`.
- Plus tunables (`SGLANG_CONTEXT_LENGTH`, `SGLANG_MEM_FRACTION`,
  `SGLANG_MAX_REQUESTS`, `SGLANG_TP`) documented in the template.

Bootstrap on a new host:

```bash
cp deploy/compose/.env.example deploy/compose/.env
$EDITOR deploy/compose/.env          # match model dir + subdir on this host
```

Compose auto-loads `deploy/compose/.env` because it lives next to the
compose file. Gemma4-specific flags (`--kv-cache-dtype fp8_e5m2`,
`--tool-call-parser gemma4`, etc.) are hardcoded in the compose `command:`
since swapping them means swapping model families.

---

## 3. Git checkout

The drem repository is a bare repository with a working worktree at
`master/`:

```bash
BARE=~/git/drem-orchestrator.git
git clone --bare https://github.com/<owner>/drem-orchestrator.git "$BARE"
git -C "$BARE" worktree add master master
cd "$BARE/master"
```

Every subsequent command is executed from `$BARE/master`.

---

## 4. Docker network bootstrap

The global compose and every per-project compose attach to an external
Docker network named `drem-net`. It must exist before `docker compose up`.
The repo ships an idempotent creator:

```bash
bash deploy/compose/network-setup.sh
```

Script contents (for install-script inlining): verifies
`docker network inspect drem-net`; on miss, runs
`docker network create drem-net`.

---

## 5. Image build and push

All images are pushed to the host-local registry at `localhost:5000`. The
registry itself is a compose service that must be running before any push
— so image builds happen **after** the registry is up, not before.

### 5.1 Registry alone

```bash
docker compose -f deploy/compose/global.yml up -d registry
curl -sSf http://127.0.0.1:5000/v2/
```

Registry storage is persisted in the named volume `drem-registry-data`.

### 5.2 Workers (Phase 2 and 5 images)

```bash
bash deploy/docker/build-workers.sh
```

This script:

1. Compiles `cmd/drem-watchdog` with `CGO_ENABLED=0 GOOS=linux` into
   `deploy/docker/context/drem-watchdog` (gitignored; regenerable).
2. Builds `drem-worker-base`, `drem-worker-go`, `drem-worker-cpp` in that
   order — later images layer on earlier ones.
3. Tags every image as `localhost:5000/drem-<name>:latest`.
4. Pushes all three to the local registry.

### 5.3 C-Suite + orchestrator (Phase 3 and 4 images)

```bash
bash deploy/docker/build-csuite.sh
```

This script:

1. Stages persona prompts (`mike/alex/ross/seth`) from
   `docs/csuite-agents/prompts/` into
   `deploy/docker/context/csuite-prompts/` (gitignored).
2. Builds `drem-csuite-base`, then `drem-csuite-{mike,alex,ross,seth}`
   from the base.
3. Builds `drem-csuite-watcher`, `drem-orch`, `drem-orch-dev` via repo-
   root context multi-stage Dockerfiles.
4. Tags and pushes each image to `localhost:5000`.

### 5.4 Single-image components (Phase 1, 2, 4, 6 miscellany)

The remaining images have no dedicated build script; they follow the
uniform pattern documented in `deploy/compose/README.md`:

```bash
for img in sglang gq spawner agentmon merger kyle docker-query-proxy; do
    docker build \
        -f "deploy/docker/${img}.Dockerfile" \
        -t "localhost:5000/drem-${img}:latest" \
        .
    docker push "localhost:5000/drem-${img}:latest"
done
```

The `sglang` Dockerfile is the heaviest (~55 GB after the model layer if
weights are baked; today the weights are bind-mounted, so the image
itself is ~1 GB).

### 5.5 Verify the catalog

```bash
curl -s http://127.0.0.1:5000/v2/_catalog
```

Expect the full list of 18 repositories (all `drem-*`).

---

## 6. Global compose bring-up

After all images are pushed, bring up the rest of the global stack:

```bash
docker compose -f deploy/compose/global.yml up -d
```

Compose starts services in dependency order: registry (already up) →
sglang → gq (waits for sglang health) → spawner → docker-query-proxy →
kyle (waits for docker-query-proxy). Healthchecks are baked in.

### 6.1 Expected running containers after bring-up

| Container | Image | Port publishing |
|---|---|---|
| `drem-registry` | `registry:2` | `127.0.0.1:5000->5000` |
| `drem-sglang` | `localhost:5000/drem-sglang:latest` | `127.0.0.1:8081->8081` |
| `drem-gq` | `localhost:5000/drem-gq:latest` | `127.0.0.1:8090->8090`, `127.0.0.1:8091->8091` |
| `drem-spawner` | `localhost:5000/drem-spawner:latest` | (socket only) |
| `drem-docker-query-proxy` | `localhost:5000/drem-docker-query-proxy:latest` | (internal) |
| `drem-kyle` | `localhost:5000/drem-kyle:latest` | `127.0.0.1:8095->8090` |

`drem-spawner` mounts `/var/run/docker.sock` and exposes its JSON-RPC
socket at `/var/run/drem/spawner.sock` via the shared
`drem-spawner-sock` named volume.

### 6.2 Validation

```bash
docker compose -f deploy/compose/global.yml ps          # all Up + healthy
curl -sSf http://127.0.0.1:5000/v2/                     # registry
curl -sSf http://127.0.0.1:8081/v1/models               # sglang
curl -sSf http://127.0.0.1:8091/metrics | head          # gq metrics
curl -sSf http://127.0.0.1:8095/healthz                 # kyle (if implemented)
```

---

## 7. Host-service cutover

Once the containerized sglang and gq are healthy and the host callers
have been pointed at the new endpoints (which are the same ports on
loopback, so no config change is required for host callers), the
pre-existing host processes are retired.

### 7.1 Stop host SGLang

```bash
# Identify and kill the long-running launcher; children follow.
pkill -f "sglang.launch_server"
```

On this host the process tree was rooted at
`/home/godinj/venvs/sglang/bin/python -m sglang.launch_server --port 8081`.
With no upstart unit wrapping it, `pkill` is sufficient; there is nothing
to `systemctl disable`.

### 7.2 Stop host GQ

GQ ran as a systemd user service.

```bash
systemctl --user stop gq
systemctl --user disable gq
```

Document the service as replaced in `deploy/compose/README.md` (already
noted there).

### 7.3 Verify the cutover

```bash
curl -sSf http://127.0.0.1:8081/v1/models    # now served by container
curl -sSf http://127.0.0.1:8091/metrics      # now served by container
```

The host TUI + orchestrator continue to point at the same ports and
should not require any config change beyond the systemd unit disable.

### 7.4 Cutover ordering choice

The operator selected a clean port swap ("option b"): stop the host
SGLang cold, bring up the container, accept ~120 seconds of downtime
during the model cold-load (`start_period: 120s` in the healthcheck).
This avoids running parallel SGLang instances on different ports and the
config thrash that implies.

---

## 8. Register the first project

```bash
drem project register drem-orchestrator \
    --lang go \
    --bare-repo "$BARE"
```

This step:

- Creates `~/.drem/projects.toml` (if absent) and adds an entry for the
  project.
- Generates `~/.drem/projects/drem-orchestrator/compose.yml` from the
  embedded per-project template (orchestrator, csuite-watcher, four
  C-Suite containers). The merger image is referenced by an
  image-prime stub (`merger-template`, `profiles: ["never"]`) that
  does not run; the previous `merger-pool` warm replicas were removed
  because `drem-merger` is a per-task one-shot binary that crash-loops
  when run with no argv. Spawn-on-demand wiring is tracked in
  `plans/merger-spawn-on-demand.md`.
- Optionally runs `docker compose -f <path> up -d` against that file.

Register additional projects (e.g. `drem-canvas --lang cpp`) via the
same command.

---

## 9. Gotchas and hygiene fixes discovered this session

Captured here so the install script can inline fixes rather than defer
them.

### 9.1 Docker group

User not in the `docker` group gets `permission denied` on the socket.
The install script must either verify membership up front (`id -nG | grep
-qw docker`) or invoke docker via `sg docker -c` during the same session
the group was added.

### 9.2 Compose schema

The stock compose shipped with a deprecated top-level `version: "3.9"`
key that modern compose ignores with a warning. Removed in commit
`b62345f`.

### 9.3 useradd / groupadd PATH

Minimal Debian/Alpine bases do not always carry `/usr/sbin` on PATH for
non-login RUN shells. The Dockerfiles now invoke `/usr/sbin/groupadd`
and `/usr/sbin/useradd` with absolute paths. Commit `b62345f`.

### 9.4 Go builder pinning

`gq.Dockerfile` was built on `golang:1.24.4-alpine`; the repo `go.mod`
directive required `1.25`. Bumped to `golang:1.25-alpine`. Commit
`b62345f`.

### 9.5 Stray root-level binaries

`go build` in the repo root without `-o` leaves `drem-watchdog` and
`drem-docker-query-proxy` in the worktree. Both are regenerable and are
now gitignored (commit `bf1773e`). The canonical artifacts live at
`deploy/docker/context/drem-watchdog` (worker build context) and inside
the multi-stage Dockerfile build for docker-query-proxy.

### 9.6 Build-context staging

`deploy/docker/context/csuite-prompts/` is repopulated by
`build-csuite.sh` on every run from the canonical `docs/csuite-agents/
prompts/` directory and must not be committed (gitignored, commit
`bf1773e`).

### 9.7 Network naming

Three names collided during the build: the first-cut compose seed used
`drem`, a proposal circulated `drem_shared`, and the final artifact
settled on `drem-net`. The install script should follow the final form
(`drem-net`, created by `deploy/compose/network-setup.sh`).

### 9.8 Watcher triggering on `.md` drops

An earlier diagnosis claimed `csuite-watcher` only globbed `.signal`
sidecar files and would silently drop plain-`.md` writes into agent
inboxes. On re-verification, the watcher fires turns on `.md` drops as
designed; the diagnosis was stale. No patch needed. The install does
not need to work around this.

### 9.9 csuite-watcher container needs env-var config

The container has no `drem.toml` mounted, so `cmd/csuite-watcher serve`
reads `DREM_BEARER_TOKEN`, `DREM_LISTEN_ADDR`, and `DREM_DB_PATH` from
the env. Precedence is env > toml > built-in default. The per-project
compose template populates all three (token reuses
`Project.SharedToken`, listen `:8090`, db `/var/lib/drem/csuite.db`).
Without these the container restart-loops on `bearer_token must be
set`.

---

## 10. Install-script outline (for future derivation)

A single-script install would sequence:

1. Verify prerequisites (docker running, nvidia toolkit present, user in
   docker group, GPU visible). Fail closed on any miss.
2. Ensure `~/.drem-csuite/` and model weight directory exist.
3. `cp deploy/compose/.env.example deploy/compose/.env` and edit values
   (model dir, subdir, served name) to match this host.
4. `bash deploy/compose/network-setup.sh`.
5. `docker compose -f deploy/compose/global.yml up -d registry`.
6. `bash deploy/docker/build-workers.sh`.
7. `bash deploy/docker/build-csuite.sh`.
8. Build + push the single-image components (sglang, gq, spawner,
   agentmon, merger, kyle, docker-query-proxy) in a loop.
9. Verify registry catalog (`curl /v2/_catalog` returns the expected
   18-entry list).
10. `docker compose -f deploy/compose/global.yml up -d`.
11. Poll all healthchecks green.
12. Retire host SGLang (`pkill -f sglang.launch_server`) and GQ
    (`systemctl --user stop gq && systemctl --user disable gq`).
13. Re-verify endpoints on `:8081` and `:8091`.
14. Print the "next step" command: `drem project register …`.

Phases 1–9 are idempotent. Phases 11–12 are one-shot; rerunning them on
an already-migrated host is a no-op.

---

*Authored 2026-04-19 by Kyle (CEO) from the session log. Commits
referenced: `bf1773e` (gitignore), `9314307` (plans drafts), `b62345f`
(deploy hygiene).*
