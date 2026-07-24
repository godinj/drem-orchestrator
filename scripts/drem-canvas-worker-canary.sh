#!/usr/bin/env bash
set -euo pipefail

# Real-model, repository-isolated worker canary. It clones one exact Canvas
# revision into a temporary directory, seeds an existing C++ test fixture, and
# requires the configured worker image/model to make one surgical in-scope edit.
# It never creates an orchestration task, pushes a branch, or mutates Canvas.

CANVAS_REPO="${DREM_CANVAS_WORKER_CANARY_REPO:-${HOME}/git/drem-canvas.git}"
BASE="${DREM_CANVAS_WORKER_CANARY_BASE:-}"
IMAGE="${DREM_CANVAS_WORKER_CANARY_IMAGE:-localhost:5000/drem-worker-cpp:latest}"
ENDPOINT="${DREM_CANVAS_WORKER_CANARY_ENDPOINT:-http://host.docker.internal:18090/v1/chat/completions}"
MODEL="${DREM_CANVAS_WORKER_CANARY_MODEL:-qwen3.6-27b-code}"
EXPECTED_SOURCE_STATE="${DREM_CANVAS_WORKER_CANARY_SOURCE_STATE:-}"
DOCKER="${DREM_DOCKER_BIN:-docker}"
KEEP="${DREM_CANVAS_WORKER_CANARY_KEEP:-no}"
MARKER="DREM_QWEN_SCOPED_MUTATION_OK"

usage() {
    printf 'usage: %s --base <canvas-commit>\n' "$0" >&2
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --base) BASE="${2:-}"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) usage; exit 2 ;;
    esac
done

json_string() {
    python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "${1:-}"
}

emit_failure() {
    local stage="$1" message="$2"
    printf '{"ok":false,"stage":%s,"message":%s,"orchestration_state_mutated":false,"canvas_repository_mutated":false}\n' \
        "$(json_string "$stage")" "$(json_string "$message")"
}

if [ -z "$BASE" ]; then
    emit_failure configuration "--base is required"
    exit 2
fi
if [ ! -d "$CANVAS_REPO" ]; then
    emit_failure configuration "Canvas repository does not exist: $CANVAS_REPO"
    exit 2
fi
if ! git --git-dir="$CANVAS_REPO" cat-file -e "${BASE}^{commit}" 2>/dev/null; then
    emit_failure configuration "Canvas base is not a commit: $BASE"
    exit 2
fi

source_state="$($DOCKER image inspect "$IMAGE" --format '{{index .Config.Labels "io.drem.source-state"}}' 2>/dev/null || true)"
if [ -z "$source_state" ]; then
    emit_failure image "worker image has no source-state attestation: $IMAGE"
    exit 1
fi
if [ -n "$EXPECTED_SOURCE_STATE" ] && [ "$source_state" != "$EXPECTED_SOURCE_STATE" ]; then
    emit_failure image "worker source-state mismatch: got $source_state expected $EXPECTED_SOURCE_STATE"
    exit 1
fi

canary_tmp_parent="${DREM_CANVAS_WORKER_CANARY_TMPDIR:-${HOME}/.drem/tmp}"
mkdir -p "$canary_tmp_parent"
if [ ! -d "$canary_tmp_parent" ] || [ ! -w "$canary_tmp_parent" ]; then
    canary_tmp_parent="${TMPDIR:-/tmp}"
fi
tmp_root="$(mktemp -d "$canary_tmp_parent/drem-canvas-worker-canary.XXXXXX")"
work_dir="$tmp_root/work"
prompt_path="$tmp_root/prompt.md"
trace_dir="$tmp_root/trace"
log_path="$tmp_root/worker.log"
fixture="tests/integration/drem_orchestration_canary.cpp"
cleanup() {
    if [ "$KEEP" = yes ]; then
        printf 'canary_workspace=%s\n' "$tmp_root" >&2
    else
        rm -rf "$tmp_root"
    fi
}
trap cleanup EXIT

git clone --quiet --no-checkout "$CANVAS_REPO" "$work_dir"
git -C "$work_dir" checkout --quiet --detach "$BASE"
git -C "$work_dir" config user.email drem-canary@localhost
git -C "$work_dir" config user.name drem-worker-canary
mkdir -p "$work_dir/tests/integration" "$trace_dir"
python3 - "$work_dir/$fixture" <<'PY'
import pathlib, sys
lines = [
    "#include <catch2/catch_test_macros.hpp>",
    "",
    'TEST_CASE ("Drem worker canary preserves existing coverage", "[integration][orchestration-canary]")',
    "{",
]
lines.extend(f"    // inherited dependency artifact line {n}" for n in range(1, 690))
lines.extend([
    "    // DREM_QWEN_INSERTION_ANCHOR",
    "    REQUIRE (true);",
    "}",
    "",
])
pathlib.Path(sys.argv[1]).write_text("\n".join(lines))
PY
git -C "$work_dir" add "$fixture"
git -C "$work_dir" commit --quiet -m "Seed isolated worker canary fixture"
baseline_sha="$(git -C "$work_dir" rev-parse HEAD)"

python3 - "$work_dir" "$BASE" "$prompt_path" "$fixture" "$MARKER" <<'PY'
import json, pathlib, subprocess, sys
work, base, output, fixture, marker = sys.argv[1:]
def excerpt(path, limit=4200):
    try:
        text=subprocess.check_output(
            ["git","-C",work,"show",f"{base}:{path}"],
            text=True,
            stderr=subprocess.DEVNULL,
        )
    except Exception:
        text=""
    return text[:limit]
pack={
  "kind":"canvas_worker_mutation_canary",
  "owned_files":[fixture],
  "source_evidence":[
    {"path":"src/ui/ActionCoordinator.cpp","excerpt":excerpt("src/ui/ActionCoordinator.cpp")},
    {"path":"src/ui/ActionAudioProcesses.cpp","excerpt":excerpt("src/ui/ActionAudioProcesses.cpp")},
    {"path":"config/default_keymap.yaml","excerpt":excerpt("config/default_keymap.yaml",2400)},
  ],
}
prompt=f'''You are a coder agent running a Canvas worker protocol canary.

## Task

Make exactly one surgical edit to the existing 696-line dependency artifact
`{fixture}`. Insert the line `    // {marker}` immediately after the near-end
line `    // DREM_QWEN_INSERTION_ANCHOR`. Preserve every existing test line.
Do not inspect or modify any other file. Do not use shell before editing.

## Working Directory

/home/drem/work

## Files to create/modify

- {fixture}

## Verified source pack

{json.dumps(pack, indent=2)}

## Execution contract

The source excerpts above are already completed reads and are intentionally
irrelevant to the tiny mutation. Read the writable fixture once, use `edit`
with an exact substring, and finish. Shell is unavailable before the mutation.
The harness owns Git finalization; do not commit or push.
'''
pathlib.Path(output).write_text(prompt)
PY

set +e
$DOCKER run --rm \
    --add-host host.docker.internal:host-gateway \
    -v "$work_dir:/home/drem/work" \
    -v "$tmp_root:/canary:ro" \
    -v "$trace_dir:/trace" \
    -e DREM_AGENT=coder \
    -e DREM_AGENT_ID=canvas-worker-canary \
    -e DREM_TRACE_DIR=/trace \
    -e DREM_DIRECT_ENDPOINT="$ENDPOINT" \
    -e DREM_MODEL="$MODEL" \
    -e DREM_DIRECT_MAX_TOKENS=2048 \
    -e DREM_DIRECT_MAX_ITERATIONS=8 \
    -e DREM_DIRECT_MAX_CUMULATIVE_INPUT_TOKENS=90000 \
    -e DREM_DIRECT_MAX_READS_BEFORE_MUTATION=2 \
    -e DREM_DIRECT_MAX_TOOL_CALLS=8 \
    -e DREM_DIRECT_MAX_INPUT_TOKENS_BEFORE_MUTATION=55000 \
    -e DREM_DIRECT_TEMPERATURE=0.1 \
    -e DREM_DIRECT_TIMEOUT=10m \
    -e DREM_DIRECT_BASH_TIMEOUT=30s \
    -e DREM_DIRECT_CHAT_TEMPLATE_KWARGS='{"enable_thinking":false}' \
    -e DREM_DIRECT_TOOL_ARGUMENTS_FORMAT=string \
    -e DREM_DIRECT_PROTECT_EXISTING_FILES=true \
    -e DREM_SCOPED_FILES_JSON="[\"$fixture\"]" \
    -e DREM_GQ_CALLER=worker-canary \
    -e DREM_GQ_PRIORITY=high \
    --entrypoint /usr/local/bin/drem-direct-agent \
    "$IMAGE" --role coder --prompt /canary/prompt.md --workdir /home/drem/work \
    >"$log_path" 2>&1
run_rc=$?
set -e

if [ "$run_rc" -ne 0 ]; then
    tail -n 40 "$log_path" >&2 || true
    emit_failure worker "direct worker exited $run_rc"
    exit 1
fi

changed="$(git -C "$work_dir" status --short)"
if [ "$changed" != " M $fixture" ]; then
    emit_failure scope "unexpected worker diff: ${changed:-none}"
    exit 1
fi
if ! grep -qF "// $MARKER" "$work_dir/$fixture"; then
    emit_failure semantic "worker did not create the required scoped marker"
    exit 1
fi
if ! grep -qF 'Drem worker canary preserves existing coverage' "$work_dir/$fixture"; then
    emit_failure semantic "worker replaced existing coverage"
    exit 1
fi
if ! git -C "$work_dir" diff --check; then
    emit_failure diff "worker checkpoint fails git diff --check"
    exit 1
fi
if [ "$(git -C "$work_dir" rev-parse HEAD)" != "$baseline_sha" ]; then
    emit_failure git "worker created a commit despite branchless canary mode"
    exit 1
fi

trace_path="$(find "$trace_dir" -type f -name 'agent-trace-*.jsonl' -print -quit)"
if [ -z "$trace_path" ]; then
    emit_failure telemetry "worker produced no structured trace"
    exit 1
fi
trace_sha="$(shasum -a 256 "$trace_path" | awk '{print $1}')"
log_sha="$(shasum -a 256 "$log_path" | awk '{print $1}')"
printf '{"ok":true,"stage":"complete","base":%s,"image":%s,"source_state":%s,"model":%s,"changed_file":%s,"trace_sha256":%s,"log_sha256":%s,"orchestration_state_mutated":false,"canvas_repository_mutated":false}\n' \
    "$(json_string "$BASE")" "$(json_string "$IMAGE")" "$(json_string "$source_state")" \
    "$(json_string "$MODEL")" "$(json_string "$fixture")" "$(json_string "$trace_sha")" "$(json_string "$log_sha")"
