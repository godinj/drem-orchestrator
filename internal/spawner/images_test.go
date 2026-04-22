package spawner

import "testing"

// TestResolveImage_PlannerNotMapped asserts that AgentType "planner" has
// no mapping in the spawner image table. The warm drem-planner is a
// long-lived service in deploy/compose/global.yml, not a spawn-on-demand
// role — so the spawner must refuse a planner spawn rather than silently
// succeeding against a stale image tag. See plans/warm-planner-pivot.md §7.
func TestResolveImage_PlannerNotMapped(t *testing.T) {
	if _, ok := resolveImage("planner", nil); ok {
		t.Fatalf("resolveImage(planner): expected ok=false (warm planner only); got mapped image")
	}
}

// TestResolveImage_MergerStillMapsToDremMerger is the regression guard:
// the planner addition must not disturb the existing merger mapping.
func TestResolveImage_MergerStillMapsToDremMerger(t *testing.T) {
	got, ok := resolveImage("merger", nil)
	if !ok {
		t.Fatalf("resolveImage(merger): expected mapping, got ok=false")
	}
	const want = "localhost:5000/drem-merger:latest"
	if got != want {
		t.Fatalf("resolveImage(merger): got %q, want %q", got, want)
	}
}

// TestResolveImage_UnknownAgentTypeReturnsFalse is the nil-mapping guard
// so the service surfaces an invalid-params error for unknown agent types
// rather than forwarding an empty image string to the runtime.
func TestResolveImage_UnknownAgentTypeReturnsFalse(t *testing.T) {
	_, ok := resolveImage("nonexistent", nil)
	if ok {
		t.Fatalf("resolveImage(nonexistent): expected ok=false")
	}
}

// TestResolveImage_NonCoderAgentTypesMapped asserts every non-coder agent
// type the orchestrator spawns through the spawner RPC resolves to a real
// image. These mappings were historically duplicated in
// internal/agent/image_resolver.go (DefaultImages) but the spawner-side
// table drifted behind, causing the 2026-04-22 v15/v16 canary regression
// where `SpawnWorker` returned `-32000 no image mapping for
// agent_type="fixer"` after a test_review approval triggered fixer
// dispatch. Every agent type in the sibling DefaultImages table must
// resolve here too; this test is the drift guard.
func TestResolveImage_NonCoderAgentTypesMapped(t *testing.T) {
	cases := []struct {
		agentType string
		want      string
	}{
		{"reviewer", "localhost:5000/drem-worker-go:latest"},
		{"fixer", "localhost:5000/drem-worker-go:latest"},
		{"supervisor", "localhost:5000/drem-worker-go:latest"},
		{"classifier", "localhost:5000/drem-worker-go:latest"},
	}
	for _, tc := range cases {
		t.Run(tc.agentType, func(t *testing.T) {
			got, ok := resolveImage(tc.agentType, nil)
			if !ok {
				t.Fatalf("resolveImage(%s): expected mapping, got ok=false", tc.agentType)
			}
			if got != tc.want {
				t.Fatalf("resolveImage(%s): got %q, want %q", tc.agentType, got, tc.want)
			}
		})
	}
}
