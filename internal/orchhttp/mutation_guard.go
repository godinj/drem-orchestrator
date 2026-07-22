package orchhttp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
)

const (
	mutationActorHeader           = "X-Drem-Actor"
	mutationObservedVersionHeader = "X-Drem-Observed-State-Version"
	mutationIdempotencyHeader     = "Idempotency-Key"
	mutationPending               = "pending"
	mutationSucceeded             = "succeeded"
	mutationFailed                = "failed"
)

// guardTaskMutation gives every legacy task write the same actor, optimistic
// concurrency, and replay contract. The pending record is persisted before
// dispatch, so a process crash can never turn an uncertain write into an
// automatic duplicate.
func (s *Server) guardTaskMutation(operation string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.runGuardedTaskMutation(w, r, operation, next)
	}
}

// Recovery classification is read-only. Only apply requests enter the write
// ledger; malformed bodies continue to be diagnosed by the handler.
func (s *Server) guardRecoveryMutation(operation string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := readAndRestoreBody(r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "read request body: "+err.Error())
			return
		}
		var mode struct {
			Apply bool `json:"apply"`
		}
		if json.Unmarshal(body, &mode) != nil || !mode.Apply {
			next(w, r)
			return
		}
		s.runGuardedTaskMutation(w, r, operation+":"+strings.TrimSpace(r.PathValue("action")), next)
	}
}

func (s *Server) runGuardedTaskMutation(w http.ResponseWriter, r *http.Request, operation string, next http.HandlerFunc) {
	body, err := readAndRestoreBody(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read request body: "+err.Error())
		return
	}
	actor := strings.TrimSpace(r.Header.Get(mutationActorHeader))
	if actor == "" {
		writeJSONError(w, http.StatusBadRequest, mutationActorHeader+" is required")
		return
	}
	if genericMutationActor(actor) {
		writeJSONError(w, http.StatusBadRequest, "a stable, specific mutation actor is required; generic actor "+actor+" is not allowed")
		return
	}
	if bodyActor, present, err := actorFromJSON(body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	} else if present && bodyActor != actor {
		writeJSONError(w, http.StatusBadRequest, "request actor does not match "+mutationActorHeader)
		return
	}

	observed, err := strconv.ParseUint(strings.TrimSpace(r.Header.Get(mutationObservedVersionHeader)), 10, 64)
	if err != nil || observed == 0 {
		writeJSONError(w, http.StatusBadRequest, mutationObservedVersionHeader+" must be a positive integer")
		return
	}
	key := strings.TrimSpace(r.Header.Get(mutationIdempotencyHeader))
	if key == "" || len(key) > 200 {
		writeJSONError(w, http.StatusBadRequest, mutationIdempotencyHeader+" is required and must be at most 200 characters")
		return
	}
	taskID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid task id: "+r.PathValue("id"))
		return
	}
	hash := mutationRequestHash(r, operation, taskID, actor, observed, body)

	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	var existing model.TaskMutationRecord
	err = s.DB.WithContext(r.Context()).Where("idempotency_key = ?", key).First(&existing).Error
	if err == nil {
		if existing.RequestHash != hash {
			writeJSONError(w, http.StatusConflict, "idempotency key was already used for a different mutation")
			return
		}
		if existing.Outcome == mutationPending || existing.CompletedAt == nil {
			writeJSONError(w, http.StatusConflict, "mutation outcome is uncertain; pending claim requires operator reconciliation")
			return
		}
		replayMutationResponse(w, existing)
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		writeDBError(w, err)
		return
	}

	var task model.Task
	if err := s.DB.WithContext(r.Context()).Where("id = ?", taskID).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeJSONError(w, http.StatusNotFound, "task not found")
			return
		}
		writeDBError(w, err)
		return
	}
	if task.StateVersion != observed {
		writeJSONError(w, http.StatusConflict, fmt.Sprintf("observed task state version %d is stale; current version is %d", observed, task.StateVersion))
		return
	}

	record := model.TaskMutationRecord{
		ID: uuid.New(), TaskID: taskID, Operation: operation, Actor: actor,
		ObservedStateVersion: observed, IdempotencyKey: key, RequestHash: hash,
		Outcome: mutationPending, CreatedAt: time.Now(),
	}
	if err := s.DB.WithContext(r.Context()).Create(&record).Error; err != nil {
		writeDBError(w, err)
		return
	}

	capture := newMutationResponseCapture()
	next(capture, r)
	resultVersion := observed
	_ = s.DB.WithContext(r.Context()).Model(&model.Task{}).Where("id = ?", taskID).Pluck("state_version", &resultVersion).Error
	now := time.Now()
	outcome := mutationSucceeded
	if capture.status >= http.StatusBadRequest {
		outcome = mutationFailed
	}
	updates := map[string]any{
		"result_state_version": resultVersion, "outcome": outcome,
		"http_status": capture.status, "response_json": capture.body.String(), "completed_at": now,
	}
	res := s.DB.WithContext(r.Context()).Model(&model.TaskMutationRecord{}).
		Where("id = ? AND outcome = ?", record.ID, mutationPending).Updates(updates)
	if res.Error != nil || res.RowsAffected != 1 {
		slog.Error("orchhttp: mutation result ledger update failed", "task_id", taskID, "operation", operation, "err", res.Error, "rows", res.RowsAffected)
		writeJSONError(w, http.StatusInternalServerError, "mutation outcome is uncertain; pending claim retained for reconciliation")
		return
	}
	capture.flushTo(w)
}

func genericMutationActor(actor string) bool {
	switch strings.ToLower(strings.TrimSpace(actor)) {
	case "user", "operator", "csuite", "dremctl":
		return true
	default:
		return false
	}
}

func readAndRestoreBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		r.Body = io.NopCloser(bytes.NewReader(nil))
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, err
}

func actorFromJSON(body []byte) (string, bool, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return "", false, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", false, err
	}
	value, ok := raw["actor"]
	if !ok {
		return "", false, nil
	}
	var actor string
	if err := json.Unmarshal(value, &actor); err != nil {
		return "", true, err
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return "", false, nil
	}
	return actor, true, nil
}

func mutationRequestHash(r *http.Request, operation string, taskID uuid.UUID, actor string, observed uint64, body []byte) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		r.Method, r.URL.Path, operation, taskID.String(), actor, strconv.FormatUint(observed, 10), string(body),
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

type mutationResponseCapture struct {
	header      http.Header
	status      int
	wroteHeader bool
	body        bytes.Buffer
}

func newMutationResponseCapture() *mutationResponseCapture {
	return &mutationResponseCapture{header: make(http.Header), status: http.StatusOK}
}

func (c *mutationResponseCapture) Header() http.Header { return c.header }
func (c *mutationResponseCapture) WriteHeader(status int) {
	if c.wroteHeader {
		return
	}
	c.status = status
	c.wroteHeader = true
}
func (c *mutationResponseCapture) Write(p []byte) (int, error) {
	c.wroteHeader = true
	return c.body.Write(p)
}

func (c *mutationResponseCapture) flushTo(w http.ResponseWriter) {
	for key, values := range c.header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(c.status)
	_, _ = w.Write(c.body.Bytes())
}

func replayMutationResponse(w http.ResponseWriter, record model.TaskMutationRecord) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Drem-Idempotent-Replay", "true")
	w.WriteHeader(record.HTTPStatus)
	_, _ = io.WriteString(w, record.ResponseJSON)
}
