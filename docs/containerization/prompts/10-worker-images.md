# Agent: Worker Container Images (Go and C++)

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent task orchestrator. Your task is Phase 2 work for the containerization initiative: build the per-language worker container images (`drem-worker-go`, `drem-worker-cpp`) that host Claude Code agents with the baked-in `drem-watchdog` binary, the `claude` CLI, and the language-specific toolchain. These images are what the spawner (prompt 07) launches for every coder and G4 worker task.

## Context

Read these specs before starting:

- `docs/prd-containerization.md` (sections: "Images" → per-language worker images; "Container filesystem model"; "Lifecycle and recovery"; user stories 32, 33, 43, 44, 45, 46)
- `cmd/drem-watchdog/` and `internal/watchdog/` (prompt 04's binary — baked into every worker image)
- Any existing host-side scripts documenting how `claude` CLI and `opencode` are installed (search `scripts/`, `infra/`, `deploy/` for references). If the project vendors a pinned `claude` install, use the same pin; otherwise use the latest npm-published version.

## Dependencies

- Prompt 04 (`cmd/drem-watchdog/`) — the watchdog binary must be buildable. If not yet available, the Dockerfile's build stage can `go build` against a stub `package main` that prints an error and exits; replace once prompt 04 lands.

## Deliverables

### New files

#### 1. `deploy/docker/worker-base.Dockerfile`

Shared base image used by both language-specific images. Keeps the common surface in one place so maintenance touches one file.

- Base: `debian:bookworm-slim`
- Install: `git`, `ca-certificates`, `curl`, `jq`, `nodejs` (for `claude` CLI), `npm`, `tini` (clean signal handling), `sudo`, `openssh-client` (for git+ssh if needed)
- Create non-root user `drem` with UID 1000, home `/home/drem`
- Install `@anthropic-ai/claude-code` globally via `npm install -g @anthropic-ai/claude-code` (pin to the version the project currently uses)
- Install `opencode` via the project's canonical method (check `scripts/` for an existing install hint; if none, use `curl -fsSL https://opencode.ai/install | bash` and pin the version via env var)
- Copy the pre-built `drem-watchdog` binary into `/usr/local/bin/drem-watchdog` (the Dockerfile expects the binary in the build context)
- `ENTRYPOINT ["/usr/bin/tini", "--"]`
- `USER drem`
- `WORKDIR /home/drem/work` — the container-local clone lives here; the bare repo is mounted read-only at `/bare` by the spawner

Tag: `localhost:5000/drem-worker-base:latest`.

#### 2. `deploy/docker/worker-go.Dockerfile`

- `FROM localhost:5000/drem-worker-base:latest`
- Install Go 1.24.4 toolchain (download from `go.dev/dl` by SHA-pinned URL; extract to `/usr/local/go`; add to PATH for the `drem` user)
- Install `golangci-lint` (pin version)
- Preload the common Go proxy cache by running `go env GOMODCACHE` and creating the dir with correct ownership
- `ENV DREM_LANGUAGE=go`

Tag: `localhost:5000/drem-worker-go:latest`.

#### 3. `deploy/docker/worker-cpp.Dockerfile`

- `FROM localhost:5000/drem-worker-base:latest`
- Install C/C++ toolchain: `build-essential`, `cmake`, `make`, `ninja-build`, `pkg-config`, `gdb`, `clang`, `clang-format`, `clang-tidy`
- `ENV DREM_LANGUAGE=cpp`

Tag: `localhost:5000/drem-worker-cpp:latest`.

#### 4. `deploy/docker/build-workers.sh`

Build-and-push script:

```bash
#!/usr/bin/env bash
set -euo pipefail

# Build the watchdog first so worker-base can COPY it in.
go build -o deploy/docker/context/drem-watchdog ./cmd/drem-watchdog

cd deploy/docker

docker build -t localhost:5000/drem-worker-base:latest -f worker-base.Dockerfile context/
docker build -t localhost:5000/drem-worker-go:latest  -f worker-go.Dockerfile  context/
docker build -t localhost:5000/drem-worker-cpp:latest -f worker-cpp.Dockerfile context/

docker push localhost:5000/drem-worker-base:latest
docker push localhost:5000/drem-worker-go:latest
docker push localhost:5000/drem-worker-cpp:latest
```

Document that `deploy/docker/context/` is the build context (add a `.gitignore` entry for `context/drem-watchdog`).

#### 5. `deploy/docker/worker-entrypoint.sh`

The script the spawner runs as the container's command. Responsible for:

1. Cloning the feature branch from `/bare` into `/home/drem/work`: `git clone --branch <branch> /bare /home/drem/work`
2. Configuring `origin` to point at `/bare` with read-write access (the bare repo is mounted read-write for workers because the watchdog needs to push)
3. Starting `drem-watchdog` in the background with the flags passed via env vars
4. Running `exec claude ...` or `exec opencode ...` based on `DREM_AGENT_HARNESS` env var — the invocation matches what the existing host-side spawn does

Environment variables the entrypoint reads (set by the spawner in `Spec.Env`):

- `DREM_BRANCH` — feature branch to clone
- `DREM_AGENT_ID`
- `DREM_AGENT_HARNESS` — `"claude"` or `"opencode"`
- `DREM_TEST_CMD` — the test command for the watchdog to run on tests-pass signal
- `DREM_PROMPT_PATH` — file inside the container holding the initial prompt the spawner wrote

Place at `deploy/docker/context/worker-entrypoint.sh`; worker-base COPYs it into `/usr/local/bin/worker-entrypoint`.

### Additions to prompt 06's global compose

**Do not add workers to the global compose.** Workers are ephemeral; the spawner launches them on demand. Only the base image needs to exist in the registry before the first worker launches.

### Tests

#### 6. `deploy/docker/test-worker-image.sh`

Smoke test, run manually after build:

```bash
#!/usr/bin/env bash
set -euo pipefail

# Confirm each image has the expected toolchain
docker run --rm localhost:5000/drem-worker-go:latest go version
docker run --rm localhost:5000/drem-worker-cpp:latest g++ --version
docker run --rm localhost:5000/drem-worker-go:latest drem-watchdog --help
docker run --rm localhost:5000/drem-worker-go:latest claude --version
```

Exit 0 means all images are functional. Document that this script is the build-verification step; there is no automated CI target yet.

## Scope Limitation

- The worker image does not include the orchestrator, spawner, agentmon, or Kyle binaries. Each image is single-purpose.
- The worker image does not bake in any project-specific configuration. Project name, branch, and task ID flow in via env vars set by the spawner.
- The C++ image does not include `vcpkg`, `conan`, or CUDA. If `drem-canvas` requires these, they can be added in a follow-up or layered in a project-specific child image.
- No image-size optimization pass in this prompt. A multi-stage minification for the worker images is a follow-up.
- Do not touch `scripts/csuite-bootstrap.sh` or other existing host-side agent launch scripts. They remain as fallback during cutover.

## Conventions

- Dockerfiles live under `deploy/docker/`; shared build context lives under `deploy/docker/context/`
- All images are tagged `localhost:5000/drem-<name>:latest`; a follow-up can add git-sha tags in CI
- Use `apt-get install -y --no-install-recommends` and `rm -rf /var/lib/apt/lists/*` after every install layer
- Pin toolchain versions via ARG at the top of each Dockerfile so version bumps are a one-line change
- Build verification:
  ```bash
  bash deploy/docker/build-workers.sh
  bash deploy/docker/test-worker-image.sh
  ```
- The drem-watchdog binary MUST build successfully before the worker images build; if prompt 04 is not yet merged, a stub watchdog is acceptable but must still accept `--help` without error so the smoke test passes
