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
		"## Test Subtasks",
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
		"Depend on ALL implementation subtasks",
		"implementation subtasks -> test subtask -> integration subtask",
	}

	for _, c := range keyContent {
		if !strings.Contains(output, c) {
			t.Errorf("plannerInstructions() missing test subtask guidance: %q", c)
		}
	}
}

func TestResearcherInstructions(t *testing.T) {
	sections := researcherInstructions()
	if len(sections) == 0 {
		t.Fatal("researcherInstructions() returned empty slice")
	}

	output := strings.Join(sections, "\n")

	expected := []string{
		"researcher agent",
		"Investigate",
		"research-report.md",
		"Summary of findings",
		"Detailed analysis",
		"Recommendations",
	}

	for _, phrase := range expected {
		if !strings.Contains(output, phrase) {
			t.Errorf("researcherInstructions() missing expected phrase: %q", phrase)
		}
	}
}

func TestFixerInstructions(t *testing.T) {
	opts := Opts{
		Diagnosis:     "nil pointer dereference in handler.go line 42",
		AffectedFiles: []string{"internal/handler.go", "internal/handler_test.go"},
		SuggestedFix:  "Add nil check before accessing request.Body",
	}

	sections := fixerInstructions(opts)
	if len(sections) == 0 {
		t.Fatal("fixerInstructions() returned empty slice")
	}

	output := strings.Join(sections, "\n")

	// Verify fixer-specific directives.
	fixerPhrases := []string{
		"fixer agent",
		"targeted fix",
		"Apply ONLY the fix",
		"minimal",
	}
	for _, phrase := range fixerPhrases {
		if !strings.Contains(output, phrase) {
			t.Errorf("fixerInstructions() missing fixer directive: %q", phrase)
		}
	}

	// Verify opts context is included.
	if !strings.Contains(output, opts.Diagnosis) {
		t.Error("fixerInstructions() missing diagnosis from opts")
	}
	for _, f := range opts.AffectedFiles {
		if !strings.Contains(output, f) {
			t.Errorf("fixerInstructions() missing affected file: %q", f)
		}
	}
	if !strings.Contains(output, opts.SuggestedFix) {
		t.Error("fixerInstructions() missing suggested fix from opts")
	}
}

func TestFixerInstructionsEmptyOpts(t *testing.T) {
	sections := fixerInstructions(Opts{})
	if len(sections) == 0 {
		t.Fatal("fixerInstructions() with empty opts returned empty slice")
	}

	output := strings.Join(sections, "\n")

	// Should still contain base fixer directives even with empty opts.
	if !strings.Contains(output, "fixer agent") {
		t.Error("fixerInstructions() with empty opts missing 'fixer agent'")
	}
	// Should not contain Diagnosis/Affected/Suggested headers when empty.
	if strings.Contains(output, "## Diagnosis") {
		t.Error("fixerInstructions() with empty opts should not include Diagnosis section")
	}
}

func TestDefaultInstructions(t *testing.T) {
	sections := defaultInstructions()
	if len(sections) == 0 {
		t.Fatal("defaultInstructions() returned empty slice")
	}

	output := strings.Join(sections, "\n")

	if !strings.Contains(output, "Complete the task") {
		t.Error("defaultInstructions() missing generic fallback guidance")
	}
	if !strings.Contains(output, "Commit your changes") {
		t.Error("defaultInstructions() missing commit instruction")
	}
}

func TestReadBuildCommands(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T) string // returns worktree path
		contains string                    // expected substring in result
		empty    bool                      // expect empty result
	}{
		{
			name: "go project with CLAUDE.md",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				content := "# My Project\n\n## Build\n\n```bash\ngo build ./...\ngo test ./...\n```\n"
				if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			contains: "go build",
		},
		{
			name: "makefile project",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				content := "# Project\n\n```bash\nmake build\nmake test\n```\n"
				if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			contains: "make",
		},
		{
			name: "npm project",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				content := "# Node Project\n\n```bash\nnpm install\nnpm test\n```\n"
				if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			contains: "npm",
		},
		{
			name: "empty directory without CLAUDE.md",
			setup: func(t *testing.T) string {
				t.Helper()
				return t.TempDir()
			},
			empty: true,
		},
		{
			name: "nonexistent directory",
			setup: func(t *testing.T) string {
				t.Helper()
				return "/nonexistent/path/that/does/not/exist"
			},
			empty: true,
		},
		{
			name: "empty worktree path",
			setup: func(t *testing.T) string {
				t.Helper()
				return ""
			},
			empty: true,
		},
		{
			name: "CLAUDE.md without bash block",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				content := "# Project\n\nNo code blocks here.\n"
				if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			empty: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.setup(t)
			result := readBuildCommands(path)

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

// helper builds a minimal Opts for Generate tests.
func minimalOpts(agentType model.AgentType) Opts {
	return Opts{
		AgentType: agentType,
		Task: &model.Task{
			Title:       "Test task",
			Description: "A task for testing prompt generation",
		},
		Project: &model.Project{
			Name:         "test-project",
			BareRepoPath: "/tmp/test.git",
		},
		WorktreePath: "/tmp/worktrees/test-branch",
	}
}

func TestGenerate_Planner(t *testing.T) {
	result := Generate(minimalOpts(model.AgentPlanner))

	if result == "" {
		t.Fatal("Generate() returned empty string for planner")
	}

	expected := []string{
		"planner",
		"Test task",
		"plan.json",
		"## Instructions",
		"test-project",
	}

	for _, phrase := range expected {
		if !strings.Contains(result, phrase) {
			t.Errorf("Generate(planner) missing expected phrase: %q", phrase)
		}
	}
}

func TestGenerate_Coder(t *testing.T) {
	result := Generate(minimalOpts(model.AgentCoder))

	if result == "" {
		t.Fatal("Generate() returned empty string for coder")
	}

	expected := []string{
		"coder",
		"Test task",
		"## Instructions",
		"Implement",
		"Commit your changes",
	}

	for _, phrase := range expected {
		if !strings.Contains(result, phrase) {
			t.Errorf("Generate(coder) missing expected phrase: %q", phrase)
		}
	}
}

func TestGenerate_Researcher(t *testing.T) {
	result := Generate(minimalOpts(model.AgentResearcher))

	if result == "" {
		t.Fatal("Generate() returned empty string for researcher")
	}

	expected := []string{
		"researcher",
		"Test task",
		"research-report.md",
		"Investigate",
	}

	for _, phrase := range expected {
		if !strings.Contains(result, phrase) {
			t.Errorf("Generate(researcher) missing expected phrase: %q", phrase)
		}
	}
}

func TestGenerate_DefaultAgent(t *testing.T) {
	opts := minimalOpts("unknown")
	result := Generate(opts)

	if result == "" {
		t.Fatal("Generate() returned empty string for unknown agent type")
	}

	if !strings.Contains(result, "Complete the task") {
		t.Error("Generate(unknown) missing default instructions")
	}
}

func TestGenerate_WithMemories(t *testing.T) {
	opts := minimalOpts(model.AgentCoder)
	opts.Memories = []model.Memory{
		{MemoryType: "lesson", Content: "Always check nil pointers"},
	}

	result := Generate(opts)

	if !strings.Contains(result, "## Prior Context") {
		t.Error("Generate() with memories missing Prior Context section")
	}
	if !strings.Contains(result, "Always check nil pointers") {
		t.Error("Generate() with memories missing memory content")
	}
}

func TestGenerate_WithBuildCommands(t *testing.T) {
	dir := t.TempDir()
	content := "# Project\n\n```bash\ngo test ./...\n```\n"
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	opts := minimalOpts(model.AgentCoder)
	opts.WorktreePath = dir

	result := Generate(opts)

	if !strings.Contains(result, "## Build & Verify") {
		t.Error("Generate() with CLAUDE.md missing Build & Verify section")
	}
	if !strings.Contains(result, "go test") {
		t.Error("Generate() with CLAUDE.md missing build commands")
	}
}

func TestGenerate_ReviewerCompletion(t *testing.T) {
	opts := minimalOpts(model.AgentReviewer)
	opts.ReviewMode = "feature"

	result := Generate(opts)

	if !strings.Contains(result, "review.json") {
		t.Error("Generate(reviewer) missing review.json in completion")
	}
	if !strings.Contains(result, "Do NOT commit") {
		t.Error("Generate(reviewer) missing 'Do NOT commit' instruction")
	}
}
