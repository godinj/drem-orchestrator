package prompt

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/constraints"
)

// depthGuidanceFromPlan extracts depth guidance from the parent task's plan
// for the current subtask. Returns empty string if no depth metadata exists.
func depthGuidanceFromPlan(opts Opts) string {
	if opts.ParentCtx == nil {
		return genericDepthGuidance()
	}

	planRaw, ok := opts.ParentCtx["plan"]
	if !ok {
		return genericDepthGuidance()
	}

	// The plan may be stored as a string (JSON) or already parsed as a map.
	var planJSON string
	switch v := planRaw.(type) {
	case string:
		planJSON = v
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return genericDepthGuidance()
		}
		planJSON = string(data)
	}

	if planJSON == "" {
		return genericDepthGuidance()
	}

	// Parse the plan to extract subtasks with depth metadata.
	var plan struct {
		Subtasks []struct {
			Title            string `json:"title"`
			ModuleBoundaries []struct {
				Package     string `json:"package"`
				Description string `json:"description"`
				Exports     int    `json:"exports"`
			} `json:"module_boundaries"`
			InterfaceShapes []struct {
				Package   string   `json:"package"`
				Functions []string `json:"functions"`
				Types     []string `json:"types"`
			} `json:"interface_shapes"`
		} `json:"subtasks"`
	}

	if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
		return genericDepthGuidance()
	}

	// Find the subtask matching the current task by title.
	var matchedBoundaries []struct {
		Package     string `json:"package"`
		Description string `json:"description"`
		Exports     int    `json:"exports"`
	}
	var matchedShapes []struct {
		Package   string   `json:"package"`
		Functions []string `json:"functions"`
		Types     []string `json:"types"`
	}

	taskTitle := ""
	if opts.Task != nil {
		taskTitle = opts.Task.Title
	}

	for _, st := range plan.Subtasks {
		if taskTitle != "" && st.Title == taskTitle {
			matchedBoundaries = st.ModuleBoundaries
			matchedShapes = st.InterfaceShapes
			break
		}
	}

	// If no matching subtask or no depth metadata, try to gather all depth
	// metadata from the plan as general guidance.
	if len(matchedBoundaries) == 0 && len(matchedShapes) == 0 {
		// Collect depth metadata from all subtasks.
		for _, st := range plan.Subtasks {
			matchedBoundaries = append(matchedBoundaries, st.ModuleBoundaries...)
			matchedShapes = append(matchedShapes, st.InterfaceShapes...)
		}
	}

	if len(matchedBoundaries) == 0 && len(matchedShapes) == 0 {
		return genericDepthGuidance()
	}

	var lines []string
	lines = append(lines, "## Depth Guidance (from plan)", "")
	lines = append(lines, "This subtask defines the following module boundaries:", "")

	for _, mb := range matchedBoundaries {
		lines = append(lines, fmt.Sprintf(
			"- **%s**: %s Expected exports: %d.",
			mb.Package, mb.Description, mb.Exports,
		))
	}
	lines = append(lines, "")

	for _, is := range matchedShapes {
		lines = append(lines, fmt.Sprintf("Target interface shape for `%s`:", is.Package))
		if len(is.Functions) > 0 {
			lines = append(lines, fmt.Sprintf("- Functions: `%s`", strings.Join(is.Functions, "`, `")))
		}
		if len(is.Types) > 0 {
			lines = append(lines, fmt.Sprintf("- Types: `%s`", strings.Join(is.Types, "`, `")))
		}
		lines = append(lines, "")
	}

	lines = append(lines, "Keep your implementation aligned with these boundaries. Do not add exports beyond what is specified.", "")

	return strings.Join(lines, "\n")
}

// depthScoringGuidance returns the depth scoring criteria, examples, and
// self-check sections appended to planner instructions.
func depthScoringGuidance() []string {
	return []string{
		"## Depth Scoring Criteria",
		"",
		"Your plan is scored on three equally-weighted depth criteria:",
		"",
		"1. **Module boundaries**: At least one subtask defines valid module boundaries with a `package` path and `description`.",
		"2. **Interface shapes**: At least one subtask specifies interface shapes with `functions` or `types`.",
		"3. **Deep decomposition**: All boundary-defining subtasks keep `exports` in the range (0, 20].",
		"",
		"Plans without `depth_meta` (module_boundaries and interface_shapes) fall back to a file-coverage " +
			"ratio, which scores lower. Plans that score 0% on all three criteria are automatically flagged " +
			"for rejection.",
		"",
		"## Shallow vs Deep Plan Examples",
		"",
		"### SHALLOW (anti-pattern) — scores 0%",
		"",
		"A subtask that wraps 5 lines of retry logic in a thin wrapper function:",
		"",
		"```",
		`Subtask: "Add retry helper"`,
		"  - No module_boundaries defined",
		"  - No interface_shapes defined",
		"  - Implementation is a pass-through that calls time.Sleep in a loop",
		"  - No internal decision-making, no policy, no state",
		"```",
		"",
		"This scores 0% because it defines no boundaries, no interface shapes, and the " +
			"implementation is just glue code with no real internal logic.",
		"",
		"### DEEP (good pattern) — scores 100%",
		"",
		"A subtask that creates a RetryPolicy with configurable backoff strategy, jitter, " +
			"circuit breaker state machine, and exports only the Policy interface:",
		"",
		"```",
		`Subtask: "Implement RetryPolicy engine"`,
		`  module_boundaries: [{package: "pkg/retry", description: "Retry engine with backoff, jitter, and circuit breaker", exports: 4}]`,
		`  interface_shapes: [{package: "pkg/retry", functions: ["New(opts ...Option) *Policy", "Policy.Execute(ctx, func) error"], types: ["Policy", "Option"]}]`,
		"```",
		"",
		"This scores 100% because it defines clear module boundaries, specifies interface " +
			"shapes with functions and types, keeps exports low (4), and the module does real " +
			"internal work: backoff calculation, jitter randomization, circuit breaker state " +
			"transitions.",
		"",
		"**Module unification**: When the codebase has multiple implementations of the same " +
			"concern (retry, logging, config loading), unify them into shared infrastructure " +
			"rather than adding another thin wrapper. Check the codebase for existing patterns " +
			"before creating a new module.",
		"",
		"## Pre-Submission Depth Self-Check",
		"",
		"Before writing plan.json, evaluate each subtask against this checklist:",
		"",
		"1. Does every implementation subtask have `module_boundaries` with `package`, " +
			"`description`, and `exports` ≤ 20?",
		"2. Does every implementation subtask have `interface_shapes` with at least one " +
			"`function` or `type`?",
		"3. Is each module doing real internal work (policy logic, state machines, validation) " +
			"rather than just delegating to another package?",
		"4. Are there opportunities to unify duplicated concerns into shared infrastructure?",
		"",
		"If any subtask is shallow (no boundaries, no interface shapes, or just a pass-through " +
			"wrapper), restructure the plan before submitting. Shallow subtasks will cause the " +
			"plan to score 0% and be flagged for rejection.",
		"",
	}
}

// genericDepthGuidance returns a generic depth guidance section when no
// plan-level depth metadata is available.
func genericDepthGuidance() string {
	return strings.Join([]string{
		"## Depth Guidance",
		"",
		"Keep modules deep: maximize functionality behind simple interfaces.",
		"- Aim for export ratio \u2264 0.15 (exported symbols / total LOC)",
		"- Avoid pass-through functions that just delegate to another package",
		"- Every exported symbol should justify its existence",
		"",
	}, "\n")
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

// readContextFiles reads the context files specified in .drem/constraints.toml
// and returns their contents as a formatted markdown section. Returns an empty
// string if no config exists or no context files are specified.
func readContextFiles(worktreePath string) string {
	if worktreePath == "" {
		return ""
	}

	cfg, err := constraints.LoadConfig(worktreePath)
	if err != nil || cfg == nil || len(cfg.ContextFiles) == 0 {
		return ""
	}

	var parts []string
	for _, relPath := range cfg.ContextFiles {
		absPath := filepath.Join(worktreePath, relPath)
		data, err := os.ReadFile(absPath)
		if err != nil {
			// Absence is normal for some worktrees — skip silently.
			continue
		}
		parts = append(parts, fmt.Sprintf("### %s\n\n%s", relPath, strings.TrimSpace(string(data))))
	}

	if len(parts) == 0 {
		return ""
	}

	header := "## Architecture & Constraints\n\n" +
		"The following project architecture constraints apply to your work.\n" +
		"Respect file length ceilings, shrink-only rules for grandfathered files,\n" +
		"and all other structural limits described below.\n"

	return header + "\n" + strings.Join(parts, "\n\n") + "\n"
}
