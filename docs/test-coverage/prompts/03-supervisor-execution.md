# Agent: Supervisor Execution Tests

You are working on the `master` branch of drem-orchestrator, a Go-based agent orchestration system using GORM+SQLite, tmux, and git worktrees.
Your task is writing tests for `Evaluate()` and `EvaluateJSON()` in `internal/supervisor/supervisor.go`, plus journal writing. Currently only `extractJSON` and `truncateForPrompt` are tested (37.3% coverage).

## Context

Read these before starting:
- `CLAUDE.md` (project conventions, build/test commands)
- `internal/supervisor/supervisor.go` (Evaluate at line 34, EvaluateJSON at line 57, extractJSON at line 77, truncateForPrompt at line 139)
- `internal/supervisor/types.go` (FailureDiagnosis, FeedbackIntegration, MergeConflictAnalysis, BuildFailureDiagnosis structs)
- `internal/supervisor/journal.go` (JournalEntry struct, WriteJournalEntry function)
- `internal/supervisor/prompts.go` (FailureDiagnosisPrompt, MergeConflictPrompt, BuildFailurePrompt, OnDemandPrompt)
- `internal/supervisor/supervisor_test.go` (existing tests for extractJSON and truncateForPrompt — do not duplicate)

## Deliverables

### New file: `internal/supervisor/evaluate_test.go`

Write tests in the `supervisor` package (white-box tests).

#### 1. TestEvaluate_Success

Tests `Evaluate(ctx, prompt)` (line 34) with a mock claude binary. Verify it:
- Invokes the claude binary with `-p --dangerously-skip-permissions` flags
- Passes the prompt via stdin
- Returns the trimmed stdout response
- Returns no error on success

**Mock claude binary approach**: Create a small shell script in `t.TempDir()` that reads stdin, writes a canned response to stdout, and exits 0. Point `Supervisor.claudeBin` at this script. Example:

```bash
#!/bin/sh
cat  # consume stdin
echo "This is the supervisor response"
```

Make the script executable with `os.Chmod(path, 0755)`.

#### 2. TestEvaluate_Failure

Tests `Evaluate()` when the claude binary exits non-zero. Verify it:
- Returns an error wrapping stderr content
- Error message includes context about the failure

Mock: Script that writes to stderr and exits 1.

#### 3. TestEvaluate_Timeout

Tests `Evaluate()` with a context deadline. Verify it:
- Respects context cancellation/timeout
- Returns an error when the command exceeds the timeout
- Cleans up the process

Mock: Script that sleeps longer than the timeout. Use `context.WithTimeout(ctx, 100*time.Millisecond)`.

#### 4. TestEvaluate_StdinPassthrough

Tests that the prompt is correctly piped to the claude binary's stdin. Verify it:
- The full prompt text arrives on stdin

Mock: Script that writes stdin content to a temp file. After Evaluate returns, read the file and assert it matches the prompt.

#### 5. TestEvaluateJSON_Success

Tests `EvaluateJSON(ctx, prompt, target)` (line 57). Verify it:
- Calls Evaluate, extracts JSON from response, unmarshals into target
- Works with `FailureDiagnosis` struct
- Works with `MergeConflictAnalysis` struct
- Works with `BuildFailureDiagnosis` struct

Mock: Script that outputs JSON wrapped in explanatory text (as claude would):
```
Based on my analysis, here is the diagnosis:
{"root_cause": "missing import", "category": "code_error", "should_retry": true, "retry_strategy": "modified_prompt", "prompt_adjustment": "add import", "max_additional_retries": 2}
```

#### 6. TestEvaluateJSON_InvalidJSON

Tests `EvaluateJSON()` when the response contains no valid JSON. Verify it:
- Returns a descriptive error
- Error includes the raw response for debugging

Mock: Script that outputs plain text with no JSON.

#### 7. TestEvaluateJSON_MismatchedFields

Tests `EvaluateJSON()` when JSON is valid but fields don't match the target struct. Verify it:
- Returns partially populated struct (Go's json.Unmarshal behavior)
- Does not error on extra/missing fields

#### 8. TestWriteJournalEntry

Tests `WriteJournalEntry(dir, entry)` (journal.go line 26). Verify it:
- Creates the journal directory if it doesn't exist
- Writes a markdown file with correct filename format: `{sanitized-name}-{timestamp}.md`
- File contains all entry fields formatted as markdown
- Sanitizes agent name (replaces "/" and " " with "-")
- Multiple entries don't overwrite each other

Setup: Use `t.TempDir()` for journal directory. Create JournalEntry with known fields. Write it. Read the file back and verify contents.

#### 9. TestPromptGeneration

Tests prompt generation functions. Verify they:
- `FailureDiagnosisPrompt`: includes task title, truncated description (1000 chars), agent type, truncated output (3000 chars), truncated error (500 chars)
- `MergeConflictPrompt`: includes branch names, conflict list, truncated diff (4000 chars)
- `BuildFailurePrompt`: includes worktree path, build output, changed files list
- All prompts request JSON output with the expected keys
- Truncation actually works (pass strings longer than limits, verify output is shorter)

These are pure function tests — no mocking needed.

#### 10. TestOnDemandPrompt

Tests `OnDemandPrompt(opts)` (prompts.go line 95). Verify it:
- Includes task context (title, description, ID, status, branch)
- Includes subtask information when provided
- Includes DB path and bare repo path
- Includes journal directory
- Output is a well-formed string (not empty, contains expected sections)

## Test Infrastructure Notes

- Create mock claude scripts in `t.TempDir()` — simple shell scripts that simulate claude behavior
- Use `os.Chmod(script, 0755)` to make scripts executable
- Use `os.WriteFile` to create scripts
- For timeout tests, use short timeouts (100-500ms) with scripts that `sleep 10`
- For journal tests, use `t.TempDir()` and verify file contents with `os.ReadFile`
- Create `Supervisor` directly: `sup := New("/path/to/mock-claude", 30*time.Second)`

## Conventions

- Package: `supervisor` (same package, white-box tests)
- Table-driven tests with `t.Run` subtests
- Error wrapping with `fmt.Errorf("context: %w", err)`
- `gofmt` formatting
- Build verification: `go test ./internal/supervisor/ -run TestEvaluate -v && go test ./internal/supervisor/ -run TestWriteJournal -v && go test ./internal/supervisor/ -run TestPrompt -v`
- Final verification: `go test ./...` (all tests must pass)
