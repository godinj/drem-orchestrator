# CanvasBench v2 plan

Status: cases 1–8 runnable; capstone case 9 pending.

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
- Fully specified fail-closed placeholder for case 9.

## Before model comparison

1. Freeze capstone native and scripted UI verification for case 9.
2. Wire a deployment-specific independent inference-server usage attestor into
   the external CLI path; harness summaries cannot provide this evidence.
3. Build and attest digest-pinned harness images on a named isolated inference
   network without credential or broad host mounts.
4. Run current/candidate history on Qwen, then the corrected harness across
   model and quantization candidates.

Do not use another Ryan goal as a model/harness probe until all nine cases are
runnable and the chosen combination clears the 90-point and case-8/case-9 hard
gates.
