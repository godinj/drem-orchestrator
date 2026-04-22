# Orch DB host-side access — implementation (Option A only)

Links back to the design doc at `plans/orch-db-host-access.md` (the
four-option survey Kyle wrote during the v13-v16 canary round). This
doc records what actually landed for the bind-mount path; Option C
(richer HTTP endpoints) is deferred until Bug D's fix lands.

## Scope

- **In scope (this commit sequence).** Option A only: replace the
  per-project `drem-<project>-db` named docker volume with a host
  bind-mount of `~/.drem/projects/<project>/data/` onto
  `/var/lib/drem` inside the orch container. Operator can now
  `sqlite3 ~/.drem/projects/<project>/data/drem.db`, `du -sh` the
  dir, `cp` snapshots, etc. without `docker run --rm -v <vol>:/src
  alpine ...` gymnastics.
- **Out of scope (deferred).** Option C — an orch HTTP endpoint that
  exposes structured DB inspection (table sizes, recent rows,
  stats). Waiting on Bug D (`plans/orch-container-constraint-gate-fix.md`
  and neighbours) so we don't ship a read path that races a broken
  write path. The design doc stays the source of truth for that
  follow-on.

## Why bind-mount the directory, not just `drem.db`

SQLite in WAL mode (which GORM defaults to on `mattn/go-sqlite3`)
writes three files side by side:

- `drem.db`           — the main database.
- `drem.db-wal`       — the write-ahead log.
- `drem.db-shm`       — the shared-memory index for WAL readers.

A single-file bind-mount of `drem.db` alone would leave the WAL and
SHM files inside the container's tmpfs / overlay, which means:

1. `sqlite3` on the host would read a stale snapshot (the WAL
   isn't checkpointed into the main db until orch exits cleanly
   or runs `PRAGMA wal_checkpoint`).
2. A container restart could corrupt the pair because the WAL
   replay file wouldn't survive the overlay reset.

Bind-mounting the directory keeps all three files on the host
filesystem, atomically visible to both orch and the operator's
tooling. This is the same rationale sqlite's own docs give for
"always put the WAL and SHM in the same directory as the main DB."

## Template change

`internal/projects/templates/project-compose.yml.tmpl` — orch
`volumes:` block now reads:

```yaml
    volumes:
      - {{.HostDataDir}}:/var/lib/drem:rw
      - drem-{{.ProjectName}}-prompts:/var/lib/drem/prompts
      ...
```

The prompts named volume stays as it was. The pre-pivot
`drem-<project>-db` named volume is still **declared** in the
top-level `volumes:` block so docker still knows about the existing
volume — that's the migration artifact. It is no longer **referenced**
by any service, so docker will not create a fresh empty volume on a
new project. Operators on an existing pre-pivot project (today:
`drem-orchestrator` with ~4.3 GiB of live state) migrate via the
runbook below before restarting orch.

## Field-add

`internal/projects/template.go`:

- New `TemplateData.HostDataDir` field. Default in `applyDefaults`
  is `<HostHome>/.drem/projects/<ProjectName>/data`, mirroring how
  `WorkerPromptRoot` is derived. Explicit override wins (e.g. NFS,
  dedicated SSD, encrypted volume).
- `WriteProjectComposeAt` now calls `applyDefaults` **locally**
  before the post-render `MkdirAll` steps so the data/prompts dirs
  actually land at the default path when the caller didn't set
  them explicitly. Render still gets its own by-value copy and
  re-runs applyDefaults internally — double-application is
  idempotent.
- `WriteProjectComposeAt` pre-creates `HostDataDir` with
  `os.MkdirAll(..., 0o755)` then best-effort
  `os.Chown(..., 1000, 1000)`. The Chown matches the drem user
  inside the container image (per `deploy/docker/worker-base.Dockerfile`
  and the csuite-watcher uid-1000 fix in commit 469dd38). Chown
  errors are ignored — the operator may be running as non-root
  on host and the orch container runs as root today regardless,
  but the alignment is load-bearing once orch goes non-root.

## Migration runbook — operator copy-paste

For the existing `drem-orchestrator` project (the only one today
carrying state in a named volume). **Kyle will run this during the
cutover — do not run it from within this feature commit sequence.**

```
# 1. Stop orch + agentmon cleanly so the WAL is checkpointed.
#    (Leave csuite + watcher + planner + classifier running;
#    they don't touch /var/lib/drem.)
docker compose -f ~/.drem/projects/drem-orchestrator/compose.yml \
  stop orch agentmon

# 2. Copy the named volume's contents into the new host dir.
#    The host dir is already pre-created with 1000:1000 ownership
#    by `drem project register --update` (see field-add above); the
#    chown in the one-liner is belt-and-suspenders for operators
#    who registered before this change.
docker run --rm \
  -v drem-orchestrator_drem-drem-orchestrator-db:/src:ro \
  -v /home/godinj/.drem/projects/drem-orchestrator/data:/dst \
  alpine sh -c 'cp -a /src/. /dst/ && chown -R 1000:1000 /dst'

# 3. Verify the drem.db + WAL + SHM are present.
ls -la /home/godinj/.drem/projects/drem-orchestrator/data/

# 4. Re-render compose with the new bind-mount shape.
drem project register --update drem-orchestrator --force

# 5. Bring orch + agentmon back up with the new bind-mount.
docker compose -f ~/.drem/projects/drem-orchestrator/compose.yml \
  up -d --no-deps orch agentmon

# 6. Confirm orch sees its historical state (row counts should
#    match pre-migration).
sqlite3 /home/godinj/.drem/projects/drem-orchestrator/data/drem.db \
  'SELECT COUNT(*) FROM tasks;'
```

The old named volume `drem-orchestrator_drem-drem-orchestrator-db`
stays on disk as a rollback artifact until the operator explicitly
prunes it (`docker volume rm`). Keep it around for at least one
sglang cycle before pruning so a rollback is just a one-line revert
of the compose template plus a restart, with no data replay.

## Regression tests

`internal/projects/template_test.go` gains four new cases:

- `TestRender_HostDataDirBindMountIsWired` — asserts the orch
  volumes block contains `<HostDataDir>:/var/lib/drem:rw` with the
  expected default-derived path, asserts the pre-pivot
  `drem-<project>-db:/var/lib/drem` named-volume mount is **gone**
  (regression guard so nothing silently re-introduces it), and
  confirms the prompts named volume still stacks on top.
- `TestRender_HostDataDirDefaultsFromHostHome` — asserts the
  applyDefaults branch fills in the per-project default path when
  both HostDataDir and HostHome start empty at the caller but
  HostHome is set.
- `TestRender_HostDataDirExplicitOverride` — asserts a caller-
  supplied HostDataDir wins over the HostHome-derived default.
- `TestWriteProjectComposeAt_PrecreatesHostDataDir` — asserts the
  write helper actually creates the host-side dir on disk, so
  docker's auto-create-as-root doesn't race the first `docker
  compose up`.

The existing drift-detection test still passes because the
`drem-<project>-db` named volume declaration in the top-level
`volumes:` block is preserved (Diff walks structural keys; the
removal is inside `services.orch.volumes[0]`, a positional list,
where the renderer emits the bind-mount string and any on-disk
named-volume string is classified as a `changed` drift entry that
--force overrides at update time).

## Rollout

No deploy changes needed from the feature commit alone — the next
time Kyle runs `drem project register --update drem-orchestrator
--force`, the regenerated compose.yml picks up the new bind-mount
shape. The migration runbook above is the operator's contract for
the one live project that has state to carry forward.

## Non-changes (explicit)

- `deploy/docker/orch.Dockerfile` and the `VOLUME ["/var/lib/drem"]`
  declaration inside it stay as-is. That declaration is a hint to
  docker build, not a binding constraint on compose mount shape; the
  bind-mount at compose-up time overrides it.
- The compose template's label blocks (`drem.project`,
  `drem.service`, `drem.agent_type`) are untouched — task #8's
  dual-label work owns that surface area in a separate worktree.
- No code under `internal/orchestrator/`, `cmd/drem/main.go`,
  `internal/agentmon/`, or the spawner label code is touched —
  those are owned by tasks #8 and #9 in sibling worktrees.

## Future work (already scoped elsewhere)

- Option C: richer HTTP endpoints for DB inspection. Blocked on
  Bug D. Design survives in `plans/orch-db-host-access.md`.
- Once orch goes non-root, revisit whether the 1000:1000 chown
  should be a hard requirement (right now it's best-effort, since
  orch runs as root and can write anything).
