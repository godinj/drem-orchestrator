package spawner

import "testing"

// TestResolveImage_PlannerMapsToDremPlanner asserts that AgentType "planner"
// resolves to the canonical drem-planner:latest image tag. The tag must
// match deploy/docker/planner.Dockerfile's push target so a fresh spawn
// reaches the image primed by `docker compose pull` on the per-project
// planner-template stub.
func TestResolveImage_PlannerMapsToDremPlanner(t *testing.T) {
	got, ok := resolveImage("planner", nil)
	if !ok {
		t.Fatalf("resolveImage(planner): expected mapping, got ok=false")
	}
	const want = "localhost:5000/drem-planner:latest"
	if got != want {
		t.Fatalf("resolveImage(planner): got %q, want %q", got, want)
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
