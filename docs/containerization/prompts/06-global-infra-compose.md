# Agent: Global Infrastructure Compose

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent task orchestrator. Your task is Phase 1 foundation work for the containerization initiative: stand up the global docker-compose topology (local registry + SGLang + GQ) so that the rest of the migration has a concrete target environment to land on. This is Phase 1 of the PRD's phased rollout: no agent-facing changes, just the shared-infra story.

## Context

Read these specs before starting:

- `docs/prd-containerization.md` (sections: "Compose topology" → Global compose; "Images" → Registry; "Phased rollout" → step 1)
- `docker-compose.yml` at the repo root (current root-level compose — understand what's already containerized)
- `docker-compose.grafana.yml` (existing compose overlay pattern you will mirror)
- `cmd/gq/` and how GQ is currently run (systemd unit, bare process, or existing container)
- Any existing SGLang launch scripts under `scripts/` or `infra/`

## Deliverables

### New files

#### 1. `deploy/compose/global.yml`

The authoritative global compose file. Lives in the repository so `docker compose -f deploy/compose/global.yml up -d` brings up the shared infrastructure.

Services:

- **`registry`** — `registry:2` image, exposes `127.0.0.1:5000:5000`, persistent volume `drem-registry-data` mounted at `/var/lib/registry`. This is the host-local image registry; all other drem images push here.
- **`sglang`** — the existing SGLang command wrapped in its Dockerfile (see below). GPU access via `deploy.resources.reservations.devices` with `driver: nvidia` and `count: all`. Exposes whatever HTTP port SGLang already serves on (match the existing port from the host-side launch script).
- **`gq`** — the existing `gq` binary containerized. Exposes its HTTP API on `127.0.0.1:<port>:<port>` (match the existing port).

All services:

- Attach to the external network `drem-net` (see below).
- Carry labels `drem.scope=global` and `drem.service=<name>`.
- `restart: unless-stopped`.

#### 2. `deploy/compose/network-setup.sh`

One-liner setup script (idempotent):

```bash
#!/usr/bin/env bash
set -euo pipefail
docker network inspect drem-net >/dev/null 2>&1 || docker network create drem-net
```

Document in the repo's operations README (or create `deploy/README.md` if none exists) that this script must be run once before `docker compose up`.

#### 3. `deploy/docker/sglang.Dockerfile`

Base image matching the CUDA + Python version the current host-side launch uses. Install SGLang from the pinned version visible in existing launch scripts. `ENTRYPOINT` runs the same `python -m sglang.launch_server ...` invocation the host currently uses.

If the project already ships a SGLang Dockerfile, reuse it verbatim and skip this file — the goal is one Dockerfile, not two.

#### 4. `deploy/docker/gq.Dockerfile`

Multi-stage Go build: `golang:1.24.4` build stage compiles `./cmd/gq`, runtime stage is `gcr.io/distroless/static-debian12`. Expose the GQ port. Tag as `localhost:5000/drem-gq:latest`.

#### 5. `deploy/compose/README.md`

Operational doc, short:

- Prerequisites (Docker, nvidia-container-toolkit, the one-time `network-setup.sh` step)
- How to bring up global infra: `docker compose -f deploy/compose/global.yml up -d`
- How to push new image tags to the local registry
- What depends on this compose (every per-project compose attaches to `drem-net` and pulls images from `localhost:5000`)

### Migration

#### 6. Existing host-side launch scripts for SGLang and GQ

Leave them in place but add a deprecation comment at the top pointing to `deploy/compose/global.yml`. Do not delete — the developer may fall back to them during cutover. Prompt 17 is NOT responsible for this cleanup; a separate operational retirement happens after Phase 1 is validated.

#### 7. `docker-compose.yml` at repo root

If this file currently starts any service that now lives in `deploy/compose/global.yml`, remove those service blocks and leave a comment `# moved to deploy/compose/global.yml`. Do not break anything else the root compose does today.

## Scope Limitation

- No orchestrator, spawner, agentmon, Kyle, worker, merger, or C-Suite images here. Those are owned by later prompts (07, 10, 14, 16).
- No per-project compose file generation — prompt 05 owns the template and the `drem project register` command that writes it.
- No automated image-push-on-build pipeline. The developer runs `docker build` + `docker push localhost:5000/...` manually for this cut; CI wiring is a future concern.
- The registry is unauthenticated and bound to `127.0.0.1` only. Do not expose it to the external network.

## Conventions

- File locations: compose under `deploy/compose/`, Dockerfiles under `deploy/docker/`. Create these directories if they do not exist.
- Compose version: `3.9` (matches the rest of the repo)
- Service labels follow the `drem.<key>=<value>` convention used throughout the PRD
- All image references use fully-qualified local names: `localhost:5000/drem-<component>:<tag>`
- Build verification:
  ```bash
  bash deploy/compose/network-setup.sh
  docker compose -f deploy/compose/global.yml config   # validates syntax
  docker compose -f deploy/compose/global.yml build
  docker compose -f deploy/compose/global.yml up -d
  docker compose -f deploy/compose/global.yml ps       # registry, sglang, gq all Up
  curl -sSf http://127.0.0.1:5000/v2/                  # registry responds
  ```
- Do not commit any image tarballs or volume data — only text sources
