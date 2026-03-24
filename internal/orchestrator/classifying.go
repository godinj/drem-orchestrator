package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/bugreport"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/prompt"
	"github.com/godinj/drem-orchestrator/internal/state"
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

		o.logger.Info("spawned classifier agent", "task_id", task.ID, "agent_id", ag.ID)
	}
}

// onClassifierCompleted handles a classifier agent that finished successfully.
// It reads the classification.json from the agent's worktree and transitions
// the task based on the output.
func (o *Orchestrator) onClassifierCompleted(ag *model.Agent, task *model.Task) error {
	path := filepath.Join(ag.WorktreePath, "classification.json")
	data, err := os.ReadFile(path)
	if err != nil {
		// Missing or unreadable output — park for human triage.
		return o.parkClassifierForTriage(task, fmt.Sprintf("read classification.json: %v", err))
	}

	var output ClassifierOutput
	if err := json.Unmarshal(data, &output); err != nil {
		return o.parkClassifierForTriage(task, fmt.Sprintf("parse classification.json: %v", err))
	}

	if output.NeedsClarification {
		return o.handleClarificationRequest(task, output.Questions)
	}

	return o.applyClassification(task, &output)
}

// onClassifierFailed handles a classifier agent that encountered an error.
// The task stays in CLASSIFYING and is parked for human triage.
func (o *Orchestrator) onClassifierFailed(ag *model.Agent, task *model.Task) error {
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

	event, err := state.TransitionTask(task, model.StatusBacklog, "classifier", nil)
	if err != nil {
		return fmt.Errorf("apply classification: transition to backlog: %w", err)
	}
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("apply classification: save task: %w", err)
	}
	if err := o.db.Create(event).Error; err != nil {
		return fmt.Errorf("apply classification: save event: %w", err)
	}
	o.emit("task_updated", task)
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
