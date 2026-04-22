package images

import "testing"

// TestResolve_EveryDefaultImageKeyResolves exhaustively enumerates every
// key in DefaultImages and asserts Resolve returns the corresponding
// image. This is the baseline "the shared table works" guard — adding a
// new entry to DefaultImages automatically gains coverage here via range.
func TestResolve_EveryDefaultImageKeyResolves(t *testing.T) {
	if len(DefaultImages) == 0 {
		t.Fatalf("DefaultImages must not be empty")
	}
	for key, want := range DefaultImages {
		key, want := key, want
		t.Run(key, func(t *testing.T) {
			// Non-coder keys look up directly. For coder-<lang> keys we
			// also assert the direct lookup works; the language-synthesis
			// path is exercised separately below.
			got, ok := Resolve(key, nil)
			if !ok {
				t.Fatalf("Resolve(%q): expected ok=true, got ok=false", key)
			}
			if got != want {
				t.Fatalf("Resolve(%q): got %q, want %q", key, got, want)
			}
		})
	}
}

// TestResolve_CoderSynthesizesLanguageKey covers the language-sensitive
// "coder" agent type. Callers pass agentType="coder" and
// labels["drem.language"]="go" (or "cpp"); Resolve synthesizes the
// composite key "coder-go" (or "coder-cpp") before looking up.
func TestResolve_CoderSynthesizesLanguageKey(t *testing.T) {
	cases := []struct {
		lang string
		want string
	}{
		{"go", "localhost:5000/drem-worker-go:latest"},
		{"cpp", "localhost:5000/drem-worker-cpp:latest"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.lang, func(t *testing.T) {
			got, ok := Resolve("coder", map[string]string{"drem.language": tc.lang})
			if !ok {
				t.Fatalf("Resolve(coder, lang=%s): expected ok=true", tc.lang)
			}
			if got != tc.want {
				t.Fatalf("Resolve(coder, lang=%s): got %q, want %q", tc.lang, got, tc.want)
			}
		})
	}
}

// TestResolve_CoderWithoutLanguageReturnsFalse asserts that the language-
// sensitive "coder" type refuses to resolve when drem.language is empty
// or missing — the spawner must surface this as an invalid-params error
// rather than guess a default language.
func TestResolve_CoderWithoutLanguageReturnsFalse(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
	}{
		{"nil-labels", nil},
		{"empty-labels", map[string]string{}},
		{"empty-language", map[string]string{"drem.language": ""}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := Resolve("coder", tc.labels); ok {
				t.Fatalf("Resolve(coder, %s): expected ok=false", tc.name)
			}
		})
	}
}

// TestResolve_PlannerNotMapped is the warm-planner invariant: AgentType
// "planner" is never spawn-on-demand. The warm drem-planner is a long-
// lived container in deploy/compose/global.yml, and the orchestrator
// reaches it over HTTP. See plans/warm-planner-pivot.md §7.
func TestResolve_PlannerNotMapped(t *testing.T) {
	if _, ok := Resolve("planner", nil); ok {
		t.Fatalf("Resolve(planner): expected ok=false (warm planner only)")
	}
}

// TestResolve_UnknownAgentTypeReturnsFalse is the nil-mapping guard so
// callers surface an invalid-params error for unknown agent types rather
// than forwarding an empty image string to the container runtime.
func TestResolve_UnknownAgentTypeReturnsFalse(t *testing.T) {
	if _, ok := Resolve("nonexistent", nil); ok {
		t.Fatalf("Resolve(nonexistent): expected ok=false")
	}
}
