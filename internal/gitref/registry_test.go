package gitref_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func newRegistry(t *testing.T) (*gitref.Registry, *gorm.DB) {
	t.Helper()
	db := testutil.NewTestDBWithModels(t, &gitref.BranchRef{})
	return gitref.NewRegistry(db), db
}

func TestRegistry_Register_InsertsAndIsQueryable(t *testing.T) {
	ctx := context.Background()
	reg, _ := newRegistry(t)
	bare := testutil.SetupBareRepo(t)

	ref := &gitref.BranchRef{
		BareRepoPath: bare,
		Project:      "drem-orch",
		TaskID:       "task-1",
		AgentType:    "coder",
		Branch:       "feature/login",
	}
	err := reg.Register(ctx, ref)
	require.NoError(t, err)
	require.NotZero(t, ref.ID, "Register should populate ID")
	require.Equal(t, gitref.StatusActive, ref.Status, "default status should be active")

	got, err := reg.FindByBranch(ctx, bare, "feature/login")
	require.NoError(t, err)
	require.Equal(t, ref.ID, got.ID)
	require.Equal(t, "task-1", got.TaskID)
	require.Equal(t, gitref.StatusActive, got.Status)
}

func TestRegistry_Register_DuplicateActiveReturnsError(t *testing.T) {
	ctx := context.Background()
	reg, _ := newRegistry(t)
	bare := testutil.SetupBareRepo(t)

	first := &gitref.BranchRef{
		BareRepoPath: bare,
		Project:      "drem-orch",
		Branch:       "feature/api",
	}
	require.NoError(t, reg.Register(ctx, first))

	second := &gitref.BranchRef{
		BareRepoPath: bare,
		Project:      "drem-orch",
		Branch:       "feature/api",
	}
	err := reg.Register(ctx, second)
	require.Error(t, err, "duplicate active branch should be rejected")
	require.Contains(t, err.Error(), "already active")
}

func TestRegistry_Register_SameBranchAcrossRepos(t *testing.T) {
	ctx := context.Background()
	reg, _ := newRegistry(t)
	bareA := testutil.SetupBareRepo(t)
	bareB := testutil.SetupBareRepo(t)
	require.NotEqual(t, bareA, bareB)

	refA := &gitref.BranchRef{
		BareRepoPath: bareA,
		Project:      "proj-a",
		Branch:       "feature/shared-name",
	}
	refB := &gitref.BranchRef{
		BareRepoPath: bareB,
		Project:      "proj-b",
		Branch:       "feature/shared-name",
	}
	require.NoError(t, reg.Register(ctx, refA))
	require.NoError(t, reg.Register(ctx, refB),
		"same branch name in a different bare repo must be allowed")
	require.NotEqual(t, refA.ID, refB.ID)

	gotA, err := reg.FindByBranch(ctx, bareA, "feature/shared-name")
	require.NoError(t, err)
	require.Equal(t, "proj-a", gotA.Project)

	gotB, err := reg.FindByBranch(ctx, bareB, "feature/shared-name")
	require.NoError(t, err)
	require.Equal(t, "proj-b", gotB.Project)
}

func TestRegistry_Register_RevivesTerminalRow(t *testing.T) {
	ctx := context.Background()
	reg, _ := newRegistry(t)
	bare := testutil.SetupBareRepo(t)

	orig := &gitref.BranchRef{
		BareRepoPath: bare,
		Project:      "drem-orch",
		TaskID:       "task-old",
		Branch:       "feature/recycled",
	}
	require.NoError(t, reg.Register(ctx, orig))
	require.NoError(t, reg.MarkMerged(ctx, orig.ID))

	// Re-register after the previous incarnation has been merged.
	reused := &gitref.BranchRef{
		BareRepoPath: bare,
		Project:      "drem-orch",
		TaskID:       "task-new",
		Branch:       "feature/recycled",
	}
	require.NoError(t, reg.Register(ctx, reused),
		"registering over a merged row should succeed")
	require.Equal(t, orig.ID, reused.ID,
		"revival should reuse the existing primary key")
	require.Equal(t, gitref.StatusActive, reused.Status)
	require.Equal(t, "task-new", reused.TaskID)
}

func TestRegistry_MarkMerged_PersistsTransition(t *testing.T) {
	ctx := context.Background()
	reg, _ := newRegistry(t)
	bare := testutil.SetupBareRepo(t)

	ref := &gitref.BranchRef{
		BareRepoPath: bare,
		Project:      "drem-orch",
		Branch:       "feature/merge-me",
	}
	require.NoError(t, reg.Register(ctx, ref))
	require.NoError(t, reg.MarkMerged(ctx, ref.ID))

	got, err := reg.Get(ctx, ref.ID)
	require.NoError(t, err)
	require.Equal(t, gitref.StatusMerged, got.Status)
}

func TestRegistry_MarkDeleted_PersistsTransition(t *testing.T) {
	ctx := context.Background()
	reg, _ := newRegistry(t)
	bare := testutil.SetupBareRepo(t)

	ref := &gitref.BranchRef{
		BareRepoPath: bare,
		Project:      "drem-orch",
		Branch:       "feature/delete-me",
	}
	require.NoError(t, reg.Register(ctx, ref))
	require.NoError(t, reg.MarkDeleted(ctx, ref.ID))

	got, err := reg.Get(ctx, ref.ID)
	require.NoError(t, err)
	require.Equal(t, gitref.StatusDeleted, got.Status)
}

func TestRegistry_Get_NotFoundReturnsSentinel(t *testing.T) {
	ctx := context.Background()
	reg, _ := newRegistry(t)

	_, err := reg.Get(ctx, 999)
	require.Error(t, err)
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound),
		"Get for unknown id must return gorm.ErrRecordNotFound")
}

func TestRegistry_MarkMerged_UnknownIDReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	reg, _ := newRegistry(t)

	err := reg.MarkMerged(ctx, 42)
	require.Error(t, err)
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound))
}

func TestRegistry_FindByBranch_NotFoundReturnsSentinel(t *testing.T) {
	ctx := context.Background()
	reg, _ := newRegistry(t)
	bare := testutil.SetupBareRepo(t)

	_, err := reg.FindByBranch(ctx, bare, "feature/ghost")
	require.Error(t, err)
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound))
}

func TestRegistry_ListByProject_FiltersByStatus(t *testing.T) {
	ctx := context.Background()
	reg, _ := newRegistry(t)
	bare := testutil.SetupBareRepo(t)

	active := &gitref.BranchRef{
		BareRepoPath: bare,
		Project:      "drem-orch",
		Branch:       "feature/active-a",
	}
	merged := &gitref.BranchRef{
		BareRepoPath: bare,
		Project:      "drem-orch",
		Branch:       "feature/merged-a",
	}
	deleted := &gitref.BranchRef{
		BareRepoPath: bare,
		Project:      "drem-orch",
		Branch:       "feature/deleted-a",
	}
	otherProj := &gitref.BranchRef{
		BareRepoPath: bare,
		Project:      "drem-canvas",
		Branch:       "feature/canvas-a",
	}

	require.NoError(t, reg.Register(ctx, active))
	require.NoError(t, reg.Register(ctx, merged))
	require.NoError(t, reg.MarkMerged(ctx, merged.ID))
	require.NoError(t, reg.Register(ctx, deleted))
	require.NoError(t, reg.MarkDeleted(ctx, deleted.ID))
	require.NoError(t, reg.Register(ctx, otherProj))

	all, err := reg.ListByProject(ctx, "drem-orch", "")
	require.NoError(t, err)
	require.Len(t, all, 3, "empty status should return all rows for the project")

	onlyActive, err := reg.ListByProject(ctx, "drem-orch", gitref.StatusActive)
	require.NoError(t, err)
	require.Len(t, onlyActive, 1)
	require.Equal(t, "feature/active-a", onlyActive[0].Branch)

	onlyMerged, err := reg.ListByProject(ctx, "drem-orch", gitref.StatusMerged)
	require.NoError(t, err)
	require.Len(t, onlyMerged, 1)
	require.Equal(t, "feature/merged-a", onlyMerged[0].Branch)

	onlyDeleted, err := reg.ListByProject(ctx, "drem-orch", gitref.StatusDeleted)
	require.NoError(t, err)
	require.Len(t, onlyDeleted, 1)
	require.Equal(t, "feature/deleted-a", onlyDeleted[0].Branch)

	// Cross-project isolation.
	canvas, err := reg.ListByProject(ctx, "drem-canvas", "")
	require.NoError(t, err)
	require.Len(t, canvas, 1)
	require.Equal(t, "feature/canvas-a", canvas[0].Branch)
}

func TestRegistry_Register_RejectsEmptyRef(t *testing.T) {
	ctx := context.Background()
	reg, _ := newRegistry(t)

	require.Error(t, reg.Register(ctx, nil))

	empty := &gitref.BranchRef{Project: "p"}
	err := reg.Register(ctx, empty)
	require.Error(t, err)
	require.Contains(t, err.Error(), "required")
}
