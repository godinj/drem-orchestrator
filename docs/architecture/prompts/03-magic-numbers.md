# Agent: Extract Magic Numbers to Named Constants

You are working on the `master` branch of Drem Orchestrator, a Go-based agent orchestration system.
Your task is to replace bare numeric literals in business logic with named constants.

## Context

Read these before starting:
- `ARCHITECTURE.md` (Constants & Magic Numbers section)
- Each file listed below — read it to understand the context of each literal before replacing it

## Deliverables

For each file, add a `const` block (or extend the existing one) at the top of the file with descriptive names, then replace the bare literals. Include a short comment on each constant explaining the choice.

### 1. `internal/orchestrator/orchestrator.go`

| Line(s) | Literal | Replacement |
|---------|---------|-------------|
| 65, 156, 4302, 4343 | `5 * time.Minute` | `defaultTestTimeout = 5 * time.Minute` |
| 128 | `85` | `defaultContextFixerPct = 85` |
| 3736 | `80` | `fixerEscalationPct = 80` |
| 4306 | `3` | `maxMergeRetries = 3` |
| 2882, 3029 | `3` (rejection/feedback limits) | `maxRejectionRounds = 3` |
| 3408, various | `[:4]` (short ID) | `shortIDLen = 4` — use `id.String()[:shortIDLen]` |

Read the surrounding code carefully before replacing. Some `3`s may be different constants (rejection limit vs merge retries vs feedback rounds). Name them distinctly if they serve different purposes.

### 2. `internal/agent/runner.go`

| Line | Literal | Replacement |
|------|---------|-------------|
| 31 | `500` | `maxCaptureLines = 500` (already exists — verify) |
| 138, 142, 146, 148 | `[:4]`, `30` | `shortIDLen = 4`, `maxDisplayNameLen = 30` |
| 305 | `5` | `idleNotifyTimeout = 5 * time.Second` |
| 364 | `10 * time.Second` | `spawnVerifyDelay = 10 * time.Second` |
| 576 | `5 * time.Second` | `contextMonitorInterval = 5 * time.Second` |

### 3. `internal/memory/memory.go`

| Line | Literal | Replacement |
|------|---------|-------------|
| 50, 79 | `50` | `defaultMemoryLimit = 50` |
| 200 | `8000` | `defaultMaxTokens = 8000` |
| 224 | `20` | `recentMemoryLimit = 20` |
| 248 | `10` | `projectDecisionLimit = 10` |

### 4. `internal/merge/merge.go`

| Line | Literal | Replacement |
|------|---------|-------------|
| 220 | `2 * time.Second` | `mergeRetryBackoff = 2 * time.Second` |
| 394 | `5 * time.Minute` | `buildVerifyTimeout = 5 * time.Minute` |

Note: `maxMergeRetries = 3` at line 24 is already a named constant — leave it alone.

### 5. `internal/tmux/tmux.go`

| Line | Literal | Replacement |
|------|---------|-------------|
| 233, 351, 437, 453 | `500 * time.Millisecond` | `paneCheckInterval = 500 * time.Millisecond` |
| 285 | `50000` | `historyLimit = 50000` |

## Scope Limitation

- Only extract constants. Do NOT refactor, rename, or restructure any logic.
- If a constant already exists with the right value, reuse it — do not create duplicates.
- Keep constants private (lowercase) unless they're already exported.
- Place constants at the top of the file in a `const` block, near any existing constants.

## Verification

```bash
# All tests must pass — this is a pure refactor
go test ./...
```

## Conventions

- Constants use camelCase (unexported): `maxMergeRetries`, `defaultTestTimeout`
- Add a brief comment on each constant: `// maxMergeRetries is the number of merge attempts before failing the task.`
- Build verification: `go test ./...`
