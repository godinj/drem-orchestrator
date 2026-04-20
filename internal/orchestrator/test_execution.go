package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
)

// processTestingReady runs the automated test gate at TESTING_READY.
// It checks for an existing fixer/reviewer agent, runs the test suite,
// and either transitions to MERGING or spawns a fixer.
func (o *Orchestrator) processTestingReady(parent *model.Task) error {
	// Check if a reviewer or fixer is already running for this task.
	var busy model.Agent
	err := o.db.Where("current_task_id = ? AND status = ? AND agent_type IN ?",
		parent.ID, model.AgentWorking,
		[]model.AgentType{model.AgentReviewer, model.AgentFixer}).
		First(&busy).Error
	if err == nil {
		// Agent already running — skip.
		return nil
	}

	// Check if we already ran the automated gate and it passed.
	if parent.Context != nil {
		if passed, ok := parent.Context["automated_gate_passed"].(bool); ok && passed {
			return nil
		}
	}

	// Resolve integration worktree.
	worktreePath := o.resolveIntegrationWorktree(parent)
	if worktreePath == "" {
		o.logger.Warn("processTestingReady: no integration worktree", "task_id", parent.ID)
		return nil
	}

	// Run the test suite.
	testsPassed, testOutput := o.runTestSuite(worktreePath)

	if testsPassed {
		// Tests pass → transition to MERGING.
		if parent.Context == nil {
			parent.Context = make(model.JSONField)
		}
		parent.Context["automated_gate_passed"] = true

		evt, err := state.TransitionTask(parent, model.StatusMerging, "orchestrator",
			map[string]any{"reason": "automated test gate passed"})
		if err != nil {
			return fmt.Errorf("processTestingReady: transition to merging: %w", err)
		}
		if err := o.db.Save(parent).Error; err != nil {
			return fmt.Errorf("processTestingReady: save parent: %w", err)
		}
		if err := o.db.Create(evt).Error; err != nil {
			return fmt.Errorf("processTestingReady: save event: %w", err)
		}

		o.emit("testing_ready_passed", map[string]any{"task_id": parent.ID})
		o.publishTaskTransition(parent.ID.String(), evt.OldValue, evt.NewValue, "automated test gate passed")
		o.logger.Info("automated test gate passed, transitioning to merging", "task_id", parent.ID)
		return nil
	}

	// Tests failed — check if a fixer already attempted and failed.
	if parent.Context == nil {
		parent.Context = make(model.JSONField)
	}

	fixerAttempted := false
	if v, ok := parent.Context["testing_ready_fixer_attempted"].(bool); ok && v {
		fixerAttempted = true
	}

	if fixerAttempted {
		// Fixer already tried and failed → flag for human review.
		parent.Context["needs_human_review"] = true
		parent.Context["testing_ready_failure"] = truncate(testOutput, maxTestFailureLen)
		if err := o.db.Save(parent).Error; err != nil {
			return fmt.Errorf("processTestingReady: save parent after fixer failure: %w", err)
		}
		o.emit("testing_ready_needs_human", map[string]any{
			"task_id": parent.ID,
			"reason":  "fixer failed to resolve test failures",
		})
		o.logger.Warn("testing_ready fixer failed, needs human review", "task_id", parent.ID)
		return nil
	}

	// Spawn a fixer agent on the integration worktree.
	parent.Context["testing_ready_fixer_attempted"] = true
	parent.Context["test_failure_output"] = truncate(testOutput, maxTestFailureLen)

	// Get the diff between feature branch and default branch.
	var gitDiff string
	diff, diffErr := gitexec.RunGit(
		context.Background(), worktreePath,
		"diff", o.worktree.DefaultBranchName()+"...HEAD",
	)
	if diffErr == nil {
		gitDiff = truncate(diff, maxGitDiffLen)
	}

	fixerPrompt := fmt.Sprintf(`Fix the integration failures. Prefer fixing implementation code over modifying tests.

## Test Failure Output
%s

## Changes (diff vs %s)
%s

## Task
Title: %s
Description: %s
`, truncate(testOutput, maxTestOutputLen), o.worktree.DefaultBranchName(), gitDiff, parent.Title, parent.Description)

	// Container-mode dispatch: when o.Spawner is wired, the fixer
	// runs in a worker container. The detailed fixerPrompt built above
	// (with test failure output + diff) is NOT plumbed through
	// buildSpawnContext yet — see
	// plans/phase-3.5-subtask-dispatch-migration.md §"Open questions"
	// Q2. The container-path prompt carries only the default
	// prompt.Opts (task title + description + project context); the
	// test-failure output flows through parent.Context (saved below)
	// and the worker can read it via the DREM_TASK_ID → GET
	// /tasks/{id} HTTP path. This is a deliberate narrowing for
	// dispatch-migration-unblocking; plumb the prompt enrichment in
	// a separate follow-up.
	// See plans/phase-3.5-subtask-dispatch-migration.md Commit 5.
	if o.Spawner != nil {
		// Save parent.Context updates (testing_ready_fixer_attempted,
		// test_failure_output) BEFORE the spawn so the worker sees them
		// when it reads the task back from orch.
		if err := o.db.Save(parent).Error; err != nil {
			return fmt.Errorf("processTestingReady: save parent context before spawn: %w", err)
		}
		// spawnFixer expects a clean AssignedAgentID — clear any prior
		// coder assignment so recordContainerOnAgent creates a fresh
		// fixer Agent row. The legacy runner.SpawnAgentInWorktree path
		// creates a new Agent row unconditionally; the container path
		// needs this explicit clear because recordContainerOnAgent
		// updates rather than replaces.
		prevAgentID := parent.AssignedAgentID
		parent.AssignedAgentID = nil
		if err := o.db.Save(parent).Error; err != nil {
			return fmt.Errorf("processTestingReady: clear assignment before spawn: %w", err)
		}
		if err := o.spawnFixer(context.Background(), parent); err != nil {
			o.logger.Error("processTestingReady: spawn fixer failed via spawner",
				"task_id", parent.ID, "error", err)
			// Restore the prior assignment so reconciliation still
			// tracks the originating worker before we gate for human
			// review.
			parent.AssignedAgentID = prevAgentID
			parent.Context["needs_human_review"] = true
			if err := o.db.Save(parent).Error; err != nil {
				return fmt.Errorf("processTestingReady: save parent after failed container spawn: %w", err)
			}
			return nil
		}
		// Reload to pick up AssignedAgentID written by
		// recordContainerOnAgent.
		if err := o.db.First(parent, "id = ?", parent.ID).Error; err != nil {
			return fmt.Errorf("processTestingReady: reload parent after container spawn: %w", err)
		}
		if parent.AssignedAgentID == nil {
			o.logger.Error("processTestingReady: no agent assignment after container spawn",
				"task_id", parent.ID)
			parent.Context["needs_human_review"] = true
			if err := o.db.Save(parent).Error; err != nil {
				return fmt.Errorf("processTestingReady: save parent after missing assignment: %w", err)
			}
			return nil
		}
		// Silences "declared and not used" — fixerPrompt is retained
		// verbatim so the legacy fallback block below continues to
		// build and consume it without duplicating the prompt
		// construction. The container path intentionally does not
		// consume fixerPrompt; see §Q2.
		_ = fixerPrompt
		o.emit("testing_ready_fixer_spawned", map[string]any{
			"task_id":  parent.ID,
			"agent_id": *parent.AssignedAgentID,
		})
		o.logger.Info("testing_ready: fixer spawned via spawner for test failures",
			"task_id", parent.ID, "fixer_id", *parent.AssignedAgentID)
		return nil
	}

	if o.runner == nil {
		o.logger.Error("processTestingReady: runner is nil, cannot spawn fixer", "task_id", parent.ID)
		parent.Context["needs_human_review"] = true
		if err := o.db.Save(parent).Error; err != nil {
			return fmt.Errorf("processTestingReady: save parent: %w", err)
		}
		return nil
	}
	fixerAg, spawnErr := o.runner.SpawnAgentInWorktree(parent, worktreePath, model.AgentFixer, fixerPrompt)
	if spawnErr != nil {
		o.logger.Error("processTestingReady: spawn fixer failed", "task_id", parent.ID, "error", spawnErr)
		parent.Context["needs_human_review"] = true
		if err := o.db.Save(parent).Error; err != nil {
			return fmt.Errorf("processTestingReady: save parent: %w", err)
		}
		return nil
	}

	if err := o.db.Save(parent).Error; err != nil {
		return fmt.Errorf("processTestingReady: save parent: %w", err)
	}

	o.emit("testing_ready_fixer_spawned", map[string]any{
		"task_id":  parent.ID,
		"agent_id": fixerAg.ID,
	})
	o.logger.Info("testing_ready: fixer spawned for test failures",
		"task_id", parent.ID, "fixer_id", fixerAg.ID)
	return nil
}

// runTestSuite runs the test suite in the given worktree and returns whether
// tests passed and the combined output.
func (o *Orchestrator) runTestSuite(worktreePath string) (passed bool, output string) {
	cmd := o.testGate.TestCommand
	if cmd == "" {
		cmd = "go test ./..."
	}
	result, err := runCommand(worktreePath, cmd)
	if err != nil || result.ExitCode != 0 {
		return false, result.Output
	}
	return true, result.Output
}

// ---------------------------------------------------------------------------
// Test gate — pre-merge test/compilation verification
// ---------------------------------------------------------------------------

// taskPhase returns the phase string for a task, reading from
// task.Context["phase"]. Returns "" if not set.
func taskPhase(task *model.Task) string {
	if task.Context == nil {
		return ""
	}
	phase, _ := task.Context["phase"].(string)
	return phase
}

// getTestCommand returns the test command to run for the given subtask.
// It checks the orchestrator's TestGateConfig first, then falls back to
// inferring from the worktree contents.
func (o *Orchestrator) getTestCommand(subtask *model.Task) string {
	if o.testGate.TestCommand != "" {
		return o.testGate.TestCommand
	}
	// Infer from worktree contents. Use the agent's worktree path from the
	// subtask's assigned agent.
	if subtask.AssignedAgentID != nil {
		var ag model.Agent
		if err := o.db.First(&ag, "id = ?", subtask.AssignedAgentID).Error; err == nil && ag.WorktreePath != "" {
			return inferTestCommand(ag.WorktreePath)
		}
	}
	return ""
}

// inferTestCommand detects the project type and returns the appropriate
// test command, or "" if none can be determined.
func inferTestCommand(dir string) string {
	if fileExistsAt(filepath.Join(dir, "go.mod")) {
		return "go test ./..."
	}
	if fileExistsAt(filepath.Join(dir, "package.json")) {
		return "npm test"
	}
	if fileExistsAt(filepath.Join(dir, "pyproject.toml")) {
		return "pytest"
	}
	if fileExistsAt(filepath.Join(dir, "Cargo.toml")) {
		return "cargo test"
	}
	return ""
}

// inferCompileCommand detects the project type and returns the appropriate
// compilation check command, or "" if none can be determined.
func inferCompileCommand(dir string) string {
	if fileExistsAt(filepath.Join(dir, "go.mod")) {
		return "go vet ./..."
	}
	if fileExistsAt(filepath.Join(dir, "tsconfig.json")) {
		return "npx tsc --noEmit"
	}
	if fileExistsAt(filepath.Join(dir, "Cargo.toml")) {
		return "cargo check"
	}
	return ""
}

// fileExistsAt returns true if path exists and is a regular file.
func fileExistsAt(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// verifyTestsBeforeMerge runs the test suite in the agent's worktree and
// returns the result. Retries up to 3 times with backoff for environmental
// flakiness. Only applies to implementation and integration phase subtasks;
// test-phase subtasks use verifyCompilationBeforeMerge instead.
func (o *Orchestrator) verifyTestsBeforeMerge(subtask *model.Task, worktreePath string) (*TestResult, error) {
	testCmd := o.getTestCommand(subtask)
	if testCmd == "" {
		o.logger.Warn("no test command configured, skipping test gate (degraded mode)",
			"subtask_id", subtask.ID)
		return &TestResult{Passed: true, RunAt: time.Now()}, nil
	}

	// Determine if scoped execution applies.
	scoped := false
	if o.testGate.ScopedTests {
		scopedCmd, didScope := o.scopeTestsForSubtask(testCmd, worktreePath)
		if didScope {
			testCmd = scopedCmd
			scoped = true
		}
	}

	timeout := o.testGate.TestTimeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	var lastResult *TestResult
	for attempt := 1; attempt <= maxBuildRetries; attempt++ {
		cmdResult := o.runCommandWithTimeout(worktreePath, testCmd, timeout)
		lastResult = &TestResult{
			Passed:       cmdResult.ExitCode == 0,
			Output:       truncate(cmdResult.Output, maxCmdOutputLen),
			ExitCode:     cmdResult.ExitCode,
			RunAt:        time.Now(),
			Duration:     cmdResult.Duration.Seconds(),
			Command:      testCmd,
			Scoped:       scoped,
			AttemptCount: attempt,
		}
		if lastResult.Passed {
			return lastResult, nil
		}
		if attempt < maxBuildRetries {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
	}
	return lastResult, nil
}

// verifyCompilationBeforeMerge runs the compilation command for test-phase
// subtasks. Test execution results are ignored — only compilation matters.
func (o *Orchestrator) verifyCompilationBeforeMerge(subtask *model.Task, worktreePath string) (*TestResult, error) {
	compileCmd := o.testGate.CompileCommand
	if compileCmd == "" {
		compileCmd = inferCompileCommand(worktreePath)
	}
	if compileCmd == "" {
		o.logger.Warn("no compile command found, skipping compilation gate",
			"subtask_id", subtask.ID)
		return &TestResult{Passed: true, RunAt: time.Now()}, nil
	}

	timeout := o.testGate.TestTimeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	cmdResult := o.runCommandWithTimeout(worktreePath, compileCmd, timeout)
	return &TestResult{
		Passed:       cmdResult.ExitCode == 0,
		Output:       truncate(cmdResult.Output, maxCmdOutputLen),
		ExitCode:     cmdResult.ExitCode,
		RunAt:        time.Now(),
		Duration:     cmdResult.Duration.Seconds(),
		Command:      compileCmd,
		Scoped:       false,
		AttemptCount: 1,
	}, nil
}

// scopeTestsForSubtask derives changed packages from the agent's diff and
// scopes the test command. Returns the scoped command and true if scoping
// was applied.
func (o *Orchestrator) scopeTestsForSubtask(baseCmd, worktreePath string) (string, bool) {
	// Get changed files via git diff.
	diffOutput, err := gitexec.RunGit(context.Background(), worktreePath, "diff", "--name-only", "HEAD~1")
	if err != nil {
		// Also try against the default branch.
		diffOutput, err = gitexec.RunGit(context.Background(), worktreePath,
			"diff", "--name-only", o.worktree.DefaultBranchName()+"...HEAD",
		)
		if err != nil {
			return baseCmd, false
		}
	}

	if diffOutput == "" {
		return baseCmd, false
	}

	changedFiles := strings.Split(strings.TrimSpace(diffOutput), "\n")
	scopedCmd, didScope := scopeTestCommand(baseCmd, changedFiles)
	return scopedCmd, didScope
}

// scopeTestCommand takes a base test command and a list of changed files,
// and returns a scoped command that only tests affected packages.
// Returns the original command if scoping isn't possible.
func scopeTestCommand(baseCmd string, changedFiles []string) (string, bool) {
	if len(changedFiles) == 0 {
		return baseCmd, false
	}

	// Only scope Go test commands that use ./...
	if !strings.Contains(baseCmd, "./...") {
		return baseCmd, false
	}

	// Map changed .go files to their package directories.
	pkgSet := make(map[string]struct{})
	for _, f := range changedFiles {
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		dir := filepath.Dir(f)
		if dir == "." {
			pkgSet["./..."] = struct{}{}
		} else {
			pkgSet["./"+dir+"/..."] = struct{}{}
		}
	}

	if len(pkgSet) == 0 {
		return baseCmd, false
	}

	// Build sorted package list for deterministic output.
	var pkgs []string
	for p := range pkgSet {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)

	// If ./... is in the set (root-level files changed), fall back to full.
	for _, p := range pkgs {
		if p == "./..." {
			return baseCmd, false
		}
	}

	scopedCmd := strings.Replace(baseCmd, "./...", strings.Join(pkgs, " "), 1)
	return scopedCmd, true
}

// runCommandWithTimeout executes a shell command with a timeout.
// Returns the combined output, exit code, and duration. Uses process
// group killing to ensure child processes are cleaned up on timeout.
func (o *Orchestrator) runCommandWithTimeout(dir, cmd string, timeout time.Duration) *commandResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Dir = dir
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		// Kill the entire process group so child processes are cleaned up.
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}

	var outBuf bytes.Buffer
	c.Stdout = &outBuf
	c.Stderr = &outBuf

	err := c.Run()
	elapsed := time.Since(start)

	exitCode := 0
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return &commandResult{
				Output:   truncate(outBuf.String(), maxCmdOutputLen) + "\n[killed: timeout exceeded]",
				ExitCode: -1,
				Duration: elapsed,
			}
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	return &commandResult{
		Output:   outBuf.String(),
		ExitCode: exitCode,
		Duration: elapsed,
	}
}

// storeTestResult persists the test result on the agent's Config field.
func (o *Orchestrator) storeTestResult(ag *model.Agent, result *TestResult) {
	if ag.Config == nil {
		ag.Config = make(model.JSONField)
	}
	ag.Config["last_test_result"] = result
	if err := o.db.Model(ag).Update("config", ag.Config).Error; err != nil {
		o.logger.Warn("failed to store test result on agent", "agent_id", ag.ID, "error", err)
	}
}

// depthScoreThreshold is the minimum depth score (0.0–1.0) a plan must achieve
// to proceed to human review without supervisor diagnosis.
const depthScoreThreshold = 0.5

// checkPlanDepthGate evaluates the plan's depth score and, if below threshold,
// requests a supervisor diagnosis. The diagnosis is stored as a system comment
// and in the task context. This is advisory only — the plan always proceeds to
// human review regardless of the supervisor outcome.
func (o *Orchestrator) checkPlanDepthGate(task *model.Task, scores map[string]any) {
	depthScore, ok := scores["depth"].(float64)
	if !ok {
		return // no depth score available
	}

	if depthScore >= depthScoreThreshold {
		return // depth is acceptable
	}

	if o.supervisor == nil {
		o.logger.Warn("plan depth score below threshold but no supervisor configured",
			"task_id", task.ID, "depth_score", depthScore)
		return
	}

	// Serialize plan for the supervisor prompt.
	planJSON := ""
	if task.Plan != nil {
		if data, err := json.MarshalIndent(task.Plan, "", "  "); err == nil {
			planJSON = string(data)
		}
	}

	var review supervisor.PlanDepthReview
	prompt := supervisor.PlanDepthReviewPrompt(task.Title, task.Description, planJSON, depthScore)

	if err := o.supervisor.EvaluateJSON(context.Background(), prompt, &review); err != nil {
		o.logger.Warn("supervisor plan depth review failed, continuing",
			"task_id", task.ID, "error", err)
		return
	}

	// Store the review in task context.
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["depth_review"] = map[string]any{
		"assessment":       review.Assessment,
		"shallow_areas":    review.ShallowAreas,
		"recommendations":  review.Recommendations,
		"rejection_reason": review.RejectionReason,
	}

	// Add supervisor diagnosis as a system comment on the task.
	if review.RejectionReason != "" {
		comment := model.TaskComment{
			TaskID: task.ID,
			Author: "system",
			Body:   fmt.Sprintf("[Depth Review] Score: %.0f%% — %s", depthScore*100, review.RejectionReason),
		}
		if err := o.db.Create(&comment).Error; err != nil {
			o.logger.Warn("failed to create depth review comment", "task_id", task.ID, "error", err)
		}
	}

	o.logger.Info("plan depth review completed",
		"task_id", task.ID, "depth_score", depthScore, "assessment", review.Assessment)
}
