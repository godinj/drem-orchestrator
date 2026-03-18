# Depth Enforcement — Agent Prompts

Agent prompts for implementing module depth enforcement through the orchestration pipeline, per [depth-enforcement-prd.md](../depth-enforcement-prd.md).

## Prompt Index

| # | Name | Tier | Depends On | Creates | Modifies |
|---|------|------|------------|---------|----------|
| 01 | depth-analysis-engine | 1 | — | `internal/constraints/depth/depth.go`, `growth.go`, `depth_test.go` | — |
| 02 | model-depth-metadata | 1 | — | — | `internal/model/models.go`, `models_test.go` |
| 03 | constraints-depth-integration | 2 | 01 | — | `internal/constraints/config.go`, `evaluate.go`, + test file, `.drem/constraints.toml` |
| 04 | plan-depth-scoring | 2 | 02 | — | `internal/score/score.go`, `score_test.go` |
| 05 | prompt-depth-guidance | 2 | 02 | — | `internal/prompt/prompt.go`, + test file |
| 06 | supervisor-depth-roles | 3 | 01, 04 | — | `internal/supervisor/types.go`, `prompts.go`, + test file |
| 07 | orchestrator-depth-wiring | 4 | 03, 04, 05, 06 | `internal/orchestrator/depth_gate.go` (optional) | `internal/orchestrator/orchestrator.go`, `score_bridge.go`, + test file |

## Dependency Graph

```
01 (depth engine) ──┬──→ 03 (constraints) ──┬──→ 06 (supervisor) ──→ 07 (orchestrator)
                    │                        │
02 (model) ────────┬┼──→ 04 (score) ────────┘
                   │└──→ 05 (prompt) ─────────────→ 07
                   └───→ 05
```

## Execution Order

### Tier 1 (parallel)

```bash
claude --agent docs/depth-enforcement/prompts/01-depth-analysis-engine.md &
claude --agent docs/depth-enforcement/prompts/02-model-depth-metadata.md &
wait
```

### Tier 2 (parallel, after Tier 1 merges)

```bash
claude --agent docs/depth-enforcement/prompts/03-constraints-depth-integration.md &
claude --agent docs/depth-enforcement/prompts/04-plan-depth-scoring.md &
claude --agent docs/depth-enforcement/prompts/05-prompt-depth-guidance.md &
wait
```

### Tier 3 (after 03, 04 merge)

```bash
claude --agent docs/depth-enforcement/prompts/06-supervisor-depth-roles.md
```

### Tier 4 (after all merge)

```bash
claude --agent docs/depth-enforcement/prompts/07-orchestrator-depth-wiring.md
```

## Build Verification

After all agents complete and branches merge:

```bash
go build ./...
go test ./...
```
