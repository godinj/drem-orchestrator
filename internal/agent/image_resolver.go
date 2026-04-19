// image_resolver.go maps agent types to container images for the
// spawner-routed agent lifecycle introduced in
// docs/containerization/prompts/13-agent-spawn-routing.md.
//
// The resolver is intentionally small: it knows a language (from the
// project's drem.toml), a per-agent-type override map (also from drem.toml)
// and a built-in fallback table. There is no runtime I/O. Callers that
// need the language-sensitive "coder" mapping pass the agent type
// "coder-<language>" or set Overrides["coder"] explicitly.
package agent

import "fmt"

// DefaultImages is the built-in agent-type → image mapping consulted when
// neither the per-agent override nor the language-derived key hits. It is
// kept in sync with internal/spawner/images.go by convention: the spawner
// is the source of truth for the image table; agent code only needs a
// copy when callers ask the resolver to produce a concrete image name
// before handing the SpawnWorkerParams to the spawner.
//
// Tags are :latest because the registry is host-local (localhost:5000)
// and operators repush when they rebuild. Pinning a digest happens in the
// compose file that references these images, not at the spawn site.
var DefaultImages = map[string]string{
	"coder-go":   "localhost:5000/drem-worker-go:latest",
	"coder-cpp":  "localhost:5000/drem-worker-cpp:latest",
	"g4":         "localhost:5000/drem-worker-go:latest",
	"merger":     "localhost:5000/drem-merger:latest",
	"reviewer":   "localhost:5000/drem-worker-go:latest",
	"fixer":      "localhost:5000/drem-worker-go:latest",
	"supervisor": "localhost:5000/drem-worker-go:latest",
	"classifier": "localhost:5000/drem-worker-go:latest",
}

// ImageResolver picks a container image for a given agent type. Language
// and Overrides are both populated from the project's drem.toml: Language
// steers the default coder image, and Overrides lets operators pin a
// specific tag for a given agent type without rebuilding the binary.
//
// An ImageResolver with a zero-value Language still resolves every
// language-agnostic agent type; only coder resolution requires Language.
type ImageResolver struct {
	// Language is the project language used to pick the per-language
	// default for the coder agent type. Supported values match
	// internal/projects: "go", "cpp".
	Language string
	// Overrides maps the caller's requested agent type to an explicit
	// image. An override always wins over the built-in default. Keys are
	// the unprefixed agent type ("coder", "merger", ...); the resolver
	// does not synthesise a language suffix before looking up an
	// override.
	Overrides map[string]string
}

// Resolve returns the image for agentType. Overrides are checked first,
// then the built-in defaults. For the language-sensitive "coder" type,
// the resolver appends "-<Language>" before looking up in DefaultImages
// so that a Go project picks drem-worker-go and a C++ project picks
// drem-worker-cpp. An unmappable agent type produces a descriptive error
// rather than silently returning an empty image.
func (r *ImageResolver) Resolve(agentType string) (string, error) {
	if agentType == "" {
		return "", fmt.Errorf("image resolver: agent type is empty")
	}
	if r != nil && r.Overrides != nil {
		if img, ok := r.Overrides[agentType]; ok && img != "" {
			return img, nil
		}
	}
	key := agentType
	if agentType == "coder" {
		lang := ""
		if r != nil {
			lang = r.Language
		}
		if lang == "" {
			return "", fmt.Errorf("image resolver: agent type %q requires project language (set [project].language in drem.toml or supply an override)", agentType)
		}
		key = "coder-" + lang
	}
	if img, ok := DefaultImages[key]; ok {
		return img, nil
	}
	return "", fmt.Errorf("image resolver: no image mapping for agent type %q", agentType)
}
