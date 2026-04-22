# Plan: csuite-persona auto-reply routing

**Status:** recon-only, not yet started (2026-04-21). Dispatch a separate
subagent to land the fix once an option below is approved.

**Related:**

- `plans/csuite-watcher-outbox-routing.md` — frozen design for the
  watcher's `/deliver` path. This plan sits on top of it: the watcher
  is doing exactly what §5 of that plan mandates; the problem is on
  the producer side.
- `plans/csuite-persona-pivot.md` — the poller that emits the stubs.
- `internal/deliver/classify.go` — the classifier that quarantines them.
- `internal/csuite/persona/poller.go` — the producer of the stubs.
- `docs/csuite-agents/prompts/seth.md` §"Message format" — documents
  the frontmatter shape personas are supposed to emit.

## Problem statement

The csuite watcher is ledger-dominant-quarantine over the last
~10 hours: every automatic reply produced by a csuite-persona
container lands in `/csuite/quarantine/<source>/` instead of the
intended recipient's inbox. Meanwhile, the operator hand-writes fully
formed `<timestamp>-<from>-to-<to>-<subject>.md` files straight into
Seth's outbox and the watcher routes those correctly — but the
operator has also been hand-depositing messages directly into
Kyle's inbox when speed matters, bypassing the pipeline entirely.

### Data (2026-04-22 ~03:20 UTC)

Fresh watcher SQLite copy (with WAL checkpoint) shows 119 total
deliveries:

| dest         | count |
| ------------ | ----- |
| `quarantine` | 102   |
| `kyle`       | 10    |
| `alex`       | 4     |
| `seth`       | 2     |
| `mike`       | 1     |

Split by origin:

| kind    | quarantined | routed |
| ------- | ----------- | ------ |
| stub    | 21          | 2*     |
| curated | 81**        | 15     |

\* The two "routed" stubs are hand-authored files whose filenames
happen to contain `-reply-` but carry valid frontmatter. Every
persona-emitted stub (100%) is quarantined.
\*\* 81 curated-but-quarantined are historical pre-watcher audit
reports (March-era violation reports, audit dumps) that never had
frontmatter; these were swept into quarantine by a single rescan pass
at 2026-04-21 17:43:26 UTC when the watcher first came online.

Of the 15 most-recent deliveries, 12 are quarantine, 2 are kyle,
1 is alex. The watcher log (`docker logs
drem-orchestrator-csuite-watcher-1 | grep quarantine`) confirms every
stub quarantine entry since 18:10:14 UTC carries
`reason="no frontmatter delimiters"`.

### Seth outbox snapshot

Seth's outbox currently holds 11 `*-seth-reply-<10hex>.md` stub files
and 6 `*-seth-to-<recipient>-<subject>.md` curated files (the rest
are older pre-pivot artefacts). The stubs are 6–1800 bytes of plain
markdown; the curated files open with a `---` frontmatter block
carrying `from:`, `to:`, `subject:`, `priority:`, `type:`, `tldr:`
fields. Representative stubs:

- `20260421T125934Z-seth-reply-d7ace4f9d2.md` — 6 bytes: `alive\n`.
- `20260421T152418Z-seth-reply-d3dfc140f2.md` — 1592 bytes, a
  "Turn complete. Summary of work:" prose reply with no
  frontmatter.
- `20260422T021208Z-seth-reply-1006c6aa2d.md` — 1638 bytes, the
  persona narrating what it wrote elsewhere; no frontmatter.

## Goals

- Ground-truth the watcher's quarantine decision for this file class
  so the fix targets the real check, not a suspected one.
- Give the operator four ranked options with LOC estimates and blast
  radius so the next session can approve-and-dispatch without
  re-litigating the design space.
- Leave behind a regression-proof hook so the next session picks up
  the quarantine-rate regression automatically.

## Non-goals

- Rewriting the csuite message format. The operator's curated files
  already match `docs/csuite-agents/prompts/seth.md` §"Message
  format"; that format is load-bearing for Kyle's host-side tooling
  and the watcher's routing. Do not invent a new schema.
- Retrying quarantined stubs after a fix. The 21 stubs already in
  quarantine stay there — content is low-value turn-summary prose,
  all relevant information has already been hand-forwarded.
- Migrating the 81 historical curated-but-quarantined files. They are
  old audit reports the operator has already consumed; a follow-up
  cleanup can drop `quarantine/` wholesale once this fix lands.

## Root cause

Located at `internal/deliver/classify.go:72-93`:

```go
func classifyBytes(data []byte) (Classification, error) {
    body, ok := extractFrontmatter(data)
    if !ok {
        return Classification{Class: ClassQuarantine, Reason: "no frontmatter delimiters"}, nil
    }
    ...
}
```

`extractFrontmatter` (same file, lines 125-135) requires the payload
to start with the literal bytes `---\n` and contain a matching
`\n---` delimiter. The persona poller's `recordSuccess`
(`internal/csuite/persona/poller.go:237-248`) writes whatever
`claude -p` emitted on stdout into the outbox without wrapping or
templating:

```go
outName := fmt.Sprintf("%s-%s-reply-%s.md",
    now.UTC().Format("20060102T150405Z"),
    p.cfg.Persona,
    shortID,
)
outPath := filepath.Join(p.cfg.OutboxDir, outName)
if err := os.WriteFile(outPath, stdout, 0o644); err != nil { ... }
```

The persona *prompts* (e.g. `docs/csuite-agents/prompts/seth.md`
§"Message format") document the frontmatter shape, but nothing in
the poller enforces it. When Claude-as-Seth emits a terse
"Turn complete. Summary:..." reply with no frontmatter, the bytes
land on disk unchanged, the post-write signal fires, and the watcher
quarantines the file on first contact. The existing regression test
`TestClassifyBytes_NoFrontmatterQuarantines`
(`internal/deliver/classify_test.go:146-155`) confirms this is the
code path hit — so the watcher is behaving per spec. The bug is the
producer emitting unspec'd payloads.

### DB-read gotcha for future recon

SQLite WAL mode: `docker cp deliveries.db` alone copies only the
last checkpoint. Always copy `deliveries.db{,-wal,-shm}` together and
run `PRAGMA wal_checkpoint(TRUNCATE)` before querying.

## Design options

### Option A — fix the persona stub writer

Wrap the poller's outbox write with a frontmatter header derived
from state the poller already has. Change site:
`internal/csuite/persona/poller.go:237-263`. Shape: parse the inbox
message's frontmatter (always valid — hand-written), extract its
`from:`, reply `to: <that>`. Inject a `---\n` header block with
`from`, `to`, `timestamp`, `subject` (auto-derived from inbox
filename), `type: report`, `priority: low`, `tldr` (first line of
stdout). Empty inbox-from falls back to `kyle` via new
`CSUITE_AUTO_REPLY_FALLBACK` env var.

- **LOC:** ~40 lines production + ~60 lines tests.
- **Risk:** low. One new code path in the poller; the watcher is
  unchanged. The existing `TestClassifyBytes_*` suite continues to
  pin watcher behaviour. Kyle's host-side inbox reader already
  tolerates the frontmatter (it's the documented format).
- **Gotcha:** the stub's body often contains unescaped `---` lines
  (Seth uses them as section dividers). The header's closing
  `\n---\n\n` delimiter still lands first, so
  `extractFrontmatter`'s `bytes.Index(rest, "\n---")` returns the
  real close; but the injected `subject`/`tldr` values must be
  YAML-quoted (`%q` via `fmt.Sprintf`) so a newline in either one
  can't prematurely close the block.

### Option B — relax the watcher classifier

Teach `classifyBytes` to infer a destination from the filename
pattern `<timestamp>-<persona>-reply-<hash>.md` and a new
`CSUITE_DEFAULT_AUTO_REPLY_DEST` env var on the watcher. A
`Classification{Class: ClassAutoReply, Dest: default}` result
routes to that destination with a minimal synthetic
frontmatter prepended during the inbox write.

- **LOC:** ~80 production (classifier change, new env read in
  `cmd/csuite-watcher/serve.go`, synthetic-header injection in
  `deliver.go`) + ~100 tests. Watcher rebuild + redeploy required.
- **Risk:** medium. The watcher is the single trusted router; widening
  its classifier surface means every future schema drift on the
  persona side silently reaches inboxes. Reverses the §8 "watcher
  stays dumb" decision from `plans/csuite-watcher-outbox-routing.md`.
- **Note:** operator has already explicitly chosen the dumb-watcher
  path in Q7; this option requires revisiting that decision.

### Option C — retire the stub path entirely

Change the poller to NOT write a stub when the Claude subprocess
produces output with no frontmatter. Log + increment a metric;
archive the inbox message; update state.md; emit nothing to the
outbox. Option-A-level fix happens only for explicit replies that
Claude tagged with frontmatter itself.

- **LOC:** ~10 lines production + ~30 tests.
- **Risk:** medium-high. The operator currently relies on the stub
  files as a turn-completion heartbeat ("Seth is alive, here's what
  he did this turn"). Dropping them leaves a gap in forensic signal
  unless state.md gains equivalent "last turn summary" data.
  Acceptable if the persona prompts are concurrently updated to
  write richer state.md entries.

### Option D — route stubs to `/drafts`, not `/quarantine`

New watcher destination class `ClassDraft` routes no-frontmatter
files from persona outboxes to `/csuite/<source>/drafts/` (a new
dir) with a ledger row marked `dest="draft"`. Quarantine is
reserved for parse errors and unknown recipients. Operator can then
scan `drafts/` for turn signal without it polluting the quarantine
audit stream.

- **LOC:** ~50 production + ~60 tests.
- **Risk:** medium. Adds a concept to the routing vocabulary without
  fixing the underlying "personas emit unspec'd files" problem. Still
  leaves every automatic reply un-delivered to its intended recipient;
  the operator keeps hand-forwarding. Value is mostly cosmetic.

## Recommendation

**Option A.** The smallest, most-localised fix that restores the
stated invariant ("every outbox file routes to a persona inbox") and
aligns producer output with the documented message format. The 40
lines of production code sit entirely inside the poller; the
watcher's frozen contract stays intact. The new recipient-inference
helper is a pure function (read one file, return a string) and is
trivially testable with a table-driven test using a couple of real
inbox fixtures.

The fallback env var `CSUITE_AUTO_REPLY_FALLBACK=kyle` gives the
operator an escape hatch for the case where the inbox message has no
parseable `from:` (e.g. an operator-hand-deposited probe file).

Option C is a clean second choice if, in review, the operator
decides the stubs carry no content worth routing. It's a strictly
smaller change but requires a separate state.md richness pass to
preserve forensic signal.

## Regression proof

Two test additions catch this regression automatically:

1. **Producer test** in `internal/csuite/persona/poller_test.go`:
   feed the poller an inbox message with a valid frontmatter `from: kyle`,
   stub a Claude spawner that returns plain-text "Turn complete." on
   stdout, assert the outbox file starts with `---\n` and parses
   through `deliver.ClassifyFile` with `Class == ClassPersona` and
   `Dest == "kyle"`. This pins the producer-side contract.

2. **Quarantine-rate ledger test** in
   `internal/deliver/ledger_test.go` (or a new
   `internal/deliver/integration_test.go`): exercise the full
   `/deliver` path through a tempdir-rooted persona outbox, write N
   stubs, POST N signals, assert `SELECT COUNT(*) WHERE dest =
   'quarantine'` is zero. Runs fast; catches either side of the
   pipeline regressing.

A captured real-world stub payload lives at
`internal/deliver/testdata/auto-reply-stub-seth.md` (committed
alongside this plan) so producer-side tests use the actual problem
shape, not a synthetic one.

## Sequencing

- **Blocks:** nothing. This is a standalone fix; the watcher stays
  healthy while the producer is patched.
- **Blocked by:** nothing. Operator decision on option A vs C is the
  only gate.
- **Follow-ups** (out of scope for this plan):
  - Cleanup of the 81 historical quarantine entries (safe one-shot
    `rm -rf ~/.drem-csuite/quarantine/` after backup).
  - Persona prompt hardening: add a "if you produce a reply, the
    poller wraps it — you do not need to emit frontmatter yourself"
    disclaimer so the prompt and the implementation converge on
    intent.
  - Consider extending Option A's helper to also populate `subject:`
    from the inbox message's own `subject:` (prefixed with "Re: ").

## References

- **Ledger:** all 119 rows in `/var/lib/watcher/deliveries.db` as of
  2026-04-22 03:20 UTC (after WAL checkpoint).
- **Outbox files sampled:** `~/.drem-csuite/seth/outbox/`
  `20260421T125934Z-seth-reply-d7ace4f9d2.md` (6-byte "alive\n"
  stub); `20260421T152418Z-seth-reply-d3dfc140f2.md` (1592-byte
  prose stub, no frontmatter); `20260422T021011Z-seth-to-kyle-audit-cli-spec.md`
  (9052-byte curated, routes to kyle).
- **Code paths involved:**
  - `internal/deliver/classify.go:72-135` — classifier + extractor.
  - `internal/deliver/deliver.go:190-214` — quarantine write path.
  - `internal/deliver/rescan.go:165-186` — rescan quarantine path.
  - `internal/csuite/persona/poller.go:237-263` — stub writer;
    single point of change for Option A.
  - `internal/csuite/persona/poller.go:481-492` — `shortHash` (the
    10-hex-char stub suffix).
  - `internal/csuite/persona/subprocess.go:45-82` — Claude spawner;
    stdout becomes the stub body verbatim.
  - `docs/csuite-agents/prompts/seth.md:384-409` — documented
    message format.
- **Test fixture added:**
  `internal/deliver/testdata/auto-reply-stub-seth.md` — exact
  bytes of a real stub captured from Seth's outbox, for
  future regression tests against the producer side.
