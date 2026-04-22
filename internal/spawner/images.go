package spawner

// defaultImages is the built-in agent-type → image mapping. The spawner
// consults this table when the caller does not supply an explicit Image
// override. Image tags are :latest because the registry is host-local
// (localhost:5000) and operators repush the tag when they rebuild; pinning
// a digest happens in the compose file that references these images, not
// at the spawn site.
//
// "coder" is language-sensitive: the resolver appends "-<language>" taken
// from Labels["drem.language"] before looking up in this table, so
// coder-go and coder-cpp map to their per-language worker images while
// language-agnostic agents (merger, csuite-*, reviewer/fixer/supervisor/
// classifier) resolve directly.
//
// Sibling table `internal/agent/image_resolver.go:DefaultImages` must
// stay in sync with this one — they are two registries for the same
// mapping and drift between them is what caused the 2026-04-22 v15/v16
// canary regression (reviewer/fixer/supervisor/classifier lived in the
// agent-side table but not here, so spawner RPC dispatch failed with
// `-32000 no image mapping for agent_type="fixer"`). The drift-guard
// test is `images_test.go:TestResolveImage_NonCoderAgentTypesMapped`;
// the longer-term fix — collapsing the two tables into one — is
// `plans/bug-f-spawner-image-registry-drift.md`.
var defaultImages = map[string]string{
	"coder-go":    "localhost:5000/drem-worker-go:latest",
	"coder-cpp":   "localhost:5000/drem-worker-cpp:latest",
	"g4":          "localhost:5000/drem-worker-go:latest",
	"merger":      "localhost:5000/drem-merger:latest",
	// "planner" is NOT mapped here anymore. The warm drem-planner is a
	// long-lived container in deploy/compose/global.yml, not a spawn-
	// on-demand role. Orch reaches it over HTTP via dispatchPlanHTTP.
	// See plans/warm-planner-pivot.md §7.
	"reviewer":    "localhost:5000/drem-worker-go:latest",
	"fixer":       "localhost:5000/drem-worker-go:latest",
	"supervisor":  "localhost:5000/drem-worker-go:latest",
	"classifier":  "localhost:5000/drem-worker-go:latest",
	"csuite-mike": "localhost:5000/drem-csuite-mike:latest",
	"csuite-alex": "localhost:5000/drem-csuite-alex:latest",
	"csuite-ross": "localhost:5000/drem-csuite-ross:latest",
	"csuite-seth": "localhost:5000/drem-csuite-seth:latest",
}

// resolveImage picks the image to spawn for a given agent type. When the
// agent type is "coder", it consults Labels["drem.language"] to pick the
// per-language worker image; for every other agent type it looks up
// directly. The second return is false when no mapping exists for the
// derived key — the service surfaces this as a -32602 invalid-params
// error rather than forwarding the empty string to the runtime.
func resolveImage(agentType string, labels map[string]string) (string, bool) {
	key := agentType
	if agentType == "coder" {
		lang := labels["drem.language"]
		if lang == "" {
			return "", false
		}
		key = "coder-" + lang
	}
	img, ok := defaultImages[key]
	return img, ok
}
