// Package prompt builds markdown prompt strings that are piped to Claude Code
// agent sessions. Each prompt includes task details, project context, worktree
// information, agent-type-specific instructions, and prior memories.
package prompt

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/model"
)

const maxDiffLen = 50000

// Opts contains all inputs needed to generate an agent prompt.
type Opts struct {
	Task         *model.Task
	Project      *model.Project
	AgentType    model.AgentType
	WorktreePath string
	Memories     []model.Memory
	Comments     []model.TaskComment
	ParentCtx    map[string]any

	// Reviewer fields
	ReviewMode string // "plan" or "feature"
	PlanJSON   string // raw plan JSON for plan review
	GitDiff    string // diff for feature review

	// Fixer fields
	Diagnosis     string   // root cause diagnosis
	AffectedFiles []string // files to fix
	SuggestedFix  string   // suggested fix from diagnosis
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
		sections = append(sections, "")
	}

	// 2b. Repository Map — structural overview before task details
	repoMap := readRepoMap(opts.WorktreePath)
	if repoMap != "" {
		sections = append(sections, repoMap)
	}

	// 2c. Context Efficiency
	if repoMap != "" {
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

	// 2d. Verification Efficiency
	sections = append(sections,
		"## Verification Strategy",
		"",
		"Each turn costs context. Minimize verification rounds:",
		"1. Write ALL code changes before running any verification",
		"2. Run `go vet ./... && go test ./...` in a SINGLE command — never separately",
		"3. If verification fails, read ALL errors, fix ALL issues in one pass, then verify ONCE more",
		"4. Maximum 2 verification cycles. If tests still fail after 2 fix attempts, commit what you have with a note",
		"5. Do NOT re-read files you already read — use your memory of their contents",
		"",
	)

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
	buildCmds := readBuildCommands(opts.WorktreePath)
	if buildCmds != "" {
		sections = append(sections, "## Build & Verify", "")
		sections = append(sections, "```bash")
		sections = append(sections, buildCmds)
		sections = append(sections, "```", "")
	}

	// 8. Architecture & Constraints — read context files from .drem/constraints.toml
	ctxFiles := readContextFiles(opts.WorktreePath)
	if ctxFiles != "" {
		sections = append(sections, ctxFiles)
	}

	// Scope limitation
	sections = append(sections, "## Scope", "")
	sections = append(sections,
		"Only modify files directly relevant to this task. "+
			"Do not refactor unrelated code or change project configuration "+
			"unless the task explicitly requires it.",
		"",
	)

	// Completion instructions (reviewer agents don't commit)
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

	return strings.Join(sections, "\n")
}

// plannerInstructions returns prompt sections for planner agents.
func plannerInstructions() []string {
	sections := []string{
		"## Instructions",
		"",
		"You are a planner agent. Decompose this task into implementable subtasks.",
		"",
		"Analyze the codebase and produce a `plan.json` file in the working directory root with this format:",
		"",
		"```json",
		"{",
		`  "subtasks": [`,
		"    {",
		`      "title": "Short descriptive title",`,
		`      "description": "Detailed implementation description",`,
		`      "agent_type": "coder",`,
		`      "phase": "test|implementation|integration",`,
		`      "tests_for": [1],`,
		`      "files": ["path/to/file1.go", "path/to/file2.go"],`,
		`      "dependencies": [],`,
		`      "priority": 1,`,
		`      "module_boundaries": [`,
		`        {"package": "pkg/foo", "description": "what it encapsulates", "exports": 5}`,
		"      ],",
		`      "interface_shapes": [`,
		`        {"package": "pkg/foo", "functions": ["DoThing(ctx context.Context) error"], "types": ["Config", "Result"]}`,
		"      ]",
		"    }",
		"  ],",
		`  "tdd_exceptions": [`,
		"    {",
		`      "subtask_index": 4,`,
		`      "justification": "Integration wiring connecting already-tested components"`,
		"    }",
		"  ],",
		`  "coverage": [`,
		"    {",
		`      "criterion": "description of the acceptance criterion",`,
		`      "subtask_indices": [0, 2]`,
		"    }",
		"  ],",
		`  "assumptions": [`,
		"    {",
		`      "decision": "what you decided",`,
		`      "alternatives": ["other option 1", "other option 2"],`,
		`      "why_chosen": "why you picked this over the alternatives"`,
		"    }",
		"  ]",
		"}",
		"```",
		"",
		"Rules:",
		"- Each subtask should be independently implementable by one agent",
		"- List specific files each subtask will create or modify",
		"- Set dependencies between subtasks where order matters (use 0-based indices)",
		`- Use agent_type "coder" for implementation, "researcher" for investigation`,
		"- Priority 1 = highest priority",
		"",
		"## Test-Driven Development (MANDATORY)",
		"",
		"Every implementation subtask MUST have exactly ONE corresponding test subtask " +
			"that runs FIRST. This is a strict 1:1 mapping — no test subtask may cover " +
			"multiple implementation subtasks. This protects against subtle regressions " +
			"by keeping each test tightly scoped to its implementation.",
		"",
		"### Test Subtask Requirements",
		"",
		"For each implementation subtask, create exactly one test subtask that:",
		`- Has phase: "test" and tests_for: [<impl subtask index>] (exactly one index)`,
		"- Writes tests that define the expected behavior BEFORE implementation",
		"- Tests should initially FAIL (they test unimplemented behavior)",
		"- Covers the acceptance criteria relevant to that implementation subtask",
		`- Has agent_type: "coder"`,
		"- Has NO dependencies on implementation subtasks",
		"- Lists ALL files it will create or modify in `files`, including stub/interface " +
			"files needed for compilation. Go test files often require stub implementations " +
			"in the same package — these MUST appear in the file list so the scheduler " +
			"can detect overlap and avoid parallel merge conflicts.",
		"",
		"### Implementation Subtask Requirements",
		"",
		"Each implementation subtask must:",
		`- Have phase: "implementation"`,
		"- Make the pre-written tests pass",
		"- NEVER modify the pre-written tests — always fix implementation to match tests",
		"",
		"Note: you do NOT need to add the test subtask to the implementation subtask's " +
			"`dependencies` — this dependency is auto-generated from `tests_for`. Only use " +
			"`dependencies` for ordering between implementation subtasks themselves.",
		"",
		"### TDD Exceptions",
		"",
		"If a subtask genuinely cannot be test-first, declare it in `tdd_exceptions` with the subtask index " +
			`and a specific justification (not "too hard to test"). Valid: integration wiring, research, ` +
			"infrastructure where the build IS the test. Invalid: UI code, behavioral refactors (API changes, interface restructuring), \"tests will be added later\". Valid: structural refactoring (moving code between files in the same package with no behavior or API change — existing tests verify correctness).",
		"",
		"### Ordering",
		"",
		"test subtasks -> HUMAN REVIEW -> implementation subtasks -> integration subtask",
		"",
		"### Example Structure",
		"",
		`Subtask 0: "Write tests for X" (phase: test, tests_for: [1])`,
		`  files: ["pkg/x_test.go", "pkg/x_stub.go"]`,
		`Subtask 1: "Implement X" (phase: implementation)`,
		"  -> auto-depends on subtask 0 via tests_for",
		`Subtask 2: "Write tests for Y" (phase: test, tests_for: [3])`,
		`  files: ["pkg/y_test.go", "pkg/y_stub.go"]`,
		`Subtask 3: "Implement Y" (phase: implementation)`,
		"  -> auto-depends on subtask 2 via tests_for",
		`Subtask 4: "Integration wiring" (phase: integration, depends: [1, 3])`,
		"",
		"Note: test subtasks 0 and 2 both target `pkg/` — the scheduler will " +
			"detect this overlap and serialize them. If they need the same shared types, " +
			"use the Shared Foundations pattern below.",
		"",
		"### Shared Foundations Pattern",
		"",
		"When multiple subtasks need the same new type or interface, create a small " +
			"foundational subtask that establishes shared types first. Other subtasks " +
			"depend on it, avoiding duplicate stubs and merge conflicts:",
		"",
		`Subtask 0: "Define shared types for pkg" (phase: implementation, files: ["pkg/types.go"])`,
		`Subtask 1: "Write tests for X" (phase: test, tests_for: [2], depends: [0])`,
		`Subtask 2: "Implement X" (phase: implementation, depends: [0])`,
		"",
		"## Coverage Verification",
		"",
		"Before finalizing your plan, verify:",
		"1. List every acceptance criterion from the task description",
		"2. For each criterion, identify which subtask(s) address it",
		"3. If any criterion is not covered, add a subtask for it",
		"4. If any subtask doesn't map to a criterion, justify it or remove it",
		"5. If the feature changes user-facing behavior, configuration, CLI flags, " +
			"or adds new capabilities, verify that at least one subtask updates " +
			"the relevant README or documentation. This can be a dedicated subtask " +
			"or a step within the integration subtask.",
		"",
		"## Assumption Reporting",
		"",
		"For each decision in your plan, evaluate whether the task description explicitly specified " +
			"it or whether you inferred it. Report ALL inferred decisions in the `assumptions` field.",
		"",
		"An assumption is any decision where:",
		"- The task description did not specify the approach and you chose one",
		"- You selected a specific technology, library, or pattern that wasn't mentioned",
		"- You made a scoping decision (included or excluded something not explicitly addressed)",
		"- You interpreted an ambiguous requirement in a specific way",
		"",
		"For each assumption, provide:",
		"- `decision`: what you decided (e.g., \"Using Redis for the cache layer\")",
		"- `alternatives`: other reasonable options you considered (at least one)",
		"- `why_chosen`: your reasoning for this choice over the alternatives",
		"",
		"If the task description is fully specified with no room for interpretation, the " +
			"assumptions array may be empty. But err on the side of reporting — if in doubt, " +
			"it's an assumption.",
		"",
		"## Integration Subtask",
		"",
		"Your plan MUST include a final integration subtask (phase: \"integration\") that:",
		"- Wires together the components built by other subtasks",
		"- Verifies end-to-end functionality (not just unit tests)",
		"- Has dependencies on ALL other implementation subtasks",
		"- Touches the files that connect subsystems (registries, routers, factories, main entry points)",
		"",
		"This subtask runs last, after all other agent work is merged.",
		"If the feature is simple enough to not need integration wiring, explicitly state why in the subtask description.",
		"",
		"## Decomposition Rules",
		"",
		"DO:",
		"- Decompose along functional boundaries that minimize file overlap",
		"- Make each subtask produce a testable, observable behavior change",
		"- Include acceptance criteria from the parent task in subtask descriptions",
		"- Prefer fewer, larger subtasks over many small ones (3-6 is typical)",
		"- Include documentation updates (README, walkthrough, usage examples) when " +
			"the feature changes user-facing behavior — either as a step in the " +
			"integration subtask or as a dedicated subtask",
		"",
		"DO NOT:",
		"- Decompose by code layer (one subtask for models, one for handlers, one for UI) — this maximizes file overlap and merge conflicts",
		"- Create subtasks for generic operations that already exist in the codebase — verify the operation doesn't exist before planning it",
		"- Plan more than 8 subtasks — if you need more, the task should be split into multiple parent tasks",
		"- Omit the files list — this is used for scheduling and must be accurate",
		"",
		"## File Overlap",
		"",
		"Subtasks that modify the same files CANNOT run in parallel — they will be serialized, increasing total time. Minimize file overlap between subtasks. If overlap is unavoidable, use the `dependencies` field to specify the correct merge order.",
		"",
		"When multiple subtasks must modify the same file (e.g., a registry or router), prefer having ONE subtask own that file and other subtasks depend on it, rather than having all subtasks append to it independently.",
		"",
		"## Module Depth Requirements",
		"",
		"You MUST design for depth. Every subtask that creates or modifies a Go package MUST specify:",
		"",
		"1. **Module boundaries** in the subtask's `module_boundaries` field:",
		"   - `package`: the Go package path (e.g., \"pkg/constraints/depth\")",
		"   - `description`: what this module encapsulates (one sentence)",
		"   - `exports`: the expected number of exported symbols (aim for ≤ 10)",
		"",
		"2. **Interface shapes** in the subtask's `interface_shapes` field:",
		"   - `package`: the Go package path",
		"   - `functions`: list of exported function signatures (e.g., \"Analyze(worktreeRoot, pkgPath string) (*DepthReport, error)\")",
		"   - `types`: list of exported type names (e.g., \"DepthReport\", \"PassThrough\")",
		"",
		"A deep module has a LOT of functionality behind a SIMPLE interface:",
		"- Few exported symbols relative to total implementation (export ratio ≤ 0.15)",
		"- No pass-through functions that just delegate to another package",
		"- Rich internal logic that justifies the module's existence",
		"",
		"If you cannot define module boundaries for a subtask, explain why in the description.",
		"Do NOT create shallow modules that redistribute complexity through pass-through interfaces.",
		"",
	}

	sections = append(sections, depthScoringGuidance()...)
	return sections
}

// coderInstructions returns prompt sections for coder agents, dispatching
// by the task's Phase field to provide TDD-specific guidance.
func coderInstructions(opts Opts) []string {
	switch opts.Task.Phase {
	case "test":
		return testPhaseCoderInstructions(opts)
	case "implementation":
		return implPhaseCoderInstructions(opts)
	default:
		return defaultCoderInstructions(opts)
	}
}

// testPhaseCoderInstructions returns instructions for writing tests BEFORE
// implementation (TDD test phase).
func testPhaseCoderInstructions(opts Opts) []string {
	task := opts.Task
	var sections []string

	sections = append(sections, "## Instructions", "")
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

	// Include estimated files from task context if present
	if len(task.Context) > 0 {
		if files, ok := task.Context["estimated_files"]; ok {
			sections = append(sections, fmt.Sprintf("Files to create/modify: %v", files), "")
		}
	}

	sections = append(sections,
		"## Test Infrastructure Rules (Constitution-Enforced)",
		"",
		"When writing or modifying tests, these rules are strictly enforced:",
		"- DB init: Use `testutil.NewTestDB(t)` or `testutil.NewTestDBWithModels(t, &Model{})` — never `gorm.Open()` in test files",
		"- Test factories: Must live in `internal/testutil/testutil.go`, not local test files",
		"- Git helpers: Use `testutil.SetupBareRepo(t)`, `testutil.CommitFile(t, ...)`, etc.",
		"",
	)

	sections = append(sections,
		"After implementation:",
		"1. Run the build command to verify compilation",
		"2. Run the FULL test suite — not just your new tests",
		"3. ALL tests must pass. Do not commit if any test fails.",
		"4. If a test fails:",
		"   - If it's a test you wrote or modified: fix it",
		"   - If it's a pre-existing test broken by your changes: fix your implementation, not the test",
		"5. If this is an integration subtask and the feature changes user-facing "+
			"behavior (CLI, config, TUI, new capabilities), update the README "+
			"or relevant documentation to reflect the changes",
		"6. Run `git add` for all new/untracked files, then commit your changes with a descriptive message",
		"7. Do NOT push to remote",
		"",
	)

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
