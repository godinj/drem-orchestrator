package worktreehost

import (
	"path/filepath"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestManagerMergeBranchUsesNeutralGeneratedCommitMessage(t *testing.T) {
	bare := testutil.SetupBareRepo(t)
	defaultBranch := testutil.GetDefaultBranch(t, bare)
	target := t.TempDir()
	testutil.AddWorktree(t, bare, "integration", target)
	source := t.TempDir()
	// This name deliberately resembles a policy-sensitive external title. The
	// generated merge subject must remain independent of it.
	testutil.AddWorktree(t, bare, "feature/competitor-product", source)
	testutil.CommitFile(t, source, "change.txt", "accepted\n", "scoped change")

	manager := &Manager{BareRepoPath: bare, DefaultBranch: defaultBranch}
	result, err := manager.MergeBranch("feature/competitor-product", target)
	require.NoError(t, err)
	require.True(t, result.Success)
	subject, err := RunGit([]string{"log", "-1", "--format=%s"}, filepath.Clean(target))
	require.NoError(t, err)
	require.Equal(t, neutralMergeMessage, subject)
}

func TestParseWorktreeListPorcelainParsesNormalEntries(t *testing.T) {
	output := `worktree /repo/main
HEAD 1111111111111111111111111111111111111111
branch refs/heads/main

worktree /repo/feature/example/integration
HEAD 2222222222222222222222222222222222222222
branch refs/heads/feature/example`

	worktrees := parseWorktreeListPorcelain(output)
	if len(worktrees) != 2 {
		t.Fatalf("len(worktrees) = %d, want 2", len(worktrees))
	}

	assertWorktreeInfo(t, worktrees[0], WorktreeInfo{
		Path:   "/repo/main",
		Branch: "main",
		Head:   "1111111111111111111111111111111111111111",
		IsBare: false,
	})
	assertWorktreeInfo(t, worktrees[1], WorktreeInfo{
		Path:   "/repo/feature/example/integration",
		Branch: "feature/example",
		Head:   "2222222222222222222222222222222222222222",
		IsBare: false,
	})
}

func TestParseWorktreeListPorcelainParsesBareEntries(t *testing.T) {
	output := `worktree /repo/project.git
bare

worktree /repo/main
HEAD 1111111111111111111111111111111111111111
branch refs/heads/main
`

	worktrees := parseWorktreeListPorcelain(output)
	if len(worktrees) != 2 {
		t.Fatalf("len(worktrees) = %d, want 2", len(worktrees))
	}

	assertWorktreeInfo(t, worktrees[0], WorktreeInfo{
		Path:   "/repo/project.git",
		Branch: "",
		Head:   "",
		IsBare: true,
	})
	assertWorktreeInfo(t, worktrees[1], WorktreeInfo{
		Path:   "/repo/main",
		Branch: "main",
		Head:   "1111111111111111111111111111111111111111",
		IsBare: false,
	})
}

func TestMainWorktreePathFromPorcelainLocatesDefaultBranch(t *testing.T) {
	output := `worktree /repo/feature/example/integration
HEAD 2222222222222222222222222222222222222222
branch refs/heads/feature/example

worktree /repo/main
HEAD 1111111111111111111111111111111111111111
branch refs/heads/main
`

	path, ok := mainWorktreePathFromPorcelain(output, "main")
	if !ok {
		t.Fatal("mainWorktreePathFromPorcelain ok = false, want true")
	}
	if path != "/repo/main" {
		t.Fatalf("path = %q, want %q", path, "/repo/main")
	}
}

func TestMainWorktreePathFromPorcelainMissingDefaultBranch(t *testing.T) {
	output := `worktree /repo/feature/example/integration
HEAD 2222222222222222222222222222222222222222
branch refs/heads/feature/example
`

	path, ok := mainWorktreePathFromPorcelain(output, "main")
	if ok {
		t.Fatalf("mainWorktreePathFromPorcelain ok = true, want false with path %q", path)
	}
}

func assertWorktreeInfo(t *testing.T, got, want WorktreeInfo) {
	t.Helper()
	if got != want {
		t.Fatalf("worktree = %+v, want %+v", got, want)
	}
}
