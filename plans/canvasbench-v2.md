# CanvasBench v2 plan

Status: core implemented; canonical artifacts pending.

## Delivered

- Strict, versioned manifest/task/matrix/result contracts, with CLI execution
  bound to the content-addressed manifest rather than mutable matrix paths.
- Content-addressed Git fixtures and temporary-worktree cleanup.
- Hidden host oracles, exact scopes, and required-mutation gates.
- Harness-neutral adapters, cross-harness task capabilities with an explicit
  attested execution policy, and ATIF v1.7 trajectories.
- Full runtime/model/quantization/image/harness and server-usage attestation.
- Repeated seeded trials, raw JSONL, weighted scoring, and confidence reports.
- Runnable cases 1–3 and production ownership-rework replay case 8.
- Fully specified fail-closed placeholders for cases 4–7 and 9.

## Before model comparison

1. Freeze reviewed canonical take-cycling implementation and red tests.
2. Freeze bad-artifact diagnostics and hidden mutants.
3. Add the production registry verifier for transient slicing.
4. Freeze capstone native and scripted UI verification.
5. Install outer-isolation wrappers and ATIF normalizers for external CLIs.
6. Run current/candidate history on Qwen, then the corrected harness across
   model and quantization candidates.

Do not use another Ryan goal as a model/harness probe until all nine cases are
runnable and the chosen combination clears the 90-point and case-8/case-9 hard
gates.
