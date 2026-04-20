# SGLang Gemma-4 Container Follow-Up

Status: **Dockerfile drafted, awaiting operator (Kyle) build verification.**
Last updated: 2026-04-19.

## Background

The first cut of `deploy/docker/sglang.Dockerfile` re-tagged
`lmsysorg/sglang:latest` and treated SGLang as a black box. That worked
for any model the upstream image had been built against, but the host
serves a Gemma-4 26B AWQ-quantized text-only model that requires:

1. The `gemma4` tool-call parser. The published image rejects it:
   `--tool-call-parser: invalid choice: 'gemma4'`.
2. A new enough `transformers` (5.5.4) to recognize the
   `gemma4_text` model architecture. The published image is older.

A brief stopgap was considered (parameterize `--tool-call-parser` via a
`SGLANG_TOOL_CALL_PARSER` env var so the container could fall back to
`hermes` and at least run). That direction was rejected: it papered
over the architectural mismatch instead of fixing it, and the model
would still fail to load because of the transformers gap. The right
answer is to reproduce the host stack inside the container image.

## Host investigation (canonical spec)

The host SGLang install is bespoke. Documented inventory:

| Component | Version |
|---|---|
| Python | 3.13.5 |
| SGLang | `0.5.10.post2.dev306+g90ef8ce54` (git build, commit `90ef8ce54` from `https://github.com/sgl-project/sglang`; 306 commits past upstream `0.5.10.post2`) |
| transformers | 5.5.4 (PyPI) |
| torch | 2.9.1 + CUDA 12 wheels (`nvidia-*-cu12==12.8.x`) |
| Total Python deps | 207 packages, frozen in the lock |

In addition, six in-tree patches sit on top of the installed sglang to
keep Gemma-4 AWQ weights working with the Marlin / Triton MoE kernels:

| # | File | Effect |
|---|---|---|
| 01 | `srt/layers/quantization/compressed_tensors/compressed_tensors.py` | MoE Triton fallback when Marlin can't handle the layer dims |
| 02 | `srt/layers/quantization/marlin_utils.py` | Add `group_size=16` to the Python whitelist |
| 03 | `jit_kernel/csrc/gemm/marlin/gptq_marlin.cuh` | Add `group_blocks=1` (group_size=16) entries to the CUDA dispatch macros |
| 04 | `srt/layers/quantization/compressed_tensors/schemes/compressed_tensors_wNa16.py` | Non-MoE zero-padding fallback for misaligned vision encoder layers |
| 05 | `srt/layers/quantization/compressed_tensors/schemes/compressed_tensors_wNa16_moe.py` | Allow GELU activation in Triton MoE (was silu-only) |
| 06 | `srt/layers/quantization/compressed_tensors/utils.py` | Match `.linear` suffix in `should_ignore_layer()` (AWQ ignore-entry shape) |

Patches were authored against and apply cleanly to the
`0.5.10.post2.dev306+g90ef8ce54` site-packages tree. Host-side
application script:
`/home/godinj/git/model-tuning/patch-sglang-gemma4.sh`. Its
version-pinning, .bak-backup, and idempotent re-apply pattern carries
into the in-container script.

The host launch flags (the spec the compose `command:` mirrors):

```
--model-path /home/godinj/models/gemma-4-26B-A4B-it-AWQ-4bit-textonly
--served-model-name gemma4-26b
--host 0.0.0.0 --port 8081
--context-length 131072
--mem-fraction-static 0.85
--kv-cache-dtype fp8_e5m2
--swa-full-tokens-ratio 0.08
--attention-backend triton
--sampling-backend pytorch
--disable-piecewise-cuda-graph
--tool-call-parser gemma4
--max-running-requests 4
--trust-remote-code
```

Plus `PYTORCH_ALLOC_CONF="expandable_segments:True"` in the env.

## New container image

The replacement Dockerfile (`deploy/docker/sglang.Dockerfile`) builds
the image as follows:

1. **GPU base:** `nvidia/cuda:12.8.1-cudnn-runtime-ubuntu24.04`.
   Concrete tag, not `:latest`. Matches torch 2.9.1's CUDA 12 wheels.
2. **Python 3.13.5:** installed via the `deadsnakes` PPA. Ubuntu 24.04
   ships 3.12; deadsnakes is the standard third-party PPA for newer
   CPython versions and adds ~30s to the build vs. ~20min if we
   compiled CPython from source. Trade-off: external apt repo, but it
   is pinned and reproducible. Alternative considered (pyenv) was
   rejected as overkill — we don't need multiple Python versions in
   the image.
3. **Venv at `/opt/venv`,** `PATH` prefixed so `python` / `pip`
   resolve to it without an explicit `source bin/activate`. Mirrors
   the host pattern (`/home/godinj/venvs/sglang`).
4. **Frozen pip install** from
   `deploy/docker/context/sglang-requirements.txt` (verbatim copy of
   the host venv's `pip freeze`, including the `git+https://…@90ef8ce54`
   sglang URL). Single layer to maximize cache reuse.
5. **Apply six patches** against the installed site-packages tree via
   `deploy/docker/context/apply-sglang-patches.sh` (adapted from the
   host script — same version check, same .bak backup, same
   `--forward` idempotent re-apply, same six-grep verification step,
   same import test). Patches are vendored under
   `deploy/docker/context/sglang-patches/` so the model-tuning host
   repo is no longer the source of truth for the container.
6. **`PYTORCH_ALLOC_CONF=expandable_segments:True`** baked into the
   image env. Documentation-only `SGLANG_HOST` / `SGLANG_PORT` /
   `SGLANG_MODEL_DIR` envs as well.
7. **ENTRYPOINT:** `python -m sglang.launch_server`. The full flag
   set (tool-call parser, KV dtype, SWA tuning, etc.) is supplied by
   the compose `command:` block in `deploy/compose/global.yml`, so
   flag tuning does not require a rebuild. Host-only pre-launch steps
   (`nvidia-smi -lgc`, the SIGKILL-and-VRAM-drain loop) are
   intentionally omitted — they fight a problem that doesn't exist
   inside the container model.

## Trade-offs

| | Host launcher | New container |
|---|---|---|
| Build time | 0 (already installed) | **30+ min cold**, ~5 min warm cache |
| Image size | n/a | ~15 GB (CUDA runtime + cuDNN + 207 wheels incl. flash-attn-4 + flashinfer prebuilts) |
| GPU at build | not needed | **not needed** (no CUDA kernels compiled — wheels are prebuilt) |
| GPU at runtime | required | required (via `nvidia-container-toolkit`) |
| Python version control | manually pinned in venv | locked to Python 3.13.5 from deadsnakes |
| Reproducibility | "this one host" | `requirements.txt` + 6 patches in git |
| Update path | `pip install -U` + re-run `patch-sglang-gemma4.sh` | refresh `sglang-requirements.txt` from a new `pip freeze`, refresh patches if SGLang upstream moved, rebuild |

The image is heavy. That's accepted. It is the price of vendoring an
entire CUDA + Python + sglang + flashinfer stack into a single
reproducible artifact.

## Acceptance / verification

The Dockerfile is **not** built by this change. It needs a GPU host
and 30+ minutes — too risky for an unsupervised CI build. Operator
(Kyle) runs the build manually:

```bash
cd /home/godinj/git/drem-orchestrator.git/master
docker build -f deploy/docker/sglang.Dockerfile \
    -t localhost:5000/drem-sglang:latest .
docker push localhost:5000/drem-sglang:latest
```

Then bring up the global compose stack (the model dir is bind-mounted
per `deploy/compose/.env`):

```bash
docker compose -f deploy/compose/global.yml up -d sglang
docker compose -f deploy/compose/global.yml logs -f sglang
curl -sf http://127.0.0.1:8081/v1/models
```

The container is healthy when `/v1/models` returns the served model
name (default `gemma4-26b`). Cold model load takes a couple of
minutes; the compose healthcheck has a 120s `start_period` to
accommodate.

## Rollback

If the container build or bring-up fails, the host-side launcher
(`start-sglang-gemma4-production.sh` in the operator's model-tuning
repo) remains the canonical fallback. Stop the container, restart the
host launcher; drem clients see the same OpenAI-compatible endpoint on
`127.0.0.1:8081` either way. Document the failure in this file and
file a follow-up rather than letting the host launcher quietly become
permanent again.

## Open follow-ups

- **Tag the image** with a concrete version once a build is validated.
  `:latest` is fine for the bring-up but not for promotion. A
  `localhost:5000/drem-sglang:0.5.10.post2.dev306-g90ef8ce54-gemma4.1`
  tag would encode both the upstream commit and the patch revision.
- **Refresh strategy** when SGLang upstream moves past `g90ef8ce54`:
  re-run `pip freeze` on the host after upgrading, replace
  `deploy/docker/context/sglang-requirements.txt`, attempt the
  patches against the new tree (the script will fail loudly if a
  patch no longer applies), and re-rev the image tag. Coupling the
  refresh to a deliberate operator step is intentional — the patches
  are tightly bound to upstream code that moves often.
- **CI smoke build** would catch lock drift earlier, but requires a
  GPU runner. Out of scope until the project has GPU CI.

## Build attempt history

| # | Commit at build | Outcome | Root cause | Fix |
|---|---|---|---|---|
| 1 | `bc8ab69` | FAIL @ pip install | `sglang` setup.py pins `transformers==5.3.0`; lock has `5.5.4` → resolver conflict | Add `--no-deps` to pip install (commit `4faf9dd`) |
| 2 | `4faf9dd` | FAIL @ pip install | `outlines_core==0.1.26` has no cp313 wheel on PyPI; sdist needs `rustc` not present in base image | Install rustup (minimal toolchain) inside same RUN layer as pip, remove after (commit `0ab4b76`) |
| 3 | `0ab4b76` | FAIL @ pip install | `outlines_core`'s cargo build pulls `openssl-sys`; needs `pkg-config` + `libssl-dev` at compile time | Add both to apt layer (this commit) |
| 4 | pending | — | — | — |

Host parity note: the host venv succeeded with the same lock because
rustup-installed Rust 1.93 is on `PATH` via `~/.cargo/bin`. The
container needs the toolchain injected and cleaned up inside one layer
so the final image stays lean.
