package orchhttp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	recordTypeCommit      = "commit"
	recordTypePush        = "push"
	recordTypeTestResult  = "test_result"
	recordTypeBuildError  = "build_error"
	recordTypeHeartbeat   = "heartbeat"
	recordTypeCrash       = "crash"
	recordTypeToolCall    = "tool_call"
	recordTypeMergeResult = "merge_result"
	maxIngestRecords      = 1000
	maxIngestBodyBytes    = 4 * 1024 * 1024 // 4 MiB guard against misbehaving clients
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

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	records, err := decodeIngestBody(body)
	if err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(records) == 0 {
		writeJSON(w, http.StatusAccepted, orchdto.IngestResponse{Accepted: 0})
		return
	}
	if len(records) > maxIngestRecords {
		http.Error(w, fmt.Sprintf("too many records (%d > %d)", len(records), maxIngestRecords), http.StatusBadRequest)
		return
	}

	rows := make([]model.TaskEvent, 0, len(records))
	for i, raw := range records {
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
		if err := enrichIngestTaskIDs(tx, rows); err != nil {
			return err
		}
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
		for i := range rows {
			if err := applyIngestSideEffects(tx, rows[i]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		writeDBError(w, err)
		return
	}

	writeJSON(w, http.StatusAccepted, orchdto.IngestResponse{Accepted: len(rows)})
}

func decodeIngestBody(body []byte) ([]json.RawMessage, error) {
	var req orchdto.IngestRequest
	if err := json.Unmarshal(body, &req); err == nil && req.Records != nil {
		return req.Records, nil
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty body")
	}
	if trimmed[0] != '{' {
		return nil, fmt.Errorf("expected object")
	}
	return []json.RawMessage{append(json.RawMessage(nil), trimmed...)}, nil
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

	taskID := uuid.Nil
	if rawTaskID := stringField(payload, "task_id"); rawTaskID != "" {
		parsed, err := uuid.Parse(rawTaskID)
		if err != nil {
			return model.TaskEvent{}, fmt.Errorf("invalid task_id %q", rawTaskID)
		}
		taskID = parsed
	}

	actor := env.WorkerID
	if actor == "" {
		actor = env.ContainerID
	}
	if actor == "" && env.Type == recordTypeMergeResult {
		actor = "merger"
	}

	return model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    taskID,
		EventType: env.Type,
		OldValue:  env.ContainerID,
		NewValue:  detail,
		Actor:     actor,
		Details:   model.JSONField(payload),
		CreatedAt: created,
	}, nil
}

func enrichIngestTaskIDs(tx *gorm.DB, rows []model.TaskEvent) error {
	for i := range rows {
		if rows[i].TaskID != uuid.Nil {
			continue
		}
		taskID, err := currentTaskIDForIngestEvent(tx, rows[i])
		if err != nil {
			return err
		}
		if taskID != uuid.Nil {
			rows[i].TaskID = taskID
		}
	}
	return nil
}

func currentTaskIDForIngestEvent(tx *gorm.DB, row model.TaskEvent) (uuid.UUID, error) {
	if taskID, err := currentTaskIDForAgent(tx, stringField(row.Details, "agent_id")); err != nil || taskID != uuid.Nil {
		return taskID, err
	}

	selectors := []string{
		stringField(row.Details, "worker_id"),
		stringField(row.Details, "container_id"),
		row.Actor,
		row.OldValue,
	}
	seen := map[string]struct{}{}
	for _, selector := range selectors {
		if selector == "" {
			continue
		}
		if _, ok := seen[selector]; ok {
			continue
		}
		seen[selector] = struct{}{}

		var spawns []model.TaskEvent
		if err := tx.Where("event_type = ? AND details LIKE ?", "worker_spawned", "%"+selector+"%").
			Order("created_at DESC").
			Limit(10).
			Find(&spawns).Error; err != nil {
			return uuid.Nil, err
		}
		for _, spawn := range spawns {
			if stringField(spawn.Details, "worker_id") != selector && stringField(spawn.Details, "container_id") != selector {
				continue
			}
			if taskID, err := currentTaskIDForAgent(tx, stringField(spawn.Details, "agent_id")); err != nil || taskID != uuid.Nil {
				return taskID, err
			}
		}
	}

	return uuid.Nil, nil
}

func currentTaskIDForAgent(tx *gorm.DB, rawAgentID string) (uuid.UUID, error) {
	agentID, err := uuid.Parse(rawAgentID)
	if err != nil {
		return uuid.Nil, nil
	}

	var agent model.Agent
	if err := tx.First(&agent, "id = ?", agentID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return uuid.Nil, nil
		}
		return uuid.Nil, err
	}
	if agent.CurrentTaskID == nil {
		return uuid.Nil, nil
	}
	return *agent.CurrentTaskID, nil
}

// isKnownRecordType reports whether t is one of the discriminated union
// tags accepted by the ingest endpoint. Kept inline rather than in a map
// so that additions require a code change (and tests catching them)
// rather than a config toggle.
func isKnownRecordType(t string) bool {
	switch t {
	case recordTypeCommit, recordTypePush, recordTypeTestResult,
		recordTypeBuildError, recordTypeHeartbeat, recordTypeCrash,
		recordTypeToolCall, recordTypeMergeResult:
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
	case recordTypeMergeResult:
		reason := stringField(payload, "failure_reason")
		if reason == "" {
			if success, _ := payload["success"].(bool); success {
				return "success", nil
			}
			return "failed", nil
		}
		return reason, nil
	}
	return "", fmt.Errorf("no detail extractor for %q", recordType)
}

func applyIngestSideEffects(tx *gorm.DB, row model.TaskEvent) error {
	if row.EventType != recordTypeMergeResult || row.TaskID == uuid.Nil {
		return nil
	}
	var task model.Task
	if err := tx.First(&task, "id = ?", row.TaskID).Error; err != nil {
		return err
	}
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	if v := stringField(row.Details, "merged_sha"); v != "" {
		task.Context["merge_commit"] = v
	}
	if conflicts, ok := stringSliceField(row.Details, "conflicts"); ok {
		task.Context["merge_conflicts"] = conflicts
	}
	if v := stringField(row.Details, "failure_reason"); v != "" {
		task.Context["merge_failure_reason"] = v
	}
	if v := stringField(row.Details, "test_output"); v != "" {
		task.Context["merge_test_output"] = v
	}
	return tx.Model(&task).Update("context", task.Context).Error
}

func stringSliceField(m map[string]any, key string) ([]string, bool) {
	v, ok := m[key]
	if !ok {
		return nil, false
	}
	raw, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		if ok {
			out = append(out, s)
		}
	}
	return out, true
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
