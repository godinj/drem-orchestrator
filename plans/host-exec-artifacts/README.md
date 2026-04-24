# drem-host-exec

## What it is

A small host-side HTTP daemon that lets container-Kyle run allowlisted commands
on the host (git, docker compose, drem CLI, filesystem mutations) via a
bearer-token-protected POST. Denylist fences (no sudo, no force-push to main,
no bash -c) are enforced before the allowlist.

## Directory tree

Host:
  /usr/local/bin/drem-host-exec              # compiled Go binary
  /etc/systemd/system/drem-host-exec.service # systemd unit
  /etc/drem/host-exec.env                    # EnvironmentFile
  /etc/drem/host-exec.token                  # bearer token, mode 0600 godinj
  /etc/drem/host-exec.allowlist              # allowed patterns
  /etc/drem/host-exec.denylist               # denied patterns
  /home/godinj/.drem-csuite/host-exec.log    # audit log (JSONL, mode 0600)

Container-Kyle:
  /opt/csuite/bin/host-exec                  # bash wrapper on PATH
  /etc/drem/host-exec.token                  # bind-mounted ro from host

## Install (host side)

    # 1. Build the binary (Go 1.21+, stdlib only).
    cd /path/to/drem-host-exec-src
    go build -o drem-host-exec .
    sudo install -m 0755 -o root -g root drem-host-exec /usr/local/bin/drem-host-exec

    # 2. Config dir + files.
    sudo install -d -m 0755 /etc/drem
    sudo install -m 0644 host-exec.env       /etc/drem/host-exec.env
    sudo install -m 0644 host-exec.allowlist /etc/drem/host-exec.allowlist
    sudo install -m 0644 host-exec.denylist  /etc/drem/host-exec.denylist

    # 3. Generate the bearer token (mode 0600, owned by godinj).
    sudo install -m 600 -o godinj /dev/null /etc/drem/host-exec.token
    sudo sh -c 'openssl rand -hex 32 > /etc/drem/host-exec.token'
    sudo chown godinj:godinj /etc/drem/host-exec.token
    sudo chmod 600 /etc/drem/host-exec.token

    # 4. Audit-log dir.
    sudo -u godinj install -d -m 0755 /home/godinj/.drem-csuite

    # 5. systemd unit.
    sudo install -m 0644 drem-host-exec.service /etc/systemd/system/drem-host-exec.service
    sudo systemctl daemon-reload
    sudo systemctl enable --now drem-host-exec
    sudo systemctl status drem-host-exec --no-pager

## Host-side smoke test

    curl -H "Authorization: Bearer $(sudo cat /etc/drem/host-exec.token)" \
         -H "Content-Type: application/json" \
         -d '{"command":"date","args":[]}' \
         http://172.17.0.1:8091/exec

Expect a JSON body with `"stdout": "..."`, `"exit_code": 0`.

## Container-side install

1. In the container-Kyle image, copy the wrapper into place and mark it
   executable:

       COPY host-exec /opt/csuite/bin/host-exec
       RUN chmod +x /opt/csuite/bin/host-exec
       ENV PATH="/opt/csuite/bin:${PATH}"
       RUN apt-get update && apt-get install -y --no-install-recommends \
             curl jq ca-certificates openssl \
         && rm -rf /var/lib/apt/lists/*

2. In container-Kyle's compose service, bind-mount the token read-only and set
   the daemon URL:

       services:
         drem-kyle:
           # ...
           environment:
             HOST_EXEC_URL: http://host.docker.internal:8091
           volumes:
             - /etc/drem/host-exec.token:/etc/drem/host-exec.token:ro
           extra_hosts:
             - "host.docker.internal:host-gateway"

3. Rebuild and restart container-Kyle:

       docker compose build drem-kyle
       docker compose up -d drem-kyle

## Smoke test plan (for operator post-install)

1. `host-exec date` from container-Kyle -> expect current UTC.
2. `host-exec drem status` -> expect drem's normal output.
3. `host-exec sudo ls` -> expect 403 (denylist hit).
4. `host-exec git push --force origin main` -> expect 403 (denylist hit).
5. `host-exec git status` from repo root -> expect clean-tree output (or whatever state is).
6. `host-exec echo hello` -> expect `hello\n`, exit 0.
7. Check `~/.drem-csuite/host-exec.log` has 6 JSON lines with 2 `denied_reason` entries.

## Uninstall / kill switch

    sudo systemctl stop drem-host-exec && sudo systemctl disable drem-host-exec

To fully remove:

    sudo rm /etc/systemd/system/drem-host-exec.service
    sudo rm /usr/local/bin/drem-host-exec
    sudo rm -rf /etc/drem
    sudo systemctl daemon-reload

## Known issues (non-blocking, flagged by Seth during quality pass)

1. **Per-stream output cap, not combined.** Each of stdout/stderr has its own
   10 MiB cap, so worst-case combined output is 20 MiB not 10 MiB. `truncated`
   flag fires correctly when the sum exceeds 10 MiB. OOM bar met either way.
   Fix is trivial (shared atomic counter) but deferred to follow-up.

2. **Token bind-mount UID mismatch risk.** Host `/etc/drem/host-exec.token` is
   mode 0600 owned by godinj. If container-Kyle runs as a different UID, the
   wrapper's `tr -d < /etc/drem/host-exec.token` will hit EACCES. Mitigation
   options:
   (a) Run container-Kyle with `user: <godinj-uid>:<godinj-gid>` in compose.
   (b) Inject token as env var via `env_file:` referencing a file shaped like
       `HOST_EXEC_TOKEN=<hex>`.
   Recommended: (a). The first container-side smoke test (`host-exec date`)
   will surface the problem immediately if this bites.
