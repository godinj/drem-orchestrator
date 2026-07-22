package orchhttp

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/orchestrator"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

type deliveryOrchestrator interface {
	VerifyDelivery(orchestrator.VerifyDeliveryRequest) (*model.VerificationRecord, error)
	AuthorizeIntegration(orchestrator.IntegrateDeliveryRequest) (*model.IntegrationAuthorization, error)
	RequestDeliveryRework(orchestrator.RequestDeliveryReworkRequest) (*model.DeliveryReworkRecord, error)
}

func (s *Server) handleRequestDeliveryRework(w http.ResponseWriter, r *http.Request) {
	orch, ok := s.Orch.(deliveryOrchestrator)
	if !ok {
		writeJSONError(w, http.StatusServiceUnavailable, "delivery mutations not configured")
		return
	}
	if !s.requireProject(w, r) {
		return
	}
	task, ok := s.loadTaskForMutation(w, r)
	if !ok {
		return
	}
	var req orchdto.RequestDeliveryReworkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	actor, ok := requireMatchingDeliveryActor(w, r, req.Actor)
	if !ok {
		return
	}
	record, err := orch.RequestDeliveryRework(orchestrator.RequestDeliveryReworkRequest{
		TaskID: task.ID, ObservedStateVersion: req.ObservedStateVersion,
		ArtifactVersion: req.ArtifactVersion, CommitSHA: req.CommitSHA,
		Actor: actor, Source: "http_delivery_api", Reason: req.Reason,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		writeDeliveryMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, orchdto.DeliveryReworkRecordDTO{
		ID: record.ID.String(), TaskID: record.TaskID.String(), ArtifactVersion: record.ArtifactVersion,
		CommitSHA: record.CommitSHA, Reason: record.Reason,
	})
}

func (s *Server) handleGetDeliveryArtifact(w http.ResponseWriter, r *http.Request) {
	if !s.requireProject(w, r) {
		return
	}
	task, ok := s.loadTaskForMutation(w, r)
	if !ok {
		return
	}
	var artifact model.DeliveryArtifact
	if err := s.DB.Where("task_id = ? AND invalidated_at IS NULL", task.ID).First(&artifact).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeJSONError(w, http.StatusNotFound, "current delivery artifact not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "load delivery artifact")
		return
	}
	envelope := orchdto.DeliveryEnvelopeDTO{Task: toTaskDTO(task), Artifact: toDeliveryArtifactDTO(artifact)}
	var verification model.VerificationRecord
	if err := s.DB.Where("delivery_artifact_id = ?", artifact.ID).Order("created_at DESC").First(&verification).Error; err == nil {
		dto := toVerificationRecordDTO(verification)
		envelope.LatestVerification = &dto
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		writeJSONError(w, http.StatusInternalServerError, "load verification record")
		return
	}
	writeJSON(w, http.StatusOK, envelope)
}

func (s *Server) handleVerifyDelivery(w http.ResponseWriter, r *http.Request) {
	orch, ok := s.Orch.(deliveryOrchestrator)
	if !ok {
		writeJSONError(w, http.StatusServiceUnavailable, "delivery mutations not configured")
		return
	}
	if !s.requireProject(w, r) {
		return
	}
	task, ok := s.loadTaskForMutation(w, r)
	if !ok {
		return
	}
	var req orchdto.VerifyDeliveryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	result, err := model.ParseVerificationResult(req.Result)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	actor, ok := requireMatchingDeliveryActor(w, r, req.Actor)
	if !ok {
		return
	}
	commands := make([]orchestrator.CommandEvidence, 0, len(req.Commands))
	for _, command := range req.Commands {
		commands = append(commands, orchestrator.CommandEvidence{
			Command: command.Command, Passed: command.Passed, ExitCode: command.ExitCode,
			Output: command.Output, StartedAt: command.StartedAt, FinishedAt: command.FinishedAt,
		})
	}
	record, err := orch.VerifyDelivery(orchestrator.VerifyDeliveryRequest{
		TaskID: task.ID, ObservedStateVersion: req.ObservedStateVersion,
		ArtifactVersion: req.ArtifactVersion, CommitSHA: req.CommitSHA,
		Actor: actor, Source: "http_delivery_api", EnvironmentFingerprint: req.EnvironmentFingerprint,
		CommandEvidence: commands, BinarySHA256: req.BinarySHA256, Result: result,
		Notes: req.Notes, IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		writeDeliveryMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toVerificationRecordDTO(*record))
}

func (s *Server) handleIntegrateDelivery(w http.ResponseWriter, r *http.Request) {
	orch, ok := s.Orch.(deliveryOrchestrator)
	if !ok {
		writeJSONError(w, http.StatusServiceUnavailable, "delivery mutations not configured")
		return
	}
	if !s.requireProject(w, r) {
		return
	}
	task, ok := s.loadTaskForMutation(w, r)
	if !ok {
		return
	}
	var req orchdto.IntegrateDeliveryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	verificationID, err := uuid.Parse(req.VerificationRecordID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "verification_record_id must be a UUID")
		return
	}
	actor, ok := requireMatchingDeliveryActor(w, r, req.Actor)
	if !ok {
		return
	}
	auth, err := orch.AuthorizeIntegration(orchestrator.IntegrateDeliveryRequest{
		TaskID: task.ID, ObservedStateVersion: req.ObservedStateVersion,
		ArtifactVersion: req.ArtifactVersion, CommitSHA: req.CommitSHA,
		VerificationRecordID: verificationID, Actor: actor, Source: "http_delivery_api",
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		writeDeliveryMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"integration_authorization_id": auth.ID.String(), "task_id": auth.TaskID.String(),
		"artifact_version": auth.ArtifactVersion, "commit_sha": auth.CommitSHA,
	})
}

func requireMatchingDeliveryActor(w http.ResponseWriter, r *http.Request, bodyActor string) (string, bool) {
	actor, ok := requireMutationActor(w, r)
	if !ok {
		return "", false
	}
	if bodyActor != "" && bodyActor != actor {
		writeJSONError(w, http.StatusBadRequest, "actor does not match X-Drem-Actor")
		return "", false
	}
	return actor, true
}

func writeDeliveryMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, state.ErrStaleTransition), errors.Is(err, orchestrator.ErrStaleArtifact), errors.Is(err, orchestrator.ErrIdempotencyConflict):
		writeJSONError(w, http.StatusConflict, err.Error())
	default:
		writeJSONError(w, http.StatusBadRequest, err.Error())
	}
}

func toDeliveryArtifactDTO(artifact model.DeliveryArtifact) orchdto.DeliveryArtifactDTO {
	return orchdto.DeliveryArtifactDTO{
		ID: artifact.ID.String(), TaskID: artifact.TaskID.String(), ArtifactVersion: artifact.ArtifactVersion,
		Branch: artifact.Branch, CommitSHA: artifact.CommitSHA, BaseBranch: artifact.BaseBranch, BaseSHA: artifact.BaseSHA,
		Preliminary: decodeCommandEvidenceDTO(artifact.PreliminaryEvidence), CreatorActor: artifact.CreatorActor,
		CreatorSource: artifact.CreatorSource, CreatedAt: artifact.CreatedAt,
	}
}

func toVerificationRecordDTO(record model.VerificationRecord) orchdto.VerificationRecordDTO {
	return orchdto.VerificationRecordDTO{
		ID: record.ID.String(), ArtifactID: record.DeliveryArtifactID.String(), ArtifactVersion: record.ArtifactVersion,
		CommitSHA: record.CommitSHA, VerifierActor: record.VerifierActor,
		EnvironmentFingerprint: record.EnvironmentFingerprint, Commands: decodeCommandEvidenceDTO(record.CommandEvidence),
		BinarySHA256: record.BinarySHA256, Result: string(record.Result), Notes: record.Notes, CreatedAt: record.CreatedAt,
	}
}

func decodeCommandEvidenceDTO(field model.JSONField) []orchdto.CommandEvidenceDTO {
	raw, ok := field["commands"]
	if !ok {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var out []orchdto.CommandEvidenceDTO
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil
	}
	return out
}
