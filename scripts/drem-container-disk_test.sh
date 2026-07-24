#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
tmp_root="$(mktemp -d)"
trap 'rm -rf "$tmp_root"' EXIT
mkdir -p "$tmp_root/bin" "$tmp_root/home/.colima/_lima/_disks/colima"
: > "$tmp_root/home/.colima/_lima/_disks/colima/datadisk"
: > "$tmp_root/docker.log"

cat > "$tmp_root/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$DREM_TEST_DOCKER_LOG"
case "${1:-}" in
  info) exit 0 ;;
  system) printf 'TYPE TOTAL ACTIVE SIZE RECLAIMABLE\nImages 3 0 10GB 9GB\n' ;;
  ps)
    if [[ "$*" == *'label=drem.task_id'* ]]; then
      [[ "${DREM_FAKE_ACTIVE:-}" == "task" ]] && printf 'task-worker-1\n'
      exit 0
    fi
    if [[ "${DREM_FAKE_ACTIVE:-}" == "protected" ]]; then printf 'drem-sglang-1\tUp 2 hours\n'; fi
    ;;
  container|image|network|builder) printf 'pruned %s\n' "$1" ;;
  *) exit 7 ;;
esac
EOF
chmod +x "$tmp_root/bin/docker"

cat > "$tmp_root/bin/colima" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$DREM_TEST_COLIMA_LOG"
EOF
chmod +x "$tmp_root/bin/colima"

run_helper() {
    HOME="$tmp_root/home" PATH="$tmp_root/bin:$PATH" DREM_TEST_DOCKER_LOG="$tmp_root/docker.log" DREM_TEST_COLIMA_LOG="$tmp_root/colima.log" DREM_CONTAINER_DISK_MIN_FREE_GIB=0 DREM_COLIMA_HOME="$tmp_root/home/.colima" DREM_FAKE_ACTIVE="${DREM_FAKE_ACTIVE:-}" \
        "$script_dir/drem-container-disk.sh" "$@"
}

audit_output="$(run_helper)"
[[ "$audit_output" == *'audit=read_only'* ]]
[[ "$audit_output" == *'/_lima/_disks/colima/datadisk'* ]]
[[ "$audit_output" == *'docker_system_df_begin'* ]]
[[ "$audit_output" == *'cleanup_status=eligible'* ]]
! grep -Eq ' (container|image|network|builder) prune ' "$tmp_root/docker.log"

if run_helper cleanup-unused >/dev/null 2>&1; then
    echo 'cleanup unexpectedly proceeded without confirmation' >&2
    exit 1
fi
run_helper cleanup-unused --confirm-unused-prune >/dev/null
grep -q '^container prune --force$' "$tmp_root/docker.log"
grep -q '^image prune --force$' "$tmp_root/docker.log"
grep -q '^network prune --force$' "$tmp_root/docker.log"
grep -q '^builder prune --force$' "$tmp_root/docker.log"
! grep -q '^volume prune' "$tmp_root/docker.log"

DREM_FAKE_ACTIVE=protected
export DREM_FAKE_ACTIVE
if run_helper cleanup-unused --confirm-unused-prune >/dev/null 2>&1; then
    echo 'cleanup unexpectedly proceeded with protected inference workload' >&2
    exit 1
fi
! grep -q '^container prune --force$' <(tail -n 4 "$tmp_root/docker.log")

DREM_FAKE_ACTIVE=task
if run_helper cleanup-unused --confirm-unused-prune >/dev/null 2>&1; then
    echo 'cleanup unexpectedly proceeded with active task-labelled container' >&2
    exit 1
fi
unset DREM_FAKE_ACTIVE

registry_output="$(run_helper registry-prune --confirm-registry-prune)"
[[ "$registry_output" == *'registry_cleanup=manual_boundary_required'* ]]
! grep -q '^image prune -a' "$tmp_root/docker.log"

run_helper colima-trim --confirm-colima-trim >/dev/null
grep -q '^ssh -- sudo fstrim -av$' "$tmp_root/colima.log"
if run_helper colima-recreate >/dev/null 2>&1; then
    echo 'colima recreate unexpectedly automated' >&2
    exit 1
fi

grep -q 'cleanup-unused --confirm-unused-prune' "$script_dir/drem-container-disk.sh"
grep -q 'drem-sglang' "$script_dir/drem-container-disk.sh"
grep -q 'label=drem.task_id' "$script_dir/drem-container-disk.sh"
