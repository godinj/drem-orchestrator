package orchhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/container"
	"github.com/godinj/drem-orchestrator/internal/logging"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// tasksTimeoutLogSampler gates the per-timeout log emitted by
// writeTasksTimeout. A saturated /tasks cap plus a slow SQLite scan
// can produce one timeout log line per shed request; during the
// 2026-04-21 retry storm that would have been ~495/s. Bug E W4.1.
var tasksTimeoutLogSampler = logging.NewSampler(logging.EveryD(time.Second))

// defaultTasksQueryTimeout is the hard ceiling the /tasks handler
// applies to its SQLite list query. Bug E W1.3: without this, a cold
// 4 GiB DB scan stretched the handler to 28 s during the 2026-04-21
// incident — a handler slower than its TUI client's retry timeout is
// never useful, so we fast-fail with 503 instead.
const defaultTasksQueryTimeout = 5 * time.Second

const maxWorkerAttemptFirstErrorLen = 512

var secretEvidencePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)([A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|API_KEY|ACCESS_KEY)[A-Z0-9_]*\s*=\s*)\S+`),
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)([?&](?:token|secret|api_key|access_key)=)[^&\s]+`),
}

// envTasksQueryTimeoutMs tunes the /tasks DB-query ceiling at runtime.
// Unset, empty, or "0" means defaultTasksQueryTimeout. Values are parsed
// as a millisecond count — a Duration string would be nicer but ints
// keep the env-var shape consistent with DREM_ORCH_MAX_INFLIGHT etc.
const envTasksQueryTimeoutMs = "DREM_ORCH_TASKS_QUERY_TIMEOUT_MS"

// tasksQueryTimeout resolves the env override into a Duration,
// returning the default when unset or malformed.
func tasksQueryTimeout() time.Duration {
	raw := os.Getenv(envTasksQueryTimeoutMs)
	if raw == "" {
		return defaultTasksQueryTimeout
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultTasksQueryTimeout
	}
	return time.Duration(n) * time.Millisecond
}

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
	// Bug E W1.3: hard ceiling on the DB query path so a slow SQLite
	// scan cannot stretch handler latency beyond the configured budget.
	// The 503 + log line is what operators see when this fires; clients
	// should retry per the plan's fast-fail contract.
	ctx, cancel := context.WithTimeout(r.Context(), tasksQueryTimeout())
	defer cancel()

	start := time.Now()
	var project model.Project
	err := s.DB.WithContext(ctx).Where("name = ?", name).First(&project).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeJSON(w, http.StatusOK, []orchdto.TaskDTO{})
		return
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			writeTasksTimeout(w, time.Since(start))
			return
		}
		writeDBError(w, err)
		return
	}

	limit, offset := paginationFrom(r)
	q := s.DB.WithContext(ctx).
		Where("project_id = ?", project.ID).
		Order("created_at DESC").
		Limit(limit).Offset(offset)
	if status := r.URL.Query().Get("status"); status != "" {
		q = q.Where("status = ?", status)
	} else if r.URL.Query().Get("include_archived") != "true" {
		q = q.Where("status <> ?", model.StatusCancelled)
	}
	var tasks []model.Task
	if err := q.Find(&tasks).Error; err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			writeTasksTimeout(w, time.Since(start))
			return
		}
		writeDBError(w, err)
		return
	}
	out := make([]orchdto.TaskDTO, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, toTaskDTO(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name != s.Project.Name {
		writeJSONError(w, http.StatusNotFound, "project not found")
		return
	}

	var req struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Actor       string `json:"actor"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Description = strings.TrimSpace(req.Description)
	req.Actor = strings.TrimSpace(req.Actor)
	if req.Title == "" {
		writeJSONError(w, http.StatusBadRequest, "title is required")
		return
	}
	if req.Description == "" {
		writeJSONError(w, http.StatusBadRequest, "description is required")
		return
	}
	if req.Actor == "" {
		req.Actor = "csuite"
	}

	var task model.Task
	err := s.DB.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		var project model.Project
		if err := tx.Where("name = ?", name).First(&project).Error; err != nil {
			return err
		}

		now := time.Now()
		task = model.Task{
			ID:          uuid.New(),
			ProjectID:   project.ID,
			Title:       req.Title,
			Description: req.Description,
			Status:      model.StatusClassifying,
			Category:    model.CategoryStandard,
		}
		if err := tx.Create(&task).Error; err != nil {
			return err
		}

		return tx.Create(&model.TaskEvent{
			ID:        uuid.New(),
			TaskID:    task.ID,
			EventType: "task_created",
			NewValue:  string(model.StatusClassifying),
			Details: model.JSONField{
				"title":       req.Title,
				"description": req.Description,
			},
			Actor:     req.Actor,
			CreatedAt: now,
		}).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeJSONError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeDBError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, toTaskDTO(task))
}

// writeTasksTimeout surfaces a /tasks DB timeout as 503 + Retry-After: 1
// and emits a terse log line with the elapsed duration so operators can
// correlate the shed response with the slow query in drem.log. The
// emission is sampled — at most one per second per site — so a retry
// storm cannot produce the gigabyte-log anti-pattern observed during
// the 2026-04-21 incident.
func writeTasksTimeout(w http.ResponseWriter, elapsed time.Duration) {
	if tasksTimeoutLogSampler.Allow("tasks-timeout") {
		slog.Warn("orchhttp /tasks DB query timeout — shedding request",
			"elapsed", elapsed,
		)
	}
	w.Header().Set("Retry-After", "1")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("tasks query timed out — retry after 1s\n"))
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

// handleTaskAttempts returns worker/container attempts attributable to a task.
// Spawn events are attempt boundaries when present; older rows without spawn
// events are represented by their Agent row so pre-containerization history is
// still visible without adding a new attempts table.
func (s *Server) handleTaskAttempts(w http.ResponseWriter, r *http.Request) {
	taskID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	var task model.Task
	err = s.DB.WithContext(r.Context()).First(&task, "id = ?", taskID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeDBError(w, err)
		return
	}

	var agents []model.Agent
	agentQuery := s.DB.WithContext(r.Context()).Where("current_task_id = ?", taskID)
	if task.AssignedAgentID != nil {
		agentQuery = agentQuery.Or("id = ?", *task.AssignedAgentID)
	}
	if err := agentQuery.Order("created_at ASC").Find(&agents).Error; err != nil {
		writeDBError(w, err)
		return
	}

	agentsByID := make(map[string]model.Agent, len(agents))
	for _, a := range agents {
		agentsByID[a.ID.String()] = a
	}

	var spawns []model.TaskEvent
	if err := s.DB.WithContext(r.Context()).
		Where("task_id = ? AND event_type = ?", taskID, "worker_spawned").
		Order("created_at ASC").
		Find(&spawns).Error; err != nil {
		writeDBError(w, err)
		return
	}

	var durableAttempts []model.WorkerAttempt
	if err := s.DB.WithContext(r.Context()).
		Where("task_id = ?", taskID).
		Order("created_at ASC").
		Find(&durableAttempts).Error; err != nil {
		writeDBError(w, err)
		return
	}

	out := make([]orchdto.WorkerAttemptDTO, 0, len(durableAttempts)+len(spawns)+len(agents))
	coveredAgents := map[string]struct{}{}
	coveredAttemptIDs := map[string]struct{}{}
	for _, attempt := range durableAttempts {
		agent, hasAgent := model.Agent{}, false
		if attempt.AgentID != nil {
			agent, hasAgent = agentsByID[attempt.AgentID.String()]
			coveredAgents[attempt.AgentID.String()] = struct{}{}
		}
		coveredAttemptIDs[attempt.ID.String()] = struct{}{}
		out = append(out, toWorkerAttemptDTOFromDurable(attempt, agent, hasAgent))
	}
	for _, e := range spawns {
		if _, ok := coveredAttemptIDs[stringField(e.Details, "attempt_id")]; ok {
			continue
		}
		agentID := stringField(e.Details, "agent_id")
		agent, ok := agentsByID[agentID]
		if agentID != "" {
			coveredAgents[agentID] = struct{}{}
		}
		out = append(out, toWorkerAttemptDTOFromSpawn(e, agent, ok))
	}
	for _, a := range agents {
		if _, ok := coveredAgents[a.ID.String()]; ok {
			continue
		}
		out = append(out, toWorkerAttemptDTOFromAgent(taskID, a))
	}
	if len(out) > 0 {
		var failureEvents []model.TaskEvent
		if err := s.DB.WithContext(r.Context()).
			Where("task_id = ? AND event_type IN ?", taskID, []string{recordTypeCrash, recordTypeBuildError, recordTypeTestResult}).
			Order("created_at ASC").
			Find(&failureEvents).Error; err != nil {
			writeDBError(w, err)
			return
		}
		applyFailureEvidence(out, failureEvents, task)
	}

	writeJSON(w, http.StatusOK, out)
}

// handleWorkerHistory returns TaskEvent rows attributable to this worker,
// worker label, or container ID in chronological order. Agentmon records
// use the drem.worker_id label as Actor, while orchestrator spawn/death
// events often only know the DB worker UUID or Docker container ID. The
// two-pass selector expansion below bridges those identifiers so a caller
// can paste any ID visible in `dremctl events`.
func (s *Server) handleWorkerHistory(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, "worker not found", http.StatusNotFound)
		return
	}
	scope, err := s.historyScopeForWorker(r.Context(), id)
	if err != nil {
		writeDBError(w, err)
		return
	}
	events, err := s.historyEventsForScope(r.Context(), scope)
	if err != nil {
		writeDBError(w, err)
		return
	}

	entries := make([]orchdto.WorkerHistoryEntry, 0, len(events))
	for _, e := range events {
		details, _ := json.Marshal(e.Details)
		entries = append(entries, orchdto.WorkerHistoryEntry{
			Timestamp: e.CreatedAt,
			Kind:      e.EventType,
			Detail:    e.NewValue,
			Details:   details,
		})
	}
	writeJSON(w, http.StatusOK, orchdto.WorkerHistoryDTO{
		WorkerID: id,
		Events:   entries,
	})
}

type workerHistoryScope struct {
	selectors map[string]struct{}
	taskIDs   map[uuid.UUID]struct{}
}

func (s *Server) historyScopeForWorker(ctx context.Context, id string) (workerHistoryScope, error) {
	scope := workerHistoryScope{selectors: map[string]struct{}{}, taskIDs: map[uuid.UUID]struct{}{}}
	addSelector(scope.selectors, id)
	if parsed, err := uuid.Parse(id); err == nil {
		var agent model.Agent
		if err := s.DB.WithContext(ctx).First(&agent, "id = ?", parsed).Error; err == nil {
			addSelector(scope.selectors, agent.ID.String())
			addSelector(scope.selectors, agent.Name)
			addSelector(scope.selectors, agent.TmuxSession)
			if agent.CurrentTaskID != nil {
				scope.taskIDs[*agent.CurrentTaskID] = struct{}{}
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return scope, err
		}
	}

	var spawns []model.TaskEvent
	if err := s.DB.WithContext(ctx).
		Where("event_type = ? AND details LIKE ?", "worker_spawned", "%"+id+"%").
		Order("created_at DESC").
		Limit(defaultLimit).
		Find(&spawns).Error; err != nil {
		return scope, err
	}
	for _, spawn := range spawns {
		if !eventMatchesSelectors(spawn, map[string]struct{}{id: {}}) {
			continue
		}
		addSelector(scope.selectors, spawn.Actor)
		addSelector(scope.selectors, spawn.OldValue)
		addSelector(scope.selectors, stringField(spawn.Details, "agent_id"))
		addSelector(scope.selectors, stringField(spawn.Details, "worker_id"))
		addSelector(scope.selectors, stringField(spawn.Details, "container_id"))
		if spawn.TaskID != uuid.Nil {
			scope.taskIDs[spawn.TaskID] = struct{}{}
		}
	}
	return scope, nil
}

func (s *Server) historyEventsForScope(ctx context.Context, scope workerHistoryScope) ([]model.TaskEvent, error) {
	q := s.DB.WithContext(ctx).Order("created_at ASC").Limit(defaultLimit)
	selectorList := mapKeys(scope.selectors)
	taskIDList := uuidMapKeys(scope.taskIDs)
	if len(selectorList) == 0 && len(taskIDList) == 0 {
		return []model.TaskEvent{}, nil
	}
	if len(selectorList) > 0 && len(taskIDList) > 0 {
		q = q.Where("actor IN ? OR old_value IN ? OR task_id IN ?", selectorList, selectorList, taskIDList)
	} else if len(selectorList) > 0 {
		q = q.Where("actor IN ? OR old_value IN ?", selectorList, selectorList)
	} else {
		q = q.Where("task_id IN ?", taskIDList)
	}

	var candidates []model.TaskEvent
	if err := q.Find(&candidates).Error; err != nil {
		return nil, err
	}
	events := make([]model.TaskEvent, 0, len(candidates))
	for _, e := range candidates {
		_, taskScoped := scope.taskIDs[e.TaskID]
		if eventMatchesSelectors(e, scope.selectors) || (taskScoped && e.EventType == "worker_spawned") {
			events = append(events, e)
		}
	}
	return events, nil
}

func addSelector(selectors map[string]struct{}, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		selectors[value] = struct{}{}
	}
}

// handleListEvents returns the raw TaskEvent stream, optionally filtered
// by ?since=<rfc3339> and bounded by ?limit. Without since, it returns
// newest-first so operator-facing CLIs show current activity by default.
// With since, it returns oldest-first so polling consumers can cursor
// forward without missing rows.
func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	limit, _ := paginationFrom(r)
	q := s.DB.WithContext(r.Context()).Limit(limit)
	if since := r.URL.Query().Get("since"); since != "" {
		ts, err := time.Parse(time.RFC3339, since)
		if err != nil {
			http.Error(w, "invalid since timestamp", http.StatusBadRequest)
			return
		}
		q = q.Where("created_at >= ?", ts).Order("created_at ASC")
	} else {
		q = q.Order("created_at DESC")
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
	containerID := strings.TrimSpace(r.URL.Query().Get("container"))
	if attemptID := strings.TrimSpace(r.URL.Query().Get("attempt")); attemptID != "" {
		resolved, err := s.containerForAttempt(r.Context(), attemptID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(w, "attempt not found", http.StatusNotFound)
			return
		}
		if err != nil {
			writeDBError(w, err)
			return
		}
		containerID = resolved
	}
	if containerID == "" {
		http.Error(w, "container or attempt is required", http.StatusBadRequest)
		return
	}
	follow, err := parseBoolQuery(r, "follow")
	if err != nil {
		http.Error(w, "follow must be a boolean", http.StatusBadRequest)
		return
	}
	since, err := parseTimeQuery(r, "since")
	if err != nil {
		http.Error(w, "since must be RFC3339", http.StatusBadRequest)
		return
	}
	rc, err := s.DockerLogs.StreamLogs(r.Context(), containerID, container.LogOptions{Since: since, Follow: follow})
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

func (s *Server) containerForAttempt(ctx context.Context, id string) (string, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return "", gorm.ErrRecordNotFound
	}
	var attempt model.WorkerAttempt
	if err := s.DB.WithContext(ctx).First(&attempt, "id = ?", parsed).Error; err == nil {
		if strings.TrimSpace(attempt.ContainerID) == "" {
			return "", gorm.ErrRecordNotFound
		}
		return attempt.ContainerID, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}

	var spawn model.TaskEvent
	if err := s.DB.WithContext(ctx).First(&spawn, "id = ? AND event_type = ?", parsed, "worker_spawned").Error; err != nil {
		return "", err
	}
	containerID := strings.TrimSpace(stringField(spawn.Details, "container_id"))
	if containerID == "" {
		return "", gorm.ErrRecordNotFound
	}
	return containerID, nil
}

func parseBoolQuery(r *http.Request, key string) (bool, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return false, nil
	}
	return strconv.ParseBool(raw)
}

func parseTimeQuery(r *http.Request, key string) (time.Time, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, raw)
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

func toTaskCommentDTO(c model.TaskComment) orchdto.TaskCommentDTO {
	return orchdto.TaskCommentDTO{
		ID:        c.ID.String(),
		TaskID:    c.TaskID.String(),
		Author:    c.Author,
		Body:      c.Body,
		CreatedAt: c.CreatedAt,
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
		ID:                   a.ID.String(),
		ContainerID:          a.TmuxSession, // repurposed: post-containerization this holds the container ID
		Project:              project,
		AgentType:            string(a.AgentType),
		Branch:               a.WorktreeBranch,
		Status:               string(a.Status),
		StartedAt:            a.CreatedAt,
		LastHeartbeat:        hb,
		CurrentTask:          current,
		Provider:             a.Provider,
		ModelID:              a.ModelID,
		Effort:               a.Effort,
		CompletedAt:          a.CompletedAt,
		ExitReason:           a.ExitReason,
		TotalCostUSD:         a.TotalCostUSD,
		FinalContextPct:      a.FinalContextPct,
		TokensIn:             a.TokensIn,
		TokensOut:            a.TokensOut,
		ConstraintViolations: a.ConstraintViolations,
	}
}

func toWorkerAttemptDTOFromAgent(taskID uuid.UUID, a model.Agent) orchdto.WorkerAttemptDTO {
	hb := time.Time{}
	if a.HeartbeatAt != nil {
		hb = *a.HeartbeatAt
	}
	return orchdto.WorkerAttemptDTO{
		AttemptID:            a.ID.String(),
		TaskID:               taskID.String(),
		WorkerID:             a.ID.String(),
		AgentID:              a.ID.String(),
		ContainerID:          a.TmuxSession,
		WorkerLabel:          a.Name,
		AgentType:            string(a.AgentType),
		Branch:               a.WorktreeBranch,
		Provider:             a.Provider,
		ModelID:              a.ModelID,
		Effort:               a.Effort,
		Status:               string(a.Status),
		StartedAt:            a.CreatedAt,
		CompletedAt:          a.CompletedAt,
		LastHeartbeat:        hb,
		ExitReason:           a.ExitReason,
		TokensIn:             a.TokensIn,
		TokensOut:            a.TokensOut,
		TotalCostUSD:         a.TotalCostUSD,
		FinalContextPct:      a.FinalContextPct,
		ConstraintViolations: a.ConstraintViolations,
	}
}

func toWorkerAttemptDTOFromDurable(attempt model.WorkerAttempt, a model.Agent, hasAgent bool) orchdto.WorkerAttemptDTO {
	d := orchdto.WorkerAttemptDTO{
		AttemptID:   attempt.ID.String(),
		TaskID:      attempt.TaskID.String(),
		WorkerID:    attempt.WorkerID,
		ContainerID: attempt.ContainerID,
		AgentType:   attempt.AgentType,
		StartedAt:   attempt.CreatedAt,
	}
	if attempt.AgentID != nil {
		d.AgentID = attempt.AgentID.String()
	}
	if hasAgent {
		fromAgent := toWorkerAttemptDTOFromAgent(attempt.TaskID, a)
		fromAgent.AttemptID = d.AttemptID
		fromAgent.WorkerID = firstNonEmpty(d.WorkerID, fromAgent.WorkerID)
		fromAgent.AgentID = firstNonEmpty(d.AgentID, fromAgent.AgentID)
		fromAgent.ContainerID = firstNonEmpty(d.ContainerID, fromAgent.ContainerID)
		fromAgent.AgentType = firstNonEmpty(d.AgentType, fromAgent.AgentType)
		fromAgent.StartedAt = d.StartedAt
		return fromAgent
	}
	return d
}

func toWorkerAttemptDTOFromSpawn(e model.TaskEvent, a model.Agent, hasAgent bool) orchdto.WorkerAttemptDTO {
	d := orchdto.WorkerAttemptDTO{
		AttemptID:   firstNonEmpty(stringField(e.Details, "attempt_id"), e.ID.String()),
		TaskID:      e.TaskID.String(),
		WorkerID:    firstNonEmpty(stringField(e.Details, "worker_id"), stringField(e.Details, "agent_id")),
		AgentID:     stringField(e.Details, "agent_id"),
		ContainerID: stringField(e.Details, "container_id"),
		WorkerLabel: firstNonEmpty(stringField(e.Details, "worker_label"), stringField(e.Details, "worker_id")),
		AgentType:   firstNonEmpty(stringField(e.Details, "agent_type"), e.NewValue),
		StartedAt:   e.CreatedAt,
	}
	if hasAgent {
		fromAgent := toWorkerAttemptDTOFromAgent(e.TaskID, a)
		fromAgent.AttemptID = d.AttemptID
		fromAgent.WorkerID = firstNonEmpty(d.WorkerID, fromAgent.WorkerID)
		fromAgent.AgentID = firstNonEmpty(d.AgentID, fromAgent.AgentID)
		fromAgent.ContainerID = firstNonEmpty(d.ContainerID, fromAgent.ContainerID)
		fromAgent.WorkerLabel = firstNonEmpty(d.WorkerLabel, fromAgent.WorkerLabel)
		fromAgent.AgentType = firstNonEmpty(d.AgentType, fromAgent.AgentType)
		fromAgent.StartedAt = d.StartedAt
		return fromAgent
	}
	return d
}

func applyFailureEvidence(attempts []orchdto.WorkerAttemptDTO, events []model.TaskEvent, task model.Task) {
	taskFailure := stringField(task.Context, "failure_reason")
	for i := range attempts {
		if classification, firstError := evidenceFromExitReason(attempts[i].ExitReason); classification != "" {
			attempts[i].FailureClassification = classification
			attempts[i].FirstError = boundFailureEvidence(firstError)
		}
		for _, e := range events {
			if len(attempts) > 1 && !attemptMatchesEvent(attempts[i], e) {
				continue
			}
			classification, firstError := evidenceFromEvent(e)
			if classification == "" {
				continue
			}
			attempts[i].FailureClassification = classification
			attempts[i].FirstError = boundFailureEvidence(firstError)
			break
		}
		if attempts[i].FailureClassification == "" && taskFailure != "" {
			attempts[i].FailureClassification = "task_failure"
			attempts[i].FirstError = boundFailureEvidence(taskFailure)
		}
	}
}

func evidenceFromExitReason(reason string) (string, string) {
	reason = strings.TrimSpace(reason)
	if reason == "" || reason == "success" || reason == "completed" {
		return "", ""
	}
	return "exit_reason", reason
}

func evidenceFromEvent(e model.TaskEvent) (string, string) {
	switch e.EventType {
	case recordTypeCrash:
		return "crash", firstNonEmpty(stringField(e.Details, "reason"), e.NewValue)
	case recordTypeBuildError:
		return "build_error", firstNonEmpty(stringField(e.Details, "message"), e.NewValue)
	case recordTypeTestResult:
		if success, ok := boolField(e.Details, "success"); ok && success {
			return "", ""
		}
		return "test_failure", firstNonEmpty(stringField(e.Details, "summary"), e.NewValue)
	}
	return "", ""
}

func attemptMatchesEvent(a orchdto.WorkerAttemptDTO, e model.TaskEvent) bool {
	selectors := map[string]struct{}{}
	addSelector(selectors, a.WorkerID)
	addSelector(selectors, a.AgentID)
	addSelector(selectors, a.ContainerID)
	addSelector(selectors, a.WorkerLabel)
	return eventMatchesSelectors(e, selectors)
}

func eventMatchesSelectors(e model.TaskEvent, selectors map[string]struct{}) bool {
	if len(selectors) == 0 {
		return false
	}
	if _, ok := selectors[e.Actor]; ok && e.Actor != "" {
		return true
	}
	if _, ok := selectors[e.OldValue]; ok && e.OldValue != "" {
		return true
	}
	for _, key := range []string{"agent_id", "worker_id", "container_id", "worker_label"} {
		if v := stringField(e.Details, key); v != "" {
			if _, ok := selectors[v]; ok {
				return true
			}
		}
	}
	return false
}

func boundFailureEvidence(s string) string {
	s = strings.TrimSpace(s)
	for _, pattern := range secretEvidencePatterns {
		s = pattern.ReplaceAllString(s, `${1}[REDACTED]`)
	}
	if len(s) <= maxWorkerAttemptFirstErrorLen {
		return s
	}
	return s[:maxWorkerAttemptFirstErrorLen]
}

func boolField(m map[string]any, key string) (bool, bool) {
	v, ok := m[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

func mapKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if strings.TrimSpace(k) != "" {
			out = append(out, k)
		}
	}
	return out
}

func uuidMapKeys(m map[uuid.UUID]struct{}) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for k := range m {
		if k != uuid.Nil {
			out = append(out, k)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
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
