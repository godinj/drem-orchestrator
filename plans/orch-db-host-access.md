# Orch SQLite DB — host-side access

**Status:** open, deferred until v13/v14 canary closes.
**Raised:** 2026-04-21, Kyle session (post-orch-rebuild), prompted by
operator: "is the SQLite DB in a container? if not, could you make
a note to address that after the canary passes successfully?"

## Current state

The orchestrator's primary SQLite store sits at
`/var/lib/drem/drem.db` inside the orch container. That path is
backed by Docker named volume
`drem-orchestrator_drem-drem-orchestrator-db` (see
`internal/projects/templates/project-compose.yml.tmpl`). The volume
survives `docker compose up --force-recreate`, which is why
v13-canary history and all prior task state persisted through the
orch rebuild at 2026-04-21T20:27Z.

The host filesystem does **not** have a direct mount of this DB.
`/home/godinj/.drem/projects/drem-orchestrator/drem.db` exists as a
zero-byte stub (created on compose bind resolution) and carries no
data. The actual 4.3 GiB DB file lives inside the Docker volume
store at `/var/lib/docker/volumes/.../\_data/drem.db`, reachable only
by Docker-issued processes.

## Why this matters

1. **Observability gap.** Any diagnostic that wants to query rows
   directly — e.g. "which tasks have non-null `worktree_branch`
   when their agent rows don't?" — has to either `docker exec` into
   orch (container lacks `sqlite3`) or spin up a sidecar container
   with `--volumes-from drem-orchestrator-orch-1`. This turned a
   routine question during v13 triage into a three-step workaround
   and we fell back to the HTTP API, which doesn't expose the fields
   we needed.

2. **Backup / snapshot friction.** No `cp /home/godinj/.drem/.../drem.db
   <snapshot>/`. Taking a consistent backup requires pausing WAL and
   using `docker exec ... sqlite3 .backup` or copying the volume
   contents while the orch is quiesced.

3. **Rescue during orch outages.** If orch won't start (schema
   migration bug, corrupt WAL, etc.) today's only triage path is
   another container. Host-side access would let us inspect or
   repair without standing up a companion image.

4. **CLI ergonomics.** `drem cli approve <id>` and similar commands
   already run inside the container because they open the DB by
   path. A host-side bind-mount would let a host-built `drem` binary
   talk to the same DB without docker hops — useful during
   development.

## Options (pick one before implementing)

**A. Bind-mount the DB directory to host.** Change
`project-compose.yml.tmpl` to mount
`/home/godinj/.drem/projects/<project>/data/` at `/var/lib/drem` (or
at least `/var/lib/drem/drem.db`). Named volume is dropped.

- Pros: zero-tooling host access (`sqlite3`, `litecli`, any GUI);
  backups become `cp`; WAL + shm alongside.
- Cons: must pre-create the host dir with correct uid (orch runs as
  root inside, DB files owned 1000:1000 per the inspect). File
  permissions become a cross-platform gotcha if we ever ship this to
  a non-Linux operator. Named-volume migration path required for
  existing projects.

**B. Add a read-only sidecar.** A tiny alpine+sqlite container with
`--volumes-from drem-orchestrator-orch-1`, started on demand. Keeps
the current volume model, adds a diagnostic surface.

- Pros: no migration, no permission gotchas, opt-in.
- Cons: still Docker-scoped; doesn't help host-built `drem`; another
  moving part.

**C. Expose richer orch HTTP endpoints.** Add `/internal/tasks/{id}`,
`/internal/agents/{id}`, `/internal/events?task_id=...` that return
full row contents with failure reasons, ordered newest-first. Keeps
the DB opaque but closes the observability gap that actually bit us
during v13 triage (Mike couldn't pull failure payloads; the API
returned only oldest-500 events with no per-task filter).

- Pros: keeps container boundary clean, same path benefits Mike and
  any future persona observer, doesn't require compose changes.
- Cons: more Go code, doesn't help backup/rescue scenarios.

**D. All of A + C.** Bind-mount for operator tooling AND expose
fuller HTTP surface for in-network personas. Most thorough.

## Recommendation (to revisit after canary)

Default toward **A** — bind-mount is cheap and closes the single
largest ergonomic gap. **C** is worth doing regardless because the
v13 triage proved the HTTP API's task/event surface is too thin for
autonomous observers. **B** is only worth it if **A** runs into
permission headaches we can't solve cleanly.

## Don't forget

- Update `internal/projects/templates/project-compose.yml.tmpl` and
  run `drem project register --update --force drem-orchestrator`
  to regenerate compose.yml.
- Migrate existing DB out of the named volume (`docker run --rm
  --volumes-from orch -v <host-path>:/dest alpine cp /var/lib/drem/
  /dest/`) before switching the mount, or accept an empty DB on
  first restart after the change.
- csuite-watcher has its own DB at
  `/var/lib/watcher/deliveries.db` on named volume
  `drem-orchestrator_drem-drem-orchestrator-csuite-watcher-data`.
  Same question applies; treat symmetrically if we take option A.
- The orch container ALSO writes a `csuite.db` at
  `/root/.drem-csuite/csuite.db` (see orch startup warning
  "event bus unavailable: open database ... no such file or
  directory") — that's a separate event-bus path issue, flagged
  here only so we don't re-mount on top of it.
