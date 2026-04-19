# Drem Global Infrastructure Compose

This directory holds the authoritative global docker-compose stack for
drem-orchestrator. It brings up the shared services every per-project compose
depends on: a host-local Docker registry, SGLang, and GQ.

Per `docs/prd-containerization.md` §Compose topology (Global compose) and
§Phased rollout step 1.

## Prerequisites

- Docker (engine + CLI) on a Linux host.
- `nvidia-container-toolkit` installed and configured so the Docker daemon can
  expose GPUs (required by SGLang).
- The shared `drem-net` network created once. Run:
  ```bash
  bash deploy/compose/network-setup.sh
  ```
  The script is idempotent; re-running it is a no-op if the network already
  exists.
- SGLang model weights present on the host. Default lookup is
  `$HOME/sglang-models` (override with `SGLANG_MODEL_DIR`). The directory is
  bind-mounted read-only into the SGLang container.
- Optional: `~/.drem-csuite/gq.toml` GQ config file. GQ has working defaults
  so the file is not required; see `internal/gq/config.go:124-166`.

## Bring up the stack

```bash
# First run (once per host):
bash deploy/compose/network-setup.sh

# Build + start:
docker compose -f deploy/compose/global.yml build
docker compose -f deploy/compose/global.yml up -d

# Verify:
docker compose -f deploy/compose/global.yml ps           # registry, sglang, gq → Up
curl -sSf http://127.0.0.1:5000/v2/                      # registry responds
curl -sSf http://127.0.0.1:8081/v1/models                # sglang responds
curl -sSf http://127.0.0.1:8091/metrics | head           # gq metrics
```

To stop:

```bash
docker compose -f deploy/compose/global.yml down
```

Registry data persists in the `drem-registry-data` named volume across
restarts. SGLang model weights live on the host filesystem and are never
written to any volume.

## Pushing images to the local registry

Every drem component image is pushed manually to the host-local registry
today (CI wiring is a future concern). The registry is unauthenticated and
bound to `127.0.0.1` only; nothing outside the host can see it.

Example — build and push `drem-gq`:

```bash
docker build -f deploy/docker/gq.Dockerfile -t localhost:5000/drem-gq:latest .
docker push localhost:5000/drem-gq:latest
```

Same pattern for any `drem-<component>` image. Use fully-qualified local
names (`localhost:5000/drem-<component>:<tag>`) everywhere so the daemon
resolves them from the local registry rather than attempting Docker Hub.

## What depends on this compose

- Every per-project compose file (generated into `~/.drem/projects/<name>/compose.yml`
  by `drem project register` — owned by prompt 05) attaches to the external
  `drem-net` network defined here and pulls its images from
  `localhost:5000/drem-<component>:<tag>`.
- Future phase images (orchestrator, spawner, agentmon, Kyle, merger, workers,
  C-Suite containers — prompts 07, 10, 14, 16) are likewise pushed to this
  registry and joined to `drem-net`.

## Scope

This compose file carries **only** the shared, always-on global
infrastructure: the registry, SGLang, and GQ. Orchestrators, spawners,
agentmons, Kyle, workers, mergers, and C-Suite containers are owned by
later prompts and live elsewhere.

The registry service is deliberately unauthenticated and loopback-only. Do
not publish `:5000` on anything other than `127.0.0.1`.
