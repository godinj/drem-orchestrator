# Agent: Supervisor Package Test Coverage

You are working on the `master` branch of Drem Orchestrator, a Go multi-agent development orchestrator.
Your task is pre-refactor test hardening: raise `internal/supervisor/` coverage from 37% to 70%+.

## Context

Read these before starting:
- `internal/supervisor/supervisor.go` (Supervisor struct, Evaluate, EvaluateJSON, extractJSON, truncateForPrompt)
- `internal/supervisor/journal.go` (WriteJournalEntry, JournalEntry struct)
- `internal/supervisor/prompts.go` (FailureDiagnosisPrompt, MergeConflictPrompt, BuildFailurePrompt, OnDemandPrompt, slugify, JournalFilename)
- `internal/supervisor/supervisor_test.go` (existing tests — extractJSON and truncateForPrompt are fully covered)

## Deliverables

Add all new tests to `internal/supervisor/supervisor_test.go`. Do NOT create new test files.

### 1. New() constructor

- `TestNew` — verify `New("claude", 30*time.Second)` returns non-nil Supervisor

### 2. Evaluate() — requires exec mock

Create a test helper that builds a fake "claude" script:

```go
func writeFakeClaudeBin(t *testing.T, dir string, stdout string, exitCode int) string {
    t.Helper()
    bin := filepath.Join(dir, "fake-claude")
    script := fmt.Sprintf("#!/bin/sh\necho '%s'\nexit %d\n", stdout, exitCode)
    os.WriteFile(bin, []byte(script), 0o755)
    return bin
}
```

Then test:
- `TestEvaluate_Success` — fake script outputs "hello", verify Evaluate returns "hello"
- `TestEvaluate_NonZeroExit` — fake script exits 1, verify error contains "supervisor evaluate"
- `TestEvaluate_Timeout` — create Supervisor with 100ms timeout, fake script sleeps 2 seconds, verify error contains "timeout"

### 3. EvaluateJSON()

- `TestEvaluateJSON_Success` — fake script outputs `{"key":"val"}`, verify unmarshal into map works
- `TestEvaluateJSON_NoJSON` — fake script outputs "no json here", verify error contains "no JSON found"
- `TestEvaluateJSON_InvalidJSON` — fake script outputs `{"broken":}`, verify error contains "unmarshal"
- `TestEvaluateJSON_WrappedJSON` — fake script outputs "Here is the result: {\"key\":\"val\"} done", verify extraction works

### 4. WriteJournalEntry()

- `TestWriteJournalEntry_Success` — use `t.TempDir()`, write entry, verify file exists with expected content
- `TestWriteJournalEntry_EmptyAgentName` — verify "unknown" is used when AgentName is ""
- `TestWriteJournalEntry_SanitizesName` — entry with AgentName "foo/bar baz", verify filename uses "foo-bar-baz"
- `TestWriteJournalEntry_CreatesDirectory` — write to non-existent subdir, verify it's created
- `TestWriteJournalEntry_ContentFormat` — verify markdown format: heading, agent, task, summary, details, outcome

### 5. Prompt generation functions

These are pure string builders. Test by verifying key interpolation points:

- `TestFailureDiagnosisPrompt` — call with all args, verify output contains: task title, agent type, "root_cause", "should_retry" (JSON schema), and that long inputs are truncated
- `TestMergeConflictPrompt` — call with 2 conflicts, verify output contains: source branch, target branch, conflict filenames, "resolution_strategy" JSON schema
- `TestBuildFailurePrompt` — call with 3 changed files, verify output contains: worktree path, all file names, "can_auto_fix" JSON schema

### 6. OnDemandPrompt()

- `TestOnDemandPrompt_BasicFields` — verify output contains: TaskID, TaskTitle, Status, Branch, DefaultBranch, BareRepoPath
- `TestOnDemandPrompt_WithSubtasks` — provide 2 SubtaskInfos, verify both appear in output with status and title
- `TestOnDemandPrompt_NoSubtasks` — provide empty Subtasks, verify "## Subtasks" heading does NOT appear
- `TestOnDemandPrompt_DatabaseSection` — verify output contains DBPath and SQL examples

### 7. slugify and JournalFilename

- `TestSlugify` — table-driven: "Hello World" → "hello-world", "Foo & Bar!" → "foo-bar", "" → "unknown", "---" → "unknown"
- `TestJournalFilename` — verify format starts with "supervisor-journal-" and ends with ".md", contains slugified title

## Conventions

- Package: `supervisor`
- Table-driven tests with `t.Run()`
- Use `t.TempDir()` for filesystem tests
- Use `strings.Contains()` for prompt content assertions
- Build verification: `go test ./internal/supervisor/ -cover`
- Target: 70%+ coverage on this package
