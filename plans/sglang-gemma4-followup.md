# SGLang `gemma4` Tool-Call Parser — Followup

Stopgap landed: `deploy/compose/.env(.example)` now ships
`SGLANG_TOOL_CALL_PARSER=hermes` and `deploy/compose/global.yml` reads it
into `--tool-call-parser`. This unblocks the `drem-sglang` container,
which was crash-looping on
`--tool-call-parser: invalid choice: 'gemma4'`. This document tracks the
real fix.

## 1. Why `gemma4` is the desired parser

The model we serve (`gemma-4-26B-A4B-it-AWQ-4bit-textonly`, advertised
as `gemma4-26b`) emits tool calls in the gemma4-native format. The host
SGLang launcher we are migrating from has been running with
`--tool-call-parser gemma4` for two days and the drem clients
(`internal/agent/direct_tool_agent.go`, the classifier, the C-Suite
agents in `docs/csuite-agents/prompts/`) all build prompts assuming that
parser is on the wire. With the wrong parser, every tool call from the
model decodes as plain text and silently drops to the user, breaking
every agent that depends on tool use (which is all of them).

The host launcher and the matching launcher block in
`plans/host-state-inventory.md §1.1` are the canonical reference; the
container command in `deploy/compose/global.yml` mirrors them.

## 2. Why the stable image lacks `gemma4`

`deploy/docker/sglang.Dockerfile` builds `FROM lmsysorg/sglang:latest`
(see the `SGLANG_TAG: latest` build-arg in `global.yml`). The stable
image's `--tool-call-parser` registry as of 2026-04-19 enumerates:

```
deepseekv3, deepseekv31, deepseekv32, glm, glm45, glm47, gpt-oss,
kimi_k2, lfm2, llama3, mimo, mistral, pythonic, qwen, qwen25,
qwen3_coder, step3, step3p5, minimax-m2, trinity, interns1, hermes,
gigachat3
```

`gemma4` is not on that list. The parser exists upstream on
`main`/`HEAD` of the SGLang git repository but has not yet rolled into
a tagged release that the `latest` mutable tag points at. The host
launcher works because the host-side install was a `pip install -e .`
of an SGLang git checkout that includes the parser; the containerized
image's `latest` tag does not.

`hermes` is the closest in-tree parser to the gemma4 tool-call format
(both emit `<tool_call>…</tool_call>` envelope tags around JSON). It is
not byte-compatible with gemma4 output, so tool-call extraction will be
lossy under load. Acceptable as a stopgap to keep the container off
crash-loop while we land a real fix.

## 3. Three real-fix options

### Option A — Roll back to host SGLang via systemd

Re-enable the host-side SGLang launcher (the same `pip install -e .`
checkout that's been working for two days) under a `systemd --user`
unit, retire the `drem-sglang` container, and let the rest of the
containerized stack point at `127.0.0.1:8081` on the host loopback as
they already do. GQ stays containerized; SGLang regresses to host.

Pros: zero new build work; immediately restores the gemma4 parser
without waiting on a CUDA image rebuild; `plans/install-log.md §7.1`
already documents the exact `pkill` we'd be inverting.

Cons: re-introduces the host-side process the containerization PRD
(`docs/prd-containerization.md` §"Phased rollout step 1") explicitly
set out to retire. Splits the bring-up story: half containers, half
systemd.

### Option B — Build SGLang from upstream git inside the image

Replace the `FROM lmsysorg/sglang:latest` base in
`deploy/docker/sglang.Dockerfile` with a multi-stage build that clones
the SGLang git repo at a commit known to include the gemma4 parser,
runs the upstream install (CUDA toolkit + Python + `pip install -e .`),
and bakes the result into a new `localhost:5000/drem-sglang:gemma4`
tag.

Pros: closes the gap with a single image rebuild; keeps the full
containerization story intact; the resulting image is reproducible at
a pinned commit.

Cons: long build (CUDA base layers + a `pip install` that compiles
custom CUDA kernels). The first build on this host will be in the 30–60
minute range and will produce a 15–20 GB image. Pinning the upstream
commit also means owning a private image branch until upstream cuts a
release that includes the parser.

### Option C — Find a newer image tag that ships the parser

Check `lmsysorg/sglang` on Docker Hub for a tag (date-stamped nightly,
release candidate, or future stable) whose parser registry includes
`gemma4`, and bump `SGLANG_TAG` in `deploy/compose/global.yml` (and
the equivalent build-arg in `deploy/docker/sglang.Dockerfile`) to that
tag.

Pros: cheapest fix if a viable tag exists; no Dockerfile rewrite;
still a single mutable artifact to track.

Cons: requires manual tag spelunking (Docker Hub search + reading the
upstream release notes / parser registry source for each candidate);
the right tag may not exist yet and we'd be back to A or B if so. Tag
churn on `lmsysorg/sglang` has been frequent — pinning to a date-stamp
is fine, pinning to `latest` reintroduces the same drift that bit us
this time.

## 4. Acceptance criteria for the future fix

The followup is done when:

1. `deploy/compose/global.yml`'s `--tool-call-parser` resolves to
   `gemma4` again — either by changing the default in the substitution
   (`${SGLANG_TOOL_CALL_PARSER:-gemma4}`) or by setting the value to
   `gemma4` in `.env` / `.env.example`.
2. The `drem-sglang` container starts and stays in `Up (healthy)` for
   30+ minutes under steady load with the new parser, with no
   `invalid choice` errors in `docker logs drem-sglang`.
3. A representative end-to-end tool call from drem (any agent that
   exercises the `Read`/`Write`/`Bash` tool path through the
   classifier) succeeds and shows up as a parsed tool call in the
   orchestrator event stream — not as plain text.
4. The stopgap notes are removed from `deploy/compose/.env`,
   `deploy/compose/.env.example`, `deploy/compose/global.yml`,
   `plans/install-log.md`, and `docs/containerization/install.md`.
5. This document is either deleted or updated with a final entry
   recording which option was taken and the commit / image tag that
   landed it.
