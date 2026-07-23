package orchhttp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

const codexGoalUsageSource = "codex_get_goal"

func (s *Server) handleSubmitCodexGoalUsage(w http.ResponseWriter, r *http.Request) {
	if !s.requireProject(w, r) {
		return
	}
	task, ok := s.loadTaskForMutation(w, r)
	if !ok {
		return
	}
	var req orchdto.SubmitCodexGoalUsageRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	actor, ok := requireMatchingDeliveryActor(w, r, req.Actor)
	if !ok {
		return
	}
	req.Actor = actor
	if err := normalizeCodexGoalUsageRequest(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	requestHash, err := codexGoalUsageRequestHash(task.ID, req)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "hash Codex goal usage")
		return
	}

	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	var existing model.CodexGoalUsage
	err = s.DB.WithContext(r.Context()).Where("idempotency_key = ?", req.IdempotencyKey).First(&existing).Error
	if err == nil {
		if existing.TaskID != task.ID || existing.RequestHash != requestHash {
			writeJSONError(w, http.StatusConflict, "idempotency key was already used for different Codex goal usage")
			return
		}
		w.Header().Set("X-Drem-Idempotent-Replay", "true")
		writeJSON(w, http.StatusOK, codexGoalUsageDTO(existing))
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		writeDBError(w, err)
		return
	}
	capturedAt := req.UsageCapturedAt.UTC()
	if capturedAt.IsZero() {
		capturedAt = time.Now().UTC()
	}
	record := model.CodexGoalUsage{
		ID: uuid.New(), TaskID: task.ID, Actor: req.Actor, ThreadID: req.ThreadID,
		GoalObjective: req.GoalObjective, GoalStatus: req.GoalStatus,
		TokensUsed: req.TokensUsed, ElapsedMS: req.ElapsedMS, Source: codexGoalUsageSource,
		IdempotencyKey: req.IdempotencyKey, RequestHash: requestHash, UsageCapturedAt: capturedAt,
	}
	if err := s.DB.WithContext(r.Context()).Create(&record).Error; err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, codexGoalUsageDTO(record))
}

func normalizeCodexGoalUsageRequest(req *orchdto.SubmitCodexGoalUsageRequest) error {
	req.Actor = strings.TrimSpace(req.Actor)
	req.ThreadID = strings.TrimSpace(req.ThreadID)
	req.GoalObjective = strings.TrimSpace(req.GoalObjective)
	req.GoalStatus = strings.ToLower(strings.TrimSpace(req.GoalStatus))
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if !strings.HasPrefix(req.Actor, "codex:") || strings.TrimPrefix(req.Actor, "codex:") == "" {
		return errors.New("Codex goal usage actor must use codex:<thread-id>")
	}
	if req.ThreadID == "" {
		req.ThreadID = strings.TrimPrefix(req.Actor, "codex:")
	}
	if req.ThreadID != strings.TrimPrefix(req.Actor, "codex:") {
		return errors.New("thread_id must match the codex actor")
	}
	if req.GoalObjective == "" || len(req.GoalObjective) > 4000 {
		return errors.New("goal_objective is required and must be at most 4000 characters")
	}
	if req.GoalStatus != "complete" && req.GoalStatus != "blocked" {
		return errors.New("goal_status must be complete or blocked")
	}
	if req.TokensUsed < 0 {
		return errors.New("tokens_used must be non-negative")
	}
	if req.ElapsedMS <= 0 {
		return errors.New("elapsed_ms must be positive")
	}
	if req.IdempotencyKey == "" || len(req.IdempotencyKey) > 200 {
		return errors.New("idempotency_key is required and must be at most 200 characters")
	}
	if !req.UsageCapturedAt.IsZero() && req.UsageCapturedAt.After(time.Now().Add(5*time.Minute)) {
		return errors.New("usage_captured_at cannot be in the future")
	}
	return nil
}

func codexGoalUsageRequestHash(taskID uuid.UUID, req orchdto.SubmitCodexGoalUsageRequest) (string, error) {
	encoded, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(taskID.String()+"\x00"), encoded...))
	return hex.EncodeToString(sum[:]), nil
}

func codexGoalUsageDTO(record model.CodexGoalUsage) orchdto.CodexGoalUsageDTO {
	return orchdto.CodexGoalUsageDTO{
		ID: record.ID.String(), TaskID: record.TaskID.String(), Actor: record.Actor,
		ThreadID: record.ThreadID, GoalObjective: record.GoalObjective, GoalStatus: record.GoalStatus,
		TokensUsed: record.TokensUsed, ElapsedMS: record.ElapsedMS, Source: record.Source,
		UsageCapturedAt: record.UsageCapturedAt, CreatedAt: record.CreatedAt,
	}
}
