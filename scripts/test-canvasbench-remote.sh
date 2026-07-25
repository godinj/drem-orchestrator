#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/canvasbench-remote-test.XXXXXX")"
trap 'rm -rf "$test_root"' EXIT
mkdir -p "$test_root/bin" "$test_root/remote" "$test_root/local"
local_canvas="$test_root/local-canvas"
git init -q "$local_canvas"
git -C "$local_canvas" config user.name CanvasBench
git -C "$local_canvas" config user.email canvasbench@example.invalid
printf 'fixture\n' > "$local_canvas/fixture.txt"
git -C "$local_canvas" add fixture.txt
git -C "$local_canvas" commit -qm 'fixture base'
canvas_base="$(git -C "$local_canvas" rev-parse HEAD)"
remote_canvas="$test_root/remote/canvas.git"
remote_orch="$test_root/remote/orch.git"
git init -q --bare "$remote_canvas"
git init -q --bare "$remote_orch"
fixture_repo="$test_root/repo"
mkdir -p "$fixture_repo/scripts" "$fixture_repo/bench/canvasbench-v2/tasks"
cp "$repo_root/scripts/canvasbench-remote.sh" "$fixture_repo/scripts/canvasbench-remote.sh"
printf '{"cases":[{"task_file":"tasks/case.json"}]}\n' > "$fixture_repo/bench/canvasbench-v2/manifest.json"
printf '{"fixture":{"repo_id":"drem-canvas","base_commit":"%s"}}\n' "$canvas_base" > "$fixture_repo/bench/canvasbench-v2/tasks/case.json"
git -C "$fixture_repo" init -q
git -C "$fixture_repo" config user.name CanvasBench
git -C "$fixture_repo" config user.email canvasbench@example.invalid
git -C "$fixture_repo" add .
git -C "$fixture_repo" commit -qm 'test fixture'

cat > "$test_root/bin/ssh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
while [[ $# -gt 0 ]]; do
    case "$1" in
        -p|-o|-i) shift 2 ;;
        *) break ;;
    esac
done
shift
exec bash -c "$1"
EOF
cat > "$test_root/bin/scp" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
while [[ $# -gt 2 ]]; do
    case "$1" in
        -P|-o|-i) shift 2 ;;
        *) break ;;
    esac
done
source_path="${1#*:}"
destination_path="${2#*:}"
mkdir -p "$(dirname "$destination_path")"
cp "$source_path" "$destination_path"
EOF
cat > "$test_root/bin/canvasbench" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" > "$CANVASBENCH_TEST_ARGS"
output=""
previous=""
for argument in "$@"; do
    if [[ "$previous" == "-out" ]]; then output="$argument"; fi
    previous="$argument"
done
[[ -n "$output" ]]
mkdir -p "$(dirname "$output")"
printf '{"ok":true}\n' > "$output.jsonl"
printf '{"ok":true}\n' > "$output.json"
printf '# ok\n' > "$output.md"
printf 'ok\n' > "$output.csv"
EOF
chmod 0700 "$test_root/bin/ssh" "$test_root/bin/scp" "$test_root/bin/canvasbench"
printf '{"schema":"canvasbench.matrix.v2"}\n' > "$test_root/matrix.json"

export CANVASBENCH_SSH_BIN="$test_root/bin/ssh"
export CANVASBENCH_SCP_BIN="$test_root/bin/scp"
export CANVASBENCH_TEST_ARGS="$test_root/args"
export DREM_CANVASBENCH_REMOTE_HOST='test@debian'
export DREM_CANVASBENCH_REMOTE_PORT=22
export DREM_CANVASBENCH_REMOTE_ROOT="$test_root/remote/runs"
export DREM_CANVASBENCH_LOCAL_CANVAS_REPO="$local_canvas"
export DREM_CANVASBENCH_REMOTE_CANVAS_REPO="$remote_canvas"
export DREM_CANVASBENCH_REMOTE_ORCH_REPO="$remote_orch"
export DREM_CANVASBENCH_REMOTE_USAGE_PROXY_TOKEN_FILE='/run/secrets/canvasbench-admin.token'
export DREM_CANVASBENCH_REMOTE_EXECUTABLE="$test_root/bin/canvasbench"

"$fixture_repo/scripts/canvasbench-remote.sh" \
    --matrix "$test_root/matrix.json" \
    --out "$test_root/local/run" >/dev/null

for suffix in jsonl json md csv; do
    test -s "$test_root/local/run.$suffix"
done
grep -Fxq "$remote_canvas" "$test_root/args"
grep -Fxq "$remote_orch" "$test_root/args"
grep -Fxq '/run/secrets/canvasbench-admin.token' "$test_root/args"
test "$(git -C "$remote_canvas" rev-parse "refs/canvasbench/fixtures/$canvas_base^{commit}")" = "$canvas_base"
if find "$test_root/remote/runs" -mindepth 1 -print -quit | grep -q .; then
    echo 'successful remote run staging was not cleaned' >&2
    exit 1
fi

printf '\n' >> "$fixture_repo/bench/canvasbench-v2/manifest.json"
if "$fixture_repo/scripts/canvasbench-remote.sh" \
    --matrix "$test_root/matrix.json" --out "$test_root/local/dirty" \
    >"$test_root/dirty.out" 2>"$test_root/dirty.err"; then
    echo 'dirty orchestrator source unexpectedly passed remote admission' >&2
    exit 1
fi
grep -q 'requires a clean committed orchestrator checkout' "$test_root/dirty.err"
