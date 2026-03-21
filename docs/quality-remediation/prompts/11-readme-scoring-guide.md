# Agent: Add Scoring Interpretation Guide to README

You are working on the `master` branch of drem-orchestrator, a Go-based task orchestrator.
Your task is to expand the "Step Scores" section in README.md with interpretation guidance.

## Context

Read these before starting:
- `README.md` (find the "Step Scores" subsection under Task Lifecycle — lines ~160-173)
- `internal/score/score.go` (the canonical scoring logic — understand how each dimension is calculated)
- `internal/orchestrator/score_bridge.go` (the bridge between orchestrator and score package)
- `internal/tui/score_display.go` (how scores are rendered in the TUI — badge format)

## Dependencies

This agent depends on Agent 08 (ARCHITECTURE.md update). If it hasn't landed, work independently.

## Deliverables

### Expand Step Scores section in `README.md`

#### 1. Add interpretation guidance after the existing score table

Keep the existing table (Plan Review vs Implementation Review columns). Add the following below it:

**Reading the badge**: Scores appear as compact badges in the task board: `T:85 C:100 D:42 Dp:67`
- `T` = TDD coverage
- `C` = Constitution compliance
- `D` = Documentation coverage
- `Dp` = Depth score

**Score ranges**:

| Range | Meaning | Action |
|-------|---------|--------|
| 80-100 | Strong | No action needed |
| 60-79 | Acceptable | Monitor — may improve as subtasks complete |
| 40-59 | Concerning | Review the plan/implementation for gaps |
| 0-39 | Weak | Address before approving |

**What to do when a score is low**:

- **Low TDD (T)**: At plan review — check that implementation subtasks have corresponding test subtasks. At testing_ready — check `go test -cover` output for the affected packages.
- **Low Constitution (C)**: Run `bash scripts/check_constitution.sh` to see which constraints are failing. Common causes: file too long, too many exports, formatting issues.
- **Low Documentation (D)**: Ensure at least one subtask touches documentation files (README, doc comments, guides). At testing_ready, check whether changed files include any `.md` updates.
- **Low Depth (Dp)**: At plan review — check that subtasks include `depth_meta` with module boundaries and interface shapes. Plans without `depth_meta` fall back to a file-coverage ratio which tends to score lower.

**Scores at different gates**:
- **Plan review**: Scores are predictive (based on plan structure, not actual code). TDD measures test subtask coverage. Depth evaluates module decomposition quality.
- **Testing ready**: Scores are measured (based on actual code). TDD uses real `go test -cover` output. Constitution runs actual constraint checks.

### Scope Limitation

- Only modify README.md
- Expand the existing "Step Scores" subsection — do not create a separate section
- Keep guidance actionable and concise — one sentence per score dimension
- Read `internal/score/score.go` to verify score calculation details are accurate

## Verification

```bash
# Badge format explained:
grep "T:.*C:.*D:.*Dp:" README.md

# Score ranges documented:
grep -c "80-100\|60-79\|40-59\|0-39" README.md

# README renders correctly
```
