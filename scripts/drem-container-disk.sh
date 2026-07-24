#!/usr/bin/env bash
# Conservative local Docker/Colima capacity audit and cleanup helper.
#
# Audit is the default and never changes Docker or Colima. Every mutating mode
# has a separate, exact confirmation flag and first refuses if Drem workloads
# or task-labelled containers are active. It never stops, restarts, removes,
# or otherwise touches drem-sglang.
set -euo pipefail

docker_bin="${DREM_DOCKER_BIN:-docker}"
colima_bin="${DREM_COLIMA_BIN:-colima}"
colima_home="${DREM_COLIMA_HOME:-$HOME/.colima}"
min_free_gib="${DREM_CONTAINER_DISK_MIN_FREE_GIB:-20}"
sparse_warn_gib="${DREM_COLIMA_SPARSE_WARN_GIB:-80}"

usage() {
    cat <<'EOF'
usage: drem-container-disk [audit|cleanup-unused|registry-prune|colima-trim|colima-recreate] [confirmation]

  audit (default)
      Read-only host and Colima sparse-disk report, Docker system df, active
      workload awareness, and capacity recommendations. Never changes state.

  cleanup-unused --confirm-unused-prune
      Prune only stopped containers, dangling images, unused networks, and
      unused build cache. Refuses while Drem orchestration/inference or any
      task-labelled container is active. Never touches volumes or drem-sglang.

  registry-prune --confirm-registry-prune
      Separate registry-GC guard. It never performs registry garbage
      collection automatically: registry GC needs registry-specific retention,
      backups, and downtime approval. This command checks safety then prints
      the required manual boundary instead of deleting registry data.

  colima-trim --confirm-colima-trim
      Explicitly runs guest fstrim only after the workload guard passes. It
      can reclaim sparse-disk blocks but may take time; it does not recreate
      the VM.

  colima-recreate
      Always refuses automation. Recreating a VM destroys local images,
      containers, and volumes; follow the documented manual recovery process.

Environment: DREM_COLIMA_HOME, DREM_CONTAINER_DISK_MIN_FREE_GIB (default 20),
DREM_COLIMA_SPARSE_WARN_GIB (default 80), DREM_DOCKER_BIN, DREM_COLIMA_BIN.
EOF
}

host_free_kib() {
    df -Pk "$HOME" | awk 'NR == 2 { print $4 }'
}

report_sparse_disk() {
    local candidate allocated_kib logical_bytes warn_kib
    warn_kib=$((sparse_warn_gib * 1024 * 1024))
    local -a candidates=(
        "$colima_home/_lima/_disks/colima/datadisk"
        "$colima_home/_lima/colima/disk"
        "$colima_home/default/diffdisk"
        "$colima_home/default/data.qcow2"
    )
    for candidate in "${candidates[@]}"; do
        # A Colima profile directory is not a sparse disk. Only inspect a
        # regular disk image so wc/du never turns an audit into an error.
        [[ -f "$candidate" ]] || continue
        allocated_kib="$(du -sk "$candidate" | awk '{print $1}')"
        logical_bytes="$(wc -c < "$candidate" | tr -d ' ')"
        printf 'colima_sparse path=%s allocated_kib=%s logical_bytes=%s threshold_kib=%s\n' \
            "$candidate" "$allocated_kib" "$logical_bytes" "$warn_kib"
        if [[ "$allocated_kib" =~ ^[0-9]+$ ]] && (( allocated_kib >= warn_kib )); then
            printf 'recommendation=colima_sparse_disk_above_threshold action="review audit; use colima-trim only after workloads stop"\n'
        fi
    done
}

docker_available() {
    command -v "$docker_bin" >/dev/null 2>&1 && "$docker_bin" info >/dev/null 2>&1
}

active_workloads() {
    "$docker_bin" ps --format '{{.Names}}\t{{.Status}}'
}

unsafe_workloads() {
    local workloads name status
    workloads="$(active_workloads)"
    while IFS=$'\t' read -r name status; do
        [[ -n "$name" ]] || continue
        # Any active Drem service is conservatively protected. This includes
        # orchestration, workers, inference, and task containers. Do not make
        # an exception for sglang: this helper must never touch it.
        if [[ "$name" =~ (^|[-_.])drem([-_.]|$) ]] || [[ "$name" =~ (sglang|gq|orchestrator|global-spawner|csuite|worker) ]]; then
            printf 'unsafe_workload name=%s status=%s\n' "$name" "$status" >&2
            return 0
        fi
    done <<< "$workloads"
    if "$docker_bin" ps --filter 'label=drem.task_id' --format '{{.Names}}' | grep -q '.'; then
        printf 'unsafe_workload reason=active_task_labelled_container\n' >&2
        return 0
    fi
    return 1
}

require_safe_cleanup_window() {
    docker_available || { printf 'cleanup_refused reason=docker_unavailable\n' >&2; return 1; }
    if unsafe_workloads; then
        printf 'cleanup_refused reason=protected_or_active_drem_workload\n' >&2
        return 1
    fi
}

audit() {
    local free_kib required_kib
    free_kib="$(host_free_kib)"
    required_kib=$((min_free_gib * 1024 * 1024))
    printf 'audit=read_only host_free_kib=%s min_free_kib=%s colima_home=%s\n' "$free_kib" "$required_kib" "$colima_home"
    if [[ "$free_kib" =~ ^[0-9]+$ ]] && (( free_kib < required_kib )); then
        printf 'recommendation=host_free_space_below_threshold action="stop workloads, inspect Docker/Colima usage, then choose an explicit cleanup mode"\n'
    fi
    report_sparse_disk
    if ! docker_available; then
        printf 'docker=unavailable recommendation="install/start Docker or Colima, then rerun audit"\n'
        return 0
    fi
    printf 'docker_system_df_begin\n'
    "$docker_bin" system df || printf 'docker_system_df=unavailable\n'
    printf 'docker_system_df_end\n'
    printf 'active_workloads_begin\n'
    active_workloads || true
    printf 'active_workloads_end\n'
    if unsafe_workloads; then
        printf 'cleanup_status=blocked recommendation="do not prune while Drem orchestration, inference, or task containers are active"\n'
    else
        printf 'cleanup_status=eligible recommendation="if capacity is needed, run cleanup-unused --confirm-unused-prune"\n'
    fi
}

cleanup_unused() {
    [[ "${1:-}" == "--confirm-unused-prune" ]] || { printf 'cleanup_refused reason=missing_exact_confirmation\n' >&2; return 2; }
    require_safe_cleanup_window
    printf 'cleanup=started scope=stopped_containers,dangling_images,unused_networks,unused_build_cache\n'
    "$docker_bin" container prune --force
    "$docker_bin" image prune --force
    "$docker_bin" network prune --force
    "$docker_bin" builder prune --force
    printf 'cleanup=completed scope=provably_unused_docker_objects\n'
}

registry_prune() {
    [[ "${1:-}" == "--confirm-registry-prune" ]] || { printf 'registry_cleanup_refused reason=missing_exact_confirmation\n' >&2; return 2; }
    require_safe_cleanup_window
    printf 'registry_cleanup=manual_boundary_required reason="registry GC can delete blobs beyond Docker unused-object proofs; create a backup and use the registry-specific retention workflow"\n'
}

colima_trim() {
    [[ "${1:-}" == "--confirm-colima-trim" ]] || { printf 'colima_trim_refused reason=missing_exact_confirmation\n' >&2; return 2; }
    require_safe_cleanup_window
    command -v "$colima_bin" >/dev/null 2>&1 || { printf 'colima_trim_refused reason=colima_unavailable\n' >&2; return 1; }
    printf 'colima_trim=started consequence="guest fstrim may take time; VM is not recreated"\n'
    "$colima_bin" ssh -- sudo fstrim -av
    printf 'colima_trim=completed\n'
}

main() {
    local mode="${1:-audit}"
    shift || true
    case "$mode" in
        audit) [[ $# -eq 0 ]] || { usage >&2; return 2; }; audit ;;
        cleanup-unused) cleanup_unused "$@" ;;
        registry-prune) registry_prune "$@" ;;
        colima-trim) colima_trim "$@" ;;
        colima-recreate)
            printf 'colima_recreate_refused consequence="manual recreation destroys local Docker images, containers, and volumes; no automated recreate exists"\n' >&2
            return 2
            ;;
        help|-h|--help) usage ;;
        *) usage >&2; return 2 ;;
    esac
}

main "$@"
