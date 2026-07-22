package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
)

var hostEvidenceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

type SubmitHostReworkRequest struct {
	TaskID               uuid.UUID
	ObservedStateVersion uint64
	SessionID            uuid.UUID
	CommitSHA            string
	Actor                string
	Source               string
	IdempotencyKey       string
}

type AbandonHostReworkRequest struct {
	TaskID               uuid.UUID
	ObservedStateVersion uint64
	SessionID            uuid.UUID
	Actor                string
	Source               string
	Reason               string
	IdempotencyKey       string
}

func createHostReworkSession(tx *gorm.DB, task model.Task, artifact model.DeliveryArtifact, actor, reason string, allowedScope []string, attestation HostDirectAttestation, idempotencyKey, requestHash string) (model.HostReworkSession, error) {
	if err := validateHostDirectRequest(actor, reason, allowedScope, attestation); err != nil {
		return model.HostReworkSession{}, err
	}
	var activeAttempts int64
	if err := tx.Model(&model.WorkerAttempt{}).Where("task_id = ? AND completed_at IS NULL", task.ID).Count(&activeAttempts).Error; err != nil {
		return model.HostReworkSession{}, fmt.Errorf("host rework: count active worker attempts: %w", err)
	}
	if activeAttempts != 0 {
		return model.HostReworkSession{}, errors.New("host rework: an active worker attempt owns the task")
	}
	var activeSessions int64
	if err := tx.Model(&model.HostReworkSession{}).Where("task_id = ? AND disposition = ?", task.ID, model.HostReworkActive).Count(&activeSessions).Error; err != nil {
		return model.HostReworkSession{}, fmt.Errorf("host rework: count active sessions: %w", err)
	}
	if activeSessions != 0 {
		return model.HostReworkSession{}, errors.New("host rework: an active session already owns the task")
	}
	attestationJSON := model.JSONField{
		"acceptance_criteria_unchanged": attestation.AcceptanceCriteriaUnchanged,
		"dependency_shape_unchanged":    attestation.DependencyShapeUnchanged,
		"no_persistence_or_schema":      attestation.NoPersistenceOrSchema,
		"no_security_or_auth":           attestation.NoSecurityOrAuth,
		"no_cross_process_ownership":    attestation.NoCrossProcessOwnership,
		"no_build_or_release_policy":    attestation.NoBuildOrReleasePolicy,
	}
	now := time.Now()
	session := model.HostReworkSession{
		ID: uuid.New(), TaskID: task.ID, DeliveryArtifactID: artifact.ID,
		PriorArtifactVersion: artifact.ArtifactVersion, PriorCommitSHA: artifact.CommitSHA,
		Branch: artifact.Branch, OwnerActor: actor, Reason: reason,
		AllowedScope: model.JSONArray(append([]string(nil), allowedScope...)), Attestation: attestationJSON,
		StartIdempotencyKey: idempotencyKey, StartRequestHash: requestHash,
		Disposition: model.HostReworkActive, StartedAt: now, UpdatedAt: now,
	}
	if err := tx.Create(&session).Error; err != nil {
		return model.HostReworkSession{}, fmt.Errorf("host rework: create session: %w", err)
	}
	return session, nil
}

func validateHostDirectRequest(actor, reason string, allowedScope []string, attestation HostDirectAttestation) error {
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(actor) == "user" {
		return errors.New("host rework: a specific actor is required")
	}
	if strings.TrimSpace(reason) == "" {
		return errors.New("host rework: reason is required")
	}
	if len(allowedScope) == 0 {
		return errors.New("host rework: at least one allowed repository path is required")
	}
	for _, raw := range allowedScope {
		if !validHostScope(raw) {
			return fmt.Errorf("host rework: invalid allowed scope %q", raw)
		}
	}
	if !attestation.AcceptanceCriteriaUnchanged || !attestation.DependencyShapeUnchanged ||
		!attestation.NoPersistenceOrSchema || !attestation.NoSecurityOrAuth ||
		!attestation.NoCrossProcessOwnership || !attestation.NoBuildOrReleasePolicy {
		return errors.New("host rework: every bounded-risk attestation must be true")
	}
	return nil
}

func validHostScope(raw string) bool {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if raw == "" || strings.HasPrefix(raw, "/") || strings.Contains(raw, "\x00") {
		return false
	}
	clean := path.Clean(raw)
	return clean == raw && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func persistVerificationInteractions(tx *gorm.DB, task model.Task, artifact model.DeliveryArtifact, record model.VerificationRecord, req VerifyDeliveryRequest) ([]string, error) {
	if len(req.Interactions) == 0 {
		return nil, nil
	}
	var specificationCount int64
	if err := tx.Model(&model.TaskSpecification{}).Where("task_id = ?", task.ID).Count(&specificationCount).Error; err != nil {
		return nil, fmt.Errorf("verify delivery: inspect task specification: %w", err)
	}
	ids := make([]string, 0, len(req.Interactions))
	for _, interaction := range req.Interactions {
		if specificationCount > 0 {
			var criterionCount int64
			if err := tx.Model(&model.TaskAcceptanceCriterion{}).
				Where("task_id = ? AND criterion_key = ?", task.ID, interaction.AcceptanceCriterionKey).
				Count(&criterionCount).Error; err != nil {
				return nil, fmt.Errorf("verify delivery: inspect acceptance criterion: %w", err)
			}
			if criterionCount != 1 {
				return nil, fmt.Errorf("verify delivery: acceptance criterion %q is not part of the task specification", interaction.AcceptanceCriterionKey)
			}
		}
		steps, err := json.Marshal(interaction.Steps)
		if err != nil {
			return nil, err
		}
		refs, err := json.Marshal(interaction.EvidenceRefs)
		if err != nil {
			return nil, err
		}
		row := model.VerificationInteraction{
			ID: uuid.New(), TaskID: task.ID, VerificationRecordID: record.ID, DeliveryArtifactID: artifact.ID,
			AcceptanceCriterionKey: interaction.AcceptanceCriterionKey, ScenarioName: interaction.ScenarioName,
			InteractionStepsJSON: string(steps), ObservedResult: interaction.ObservedResult,
			EvidenceRefsJSON: string(refs), BinarySHA256: req.BinarySHA256,
			ApplicationVersion: interaction.ApplicationVersion, HostEnvironment: interaction.HostEnvironment,
			RunPID: interaction.RunPID, Result: interaction.Result, Discrepancy: interaction.Discrepancy,
		}
		if err := tx.Create(&row).Error; err != nil {
			return nil, fmt.Errorf("verify delivery: create interaction evidence: %w", err)
		}
		ids = append(ids, row.ID.String())
	}
	return ids, nil
}

func validateVerificationInteractions(req VerifyDeliveryRequest) error {
	if len(req.Interactions) == 0 {
		return nil
	}
	if !validSHA256(req.BinarySHA256) {
		return errors.New("verify delivery: Computer Use evidence requires the exact binary SHA-256")
	}
	seen := make(map[string]struct{}, len(req.Interactions))
	failed := false
	for i, interaction := range req.Interactions {
		if !hostEvidenceID.MatchString(strings.TrimSpace(interaction.AcceptanceCriterionKey)) || strings.TrimSpace(interaction.ScenarioName) == "" {
			return fmt.Errorf("verify delivery: interaction %d requires a stable criterion ID and scenario name", i+1)
		}
		key := interaction.AcceptanceCriterionKey + "\x00" + interaction.ScenarioName
		if _, exists := seen[key]; exists {
			return fmt.Errorf("verify delivery: duplicate interaction scenario %q", interaction.ScenarioName)
		}
		seen[key] = struct{}{}
		if len(interaction.Steps) == 0 || strings.TrimSpace(interaction.ObservedResult) == "" ||
			strings.TrimSpace(interaction.ApplicationVersion) == "" || strings.TrimSpace(interaction.HostEnvironment) == "" || interaction.RunPID <= 0 {
			return fmt.Errorf("verify delivery: interaction %d lacks steps, observed result, application version, host environment, or PID", i+1)
		}
		for j, step := range interaction.Steps {
			if strings.TrimSpace(step.Action) == "" || strings.TrimSpace(step.Observed) == "" {
				return fmt.Errorf("verify delivery: interaction %d step %d requires action and observation", i+1, j+1)
			}
		}
		if len(interaction.EvidenceRefs) == 0 {
			return fmt.Errorf("verify delivery: interaction %d requires content-addressed evidence", i+1)
		}
		for j, ref := range interaction.EvidenceRefs {
			if !hostEvidenceID.MatchString(strings.TrimSpace(ref.ArtifactID)) || !validSHA256(ref.SHA256) ||
				(!strings.HasPrefix(ref.MediaType, "image/") && !strings.HasPrefix(ref.MediaType, "video/")) {
				return fmt.Errorf("verify delivery: interaction %d evidence %d is not a valid content-addressed image/video reference", i+1, j+1)
			}
		}
		if interaction.Result != model.VerificationPassed && interaction.Result != model.VerificationFailed {
			return fmt.Errorf("verify delivery: interaction %d result must be pass or fail", i+1)
		}
		if interaction.Result == model.VerificationFailed {
			failed = true
			if strings.TrimSpace(interaction.Discrepancy) == "" {
				return fmt.Errorf("verify delivery: failed interaction %d requires a discrepancy", i+1)
			}
		}
	}
	if req.Result == model.VerificationPassed && failed {
		return errors.New("verify delivery: passing verification contains a failed interaction")
	}
	if req.Result == model.VerificationFailed && !failed {
		return errors.New("verify delivery: failed Computer Use verification requires a failed interaction")
	}
	return nil
}

func normalizeVerifyRequest(req *VerifyDeliveryRequest) {
	if req.Result == model.VerificationFailed {
		if req.FailureMode == "" {
			req.FailureMode = model.DeliveryReworkOrchestrated
		}
		if strings.TrimSpace(req.FailureReason) == "" {
			req.FailureReason = "verification failed"
		}
	}
}

// SubmitHostRework proves that the canonical feature ref names a new commit,
// that the prior artifact is its ancestor, and that every changed path is
// within the session's explicit scope before returning to the gate runner.
func (o *Orchestrator) SubmitHostRework(req SubmitHostReworkRequest) (*model.HostReworkSubmission, error) {
	o.deliveryMu.Lock()
	defer o.deliveryMu.Unlock()
	if err := validateSubmitHostRework(req); err != nil {
		return nil, err
	}
	requestHash, err := hashRequest(req)
	if err != nil {
		return nil, err
	}
	var replay model.HostReworkSubmission
	if err := o.db.Where("idempotency_key = ?", req.IdempotencyKey).First(&replay).Error; err == nil {
		if replay.RequestHash != requestHash {
			return nil, ErrIdempotencyConflict
		}
		return &replay, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	var task model.Task
	if err := o.db.First(&task, "id = ?", req.TaskID).Error; err != nil {
		return nil, err
	}
	var session model.HostReworkSession
	if err := o.db.First(&session, "id = ? AND task_id = ?", req.SessionID, task.ID).Error; err != nil {
		return nil, err
	}
	if task.Status != model.StatusHostRework || task.StateVersion != req.ObservedStateVersion || session.Disposition != model.HostReworkActive {
		return nil, fmt.Errorf("%w: host rework task/session is no longer current", state.ErrStaleTransition)
	}
	if session.OwnerActor != req.Actor {
		return nil, errors.New("host rework: only the owning actor may submit")
	}
	changedPaths, err := o.verifyHostReworkGit(task, session, req.CommitSHA)
	if err != nil {
		return nil, err
	}

	var submission model.HostReworkSubmission
	err = o.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&task, "id = ?", req.TaskID).Error; err != nil {
			return err
		}
		if err := tx.First(&session, "id = ? AND task_id = ?", req.SessionID, task.ID).Error; err != nil {
			return err
		}
		if task.Status != model.StatusHostRework || task.StateVersion != req.ObservedStateVersion || session.Disposition != model.HostReworkActive {
			return fmt.Errorf("%w: host rework task/session changed before submission", state.ErrStaleTransition)
		}
		submission = model.HostReworkSubmission{
			ID: uuid.New(), SessionID: session.ID, TaskID: task.ID,
			PriorCommitSHA: session.PriorCommitSHA, ReplacementCommitSHA: req.CommitSHA,
			Actor: req.Actor, IdempotencyKey: req.IdempotencyKey, RequestHash: requestHash,
			ChangedPaths: model.JSONArray(changedPaths),
		}
		if err := tx.Create(&submission).Error; err != nil {
			return fmt.Errorf("host rework: create submission: %w", err)
		}
		now := time.Now()
		res := tx.Model(&model.HostReworkSession{}).
			Where("id = ? AND disposition = ?", session.ID, model.HostReworkActive).
			Updates(map[string]any{
				"disposition": model.HostReworkSubmitted, "replacement_commit_sha": req.CommitSHA,
				"terminal_actor": req.Actor, "terminal_reason": "replacement submitted",
				"terminal_idempotency_key": req.IdempotencyKey, "terminal_request_hash": requestHash,
				"finished_at": now, "updated_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("%w: host rework session was already resolved", state.ErrStaleTransition)
		}
		return casTaskTransition(tx, &task, model.StatusHostRework, model.StatusTestingReady, req.Actor, req.Source,
			"host rework replacement submitted for fresh gates", map[string]any{
				"host_rework_session_id": session.ID.String(), "host_rework_submission_id": submission.ID.String(),
				"prior_commit_sha": session.PriorCommitSHA, "replacement_commit_sha": req.CommitSHA,
			})
	})
	if err != nil {
		return nil, err
	}
	return &submission, nil
}

func validateSubmitHostRework(req SubmitHostReworkRequest) error {
	if req.TaskID == uuid.Nil || req.SessionID == uuid.Nil || req.ObservedStateVersion == 0 {
		return errors.New("host rework submission: task, session, and observed state version are required")
	}
	if !validObjectID(req.CommitSHA) {
		return errors.New("host rework submission: full replacement commit SHA is required")
	}
	if strings.TrimSpace(req.Actor) == "" || req.Actor == "user" || strings.TrimSpace(req.Source) == "" || strings.TrimSpace(req.IdempotencyKey) == "" {
		return errors.New("host rework submission: actor, source, and idempotency key are required")
	}
	return nil
}

func (o *Orchestrator) verifyHostReworkGit(task model.Task, session model.HostReworkSession, replacementSHA string) ([]string, error) {
	worktreePath := o.resolveIntegrationWorktree(&task)
	if worktreePath == "" {
		return nil, errors.New("host rework: canonical feature worktree is unavailable")
	}
	branchRef := "refs/heads/" + strings.TrimPrefix(session.Branch, "refs/heads/")
	branchSHA, err := gitexec.RunGit(context.Background(), worktreePath, "rev-parse", "--verify", branchRef+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("host rework: resolve canonical feature ref: %w", err)
	}
	if strings.TrimSpace(branchSHA) != strings.TrimSpace(replacementSHA) {
		return nil, ErrStaleArtifact
	}
	if replacementSHA == session.PriorCommitSHA {
		return nil, errors.New("host rework: replacement commit must differ from the invalidated artifact")
	}
	if _, err := gitexec.RunGit(context.Background(), worktreePath, "merge-base", "--is-ancestor", session.PriorCommitSHA, replacementSHA); err != nil {
		return nil, errors.New("host rework: replacement must descend from the invalidated artifact")
	}
	status, err := gitexec.RunGit(context.Background(), worktreePath, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(status) != "" {
		return nil, errors.New("host rework: feature worktree must be clean before submission")
	}
	diff, err := gitexec.RunGit(context.Background(), worktreePath, "diff", "--name-only", session.PriorCommitSHA+".."+replacementSHA)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(diff) == "" {
		return nil, errors.New("host rework: replacement contains no changed paths")
	}
	changed := strings.Split(strings.TrimSpace(diff), "\n")
	for _, changedPath := range changed {
		if !pathAllowed(changedPath, session.AllowedScope) {
			return nil, fmt.Errorf("host rework: changed path %q is outside the allowed scope", changedPath)
		}
	}
	return changed, nil
}

func pathAllowed(changed string, allowed []string) bool {
	changed = strings.TrimSpace(strings.ReplaceAll(changed, "\\", "/"))
	for _, raw := range allowed {
		scope := strings.TrimSuffix(raw, "/")
		if changed == scope || strings.HasPrefix(changed, scope+"/") {
			return true
		}
	}
	return false
}

// AbandonHostRework releases a bounded host session back to normal
// orchestrated implementation without creating a worker or guessing a repair.
func (o *Orchestrator) AbandonHostRework(req AbandonHostReworkRequest) (*model.HostReworkSession, error) {
	o.deliveryMu.Lock()
	defer o.deliveryMu.Unlock()
	if req.TaskID == uuid.Nil || req.SessionID == uuid.Nil || req.ObservedStateVersion == 0 ||
		strings.TrimSpace(req.Actor) == "" || req.Actor == "user" || strings.TrimSpace(req.Source) == "" ||
		strings.TrimSpace(req.Reason) == "" || strings.TrimSpace(req.IdempotencyKey) == "" {
		return nil, errors.New("abandon host rework: task, session, observed version, actor, source, reason, and idempotency key are required")
	}
	requestHash, err := hashRequest(req)
	if err != nil {
		return nil, err
	}
	var replay model.HostReworkSession
	if err := o.db.Where("terminal_idempotency_key = ?", req.IdempotencyKey).First(&replay).Error; err == nil {
		if replay.TerminalRequestHash != requestHash {
			return nil, ErrIdempotencyConflict
		}
		return &replay, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var session model.HostReworkSession
	err = o.db.Transaction(func(tx *gorm.DB) error {
		var task model.Task
		if err := tx.First(&task, "id = ?", req.TaskID).Error; err != nil {
			return err
		}
		if err := tx.First(&session, "id = ? AND task_id = ?", req.SessionID, task.ID).Error; err != nil {
			return err
		}
		if task.Status != model.StatusHostRework || task.StateVersion != req.ObservedStateVersion || session.Disposition != model.HostReworkActive {
			return fmt.Errorf("%w: host rework task/session is no longer current", state.ErrStaleTransition)
		}
		if session.OwnerActor != req.Actor {
			return errors.New("host rework: only the owning actor may abandon")
		}
		now := time.Now()
		key := req.IdempotencyKey
		res := tx.Model(&model.HostReworkSession{}).Where("id = ? AND disposition = ?", session.ID, model.HostReworkActive).
			Updates(map[string]any{
				"disposition": model.HostReworkOrchestrated, "terminal_actor": req.Actor,
				"terminal_reason": req.Reason, "terminal_idempotency_key": &key,
				"terminal_request_hash": requestHash, "finished_at": now, "updated_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("%w: host rework session was already resolved", state.ErrStaleTransition)
		}
		if err := casTaskTransition(tx, &task, model.StatusHostRework, model.StatusInProgress, req.Actor, req.Source,
			"host rework returned to orchestrated implementation", map[string]any{
				"host_rework_session_id": session.ID.String(), "reason": req.Reason,
			}); err != nil {
			return err
		}
		return tx.First(&session, "id = ?", session.ID).Error
	})
	if err != nil {
		return nil, err
	}
	return &session, nil
}
