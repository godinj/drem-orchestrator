#!/usr/bin/env bash
# Build locally attested Drem images and optionally publish them to the local
# registry. Dirty source is identified by a content digest rather than being
# mislabeled as the clean HEAD commit.

set -euo pipefail

publish=1
images=(orch classifier planner spawner agentmon merger worker-cpp)
for arg in "$@"; do
    case "$arg" in
        --publish) publish=1 ;;
        --images=*) IFS=',' read -r -a images <<< "${arg#--images=}" ;;
        -h|--help) sed -n '1,18p' "$0"; exit 0 ;;
        *) echo "unknown argument: $arg" >&2; exit 2 ;;
    esac
done

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repo_root="$(cd "${script_dir}/.." && pwd -P)"
cd "$repo_root"

revision="$(git rev-parse HEAD)"
source_state="$revision"
if [[ -n "$(git status --porcelain=v1 --untracked-files=all)" ]]; then
    digest="$({ git diff --binary HEAD; git ls-files --others --exclude-standard -z | sort -z | xargs -0 shasum -a 256 2>/dev/null || true; } | shasum -a 256 | awk '{print $1}')"
    source_state="${revision}-dirty-${digest}"
fi

dockerfile_for() {
    case "$1" in
        worker-base|worker-cpp) printf 'deploy/docker/%s.Dockerfile' "$1" ;;
        *) printf 'deploy/docker/%s.Dockerfile' "$1" ;;
    esac
}

workers_built=0
for image in "${images[@]}"; do
    if [[ "$image" == worker-base || "$image" == worker-go || "$image" == worker-cpp ]]; then
        if [[ "$workers_built" -eq 0 ]]; then
            DREM_SOURCE_REVISION="$revision" DREM_SOURCE_STATE="$source_state" bash deploy/docker/build-workers.sh
            workers_built=1
        fi
        continue
    fi
    dockerfile="$(dockerfile_for "$image")"
    [[ -f "$dockerfile" ]] || { echo "missing Dockerfile: $dockerfile" >&2; exit 1; }
    tag="localhost:5000/drem-${image}"
    docker build \
        --label "org.opencontainers.image.revision=${revision}" \
        --label "io.drem.source-state=${source_state}" \
        --build-arg "DREM_SOURCE_REVISION=${revision}" \
        -f "$dockerfile" \
        -t "${tag}:${source_state}" \
        -t "${tag}:latest" .
    actual="$(docker image inspect "${tag}:latest" --format '{{ index .Config.Labels "io.drem.source-state" }}')"
    [[ "$actual" == "$source_state" ]] || { echo "attestation mismatch for $tag: $actual" >&2; exit 1; }
    if [[ "$publish" -eq 1 ]]; then
        docker push "${tag}:${source_state}"
        docker push "${tag}:latest"
    fi
done

printf 'source_revision=%s\nsource_state=%s\n' "$revision" "$source_state"
