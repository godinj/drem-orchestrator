// image_resolver.go maps agent types to container images for the
// spawner-routed agent lifecycle introduced in
// docs/containerization/prompts/13-agent-spawn-routing.md.
//
// The resolver is intentionally small: it knows a language (from the
// project's drem.toml), a per-agent-type override map (also from drem.toml)
// and delegates the built-in fallback table lookup to the shared
// internal/images package. There is no runtime I/O. Callers that need
// the language-sensitive "coder" mapping pass agent type "coder" and
// let Language steer the synthesis, or set Overrides["coder"] explicitly.
package agent

import (
	"fmt"

	"github.com/godinj/drem-orchestrator/internal/images"
)

// ImageResolver picks a container image for a given agent type. Language
// and Overrides are both populated from the project's drem.toml: Language
// steers the default coder image, and Overrides lets operators pin a
// specific tag for a given agent type without rebuilding the binary.
//
// An ImageResolver with a zero-value Language still resolves every
// language-agnostic agent type; only coder resolution requires Language.
// The fallback table itself lives in internal/images.DefaultImages so
// the spawner and the agent package share one source of truth.
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
// then the shared internal/images table. For the language-sensitive
// "coder" type, Resolve delegates to images.Resolve with a synthetic
// labels map carrying the Language — that matches the spawner contract
// (labels["drem.language"] steers coder-go vs coder-cpp). An unmappable
// agent type produces a descriptive error rather than silently returning
// an empty image.
func (r *ImageResolver) Resolve(agentType string) (string, error) {
	if agentType == "" {
		return "", fmt.Errorf("image resolver: agent type is empty")
	}
	if r != nil && r.Overrides != nil {
		if img, ok := r.Overrides[agentType]; ok && img != "" {
			return img, nil
		}
	}
	// Coder needs a language; surface a language-specific error rather
	// than falling through to images.Resolve's generic ok=false. The
	// existing callers and tests pattern-match on the "language" word
	// in the error string.
	if agentType == "coder" {
		lang := ""
		if r != nil {
			lang = r.Language
		}
		if lang == "" {
			return "", fmt.Errorf("image resolver: agent type %q requires project language (set [project].language in drem.toml or supply an override)", agentType)
		}
		if img, ok := images.Resolve("coder", map[string]string{"drem.language": lang}); ok {
			return img, nil
		}
		return "", fmt.Errorf("image resolver: no image mapping for agent type %q (language=%q)", agentType, lang)
	}
	if img, ok := images.Resolve(agentType, nil); ok {
		return img, nil
	}
	return "", fmt.Errorf("image resolver: no image mapping for agent type %q", agentType)
}
