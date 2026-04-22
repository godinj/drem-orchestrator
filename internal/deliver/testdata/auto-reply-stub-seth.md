Turn complete. Summary of work:

**Processed:** Kyle's design request (`kyle-design-request-1776784900.md`) asking for a routing design for persona outbox → csuite-watcher → destination inbox.

**Reply written to outbox:** `2026-04-21T153000Z-seth-reply-design-outbox-routing.md` (~1,400 words, all 9 requested sections).

**Key design choices:**
- **Signal:** HTTP `POST /deliver` on `:8090` (watcher already listens; clean cross-container on `drem-net`). Unix socket and FS sentinels rejected as primary, FS rescan kept as fallback.
- **Payload:** small JSON with `source_persona`, `outbox_path`, `sha256`, `emitted_at`. Watcher reads the file itself — no dual source of truth.
- **Mount topology:** watcher gets RW on the full `~/.drem-csuite/` tree; personas unchanged (still no cross-tenant write access).
- **Three dest classes:** persona / Kyle (host-runtime) / quarantine.
- **Failure modes:** at-least-once with sha256-keyed delivery ledger (SQLite), atomic rename writes, retry-on-partial-flush, `/rescan` endpoint + hourly cron for belt-and-braces.
- **Backcompat:** `CSUITE_SIGNAL_ENDPOINT` env var flag on personas; watcher deployed first, flag flipped last. Zero regression if either side is missing.
- **Plan:** 7 tracer commits; MVP end of commit 4 (manual curl proves Alex→Kyle routing end-to-end).
- **7 open questions** surfaced for operator (auth, retention, ledger location, multi-recipient, ordering, Kyle-in-container future, no-Claude-in-watcher confirmation).

**Housekeeping:** inbox message archived; state.md updated with finding and pending-report entry.
