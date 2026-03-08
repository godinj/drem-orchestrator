// Package prompt builds markdown prompt strings that are piped to Claude Code
// agent sessions. Each prompt includes task details, project context, worktree
// information, agent-type-specific instructions, and prior memories.
package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// Opts contains all inputs needed to generate an agent prompt.
type Opts struct {
	Task         *model.Task
	Project      *model.Project
	AgentType    model.AgentType
	WorktreePath string
	Memories     []model.Memory
	Comments     []model.TaskComment
	ParentCtx    map[string]any
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

	// 3. Task Details
	sections = append(sections, "## Task Description", "")
	sections = append(sections, opts.Task.Description, "")

	// Task-specific context (exclude internal keys injected below).
	if len(opts.Task.Context) > 0 {
		sections = append(sections, "## Additional Context", "")
		for key, value := range opts.Task.Context {
			// Skip keys that are injected as dedicated sections below.
			switch key {
			case "prompt_adjustment":
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
		sections = append(sections, coderInstructions(opts.Task)...)
	case model.AgentResearcher:
		sections = append(sections, researcherInstructions()...)
	default:
		sections = append(sections, defaultInstructions()...)
	}

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

	// Scope limitation
	sections = append(sections, "## Scope", "")
	sections = append(sections,
		"Only modify files directly relevant to this task. "+
			"Do not refactor unrelated code or change project configuration "+
			"unless the task explicitly requires it.",
		"",
	)

	// Completion instructions
	sections = append(sections, "## Completion", "")
	sections = append(sections,
		"When you have completed the task, commit all changes with a "+
			"descriptive commit message. Ensure all tests pass before committing.",
		"",
	)

	return strings.Join(sections, "\n")
}

// plannerInstructions returns prompt sections for planner agents.
func plannerInstructions() []string {
	return []string{
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
		`      "files": ["path/to/file1.go", "path/to/file2.go"],`,
		`      "dependencies": [],`,
		`      "priority": 1`,
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
		"## Coverage Verification",
		"",
		"Before finalizing your plan, verify:",
		"1. List every acceptance criterion from the task description",
		"2. For each criterion, identify which subtask(s) address it",
		"3. If any criterion is not covered, add a subtask for it",
		"4. If any subtask doesn't map to a criterion, justify it or remove it",
		"",
		"Include this mapping in your plan.json:",
		"",
		`"coverage": [`,
		"  {",
		`    "criterion": "description of the acceptance criterion",`,
		`    "subtask_indices": [0, 2]`,
		"  }",
		"]",
		"",
		"## Integration Subtask",
		"",
		"Your plan MUST include a final integration subtask that:",
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
		"## Test Subtasks",
		"",
		"If you include a testing subtask, it MUST:",
		"- Depend on ALL implementation subtasks (list all indices in `dependencies`)",
		"- Be the last subtask (or second-to-last, before integration)",
		"- Have agent_type \"coder\" (not \"researcher\")",
		"",
		"Ordering: implementation subtasks -> test subtask -> integration subtask",
		"",
	}
}

// coderInstructions returns prompt sections for coder agents.
func coderInstructions(task *model.Task) []string {
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
		"After implementation:",
		"1. Run the build command to verify compilation",
		"2. Run tests if applicable",
		"3. Commit your changes with a descriptive message",
		"4. Do NOT push to remote",
		"",
	)

	// Include test plan if set
	if task.TestPlan != "" {
		sections = append(sections, "## Test Plan", "")
		sections = append(sections, task.TestPlan, "")
	}

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

// defaultInstructions returns generic prompt sections for unknown agent types.
func defaultInstructions() []string {
	return []string{
		"## Instructions",
		"",
		"Complete the task as described above. Commit your changes when done.",
		"",
	}
}

// readBuildCommands attempts to read build/test commands from CLAUDE.md in the
// worktree. Returns the commands block or an empty string if the file is absent
// or unreadable.
func readBuildCommands(worktreePath string) string {
	if worktreePath == "" {
		return ""
	}

	claudeMD := filepath.Join(worktreePath, "CLAUDE.md")
	data, err := os.ReadFile(claudeMD)
	if err != nil {
		return ""
	}

	// Extract the first ```bash block from CLAUDE.md as the build commands
	content := string(data)
	start := strings.Index(content, "```bash\n")
	if start < 0 {
		return ""
	}
	start += len("```bash\n")
	end := strings.Index(content[start:], "```")
	if end < 0 {
		return ""
	}

	return strings.TrimSpace(content[start : start+end])
}

// WritePromptFile writes the prompt to <worktree>/.claude/agent-prompt.md,
// creating directories as needed. Returns the full path to the written file.
func WritePromptFile(worktreePath, prompt string) (string, error) {
	claudeDir := filepath.Join(worktreePath, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return "", fmt.Errorf("create .claude directory: %w", err)
	}

	promptPath := filepath.Join(claudeDir, "agent-prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		return "", fmt.Errorf("write prompt file: %w", err)
	}

	return promptPath, nil
}
