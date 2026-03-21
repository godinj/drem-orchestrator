# Agent: Improve TUI Package Test Coverage

You are working on the `master` branch of drem-orchestrator, a Go-based task orchestrator.
Your task is to increase test coverage of `internal/tui/` from 38.6% to at least 55%.

## Context

Read these before starting:
- `internal/tui/` — all `.go` files (both source and existing tests)
- `internal/testutil/testutil.go` (shared test helpers — use `testutil.NewTestDB`)
- `README.md` (TUI keybindings section — understand what features exist)

The TUI uses Bubble Tea (`bubbletea` framework). Do NOT test rendering output (`View()` methods). Instead, test the **logic** that drives the UI.

## Deliverables

### Add tests for TUI logic (non-rendering)

Identify untested functions first:
```bash
go test -coverprofile=coverage.out ./internal/tui/
go tool cover -func=coverage.out | grep -v '100.0%' | sort -t: -k3 -n
```

Focus on these categories:

#### 1. Key handler dispatch

Test that key events produce the expected model state changes. For each keybinding in the README:
- Create a model in the appropriate state
- Send a `tea.KeyMsg` through `Update()`
- Assert the resulting model state (not the view)

Example pattern:
```go
func TestKeyHandler_ApprovePlan(t *testing.T) {
    m := newTestModel(t)  // set up with a task in plan_review
    msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
    updated, cmd := m.Update(msg)
    // Assert task transitioned or appropriate command returned
}
```

#### 2. Score badge formatting

Test `internal/tui/score_display.go` functions:
- Zero scores, partial scores, full scores
- Edge cases (negative, > 100)
- Badge string format matches expected pattern

#### 3. Filter logic

Test task and agent filtering:
- Filter by status
- Toggle archived agents
- Filter combinations

#### 4. State transitions

Test panel switching, mode changes, expand/collapse:
- Tab cycles through panels
- Enter expands/collapses tree nodes
- Mode transitions (normal → create → feedback → normal)

### Conventions

- Use `testutil.NewTestDB(t)` for any test that needs a database
- Use table-driven tests for parameterized cases
- Use `t.Helper()` in test helper functions
- Name test files to match source files: `score_display_test.go`, `keyhandlers_test.go`, etc.
- Do NOT import `bubbletea` testing utilities that don't exist — use `Update()` directly

### Scope Limitation

- Only add/modify test files in `internal/tui/`
- Do NOT modify source files
- Do NOT test `View()` output or visual rendering
- Do NOT test Bubble Tea framework internals

## Verification

```bash
# Coverage must be >= 55%:
go test -cover ./internal/tui/...

# All tests must pass:
go test ./internal/tui/...

# No regressions:
go test ./...
```
