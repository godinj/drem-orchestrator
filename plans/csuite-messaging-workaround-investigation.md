# Investigation: why csuite persona messaging still needs operator workarounds

**Status:** recon-only, 2026-04-22. No code changes. Extends
`plans/csuite-persona-auto-reply-routing.md` (still un-implemented) with
gap analysis covering the claude-invocation failure path, orphan
`.failures` sidecars, stale 361-file Kyle inbox, and the dual-write
curated+stub emission pattern.

## Executive summary

The quarantine bug diagnosed in `csuite-persona-auto-reply-routing.md`
is untouched on master — Option A (~40 LOC frontmatter-wrapping in
`internal/csuite/persona/poller.go`) has not been implemented and every
`*-seth-reply-*.md` stub continues to land in `~/.drem-csuite/quarantine/`.
Inbound routing (Kyle → Seth inbox) is healthy; curated persona-emitted
replies (`*-seth-to-kyle-*.md`) ARE routed correctly to Kyle's inbox.
What the existing plan does not cover and is independently broken: (a)
Kyle's inbox has 361 un-consumed files because `drem-kyle` the binary
does not read its own inbox, (b) Seth's `claude -p` subprocess has hit
exit=-1 twice recently, leaving orphan `.failures=1` sidecars with no
retry path and no operator-visible alert, and (c) the persona prompts
instruct Claude to hand-write the curated `*-to-<dest>-*.md` AND then
the poller blindly wraps the subprocess stdout into a stub, so every
successful turn emits TWO files, doubling quarantine noise even after
Option A lands.

## What's still broken (workarounds operator has to run today)

1. **Operator hand-writes Seth's outbox files with frontmatter.** Reason:
   persona poller's `recordSuccess` writes raw Claude stdout to the
   outbox without wrapping, watcher quarantines on "no frontmatter
   delimiters". Confirmed unchanged since the plan was filed:
   `internal/csuite/persona/poller.go:237-263` still calls
   `os.WriteFile(outPath, stdout, 0o644)` with no frontmatter template.

2. **Operator hand-deposits messages directly into Kyle's inbox.** Reason:
   Kyle's inbox (`~/.drem-csuite/kyle/inbox/`) is a write-only mailbox
   from the system's perspective — the `drem-kyle` Go binary does NOT
   poll it. See `cmd/drem-kyle/main.go` + `internal/kyle/` — zero
   references to `.drem-csuite`, zero inbox-scanning code. The 361 files
   accumulated there are a mix of legitimately-routed persona replies
   (see Apr 21 17:22-18:06 cluster from the watcher rescan) and March-era
   manual drops. None have been consumed by Kyle the binary, because
   Kyle the binary has no concept of an inbox.

3. **Operator forwards persona replies into the next message manually.**
   Kyle-the-human-operator reads `~/.drem-csuite/kyle/inbox/`, picks what
   matters, and pastes it into Claude Code as fresh context. The `drem
   cli` has no `kyle inbox read` / `kyle inbox ack` verb. This was not
   scoped in the existing plan — it is a separate product gap.

4. **Operator retries Claude-failed inbox messages manually.** When
   Seth's `claude -p` subprocess exits -1 (two occurrences since watcher
   restart: 2026-04-21T22:13:44Z for `20260421T220750Z-kyle-csuite-audit-cli-design.md`
   and 2026-04-22T00:00:12Z for `20260421T235306Z-kyle-tui-retry-storm-design.md`),
   the poller writes a `.failures` sidecar but does NOT archive the
   original; Claude Code may have already moved the original via its
   Bash tool. Result: `.md` is gone, `.md.failures=1` orphan remains, no
   visible alert surface.

5. **Operator clears watcher quarantine periodically.** 102 files in
   `~/.drem-csuite/quarantine/` as of investigation time: 63 from seth,
   35 from alex, 3 from ross, 2 from mike. No automated cleanup.

## What the existing plan covers (and does not)

The plan at `plans/csuite-persona-auto-reply-routing.md` covers:

- Root cause: `internal/deliver/classify.go:72-93` requires frontmatter
  delimiters, producer (`poller.go:237-248`) emits raw Claude stdout.
- Four fix options (A–D) with LOC estimates + risk analysis.
- Recommendation of Option A (wrap stub with synthesized frontmatter).
- Regression proof (two proposed test additions).
- Test fixture at `internal/deliver/testdata/auto-reply-stub-seth.md`.

It does NOT cover:

- The `claude -p` exit=-1 failure path. The plan assumes Claude always
  produces output; in practice 2 of the last ~8 invocations have failed
  with "non-zero exit" after the 5m `claude-timeout` ceiling. That
  failure bypasses `recordSuccess` entirely — it goes through
  `recordFailure`, which bumps `.failures` and leaves a sidecar. If the
  Claude subprocess used its Bash tool to move the inbox file into
  `.archive` before failing (the "already moved before archive"
  log-line), the failure counter points at a file that no longer
  exists in the inbox. Option A does not help this path.

- The **curated + stub dual-write pattern**. Inspection of Seth's outbox
  shows every successful turn produces BOTH a `*-seth-to-kyle-<subj>.md`
  (Claude-authored via its Write tool, proper frontmatter, routes
  correctly) AND a `*-seth-reply-<hash>.md` (poller-authored from stdout,
  no frontmatter, quarantines). Example: 2026-04-22T06:47:30Z Seth
  wrote `20260422T064730Z-seth-to-kyle-bug-h-bug-i-plan-review-response.md`
  (6634B, valid frontmatter, routes to Kyle) AND two minutes later the
  poller wrote `20260422T064927Z-seth-reply-243b174302.md` (1701B, no
  frontmatter, quarantines). Option A adds frontmatter to the stub but
  the stub's body is just a `"Turn complete. Response summary: …"`
  prose digest — it is fundamentally a *second copy* of what the curated
  file already said. Routing it to Kyle too means Kyle sees two near-
  duplicate messages per turn.

- The 361-file Kyle-inbox pileup. The plan notes "operator hand-
  deposits" into Kyle's inbox but does not address that Kyle-the-binary
  cannot consume anything there. This is not a plan-option-A problem;
  it is a separate product gap.

- Kyle-as-persona vs Kyle-as-binary split responsibility. Kyle's
  container is `drem-kyle` running a Go HTTP server (`:8090`, polling
  orch `/tasks`); that binary is not the consumer of the csuite
  pipeline. The csuite pipeline treats `kyle` as a valid `to:`
  destination (see `classify.go:100-101`) but there is no process on
  the receiving side of that queue.

## Gap analysis

### Gap G1 — Claude subprocess failure leaves orphan sidecars

`internal/csuite/persona/poller.go:462` puts the sidecar at
`<inboxDir>/<name>.failures`. When the Claude subprocess moved the
inbox file into `.archive` via its Bash tool before exiting non-zero,
the sidecar's anchor file is gone and the counter becomes an orphan.
Current evidence: `~/.drem-csuite/seth/inbox/20260421T235306Z-kyle-tui-retry-storm-design.md.failures`
(2 bytes, contains "1\n") but the `.md` is missing from both inbox and
archive; same pattern in `alex/inbox/2026-04-21T17:43:26Z-alex-4c24a59c.md.failures`.
The poller will never retry (no `.md`), never escalate (under the
`MaxFailures` threshold), and the sidecar will stay forever.

### Gap G2 — Kyle-the-binary never reads its inbox

`cmd/drem-kyle/main.go` + `internal/kyle/*.go` have no reference to
`~/.drem-csuite/kyle/inbox/` or any inbox-polling code. Kyle's
container's only loops are the `/tasks` poller and the `:8090` HTTP
handler. Persona replies routed to Kyle's inbox by the watcher sit
there until an operator reads them out-of-band. The 361 files span
Apr 10 (pre-pivot) through Apr 21 18:06 (the most recent watcher
rescan batch). Most are legitimately-routed messages that have been
un-consumed for weeks.

### Gap G3 — Dual-write: poller stub duplicates the curated Claude file

Seth's prompt (`docs/csuite-agents/prompts/seth.md` §"Message format")
tells Claude to produce the `*-to-<dest>-*.md` itself via its Write
tool. But the poller ALSO wraps stdout into `*-reply-*.md`. Both files
fire separate watcher `/deliver` signals (curated: routed OK; stub:
quarantined). Option A would route both — Kyle gets two messages for
every turn, one full reply + one terse prose digest. The stub body is
forensic signal only ("what did Seth do this turn?"); routing it to
Kyle is not its job.

### Gap G4 — Watcher is healthy but on a stale image

`docker logs drem-orchestrator-csuite-watcher-1` shows continuous
healthy operation (13h uptime). Rescan ran at 18:07:39 and 18:08:25 on
Apr 21, then every `/deliver` POST since has either succeeded or
quarantined-with-reason exactly as `plans/csuite-watcher-outbox-routing.md`
specifies. No crash, no restart, no health regression. But the image
predates commits `83c2955` (csuite audit queue subcommand), `19188cc`
(/v1/queue handler), `fa4abd5` (/v1/deliveries handler) — so the
operator cannot run `drem csuite audit {list,queue}` against the live
watcher until it is rebuilt. This was known at last save (Kyle's
`state.md:19` "11+h old, pre-Task-#11") but is still true.

### Gap G5 — 102 files in quarantine with no UI

`~/.drem-csuite/quarantine/` has 63 seth + 35 alex + 3 ross + 2 mike
= 103 files. The existing plan §Non-goals declares "they stay there"
and proposes a one-shot cleanup as a follow-up. No CLI to list them,
no alerting when the quarantine rate spikes, no per-turn quarantine
counter surfaced anywhere.

## Recommended next steps

1. **Land the existing plan's Option A first.** It is the smallest
   change and it converts the 100%-quarantine-rate stubs into routed
   messages. All the other gaps here are additive; they do not block
   Option A.

2. **Extend Option A with a stub-content check (new sub-option A').**
   Before wrapping, inspect whether Claude already wrote a
   `*-to-<recipient>-*.md` in the outbox earlier this turn; if yes,
   SUPPRESS the stub (do not emit at all — see Gap G3). The poller
   already knows the turn's start time; `filepath.Glob` on the outbox
   for recent `*-to-*-*.md` files is cheap. This closes the dual-write
   duplicate-routing problem that Option A would otherwise amplify.

3. **File a new plan for Claude-exit-failure orphan sidecars (Gap G1).**
   Options: (a) poller synchronously re-stats the `.md` after subprocess
   exit; if missing, drop the sidecar (nothing to retry against); (b)
   emit a structured alert to a new `state.md` field `last_failure_file`
   so the operator can see it in the container logs; (c) enforce that
   the subprocess cannot move its own inbox file (revoke Bash-tool
   access to `$INBOX_DIR`) — this is a prompt-level fix, not a code-
   level one.

4. **File a new plan for "Kyle inbox has no reader" (Gap G2).** Three
   options: (a) `drem cli kyle inbox list|read|ack` subcommands that
   read the host-side inbox directly (operator tooling); (b) Kyle-the-
   binary grows an inbox-scan loop that exposes unread messages on its
   HTTP `:8090` surface for a future TUI; (c) rename Kyle's inbox to
   something like `~/.drem-csuite/operator/inbox/` to reflect that its
   consumer is a human, not a process.

5. **File a new plan for stale-image rebuild cadence (Gap G4).**
   Arguably out-of-scope for "csuite messaging" but blocks dogfooding
   the audit CLI; note it as a follow-up so the next session does not
   re-discover the problem.

6. **Sweep-clean quarantine after Option A lands.** Per existing plan §
   Non-goals: once fixed, `rm -rf ~/.drem-csuite/quarantine/` is safe
   because routing regressed files are forensic-only. Do this in the
   same session that lands Option A so the next quarantine entry is
   unambiguously new-regression signal.

The existing plan is correct about Option A being the right first
move. It is not sufficient on its own — G1 (orphan sidecars) and G2
(Kyle-inbox unread) would still leave operator workarounds in place
after Option A lands. G3 (dual-write) makes Option A worse by
routing duplicates.

## Raw evidence appendix

### E1 — No commits have landed the fix

```
$ git log --all --oneline -- internal/csuite/persona/poller.go internal/deliver/classify.go
c5433bd fix(csuite-persona): signal before archive so Claude-moved inbox still routes
fbe56db feat(csuite-persona): post-write HTTP signal behind flag
0e5f6be feat(csuite-watcher): frontmatter parse, classify, quarantine
836cdf7 fix(csuite-persona): pipe message body via stdin to avoid argv parsing
077c006 feat(cmd): add csuite-persona headless poller binary
```
Last commit is `c5433bd` (Apr 18, "signal before archive" — pre-plan).

### E2 — Poller still writes raw stdout with no wrapping

`internal/csuite/persona/poller.go:240-248`:
```go
outName := fmt.Sprintf("%s-%s-reply-%s.md",
    now.UTC().Format("20060102T150405Z"),
    p.cfg.Persona,
    shortID,
)
outPath := filepath.Join(p.cfg.OutboxDir, outName)
if err := os.WriteFile(outPath, stdout, 0o644); err != nil {
    return fmt.Errorf("write outbox %q: %w", outPath, err)
}
```
Unchanged since the plan diagnosis.

### E3 — Fresh quarantine entries post-plan

Watcher logs show new quarantines AFTER the plan was filed at 2026-04-21
~03:20 UTC:
```
2026/04/22 02:12:08 deliver: quarantine: source=seth reason="no frontmatter delimiters" sha=75a0977a3a45...
2026/04/22 02:17:03 deliver: quarantine: source=seth reason="no frontmatter delimiters" sha=0cabb1e0bc07...
2026/04/22 03:16:38 deliver: quarantine: source=ross reason="no frontmatter delimiters" sha=8d16c271dfa4...
2026/04/22 06:49:27 deliver: quarantine: source=seth reason="no frontmatter delimiters" sha=613f5fb65b91...
```

### E4 — Seth inbound routing works

Seth container logs show successful message processing (inbound =
healthy, outbound stub = quarantined):
```
2026-04-22T06:49:27Z INFO processed message inbox_file=20260422-064500-kyle-bug-h-bug-i-plan-review.md outbox_file=20260422T064927Z-seth-reply-243b174302.md duration=274s
2026-04-22T06:49:27Z INFO signaled watcher delivery_id=613f5fb65b... status=202
```
Then watcher quarantines that delivery (see E3 last line).

### E5 — Dual-write proof

Seth's outbox for the same turn:
- `20260422T064730Z-seth-to-kyle-bug-h-bug-i-plan-review-response.md`
  (6634 bytes, opens with `---\nfrom: seth\nto: kyle\n…`, routes to
  Kyle's inbox per classifier).
- `20260422T064927Z-seth-reply-243b174302.md` (1701 bytes, opens
  with `Turn complete. Response summary: …`, quarantines).

Both files emitted by the same turn, two minutes apart. The `-to-` one
is Claude's Write-tool output; the `-reply-` one is stdout wrapped by
the poller.

### E6 — Kyle binary has no inbox reader

```
$ grep -rn 'drem-csuite\|csuite.*inbox' cmd/drem-kyle internal/kyle
(no matches)
```
Kyle's binary only has the `/tasks` HTTP poller and `:8090` HTTP
handler; inbox consumption does not exist in code.

### E7 — Orphan `.failures` sidecars

```
$ ls -la ~/.drem-csuite/seth/inbox/
-rw-r--r-- ...  2 Apr 21 17:00  20260421T235306Z-kyle-tui-retry-storm-design.md.failures
(no matching .md file in inbox OR archive)
```
Corresponding container log line:
```
2026-04-22T00:00:12Z WARN claude invocation failed persona=seth file=20260421T235306Z-kyle-tui-retry-storm-design.md exit_code=-1 failure_count=1 reason="non-zero exit" duration=300097380242
```
Same pattern at `~/.drem-csuite/alex/inbox/2026-04-21T17:43:26Z-alex-4c24a59c.md.failures`.

### E8 — Quarantine distribution

```
$ find ~/.drem-csuite/quarantine -type f | awk -F/ '{print $(NF-1)}' | sort | uniq -c
     35 alex
      2 mike
      3 ross
     63 seth
$ find ~/.drem-csuite/quarantine -type f | wc -l
103
```

### E9 — Kyle inbox pileup

```
$ ls ~/.drem-csuite/kyle/inbox/ | wc -l
361
$ ls -ltr ~/.drem-csuite/kyle/inbox/ | head -2
-rw-r--r-- ... Apr 10 17:47 20260411-004729-mike.md
```
Oldest unconsumed message is from 2026-04-10. Newest routed entry is
Apr 21 18:06 (from the watcher's initial rescan sweep).

### E10 — Code paths cited

- `internal/csuite/persona/poller.go:237-263` — stub writer (unchanged),
  `:462` — failure sidecar path.
- `internal/csuite/persona/subprocess.go:68-82` — exit=-1 path.
- `internal/deliver/classify.go:72-93` — frontmatter classifier.
- `internal/deliver/deliver.go:204-242` — quarantine + real-delivery.
- `cmd/drem-kyle/main.go` + `internal/kyle/*.go` — no inbox reader.
- `deploy/docker/csuite-base.Dockerfile:153-176` —
  `csuite-persona -persona "${CSUITE_AGENT}"` entrypoint (Wave 2).

### E11 — Containers healthy (do not restart)

All five csuite containers up 13h. Watcher routing healthy; watcher
image pre-Task-#11 so `drem csuite audit {list,queue}` won't work
against the running container until rebuild.
