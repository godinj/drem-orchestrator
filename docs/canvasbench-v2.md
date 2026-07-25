# CanvasBench v2

CanvasBench v2 is the qualification boundary for choosing a model and coding
harness for Canvas work. It evaluates immutable repository states and hidden
host-side semantic oracles, not generated prose or substring presence.

## Reproducibility contract

The suite manifest is `bench/canvasbench-v2/manifest.json`. Every task file is
bound by SHA-256. Runnable Git fixtures identify a repository, exact base
commit, every model-visible blob SHA, and an optional seed-patch SHA-256. The
runner creates a detached temporary worktree and removes it after verification;
it never resets or mutates the source repository.

Task, matrix, manifest, and result documents use versioned strict decoders and
published schemas under `bench/canvasbench-v2/schemas`. Unknown fields fail.
Matrices expose fixed trial seeds, temperature, top-p, top-k, context window,
thinking-preservation policy, and seed policy. `preserve_thinking` is sent as
that exact Qwen chat-template argument; enabling model thinking is a separate
runtime choice bound by the runtime config hash.

The model receives only the public prompt and read/write contracts. Oracle
sources remain outside its worktree. Inference tasks allow both
`structured_only` and `sandboxed_shell`; the attested harness selects exactly
one. DirectToolAgent uses exact structured read/write lists without shell.
External adapters require the future outer-container sandboxed-shell wrapper.
Case 8 allows only `deterministic_replay`. This keeps fixture, oracle, scope,
and budget identical across harnesses without claiming that unlike execution
policies are equivalent. Host verification runs only after the adapter exits
and the exact changed-path gate is collected.

## Harness adapters

Fixtures, oracles, scoring, and results depend on `HarnessAdapter`, not a
particular loop. Initial adapters cover DirectToolAgent, deterministic case-8
replay, and dry-run command contracts for OpenCode 1.17+, Qwen Code,
mini-SWE-agent, and Pi.

External CLI adapters are benchmark-only and require Harbor-style outer
container isolation. They do not revive the retired OpenCode host/worktree
path. Until filesystem wrappers and ATIF normalizers are installed, their run
methods fail closed after producing a dry-run invocation.

Trajectories normalize to ATIF v1.7. Inference trials require complete
server-reported request/token usage; estimates do not qualify. Case 8 declares
an explicit deterministic no-inference exemption.

## Corpus and qualification

Cases 1–3 are runnable against Canvas commit
`96db6b709f0a4f2069db4a7d3415ef17867b0274`: API grounding, a real seeded
LowerZoneState repair, and keymap assembly. Case 8 runs production
ownership-aware delivery rework from orchestrator commit
`1d1796eb98222ca8de743730efaa5de8f9f61277` over 100 diagnostic orders.

Cases 4–7 and 9 are fully specified but marked `placeholder` until reviewed
hidden canonical patches, diagnostics, mutants, production checks, and UI
scripts exist. Placeholders return `non_runnable`, score zero, and make a matrix
ineligible; they never silently pass.

The weighted threshold is 90. Non-compiling output, out-of-scope access,
missing required mutation, unmeasured inference, oracle exposure, or missing
attestation caps a case at 40. Cases 8 and 9 are mandatory hard gates.

## Running

Copy `bench/canvasbench-v2/matrices/example.json` and fill every attestation.
The Qwen precise-coding baseline is temperature 0.6, top-p 0.95, top-k 20,
a 131072-token context window, and thinking preserved.

```bash
go run ./cmd/canvasbench \
  -manifest /absolute/path/to/bench/canvasbench-v2/manifest.json \
  -matrix /absolute/path/to/matrix.json \
  -canvas-repo /absolute/path/to/drem-canvas.git/main \
  -orchestrator-repo /absolute/path/to/drem-orchestrator.git/master \
  -out /absolute/path/to/results/run-id
```

Each trial appends raw JSONL. Completion writes aggregate JSON, Markdown, and
CSV with pass rates and Wilson 95% confidence intervals. Codex usage is nullable
and is never inferred from local model usage.

## Why drembench v1 is non-authoritative

`cmd/drembench` remains the explicit v1 synthetic diagnostic. Historical runs
copied mutable fixtures and often accepted substring checks without native
compilation or behavioral verification. They did not consistently bind model
weights, quantization, runtime, image, harness, prompt, server config, or full
trajectories. They can diagnose mechanics but cannot rank Canvas model/harness
combinations.
