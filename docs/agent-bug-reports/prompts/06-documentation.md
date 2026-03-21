# Agent: Bug Reports Documentation

You are working on the `worktree-agent-f29d44cf` branch of Drem Orchestrator, a Go-based agent orchestration system.
Your task is to write user-facing documentation for the bug reports feature: a README section and a walkthrough guide.

## Context

Read these specs before starting:
- `plans/agent-bug-reports-prd.md` (full PRD — user stories, implementation decisions, state machine, TUI screen, filing mechanism)
- `README.md` (existing README — understand the current structure, section ordering, and writing style)
- `internal/model/bugreport.go` (actual model fields)
- `internal/model/bugreport_enums.go` (actual enum values)
- `internal/bugreport/service.go` (actual service API)
- `internal/tui/bugreports.go` (actual TUI implementation — keybindings, column layout)
- `internal/prompt/prompt.go` (search for `bugReportInstructions` to see the agent filing instructions)

## Dependencies

This agent depends on all prior agents (01–05). Read the implemented code to document what was actually built, not just what the PRD specified. If the implementation diverges from the PRD, document the implementation.

## Deliverables

### Modified files

#### 1. `README.md`

Add a **Bug Reports** section to the README. Place it after the existing TUI/Dashboard section (or after the most relevant existing section). Include:

**Overview** (2-3 sentences): What bug reports are, why they exist, how agents file them.

**Filing Bug Reports** (for agent operators):
- The JSON schema with all fields explained
- File path convention: `.drem/bug-reports/<uuid>.json`
- Category and severity value descriptions (one line each)
- Example JSON file

**TUI Bug Report Screen**:
- How to access it (`b` from dashboard)
- Column descriptions
- Keybindings table:
  | Key | Action |
  |-----|--------|
  | `b` | Toggle bug report screen |
  | `j/k` | Navigate |
  | `a` | Acknowledge |
  | `p` | Promote to task |
  | `D` | Dismiss |
  | `x` | Delete (with confirmation) |
  | `c` | Add comment |
  | `/` | Filter |
  | `Esc` | Return to dashboard |

**Bug Report Lifecycle**:
- State diagram: open → acknowledged → promoted/dismissed
- What each status means
- How promotion works (opens $EDITOR, creates task in backlog)

**Filtering**:
- Available filter dimensions (category, severity, status, project)
- How to toggle dismissed reports visibility

Keep the writing style consistent with the existing README — concise, technical, no marketing language. Use code blocks for JSON examples and keybinding references.

### New files

#### 2. `docs/agent-bug-reports/walkthrough.md`

A step-by-step walkthrough showing the feature end-to-end:

1. **Agent files a bug report**: Show the JSON file an agent would write, explain the fields
2. **Orchestrator ingests it**: Explain the tick-based pickup (every 5s), validation, DB insertion, file cleanup
3. **Operator sees it in TUI**: Describe the bug report screen, what the list looks like
4. **Operator triages**: Acknowledge → add comment → promote to task
5. **Promotion workflow**: $EDITOR opens, pre-populated content, save creates a task, bug report linked to task

Include the state machine diagram:
```
open ──→ acknowledged ──→ promoted
  │          │
  │          └──→ dismissed
  │
  ├──→ promoted
  └──→ dismissed
```

## Scope Limitation

Only modify `README.md` and create `docs/agent-bug-reports/walkthrough.md`. Do not modify any Go code.

## Conventions

- Markdown formatting consistent with existing README
- No emojis
- Code blocks for JSON, file paths, and keybindings
- Concise technical writing
- Build verification: N/A (documentation only) — but verify all referenced keybindings and field names match the actual implementation by reading the source files listed in Context
