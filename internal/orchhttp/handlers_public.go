package orchhttp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// defaultLimit is applied when the caller does not specify ?limit= on an
// endpoint that supports pagination. It is intentionally small so default
// responses stay cheap to decode on Kyle's side; callers wanting more pass
// an explicit limit.
const defaultLimit = 100

// maxLimit caps the highest value a caller may request via ?limit=. A
// single HTTP response is never permitted to return more rows than this,
// so a misbehaving client cannot force an unbounded scan.
const maxLimit = 500

// handleListProjects returns the single project this orchestrator serves,
// decorated with a live worker count pulled from the agents table. The
// response is a slice so Kyle can concatenate across orchestrators
// without reshaping.
func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	var count int64
	if err := s.DB.WithContext(r.Context()).Model(&model.Agent{}).
		Where("status = ?", model.AgentWorking).Count(&count).Error; err != nil {
		writeDBError(w, err)
		return
	}
	resp := []orchdto.ProjectDTO{{
		Name:        s.Project.Name,
		Language:    s.Project.Language,
		OrchURL:     s.Project.OrchURL,
		WorkerCount: int(count),
	}}
	writeJSON(w, http.StatusOK, resp)
}

// handleListTasks returns the project's tasks filtered by ?status=,
// ordered newest-first, with limit/offset pagination. An unknown project
// name returns 404 (the orchestrator only serves one project — any other
// name is a client bug).
func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name != s.Project.Name {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	var project model.Project
	err := s.DB.WithContext(r.Context()).Where("name = ?", name).First(&project).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeJSON(w, http.StatusOK, []orchdto.TaskDTO{})
		return
	}
	if err != nil {
		writeDBError(w, err)
		return
	}

	limit, offset := paginationFrom(r)
	q := s.DB.WithContext(r.Context()).
		Where("project_id = ?", project.ID).
		Order("created_at DESC").
		Limit(limit).Offset(offset)
	if status := r.URL.Query().Get("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	var tasks []model.Task
	if err := q.Find(&tasks).Error; err != nil {
		writeDBError(w, err)
		return
	}
	out := make([]orchdto.TaskDTO, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, toTaskDTO(t))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleListWorkers returns all agents associated with this project. The
// orchestrator treats agent and "worker" as synonyms — both refer to
// Claude/opencode sessions running in a container or (pre-migration) a
// tmux pane.
func (s *Server) handleListWorkers(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name != s.Project.Name {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	var project model.Project
	err := s.DB.WithContext(r.Context()).Where("name = ?", name).First(&project).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeJSON(w, http.StatusOK, []orchdto.WorkerDTO{})
		return
	}
	if err != nil {
		writeDBError(w, err)
		return
	}
	var agents []model.Agent
	if err := s.DB.WithContext(r.Context()).
		Where("project_id = ?", project.ID).
		Order("created_at DESC").
		Find(&agents).Error; err != nil {
		writeDBError(w, err)
		return
	}
	out := make([]orchdto.WorkerDTO, 0, len(agents))
	for _, a := range agents {
		out = append(out, toWorkerDTO(a, s.Project.Name))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetWorker returns a single agent by UUID. An invalid UUID or an
// unknown ID both produce 404 — the public API deliberately conflates
// "malformed input" and "not present" so probes cannot enumerate IDs.
func (s *Server) handleGetWorker(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "worker not found", http.StatusNotFound)
		return
	}
	var agent model.Agent
	err = s.DB.WithContext(r.Context()).Where("id = ?", id).First(&agent).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		http.Error(w, "worker not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toWorkerDTO(agent, s.Project.Name))
}

// handleWorkerHistory returns the TaskEvent rows attributable to this
// worker in chronological order. Agentmon appends structured state
// records here via POST /internal/logs; Kyle uses the endpoint to answer
// "what has this worker been doing recently".
func (s *Server) handleWorkerHistory(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "worker not found", http.StatusNotFound)
		return
	}
	// Worker existence is not required to return an empty history — Kyle
	// may legitimately ask about a worker that has already been reaped.
	var events []model.TaskEvent
	if err := s.DB.WithContext(r.Context()).
		Where("actor = ?", id.String()).
		Order("created_at ASC").
		Limit(defaultLimit).
		Find(&events).Error; err != nil {
		writeDBError(w, err)
		return
	}
	entries := make([]orchdto.WorkerHistoryEntry, 0, len(events))
	for _, e := range events {
		entries = append(entries, orchdto.WorkerHistoryEntry{
			Timestamp: e.CreatedAt,
			Kind:      e.EventType,
			Detail:    e.NewValue,
		})
	}
	writeJSON(w, http.StatusOK, orchdto.WorkerHistoryDTO{
		WorkerID: id.String(),
		Events:   entries,
	})
}

// handleListEvents returns the raw TaskEvent stream, optionally filtered
// by ?since=<rfc3339> and bounded by ?limit. Events are returned
// oldest-first so a consumer that polls with since=previous-last-ts can
// cursor forward without missing rows.
func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	limit, _ := paginationFrom(r)
	q := s.DB.WithContext(r.Context()).
		Order("created_at ASC").
		Limit(limit)
	if since := r.URL.Query().Get("since"); since != "" {
		ts, err := time.Parse(time.RFC3339, since)
		if err != nil {
			http.Error(w, "invalid since timestamp", http.StatusBadRequest)
			return
		}
		q = q.Where("created_at >= ?", ts)
	}
	var events []model.TaskEvent
	if err := q.Find(&events).Error; err != nil {
		writeDBError(w, err)
		return
	}
	out := make([]orchdto.EventDTO, 0, len(events))
	for _, e := range events {
		payload, _ := json.Marshal(map[string]any{
			"task_id":   e.TaskID.String(),
			"old_value": e.OldValue,
			"new_value": e.NewValue,
			"actor":     e.Actor,
			"details":   e.Details,
		})
		out = append(out, orchdto.EventDTO{
			Timestamp: e.CreatedAt,
			Type:      e.EventType,
			Payload:   payload,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleLogs proxies `docker logs` via the configured LogStreamer. The
// response is text/plain and uses chunked transfer encoding so callers
// can stream large outputs or ?follow=true tail the container's stdout
// without buffering. When LogStreamer is nil, the endpoint returns 503
// so the TUI can fall back to a friendly message.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.DockerLogs == nil {
		http.Error(w, "log streaming not configured", http.StatusServiceUnavailable)
		return
	}
	container := r.URL.Query().Get("container")
	if container == "" {
		http.Error(w, "container is required", http.StatusBadRequest)
		return
	}
	rc, err := s.DockerLogs.StreamLogs(r.Context(), container)
	if err != nil {
		http.Error(w, "stream logs: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.WriteHeader(http.StatusOK)
	// Best-effort copy — we want partial output to reach the client even
	// if the upstream reader later errors, so errors here are ignored.
	_, _ = io.Copy(flushingWriter{w}, rc)
}

// flushingWriter wraps an http.ResponseWriter and calls Flush after each
// Write so io.Copy streams bytes to the client in real time rather than
// batching them. Non-flushable writers (rare in net/http) silently fall
// back to plain Write.
type flushingWriter struct {
	w http.ResponseWriter
}

// Write forwards p to the wrapped writer and flushes if the writer
// supports http.Flusher. The flush makes chunked-transfer-encoding work
// as a true stream rather than a single deferred chunk.
func (f flushingWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}

// paginationFrom extracts limit and offset from the request's query
// string. Missing or malformed values use defaults rather than erroring
// so dashboards that forget the params still work.
func paginationFrom(r *http.Request) (int, int) {
	limit := defaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}

// toTaskDTO marshals an internal model.Task into the public TaskDTO
// shape. AssignedAgentID is rendered as an empty string when nil so the
// JSON shape stays stable across assigned/unassigned tasks.
func toTaskDTO(t model.Task) orchdto.TaskDTO {
	assigned := ""
	if t.AssignedAgentID != nil {
		assigned = t.AssignedAgentID.String()
	}
	return orchdto.TaskDTO{
		ID:             t.ID.String(),
		Title:          t.Title,
		Status:         string(t.Status),
		CreatedAt:      t.CreatedAt,
		UpdatedAt:      t.UpdatedAt,
		AssignedWorker: assigned,
	}
}

// toWorkerDTO marshals an internal model.Agent into the public WorkerDTO
// shape. HeartbeatAt is the "last I saw you alive" timestamp; a nil
// pointer surfaces as the zero time, which is the agreed sentinel.
func toWorkerDTO(a model.Agent, project string) orchdto.WorkerDTO {
	current := ""
	if a.CurrentTaskID != nil {
		current = a.CurrentTaskID.String()
	}
	hb := time.Time{}
	if a.HeartbeatAt != nil {
		hb = *a.HeartbeatAt
	}
	return orchdto.WorkerDTO{
		ID:            a.ID.String(),
		ContainerID:   a.TmuxSession, // repurposed: post-containerization this holds the container ID
		Project:       project,
		AgentType:     string(a.AgentType),
		Branch:        a.WorktreeBranch,
		Status:        string(a.Status),
		StartedAt:     a.CreatedAt,
		LastHeartbeat: hb,
		CurrentTask:   current,
	}
}

// writeJSON marshals v, sets the Content-Type header, and writes the
// response. JSON marshal errors are surfaced as 500s — they should be
// impossible for the DTO types defined in pkg/orchdto but are handled so
// that a future field addition cannot silently produce an empty body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Response already started — best effort only.
		_, _ = w.Write([]byte(`{"error":"encode"}`))
	}
}

// writeDBError maps a GORM error to an HTTP 500. Errors are logged with
// slog so operators can trace them without the body leaking details to
// clients.
func writeDBError(w http.ResponseWriter, err error) {
	http.Error(w, "database error", http.StatusInternalServerError)
	_ = err // logged at a higher level via the request middleware
}
