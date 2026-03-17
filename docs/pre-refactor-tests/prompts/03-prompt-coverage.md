# Agent: Prompt Package Test Coverage

You are working on the `master` branch of Drem Orchestrator, a Go multi-agent development orchestrator.
Your task is pre-refactor test hardening: raise `internal/prompt/` coverage from 54% to 70%+.

## Context

Read these before starting:
- `internal/prompt/prompt.go` (Generate, all instruction functions, readBuildCommands)
- `internal/prompt/prompt_test.go` (existing tests — planner, researcher, fixer, readBuildCommands covered)
- `internal/model/enums.go` (AgentType constants: AgentPlanner, AgentCoder, AgentReviewer, AgentFixer, AgentResearcher)
- `internal/model/models.go` (Task struct with Phase, Context, TestsFor, TestPlan fields)

## Deliverables

Add all new tests to `internal/prompt/prompt_test.go`. Do NOT create new test files.

The existing test file has a `minimalOpts()` helper that returns a basic `Opts` struct. Reuse it.

### 1. coderInstructions dispatch (`TestCoderInstructions_*`)

Test that `Generate()` produces phase-specific instructions based on `Task.Phase`:

- `TestGenerate_CoderTestPhase` — set `Task.Phase = "test"`, `AgentType = AgentCoder`, verify output contains "writing tests BEFORE implementation" and "Stub Requirements"
- `TestGenerate_CoderImplPhase` — set `Task.Phase = "implementation"`, `AgentType = AgentCoder`, verify output contains "implementing code to pass pre-written tests"
- `TestGenerate_CoderDefaultPhase` — set `Task.Phase = ""`, `AgentType = AgentCoder`, verify output contains "Implement the described task"
- `TestGenerate_CoderIntegrationPhase` — set `Task.Phase = "integration"`, `AgentType = AgentCoder`, verify output uses default coder instructions (same as empty phase)

### 2. testPhaseCoderInstructions detail tests

- `TestGenerate_TestPhase_WithEstimatedFiles` — set `Task.Phase = "test"`, `Task.Context = map[string]any{"estimated_files": []any{"foo_test.go"}}`, verify output contains "foo_test.go"
- `TestGenerate_TestPhase_WithTestPlan` — set `Task.TestPlan = "Test X and Y"`, verify output contains "Test X and Y" under "## Test Plan"

### 3. implPhaseCoderInstructions detail tests

- `TestGenerate_ImplPhase_WithActualTestFiles` — set `Task.Phase = "implementation"`, `Task.Context = map[string]any{"actual_test_files": []any{"pkg/foo_test.go", "pkg/bar_test.go"}}`, verify output contains both file paths under "Pre-written tests exist at"
- `TestGenerate_ImplPhase_FallbackToEstimatedFiles` — set Context with only `estimated_files` (no `actual_test_files`), verify the estimated files appear
- `TestGenerate_ImplPhase_NoTestFiles` — empty Context, verify "implementing code to pass pre-written tests" appears but no file list
- `TestGenerate_ImplPhase_WithTestPlan` — set TestPlan, verify it appears

### 4. defaultCoderInstructions detail tests

- `TestGenerate_DefaultCoder_WithEstimatedFiles` — set `Task.Context = map[string]any{"estimated_files": []any{"main.go"}}`, verify "main.go" appears in output
- `TestGenerate_DefaultCoder_WithTestPlan` — set `Task.TestPlan = "Run go test"`, verify it appears

### 5. reviewerInstructions dispatch

- `TestGenerate_PlanReviewer` — set `AgentType = AgentReviewer`, `ReviewMode = "plan"`, `PlanJSON = "{...}"`, verify output contains "plan reviewer" and "Review Criteria" and the plan JSON
- `TestGenerate_FeatureReviewer` — set `AgentType = AgentReviewer`, `ReviewMode = "feature"`, `GitDiff = "+added line"`, verify output contains "feature reviewer" and diff content

### 6. planReviewerInstructions details

- `TestGenerate_PlanReviewer_ReviewCriteria` — verify output contains all 8 review criteria: "Coverage", "File overlap", "Integration", "Decomposition quality", "Dependency correctness", "TDD structure", "Test quality", "TDD exceptions"
- `TestGenerate_PlanReviewer_OutputSchema` — verify output contains JSON schema keys: "coverage", "tdd_assessment", "recommendation"
- `TestGenerate_PlanReviewer_NoPlanJSON` — omit PlanJSON, verify no "```json" block in plan section

### 7. featureReviewerInstructions details

- `TestGenerate_FeatureReviewer_DiffTruncation` — set GitDiff to a string >50000 chars, verify output contains "truncated"
- `TestGenerate_FeatureReviewer_NoDiff` — omit GitDiff, verify "## Changes" section is absent
- `TestGenerate_FeatureReviewer_OutputSchema` — verify output contains JSON schema keys: "test_results", "criteria_results", "recommendation"

### 8. Generate() conditional sections

- `TestGenerate_WithComments` — provide 2 TaskComments, verify both appear under "## User Feedback Comments"
- `TestGenerate_WithParentCtx` — provide ParentCtx map, verify values appear under "## Parent Task Context"
- `TestGenerate_WithPromptAdjustment` — set `Task.Context = map[string]any{"prompt_adjustment": "try a different approach"}`, verify it appears under "## Additional Guidance from Prior Attempt"
- `TestGenerate_WithTaskContext` — set Task.Context with a regular key like `{"retry_count": 2}`, verify it appears under "## Additional Context"
- `TestGenerate_ReviewerCompletion` — set AgentType to AgentReviewer, verify completion section says "review.json" not "commit"

## Conventions

- Package: `prompt`
- Use `strings.Contains()` for content assertions
- Use the existing `minimalOpts()` helper
- For each test, call `Generate(opts)` and check the returned string
- Build verification: `go test ./internal/prompt/ -cover`
- Target: 70%+ coverage on this package
