package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/prompt"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// SpawnReviewerSession spawns a reviewer agent for the given task.
func (o *Orchestrator) SpawnReviewerSession(taskID uuid.UUID) (string, error) {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return "", fmt.Errorf("spawn reviewer: find task: %w", err)
	}

	// Validate status.
	if task.Status != model.StatusPlanReview && task.Status != model.StatusTestingReady {
		return "", fmt.Errorf("spawn reviewer: task must be in plan_review or testing_ready, got %s", task.Status)
	}

	// Check for existing working reviewer on the same task.
	var existing model.Agent
	err := o.db.Where("current_task_id = ? AND agent_type = ? AND status = ?",
		taskID, model.AgentReviewer, model.AgentWorking).First(&existing).Error
	if err == nil {
		// Already a working reviewer — return its session.
		o.logger.Info("reviewer already running for task", "task_id", taskID, "agent_id", existing.ID)
		return existing.TmuxSession, nil
	}

	// Resolve integration worktree.
	worktreePath := o.resolveIntegrationWorktree(&task)
	if worktreePath == "" {
		return "", fmt.Errorf("spawn reviewer: no integration worktree found for task %s", taskID)
	}

	// Load project.
	var project model.Project
	if err := o.db.First(&project, "id = ?", o.projectID).Error; err != nil {
		return "", fmt.Errorf("spawn reviewer: load project: %w", err)
	}

	// Determine review mode and build context.
	var reviewMode, planJSON, gitDiff string
	if task.Status == model.StatusPlanReview {
		reviewMode = "plan"
		if task.Plan != nil {
			if data, err := json.MarshalIndent(task.Plan, "", "  "); err == nil {
				planJSON = string(data)
			}
		}
	} else {
		reviewMode = "feature"
		// Get diff of integration branch vs default branch.
		diff, err := worktree.RunGit(
			[]string{"diff", o.worktree.DefaultBranch + "...HEAD", "--stat"},
			worktreePath,
		)
		if err == nil {
			// Also get the full diff (limited size).
			fullDiff, _ := worktree.RunGit(
				[]string{"diff", o.worktree.DefaultBranch + "...HEAD"},
				worktreePath,
			)
			if fullDiff != "" {
				gitDiff = fullDiff
			} else {
				gitDiff = diff
			}
		}
	}

	// Generate prompt.
	comments, _ := o.GetComments(task.ID)
	reviewerPrompt := prompt.Generate(prompt.Opts{
		Task:         &task,
		Project:      &project,
		AgentType:    model.AgentReviewer,
		WorktreePath: worktreePath,
		Comments:     comments,
		ReviewMode:   reviewMode,
		PlanJSON:     planJSON,
		GitDiff:      gitDiff,
	})

	// Spawn via runner.
	ag, err := o.runner.SpawnAgentInWorktree(&task, worktreePath, model.AgentReviewer, reviewerPrompt)
	if err != nil {
		return "", fmt.Errorf("spawn reviewer: %w", err)
	}

	o.emit("reviewer_spawned", map[string]any{"task_id": taskID, "agent_id": ag.ID, "mode": reviewMode})
	o.logger.Info("reviewer spawned", "task_id", taskID, "agent_id", ag.ID, "mode", reviewMode)
	return ag.TmuxSession, nil
}

// SpawnFixerSession spawns a fixer agent for the given task.
// The task should be in a status where a fix is applicable (in_progress, failed,
// testing_ready). Returns the tmux session name so the TUI can focus it.
func (o *Orchestrator) SpawnFixerSession(taskID uuid.UUID) (string, error) {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return "", fmt.Errorf("spawn fixer: find task: %w", err)
	}

	// Validate status.
	switch task.Status {
	case model.StatusInProgress, model.StatusFailed, model.StatusTestingReady:
		// OK
	default:
		return "", fmt.Errorf("spawn fixer: task must be in in_progress, failed, or testing_ready, got %s", task.Status)
	}

	// Resolve integration worktree.
	worktreePath := o.resolveIntegrationWorktree(&task)
	if worktreePath == "" {
		return "", fmt.Errorf("spawn fixer: no integration worktree found for task %s", taskID)
	}

	// Check for any agent (reviewer/fixer) working in the same integration worktree.
	var busy model.Agent
	err := o.db.Where("worktree_path = ? AND status = ? AND agent_type IN ?",
		worktreePath, model.AgentWorking, []model.AgentType{model.AgentReviewer, model.AgentFixer}).
		First(&busy).Error
	if err == nil {
		return "", fmt.Errorf("spawn fixer: integration worktree is occupied by %s agent %s", busy.AgentType, busy.ID)
	}

	// Load project.
	var project model.Project
	if err := o.db.First(&project, "id = ?", o.projectID).Error; err != nil {
		return "", fmt.Errorf("spawn fixer: load project: %w", err)
	}

	// Extract diagnosis from task context.
	var diagnosis, suggestedFix string
	var affectedFiles []string
	if task.Context != nil {
		if d, ok := task.Context["failure_diagnosis"].(string); ok {
			diagnosis = d
		}
		if d, ok := task.Context["failure_reason"].(string); ok && diagnosis == "" {
			diagnosis = d
		}
		if sf, ok := task.Context["suggested_fix"].(string); ok {
			suggestedFix = sf
		}
		if af, ok := task.Context["affected_files"].([]any); ok {
			for _, f := range af {
				if s, ok := f.(string); ok {
					affectedFiles = append(affectedFiles, s)
				}
			}
		}
	}

	// Generate prompt.
	comments, _ := o.GetComments(task.ID)
	fixerPrompt := prompt.Generate(prompt.Opts{
		Task:          &task,
		Project:       &project,
		AgentType:     model.AgentFixer,
		WorktreePath:  worktreePath,
		Comments:      comments,
		Diagnosis:     diagnosis,
		AffectedFiles: affectedFiles,
		SuggestedFix:  suggestedFix,
	})

	// Spawn via runner.
	ag, err := o.runner.SpawnAgentInWorktree(&task, worktreePath, model.AgentFixer, fixerPrompt)
	if err != nil {
		return "", fmt.Errorf("spawn fixer: %w", err)
	}

	o.emit("fixer_spawned", map[string]any{"task_id": taskID, "agent_id": ag.ID})
	o.logger.Info("fixer spawned", "task_id", taskID, "agent_id", ag.ID)
	return ag.TmuxSession, nil
}

// resolveIntegrationWorktree returns the integration worktree path for a task.
// For parent tasks, it derives from WorktreeBranch. For subtasks, it looks up
// the parent task. Returns empty string if not found.
func (o *Orchestrator) resolveIntegrationWorktree(task *model.Task) string {
	branch := task.WorktreeBranch
	if branch == "" && task.ParentTaskID != nil {
		var parent model.Task
		if err := o.db.Select("worktree_branch").First(&parent, "id = ?", task.ParentTaskID).Error; err != nil {
			return ""
		}
		branch = parent.WorktreeBranch
	}
	if branch == "" {
		return ""
	}
	fn := strings.TrimPrefix(branch, "feature/")
	path := o.worktree.FeatureWorktreePath(fn)
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return ""
	}
	return path
}

// IntegrationWorktreePath returns the integration worktree path for a task,
// resolving through the parent if the task is a subtask.
func (o *Orchestrator) IntegrationWorktreePath(taskID uuid.UUID) string {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return ""
	}
	return o.resolveIntegrationWorktree(&task)
}

// GetAgentOutput returns the output log content for the given agent.
func (o *Orchestrator) GetAgentOutput(agentID uuid.UUID) (string, error) {
	return o.runner.GetAgentOutput(agentID)
}

// SpawnSupervisorSession creates an interactive Claude session in a tmux
// session for on-demand supervisor work on a task. The session runs in the
// task's integration worktree with a system prompt containing task context.
// Returns the tmux session name so the TUI can switch to it.
func (o *Orchestrator) SpawnSupervisorSession(taskID uuid.UUID) (string, error) {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return "", fmt.Errorf("spawn supervisor: find task: %w", err)
	}

	// Determine the working directory. Prefer the task's integration worktree;
	// fall back to the default branch worktree.
	cwd := filepath.Join(o.worktree.BareRepoPath, o.worktree.DefaultBranch)
	if task.WorktreeBranch != "" {
		candidate := filepath.Join(o.worktree.BareRepoPath, task.WorktreeBranch, "integration")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			cwd = candidate
		}
	}

	// Gather subtask info for context.
	var subtasks []model.Task
	o.db.Where("parent_task_id = ?", taskID).Find(&subtasks)
	stInfos := make([]supervisor.SubtaskInfo, len(subtasks))
	for i, st := range subtasks {
		stInfos[i] = supervisor.SubtaskInfo{
			ID:     st.ID.String(),
			Title:  st.Title,
			Status: string(st.Status),
			Branch: st.WorktreeBranch,
		}
	}

	// Build the system prompt with full orchestration context.
	prompt := supervisor.OnDemandPrompt(supervisor.OnDemandOpts{
		TaskTitle:     task.Title,
		TaskDesc:      task.Description,
		TaskID:        taskID.String(),
		Status:        string(task.Status),
		Branch:        task.WorktreeBranch,
		DBPath:        o.dbPath,
		BareRepoPath:  o.worktree.BareRepoPath,
		DefaultBranch: o.worktree.DefaultBranch,
		Subtasks:      stInfos,
	})

	// Write prompt to a temp file in the worktree.
	claudeDir := filepath.Join(cwd, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return "", fmt.Errorf("spawn supervisor: mkdir .claude: %w", err)
	}
	promptPath := filepath.Join(claudeDir, "supervisor-prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		return "", fmt.Errorf("spawn supervisor: write prompt: %w", err)
	}

	// Build session name under the dashboard's namespace.
	shortID := taskID.String()[:shortIDLen]
	title := strings.ReplaceAll(task.Title, "/", "-")
	title = truncate(title, maxDisplayNameLen)
	sessionName := fmt.Sprintf("%s/supervisor - %s %s", o.runner.TmuxSessionName(), title, shortID)
	sessionName = strings.ReplaceAll(sessionName, ".", "-")
	sessionName = strings.ReplaceAll(sessionName, ":", "-")

	// Kill any existing supervisor session for this task.
	tmuxMgr := o.runner.TmuxManager()
	_ = tmuxMgr.KillAgentSession(sessionName)

	// Build the claude command with per-type model/effort flags.
	claudeBin := o.runner.ClaudeBin()
	cliArgs := o.interactiveSupervisorConfig.CLIArgs()
	cmdParts := []string{claudeBin, "--dangerously-skip-permissions"}
	cmdParts = append(cmdParts, cliArgs...)
	cmdParts = append(cmdParts, fmt.Sprintf("\"$(cat %s)\"", promptPath))
	cmd := strings.Join(cmdParts, " ")

	// Create the tmux session.
	if err := tmuxMgr.CreateAgentSession(sessionName, cmd, cwd); err != nil {
		return "", fmt.Errorf("spawn supervisor: create session: %w", err)
	}

	o.logger.Info("supervisor session spawned", "task_id", taskID, "session", sessionName)
	return sessionName, nil
}
