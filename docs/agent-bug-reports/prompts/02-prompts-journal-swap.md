# Agent: Prompt Updates & Journal Replacement

You are working on the `worktree-agent-f29d44cf` branch of Drem Orchestrator, a Go-based agent orchestration system.
Your task is to add bug report filing instructions to all agent prompts and remove the supervisor journal system.

## Context

Read these specs before starting:
- `plans/agent-bug-reports-prd.md` (Filing Mechanism, Prompt Changes, Further Notes sections)
- `internal/prompt/prompt.go` (existing prompt generation — `Generate()`, all `*Instructions()` functions)
- `internal/prompt/prompt_review_fixer.go` (reviewer and fixer instruction functions)
- `internal/supervisor/journal.go` (journal code to remove)
- `internal/supervisor/supervisor_test.go` (journal tests to remove — search for `WriteJournalEntry`)
- `internal/orchestrator/orchestrator.go` (search for `logSupervisorAction` and `journalDir` — callers to update)

## Deliverables

### Modified files

#### 1. `internal/prompt/prompt.go`

Add a new `bugReportInstructions()` function that returns a `[]string` section with:

```
## Bug Report Filing

If you encounter any of the following during your work, file a bug report:
- Broken builds or failing dependencies
- Flaky or unexpectedly failing tests
- Unclear or contradictory requirements
- Constraint violations you cannot resolve
- Upstream code issues
- Environment problems

To file a bug report, write a JSON file to `.drem/bug-reports/<uuid>.json` with this schema:

{
  "title": "Short descriptive title",
  "description": "What went wrong — be specific",
  "category": "tooling|merge_conflict|requirements|constraint_violation|upstream_code|test_failure|environment|other",
  "severity": "blocking|degraded|informational",
  "reproduction_context": "File paths, commands run, error output — enough to reproduce",
  "agent_id": "<your agent ID from the metadata file>",
  "task_id": "<your current task ID>"
}

Severity guide:
- blocking: You cannot continue your work
- degraded: You worked around it but the problem remains
- informational: You observed it but it has no immediate impact

File the report and continue your work — do not stop to spawn a new agent.
Read your agent ID from `.claude/agent-metadata.json` in your worktree.
```

Call `bugReportInstructions()` in `Generate()` — insert it after the agent-type-specific instructions block (after the switch on `opts.AgentType`, before the "Prior Context" section). This ensures ALL agent types get the instructions.

#### 2. `internal/supervisor/journal.go`

Delete this file entirely.

#### 3. `internal/supervisor/supervisor_test.go`

Remove all test functions related to `WriteJournalEntry`:
- `TestWriteJournalEntry_Success`
- `TestWriteJournalEntry_EmptyAgentName`
- `TestWriteJournalEntry_SanitizesName`
- `TestWriteJournalEntry_CreatesDirectory`
- `TestWriteJournalEntry_ContentFormat`

Keep all other tests in the file intact.

#### 4. `internal/orchestrator/orchestrator.go`

- Remove the `journalDir()` method (around line 1149-1152)
- Remove the `logSupervisorAction()` method (around line 1155-1161)
- Find all call sites of `logSupervisorAction` in the file and remove those calls. The supervisor decisions themselves remain — only the journal-writing side effect is removed. Search for `o.logSupervisorAction(` to find all call sites.
- Remove the `supervisor.JournalEntry` usages — any local variables or struct literals of this type become dead code once the calls are removed.

### Tests

#### 5. `internal/prompt/prompt_test.go`

Add a test that verifies `Generate()` includes the bug report filing section for each agent type. Use table-driven tests:
- For each agent type (planner, coder, researcher, reviewer, fixer), generate a prompt and assert it contains "Bug Report Filing" and the JSON schema fields ("category", "severity", "reproduction_context")
- This ensures no agent type is accidentally excluded from bug report instructions

## Scope Limitation

Only modify files in `internal/prompt/`, `internal/supervisor/`, and `internal/orchestrator/orchestrator.go`. Do not create any new packages. Do not modify the bug report model or TUI code.

## Conventions

- Package: `prompt`, `supervisor`, `orchestrator`
- Go 1.22+, `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./internal/prompt/... ./internal/supervisor/... ./internal/orchestrator/...`
