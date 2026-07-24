#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
target="$script_dir/drem-canvas-orchestration-regressions.sh"

test -x "$target"
for required in \
  EmptyResponse \
  CompleteBoundedScopedArtifact \
  CPlusPlusMutation \
  OutOfScopeMutation \
  CumulativeInputBudget \
  ResumesAfterTimeout \
  SemanticLoopDetector \
  PreservesMutatedCheckpoint \
  FailedChildDrainsAlreadyRunningSibling \
  IntegrationContext \
  FreezeDeliveryArtifact \
  FailedVerification \
  ComputerUseFailure \
  CountsResumedJournalUsageOnce \
  drem-container-disk_test \
  worker-canary; do
  grep -q "$required" "$target"
done

printf '%s\n' 'canvas orchestration regression corpus contract: PASS'
