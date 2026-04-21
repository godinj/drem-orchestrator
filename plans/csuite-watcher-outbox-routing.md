# Plan: csuite-watcher outbox routing

**Status:** accepted, not yet started (2026-04-21). Kicks off once a
subagent is dispatched against commits 1-4 (the MVP slice).

**Related:**

- `plans/csuite-persona-pivot.md` — the Wave-2 poller that produces the
  outbox files this plan consumes.
- `plans/csuite-watcher.md` — the pre-Wave-2 event-bus design for
  `csuite-watcher`. That plan described an orchestration-event bus
  (`task_status_changed`, turn metrics). This plan adds a second,
  complementary responsibility to the same service: outbox routing.
  The two responsibilities share the container and the `:8090` port
  but are independent code paths.
- `docs/prd-containerization.md` §C-Suite personas — architectural view.
- `~/.drem-csuite/seth/outbox/2026-04-21T153000Z-seth-reply-design-outbox-routing.md`
  — Seth's source design delivered via the csuite inbox/outbox pipeline.
  This plan is a distillation + the operator's answers to his seven
  open questions.

## Problem

After Wave 2 shipped, each csuite persona container writes its reply
to its own `outbox/` on successful message processing. There is no
consumer. Kyle (running as a host-side Go binary) reads outboxes
manually. The inter-persona message mesh is not closed.

## Design (decisions frozen)

### 1. Signal mechanism — HTTP POST on `:8090`

Persona `csuite-persona` binary `POST`s to
`http://csuite-watcher:8090/deliver` after a successful outbox write.
Watcher already listens on that port; cross-container on `drem-net`
is trivial.

Rejected alternatives: unix sockets (multiplies mount surface for no
benefit over HTTP on a local Docker network); filesystem sentinels
(reintroduces polling delay and cross-tenant write surface).
Filesystem rescan is kept as a backup path (§Failure modes).

### 2. Signal payload

Small, deterministic JSON. Watcher reads the file itself — no
duplicated body in the request.

```json
POST /deliver
Content-Type: application/json
X-Csuite-Token: <shared-secret from env>

{
  "source_persona": "alex",
  "outbox_path": "/csuite/alex/outbox/2026-04-21T153000Z-alex-reply-abc123.md",
  "sha256": "9f1c...",
  "emitted_at": "2026-04-21T15:30:00.123Z"
}
```

Responses:

- `202 Accepted` with `{"delivery_id": "..."}` on enqueue.
- `400 Bad Request` on schema failure or multi-recipient `to:` field.
- `401 Unauthorized` on missing/bad `X-Csuite-Token`.
- `404 Not Found` if `outbox_path` doesn't exist after retries.
- `409 Conflict` if the ledger already has this sha256 (duplicate).
- `507 Insufficient Storage` if destination writes fail on disk pressure.

### 3. Auth — shared secret from day one

Operator decision (Q1). `X-Csuite-Token` header required on every
`:8090` request. Secret comes from a single env var mounted into both
the watcher and each persona (`CSUITE_WATCHER_TOKEN` in compose, read
from a host file). Watcher rejects with `401` on mismatch.

### 4. Bind-mount topology change

| Container       | Mount                                                | Mode |
| --------------- | ---------------------------------------------------- | ---- |
| persona (each)  | `~/.drem-csuite/<persona>/` → `/csuite/<persona>/`   | rw   |
| persona (each)  | `~/.claude/.credentials.json`                        | ro   |
| csuite-watcher  | `~/.drem-csuite/` → `/csuite/`                       | rw   |
| csuite-watcher  | (named volume) `csuite-watcher-data` → `/var/lib/watcher/` | rw   |

Persona-side mounts are unchanged (still no cross-persona write
access). Watcher becomes the single trusted router — same pattern
Kyle represents on the host side.

The `csuite-watcher-data` named volume (Q6 decision) houses the
delivery ledger SQLite file and any future watcher-private state.
Kept out of the shared `/csuite/` tree so the blast radius on
corruption or migration is isolated.

### 5. Destination classes

Resolved from the parsed `to:` frontmatter field of the source file.

1. **csuite persona** — `to: {mike|alex|ross|seth}` → write to
   `/csuite/<to>/inbox/`.
2. **Kyle** — `to: kyle` → write to `/csuite/kyle/inbox/`. Host-side
   Kyle Go binary polls it.
3. **Unknown / malformed** — write to `/csuite/quarantine/<source>/<basename>`
   with a `quarantine` log line. Never drop silently.

**Multi-recipient rejected.** Operator decision (Q4). Any `to:`
value that parses as a list returns `400 Bad Request`. Personas must
emit one message per destination.

### 6. FIFO ordering

Operator decision (Q5). Watcher processes signals in arrival order;
deliveries within a single source-persona inbox preserve the source's
emission order. Cross-source FIFO is not guaranteed and no component
should rely on it.

Implementation: watcher serialises the write phase per-destination
with a small in-memory mutex keyed by destination persona. Since
files are named with RFC3339 timestamps + a hash, any consumer that
lists the inbox sees a deterministic sort.

### 7. Failure modes and recovery

| Mode                                    | Behavior |
| --------------------------------------- | -------- |
| Watcher down when persona signals       | Persona retries POST (3 attempts, ~1s/3s/9s backoff). On final failure, file stays in outbox; hourly FS-rescan recovers it. |
| Watcher crashes mid-delivery            | Atomic `rename` means dest inbox file is either fully there or absent. On restart, watcher replays from ledger and rescans outboxes for unrecorded files. |
| Signal lost                             | Backoff retry; else FS-rescan path. `POST /rescan` is operator-callable. |
| Duplicate signal                        | Ledger keyed by `sha256(file contents)` → `409 Conflict`, no second write. |
| Signal arrives before file flushed      | Persona `fsync`s before signaling. Watcher also retries open up to 5× over 500ms on hash mismatch. |
| Destination inbox missing               | Known persona: `mkdir -p`. Unknown: quarantine. |
| Destination full                        | `507`; persona leaves file in outbox; operator alerted via watcher logs. |
| Malformed frontmatter                   | Quarantine. |

**Delivery ledger.** SQLite at `/var/lib/watcher/deliveries.db`
(named volume, watcher-only). Schema:
`(sha256 PRIMARY KEY, source_persona, dest, source_path, dest_path, delivered_at)`.
Idempotency key + post-hoc audit.

**Retention of delivered outbox files.** Operator decision (Q2).
Move to `<source>/outbox/delivered/` on successful delivery, plus a
weekly GC pass (configurable retention window, default 30 days).
Retains forensic history without growing the live outbox.

**FS-rescan.** `POST /rescan` walks every `/csuite/*/outbox/`
(skipping `delivered/`) and runs the delivery path for any file not
in the ledger. Wired to watcher startup and to an hourly cron.

### 8. Watcher does NOT call Claude

Operator decision (Q7). The watcher never invokes the Claude API or
CLI. No smart routing. Malformed destinations go straight to
quarantine. This preserves the subscription-auth boundary —
credentials are only bind-mounted into personas, never into the
watcher.

## Deferred

### Kyle containerization

Operator decision (Q3). Kyle stays a host-side Go binary for now;
operator plans to eventually containerize it and rename the binary.
Path to revisit:

- Rename `cmd/drem-kyle` to something more durable (candidates:
  `cmd/drem-conductor`, `cmd/drem-ceo` — TBD).
- Wrap in a container on `drem-net` so Kyle reaches the watcher via
  the same `drem-net` DNS name as personas do.
- Collapse the two inbox-polling code paths (csuite-persona for
  personas, Kyle's Go polling for Kyle) if desired.

Out of scope for this plan. Tracked here so it doesn't get lost.

## Implementation plan — tracer bullets

Seven commits, each independently deployable. Commits 1-4 land in
the watcher Go service; commits 5+ touch the csuite-persona binary
and compose.

1. **`feat(csuite-watcher): add /healthz and /deliver skeleton`**
   Accept the documented payload shape, validate `X-Csuite-Token`,
   return `501 Not Implemented`. Unit tests for token check + schema
   validation. Proves wiring.

2. **`feat(csuite-watcher): add delivery ledger with idempotency check`**
   SQLite at `/var/lib/watcher/deliveries.db`. `/deliver` now looks
   up by sha256; returns `409` on duplicate; logs "would deliver"
   otherwise. Still no actual writes. Tests cover idempotency +
   WAL-mode concurrency.

3. **`feat(csuite-watcher): parse frontmatter, classify, quarantine`**
   `/deliver` now reads the source file, parses frontmatter,
   classifies the destination. Real destinations still log-only;
   quarantine destinations actually get written to
   `/csuite/quarantine/<source>/`. Tests cover all three classes
   including multi-recipient rejection.

4. **`feat(csuite-watcher): atomic write to destination inbox + ledger mark`**
   **MVP slice ends here.** `/deliver` now does the atomic
   `open(O_WRONLY|O_CREAT|O_EXCL)` + `fsync` + `rename` into the
   destination inbox and marks the ledger. Manual curl proves
   Alex → Kyle routing end-to-end:

   ```bash
   curl -X POST http://csuite-watcher:8090/deliver \
     -H 'X-Csuite-Token: <secret>' \
     -d '{"source_persona":"alex","outbox_path":"/csuite/alex/outbox/<file>","sha256":"...","emitted_at":"..."}'
   ```

   Kyle's host-side poller sees the new file in `~/.drem-csuite/kyle/inbox/`.
   Everything after commit 4 is retry hardening + automation.

5. **`feat(csuite-persona): post-write HTTP signal behind flag`**
   New env var `CSUITE_SIGNAL_ENDPOINT` on persona containers. Empty
   → old behaviour (no signal). Set → `fsync` outbox file, then POST
   to the endpoint with 3-attempt backoff. Tests cover retry
   behaviour + fsync ordering.

6. **`feat(csuite-watcher): rescan endpoint + startup rescan`**
   `POST /rescan` walks all `/csuite/*/outbox/` dirs (skipping
   `delivered/`) and re-runs delivery for any file not in the
   ledger. Wired to watcher startup. Closes the "signal lost" and
   "persona without signal code" gaps.

7. **`feat(compose): wire watcher mount + flip signal flag on for all personas`**
   Compose changes: add `/csuite/` rw mount to watcher, named volume
   for `watcher-data`, `CSUITE_WATCHER_TOKEN` env var on watcher,
   `CSUITE_SIGNAL_ENDPOINT` + token env on each persona. Flip flag
   on. Smoke test Alex → Kyle end-to-end via the real csuite-persona
   signal, not curl.

## Rollout order

1. Land commits 1-4 on master (watcher-only; no persona changes;
   safe to deploy anytime).
2. Rebuild + deploy csuite-watcher with new mount and token.
3. Validate via curl against `/deliver` (MVP slice).
4. Land commits 5-6 on master (persona signal + rescan).
5. Land commit 7 (compose flip).
6. Rebuild + redeploy personas.
7. Smoke test: Alex replies to a Kyle-addressed message; Kyle's
   host-side poller picks it up from `~/.drem-csuite/kyle/inbox/`
   within seconds.
8. Retire any manual `cat` workflows once routing is green.

## Rollback

Any of: unset `CSUITE_SIGNAL_ENDPOINT` on personas (they stop
signaling; outboxes pile up harmlessly, Kyle reads them manually
again); stop `csuite-watcher` (personas log retry failures but
continue processing inboxes; no regression on persona-side
work).

## Open questions (none blocking)

All seven of Seth's open questions answered and baked in above. Net
new open questions may surface during implementation; update this
plan if any decision changes.
