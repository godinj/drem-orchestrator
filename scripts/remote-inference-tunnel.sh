#!/usr/bin/env bash
# Open a foreground loopback-only SSH tunnel from this host to a remote GQ.
# The caller owns process supervision (interactive shell, launchd, systemd,
# or autossh). No credentials or hostnames are baked into the repository.

set -euo pipefail

: "${DREM_INFERENCE_SSH_HOST:?set DREM_INFERENCE_SSH_HOST (for example user@host)}"

local_port="${DREM_INFERENCE_LOCAL_PORT:-18090}"
remote_host="${DREM_INFERENCE_REMOTE_HOST:-127.0.0.1}"
remote_port="${DREM_INFERENCE_REMOTE_PORT:-8090}"
ssh_port="${DREM_INFERENCE_SSH_PORT:-22}"

ssh_args=(
    -N
    -L "127.0.0.1:${local_port}:${remote_host}:${remote_port}"
    -p "${ssh_port}"
    -o ExitOnForwardFailure=yes
	-o BatchMode=yes
	-o ConnectTimeout=10
    -o ServerAliveInterval=30
    -o ServerAliveCountMax=3
)

if [[ -n "${DREM_INFERENCE_SSH_IDENTITY:-}" ]]; then
    ssh_args+=(-i "${DREM_INFERENCE_SSH_IDENTITY}")
fi

exec ssh "${ssh_args[@]}" "${DREM_INFERENCE_SSH_HOST}"
