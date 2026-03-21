# Agent: Add Constraints Configuration Guide to README

You are working on the `master` branch of drem-orchestrator, a Go-based task orchestrator.
Your task is to add a user-facing "Quality Constraints" section to README.md explaining how to configure and use the constraint system.

## Context

Read these before starting:
- `README.md` (the file to update — find the "Running the Constitution Check" troubleshooting section)
- `.drem/constraints.toml` (the actual constraint definitions — use as examples)
- `docs/constraints-system/design.md` (technical design — extract user-relevant details)
- `internal/constraints/constraints.go` or similar (understand constraint types and evaluation)
- `ARCHITECTURE.md` (Graduation Path section — explains enforcement tiers)

## Dependencies

This agent depends on Agent 08 (ARCHITECTURE.md update). If it hasn't landed, work independently.

## Deliverables

### Add Quality Constraints section to `README.md`

#### 1. New section: `## Quality Constraints`

Insert after the "Supervisor" section (before "Merge Conflict Prevention" or similar).

Content must cover:

1. **What constraints are**: Automated quality checks that enforce your project's constitution. Defined in `.drem/constraints.toml`. The orchestrator evaluates them at three gates: plan validation, post-agent merge, and integration (before `testing_ready`).

2. **Where they are defined**: `.drem/constraints.toml` in the bare repo root. The file uses TOML array-of-tables syntax.

3. **Constraint types** — one example of each from the actual `.drem/constraints.toml`:

   - **`[[command]]`** — Run a shell command; pass if exit code is 0 (or output is empty for `expect = "empty_output"`):
     ```toml
     [[command]]
     name   = "gofmt compliance"
     run    = "gofmt -l ./internal/ ./cmd/"
     expect = "empty_output"
     ```

   - **`[[max_lines]]`** — File length ceiling with optional per-file exceptions:
     ```toml
     [[max_lines]]
     name    = "File length ceiling"
     glob    = "internal/**/*.go"
     exclude = ["*_test.go"]
     limit   = 800

     [[max_lines.exception]]
     path           = "internal/orchestrator/orchestrator.go"
     rule           = "shrink-only"
     baseline_lines = 2250
     ```

   - **`[[max_matches]]`** — Count regex matches per file or directory:
     ```toml
     [[max_matches]]
     name    = "Exported function count"
     glob    = "internal/**/*.go"
     exclude = ["*_test.go"]
     pattern = "^func [A-Z]"
     limit   = 20
     scope   = "file"
     ```

   - **`[[no_match]]`** — Forbidden patterns (must match zero times):
     ```toml
     [[no_match]]
     name         = "DB init outside testutil"
     glob         = "internal/**/*_test.go"
     exclude_path = ["internal/testutil/", "internal/model/"]
     pattern      = "gorm\\.Open\\(sqlite"
     ```

   - **`[[depth]]`** — Module depth enforcement (export ratio and pass-through limits):
     ```toml
     [[depth]]
     name              = "Export ratio ceiling"
     glob              = "internal/**/*.go"
     exclude           = ["*_test.go"]
     max_export_ratio  = 0.15
     max_pass_throughs = 3
     ```

4. **Running checks manually**:
   ```bash
   bash scripts/check_constitution.sh
   ```

5. **Automatic enforcement**: Constraints are checked at three gates:
   - **Plan validation** — warns when plans target constrained files
   - **Post-agent gate** — checks file-based constraints after each agent merges
   - **Integration gate** — runs all constraints before a task can reach `testing_ready`

6. **Adding exceptions**: Use `[[<type>.exception]]` sub-tables. Two rules: `shrink-only` (must not exceed a baseline) and `grandfathered` (exempt from the rule entirely).

#### 2. Replace the troubleshooting subsection

The existing "Running the Constitution Check" subsection under Troubleshooting can be shortened to a cross-reference: "See [Quality Constraints](#quality-constraints) for details."

### Scope Limitation

- Only modify `README.md`
- Use real examples from `.drem/constraints.toml` — do not invent hypothetical ones
- Write for operators who need to add/modify constraints, not for constraint engine developers

## Verification

```bash
# Section exists:
grep -n "Quality Constraints" README.md

# All constraint types mentioned:
grep -c "command\|max_lines\|max_matches\|no_match\|depth" README.md

# README renders correctly (no broken markdown)
```
