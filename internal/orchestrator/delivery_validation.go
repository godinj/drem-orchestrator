package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

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
	if err := validateVerificationInteractions(req); err != nil {
		return err
	}
	if req.Result == model.VerificationPassed {
		if req.FailureMode != "" || strings.TrimSpace(req.FailureReason) != "" || len(req.AllowedScope) != 0 {
			return errors.New("verify delivery: passing evidence cannot request rework")
		}
		return nil
	}
	if req.FailureMode != model.DeliveryReworkOrchestrated && req.FailureMode != model.DeliveryReworkHostDirect {
		return errors.New("verify delivery: failed evidence requires orchestrated or host_direct rework mode")
	}
	if strings.TrimSpace(req.FailureReason) == "" {
		return errors.New("verify delivery: failed evidence requires a rework reason")
	}
	if req.FailureMode == model.DeliveryReworkHostDirect {
		return validateHostDirectRequest(req.Actor, req.FailureReason, req.AllowedScope, req.HostDirectAttestation)
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
	if req.Mode != model.DeliveryReworkOrchestrated && req.Mode != model.DeliveryReworkHostDirect {
		return errors.New("request delivery rework: mode must be orchestrated or host_direct")
	}
	if req.Mode == model.DeliveryReworkHostDirect {
		return validateHostDirectRequest(req.Actor, req.Reason, req.AllowedScope, req.HostDirectAttestation)
	}
	if len(req.AllowedScope) != 0 {
		return errors.New("request delivery rework: allowed scope is valid only for host_direct mode")
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
