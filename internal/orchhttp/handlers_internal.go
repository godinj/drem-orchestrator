package orchhttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// ingestRecordType is the discriminant on every record in a POST
// /internal/logs batch. The values mirror the event type names in
// internal/extract so agentmon can emit the same strings it already
// uses internally.
const (
	recordTypeCommit     = "commit"
	recordTypePush       = "push"
	recordTypeTestResult = "test_result"
	recordTypeBuildError = "build_error"
	recordTypeHeartbeat  = "heartbeat"
	recordTypeCrash      = "crash"
	recordTypeToolCall   = "tool_call"
	maxIngestRecords     = 1000
	maxIngestBodyBytes   = 4 * 1024 * 1024 // 4 MiB guard against misbehaving clients
)

// ingestEnvelope captures the common header present on every record
// regardless of type. The remaining type-specific fields live in
// ingestRecord's typed fields below, which the decoder populates per
// discriminant.
type ingestEnvelope struct {
	Type        string    `json:"type"`
	ContainerID string    `json:"container_id"`
	WorkerID    string    `json:"worker_id"`
	Timestamp   time.Time `json:"timestamp"`
}

// handleIngest decodes a POST /internal/logs body and, in a single
// transaction, appends one TaskEvent row per record. Either every
// record lands or none of them do: a bad record in the middle rolls the
// whole batch back so agentmon can retry without fear of duplicates.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	// Cap the body size to protect the orchestrator from an over-eager
	// or compromised agentmon flooding the ingestion endpoint.
	r.Body = http.MaxBytesReader(w, r.Body, maxIngestBodyBytes)
	defer r.Body.Close()

	var req orchdto.IngestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Records) == 0 {
		writeJSON(w, http.StatusAccepted, orchdto.IngestResponse{Accepted: 0})
		return
	}
	if len(req.Records) > maxIngestRecords {
		http.Error(w, fmt.Sprintf("too many records (%d > %d)", len(req.Records), maxIngestRecords), http.StatusBadRequest)
		return
	}

	rows := make([]model.TaskEvent, 0, len(req.Records))
	for i, raw := range req.Records {
		row, err := decodeIngestRecord(raw)
		if err != nil {
			http.Error(w, fmt.Sprintf("record %d: %v", i, err), http.StatusBadRequest)
			return
		}
		rows = append(rows, row)
	}

	if err := s.DB.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if len(rows) == 0 {
			return nil
		}
		return tx.Create(&rows).Error
	}); err != nil {
		writeDBError(w, err)
		return
	}

	writeJSON(w, http.StatusAccepted, orchdto.IngestResponse{Accepted: len(rows)})
}

// decodeIngestRecord turns a raw JSON blob into a ready-to-insert
// TaskEvent. It validates the record's type against the known set and
// returns a 400-friendly error otherwise; it also normalises timestamps
// so a zero-value Timestamp is replaced with the current time.
func decodeIngestRecord(raw []byte) (model.TaskEvent, error) {
	var env ingestEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return model.TaskEvent{}, fmt.Errorf("envelope: %w", err)
	}
	if env.Type == "" {
		return model.TaskEvent{}, fmt.Errorf("missing type")
	}
	if !isKnownRecordType(env.Type) {
		return model.TaskEvent{}, fmt.Errorf("unknown type %q", env.Type)
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return model.TaskEvent{}, fmt.Errorf("payload: %w", err)
	}

	detail, err := ingestDetail(env.Type, payload)
	if err != nil {
		return model.TaskEvent{}, err
	}

	created := env.Timestamp
	if created.IsZero() {
		created = time.Now().UTC()
	}

	// Actor is stored as the worker UUID string so handleWorkerHistory
	// can reuse the same column as its filter. When a record does not
	// name a task (heartbeats, crashes) TaskID stays uuid.Nil so the
	// row is still valid.
	return model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    uuid.Nil,
		EventType: env.Type,
		OldValue:  env.ContainerID,
		NewValue:  detail,
		Actor:     env.WorkerID,
		Details:   model.JSONField(payload),
		CreatedAt: created,
	}, nil
}

// isKnownRecordType reports whether t is one of the discriminated union
// tags accepted by the ingest endpoint. Kept inline rather than in a map
// so that additions require a code change (and tests catching them)
// rather than a config toggle.
func isKnownRecordType(t string) bool {
	switch t {
	case recordTypeCommit, recordTypePush, recordTypeTestResult,
		recordTypeBuildError, recordTypeHeartbeat, recordTypeCrash,
		recordTypeToolCall:
		return true
	default:
		return false
	}
}

// ingestDetail extracts the most useful single-line description for a
// given record type. It is stored in TaskEvent.NewValue so the worker
// history endpoint can surface a friendly summary without consumers
// reaching into Details themselves.
func ingestDetail(recordType string, payload map[string]any) (string, error) {
	switch recordType {
	case recordTypeCommit:
		return stringField(payload, "message"), nil
	case recordTypePush:
		return stringField(payload, "branch") + " -> " + stringField(payload, "remote"), nil
	case recordTypeTestResult:
		return stringField(payload, "summary"), nil
	case recordTypeBuildError:
		return stringField(payload, "message"), nil
	case recordTypeHeartbeat:
		return stringField(payload, "agent_id"), nil
	case recordTypeCrash:
		return stringField(payload, "reason"), nil
	case recordTypeToolCall:
		return stringField(payload, "tool") + ":" + stringField(payload, "target"), nil
	}
	return "", fmt.Errorf("no detail extractor for %q", recordType)
}

// stringField safely extracts a string value from the raw decoded JSON
// map. Non-string values and missing keys both yield the empty string
// so detail extraction cannot error at runtime on a malformed payload.
func stringField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}
