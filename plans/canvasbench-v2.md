# CanvasBench v2 plan

Status: all nine cases runnable; exact-base native capstone acceptance passed;
nine digest-pinned harness images are defined and the previously released harnesses
passed the combined no-inference canary on Debian. Cline and Continue runtime canaries
and controlled matrices are the current qualification work; model/harness matrix runs are
the next qualification boundary.

## Delivered

- Strict, versioned manifest/task/matrix/result contracts, with CLI execution
  bound to the content-addressed manifest rather than mutable matrix paths.
- Content-addressed Git fixtures and temporary-worktree cleanup.
- Hidden host oracles, exact scopes, and required-mutation gates.
- Harness-neutral adapters, cross-harness task capabilities with an explicit
  attested execution policy, and ATIF v1.7 trajectories.
- Full runtime/model/quantization/image/harness and server-usage attestation.
- Injectable digest-pinned outer-container execution for OpenCode, Qwen Code,
  mini-SWE-agent, Pi, Aider, OpenHands, Goose, Cline, and Continue, with structural declared-file workspace projection.
- Fail-closed ATIF v1.7 normalizers and deterministic golden/fake-executor
  coverage for each documented external wire format.
- Repeated seeded trials, raw JSONL, weighted scoring, and confidence reports.
- Runnable cases 1–8, including content-addressed canonical take-cycling tests,
  member implementation, preserved bad artifact, compiler diagnostics, and an
  eight-mutant hidden native verifier for cases 4–6.
- Two-sided case-6 repair grading: hidden tests grade candidate production,
  while candidate tests independently prove clean-base red, canonical green,
  and mutant sensitivity.
- Constitution-safe take production in a focused include fragment, with
  exact-base `scripts/dev check changed` acceptance for cases 5 and 6.
- Runnable case-9 exact-artifact capstone with six-path scope, two-sided hidden
  grading, ten mutants, focused native and changed-file gates, and host-attested
  Release SHA-256/size evidence.
- Explicit evidence boundary: case 8 owns deterministic rework scheduling;
  case 9 owns a single worker/artifact. Neither claims live UI or Computer Use.
- Trusted OpenAI-compatible usage proxy and host attestor for external harness
  trials: random per-trial credentials, stream/non-stream server parsing,
  aggregate consume-once ledgers, fail-closed correlation, and no harness-log
  fallback.
- External CLI wiring with owner-only admin token files and validated
  adapter-specific inference environment contracts. Matrix attestation binds
  proxy source, digest-pinned image, and effective config hash.
- Authenticated live proxy identity preflight before credential creation, plus
  ephemeral mode-0600 secret env files so trial key bytes never enter Docker
  argv and are cleaned on every executor exit.
- Digest-only base-image locks, exact package/dependency locks, non-root
  Dockerfiles, executable environment shims, and reproducible build attestation
  for the trusted proxy and all nine external harnesses.
- A deterministic fake OpenAI-compatible upstream and fail-closed runtime
  canary that exercises the real harness CLI, trusted proxy usage ledger, and
  production normalizer wire without spending inference tokens.
- A Mac-control/Debian-execution adapter that stages only a clean committed
  orchestrator archive and matrix, keeps proxy secrets remote, runs harnesses,
  Canvas worktrees, native builds, and model access on Debian, and returns only
  the four report artifacts.

## Before model comparison

1. Retain the revision-`377f6920cb96` build attestation and combined canary
   evidence. Re-run both whenever a lock, wrapper, normalizer, proxy, or image
   definition changes.
2. Run current/candidate history on Qwen through the Debian remote adapter,
   then the qualified harness across
   model and quantization candidates.
3. After a model/harness qualifies, run a separate exact-Release Canvas pilot
   with live Computer Use. Do not score that evidence in CanvasBench until a
   deterministic multi-take UI fixture and transport exist.

Do not use another Ryan goal as a model/harness probe until all nine cases are
runnable and the chosen combination clears the 90-point and case-8/case-9 hard
gates.
