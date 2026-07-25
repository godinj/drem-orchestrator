# CanvasBench v2 plan

Status: all nine cases runnable; exact-base native capstone acceptance passed.

## Delivered

- Strict, versioned manifest/task/matrix/result contracts, with CLI execution
  bound to the content-addressed manifest rather than mutable matrix paths.
- Content-addressed Git fixtures and temporary-worktree cleanup.
- Hidden host oracles, exact scopes, and required-mutation gates.
- Harness-neutral adapters, cross-harness task capabilities with an explicit
  attested execution policy, and ATIF v1.7 trajectories.
- Full runtime/model/quantization/image/harness and server-usage attestation.
- Injectable digest-pinned outer-container execution for OpenCode, Qwen Code,
  mini-SWE-agent, and Pi, with structural declared-file workspace projection.
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

## Before model comparison

1. Wire a deployment-specific independent inference-server usage attestor into
   the external CLI path; harness summaries cannot provide this evidence.
2. Build and attest digest-pinned harness images on a named isolated inference
   network without credential or broad host mounts.
3. Run current/candidate history on Qwen, then the corrected harness across
   model and quantization candidates.
4. After a model/harness qualifies, run a separate exact-Release Canvas pilot
   with live Computer Use. Do not score that evidence in CanvasBench until a
   deterministic multi-take UI fixture and transport exist.

Do not use another Ryan goal as a model/harness probe until all nine cases are
runnable and the chosen combination clears the 90-point and case-8/case-9 hard
gates.
