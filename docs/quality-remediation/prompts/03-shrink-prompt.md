# Agent: Shrink prompt.go Below 800 Lines

You are working on the `master` branch of drem-orchestrator, a Go-based task orchestrator.
Your task is to extract functions from `prompt.go` (803 lines) to bring it under the 800-line ceiling.

## Context

Read these before starting:
- `ARCHITECTURE.md` (File length ceiling rule — 800 lines max)
- `internal/prompt/prompt.go` (the file to shrink — note the function layout)
- `internal/prompt/prompt_helpers.go` (existing extraction — helpers already live here)

The file contains agent-type-specific instruction generators:
- `plannerInstructions()` at line 192
- `coderInstructions()` at line 410
- `testPhaseCoderInstructions()` at line 423
- `implPhaseCoderInstructions()` at line 488
- `defaultCoderInstructions()` at line 562
- `researcherInstructions()` at line 605
- `reviewerInstructions()` at line 624
- `planReviewerInstructions()` at line 632
- `featureReviewerInstructions()` at line 692
- `fixerInstructions()` at line 754
- `defaultInstructions()` at line 796

## Deliverables

### Extract reviewer/fixer instructions

#### 1. New file: `internal/prompt/prompt_review_fixer.go`

Extract the tail-end instruction functions into a new file. A natural seam is the reviewer + fixer block (lines 624-803, ~180 lines):
- `reviewerInstructions(opts Opts) []string`
- `planReviewerInstructions(opts Opts) []string`
- `featureReviewerInstructions(opts Opts) []string`
- `fixerInstructions(opts Opts) []string`
- `defaultInstructions() []string`

The new file must:
- Be in `package prompt`
- Contain only the extracted functions, verbatim
- Include necessary imports (likely just `fmt` and `strings`)
- NOT add any new exported symbols

After extraction, `prompt.go` should be ~623 lines (803 - 180).

### Scope Limitation

- Move functions verbatim — do NOT refactor
- Do NOT rename anything
- Do NOT change any logic
- Only touch `prompt.go` (removing functions) and the new file (adding them)

## Verification

```bash
# prompt.go must be <= 800 lines:
wc -l internal/prompt/prompt.go

# New file must be under 800 lines:
wc -l internal/prompt/prompt_review_fixer.go

# Constitution check must pass:
bash scripts/check_constitution.sh

# All tests must pass:
go test ./internal/prompt/...
```
