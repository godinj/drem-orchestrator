package main

import "testing"

func TestDefaultCsuiteRoot(t *testing.T) {
	t.Run("legacy default without project", func(t *testing.T) {
		t.Setenv("DREM_CSUITE_ROOT", "")
		t.Setenv("DREM_PROJECT", "")

		if got := defaultCsuiteRoot(); got != "~/.drem-csuite" {
			t.Fatalf("defaultCsuiteRoot() = %q, want legacy global root", got)
		}
	})

	t.Run("project default", func(t *testing.T) {
		t.Setenv("DREM_CSUITE_ROOT", "")
		t.Setenv("DREM_PROJECT", "drem-canvas")

		want := "~/.drem/projects/drem-canvas/csuite"
		if got := defaultCsuiteRoot(); got != want {
			t.Fatalf("defaultCsuiteRoot() = %q, want %q", got, want)
		}
	})

	t.Run("explicit root wins", func(t *testing.T) {
		t.Setenv("DREM_CSUITE_ROOT", "/srv/csuite")
		t.Setenv("DREM_PROJECT", "drem-canvas")

		if got := defaultCsuiteRoot(); got != "/srv/csuite" {
			t.Fatalf("defaultCsuiteRoot() = %q, want explicit root", got)
		}
	})
}
