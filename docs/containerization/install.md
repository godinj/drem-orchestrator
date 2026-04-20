# Drem Orchestrator — Containerized Stack Install Runbook

Captures the exact steps that brought a fresh host from zero to a (subset)
containerized stack running against the local registry. Use as the source
of truth for a fresh install or for scripting `install.sh`.

Written against commit `f418522` on `master`, 2026-04-19.

## Prerequisites

- Linux host.
- Docker 20.10+ (with BuildKit).
- For the SGLang + GQ services: NVIDIA GPU, `nvidia-container-toolkit`,
  and a model weights directory at `$HOME/sglang-models/` containing the
  subdir pointed to by `SGLANG_MODEL_SUBDIR` (default `gemma4-26b`,
  ~50 GB).
- Go 1.25+ on the host is **not** required — all Go builds run inside
  multi-stage Dockerfiles.

## Known repo fixups applied during this run

The bring-up exposed two bugs that the repo now carries fixes for. If you
are installing from a commit older than the fix commit, re-apply these
manually:

1. `deploy/docker/csuite-base.Dockerfile` and
   `deploy/docker/worker-base.Dockerfile` override `PATH` without including
   `/usr/sbin`, so `groupadd` / `useradd` fail at build time on
   `debian:bookworm-slim`. Fix: use absolute paths
   (`/usr/sbin/groupadd`, `/usr/sbin/useradd`) in the non-root-user RUN
   step.

2. `deploy/docker/gq.Dockerfile` pinned `FROM golang:1.24.4-alpine`, but
   `go.mod` has moved past 1.25.0. Fix: bump to `golang:1.25-alpine` to
   match every other Go Dockerfile in `deploy/docker/`.

3. `deploy/compose/global.yml` had an obsolete top-level `version: "3.9"`
   key that Compose V2 warns on. Removed.

4. Kyle's bind-mount of `${HOME}/.drem/projects.toml` is silently
   autocreated as a directory when the source doesn't exist or when
   `${HOME}` resolves to `/root` under `sudo`. Step 1 and Step 1b below
   document the workaround. A more durable fix (require-file semantics
   via Compose `configs:`, or a `DREM_HOME`-style explicit env var
   validated by an entrypoint guard) is open follow-up work — see
   `remaining-work.md`.

## Step 1 — Docker group membership

```bash
sudo usermod -aG docker "$USER"
# Log out and back in (or `newgrp docker`) for the group to take effect.
```

Without docker-group membership, every `docker` / `docker compose` call
needs `sudo`, and **`sudo` resets `$HOME` to `/root`**. The global compose
file bind-mounts `${HOME}/.drem/projects.toml` into Kyle, so under plain
`sudo` that path expands to `/root/.drem/projects.toml` — which doesn't
exist, so Docker silently auto-creates it as a *directory* owned by
`root`. Kyle then crash-loops on "is a directory", and the directory
persists across `compose up --force-recreate` because the mount source
resolves the same way on every restart.

If you must proceed before re-login, use `sudo -E docker …` so `$HOME`
propagates. If you have already hit the directory-autocreate trap, clean
up both locations before the next bring-up:

```bash
sudo rm -rf /root/.drem ~/.drem    # remove any auto-created junk
```

## Step 1b — Pre-create the host-side registry

The registry file must exist as a *regular file* before the first Kyle
bring-up. Create it empty (Kyle accepts an empty registry — it just logs
`projects=0` and serves `/healthz` 200):

```bash
mkdir -p ~/.drem
[ -e ~/.drem/projects.toml ] || cat > ~/.drem/projects.toml <<'EOF'
# Drem host-wide project registry. Managed by:
#   drem project {register,list,remove}
# Kyle reads this at startup to discover orchestrators to poll.
EOF
ls -la ~/.drem/projects.toml   # must be -rw-…, not drwx-…
```

If this file is a directory at the moment `docker compose up kyle` runs,
Kyle will crash-loop until you `docker rm -f drem-kyle`, fix the source,
and recreate the container (a restart alone will not rebind the mount).

## Step 2 — External network

```bash
cd /home/godinj/git/drem-orchestrator.git/master
bash deploy/compose/network-setup.sh
docker network ls | grep drem-net
```

Creates the external `drem-net` network every service attaches to. Idempotent.

## Step 3 — Bring up the local registry alone

The registry must exist before any push, and before any compose `up` that
pulls `localhost:5000/*` images.

```bash
docker compose -f deploy/compose/global.yml up -d registry
curl -s 127.0.0.1:5000/v2/_catalog   # → {"repositories":[]}
```

## Step 4 — Build and push images (in cost order)

All builds run from the repo root. Each stage ends with a catalog check so
you can resume cleanly after any failure.

### 4a — Smoke-test image (cheapest first)

```bash
docker build -f deploy/docker/docker-query-proxy.Dockerfile \
  -t localhost:5000/drem-docker-query-proxy:latest .
docker push localhost:5000/drem-docker-query-proxy:latest
curl -s 127.0.0.1:5000/v2/_catalog
```

### 4b — Remaining Go infra images

```bash
for img in spawner agentmon kyle merger; do
  docker build -f "deploy/docker/${img}.Dockerfile" \
      -t "localhost:5000/drem-${img}:latest" . \
    && docker push "localhost:5000/drem-${img}:latest" \
    || { echo "FAIL: $img"; break; }
done
curl -s 127.0.0.1:5000/v2/_catalog
```

### 4c — C-Suite personas + orchestrator images

```bash
bash deploy/docker/build-csuite.sh
curl -s 127.0.0.1:5000/v2/_catalog
```

Builds and pushes: `drem-csuite-base`, `drem-csuite-{mike,alex,ross,seth}`,
`drem-csuite-watcher`, `drem-orch`, `drem-orch-dev`.

### 4d — Worker images

```bash
bash deploy/docker/build-workers.sh
curl -s 127.0.0.1:5000/v2/_catalog
```

Builds and pushes: `drem-worker-base`, `drem-worker-go`, `drem-worker-cpp`.
`worker-cpp` is the slowest (C/C++ toolchain layer) — budget 10–15 min on
first build.

### 4e — SGLang + GQ (GPU required)

```bash
docker build -f deploy/docker/sglang.Dockerfile \
  -t localhost:5000/drem-sglang:latest . \
  && docker push localhost:5000/drem-sglang:latest

docker build -f deploy/docker/gq.Dockerfile \
  -t localhost:5000/drem-gq:latest . \
  && docker push localhost:5000/drem-gq:latest

curl -s 127.0.0.1:5000/v2/_catalog
```

`drem-sglang` is a thin re-tag of `lmsysorg/sglang:latest`; the first
build pulls 15–20 GB of CUDA runtime layers. `drem-gq` is a small Go
binary.

After 4a–4e the registry catalog should list 18 repositories:

```
drem-agentmon
drem-csuite-{alex,base,mike,ross,seth}
drem-csuite-watcher
drem-docker-query-proxy
drem-gq
drem-kyle
drem-merger
drem-orch
drem-orch-dev
drem-sglang
drem-spawner
drem-worker-{base,cpp,go}
```

## Step 5 — Bring up the global stack

### 5a — Subset (no GPU needed)

Use this for CI smoke tests or when SGLang is not wanted yet. Skips
`sglang` and `gq`; the orchestrator's classifier will fall back to the
Anthropic API (or fail closed if not configured).

```bash
docker compose -f deploy/compose/global.yml up -d \
  registry spawner kyle docker-query-proxy
docker compose -f deploy/compose/global.yml ps
```

Expect four containers in `Up` state. `agentmon` is **not** in the global
compose — it lives in the per-project compose template (Step 6).

### 5b — Full stack (GPU)

Requires `$HOME/sglang-models/<subdir>` and `nvidia-container-toolkit`.
Optional per-host tuning via `deploy/compose/.env`
(see `.env.example` for the full knob list).

> SGLang tool-call parser caveat: `SGLANG_TOOL_CALL_PARSER` defaults to
> `hermes` because the stable `lmsysorg/sglang` image's parser registry
> does not include `gemma4` and the container crash-loops on
> `--tool-call-parser: invalid choice: 'gemma4'`. `hermes` is a
> stopgap; see `plans/sglang-gemma4-followup.md` for the real fix
> options (host SGLang via systemd, build SGLang from upstream git, or
> bump to a newer image tag that ships the gemma4 parser).

```bash
docker compose -f deploy/compose/global.yml up -d
docker compose -f deploy/compose/global.yml ps
```

Expect six containers: `registry`, `sglang`, `gq`, `spawner`, `kyle`,
`docker-query-proxy`. `sglang`'s `start_period` is 120 s for cold model
load; `gq` waits on `sglang: service_healthy` before starting.

## Step 6 — Register the first project

```bash
drem project register \
  --name drem-orchestrator \
  --bare  /home/godinj/git/drem-orchestrator.git \
  --language go
drem project list
```

Writes `~/.drem/projects.toml` and generates
`~/.drem/projects/drem-orchestrator/compose.yml` from
`internal/projects/templates/project-compose.yml.tmpl`.

> Known wiring gap (carried from Tier 3, see `remaining-work.md`
> §Known caveats): `cmd/drem/project.go`'s `templateDataFor` uses
> `uuid.NewString()` for the shared token and leaves `OrchHostPort`,
> `DevMode`, and `OrchImage` to `applyDefaults`. The template renders,
> but the first multi-project register is untested. Fix planned before
> registering a second project.

## Step 7 — Bring up the per-project stack

```bash
docker compose -f ~/.drem/projects/drem-orchestrator/compose.yml up -d
docker compose -f ~/.drem/projects/drem-orchestrator/compose.yml ps
```

Expect: `orch`, `agentmon`, `csuite-watcher`, `csuite-{mike,alex,ross,seth}`.

The merger image is *not* listed; the previous `merger-pool` warm
replicas were removed because `drem-merger` is a per-task one-shot
binary that crash-loops when run with no argv. The template still
declares a `merger-template` stub gated behind `profiles: ["never"]`
so `docker compose pull` primes the image, but no merger container
runs until spawn-on-demand wiring lands. See
`plans/merger-spawn-on-demand.md`.

> The `csuite-watcher` service reads its bridge auth + listen + DB path from
> `DREM_BEARER_TOKEN` / `DREM_LISTEN_ADDR` / `DREM_DB_PATH` env vars (see the
> per-project compose template). Precedence is env > `drem.toml [serve]` >
> built-in default; the container has no `drem.toml` mounted and relies on
> the env block populated from `Project.SharedToken`.

## Step 8 — Verify

```bash
# Orchestrator HTTP API responds.
curl -s http://127.0.0.1:${ORCH_HOST_PORT}/projects | jq .

# Kyle sees the registered project.
curl -s http://127.0.0.1:8095/projects | jq .

# Spawner RPC over Unix socket is reachable from inside the network.
docker run --rm --network drem-net curlimages/curl:latest \
  -s --unix-socket /var/run/drem/spawner.sock http://localhost/ListWorkers
```

## Teardown

```bash
docker compose -f ~/.drem/projects/drem-orchestrator/compose.yml down
docker compose -f deploy/compose/global.yml down
# Nuclear option — drops all drem images and volumes:
# docker images --format '{{.Repository}}:{{.Tag}}' | grep '^localhost:5000/drem-' | xargs -r docker rmi
# docker volume rm drem-registry-data drem-spawner-sock
# docker network rm drem-net
```

## Open follow-ups blocking a clean full-stack install

From `docs/containerization/remaining-work.md`:

1. Host path still load-bearing — `internal/worktreehost/` is the
   copy-rename shim of the deleted `internal/worktree/`; `cmd/drem/main.go`
   still builds `orchestrator.NewHostManager`. Dual execution path.
2. `agentmon` mounts `/var/run/docker.sock:ro` directly; PRD says socket
   should only live in `spawner`. Route through `docker-query-proxy` as
   a Phase-1 hardening step.
3. `drem project register` template data gap — wire
   `projects.NewSharedToken()`, `Registry.AllocateOrchHostPort()`,
   `DevMode`, and `OrchImage` into `templateDataFor` before registering
   a second project.
4. Prompt-17 scrub (tmux section removal from `drem.toml`,
   `scripts/csuite-*.sh` cleanup, `ARCHITECTURE.md` package-map update)
   never ran.
