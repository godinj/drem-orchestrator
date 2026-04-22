package spawner

import "github.com/godinj/drem-orchestrator/internal/images"

// resolveImage picks the image to spawn for a given agent type. When the
// agent type is "coder", it consults Labels["drem.language"] to pick the
// per-language worker image; for every other agent type it looks up
// directly. The second return is false when no mapping exists for the
// derived key — the service surfaces this as a -32602 invalid-params
// error rather than forwarding the empty string to the runtime.
//
// This is a thin shim over internal/images.Resolve. The mapping table
// itself lives in that shared package so the spawner and the agent
// package cannot drift. See plans/bug-f-spawner-image-registry-drift.md.
func resolveImage(agentType string, labels map[string]string) (string, bool) {
	return images.Resolve(agentType, labels)
}
