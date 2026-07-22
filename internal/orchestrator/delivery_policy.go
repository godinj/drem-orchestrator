package orchestrator

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func (o *Orchestrator) processVerificationReady(task *model.Task) error {
	if o.deliveryPolicy.VerificationPolicy == model.VerificationExternalAck {
		return nil
	}
	artifact, err := currentArtifact(o.db, task.ID)
	if err != nil {
		return err
	}
	commands := decodeCommandEvidence(artifact.PreliminaryEvidence)
	if len(commands) == 0 {
		return fmt.Errorf("local automated verification: artifact %s has no command evidence", artifact.ID)
	}
	_, err = o.VerifyDelivery(VerifyDeliveryRequest{
		TaskID:                 task.ID,
		ObservedStateVersion:   task.StateVersion,
		ArtifactVersion:        artifact.ArtifactVersion,
		CommitSHA:              artifact.CommitSHA,
		Actor:                  "orchestrator",
		Source:                 "local_automated_verification",
		EnvironmentFingerprint: runtime.GOOS + "/" + runtime.GOARCH,
		CommandEvidence:        commands,
		Result:                 model.VerificationPassed,
		Notes:                  "preliminary gate accepted as local automated verification",
		IdempotencyKey:         "local-verification:" + artifact.ID.String(),
	})
	return err
}

func (o *Orchestrator) processIntegrationReady(task *model.Task) error {
	if o.deliveryPolicy.IntegrationPolicy == model.IntegrationPrepareBranch {
		return nil
	}
	artifact, err := currentArtifact(o.db, task.ID)
	if err != nil {
		return err
	}
	var verification model.VerificationRecord
	if err := o.db.Where("delivery_artifact_id = ? AND result = ?", artifact.ID, model.VerificationPassed).
		Order("created_at DESC").First(&verification).Error; err != nil {
		return fmt.Errorf("auto integration: passing verification: %w", err)
	}
	_, err = o.AuthorizeIntegration(IntegrateDeliveryRequest{
		TaskID:               task.ID,
		ObservedStateVersion: task.StateVersion,
		ArtifactVersion:      artifact.ArtifactVersion,
		CommitSHA:            artifact.CommitSHA,
		VerificationRecordID: verification.ID,
		Actor:                "orchestrator",
		Source:               "auto_merge_policy",
		IdempotencyKey:       "auto-integration:" + artifact.ID.String(),
	})
	return err
}

func decodeCommandEvidence(field model.JSONField) []CommandEvidence {
	// JSONField represents objects, while command evidence is stored as an
	// array. Older JSONField decoding cannot preserve a top-level array, so the
	// encoder wraps command lists under "commands".
	commandsRaw, ok := field["commands"]
	if !ok {
		return nil
	}
	encoded, err := json.Marshal(commandsRaw)
	if err != nil {
		return nil
	}
	var commands []CommandEvidence
	if err := json.Unmarshal(encoded, &commands); err != nil {
		return nil
	}
	return commands
}
