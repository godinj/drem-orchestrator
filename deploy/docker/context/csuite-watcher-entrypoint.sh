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

# chown is idempotent and cheap: once the named volume has been
# chowned on first start it stays that way across restarts. Running it
# unconditionally keeps the entrypoint stateless.
chown -R drem:drem /var/lib/watcher 2>/dev/null || true

# /csuite/ is a bind-mount of the operator's host tree — we do NOT
# chown it here. The persona containers are already uid 1000 and so
# is the host operator, so ownership is already correct; recursively
# chowning a host tree from inside a container would be a nasty
# surprise.

exec gosu drem "$@"
