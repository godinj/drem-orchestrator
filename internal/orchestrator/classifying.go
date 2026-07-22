package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/bugreport"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/prompt"
)

// ClassifierOutput represents the structured JSON output produced by a
// classifier agent after exploring the codebase. It contains either a
// successful classification or a clarification request.
type ClassifierOutput struct {
	// Successful classification fields
	Category        string   `json:"category"`
	ComplexityScore int      `json:"complexity_score"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	TargetFiles     []string `json:"target_files"`
	Rationale       string   `json:"rationale"`

	// Clarification request fields
	NeedsClarification bool     `json:"needs_clarification"`
	Questions          []string `json:"questions"`
}

// processClassifyingTasks finds tasks in CLASSIFYING with no assigned agent
// and spawns a classifier agent for each via the runner.
func (o *Orchestrator) processClassifyingTasks() {
	if o.runner == nil {
		return
	}

	// Resolve main worktree — classifiers run read-only against it.
	mainWT, err := o.worktree.MainWorktreePath()
	if err != nil {
		o.logger.Error("classifier: resolve main worktree", "error", err)
		return
	}

	var project model.Project
	if err := o.db.First(&project, "id = ?", o.projectID).Error; err != nil {
		o.logger.Error("classifier: load project", "error", err)
		return
	}

	var tasks []model.Task
	if err := o.db.Where("project_id = ? AND status = ? AND assigned_agent_id IS NULL",
		o.projectID, model.StatusClassifying).Find(&tasks).Error; err != nil {
		o.logger.Error("query classifying tasks", "error", err)
		return
	}

	for i := range tasks {
		task := &tasks[i]

		// Skip tasks parked for human triage — they should not get a new
		// classifier agent spawned.
		if task.Context != nil {
			if ht, ok := task.Context["human_triage"]; ok && ht == true {
				continue
			}
		}

		// Check capacity before spawning the next classifier agent.
		if !o.runner.CanSpawn() {
			break
		}

		classifierPrompt := prompt.Generate(prompt.Opts{
			Task:         task,
			Project:      &project,
			AgentType:    model.AgentClassifier,
			WorktreePath: mainWT,
		})

		ag, err := o.runner.SpawnAgentInWorktree(task, mainWT, model.AgentClassifier, classifierPrompt)
		if err != nil {
			o.logger.Error("spawn classifier agent", "task_id", task.ID, "error", err)
			continue
		}

		task.AssignedAgentID = &ag.ID
		if err := o.db.Save(task).Error; err != nil {
			o.logger.Error("assign classifier agent to task", "task_id", task.ID, "error", err)
			continue
		}

		o.publishAgentStatus(task.ID.String(), ag.ID.String(), string(model.AgentClassifier), string(model.AgentWorking))
		o.logger.Info("spawned classifier agent", "task_id", task.ID, "agent_id", ag.ID)
	}
}

// maxDirectClassifiersPerTick limits how many direct classifier API calls are
// made per tick to prevent tick starvation.
const maxDirectClassifiersPerTick = 3

// processClassifyingTasksDirect handles CLASSIFYING tasks by calling the
// SGLang API directly instead of spawning OpenCode subprocesses. It creates
// a lightweight agent DB record for audit trail and duplicate-dispatch
// prevention, then calls RunDirectClassifier synchronously.
func (o *Orchestrator) processClassifyingTasksDirect() {
	cfg := o.directClassifierCfg
	if cfg == nil {
		return
	}

	// Circuit breaker: skip dispatch when LLM endpoint is unreachable.
	if o.endpointHealth != nil && !o.endpointHealth.IsHealthy() {
		o.logger.Warn("direct classifier: LLM endpoint unhealthy, skipping dispatch")
		return
	}

	// Resolve main worktree — classification output is written there.
	mainWT, err := o.worktree.MainWorktreePath()
	if err != nil {
		o.logger.Error("direct classifier: resolve main worktree", "error", err)
		return
	}

	var tasks []model.Task
	if err := o.db.Where("project_id = ? AND status = ? AND assigned_agent_id IS NULL",
		o.projectID, model.StatusClassifying).Find(&tasks).Error; err != nil {
		o.logger.Error("direct classifier: query classifying tasks", "error", err)
		return
	}

	dispatched := 0
	for i := range tasks {
		if dispatched >= maxDirectClassifiersPerTick {
			break
		}

		task := &tasks[i]

		// Skip tasks parked for human triage.
		if task.Context != nil {
			if ht, ok := task.Context["human_triage"]; ok && ht == true {
				continue
			}
		}

		// Create a lightweight agent DB record for audit trail and to
		// prevent duplicate dispatch on subsequent ticks.
		agentID := uuid.New()
		now := time.Now()
		ag := &model.Agent{
			ID:            agentID,
			ProjectID:     task.ProjectID,
			AgentType:     model.AgentClassifier,
			Name:          fmt.Sprintf("direct-classifier-%s", task.ID.String()[:4]),
			Status:        model.AgentWorking,
			CurrentTaskID: &task.ID,
			WorktreePath:  mainWT,
			Provider:      "sglang-direct",
			ModelID:       cfg.Model,
			HeartbeatAt:   &now,
		}
		if err := o.db.Create(ag).Error; err != nil {
			o.logger.Error("direct classifier: create agent record", "task_id", task.ID, "error", err)
			continue
		}

		task.AssignedAgentID = &ag.ID
		if err := o.db.Save(task).Error; err != nil {
			o.logger.Error("direct classifier: assign agent to task", "task_id", task.ID, "error", err)
			continue
		}

		o.publishAgentStatus(task.ID.String(), ag.ID.String(), string(model.AgentClassifier), string(model.AgentWorking))
		o.logger.Info("direct classifier: classifying task", "task_id", task.ID, "agent_id", ag.ID)

		// Call either the warm drem-classifier container (when configured)
		// or the inline SGLang API path. Both produce the same on-disk
		// classification-<taskID>.json artifact so the downstream completion
		// handler is unchanged.
		var taskCtx map[string]any
		if task.Context != nil {
			taskCtx = task.Context
		}
		tokensIn, tokensOut, classifyErr := o.dispatchClassify(*cfg, task, taskCtx, mainWT)
		if classifyErr != nil {
			o.logger.Error("direct classifier: API call failed", "task_id", task.ID, "error", classifyErr)
			if failErr := o.onClassifierFailed(ag, task); failErr != nil {
				o.logger.Error("direct classifier: on failed handler", "task_id", task.ID, "error", failErr)
			}
			dispatched++
			continue
		}

		// Record token usage on the agent for observability.
		ag.TokensIn = tokensIn
		ag.TokensOut = tokensOut
		_ = o.db.Save(ag).Error

		// Reuse the existing completion handler which reads the classification
		// JSON file and transitions the task.
		if err := o.onClassifierCompleted(ag, task); err != nil {
			o.logger.Error("direct classifier: on completed handler", "task_id", task.ID, "error", err)
		}

		dispatched++
	}
}

// dispatchClassify routes to the warm drem-classifier container when
// o.classifierContainerURL is set; otherwise it falls through to the inline
// agent.RunDirectClassifier path. Both branches write the same
// classification-<taskID>.json artifact in outputDir so onClassifierCompleted
// doesn't need to know which path ran. Returns the prompt/completion token
// counts for agent bookkeeping.
func (o *Orchestrator) dispatchClassify(cfg agent.DirectClassifierConfig, task *model.Task, taskCtx map[string]any, outputDir string) (tokensIn, tokensOut int, err error) {
	if o.classifierContainerURL != "" {
		return o.classifyViaContainer(cfg, task, taskCtx, outputDir)
	}
	result, err := agent.RunDirectClassifier(cfg, task.ID, task.Title, task.Description, taskCtx, outputDir)
	if err != nil {
		return 0, 0, err
	}
	return result.TokensIn, result.TokensOut, nil
}

// classifyContainerRequest is the POST /classify body orch sends the warm
// classifier container. Kept in this file so the shape-vs-remote shape is
// co-located with its only caller.
type classifyContainerRequest struct {
	TaskID      string         `json:"task_id"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Context     map[string]any `json:"context,omitempty"`
}

// classifyContainerResponse mirrors cmd/drem-classifier's 200 response body.
type classifyContainerResponse struct {
	TaskID             string   `json:"task_id"`
	Category           string   `json:"category,omitempty"`
	ComplexityScore    int      `json:"complexity_score,omitempty"`
	Title              string   `json:"title,omitempty"`
	Description        string   `json:"description,omitempty"`
	TargetFiles        []string `json:"target_files,omitempty"`
	Rationale          string   `json:"rationale,omitempty"`
	NeedsClarification bool     `json:"needs_clarification,omitempty"`
	Questions          []string `json:"questions,omitempty"`
	TokensIn           int      `json:"tokens_in"`
	TokensOut          int      `json:"tokens_out"`
	DurationMS         int      `json:"duration_ms"`
}

// classifyViaContainer POSTs a classify request to the warm drem-classifier
// container and, on 200, writes the returned decision to
// classification-<taskID>.json so onClassifierCompleted can pick it up via
// the same path the inline runner uses. A 4xx/5xx upstream surfaces as a
// non-nil error so the caller parks the task via onClassifierFailed.
func (o *Orchestrator) classifyViaContainer(cfg agent.DirectClassifierConfig, task *model.Task, taskCtx map[string]any, outputDir string) (int, int, error) {
	reqBody := classifyContainerRequest{
		TaskID:      task.ID.String(),
		Title:       task.Title,
		Description: task.Description,
		Context:     taskCtx,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return 0, 0, fmt.Errorf("classifier container: marshal request: %w", err)
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.classifierContainerURL, bytes.NewReader(body))
	if err != nil {
		return 0, 0, fmt.Errorf("classifier container: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if o.classifierContainerToken != "" {
		req.Header.Set("Authorization", "Bearer "+o.classifierContainerToken)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("classifier container: POST failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, 0, fmt.Errorf("classifier container: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("classifier container: POST %s returned %d: %s", o.classifierContainerURL, resp.StatusCode, truncateClassifierBody(respBody, 500))
	}

	var parsed classifyContainerResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return 0, 0, fmt.Errorf("classifier container: decode response: %w", err)
	}

	// Reassemble the ClassifierOutput shape that onClassifierCompleted reads
	// from classification-<taskID>.json. We can't just forward respBody
	// because the container's response envelope wraps the decision fields
	// alongside telemetry; ClassifierOutput doesn't know about tokens_in.
	fileOut := ClassifierOutput{
		Category:           parsed.Category,
		ComplexityScore:    parsed.ComplexityScore,
		Title:              parsed.Title,
		Description:        parsed.Description,
		TargetFiles:        parsed.TargetFiles,
		Rationale:          parsed.Rationale,
		NeedsClarification: parsed.NeedsClarification,
		Questions:          parsed.Questions,
	}
	fileJSON, err := json.MarshalIndent(fileOut, "", "  ")
	if err != nil {
		return 0, 0, fmt.Errorf("classifier container: marshal output file: %w", err)
	}
	outputPath := filepath.Join(outputDir, fmt.Sprintf("classification-%s.json", task.ID))
	if err := os.WriteFile(outputPath, fileJSON, 0o644); err != nil {
		return 0, 0, fmt.Errorf("classifier container: write %s: %w", outputPath, err)
	}

	o.logger.Info("classifier container: classified task",
		"task_id", task.ID,
		"duration_ms", parsed.DurationMS,
		"tokens_in", parsed.TokensIn,
		"tokens_out", parsed.TokensOut,
		"needs_clarification", parsed.NeedsClarification,
	)

	return parsed.TokensIn, parsed.TokensOut, nil
}

// truncateClassifierBody bounds the error body captured in upstream-failure
// error strings so classify logs don't drag in multi-KB LLM payloads.
func truncateClassifierBody(b []byte, maxLen int) string {
	if len(b) <= maxLen {
		return string(b)
	}
	return string(b[:maxLen])
}

// onClassifierCompleted handles a classifier agent that finished successfully.
// It reads the classification.json from the agent's worktree and transitions
// the task based on the output.
func (o *Orchestrator) onClassifierCompleted(ag *model.Agent, task *model.Task) error {
	// Mark the agent idle and detach from the task so recoverStuckAgents
	// does not re-detect it on subsequent ticks.
	ag.Status = model.AgentIdle
	ag.CurrentTaskID = nil
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("on classifier completed: save agent: %w", err)
	}
	task.AssignedAgentID = nil

	path := filepath.Join(ag.WorktreePath, fmt.Sprintf("classification-%s.json", task.ID))
	data, err := os.ReadFile(path)
	if err != nil {
		// Missing or unreadable output — park for human triage.
		return o.parkClassifierForTriage(task, fmt.Sprintf("read classification-%s.json: %v", task.ID, err))
	}

	// Clean up the classification file so they don't accumulate in the master worktree.
	_ = os.Remove(path)

	var output ClassifierOutput
	if err := json.Unmarshal(data, &output); err != nil {
		return o.parkClassifierForTriage(task, fmt.Sprintf("parse classification-%s.json: %v", task.ID, err))
	}

	if output.NeedsClarification {
		return o.handleClarificationRequest(task, output.Questions)
	}

	return o.applyClassification(task, &output)
}

// onClassifierFailed handles a classifier agent that encountered an error.
// The task stays in CLASSIFYING and is parked for human triage.
func (o *Orchestrator) onClassifierFailed(ag *model.Agent, task *model.Task) error {
	// Mark the agent dead and detach from the task so recoverStuckAgents
	// does not re-detect it on subsequent ticks.
	ag.Status = model.AgentDead
	ag.CurrentTaskID = nil
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("on classifier failed: save agent: %w", err)
	}
	task.AssignedAgentID = nil

	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["human_triage"] = true
	task.Context["classifier_error"] = fmt.Sprintf("classifier agent %s failed", ag.ID)
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("on classifier failed: save task: %w", err)
	}
	o.logger.Info("classifier agent failed, parked for triage", "task_id", task.ID, "agent_id", ag.ID)
	return nil
}

// parkClassifierForTriage marks a classifying task for human triage when the
// classifier output is missing or malformed.
func (o *Orchestrator) parkClassifierForTriage(task *model.Task, reason string) error {
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["human_triage"] = true
	task.Context["classifier_error"] = reason
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("park classifier for triage: save task: %w", err)
	}
	o.logger.Info("classifier output unusable, parked for triage", "task_id", task.ID, "reason", reason)
	return nil
}

// handleClarificationRequest stores the classifier's questions in the task
// context and keeps the task in CLASSIFYING for human input.
func (o *Orchestrator) handleClarificationRequest(task *model.Task, questions []string) error {
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	// Store as []interface{} to match JSON round-trip behavior.
	qs := make([]interface{}, len(questions))
	for i, q := range questions {
		qs[i] = q
	}
	task.Context["clarification_questions"] = qs
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("handle clarification request: save task: %w", err)
	}
	o.logger.Info("classifier needs clarification", "task_id", task.ID, "questions", len(questions))
	return nil
}

// applyClassification updates the task with enriched classification data and
// transitions it from CLASSIFYING to BACKLOG.
func (o *Orchestrator) applyClassification(task *model.Task, output *ClassifierOutput) error {
	task.Title = output.Title
	task.Description = output.Description
	task.ComplexityScore = output.ComplexityScore

	cat, err := model.ParseTaskCategory(output.Category)
	if err != nil {
		// Unknown category — default to standard.
		cat = model.CategoryStandard
	}
	task.Category = cat

	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	// Store target_files as []interface{} for JSON round-trip consistency.
	files := make([]interface{}, len(output.TargetFiles))
	for i, f := range output.TargetFiles {
		files[i] = f
	}
	task.Context["target_files"] = files
	task.Context["rationale"] = output.Rationale

	oldStatus := task.Status
	if err := o.transitionTaskAtomic(task, model.StatusBacklog, "classifier", "classification_result",
		"classification completed", map[string]any{"category": string(task.Category), "complexity_score": task.ComplexityScore}); err != nil {
		return fmt.Errorf("apply classification: transition to backlog: %w", err)
	}
	o.emit("task_updated", task)
	o.publishTaskTransition(task.ID.String(), string(oldStatus), string(task.Status), "classifier completed")
	o.logger.Info("classifier completed, task moved to backlog",
		"task_id", task.ID, "category", task.Category, "complexity", task.ComplexityScore)
	return nil
}

// ingestBugReports scans the bug report directory for new reports and ingests
// them into the database. If any new reports are found, it triggers
// classification.
func (o *Orchestrator) ingestBugReports() {
	if o.bugreport == nil {
		return
	}
	n, err := o.bugreport.Ingest(o.bugreportDir, o.projectID)
	if err != nil {
		o.logger.Warn("bug report ingestion error", "error", err)
	}
	if n > 0 {
		o.logger.Info("ingested bug reports", "count", n)
		o.classifyNewBugReports()
	}
}

// classifyNewBugReports queries for unclassified (open, no PromotedTaskID) bug
// reports and creates a task in CLASSIFYING for each. The classifier agent will
// pick up these tasks and determine category, complexity, and target files.
func (o *Orchestrator) classifyNewBugReports() {
	openStatus := model.BugStatusOpen
	reports, err := o.bugreport.List(bugreport.ListFilters{
		Status:    &openStatus,
		ProjectID: &o.projectID,
	})
	if err != nil {
		o.logger.Warn("classify new bug reports: list open reports", "error", err)
		return
	}

	for i := range reports {
		report := &reports[i]

		// Skip already-promoted reports.
		if report.PromotedTaskID != nil {
			continue
		}

		// Store bug report context for the classifier agent.
		ctx := model.JSONField{
			"title":                report.Title,
			"category":             string(report.Category),
			"severity":             string(report.Severity),
			"description":          report.Description,
			"reproduction_context": report.ReproductionContext,
		}

		// Create the task in CLASSIFYING for the classifier agent.
		task := &model.Task{
			ID:          uuid.New(),
			ProjectID:   o.projectID,
			Title:       report.Title,
			Description: report.Description,
			Status:      model.StatusClassifying,
			Category:    model.CategoryStandard,
			Context:     ctx,
		}

		if err := o.db.Create(task).Error; err != nil {
			o.logger.Warn("classify new bug reports: create task",
				"report_id", report.ID, "error", err)
			continue
		}

		// Update the bug report: mark as promoted and link to the new task.
		report.PromotedTaskID = &task.ID
		report.Status = model.BugStatusPromoted
		if err := o.db.Save(report).Error; err != nil {
			o.logger.Warn("classify new bug reports: update bug report",
				"report_id", report.ID, "error", err)
			continue
		}

		o.emit("bugreport_classified", map[string]any{
			"task_id":   task.ID,
			"report_id": report.ID,
		})

		o.logger.Info("bug report promoted to classifying",
			"report_id", report.ID,
			"task_id", task.ID,
		)
	}
}
