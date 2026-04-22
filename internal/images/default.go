// Package images is the single source of truth for the orchestrator's
// agent-type → container-image mapping. Historically the table was
// duplicated in internal/spawner/images.go (defaultImages) and
// internal/agent/image_resolver.go (DefaultImages). Those two tables
// drifted — see plans/bug-f-spawner-image-registry-drift.md — and the
// fix was to hoist the mapping into this package and have both sides
// delegate here. Keeping this package free of internal imports keeps
// the dependency graph one-way: spawner and agent import images, not
// the other way around.
//
// Tags are :latest because the registry is host-local (localhost:5000)
// and operators repush the tag when they rebuild. Digest pinning
// happens in the compose file that references these images, not at
// the spawn site.
package images

// DefaultImages is the built-in agent-type → image mapping. Keys are
// the concrete image-selector — "coder-go" and "coder-cpp" are the
// language-specialised worker images, and every non-coder agent type
// has a direct key. Callers should go through Resolve rather than
// indexing this map directly; Resolve owns the language-synthesis
// rule for the "coder" agent type.
var DefaultImages = map[string]string{
	"coder-go":    "localhost:5000/drem-worker-go:latest",
	"coder-cpp":   "localhost:5000/drem-worker-cpp:latest",
	"g4":          "localhost:5000/drem-worker-go:latest",
	"merger":      "localhost:5000/drem-merger:latest",
	"reviewer":    "localhost:5000/drem-worker-go:latest",
	"fixer":       "localhost:5000/drem-worker-go:latest",
	"supervisor":  "localhost:5000/drem-worker-go:latest",
	"classifier":  "localhost:5000/drem-worker-go:latest",
	"csuite-mike": "localhost:5000/drem-csuite-mike:latest",
	"csuite-alex": "localhost:5000/drem-csuite-alex:latest",
	"csuite-ross": "localhost:5000/drem-csuite-ross:latest",
	"csuite-seth": "localhost:5000/drem-csuite-seth:latest",
	// "planner" is deliberately absent — the warm drem-planner is a
	// long-lived container in deploy/compose/global.yml, not a spawn-
	// on-demand role. Orch reaches it over HTTP via dispatchPlanHTTP.
	// See plans/warm-planner-pivot.md §7.
}

// Resolve returns the image for agentType. When agentType is "coder"
// the lookup key is synthesised as "coder-<lang>" where lang is read
// from labels["drem.language"]; an empty or missing language short-
// circuits to (", false) so callers surface an invalid-params error
// rather than guessing a default. For every other agent type the
// lookup is direct. ok=false indicates the caller asked for an agent
// type the orchestrator does not spawn on-demand (planner, unknown
// types) and must refuse rather than forward an empty image to the
// runtime.
func Resolve(agentType string, labels map[string]string) (string, bool) {
	key := agentType
	if agentType == "coder" {
		lang := labels["drem.language"]
		if lang == "" {
			return "", false
		}
		key = "coder-" + lang
	}
	img, ok := DefaultImages[key]
	return img, ok
}
