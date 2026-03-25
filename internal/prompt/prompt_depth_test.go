package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// repoRoot returns the repository root by walking up from the test file location.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to determine test file location")
	}
	// thisFile is internal/prompt/prompt_depth_test.go — walk up three levels.
	root := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	return root
}

func TestREADMEContainsDepthScoringDocumentation(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join(repoRoot(t), "README.md"))
	if err != nil {
		t.Fatalf("failed to read README.md: %v", err)
	}
	content := string(readme)

	t.Run("documents the three depth scoring criteria", func(t *testing.T) {
		criteria := []string{
			"module_boundaries",
			"interface_shapes",
			"export",
		}
		for _, c := range criteria {
			if !strings.Contains(content, c) {
				t.Errorf("README missing depth scoring criterion: %q", c)
			}
		}
	})

	t.Run("documents the planner self-check step", func(t *testing.T) {
		// The README should describe the self-check that planners run
		// before submitting a plan to verify depth quality.
		selfCheckTerms := []string{
			"self-check",
			"meaningful internal logic",
		}
		for _, term := range selfCheckTerms {
			if !strings.Contains(strings.ToLower(content), strings.ToLower(term)) {
				t.Errorf("README missing planner self-check documentation: %q", term)
			}
		}
	})

	t.Run("documents shallow vs deep plan examples", func(t *testing.T) {
		// The README should reference the shallow vs deep plan examples
		// that are included in the planner prompt.
		exampleTerms := []string{
			"shallow",
			"deep",
		}
		for _, term := range exampleTerms {
			if !strings.Contains(strings.ToLower(content), strings.ToLower(term)) {
				t.Errorf("README missing shallow vs deep plan example reference: %q", term)
			}
		}

		// Should describe what makes a plan shallow (thin wrappers, no logic)
		// vs deep (real internal decision-making, policy objects, state machines).
		depthIndicators := []string{
			"thin wrapper",
			"pass-through",
		}
		found := false
		for _, indicator := range depthIndicators {
			if strings.Contains(strings.ToLower(content), strings.ToLower(indicator)) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("README should describe shallow plan anti-patterns (e.g., %v)", depthIndicators)
		}
	})
}

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
		`"package": "pkg/foo"`,
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

// ── Depth Scoring Criteria in planner prompt ──────────────────────────────

func TestPlannerPromptContainsDepthScoringCriteria(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	required := []string{
		"## Depth Scoring Criteria",
		"module_boundaries",
		"interface_shapes",
		"exports",
	}

	for _, r := range required {
		if !strings.Contains(output, r) {
			t.Errorf("planner prompt missing depth scoring criteria text: %q", r)
		}
	}

	// Must explain all three scoring dimensions.
	scoringDimensions := []string{
		"module boundaries",
		"interface shapes",
		"deep decomposition",
	}
	for _, dim := range scoringDimensions {
		if !strings.Contains(strings.ToLower(output), dim) {
			t.Errorf("planner prompt missing scoring dimension: %q", dim)
		}
	}

	// Must mention the exports ≤ 20 threshold for deep decomposition.
	if !strings.Contains(output, "20") {
		t.Errorf("planner prompt missing exports threshold (≤ 20) in depth scoring criteria")
	}
}

// ── Shallow anti-pattern and deep pattern examples ────────────────────────

func TestPlannerPromptContainsDepthExamples(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	// Must have at least one shallow anti-pattern example.
	shallowIndicators := []string{
		"Shallow",
		"anti-pattern",
	}
	foundShallow := false
	for _, s := range shallowIndicators {
		if strings.Contains(output, s) {
			foundShallow = true
			break
		}
	}
	if !foundShallow {
		t.Error("planner prompt missing shallow anti-pattern example; " +
			"expected at least one of: 'Shallow', 'anti-pattern'")
	}

	// Must have at least one deep pattern example.
	deepIndicators := []string{
		"Deep",
		"good pattern",
		"deep pattern",
	}
	foundDeep := false
	lower := strings.ToLower(output)
	for _, d := range deepIndicators {
		if strings.Contains(lower, strings.ToLower(d)) {
			foundDeep = true
			break
		}
	}
	if !foundDeep {
		t.Error("planner prompt missing deep pattern example; " +
			"expected at least one of: 'Deep', 'good pattern', 'deep pattern'")
	}

	// The examples section must contain concrete code-like indicators
	// (package paths, function signatures, or export counts) to be useful.
	concreteIndicators := []string{
		"pass-through",
		"thin wrapper",
	}
	foundConcrete := false
	for _, c := range concreteIndicators {
		if strings.Contains(lower, c) {
			foundConcrete = true
			break
		}
	}
	if !foundConcrete {
		t.Error("planner prompt depth examples missing concrete anti-pattern indicators; " +
			"expected at least one of: 'pass-through', 'thin wrapper'")
	}
}

// ── Pre-Submission Depth Self-Check ───────────────────────────────────────

func TestPlannerPromptContainsDepthSelfCheck(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")

	required := []string{
		"Pre-Submission Depth Self-Check",
	}

	for _, r := range required {
		if !strings.Contains(output, r) {
			t.Errorf("planner prompt missing self-check section: %q", r)
		}
	}

	// The self-check must require evaluating each subtask.
	selfCheckIndicators := []string{
		"each subtask",
		"evaluate",
	}
	lower := strings.ToLower(output)
	for _, s := range selfCheckIndicators {
		if !strings.Contains(lower, s) {
			t.Errorf("planner prompt self-check missing required concept: %q", s)
		}
	}

	// Must mention that shallow subtasks should be reworked or justified.
	if !strings.Contains(lower, "shallow") {
		t.Error("planner prompt self-check should mention 'shallow' subtasks needing rework")
	}
}

// ── Module unification guidance ───────────────────────────────────────────

func TestPlannerPromptContainsModuleUnificationGuidance(t *testing.T) {
	sections := plannerInstructions()
	output := strings.Join(sections, "\n")
	lower := strings.ToLower(output)

	// Must mention unification of related implementations.
	if !strings.Contains(lower, "unif") {
		t.Error("planner prompt missing module unification guidance; " +
			"expected text containing 'unif' (unify/unification)")
	}

	// Must give a concrete example like RetryPolicy.
	if !strings.Contains(output, "RetryPolicy") && !strings.Contains(lower, "retry") {
		t.Error("planner prompt missing concrete unification example (e.g., RetryPolicy)")
	}

	// Must mention shared infrastructure or shared module.
	sharedIndicators := []string{
		"shared",
		"consolidat",
	}
	foundShared := false
	for _, s := range sharedIndicators {
		if strings.Contains(lower, s) {
			foundShared = true
			break
		}
	}
	if !foundShared {
		t.Error("planner prompt unification guidance missing 'shared' or 'consolidat' concept")
	}
}

// ── Generate() includes depth scoring for planner ─────────────────────────

func TestPlannerGenerateIncludesDepthScoringCriteria(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentPlanner
	output := Generate(opts)

	required := []string{
		"Depth Scoring Criteria",
		"Pre-Submission Depth Self-Check",
	}

	for _, r := range required {
		if !strings.Contains(output, r) {
			t.Errorf("Generate() for planner missing depth scoring text: %q", r)
		}
	}
}

// ── Plan reviewer includes depth as review criterion ──────────────────────

func TestPlanReviewerIncludesDepthCriterion(t *testing.T) {
	opts := minimalOpts()
	opts.AgentType = model.AgentReviewer
	opts.ReviewMode = "plan"
	opts.PlanJSON = `{"subtasks": []}`

	output := Generate(opts)

	// The plan reviewer must evaluate depth as one of its review criteria.
	if !strings.Contains(output, "depth") && !strings.Contains(output, "Depth") {
		t.Error("plan reviewer prompt missing 'depth' as a review criterion")
	}

	// Should specifically mention module boundaries or depth scoring.
	depthReviewIndicators := []string{
		"module_boundaries",
		"module boundaries",
		"depth scor",
		"Depth Scor",
	}
	found := false
	for _, d := range depthReviewIndicators {
		if strings.Contains(output, d) {
			found = true
			break
		}
	}
	if !found {
		t.Error("plan reviewer prompt should reference module boundaries or depth scoring in review criteria")
	}

	// Should mention that shallow plans should be flagged.
	lower := strings.ToLower(output)
	if !strings.Contains(lower, "shallow") {
		t.Error("plan reviewer prompt should mention 'shallow' plans as something to flag")
	}
}
