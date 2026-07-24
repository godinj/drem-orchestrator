#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

# Canvas-specific golden failure corpus. These are deliberately grouped by
# operational boundary so one failed command names the capability that
# regressed instead of burying it in the full Go suite.
go test ./internal/agent -count=1 -run 'TestRunDirectPlanReviewer_EmptyResponse|TestExecRead_DefaultsToCompleteBoundedScopedArtifact|TestCPlusPlusMutation|TestRunDirectToolAgent_BlocksOutOfScopeMutationAndPreservesAuthorizedCheckpoint|TestRunDirectToolAgent_CumulativeInputBudgetCheckpointsToolBatch|TestRunDirectToolAgentResumesAfterTimeoutWithoutRepeatingMutationTurn|TestSemanticLoopDetector'
go test ./internal/branchpolicy -count=1 -run 'TestAcceptCleanScope|TestValidateTestCheckpoint'
go test ./internal/orchestrator -count=1 -run 'TestCompileExecutionManifest|TestDispatchEvent_PreservesMutatedCheckpointInsteadOfRespawning|TestStandardParent_FailedChildDrainsAlreadyRunningSibling|TestMaterializedIntegrationContextRetainsReadScopeAndNarrowsWriteScope|TestFreezeDeliveryArtifactAtomicCAS|TestFailedVerificationRetainedAndArtifactInvalidated|TestComputerUseFailureHostReworkSubmissionRequiresFreshArtifact|TestRepeatedComputerUseTweaksReenterFreshArtifactCycle'
go test ./internal/orchhttp -count=1 -run 'TestTaskReportCountsResumedJournalUsageOnce'
go test ./internal/spawner -count=1 -run 'TestService_SpawnWorker_JournalMountIsWritableAndDeterministic|TestParseWorkerUsageIncludesContextCompactionAndResumeTelemetry'
"$repo_root/scripts/drem-canvas-worker-canary_test.sh"
"$repo_root/scripts/drem-container-disk_test.sh"

printf '%s\n' 'canvas orchestration regression corpus: PASS'
