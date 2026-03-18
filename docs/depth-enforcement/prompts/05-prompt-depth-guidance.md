# Agent: Prompt Depth Guidance

You are working on the `master` branch of drem-orchestrator, a Go-based multi-agent orchestrator with TDD enforcement and constitution-based quality constraints.
Your task is to update planner and coder agent prompts with depth guidance, so that agents design and implement deep modules from the start.

## Context

Read these specs before starting:
- `docs/depth-enforcement-prd.md` (Prompt-level enforcement, User stories 6-7)
- `internal/prompt/prompt.go` (Generate function, Opts struct, plannerInstructions, testPhaseCoderInstructions, implPhaseCoderInstructions, defaultCoderInstructions)
- `internal/model/models.go` (SubtaskPlan with ModuleBoundary and InterfaceShape — added by Agent 02)

## Dependencies

This agent depends on Agent 02 (model-depth-metadata). The model types `ModuleBoundary` and `InterfaceShape` on `SubtaskPlan` are used to extract plan-derived depth guidance for coder prompts.

## Deliverables

### Migration (internal/prompt/)

#### 1. prompt.go — Planner instructions

Modify `plannerInstructions()` to add depth guidance. Insert a new section after the existing decomposition rules. The tone should be heavy-handed and prescriptive (per PRD: "Heavy-handed prescriptive style"):

Add this section to the planner instructions output (exact wording):

```
## Module Depth Requirements

You MUST design for depth. Every subtask that creates or modifies a Go package MUST specify:

1. **Module boundaries** in the subtask's `module_boundaries` field:
   - `package`: the Go package path (e.g., "internal/constraints/depth")
   - `description`: what this module encapsulates (one sentence)
   - `exports`: the expected number of exported symbols (aim for ≤ 10)

2. **Interface shapes** in the subtask's `interface_shapes` field:
   - `package`: the Go package path
   - `functions`: list of exported function signatures (e.g., "Analyze(worktreeRoot, pkgPath string) (*DepthReport, error)")
   - `types`: list of exported type names (e.g., "DepthReport", "PassThrough")

A deep module has a LOT of functionality behind a SIMPLE interface:
- Few exported symbols relative to total implementation (export ratio ≤ 0.15)
- No pass-through functions that just delegate to another package
- Rich internal logic that justifies the module's existence

If you cannot define module boundaries for a subtask, explain why in the description.
Do NOT create shallow modules that redistribute complexity through pass-through interfaces.
```

Also update the `plan.json` schema in `plannerInstructions()` to include the new fields. Add `module_boundaries` and `interface_shapes` to the subtask schema shown in the prompt:

```json
{
  "subtasks": [
    {
      "title": "...",
      "description": "...",
      "agent_type": "coder|researcher",
      "phase": "test|implementation|integration",
      "tests_for": [1],
      "files": ["path/to/file.go"],
      "dependencies": [],
      "module_boundaries": [
        {"package": "internal/foo", "description": "what it encapsulates", "exports": 5}
      ],
      "interface_shapes": [
        {"package": "internal/foo", "functions": ["DoThing(ctx context.Context) error"], "types": ["Config", "Result"]}
      ]
    }
  ]
}
```

#### 2. prompt.go — Coder instructions (plan-derived depth guidance)

Modify `testPhaseCoderInstructions(task)`, `implPhaseCoderInstructions(task)`, and `defaultCoderInstructions(task)` to include depth guidance when the task's parent plan contains depth metadata.

Add a helper function:

```go
// depthGuidanceFromPlan extracts depth guidance from the parent task's plan
// for the current subtask. Returns empty string if no depth metadata exists.
func depthGuidanceFromPlan(opts Opts) string
```

This function should:
- Access `opts.ParentCtx` to find the plan JSON (stored under key `"plan"`)
- Parse the plan to find the subtask matching the current task
- If the matching subtask has `module_boundaries` or `interface_shapes`, format them as a guidance section
- Return a markdown section like:

```
## Depth Guidance (from plan)

This subtask defines the following module boundaries:

- **internal/constraints/depth**: Depth analysis engine computing export ratio, pass-through detection, and growth rate. Expected exports: 5.

Target interface shape for `internal/constraints/depth`:
- Functions: `Analyze(worktreeRoot, pkgPath string) (*DepthReport, error)`, `AnalyzeAll(worktreeRoot string, pkgPaths []string) (map[string]*DepthReport, error)`
- Types: `DepthReport`, `PassThrough`, `GrowthReport`

Keep your implementation aligned with these boundaries. Do not add exports beyond what is specified.
```

If no plan-level depth metadata exists, fall back to generic constraint-derived guidance:

```
## Depth Guidance

Keep modules deep: maximize functionality behind simple interfaces.
- Aim for export ratio ≤ 0.15 (exported symbols / total LOC)
- Avoid pass-through functions that just delegate to another package
- Every exported symbol should justify its existence
```

Insert the depth guidance section into each coder instruction function's output, after the main task-specific instructions and before the scope limitation.

#### 3. prompt_test.go (or prompt_depth_test.go)

Add tests to verify depth guidance appears in generated prompts:

- **Planner prompt contains depth section**: `Generate` with `AgentType: "planner"` → output contains "Module Depth Requirements" and "module_boundaries"
- **Coder prompt with plan-derived depth**: `Generate` with `AgentType: "coder"` and `ParentCtx` containing a plan with depth metadata → output contains "Depth Guidance (from plan)" and the specific module boundaries
- **Coder prompt without plan falls back**: `Generate` with `AgentType: "coder"` and no plan in `ParentCtx` → output contains "Depth Guidance" with generic constraint-derived text
- **Test phase coder gets depth guidance**: verify depth guidance appears in test-phase coder prompts
- **Implementation phase coder gets depth guidance**: verify depth guidance appears in impl-phase coder prompts

## Scope Limitation

- Do NOT modify the core `Generate` function signature or `Opts` struct
- Do NOT modify researcher, reviewer, or fixer instructions — depth guidance only applies to planner and coder agents
- Do NOT import `internal/constraints/depth` — the prompt package generates text, it doesn't call depth analysis
- Do NOT modify any files outside `internal/prompt/`

## Conventions

- Package: `package prompt`
- Go 1.22+ with standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Table-driven tests with `t.Run()` sub-tests
- Build verification: `go build ./internal/prompt/ && go test ./internal/prompt/ -v`
