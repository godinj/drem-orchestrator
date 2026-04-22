# csuite audit CLI — plan

**Status**: v1 merged. Two watcher endpoints (GET /v1/deliveries,
GET /v1/queue) and two CLI subcommands (`drem csuite audit list`,
`drem csuite audit queue`) shipped in April 2026 alongside the
existing orch-API gate-mutation pivot. v2 backlog (tail, show, SIGHUP
reload) is still open; see §V2 backlog.

**Origin**: operator asked Kyle for a way to audit csuite agent-to-agent
messages without hand-grepping filesystem inboxes/outboxes/quarantine.
Three options were surfaced (A: watcher endpoints, B: CLI wrapper, C:
drem-kyle rollup). Seth's reply selected **hybrid A+B phased**, with C
deferred to Phase 3 after drem-kyle sheds its grandfathered line-count
cap. Spec: `~/.drem-csuite/seth/outbox/20260422T021011Z-seth-to-kyle-audit-cli-spec.md`.

## Problem statement

Today there is no durable audit surface for csuite deliveries. The
watcher writes a ledger to `/var/lib/watcher/deliveries.db` and moves
files between `inbox/` `outbox/` `quarantine/` dirs, but inspecting
that state means:

- `docker exec` into the watcher container and `sqlite3` the ledger
  (the container is uid-1000 gosu; shell commands work but are manual),
- or `ls` host-bind-mount dirs, which surfaces raw filenames with no
  structure about delivery status, recipients, quarantine reasons, or
  failure causes,
- or grep the watcher's stdout log, which is unbounded and noisy.

The operator needs a first-class CLI: `drem csuite audit list` and
`drem csuite audit queue`. Read-only, filterable, JSON-or-table output,
bearer-auth against a token file the watcher controls.

## Goals

1. **Single source of truth**: the watcher's ledger is the only
   storage; the CLI is a stateless thin HTTP client. No CLI-side state,
   no CLI-side filesystem reads of inbox/outbox dirs.
2. **Fits the existing csuite security model**: same gosu-1000 process,
   same uid, same host token file that kyle and operator already use
   for subscription auth elsewhere.
3. **Namespace correctly**: `drem csuite` becomes the parent for
   csuite-scoped commands — `audit` is one subgroup; `approve`,
   `reject`, `status` will land under the same parent as Phase 1's
   orch-gate commands lift into the csuite-owned surface.
4. **V1 ships read-only**. Writes (retry, release-from-quarantine) are
   explicitly v3 and need their own design.

## Non-goals

- Token rotation (v2 — SIGHUP reload).
- SSE / streaming tail (v2 — `audit tail`).
- Full message-body view (v2 — `audit show <id>`).
- Write operations (v3).
- Rolling the audit data into drem-kyle's `/world` response (Phase 3,
  after drem-kyle sheds its grandfathered cap).
- Unifying with `docker logs` / `journalctl` output — explicit non-goal
  per Seth's v1 scope.

## V1 decision summary

Per Seth 2026-04-22T02:10Z:

| Aspect | Decision |
|---|---|
| **A (endpoints)** | csuite-watcher gets `GET /v1/deliveries` + `GET /v1/queue` |
| **B (CLI wrapper)** | `drem csuite audit list` + `drem csuite audit queue`, thin HTTP client |
| **C (drem-kyle rollup)** | deferred to Phase 3 |
| **Auth** | bearer token, file at `~/.drem/csuite-watcher.token`, `0600` perms enforced by watcher |
| **Output** | `--format table\|json`, table default on TTY, JSON otherwise |

## Namespace placement

```
drem csuite
├── audit
│   ├── list        (v1)
│   └── queue       (v1)
│   └── tail        (v2)
│   └── show <id>   (v2)
├── approve         (Phase 1 orch-gate — lifts from `drem cli approve`)
├── reject          (Phase 1)
└── status          (Phase 1)
```

The `drem cli approve|reject|pass|fail|answer` subcommands landing in
Phase 2 of the orch-API pivot (`plans/orch-api-gate-mutations.md`) are
the natural siblings of `audit`. Whether they relocate under
`drem csuite approve|reject|...` in Phase 3 or stay under `drem cli`
is out of scope for the audit CLI itself; Seth flagged the sibling
slot exists so we don't re-namespace later.

## V1 subcommand surface

### `drem csuite audit list`

Lists delivery ledger entries from the watcher.

Flags:
- `--from <agent>` — filter sender
- `--to <agent>` — filter recipient
- `--status <s>` — `delivered | quarantined | failed | all` (default `all`)
- `--type <t>` — `observation | request | report | decision`
- `--since <duration|date>` — e.g. `1h`, `24h`, `2026-04-21`
- `--limit <n>` — default `50`, max `500`
- `--offset <n>` — pagination; default `0`
- `--format <fmt>` — `table | json`

Common flags on every `drem csuite audit` subcommand:
- `--watcher-url <url>` — override; default from `drem.toml`
- `--token <path>` — override; default `~/.drem/csuite-watcher.token`

### `drem csuite audit queue`

Shows current queue state (inbox / outbox / quarantine) aggregated per
agent.

Flags:
- `--agent <name>` — filter; default all agents
- `--scope <s>` — `inbox | outbox | quarantine | all` (default `all`)
- `--stale <duration>` — only entries older than N (e.g. `--stale 30m`)
- `--format <fmt>` — `table | json`

### Sample output

`drem csuite audit list` (table, default on TTY):

```
TIME                 FROM   TO     TYPE       PRIO  SUBJECT                              STATUS      ID
2026-04-22T00:05:00Z kyle   seth   request    med   Nudge: full spec for csuite audit    delivered   d47a2f
2026-04-21T23:57:00Z seth   kyle   decision   med   TUI retry-storm design               delivered   3b11c9
2026-04-21T22:45:00Z seth   kyle   report     high  orch-wedge RCA                       delivered   9ff0d1
```

No message body in list output — that's `audit show <id>` (v2).

`drem csuite audit queue` (table):

```
AGENT   SCOPE        COUNT  OLDEST                 NEWEST
seth    inbox        1      2026-04-22T00:05:00Z   2026-04-22T00:05:00Z
seth    outbox       42     2026-03-24T00:27:48Z   2026-04-22T02:10:11Z
kyle    inbox        0      -                      -
alex    inbox        2      2026-04-19T14:00:00Z   2026-04-21T09:10:00Z
alex    quarantine   1      2026-04-20T19:00:00Z   2026-04-20T19:00:00Z
```

## V1 endpoint surface

### `GET /v1/deliveries`

Query params map 1:1 to `audit list` flags (`from`, `to`, `status`,
`type`, `since`, `limit`, `offset`). Returns a JSON array of delivery
objects. Shape:

```json
[
  {
    "id": "d47a2f...",
    "from": "kyle",
    "to": "seth",
    "type": "request",
    "priority": "medium",
    "subject": "Nudge: full spec for csuite audit CLI",
    "tldr": "...",
    "delivered_at": "2026-04-22T00:05:00Z",
    "status": "delivered",
    "filename": "20260422T000500Z-kyle-audit-cli-spec-followup.md"
  }
]
```

### `GET /v1/queue`

Query params map 1:1 to `audit queue` flags (`agent`, `scope`, `stale`).
Returns a JSON array of `{agent, scope, count, oldest, newest}` rows.

Watcher reads the dirs it already owns. No filesystem paths leak to
clients — CLI sees agent/scope tuples only.

Both endpoints read-only. **No POST/PUT/PATCH/DELETE in v1.**

## Auth flow

1. csuite-watcher reads `~/.drem/csuite-watcher.token` at startup.
   - File mode must be `0600`; watcher refuses to start if
     world-readable.
   - Token held in memory only; never logged.
2. Every `/v1/*` endpoint requires `Authorization: Bearer <token>`
   header. `401 Unauthorized` on mismatch or missing header. No
   unauthenticated surface on `/v1/*`.
3. CLI reads the same token file, sends the bearer header.
4. If token file is missing or unreadable: CLI prints the expected
   path and exits `2`. Do not prompt, do not auto-generate — that is a
   watcher-init concern, not the CLI's.

Token rotation out of scope for v1. v2 adds SIGHUP reload in the
watcher.

## Constitution fit

- **Watcher new code**: two handlers + query-param parsing + ledger
  query. One of Seth's recon items is whether the existing watcher
  HTTP-surface file is near the 800-line ceiling; if within 100 lines,
  split `handlers_audit.go` off before adding. Otherwise inline.
- **drem CLI new code**: two new files — `csuite.go` (parent) and
  `csuite_audit.go` (child group). Both well under 800 lines at v1.
  Zero new internal-package imports beyond what the CLI already pulls
  for HTTP + JSON.
- **testutil**: new CLI subcommand tests use `internal/testutil`
  httptest helpers. No local mock setup.
- **No duplication risk**: single source of truth for delivery data is
  the watcher ledger; CLI is stateless.

No grandfathered files affected.

## Open items (recon before implementation) — resolved at v1 merge

1. **Watcher HTTP-surface file headroom** — resolved. `internal/
   deliver/deliver.go` was 368 lines at dispatch, well under the
   800-line cap. We still split the audit handlers into
   `internal/deliver/handlers_audit.go` and
   `internal/deliver/handlers_audit_queue.go` for separation of
   concerns (the audit surface is read-only and has a different auth
   audience from the /deliver pipeline). No grandfathered files
   were affected.
2. **drem-kyle line-count headroom** — out of scope for v1, not
   touched. Phase 3 C (rollup) remains blocked on the line-count
   cap shed.
3. **Token file creation contract** — resolved as "operator-init,
   pending v2 automation". The watcher reads `~/.drem/csuite-watcher.
   token` on serve startup, enforces 0600, and fails-closed on a
   missing or world-readable file. The CLI reads the same file and
   exits 2 with a diagnostic if it is absent. Neither the watcher
   nor the CLI generates the token — that step is operator-manual
   (e.g. `openssl rand -hex 32 | install -m 0600 /dev/stdin
   ~/.drem/csuite-watcher.token`). A v2 follow-up will add either
   an `operator-init` subcommand or a Makefile target so the
   bootstrap is one command instead of an operator recipe.

## V2 backlog (after v1 ships)

1. **`drem csuite audit tail`** — SSE stream of new deliveries.
   Watcher adds `GET /v1/deliveries/stream`. Not WebSocket.
2. **`drem csuite audit show <id>`** — single delivery with full
   message body. Watcher adds `GET /v1/deliveries/<id>`.
3. **Token rotation via SIGHUP** in the watcher.

## V3 backlog

Write operations — retry failed delivery, release from quarantine.
Explicit v3-earliest; writes need their own design.

## Sequencing — merged

1. Orch-API Phase 2 (CLI gate commands via orchclient) — merged.
2. Orch-API Phase 3 (TUI gate commands via orchclient) — merged.
3. Recon pass for the three open items — folded into the v1 dispatch
   rather than run as a separate temp-worker pass.
4. csuite audit CLI v1 — merged (this doc). Commit train:
   - `test(watcher): failing regressions for GET /v1/deliveries`
   - `feat(watcher): /v1/deliveries handler + bearer auth`
   - `test(watcher): failing regressions for GET /v1/queue`
   - `feat(watcher): /v1/queue handler`
   - `feat(csuite-watcher): wire audit token + mount /v1/* on bridge`
   - `test(cmd/drem): failing regressions for csuite audit list`
   - `feat(cmd/drem): csuite audit list subcommand`
   - `test(cmd/drem): failing regressions for csuite audit queue`
   - `feat(cmd/drem): csuite audit queue subcommand`
   - `docs(plans): mark csuite-audit-cli.md v1 as merged`

## References

- `~/.drem-csuite/seth/outbox/20260422T021011Z-seth-to-kyle-audit-cli-spec.md` (9 KB, Seth's v1 + v2 spec)
- `~/.drem-csuite/seth/outbox/20260421T221019Z-seth-to-kyle-audit-cli-ack.md` (2.2 KB, Seth's initial ACK picking option A+B)
- `plans/orch-api-gate-mutations.md` — sibling feature in the `drem csuite` namespace
- `plans/csuite-watcher-outbox-routing.md` — background on the watcher's delivery model

— kyle, 2026-04-22
