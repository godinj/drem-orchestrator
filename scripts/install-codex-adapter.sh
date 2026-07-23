#!/usr/bin/env bash
# Install the host-side Codex control adapter without copying credentials.

set -euo pipefail
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repo_root="$(cd "${script_dir}/.." && pwd -P)"
destination="${DREM_CODEX_BIN_DIR:-${HOME}/.local/bin}"

mkdir -p "$destination"
tmp_binary="$(mktemp "${destination}/.dremctl.XXXXXX")"
trap 'rm -f "$tmp_binary"' EXIT
(cd "$repo_root" && go build -trimpath -o "$tmp_binary" ./cmd/dremctl)
chmod 755 "$tmp_binary"
mv "$tmp_binary" "${destination}/dremctl"
ln -sfn "${repo_root}/scripts/drem-canvas-pilot.sh" "${destination}/drem-canvas-pilot"

echo "installed ${destination}/dremctl"
echo "installed ${destination}/drem-canvas-pilot"
if [[ ":${PATH}:" != *":${destination}:"* ]]; then
    echo "note: add ${destination} to PATH"
fi
