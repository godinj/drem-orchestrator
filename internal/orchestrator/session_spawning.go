package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/prompt"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
	"github.com/godinj/drem-orchestrator/internal/workeridentity"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// SpawnReviewerSession spawns a reviewer agent for the given task.
func (o *Orchestrator) SpawnReviewerSession(taskID uuid.UUID) (string, error) {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return "", fmt.Errorf("spawn reviewer: find task: %w", err)
	}

	// Validate status.
	if task.Status != model.StatusPlanReview && task.Status != model.StatusTestReview && task.Status != model.StatusTestingReady {
		return "", fmt.Errorf("spawn reviewer: task must be in plan_review, test_review, or testing_ready, got %s", task.Status)
	}

	// Check for existing working reviewer on the same task.
	var existing model.Agent
	err := o.db.Where("current_task_id = ? AND agent_type = ? AND status = ?",
		taskID, model.AgentReviewer, model.AgentWorking).First(&existing).Error
	if err == nil {
		// Already a working reviewer — return its session.
		o.logger.Info("reviewer already running for task", "task_id", taskID, "agent_id", existing.ID)
		return workerHandleString(existing), nil
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
		if err := o.verifyTaskSpecSourceEvidence(&task, worktreePath); err != nil {
			return "", fmt.Errorf("spawn reviewer: source-backed integration seam: %w", err)
		}
		if task.Plan != nil {
			if data, err := json.MarshalIndent(task.Plan, "", "  "); err == nil {
				planJSON = string(data)
			}
		}
	} else if task.Status == model.StatusTestReview {
		reviewMode = "tests"
		evidence, evidenceErr := o.buildTestReviewEvidence(&task, worktreePath)
		if evidenceErr != nil {
			return "", evidenceErr
		}
		planJSON = evidence
	} else {
		reviewMode = "feature"
		// Get diff of integration branch vs default branch.
		diff, err := gitexec.RunGit(
			context.Background(), worktreePath,
			"diff", o.worktree.DefaultBranchName()+"...HEAD", "--stat",
		)
		if err == nil {
			// Also get the full diff (limited size).
			fullDiff, _ := gitexec.RunGit(
				context.Background(), worktreePath,
				"diff", o.worktree.DefaultBranchName()+"...HEAD",
			)
			if fullDiff != "" {
				gitDiff = fullDiff
			} else {
				gitDiff = diff
			}
		}
	}

	// Direct SGLang path: plan review only. When enabled, call the API
	// synchronously, write review.json into the worktree, and run the
	// completion handler inline. Feature reviews fall through to the
	// subprocess path below.
	if o.directPlanReviewerCfg != nil {
		switch reviewMode {
		case "plan":
			return o.spawnDirectPlanReviewer(&task, worktreePath, planJSON)
		case "tests":
			return o.spawnDirectTestReviewer(&task, worktreePath, planJSON)
		}
	}

	// Container-mode dispatch: when o.Spawner is wired, route the
	// reviewer through spawnReviewer so the worker runs inside a
	// drem-worker-<lang> container. The prompt is rendered and
	// bind-mounted by buildSpawnContext; ReviewMode / PlanJSON /
	// GitDiff plumbing into the container-path prompt is tracked as
	// plans/phase-3.5-subtask-dispatch-migration.md §"Open questions"
	// Q2 — ship the dispatch-migration first, plumb the review-mode
	// fields second so the T3 canary is unblocked without a risky
	// signature change to spawnTypedWorker. Legacy
	// o.runner.SpawnAgentInWorktree path below runs only when
	// o.Spawner is nil (development on host with claude installed).
	// See plans/phase-3.5-subtask-dispatch-migration.md Commit 3.
	if o.Spawner != nil {
		if err := o.spawnReviewer(context.Background(), &task); err != nil {
			return "", fmt.Errorf("spawn reviewer via spawner: %w", err)
		}
		// Reload so AssignedAgentID (written by worker identity recording)
		// is visible, then look up the Agent row to return the
		// container ID as the "session name" the TUI displays.
		if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
			return "", fmt.Errorf("spawn reviewer: reload task: %w", err)
		}
		if task.AssignedAgentID == nil {
			return "", fmt.Errorf("spawn reviewer: no agent assignment after container spawn")
		}
		var ag model.Agent
		if err := o.db.First(&ag, "id = ?", task.AssignedAgentID).Error; err != nil {
			return "", fmt.Errorf("spawn reviewer: load agent after spawn: %w", err)
		}
		o.emit("reviewer_spawned", map[string]any{"task_id": taskID, "agent_id": ag.ID, "mode": reviewMode})
		o.logger.Info("reviewer spawned via spawner",
			"task_id", taskID, "agent_id", ag.ID, "mode", reviewMode)
		return workerHandleString(ag), nil
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
	return workerHandleString(*ag), nil
}

func (o *Orchestrator) verifyTaskSpecSourceEvidence(task *model.Task, worktreePath string) error {
	var stored model.TaskSpecification
	if err := o.db.Where("task_id = ?", task.ID).First(&stored).Error; err != nil {
		// Legacy planner-created tasks have no immutable adapter specification.
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	var spec orchdto.TaskSpecDTO
	if err := json.Unmarshal([]byte(stored.SpecJSON), &spec); err != nil {
		return fmt.Errorf("decode immutable task specification: %w", err)
	}
	for _, seam := range spec.IntegrationSeams {
		for _, evidence := range seam.SourceEvidence {
			content, err := os.ReadFile(filepath.Join(worktreePath, filepath.Clean(evidence.Path)))
			if err != nil {
				return fmt.Errorf("%s evidence file %s: %w", seam.ID, evidence.Path, err)
			}
			if !strings.Contains(string(content), evidence.Excerpt) {
				return fmt.Errorf("%s evidence excerpt for %s is stale or absent at the task base", seam.ID, evidence.Symbol)
			}
		}
	}
	return nil
}

// spawnDirectPlanReviewer runs the direct SGLang plan review synchronously.
// It mirrors the lightweight-agent pattern from processClassifyingTasksDirect:
// create an Agent DB record, run the API call, invoke onReviewerCompleted
// which parses review.json and stores it on task.Context. Returns
// ("", nil) on success — no tmux session exists for direct agents.
func (o *Orchestrator) spawnDirectPlanReviewer(task *model.Task, worktreePath, planJSON string) (string, error) {
	return o.spawnDirectGateReviewer(task, worktreePath, "plan", planJSON)
}

func (o *Orchestrator) spawnDirectTestReviewer(task *model.Task, worktreePath, evidenceJSON string) (string, error) {
	return o.spawnDirectGateReviewer(task, worktreePath, "tests", evidenceJSON)
}

func (o *Orchestrator) spawnDirectGateReviewer(task *model.Task, worktreePath, reviewKind, payload string) (string, error) {
	cfg := o.directPlanReviewerCfg
	now := time.Now()
	ag := &model.Agent{
		ID:            uuid.New(),
		ProjectID:     task.ProjectID,
		AgentType:     model.AgentReviewer,
		Name:          fmt.Sprintf("direct-reviewer-%s", task.ID.String()[:4]),
		Status:        model.AgentWorking,
		CurrentTaskID: &task.ID,
		WorktreePath:  worktreePath,
		Provider:      "sglang-direct",
		ModelID:       cfg.Model,
		HeartbeatAt:   &now,
	}
	if err := o.db.Create(ag).Error; err != nil {
		return "", fmt.Errorf("spawn direct reviewer: create agent record: %w", err)
	}

	task.AssignedAgentID = &ag.ID
	if err := o.db.Save(task).Error; err != nil {
		return "", fmt.Errorf("spawn direct reviewer: assign agent to task: %w", err)
	}

	o.logger.Info("direct gate reviewer: reviewing",
		"task_id", task.ID, "agent_id", ag.ID, "review_kind", reviewKind)

	var result *agent.DirectPlanReviewerResult
	var err error
	if reviewKind == "tests" {
		result, err = agent.RunDirectTestReviewer(*cfg, task.ID, task.Title, task.Description, payload, worktreePath)
	} else {
		result, err = agent.RunDirectPlanReviewer(*cfg, task.ID, task.Title, task.Description, payload, worktreePath)
	}
	if err != nil {
		ag.Status = model.AgentDead
		ag.CurrentTaskID = nil
		_ = o.db.Save(ag).Error
		return "", fmt.Errorf("spawn direct reviewer: %w", err)
	}

	// Record token usage for observability.
	ag.TokensIn = result.TokensIn
	ag.TokensOut = result.TokensOut
	_ = o.db.Save(ag).Error
	if usageErr := o.recordInferenceUsage(task.ID, directReviewPhase(reviewKind), "reviewer", "sglang-direct", cfg.Model, result.TokensIn, result.TokensOut, result.Duration); usageErr != nil {
		o.logger.Warn("direct gate reviewer: persist inference usage", "task_id", task.ID, "error", usageErr)
	}

	if err := o.onReviewerCompleted(ag, task); err != nil {
		return "", fmt.Errorf("spawn direct reviewer: on completed: %w", err)
	}

	o.emit("reviewer_spawned", map[string]any{
		"task_id":  task.ID,
		"agent_id": ag.ID,
		"mode":     reviewKind + "-direct",
	})
	return "", nil
}

func (o *Orchestrator) buildTestReviewEvidence(task *model.Task, worktreePath string) (string, error) {
	var testTasks []model.Task
	if err := o.db.Where("parent_task_id = ? AND phase = ?", task.ID, "test").Order("created_at asc").Find(&testTasks).Error; err != nil {
		return "", fmt.Errorf("build test review evidence: query test tasks: %w", err)
	}
	type testTaskEvidence struct {
		Title       string           `json:"title"`
		Description string           `json:"description"`
		Status      model.TaskStatus `json:"status"`
		TestsFor    model.JSONArray  `json:"tests_for"`
	}
	completed := make([]testTaskEvidence, 0, len(testTasks))
	for i := range testTasks {
		completed = append(completed, testTaskEvidence{
			Title: testTasks[i].Title, Description: testTasks[i].Description,
			Status: testTasks[i].Status, TestsFor: testTasks[i].TestsFor,
		})
	}
	diff, _ := gitexec.RunGit(context.Background(), worktreePath,
		"diff", o.worktree.DefaultBranchName()+"...HEAD", "--", "tests")
	if len(diff) > 60000 {
		diff = diff[:60000] + "\n[diff truncated]"
	}
	payload := map[string]any{
		"approved_plan":          task.Plan,
		"completed_test_tasks":   completed,
		"test_only_feature_diff": diff,
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("build test review evidence: marshal: %w", err)
	}
	return string(raw), nil
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
	case model.StatusMerging:
		if task.Context == nil || task.Context[contextKeyMergeConflictFiles] == nil {
			return "", fmt.Errorf("spawn fixer: merging task requires merge_conflict_files context")
		}
	default:
		return "", fmt.Errorf("spawn fixer: task must be in in_progress, failed, testing_ready, or merging with merge conflicts, got %s", task.Status)
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
	diagnosis, suggestedFix, affectedFiles := extractFixerContext(&task)

	// Container-mode dispatch: when o.Spawner is wired, route the fixer
	// through spawnFixer. renderAndWritePrompt pulls the diagnosis,
	// affected files, and suggested fix from task.Context so container
	// fixers receive the same context as legacy prompt-based fixers.
	if o.Spawner != nil {
		if err := o.spawnFixer(context.Background(), &task); err != nil {
			return "", fmt.Errorf("spawn fixer via spawner: %w", err)
		}
		if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
			return "", fmt.Errorf("spawn fixer: reload task: %w", err)
		}
		if task.AssignedAgentID == nil {
			return "", fmt.Errorf("spawn fixer: no agent assignment after container spawn")
		}
		var ag model.Agent
		if err := o.db.First(&ag, "id = ?", task.AssignedAgentID).Error; err != nil {
			return "", fmt.Errorf("spawn fixer: load agent after spawn: %w", err)
		}
		// Record fixer spawn metric. The legacy path reads the parent
		// agent (the originating agent whose work triggered the fixer)
		// to tag the metric with parent_model. In container mode the
		// AssignedAgentID was overwritten by worker identity recording to
		// point at the new fixer agent — the parent-model label here
		// records the fixer's image rather than the originator's
		// model. An accurate parent_model label requires reading the
		// previous AssignedAgentID before spawnFixer overrides it;
		// tracked as a follow-up on §Open questions Q2.
		if o.metrics != nil {
			o.metrics.Record(ag.ID, "fixer_spawn", 1.0, map[string]string{
				"parent_model": ag.ModelID,
			})
		}
		o.emit("fixer_spawned", map[string]any{"task_id": taskID, "agent_id": ag.ID})
		o.logger.Info("fixer spawned via spawner",
			"task_id", taskID, "agent_id", ag.ID)
		return workerHandleString(ag), nil
	}

	// Generate prompt.
	if o.runner == nil {
		return "", fmt.Errorf("spawn fixer: no spawner or runner configured")
	}
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

	// Record fixer spawn metric
	if o.metrics != nil && task.AssignedAgentID != nil {
		var parentAgent model.Agent
		if err := o.db.First(&parentAgent, "id = ?", *task.AssignedAgentID).Error; err == nil {
			o.metrics.Record(parentAgent.ID, "fixer_spawn", 1.0, map[string]string{
				"parent_model": parentAgent.ModelID,
			})
		}
	}

	o.emit("fixer_spawned", map[string]any{"task_id": taskID, "agent_id": ag.ID})
	o.logger.Info("fixer spawned", "task_id", taskID, "agent_id", ag.ID)
	return workerHandleString(*ag), nil
}

func workerHandleString(ag model.Agent) string {
	handle := workeridentity.FromAgent(ag)
	if handle.HasContainer() {
		return handle.ContainerID
	}
	return handle.TmuxSession
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
	cwd := filepath.Join(o.worktree.BareRepo(), o.worktree.DefaultBranchName())
	if task.WorktreeBranch != "" {
		candidate := filepath.Join(o.worktree.BareRepo(), task.WorktreeBranch, "integration")
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
		BareRepoPath:  o.worktree.BareRepo(),
		DefaultBranch: o.worktree.DefaultBranchName(),
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
