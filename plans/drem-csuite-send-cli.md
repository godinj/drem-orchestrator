# `drem csuite send` CLI plan

**Status:** v1, 2026-04-22. Operator-greenlit 2026-04-22. Awaiting
implementation.

## Context

The operator's current "talk to a C-Suite persona" workflow is
hand-shaped: type YAML frontmatter (from, to, topic, sent_at), write
the body, save under a UTC-timestamped filename matching
`<ts>-operator-to-<persona>-<subject>.md`, move it into
`~/.drem-csuite/<persona>/inbox/`, then poll the persona's outbox
(or the watcher's quarantine tree on malformed frontmatter) for a
reply. Every turn risks a fat-fingered filename, a missing field, or
a race with the 5-min rescan cadence. It is the last remaining
"operator-acts-like-a-csuite-persona-binary" surface in the system.

The watcher already routes `to: <persona>` outbox files via
`ClassPersona`. This plan adds a peer `ClassOperator` destination so
a reply with `to: operator` lands in a durable host-side inbox at
`~/.drem-csuite/operator/inbox/` — an inbox-only, no-outbox, no-
state pseudo-persona tree the CLI reads from. Durable-first means
the reply survives CLI crash, operator walk-away, and host reboot.
The alternative of having the CLI scrape the responder's outbox
before the watcher quarantines it was rejected as fragile.

## Why now

- Container-Kyle pivoted this session (commits `2639508` Phase 1
  image+compose, `2fdacd8` Phase 2 ClassKyle collapse, `c92065e`
  Phase 4 coexistence rules, `f9d3399` Phase 5 housekeeping).
  All four csuite personas (kyle, mike, alex, seth) now run under
  the same `csuite-persona` runtime — they are symmetric sources
  and destinations.
- With container-Kyle live, the host-side operator→persona drop is
  the last manual YAML-authoring surface in the dogfood loop.
  Everything else (persona→persona, orch→persona, persona→operator)
  already routes through `csuite-watcher`.
- Aligns with the operator's Phase 3 check-in Path A: interactive-
  Kyle is canonical for operator↔Kyle dialogue, but mike/alex/seth
  (and container-Kyle when the operator prefers an async turn) are
  reachable via inbox drop. A CLI makes that drop friction-free.
- The plan that `container-kyle-transition.md` §Why now bullet 1
  "subsumed" was always a real piece of missing UX — container-Kyle
  eliminated the *Kyle-shaped hole* in that CLI, not the CLI itself.

## Non-goals

- NOT a TUI. No curses, no live-tailing panes. One-shot commands
  only. The existing `drem tui` covers interactive surfaces.
- NOT a persistent chat session. Each `send` is one message +
  optional reply wait, then exit. State lives in the filesystem.
- NOT a replacement for interactive-Kyle. The operator still runs
  `claude` against `docs/csuite-agents/prompts/kyle.md` for any
  multi-turn strategic dialogue. `drem csuite send kyle` is the
  async escape hatch, not the canonical channel.
- NOT a markdown renderer. Reply output is plain text to the
  terminal. `less`, `bat`, `glow` remain the operator's call.
- NOT broadcast / multi-recipient in v1. `drem csuite send mike,alex`
  is out of scope; revisit post-v1 if demand emerges.
- NOT touching `cmd/drem-kyle` (the Go binary) — that is the
  world-state HTTP/WS API, a separate concern that shares only the
  name "kyle".

## User stories

1. As the operator, I want `drem csuite send mike -m "status of
   Pod 1?"` so I can get Mike's one-shot answer without writing a
   YAML file by hand.
2. As the operator, I want to pipe a long pre-composed markdown
   body from another process (`my-report-gen | drem csuite send
   alex -`) so I can script persona inputs without a temp file.
3. As the operator, I want `drem csuite send mike -m "q" --no-wait`
   so I can fire off a message and review the reply later via
   `drem csuite inbox read` — useful when I'm walking away from the
   terminal for a while.
4. As the operator, I want `drem csuite send kyle -f pivot.md` so
   I can hand container-Kyle a heavy async context dump when
   interactive-Kyle is not running (e.g. overnight, or when I'm
   specifically testing the container's Path #3 symmetry).
5. As the operator, I want `drem csuite send seth -e` to open
   `$EDITOR` with a commit-message-style template so I can compose a
   long review request without fighting shell quoting.
6. As the operator, I want a non-zero exit + diagnostic when
   `--timeout` expires so my automation scripts can tell a silent
   persona from a healthy one, and I can grep the written filename
   back out of the persona's inbox to confirm delivery landed.

## Command shape

```
drem csuite send <persona> [body source] [options]

<persona>              kyle | mike | alex | seth

Body sources (pick one):
  -m, --message STR    inline string
  -f, --file FILE      read body from file
  -                    read stdin (when arg is a bare -)
  -e, --editor         open $EDITOR
  (no body flag)       read stdin if piped, else error

Options:
  -t, --topic TOPIC    subject line (frontmatter topic: key);
                       default: auto-derive from first body line
                       truncated to 60 chars
  --wait               block until reply (default behavior)
  --no-wait            exit after inbox drop, print the written
                       filename on stdout
  --timeout DURATION   max wait duration in Go duration syntax
                       (default: 3m)
  --with-frontmatter   include YAML frontmatter when printing the
                       reply (default: body-only)
  --json               emit reply as {"frontmatter": {...},
                       "body": "..."} JSON (supersedes
                       --with-frontmatter)
  --correlation-id ID  override the auto-generated 8-char
                       correlation ID (for scripting)
```

Persona validation reuses
`internal/csuite/persona/persona.go::AllowedPersonas` (the same
closed allow-list honored by csuite-persona). `--json` wins over
`--with-frontmatter` when both are set. `-m` / `-f` / `-e` / stdin
are mutually exclusive; supplying more than one exits non-zero
with a clear diagnostic.

## Generated inbox file format

Filename:
```
<UTCTS>-operator-to-<persona>-<corrid>.md
```
Where:
- `<UTCTS>` is Go format `20060102T150405Z` (UTC, no colons — keeps
  the name filesystem-safe across any of the operator's machines).
- `<persona>` is the destination persona (kyle | mike | alex | seth).
- `<corrid>` is 8 hex chars from `crypto/rand.Read`, overridable by
  `--correlation-id` for deterministic tests and scripting.

File body:
```
---
from: operator
to: <persona>
topic: <topic>
sent_at: <RFC3339 UTC>
correlation_id: <corrid>
---
<body>
```

Written atomically via `os.WriteFile(tmp, ...)` + `os.Rename(tmp,
final)` so a mid-write crash never leaves a half-formed file for
`csuite-persona` to pick up.

## ClassOperator routing (Phase 1)

Core watcher change — adds a routing class peer to `ClassPersona`
and `ClassQuarantine`:

- **`internal/deliver/classify.go`**: declare `ClassOperator =
  "operator"` in the const block beside the existing classes. In
  the `classifyBytes` switch on `toNode.Value`, add an
  `"operator"` arm returning `Classification{Class: ClassOperator,
  Dest: "operator"}`. Do NOT add operator to the persona list —
  operator is a destination, not a source.
- **`internal/deliver/deliver.go::deliver`**: add a `case
  ClassOperator:` arm that delegates to `h.deliverToInbox(req,
  class)` the same way `ClassPersona` does. The existing
  `deliverToInbox` path-builder formats the destination as
  `/csuite/%s/inbox/%s` — no change needed; it already treats the
  destination name as opaque.
- **`internal/deliver/rescan.go::rescanPersona`**: add the same
  `case ClassOperator:` arm to the switch on `class.Class`.
  `rescanPersonas` (the source list) is NOT extended — operator has
  no outbox, so there is nothing to rescan FROM. Operator is
  destination-only.
- **`validPersonas` map in `deliver.go`**: NOT extended. Operator
  is not a valid `source_persona` in `DeliverRequest` payloads; the
  watcher accepts outbox writes from personas only. A persona that
  writes `from: operator, to: mike` in its outbox frontmatter will
  still be classified correctly on the `to:` side — the frontmatter
  `from:` field is informational, not used for auth.
- **Filename landed in operator inbox**: matches any other persona
  inbox — `<RFC3339>-<source>-<sha8>.md`, where `<source>` is
  whichever persona replied. The watcher's existing filename builder
  in `deliverToInbox` produces this automatically.
- **Operator inbox directory provisioning**: created host-side by
  `drem project register` (and `--update`) alongside the four
  persona trees, matching the pattern where host-side registration
  provisions `<CsuiteHomeRoot>/<persona>/{inbox,outbox,archive,...}`
  trees and the compose `{{.CsuiteHomeRoot}}:/csuite:rw` mount
  carries them into the watcher container. The watcher already has
  `:rw` on the entire tree, so the container side needs no change.
  Preferred over adding an `ensureCsuiteRoot` bootstrap step to
  the watcher — matches how other persona dirs come into existence.
- **No new filename-router arm** in `deliverToInbox` — the existing
  `fmt.Sprintf("/csuite/%s/inbox/%s", class.Dest, filename)` string
  handles any destination name, operator included.

## CLI internals (Phase 2+)

Module layout — each file pulls its own test file as a sibling:

- **`cmd/drem/csuite_send.go`** — command definition. Flag parsing
  (`flag.NewFlagSet("csuite send", ...)`), body-source resolution
  (picks one of `-m` / `-f` / `-e` / stdin), calls the writer and
  waiter, prints reply (or filename on `--no-wait`).
- **`cmd/drem/csuite_send_writer.go`** — `writeInboxMessage(ctx,
  opts) (written string, err error)`. Generates the correlation
  ID (or uses `opts.CorrelationID`), formats the frontmatter +
  filename, writes atomically to `<CsuiteHomeRoot>/<persona>/
  inbox/`. No network, no waiter-coupling. Accepts an injectable
  `corridGen func() string` so tests get determinism.
- **`cmd/drem/csuite_send_waiter.go`** — `waitForReply(ctx,
  operatorInbox, corrid, timeout) (filePath string, fm map[string]
  any, body string, err error)`. Reads
  `<CsuiteHomeRoot>/operator/inbox/`, parses frontmatter of each
  `.md` (cap at `frontmatterCap` = 64 KiB to match
  `internal/deliver/classify.go`), matches on
  `correlation_id == corrid` OR (fallback) `to == operator AND
  sent_at > request.sent_at` when `in_reply_to` is missing (see
  risks). Uses an `fsnotify` watch when available, falls back to a
  1s polling loop. Respects `ctx.Done()` for `--timeout`.
- **`cmd/drem/csuite_send_test.go`** — unit tests per §Tests.

Dependencies:
- Reuses `gopkg.in/yaml.v3` (already vendored; same lib
  `internal/deliver/classify.go` uses for frontmatter parsing). Do
  NOT add a second YAML lib.
- `crypto/rand` + `encoding/hex` for correlation ID generation.
  Wrapped behind a package-level `var defaultCorridGen = func()
  string { ... }` so tests can monkey-patch.
- `fsnotify` is already a transitive dep of several internal
  packages; if the waiter picks it up, reuse the existing version
  in `go.mod`. Polling fallback keeps the hard dep optional.

Registration wiring: `cmd/drem/csuite.go::dispatchCsuite` adds a
`case "send":` arm calling `runCsuiteSend(args[1:], stdout,
stderr)`. The top-level help text gains a `send` entry alongside
`audit`.

## Phase breakdown

The phase shape mirrors `plans/container-kyle-transition.md` —
small, independently-shippable, each commit leaves tests green.

### Phase 1 — ClassOperator routing (~60 LOC + tests, ~45 min)

Exact files touched:
- `internal/deliver/classify.go` (+~15 LOC) — new const, new switch
  arm.
- `internal/deliver/classify_test.go` (+~20 LOC) — new
  `TestClassifyBytes_Operator` asserting both the class and the
  dest.
- `internal/deliver/deliver.go` (+2 LOC case arm in the deliver
  switch).
- `internal/deliver/rescan.go` (+2 LOC case arm in the rescan
  switch).
- Host state-dir provisioning — `grep -rn "mkdir.*drem-csuite"
  internal/ cmd/` first to locate. Extend the provisioner to
  create `<CsuiteHomeRoot>/operator/inbox/` (and `.archive/`) on
  `drem project register` / `--update`. Touch the matching test
  (likely under `internal/projects/` or `cmd/drem/project_test.go`)
  to assert the operator inbox tree is created.

Green test: `TestClassifyBytes_Operator` — feed a payload with
`to: operator` to `classifyBytes`, expect `ClassOperator` +
`Dest: "operator"` + no quarantine.

Green integration test: if the package ships a
`TestDeliver_Integration`-style test, extend it with an
operator-addressed payload and assert the file lands in
`<CsuiteHomeRoot>/operator/inbox/`. (Same shape as the existing
persona integration test — likely a helper already builds a fake
outbox tree.)

Commit: `feat(deliver): add ClassOperator routing for operator-
addressed replies`.

### Phase 2 — `drem csuite send` core CLI (~250 LOC + tests, ~1.5 h)

Scope: `-m` inline string, stdin, `-t` topic, `--wait` (default) /
`--no-wait`, `--timeout`, `--correlation-id`. Body-only reply
output (no `--with-frontmatter` / `--json` yet — those land in
Phase 3).

New files:
- `cmd/drem/csuite_send.go`
- `cmd/drem/csuite_send_writer.go`
- `cmd/drem/csuite_send_waiter.go`
- `cmd/drem/csuite_send_test.go`

Modified:
- `cmd/drem/csuite.go::dispatchCsuite` — add `case "send":` arm;
  update help text.
- Any help-text golden test under `cmd/drem/` that asserts the
  subgroup list.

Unit tests:
- `TestCsuiteSend_WriterGeneratesValidFrontmatter` — given a
  deterministic corrid generator, a tempdir as the fake
  `<CsuiteHomeRoot>`, and a known body, the writer produces the
  expected filename + content byte-for-byte.
- `TestCsuiteSend_WaiterMatchesByCorrelationID` — stage a reply
  file in the tempdir, call waiter, expect it to return the body.
- `TestCsuiteSend_WaiterTimeoutReturnsNonZero` — empty inbox,
  tight timeout, waiter returns a typed timeout error; CLI
  translates it to a non-zero exit.
- `TestCsuiteSend_StdinAndMessageAreMutuallyExclusive` — flag
  conflict diagnostic.

Commit: `feat(cli): drem csuite send — one-shot persona messaging
with reply wait`.

### Phase 3 — Editor, file, and JSON output (~80 LOC + tests, ~45 min)

Scope additions:
- `-e / --editor` — write a tempfile with a commented instructional
  header (`# Lines starting with '#' are ignored. Write your
  message below.`), open `$EDITOR` (default `vi`), strip commented
  lines on read, refuse if body is empty after strip.
- `-f / --file PATH` — read body from the given file; refuse
  directories, symlinks resolving outside `$HOME`, and files
  > 64 KiB.
- `--with-frontmatter` — include the reply's YAML frontmatter in
  plain-text output.
- `--json` — wrap `{"frontmatter": {...}, "body": "..."}` as
  canonical JSON. `--with-frontmatter` is implied + overridden.

Unit tests:
- `TestCsuiteSend_EditorInvocation` — stub `$EDITOR` to a `sh -c`
  script that writes a canned body; assert the tempfile was seeded
  with the instructional header and stripped correctly.
- `TestCsuiteSend_FileMode` — tempfile + path handed to `-f`;
  assert writer produces the expected content.
- `TestCsuiteSend_JSONOutput` — stage a reply, run with `--json`,
  assert the output parses as `{"frontmatter": {...}, "body":
  "..."}`.
- `TestCsuiteSend_WithFrontmatter` — same stage, with
  `--with-frontmatter`, assert the plain-text output begins with
  `---\n`.

Commit: `feat(cli): drem csuite send — editor, file, and json
output modes`.

### Phase 4 — `drem csuite inbox` companion (~60 LOC + tests, ~30 min)

Three subcommands under the same `dispatchCsuite`:

- `drem csuite inbox list` — enumerate files in
  `<CsuiteHomeRoot>/operator/inbox/`. Output columns: timestamp
  (local), from, topic, first body line (truncated to 60 chars).
  `--json` flag for machine-readable.
- `drem csuite inbox read <file|index>` — print one file's body,
  or full content with `--with-frontmatter`. Accepts either the
  filename as seen in `list` or a 1-based index.
- `drem csuite inbox archive <file|index>` — move to
  `<CsuiteHomeRoot>/operator/inbox/.archive/`. `mkdir -p` the
  archive dir if missing (matches Phase 1 provisioning).

New files:
- `cmd/drem/csuite_inbox.go`
- `cmd/drem/csuite_inbox_test.go`

Modified:
- `cmd/drem/csuite.go::dispatchCsuite` — add `case "inbox":` arm.

Unit tests:
- `TestCsuiteInbox_List` — stage three files in a tempdir, assert
  output lines + order (newest first).
- `TestCsuiteInbox_ReadByIndex` — stage files, read `--index 2`,
  assert body matches.
- `TestCsuiteInbox_Archive` — stage a file, archive it, assert
  the file moved and that `list` no longer shows it.

Commit: `feat(cli): drem csuite inbox — list/read/archive operator
inbox`.

### Phase 5 — Persona prompts + image rebuild (~40 LOC docs + ops, ~30 min)

Add a `## Replying to the operator` section to each of:
- `docs/csuite-agents/prompts/kyle.md`
- `docs/csuite-agents/prompts/mike.md`
- `docs/csuite-agents/prompts/alex.md`
- `docs/csuite-agents/prompts/seth.md`

Verbatim block (copy-paste into each prompt, sibling to the existing
`## Runtime mode` preamble that Phase 4 of container-kyle-transition
added):

```
## Replying to the operator

When you receive an inbox message with `from: operator`, your reply
goes to a dedicated operator inbox at `/csuite/operator/inbox/` (the
watcher routes `to: operator` there — see
`plans/drem-csuite-send-cli.md`). Your outbox file should:

- Set `to: operator` in the frontmatter.
- Copy the sender's `correlation_id` verbatim into an
  `in_reply_to:` field in your frontmatter. This lets the
  operator's `drem csuite send --wait` command pick up your reply
  without ambiguity.
- Use the filename convention `<UTCTS>-<your-persona>-to-operator-<corrid>.md`
  matching your own persona's naming style. Watcher classifier
  reads the frontmatter `to:` field for routing, but the filename
  convention keeps operator workflows consistent.

Reply body should be direct and concise — the operator is reading
this at a terminal, not in a browser. Plain markdown, no HTML, no
embedded images.
```

Then rebuild + recreate:
```bash
bash deploy/docker/build-csuite.sh
sg docker -c "docker compose -f ~/.drem/projects/drem-orchestrator/compose.yml up -d --no-deps --force-recreate csuite-kyle csuite-mike csuite-alex csuite-seth"
```

Live smoke-test: `drem csuite send mike -m "smoke test"` — expect a
reply within 3m, printed body-only to stdout, non-zero exit on
timeout.

Commit: `docs(csuite): persona prompts — operator-reply protocol`.

## Tests / acceptance criteria

- `go test ./...` stays green after every phase commit. If any
  package's test run currently touches the network, this plan does
  not change that — CLI tests are strictly tempdir + in-process.
- **Phase 1:** `TestClassifyBytes_Operator` covers the new branch;
  the host-provisioning test asserts `<CsuiteHomeRoot>/operator/
  inbox/` is created on `drem project register --update`.
- **Phase 2:** `TestCsuiteSend_WriterGeneratesValidFrontmatter`,
  `TestCsuiteSend_WaiterMatchesByCorrelationID`,
  `TestCsuiteSend_WaiterTimeoutReturnsNonZero`, and an integration
  test with a tempdir simulating the operator inbox being fed a
  mock persona reply file mid-wait.
- **Phase 3:** `TestCsuiteSend_EditorInvocation`,
  `TestCsuiteSend_FileMode`, `TestCsuiteSend_JSONOutput`,
  `TestCsuiteSend_WithFrontmatter`.
- **Phase 4:** `TestCsuiteInbox_List`,
  `TestCsuiteInbox_ReadByIndex`, `TestCsuiteInbox_Archive`.
- **Phase 5:** live end-to-end smoke — `drem csuite send mike -m
  "smoke"` returns a body-only reply within 3 minutes on the
  dogfood host.

## Risks + mitigations

| Risk | Severity | Mitigation |
|---|---|---|
| Interactive-Kyle coexistence rules (from container-kyle Phase 4) forbid writes to `~/.drem-csuite/kyle/`; `drem csuite send kyle` writes into Kyle's inbox — is that a violation? | Low | No. Phase 4 of container-kyle-transition.md explicitly names `state.md`, `restart-context.md`, `heartbeat`, `outbox/`, `inbox/archive/` as container-Kyle's exclusive write domain. Inbox drops are explicitly allowed: "If operator is willing, they can drop markdown files into `~/.drem-csuite/kyle/inbox/` and container-Kyle will process them". The CLI writes to kyle's INBOX — container-Kyle's read side, not its write side. No race. |
| Large body size or binary content in `-f` mode | Low | Enforce 64 KiB max body size (matches `frontmatterCap` in `classify.go`) and refuse non-UTF-8 input. Same ceiling the watcher already honors. |
| Stale operator inbox accumulation | Low | Phase 4 ships `drem csuite inbox archive`. Operator housekeeping responsibility; no auto-reap in v1. |
| Persona prompts do not honor `in_reply_to` yet | Medium | Phase 5 updates all 4 prompts. But v1 waiter ALSO handles the degraded case: if a reply arrives with `to: operator` and no `in_reply_to`, the waiter falls back to matching on `(to == operator) AND (sent_at > request.sent_at)` — first matching file after the send timestamp wins. This keeps the CLI usable during the rebuild window between Phase 1 landing and Phase 5 reaching every container. |
| Concurrent `drem csuite send` invocations racing for the same operator inbox | Low | Each `send` writes a unique 8-hex correlation ID and the waiter matches on that ID first. Two concurrent waits for different corrids never collide. Two concurrent waits with the same corrid (impossible without `--correlation-id` collision) would see the same reply — harmless. |
| Watcher `CSUITE_WATCHER_TOKEN` 401 fast-path issue known-unfixed (see `plans/agentmon-auth-401-fix.md` / restart-context §Known issues #1) | Low | v1 CLI relies on the watcher's 5-min rescan path — same as everything else today. When the 401 fix lands, CLI latency improves automatically. Not a blocker. |
| `fsnotify` unavailable in some dev environments | Low | Waiter falls back to a 1-second polling loop. Same shape used by `internal/csuite/persona/poller.go`. |
| `$EDITOR` unset or pointing at a broken binary | Low | Default to `vi`; non-zero exit from the editor aborts the send with a clear "editor exited non-zero, message not sent" diagnostic. |
| Operator writes to `<CsuiteHomeRoot>/operator/inbox/` while Phase 1 is landed but before Phase 2 CLI exists | Low | The inbox is host-side filesystem; plain `ls`/`cat` work. No breakage. |

## Rollback

If the v1 CLI misbehaves, Phase 1's `ClassOperator` routing is
still useful standalone: personas can send `to: operator` and the
watcher delivers to `<CsuiteHomeRoot>/operator/inbox/`, which the
operator reads via plain `cat` / `ls`. Deleting `cmd/drem/
csuite_send.go` + `csuite_send_writer.go` + `csuite_send_waiter.go`
+ `csuite_inbox.go` (and the `case "send"` / `case "inbox"` arms in
`cmd/drem/csuite.go`) reverts the CLI surface without touching
Phase 1. No data loss path. The operator inbox directory stays;
it's just a directory.

## Out of scope

- Broadcast / multi-recipient send.
- TUI surface.
- Editable reply threading (e.g. `drem csuite reply <corrid>`).
- `drem csuite tail` for live log-watching a persona's turn.
- Installing the `drem` CLI inside `csuite-base` so container-Kyle
  could invoke `drem csuite send` itself. (Noted in the
  container-Kyle plan's risk table; still deferred.)
- Auto-archiving operator inbox files after N days.

## Decision requested

Status: operator-ratified 2026-04-22. No further decision required
before implementation. Proceeds directly to Phase 1.

## Estimated totals

| Phase | LOC | Time |
|---|---|---|
| 1. ClassOperator routing | ~60 | 45 min |
| 2. `drem csuite send` core CLI | ~250 | 1.5 h |
| 3. Editor, file, JSON output | ~80 | 45 min |
| 4. `drem csuite inbox` companion | ~60 | 30 min |
| 5. Persona prompt updates + rebuild | ~40 docs + ops | 30 min |
| **Total** | **~490 LOC + ops** | **~4 h** |
