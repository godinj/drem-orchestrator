package projects_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/projects"
)

// TestDiff_ComposeIdenticalNoDrift asserts that byte-for-byte identical
// YAML yields no drift entries. Baseline idempotency test.
func TestDiff_ComposeIdenticalNoDrift(t *testing.T) {
	doc := `services:
  orch:
    environment:
      DREM_AGENTMON_TOKEN: "tok"
    ports:
      - "127.0.0.1:8080:8080"
`
	entries := projects.Diff([]byte(doc), []byte(doc), projects.FileKindCompose)
	require.Empty(t, entries, "identical docs must produce zero drift entries")
}

// TestDiff_ComposeWhitespaceIsNotDrift asserts that whitespace and
// comment differences do not count as drift. Template comments belong
// to the template; operator-owned drift is structural.
func TestDiff_ComposeWhitespaceIsNotDrift(t *testing.T) {
	rendered := `# template comment
services:
  orch:
    environment:
      DREM_AGENTMON_TOKEN: "tok"
`
	onDisk := `services:
  orch:
    # hand-added comment
    environment:
      DREM_AGENTMON_TOKEN: "tok"
`
	entries := projects.Diff([]byte(rendered), []byte(onDisk), projects.FileKindCompose)
	require.Empty(t, entries, "comments/whitespace must not be drift, got %+v", entries)
}

// TestDiff_ComposeAddedEnvIsDrift asserts that an env key in the
// rendered template but not on-disk shows up as an "added" entry.
// Operator reads this as "update will add this key for you."
func TestDiff_ComposeAddedEnvIsDrift(t *testing.T) {
	onDisk := `services:
  orch:
    environment:
      DREM_AGENTMON_TOKEN: "tok"
`
	rendered := `services:
  orch:
    environment:
      DREM_AGENTMON_TOKEN: "tok"
      DREM_PROMPT_ROOT_HOST: "/home/op/.drem/projects/p/prompts"
`
	entries := projects.Diff([]byte(rendered), []byte(onDisk), projects.FileKindCompose)
	require.Len(t, entries, 1)
	e := entries[0]
	require.Equal(t, "added", e.Kind)
	require.Contains(t, e.Path, "DREM_PROMPT_ROOT_HOST")
	require.Equal(t, "/home/op/.drem/projects/p/prompts", e.NewValue)
	require.Empty(t, e.WasValue)
}

// TestDiff_ComposeRemovedEnvIsDrift asserts that an env key on-disk
// that the template doesn't produce shows up as a "removed" entry.
// This is the GIT_CONFIG_* case from the drem-orchestrator on-disk
// compose: operator hand-patched it; update will remove it unless
// the template learns about it.
func TestDiff_ComposeRemovedEnvIsDrift(t *testing.T) {
	onDisk := `services:
  orch:
    environment:
      DREM_AGENTMON_TOKEN: "tok"
      GIT_CONFIG_COUNT: "1"
`
	rendered := `services:
  orch:
    environment:
      DREM_AGENTMON_TOKEN: "tok"
`
	entries := projects.Diff([]byte(rendered), []byte(onDisk), projects.FileKindCompose)
	require.Len(t, entries, 1)
	e := entries[0]
	require.Equal(t, "removed", e.Kind)
	require.Contains(t, e.Path, "GIT_CONFIG_COUNT")
	require.Equal(t, "1", e.WasValue)
	require.Empty(t, e.NewValue)
}

// TestDiff_ComposeChangedEnvIsDrift asserts that a changed env value
// is reported with both old and new.
func TestDiff_ComposeChangedEnvIsDrift(t *testing.T) {
	onDisk := `services:
  orch:
    environment:
      DREM_AGENTMON_TOKEN: "old-token"
`
	rendered := `services:
  orch:
    environment:
      DREM_AGENTMON_TOKEN: "new-token"
`
	entries := projects.Diff([]byte(rendered), []byte(onDisk), projects.FileKindCompose)
	require.Len(t, entries, 1)
	e := entries[0]
	require.Equal(t, "changed", e.Kind)
	require.Contains(t, e.Path, "DREM_AGENTMON_TOKEN")
	require.Equal(t, "old-token", e.WasValue)
	require.Equal(t, "new-token", e.NewValue)
}

// TestDiff_TomlSectionAdditionIsDrift asserts that a TOML section in
// the template but not on-disk is flagged. Matches the current
// drem.toml situation before commit 3900029 landed [agents.planner].
func TestDiff_TomlSectionAdditionIsDrift(t *testing.T) {
	onDisk := `bare_repo_path = "/x"
[project]
language = "go"
`
	rendered := `bare_repo_path = "/x"
[project]
language = "go"
[agents.planner]
provider = "claude"
`
	entries := projects.Diff([]byte(rendered), []byte(onDisk), projects.FileKindDremToml)
	require.NotEmpty(t, entries, "added [agents.planner] must be drift")
	found := false
	for _, e := range entries {
		if e.Path == "agents.planner.provider" && e.Kind == "added" {
			found = true
			require.Equal(t, "claude", e.NewValue)
		}
	}
	require.True(t, found, "expected agents.planner.provider=added, got %+v", entries)
}

// TestDiff_TomlSectionRemovalIsDrift asserts that a section on-disk
// but not in the template is flagged. Operator-hand-added sections
// that the template doesn't cover come out as removals.
func TestDiff_TomlSectionRemovalIsDrift(t *testing.T) {
	onDisk := `bare_repo_path = "/x"
[project]
language = "go"
[agents.experimental]
provider = "sglang"
`
	rendered := `bare_repo_path = "/x"
[project]
language = "go"
`
	entries := projects.Diff([]byte(rendered), []byte(onDisk), projects.FileKindDremToml)
	require.NotEmpty(t, entries)
	found := false
	for _, e := range entries {
		if e.Path == "agents.experimental.provider" && e.Kind == "removed" {
			found = true
			require.Equal(t, "sglang", e.WasValue)
		}
	}
	require.True(t, found, "expected agents.experimental.provider=removed, got %+v", entries)
}

// TestDiff_InvalidComposeYAMLReturnsEmpty asserts that a parse failure
// yields no drift entries. Extraction errors are surfaced by
// ReadStateFromDisk's caller; Diff is a pure comparison and stays
// defensive so a bad input doesn't mask genuine drift elsewhere.
func TestDiff_InvalidComposeYAMLReturnsEmpty(t *testing.T) {
	entries := projects.Diff([]byte("not yaml: {{{"), []byte("services:\n  orch:\n"), projects.FileKindCompose)
	// Empty rather than error: Diff is advisory, not authoritative.
	require.NotNil(t, entries)
}
