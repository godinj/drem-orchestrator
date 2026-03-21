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
