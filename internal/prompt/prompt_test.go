package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestPlannerInstructionsContainsNewSections(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	requiredHeaders := []string{
		"## Coverage Verification",
		"## Integration Subtask",
		"## Decomposition Rules",
		"## File Overlap",
		"### Test Subtask Requirements",
	}

	for _, header := range requiredHeaders {
		if !strings.Contains(output, header) {
			t.Errorf("plannerInstructions() missing section header: %q", header)
		}
	}
}

func TestPlannerInstructionsPreservesExistingContent(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	// Verify existing content is still present.
	existingContent := []string{
		"You are a planner agent",
		"plan.json",
		"Each subtask should be independently implementable",
		"List specific files each subtask will create or modify",
		"Set dependencies between subtasks where order matters",
		`"coder" for implementation, "researcher" for investigation`,
	}

	for _, content := range existingContent {
		if !strings.Contains(output, content) {
			t.Errorf("plannerInstructions() missing existing content: %q", content)
		}
	}
}

func TestPlannerInstructionsDecompositionRulesContent(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	// Verify key decomposition guidance is present.
	keyGuidance := []string{
		"Decompose along functional boundaries",
		"Decompose by code layer",
		"Plan more than 8 subtasks",
		"Omit the files list",
	}

	for _, g := range keyGuidance {
		if !strings.Contains(output, g) {
			t.Errorf("plannerInstructions() missing decomposition guidance: %q", g)
		}
	}
}

func TestPlannerInstructionsIntegrationSubtaskContent(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	keyContent := []string{
		"Wires together the components",
		"dependencies on ALL other implementation subtasks",
		"end-to-end functionality",
	}

	for _, c := range keyContent {
		if !strings.Contains(output, c) {
			t.Errorf("plannerInstructions() missing integration subtask guidance: %q", c)
		}
	}
}

func TestPlannerInstructionsTestSubtasksContent(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	keyContent := []string{
		"Has NO dependencies on implementation subtasks",
		"test subtasks -> HUMAN REVIEW -> implementation subtasks -> integration subtask",
	}

	for _, c := range keyContent {
		if !strings.Contains(output, c) {
			t.Errorf("plannerInstructions() missing test subtask guidance: %q", c)
		}
	}
}

// minimalOpts returns a basic Opts struct suitable for testing.
func minimalOpts() Opts {
	return Opts{
		Task: &model.Task{
			Title:       "Test Task",
			Description: "A test task description",
		},
		Project: &model.Project{
			Name:         "test-project",
			BareRepoPath: "/tmp/test-repo.git",
		},
		AgentType:    model.AgentCoder,
		WorktreePath: "/tmp/worktrees/test-branch",
	}
}

// ── 1. coderInstructions dispatch ──────────────────────────────────────────

func TestGenerate_CoderDefaultPhase(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentCoder
	output := Generate(opts)

	if !strings.Contains(output, "Implement the described task") {
		t.Error("expected default coder instructions containing 'Implement the described task'")
	}
}

func TestGenerate_CoderIntegrationPhase(t *testing.T) {
	// With no phase-specific dispatch, the coder should use default instructions.
	opts := minimalOpts()
	opts.AgentType = model.AgentCoder
	output := Generate(opts)

	if !strings.Contains(output, "Implement the described task") {
		t.Error("expected default coder instructions for unknown phase")
	}
}

// ── 2. coderInstructions detail tests ──────────────────────────────────────

func TestGenerate_CoderWithEstimatedFiles(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentCoder
	opts.Task.Context = model.JSONField{
		"estimated_files": []any{"foo_test.go", "bar.go"},
	}
	output := Generate(opts)

	if !strings.Contains(output, "foo_test.go") {
		t.Error("expected estimated file 'foo_test.go' in output")
	}
	if !strings.Contains(output, "bar.go") {
		t.Error("expected estimated file 'bar.go' in output")
	}
}

func TestGenerate_CoderWithTestPlan(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentCoder
	opts.Task.TestPlan = "Test X and Y"
	output := Generate(opts)

	if !strings.Contains(output, "## Test Plan") {
		t.Error("expected '## Test Plan' section header")
	}
	if !strings.Contains(output, "Test X and Y") {
		t.Error("expected test plan content 'Test X and Y' in output")
	}
}

func TestGenerate_CoderNoEstimatedFiles(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentCoder
	// No context set
	output := Generate(opts)

	if !strings.Contains(output, "Implement the described task") {
		t.Error("expected default coder instructions")
	}
	if strings.Contains(output, "Files to create/modify") {
		t.Error("did not expect 'Files to create/modify' when no estimated files set")
	}
}

func TestGenerate_DefaultCoder_WithEstimatedFiles(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentCoder
	opts.Task.Context = model.JSONField{
		"estimated_files": []any{"main.go"},
	}
	output := Generate(opts)

	if !strings.Contains(output, "main.go") {
		t.Error("expected 'main.go' in output when set as estimated file")
	}
}

func TestGenerate_DefaultCoder_WithTestPlan(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentCoder
	opts.Task.TestPlan = "Run go test"
	output := Generate(opts)

	if !strings.Contains(output, "Run go test") {
		t.Error("expected test plan 'Run go test' in output")
	}
}

// ── 3. reviewerInstructions dispatch ───────────────────────────────────────

func TestGenerate_PlanReviewer(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentReviewer
	opts.ReviewMode = "plan"
	opts.PlanJSON = `{"subtasks": []}`

	output := Generate(opts)

	if !strings.Contains(output, "plan reviewer") {
		t.Error("expected 'plan reviewer' in output")
	}
	if !strings.Contains(output, "Review Criteria") {
		t.Error("expected 'Review Criteria' section")
	}
	if !strings.Contains(output, `{"subtasks": []}`) {
		t.Error("expected plan JSON in output")
	}
}

func TestGenerate_FeatureReviewer(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentReviewer
	opts.ReviewMode = "feature"
	opts.GitDiff = "+added line"

	output := Generate(opts)

	if !strings.Contains(output, "feature reviewer") {
		t.Error("expected 'feature reviewer' in output")
	}
	if !strings.Contains(output, "+added line") {
		t.Error("expected diff content in output")
	}
}

// ── 4. planReviewerInstructions details ────────────────────────────────────

func TestGenerate_PlanReviewer_ReviewCriteria(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentReviewer
	opts.ReviewMode = "plan"
	opts.PlanJSON = `{}`

	output := Generate(opts)

	criteria := []string{
		"Coverage",
		"File overlap",
		"Integration",
		"Decomposition quality",
		"Dependency correctness",
	}

	for _, c := range criteria {
		if !strings.Contains(output, c) {
			t.Errorf("expected review criterion %q in output", c)
		}
	}
}

func TestGenerate_PlanReviewer_OutputSchema(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentReviewer
	opts.ReviewMode = "plan"
	opts.PlanJSON = `{}`

	output := Generate(opts)

	schemaKeys := []string{
		"coverage",
		"recommendation",
	}

	for _, k := range schemaKeys {
		if !strings.Contains(output, k) {
			t.Errorf("expected JSON schema key %q in output", k)
		}
	}
}

func TestGenerate_PlanReviewer_NoPlanJSON(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentReviewer
	opts.ReviewMode = "plan"
	opts.PlanJSON = "" // No plan JSON

	output := Generate(opts)

	// The "## Plan" section should not appear when PlanJSON is empty.
	if strings.Contains(output, "## Plan\n") {
		t.Error("did not expect '## Plan' section when PlanJSON is empty")
	}
}

// ── 5. featureReviewerInstructions details ─────────────────────────────────

func TestGenerate_FeatureReviewer_DiffTruncation(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentReviewer
	opts.ReviewMode = "feature"
	// Create a diff longer than 50000 characters.
	opts.GitDiff = strings.Repeat("a", 50001)

	output := Generate(opts)

	if !strings.Contains(output, "truncated") {
		t.Error("expected 'truncated' marker in output for large diff")
	}
}

func TestGenerate_FeatureReviewer_NoDiff(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentReviewer
	opts.ReviewMode = "feature"
	opts.GitDiff = "" // No diff

	output := Generate(opts)

	if strings.Contains(output, "## Changes") {
		t.Error("did not expect '## Changes' section when no diff provided")
	}
}

func TestGenerate_FeatureReviewer_OutputSchema(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentReviewer
	opts.ReviewMode = "feature"
	opts.GitDiff = "+some change"

	output := Generate(opts)

	schemaKeys := []string{
		"criteria_results",
		"recommendation",
	}

	for _, k := range schemaKeys {
		if !strings.Contains(output, k) {
			t.Errorf("expected JSON schema key %q in feature review output", k)
		}
	}
}

// ── 6. Generate() conditional sections ─────────────────────────────────────

func TestGenerate_WithComments(t *testing.T) {
	opts := minimalOpts()
	opts.Comments = []model.TaskComment{
		{Author: "user", Body: "Please add logging"},
		{Author: "system", Body: "Build failed on retry 2"},
	}

	output := Generate(opts)

	if !strings.Contains(output, "## User Feedback Comments") {
		t.Error("expected '## User Feedback Comments' section header")
	}
	if !strings.Contains(output, "Please add logging") {
		t.Error("expected first comment body in output")
	}
	if !strings.Contains(output, "Build failed on retry 2") {
		t.Error("expected second comment body in output")
	}
}

func TestGenerate_WithParentCtx(t *testing.T) {
	opts := minimalOpts()
	opts.ParentCtx = map[string]any{
		"parent_goal": "Implement feature X",
	}

	output := Generate(opts)

	if !strings.Contains(output, "## Parent Task Context") {
		t.Error("expected '## Parent Task Context' section header")
	}
	if !strings.Contains(output, "Implement feature X") {
		t.Error("expected parent context value in output")
	}
}

func TestGenerate_WithPromptAdjustment(t *testing.T) {
	opts := minimalOpts()
	opts.Task.Context = model.JSONField{
		"prompt_adjustment": "try a different approach",
	}

	output := Generate(opts)

	if !strings.Contains(output, "## Additional Guidance from Prior Attempt") {
		t.Error("expected '## Additional Guidance from Prior Attempt' section header")
	}
	if !strings.Contains(output, "try a different approach") {
		t.Error("expected prompt adjustment text in output")
	}
}

func TestGenerate_WithTaskContext(t *testing.T) {
	opts := minimalOpts()
	opts.Task.Context = model.JSONField{
		"retry_count": 2,
	}

	output := Generate(opts)

	if !strings.Contains(output, "## Additional Context") {
		t.Error("expected '## Additional Context' section header")
	}
	if !strings.Contains(output, "retry_count") {
		t.Error("expected context key 'retry_count' in output")
	}
}

func TestGenerate_ReviewerCompletion(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentReviewer
	opts.ReviewMode = "feature"

	output := Generate(opts)

	if !strings.Contains(output, "review.json") {
		t.Error("expected 'review.json' in reviewer completion section")
	}
	if strings.Contains(output, "commit all changes") {
		t.Error("reviewer should not be told to commit changes")
	}
}

func TestGenerate_NonReviewerCompletion(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentCoder

	output := Generate(opts)

	if !strings.Contains(output, "commit all changes") {
		t.Error("non-reviewer should be told to commit changes")
	}
	if strings.Contains(output, "review.json") {
		t.Error("non-reviewer should not mention review.json")
	}
}

// ── 7. Generate() role and project sections ────────────────────────────────

func TestGenerate_RoleAndTask(t *testing.T) {
	opts := minimalOpts()
	output := Generate(opts)

	if !strings.Contains(output, "# Agent Task: Test Task") {
		t.Error("expected task title in header")
	}
	if !strings.Contains(output, "**coder** agent") {
		t.Error("expected agent type in role description")
	}
}

func TestGenerate_ProjectContext(t *testing.T) {
	opts := minimalOpts()
	output := Generate(opts)

	if !strings.Contains(output, "## Project Context") {
		t.Error("expected '## Project Context' section")
	}
	if !strings.Contains(output, "test-project") {
		t.Error("expected project name in output")
	}
	if !strings.Contains(output, "/tmp/test-repo.git") {
		t.Error("expected bare repo path in output")
	}
}

func TestGenerate_ProjectContextWithDescription(t *testing.T) {
	opts := minimalOpts()
	opts.Project.Description = "A test orchestrator project"
	output := Generate(opts)

	if !strings.Contains(output, "A test orchestrator project") {
		t.Error("expected project description in output")
	}
}

func TestGenerate_NilProject(t *testing.T) {
	opts := minimalOpts()
	opts.Project = nil
	output := Generate(opts)

	if strings.Contains(output, "## Project Context") {
		t.Error("did not expect '## Project Context' section with nil project")
	}
}

func TestGenerate_WorktreeInfo(t *testing.T) {
	opts := minimalOpts()
	output := Generate(opts)

	if !strings.Contains(output, "## Working Environment") {
		t.Error("expected '## Working Environment' section")
	}
	if !strings.Contains(output, "/tmp/worktrees/test-branch") {
		t.Error("expected worktree path in output")
	}
	if !strings.Contains(output, "test-branch") {
		t.Error("expected branch name derived from worktree path")
	}
}

func TestGenerate_ScopeSection(t *testing.T) {
	opts := minimalOpts()
	output := Generate(opts)

	if !strings.Contains(output, "## Scope") {
		t.Error("expected '## Scope' section")
	}
	if !strings.Contains(output, "Only modify files directly relevant") {
		t.Error("expected scope limitation text")
	}
}

// ── 8. Agent-type specific Generate dispatch ───────────────────────────────

func TestGenerate_PlannerDispatch(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentPlanner
	output := Generate(opts)

	if !strings.Contains(output, "You are a planner agent") {
		t.Error("expected planner instructions in output")
	}
}

func TestGenerate_ResearcherDispatch(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentResearcher
	output := Generate(opts)

	if !strings.Contains(output, "You are a researcher agent") {
		t.Error("expected researcher instructions in output")
	}
	if !strings.Contains(output, "research-report.md") {
		t.Error("expected research-report.md reference in output")
	}
}

func TestGenerate_FixerDispatch(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentFixer
	opts.Diagnosis = "Nil pointer in handler"
	opts.AffectedFiles = []string{"internal/handler.go"}
	opts.SuggestedFix = "Add nil check before dereference"

	output := Generate(opts)

	if !strings.Contains(output, "You are a fixer agent") {
		t.Error("expected fixer instructions in output")
	}
	if !strings.Contains(output, "Nil pointer in handler") {
		t.Error("expected diagnosis in output")
	}
	if !strings.Contains(output, "internal/handler.go") {
		t.Error("expected affected file in output")
	}
	if !strings.Contains(output, "Add nil check before dereference") {
		t.Error("expected suggested fix in output")
	}
}

func TestGenerate_DefaultAgentDispatch(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentType("unknown")
	output := Generate(opts)

	if !strings.Contains(output, "Complete the task as described above") {
		t.Error("expected default instructions for unknown agent type")
	}
}

// ── 9. Fixer instructions details ──────────────────────────────────────────

func TestGenerate_FixerNoDiagnosis(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentFixer

	output := Generate(opts)

	if !strings.Contains(output, "You are a fixer agent") {
		t.Error("expected fixer instructions")
	}
	if strings.Contains(output, "## Diagnosis") {
		t.Error("did not expect '## Diagnosis' section when no diagnosis provided")
	}
}

func TestGenerate_FixerNoAffectedFiles(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentFixer
	opts.Diagnosis = "Some issue"

	output := Generate(opts)

	if !strings.Contains(output, "## Diagnosis") {
		t.Error("expected '## Diagnosis' section")
	}
	if strings.Contains(output, "## Affected Files") {
		t.Error("did not expect '## Affected Files' when none provided")
	}
}

func TestGenerate_FixerNoSuggestedFix(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentFixer
	opts.Diagnosis = "Some issue"
	opts.AffectedFiles = []string{"file.go"}

	output := Generate(opts)

	if !strings.Contains(output, "## Affected Files") {
		t.Error("expected '## Affected Files' section")
	}
	if strings.Contains(output, "## Suggested Fix") {
		t.Error("did not expect '## Suggested Fix' when none provided")
	}
}

// ── 10. Memories section ───────────────────────────────────────────────────

func TestGenerate_WithMemories(t *testing.T) {
	opts := minimalOpts()
	opts.Memories = []model.Memory{
		{MemoryType: "context", Content: "Previously explored the API design"},
		{MemoryType: "lesson", Content: "Tests must run before commit"},
	}

	output := Generate(opts)

	if !strings.Contains(output, "## Prior Context") {
		t.Error("expected '## Prior Context' section")
	}
	if !strings.Contains(output, "Previously explored the API design") {
		t.Error("expected first memory content in output")
	}
	if !strings.Contains(output, "Tests must run before commit") {
		t.Error("expected second memory content in output")
	}
}

func TestGenerate_NoMemories(t *testing.T) {
	opts := minimalOpts()
	output := Generate(opts)

	if strings.Contains(output, "## Prior Context") {
		t.Error("did not expect '## Prior Context' section with no memories")
	}
}

// ── 11. prompt_adjustment is excluded from Additional Context ──────────────

func TestGenerate_PromptAdjustmentExcludedFromContext(t *testing.T) {
	opts := minimalOpts()
	opts.Task.Context = model.JSONField{
		"prompt_adjustment": "try harder",
		"other_key":         "other_value",
	}

	output := Generate(opts)

	// prompt_adjustment should appear in its own section, not under Additional Context.
	if !strings.Contains(output, "## Additional Guidance from Prior Attempt") {
		t.Error("expected prompt adjustment in its own section")
	}

	// The Additional Context section should contain other_key but not prompt_adjustment as a key.
	lines := strings.Split(output, "\n")
	inAdditionalCtx := false
	inGuidance := false
	for _, line := range lines {
		if strings.Contains(line, "## Additional Context") {
			inAdditionalCtx = true
			inGuidance = false
		} else if strings.Contains(line, "## Additional Guidance") {
			inGuidance = true
			inAdditionalCtx = false
		} else if strings.HasPrefix(line, "## ") {
			inAdditionalCtx = false
			inGuidance = false
		}
		if inAdditionalCtx && strings.Contains(line, "prompt_adjustment") {
			t.Error("prompt_adjustment should not appear under Additional Context")
		}
	}
	_ = inGuidance
}

// ── 12. readBuildCommands ──────────────────────────────────────────────────

func TestReadBuildCommands_EmptyPath(t *testing.T) {
	result := readBuildCommands("")
	if result != "" {
		t.Error("expected empty string for empty worktree path")
	}
}

func TestReadBuildCommands_NonexistentPath(t *testing.T) {
	result := readBuildCommands("/nonexistent/path")
	if result != "" {
		t.Error("expected empty string for nonexistent worktree path")
	}
}

func TestResearcherInstructions(t *testing.T) {
	sections := researcherInstructions()
	output := strings.Join(sections, "\n")

	t.Run("contains key researcher phrases", func(t *testing.T) {
		required := []string{
			"You are a researcher agent",
			"research-report.md",
			"Summary of findings",
			"Detailed analysis",
			"Recommendations",
		}
		for _, phrase := range required {
			if !strings.Contains(output, phrase) {
				t.Errorf("researcherInstructions() missing phrase: %q", phrase)
			}
		}
	})

	t.Run("does not contain coder-specific phrases", func(t *testing.T) {
		coderPhrases := []string{
			"commit all changes",
			"You are a coder agent",
			"You are a planner agent",
		}
		for _, phrase := range coderPhrases {
			if strings.Contains(output, phrase) {
				t.Errorf("researcherInstructions() should not contain coder phrase: %q", phrase)
			}
		}
	})
}

func TestFixerInstructions(t *testing.T) {
	t.Run("contains fixer role", func(t *testing.T) {
		opts := minimalOpts()
		opts.AgentType = model.AgentFixer
		sections := fixerInstructions(opts)
		output := strings.Join(sections, "\n")

		if !strings.Contains(output, "You are a fixer agent") {
			t.Error("fixerInstructions() missing 'You are a fixer agent'")
		}
	})

	t.Run("includes diagnosis when set", func(t *testing.T) {
		opts := minimalOpts()
		opts.AgentType = model.AgentFixer
		opts.Diagnosis = "nil pointer dereference in handler.go line 42"
		sections := fixerInstructions(opts)
		output := strings.Join(sections, "\n")

		if !strings.Contains(output, opts.Diagnosis) {
			t.Errorf("fixerInstructions() missing diagnosis: %q", opts.Diagnosis)
		}
		if !strings.Contains(output, "## Diagnosis") {
			t.Error("fixerInstructions() missing Diagnosis section header")
		}
	})

	t.Run("includes affected files when set", func(t *testing.T) {
		opts := minimalOpts()
		opts.AgentType = model.AgentFixer
		opts.AffectedFiles = []string{"internal/handler.go", "internal/router.go"}
		sections := fixerInstructions(opts)
		output := strings.Join(sections, "\n")

		for _, f := range opts.AffectedFiles {
			if !strings.Contains(output, f) {
				t.Errorf("fixerInstructions() missing affected file: %q", f)
			}
		}
		if !strings.Contains(output, "## Affected Files") {
			t.Error("fixerInstructions() missing Affected Files section header")
		}
	})

	t.Run("includes suggested fix when set", func(t *testing.T) {
		opts := minimalOpts()
		opts.AgentType = model.AgentFixer
		opts.SuggestedFix = "Add nil check before accessing request.Body"
		sections := fixerInstructions(opts)
		output := strings.Join(sections, "\n")

		if !strings.Contains(output, opts.SuggestedFix) {
			t.Errorf("fixerInstructions() missing suggested fix: %q", opts.SuggestedFix)
		}
		if !strings.Contains(output, "## Suggested Fix") {
			t.Error("fixerInstructions() missing Suggested Fix section header")
		}
	})
}

func TestDefaultInstructions(t *testing.T) {
	sections := defaultInstructions()
	output := strings.Join(sections, "\n")

	if !strings.Contains(output, "Complete the task as described above") {
		t.Error("defaultInstructions() missing fallback instruction text")
	}

	if !strings.Contains(output, "## Instructions") {
		t.Error("defaultInstructions() missing Instructions section header")
	}
}

func TestReadBuildCommands(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T) string // returns worktree path
		contains string                    // expected substring in output
		empty    bool                      // if true, expect empty string
	}{
		{
			name: "go.mod and CLAUDE.md with bash block",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				// Create go.mod
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n"), 0644); err != nil {
					t.Fatal(err)
				}
				// Create CLAUDE.md with a bash block
				claudeContent := "# My Project\n\n## Build\n\n```bash\ngo build ./...\ngo test ./...\n```\n"
				if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(claudeContent), 0644); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			contains: "go build ./...",
		},
		{
			name: "CLAUDE.md with build section",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				claudeContent := "# Project\n\n```bash\nmake build\nmake test\n```\n"
				if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(claudeContent), 0644); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			contains: "make build",
		},
		{
			name: "no build files in directory",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			empty: true,
		},
		{
			name: "CLAUDE.md without bash block",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				claudeContent := "# Project\n\nNo code blocks here.\n"
				if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(claudeContent), 0644); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			empty: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			worktreePath := tc.setup(t)
			result := readBuildCommands(worktreePath)

			if tc.empty {
				if result != "" {
					t.Errorf("readBuildCommands() = %q, want empty string", result)
				}
				return
			}

			if !strings.Contains(result, tc.contains) {
				t.Errorf("readBuildCommands() = %q, want substring %q", result, tc.contains)
			}
		})
	}
}

// ── 13. Bug report instructions for all agent types ────────────────────────

func TestGenerate_BugReportInstructionsAllAgentTypes(t *testing.T) {
	agentTypes := []struct {
		name      string
		agentType model.AgentType
	}{
		{"planner", model.AgentPlanner},
		{"coder", model.AgentCoder},
		{"researcher", model.AgentResearcher},
		{"reviewer", model.AgentReviewer},
		{"fixer", model.AgentFixer},
	}

	for _, at := range agentTypes {
		t.Run(at.name, func(t *testing.T) {
			opts := minimalOpts()
			opts.AgentType = at.agentType

			output := Generate(opts)

			if !strings.Contains(output, "Bug Report Filing") {
				t.Errorf("Generate() for %s agent missing 'Bug Report Filing' section", at.name)
			}
			requiredFields := []string{"category", "severity", "reproduction_context"}
			for _, field := range requiredFields {
				if !strings.Contains(output, field) {
					t.Errorf("Generate() for %s agent missing JSON schema field %q", at.name, field)
				}
			}
		})
	}
}
