# Agent: Classifier — Bug Report Classification Function

You are working on the `master` branch of Drem Orchestrator, a terminal-based task orchestrator that coordinates multiple Claude Code agents to work on software projects in parallel.
Your task is the classifier function: implement a single-LLM-call function that classifies bug reports as quickfix or standard, and enriches sparse task descriptions with target file hints.

## Context

Read these specs before starting:
- `docs/quickfix-tasks/prd-quickfix-tasks.md` (sections 4.3, 4.4, 4.5 — Classifier Function, Bug Report Integration, Human-Created Quick Fix Flow)
- `internal/supervisor/supervisor.go` (`Supervisor.EvaluateJSON` — the LLM call interface you'll use)
- `internal/supervisor/prompts.go` (existing prompt builder patterns — `MergeConflictPrompt`, `BuildFailurePrompt`, etc.)
- `internal/model/bugreport.go` (`BugReport` struct)
- `internal/model/bugreport_enums.go` (`BugReportCategory`, `BugReportSeverity`)
- `internal/model/enums.go` (`TaskCategory` — `CategoryStandard`, `CategoryQuickFix`)
- `internal/orchestrator/orchestrator.go` (`Orchestrator` struct — has `supervisor *supervisor.Supervisor` field)
- `internal/testutil/testutil.go` (`NewMockSupervisor` — for testing)

## Dependencies

This agent depends on Agent 01 (Task Model). If `model.TaskCategory` doesn't exist yet, create a stub in the classifier file:

```go
// Stub — remove when Agent 01 merges
type taskCategory = string
const categoryStandard taskCategory = "standard"
const categoryQuickFix taskCategory = "quickfix"
```

## Deliverables

### New files

#### 1. `internal/orchestrator/classifier.go`

Implement the bug report classifier as a method on `*Orchestrator`. It uses the existing `supervisor.EvaluateJSON` for the LLM call.

**Types:**

```go
// ClassificationResult is the structured output from the bug report classifier.
type ClassificationResult struct {
    Category    string   `json:"category"`     // "quickfix" or "standard"
    Title       string   `json:"title"`         // task title
    Description string   `json:"description"`   // enriched task description
    TargetFiles []string `json:"target_files"`  // file paths the fix should target
    Rationale   string   `json:"rationale"`     // why this classification was chosen
}
```

**Functions:**

```go
// classifyBugReport calls the supervisor LLM to classify a bug report as
// quickfix or standard, and produces a structured task description with
// target file hints. Returns nil if the supervisor is not available.
func (o *Orchestrator) classifyBugReport(report *model.BugReport) (*ClassificationResult, error)
```

Implementation:
1. If `o.supervisor == nil`, return `nil, nil` (graceful degradation — all reports default to standard)
2. Build a classification prompt (see prompt template below)
3. Call `o.supervisor.EvaluateJSON(context.Background(), prompt, &result)`
4. Validate the result: `Category` must be `"quickfix"` or `"standard"`, `Title` must be non-empty
5. Return the result

```go
// enrichQuickFixDescription calls the supervisor LLM to enrich a sparse
// task description with target file hints and expanded context. Used for
// human-created quick fix tasks with minimal descriptions.
// Returns the enriched description and target files, or the original
// description if enrichment fails or supervisor is unavailable.
func (o *Orchestrator) enrichQuickFixDescription(title, description string) (enrichedDesc string, targetFiles []string, err error)
```

Implementation:
1. If `o.supervisor == nil`, return `description, nil, nil`
2. Build an enrichment prompt (see prompt template below)
3. Call `o.supervisor.EvaluateJSON`
4. Return enriched description and target files

**Classification prompt template** (build as a Go string):

```
You are a bug report classifier for a software project. Analyze the following bug report and decide whether it should be a "quickfix" (trivial, single-file fix) or "standard" (complex, multi-file change requiring planning).

Bug Report:
- Title: {report.Title}
- Category: {report.Category}
- Severity: {report.Severity}
- Description: {report.Description}
- Reproduction Context: {report.ReproductionContext}

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
}
```

**Enrichment prompt template:**

```
You are a code assistant. A developer created a quick fix task with a sparse description. Enrich it with target file hints and expanded context.

Task Title: {title}
Task Description: {description}

Based on the title and description, identify:
1. Which files likely need to be modified
2. What the fix likely involves

Respond with a JSON object:
{
  "description": "expanded description with specific fix guidance",
  "target_files": ["path/to/likely/file.go"]
}
```

#### 2. `internal/orchestrator/classifier_test.go`

Test the classifier function. Use `testutil.NewMockSupervisor` for LLM mocking.

**Tests:**

- `TestClassifyBugReport_QuickFix` — Mock supervisor returns `{"category":"quickfix",...}` for a constraint violation bug report. Verify result fields.
- `TestClassifyBugReport_Standard` — Mock supervisor returns `{"category":"standard",...}` for a multi-file architectural issue. Verify result fields.
- `TestClassifyBugReport_NilSupervisor` — Supervisor is nil. Verify returns `nil, nil`.
- `TestClassifyBugReport_InvalidCategory` — Mock supervisor returns invalid category. Verify error.
- `TestClassifyBugReport_AllFieldsPopulated` — Verify all fields (title, description, target_files, rationale) are present in the result.
- `TestEnrichQuickFixDescription_Enriches` — Mock supervisor returns enriched description. Verify result.
- `TestEnrichQuickFixDescription_NilSupervisor` — Supervisor is nil. Verify returns original description.

**Test setup:**
- Create an orchestrator with a mock supervisor using the pattern from existing tests
- The mock supervisor takes a response string — pass valid JSON matching the expected output schema

```go
func setupClassifierTest(t *testing.T, mockResponse string) *Orchestrator {
    t.Helper()
    db := testutil.NewTestDB(t)
    sup := testutil.NewMockSupervisor(t, mockResponse)
    // ... minimal orchestrator with db and supervisor
}
```

If `NewMockSupervisor` doesn't exist or has a different signature, read `internal/testutil/testutil.go` to find the correct pattern.

## Scope Limitation

- Do NOT modify `ingestBugReports()` or `doTick()` — wiring the classifier into the tick loop belongs to Agent 05
- Do NOT modify any TUI files — that belongs to Agent 04
- Do NOT modify `processBacklog` or lifecycle routing — that belongs to Agent 02
- You own: `classifier.go` and `classifier_test.go` only

## Conventions

- Namespace: `package orchestrator`
- Error wrapping: `fmt.Errorf("classify bug report: %w", err)`
- Logging: `o.logger.Info("classified bug report", "report_id", report.ID, "category", result.Category)`
- Prompt strings: use raw string literals or `fmt.Sprintf` — follow the pattern in `internal/supervisor/prompts.go`
- Build verification: `go build ./... && go test ./internal/orchestrator/...`
- Format: `gofmt -w .`
