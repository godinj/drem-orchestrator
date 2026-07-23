package orchestrator

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func completeHostAttestation() HostDirectAttestation {
	return HostDirectAttestation{
		AcceptanceCriteriaUnchanged: true,
		DependencyShapeUnchanged:    true,
		NoPersistenceOrSchema:       true,
		NoSecurityOrAuth:            true,
		NoCrossProcessOwnership:     true,
		NoBuildOrReleasePolicy:      true,
	}
}

func seedAcceptanceCriterion(t *testing.T, orch *Orchestrator, task model.Task, criterionKey string) {
	t.Helper()
	specification := model.TaskSpecification{
		ID: uuid.New(), TaskID: task.ID, ProjectID: task.ProjectID,
		ObservationSessionID: "cubase-session", Product: "Cubase Pro", ProductVersion: "15",
		OperatingSystem: "Windows 11", DisplayEnvironment: "1920x1080",
		ObservedAt: time.Now(), ObserverActor: "codex:observer", CreatorActor: "codex:creator",
		IdempotencyKey: uuid.NewString(), RequestHash: strings.Repeat("a", 64),
		SpecFingerprint: strings.Repeat("b", 64), SpecJSON: `{}`,
	}
	require.NoError(t, orch.db.Create(&specification).Error)
	require.NoError(t, orch.db.Create(&model.TaskAcceptanceCriterion{
		ID: uuid.New(), SpecificationID: specification.ID, TaskID: task.ID,
		CriterionKey: criterionKey, Position: 0, Description: "Range selection is visible",
		VerificationStepsJSON: `[]`, ExpectedBehaviorJSON: `[]`, NegativeBehaviorJSON: `[]`,
	}).Error)
}

func failedInteraction(binarySHA string) VerificationInteractionEvidence {
	return VerificationInteractionEvidence{
		AcceptanceCriterionKey: "range-selection",
		ScenarioName:           "drag lower take",
		Steps: []InteractionStep{{
			Action: "Drag across take lane 2", Observed: "The entire take was promoted",
		}},
		ObservedResult: "The complete take changed instead of the selected range.",
		EvidenceRefs: []InteractionEvidenceRef{{
			ArtifactID: "canvas-failed-range-selection", SHA256: strings.Repeat("c", 64), MediaType: "image/png",
		}},
		ApplicationVersion: "Canvas 0.1.0", HostEnvironment: "macOS-arm64-15.5",
		RunPID: 4242, Result: model.VerificationFailed, Discrepancy: "Selection boundaries were ignored.",
	}
}

func TestComputerUseFailureHostReworkSubmissionRequiresFreshArtifact(t *testing.T) {
	orch, task, snapshot := deliveryFixture(t)
	persistAcceptedSnapshot(t, orch, task, snapshot)
	seedAcceptanceCriterion(t, orch, task, "range-selection")
	artifact1, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
	require.NoError(t, err)
	var current model.Task
	require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
	now := time.Now()
	binary1 := strings.Repeat("d", 64)
	verification1, err := orch.VerifyDelivery(VerifyDeliveryRequest{
		TaskID: task.ID, ObservedStateVersion: current.StateVersion,
		ArtifactVersion: artifact1.ArtifactVersion, CommitSHA: artifact1.CommitSHA,
		Actor: "codex:canvas-verifier", Source: "test", EnvironmentFingerprint: "macOS-arm64-15.5",
		CommandEvidence: []CommandEvidence{{Command: "scripts/dev build", Passed: false, ExitCode: 1, StartedAt: now, FinishedAt: now}},
		BinarySHA256:    binary1, Result: model.VerificationFailed, Interactions: []VerificationInteractionEvidence{failedInteraction(binary1)},
		FailureMode: model.DeliveryReworkHostDirect, FailureReason: "bounded range highlight mismatch",
		AllowedScope: []string{"delivery.txt"}, HostDirectAttestation: completeHostAttestation(),
		IdempotencyKey: "verify-failed-host-direct",
	})
	require.NoError(t, err)
	require.Equal(t, model.VerificationFailed, verification1.Result)
	require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusHostRework, current.Status)

	var interaction model.VerificationInteraction
	require.NoError(t, orch.db.First(&interaction, "verification_record_id = ?", verification1.ID).Error)
	require.Equal(t, "range-selection", interaction.AcceptanceCriterionKey)
	require.Equal(t, binary1, interaction.BinarySHA256)
	var session model.HostReworkSession
	require.NoError(t, orch.db.First(&session, "task_id = ? AND disposition = ?", task.ID, model.HostReworkActive).Error)
	require.Equal(t, "codex:canvas-verifier", session.OwnerActor)
	require.Equal(t, artifact1.CommitSHA, session.PriorCommitSHA)

	featureDir := orch.resolveIntegrationWorktree(&task)
	writeFile(t, featureDir, "delivery.txt", "bounded visual correction")
	runGitCmd(t, featureDir, "add", "delivery.txt")
	runGitCmd(t, featureDir, "commit", "-m", "fix range selection highlight")
	replacementSHA := runGitCmd(t, featureDir, "rev-parse", "HEAD")
	submitReq := SubmitHostReworkRequest{
		TaskID: task.ID, ObservedStateVersion: current.StateVersion, SessionID: session.ID,
		CommitSHA: replacementSHA, Actor: session.OwnerActor, Source: "test", IdempotencyKey: "submit-host-rework-1",
	}
	submission, err := orch.SubmitHostRework(submitReq)
	require.NoError(t, err)
	replayed, err := orch.SubmitHostRework(submitReq)
	require.NoError(t, err)
	require.Equal(t, submission.ID, replayed.ID)
	require.Equal(t, model.JSONArray{"delivery.txt"}, submission.ChangedPaths)
	require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusTestingReady, current.Status)
	require.NoError(t, orch.db.First(&session, "id = ?", session.ID).Error)
	require.Equal(t, model.HostReworkSubmitted, session.Disposition)
	require.Equal(t, session.OwnerActor, session.TerminalActor)
	require.NotNil(t, session.TerminalIdempotencyKey)
	require.Equal(t, submitReq.IdempotencyKey, *session.TerminalIdempotencyKey)
	candidate, err := orch.acceptedDeliveryCandidate(&current)
	require.NoError(t, err)
	require.Equal(t, replacementSHA, candidate.CommitSHA)
	require.Equal(t, snapshot.BaseSHA, candidate.BaseSHA)

	snapshot.CommitSHA = replacementSHA
	snapshot.GateWorkspaceID = "fresh-gate-workspace"
	artifact2, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
	require.NoError(t, err)
	require.Equal(t, uint64(2), artifact2.ArtifactVersion)
	require.NotEqual(t, artifact1.ID, artifact2.ID)
	require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
	passing := failedInteraction(strings.Repeat("e", 64))
	passing.Result = model.VerificationPassed
	passing.Discrepancy = ""
	passing.ObservedResult = "Only the selected range was promoted."
	passing.Steps[0].Observed = "The selected range became active."
	verification2, err := orch.VerifyDelivery(VerifyDeliveryRequest{
		TaskID: task.ID, ObservedStateVersion: current.StateVersion,
		ArtifactVersion: artifact2.ArtifactVersion, CommitSHA: artifact2.CommitSHA,
		Actor: "codex:canvas-verifier", Source: "test", EnvironmentFingerprint: "macOS-arm64-15.5",
		CommandEvidence: snapshot.PreliminaryEvidence, BinarySHA256: strings.Repeat("e", 64),
		Result: model.VerificationPassed, Interactions: []VerificationInteractionEvidence{passing},
		IdempotencyKey: "verify-passed-replacement",
	})
	require.NoError(t, err)
	require.NotEqual(t, verification1.ID, verification2.ID)
	require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusIntegrationReady, current.Status)

	var artifacts, verifications, interactions int64
	require.NoError(t, orch.db.Model(&model.DeliveryArtifact{}).Where("task_id = ?", task.ID).Count(&artifacts).Error)
	require.NoError(t, orch.db.Model(&model.VerificationRecord{}).Where("task_id = ?", task.ID).Count(&verifications).Error)
	require.NoError(t, orch.db.Model(&model.VerificationInteraction{}).Where("task_id = ?", task.ID).Count(&interactions).Error)
	require.EqualValues(t, 2, artifacts)
	require.EqualValues(t, 2, verifications)
	require.EqualValues(t, 2, interactions)
}

func TestRepeatedComputerUseTweaksReenterFreshArtifactCycle(t *testing.T) {
	orch, task, snapshot := deliveryFixture(t)
	seedAcceptanceCriterion(t, orch, task, "range-selection")
	artifact, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
	require.NoError(t, err)
	owner := "codex:canvas-canary:multi-tweak"
	now := time.Now()

	for cycle := 1; cycle <= 2; cycle++ {
		cycleID := strconv.Itoa(cycle)
		var current model.Task
		require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
		binarySHA := strings.Repeat(string(rune('a'+cycle)), 64)
		interaction := failedInteraction(binarySHA)
		interaction.Discrepancy = "bounded mismatch cycle " + cycleID
		_, err := orch.VerifyDelivery(VerifyDeliveryRequest{
			TaskID: task.ID, ObservedStateVersion: current.StateVersion,
			ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
			Actor: owner, Source: "multi-tweak-canary", EnvironmentFingerprint: "macOS-arm64-canary",
			CommandEvidence: []CommandEvidence{{Command: "scripts/dev build", Passed: true, ExitCode: 0, StartedAt: now, FinishedAt: now}},
			BinarySHA256:    binarySHA, Result: model.VerificationFailed,
			Interactions: []VerificationInteractionEvidence{interaction},
			FailureMode:  model.DeliveryReworkHostDirect, FailureReason: interaction.Discrepancy,
			AllowedScope: []string{"delivery.txt"}, HostDirectAttestation: completeHostAttestation(),
			IdempotencyKey: "multi-tweak-fail-" + cycleID,
		})
		require.NoError(t, err)

		var session model.HostReworkSession
		require.NoError(t, orch.db.Where("task_id = ? AND disposition = ?", task.ID, model.HostReworkActive).First(&session).Error)
		require.Equal(t, owner, session.OwnerActor)
		featureDir := orch.resolveIntegrationWorktree(&task)
		writeFile(t, featureDir, "delivery.txt", "bounded correction cycle "+cycleID)
		runGitCmd(t, featureDir, "add", "delivery.txt")
		runGitCmd(t, featureDir, "commit", "-m", "bounded correction cycle "+cycleID)
		replacementSHA := runGitCmd(t, featureDir, "rev-parse", "HEAD")
		require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
		_, err = orch.SubmitHostRework(SubmitHostReworkRequest{
			TaskID: task.ID, ObservedStateVersion: current.StateVersion, SessionID: session.ID,
			CommitSHA: replacementSHA, Actor: owner, Source: "multi-tweak-canary",
			IdempotencyKey: "multi-tweak-submit-" + cycleID,
		})
		require.NoError(t, err)
		snapshot.CommitSHA = replacementSHA
		snapshot.GateWorkspaceID = "multi-tweak-gate-" + cycleID
		artifact, err = orch.FreezeDeliveryArtifact(task.ID, snapshot)
		require.NoError(t, err)
		require.Equal(t, uint64(cycle+1), artifact.ArtifactVersion)
	}

	var current model.Task
	require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
	passing := failedInteraction(strings.Repeat("f", 64))
	passing.Result = model.VerificationPassed
	passing.Discrepancy = ""
	passing.ObservedResult = "The second bounded correction satisfies the criterion."
	_, err = orch.VerifyDelivery(VerifyDeliveryRequest{
		TaskID: task.ID, ObservedStateVersion: current.StateVersion,
		ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
		Actor: owner, Source: "multi-tweak-canary", EnvironmentFingerprint: "macOS-arm64-canary",
		CommandEvidence: snapshot.PreliminaryEvidence, BinarySHA256: strings.Repeat("f", 64),
		Result: model.VerificationPassed, Interactions: []VerificationInteractionEvidence{passing},
		IdempotencyKey: "multi-tweak-pass",
	})
	require.NoError(t, err)
	require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusIntegrationReady, current.Status)

	var artifacts, verifications, interactions, sessions, submissions, activeAttempts int64
	require.NoError(t, orch.db.Model(&model.DeliveryArtifact{}).Where("task_id = ?", task.ID).Count(&artifacts).Error)
	require.NoError(t, orch.db.Model(&model.VerificationRecord{}).Where("task_id = ?", task.ID).Count(&verifications).Error)
	require.NoError(t, orch.db.Model(&model.VerificationInteraction{}).Where("task_id = ?", task.ID).Count(&interactions).Error)
	require.NoError(t, orch.db.Model(&model.HostReworkSession{}).Where("task_id = ?", task.ID).Count(&sessions).Error)
	require.NoError(t, orch.db.Model(&model.HostReworkSubmission{}).Where("task_id = ?", task.ID).Count(&submissions).Error)
	require.NoError(t, orch.db.Model(&model.WorkerAttempt{}).Where("task_id = ? AND state IN ?", task.ID, []string{model.WorkerAttemptReserved, model.WorkerAttemptRunning}).Count(&activeAttempts).Error)
	require.EqualValues(t, 3, artifacts)
	require.EqualValues(t, 3, verifications)
	require.EqualValues(t, 3, interactions)
	require.EqualValues(t, 2, sessions)
	require.EqualValues(t, 2, submissions)
	require.Zero(t, activeAttempts)
}

func TestHostDirectReworkRefusesActiveWorkerAndOutOfScopeSubmission(t *testing.T) {
	t.Run("active worker", func(t *testing.T) {
		orch, task, snapshot := deliveryFixture(t)
		artifact, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
		require.NoError(t, err)
		attempt := model.WorkerAttempt{ID: uuid.New(), TaskID: task.ID, State: model.WorkerAttemptRunning, Branch: snapshot.Branch}
		require.NoError(t, orch.db.Create(&attempt).Error)
		var current model.Task
		require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
		_, err = orch.RequestDeliveryRework(RequestDeliveryReworkRequest{
			TaskID: task.ID, ObservedStateVersion: current.StateVersion,
			ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
			Actor: "codex:owner", Source: "test", Reason: "bounded correction",
			Mode: model.DeliveryReworkHostDirect, AllowedScope: []string{"delivery.txt"},
			HostDirectAttestation: completeHostAttestation(), IdempotencyKey: "active-worker-refusal",
		})
		require.ErrorContains(t, err, "active worker attempt")
		require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
		require.Equal(t, model.StatusVerificationReady, current.Status)
	})

	t.Run("out of scope", func(t *testing.T) {
		orch, task, snapshot := deliveryFixture(t)
		artifact, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
		require.NoError(t, err)
		var current model.Task
		require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
		record, err := orch.RequestDeliveryRework(RequestDeliveryReworkRequest{
			TaskID: task.ID, ObservedStateVersion: current.StateVersion,
			ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
			Actor: "codex:owner", Source: "test", Reason: "bounded correction",
			Mode: model.DeliveryReworkHostDirect, AllowedScope: []string{"src/ui"},
			HostDirectAttestation: completeHostAttestation(), IdempotencyKey: "out-of-scope-start",
		})
		require.NoError(t, err)
		require.NotNil(t, record.HostReworkSessionID)
		featureDir := orch.resolveIntegrationWorktree(&task)
		writeFile(t, featureDir, "delivery.txt", "out of scope correction")
		runGitCmd(t, featureDir, "add", "delivery.txt")
		runGitCmd(t, featureDir, "commit", "-m", "out of scope")
		replacementSHA := runGitCmd(t, featureDir, "rev-parse", "HEAD")
		require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
		_, err = orch.SubmitHostRework(SubmitHostReworkRequest{
			TaskID: task.ID, ObservedStateVersion: current.StateVersion, SessionID: *record.HostReworkSessionID,
			CommitSHA: replacementSHA, Actor: "codex:owner", Source: "test", IdempotencyKey: "out-of-scope-submit",
		})
		require.ErrorContains(t, err, "outside the allowed scope")
	})
}

func TestHostReworkOwnerCanReturnSessionToOrchestratedImplementation(t *testing.T) {
	orch, task, snapshot := deliveryFixture(t)
	artifact, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
	require.NoError(t, err)
	var current model.Task
	require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
	record, err := orch.RequestDeliveryRework(RequestDeliveryReworkRequest{
		TaskID: task.ID, ObservedStateVersion: current.StateVersion,
		ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
		Actor: "codex:owner", Source: "test", Reason: "bounded correction",
		Mode: model.DeliveryReworkHostDirect, AllowedScope: []string{"delivery.txt"},
		HostDirectAttestation: completeHostAttestation(), IdempotencyKey: "abandon-start",
	})
	require.NoError(t, err)
	require.NotNil(t, record.HostReworkSessionID)
	require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)

	request := AbandonHostReworkRequest{
		TaskID: task.ID, ObservedStateVersion: current.StateVersion, SessionID: *record.HostReworkSessionID,
		Actor: "codex:owner", Source: "test", Reason: "repair is architectural", IdempotencyKey: "abandon-terminal",
	}
	session, err := orch.AbandonHostRework(request)
	require.NoError(t, err)
	replayed, err := orch.AbandonHostRework(request)
	require.NoError(t, err)
	require.Equal(t, session.ID, replayed.ID)
	require.Equal(t, model.HostReworkOrchestrated, session.Disposition)
	require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusInProgress, current.Status)
}
