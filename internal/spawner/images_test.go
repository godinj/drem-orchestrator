package spawner

import "testing"

// TestResolveImage_PlannerNotMapped asserts that AgentType "planner" has
// no mapping in the shared image table the spawner delegates to. The
// warm drem-planner is a long-lived service in deploy/compose/global.yml,
// not a spawn-on-demand role — so the spawner must refuse a planner
// spawn rather than silently succeeding against a stale image tag. See
// plans/warm-planner-pivot.md §7.
func TestResolveImage_PlannerNotMapped(t *testing.T) {
	if _, ok := resolveImage("planner", nil); ok {
		t.Fatalf("resolveImage(planner): expected ok=false (warm planner only); got mapped image")
	}
}

// TestResolveImage_MergerStillMapsToDremMerger pins the spawner-side
// behaviour of the image shim: merger must resolve even though the
// table itself lives in internal/images. Catches accidental rewires
// of the shim that break a hot-path agent type.
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

// Note: TestResolveImage_NonCoderAgentTypesMapped (the drift guard for
// reviewer/fixer/supervisor/classifier) was retired when the spawner
// migrated to the shared internal/images package. The guard is no
// longer meaningful against a single-table design — it now lives as
// internal/images/enum_coverage_test.go, which sources agent types
// from internal/model/enums.go directly.
