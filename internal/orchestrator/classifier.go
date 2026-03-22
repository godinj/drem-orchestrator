package orchestrator

import (
	"context"
	"fmt"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ClassificationResult is the structured output from the bug report classifier.
type ClassificationResult struct {
	Category    string   `json:"category"`     // "quickfix" or "standard"
	Title       string   `json:"title"`        // task title
	Description string   `json:"description"`  // enriched task description
	TargetFiles []string `json:"target_files"` // file paths the fix should target
	Rationale   string   `json:"rationale"`    // why this classification was chosen
}

// enrichmentResult is the structured output from the description enrichment call.
type enrichmentResult struct {
	Description string   `json:"description"`
	TargetFiles []string `json:"target_files"`
}

// classifyBugReport calls the supervisor LLM to classify a bug report as
// quickfix or standard, and produces a structured task description with
// target file hints. Returns nil if the supervisor is not available.
func (o *Orchestrator) classifyBugReport(report *model.BugReport) (*ClassificationResult, error) {
	if o.supervisor == nil {
		return nil, nil
	}

	prompt := fmt.Sprintf(`You are a bug report classifier for a software project. Analyze the following bug report and decide whether it should be a "quickfix" (trivial, single-file fix) or "standard" (complex, multi-file change requiring planning).

Bug Report:
- Title: %s
- Category: %s
- Severity: %s
- Description: %s
- Reproduction Context: %s

Classification criteria for "quickfix":
- Constraint violations (formatting, line count, naming)
- Typo fixes
- Single-line bug fixes with obvious cause
- Simple config adjustments
- Clear error messages pointing to a specific file and line

Classification criteria for "standard":
- Multi-file changes
- Architectural changes
- New features
- Complex bugs requiring investigation
- Changes that affect public APIs

Respond with a JSON object:
{
  "category": "quickfix" or "standard",
  "title": "concise task title",
  "description": "detailed task description including what to fix and how",
  "target_files": ["path/to/file1.go", "path/to/file2.go"],
  "rationale": "one sentence explaining why this classification"
}`,
		report.Title,
		report.Category,
		report.Severity,
		report.Description,
		report.ReproductionContext,
	)

	var result ClassificationResult
	if err := o.supervisor.EvaluateJSON(context.Background(), prompt, &result); err != nil {
		return nil, fmt.Errorf("classify bug report: %w", err)
	}

	if result.Category != "quickfix" && result.Category != "standard" {
		return nil, fmt.Errorf("classify bug report: invalid category %q, must be \"quickfix\" or \"standard\"", result.Category)
	}
	if result.Title == "" {
		return nil, fmt.Errorf("classify bug report: title must not be empty")
	}

	o.logger.Info("classified bug report", "report_id", report.ID, "category", result.Category)

	return &result, nil
}

// enrichQuickFixDescription calls the supervisor LLM to enrich a sparse
// task description with target file hints and expanded context. Used for
// human-created quick fix tasks with minimal descriptions.
// Returns the enriched description and target files, or the original
// description if enrichment fails or supervisor is unavailable.
func (o *Orchestrator) enrichQuickFixDescription(title, description string) (string, []string, error) {
	if o.supervisor == nil {
		return description, nil, nil
	}

	prompt := fmt.Sprintf(`You are a code assistant. A developer created a quick fix task with a sparse description. Enrich it with target file hints and expanded context.

Task Title: %s
Task Description: %s

Based on the title and description, identify:
1. Which files likely need to be modified
2. What the fix likely involves

Respond with a JSON object:
{
  "description": "expanded description with specific fix guidance",
  "target_files": ["path/to/likely/file.go"]
}`,
		title,
		description,
	)

	var result enrichmentResult
	if err := o.supervisor.EvaluateJSON(context.Background(), prompt, &result); err != nil {
		return description, nil, fmt.Errorf("enrich quick fix description: %w", err)
	}

	if result.Description == "" {
		return description, nil, nil
	}

	return result.Description, result.TargetFiles, nil
}
