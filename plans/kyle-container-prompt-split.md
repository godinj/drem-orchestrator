# Plan: Kyle container-prompt split (Fix B)

**Status:** in progress (2026-04-23). Opens the path for container-Kyle
to run as a csuite persona alongside Mike/Alex/Seth without dragging
the interactive-Kyle conversational contract along for the ride.

**Related:**

- `plans/container-kyle-transition.md` — the 5-phase plan that shipped
  csuite-kyle service + image + compose wiring + runtime-mode preamble
  retrofit. This split is the downstream cleanup the Phase 4 preamble
  retrofit deferred (intermittent drift at runtime).
- `plans/csuite-persona-pivot.md` — Wave-2 poller that loads
  `/opt/csuite/prompts/<persona>.md` and invokes `claude -p` once per
  inbox message. Every container persona shares that loop.
- `plans/containerization.md` — the multi-phase containerization
  effort. Kyle is the last persona to move off host runtime.
- `docs/csuite-agents/prompts/kyle.md` — today's prompt, authored for
  interactive invocation and then retrofitted with runtime-mode
  preambles.
- `docs/csuite-agents/prompts/mike.md` — canonical poller-shaped
  prompt (container-mode, poller runtime). Shape reference for
  container-Kyle.

## Problem

Kyle's system prompt was written for an interactive host-side Claude
Code session — ongoing chat, TTY, stdout replies, conversational
voice. The containerization pivot needs container-Kyle to run under
the csuite-persona poller: `claude -p` once per inbox message, no
TTY, one file out to outbox per turn, no stdout reply at all.

Wave-1 retrofit stacked runtime-mode preambles at the top of
`kyle.md` (Phase 4 + Phase 5 blocks) to teach the same prompt both
contracts. In practice the preambles drift: terse operator messages
bypass the "MUST write outbox file" discipline and Kyle falls back
to the conversational stdout habit baked into the 500+ lines below.
The csuite-watcher then routes the empty outbox as a stub and
suppresses it.

Worked example — corrid `88ed93b6`:

- Operator sends two substantive messages (roadmap triage, status
  brief). Both succeed: Kyle writes `outbox/<ts>-kyle-to-operator-
  <corrid>.md` with frontmatter, watcher routes to operator inbox.
- Operator sends a third message, body `hello?`. Kyle answers via
  stdout — no outbox file, just a conversational reply the poller
  discards. Watcher logs a stub and suppresses.

The failure mode is probabilistic on message length + terseness. Not
reliably reproducible, not fixable by tightening the preamble
wording alone — the body of the prompt keeps pulling the model back
toward the interactive contract.

## Why Fix B over Fix A

Fix A (bigger warning block at the top of kyle.md) was attempted and
rejected. It adds signal strength but keeps the conflicting body of
the prompt intact. The model still has ~500 lines of "you are an
interactive session, you brief the operator, you chat" immediately
below the warning. Intermittent drift continues.

Fix B splits the prompt along runtime boundaries:

- `kyle.md` — interactive-Kyle, host `claude` CLI. TTY, ongoing
  chat, stdout replies. No mention of container mode.
- `kyle-container.md` — container-Kyle, poller runtime. Shape like
  `mike.md`: invocation contract, output contract (must-write-one-
  file-to-outbox), tools, canonical references, responsibilities.

Each prompt is internally consistent. No conflicting signals. Per-
runtime discipline is enforced at the prompt level rather than at
runtime via retrofit warnings.

## Scope

### Phase 2 — add `kyle-container.md`

New file `docs/csuite-agents/prompts/kyle-container.md`, ~80-120
lines, shaped like `mike.md`:

1. Identity (CEO, strategic orchestrator, not ops).
2. Invocation contract (poller, `claude -p`, no TTY, no chat).
3. Output contract (MUST write one frontmattered file to outbox,
   MUST NOT reply via stdout).
4. Tools available (Read, Write, Bash — match mike.md).
5. Canonical references (CLAUDE.md, world-state §2a for
   `orch-plans/` write access, plan docs, Kyle HTTP API at
   `http://drem-kyle:8090`).
6. Responsibilities (CEO scope — one short list: triage operator
   messages, delegate to Mike/Alex/Seth, review outputs, maintain
   plan docs).
7. Persona voice (conversational prose even in replies; the caveman
   session hook is interactive-Claude-only and does not apply).

### Phase 3 — strip container preambles from `kyle.md`

Remove any container-mode preambles and any scattered "when in
container mode do X / when interactive do Y" branches in the body.
`kyle.md` ends up as interactive-only: no mention of container
runtime, no outbox-file discipline, no poller invocation.

### Phase 4 — update `deploy/docker/build-csuite.sh`

When building the container-Kyle image, stage
`docs/csuite-agents/prompts/kyle-container.md` as
`/opt/csuite/prompts/kyle.md` inside the image — the csuite-persona
poller derives the prompt path from `CSUITE_AGENT` and the
runtime expects `<persona>.md` on disk.

Mike/Alex/Seth keep their existing 1:1 copy behavior.

## Acceptance criteria

- `drem csuite send kyle -m "hello?"` returns a frontmattered reply
  in the operator's inbox within ~30s. No stub suppression.
- Interactive `claude` against `kyle.md` still produces
  conversational stdout replies, same as before the split.
- `kyle-container.md` is < 150 lines. `kyle.md` has zero references
  to container mode, `claude -p`, outbox-file discipline, or the
  poller runtime.
- `deploy/docker/build-csuite.sh` passes `bash -n`; the staged
  prompt file at `deploy/docker/context/csuite-prompts/kyle.md`
  contains the content of `kyle-container.md` (verified by diff).

## Out of scope

- `csuite-kyle.Dockerfile`, `csuite-entrypoint.sh` whitelist entry
  for `kyle`, and the compose-topology service
  `drem-orchestrator-csuite-kyle-1`. All already shipped in
  `plans/container-kyle-transition.md` Phase 1 (commit `2639508`).
  This split is the prompt-content fix for that infrastructure,
  not new wiring.
- Changing kyle-the-CEO's responsibilities, decision boundaries, or
  routing table. This is a prompt-shape split, not a role change.
- Deleting `drem-kyle` (the Go world-state API container). That is
  a separate service with its own image (`kyle.Dockerfile`) and
  lifecycle; the csuite-Kyle container is additive.
