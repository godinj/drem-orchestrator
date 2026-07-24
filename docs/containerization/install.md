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

Builds and pushes: `drem-csuite-base`, `drem-csuite-{mike,alex,seth}`,
`drem-csuite-watcher`, `drem-orch`, `drem-orch-dev`.

### 4d — Worker images

```bash
bash deploy/docker/build-workers.sh
curl -s 127.0.0.1:5000/v2/_catalog
```

Builds and pushes: `drem-worker-base`, `drem-worker-go`, `drem-worker-cpp`.
`worker-cpp` is the slowest (C/C++ toolchain layer) — budget 10–15 min on
first build.

The direct-agent binary is embedded in each language worker image. After
changing direct-agent checkpoint, prompt, or budget behavior, rebuild and push
the worker image as well as `drem-orch`; rebuilding only the orchestrator leaves
ephemeral workers on stale behavior. Confirm the source-state labels on
`drem-orch`, `drem-spawner`, and the selected worker image match before a
measured pilot. Restart/deploy control-plane services with `--no-deps` so this
verification never restarts the warm SGLang service.

`[direct_tool_agent].*_max_cumulative_input_tokens` are cumulative replay-cost
ceilings, not SGLang context-window settings. The Canvas profile uses 65k for
tests, 90k for implementation, 75k for integration, and 30k for review. A paid
response that already mutated the repository is checkpointed at the ceiling;
an empty run fails closed.

`max_tool_calls` is a hard run-wide limit. The
`*_max_input_tokens_before_mutation` settings are earlier no-progress limits,
and the compatibility-named `*_max_reads_before_mutation` settings count all
reconnaissance rather than only the structured read tool. Scoped workers reject
all shell commands before mutation; use the declared
files and the generated planned-interface contract instead.

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
drem-csuite-{alex,base,mike,seth}
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

### 5c — Docker Desktop control plane with remote GQ/SGLang

This topology keeps the orchestrator, spawner, worker containers, project
database, and bare repository on the local machine. Only inference crosses an
SSH tunnel to a remote GQ. It is the supported path for native macOS project
verification; SGLang itself remains on the GPU host.

Open the loopback-only tunnel in a supervised foreground process:

```bash
export DREM_INFERENCE_SSH_HOST=user@gpu-host
export DREM_INFERENCE_SSH_PORT=22
export DREM_INFERENCE_SSH_IDENTITY="$HOME/.ssh/id_ed25519"
scripts/remote-inference-tunnel.sh
```

Verify GQ through the host endpoint before starting containers:

```bash
curl -fsS http://127.0.0.1:18090/v1/models
```

Docker Desktop containers reach that tunnel through
`host.docker.internal`. Start only the control-plane services; `--no-deps` is
required so Compose never starts the local SGLang/GQ pair:

```bash
export DREM_EXTERNAL_INFERENCE_ENDPOINT=http://host.docker.internal:18090/v1/chat/completions
docker compose \
  -f deploy/compose/global.yml \
  -f deploy/compose/remote-inference.override.yml \
  up -d --no-deps registry spawner drem-classifier drem-planner
```

On Apple Silicon, build the Drem and worker images locally so copied Go
binaries and image layers target Linux/arm64. Do not copy x86_64 worker images
from the GPU host.

## Step 6 — Register the first project

```bash
drem project register \
  --name drem-orchestrator \
  --bare  /home/godinj/git/drem-orchestrator.git \
  --language go
drem project list
```

For the remote-inference topology, add the container-visible endpoint:

```bash
drem project register \
  --name drem-canvas \
  --bare /Users/operator/git/drem-canvas.git \
  --language cpp \
  --orch-url http://127.0.0.1:8080 \
  --inference-endpoint http://host.docker.internal:18090/v1/chat/completions \
  --inference-model gemma4-26b \
  --integration-policy prepare_branch \
  --verification-policy external_ack
```

This keeps inference remote while project Git, build artifacts, verification,
and integration authority remain local. A successful worker parks an exact
artifact at `verification_ready`; it cannot advance the default branch without
native evidence and explicit integration authorization.

`--inference-model` must match the model ID returned by the endpoint's
`/v1/models` response. It is persisted in the project registry and rendered
for classifier, coder, reviewer, fixer, merger, and direct-tool roles. Existing
registrations that omit it retain the backward-compatible `gemma4-26b`
default. To switch a registered project without hand-editing generated files:

```bash
drem project register --update drem-canvas --force \
  --inference-model qwen3.6-27b-code
```

Generated direct-agent configuration also selects the OpenAI wire adapter for
the model family. Gemma retains object-form replay for historical SGLang tool
calls; Qwen and other OpenAI-compatible servers use string-form
`function.arguments`. Direct classifier and worker requests explicitly set
`chat_template_kwargs.enable_thinking = false` so short deterministic phases
cannot spend their entire response budget in an unobserved reasoning channel.
These are compatibility settings, not task-spec knobs; regenerate the project
instead of hand-editing them.

For Qwen 3.6, the validated local profile serves `qwen3.6-27b-code` through
vLLM while keeping the same loopback GQ/SSH boundary. SGLang remains a valid
Gemma backend, but the orchestration client depends only on the
OpenAI-compatible endpoint. Run the repository-free and Canvas C++ canaries
after changing either the model or the serving engine.

#### GPU host with native SGLang and containerized GQ

The GPU host may retain its canonical host-native SGLang launcher instead of
building the SGLang container. After the operator starts SGLang and verifies
`http://127.0.0.1:8081/v1/models`, recreate only GQ with the host overlay:

```bash
export DREM_HOST_SGLANG_ENDPOINT=http://host.docker.internal:8081
docker compose \
  -f deploy/compose/global.yml \
  -f deploy/compose/host-sglang.override.yml \
  up -d --no-deps gq
curl -fsS http://127.0.0.1:8090/v1/models
```

The override removes GQ's dependency on the compose-managed SGLang service and
adds the Linux Docker host-gateway mapping. It does not start, stop, or restart
the host inference process. Keep the GQ ports loopback-only, then use the SSH
tunnel and local control-plane commands above unchanged.

Before enabling a Canvas writer, run the repository-free inference,
non-integrating delivery, and repeated Computer Use stages in
[`docs/host-verification-canary.md`](../host-verification-canary.md). The first
stage is directly executable with `scripts/drem-remote-inference-canary.sh` and
does not read a checkout or mutate orchestration state.

Codex tasks and operators advance approval gates through the same HTTP-only
CLI. No gate mutation should edit `drem.db` directly:

```bash
export DREM_ORCH_URL=http://127.0.0.1:8080
export DREM_PROJECT=drem-canvas
export DREM_ORCH_TOKEN='<project shared token>'
export DREM_ACTOR='codex:<task-or-thread-id>'
dremctl tasks --status plan_review
dremctl accept-assumptions <task-id-prefix>
dremctl revise-plan <task-id-prefix> --spec task.json --reason "address reviewer findings"
dremctl reject <task-id-prefix> --reason "specific evidence-backed rework"

dremctl artifact <task-id-prefix>
dremctl verify <task-id-prefix> \
  --result pass \
  --environment 'macos-arm64:xcode' \
  --command 'scripts/dev verify' \
  --binary-sha256 '<sha256-if-produced>'
dremctl integrate <task-id-prefix>
```

For Canvas, use the host adapter rather than duplicating artifact parsing or
running project-native commands in the Linux control plane:

```bash
scripts/drem-canvas-pilot.sh doctor --base <canvas-base-sha> --min-free-gib 8
scripts/drem-canvas-pilot.sh start --spec plans/canvas-canary-task-spec.json
scripts/drem-canvas-pilot.sh revise <task-id-prefix> --spec plans/canvas-canary-task-spec.json --reason "address reviewer findings"
scripts/drem-canvas-pilot.sh await <task-id-prefix> --timeout 30m
scripts/drem-canvas-pilot.sh build <task-id-prefix>
# Launch the reported exact binary and capture each acceptance criterion with
# Computer Use, then submit the content-addressed interaction JSON:
scripts/drem-canvas-pilot.sh verify <task-id-prefix> \
  --worktree <reported-worktree> \
  --binary <reported-worktree>/build-debug/DremCanvas \
  --interactions interactions.json
scripts/drem-canvas-pilot.sh goal-usage <task-id-prefix> \
  --goal-objective "supervise Canvas task" --goal-status complete \
  --tokens-used <final-goal-tokens> --elapsed-ms <final-goal-elapsed-ms>
scripts/drem-canvas-pilot.sh report <task-id-prefix> --output canary-report.md
scripts/drem-canvas-pilot.sh report <task-id-prefix> --json --output canary-report.json
```

`doctor` is a pre-goal gate: do not activate subscription inference until it
confirms the registered base, control-plane connectivity, shared Skia cache,
writable evidence roots, local tools, and disk headroom. Phrase the explicit
Codex goal as “supervise this run to a measured terminal report.” A terminal
worker or verification failure is an experiment outcome, not a failure to
complete the supervisory goal.

Paired comparisons use one immutable contract:

```bash
scripts/drem-canvas-pilot.sh experiment-init --id <experiment> --spec task.json --base <sha>
scripts/drem-canvas-pilot.sh direct-prepare --base <sha> --run-id <experiment>-direct
scripts/drem-canvas-pilot.sh experiment-record --id <experiment> --arm orchestrated --outcome <status> --tokens <n> --elapsed-ms <n> --commit <sha> --task <task>
scripts/drem-canvas-pilot.sh experiment-record --id <experiment> --arm direct --outcome <status> --tokens <n> --elapsed-ms <n> --commit <sha> --binary <path> --evidence <json>
scripts/drem-canvas-pilot.sh experiment-report --id <experiment>
```

Each arm record is append-only and must descend from the frozen base. This
keeps the direct arm inside Drem’s worktree/evidence workflow without routing
its implementation through SGLang.

The adapter resolves the current delivery envelope again at submission time.
It refuses a worktree with the wrong HEAD, local source changes, a different
Git common directory, or a binary outside that worktree. A new artifact version
therefore invalidates an older prepared worktree even when its native build is
still present. `cleanup` similarly refuses dirty pilot worktrees.

The report endpoint correlates the parent and its immediate child tasks, every
durable worker attempt, cumulative time spent in repeated lifecycle phases,
artifact versions, host-rework sessions/submissions, native verification, and
Computer Use interactions. Warm direct reviewer usage is persisted as a
task-correlated `inference_usage` event; container workers use the durable
WorkerAttempt token fields. Measurement coverage distinguishes an actual zero
from a historical run that predates terminal usage capture. A measured Codex
pilot begins only after `doctor` passes and an explicit supervisory goal is
created. Once the run has a terminal measured outcome, complete the goal, take
the final token and
elapsed values returned by Codex, submit them with `goal-usage`, and regenerate
the report. The append-only record is actor/thread attributed and idempotent;
it is reported separately from SGLang input/output tokens.

Canvas embeds the current Git branch in its title. A detached exact-artifact
worktree reports `HEAD`; when a criterion explicitly requires a representative
branch label, build the same frozen SHA in a temporary local branch context,
record that context in the interaction step, then detach back to the frozen SHA
and remove the temporary ref before submission. The commit and source tree must
remain unchanged.

When a failed child has a deterministic host repair on its canonical branch,
`dremctl adopt <child-id> --commit <sha>` re-runs immutable-base scope admission
and merges only the accepted head. It is not a general force-complete command:
active attempts, a non-failed child/parent, ref drift, or out-of-scope paths are
refused.

Computer Use evidence is supplied as a JSON array with one object per
acceptance criterion. Each object records its criterion ID, scenario, ordered
steps, observed result, content-addressed media references, exact binary hash,
application version, host fingerprint, PID, result, and discrepancy. Pass the
file with `verify --interactions interactions.json`. A failed verification must
also choose its route explicitly:

```bash
# Return broad or architectural work to an orchestrated worker.
dremctl verify <task-id-prefix> \
  --result fail --environment 'macos-arm64:xcode' \
  --command 'scripts/dev verify' --binary-sha256 '<canvas-binary-sha256>' \
  --interactions interactions.json \
  --failure-mode orchestrated --failure-reason 'acceptance contract changed'

# Reserve a semantically bounded repair for this same Codex task.
dremctl verify <task-id-prefix> \
  --result fail --environment 'macos-arm64:xcode' \
  --command 'scripts/dev verify' --binary-sha256 '<canvas-binary-sha256>' \
  --interactions interactions.json \
  --failure-mode host-direct --failure-reason 'toolbar hit target is offset' \
  --scope src/ui/Toolbar.cpp --scope tests/gui/test_toolbar.cpp \
  --host-direct-attest-bounded
dremctl submit-rework <task-id-prefix> \
  --session '<session-uuid>' --commit '<canonical-feature-ref-sha>'
```

The host-direct attestation asserts that the repair preserves acceptance
criteria, dependency shape, persistence/schema, security/authentication,
cross-process ownership, and build/release policy. The server enforces one
actor-owned session, no active worker attempt, exact canonical-ref equality,
allowed-path scope, and a clean worktree. Use `abandon-rework` to release the
session to orchestrated implementation when any of those assumptions stops
being true.

Integration authorization also writes an immutable merge intent before the
task enters `merging`. On every merge tick, the orchestrator reads the
authoritative target ref directly from the local bare repository and completes
the task only when that ref contains the intent's exact artifact commit. It
checks both before and after merger-container dispatch, so a successful push is
recoverable even if the container exits unexpectedly or Agentmon never delivers
its result. `/internal/logs` merge reports remain useful diagnostics, but are
not part of the correctness path.

`approve` works only for `plan_review` and `test_review`. `testing_ready` is an
automated preparation state. The older `pass` and `fail` commands remain
recognized but fail closed for delivery states; use `verify --result pass`,
`verify --result fail`, or `request-rework --mode orchestrated|host-direct
--reason ...`.

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
merger clones the integration branch fresh into a disposable
workspace on every run (see `internal/merger/merger.go`), so it
reads the bare repo's refs directly and never touches the host
worktree.

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

Generated `drem.toml` includes language-default gate commands. For C++
projects, registration writes a CMake build-and-ctest `test_command`
and a CMake build `compile_command`; existing projects pick these up by
re-running `register --update`.

```bash
# Review what would change (no writes, no side effects).
drem project register --update drem-orchestrator --dry-run

# Apply. Operators will see a drift summary if the on-disk files
# have hand-patches the template doesn't produce; add --force to
# overwrite them.
drem project register --update drem-orchestrator
drem project register --update drem-orchestrator --force

# Apply the same template update to an existing C++ project.
drem project register --update drem-canvas

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

Expect: `orch`, `agentmon`, `csuite-watcher`, `csuite-{mike,alex,seth}`.

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

All worker spawns now include image readiness at the spawner boundary. Docker
first inspects the configured image and, only when missing, performs one
serialized pull before `ContainerCreate`. If that bounded operation fails, the
task records `worker_image_unavailable` and stops rather than generating a
tick-driven spawn storm. Restore the registry/image, then use the ordinary
`dremctl retry` operation.

For `sglang-direct` workers, `InspectWorker` reads only the last 200 log lines
after termination and returns the harness's iterations, input/output tokens,
and stop reason. The orchestrator persists those values on the immutable
attempt and public agent record before it applies completion. Historical rows
created before this behavior remain zero; no estimate is backfilled.

**Merger.** When a task reaches `StatusMerging`, the orchestrator's
`dispatchMerge` asks the spawner for a short-lived `drem-merger`
container with `/bare` mounted read-write and all six required
flags (`--feature-branch`, `--project`, `--task-id`, `--test-cmd`,
`--orch-url`, `--agentmon-token`) passed as argv. The container
runs one merge, POSTs a telemetry-only `merge_result` record to
`/internal/logs`, exits with a typed code (0=success, 2=conflict, 3=tests-failed,
4=push-failed, 1=misc), and the spawner removes it on the
Docker-event path. Task state is derived from the persisted merger
`WorkerAttempt` plus the authoritative target ref; loss, delay, or duplication
of the telemetry record cannot approve or fail the merge.

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

## C-Suite personas: the persona poller runtime

Wave 2 of the csuite-docker pivot (2026-04-20) replaced the long-lived
interactive `claude` process that served as each persona container's
entrypoint with the `csuite-persona` headless poller baked into
`drem-csuite-base`. Before the pivot, `csuite-run.sh` did
`exec claude --print --system-prompt-file …` and then just sat there —
the CLI had no mechanism to notice files appearing in the persona's
inbox, so the dispatcher's "drop a message into the project's C-Suite root"
pattern reached a dead end. The poller closes that gap.

2026-04-24 correction: the shipped poller invokes OpenCode, not
`claude -p`. Older prompt text and plans that describe `claude -p` as
the persona subprocess are historical unless they explicitly describe an
interactive Claude runtime.

### What the poller does

On each tick (default 2s) the binary at `/usr/local/bin/csuite-persona`
scans `/home/drem/.drem-csuite/<persona>/inbox/` for `*.md` files. For
each file, oldest-first by mtime:

1. Reads the message body.
2. Invokes `opencode run --format json --agent build --dir /home/drem`
   as a subprocess with a 5-minute default timeout (configurable via
   `-claude-timeout`, a legacy flag name retained for compatibility).
   The poller resolves the model immediately before each invocation from
   `~/.drem-csuite/<persona>/config.json`, then `DREM_OPENCODE_MODEL`,
   then `DREM_CODEX_MODEL`, falling back to `openai/gpt-5.5`. The
   poller embeds the persona prompt and inbox body into OpenCode's final
   positional prompt argument.
3. On exit 0: signals any well-formed outbox files the persona wrote,
   suppresses malformed stdout stubs, atomically replaces
   `~/.drem-csuite/<persona>/state.md` with a structured record of the
   last-processed message, and moves the original inbox file to
   `~/.drem-csuite/<persona>/inbox/.archive/`.
4. On non-zero exit or spawn error: bumps a sidecar counter
   (`<name>.failures`) and leaves the message in the inbox for the
   next tick. After three failures (configurable via `-max-failures`)
   the file is archived as `<name>.failed` so the loop moves on.

Structured logs go to stdout via slog's JSON handler, which means
`docker logs csuite-<persona>` is machine-parseable without extra
formatting.

Protocol invariants: personas must not manually move, rename, or archive
inbox files; the poller owns archive, lease, retry, and processing state.
ACK/receipt messages are terminal and must not receive replies. One
inbound normally yields at most one substantive response. Status output
must report `inbox`, `acks`, `outbox`, `db_unread`, and `event_unacked`
as separate counts.

### Directory layout inside the container

```
/home/drem/
├── .claude/
│   ├── .credentials.json   # bind-mounted read-only from host (Wave 1)
│   └── settings.json       # baked into the image (theme preseed)
├── .claude.json            # baked (onboarding skip flags)
├── .drem-csuite/
│   └── <persona>/
│       ├── inbox/
│       │   ├── *.md            # pending messages
│       │   ├── *.md.failures   # sidecar counters (for retries)
│       │   └── .archive/       # processed and failed messages
│       ├── outbox/
│       │   └── <ts>-<persona>-reply-<shortid>.md
│       ├── config.json         # optional model override
│       └── state.md            # last-processed record

/opt/csuite/prompts/
├── mike.md
├── alex.md
└── seth.md                 # --system-prompt value for the CLI
```

The whole in-container `.drem-csuite/<persona>/` tree is bind-mounted from
the host at `~/.drem/projects/<project>/csuite/<persona>/` so an operator
running `ls ~/.drem/projects/<project>/csuite/seth/outbox/` on the host sees
the replies without `docker cp`. Older registrations used the global
`~/.drem-csuite/<persona>/` root; re-rendering compose after the isolation
pivot moves new project state under the per-project root unless an explicit
`CsuiteHomeRoot` override is configured.

### Persona model selection

`csuite-chat` can switch the active persona between `GPT 5.5`
(`openai/gpt-5.5`) and `GPT 5.4 Mini` (`openai/gpt-5.4-mini`) without
restarting containers. Press `F8` in the chat TUI, choose the model, and
confirm with Enter. The bridge writes the selected model to
the active C-Suite root's `<persona>/config.json`; the persona poller applies
it on the next `opencode run`, not to any turn already in flight. For generated
per-project compose, that host path is
`~/.drem/projects/<project>/csuite/<persona>/config.json`.

### Authentication — subscription-only

The poller never reads or sets `CLAUDE_CODE_OAUTH_TOKEN`,
`ANTHROPIC_API_KEY`, or `ANTHROPIC_AUTH_TOKEN`. The shipped persona
runtime uses OpenCode with the Codex subscription auth file bind-mounted
read-only at `/home/drem/.codex/auth.json`; OpenCode reads it through
the pinned `@guard22/opencode-multi-auth-codex@1.4.3` multi-auth plugin.
The baked OpenCode config at
`deploy/docker/context/opencode-codex-subscription.json` mirrors the
portable Codex-subscription config from dotfiles commit `145d16f`, so
the same `codex login` OAuth state is used on the host and inside
C-Suite persona containers.
If that file is missing or expired,
each `opencode run` invocation fails and the message hits the .failures
retry path. Refresh the subscription auth on the host; do not add API
token env-var fallback.

### Operator runbook — upgrading a pre-pivot project

The bind-mounts that the poller depends on (per-persona
`~/.drem/projects/<project>/csuite/<persona>/`, the credentials file, the settings
preseed) landed in the per-project compose template in Wave 1 and the
image switch is this commit sequence. A project registered before Wave
1 needs the mounts regenerated AND the image rebuilt:

1. **Re-render `compose.yml` for each project that was registered
   before Wave 1.** This picks up the bind-mounts and any other
   template drift:

   ```bash
   drem project register --update <name>
   # or with --force to stomp a hand-patched compose.yml
   drem project register --update <name> --force
   ```

2. **Rebuild the csuite images with the poller baked in.** The build
   script pre-compiles `cmd/csuite-persona` into the Docker build
   context before invoking `docker build`:

   ```bash
   bash deploy/docker/build-csuite.sh
   ```

   This refreshes `drem-csuite-base` and
   `drem-csuite-{mike,alex,seth}` in the local registry at
   `localhost:5000` and pushes them. It also builds
   `drem-csuite-watcher` from `./cmd/csuite-watcher`; if that command is
   missing, the build-contract test in `deploy/docker` should fail before
   an image build is attempted.

3. **Edit `~/.drem/projects/<name>/compose.override.yml` to remove
   the obsolete `csuite-run.sh` bind-mount** from every csuite-*
   service. The file lives on the operator's host outside the repo.
   With the Wave-2 image the poller is baked into the image and no
   longer needs an override. Lines that look like the following on each
   of `csuite-mike`, `csuite-alex`, `csuite-seth` should
   be deleted:

   ```yaml
   # Remove these if present:
   - type: bind
     source: <repo>/deploy/docker/context/csuite-run.sh
     target: /usr/local/bin/csuite-run.sh
     read_only: true
   ```

4. **Recreate only the persona services** so the compose graph does
   not cascade into SGLang's CUDA-graph recapture. `--no-deps` is
   mandatory (per CLAUDE.md "Do not run `docker compose up` without
   `--no-deps` when scoping to a subset of services"):

   ```bash
   docker compose -f ~/.drem/projects/<name>/compose.yml \
       up -d --no-deps --force-recreate \
       csuite-mike csuite-alex csuite-seth
   ```

5. **Verify** with a round-trip test against one persona. Seth is a
   good default target since he is the CTO and the prompt is the
   smallest:

   ```bash
   CSUITE_ROOT=~/.drem/projects/<name>/csuite
   cat > "$CSUITE_ROOT/seth/inbox/$(date +%Y%m%dT%H%M%S)-smoke.md" <<'EOF'
   Smoke test — reply with a single sentence.
   EOF
   # Wait a few seconds, then check:
   ls "$CSUITE_ROOT/seth/outbox/"
   cat "$CSUITE_ROOT/seth/state.md"
   ls "$CSUITE_ROOT/seth/inbox/.archive/"
   ```

   Expected outcome: the smoke-test file moved from inbox to archive,
   one new file in outbox, `state.md` shows `last_status: ok`. If the
   message sticks in the inbox with a `.failures` sidecar, check
   `docker logs csuite-seth` for the reason — the most common cause is
   a missing or expired OpenCode/Codex subscription auth file on host.

### Deferred items (tracked in `plans/csuite-persona-pivot.md`)

- fsnotify-based inbox wake-up (the first cut is a polling loop).
- Cross-message memory support for the current OpenCode persona runtime.
- A worker-base preseed analogous to this one (separate drive-by task).

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
  model    = "<registered inference model>"
```

…and the per-project `compose.yml` passes
`DREM_CLASSIFIER_URL=http://drem-classifier:8090/classify` to the
`orch` service so the env var wins over the toml key during a rolling
upgrade.

The warm classifier also selects its upstream model at process start. When a
project switches models, recreate only `drem-classifier` with
`DREM_CLASSIFIER_MODEL` set to the same served ID; do not restart unrelated
inference or planner services.

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

This one prerequisite covers the Claude-backed consumers in the stack:
- **drem-planner** — the warm planner service documented above.
- **drem-worker-{go,cpp} coder / reviewer / fixer / tester /
  supervisor** — ephemeral per-task workers spawned by orch through
  `drem-spawner`. See plans/worker-subscription-auth.md for the
  end-to-end design.

The C-Suite persona poller is a separate OpenCode runtime. It uses the
OpenCode/Codex subscription auth mount described in "C-Suite personas:
the persona poller runtime" and does not consume Claude API tokens.

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
   the path resolves without `CLAUDE_CONFIG_DIR` overrides. The planner
   mounts the file from `deploy/compose/global.yml`; worker containers
   get it via orch — see the "Worker mount path" note below. C-Suite
   personas may retain the mount for rollback/debugging, but their
   current poller path is OpenCode.

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

`ListWorkers` is a lifecycle observation, not an absence detector. A listed
terminal worker is safe for the orchestrator to consume; a missing worker does
not cause automatic failure or respawn. If an attempt appears stuck after a
spawner restart or inventory gap, inspect its persisted `WorkerAttempt` and
recover it explicitly rather than deleting task or attempt records.

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
