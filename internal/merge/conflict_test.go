package merge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// ---------------------------------------------------------------------------
// ClassifyConflicts
// ---------------------------------------------------------------------------

func TestClassifyConflicts_DocFilesAreTrivial(t *testing.T) {
	paths := []string{"README.md", "ARCHITECTURE.md", "docs/design.md", "docs/api.md"}
	classified := ClassifyConflicts(paths)

	if len(classified) != len(paths) {
		t.Fatalf("expected %d classifications, got %d", len(paths), len(classified))
	}
	for _, cc := range classified {
		if cc.Class != Trivial {
			t.Errorf("expected %s to be Trivial, got %s", cc.Path, cc.Class)
		}
	}
}

func TestClassifyConflicts_GoSourceFilesAreNonTrivial(t *testing.T) {
	paths := []string{"main.go", "internal/merge/merge.go", "cmd/drem/config.go"}
	classified := ClassifyConflicts(paths)

	if len(classified) != len(paths) {
		t.Fatalf("expected %d classifications, got %d", len(paths), len(classified))
	}
	for _, cc := range classified {
		if cc.Class != NonTrivial {
			t.Errorf("expected %s to be NonTrivial, got %s", cc.Path, cc.Class)
		}
	}
}

func TestClassifyConflicts_MixedSet(t *testing.T) {
	paths := []string{"README.md", "main.go", "docs/guide.md", "internal/foo.go"}
	classified := ClassifyConflicts(paths)

	if len(classified) != 4 {
		t.Fatalf("expected 4 classifications, got %d", len(classified))
	}

	want := map[string]ConflictClass{
		"README.md":       Trivial,
		"main.go":         NonTrivial,
		"docs/guide.md":   Trivial,
		"internal/foo.go": NonTrivial,
	}
	for _, cc := range classified {
		exp, ok := want[cc.Path]
		if !ok {
			t.Errorf("unexpected path %s", cc.Path)
			continue
		}
		if cc.Class != exp {
			t.Errorf("path %s: expected %s, got %s", cc.Path, exp, cc.Class)
		}
	}
}

func TestClassifyConflicts_EmptyList(t *testing.T) {
	classified := ClassifyConflicts(nil)
	if len(classified) != 0 {
		t.Errorf("expected empty classification, got %d entries", len(classified))
	}
}

func TestClassifyConflicts_NoExtensionIsNonTrivial(t *testing.T) {
	paths := []string{"Makefile", "Dockerfile", "somefile"}
	classified := ClassifyConflicts(paths)

	if len(classified) != len(paths) {
		t.Fatalf("expected %d classifications, got %d", len(paths), len(classified))
	}
	for _, cc := range classified {
		if cc.Class != NonTrivial {
			t.Errorf("expected %s to be NonTrivial, got %s", cc.Path, cc.Class)
		}
	}
}

func TestClassifyConflicts_ChangelogAndLicenseAreTrivial(t *testing.T) {
	paths := []string{"CHANGELOG.md", "LICENSE"}
	classified := ClassifyConflicts(paths)

	if len(classified) != 2 {
		t.Fatalf("expected 2 classifications, got %d", len(classified))
	}
	// CHANGELOG.md is .md → trivial; LICENSE has no extension → treated as trivial by name
	for _, cc := range classified {
		if cc.Class != Trivial {
			t.Errorf("expected %s to be Trivial, got %s", cc.Path, cc.Class)
		}
	}
}

func TestClassifyConflicts_PromptsMarkdownIsTrivial(t *testing.T) {
	paths := []string{"prompts/planner.md", "prompts/coder.md", "prompts/reviewer.md"}
	classified := ClassifyConflicts(paths)

	if len(classified) != len(paths) {
		t.Fatalf("expected %d classifications, got %d", len(paths), len(classified))
	}
	for _, cc := range classified {
		if cc.Class != Trivial {
			t.Errorf("expected %s to be Trivial, got %s", cc.Path, cc.Class)
		}
	}
}

// ---------------------------------------------------------------------------
// AutoResolveConflicts — uses real git repos via testutil
// ---------------------------------------------------------------------------

// createConflictingMerge sets up a bare repo with two branches that conflict
// on the given files, leaves the worktree in a conflicted state, and returns
// the worktree path.
func createConflictingMerge(t *testing.T, files []string) string {
	t.Helper()
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create feature worktree (the "ours" side)
	featureDir := filepath.Join(dir, "feature")
	testutil.AddWorktree(t, bareRepo, "feature/conflict-test", featureDir)

	// Create agent worktree (the "theirs" side)
	agentDir := filepath.Join(dir, "agent")
	testutil.AddWorktree(t, bareRepo, "agent-conflict", agentDir)

	// Commit conflicting content on both branches for each file
	for _, f := range files {
		fdir := filepath.Dir(f)
		if fdir != "." {
			os.MkdirAll(filepath.Join(featureDir, fdir), 0o755)
			os.MkdirAll(filepath.Join(agentDir, fdir), 0o755)
		}
		testutil.CommitFile(t, featureDir, f, "feature-side content for "+f+"\n", "feature: "+f)
		testutil.CommitFile(t, agentDir, f, "agent-side content for "+f+"\n", "agent: "+f)
	}

	// Attempt merge in feature worktree (expected to conflict)
	_, _ = testutil.RunGit([]string{"merge", "agent-conflict", "--no-edit"}, featureDir)

	return featureDir
}

func TestAutoResolveConflicts_TrivialOnly(t *testing.T) {
	files := []string{"README.md", "docs/guide.md"}
	wtPath := createConflictingMerge(t, files)

	classified := []ClassifiedConflict{
		{Path: "README.md", Class: Trivial, Description: "doc file"},
		{Path: "docs/guide.md", Class: Trivial, Description: "doc file"},
	}

	result := AutoResolveConflicts(wtPath, classified)
	if !result.Resolved {
		t.Errorf("expected trivial-only conflicts to be resolved, got resolved=false: %s", result.Message)
	}
}

func TestAutoResolveConflicts_NonTrivialPresent(t *testing.T) {
	files := []string{"main.go"}
	wtPath := createConflictingMerge(t, files)

	classified := []ClassifiedConflict{
		{Path: "main.go", Class: NonTrivial, Description: "source file"},
	}

	result := AutoResolveConflicts(wtPath, classified)
	if result.Resolved {
		t.Error("expected non-trivial conflicts to NOT be resolved")
	}
}

func TestAutoResolveConflicts_MixedTrivialAndNonTrivial(t *testing.T) {
	files := []string{"README.md", "main.go"}
	wtPath := createConflictingMerge(t, files)

	classified := []ClassifiedConflict{
		{Path: "README.md", Class: Trivial, Description: "doc file"},
		{Path: "main.go", Class: NonTrivial, Description: "source file"},
	}

	result := AutoResolveConflicts(wtPath, classified)
	if result.Resolved {
		t.Error("expected mixed conflicts to NOT be resolved (conservative policy)")
	}
}

func TestAutoResolveConflicts_EmptyList(t *testing.T) {
	result := AutoResolveConflicts("", nil)
	if !result.Resolved {
		t.Error("expected empty classified list to return resolved=true")
	}
}

// ---------------------------------------------------------------------------
// FormatClassifiedConflicts
// ---------------------------------------------------------------------------

func TestFormatClassifiedConflicts_StructuredOutput(t *testing.T) {
	classified := []ClassifiedConflict{
		{Path: "README.md", Class: Trivial, Description: "documentation file"},
		{Path: "main.go", Class: NonTrivial, Description: "Go source file"},
		{Path: "docs/api.md", Class: Trivial, Description: "documentation file"},
	}

	output := FormatClassifiedConflicts(classified)

	if output == "" {
		t.Fatal("expected non-empty formatted output")
	}

	// Each file path should appear in the output
	for _, cc := range classified {
		if !strings.Contains(output, cc.Path) {
			t.Errorf("output missing file path %s", cc.Path)
		}
	}

	// Class labels should appear
	if !strings.Contains(output, "trivial") {
		t.Error("output missing 'trivial' class label")
	}
	if !strings.Contains(output, "non-trivial") {
		t.Error("output missing 'non-trivial' class label")
	}

	// Descriptions should appear
	for _, cc := range classified {
		if !strings.Contains(output, cc.Description) {
			t.Errorf("output missing description %q", cc.Description)
		}
	}
}

func TestFormatClassifiedConflicts_EmptyList(t *testing.T) {
	output := FormatClassifiedConflicts(nil)
	// Empty input should produce empty or minimal output, not panic
	if strings.Contains(output, "non-trivial") {
		t.Error("empty list should not mention conflict classes")
	}
}
