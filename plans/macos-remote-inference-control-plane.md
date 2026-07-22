# macOS control plane with remote inference

Status: local Docker control plane and host-authoritative delivery state machine
implemented. A non-integrating Canvas delivery canary passed end to end. The
remote inference transport is configured and reachable, but live inference is
blocked because the remote GQ instance currently has no running SGLang
upstream. No remote inference service was restarted.

## Goal

Run the Drem orchestrator, spawner, workers, task database, and project bare
repository on a Docker Desktop Mac while using a remote GQ/SGLang deployment
for inference. Canvas builds, application launches, and final integration stay
native to the Mac.

## Invariants

- Only inference requests cross the host boundary.
- The remote inference host never owns or advances Canvas Git refs.
- The SSH forward binds to Mac loopback only.
- Remote GQ remains the single GPU admission queue.
- Worker images are built locally for Linux/arm64 on Apple Silicon.
- Top-level Canvas work must not merge automatically into a branch checked out
  by a host worktree.

## Implemented slice

- Project registry entries can persist an explicit `inference_endpoint`.
- Generated `drem.toml` files route direct workers to that endpoint while
  retaining the in-stack GQ endpoint as the default.
- Direct workers send their role in `X-GQ-Caller`; optional priority is sent in
  `X-GQ-Priority`.
- `remote-inference.override.yml` removes the classifier's local-SGLang
  dependency and points it at a container-visible external endpoint.
- `remote-inference-tunnel.sh` provides a generic foreground SSH tunnel.
- Static Go service images build for the Docker target architecture, including
  Apple Silicon Linux/arm64.
- Explicit `DREM_PROJECT` identity prevents a local registration from being
  confused with the bare repository's basename.
- Cross-UID worker Git access is scoped to `/bare`; watchdog finalization and
  Codex stdin delivery are deterministic for fast workers.
- The host integration worktree synchronizes to the exact accepted worker SHA
  before preliminary gates, but only after proving no local files/index entries
  changed and no accepted-ref drift occurred.
- Direct-agent runtime traces live outside the checkout.
- Canvas uses `prepare_branch` plus `external_ack`; native verification records
  an exact artifact and does not imply integration authority.

## Verified host mapping

- Local control plane: macOS Docker/Colima on arm64; registry, global spawner,
  per-project orchestrator, agent monitor, and worker images run locally.
- Canvas Git authority and native verification:
  `/Users/jonathangodin/git/drem-canvas.git` on the Mac.
- Remote inference transport: SSH to `godinj@script.dremhome.org:21337` using
  the operator's `npllm` identity, forwarding a Mac-loopback port to remote
  `127.0.0.1:8090`.
- Remote host observed as `debian2`; its `drem-gq` container is loopback-bound
  and configured for `http://sglang:8081`.
- The remote host had no `sglang` container, relevant listener, or GPU process
  during the 2026-07-22 canary. Calls therefore returned 502. This plan does
  not authorize restarting that service.

## Canary evidence

The local delivery path was proven with a disposable one-file Canvas task:

```text
classifying -> backlog -> in_progress -> testing_ready
  -> verification_ready -> integration_ready
```

Native macOS verification referenced artifact
`100a9c14e63a202aff5cf7e67c4a6ff089f996c3` and base
`b53d312e1b75d1cd59ff6d515e74f85d057c7bfd`. It checked the exact SHA and
parent, sole changed path, JSON payload, diff hygiene, and clean detached
worktree. The task was never integrated; archive invalidated the artifact and
all disposable Git/container state was removed.

## Required before a remote-inference Canvas writer

1. Restore or separately start the already-configured remote SGLang service;
   this requires explicit operational authority outside this plan.
2. Re-run the fixed, repository-free read-only inference canary and retain its
   endpoint/model/latency/token evidence.
3. Prove Canvas default SHA, worktree inventory, and dirty-worktree
   fingerprints are unchanged by that inference-only call.
4. Keep container gates preliminary and perform authoritative Canvas build,
   GUI/application checks, and `scripts/dev verify` natively on the Mac.
5. Continue to stop real work at `verification_ready` and
   `integration_ready` unless an explicit integration authorization is issued.

## Rollback

Omit the Compose override and the project `--inference-endpoint` flag. Generated
configuration then returns to `http://gq:8090/v1/chat/completions`, and the
normal in-stack SGLang/GQ deployment is unchanged.
