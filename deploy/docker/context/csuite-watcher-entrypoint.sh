#!/bin/sh
#
# csuite-watcher container entrypoint.
#
# Two responsibilities:
#
#   1. Ensure the persisted watcher state dir (/var/lib/watcher,
#      backed by the drem-<project>-csuite-watcher-data named volume)
#      is writable by uid 1000. Docker-created named volumes come up
#      root-owned on first mount; the drem user cannot create
#      deliveries.db inside a root-owned dir. We chown it before
#      dropping privileges.
#
#   2. Exec the watcher binary as the drem user (uid 1000) so files
#      the watcher writes into /csuite/<persona>/inbox/ are owned by
#      uid 1000 on the host. The host operator (godinj, uid 1000) and
#      Kyle's host-side Go binary can then archive those files without
#      sudo. See plans/csuite-watcher-outbox-routing.md §7a.
#
# The script runs as root only long enough to fix ownership; the
# actual watcher process runs as drem.

set -eu

# Accept both plain `serve` (the Dockerfile CMD) and an explicit full
# command. If the caller passed an explicit path we respect it — this
# is how `docker compose run --rm csuite-watcher sh` still works.
if [ "${1:-}" = "serve" ] || [ $# -eq 0 ]; then
    set -- /usr/local/bin/drem-csuite-watcher serve
fi

# chown is idempotent and cheap: once the named volumes have been
# chowned on first start they stay that way across restarts. Running
# it unconditionally keeps the entrypoint stateless.
#
# /var/lib/watcher holds the delivery ledger SQLite file
# (deliveries.db). /var/lib/drem holds the bridge API's csuite.db
# (historical bridge state). Both land on docker-managed named or
# anonymous dirs that come up root-owned on first mount; the drem
# user cannot create SQLite files under a root-owned parent.
chown -R drem:drem /var/lib/watcher /var/lib/drem 2>/dev/null || true

# If the Docker socket is mounted for persona controls, add drem to the
# socket's host group before dropping privileges. The socket is usually
# root:docker 0660, and the host docker gid is not portable across machines.
if [ -S /var/run/docker.sock ]; then
    docker_gid="$(stat -c %g /var/run/docker.sock 2>/dev/null || true)"
    if [ -n "$docker_gid" ]; then
        if ! getent group "$docker_gid" >/dev/null 2>&1; then
            groupadd --gid "$docker_gid" docker-host >/dev/null 2>&1 || true
        fi
        usermod -aG "$docker_gid" drem >/dev/null 2>&1 || true
    fi
fi

# /csuite/ is a bind-mount of the operator's host tree — we do NOT
# chown it here. The persona containers are already uid 1000 and so
# is the host operator, so ownership is already correct; recursively
# chowning a host tree from inside a container would be a nasty
# surprise.

exec gosu drem "$@"
