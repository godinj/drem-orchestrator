# Container-side host-exec smoke — kyle-container

**Date:** 2026-04-23T22:35Z
**Actor:** Kyle (inside drem-kyle container)
**Wrapper:** `/opt/csuite/bin/host-exec` (mode 0755, owner root, 2172 bytes)
**Token:** `/etc/drem/host-exec.token` (mode 0600, 65 bytes, bind-mount from host)
**Daemon URL:** `http://host.docker.internal:8091` (default in wrapper)

## Probes (README §Smoke test plan, items 1–6)

| # | Command | Expected | Observed | Verdict |
|---|---|---|---|---|
| 1 | `host-exec date` | current UTC | `Thu Apr 23 03:35:37 PM PDT 2026` | ✅ pass |
| 2 | `host-exec drem status` | drem CLI output | `2026/04/23 15:35:37 --repo is required: path to bare git repo` | ✅ pass (CLI reached; missing `--repo` is a drem-args issue, not a wiring issue; earlier `drem --help` also succeeded) |
| 3 | `host-exec sudo ls` | 403 / denylist | `denied: denylist: sudo *` | ✅ pass |
| 4 | `host-exec git push --force origin main` | 403 / denylist | `denied: denylist: git push --force origin main` | ✅ pass |
| 5 | `host-exec git status` | clean-tree or similar | `fatal: not a git repository (or any of the parent directories): .git` | ✅ pass (command reached git; daemon CWD is not a repo — expected) |
| 6 | `host-exec echo hello` | `hello` | `hello` | ✅ pass |
| 7 | audit log has 6 JSON lines with 2 `denied_reason` entries | — | deferred — log lives at `/home/godinj/.drem-csuite/host-exec.log` on host, not visible from container. Operator or Mike (if wired later) to verify. | ⏸ deferred |

## Sidecar observations

- `host-exec /bin/true` returns `denied: no allowlist match` — correct, `/bin/true` isn't on the allowlist. Not a smoke probe per README, recorded to disambiguate.
- Probe sequence took ~2s wall-clock; no retries, no timeouts.

## Outcome

Container-side wiring (Dockerfile bake + compose bind-mount + force-recreate) is **live and functional** from inside kyle-container. The "Pending — container-side smoke" item in `orch-plans/host-exec-daemon-option-a.md` can be resolved.

Mike (ops audit): cross-sign welcome.
Seth (quality audit): ready for your pass whenever you pick it up.

— Kyle
