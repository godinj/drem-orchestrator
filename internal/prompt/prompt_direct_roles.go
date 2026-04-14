// prompt_direct_roles.go produces compact system prompts for the direct
// SGLang tool-calling agents (coder, reviewer, fixer). These prompts omit
// the heavy context sections (repo map, CLAUDE.md, context efficiency) that
// the full Generate() function emits for Claude Code — local models used
// behind the direct path have tight context budgets and do not need those
// orientations.
package prompt

import (
	"fmt"
	"strings"
)

// maxDirectDiff caps the git diff included in reviewer prompts for local
// model context budgets.
const maxDirectDiff = 10000

// GenerateDirectCoder builds a compact system prompt for a local-model coder
// agent. Unlike Generate(), it omits repo map, CLAUDE.md, and other heavy
// sections that would overflow a constrained context window.
func GenerateDirectCoder(opts Opts) string {
	task := opts.Task
	var b strings.Builder

	title := ""
	description := ""
	if task != nil {
		title = task.Title
		description = task.Description
	}

	fmt.Fprintf(&b, "You are a coder agent implementing: %s\n\n", title)
	if description != "" {
		fmt.Fprintf(&b, "## Task\n\n%s\n\n", description)
	}

	fmt.Fprintf(&b, "## Working Directory\n\n%s\n\n", opts.WorktreePath)

	if task != nil && len(task.Context) > 0 {
		if files, ok := task.Context["estimated_files"]; ok {
			b.WriteString("## Files to create/modify\n\n")
			writeFileList(&b, files)
			b.WriteString("\n")
		}
	}

	if len(opts.ParentCtx) > 0 {
		b.WriteString("## Parent Task Context\n\n")
		for k, v := range opts.ParentCtx {
			fmt.Fprintf(&b, "- %s: %v\n", k, v)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Verification\n\n")
	b.WriteString("Write all code changes, then run `go vet ./... && go test ./...` in a SINGLE bash command.\n")
	b.WriteString("If tests fail, read ALL errors, fix in one pass, re-run once more. Max 2 verification cycles.\n\n")

	b.WriteString("## Test Infrastructure Rules\n\n")
	b.WriteString("- DB init in tests: use `testutil.NewTestDB(t)`, never `gorm.Open` directly.\n")
	b.WriteString("- Shared test helpers live in `internal/testutil/testutil.go`; do not define local `createTest*`, `setupBareRepo`, etc.\n")
	b.WriteString("- Use `testutil.SetupBareRepo(t)`, `testutil.CommitFile(t, ...)` for git test helpers.\n\n")

	b.WriteString("## Commit\n\n")
	b.WriteString("When tests pass, `git add` new/modified files and `git commit` with a descriptive message.\n")
	b.WriteString("Do NOT push to remote.\n")

	return b.String()
}

// GenerateDirectReviewer builds a compact system prompt for a local-model
// reviewer agent. Output instructs the agent to write review.json.
func GenerateDirectReviewer(opts Opts) string {
	task := opts.Task
	var b strings.Builder

	title := ""
	description := ""
	if task != nil {
		title = task.Title
		description = task.Description
	}

	fmt.Fprintf(&b, "You are a reviewer agent reviewing: %s\n\n", title)
	if description != "" {
		fmt.Fprintf(&b, "## Task\n\n%s\n\n", description)
	}
	fmt.Fprintf(&b, "## Working Directory\n\n%s\n\n", opts.WorktreePath)

	mode := opts.ReviewMode
	if mode == "" {
		mode = "feature"
	}
	fmt.Fprintf(&b, "## Review Mode\n\n%s\n\n", mode)

	switch mode {
	case "plan":
		if opts.PlanJSON != "" {
			b.WriteString("## Plan to Review\n\n```json\n")
			b.WriteString(opts.PlanJSON)
			b.WriteString("\n```\n\n")
		}
		b.WriteString("## Review Criteria\n\n")
		b.WriteString("- Does the plan cover the task scope?\n")
		b.WriteString("- Are subtasks right-sized and independently completable?\n")
		b.WriteString("- Are dependencies and file conflicts identified?\n")
		b.WriteString("- Do estimated files match the task intent?\n\n")
	case "feature":
		diff := opts.GitDiff
		if len(diff) > maxDirectDiff {
			diff = diff[:maxDirectDiff] + "\n...[truncated]"
		}
		if diff != "" {
			b.WriteString("## Feature Diff\n\n```diff\n")
			b.WriteString(diff)
			b.WriteString("\n```\n\n")
		}
		b.WriteString("## Review Criteria\n\n")
		b.WriteString("- Does the diff implement the task correctly?\n")
		b.WriteString("- Are there missing tests, edge cases, or regressions?\n")
		b.WriteString("- Does the code follow project conventions?\n")
		b.WriteString("- Are there security or reliability issues?\n\n")
	}

	b.WriteString("## Output\n\n")
	b.WriteString("Write a JSON file at `review.json` in the working directory with this schema:\n\n")
	b.WriteString("```json\n")
	b.WriteString("{\n")
	b.WriteString("  \"verdict\": \"approve\" | \"reject\" | \"needs_revision\",\n")
	b.WriteString("  \"summary\": \"one paragraph overall assessment\",\n")
	b.WriteString("  \"findings\": [{\"severity\": \"critical|major|minor\", \"message\": \"...\"}],\n")
	b.WriteString("  \"suggestions\": [\"...\"]\n")
	b.WriteString("}\n")
	b.WriteString("```\n\n")
	b.WriteString("Use the read, grep, glob, and bash tools for exploration. ")
	b.WriteString("Do NOT modify code. Your only write is `review.json`.\n")

	return b.String()
}

// GenerateDirectFixer builds a compact system prompt for a local-model fixer
// agent. Includes diagnosis, affected files, and suggested fix.
func GenerateDirectFixer(opts Opts) string {
	task := opts.Task
	var b strings.Builder

	title := ""
	description := ""
	if task != nil {
		title = task.Title
		description = task.Description
	}

	fmt.Fprintf(&b, "You are a fixer agent resolving: %s\n\n", title)
	if description != "" {
		fmt.Fprintf(&b, "## Task\n\n%s\n\n", description)
	}
	fmt.Fprintf(&b, "## Working Directory\n\n%s\n\n", opts.WorktreePath)

	if opts.Diagnosis != "" {
		fmt.Fprintf(&b, "## Diagnosis\n\n%s\n\n", opts.Diagnosis)
	}

	if len(opts.AffectedFiles) > 0 {
		b.WriteString("## Affected Files\n\n")
		for _, f := range opts.AffectedFiles {
			fmt.Fprintf(&b, "- %s\n", f)
		}
		b.WriteString("\n")
	}

	if opts.SuggestedFix != "" {
		fmt.Fprintf(&b, "## Suggested Fix\n\n%s\n\n", opts.SuggestedFix)
	}

	b.WriteString("## Rules\n\n")
	b.WriteString("- Apply the minimal change needed to resolve the diagnosis.\n")
	b.WriteString("- Do not refactor unrelated code.\n")
	b.WriteString("- Verify with `go vet ./... && go test ./...` in a SINGLE bash command.\n")
	b.WriteString("- When tests pass, `git add` and `git commit` with a short descriptive message.\n")
	b.WriteString("- Do NOT push to remote.\n")

	return b.String()
}

// writeFileList formats a task.Context["estimated_files"] value into a
// newline-separated bullet list. Accepts []any (JSON round-trip) or []string.
func writeFileList(b *strings.Builder, v any) {
	switch files := v.(type) {
	case []any:
		for _, f := range files {
			if s, ok := f.(string); ok {
				fmt.Fprintf(b, "- %s\n", s)
			}
		}
	case []string:
		for _, s := range files {
			fmt.Fprintf(b, "- %s\n", s)
		}
	default:
		fmt.Fprintf(b, "- %v\n", v)
	}
}
