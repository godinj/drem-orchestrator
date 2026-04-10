package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/prompt"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// PrepOutput is the structured output from a task preparation agent.
// It contains tactical context about target files that enriches the
// subsequent coder agent's prompt.
type PrepOutput struct {
	TargetFiles []PrepTargetFile `json:"target_files"`
	Insertions  []PrepInsertion  `json:"insertion_points"`
	Patterns    []PrepPattern    `json:"patterns_to_follow"`
	Warnings    []string         `json:"warnings"`
	Constructors []PrepConstructor `json:"constructors"`
}

// PrepTargetFile describes a file the coder will modify.
type PrepTargetFile struct {
	Path        string   `json:"path"`
	Definitions string   `json:"relevant_definitions"`
	Methods     []string `json:"methods"`
	Notes       string   `json:"notes"`
}

// PrepInsertion identifies where new code should be added.
type PrepInsertion struct {
	File     string `json:"file"`
	Location string `json:"location"`
	What     string `json:"what"`
}

// PrepPattern describes an existing codebase pattern the coder should follow.
type PrepPattern struct {
	Description string `json:"description"`
	Example     string `json:"example"`
	SourceFile  string `json:"source_file"`
}

// PrepConstructor describes a constructor/factory function that may need
// updating when a struct field is added.
type PrepConstructor struct {
	StructName  string   `json:"struct_name"`
	Constructor string   `json:"constructor"`
	TestHelpers []string `json:"test_helpers"`
}

// needsPrep returns true if a subtask should go through the prep agent step
// before being dispatched to a coder. This is gated on the target coder being
// a local model (provider != "claude").
func (o *Orchestrator) needsPrep(sub *model.Task) bool {
	if sub == nil || sub.Context == nil {
		return false
	}

	// Skip prep if already prepped.
	if _, done := sub.Context["prep_complete"]; done {
		return false
	}

	// Skip prep if currently being prepped.
	if _, inProgress := sub.Context["prep_in_progress"]; inProgress {
		return false
	}

	// Only prep subtasks with estimated_files (the prep agent needs files to inspect).
	if _, hasFiles := sub.Context["estimated_files"]; !hasFiles {
		return false
	}

	// Only prep when the coder is a local model.
	if o.runner == nil {
		return false
	}
	coderCfg := o.runner.AgentConfig(model.AgentCoder)
	return coderCfg.EffectiveProvider() != model.ProviderClaude
}

// spawnPrepAgent spawns a task preparation agent for a subtask. The prep agent
// reads the codebase and writes a task-prep-<id>.json file with tactical context.
// The subtask is marked prep_in_progress to prevent re-dispatch until prep completes.
func (o *Orchestrator) spawnPrepAgent(sub *model.Task, parent *model.Task) error {
	featureName := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
	featureDir := o.worktree.FeatureWorktreePath(featureName)

	var project model.Project
	if err := o.db.First(&project, "id = ?", o.projectID).Error; err != nil {
		return fmt.Errorf("spawn prep agent: load project: %w", err)
	}

	parentCtx := map[string]any{
		"parent_title":       parent.Title,
		"parent_description": parent.Description,
		"feature_branch":     parent.WorktreeBranch,
	}

	// Include the plan in parent context so prep agent can see the full picture.
	if parent.Plan != nil {
		parentCtx["plan"] = parent.Plan
	}

	prepPrompt := prompt.Generate(prompt.Opts{
		Task:         sub,
		Project:      &project,
		AgentType:    model.AgentPrep,
		WorktreePath: featureDir,
		ParentCtx:    parentCtx,
	})

	// Generate repo map in the feature worktree for the prep agent.
	worktree.GenerateRepoMapAsync(featureDir)

	ag, err := o.runner.SpawnAgent(sub, featureName, model.AgentPrep, prepPrompt)
	if err != nil {
		return fmt.Errorf("spawn prep agent: %w", err)
	}

	sub.AssignedAgentID = &ag.ID
	if sub.Context == nil {
		sub.Context = make(model.JSONField)
	}
	sub.Context["prep_in_progress"] = true
	if err := o.db.Save(sub).Error; err != nil {
		return fmt.Errorf("spawn prep agent: save subtask: %w", err)
	}

	o.logger.Info("prep agent spawned", "subtask_id", sub.ID, "agent_id", ag.ID)
	return nil
}

// onPrepCompleted handles a completed task preparation agent. It reads the
// task-prep-<id>.json output, stores the prep data in the subtask's context,
// and marks the subtask ready for coder dispatch.
func (o *Orchestrator) onPrepCompleted(ag *model.Agent, task *model.Task) error {
	// Mark agent idle and detach.
	ag.Status = model.AgentIdle
	ag.CurrentTaskID = nil
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("on prep completed: save agent: %w", err)
	}
	task.AssignedAgentID = nil

	// Read the prep output from the agent's worktree.
	prepFile := filepath.Join(ag.WorktreePath, fmt.Sprintf("task-prep-%s.json", task.ID))
	data, err := os.ReadFile(prepFile)
	if err != nil {
		// Prep agent failed to produce output. Log warning and let coder proceed
		// without enrichment (graceful degradation).
		o.logger.Warn("prep agent produced no output, coder will proceed without enrichment",
			"task_id", task.ID, "agent_id", ag.ID, "error", err)
		if task.Context == nil {
			task.Context = make(model.JSONField)
		}
		delete(task.Context, "prep_in_progress")
		task.Context["prep_complete"] = true
		task.Context["prep_failed"] = true
		if err := o.db.Save(task).Error; err != nil {
			return fmt.Errorf("on prep completed: save task after failure: %w", err)
		}
		return nil
	}

	// Parse the prep output.
	var prepOutput PrepOutput
	if err := json.Unmarshal(data, &prepOutput); err != nil {
		o.logger.Warn("prep agent output malformed, coder will proceed without enrichment",
			"task_id", task.ID, "agent_id", ag.ID, "error", err)
		if task.Context == nil {
			task.Context = make(model.JSONField)
		}
		delete(task.Context, "prep_in_progress")
		task.Context["prep_complete"] = true
		task.Context["prep_failed"] = true
		if err := o.db.Save(task).Error; err != nil {
			return fmt.Errorf("on prep completed: save task after parse failure: %w", err)
		}
		return nil
	}

	// Clean up the prep file.
	_ = os.Remove(prepFile)

	// Store the prep data in subtask context for the coder prompt to pick up.
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	delete(task.Context, "prep_in_progress")
	task.Context["prep_complete"] = true
	task.Context["prep_data"] = prepOutput

	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("on prep completed: save task: %w", err)
	}

	o.logger.Info("prep agent completed, subtask enriched",
		"task_id", task.ID,
		"target_files", len(prepOutput.TargetFiles),
		"warnings", len(prepOutput.Warnings))
	return nil
}

// onPrepFailed handles a failed task preparation agent. It gracefully degrades
// by marking the subtask as prep_complete with prep_failed, allowing the coder
// to proceed without enrichment on the next dispatch tick.
func (o *Orchestrator) onPrepFailed(ag *model.Agent, task *model.Task) error {
	// Clean up agent worktree if it exists.
	if ag.WorktreeBranch != "" {
		if err := o.worktree.RemoveAgentWorktree(ag.WorktreeBranch); err != nil {
			o.logger.Warn("cleanup failed prep agent worktree failed", "agent_id", ag.ID, "error", err)
		}
	}

	// Mark agent as dead.
	ag.Status = model.AgentDead
	ag.CurrentTaskID = nil
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("on prep failed: save agent: %w", err)
	}

	// Mark task as prepped (with failure flag) so coder dispatch proceeds.
	task.AssignedAgentID = nil
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	delete(task.Context, "prep_in_progress")
	task.Context["prep_complete"] = true
	task.Context["prep_failed"] = true
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("on prep failed: save task: %w", err)
	}

	o.logger.Warn("prep agent failed, coder will proceed without enrichment",
		"task_id", task.ID, "agent_id", ag.ID)
	return nil
}
