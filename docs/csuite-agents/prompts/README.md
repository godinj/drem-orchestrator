# C-Suite Agent Prompts

Implementation prompts for the C-Suite agent team. Each prompt is a self-contained task for a Claude Code agent.

## Prompt Index

| # | Name | Tier | Dependencies | Creates | Modifies |
|---|------|------|-------------|---------|----------|
| 01 | [Headless CLI](01-headless-cli.md) | 1 | — | `internal/cli/cli.go`, `internal/cli/cli_test.go`, `cmd/drem/cli_cmd.go` | `cmd/drem/main.go` |
| 02 | [Disk Protocol & Bootstrap](02-disk-protocol.md) | 1 | — | `scripts/csuite-bootstrap.sh`, `scripts/csuite-proto.sh`, `scripts/csuite-proto_test.sh` | — |
| 03 | [Seth (CTO) Prompt](03-seth-cto-prompt.md) | 1 | — | `docs/csuite-agents/prompts/seth.md` | — |
| 04 | [Ross (HR) Prompt](04-ross-hr-prompt.md) | 1 | — | `docs/csuite-agents/prompts/ross.md` | — |
| 05 | [Alex (CPO) Prompt](05-alex-cpo-prompt.md) | 1 | 01 (fallback if missing) | `docs/csuite-agents/prompts/alex.md` | — |
| 06 | [Mike (COO) + Temp Worker](06-mike-coo-prompt.md) | 2 | 01, 02 | `docs/csuite-agents/prompts/mike.md`, `docs/csuite-agents/prompts/temp-worker.md` | — |
| 07 | [Kyle (CEO) + Launch Scripts](07-kyle-ceo-prompt.md) | 2 | 02, 03, 04, 05, 06 | `docs/csuite-agents/prompts/kyle.md`, `scripts/csuite-launch.sh` | — |

## Execution Order

### Tier 1 (parallel — no dependencies)

```bash
claude --agent docs/csuite-agents/prompts/01-headless-cli.md &
claude --agent docs/csuite-agents/prompts/02-disk-protocol.md &
claude --agent docs/csuite-agents/prompts/03-seth-cto-prompt.md &
claude --agent docs/csuite-agents/prompts/04-ross-hr-prompt.md &
claude --agent docs/csuite-agents/prompts/05-alex-cpo-prompt.md &
wait
```

### Tier 2 (after Tier 1 merges)

```bash
claude --agent docs/csuite-agents/prompts/06-mike-coo-prompt.md &
claude --agent docs/csuite-agents/prompts/07-kyle-ceo-prompt.md &
wait
```

## Dependency Graph

```
Tier 1 (parallel):
  01-headless-cli ─────────────────┐
  02-disk-protocol ────────────────┤
  03-seth-cto-prompt ──────────────┤
  04-ross-hr-prompt ───────────────┤
  05-alex-cpo-prompt ──────────────┤
                                   ▼
Tier 2 (parallel):
  06-mike-coo-temp-worker (01,02)──┐
  07-kyle-ceo-launch (02-06) ──────┘
```

## Notes

- All Tier 1 agents include fallback instructions for when their dependencies don't exist yet (e.g., `drem cli` → sqlite3 fallback). This means Tier 2 is a soft dependency — the prompts will work even if executed before Tier 1 merges, but the output will be more accurate after.
- Agent prompts 03-07 produce markdown files, not code. They do not need compilation or tests — verify by reading.
- The headless CLI (01) and disk protocol (02) produce testable code/scripts. Run their tests before proceeding to Tier 2.
