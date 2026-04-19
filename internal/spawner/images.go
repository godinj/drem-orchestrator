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
// language-agnostic agents (merger, csuite-*) resolve directly.
var defaultImages = map[string]string{
	"coder-go":    "localhost:5000/drem-worker-go:latest",
	"coder-cpp":   "localhost:5000/drem-worker-cpp:latest",
	"g4":          "localhost:5000/drem-worker-go:latest",
	"merger":      "localhost:5000/drem-merger:latest",
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
