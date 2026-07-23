#!/usr/bin/env bash
# Install a self-healing, loopback-only remote-inference SSH tunnel on macOS.

set -euo pipefail

label="org.drem.remote-inference-tunnel"
mode="install"
dry_run=0
for arg in "$@"; do
    case "$arg" in
        --dry-run) dry_run=1 ;;
        --uninstall) mode="uninstall" ;;
        -h|--help)
            sed -n '1,28p' "$0"
            exit 0
            ;;
        *) echo "unknown argument: $arg" >&2; exit 2 ;;
    esac
done

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
tunnel_script="${script_dir}/remote-inference-tunnel.sh"
launch_agents_dir="${HOME}/Library/LaunchAgents"
plist_path="${launch_agents_dir}/${label}.plist"
log_dir="${HOME}/.drem/logs"
domain="gui/$(id -u)"

if [[ "$mode" == "uninstall" ]]; then
    if [[ "$dry_run" -eq 1 ]]; then
        echo "would bootout ${domain}/${label} and remove ${plist_path}"
        exit 0
    fi
    launchctl bootout "${domain}/${label}" 2>/dev/null || true
    rm -f "$plist_path"
    echo "uninstalled ${label}"
    exit 0
fi

: "${DREM_INFERENCE_SSH_HOST:?set DREM_INFERENCE_SSH_HOST}"
local_port="${DREM_INFERENCE_LOCAL_PORT:-18090}"
remote_host="${DREM_INFERENCE_REMOTE_HOST:-127.0.0.1}"
remote_port="${DREM_INFERENCE_REMOTE_PORT:-8090}"
ssh_port="${DREM_INFERENCE_SSH_PORT:-22}"
identity="${DREM_INFERENCE_SSH_IDENTITY:-}"

xml_escape() {
    printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' -e 's/"/\&quot;/g'
}

render_plist() {
    cat <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>${label}</string>
  <key>ProgramArguments</key><array><string>$(xml_escape "$tunnel_script")</string></array>
  <key>EnvironmentVariables</key><dict>
    <key>DREM_INFERENCE_SSH_HOST</key><string>$(xml_escape "$DREM_INFERENCE_SSH_HOST")</string>
    <key>DREM_INFERENCE_LOCAL_PORT</key><string>$(xml_escape "$local_port")</string>
    <key>DREM_INFERENCE_REMOTE_HOST</key><string>$(xml_escape "$remote_host")</string>
    <key>DREM_INFERENCE_REMOTE_PORT</key><string>$(xml_escape "$remote_port")</string>
    <key>DREM_INFERENCE_SSH_PORT</key><string>$(xml_escape "$ssh_port")</string>
    <key>DREM_INFERENCE_SSH_IDENTITY</key><string>$(xml_escape "$identity")</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>$(xml_escape "$log_dir/tunnel.stdout.log")</string>
  <key>StandardErrorPath</key><string>$(xml_escape "$log_dir/tunnel.stderr.log")</string>
</dict></plist>
EOF
}

if [[ "$dry_run" -eq 1 ]]; then
    render_plist
    exit 0
fi

mkdir -p "$launch_agents_dir" "$log_dir"
tmp_plist="$(mktemp "${launch_agents_dir}/.${label}.XXXXXX")"
trap 'rm -f "$tmp_plist"' EXIT
render_plist > "$tmp_plist"
plutil -lint "$tmp_plist" >/dev/null
chmod 600 "$tmp_plist"
mv "$tmp_plist" "$plist_path"
launchctl bootout "${domain}/${label}" 2>/dev/null || true
launchctl bootstrap "$domain" "$plist_path"
launchctl enable "${domain}/${label}"
launchctl kickstart -k "${domain}/${label}"
echo "installed ${label}; tunnel endpoint is http://127.0.0.1:${local_port}"
