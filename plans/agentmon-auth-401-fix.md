# Plan: agentmon → orch ingest 401 fix

## Failure mode

`drem-orchestrator-agentmon-1` logged
`agentmon ingest: status 401: unauthorized` against orch's
`POST /internal/logs` endpoint continuously for 41+ hours (April 2026).
Because heartbeats never reached the orch SQLite database, every
agent row stayed at `HeartbeatAt == StartedAt`. The stuck-agent
reconciler interpreted that as a dead worker and failed tasks with
`agent session died without producing commits` — even when the
containerized worker had already committed and pushed code via its
watchdog. Observed end-to-end during task
`a23ebaa2-157b-492b-83c1-2a199490268c`, subtask
`777f6507-5a8c-42d8-9d86-0e5dd8566573` (the v13 canary).

Superficial inspection ruled out the obvious causes:

- Both `orch` and `agentmon` containers carried the same
  `DREM_AGENTMON_TOKEN=5945505c-1391-4a23-940e-f3781c257789` in
  their compose env block (verified with `docker inspect`).
- The header constant on the server side
  (`internal/orchhttp/server.go:95` —
  `const agentmonTokenHeader = "X-Drem-Agentmon-Token"`) matched the
  client side verbatim
  (`internal/agentmon/client.go:25` —
  `const agentmonTokenHeader = "X-Drem-Agentmon-Token"`, with a comment
  noting the duplication is intentional to avoid an internal/orchhttp
  dep on agentmon).
- The URL path (`POST /internal/logs`) matched on both sides.
- The mux wiring
  (`mux.Handle("POST /internal/logs", s.requireAgentmonToken(...))`)
  and the middleware's equality check
  (`if s.SharedToken == "" || got != s.SharedToken { ... 401 }`) were
  both correct.

Yet orch kept returning 401.

## Root cause

`cmd/drem/orchhttp_server.go:startOrchHTTP` constructed the
`orchhttp.Server` with `cfg.AgentmonToken` as the `SharedToken`. The
`Config.AgentmonToken` field is populated by TOML unmarshal only
(`cmd/drem/config.go:147`, `agentmon_token = ...`). But the
per-project compose template
(`internal/projects/templates/project-compose.yml.tmpl`) writes the
token to the orch container **env block** (`DREM_AGENTMON_TOKEN: ...`)
and the generated `drem.toml` mounted into the container carries no
`agentmon_token` key — consistent with the documented design intent
that the TOML does not embed secrets
(`plans/drem-project-register-update.md` §"`doesn't touch
DREM_AGENTMON_TOKEN / DREM_BEARER_TOKEN`"`).

Result: `cfg.AgentmonToken == ""` on startup inside the orch
container, `Server.SharedToken == ""`, and the middleware's
`s.SharedToken == ""` short-circuit rejected every incoming request
with 401. The agentmon side was sending the correct token — there
was simply no token on the server side to match against.

This pattern already existed elsewhere in `cmd/drem/main.go`:
`classifierURL`, `plannerURL`, and the merge-dispatch path all read
`os.Getenv("DREM_AGENTMON_TOKEN")` directly, because the container's
env is the authoritative source for the shared secret. Only the
orch HTTP listener had been overlooked.

## Fix

Add an `effectiveAgentmonToken(cfg Config) string` helper in
`cmd/drem/orchhttp_server.go` that resolves the token with explicit
precedence:

1. `cfg.AgentmonToken` (drem.toml) — honored when non-empty so
   file-driven dev setups remain stable.
2. `os.Getenv("DREM_AGENTMON_TOKEN")` — falls through when the TOML
   key is absent, matching the per-project compose convention.
3. Empty — fail closed. The middleware still rejects every request
   with 401; a new `slog.Warn` logs at startup so an operator who
   forgets to configure the token gets a visible hint in the journal
   instead of silent failure.

`startOrchHTTP` now passes the resolved token into `orchhttp.New`.
No changes required on the `orchhttp.Server` side — the middleware
contract is unchanged, we just actually feed it the secret the
compose already supplies.

## Files changed

| Path | Lines | Purpose |
|---|---|---|
| `cmd/drem/orchhttp_server.go` | +35/-4 | Add `effectiveAgentmonToken`; wire env-var fallback into `startOrchHTTP`; warn-log when both sources are empty. |
| `cmd/drem/orchhttp_server_test.go` | +195 | New regression tests: TOML-beats-env precedence, env-var fallback, fail-closed-when-both-empty, full round-trip via real `agentmon.HTTPIngestor`, wrong-token rejection. |
| `internal/orchhttp/server_test.go` | +50 | Cross-package contract test: drive `agentmon.HTTPIngestor` against `orchhttp.Server.requireAgentmonToken` to catch future header-name or URL-path drift on either side. |
| `plans/agentmon-auth-401-fix.md` | +this | Failure mode, root cause, fix, verification. |

## Verification

### Red-green proof

Temporarily stubbed `effectiveAgentmonToken` to return
`cfg.AgentmonToken` (the pre-fix behaviour). The new tests failed
with the **exact** production error:

    --- FAIL: TestStartOrchHTTPAuthenticatesAgentmonClient
        Error: Received unexpected error:
               agentmon ingest: status 401: unauthorized

That is the same byte-for-byte error string emitted by
`internal/agentmon/client.go:134` in the 41-hour production log,
confirming the test covers the actual production path and not just
an adjacent one.

After restoring the fallback, all new tests pass and
`go test ./... ` is green end-to-end.

### Full suite

    $ go test ./... 2>&1 | tail -20
    ok  	github.com/godinj/drem-orchestrator/cmd/drem	0.675s
    ok  	github.com/godinj/drem-orchestrator/internal/agentmon	1.910s
    ok  	github.com/godinj/drem-orchestrator/internal/orchhttp	0.047s
    ok  	github.com/godinj/drem-orchestrator/internal/orchestrator	43.800s
    ... (all packages PASS, 0 FAIL)

## Regression coverage

The cross-package contract test in
`internal/orchhttp/server_test.go` is the important one for future
maintainers. Before this fix, the 401 outage slipped through because
two separately-passing test suites tested each side in isolation:

- `internal/orchhttp/server_test.go` hand-rolled the
  `X-Drem-Agentmon-Token` header with the name it expected the
  server to accept.
- `internal/agentmon/client_test.go` stood up an `httptest.Server`
  and read whatever header name the client happened to send.

Neither caught a server-config gap or a cross-side rename. The new
`TestIngestAcceptsAgentmonHTTPIngestorRoundTrip` exercises the real
production client against the real production middleware — any future
header-name, URL-path, or config-loading drift on either side fails
this test immediately.

## What Kyle should know before merging

- **No compose.yml regeneration required.** The existing compose
  template already sets `DREM_AGENTMON_TOKEN` on orch; the fix
  teaches the orch binary to read it. Operators on existing
  projects do not need to run `drem project register --update`.
- **Rebuild scope: drem-orch image only.** The agentmon binary is
  unchanged; do not rebuild `drem-agentmon`. The fix lives in
  `cmd/drem` which is packaged into the `drem-orch` image.
- **No SGLang restart.** The fix does not touch any service with a
  `depends_on: service_healthy` edge into sglang.
- **No token rotation.** The existing shared token
  (`5945505c-...`) stays valid; we are only teaching orch to read
  what is already in its env.
- **Fail-closed preserved.** If an operator deploys without
  `DREM_AGENTMON_TOKEN` set anywhere, ingestion still returns 401
  as before, now with a startup warning in the orch journal to help
  diagnose.
- **Subscription-only auth preserved.** This fix reads
  `DREM_AGENTMON_TOKEN` (the agentmon ingest token), not
  `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` /
  `ANTHROPIC_AUTH_TOKEN`. The repo-root `CLAUDE.md` constraint is
  unchanged.
