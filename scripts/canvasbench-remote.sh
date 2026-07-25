#!/usr/bin/env bash
set -euo pipefail

# Mac control-plane adapter for a Debian-hosted CanvasBench run. The complete
# benchmark process runs remotely so Docker bind mounts, Canvas worktrees,
# native builds, harnesses, and inference all share the Debian filesystem.

usage() {
    cat >&2 <<'EOF'
usage: scripts/canvasbench-remote.sh --matrix FILE --out PREFIX [--manifest REPO_RELATIVE_FILE]

Required environment:
  DREM_CANVASBENCH_REMOTE_HOST                 user@debian-host
  DREM_CANVASBENCH_LOCAL_CANVAS_REPO           authoritative Mac Canvas repository
  DREM_CANVASBENCH_REMOTE_CANVAS_REPO          absolute Debian Canvas checkout
  DREM_CANVASBENCH_REMOTE_ORCH_REPO            absolute Debian orchestrator checkout
  DREM_CANVASBENCH_REMOTE_USAGE_PROXY_TOKEN_FILE

Optional environment:
  DREM_CANVASBENCH_REMOTE_PORT                 SSH port (default 22)
  DREM_CANVASBENCH_REMOTE_IDENTITY             SSH identity file
  DREM_CANVASBENCH_REMOTE_ROOT                 Debian staging root
  DREM_CANVASBENCH_REMOTE_ENDPOINT             Debian-host-visible inference endpoint
  DREM_CANVASBENCH_REMOTE_USAGE_PROXY_ADMIN_URL
  DREM_CANVASBENCH_REMOTE_USAGE_PROXY_PUBLIC_BASE_URL
  DREM_CANVASBENCH_REMOTE_EXECUTABLE           installed canvasbench binary; default uses go run
  DREM_CANVASBENCH_REMOTE_KEEP_RUN=yes         retain successful remote staging
EOF
    exit 2
}

matrix=""
manifest="bench/canvasbench-v2/manifest.json"
output=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --matrix) matrix="${2:-}"; shift 2 ;;
        --manifest) manifest="${2:-}"; shift 2 ;;
        --out) output="${2:-}"; shift 2 ;;
        -h|--help) usage ;;
        *) printf 'unknown argument: %s\n' "$1" >&2; usage ;;
    esac
done

[[ -n "$matrix" && -f "$matrix" && -n "$output" ]] || usage
: "${DREM_CANVASBENCH_REMOTE_HOST:?set DREM_CANVASBENCH_REMOTE_HOST}"
: "${DREM_CANVASBENCH_LOCAL_CANVAS_REPO:?set DREM_CANVASBENCH_LOCAL_CANVAS_REPO}"
: "${DREM_CANVASBENCH_REMOTE_CANVAS_REPO:?set DREM_CANVASBENCH_REMOTE_CANVAS_REPO}"
: "${DREM_CANVASBENCH_REMOTE_ORCH_REPO:?set DREM_CANVASBENCH_REMOTE_ORCH_REPO}"
: "${DREM_CANVASBENCH_REMOTE_USAGE_PROXY_TOKEN_FILE:?set DREM_CANVASBENCH_REMOTE_USAGE_PROXY_TOKEN_FILE}"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repo_root="$(cd "$script_dir/.." && pwd -P)"
local_canvas_repo="$(cd "$DREM_CANVASBENCH_LOCAL_CANVAS_REPO" && pwd -P)"
matrix="$(cd "$(dirname "$matrix")" && pwd -P)/$(basename "$matrix")"
output="$(mkdir -p "$(dirname "$output")" && cd "$(dirname "$output")" && pwd -P)/$(basename "$output")"

if ! git -C "$repo_root" ls-files --error-unmatch "$manifest" >/dev/null 2>&1; then
    printf 'manifest must be a tracked repository-relative file: %s\n' "$manifest" >&2
    exit 2
fi
revision="$(git -C "$repo_root" rev-parse --verify HEAD^{commit})"
if [[ -n "$(git -C "$repo_root" status --short)" ]]; then
    printf 'remote CanvasBench requires a clean committed orchestrator checkout\n' >&2
    exit 2
fi

remote_host="$DREM_CANVASBENCH_REMOTE_HOST"
remote_port="${DREM_CANVASBENCH_REMOTE_PORT:-22}"
remote_root="${DREM_CANVASBENCH_REMOTE_ROOT:-/home/${USER}/.cache/drem/canvasbench/remote-runs}"
remote_endpoint="${DREM_CANVASBENCH_REMOTE_ENDPOINT:-http://127.0.0.1:18090/v1/chat/completions}"
remote_admin_url="${DREM_CANVASBENCH_REMOTE_USAGE_PROXY_ADMIN_URL:-http://127.0.0.1:18091}"
remote_public_url="${DREM_CANVASBENCH_REMOTE_USAGE_PROXY_PUBLIC_BASE_URL:-http://canvasbench-usage-proxy:8080/v1}"

case "$remote_host" in *[!A-Za-z0-9._@-]*|'') printf 'invalid remote host\n' >&2; exit 2 ;; esac
case "$remote_port" in *[!0-9]*|'') printf 'invalid remote port\n' >&2; exit 2 ;; esac
for remote_path in "$remote_root" "$DREM_CANVASBENCH_REMOTE_CANVAS_REPO" "$DREM_CANVASBENCH_REMOTE_ORCH_REPO" "$DREM_CANVASBENCH_REMOTE_USAGE_PROXY_TOKEN_FILE"; do
    case "$remote_path" in /*) ;; *) printf 'remote paths must be absolute: %s\n' "$remote_path" >&2; exit 2 ;; esac
    [[ "$remote_path" != "/" && "$remote_path" != *$'\n'* && "$remote_path" != *$'\r'* ]] || {
        printf 'unsafe remote path: %s\n' "$remote_path" >&2
        exit 2
    }
done

quote_remote() {
    python3 -c 'import shlex,sys; print(shlex.quote(sys.argv[1]))' "$1"
}

ssh_bin="${CANVASBENCH_SSH_BIN:-ssh}"
scp_bin="${CANVASBENCH_SCP_BIN:-scp}"
ssh_args=(-p "$remote_port" -o BatchMode=yes -o ConnectTimeout=10)
scp_args=(-P "$remote_port" -o BatchMode=yes -o ConnectTimeout=10)
if [[ -n "${DREM_CANVASBENCH_REMOTE_IDENTITY:-}" ]]; then
    ssh_args+=(-i "$DREM_CANVASBENCH_REMOTE_IDENTITY")
    scp_args+=(-i "$DREM_CANVASBENCH_REMOTE_IDENTITY")
fi

run_id="${revision:0:12}-$(date -u +%Y%m%dT%H%M%SZ)-$$"
remote_run="${remote_root%/}/$run_id"
remote_source="$remote_run/source"
remote_scratch="$remote_run/scratch"
remote_output="$remote_run/results/run"
remote_matrix="$remote_run/matrix.json"

remote_exec() {
    "$ssh_bin" "${ssh_args[@]}" "$remote_host" "$1"
}

# Git is the code data plane. MCP/API may supervise a run, but exact fixture
# commits move from the Mac's authoritative repositories to namespaced refs in
# the Debian execution caches before any workspace is created.
git_ssh_command="$ssh_bin -p $remote_port -o BatchMode=yes -o ConnectTimeout=10"
if [[ -n "${DREM_CANVASBENCH_REMOTE_IDENTITY:-}" ]]; then
    git_ssh_command+=" -i $(quote_remote "$DREM_CANVASBENCH_REMOTE_IDENTITY")"
fi
fixture_commits="$(python3 - "$repo_root/$manifest" <<'PY'
import json
from pathlib import Path
import sys

manifest_path = Path(sys.argv[1]).resolve()
manifest = json.loads(manifest_path.read_text())
fixtures = set()
for case in manifest.get("cases", []):
    task = json.loads((manifest_path.parent / case["task_file"]).resolve().read_text())
    fixture = task["fixture"]
    fixtures.add((fixture["repo_id"], fixture["base_commit"]))
for repo_id, commit in sorted(fixtures):
    print(repo_id, commit)
PY
)"
while read -r repo_id commit; do
    [[ -n "$repo_id" ]] || continue
    case "$repo_id" in
        drem-canvas) local_repo="$local_canvas_repo"; remote_repo="$DREM_CANVASBENCH_REMOTE_CANVAS_REPO" ;;
        drem-orchestrator) local_repo="$repo_root"; remote_repo="$DREM_CANVASBENCH_REMOTE_ORCH_REPO" ;;
        *) printf 'unsupported fixture repository: %s\n' "$repo_id" >&2; exit 2 ;;
    esac
    if ! git -C "$local_repo" cat-file -e "$commit^{commit}"; then
        printf 'authoritative Mac repository is missing fixture commit %s:%s\n' "$repo_id" "$commit" >&2
        exit 1
    fi
    fixture_ref="refs/canvasbench/fixtures/$commit"
    GIT_SSH_COMMAND="$git_ssh_command" git -C "$local_repo" push --porcelain \
        "$remote_host:$remote_repo" "$commit:$fixture_ref" </dev/null >/dev/null
    remote_commit="$(remote_exec "git -C $(quote_remote "$remote_repo") rev-parse $(quote_remote "$fixture_ref^{commit}")")"
    if [[ "$remote_commit" != "$commit" ]]; then
        printf 'Debian fixture ref mismatch for %s:%s\n' "$repo_id" "$commit" >&2
        exit 1
    fi
done <<< "$fixture_commits"

setup_command="mkdir -p $(quote_remote "$remote_source") $(quote_remote "$remote_scratch") $(quote_remote "$(dirname "$remote_output")")"
remote_exec "$setup_command"
if ! git -C "$repo_root" archive --format=tar "$revision" | remote_exec "tar -xf - -C $(quote_remote "$remote_source")"; then
    printf 'failed to stage committed orchestrator source; remote run retained at %s\n' "$remote_run" >&2
    exit 1
fi
"$scp_bin" "${scp_args[@]}" "$matrix" "$remote_host:$remote_matrix"

if [[ -n "${DREM_CANVASBENCH_REMOTE_EXECUTABLE:-}" ]]; then
    remote_command=("$DREM_CANVASBENCH_REMOTE_EXECUTABLE")
else
    remote_command=(go run ./cmd/canvasbench)
fi
remote_command+=(
    -matrix "$remote_matrix"
    -manifest "$remote_source/$manifest"
    -canvas-repo "$DREM_CANVASBENCH_REMOTE_CANVAS_REPO"
    -orchestrator-repo "$DREM_CANVASBENCH_REMOTE_ORCH_REPO"
    -scratch "$remote_scratch"
    -out "$remote_output"
    -endpoint "$remote_endpoint"
    -usage-proxy-admin-url "$remote_admin_url"
    -usage-proxy-public-base-url "$remote_public_url"
    -usage-proxy-admin-token-file "$DREM_CANVASBENCH_REMOTE_USAGE_PROXY_TOKEN_FILE"
)
quoted_command="cd $(quote_remote "$remote_source") &&"
for argument in "${remote_command[@]}"; do
    quoted_command+=" $(quote_remote "$argument")"
done

if ! remote_exec "$quoted_command"; then
    printf 'remote CanvasBench failed; evidence retained at %s:%s\n' "$remote_host" "$remote_run" >&2
    exit 1
fi

local_stage="$(mktemp -d "${TMPDIR:-/tmp}/canvasbench-remote-results.XXXXXX")"
cleanup_local() { rm -rf "$local_stage"; }
trap cleanup_local EXIT
for suffix in jsonl json md csv; do
    "$scp_bin" "${scp_args[@]}" "$remote_host:$remote_output.$suffix" "$local_stage/run.$suffix"
    [[ -s "$local_stage/run.$suffix" ]] || {
        printf 'remote CanvasBench result is missing or empty: %s.%s\n' "$remote_output" "$suffix" >&2
        exit 1
    }
done
for suffix in jsonl json md csv; do
    mv "$local_stage/run.$suffix" "$output.$suffix"
done

if [[ "${DREM_CANVASBENCH_REMOTE_KEEP_RUN:-no}" != "yes" ]]; then
    remote_exec "rm -rf -- $(quote_remote "$remote_run")"
fi
printf '%s\n' "$output"
