package prompt

import (
	"strings"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/promptassets"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateDirectCoder(t *testing.T) {
	task := &model.Task{
		Title:       "Add widget parsing",
		Description: "Parse widgets from the input stream and store them.",
		Context:     model.JSONField{"estimated_files": []any{"internal/widget/parser.go"}},
	}
	task.ID = uuid.New()

	opts := Opts{
		Task:         task,
		AgentType:    model.AgentCoder,
		WorktreePath: "/tmp/worktree/feature-branch",
	}

	result := GenerateDirectCoder(opts)
	require.NotEmpty(t, result, "GenerateDirectCoder must return non-empty prompt")

	assert.Contains(t, result, "coder agent", "should identify as coder agent")
	assert.Contains(t, result, task.Title, "should include task title")
	assert.Contains(t, result, task.Description, "should include task description")
	assert.Contains(t, result, opts.WorktreePath, "should include working directory")
	assert.Contains(t, result, "go vet", "should include verification strategy")
	assert.Contains(t, result, "go test", "should include test verification")
	assert.Contains(t, result, "commit", "should include commit instructions")

	// Must NOT include repo map or heavy Claude Code sections
	assert.NotContains(t, result, "Repository Map", "direct prompt must not include repo map")
	assert.NotContains(t, result, "Context Efficiency", "direct prompt must not include context efficiency section")
}

func TestGenerateDirectCoder_WithParentCtx(t *testing.T) {
	task := &model.Task{
		Title:       "Implement subtask",
		Description: "A subtask of the parent feature.",
	}
	task.ID = uuid.New()

	opts := Opts{
		Task:         task,
		AgentType:    model.AgentCoder,
		WorktreePath: "/tmp/worktree/sub",
		ParentCtx:    map[string]any{"parent_title": "Parent Feature", "parent_description": "The big picture."},
	}

	result := GenerateDirectCoder(opts)
	assert.Contains(t, result, "Parent Feature", "should include parent context")
}

func TestGenerateDirectCoder_CppUsesProjectAssets(t *testing.T) {
	task := &model.Task{Title: "Add CMake test", Description: "Add a focused C++ test."}
	task.ID = uuid.New()
	assets := map[string]string{}
	for _, a := range promptassets.DefaultsForLanguage("cpp") {
		assets[promptassets.Key(a.Kind, a.Name)] = a.Content
	}

	result := GenerateDirectCoder(Opts{
		Task:         task,
		Project:      &model.Project{Language: "cpp"},
		AgentType:    model.AgentCoder,
		WorktreePath: "/tmp/worktree/cpp",
		PromptAssets: assets,
	})

	assert.Contains(t, result, "cmake --preset test")
	assert.NotContains(t, result, "go vet ./... && go test ./...")
	assert.NotContains(t, result, "testutil.NewTestDB")
}

func TestGenerateDirectCoder_ExternalVerificationIsPhaseAwareAndCompact(t *testing.T) {
	task := &model.Task{
		Title:       "Specify marked title behavior",
		Description: "Add a focused test for the exact marked title.",
		Phase:       "test",
		Context: model.JSONField{
			"estimated_files":            []any{"tests/unit/test_MainWindowTitle.cpp"},
			"prep_data":                  "MainWindow title formatting is the seam.",
			"planned_interface_contract": `{"kind":"planned_api","interfaces":[{"functions":["formatMarkedTitle()"]}]}`,
			"internal_retry_failures":    strings.Repeat("irrelevant retry state ", 1000),
		},
	}
	assets := map[string]string{}
	for _, a := range promptassets.DefaultsForLanguage("cpp") {
		assets[promptassets.Key(a.Kind, a.Name)] = a.Content
	}

	result := GenerateDirectCoder(Opts{
		Task:                 task,
		Project:              &model.Project{Language: "cpp"},
		AgentType:            model.AgentCoder,
		WorktreePath:         "/home/drem/work",
		WorktreeBranch:       "feature/marked-title",
		PromptAssets:         assets,
		ExternalVerification: true,
	})

	assert.Contains(t, result, "TEST phase")
	assert.Contains(t, result, "focused red-state test")
	assert.Contains(t, result, "planned interface contract")
	assert.Contains(t, result, "do not mock the production type")
	assert.Contains(t, result, "blocks all shell commands")
	assert.NotContains(t, result, "Do not read more than 2 files")
	assert.Contains(t, result, "external host")
	assert.Contains(t, result, "git diff --check")
	assert.Contains(t, result, "harness owns commit and push")
	assert.NotContains(t, result, "git add -A")
	assert.Contains(t, result, "/home/drem/work")
	assert.Contains(t, result, "feature/marked-title")
	assert.NotContains(t, result, "cmake --preset")
	assert.NotContains(t, result, "internal_retry_failures")
	assert.Less(t, len(result), 8000)
}

func TestGenerateDirectReviewer_PlanReview(t *testing.T) {
	task := &model.Task{
		Title:       "Review the plan",
		Description: "Evaluate the plan for completeness.",
	}
	task.ID = uuid.New()

	planJSON := `{"subtasks": [{"title": "step 1"}]}`

	opts := Opts{
		Task:         task,
		AgentType:    model.AgentReviewer,
		WorktreePath: "/tmp/worktree/review",
		ReviewMode:   "plan",
		PlanJSON:     planJSON,
	}

	result := GenerateDirectReviewer(opts)
	require.NotEmpty(t, result, "GenerateDirectReviewer must return non-empty prompt")

	assert.Contains(t, result, "reviewer agent", "should identify as reviewer agent")
	assert.Contains(t, result, "review.json", "should mention review.json output format")
	assert.Contains(t, result, planJSON, "should include the plan JSON for plan review")
	assert.Contains(t, result, "Do NOT modify code", "should instruct not to modify code")

	assert.NotContains(t, result, "Repository Map", "direct prompt must not include repo map")
}

func TestGenerateDirectReviewer_FeatureReview(t *testing.T) {
	task := &model.Task{
		Title:       "Review feature diff",
		Description: "Review the implementation diff.",
	}
	task.ID = uuid.New()

	diff := "diff --git a/main.go b/main.go\n+func newFunc() {}"

	opts := Opts{
		Task:         task,
		AgentType:    model.AgentReviewer,
		WorktreePath: "/tmp/worktree/review",
		ReviewMode:   "feature",
		GitDiff:      diff,
	}

	result := GenerateDirectReviewer(opts)
	assert.Contains(t, result, diff, "should include git diff for feature review")
	assert.Contains(t, result, "review.json", "should mention review.json output")
}

func TestGenerateDirectReviewer_DiffTruncation(t *testing.T) {
	task := &model.Task{
		Title:       "Review large diff",
		Description: "A large diff that exceeds the limit.",
	}
	task.ID = uuid.New()

	largeDiff := strings.Repeat("x", 15000)

	opts := Opts{
		Task:         task,
		AgentType:    model.AgentReviewer,
		WorktreePath: "/tmp/worktree/review",
		ReviewMode:   "feature",
		GitDiff:      largeDiff,
	}

	result := GenerateDirectReviewer(opts)
	// The diff should be truncated to ~10000 chars
	assert.True(t, len(result) < len(largeDiff), "prompt should truncate oversized diffs")
}

func TestGenerateDirectFixer(t *testing.T) {
	task := &model.Task{
		Title:       "Fix build failure",
		Description: "The build is broken due to a missing import.",
	}
	task.ID = uuid.New()

	opts := Opts{
		Task:          task,
		AgentType:     model.AgentFixer,
		WorktreePath:  "/tmp/worktree/fix",
		Diagnosis:     "Missing import of fmt package in handler.go",
		AffectedFiles: []string{"internal/handler/handler.go", "internal/handler/handler_test.go"},
		SuggestedFix:  "Add import \"fmt\" to handler.go",
	}

	result := GenerateDirectFixer(opts)
	require.NotEmpty(t, result, "GenerateDirectFixer must return non-empty prompt")

	assert.Contains(t, result, "fixer agent", "should identify as fixer agent")
	assert.Contains(t, result, opts.Diagnosis, "should include diagnosis text")
	assert.Contains(t, result, "internal/handler/handler.go", "should include affected files")
	assert.Contains(t, result, "internal/handler/handler_test.go", "should include all affected files")
	assert.Contains(t, result, opts.SuggestedFix, "should include suggested fix")
	assert.Contains(t, result, "minimal", "should instruct minimal fix approach")

	assert.NotContains(t, result, "Repository Map", "direct prompt must not include repo map")
}

func TestGenerateDirectFixer_CppUsesProjectAssets(t *testing.T) {
	task := &model.Task{Title: "Fix CMake target", Description: "Fix a C++ build failure."}
	task.ID = uuid.New()
	assets := map[string]string{}
	for _, a := range promptassets.DefaultsForLanguage("cpp") {
		assets[promptassets.Key(a.Kind, a.Name)] = a.Content
	}

	result := GenerateDirectFixer(Opts{
		Task:         task,
		Project:      &model.Project{Language: "cpp"},
		AgentType:    model.AgentFixer,
		WorktreePath: "/tmp/worktree/cpp",
		PromptAssets: assets,
	})

	assert.Contains(t, result, "C++/CMake")
	assert.NotContains(t, result, "go vet ./... && go test ./...")
}

func TestGenerateDirectFixer_EmptyDiagnosis(t *testing.T) {
	task := &model.Task{
		Title:       "Fix unknown issue",
		Description: "Something is broken.",
	}
	task.ID = uuid.New()

	opts := Opts{
		Task:         task,
		AgentType:    model.AgentFixer,
		WorktreePath: "/tmp/worktree/fix",
	}

	result := GenerateDirectFixer(opts)
	// Should still produce a valid prompt even without diagnosis
	assert.Contains(t, result, "fixer agent", "should identify as fixer agent even without diagnosis")
	assert.Contains(t, result, task.Description, "should include task description as fallback context")
}

func TestDirectPrompts_AreCompact(t *testing.T) {
	task := &model.Task{
		Title:       "Test compactness",
		Description: "Verify prompts stay small for local models.",
	}
	task.ID = uuid.New()

	opts := Opts{
		Task:         task,
		AgentType:    model.AgentCoder,
		WorktreePath: "/tmp/worktree/compact",
	}

	coder := GenerateDirectCoder(opts)
	// Rough token estimate: ~4 chars per token. 2000 tokens = ~8000 chars.
	assert.Less(t, len(coder), 8000, "coder prompt should be under ~2000 tokens")

	opts.AgentType = model.AgentReviewer
	opts.ReviewMode = "plan"
	opts.PlanJSON = `{"subtasks": [{"title": "a"}]}`
	reviewer := GenerateDirectReviewer(opts)
	assert.Less(t, len(reviewer), 8000, "reviewer prompt (without large diff) should be compact")

	opts.AgentType = model.AgentFixer
	opts.Diagnosis = "short diagnosis"
	opts.SuggestedFix = "short fix"
	fixer := GenerateDirectFixer(opts)
	assert.Less(t, len(fixer), 8000, "fixer prompt should be compact")
}
