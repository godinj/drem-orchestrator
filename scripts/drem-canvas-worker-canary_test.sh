#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
canary="$script_dir/drem-canvas-worker-canary.sh"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT
mkdir -p "$test_root/bin" "$test_root/seed/tests/integration" "$test_root/tmp"

git -C "$test_root/seed" init -q
git -C "$test_root/seed" config user.email test@example.com
git -C "$test_root/seed" config user.name Test
printf 'base\n' > "$test_root/seed/README.md"
git -C "$test_root/seed" add README.md
git -C "$test_root/seed" commit -q -m base
base_sha="$(git -C "$test_root/seed" rev-parse HEAD)"
git clone -q --bare "$test_root/seed" "$test_root/canvas.git"

cat > "$test_root/bin/docker" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
if [ "$1" = image ] && [ "$2" = inspect ]; then
    printf '%s\n' fake-source-state
    exit 0
fi
if [ "$1" != run ]; then
    exit 2
fi
work=
trace=
while [ "$#" -gt 0 ]; do
    if [ "$1" = -v ]; then
        case "$2" in
            *:/home/drem/work) work="${2%:/home/drem/work}" ;;
            *:/trace) trace="${2%:/trace}" ;;
        esac
        shift 2
        continue
    fi
    shift
done
case "${FAKE_WORKER_MODE:-success}" in
    success)
        python3 - "$work/tests/integration/drem_orchestration_canary.cpp" <<'PY'
import pathlib,sys
p=pathlib.Path(sys.argv[1]); s=p.read_text(); p.write_text(s.replace("    // DREM_QWEN_INSERTION_ANCHOR\n", "    // DREM_QWEN_INSERTION_ANCHOR\n    // DREM_QWEN_SCOPED_MUTATION_OK\n", 1))
PY
        mkdir -p "$trace"
        printf '{"iteration":0,"tool_calls":[{"name":"edit","result":"ok"}]}\n' > "$trace/agent-trace-canary.jsonl"
        ;;
    no-mutation) ;;
    out-of-scope) printf 'bad\n' > "$work/unrelated.txt" ;;
    fail) exit 9 ;;
esac
FAKE
chmod +x "$test_root/bin/docker" "$canary"

pass=0
fail=0
expect_success() {
    local output
    if output="$(DREM_DOCKER_BIN="$test_root/bin/docker" DREM_CANVAS_WORKER_CANARY_REPO="$test_root/canvas.git" DREM_CANVAS_WORKER_CANARY_TMPDIR="$test_root/tmp" DREM_CANVAS_WORKER_CANARY_SOURCE_STATE=fake-source-state "$canary" --base "$base_sha")"; then
        printf '%s' "$output" | grep -q '"ok":true' && pass=$((pass + 1)) || fail=$((fail + 1))
    else
        fail=$((fail + 1))
    fi
}
expect_failure() {
    local mode="$1" output rc
    set +e
    output="$(FAKE_WORKER_MODE="$mode" DREM_DOCKER_BIN="$test_root/bin/docker" DREM_CANVAS_WORKER_CANARY_REPO="$test_root/canvas.git" DREM_CANVAS_WORKER_CANARY_TMPDIR="$test_root/tmp" "$canary" --base "$base_sha")"
    rc=$?
    set -e
    if [ "$rc" -ne 0 ] && printf '%s' "$output" | grep -q '"ok":false'; then
        pass=$((pass + 1))
    else
        fail=$((fail + 1))
    fi
}

expect_success
expect_failure no-mutation
expect_failure out-of-scope
expect_failure fail

set +e
output="$(DREM_DOCKER_BIN="$test_root/bin/docker" DREM_CANVAS_WORKER_CANARY_REPO="$test_root/canvas.git" DREM_CANVAS_WORKER_CANARY_TMPDIR="$test_root/tmp" "$canary")"
rc=$?
set -e
if [ "$rc" -eq 2 ] && printf '%s' "$output" | grep -q -- '--base is required'; then
    pass=$((pass + 1))
else
    fail=$((fail + 1))
fi

printf '%s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
