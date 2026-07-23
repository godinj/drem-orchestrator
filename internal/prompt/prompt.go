// Package prompt builds markdown prompt strings that are piped to Claude Code
// agent sessions. Each prompt includes task details, project context, worktree
// information, agent-type-specific instructions, and prior memories.
package prompt

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/promptassets"
)

const maxDiffLen = 50000

// Opts contains all inputs needed to generate an agent prompt.
type Opts struct {
	Task           *model.Task
	Project        *model.Project
	AgentType      model.AgentType
	WorktreePath   string
	WorktreeBranch string
	Memories       []model.Memory
	Comments       []model.TaskComment
	ParentCtx      map[string]any
	PromptAssets   map[string]string

	// Reviewer fields
	ReviewMode string // "plan" or "feature"
	PlanJSON   string // raw plan JSON for plan review
	GitDiff    string // diff for feature review

	// Fixer fields
	Diagnosis     string   // root cause diagnosis
	AffectedFiles []string // files to fix
	SuggestedFix  string   // suggested fix from diagnosis

	// Model-awareness: tells the planner what model the downstream coders use.
	// Empty fields mean "unknown / use defaults".
	TargetCoderProvider string // "claude", "opencode", etc.
	TargetCoderModel    string // e.g. "Qwen3-Coder-30B-A3B", "claude-sonnet-4-6"

	// ExternalVerification means project-native gates run outside the worker
	// (for example, a macOS Canvas build driven by the Codex adapter).
	ExternalVerification bool
}

// Generate builds a full markdown prompt for a Claude Code agent.
func Generate(opts Opts) string {
	var sections []string

	// 1. Role & Task
	sections = append(sections,
		fmt.Sprintf("# Agent Task: %s", opts.Task.Title),
		"",
		fmt.Sprintf("You are a **%s** agent working on: **%s**", opts.AgentType, opts.Task.Title),
		"",
	)

	// 2. Project Context
	if opts.Project != nil {
		sections = append(sections, "## Project Context", "")
		sections = append(sections, fmt.Sprintf("- **Project**: %s", opts.Project.Name))
		if opts.Project.Description != "" {
			sections = append(sections, fmt.Sprintf("- **Description**: %s", opts.Project.Description))
		}
		sections = append(sections, fmt.Sprintf("- **Bare repo**: `%s`", opts.Project.BareRepoPath))
		if language := projectLanguage(opts); language != "" {
			sections = append(sections, fmt.Sprintf("- **Language**: %s", language))
		}
		sections = append(sections, "")
	}

	// 2b. Repository Map — structural overview before task details.
	// Skip for classifiers: they explore with grep, not a static map.
	var repoMap string
	if opts.AgentType != model.AgentClassifier {
		repoMap = readRepoMap(opts.WorktreePath)
		if repoMap != "" {
			sections = append(sections, repoMap)
		}
	}

	// 2c. Context Efficiency
	if repoMap != "" && opts.AgentType != model.AgentClassifier {
		sections = append(sections,
			"## Context Efficiency",
			"",
			"You have a limited context window. Before reading a file:",
			"1. Check the Repository Map above for the file's function signatures",
			"2. Only read files you need to MODIFY, not files you need to UNDERSTAND",
			"3. Use grep/search to find specific lines rather than reading entire files",
			"4. If you need to understand an interface, check the Repository Map first",
			"",
		)
	}

	// 2d. Verification Efficiency — not needed for read-only classifiers.
	if opts.AgentType != model.AgentClassifier {
		if opts.ExternalVerification {
			sections = append(sections,
				"## Verification Strategy",
				"",
				"Project-native verification is owned by the external host adapter after this branch is assembled.",
				"Do not change build configuration, dependencies, or manifests to make this worker container pass a native host build.",
				"Use only lightweight checks available without changing repository files, then commit the scoped result.",
				"",
			)
		} else {
			verification := asset(opts, "verification", "strategy")
			if verification == "" {
				verification = "Run `go vet ./... && go test ./...` in a SINGLE command — never separately"
			}
			sections = append(sections,
				"## Verification Strategy",
				"",
				"Each turn costs context. Minimize verification rounds:",
				"1. Write ALL code changes before running any verification",
				"2. "+verification,
				"3. If verification fails, read ALL errors, fix ALL issues in one pass, then verify ONCE more",
				"4. Maximum 2 verification cycles. If tests still fail after 2 fix attempts, commit what you have with a note",
				"5. Do NOT re-read files you already read — use your memory of their contents",
				"",
			)
		}
	}

	// 2e. Critical Rules Library — standing guardrails from observed failure patterns.
	// Skip for classifiers: they don't write code, so build/style rules don't apply.
	if opts.AgentType != model.AgentClassifier {
		criticalRules := readCriticalRules(opts.WorktreePath)
		if criticalRules != "" {
			sections = append(sections, criticalRules)
		}
	}

	// 3. Task Details
	sections = append(sections, "## Task Description", "")
	sections = append(sections, opts.Task.Description, "")

	// Task-specific context (exclude internal keys injected below).
	if len(opts.Task.Context) > 0 {
		sections = append(sections, "## Additional Context", "")
		for key, value := range opts.Task.Context {
			// Skip keys that are injected as dedicated sections below.
			switch key {
			case "prompt_adjustment", "clarification_context", "clarification_session":
				continue
			}
			sections = append(sections, fmt.Sprintf("- **%s**: %v", key, value))
		}
		sections = append(sections, "")
	}

	// Prompt adjustment from supervisor failure diagnosis.
	if opts.Task.Context != nil {
		if adj, ok := opts.Task.Context["prompt_adjustment"].(string); ok && adj != "" {
			sections = append(sections, "## Additional Guidance from Prior Attempt", "")
			sections = append(sections, adj, "")
		}
	}

	// Clarification context from prior clarification loop.
	if opts.Task.Context != nil {
		if clarCtx, ok := opts.Task.Context["clarification_context"].(string); ok && clarCtx != "" {
			sections = append(sections, clarCtx, "")
		}
	}

	// User feedback comments thread.
	if len(opts.Comments) > 0 {
		sections = append(sections, "## User Feedback Comments", "")
		for _, c := range opts.Comments {
			sections = append(sections, fmt.Sprintf("- **[%s]** %s", c.Author, c.Body))
		}
		sections = append(sections, "")
	}

	// Parent task context if subtask
	if len(opts.ParentCtx) > 0 {
		sections = append(sections, "## Parent Task Context", "")
		for key, value := range opts.ParentCtx {
			sections = append(sections, fmt.Sprintf("- **%s**: %v", key, value))
		}
		sections = append(sections, "")
	}

	// 4. Worktree Info
	sections = append(sections, "## Working Environment", "")
	sections = append(sections, fmt.Sprintf("- **Worktree path**: `%s`", opts.WorktreePath))
	if opts.WorktreePath != "" {
		branch := filepath.Base(opts.WorktreePath)
		sections = append(sections, fmt.Sprintf("- **Branch**: `%s`", branch))
	}
	if opts.Project != nil {
		sections = append(sections, fmt.Sprintf("- **Project**: %s", opts.Project.Name))
	}
	sections = append(sections, "")

	// 5. Agent-Type Instructions
	switch opts.AgentType {
	case model.AgentPlanner:
		sections = append(sections, plannerInstructions()...)
		if guidance := asset(opts, "planner", "guidance"); guidance != "" {
			sections = append(sections, "## Project Planning Guidance", "", guidance, "")
		}
		sections = append(sections, targetModelGuidance(opts.TargetCoderProvider, opts.TargetCoderModel)...)
	case model.AgentCoder:
		sections = append(sections, coderInstructions(opts)...)
	case model.AgentResearcher:
		sections = append(sections, researcherInstructions()...)
	case model.AgentReviewer:
		sections = append(sections, reviewerInstructions(opts)...)
	case model.AgentFixer:
		sections = append(sections, fixerInstructions(opts)...)
	case model.AgentClassifier:
		sections = append(sections, classifierInstructions(opts.WorktreePath, opts.Task.ID.String())...)
	default:
		sections = append(sections, defaultInstructions()...)
	}

	// 5b. Bug Report Filing — applies to all agent types
	sections = append(sections, bugReportInstructions()...)

	// 6. Prior Context — Agent memories
	if len(opts.Memories) > 0 {
		sections = append(sections, "## Prior Context", "")
		for _, mem := range opts.Memories {
			sections = append(sections, fmt.Sprintf("### %s", mem.MemoryType))
			sections = append(sections, mem.Content, "")
		}
	}

	// 7. Build & Verify — read CLAUDE.md if present
	// Skip for classifiers: they don't write code.
	buildCmds := readBuildCommands(opts.WorktreePath)
	if buildCmds != "" && opts.AgentType != model.AgentClassifier && !opts.ExternalVerification {
		sections = append(sections, "## Build & Verify", "")
		sections = append(sections, "```bash")
		sections = append(sections, buildCmds)
		sections = append(sections, "```", "")
	}

	// 8. Architecture & Constraints — read context files from .drem/constraints.toml
	// Skip for classifiers: they don't write code.
	ctxFiles := readContextFiles(opts.WorktreePath)
	if ctxFiles != "" && opts.AgentType != model.AgentClassifier {
		sections = append(sections, ctxFiles)
	}

	// Scope limitation
	// Skip for classifiers: they don't write code.
	if opts.AgentType != model.AgentClassifier {
		sections = append(sections, "## Scope", "")
		if files := taskContextStrings(opts.Task, "estimated_files"); len(files) > 0 {
			quoted := make([]string, 0, len(files))
			for _, file := range files {
				quoted = append(quoted, "`"+file+"`")
			}
			sections = append(sections,
				"Exact allowed files: "+strings.Join(quoted, ", ")+".",
				"Do not modify, delete, or generate any other repository file; deterministic branch acceptance will reject the entire attempt.",
				"",
			)
		}
		sections = append(sections,
			"Only modify files directly relevant to this task. "+
				"Do not refactor unrelated code or change project configuration "+
				"unless the task explicitly requires it.",
			"",
		)
	}

	// Completion instructions (reviewer agents don't commit)
	// Skip for classifiers: they don't write code.
	if opts.AgentType != model.AgentClassifier {
		sections = append(sections, "## Completion", "")
		if opts.AgentType == model.AgentReviewer {
			sections = append(sections,
				"When you have completed your review, ensure `review.json` has been written "+
					"to the working directory root. Do NOT commit any changes or modify code.",
				"",
			)
		} else {
			sections = append(sections,
				"When you have completed the task, you MUST commit all changes before exiting. "+
					"Run `git add` for any new or untracked files (do NOT rely solely on `git add -u`, "+
					"which skips untracked files), then run `git commit` with a descriptive message. "+
					"Ensure all tests pass before committing.",
				"",
			)
		}
	}

	return strings.Join(sections, "\n")
}

func taskContextStrings(task *model.Task, key string) []string {
	if task == nil || task.Context == nil {
		return nil
	}
	raw, ok := task.Context[key]
	if !ok {
		return nil
	}
	var out []string
	switch values := raw.(type) {
	case []string:
		out = append(out, values...)
	case []any:
		for _, value := range values {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

func asset(opts Opts, kind, name string) string {
	if opts.PromptAssets == nil {
		return ""
	}
	return strings.TrimSpace(opts.PromptAssets[promptassets.Key(kind, name)])
}

func projectLanguage(opts Opts) string {
	if opts.Project != nil && opts.Project.Language != "" {
		return opts.Project.Language
	}
	return "go"
}

// coderInstructions returns prompt sections for coder agents, dispatching
// by the task's Phase field to provide TDD-specific guidance. If a prep agent
// produced a tactical brief (stored in task context as prep_data), it is
// appended to the instructions so the coder has pre-analyzed context.
func coderInstructions(opts Opts) []string {
	var sections []string
	switch opts.Task.Phase {
	case "test":
		sections = testPhaseCoderInstructions(opts)
	case "implementation":
		sections = implPhaseCoderInstructions(opts)
	default:
		sections = defaultCoderInstructions(opts)
	}

	// Inject prep agent's tactical brief if available.
	if brief := prepDataBrief(opts); len(brief) > 0 {
		sections = append(sections, brief...)
	}

	return sections
}

// testPhaseCoderInstructions returns instructions for writing tests BEFORE
// implementation (TDD test phase).
func testPhaseCoderInstructions(opts Opts) []string {
	task := opts.Task
	var sections []string

	sections = append(sections, "## Instructions", "")
	if custom := asset(opts, "coder", "test"); custom != "" {
		sections = append(sections, custom, "")
	}
	sections = append(sections,
		"You are writing tests BEFORE implementation (TDD).",
		"",
		"Your tests should:",
		"1. Define the expected behavior described in the task",
		"2. Be thorough — cover happy paths, edge cases, and error conditions",
		"3. FAIL when run (the implementation doesn't exist yet)",
		"4. Be clear about WHAT is being tested and WHY",
		"5. Use the project's existing test patterns and frameworks",
		"",
	)

	// Include estimated files from task context if present
	if len(task.Context) > 0 {
		if files, ok := task.Context["estimated_files"]; ok {
			sections = append(sections, fmt.Sprintf("Files to create/modify: %v", files), "")
		}
	}

	if projectLanguage(opts) != "cpp" {
		sections = append(sections,
			"## Test Infrastructure Rules (Constitution-Enforced)",
			"",
			"These rules are enforced by the post-merge constitution check. Violations will cause",
			"your merge to be rejected.",
			"",
			"1. **DB init**: NEVER call `gorm.Open(sqlite.Open(...))` in test files. Always use",
			"   `testutil.NewTestDB(t)` for core models or `testutil.NewTestDBWithModels(t, &YourModel{})`",
			"   for packages with custom GORM models (e.g., csuite). Import `internal/testutil`.",
			"",
			"2. **Test factories**: NEVER define helper functions matching `func createTest*`,",
			"   `func newTest*`, or `func mockTestDB*` in your test files. All shared test helpers",
			"   MUST live in `internal/testutil/testutil.go`. If you need a new factory, add it there.",
			"",
			"3. **Git test helpers**: NEVER define `func setupBareRepo`, `func initBareRepo`,",
			"   `func addWorktree`, or `func commitFile` in test files. Use the equivalents from",
			"   `testutil` (e.g., `testutil.SetupBareRepo(t)`, `testutil.CommitFile(t, ...)`).",
			"",

			"## Stub Requirements",
			"",
			"Your tests must compile and link. To achieve this, create minimal stub implementations",
			"alongside your tests:",
			"",
			"- **Headers**: Full class/struct declarations with method signatures matching what your",
			"  tests call. Use correct includes and namespaces.",
			"- **Source files**: Method bodies that compile and link but do NOT implement real logic.",
			"  Return default values (0, false, \"\", empty containers). The goal is that tests fail",
			"  on ASSERTIONS, not on compilation or linker errors.",
			"- **Only stub what tests need**: Don't stub internal helpers or implementation details.",
			"  The stubs define the public API contract that the implementation must fulfill.",
			"",
			"The human reviewer will examine your tests AND stubs together to approve the API surface",
			"before implementation begins. Your stubs ARE the interface specification.",
			"",
			"After writing tests and stubs:",
			"1. Run `go vet ./... && go test ./...` in ONE command to verify compilation and test status",
			"2. Tests SHOULD fail on assertions (that's expected for TDD) — verify failures are not compilation errors",
			"3. If compilation fails, fix ALL errors in one pass, then re-run ONE more time",
			"4. Run `git add` for all new/untracked files, then commit both test files AND stub files "+
				`together with message: "test: <what these tests verify>"`,
			"5. Do NOT push to remote",
			"",
		)
	}

	// Include test plan if set
	if task.TestPlan != "" {
		sections = append(sections, "## Test Plan", "")
		sections = append(sections, task.TestPlan, "")
	}

	// Depth guidance from plan or generic fallback.
	sections = append(sections, depthGuidanceFromPlan(opts))

	return sections
}

// implPhaseCoderInstructions returns instructions for implementing code to
// pass pre-written tests (TDD implementation phase).
func implPhaseCoderInstructions(opts Opts) []string {
	task := opts.Task
	var sections []string

	// Resolve test file paths: prefer actual_test_files, fall back to estimated_files.
	var testFiles []string
	if task.Context != nil {
		if files, ok := task.Context["actual_test_files"]; ok {
			if fileList, ok := files.([]any); ok {
				for _, f := range fileList {
					if s, ok := f.(string); ok {
						testFiles = append(testFiles, s)
					}
				}
			}
		}
	}
	if len(testFiles) == 0 && task.Context != nil {
		if files, ok := task.Context["estimated_files"]; ok {
			if fileList, ok := files.([]any); ok {
				for _, f := range fileList {
					if s, ok := f.(string); ok {
						testFiles = append(testFiles, s)
					}
				}
			}
		}
	}

	sections = append(sections, "## Instructions", "")
	if custom := asset(opts, "coder", "implementation"); custom != "" {
		sections = append(sections, custom, "")
	}
	sections = append(sections,
		"You are implementing code to pass pre-written tests (TDD).",
		"",
	)

	if len(testFiles) > 0 {
		sections = append(sections, "Pre-written tests exist at:", "")
		for _, f := range testFiles {
			sections = append(sections, fmt.Sprintf("- `%s`", f))
		}
		sections = append(sections, "")
	}

	if projectLanguage(opts) != "cpp" {
		sections = append(sections,
			"Your implementation should:",
			"1. Read the pre-written tests first to understand expected behavior",
			"2. Implement the minimum code to make ALL tests pass",
			"3. Do NOT modify the pre-written tests unless they have a genuine bug",
			"4. If you believe a test is wrong, note it in your commit message but make it pass anyway",
			"",
			"After implementation:",
			"1. Run `go vet ./... && go test ./...` in ONE command",
			"2. If anything fails, read ALL errors, fix ALL issues in one pass, re-run ONCE",
			"3. NEVER modify pre-written TDD tests. Fix your code to match the tests.",
			"4. Run `git add` for all new/untracked files, then commit with "+
				`message: "feat: <what was implemented>"`,
			"5. Do NOT push to remote",
			"",
		)
	}

	// Include test plan if set
	if task.TestPlan != "" {
		sections = append(sections, "## Test Plan", "")
		sections = append(sections, task.TestPlan, "")
	}

	// Depth guidance from plan or generic fallback.
	sections = append(sections, depthGuidanceFromPlan(opts))

	return sections
}

// defaultCoderInstructions returns generic coder instructions for subtasks
// with no phase set (backward compatibility) and integration-phase subtasks.
func defaultCoderInstructions(opts Opts) []string {
	task := opts.Task
	var sections []string

	sections = append(sections, "## Instructions", "")
	sections = append(sections, "You are a coder agent. Implement the described task.", "")
	if custom := asset(opts, "coder", "default"); custom != "" {
		sections = append(sections, custom, "")
	}

	// Include estimated files from task context if present
	if len(task.Context) > 0 {
		if files, ok := task.Context["estimated_files"]; ok {
			sections = append(sections, fmt.Sprintf("Files to create/modify: %v", files), "")
		}
	}

	if projectLanguage(opts) != "cpp" {
		sections = append(sections,
			"## Test Infrastructure Rules (Constitution-Enforced)",
			"",
			"When writing or modifying tests, these rules are strictly enforced:",
			"- DB init: Use `testutil.NewTestDB(t)` or `testutil.NewTestDBWithModels(t, &Model{})` — never `gorm.Open()` in test files",
			"- Test factories: Must live in `internal/testutil/testutil.go`, not local test files",
			"- Git helpers: Use `testutil.SetupBareRepo(t)`, `testutil.CommitFile(t, ...)`, etc.",
			"",
		)
	}

	if projectLanguage(opts) != "cpp" {
		sections = append(sections,
			"After implementation:",
			"1. Run `go vet ./... && go test ./...` in ONE command — not separately",
			"2. If anything fails, read ALL errors, fix ALL in one pass, re-run ONCE more",
			"3. If a pre-existing test breaks: fix your implementation, not the test",
			"4. If this is an integration subtask and the feature changes user-facing "+
				"behavior (CLI, config, TUI, new capabilities), update the README "+
				"or relevant documentation to reflect the changes",
			"5. Run `git add` for all new/untracked files, then commit your changes with a descriptive message",
			"6. Do NOT push to remote",
			"",
		)
	}

	// Include test plan if set
	if task.TestPlan != "" {
		sections = append(sections, "## Test Plan", "")
		sections = append(sections, task.TestPlan, "")
	}

	// Depth guidance from plan or generic fallback.
	sections = append(sections, depthGuidanceFromPlan(opts))

	return sections
}

// researcherInstructions returns prompt sections for researcher agents.
func researcherInstructions() []string {
	return []string{
		"## Instructions",
		"",
		"You are a researcher agent. Investigate the topic and document findings.",
		"",
		"Write your findings to `research-report.md` in the working directory.",
		"",
		"Structure your report with:",
		"1. Summary of findings",
		"2. Detailed analysis",
		"3. Recommendations",
		"4. References to relevant files/code",
		"",
	}
}

// classifierInstructions returns prompt sections for classifier agents.
// The classifier explores the codebase (read-only) and produces a structured
// classification JSON file with category, complexity score, and enriched metadata.
// The output file uses an absolute path with the task ID to avoid path-resolution
// issues and filename collisions between concurrent classifiers.
func classifierInstructions(worktreePath, taskID string) []string {
	outputFile := filepath.Join(worktreePath, fmt.Sprintf("classification-%s.json", taskID))
	return []string{
		"## Instructions",
		"",
		"You are a **classifier** agent. Your job is to explore the codebase and",
		"classify the task described above.",
		"",
		"**IMPORTANT: Do NOT modify any source code files in the repository.**",
		"Your only permitted write is the classification output file described below.",
		"",
		"### Exploration Budget Limit",
		"",
		"Due to the high token cost of extensive codebase exploration, please limit your exploration to:",
		"- **Maximum 8-10 files** for complex tasks",
		"- Focus on **title + description + limited exploration** to make classification decisions",
		"- For complex tasks, classify as **standard** without exhaustive exploration",
		"",
		"### Process",
		"",
		"1. Read the task title and description carefully.",
		"2. Explore the codebase: grep for relevant identifiers, read candidate files,",
		"   assess how many files would need to change.",
		"3. Determine the category and complexity.",
		fmt.Sprintf("4. **You MUST write your classification to the EXACT absolute path `%s`.** This is your only deliverable — if you do not write this file, the pipeline cannot proceed.", outputFile),
		"",
		"### Output Format",
		"",
		fmt.Sprintf("Write a JSON file at `%s` with this schema:", outputFile),
		"",
		"```json",
		"{",
		`  "category": "quickfix" or "standard",`,
		`  "complexity_score": 1-10,`,
		`  "title": "Refined task title based on code exploration",`,
		`  "description": "Enriched description with specifics from code",`,
		`  "target_files": ["path/to/file1.go", "path/to/file2.go"],`,
		`  "rationale": "Evidence-based explanation for the classification"`,
		"}",
		"```",
		"",
		"If the task is too ambiguous to classify even after exploration, output:",
		"",
		"```json",
		"{",
		`  "needs_clarification": true,`,
		`  "questions": ["Specific question 1", "Specific question 2"]`,
		"}",
		"```",
		"",
		"### Classification Guide",
		"",
		"- **quickfix** (complexity 1-3): Single-file change, clear fix, no architectural impact.",
		"- **standard** (complexity 4-10): Multi-file change, design decisions needed, or broad impact.",
		"",
		"Base your classification on what you actually find in the code, not just the description.",
		"",
	}
}

// bugReportInstructions returns prompt sections instructing agents how to
// file structured bug reports. This section is included for all agent types.
func bugReportInstructions() []string {
	return []string{
		"## Bug Report Filing",
		"",
		"If you encounter any of the following during your work, file a bug report:",
		"- Broken builds or failing dependencies",
		"- Flaky or unexpectedly failing tests",
		"- Unclear or contradictory requirements",
		"- Constraint violations you cannot resolve",
		"- Upstream code issues",
		"- Environment problems",
		"",
		"To file a bug report, write a JSON file to `.drem/bug-reports/<uuid>.json` with this schema:",
		"",
		"```json",
		"{",
		`  "title": "Short descriptive title",`,
		`  "description": "What went wrong — be specific",`,
		`  "category": "tooling|merge_conflict|requirements|constraint_violation|upstream_code|test_failure|environment|other",`,
		`  "severity": "blocking|degraded|informational",`,
		`  "reproduction_context": "File paths, commands run, error output — enough to reproduce",`,
		`  "agent_id": "<your agent ID from the metadata file>",`,
		`  "task_id": "<your current task ID>"`,
		"}",
		"```",
		"",
		"Severity guide:",
		"- blocking: You cannot continue your work",
		"- degraded: You worked around it but the problem remains",
		"- informational: You observed it but it has no immediate impact",
		"",
		"File the report and continue your work — do not stop to spawn a new agent.",
		"Read your agent ID from `.claude/agent-metadata.json` in your worktree.",
		"",
	}
}
