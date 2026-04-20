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
for img in spawner agentmon kyle merger planner; do
  docker build -f "deploy/docker/${img}.Dockerfile" \
      -t "localhost:5000/drem-${img}:latest" . \
    && docker push "localhost:5000/drem-${img}:latest" \
    || { echo "FAIL: $img"; break; }
done
curl -s 127.0.0.1:5000/v2/_catalog
```

Note: `drem-planner` is a non-Go image (it layers node + the
`@anthropic-ai/claude-code` npm package on debian:bookworm-slim), but
the build step is the same shape as the Go ones. See
`deploy/docker/planner.Dockerfile` and
`plans/warm-direct-planner.md` for the rationale.

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

`drem-sglang` reproduces the operator's host SGLang install: a
CUDA 12.8 + cuDNN base on Ubuntu 24.04, Python 3.13.5 via the
deadsnakes PPA, a frozen pip lock from the host venv (207 packages
including a git-built SGLang at commit `g90ef8ce54`, `transformers
5.5.4`, and `torch 2.9.1`), then six in-tree patches applied against
site-packages to make AWQ-quantized Gemma-4 weights work with the
Marlin / Triton MoE kernels. Budget **30+ min** for a cold build and
expect a ~15 GB image (prebuilt flash-attn-4 + flashinfer wheels
dominate). The build itself does not need a GPU; the runtime does.
`drem-gq` is a small Go binary.

Rollback: the host-side launcher (operator's model-tuning
`start-sglang-gemma4-production.sh`) remains the canonical fallback if
the container build or bring-up fails. Same OpenAI-compatible endpoint
on `127.0.0.1:8081`, so drem clients don't notice.

#### Model-directory symlink caveat

If the model dir contains a "textonly" variant that shares weights with
the full multimodal dir via symlinks (as on the operator's reference
host — `gemma-4-26B-A4B-it-AWQ-4bit-textonly/` links into
`gemma-4-26B-A4B-it-AWQ-4bit/`), verify those symlinks are RELATIVE,
not absolute. Absolute symlinks that point outside the bind-mount
target (`/models` inside the container) dangle, producing
`ValueError: Couldn't instantiate the backend tokenizer` on startup.
One-shot fix:

```bash
cd $SGLANG_MODEL_DIR/gemma-4-26B-A4B-it-AWQ-4bit-textonly
for f in chat_template.jinja generation_config.json \
         model-0000{1,2,3,4}-of-00004.safetensors \
         processor_config.json README.md recipe.yaml \
         tokenizer_config.json tokenizer.json; do
  ln -sfn "../gemma-4-26B-A4B-it-AWQ-4bit/$f" "$f"
done
```

### 4f — Warm classifier

```bash
docker build -f deploy/docker/classifier.Dockerfile \
  -t localhost:5000/drem-classifier:latest . \
  && docker push localhost:5000/drem-classifier:latest

curl -s 127.0.0.1:5000/v2/_catalog
```

`drem-classifier` is the warm direct-classifier container described in
`plans/warm-direct-classifier.md`. A two-stage Go build (~1 min cold);
the runtime image is `debian:bookworm-slim` + `tini` + `wget` for the
compose-level `/healthz` check. Binary reads `DREM_CLASSIFIER_UPSTREAM`
(defaulting to `http://gq:8090/v1/chat/completions`) and
`DREM_AGENTMON_TOKEN` from its container env.

After 4a–4f the registry catalog should list 20 repositories:

```
drem-agentmon
drem-classifier
drem-csuite-{alex,base,mike,ross,seth}
drem-csuite-watcher
drem-docker-query-proxy
drem-gq
drem-kyle
drem-merger
drem-orch
drem-orch-dev
drem-planner
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

Expect seven containers: `registry`, `sglang`, `gq`, `spawner`, `kyle`,
`docker-query-proxy`, and `drem-classifier` (see Step 4f below).
`sglang`'s `start_period` is 120 s for cold model load; `gq` waits on
`sglang: service_healthy` before starting, and `drem-classifier` in
turn waits on `sglang: service_healthy` before serving /classify.

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

The merger AND planner images are *not* listed as running services;
they both spawn on-demand per task. The template declares
`merger-template` and `planner-template` stubs gated behind
`profiles: ["never"]` so `docker compose pull` primes those images
without running them.

### Spawn-on-demand agents

Two agent roles run as short-lived per-task containers rather than
warm pools: `merger` (one-shot Go binary that merges feature → main
and pushes) and `planner` (one-shot claude-CLI container that writes
`plan.json`). Both share the same spawner RPC path; the design and
rationale are captured in `plans/merger-spawn-on-demand-impl.md` and
`plans/warm-direct-planner.md`.

**Merger.** When a task reaches `StatusMerging`, the orchestrator's
`dispatchMerge` asks the spawner for a short-lived `drem-merger`
container with `/bare` mounted read-write and all six required
flags (`--feature-branch`, `--project`, `--task-id`, `--test-cmd`,
`--orch-url`, `--agentmon-token`) passed as argv. The container
runs one merge, POSTs a `merge_result` record to `/internal/logs`,
exits with a typed code (0=success, 2=conflict, 3=tests-failed,
4=push-failed, 1=misc), and the spawner removes it on the
Docker-event path.

**Planner.** When a task reaches `StatusPlanning` and the planner
provider resolves to `claude` (the default in the generated
drem.toml), the orchestrator's `dispatchPlan` asks the spawner for
a short-lived `drem-planner` container with `/bare` mounted
read-only, argv `--task-id / --branch / --prompt-file / --model /
--effort`, and `ANTHROPIC_API_KEY` forwarded via env. The container
clones the feature branch, runs the claude CLI in headless mode
against the clone, writes `plan.json` to the worktree root, and
exits. `dispatchPlan` reads plan.json back, validates it (subtasks
non-empty, every `tests_for` / `dependencies` index valid, TDD
pairing), and stores the result on the task so the next tick
advances to `plan_review`.

Exit codes per `plans/warm-direct-planner.md §7`: 0=success,
1=cli_error, 2=precondition_failed, 124/137=timeout, other=unknown.
Validation failures and `0 + missing plan.json` are surfaced via
`PlanResult.FailureReason` so processPlanning can retry with
feedback appended up to `MaxTotalPlannerSpawns`.

Watch either role with:

```bash
docker ps -a --filter label=drem.agent_type=merger
docker ps -a --filter label=drem.agent_type=planner
```

You should see exactly one entry per merged/planned task, all in
`Exited` state within seconds (merger) or 60-180s (planner) of
completion.

#### ANTHROPIC_API_KEY plumbing

The planner container calls out to `api.anthropic.com`, so the
`ANTHROPIC_API_KEY` must reach the orch container's env. The chain is:

1. Host operator exports `ANTHROPIC_API_KEY` in their shell (or sources
   it from a secrets file). The key is *not* checked into any repo or
   compose file.
2. `deploy/compose/global.yml` does NOT hold a long-lived reference to
   the key; every project's compose file (generated by `drem project
   register`) forwards the host env into `orch` via a standard
   docker-compose env passthrough.
3. The orch process reads `os.Getenv("ANTHROPIC_API_KEY")` at
   `dispatchPlan` time and populates `SpawnWorkerParams.Env` for the
   planner container.
4. Missing key → orch logs `planner_missing_api_key` and leaves the
   task in `StatusPlanning` for the next tick. No planner container is
   spawned (fail-closed).

Operators bringing up the per-project stack must ensure the variable
is in their shell environment before `docker compose up`:

```bash
# Before the per-project compose up:
export ANTHROPIC_API_KEY="sk-ant-…"
docker compose -f ~/.drem/projects/drem-orchestrator/compose.yml up -d
```

Verifying the key reached the orch container:

```bash
docker exec drem-orchestrator_orch_1 \
    sh -c 'echo ${ANTHROPIC_API_KEY:+set} ${ANTHROPIC_API_KEY:-unset}'
# expected: set
```

> The `csuite-watcher` service reads its bridge auth + listen + DB path from
> `DREM_BEARER_TOKEN` / `DREM_LISTEN_ADDR` / `DREM_DB_PATH` env vars (see the
> per-project compose template). Precedence is env > `drem.toml [serve]` >
> built-in default; the container has no `drem.toml` mounted and relies on
> the env block populated from `Project.SharedToken`.

## Warm direct agents

The direct classifier runs as a long-lived `drem-classifier` service on
`drem-net` (see `plans/warm-direct-classifier.md`). Every
newly-registered project's `drem.toml` ships with the classify endpoint
pre-set:

```toml
[agents.classifier]
  direct   = true
  endpoint = "http://drem-classifier:8090/classify"
  model    = "gemma4-26b"
```

…and the per-project `compose.yml` passes
`DREM_CLASSIFIER_URL=http://drem-classifier:8090/classify` to the
`orch` service so the env var wins over the toml key during a rolling
upgrade.

When orch sees a classify endpoint (via env or toml), it POSTs each
`CLASSIFYING` task to `drem-classifier` instead of running the SGLang
call inline in its own process. Benefits:

- Classifier LLM work can't starve orch's tick loop.
- Thread-exhaustion failure modes have a container-level restart policy
  plus `/healthz` as their boundary, not the orch process.
- Classifier can be scaled, paused, or swapped model-by-model without
  restarting orch.

### Container lifecycle

```bash
# Status + health.
docker compose -f deploy/compose/global.yml ps drem-classifier
docker inspect --format '{{.State.Health.Status}}' drem-classifier

# Tail structured JSON logs.
docker compose -f deploy/compose/global.yml logs -f drem-classifier

# Health endpoint directly — returns 200 ok / 503 unreachable.
docker exec drem-classifier wget -qO- http://localhost:8090/healthz

# Metrics (expvar JSON — request counters, duration sum, upstream_up).
docker exec drem-classifier wget -qO- http://localhost:8090/metrics | head -40
```

### Scaling

Single replica is enough for a typical classify rate. When you see
queue buildup (orch logs `direct classifier: API call failed: context
deadline exceeded` or observed `/classify` latency > 10 s at p95),
raise `deploy: replicas: N` under the `drem-classifier` service in
`deploy/compose/global.yml` and let compose load-balance across
replicas on `drem-net`. The binary is stateless — no coordination
needed.

### Rolling back to inline

Unset the endpoint (either clear `DREM_CLASSIFIER_URL` in
`~/.drem/projects/<name>/compose.yml` or drop `endpoint` from the
project's drem.toml), `docker compose up -d orch`, and orch reverts
to the inline `agent.RunDirectClassifier` path. The classifier
container can stay up; orch just ignores it.

### Same pattern coming for planner + prep

Planner and prep will land as their own warm services (see plan §9,
open questions). Each gets its own binary, Dockerfile, and
`[agents.<role>].endpoint` key following the classifier template.

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
