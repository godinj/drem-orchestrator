# csuite-watcher audit-token mount gap (199efe2 follow-up)

**Status:** filed 2026-04-22 post-Session-N-restart Option-A rebuild.
Awaiting operator greenlight. Blocks csuite routing + F28
re-measurement.

## Symptom

After `deploy/docker/build-csuite.sh` + `docker compose up -d --no-deps
csuite-watcher` using the post-199efe2 image, the watcher restart-loops
(exit 1) with:

```
error: load audit token: stat /home/drem/.drem/csuite-watcher.token:
       stat /home/drem/.drem/csuite-watcher.token: no such file or
       directory
```

Container status: `Restarting (1) N seconds ago` forever.

## Root cause

Commit `199efe2` ("feat(csuite-watcher): wire audit token + mount
/v1/* on bridge", 2026-04-21 20:41 PDT) added a required audit-token
file to the watcher binary's startup:

```go
// cmd/csuite-watcher/serve.go:57
const DefaultAuditTokenPath = "~/.drem/csuite-watcher.token"

// serve.go:219-227
auditToken, err := loadAuditToken(auditTokenPath())
if err != nil {
    fmt.Fprintf(stderr, "error: load audit token: %v\n", err)
    return 1
}
```

Env override: `DREM_AUDIT_TOKEN_PATH`.

The compose template at
`internal/projects/templates/project-compose.yml.tmpl:141-180` was
**not** updated to bind-mount the host token file or set
`DREM_AUDIT_TOKEN_PATH`. Result: the watcher container's
`/home/drem/.drem/csuite-watcher.token` does not exist, loadAuditToken
returns ENOENT, the process exits 1, compose restarts it, it exits
1, loop.

### Why it didn't surface until now

The watcher container that was running pre-rebuild was built before
`199efe2` shipped. `docker compose up` without rebuild reused the
old image and old binary, which didn't require the audit token.
Session N's rebuild step (`build-csuite.sh`) finally produced a
watcher image containing `199efe2`'s binary — at which point the
compose template gap becomes fatal.

This is the **half-shipped feature pattern** the docs-as-acceptance-
criteria rule is designed to catch. 199efe2 landed binary-side
without its compose-template counterpart.

## Session N connection (and why this is not itself a Session N bug)

Session N item 33 addressed the `CSUITE_WATCHER_TOKEN` environment
variable (persona→watcher /deliver shared secret). That fix shipped
the persona-side bind-mount at `/run/secrets/csuite-watcher-token`
and the fallback env var `CSUITE_WATCHER_TOKEN_FILE`. Those are
correct.

The audit token is a **second, orthogonal token**, only read by the
watcher for `/v1/audit/*` endpoints (consumed by `drem csuite audit`
CLI). Same host file (`~/.drem/csuite-watcher.token`) coincidentally
— but a distinct in-container path and code path. 199efe2 never
touched the compose template; Session N focused on item 33 on the
persona side; the watcher-side mount fell between them.

## Fix

Add two lines to the watcher service in the compose template:

```yaml
# internal/projects/templates/project-compose.yml.tmpl
csuite-watcher:
  image: localhost:5000/drem-csuite-watcher:latest
  environment:
    ...
    CSUITE_WATCHER_TOKEN:
    # NEW — mirror the persona-side convention
    DREM_AUDIT_TOKEN_PATH: "/run/secrets/csuite-watcher-token"
  volumes:
    - {{.CsuiteHomeRoot}}:/csuite:rw
    - drem-{{.ProjectName}}-csuite-watcher-data:/var/lib/watcher
    # NEW — host token, read-only, same bind as personas
    - {{.WatcherTokenFile}}:/run/secrets/csuite-watcher-token:ro
```

Where `{{.WatcherTokenFile}}` resolves to the host-side
`~/.drem/csuite-watcher.token` path (the same value the persona
services already use — template already has a matching field for
`WorkerCredsPath`; this follows the same pattern).

Then: `drem project register --update --force` + `docker compose up
-d --no-deps csuite-watcher`.

## Required unit test

Extend `internal/projects/template_test.go` with an assertion that
the rendered compose's `csuite-watcher.volumes` includes the
watcher-token bind-mount AND that `DREM_AUDIT_TOKEN_PATH` env is
set. That test would have caught 199efe2's half-ship.

## Failure shape when not fixed

- Watcher in endless restart loop.
- No csuite-watcher → no /deliver endpoint → personas can POST
  signals but they 5xx at the network.
- No csuite-watcher → no /v1/audit/* endpoint → `drem csuite audit`
  CLI returns connection refused.
- F28 quarantine metric unreachable (deliveries.db is inside the
  watcher volume but can only be introspected via the binary's
  SQLite inside its container).
- Persona-to-persona routing stops (watcher is the single router).

## Recommendation

**Tiny inline fix**, two lines of template + one unit test. Per
CLAUDE.md "one-line or single-function fixes can be inline", this
qualifies. Operator greenlight gate because it's a live-service fix
during dogfood.

If greenlit: fix + regen + `compose up --no-deps csuite-watcher`
completes in under 60s, then F28 measurement unblocks.

If NOT greenlit tonight: watcher stays down, Session N+1 picks it
up. No data corruption risk — deliveries DB is unchanged, it just
can't be reached.
