# Agent: Update ARCHITECTURE.md to Reflect Current Codebase

You are working on the `master` branch of drem-orchestrator, a Go-based task orchestrator.
Your task is to update `ARCHITECTURE.md` so it accurately reflects the current state of the codebase.

## Context

Read these before starting:
- `ARCHITECTURE.md` (the file to update)
- `internal/model/enums.go` (authoritative list of task states, agent types)
- `cmd/drem/config.go` (authoritative list of config options and defaults)
- `internal/` directory listing (authoritative list of packages)
- `cmd/` directory listing (authoritative list of CLI tools)
- `.drem/constraints.toml` (constraint definitions — check if Graduation Path section is current)

## Dependencies

This agent depends on Agents 01-07 (constitution fixes and deep module narrowing).
If those changes haven't landed yet, document the packages as they currently exist — the narrowing only changes visibility, not package existence.

## Deliverables

### Update `ARCHITECTURE.md`

Make these specific additions/changes:

#### 1. Missing packages — add to the architecture diagram

The existing diagram lists packages under `internal/`. Add these missing entries in the same style:

- `agentmon/` — Agent transcript monitoring: tails Claude conversation JSONL, extracts test results, build errors, git operations, and context usage signals
- `clarification/` — Plan clarification loop: evaluates plan assumptions, generates clarification questions, processes user answers, produces replan context
- `constraints/` — Constitution constraint engine: loads `.drem/constraints.toml`, evaluates `command`, `max_lines`, `max_matches`, `no_match`, and `depth` rules
- `ctxmon/` — Context window monitoring: tracks agent token usage, triggers compaction and fixer escalation
- `score/` — Quality scoring: computes TDD, Constitution, Documentation, and Depth scores for plans and implementations
- `testutil/` — Shared test infrastructure: database factories, git repo setup, mock supervisor

#### 2. Missing cmd/ tools — add alongside `cmd/drem/`

- `cmd/check-constraints/` — CLI wrapper for running constitution constraint checks (`bash scripts/check_constitution.sh`)
- `cmd/ctxmon/` — CLI for setting up and querying context window monitoring in agent worktrees

#### 3. Missing task lifecycle states — update the lifecycle section

The current lifecycle diagram is missing 4 states. Update the ASCII diagram and descriptions to include:

- `needs_clarification` — between `planning` and `plan_review`: plan assumptions need human clarification before review
- `test_writing` — between `plan_review` and `in_progress`: TDD phase, test agent is writing tests
- `test_review` — between `test_writing` and `in_progress`: human gate to review written tests
- `rejected` — terminal state reachable from review gates

Read `internal/model/enums.go` for the authoritative list. Every status constant defined there must appear in the document.

#### 4. Missing config options

Read `cmd/drem/config.go` for the full list of config fields and their defaults. Add any options not currently in the Configuration table. Known missing:

- `context_warn_percent` — Context usage % that triggers a warning
- `context_stop_percent` — Context usage % that triggers a hard stop
- `context_fixer_percent` — Context usage % that triggers fixer escalation
- `test_command` — Build/test command for the project
- `compile_command` — Compile command for the project
- `scoped_tests` — Whether to run tests scoped to subtask file changes
- `test_timeout` — Timeout for test command execution

Use the defaults from config.go. Verify against the actual struct — there may be additional options.

#### 5. Update Graduation Path if needed

Check whether the Graduation Path section accurately describes the current constraint system. The constraints package now supports `depth` constraints in addition to `command`, `max_lines`, `max_matches`, and `no_match`. Update if needed.

### Scope Limitation

- Only modify `ARCHITECTURE.md`
- Keep the existing style and structure — add new entries alongside existing ones
- Do NOT remove existing content unless it is factually wrong
- Do NOT add sections that duplicate README.md content

## Verification

```bash
# Verify all internal packages are listed:
ls internal/ | while read pkg; do grep -q "$pkg" ARCHITECTURE.md || echo "MISSING: $pkg"; done

# Verify all cmd tools are listed:
ls cmd/ | while read tool; do grep -q "$tool" ARCHITECTURE.md || echo "MISSING: $tool"; done

# Verify all task states are listed (check against enums.go):
grep 'Status[A-Z]' internal/model/enums.go | sed 's/.*= "//' | sed 's/"//' | while read state; do
  grep -q "$state" ARCHITECTURE.md || echo "MISSING STATE: $state"
done
```
