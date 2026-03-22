# Agent: Bug Report Integration — Wire Classifier into Orchestrator Tick Loop

You are working on the `master` branch of Drem Orchestrator, a terminal-based task orchestrator that coordinates multiple Claude Code agents to work on software projects in parallel.
Your task is integration: wire the classifier function into the orchestrator's bug report ingestion phase so that newly ingested bug reports are automatically classified and turned into quickfix or standard tasks.

## Context

Read these specs before starting:
- `docs/quickfix-tasks/prd-quickfix-tasks.md` (sections 4.3, 4.4 — Classifier Function, Bug Report Integration)
- `internal/orchestrator/orchestrator.go` (`ingestBugReports` method — line ~204, `doTick` — line ~220)
- `internal/orchestrator/classifier.go` (`classifyBugReport` method, `ClassificationResult` type)
- `internal/bugreport/ingest.go` (`Service.Ingest` — returns count, currently just inserts into DB)
- `internal/bugreport/service.go` (`Service.List`, `Service.UpdateStatus` — querying and updating bug reports)
- `internal/model/bugreport.go` (`BugReport` struct — `ID`, `Status`, `PromotedTaskID`)
- `internal/model/bugreport_enums.go` (`BugReportStatus` — `open`, `acknowledged`, `promoted`, `dismissed`)
- `internal/model/enums.go` (`TaskCategory`, `CategoryQuickFix`, `CategoryStandard`)
- `internal/orchestrator/bugreport_test.go` (existing bug report test patterns)

## Dependencies

This agent depends on:
- **Agent 01 (Task Model)**: `model.TaskCategory`, `model.CategoryQuickFix`, `model.CategoryStandard`, `Task.Category` field
- **Agent 02 (Lifecycle Routing)**: quickfix tasks in backlog are processed via `processQuickFix` in the next tick
- **Agent 03 (Classifier)**: `classifyBugReport(*model.BugReport) (*ClassificationResult, error)` method on `*Orchestrator`

If `classifyBugReport` doesn't exist yet, create a stub:

```go
func (o *Orchestrator) classifyBugReport(report *model.BugReport) (*ClassificationResult, error) {
    return nil, nil // stub — replaced by Agent 03
}

type ClassificationResult struct {
    Category    string   `json:"category"`
    Title       string   `json:"title"`
    Description string   `json:"description"`
    TargetFiles []string `json:"target_files"`
    Rationale   string   `json:"rationale"`
}
```

## Deliverables

### Modified files

#### 1. `internal/orchestrator/orchestrator.go`

**Modify `ingestBugReports`** to classify newly ingested reports and create tasks.

Current implementation:

```go
func (o *Orchestrator) ingestBugReports() {
    if o.bugreport == nil {
        return
    }
    n, err := o.bugreport.Ingest(o.bugreportDir, o.projectID)
    if err != nil {
        o.logger.Warn("bug report ingestion error", "error", err)
    }
    if n > 0 {
        o.logger.Info("ingested bug reports", "count", n)
    }
}
```

New implementation:

```go
func (o *Orchestrator) ingestBugReports() {
    if o.bugreport == nil {
        return
    }
    n, err := o.bugreport.Ingest(o.bugreportDir, o.projectID)
    if err != nil {
        o.logger.Warn("bug report ingestion error", "error", err)
    }
    if n > 0 {
        o.logger.Info("ingested bug reports", "count", n)
        o.classifyNewBugReports()
    }
}
```

**Add `classifyNewBugReports`** as a new method:

```go
// classifyNewBugReports queries for unclassified (open, no PromotedTaskID) bug
// reports and runs the classifier on each. Creates a quickfix or standard task
// based on the classification result.
func (o *Orchestrator) classifyNewBugReports()
```

Implementation:
1. Query bug reports with `Status = "open"` and `PromotedTaskID IS NULL` and `ProjectID = o.projectID`
   - Use `o.bugreport.List(bugreport.ListFilters{...})` if a suitable filter exists, or query directly via `o.db`
2. For each report:
   a. Call `o.classifyBugReport(report)`
   b. If classifier returns nil (no supervisor), skip — the report stays open for manual triage
   c. If classifier returns an error, log and skip
   d. If classification succeeds:
      - Determine category: `model.CategoryQuickFix` if `result.Category == "quickfix"`, else `model.CategoryStandard`
      - Build task description by combining the classification result with bug report context:
        ```
        Description: result.Description

        --- Bug Report Context ---
        Category: report.Category
        Severity: report.Severity
        Original: report.Description
        Reproduction: report.ReproductionContext
        Rationale: result.Rationale
        ```
      - Create the task:
        ```go
        task := &model.Task{
            ID:          uuid.New(),
            ProjectID:   o.projectID,
            Title:       result.Title,
            Description: enrichedDescription,
            Status:      model.StatusBacklog,
            Category:    category,
        }
        ```
      - If `result.TargetFiles` is non-empty, store in `task.Context["target_files"]`
      - Save task to DB
      - Update bug report: set `PromotedTaskID = &task.ID`, `Status = model.BugReportPromoted`
      - Save bug report
      - Emit `"bugreport_classified"` event with task_id, report_id, category
      - Log the classification
3. Do NOT error out if one report fails — log and continue to the next

**Important:** The classifier runs synchronously within the tick loop (not a separate goroutine). This is by design per the PRD — classification is a single fast LLM call, not an agent spawn.

### New test file

#### 2. `internal/orchestrator/bugreport_classify_test.go`

Test the bug report classification integration. Follow the pattern from `bugreport_test.go`.

**Tests:**

- `TestClassifyNewBugReports_CreatesQuickFixTask` — Ingest a bug report file, mock supervisor returns `{"category":"quickfix",...}`. Run `ingestBugReports`. Verify:
  - A task was created with `Category = model.CategoryQuickFix`
  - The bug report has `Status = promoted` and `PromotedTaskID` set
  - Task description includes bug report context

- `TestClassifyNewBugReports_CreatesStandardTask` — Same as above but mock returns `{"category":"standard",...}`. Verify task has `Category = model.CategoryStandard`.

- `TestClassifyNewBugReports_NilSupervisor_SkipsClassification` — No supervisor configured. Ingest a bug report. Verify no task is created and bug report stays `open`.

- `TestClassifyNewBugReports_ClassifierError_SkipsReport` — Mock supervisor returns an error. Verify no task is created and bug report stays `open`.

- `TestClassifyNewBugReports_TargetFilesStoredInContext` — Mock returns target_files. Verify `task.Context["target_files"]` is populated.

- `TestClassifyNewBugReports_AlreadyPromotedSkipped` — Bug report already has `PromotedTaskID` set. Verify it is not re-classified.

**Test setup:**

```go
func setupClassifyTest(t *testing.T, mockResponse string) (*Orchestrator, *bugreport.Service, uuid.UUID) {
    // Similar to setupOrchestratorWithBugReports but with mock supervisor
}
```

Use `testutil.NewMockSupervisor(t, mockResponse)` to mock the supervisor's LLM response.

## Scope Limitation

- Do NOT modify `classifier.go` — that belongs to Agent 03
- Do NOT modify `task_processing.go` or lifecycle routing — that belongs to Agent 02
- Do NOT modify TUI files — that belongs to Agent 04
- Do NOT modify `internal/model/` — that belongs to Agent 01
- You own: `orchestrator.go` (`ingestBugReports` modification + `classifyNewBugReports` method) and `bugreport_classify_test.go`

## Conventions

- Namespace: `package orchestrator`
- Error wrapping: `fmt.Errorf("classify new bug reports: %w", err)`
- Logging: `o.logger.Info("classified bug report", "report_id", report.ID, "category", result.Category, "task_id", task.ID)`
- Events: `o.emit("bugreport_classified", map[string]any{...})`
- Build verification: `go build ./... && go test ./internal/orchestrator/...`
- Format: `gofmt -w .`
