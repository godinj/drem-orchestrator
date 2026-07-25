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
External adapters require the outer-container sandboxed-shell wrapper described
below.
Case 8 allows only `deterministic_replay`. This keeps fixture, oracle, scope,
and budget identical across harnesses without claiming that unlike execution
policies are equivalent. Host verification runs only after the adapter exits
and the exact changed-path gate is collected.

The runner invokes one harness adapter exactly once per trial. It is not the
production orchestration state machine and does not exercise delivery rework,
repair routing, or multi-worker ownership. Case 8 is the separate deterministic
hard gate for ownership-aware delivery-rework scheduling. No benchmark result
field claims Computer Use or live UI evidence.

## Harness adapters

Fixtures, oracles, scoring, and results depend on `HarnessAdapter`, not a
particular loop. Initial adapters cover DirectToolAgent, deterministic case-8
replay, and documented JSON or JSONL contracts for OpenCode 1.17+, Qwen Code,
mini-SWE-agent, and Pi.

External CLI adapters are benchmark-only and always execute through an
injectable outer-container boundary. Production command construction requires
a digest-pinned OCI image, an unprivileged user, a read-only root filesystem,
all capabilities dropped, `no-new-privileges`, and a named isolated inference
network. Host, bridge, and default networking are rejected. The image must
contain the harness and its configuration; credentials, the Docker socket,
home directories, and broad host paths are never mounted.

The runner creates a disposable agent-visible projection containing only the
declared read and write paths. The full fixture, `.git`, and oracle material are
not present in the mounted workspace. On successful completion it rejects
undeclared files and read-only mutations, then copies only declared writable
outputs back into the full fixture. This is structural scope isolation, not a
prompt convention, and it does not revive the retired OpenCode host/worktree
path.

Documented harness output normalizes to ATIF v1.7 without synthesizing
assistant text. Malformed, incomplete, or unsupported streams fail closed.
Harness-reported usage is retained only as harness output and can never attest
inference-server truth. External trials use the trusted CanvasBench usage proxy.
Before each trial, the host creates an in-memory ledger through the
admin-authenticated endpoint. The proxy generates an unguessable correlation ID
and 256-bit bearer key; only that key and the public `/v1` base URL enter the
outer harness. The admin credential and any upstream credential remain on the
host/proxy side.

The proxy forwards streaming and non-streaming OpenAI-compatible
chat-completions. Trial creation sends the matrix model ID, seed, temperature,
top-p, top-k, context declaration, per-case output limit, and thinking policy
over the authenticated admin channel. The proxy overwrites those request fields
on every harness call, preserving only unrelated chat-template arguments. This
makes the trusted boundary—not four CLI defaults—the sampling authority. It
also forces `stream_options.include_usage=true`, parses usage from the server
response, and aggregates every request. The host consumes the ledger exactly
once after execution. Zero requests, invalid trial policies, in-flight requests,
upstream errors, missing or duplicate usage, request-count mismatch, wrong
correlation, or a second consume fail closed. A successful record has source
`trusted_usage_proxy`; harness JSON/JSONL token fields are never a fallback.
Case 8 declares an explicit deterministic no-inference exemption.

External matrix attestation binds the proxy source state, digest-pinned image,
effective config SHA-256, and adapter environment contract. OpenCode, Qwen Code,
and Pi use `openai_base_url_api_key.v1` (`OPENAI_BASE_URL` and
`OPENAI_API_KEY`). mini-SWE-agent uses `openai_api_base_api_key.v1`
(`OPENAI_API_BASE` and `OPENAI_API_KEY`). A mismatched or assumed common
contract is rejected before container launch. The digest-pinned harness image
must provide an executable/config shim that honors its declared contract; the
benchmark does not assume the upstream CLI's native environment behavior.
Before creating any trial credential, the CLI performs an authenticated,
operator-rooted identity/config handshake with the proxy and compares all three
fields byte-for-byte with the matrix. This handshake does not independently
inspect Docker's live container digest; deployment must root the configured
identity in its pinned image launch. A mismatch stops the run before
`/admin/v1/trials` is called. The per-trial API key is delivered through a
temporary owner-only Docker env file; the key bytes never appear in Docker argv
and the file is removed on every executor exit. Captured output and errors are
defensively scrubbed, while key bytes written into the scoped workspace reject
the trial before candidate outputs are applied.

### Reproducible external images and compatibility canary

`deploy/docker/canvasbench/locks.json` is the build authority for the trusted
usage proxy and the four external harness images. It pins every base image by
manifest digest and pins harness inputs to OpenCode `opencode-ai@1.18.5`, Qwen
Code `@qwen-code/qwen-code@0.21.0`, mini-SWE-agent `2.4.6`, and Pi
`@earendil-works/pi-coding-agent@0.82.0`. npm lockfiles and a fully hashed Python
requirements closure bind the transitive package inputs. Floating tags,
uncommitted source trees, missing upstream integrity, absent BuildKit digests,
wrong image labels, and a runtime user other than uid/gid 65532 fail the build
tool.

Each image contains a small executable shim for its declared CanvasBench
environment contract. Credentials are injected only at runtime through
owner-only env/token files. They are not build arguments, labels, image
environment values, or command-line values. Build output records the immutable
image reference, source/upstream identity, package integrity, environment
contract, normalizer, and hashes of canonical non-secret image and proxy
configuration. The tool never emits `latest`.

The adapter also gives each shim the matrix seed, sampling values, effective
context window, per-case output limit, and thinking policy. OpenCode and Pi use
those values for truthful model context/output metadata; Qwen Code writes a
private ephemeral generation configuration; mini-SWE-agent receives equivalent
LiteLLM arguments. The trusted proxy still overwrites the wire request, so a
harness cannot silently weaken or substitute the benchmark policy.

From a clean committed checkout, build but do not deploy the canonical image
set:

```bash
deploy/docker/canvasbench/build-images.sh \
  --repository localhost:5000/canvasbench \
  --publish \
  --output /absolute/private/run/image-attestation.json
```

The build requires Docker Buildx with `linux/amd64` support and an explicitly
chosen trusted registry. `--publish` is mandatory: a locally loaded mutable tag
plus detached metadata is not an adequate runtime identity. The tool pushes
each result, pulls its exact `repository:version-revision@sha256:...` reference,
and inspects that object before writing the attestation. Use the local registry
shown above when publication must remain on this machine. Retaining the JSON is
part of the benchmark evidence. Running only
`python3 deploy/docker/canvasbench/image_tool.py validate` validates the static
lock and performs no Docker operation.

Before real inference, run the deterministic compatibility canary:

```bash
deploy/docker/canvasbench/run-canaries.sh \
  --attestation /absolute/private/run/image-attestation.json \
  --output /absolute/private/run/image-canary.json
```

The canary creates a temporary internal Docker network, starts a pinned Python
fake OpenAI-compatible server and the exact trusted-proxy image, then runs each
content-addressed harness image as uid/gid 65532 with a read-only root,
capabilities dropped, no Docker socket, and only a disposable workspace mount.
For every harness it requires a real CLI request, complete consume-once proxy
usage, the exact assistant sentinel, and successful normalization through the
production ATIF wire parser. Secrets exist only in temporary mode-0600 files
and are deleted with the network and containers on exit.

Any package install/entrypoint mismatch, unsupported CLI option, ignored
environment contract, proxy identity mismatch, missing usage, or changed output
wire exits nonzero with `unsupported canary:`. There is intentionally no
fallback or compatibility guess. Revision `4ca7b96ab621` was built natively on
the Debian AMD64 execution host, published to GHCR, and passed the combined
four-harness canary. The retained attestation SHA-256 is
`7808d44d279c252c5c6258ed96796e75d0edcb6952dddd569133a5ae1b191c0e`;
the canary document SHA-256 is
`40bc0c477dc7aa23bc3460715f076b9ffb0b2c08d2ad65231729f17c63c034ab`.
This qualifies the pinned CLI/runtime contracts only. It does not qualify any
model or quantization, which still requires the full matrix.

## Corpus and qualification

Cases 1–3 are runnable against Canvas commit
`96db6b709f0a4f2069db4a7d3415ef17867b0274`: API grounding, a real seeded
LowerZoneState repair, and keymap assembly. Case 8 runs production
ownership-aware delivery rework from orchestrator commit
`1d1796eb98222ca8de743730efaa5de8f9f61277` over 100 diagnostic orders.
Case 7 isolates the missing `registerAllActions` →
`registerAudioProcessActions` seam on the otherwise verified transient-slicing
artifact `2ff61e8be0020395554bc6945b221253376b818e`. Its hidden verifier strips C++
comments and literals, requires exactly one executable call in the correct
function body, and revalidates the pinned declaration, definition, action ID,
and production keymap route.

Cases 4–6 are runnable take-cycling stages. Case 4 grades a candidate red-test
TU first on the clean base, then against the hidden canonical implementation
and eight deterministic mutants: missing wrap, automation-focus leakage,
non-audio mutation, empty no-op undo, missing status, missing notification,
declaration mismatch, and registration mismatch. Case 5 grades the four-file
member implementation—header declarations, the EditorAdapter include seam, a
focused `EditorAdapterTakeActions.inc`, and action registrations—against the
hidden canonical test patch. The existing action-handler fragment remains at
its pinned 597-line baseline. Case 6 starts
from the exact `96db6b7..861eebff` bad-artifact diff plus verbatim pinned
compiler diagnostics, then grades production with hidden tests and independently
grades the repaired candidate tests on clean-base red, canonical green, and the
same mutant corpus. This two-sided check prevents weakened candidate tests from
grading their own production.

Every canonical patch, diagnostic file, and mutant corpus has a SHA-256 pin in
the task document; the manifest pins that task document. The native verifier
uses a separate disposable detached worktree and the smallest stable native
gate, `scripts/dev test --filter '(Take cycling|take\.)'`. The pinned historical
Canvas base has unrelated integration failures, so the focused gate keeps
benchmark outcomes attributable to the candidate and hidden corpus.
That base also relied on a transitive `<cmath>` include in an unrelated
tempo-map unit test. The hidden take-cycling gate injects `cmath` through its
compiler environment on Debian; it does not edit or expose another candidate
file and cannot change the scoped take-cycling behavior being graded.
It may reuse only that worktree's generated build while resetting source files
to the exact base between independent grading phases. Production grading for
cases 5 and 6 also runs `scripts/dev check changed`, so a behaviorally correct
candidate cannot pass while violating Canvas's file-size or architecture
constraints.

Case 9 is the runnable exact-artifact capstone. A single worker owns the exact
six changed paths: the test TU, EditorAdapter header and include seam, focused
take-actions fragment, action registration, and shipped keymap. Candidate tests
must be compile-valid red on the exact base, green on hidden production, and
kill eight behavior mutants. The hidden keymap verifier kills two route mutants.
Candidate production is graded independently with hidden tests and
structural/keymap checks. The exact
candidate then runs `scripts/dev check changed` and `scripts/dev build release`;
the result records the host-computed SHA-256 and byte length of
`build/DremCanvas`. Missing or malformed Release evidence is a hard-gate
failure.

Canvas has no deterministic e2e fixture that creates and selects a multi-take
audio track, so live take-cycling Computer Use is deliberately not scored.
Operational adoption still requires a post-benchmark Canvas pilot to run the
attested Release binary and record live Computer Use evidence. That pilot is a
separate mandatory release decision, not benchmark evidence retroactively
attributed to case 9.

The weighted threshold is 90. Non-compiling output, out-of-scope access,
missing required mutation, unmeasured inference, oracle exposure, or missing
attestation caps a case at 40. Cases 8 and 9 are mandatory hard gates.

## Running

Copy `bench/canvasbench-v2/matrices/example.json` and fill every attestation.
The Qwen precise-coding baseline is temperature 0.6, top-p 0.95, top-k 20,
and thinking preserved. `context_window` must equal the effective context limit
in the attested live runtime; the current Debian Qwen3.6-27B vLLM qualification
uses 32768. A future 65536/131072 runtime must receive a new runtime-config hash
and matrix rather than merely advertising the larger value to a harness.

The canonical two-host topology keeps the Mac as the control plane and runs
the entire benchmark process on Debian. This is stronger than setting
`DOCKER_HOST`: a remote Docker daemon cannot safely bind-mount a Mac scoped
workspace, and native Canvas verification must execute on the same Debian
filesystem as the candidate. Configure the remote paths and invoke the adapter:

```bash
export DREM_CANVASBENCH_REMOTE_HOST=godinj@script.dremhome.org
export DREM_CANVASBENCH_REMOTE_PORT=21337
export DREM_CANVASBENCH_REMOTE_ROOT=/home/godinj/.cache/drem/canvasbench/remote-runs
export DREM_CANVASBENCH_LOCAL_CANVAS_REPO=/Users/jonathangodin/git/drem-canvas.git/main
export DREM_CANVASBENCH_REMOTE_CANVAS_REPO=/home/godinj/git/drem-canvas.git
export DREM_CANVASBENCH_REMOTE_ORCH_REPO=/home/godinj/git/drem-orchestrator.git
export DREM_CANVASBENCH_REMOTE_USAGE_PROXY_TOKEN_FILE=/home/godinj/.drem/secrets/canvasbench-admin.token

scripts/canvasbench-remote.sh \
  --matrix /absolute/path/to/matrix.json \
  --out /absolute/path/to/results/run-id
```

The adapter refuses dirty source and first pushes every manifest fixture commit
from the authoritative Mac repositories to a content-addressed
`refs/canvasbench/fixtures/<sha>` ref in the Debian repositories. It verifies
each remote ref byte-for-byte before streaming the committed runner archive and
matrix. CanvasBench then runs wholly on Debian. The usage-proxy admin token is
a Debian path and never crosses to the Mac. On success only JSONL, JSON,
Markdown, and CSV reports return to the Mac, and remote staging is removed. On
failure staging is retained and its exact path is reported.

Running `go run ./cmd/canvasbench` directly remains supported for an all-in-one
Debian host:

```bash
go run ./cmd/canvasbench \
  -manifest /absolute/path/to/bench/canvasbench-v2/manifest.json \
  -matrix /absolute/path/to/matrix.json \
  -canvas-repo /absolute/path/to/drem-canvas.git/main \
  -orchestrator-repo /absolute/path/to/drem-orchestrator.git/master \
  -out /absolute/path/to/results/run-id \
  -usage-proxy-admin-url http://127.0.0.1:18091 \
  -usage-proxy-public-base-url http://canvasbench-usage-proxy:8080/v1 \
  -usage-proxy-admin-token-file /absolute/private/path/admin.token
```

The three usage-proxy flags are required only for external harness matrices.
The token file must be a regular owner-only file. DirectToolAgent and the
deterministic replay case do not receive or require it.

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
