package prompt

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestPlannerPromptContainsDepthSection(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentPlanner
	output := Generate(opts)

	required := []string{
		"## Module Depth Requirements",
		"module_boundaries",
		"interface_shapes",
		"You MUST design for depth",
		"export ratio",
	}

	for _, r := range required {
		if !strings.Contains(output, r) {
			t.Errorf("planner prompt missing required depth text: %q", r)
		}
	}
}

func TestPlannerPromptSchemaIncludesDepthFields(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	// The schema example in the planner instructions should contain
	// the module_boundaries and interface_shapes fields.
	schemaFields := []string{
		`"module_boundaries"`,
		`"interface_shapes"`,
		`"package": "internal/foo"`,
		`"description": "what it encapsulates"`,
		`"exports": 5`,
		`"functions": ["DoThing(ctx context.Context) error"]`,
		`"types": ["Config", "Result"]`,
	}

	for _, f := range schemaFields {
		if !strings.Contains(output, f) {
			t.Errorf("planner schema missing field: %q", f)
		}
	}
}

func TestCoderPromptWithPlanDerivedDepth(t *testing.T) {
	plan := map[string]any{
		"subtasks": []map[string]any{
			{
				"title":       "Implement depth analyzer",
				"description": "Build the depth analysis engine",
				"module_boundaries": []map[string]any{
					{
						"package":     "internal/constraints/depth",
						"description": "Depth analysis engine computing export ratio.",
						"exports":     5,
					},
				},
				"interface_shapes": []map[string]any{
					{
						"package":   "internal/constraints/depth",
						"functions": []string{"Analyze(worktreeRoot, pkgPath string) (*DepthReport, error)"},
						"types":     []string{"DepthReport", "PassThrough"},
					},
				},
			},
		},
	}

	planJSON, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}

	opts := minimalOpts()
	opts.AgentType = model.AgentCoder
	opts.Task.Title = "Implement depth analyzer"
	opts.ParentCtx = map[string]any{
		"plan": string(planJSON),
	}

	output := Generate(opts)

	required := []string{
		"## Depth Guidance (from plan)",
		"internal/constraints/depth",
		"Depth analysis engine computing export ratio.",
		"Expected exports: 5",
		"Analyze(worktreeRoot, pkgPath string) (*DepthReport, error)",
		"DepthReport",
		"PassThrough",
		"Keep your implementation aligned with these boundaries",
	}

	for _, r := range required {
		if !strings.Contains(output, r) {
			t.Errorf("coder prompt with plan depth missing: %q", r)
		}
	}
}

func TestCoderPromptWithoutPlanFallsBack(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentCoder
	// No ParentCtx set — should fall back to generic guidance.

	output := Generate(opts)

	required := []string{
		"## Depth Guidance",
		"Keep modules deep: maximize functionality behind simple interfaces.",
		"export ratio",
		"pass-through functions",
		"Every exported symbol should justify its existence",
	}

	for _, r := range required {
		if !strings.Contains(output, r) {
			t.Errorf("coder prompt without plan missing generic depth text: %q", r)
		}
	}

	// Should NOT contain plan-derived header.
	if strings.Contains(output, "## Depth Guidance (from plan)") {
		t.Error("coder prompt without plan should not contain plan-derived depth guidance header")
	}
}

func TestTestPhaseCoderGetsDepthGuidance(t *testing.T) {
	t.Run("with plan depth metadata", func(t *testing.T) {
		plan := map[string]any{
			"subtasks": []map[string]any{
				{
					"title": "Write tests for analyzer",
					"module_boundaries": []map[string]any{
						{
							"package":     "internal/analyzer",
							"description": "Static code analyzer.",
							"exports":     3,
						},
					},
					"interface_shapes": []map[string]any{
						{
							"package":   "internal/analyzer",
							"functions": []string{"Run(path string) (*Report, error)"},
							"types":     []string{"Report"},
						},
					},
				},
			},
		}
		planJSON, _ := json.Marshal(plan)

		opts := minimalOpts()
		opts.AgentType = model.AgentCoder
		opts.Task.Title = "Write tests for analyzer"
		opts.Task.Phase = "test"
		opts.ParentCtx = map[string]any{
			"plan": string(planJSON),
		}

		output := Generate(opts)

		if !strings.Contains(output, "## Depth Guidance (from plan)") {
			t.Error("test-phase coder with plan should contain plan-derived depth guidance")
		}
		if !strings.Contains(output, "internal/analyzer") {
			t.Error("test-phase coder should reference package from plan")
		}
	})

	t.Run("without plan falls back to generic", func(t *testing.T) {
		opts := minimalOpts()
		opts.AgentType = model.AgentCoder
		opts.Task.Phase = "test"

		output := Generate(opts)

		if !strings.Contains(output, "## Depth Guidance") {
			t.Error("test-phase coder without plan should contain generic depth guidance")
		}
		if !strings.Contains(output, "Keep modules deep") {
			t.Error("test-phase coder without plan should contain generic depth text")
		}
	})
}

func TestImplPhaseCoderGetsDepthGuidance(t *testing.T) {
	t.Run("with plan depth metadata", func(t *testing.T) {
		plan := map[string]any{
			"subtasks": []map[string]any{
				{
					"title": "Implement parser",
					"module_boundaries": []map[string]any{
						{
							"package":     "internal/parser",
							"description": "Go source parser for depth metrics.",
							"exports":     4,
						},
					},
					"interface_shapes": []map[string]any{
						{
							"package":   "internal/parser",
							"functions": []string{"Parse(path string) (*AST, error)"},
							"types":     []string{"AST"},
						},
					},
				},
			},
		}
		planJSON, _ := json.Marshal(plan)

		opts := minimalOpts()
		opts.AgentType = model.AgentCoder
		opts.Task.Title = "Implement parser"
		opts.Task.Phase = "implementation"
		opts.ParentCtx = map[string]any{
			"plan": string(planJSON),
		}

		output := Generate(opts)

		if !strings.Contains(output, "## Depth Guidance (from plan)") {
			t.Error("impl-phase coder with plan should contain plan-derived depth guidance")
		}
		if !strings.Contains(output, "internal/parser") {
			t.Error("impl-phase coder should reference package from plan")
		}
		if !strings.Contains(output, "Go source parser for depth metrics.") {
			t.Error("impl-phase coder should include module description from plan")
		}
	})

	t.Run("without plan falls back to generic", func(t *testing.T) {
		opts := minimalOpts()
		opts.AgentType = model.AgentCoder
		opts.Task.Phase = "implementation"

		output := Generate(opts)

		if !strings.Contains(output, "## Depth Guidance") {
			t.Error("impl-phase coder without plan should contain generic depth guidance")
		}
		if !strings.Contains(output, "Avoid pass-through functions") {
			t.Error("impl-phase coder without plan should contain generic depth text")
		}
	})
}

func TestDepthGuidanceFromPlan_EmptyParentCtx(t *testing.T) {
	opts := minimalOpts()
	opts.ParentCtx = nil

	result := depthGuidanceFromPlan(opts)

	if !strings.Contains(result, "## Depth Guidance") {
		t.Error("nil ParentCtx should produce generic depth guidance")
	}
	if strings.Contains(result, "(from plan)") {
		t.Error("nil ParentCtx should not produce plan-derived header")
	}
}

func TestDepthGuidanceFromPlan_PlanWithoutDepthMetadata(t *testing.T) {
	plan := map[string]any{
		"subtasks": []map[string]any{
			{
				"title":       "Simple subtask",
				"description": "No depth metadata here",
			},
		},
	}
	planJSON, _ := json.Marshal(plan)

	opts := minimalOpts()
	opts.Task.Title = "Simple subtask"
	opts.ParentCtx = map[string]any{
		"plan": string(planJSON),
	}

	result := depthGuidanceFromPlan(opts)

	if !strings.Contains(result, "## Depth Guidance") {
		t.Error("plan without depth metadata should produce generic depth guidance")
	}
	if strings.Contains(result, "(from plan)") {
		t.Error("plan without depth metadata should not produce plan-derived header")
	}
}

func TestDepthGuidanceFromPlan_InvalidPlanJSON(t *testing.T) {
	opts := minimalOpts()
	opts.ParentCtx = map[string]any{
		"plan": "not valid json{{{",
	}

	result := depthGuidanceFromPlan(opts)

	if !strings.Contains(result, "## Depth Guidance") {
		t.Error("invalid plan JSON should produce generic depth guidance")
	}
	if strings.Contains(result, "(from plan)") {
		t.Error("invalid plan JSON should not produce plan-derived header")
	}
}

func TestDepthGuidanceFromPlan_PlanAsMap(t *testing.T) {
	// Plan stored as already-parsed map (not a string).
	plan := map[string]any{
		"subtasks": []any{
			map[string]any{
				"title": "Build widget",
				"module_boundaries": []any{
					map[string]any{
						"package":     "internal/widget",
						"description": "Widget manager.",
						"exports":     2,
					},
				},
				"interface_shapes": []any{
					map[string]any{
						"package":   "internal/widget",
						"functions": []any{"New() *Widget"},
						"types":     []any{"Widget"},
					},
				},
			},
		},
	}

	opts := minimalOpts()
	opts.Task.Title = "Build widget"
	opts.ParentCtx = map[string]any{
		"plan": plan, // raw map, not JSON string
	}

	result := depthGuidanceFromPlan(opts)

	if !strings.Contains(result, "## Depth Guidance (from plan)") {
		t.Error("plan as map should produce plan-derived depth guidance")
	}
	if !strings.Contains(result, "internal/widget") {
		t.Error("plan as map should contain package from plan")
	}
}

func TestDepthGuidanceFromPlan_NoMatchingSubtask(t *testing.T) {
	// When the current task title doesn't match any subtask,
	// the function should collect all depth metadata from the plan.
	plan := map[string]any{
		"subtasks": []map[string]any{
			{
				"title": "Other subtask",
				"module_boundaries": []map[string]any{
					{
						"package":     "internal/other",
						"description": "Other module.",
						"exports":     7,
					},
				},
				"interface_shapes": []map[string]any{
					{
						"package":   "internal/other",
						"functions": []string{"Do() error"},
						"types":     []string{"Config"},
					},
				},
			},
		},
	}
	planJSON, _ := json.Marshal(plan)

	opts := minimalOpts()
	opts.Task.Title = "Non-matching title"
	opts.ParentCtx = map[string]any{
		"plan": string(planJSON),
	}

	result := depthGuidanceFromPlan(opts)

	// Should still produce plan-derived guidance using all subtask metadata.
	if !strings.Contains(result, "## Depth Guidance (from plan)") {
		t.Error("non-matching title should still produce plan-derived guidance from all subtasks")
	}
	if !strings.Contains(result, "internal/other") {
		t.Error("non-matching title should include depth metadata from all subtasks")
	}
}
