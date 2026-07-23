#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
tmp_root="$(mktemp -d)"
trap 'rm -rf "$tmp_root"' EXIT
mkdir -p "$tmp_root/bin" "$tmp_root/out"
mkdir -p "$tmp_root/home/.drem" "$tmp_root/seed"

git -C "$tmp_root/seed" init -q
git -C "$tmp_root/seed" config user.email test@example.com
git -C "$tmp_root/seed" config user.name Test
printf 'base\n' > "$tmp_root/seed/README.md"
git -C "$tmp_root/seed" add README.md
git -C "$tmp_root/seed" commit -q -m base
base_sha="$(git -C "$tmp_root/seed" rev-parse HEAD)"
git clone -q --bare "$tmp_root/seed" "$tmp_root/canvas.git"
mkdir -p "$tmp_root/canvas.git/.cache/skia"
cat > "$tmp_root/home/.drem/projects.toml" <<EOF
[[projects]]
name = "canvas-local"
bare_repo_path = "$tmp_root/canvas.git"
EOF

cat > "$tmp_root/bin/dremctl" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$DREM_TEST_ARGS"
case " $* " in
  *" create "*) printf '{"id":"12345678-1234-1234-1234-123456789abc"}\n' ;;
  *" revise-plan "*) printf '12345678 v2 plan_review canary\n' ;;
  *" --json report "*) printf '{"task":{"id":"12345678-1234-1234-1234-123456789abc"},"totals":{"tokens_in":12}}\n' ;;
  *" report "*) printf '# Drem task report: canary\n' ;;
  *" codex-usage "*) printf 'codex_usage_id=usage-1 tokens_used=321 elapsed_ms=654\n' ;;
  *" follow "*) printf '12345678 v4 verification_ready\n' ;;
  *" status "*) printf 'canvas-local healthy\n' ;;
  *) exit 3 ;;
esac
FAKE
chmod +x "$tmp_root/bin/dremctl"
printf '{}\n' > "$tmp_root/spec.json"
: > "$tmp_root/args.log"

run_pilot() {
    HOME="$tmp_root/home" PATH="$tmp_root/bin:$PATH" DREM_PROJECT=canvas-local DREM_TEST_ARGS="$tmp_root/args.log" \
        "$script_dir/drem-canvas-pilot.sh" "$@"
}

doctor_output="$(run_pilot doctor --base "$base_sha" --min-free-gib 0)"
[[ "$doctor_output" == doctor=ready* ]]

direct_worktree="$(run_pilot direct-prepare --base "$base_sha" --run-id arm-direct)"
[[ "$(git -C "$direct_worktree" rev-parse HEAD)" == "$base_sha" ]]
[[ -L "$direct_worktree/libs/skia" ]]

experiment_output="$(run_pilot experiment-init --id paired-1 --spec "$tmp_root/spec.json" --base "$base_sha")"
[[ "$experiment_output" == experiment_id=paired-1* ]]
printf 'binary\n' > "$tmp_root/out/Canvas"
printf '{"result":"pass"}\n' > "$tmp_root/out/evidence.json"
run_pilot experiment-record --id paired-1 --arm orchestrated --outcome blocked --tokens 293191 --elapsed-ms 996000 --commit "$base_sha" --task 12345678 > "$tmp_root/out/arm-a.out"
run_pilot experiment-record --id paired-1 --arm direct --outcome complete --tokens 653192 --elapsed-ms 2200000 --commit "$base_sha" --binary "$tmp_root/out/Canvas" --evidence "$tmp_root/out/evidence.json" > "$tmp_root/out/arm-b.out"
run_pilot experiment-report --id paired-1 > "$tmp_root/out/experiment-report.json"
grep -q '"codex_token_delta_direct_minus_orchestrated": 360001' "$tmp_root/out/experiment-report.json"

start_output="$(run_pilot start --spec "$tmp_root/spec.json")"
[[ "$start_output" == "task_id=12345678-1234-1234-1234-123456789abc" ]]

run_pilot revise 12345678 --spec "$tmp_root/spec.json" --reason "cover reviewer findings" > "$tmp_root/revise.out"
grep -q 'plan_review' "$tmp_root/revise.out"

run_pilot await 12345678 --timeout 2m > "$tmp_root/await.out"
grep -q 'verification_ready' "$tmp_root/await.out"

run_pilot goal-usage 12345678 --goal-objective "supervise canary" --goal-status complete --tokens-used 321 --elapsed-ms 654 > "$tmp_root/goal.out"
grep -q 'tokens_used=321' "$tmp_root/goal.out"

report_output="$(run_pilot report 12345678 --json --output "$tmp_root/out/report.json")"
[[ "$report_output" == "report=$tmp_root/out/report.json" ]]
grep -q '"tokens_in":12' "$tmp_root/out/report.json"

grep -q -- '--project canvas-local --json create --spec' "$tmp_root/args.log"
grep -q -- '--project canvas-local revise-plan 12345678 --spec' "$tmp_root/args.log"
grep -q -- '--project canvas-local follow 12345678 --until verification_ready,host_rework,integration_ready,done,failed,rejected,needs_clarification,cancelled --timeout 2m' "$tmp_root/args.log"
grep -q -- '--project canvas-local --json report 12345678' "$tmp_root/args.log"
grep -q -- '--project canvas-local codex-usage 12345678 --goal-objective supervise canary --goal-status complete --tokens-used 321 --elapsed-ms 654' "$tmp_root/args.log"

echo "drem-canvas-pilot tests passed"
