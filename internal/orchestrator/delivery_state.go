package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	TaskID               uuid.UUID
	ObservedStateVersion uint64
	ArtifactVersion      uint64
	CommitSHA            string
	Actor                string
	Source               string
	Reason               string
	IdempotencyKey       string
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

		target := model.StatusIntegrationReady
		reason := "delivery artifact verified"
		if req.Result == model.VerificationFailed {
			target = model.StatusInProgress
			reason = "delivery artifact verification failed"
			now := time.Now()
			if err := tx.Model(&model.DeliveryArtifact{}).Where("id = ? AND invalidated_at IS NULL", artifact.ID).
				Updates(map[string]any{"invalidated_at": now, "invalidation_reason": "verification_failed"}).Error; err != nil {
				return fmt.Errorf("verify delivery: invalidate artifact: %w", err)
			}
		}
		return casTaskTransition(tx, &task, model.StatusVerificationReady, target, req.Actor, req.Source, reason, map[string]any{
			"delivery_artifact_id":   artifact.ID.String(),
			"verification_record_id": record.ID.String(),
			"artifact_version":       artifact.ArtifactVersion,
			"commit_sha":             artifact.CommitSHA,
			"result":                 string(req.Result),
		})
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
		return casTaskTransition(tx, &task, model.StatusIntegrationReady, model.StatusMerging, req.Actor, req.Source,
			"verified artifact authorized for integration", map[string]any{
				"delivery_artifact_id":         artifact.ID.String(),
				"verification_record_id":       verification.ID.String(),
				"integration_authorization_id": auth.ID.String(),
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
			Actor: req.Actor, Source: req.Source, Reason: req.Reason,
			IdempotencyKey: req.IdempotencyKey, RequestHash: requestHash,
		}
		if err := tx.Create(&record).Error; err != nil {
			return fmt.Errorf("request delivery rework: create record: %w", err)
		}
		now := time.Now()
		if err := tx.Model(&model.DeliveryArtifact{}).Where("id = ? AND invalidated_at IS NULL", artifact.ID).
			Updates(map[string]any{"invalidated_at": now, "invalidation_reason": "rework_requested"}).Error; err != nil {
			return fmt.Errorf("request delivery rework: invalidate artifact: %w", err)
		}
		return casTaskTransition(tx, &task, task.Status, model.StatusInProgress, req.Actor, req.Source,
			"delivery rework requested", map[string]any{
				"delivery_artifact_id": artifact.ID.String(), "artifact_version": artifact.ArtifactVersion,
				"commit_sha": artifact.CommitSHA, "rework_record_id": record.ID.String(), "reason": req.Reason,
			})
	})
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func casTaskTransition(tx *gorm.DB, task *model.Task, expected, target model.TaskStatus, actor, source, reason string, references map[string]any) error {
	originalStatus := task.Status
	originalVersion := task.StateVersion
	originalUpdatedAt := task.UpdatedAt
	transitionPersisted := false
	defer func() {
		if !transitionPersisted {
			task.Status = originalStatus
			task.StateVersion = originalVersion
			task.UpdatedAt = originalUpdatedAt
		}
	}()

	event, err := state.GuardedTransitionTask(task, state.TransitionRequest{
		Target:         target,
		Actor:          actor,
		ExpectedStatus: expected,
		Evidence: state.Evidence{
			TaskID:     task.ID,
			Actor:      actor,
			Source:     source,
			Reason:     reason,
			References: references,
		},
	})
	if err != nil {
		return err
	}
	oldVersion := originalVersion
	task.StateVersion = oldVersion + 1
	res := tx.Model(&model.Task{}).
		Where("id = ? AND status = ? AND state_version = ?", task.ID, expected, oldVersion).
		Updates(taskTransitionColumns(task))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("%w: transition %s/%d was already claimed", state.ErrStaleTransition, expected, oldVersion)
	}
	if err := tx.Create(event).Error; err != nil {
		return fmt.Errorf("persist transition event: %w", err)
	}
	transitionPersisted = true
	return nil
}

// transitionTaskAtomic is the persistence boundary for ordinary task-state
// changes. It gives legacy lifecycle paths the same compare-and-swap and
// task/event transaction semantics as delivery transitions without requiring
// them to manufacture delivery artifacts.
func (o *Orchestrator) transitionTaskAtomic(task *model.Task, target model.TaskStatus, actor, source, reason string, references map[string]any) error {
	if task == nil {
		return errors.New("transition task: task is nil")
	}
	expected := task.Status
	originalStatus := task.Status
	originalVersion := task.StateVersion
	originalUpdatedAt := task.UpdatedAt
	err := o.db.Transaction(func(tx *gorm.DB) error {
		return casTaskTransition(tx, task, expected, target, actor, source, reason, references)
	})
	if err != nil {
		task.Status = originalStatus
		task.StateVersion = originalVersion
		task.UpdatedAt = originalUpdatedAt
	}
	return err
}

// GuardedTaskTransitionTx exposes the single task-state persistence boundary
// to the in-process HTTP recovery surface. The caller owns the surrounding
// transaction so companion recovery/audit rows commit atomically with the
// task row and status-change event.
func GuardedTaskTransitionTx(tx *gorm.DB, task *model.Task, target model.TaskStatus, actor, source, reason string, references map[string]any) error {
	if tx == nil || task == nil {
		return errors.New("guarded task transition: transaction and task are required")
	}
	return casTaskTransition(tx, task, task.Status, target, actor, source, reason, references)
}

func taskTransitionColumns(task *model.Task) map[string]any {
	return map[string]any{
		"parent_task_id": task.ParentTaskID, "title": task.Title, "description": task.Description,
		"status": task.Status, "state_version": task.StateVersion, "category": task.Category,
		"priority": task.Priority, "complexity_score": task.ComplexityScore, "labels": task.Labels,
		"dependency_ids": task.DependencyIDs, "assigned_agent_id": task.AssignedAgentID, "plan": task.Plan,
		"plan_feedback": task.PlanFeedback, "test_plan": task.TestPlan, "test_feedback": task.TestFeedback,
		"worktree_branch": task.WorktreeBranch, "worktree_base_sha": task.WorktreeBaseSHA,
		"pr_url": task.PRUrl, "phase": task.Phase,
		"tests_for": task.TestsFor, "tdd_exceptions": task.TDDExceptions,
		"needs_human_review": task.NeedsHumanReview, "context": task.Context, "updated_at": task.UpdatedAt,
	}
}

func casSubtaskCompletion(tx *gorm.DB, task *model.Task, actor string, references map[string]any) error {
	event, err := state.GuardedCompleteSubtask(task, state.TransitionRequest{
		Target: model.StatusDone, Actor: actor, ExpectedStatus: task.Status,
		Evidence: state.Evidence{
			TaskID: task.ID, Actor: actor, Source: "agent_completion",
			Reason: "worker completion accepted", NormalizedReason: "accepted_worker_completion",
			Timestamp: time.Now(), References: references,
		},
	})
	if err != nil {
		return err
	}
	oldStatus := model.TaskStatus(event.OldValue)
	oldVersion := task.StateVersion
	res := tx.Model(&model.Task{}).
		Where("id = ? AND status = ? AND state_version = ?", task.ID, oldStatus, oldVersion).
		Updates(map[string]any{"status": model.StatusDone, "state_version": oldVersion + 1,
			"assigned_agent_id": nil, "context": task.Context, "updated_at": task.UpdatedAt})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("%w: subtask completion %s/%d was already claimed", state.ErrStaleTransition, oldStatus, oldVersion)
	}
	task.StateVersion = oldVersion + 1
	task.AssignedAgentID = nil
	return tx.Create(event).Error
}

func casAcceptedExistingSubtask(tx *gorm.DB, task *model.Task, references map[string]any) error {
	event, err := state.GuardedAcceptExistingSubtask(task, state.TransitionRequest{
		Target: model.StatusDone, Actor: "orchestrator", ExpectedStatus: task.Status,
		Evidence: state.Evidence{
			TaskID: task.ID, Actor: "orchestrator", Source: "dedup_existing_work",
			Reason: "estimated files and commit evidence already present", NormalizedReason: "accepted_existing_work",
			Timestamp: time.Now(), References: references,
		},
	})
	if err != nil {
		return err
	}
	oldStatus := model.TaskStatus(event.OldValue)
	oldVersion := task.StateVersion
	res := tx.Model(&model.Task{}).
		Where("id = ? AND status = ? AND state_version = ?", task.ID, oldStatus, oldVersion).
		Updates(map[string]any{"status": model.StatusDone, "state_version": oldVersion + 1,
			"assigned_agent_id": nil, "context": task.Context, "updated_at": task.UpdatedAt})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("%w: existing-work acceptance %s/%d was already claimed", state.ErrStaleTransition, oldStatus, oldVersion)
	}
	task.StateVersion = oldVersion + 1
	task.AssignedAgentID = nil
	return tx.Create(event).Error
}

func casSupersedeCompletedTestSubtask(tx *gorm.DB, task *model.Task, actor string, references map[string]any) error {
	originalStatus := task.Status
	originalVersion := task.StateVersion
	originalUpdatedAt := task.UpdatedAt
	originalAssignment := task.AssignedAgentID
	persisted := false
	defer func() {
		if !persisted {
			task.Status = originalStatus
			task.StateVersion = originalVersion
			task.UpdatedAt = originalUpdatedAt
			task.AssignedAgentID = originalAssignment
		}
	}()

	event, err := state.GuardedSupersedeCompletedTestSubtask(task, state.TransitionRequest{
		Target: model.StatusRejected, Actor: actor, ExpectedStatus: model.StatusDone,
		Evidence: state.Evidence{
			TaskID: task.ID, Actor: actor, Source: "review_gate",
			Reason:    "completed test subtask superseded after review rejection",
			Timestamp: time.Now(), References: references,
		},
	})
	if err != nil {
		return err
	}
	task.StateVersion = originalVersion + 1
	task.AssignedAgentID = nil
	res := tx.Model(&model.Task{}).
		Where("id = ? AND status = ? AND state_version = ?", task.ID, model.StatusDone, originalVersion).
		Updates(map[string]any{"status": task.Status, "state_version": task.StateVersion,
			"assigned_agent_id": nil, "updated_at": task.UpdatedAt})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("%w: test subtask supersession done/%d was already claimed", state.ErrStaleTransition, originalVersion)
	}
	if err := tx.Create(event).Error; err != nil {
		return fmt.Errorf("persist test subtask supersession event: %w", err)
	}
	persisted = true
	return nil
}

// casClaimInProgressSubtask attaches a worker to a recovered, unassigned
// in-progress child without inventing an in_progress -> in_progress status
// transition. Assignment, state version, and audit event remain one CAS
// transaction so competing dispatchers cannot both claim the child.
func casClaimInProgressSubtask(tx *gorm.DB, task *model.Task, agentID uuid.UUID, actor, source string) error {
	if task == nil || task.ParentTaskID == nil || task.Status != model.StatusInProgress {
		return errors.New("only an in-progress subtask may be claimed without a status transition")
	}
	if task.AssignedAgentID != nil {
		return fmt.Errorf("%w: subtask is already assigned", state.ErrStaleTransition)
	}
	originalVersion := task.StateVersion
	originalUpdatedAt := task.UpdatedAt
	now := time.Now()
	task.AssignedAgentID = &agentID
	task.StateVersion = originalVersion + 1
	task.UpdatedAt = now
	persisted := false
	defer func() {
		if !persisted {
			task.AssignedAgentID = nil
			task.StateVersion = originalVersion
			task.UpdatedAt = originalUpdatedAt
		}
	}()

	res := tx.Model(&model.Task{}).
		Where("id = ? AND status = ? AND state_version = ? AND assigned_agent_id IS NULL",
			task.ID, model.StatusInProgress, originalVersion).
		Updates(map[string]any{"assigned_agent_id": agentID, "state_version": task.StateVersion, "updated_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("%w: in-progress subtask claim was already taken", state.ErrStaleTransition)
	}
	event := &model.TaskEvent{
		ID: uuid.New(), TaskID: task.ID, EventType: "subtask_claimed",
		OldValue: string(model.StatusInProgress), NewValue: string(model.StatusInProgress),
		Details: model.JSONField{"evidence": map[string]any{
			"task_id": task.ID.String(), "actor": actor, "source": source,
			"reason": "unassigned in-progress subtask claimed", "timestamp": now.Format(time.RFC3339Nano),
			"references": map[string]any{"agent_id": agentID.String()},
		}},
		Actor: actor, CreatedAt: now,
	}
	if err := tx.Create(event).Error; err != nil {
		return fmt.Errorf("persist in-progress subtask claim event: %w", err)
	}
	persisted = true
	return nil
}

func currentArtifact(tx *gorm.DB, taskID uuid.UUID) (model.DeliveryArtifact, error) {
	var artifact model.DeliveryArtifact
	if err := tx.Where("task_id = ? AND invalidated_at IS NULL", taskID).First(&artifact).Error; err != nil {
		return artifact, fmt.Errorf("current delivery artifact: %w", err)
	}
	return artifact, nil
}

func validateArtifactSnapshot(snapshot ArtifactSnapshot) error {
	if strings.TrimSpace(snapshot.Branch) == "" || strings.TrimSpace(snapshot.BaseBranch) == "" {
		return errors.New("delivery artifact: branch and base branch are required")
	}
	if !validObjectID(snapshot.CommitSHA) || !validObjectID(snapshot.BaseSHA) {
		return errors.New("delivery artifact: commit and base must be full Git object IDs")
	}
	if strings.TrimSpace(snapshot.Actor) == "" || strings.TrimSpace(snapshot.Source) == "" {
		return errors.New("delivery artifact: actor and source are required")
	}
	if strings.TrimSpace(snapshot.GateWorkspaceID) == "" || strings.TrimSpace(snapshot.EnvironmentFingerprint) == "" {
		return errors.New("delivery artifact: gate workspace and environment fingerprint are required")
	}
	if len(snapshot.PreliminaryEvidence) == 0 {
		return errors.New("delivery artifact: preliminary evidence is required")
	}
	if err := validateCommandEvidence(snapshot.PreliminaryEvidence, true); err != nil {
		return fmt.Errorf("delivery artifact: %w", err)
	}
	return nil
}

func validateVerifyRequest(req VerifyDeliveryRequest) error {
	if req.TaskID == uuid.Nil || req.ObservedStateVersion == 0 || req.ArtifactVersion == 0 {
		return errors.New("verify delivery: task ID, observed state version, and artifact version are required")
	}
	if !validObjectID(req.CommitSHA) {
		return errors.New("verify delivery: full commit SHA is required")
	}
	if strings.TrimSpace(req.Actor) == "" || strings.TrimSpace(req.Source) == "" || strings.TrimSpace(req.EnvironmentFingerprint) == "" || strings.TrimSpace(req.IdempotencyKey) == "" {
		return errors.New("verify delivery: actor, source, environment fingerprint, and idempotency key are required")
	}
	if req.Result != model.VerificationPassed && req.Result != model.VerificationFailed {
		return errors.New("verify delivery: result must be pass or fail")
	}
	if len(req.CommandEvidence) == 0 {
		return errors.New("verify delivery: command evidence is required")
	}
	if err := validateCommandEvidence(req.CommandEvidence, req.Result == model.VerificationPassed); err != nil {
		return fmt.Errorf("verify delivery: %w", err)
	}
	if req.BinarySHA256 != "" && !validSHA256(req.BinarySHA256) {
		return errors.New("verify delivery: binary SHA-256 must be 64 hexadecimal characters")
	}
	return nil
}

func validateCommandEvidence(commands []CommandEvidence, requireAllPassed bool) error {
	for i, command := range commands {
		if strings.TrimSpace(command.Command) == "" {
			return fmt.Errorf("command evidence %d has an empty command", i+1)
		}
		if command.StartedAt.IsZero() || command.FinishedAt.IsZero() || command.FinishedAt.Before(command.StartedAt) {
			return fmt.Errorf("command evidence %d has invalid timestamps", i+1)
		}
		if requireAllPassed && (!command.Passed || command.ExitCode != 0) {
			return fmt.Errorf("passing verification contains failed command evidence %d", i+1)
		}
	}
	return nil
}

func validSHA256(raw string) bool {
	raw = strings.TrimSpace(raw)
	if len(raw) != 64 {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

func validateIntegrateRequest(req IntegrateDeliveryRequest) error {
	if req.TaskID == uuid.Nil || req.VerificationRecordID == uuid.Nil || req.ObservedStateVersion == 0 || req.ArtifactVersion == 0 {
		return errors.New("authorize integration: task, verification record, observed state version, and artifact version are required")
	}
	if !validObjectID(req.CommitSHA) {
		return errors.New("authorize integration: full commit SHA is required")
	}
	if strings.TrimSpace(req.Actor) == "" || strings.TrimSpace(req.Source) == "" || strings.TrimSpace(req.IdempotencyKey) == "" {
		return errors.New("authorize integration: actor, source, and idempotency key are required")
	}
	return nil
}

func validateReworkRequest(req RequestDeliveryReworkRequest) error {
	if req.TaskID == uuid.Nil || req.ObservedStateVersion == 0 || req.ArtifactVersion == 0 {
		return errors.New("request delivery rework: task, observed state version, and artifact version are required")
	}
	if !validObjectID(req.CommitSHA) {
		return errors.New("request delivery rework: full commit SHA is required")
	}
	if strings.TrimSpace(req.Actor) == "" || strings.TrimSpace(req.Source) == "" || strings.TrimSpace(req.Reason) == "" || strings.TrimSpace(req.IdempotencyKey) == "" {
		return errors.New("request delivery rework: actor, source, reason, and idempotency key are required")
	}
	return nil
}

func validObjectID(raw string) bool {
	raw = strings.TrimSpace(raw)
	if len(raw) != 40 && len(raw) != 64 {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

func commandEvidenceField(commands []CommandEvidence) (model.JSONField, error) {
	raw, err := json.Marshal(commands)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return nil, err
	}
	return model.JSONField{"commands": normalized}, nil
}

func hashRequest(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
