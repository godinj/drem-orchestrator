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
	if opts.WorktreeBranch != "" {
		fmt.Fprintf(&b, "Branch: `%s`\n\n", opts.WorktreeBranch)
	}

	if task != nil && len(task.Context) > 0 {
		if files, ok := firstContextValue(task.Context, "estimated_files", "actual_test_files"); ok {
			b.WriteString("## Files to create/modify\n\n")
			writeFileList(&b, files)
			b.WriteString("\n")
		}
		writeDirectContext(&b, task.Context, "prep_data", "Prepared context")
		writeDirectContext(&b, task.Context, "verified_source_pack", "Verified source pack")
		writeDirectContext(&b, task.Context, "planned_interface_contract", "Planned interface contract")
		writeDirectContext(&b, task.Context, "implementation_interface_contract", "Implementation interface contract")
		writeDirectContext(&b, task.Context, "prompt_adjustment", "Prior actionable failure")
	}
	hasPlannedInterfaceContract := task != nil && task.Context != nil && task.Context["planned_interface_contract"] != nil

	if len(opts.ParentCtx) > 0 {
		b.WriteString("## Parent Task Context\n\n")
		for k, v := range opts.ParentCtx {
			fmt.Fprintf(&b, "- %s: %v\n", k, v)
		}
		b.WriteString("\n")
	}

	phase := ""
	if task != nil {
		phase = task.Phase
		if phase == "" && task.Context != nil {
			phase, _ = task.Context["phase"].(string)
		}
	}
	b.WriteString("## Execution contract\n\n")
	b.WriteString("Work only in the listed scope. Read the minimum needed and make the change early. The worker harness owns commit and push; do not run git commit or git push.\n")
	switch phase {
	case "test":
		if hasPlannedInterfaceContract {
			b.WriteString("This is the TEST phase. Treat the planned interface contract and verified source pack above as authoritative, then write the focused red-state test. Follow its red_mode exactly: compile failure is valid only for listed missing C++ symbols; registry, keymap, and call-edge contracts require a compiling behavioral assertion. Exercise production APIs: do not mock the production type, fabricate headers, comment out the contract, hardcode a failure, or implement behavior.\n")
		} else {
			b.WriteString("This is the TEST phase, but no planned interface contract was supplied. Use only existing production APIs. If the task requires a new symbol, stop with a concise missing-contract result instead of searching or inventing an API.\n")
		}
	case "implementation":
		b.WriteString("This is the IMPLEMENTATION phase. The implementation interface contract and verified source pack are authoritative. Read the paired test and named production seam, implement the smallest behavior that satisfies them, and do not modify tests. Read at most 6 relevant files before the first edit.\n")
	case "integration":
		b.WriteString("This is the INTEGRATION phase. Inspect the declared production entrypoint chain, then do only manifest/wiring/assembly work; do not broaden behavior. Read at most 6 relevant files before the first edit.\n")
	default:
		b.WriteString("Implement the smallest complete scoped change. Read at most 4 relevant files before the first edit.\n")
	}
	if opts.ExternalVerification {
		b.WriteString("Native builds and tests run later on the external host. Do not run CMake, install dependencies, or modify build settings to satisfy this container. Run only `git diff --check` (and another lightweight check only if already available), then leave the scoped result for the harness. A test-phase red result is the intended artifact.\n")
	} else if rules := asset(opts, "direct", "coder"); rules != "" {
		b.WriteString(rules)
		b.WriteString("\n")
	} else {
		b.WriteString("Run `go vet ./... && go test ./...` after editing.\n")
	}
	b.WriteString("The harness enforces a 12-call total budget and blocks all shell commands before the first mutation. Do not inventory the repository. If a check fails, make one focused repair attempt; preserve useful scoped work rather than looping.\n\n")
	if projectLanguage(opts) != "cpp" {
		b.WriteString("Test Infrastructure:\n")
		b.WriteString("- DB: use `testutil.NewTestDB(t)`, never `gorm.Open` directly.\n")
		b.WriteString("- Git helpers: `testutil.SetupBareRepo(t)`, `testutil.CommitFile(t, ...)`\n")
		fmt.Fprintf(&b, "- Shared helpers: `%s`\n\n", "internal"+"/testutil/testutil.go")
	}
	b.WriteString("Finish after the lightweight check and leave scoped changes in the working tree. The harness will commit and push them.\n")

	return b.String()
}

func firstContextValue(ctx map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, ok := ctx[key]; ok {
			return value, true
		}
	}
	return nil, false
}

func writeDirectContext(b *strings.Builder, ctx map[string]any, key, label string) {
	value, ok := ctx[key]
	if !ok || value == nil || strings.TrimSpace(fmt.Sprint(value)) == "" {
		return
	}
	fmt.Fprintf(b, "## %s\n\n%v\n\n", label, value)
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
	if rules := asset(opts, "direct", "fixer"); rules != "" {
		for _, line := range strings.Split(rules, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				fmt.Fprintf(&b, "- %s\n", strings.TrimPrefix(line, "- "))
			}
		}
	} else {
		b.WriteString("- Verify with `go vet ./... && go test ./...` in a SINGLE bash command.\n")
	}
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
