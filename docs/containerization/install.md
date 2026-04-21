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

Note: `drem-planner` is a multi-stage image (golang:1.25-bookworm
compile stage + debian:bookworm-slim runtime that layers node + the
`@anthropic-ai/claude-code` npm package + the compiled
`cmd/drem-planner` HTTP server). The container runs long-lived and
orch reaches it over HTTP at `POST /plan` — see
`deploy/docker/planner.Dockerfile` and
`plans/warm-planner-pivot.md` for the design.

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

Register also sets `receive.denyCurrentBranch=ignore` on the target
bare repo so the worker watchdog's final `git push` (issued from
inside a container with the bare repo bind-mounted at `/bare`)
succeeds against our shared-workspace bare (which has host worktrees
checked out under it; a plain bare would reject the push, and
`updateInstead` can't follow through because the worktree path is
host-absolute and not visible inside the container). Verify with:

```bash
git -C /home/godinj/git/drem-orchestrator.git config --get receive.denyCurrentBranch
# → ignore
```

Host worktrees go stale after a worker pushes, but that's fine: the
merger always runs `git fetch --all && git reset --hard` before its
work, so it reads the freshest tree.

See `plans/bare-repo-denyCurrentBranch.md` for the design. Migrators
with an existing project from before this change can either re-run
`drem project register --update <name> --force` (reapplies it
alongside any template drift) or back-fill directly with `git -C
<bare> config receive.denyCurrentBranch ignore`. If you previously
set `updateInstead` manually, re-running register or the back-fill
with `ignore` overwrites it.

> Known wiring gap (carried from Tier 3, see `remaining-work.md`
> §Known caveats): `cmd/drem/project.go`'s `templateDataFor` uses
> `uuid.NewString()` for the shared token and leaves `OrchHostPort`,
> `DevMode`, and `OrchImage` to `applyDefaults`. The template renders,
> but the first multi-project register is untested. Fix planned before
> registering a second project.

### Regenerating compose.yml + drem.toml — `register --update`

Every plan that lands a new env var, bind-mount, or config key on
the template adds template output the on-disk per-project files
don't have. `drem project register --update` re-renders the
per-project compose.yml + drem.toml from current master templates
while preserving state that can't be regenerated from the registry
alone (SharedToken above all).

```bash
# Review what would change (no writes, no side effects).
drem project register --update drem-orchestrator --dry-run

# Apply. Operators will see a drift summary if the on-disk files
# have hand-patches the template doesn't produce; add --force to
# overwrite them.
drem project register --update drem-orchestrator
drem project register --update drem-orchestrator --force

# Bring up the stack with the new template output.
docker compose -f ~/.drem/projects/drem-orchestrator/compose.yml up -d
```

Preserved across an update:
- **SharedToken** (`DREM_AGENTMON_TOKEN` on orch + agentmon +
  `DREM_BEARER_TOKEN` on csuite-watcher). Extracted from the
  on-disk compose.yml. A missing token is fail-closed — the update
  refuses to proceed without `--regenerate-token`, because silent
  rotation would 401 every running service.
- **OrchHostPort**. Registry-carried (`Project.OrchHostPort`); when
  zero, falls back to the port observed in the on-disk compose.
- **DevMode** and **ContainerImageOverrides**. Registry-carried.
- **compose.override.yml** and any operator-owned sidecar files
  (`csuite-run.sh`, etc.). The update writes ONLY `compose.yml`
  and `drem.toml`.

Reset auth intentionally (operator action, restart required):

```bash
# Rotate SharedToken — restart orch + agentmon + csuite-watcher
# after this so every service picks up the new token.
drem project register --update drem-orchestrator --regenerate-token --force
docker compose -f ~/.drem/projects/drem-orchestrator/compose.yml up -d --force-recreate
```

CI invocation — block merges that would drift the on-disk compose:

```bash
drem project register --update drem-orchestrator --fail-on-drift --home-dir /path/to/sandbox
# exit 0 if no drift; exit 1 and prints the diff if drift exists.
```

See `plans/drem-project-register-update.md` for the full design.

## Step 7 — Bring up the per-project stack

```bash
docker compose -f ~/.drem/projects/drem-orchestrator/compose.yml up -d
docker compose -f ~/.drem/projects/drem-orchestrator/compose.yml ps
```

Expect: `orch`, `agentmon`, `csuite-watcher`, `csuite-{mike,alex,ross,seth}`.

The merger image is *not* listed as a running service; it spawns
on-demand per task. The template declares a `merger-template` stub
gated behind `profiles: ["never"]` so `docker compose pull` primes
the image without running it. The planner, by contrast, is a
long-lived warm service in `deploy/compose/global.yml` (see
"Warm direct agents" below) — orch reaches it over HTTP.

### Spawn-on-demand agents

One agent role still runs as a short-lived per-task container:
`merger` (one-shot Go binary that merges feature → main and pushes).
Design and rationale: `plans/merger-spawn-on-demand-impl.md`.

**Merger.** When a task reaches `StatusMerging`, the orchestrator's
`dispatchMerge` asks the spawner for a short-lived `drem-merger`
container with `/bare` mounted read-write and all six required
flags (`--feature-branch`, `--project`, `--task-id`, `--test-cmd`,
`--orch-url`, `--agentmon-token`) passed as argv. The container
runs one merge, POSTs a `merge_result` record to `/internal/logs`,
exits with a typed code (0=success, 2=conflict, 3=tests-failed,
4=push-failed, 1=misc), and the spawner removes it on the
Docker-event path.

Watch with:

```bash
docker ps -a --filter label=drem.agent_type=merger
```

You should see exactly one entry per merged task, all in `Exited`
state within seconds of completion.

> The `csuite-watcher` service reads its bridge auth + listen + DB path from
> `DREM_BEARER_TOKEN` / `DREM_LISTEN_ADDR` / `DREM_DB_PATH` env vars (see the
> per-project compose template). Precedence is env > `drem.toml [serve]` >
> built-in default; the container has no `drem.toml` mounted and relies on
> the env block populated from `Project.SharedToken`.

## Warm direct agents

Two roles run as long-lived warm services on `drem-net` rather than
per-task spawns: the **classifier** (against SGLang/gq) and the
**planner** (against Anthropic Opus via the claude CLI).

### Warm classifier

The direct classifier runs as a long-lived `drem-classifier` service
(see `plans/warm-direct-classifier.md`). Every newly-registered
project's `drem.toml` ships with the classify endpoint pre-set:

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

#### Container lifecycle

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

#### Scaling

Single replica is enough for a typical classify rate. When you see
queue buildup (orch logs `direct classifier: API call failed: context
deadline exceeded` or observed `/classify` latency > 10 s at p95),
raise `deploy: replicas: N` under the `drem-classifier` service in
`deploy/compose/global.yml` and let compose load-balance across
replicas on `drem-net`. The binary is stateless — no coordination
needed.

#### Rolling back to inline

Unset the endpoint (either clear `DREM_CLASSIFIER_URL` in
`~/.drem/projects/<name>/compose.yml` or drop `endpoint` from the
project's drem.toml), `docker compose up -d orch`, and orch reverts
to the inline `agent.RunDirectClassifier` path. The classifier
container can stay up; orch just ignores it.

### Warm planner

The planner runs as a long-lived `drem-planner` service (see
`plans/warm-planner-pivot.md`). Orch POSTs `/plan` with the full task
+ project context; the container shells out to the `claude` CLI per
request against Anthropic Opus and returns `plan.json` inline in the
response body. Every newly-registered project's `drem.toml` ships with
planner provider pre-set to claude:

```toml
[agents.planner]
  provider = "claude"
  model    = "claude-opus-4-6"
  effort   = "high"
```

…and the per-project `compose.yml` passes
`DREM_PLANNER_URL=http://drem-planner:8090/plan` to `orch` so orch
routes plan jobs through the HTTP path.

#### Claude subscription auth (prerequisite)

**Every Claude-backed role in the drem stack uses subscription auth
only. No `ANTHROPIC_API_KEY` fallback anywhere in the default path.**
Per operator direction 2026-04-20, the whole agent fleet shares the
operator's Claude Max rate-limit pool so API-key spending stays
reserved for work that truly needs it.

This one prerequisite covers three consumers in the stack:

- **csuite agents** (mike / alex / ross / seth) — long-lived warm
  containers that run the claude CLI inside their entrypoint.
- **drem-planner** — the warm planner service documented above.
- **drem-worker-{go,cpp} coder / reviewer / fixer / tester /
  supervisor** — ephemeral per-task workers spawned by orch through
  `drem-spawner`. See plans/worker-subscription-auth.md for the
  end-to-end design.

The merger role (`drem-merger`) is a Go binary, does NOT run the
claude CLI, and deliberately skips this mount.

Implications for the operator:

1. **Run `claude login` on the host once.** This writes
   `~/.claude/.credentials.json` with the refreshable OAuth token that
   every consumer above reads.

   ```bash
   claude login
   ls -l ~/.claude/.credentials.json   # must exist, must be readable
   ```

2. **The credentials file is bind-mounted read-only** into each
   consumer at `/home/drem/.claude/.credentials.json`. Every container
   runs as UID 1000 `drem` (matches the operator's typical host UID) so
   the path resolves without `CLAUDE_CONFIG_DIR` overrides. csuite
   agents declare the mount in
   `~/.drem/projects/drem-orchestrator/compose.override.yml`; the
   planner mounts the file from `deploy/compose/global.yml`; worker
   containers get it via orch — see the "Worker mount path" note below.

3. **The bind-mount is read-only on purpose.** Host `claude` CLI
   interactive sessions own OAuth refresh; each container only reads
   the file fresh on invocation. Making the mount read-write would let
   a container overwrite refresh tokens the host then has to reconcile,
   creating drift between host and container state.

4. **Boot-time validation** (planner only): the planner binary
   validates the credentials file at startup and exits 1 with a loud
   error if the file is missing. `restart: unless-stopped` then
   crash-loops the container visibly in `docker ps` until the operator
   runs `claude login`. Never silent.

5. **Dispatch-time validation** (planner only): orch's
   `dispatchPlanHTTP` probes the planner's `/healthz` before POSTing.
   `/healthz` returns 503 when either the credentials file is
   unreadable OR `claude --version` fails in <2s, so orch fails fast
   on missing auth instead of waiting 5 minutes for an Anthropic 401.

6. **Spawn-time validation** (workers only): workers are ephemeral —
   no `/healthz` to poll. Instead, orch passes the host creds path to
   drem-spawner on every SpawnWorker RPC, and the spawner `stat(2)`'s
   the path before creating the container. A missing file surfaces as
   a `worker_spawn_failed` event with the exact path that was not
   found, plus the hint to run `claude login`.

7. **No API-key env var anywhere.** The generated compose files do
   NOT set `ANTHROPIC_API_KEY` on orch, on the planner, or on any
   worker spawn params. `internal/orchestrator/worker_spawn.go`
   additionally rejects an `ANTHROPIC_API_KEY` key if one ever lands
   in the spawn env — fail-closed with
   `reason=policy_violation_api_key` in the audit trail. If you really
   want API-key access for an ad-hoc test, set the env manually on the
   target container via `docker run`; the default path never touches
   it.

##### Worker mount path — how orch knows the host path

The per-project compose file renders `DREM_WORKER_CREDS_PATH` on orch,
populated at `drem project register` time from `os.UserHomeDir()` on
host:

```yaml
services:
  orch:
    environment:
      DREM_WORKER_CREDS_PATH: "/home/<operator>/.claude/.credentials.json"
```

At spawn time, `buildSpawnContext` reads that env var and copies it
into `spawner.SpawnWorkerParams.CredsMount`. The spawner bind-mounts
it at `/home/drem/.claude/.credentials.json` inside the worker with
the read-only flag set. The worker's `worker-base` image pre-creates
`/home/drem/.claude` with `drem:drem` ownership so docker does not
auto-create the parent as root and block the claude CLI's own writes
to `~/.claude/` (session state, project caches).

##### Worker prompt delivery — how the coder sees its task

Subscription auth gets a worker container into a state where the
claude CLI can authenticate. It still needs something to DO. Prompt
delivery is the companion mechanism: orch renders a per-task markdown
prompt, writes it atomically to a host dir, and the spawner
bind-mounts that one file into the worker.

1. **Host prompt dir**, per project:

   ```
   ~/.drem/projects/<project>/prompts/
       └── <task-uuid>.md      # one per task, written at spawn time
   ```

   `drem project register` pre-creates the directory. The per-project
   compose renders `DREM_PROMPT_ROOT_HOST` on the `orch` service AND
   bind-mounts the same host-identical path read-write so orch can
   `os.WriteFile` into it.

2. **Orch renders the prompt** inside `buildSpawnContext` via
   `internal/prompt.Generate`, the same function the legacy host
   runner uses. Content is task-specific: title, description, repo
   map (if present), agent-type-specific instructions, per-phase TDD
   guidance, prior comments, etc.

3. **Atomic write:** orch writes `<task-uuid>.md.tmp` then
   `rename(2)`s to `<task-uuid>.md`. The worker's spawn call cannot
   race a partial file — rename is atomic within a filesystem, so
   the file either does not exist or is complete.

4. **Spawner bind-mounts read-only** at
   `/home/drem/.drem/prompt.md` inside the worker and injects
   `DREM_PROMPT_PATH=/home/drem/.drem/prompt.md` deterministically —
   callers cannot regress the target by setting a conflicting env
   key. `worker-base.Dockerfile` pre-creates `/home/drem/.drem` with
   `drem:drem` ownership so the bind target's parent isn't
   auto-created as root.

5. **Worker entrypoint** reads the env var, execs claude:

   ```bash
   exec claude --dangerously-skip-permissions "$(cat /home/drem/.drem/prompt.md)"
   ```

   See `deploy/docker/context/worker-entrypoint.sh:132-160`.

6. **Spawn-time validation** (workers only): orch passes the host
   prompt path to drem-spawner on every SpawnWorker RPC; the spawner
   `stat(2)`s the path before creating the container. A missing file
   fails the spawn fast with a `worker_spawn_failed` event whose
   `reason=prompt_render_failed` — no silent interactive-claude
   fallback.

7. **Artifact retention:** prompt files are NOT deleted on task
   completion. They remain on host as post-mortem evidence
   (`ls ~/.drem/projects/<project>/prompts/` shows every task that
   spawned a worker). Operator may `rm -rf` between canary runs for a
   clean slate; a future GC plan can wire this to task-terminal
   transitions if size pressure emerges.

Debug runbook — "the worker spawned but claude never got started":

```bash
# Is the prompt file actually on host?
ls -la ~/.drem/projects/drem-orchestrator/prompts/

# Is orch's view of the host root correct?
docker exec drem-orchestrator-orch-1 env | grep DREM_PROMPT_ROOT_HOST

# Is the file mounted inside the worker?
docker inspect <worker-container-id> | jq '.[0].Mounts[]'

# What did the entrypoint log say?
docker logs <worker-container-id> | grep -E 'execing claude|interactive mode'
```

##### Rotation and the already-running-worker caveat

`claude login` on host refreshes the file in place. New worker spawns
pick up the refreshed file naturally. An already-running worker has
the old file open; if the CLI re-opens on each invocation (its
default), rotation is transparent. If the refresh uses `rename(2)`
atomic replacement, a long-lived worker could retain an old inode and
eventually hit a 401 on token expiry. Current worker lifecycle is per-
task (minutes to low-hours), so this is unlikely to bite — but worth
noting: if a worker 401s mid-task, `docker restart <container>` picks
up the fresh file.

#### Container lifecycle

```bash
# Status + health.
docker compose -f deploy/compose/global.yml ps drem-planner
docker inspect --format '{{.State.Health.Status}}' drem-planner

# Tail structured JSON logs.
docker compose -f deploy/compose/global.yml logs -f drem-planner

# Health endpoint directly — returns 200 ok / 503 unreachable.
docker exec drem-planner wget -qO- http://localhost:8090/healthz

# Metrics (expvar JSON — request counters, duration sum, credentials_readable).
docker exec drem-planner wget -qO- http://localhost:8090/metrics | head -40
```

#### Scaling

Single replica with serialized requests by default. Claude CLI is one
subprocess per invocation, and the Claude Max subscription rate limit
is shared across every claude-backed consumer (including the
operator's own interactive sessions). Raise concurrency with a
`-concurrency` flag only once rate-limit metrics show headroom.

#### Rolling back to the legacy subprocess path

Clear `DREM_PLANNER_URL` in `~/.drem/projects/<name>/compose.yml`
AND drop `endpoint` from `[agents.planner]` in the project's
drem.toml, then `docker compose up -d orch`. Orch reverts to the
legacy `runner.SpawnAgent` subprocess path (requires a host-side
claude binary). The planner container can stay up; orch ignores it.

### Same pattern coming for prep

Prep will land as its own warm service (see
`plans/warm-direct-prep.md`). It gets its own binary, Dockerfile,
and `[agents.prep].endpoint` key following the classifier template.

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
