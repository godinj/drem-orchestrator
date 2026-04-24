# csuite-persona poller: fsync Claude-written outbox files before archive rename

**Status:** open, low priority
**Owner:** Mike (COO) when queue clears
**Surfaced by:** local Claude, 2026-04-23, during review of the multi-file outbox relaxation
**Related:** `docs/csuite-agents/prompts/kyle-container.md` (Output Contract now allows multiple files per turn)

## Problem

`internal/csuite/persona/poller.go` archives the inbound message synchronously at line ~441. Outbox files are written by the `claude -p` subprocess before the post-turn diff at line ~287, so they are on disk before archive — no replay risk from the multi-file relaxation itself.

However, the poller fsyncs only its own stub path (line ~379). It does NOT fsync Claude-written outbox files before renaming the inbound into the archive. A power-loss between archive-rename and kernel writeback could:

- lose a Claude-written outbox file, while
- the inbound is already archived, so
- no re-trigger fires to recover.

The watcher's sha256 ledger dedupes on replay, but a file that never made it to disk has nothing to replay.

## Why the relaxation makes it worse (marginally)

Pre-change: one outbox file per turn → one file to durably land. Post-change: N files → all N must durably land for full correctness. The probability of *any* file being lost scales with N, though realistic N is 1–3 and the window is still narrow (single kernel writeback delay).

## Fix sketch

In `recordSuccess` (or wherever `claudeWritten` is materialized), iterate and `fsync(2)` each file before the archive rename. Also fsync the outbox directory to durably commit the dirent. Then rename the inbound.

```go
for _, p := range claudeWritten {
    f, err := os.Open(p)
    if err != nil { continue } // already gone, best-effort
    _ = f.Sync()
    _ = f.Close()
}
// fsync the outbox dir so the dirent is durable
if d, err := os.Open(outboxDir); err == nil {
    _ = d.Sync()
    _ = d.Close()
}
// now archive the inbound
```

Cost is a handful of syscalls per turn. Negligible compared to `claude -p` wall time.

## Do not fix yet

Operator has not asked for this. Flagging here so it doesn't get lost. Bundle with the next poller hardening pass.
