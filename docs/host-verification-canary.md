# Canvas host-verification canary ladder

Run these stages in order. A later stage is not authorized by a partial or
failed earlier stage. Remote SGLang supplies inference only; the local Mac owns
the orchestrator, Canvas Git refs, native build, application process, Computer
Use evidence, and integration decision.

## 1. Repository-free remote inference

Open the loopback SSH tunnel separately, then run:

```bash
scripts/drem-remote-inference-canary.sh
```

The canary discovers a model through `/v1/models` and sends one constant prompt
through `/v1/chat/completions`. It refuses a non-loopback endpoint by default,
sends no repository or task content, and performs no orchestration mutation. A
passing result has `ok: true`, `repository_data_sent: false`, and
`orchestration_state_mutated: false`. Do not start a Canvas writer if this
stage is unreachable, malformed, or semantically wrong.

The tunnel is an operator-owned foreground process:

```bash
export DREM_INFERENCE_SSH_HOST='<user@remote-host>'
scripts/remote-inference-tunnel.sh
```

No canary script starts or restarts SGLang, GQ, Docker, or SSH on its own.
When the GPU host runs SGLang natively but GQ in Docker, use
`deploy/compose/host-sglang.override.yml` as documented in the containerization
guide; otherwise GQ's default `sglang:8081` Docker upstream cannot reach the
host process.

## 1b. Canvas C++ tool-use smoke

Before exposing a new model/server pair to the Canvas repository, run the
checked-in Canvas-style C++ fixture through the same direct tool agent used by
workers:

```bash
go run ./cmd/drembench \
  -endpoint http://127.0.0.1:18090/v1/chat/completions \
  -model qwen3.6-27b-code \
  -task canvas-cpp-lower-zone-smoke \
  -runs 3 \
  -max-iter 20 \
  -out bench/results/run-canvas-cpp-smoke.csv
```

`bench/tasks/canvas-cpp-smoke.json` requires the model to inspect Canvas-style
C++17, repair a bounded state invariant, invoke the native compiler, run the
focused executable, and stop. The stage passes only when all three isolated
trials verify and finish naturally; a single lucky pass is insufficient. This
fixture contains no Canvas checkout data and cannot replace the real task in
stage 2.

For a production candidate, stage 2 should use a test-only quick-fix on the
exact Canvas base. Review the semantic diff before acknowledging native gates:
compilation can prove that a test is valid without proving that the worker
implemented the requested assertion. If the semantic review fails, request
orchestrated rework and require a new worker attempt, commit SHA, artifact
version, and native verification record. Refreezing the same SHA is a failed
canary.

## 2. Non-integrating delivery

Use a disposable task and feature branch against the local Canvas bare
repository. The task must reach `verification_ready` with a typed artifact,
exact commit/base SHAs, and passing preliminary gates. Inspect it with:

```bash
export DREM_ACTOR='codex:canvas-canary:<task-id>'
dremctl artifact <task-id>
```

The stage passes only when the artifact names the expected feature ref and
exact SHA, the default branch is unchanged, and archive/cancel invalidates the
artifact without merging it. Never use `pass` to synthesize native evidence.

For a normal Codex-operated task, `accept-assumptions` replaces the former
manual plan-approval pause. If a child worker is rejected for deterministic
scope contamination, the Codex task may repair that child branch and run
`dremctl adopt <child> --commit <sha>`. Adoption replays branch admission and
records typed evidence; it does not trust the commit merely because Codex
created it.

With `verification_policy = "external_ack"`, the preliminary Linux gate is
Git-only. It verifies the exact clean candidate and records that project-native
commands are deferred. `scripts/drem-canvas-pilot.sh build <task>` then creates
the exact detached Mac worktree and runs `scripts/dev verify` natively.

## 3. Repeated native Computer Use tweaks

Build the exact artifact commit on the Mac, record the binary SHA-256, launch
that binary, and exercise every acceptance criterion with Computer Use. Keep
the actor stable for the entire canary.

Use this routing rule after each observation:

| Observation | Route |
| --- | --- |
| Inconclusive UI observation; no source, resource, build-input, commit, or binary change | Repeat Computer Use locally against the same artifact and binary. Do not re-enter orchestration. |
| A bounded implementation correction; acceptance criteria and dependency/persistence/security/process/build-policy shape are unchanged | Submit failed verification with `host-direct`, enter the actor-owned `host_rework` session, commit only the allowed scope, then `submit-rework`. Fresh deterministic gates must create the next artifact version before another native build. |
| The correction changes acceptance criteria, dependencies, persistence/schema, security/auth, cross-process ownership, or build/release policy | Route to `orchestrated` rework. The task returns to normal worker planning/implementation; the Codex verifier must not patch it as a local tweak. |

For every code tweak, the required sequence is:

```text
verification failure
  -> host_rework session owned by the same actor
  -> new commit on the canonical feature ref
  -> submit-rework
  -> testing_ready deterministic gates
  -> new artifact version and exact SHA
  -> native rebuild and new binary SHA
  -> Computer Use verification
```

Two discrepancies therefore produce two distinct rework sessions, two new
commits, and three total artifact/verification cycles before a final pass. The
orchestrator test `TestRepeatedComputerUseTweaksReenterFreshArtifactCycle`
enforces this shape, including zero active worker attempts during host rework.

The stage passes only when the final exact artifact reaches
`integration_ready`, all failed and passing interaction records remain
append-only, every screenshot/video reference is content-addressed, and the
default branch is still unchanged. Integration is a separate explicit action.

## Evidence to retain

- inference canary JSON, request ID, model ID, and response SHA-256;
- task ID, actor, artifact IDs/versions, exact commit and base SHAs;
- gate workspace IDs and command evidence;
- Canvas binary SHA-256, application version, host fingerprint, and PID per
  native run;
- per-criterion Computer Use actions, observations, media IDs/hashes, result,
  and discrepancy;
- rework session/submission IDs and allowed changed paths;
- proof that the Canvas default ref did not move during the canary.

Do not record credentials, mutable media paths, raw repository content in the
inference canary, or direct SQLite edits as evidence.
