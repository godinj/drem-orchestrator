package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
)

// SetExperimentScheduling enables experiment-aware scheduling on the
// orchestrator. When experiments are active, normal tasks are paused
// and the agent pool is partitioned across experiment variants.
func (o *Orchestrator) SetExperimentScheduling(maxConcurrent int) {
	o.experimentScheduler = NewExperimentScheduler(o.db, maxConcurrent)
}

func (o *Orchestrator) processTestWriting(parent *model.Task) error {
	if err := o.ensureFeatureWorktree(parent, "process test writing"); err != nil {
		return err
	}
	if parent.Context == nil {
		parent.Context = make(model.JSONField)
	}
	if _, checked := parent.Context["baseline_tests_checked"]; !checked {
		if testCmd := o.getTestCommand(parent); testCmd != "" {
			featureName := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
			featureDir := o.worktree.FeatureWorktreePath(featureName)
			result, runErr := runCommand(featureDir, testCmd)
			parent.Context["baseline_tests_checked"] = true
			if runErr != nil || result.ExitCode != 0 {
				parent.Context["baseline_tests_failed"] = true
				parent.Context["baseline_test_output"] = truncate(result.Output, maxTestOutputLen)
				o.logger.Warn("baseline tests fail on integration branch", "task_id", parent.ID, "exit_code", result.ExitCode)
			}
			if err := o.db.Save(parent).Error; err != nil {
				return fmt.Errorf("process test writing: save baseline check: %w", err)
			}
			if runErr != nil || result.ExitCode != 0 {
				return nil
			}
		}
	}

	// Defensive subtask materialization: if the plan contains subtasks but
	// none exist in the DB, a plan approval bypassed HandlePlanApproved
	// (e.g. raw DB status update). Auto-materialize to prevent replan loops.
	if parent.Plan != nil {
		var existingCount int64
		o.db.Model(&model.Task{}).Where("parent_task_id = ?", parent.ID).Count(&existingCount)
		if existingCount == 0 {
			o.logger.Warn("auto-materializing subtasks from plan (approval bypassed subtask creation)",
				"task_id", parent.ID)
			if _, _, err := o.materializeSubtasks(parent); err != nil {
				return fmt.Errorf("process test writing: defensive materialize: %w", err)
			}
		}
	}

	if failed, ok := parent.Context["baseline_tests_failed"].(bool); ok && failed {
		o.logger.Warn("baseline tests failed but proceeding with test scheduling — agents work on worktree branches",
			"task_id", parent.ID)
	}
	if err := o.scheduleSubtasks(parent, "test"); err != nil {
		return fmt.Errorf("process test writing: schedule: %w", err)
	}

	var testSubtasks []model.Task
	if err := o.db.Where("parent_task_id = ? AND phase = ?", parent.ID, "test").
		Find(&testSubtasks).Error; err != nil {
		return fmt.Errorf("process test writing: query test subtasks: %w", err)
	}
	testSubtasks = activeTestWritingSubtasks(testSubtasks)

	switch o.subtaskRecovery.Evaluate(parent, len(testSubtasks)) {
	case RecoveryReplan:
		// Cap test_writing replans at 1. After that, flag for human review
		// instead of spinning through more planning cycles.
		replanCount := 0
		if v, ok := parent.Context["test_replan_count"].(float64); ok {
			replanCount = int(v)
		}
		if replanCount >= 1 {
			parent.Context["needs_human_review"] = true
			parent.Context["review_reason"] = "repeated empty test subtasks after replan"
			return o.failTask(parent, "repeated empty test subtasks — needs human review")
		}
		parent.Context["test_replan_count"] = float64(replanCount + 1)

		// Clear the plan so processPlanning spawns a new planner instead of
		// auto-advancing the same stale plan to PLAN_REVIEW.
		replanMsg := "Previous plan produced no test-phase subtasks. Re-plan with explicit test subtasks for each implementation subtask."
		parent.Plan = nil
		parent.PlanFeedback = replanMsg
		parent.AssignedAgentID = nil
		parent.Context["replan_directive"] = replanMsg
		// NOTE: Do NOT reset retry_count on replan. The global
		// total_planner_spawns cap prevents runaway spawning, and
		// per-cycle retries use the existing retry_count which accumulates.

		// Detach old subtasks to prevent duplicates and stale data.
		var oldSubtasks []model.Task
		o.db.Where("parent_task_id = ?", parent.ID).Find(&oldSubtasks)
		for i := range oldSubtasks {
			oldSubtasks[i].ParentTaskID = nil
			o.db.Save(&oldSubtasks[i])
		}
		if len(oldSubtasks) > 0 {
			o.logger.Info("detached old subtasks for replan",
				"task_id", parent.ID, "count", len(oldSubtasks))
		}

		if err := o.transitionTaskAtomic(parent, model.StatusPlanning, "orchestrator", "test_subtask_recovery",
			"empty test subtasks require replanning", nil); err != nil {
			return fmt.Errorf("process test writing: replan transition: %w", err)
		}
		o.emit("task_replan", map[string]any{"task_id": parent.ID})
		return nil
	case RecoveryFail:
		return o.failTask(parent, fmt.Sprintf("no test subtasks after %d recovery attempts", defaultMaxEmptyChecks))
	default:
		if err := o.db.Save(parent).Error; err != nil {
			return fmt.Errorf("process test writing: save recovery state: %w", err)
		}
	}
	allTerminal, anyFailed, allDone := true, false, true
	for _, sub := range testSubtasks {
		switch sub.Status {
		case model.StatusDone:
			// good
		case model.StatusCancelled:
			// Cancelled subtasks are terminal but don't count as failures.
			// They were intentionally removed (e.g. during replan) and should
			// not block completion of remaining test subtasks.
		case model.StatusFailed, model.StatusRejected:
			anyFailed = true
			allDone = false
		case model.StatusBacklog:
			// Future test subtasks may depend on implementation subtasks.
			// Those cannot run during test_writing, so they should not block
			// the initial test-review gate. Runnable backlog tests still block.
			depsMet, err := DependenciesMet(o.db, sub.DependencyIDs)
			if err != nil {
				return fmt.Errorf("process test writing: check test subtask dependencies: %w", err)
			}
			if depsMet {
				allTerminal = false
				allDone = false
			}
		default:
			allTerminal = false
			allDone = false
		}
	}

	if allDone {
		readiness, err := o.evaluateParentReadiness(parent, model.StatusTestReview)
		if err != nil {
			return fmt.Errorf("process test writing: parent readiness: %w", err)
		}
		if !readiness.Ready {
			if err := o.recordParentReadinessBlocked(parent, model.StatusTestReview, readiness); err != nil {
				return fmt.Errorf("process test writing: save readiness blockers: %w", err)
			}
			o.logger.Info("test review blocked by parent readiness", "task_id", parent.ID, "blockers", readiness.Blockers)
			return nil
		}

		// All test subtasks done -> transition to TEST_REVIEW.
		// Clear any blocking flags that may have been set by prior failure handling.
		delete(parent.Context, "baseline_tests_failed")
		delete(parent.Context, "needs_human_review")
		delete(parent.Context, "parent_readiness_target")
		delete(parent.Context, "parent_readiness_blockers")
		delete(parent.Context, "parent_readiness_blocker_count")

		pendingSourceLane, err := o.hasPendingSourceLaneSubtasks(parent.ID)
		if err != nil {
			return fmt.Errorf("process test writing: check pending source subtasks: %w", err)
		}

		// Run full constraint evaluation before test_review only when there is no
		// pending source-lane implementation work. Source-lane tasks get the full
		// constraint gate later at testing_ready, after implementation has run.
		if parent.WorktreeBranch != "" && !o.skipConstraintGate && !pendingSourceLane {
			blocked, err := o.evaluateConstraintGate(parent)
			if err != nil {
				return err
			}
			if blocked {
				return nil
			}
		}

		if err := o.transitionTaskAtomic(parent, model.StatusTestReview, "orchestrator", "test_subtask_completion",
			"all test subtasks completed", nil); err != nil {
			return fmt.Errorf("process test writing: transition to test_review: %w", err)
		}
		o.emit("test_review_ready", map[string]any{"task_id": parent.ID})
		o.logger.Info("all test subtasks done, test review ready", "task_id", parent.ID)
	} else if allTerminal && anyFailed {
		// All test subtasks terminal but some failed -> fail the parent.
		var failedNames []string
		for _, sub := range testSubtasks {
			if sub.Status == model.StatusFailed || sub.Status == model.StatusRejected {
				failedNames = append(failedNames, fmt.Sprintf("%s (%s: %s)", sub.ID, sub.Status, sub.Title))
			}
		}
		if err := o.failTask(parent, fmt.Sprintf("test subtasks failed: %s",
			strings.Join(failedNames, ", "))); err != nil {
			return err
		}
	}

	return nil
}

func activeTestWritingSubtasks(subtasks []model.Task) []model.Task {
	newestByTitle := make(map[string]model.Task, len(subtasks))
	for _, sub := range subtasks {
		key := testWritingTitleKey(sub.Title)
		newest, ok := newestByTitle[key]
		if !ok || sub.CreatedAt.After(newest.CreatedAt) {
			newestByTitle[key] = sub
		}
	}

	active := make([]model.Task, 0, len(subtasks))
	for _, sub := range subtasks {
		if sub.Status == model.StatusRejected {
			if newest, ok := newestByTitle[testWritingTitleKey(sub.Title)]; ok && newest.ID != sub.ID {
				continue
			}
		}
		active = append(active, sub)
	}
	return active
}

func testWritingTitleKey(title string) string {
	for {
		idx := strings.LastIndex(title, " (revision ")
		if idx < 0 || !strings.HasSuffix(title, ")") {
			return title
		}
		revision := title[idx+len(" (revision ") : len(title)-1]
		if revision == "" {
			return title
		}
		for _, r := range revision {
			if r < '0' || r > '9' {
				return title
			}
		}
		title = title[:idx]
	}
}

func (o *Orchestrator) hasPendingSourceLaneSubtasks(parentID uuid.UUID) (bool, error) {
	var count int64
	if err := o.db.Model(&model.Task{}).
		Where("parent_task_id = ? AND phase IN ? AND status NOT IN ?",
			parentID,
			[]string{"implementation", "integration"},
			[]model.TaskStatus{model.StatusDone, model.StatusFailed, model.StatusRejected, model.StatusCancelled}).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (o *Orchestrator) spawnDiagnosticAgent(parent *model.Task) error {
	// Gather rejection history from the parent context.
	var rounds []string
	for i := 1; i <= 3; i++ {
		key := fmt.Sprintf("test_rejection_feedback_%d", i)
		if fb, ok := parent.Context[key].(string); ok {
			rounds = append(rounds, fmt.Sprintf("Round %d feedback: %s", i, fb))
		}
	}

	// Gather test subtask history (all test-phase subtasks including rejected).
	var testSubtasks []model.Task
	if err := o.db.Where(
		"parent_task_id = ? AND phase = ?",
		parent.ID, "test",
	).Order("created_at asc").Find(&testSubtasks).Error; err != nil {
		o.logger.Warn("diagnostic agent: failed to load test subtasks", "error", err)
	}

	var subtaskSummary []string
	for _, sub := range testSubtasks {
		subtaskSummary = append(subtaskSummary,
			fmt.Sprintf("- %s [%s]", sub.Title, sub.Status))
	}

	diagnosticPrompt := fmt.Sprintf(
		"The tests for this task have been rejected 3 times. Help the human understand why.\n\n"+
			"Task: %s\n%s\n\n"+
			"Test subtask history:\n%s\n\n"+
			"Rejection rounds:\n%s\n\n"+
			"Summarize the pattern of rejections and suggest a path forward.\n"+
			"Either the test premise is wrong, the acceptance criteria are ambiguous,\n"+
			"or there's a misunderstanding.",
		parent.Title,
		parent.Description,
		strings.Join(subtaskSummary, "\n"),
		strings.Join(rounds, "\n"),
	)

	// Store the diagnostic prompt in the parent context for reference.
	parent.Context["diagnostic_prompt"] = diagnosticPrompt
	if err := o.db.Save(parent).Error; err != nil {
		return fmt.Errorf("spawn diagnostic agent: save context: %w", err)
	}

	// If the runner is not available, log and return.
	if o.runner == nil {
		o.logger.Warn("diagnostic agent: runner not available, diagnostic prompt stored in task context",
			"task_id", parent.ID)
		return nil
	}

	// Spawn a reviewer-type agent. Use the integration branch worktree if available.
	featureName := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
	worktreePath := ""
	if featureName != "" && o.worktree != nil {
		worktreePath = o.worktree.FeatureWorktreePath(featureName)
	}

	ag := model.Agent{
		ID:             uuid.New(),
		ProjectID:      parent.ProjectID,
		AgentType:      model.AgentReviewer,
		Name:           fmt.Sprintf("diagnostic-%s", parent.ID.String()[:8]),
		Status:         model.AgentWorking,
		CurrentTaskID:  &parent.ID,
		WorktreePath:   worktreePath,
		WorktreeBranch: parent.WorktreeBranch,
	}

	if err := o.db.Create(&ag).Error; err != nil {
		return fmt.Errorf("spawn diagnostic agent: create agent record: %w", err)
	}

	o.logger.Info("diagnostic agent spawned",
		"task_id", parent.ID,
		"agent_id", ag.ID)

	return nil
}

// extractTestFiles runs git diff --name-only on the agent's worktree and
// returns files matching test patterns.
func (o *Orchestrator) extractTestFiles(worktreePath, baseBranch string) []string {
	output, err := gitexec.RunGit(context.Background(), worktreePath,
		"diff", "--name-only", baseBranch+"...HEAD",
	)
	if err != nil {
		o.logger.Warn("extract test files: git diff failed", "path", worktreePath, "error", err)
		return nil
	}
	if output == "" {
		return nil
	}

	var testFiles []string
	for _, file := range strings.Split(output, "\n") {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		if isTestFile(file) {
			testFiles = append(testFiles, file)
		}
	}
	return testFiles
}

// isTestFile checks if a filename matches common test file patterns.
func isTestFile(name string) bool {
	base := filepath.Base(name)
	lower := strings.ToLower(base)

	// Go: *_test.go
	if strings.HasSuffix(lower, "_test.go") {
		return true
	}
	// Python: test_*.py or *_test.py
	if strings.HasSuffix(lower, ".py") && (strings.HasPrefix(lower, "test_") || strings.HasSuffix(lower, "_test.py")) {
		return true
	}
	// JavaScript/TypeScript: *.test.ts, *.test.js, *.spec.ts, *.spec.js
	for _, suffix := range []string{".test.ts", ".test.js", ".spec.ts", ".spec.js", ".test.tsx", ".test.jsx", ".spec.tsx", ".spec.jsx"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	// C++: *Test.cpp, *Tests.cpp, *_test.cpp, *_tests.cpp, *Test.h, *Tests.h
	for _, suffix := range []string{"test.cpp", "tests.cpp", "_test.cpp", "_tests.cpp", "test.h", "tests.h"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	// C++: files under tests/ or test/ directories
	normalizedPath := strings.ToLower(name)
	for _, prefix := range []string{"tests/", "test/"} {
		if strings.HasPrefix(normalizedPath, prefix) || strings.Contains(normalizedPath, "/"+prefix) {
			// Only match C++ source/header files in test directories
			for _, ext := range []string{".cpp", ".cc", ".cxx", ".h", ".hpp"} {
				if strings.HasSuffix(lower, ext) {
					return true
				}
			}
		}
	}
	return false
}
