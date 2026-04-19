package agent

import (
	"strings"
	"testing"
)

func TestImageResolver_Coder_GoLanguage(t *testing.T) {
	r := &ImageResolver{Language: "go"}
	got, err := r.Resolve("coder")
	if err != nil {
		t.Fatalf("Resolve coder (go): unexpected error: %v", err)
	}
	want := "localhost:5000/drem-worker-go:latest"
	if got != want {
		t.Errorf("Resolve coder (go) = %q, want %q", got, want)
	}
}

func TestImageResolver_Coder_CppLanguage(t *testing.T) {
	r := &ImageResolver{Language: "cpp"}
	got, err := r.Resolve("coder")
	if err != nil {
		t.Fatalf("Resolve coder (cpp): unexpected error: %v", err)
	}
	want := "localhost:5000/drem-worker-cpp:latest"
	if got != want {
		t.Errorf("Resolve coder (cpp) = %q, want %q", got, want)
	}
}

func TestImageResolver_Merger_IgnoresLanguage(t *testing.T) {
	// Merger resolution is not language-sensitive. Both projects must
	// end up on the same image so a cross-language merge behaves the same.
	for _, lang := range []string{"go", "cpp", ""} {
		r := &ImageResolver{Language: lang}
		got, err := r.Resolve("merger")
		if err != nil {
			t.Fatalf("Resolve merger (lang=%q): unexpected error: %v", lang, err)
		}
		want := "localhost:5000/drem-merger:latest"
		if got != want {
			t.Errorf("Resolve merger (lang=%q) = %q, want %q", lang, got, want)
		}
	}
}

func TestImageResolver_Override_TakesPrecedence(t *testing.T) {
	r := &ImageResolver{
		Language: "go",
		Overrides: map[string]string{
			"coder": "localhost:5000/drem-worker-go:v1.2",
		},
	}
	got, err := r.Resolve("coder")
	if err != nil {
		t.Fatalf("Resolve coder with override: unexpected error: %v", err)
	}
	want := "localhost:5000/drem-worker-go:v1.2"
	if got != want {
		t.Errorf("Resolve coder with override = %q, want %q", got, want)
	}
}

func TestImageResolver_Override_PinsMerger(t *testing.T) {
	r := &ImageResolver{
		Overrides: map[string]string{
			"merger": "localhost:5000/drem-merger:v1.2",
		},
	}
	got, err := r.Resolve("merger")
	if err != nil {
		t.Fatalf("Resolve merger with override: unexpected error: %v", err)
	}
	want := "localhost:5000/drem-merger:v1.2"
	if got != want {
		t.Errorf("Resolve merger with override = %q, want %q", got, want)
	}
}

func TestImageResolver_UnknownAgentType_ReturnsError(t *testing.T) {
	r := &ImageResolver{Language: "go"}
	_, err := r.Resolve("does-not-exist")
	if err == nil {
		t.Fatal("Resolve unknown agent type: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error should name the agent type, got %q", err.Error())
	}
}

func TestImageResolver_Coder_MissingLanguage_ReturnsError(t *testing.T) {
	r := &ImageResolver{}
	_, err := r.Resolve("coder")
	if err == nil {
		t.Fatal("Resolve coder with no language: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "language") {
		t.Errorf("error should mention language, got %q", err.Error())
	}
}

func TestImageResolver_EmptyAgentType_ReturnsError(t *testing.T) {
	r := &ImageResolver{Language: "go"}
	_, err := r.Resolve("")
	if err == nil {
		t.Fatal("Resolve empty agent type: expected error, got nil")
	}
}

func TestImageResolver_NilReceiver_LanguageAgnostic(t *testing.T) {
	// A nil *ImageResolver still works for language-agnostic types so
	// that a caller who has not constructed one yet can still resolve
	// defaults. Only the coder case needs Language.
	var r *ImageResolver
	got, err := r.Resolve("merger")
	if err != nil {
		t.Fatalf("Resolve merger on nil resolver: unexpected error: %v", err)
	}
	if got == "" {
		t.Errorf("Resolve merger on nil resolver returned empty image")
	}
}
