package orchestrator

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
)

var (
	ErrStaleArtifact       = errors.New("stale delivery artifact")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different request")
)

// CommandEvidence is one structured command result attached to a delivery or
// verification record. Output is bounded by the caller before persistence.
type CommandEvidence struct {
	Command    string    `json:"command"`
	Passed     bool      `json:"passed"`
	ExitCode   int       `json:"exit_code"`
	Output     string    `json:"output,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

type InteractionStep struct {
	Action   string `json:"action"`
	Observed string `json:"observed"`
}

type InteractionEvidenceRef struct {
	ArtifactID string `json:"artifact_id"`
	SHA256     string `json:"sha256"`
	MediaType  string `json:"media_type"`
}

type VerificationInteractionEvidence struct {
	AcceptanceCriterionKey string                   `json:"acceptance_criterion_id"`
	ScenarioName           string                   `json:"scenario_name"`
	Steps                  []InteractionStep        `json:"steps"`
	ObservedResult         string                   `json:"observed_result"`
	EvidenceRefs           []InteractionEvidenceRef `json:"evidence_refs"`
	ApplicationVersion     string                   `json:"application_version"`
	HostEnvironment        string                   `json:"host_environment"`
	RunPID                 int                      `json:"run_pid"`
	Result                 model.VerificationResult `json:"result"`
	Discrepancy            string                   `json:"discrepancy,omitempty"`
}

type HostDirectAttestation struct {
	AcceptanceCriteriaUnchanged bool `json:"acceptance_criteria_unchanged"`
	DependencyShapeUnchanged    bool `json:"dependency_shape_unchanged"`
	NoPersistenceOrSchema       bool `json:"no_persistence_or_schema"`
	NoSecurityOrAuth            bool `json:"no_security_or_auth"`
	NoCrossProcessOwnership     bool `json:"no_cross_process_ownership"`
	NoBuildOrReleasePolicy      bool `json:"no_build_or_release_policy"`
}

// ArtifactSnapshot is resolved from Git before the database transaction. The
// exact object IDs, not the branch names, are the verification contract.
type ArtifactSnapshot struct {
	Branch                 string
	CommitSHA              string
	BaseBranch             string
	BaseSHA                string
	GateWorkspaceID        string
	EnvironmentFingerprint string
	PreliminaryEvidence    []CommandEvidence
	Actor                  string
	Source                 string
}

type VerifyDeliveryRequest struct {
	TaskID                 uuid.UUID
	ObservedStateVersion   uint64
	ArtifactVersion        uint64
	CommitSHA              string
	Actor                  string
	Source                 string
	EnvironmentFingerprint string
	CommandEvidence        []CommandEvidence
	BinarySHA256           string
	Result                 model.VerificationResult
	Notes                  string
	Interactions           []VerificationInteractionEvidence
	FailureMode            model.DeliveryReworkMode
	FailureReason          string
	AllowedScope           []string
	HostDirectAttestation  HostDirectAttestation
	IdempotencyKey         string
}

type IntegrateDeliveryRequest struct {
	TaskID               uuid.UUID
	ObservedStateVersion uint64
	ArtifactVersion      uint64
	CommitSHA            string
	VerificationRecordID uuid.UUID
	Actor                string
	Source               string
	IdempotencyKey       string
}

type RequestDeliveryReworkRequest struct {
	TaskID                uuid.UUID
	ObservedStateVersion  uint64
	ArtifactVersion       uint64
	CommitSHA             string
	Actor                 string
	Source                string
	Reason                string
	Mode                  model.DeliveryReworkMode
	AllowedScope          []string
	HostDirectAttestation HostDirectAttestation
	IdempotencyKey        string
}

func stageOrchestratedDeliveryRework(tx *gorm.DB, task *model.Task, reason string) error {
	if task.Context == nil {
		task.Context = model.JSONField{}
	}
	task.Context["prompt_adjustment"] = strings.TrimSpace(reason)
	if task.Category.IsQuickFix() {
		task.Context[quickFixDeliveryReworkPendingKey] = true
		task.AssignedAgentID = nil
		if err := tx.Model(task).Select("context", "assigned_agent_id").Updates(task).Error; err != nil {
			return fmt.Errorf("stage orchestrated delivery rework: %w", err)
		}
		return nil
	}

	// A standard task cannot be returned to in_progress with every planned
	// child still terminal: checkFeatureCompletion would immediately freeze a
	// new artifact at the same commit. Reopen the latest integration worker (or
	// implementation worker when no integration child exists) and force it to
	// consume the host failure before another artifact can be produced.
	var repair model.Task
	err := tx.Where("parent_task_id = ? AND status = ? AND phase IN ?", task.ID, model.StatusDone,
		[]string{"integration", "implementation"}).
		Order("CASE phase WHEN 'integration' THEN 0 ELSE 1 END, updated_at DESC").
		First(&repair).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errors.New("stage orchestrated delivery rework: no completed integration or implementation subtask is available for repair")
	}
	if err != nil {
		return fmt.Errorf("stage orchestrated delivery rework: find repair subtask: %w", err)
	}
	if repair.Context == nil {
		repair.Context = model.JSONField{}
	}
	repair.Context["prompt_adjustment"] = strings.TrimSpace(reason)
	repair.Context["delivery_rework_pending"] = true
	repair.Context["skip_existing_work_dedup"] = true
	repair.Context["skip_existing_work_dedup_reason"] = "delivery_verification_failed"
	repair.Status = model.StatusBacklog
	repair.AssignedAgentID = nil
	repair.WorktreeBranch = fmt.Sprintf("feature/%s-rework-%s", taskFeatureName(&repair), uuid.NewString()[:8])
	if err := tx.Model(&repair).Select("context", "status", "assigned_agent_id", "worktree_branch").Updates(&repair).Error; err != nil {
		return fmt.Errorf("stage orchestrated delivery rework: reopen repair subtask: %w", err)
	}

	task.Context["delivery_rework_pending"] = repair.ID.String()
	task.AssignedAgentID = nil
	if err := tx.Model(task).Select("context", "assigned_agent_id").Updates(task).Error; err != nil {
		return fmt.Errorf("stage orchestrated delivery rework: %w", err)
	}
	return nil
}

// FreezeDeliveryArtifact atomically creates a versioned immutable handoff and
// advances testing_ready to verification_ready. A stale task claim rolls back
// the artifact and event together.
func (o *Orchestrator) FreezeDeliveryArtifact(taskID uuid.UUID, snapshot ArtifactSnapshot) (*model.DeliveryArtifact, error) {
	o.deliveryMu.Lock()
	defer o.deliveryMu.Unlock()
	if err := validateArtifactSnapshot(snapshot); err != nil {
		return nil, err
	}
	var artifact model.DeliveryArtifact
	err := o.db.Transaction(func(tx *gorm.DB) error {
		var task model.Task
		if err := tx.First(&task, "id = ?", taskID).Error; err != nil {
			return fmt.Errorf("freeze delivery: load task: %w", err)
		}
		if task.Status != model.StatusTestingReady {
			return fmt.Errorf("%w: task in %s, expected testing_ready", state.ErrStaleTransition, task.Status)
		}

		var maxVersion uint64
		if err := tx.Model(&model.DeliveryArtifact{}).Where("task_id = ?", task.ID).
			Select("COALESCE(MAX(artifact_version), 0)").Scan(&maxVersion).Error; err != nil {
			return fmt.Errorf("freeze delivery: next artifact version: %w", err)
		}
		evidence, err := commandEvidenceField(snapshot.PreliminaryEvidence)
		if err != nil {
			return fmt.Errorf("freeze delivery: encode preliminary evidence: %w", err)
		}
		startedAt := snapshot.PreliminaryEvidence[0].StartedAt
		finishedAt := snapshot.PreliminaryEvidence[0].FinishedAt
		for _, command := range snapshot.PreliminaryEvidence[1:] {
			if command.StartedAt.Before(startedAt) {
				startedAt = command.StartedAt
			}
			if command.FinishedAt.After(finishedAt) {
				finishedAt = command.FinishedAt
			}
		}
		gateRun := model.PreliminaryGateRun{
			ID: uuid.New(), TaskID: task.ID,
			Branch: snapshot.Branch, CommitSHA: snapshot.CommitSHA,
			BaseBranch: snapshot.BaseBranch, BaseSHA: snapshot.BaseSHA,
			WorkspaceID: snapshot.GateWorkspaceID, EnvironmentFingerprint: snapshot.EnvironmentFingerprint,
			CommandEvidence: evidence, Outcome: model.PreliminaryGatePassed,
			Actor: snapshot.Actor, Source: snapshot.Source,
			StartedAt: startedAt, FinishedAt: finishedAt,
		}
		if err := tx.Create(&gateRun).Error; err != nil {
			return fmt.Errorf("freeze delivery: create preliminary gate run: %w", err)
		}
		artifact = model.DeliveryArtifact{
			ID:                   uuid.New(),
			TaskID:               task.ID,
			PreliminaryGateRunID: &gateRun.ID,
			ArtifactVersion:      maxVersion + 1,
			Branch:               snapshot.Branch,
			CommitSHA:            snapshot.CommitSHA,
			BaseBranch:           snapshot.BaseBranch,
			BaseSHA:              snapshot.BaseSHA,
			PreliminaryEvidence:  evidence,
			CreatorActor:         snapshot.Actor,
			CreatorSource:        snapshot.Source,
		}
		if err := tx.Create(&artifact).Error; err != nil {
			return fmt.Errorf("freeze delivery: create artifact: %w", err)
		}
		return casTaskTransition(tx, &task, model.StatusTestingReady, model.StatusVerificationReady, snapshot.Actor, snapshot.Source,
			"preliminary gates passed and artifact frozen", map[string]any{
				"delivery_artifact_id":    artifact.ID.String(),
				"preliminary_gate_run_id": gateRun.ID.String(),
				"artifact_version":        artifact.ArtifactVersion,
			})
	})
	if err != nil {
		return nil, err
	}
	return &artifact, nil
}

// RecordPreliminaryGateFailure appends the isolated gate evidence and routes
// the task without spending model tokens. Code failures return to normal
// implementation; runner/configuration failures park for explicit retry.
func (o *Orchestrator) RecordPreliminaryGateFailure(taskID uuid.UUID, gate deliveryGateResult) error {
	o.deliveryMu.Lock()
	defer o.deliveryMu.Unlock()
	if taskID == uuid.Nil || gate.Candidate.Branch == "" || !validObjectID(gate.Candidate.CommitSHA) ||
		!validObjectID(gate.Candidate.BaseSHA) || strings.TrimSpace(gate.WorkspaceID) == "" ||
		strings.TrimSpace(gate.EnvironmentFingerprint) == "" {
		return errors.New("record preliminary gate failure: exact candidate, workspace, and environment are required")
	}
	if gate.Outcome == model.PreliminaryGatePassed || gate.Outcome == "" {
		return errors.New("record preliminary gate failure: a closed failure outcome is required")
	}
	if err := validateCommandEvidence([]CommandEvidence{gate.Evidence}, false); err != nil {
		return fmt.Errorf("record preliminary gate failure: %w", err)
	}
	evidence, err := commandEvidenceField([]CommandEvidence{gate.Evidence})
	if err != nil {
		return fmt.Errorf("record preliminary gate failure: encode evidence: %w", err)
	}
	target := model.StatusPaused
	if gate.Outcome == model.PreliminaryGateCodeFailure {
		target = model.StatusInProgress
	}
	return o.db.Transaction(func(tx *gorm.DB) error {
		var task model.Task
		if err := tx.First(&task, "id = ?", taskID).Error; err != nil {
			return fmt.Errorf("record preliminary gate failure: load task: %w", err)
		}
		if task.Status != model.StatusTestingReady {
			return fmt.Errorf("%w: task in %s, expected testing_ready", state.ErrStaleTransition, task.Status)
		}
		gateRun := model.PreliminaryGateRun{
			ID: uuid.New(), TaskID: task.ID,
			Branch: gate.Candidate.Branch, CommitSHA: gate.Candidate.CommitSHA,
			BaseBranch: gate.Candidate.BaseBranch, BaseSHA: gate.Candidate.BaseSHA,
			WorkspaceID: gate.WorkspaceID, EnvironmentFingerprint: gate.EnvironmentFingerprint,
			CommandEvidence: evidence, Outcome: gate.Outcome,
			Actor: "orchestrator", Source: "testing_ready",
			StartedAt: gate.Evidence.StartedAt, FinishedAt: gate.Evidence.FinishedAt,
		}
		if err := tx.Create(&gateRun).Error; err != nil {
			return fmt.Errorf("record preliminary gate failure: create gate run: %w", err)
		}
		return casTaskTransition(tx, &task, model.StatusTestingReady, target, "orchestrator", "testing_ready",
			"preliminary gate failed", map[string]any{
				"preliminary_gate_run_id": gateRun.ID.String(),
				"outcome":                 gate.Outcome,
			})
	})
}

// VerifyDelivery appends verification evidence and atomically advances the
// exact current artifact. Failures return to implementation and invalidate the
// artifact without deleting the failed record.
func (o *Orchestrator) VerifyDelivery(req VerifyDeliveryRequest) (*model.VerificationRecord, error) {
	o.deliveryMu.Lock()
	defer o.deliveryMu.Unlock()
	normalizeVerifyRequest(&req)
	requestHash, err := hashRequest(req)
	if err != nil {
		return nil, fmt.Errorf("verify delivery: hash request: %w", err)
	}
	if err := validateVerifyRequest(req); err != nil {
		return nil, err
	}
	var record model.VerificationRecord
	err = o.db.Transaction(func(tx *gorm.DB) error {
		var replay model.VerificationRecord
		if err := tx.Where("idempotency_key = ?", req.IdempotencyKey).First(&replay).Error; err == nil {
			if replay.RequestHash != requestHash {
				return ErrIdempotencyConflict
			}
			record = replay
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var task model.Task
		if err := tx.First(&task, "id = ?", req.TaskID).Error; err != nil {
			return fmt.Errorf("verify delivery: load task: %w", err)
		}
		if task.Status != model.StatusVerificationReady || task.StateVersion != req.ObservedStateVersion {
			return fmt.Errorf("%w: task status/version is %s/%d, observed verification_ready/%d", state.ErrStaleTransition, task.Status, task.StateVersion, req.ObservedStateVersion)
		}
		artifact, err := currentArtifact(tx, req.TaskID)
		if err != nil {
			return err
		}
		if artifact.ArtifactVersion != req.ArtifactVersion || artifact.CommitSHA != req.CommitSHA {
			return fmt.Errorf("%w: current artifact is version %d commit %s", ErrStaleArtifact, artifact.ArtifactVersion, artifact.CommitSHA)
		}
		commands, err := commandEvidenceField(req.CommandEvidence)
		if err != nil {
			return err
		}
		record = model.VerificationRecord{
			ID:                     uuid.New(),
			TaskID:                 task.ID,
			DeliveryArtifactID:     artifact.ID,
			ArtifactVersion:        artifact.ArtifactVersion,
			CommitSHA:              artifact.CommitSHA,
			VerifierActor:          req.Actor,
			EnvironmentFingerprint: req.EnvironmentFingerprint,
			CommandEvidence:        commands,
			BinarySHA256:           req.BinarySHA256,
			Result:                 req.Result,
			Notes:                  req.Notes,
			IdempotencyKey:         req.IdempotencyKey,
			RequestHash:            requestHash,
		}
		if err := tx.Create(&record).Error; err != nil {
			return fmt.Errorf("verify delivery: create record: %w", err)
		}
		interactionIDs, err := persistVerificationInteractions(tx, task, artifact, record, req)
		if err != nil {
			return err
		}

		target := model.StatusIntegrationReady
		reason := "delivery artifact verified"
		var reworkRecordID, hostReworkSessionID uuid.UUID
		if req.Result == model.VerificationFailed {
			target = model.StatusInProgress
			reason = "delivery artifact verification failed"
			rework := model.DeliveryReworkRecord{
				ID: uuid.New(), TaskID: task.ID, DeliveryArtifactID: artifact.ID,
				ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
				Actor: req.Actor, Source: req.Source, Reason: req.FailureReason, Mode: req.FailureMode,
				IdempotencyKey: req.IdempotencyKey, RequestHash: requestHash,
			}
			if req.FailureMode == model.DeliveryReworkHostDirect {
				session, err := createHostReworkSession(tx, task, artifact, req.Actor, req.FailureReason, req.AllowedScope, req.HostDirectAttestation, req.IdempotencyKey, requestHash)
				if err != nil {
					return err
				}
				target = model.StatusHostRework
				hostReworkSessionID = session.ID
				rework.HostReworkSessionID = &session.ID
			} else if err := stageOrchestratedDeliveryRework(tx, &task, req.FailureReason); err != nil {
				return err
			}
			if err := tx.Create(&rework).Error; err != nil {
				return fmt.Errorf("verify delivery: create rework record: %w", err)
			}
			reworkRecordID = rework.ID
			now := time.Now()
			if err := tx.Model(&model.DeliveryArtifact{}).Where("id = ? AND invalidated_at IS NULL", artifact.ID).
				Updates(map[string]any{"invalidated_at": now, "invalidation_reason": "verification_failed"}).Error; err != nil {
				return fmt.Errorf("verify delivery: invalidate artifact: %w", err)
			}
		}
		references := map[string]any{
			"delivery_artifact_id":   artifact.ID.String(),
			"verification_record_id": record.ID.String(),
			"artifact_version":       artifact.ArtifactVersion,
			"commit_sha":             artifact.CommitSHA,
			"result":                 string(req.Result),
		}
		if len(interactionIDs) != 0 {
			references["interaction_record_ids"] = interactionIDs
		}
		if reworkRecordID != uuid.Nil {
			references["delivery_rework_record_id"] = reworkRecordID.String()
		}
		if hostReworkSessionID != uuid.Nil {
			references["host_rework_session_id"] = hostReworkSessionID.String()
		}
		return casTaskTransition(tx, &task, model.StatusVerificationReady, target, req.Actor, req.Source, reason, references)
	})
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// AuthorizeIntegration records the accepted verification and advances an
// exact artifact from integration_ready to merging. The merger runs later.
func (o *Orchestrator) AuthorizeIntegration(req IntegrateDeliveryRequest) (*model.IntegrationAuthorization, error) {
	o.deliveryMu.Lock()
	defer o.deliveryMu.Unlock()
	requestHash, err := hashRequest(req)
	if err != nil {
		return nil, fmt.Errorf("authorize integration: hash request: %w", err)
	}
	if err := validateIntegrateRequest(req); err != nil {
		return nil, err
	}
	var auth model.IntegrationAuthorization
	var preflightErr error
	err = o.db.Transaction(func(tx *gorm.DB) error {
		var replay model.IntegrationAuthorization
		if err := tx.Where("idempotency_key = ?", req.IdempotencyKey).First(&replay).Error; err == nil {
			if replay.RequestHash != requestHash {
				return ErrIdempotencyConflict
			}
			auth = replay
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var task model.Task
		if err := tx.First(&task, "id = ?", req.TaskID).Error; err != nil {
			return err
		}
		if task.Status != model.StatusIntegrationReady || task.StateVersion != req.ObservedStateVersion {
			return fmt.Errorf("%w: task status/version is %s/%d, observed integration_ready/%d", state.ErrStaleTransition, task.Status, task.StateVersion, req.ObservedStateVersion)
		}
		artifact, err := currentArtifact(tx, task.ID)
		if err != nil {
			return err
		}
		if artifact.ArtifactVersion != req.ArtifactVersion || artifact.CommitSHA != req.CommitSHA {
			return fmt.Errorf("%w: current artifact is version %d commit %s", ErrStaleArtifact, artifact.ArtifactVersion, artifact.CommitSHA)
		}
		var verification model.VerificationRecord
		if err := tx.First(&verification, "id = ? AND delivery_artifact_id = ? AND result = ?", req.VerificationRecordID, artifact.ID, model.VerificationPassed).Error; err != nil {
			return fmt.Errorf("authorize integration: passing verification record: %w", err)
		}
		driftReason, err := o.deliveryArtifactDrift(&task, &artifact)
		if err != nil {
			return fmt.Errorf("authorize integration: %w", err)
		}
		if driftReason != "" {
			now := time.Now()
			if err := tx.Model(&model.DeliveryArtifact{}).Where("id = ? AND invalidated_at IS NULL", artifact.ID).
				Updates(map[string]any{"invalidated_at": now, "invalidation_reason": driftReason}).Error; err != nil {
				return fmt.Errorf("authorize integration: invalidate drifted artifact: %w", err)
			}
			if err := casTaskTransition(tx, &task, model.StatusIntegrationReady, model.StatusInProgress,
				req.Actor, "integration_preflight", driftReason, map[string]any{
					"delivery_artifact_id": artifact.ID.String(), "artifact_version": artifact.ArtifactVersion,
					"commit_sha": artifact.CommitSHA,
				}); err != nil {
				return err
			}
			preflightErr = fmt.Errorf("%w: %s", ErrStaleArtifact, driftReason)
			return nil
		}
		auth = model.IntegrationAuthorization{
			ID:                   uuid.New(),
			TaskID:               task.ID,
			DeliveryArtifactID:   artifact.ID,
			VerificationRecordID: verification.ID,
			ArtifactVersion:      artifact.ArtifactVersion,
			CommitSHA:            artifact.CommitSHA,
			BaseSHA:              artifact.BaseSHA,
			Actor:                req.Actor,
			Source:               req.Source,
			IdempotencyKey:       req.IdempotencyKey,
			RequestHash:          requestHash,
		}
		if err := tx.Create(&auth).Error; err != nil {
			return fmt.Errorf("authorize integration: create record: %w", err)
		}
		intent := model.MergeIntent{
			ID: uuid.New(), TaskID: task.ID, DeliveryArtifactID: artifact.ID,
			VerificationRecordID: verification.ID, IntegrationAuthorizationID: auth.ID,
			ArtifactCommitSHA: artifact.CommitSHA, FeatureBranch: artifact.Branch,
			TargetBranch: artifact.BaseBranch, TargetBaseSHA: artifact.BaseSHA,
			Actor: req.Actor, Source: req.Source,
		}
		if err := tx.Create(&intent).Error; err != nil {
			return fmt.Errorf("authorize integration: create merge intent: %w", err)
		}
		return casTaskTransition(tx, &task, model.StatusIntegrationReady, model.StatusMerging, req.Actor, req.Source,
			"verified artifact authorized for integration", map[string]any{
				"delivery_artifact_id":         artifact.ID.String(),
				"verification_record_id":       verification.ID.String(),
				"integration_authorization_id": auth.ID.String(),
				"merge_intent_id":              intent.ID.String(),
				"artifact_version":             artifact.ArtifactVersion,
				"commit_sha":                   artifact.CommitSHA,
			})
	})
	if err != nil {
		return nil, err
	}
	if preflightErr != nil {
		return nil, preflightErr
	}
	return &auth, nil
}

// RequestDeliveryRework atomically invalidates the exact reviewed artifact
// and returns verification_ready or integration_ready work to implementation.
func (o *Orchestrator) RequestDeliveryRework(req RequestDeliveryReworkRequest) (*model.DeliveryReworkRecord, error) {
	o.deliveryMu.Lock()
	defer o.deliveryMu.Unlock()
	requestHash, err := hashRequest(req)
	if err != nil {
		return nil, fmt.Errorf("request delivery rework: hash request: %w", err)
	}
	if err := validateReworkRequest(req); err != nil {
		return nil, err
	}
	var record model.DeliveryReworkRecord
	err = o.db.Transaction(func(tx *gorm.DB) error {
		var replay model.DeliveryReworkRecord
		if err := tx.Where("idempotency_key = ?", req.IdempotencyKey).First(&replay).Error; err == nil {
			if replay.RequestHash != requestHash {
				return ErrIdempotencyConflict
			}
			record = replay
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var task model.Task
		if err := tx.First(&task, "id = ?", req.TaskID).Error; err != nil {
			return fmt.Errorf("request delivery rework: load task: %w", err)
		}
		if (task.Status != model.StatusVerificationReady && task.Status != model.StatusIntegrationReady) || task.StateVersion != req.ObservedStateVersion {
			return fmt.Errorf("%w: task status/version is %s/%d", state.ErrStaleTransition, task.Status, task.StateVersion)
		}
		artifact, err := currentArtifact(tx, task.ID)
		if err != nil {
			return err
		}
		if artifact.ArtifactVersion != req.ArtifactVersion || artifact.CommitSHA != req.CommitSHA {
			return fmt.Errorf("%w: current artifact is version %d commit %s", ErrStaleArtifact, artifact.ArtifactVersion, artifact.CommitSHA)
		}
		record = model.DeliveryReworkRecord{
			ID: uuid.New(), TaskID: task.ID, DeliveryArtifactID: artifact.ID,
			ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
			Actor: req.Actor, Source: req.Source, Reason: req.Reason, Mode: req.Mode,
			IdempotencyKey: req.IdempotencyKey, RequestHash: requestHash,
		}
		target := model.StatusInProgress
		var hostReworkSessionID uuid.UUID
		if req.Mode == model.DeliveryReworkHostDirect {
			session, err := createHostReworkSession(tx, task, artifact, req.Actor, req.Reason, req.AllowedScope, req.HostDirectAttestation, req.IdempotencyKey, requestHash)
			if err != nil {
				return err
			}
			target = model.StatusHostRework
			hostReworkSessionID = session.ID
			record.HostReworkSessionID = &session.ID
		} else if err := stageOrchestratedDeliveryRework(tx, &task, req.Reason); err != nil {
			return err
		}
		if err := tx.Create(&record).Error; err != nil {
			return fmt.Errorf("request delivery rework: create record: %w", err)
		}
		now := time.Now()
		if err := tx.Model(&model.DeliveryArtifact{}).Where("id = ? AND invalidated_at IS NULL", artifact.ID).
			Updates(map[string]any{"invalidated_at": now, "invalidation_reason": "rework_requested"}).Error; err != nil {
			return fmt.Errorf("request delivery rework: invalidate artifact: %w", err)
		}
		return casTaskTransition(tx, &task, task.Status, target, req.Actor, req.Source,
			"delivery rework requested", map[string]any{
				"delivery_artifact_id": artifact.ID.String(), "artifact_version": artifact.ArtifactVersion,
				"commit_sha": artifact.CommitSHA, "rework_record_id": record.ID.String(), "reason": req.Reason,
				"mode": string(req.Mode), "host_rework_session_id": hostReworkSessionID.String(),
			})
	})
	if err != nil {
		return nil, err
	}
	return &record, nil
}
