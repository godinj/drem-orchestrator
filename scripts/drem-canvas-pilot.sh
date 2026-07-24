#!/usr/bin/env bash
# Exact-artifact native Canvas preparation and evidence helper for Codex.

set -euo pipefail

project="${DREM_PROJECT:-canvas-local}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repo_root="$(cd "${script_dir}/.." && pwd -P)"
pilot_root="${DREM_CANVAS_PILOT_ROOT:-${HOME}/.drem/projects/${project}}"
pilot_root="${pilot_root%/}"
[[ "$pilot_root" == /* && "$pilot_root" != "/" ]] || {
    echo "DREM_CANVAS_PILOT_ROOT must be an absolute non-root path: $pilot_root" >&2
    exit 2
}

dremctl_cmd() {
    if command -v dremctl >/dev/null 2>&1; then
        dremctl --project "$project" "$@"
    else
        (cd "$repo_root" && go run ./cmd/dremctl --project "$project" "$@")
    fi
}

registered_bare_repo() {
    PROJECT_NAME="$project" python3 - <<'PY'
import ast, os, pathlib
p = pathlib.Path.home() / ".drem" / "projects.toml"
current = {}
projects = []
for raw in p.read_text().splitlines():
    line = raw.strip()
    if line == "[[projects]]":
        if current:
            projects.append(current)
        current = {}
        continue
    if not line or line.startswith("#") or "=" not in line:
        continue
    key, value = (part.strip() for part in line.split("=", 1))
    if key in {"name", "bare_repo_path"}:
        current[key] = ast.literal_eval(value)
if current:
    projects.append(current)
for registered in projects:
    if registered.get("name") == os.environ["PROJECT_NAME"]:
        print(registered["bare_repo_path"])
        raise SystemExit(0)
raise SystemExit(f"project {os.environ['PROJECT_NAME']!r} is not registered in {p}")
PY
}

artifact_field() {
    local json_file="$1" field="$2"
    ARTIFACT_JSON="$json_file" ARTIFACT_FIELD="$field" python3 - <<'PY'
import json, os
with open(os.environ["ARTIFACT_JSON"]) as f:
    envelope = json.load(f)
print(envelope["artifact"][os.environ["ARTIFACT_FIELD"]])
PY
}

artifact_json() {
    local task="$1" output
    output="$(mktemp)"
    dremctl_cmd --json artifact "$task" > "$output"
    printf '%s\n' "$output"
}

canonical_dir() {
    (cd "$1" && pwd -P)
}

assert_exact_worktree() {
    local task="$1" worktree="$2" metadata expected_commit actual_commit bare common
    metadata="$(artifact_json "$task")"
    expected_commit="$(artifact_field "$metadata" commit_sha)"
    rm -f "$metadata"

    [[ -d "$worktree" ]] || { echo "artifact worktree does not exist: $worktree" >&2; exit 1; }
    actual_commit="$(git -C "$worktree" rev-parse HEAD)"
    [[ "$actual_commit" == "$expected_commit" ]] || {
        echo "artifact worktree HEAD $actual_commit does not match frozen commit $expected_commit" >&2
        exit 1
    }
    [[ -z "$(git -C "$worktree" status --porcelain --untracked-files=all)" ]] || {
        echo "artifact worktree is dirty: $worktree" >&2
        exit 1
    }

    bare="$(canonical_dir "$(registered_bare_repo)")"
    common="$(git -C "$worktree" rev-parse --git-common-dir)"
    if [[ "$common" != /* ]]; then
        common="$(canonical_dir "$worktree/$common")"
    else
        common="$(canonical_dir "$common")"
    fi
    [[ "$common" == "$bare" ]] || {
        echo "artifact worktree belongs to $common, expected registered repository $bare" >&2
        exit 1
    }
}

usage() {
    cat <<'EOF'
usage: drem-canvas-pilot <doctor|start|revise|await|prepare|direct-prepare|build|verify|goal-usage|report|experiment-init|experiment-record|experiment-report|cleanup> ...

  doctor [--base SHA] [--min-free-gib N] [--container-disk-audit]
      Fail fast on repository, disk, cache, toolchain, and control-plane readiness.

  start --spec FILE
      File one attributed Canvas canary specification and print its task ID.
  revise TASK --spec FILE --reason TEXT
      Replace a reviewer-rejected execution plan without invoking the planner.
  await TASK [--timeout DURATION]
      Follow orchestration until host verification, rework, or a terminal state.

  prepare TASK
      Create/reuse a detached exact-artifact host worktree and print its path.
  direct-prepare --base SHA --run-id ID
      Create a Drem-owned detached worktree for a direct Codex comparison arm.
  build TASK
      Prepare the worktree, then run scripts/dev verify natively.
  verify TASK --worktree PATH --binary PATH --interactions FILE [--result pass|fail]
      [--failure-mode orchestrated|host-direct --failure-reason TEXT]
      Submit exact binary and Computer Use evidence through dremctl.
  goal-usage TASK --goal-objective TEXT --goal-status complete|blocked --tokens-used N --elapsed-ms N
      Attach the final explicit Codex goal usage returned after goal completion.
  report TASK [--json] [--output FILE]
      Emit one correlated phase/token/artifact/Computer Use measurement report.
  experiment-init --id ID --spec FILE --base SHA
      Freeze one immutable paired-run contract (exact spec bytes and base commit).
  experiment-record --id ID --arm orchestrated|direct --outcome STATUS --tokens N --elapsed-ms N --commit SHA [--task ID] [--binary FILE] [--evidence FILE]
      Append one immutable arm result to the paired experiment.
  experiment-report --id ID
      Render the canonical apples-to-apples arm comparison JSON.
  cleanup --worktree PATH
      Remove one pilot-owned worktree after validating its path.
EOF
}

doctor() {
    local base="" min_free_gib=8 container_disk_audit=0 bare available_kib required_kib path
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --base) base="$2"; shift 2 ;;
            --min-free-gib) min_free_gib="$2"; shift 2 ;;
            --container-disk-audit) container_disk_audit=1; shift ;;
            *) echo "unknown doctor argument: $1" >&2; return 2 ;;
        esac
    done
    [[ "$min_free_gib" =~ ^[0-9]+$ ]] || { echo "--min-free-gib must be a non-negative integer" >&2; return 2; }
    for tool in git python3 shasum cmake; do
        command -v "$tool" >/dev/null 2>&1 || { echo "missing required tool: $tool" >&2; return 1; }
    done
    bare="$(registered_bare_repo)"
    [[ -d "$bare" ]] || { echo "registered bare repository does not exist: $bare" >&2; return 1; }
    if [[ -n "$base" ]]; then
        git --git-dir="$bare" cat-file -e "${base}^{commit}" 2>/dev/null || { echo "base commit is absent from registered repository: $base" >&2; return 1; }
    fi
    for path in "$pilot_root" "$pilot_root/host-verification" "$pilot_root/direct-runs" "$pilot_root/experiments"; do
        mkdir -p "$path"
        [[ -w "$path" ]] || { echo "path is not writable: $path" >&2; return 1; }
    done
    [[ -d "${bare}/.cache/skia" ]] || { echo "shared Skia cache is missing: ${bare}/.cache/skia" >&2; return 1; }
    available_kib="$(df -Pk "$bare" | awk 'NR==2 {print $4}')"
    required_kib=$((min_free_gib * 1024 * 1024))
    (( available_kib >= required_kib )) || { echo "insufficient disk: ${available_kib} KiB available, ${required_kib} KiB required" >&2; return 1; }
    dremctl_cmd status >/dev/null
    if (( container_disk_audit )); then
        if ! "$repo_root/scripts/drem-container-disk.sh" audit; then
            printf 'doctor_warning=container_disk_audit_unavailable action="rerun scripts/drem-container-disk.sh audit directly"\n' >&2
        fi
    fi
    printf 'doctor=ready project=%s bare_repo=%s available_kib=%s min_free_gib=%s\n' "$project" "$bare" "$available_kib" "$min_free_gib"
}

direct_prepare() {
    local base="" run_id="" bare target root cache resolved
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --base) base="$2"; shift 2 ;;
            --run-id) run_id="$2"; shift 2 ;;
            *) echo "unknown direct-prepare argument: $1" >&2; return 2 ;;
        esac
    done
    [[ -n "$base" && "$run_id" =~ ^[A-Za-z0-9._-]+$ ]] || { echo "direct-prepare requires --base SHA and a safe --run-id" >&2; return 2; }
    bare="$(registered_bare_repo)"
    resolved="$(git --git-dir="$bare" rev-parse "${base}^{commit}")"
    root="$pilot_root/direct-runs"
    target="${root}/${run_id}"
    mkdir -p "$root"
    if [[ -e "$target" ]]; then
        [[ "$(git -C "$target" rev-parse HEAD)" == "$resolved" ]] || { echo "existing direct run has the wrong HEAD: $target" >&2; return 1; }
        [[ -z "$(git -C "$target" status --porcelain --untracked-files=all)" ]] || { echo "existing direct run is dirty: $target" >&2; return 1; }
    else
        git --git-dir="$bare" worktree add --detach "$target" "$resolved" >/dev/null
    fi
    cache="${bare}/.cache/skia"
    if [[ -d "$cache" && ! -e "$target/libs/skia" ]]; then
        mkdir -p "$target/libs"
        ln -s "$cache" "$target/libs/skia"
    fi
    printf '%s\n' "$target"
}

experiment_root() {
    local experiment_id="$1"
    [[ "$experiment_id" =~ ^[A-Za-z0-9._-]+$ ]] || { echo "unsafe experiment id: $experiment_id" >&2; return 2; }
    printf '%s/experiments/%s\n' "$pilot_root" "$experiment_id"
}

prepare() {
    local task="$1" bare artifact_json commit version target root cache
    bare="$(registered_bare_repo)"
    artifact_json="$(mktemp)"
    dremctl_cmd --json artifact "$task" > "$artifact_json"
    commit="$(artifact_field "$artifact_json" commit_sha)"
    version="$(artifact_field "$artifact_json" artifact_version)"
    rm -f "$artifact_json"
    root="$pilot_root/host-verification"
    target="${root}/${task:0:8}-v${version}-${commit:0:12}"
    mkdir -p "$root"
    if [[ -e "$target" ]]; then
        [[ "$(git -C "$target" rev-parse HEAD)" == "$commit" ]] || { echo "existing worktree has the wrong HEAD: $target" >&2; exit 1; }
        [[ -z "$(git -C "$target" status --porcelain --untracked-files=all)" ]] || { echo "existing worktree is dirty: $target" >&2; exit 1; }
    else
        git --git-dir="$bare" worktree add --detach "$target" "$commit" >/dev/null
    fi
    cache="${bare}/.cache/skia"
    if [[ -d "$cache" && ! -e "$target/libs/skia" ]]; then
        mkdir -p "$target/libs"
        ln -s "$cache" "$target/libs/skia"
    fi
    printf '%s\n' "$target"
}

command="${1:-}"
case "$command" in
    doctor)
        shift
        doctor "$@"
        ;;
    direct-prepare)
        shift
        direct_prepare "$@"
        ;;
    experiment-init)
        shift
        experiment_id="" spec="" base=""
        while [[ $# -gt 0 ]]; do
            case "$1" in
                --id) experiment_id="$2"; shift 2 ;;
                --spec) spec="$2"; shift 2 ;;
                --base) base="$2"; shift 2 ;;
                *) echo "unknown experiment-init argument: $1" >&2; exit 2 ;;
            esac
        done
        [[ -f "$spec" && -n "$base" ]] || { echo "experiment-init requires --id, --spec FILE, and --base SHA" >&2; exit 2; }
        root="$(experiment_root "$experiment_id")"
        bare="$(registered_bare_repo)"
        base="$(git --git-dir="$bare" rev-parse "${base}^{commit}")"
        mkdir -p "$(dirname "$root")"
        EXPERIMENT_ROOT="$root" EXPERIMENT_ID="$experiment_id" EXPERIMENT_SPEC="$spec" EXPERIMENT_BASE="$base" PROJECT_NAME="$project" python3 - <<'PY'
import hashlib, json, os, pathlib, tempfile
root = pathlib.Path(os.environ["EXPERIMENT_ROOT"])
spec = pathlib.Path(os.environ["EXPERIMENT_SPEC"]).read_bytes()
metadata = {
    "schema_version": 1,
    "id": os.environ["EXPERIMENT_ID"],
    "project": os.environ["PROJECT_NAME"],
    "base_commit": os.environ["EXPERIMENT_BASE"],
    "spec_sha256": hashlib.sha256(spec).hexdigest(),
}
if root.exists():
    if (root / "spec.json").read_bytes() != spec or json.loads((root / "experiment.json").read_text()) != metadata:
        raise SystemExit("experiment id already exists with a different immutable contract")
else:
    root.mkdir()
    (root / "arms").mkdir()
    (root / "spec.json").write_bytes(spec)
    (root / "experiment.json").write_text(json.dumps(metadata, sort_keys=True, indent=2) + "\n")
print(f"experiment_id={metadata['id']} spec_sha256={metadata['spec_sha256']} base_commit={metadata['base_commit']}")
PY
        ;;
    experiment-record)
        shift
        experiment_id="" arm="" outcome="" tokens="" elapsed_ms="" commit="" task="" binary="" evidence=""
        while [[ $# -gt 0 ]]; do
            case "$1" in
                --id) experiment_id="$2"; shift 2 ;;
                --arm) arm="$2"; shift 2 ;;
                --outcome) outcome="$2"; shift 2 ;;
                --tokens) tokens="$2"; shift 2 ;;
                --elapsed-ms) elapsed_ms="$2"; shift 2 ;;
                --commit) commit="$2"; shift 2 ;;
                --task) task="$2"; shift 2 ;;
                --binary) binary="$2"; shift 2 ;;
                --evidence) evidence="$2"; shift 2 ;;
                *) echo "unknown experiment-record argument: $1" >&2; exit 2 ;;
            esac
        done
        [[ "$arm" == "orchestrated" || "$arm" == "direct" ]] || { echo "--arm must be orchestrated or direct" >&2; exit 2; }
        [[ "$tokens" =~ ^[0-9]+$ && "$elapsed_ms" =~ ^[1-9][0-9]*$ ]] || { echo "--tokens and positive --elapsed-ms are required" >&2; exit 2; }
        [[ -n "$outcome" && -n "$commit" ]] || { echo "--outcome and --commit are required" >&2; exit 2; }
        [[ -z "$binary" || -f "$binary" ]] || { echo "binary does not exist: $binary" >&2; exit 2; }
        [[ -z "$evidence" || -f "$evidence" ]] || { echo "evidence does not exist: $evidence" >&2; exit 2; }
        root="$(experiment_root "$experiment_id")"
        [[ -f "$root/experiment.json" ]] || { echo "unknown experiment: $experiment_id" >&2; exit 1; }
        bare="$(registered_bare_repo)"
        commit="$(git --git-dir="$bare" rev-parse "${commit}^{commit}")"
        base="$(EXPERIMENT_ROOT="$root" python3 -c 'import json,os; print(json.load(open(os.path.join(os.environ["EXPERIMENT_ROOT"],"experiment.json")))["base_commit"])')"
        git --git-dir="$bare" merge-base --is-ancestor "$base" "$commit" || { echo "arm commit is not descended from immutable experiment base" >&2; exit 1; }
        binary_sha=""; evidence_sha=""
        [[ -z "$binary" ]] || binary_sha="$(shasum -a 256 "$binary" | awk '{print $1}')"
        [[ -z "$evidence" ]] || evidence_sha="$(shasum -a 256 "$evidence" | awk '{print $1}')"
        EXPERIMENT_ROOT="$root" EXPERIMENT_ARM="$arm" EXPERIMENT_OUTCOME="$outcome" EXPERIMENT_TOKENS="$tokens" EXPERIMENT_ELAPSED_MS="$elapsed_ms" EXPERIMENT_COMMIT="$commit" EXPERIMENT_TASK="$task" EXPERIMENT_BINARY_SHA="$binary_sha" EXPERIMENT_EVIDENCE_SHA="$evidence_sha" CODEX_THREAD="${CODEX_THREAD_ID:-}" python3 - <<'PY'
import json, os, pathlib
root = pathlib.Path(os.environ["EXPERIMENT_ROOT"])
target = root / "arms" / (os.environ["EXPERIMENT_ARM"] + ".json")
record = {
    "arm": os.environ["EXPERIMENT_ARM"],
    "mode": "sglang_orchestrated" if os.environ["EXPERIMENT_ARM"] == "orchestrated" else "codex_direct",
    "outcome": os.environ["EXPERIMENT_OUTCOME"],
    "codex_tokens": int(os.environ["EXPERIMENT_TOKENS"]),
    "elapsed_ms": int(os.environ["EXPERIMENT_ELAPSED_MS"]),
    "commit_sha": os.environ["EXPERIMENT_COMMIT"],
    "task_id": os.environ["EXPERIMENT_TASK"] or None,
    "binary_sha256": os.environ["EXPERIMENT_BINARY_SHA"] or None,
    "evidence_sha256": os.environ["EXPERIMENT_EVIDENCE_SHA"] or None,
    "codex_thread_id": os.environ["CODEX_THREAD"] or None,
}
try:
    with target.open("x") as f:
        json.dump(record, f, sort_keys=True, indent=2)
        f.write("\n")
except FileExistsError:
    if json.loads(target.read_text()) != record:
        raise SystemExit("experiment arm is append-only and already has a different result")
print(f"experiment_arm={record['arm']} outcome={record['outcome']} codex_tokens={record['codex_tokens']} commit_sha={record['commit_sha']}")
PY
        ;;
    experiment-report)
        shift
        [[ "${1:-}" == "--id" && $# -eq 2 ]] || { echo "experiment-report requires --id ID" >&2; exit 2; }
        root="$(experiment_root "$2")"
        EXPERIMENT_ROOT="$root" python3 - <<'PY'
import json, os, pathlib
root = pathlib.Path(os.environ["EXPERIMENT_ROOT"])
meta = json.loads((root / "experiment.json").read_text())
arms = []
for name in ("orchestrated", "direct"):
    path = root / "arms" / (name + ".json")
    if path.exists():
        arms.append(json.loads(path.read_text()))
report = {"experiment": meta, "arms": arms}
if len(arms) == 2:
    report["comparison"] = {
        "codex_token_delta_direct_minus_orchestrated": arms[1]["codex_tokens"] - arms[0]["codex_tokens"],
        "elapsed_ms_delta_direct_minus_orchestrated": arms[1]["elapsed_ms"] - arms[0]["elapsed_ms"],
    }
print(json.dumps(report, sort_keys=True, indent=2))
PY
        ;;
    start)
        shift
        [[ "${1:-}" == "--spec" && $# -eq 2 && -f "${2:-}" ]] || { usage >&2; exit 2; }
        task_json="$(mktemp)"
        trap 'rm -f "$task_json"' EXIT
        dremctl_cmd --json create --spec "$2" > "$task_json"
        TASK_JSON="$task_json" python3 - <<'PY'
import json, os
with open(os.environ["TASK_JSON"]) as f:
    task = json.load(f)
print("task_id=" + task["id"])
PY
        ;;
    revise)
        [[ $# -ge 5 ]] || { usage >&2; exit 2; }
        task="$2"; shift 2
        spec=""; reason=""
        while [[ $# -gt 0 ]]; do
            case "$1" in
                --spec) spec="$2"; shift 2 ;;
                --reason) reason="$2"; shift 2 ;;
                *) echo "unknown revise argument: $1" >&2; exit 2 ;;
            esac
        done
        [[ -f "$spec" && -n "$reason" ]] || { echo "revise requires --spec FILE and --reason TEXT" >&2; exit 2; }
        dremctl_cmd revise-plan "$task" --spec "$spec" --reason "$reason"
        ;;
    await)
        [[ $# -ge 2 ]] || { usage >&2; exit 2; }
        task="$2"; shift 2
        timeout="30m"
        while [[ $# -gt 0 ]]; do
            case "$1" in
                --timeout) timeout="$2"; shift 2 ;;
                *) echo "unknown await argument: $1" >&2; exit 2 ;;
            esac
        done
        dremctl_cmd follow "$task" \
            --until verification_ready,host_rework,integration_ready,done,failed,rejected,needs_clarification,cancelled \
            --timeout "$timeout"
        ;;
    prepare)
        [[ $# -eq 2 ]] || { usage >&2; exit 2; }
        prepare "$2"
        ;;
    build)
        [[ $# -eq 2 ]] || { usage >&2; exit 2; }
        worktree="$(prepare "$2")"
        (cd "$worktree" && scripts/dev preflight && scripts/dev verify)
        printf 'verified_worktree=%s\n' "$worktree"
        ;;
    verify)
        [[ $# -ge 2 ]] || { usage >&2; exit 2; }
        task="$2"; shift 2
        worktree=""; binary=""; interactions=""; result="pass"; failure_mode=""; failure_reason=""
        while [[ $# -gt 0 ]]; do
            case "$1" in
                --worktree) worktree="$2"; shift 2 ;;
                --binary) binary="$2"; shift 2 ;;
                --interactions) interactions="$2"; shift 2 ;;
                --result) result="$2"; shift 2 ;;
                --failure-mode) failure_mode="$2"; shift 2 ;;
                --failure-reason) failure_reason="$2"; shift 2 ;;
                *) echo "unknown verify argument: $1" >&2; exit 2 ;;
            esac
        done
        [[ -d "$worktree" && -f "$binary" && -f "$interactions" ]] || { echo "verify requires existing --worktree, --binary, and --interactions" >&2; exit 2; }
        [[ "$result" == "pass" || "$result" == "fail" ]] || { echo "--result must be pass or fail" >&2; exit 2; }
        if [[ "$result" == "fail" && ( -z "$failure_mode" || -z "$failure_reason" ) ]]; then
            echo "failed verification requires --failure-mode and --failure-reason" >&2
            exit 2
        fi
        assert_exact_worktree "$task" "$worktree"
        worktree="$(canonical_dir "$worktree")"
        binary_dir="$(canonical_dir "$(dirname "$binary")")"
        binary="${binary_dir}/$(basename "$binary")"
        case "$binary" in
            "$worktree"/*) ;;
            *) echo "verified binary must be inside the exact artifact worktree: $binary" >&2; exit 1 ;;
        esac
        binary_sha="$(shasum -a 256 "$binary" | awk '{print $1}')"
        environment="$(sw_vers -productVersion 2>/dev/null || uname -r);$(uname -m);$(hostname)"
        verify_args=(verify "$task" --result "$result" \
            --environment "$environment" \
            --binary-sha256 "$binary_sha" \
            --command "scripts/dev verify" \
            --interactions "$interactions")
        if [[ "$result" == "fail" ]]; then
            verify_args+=(--failure-mode "$failure_mode" --failure-reason "$failure_reason")
        fi
        dremctl_cmd "${verify_args[@]}"
        ;;
    report)
        [[ $# -ge 2 ]] || { usage >&2; exit 2; }
        task="$2"; shift 2
        json_mode=false; output=""
        while [[ $# -gt 0 ]]; do
            case "$1" in
                --json) json_mode=true; shift ;;
                --output) output="$2"; shift 2 ;;
                *) echo "unknown report argument: $1" >&2; exit 2 ;;
            esac
        done
        report_args=(report "$task")
        if [[ "$json_mode" == true ]]; then
            report_args=(--json "${report_args[@]}")
        fi
        if [[ -n "$output" ]]; then
            mkdir -p "$(dirname "$output")"
            report_tmp="$(mktemp "$(dirname "$output")/.drem-report.XXXXXX")"
            trap 'rm -f "$report_tmp"' EXIT
            dremctl_cmd "${report_args[@]}" > "$report_tmp"
            mv "$report_tmp" "$output"
            trap - EXIT
            printf 'report=%s\n' "$output"
        else
            dremctl_cmd "${report_args[@]}"
        fi
        ;;
    goal-usage)
        [[ $# -ge 2 ]] || { usage >&2; exit 2; }
        task="$2"; shift 2
        dremctl_cmd codex-usage "$task" "$@"
        ;;
    cleanup)
        shift
        [[ "${1:-}" == "--worktree" && $# -eq 2 ]] || { usage >&2; exit 2; }
        target="$2"
        root="$pilot_root/host-verification"
        case "$target" in
            "$root"/*) ;;
            *) echo "refusing to remove non-pilot path: $target" >&2; exit 1 ;;
        esac
        bare="$(registered_bare_repo)"
        [[ -z "$(git -C "$target" status --porcelain --untracked-files=all)" ]] || {
            echo "refusing to remove dirty pilot worktree: $target" >&2
            exit 1
        }
        git --git-dir="$bare" worktree remove "$target"
        ;;
    -h|--help|help|'') usage ;;
    *) echo "unknown command: $command" >&2; usage >&2; exit 2 ;;
esac
