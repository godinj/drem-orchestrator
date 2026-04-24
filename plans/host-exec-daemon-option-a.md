# Host-exec daemon — Option A (greenlit, full write scope)

**Status:** Operator greenlit 2026-04-23T19:16:43Z (corrid 08771633). Scope widened to full write 2026-04-23T20:17:49Z (corrid b128fbe7). **Installed 2026-04-23T20:44:46Z (corrid 2720d0a8) — system-scope, 7/8 host-side smokes pass. See addendum at bottom.**
**Owner:** Kyle (planning) → Seth (implementation).
**Origin message:** `outbox/delivered/2026-04-23T19:14:24Z-kyle-to-operator-e433ddda.md`.
**Scope expansion message:** `inbox/.archive/20260423T201749Z-operator-to-kyle-b128fbe7.md`.
**Install report:** `inbox/20260423T204446Z-operator-to-kyle-2720d0a8.md`.

## Decision

Build a host-side HTTP exec daemon (Option A from the four-option writeup). Container-Kyle POSTs a command + bearer token to `host.docker.internal:8091`; daemon checks an allowlist, shells out, returns stdout/stderr/exitcode as JSON. Symmetric to how Kyle already talks to `drem-kyle:8090`.

## Allowlist scope — FULL WRITE (revised 2026-04-23T20:17:49Z)

Operator explicitly said "full write scope" on b128fbe7. Prior read-mostly floor is retired. The daemon now admits writes — git commits, docker compose, filesystem mutations — with security fences preserved (bearer token, bridge-only bind, 30s timeout, full audit log, deny-list enforcement).

### Allowlist (one pattern per line, whitespace-split, glob on args)

```
# drem CLI — full
drem *

# git — reads
git log *
git status
git status -s
git status *
git diff
git diff *
git show *
git blame *
git ls-files *
git ls-remote *
git remote *
git config --get *
git rev-parse *
git describe *

# git — writes (force-push denylist is enforced below)
git add *
git rm *
git mv *
git commit *
git restore *
git checkout *
git switch *
git branch *
git merge *
git rebase *
git stash *
git reset *
git clean *
git push *
git pull *
git fetch *
git tag *
git cherry-pick *
git revert *
git notes *
git worktree *

# docker / compose
docker ps *
docker ps
docker logs *
docker inspect *
docker images *
docker images
docker image *
docker compose *
docker exec *
docker restart *
docker start *
docker stop *
docker rm *
docker rmi *
docker pull *
docker build *
docker network *
docker volume *
docker system prune *

# filesystem reads
ls *
ls
cat *
head *
tail *
stat *
file *
find *
wc *
du *
df *
df
realpath *
readlink *
tree *

# filesystem writes
mkdir *
touch *
cp *
mv *
rm *
tee *
chmod *
chown *
ln *

# process / diagnostics
ps *
pgrep *
pidof *
kill *
systemctl status *
systemctl restart drem-*
systemctl stop drem-*
systemctl start drem-*
journalctl *

# build / run
make *
go *
python *
python3 *
pip *
pip3 *
node *
npm *
yarn *
cargo *
bun *

# misc
date
date *
uname *
uname
hostname
whoami
id
env
printenv *
printenv
which *
echo *
sleep *
```

### Denylist (enforced BEFORE allowlist match; reject on hit)

These patterns are rejected even if they'd otherwise satisfy the allowlist. Kept deliberately short — audit log catches everything else.

```
# privilege escalation — entirely out
sudo *
su *
pkexec *
doas *

# force-push to main/master — matches CLAUDE.md standing rule
git push --force origin main
git push --force origin master
git push -f origin main
git push -f origin master
git push --force-with-lease origin main
git push --force-with-lease origin master

# destructive root targets
rm -rf /
rm -rf /*
rm -rf /home
rm -rf /etc
rm -rf /usr
rm -rf /var
chmod -R 777 /
chown -R * /

# interactive / TTY-required
vim *
vi *
nano *
emacs *
less *
more *
top
htop
ssh *
telnet *

# bash -c / eval escape hatches — force explicit commands
bash -c *
sh -c *
zsh -c *
eval *
exec *
```

### Arg-matching semantics (for Seth)

- Pattern matches by **whitespace-split prefix** with `*` as "match remaining args exactly as provided, no re-parse."
- `drem *` matches `drem csuite send mike -m "hello"` — args are passed through as a single argv slice, not re-globbed by the shell.
- Denylist is checked first, then allowlist. Deny wins ties.
- Empty args still match bare-command patterns (`ls` alone matches the `ls` line, not the `ls *` line — both are fine to list).

## Security fences (unchanged from read-mostly draft)

- Bind only to the docker bridge interface (not 0.0.0.0). Prefer `172.17.0.1:8091` or whatever `host.docker.internal` resolves to from the container side.
- Bearer token in systemd unit env via `HOST_EXEC_TOKEN_FILE=/etc/drem/host-exec.token` (mode 600, owner = repo owner).
- Every call appended to `~/.drem-csuite/host-exec.log` as one JSON line: `{ts, corrid, cmd, argv, exit, duration_ms, caller_ip, denied_reason?}`. Denied calls logged with `denied_reason` set.
- 30s hard timeout per exec (configurable via `?timeout=<seconds>` query param, max 600s).
- Reversible kill switch: `systemctl stop drem-host-exec` and Kyle loses access cleanly.
- No stdin piping — synchronous request/response, stdout + stderr captured to buffers.
- Per-call output size cap (e.g. 10 MiB stdout + stderr combined) to prevent OOM.

## Deliverable shape (unchanged)

Seth produces the implementation bundle as artifact-production (exact diffs + commands) and routes to Kyle's inbox. Kyle reviews and forwards to operator for install + smoke test. Operator installs, starts the systemd unit, verifies from container-Kyle with a smoke call.

## Smoke test plan (for operator post-install)

1. `host-exec date` from container-Kyle → expect current UTC.
2. `host-exec drem status` → expect drem's normal output.
3. `host-exec sudo ls` → expect 403 (denylist hit).
4. `host-exec git push --force origin main` → expect 403 (denylist hit).
5. `host-exec git status` from repo root → expect clean-tree output (or whatever state is).
6. `host-exec echo hello` → expect `hello\n`, exit 0.
7. Check `~/.drem-csuite/host-exec.log` has 6 JSON lines with 2 `denied_reason` entries.

## Delegation to Seth — firing this turn

Dropped into Seth's inbox as part of this turn (corrid below). Spec body follows in that message; this plan doc is the permanent reference.

## Addendum — as-shipped install shape (2026-04-23T20:44:46Z, corrid 2720d0a8)

The delivered install is **system-scope**, not the user-scope draft this plan originally sketched. Operator approved the sudo path at install time. This addendum is the reconciliation between plan and reality; the user-scope draft above is retained for history but is **considered, superseded** for the landed configuration.

### Delivered paths (supersede user-scope draft)
- **Binary:** `/usr/local/bin/drem-host-exec` (8.75 MiB, go1.25, stdlib only).
- **Unit:** `/etc/systemd/system/drem-host-exec.service` (`User=godinj`, `ProtectSystem=strict`, `ReadWritePaths=/home/godinj`).
- **Configs:** `/etc/drem/{host-exec.env, allowlist, denylist, token}` — token mode 0600 owned by godinj.
- **Audit log:** `/home/godinj/.drem-csuite/host-exec.log` (0600 godinj) — unchanged from user-scope draft, stayed in $HOME.
- **Listen:** `172.17.0.1:8091` (docker bridge, per spec).

### Install result
7/8 host-side smokes pass. Denylist (sudo, force-push, no-match), auth gate (401 bearer), and allowlist-matched exec paths all behave per spec. 7 JSONL audit entries written; 401 is pre-audit as designed. Install report: `inbox/20260423T204446Z-operator-to-kyle-2720d0a8.md`.

### One open flag — drem binary PATH
Smoke probe #2 (`drem --help`) matched allowlist correctly but execed with exit=-1 because systemd's minimal PATH doesn't include `/home/godinj/bin/drem`. **Kyle decision 2026-04-23T20:47:55Z (corrid d4e5f6a7):** append `PATH=/home/godinj/bin:/usr/local/bin:/usr/bin:/bin` to `/etc/drem/host-exec.env`, `daemon-reload`, restart, re-run probe #2. Env-file approach chosen over `/usr/local/bin/drem` symlink — keeps the user-scope binary boundary respected and makes the daemon's environment explicit/auditable.

### Pending — container-side smoke
README steps 1-6 via the `/opt/csuite/bin/host-exec` wrapper from container-Kyle. Requires image rebuild with wrapper baked in and `/etc/drem/host-exec.token:ro` bind-mounted into the container. Kyle threads to Mike once the PATH fix confirms.

### Resolved — container-side smoke landed (2026-04-23T22:22Z)

Image rebuild + compose wiring landed. Persistent across container restarts. Summary of the changes so the plan stays in sync with reality:

- **Baked into `drem-csuite-base`.** `deploy/docker/csuite-base.Dockerfile` now COPYs the wrapper to `/opt/csuite/bin/host-exec` (mode 0755) and extends the image `PATH` with `/opt/csuite/bin`. All four persona images (mike/alex/seth/kyle) inherit it, so Alex's pass-2 artifact flow gets the same access surface as Kyle's.
- **Staged by `build-csuite.sh`.** The build script copies `plans/host-exec-artifacts/host-exec` → `deploy/docker/context/host-exec` before `docker build` so the COPY target resolves. Canonical source stays under `plans/` (Seth's artifact-production output).
- **Compose: token mount + `extra_hosts`.** The live compose at `/home/godinj/.drem/projects/drem-orchestrator/compose.yml` and the `internal/projects/templates/project-compose.yml.tmpl` template both gained, on each csuite persona block:
  - `- /etc/drem/host-exec.token:/etc/drem/host-exec.token:ro` — host uid 1000 maps to container uid 1000, so mode 0600 on the host file is readable inside the container without chown, same pattern as `csuite-watcher.token`.
  - `extra_hosts: ["host.docker.internal:host-gateway"]` — `drem-net` is a user-defined bridge and otherwise hides the docker0 magic DNS entry; adding this lets the wrapper's default URL `http://host.docker.internal:8091` resolve without an env override.
- **Template field.** `TemplateData` gained `HostExecTokenPath` (defaults to `/etc/drem/host-exec.token`; documented in `internal/projects/template.go`). No caller needs to set it explicitly.
- **Stale cleanup.** `compose.override.yml` had a leftover `csuite-ross` stanza referencing a service that no longer exists in the base compose (legacy from before the fifth persona slot was repurposed for Kyle). Removed so `docker compose up` validates. Unrelated to host-exec work, but blocked the recreate.

Smoke results (all four personas recreated with `docker compose up -d --no-deps csuite-{mike,alex,seth,kyle}`):
- `host-exec date` from each of mike/alex/seth/kyle → current UTC ✓
- `host-exec echo hello` from kyle → `hello\n`, exit 0 ✓
- `host-exec sudo ls` from kyle → `denied: denylist: sudo *`, exit 126 ✓
- Wrapper on `PATH` (no absolute path needed); `host.docker.internal` resolves to `172.17.0.1` via `extra_hosts`.

### Seth audit hook — still outstanding
Seth's audit target is now the rebuilt images + the two compose files (live + template), not the ephemeral `docker cp` state from the first container-side probe. Drop a follow-up into his inbox when convenient.

### Seth audit hook
Seth confirmed (corrid b2e1c743) he will audit whichever configuration lands once smoke clears. System-scope shape is the audit target, not the user-scope draft.
