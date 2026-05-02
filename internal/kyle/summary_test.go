package kyle_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/kyle"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// TestRenderSummary_Golden compares the rendered digest against the
// committed golden file. Regenerating it is intentional friction: a
// summary change is a public-surface change and should be a deliberate
// diff to review.
func TestRenderSummary_Golden(t *testing.T) {
	now := time.Date(2026, 4, 19, 15, 22, 0, 0, time.UTC)
	snapshots := []kyle.ProjectSnapshot{
		{
			Project: "drem-orchestrator",
			Workers: []orchdto.WorkerDTO{
				{AgentType: "coder-go", Status: "working"},
				{AgentType: "coder-go", Status: "working"},
				{AgentType: "merger", Status: "working"},
			},
			Tasks: []orchdto.TaskDTO{
				{Status: "planning"}, {Status: "planning"}, {Status: "planning"}, {Status: "planning"},
				{Status: "in_progress"}, {Status: "in_progress"}, {Status: "in_progress"}, {Status: "in_progress"}, {Status: "in_progress"},
				{Status: "merging"}, {Status: "merging"}, {Status: "merging"},
			},
			RecentEvents: []orchdto.EventDTO{
				{Type: "commit"}, {Type: "commit"}, {Type: "merge_success"},
			},
			FetchedAt: now,
		},
		{
			Project: "drem-other",
			Workers: []orchdto.WorkerDTO{
				{AgentType: "coder-go", Status: "failed"},
			},
			RecentEvents: []orchdto.EventDTO{{Type: "agent_crash"}},
			FetchedAt:    now,
		},
	}

	got := kyle.RenderSummary(now, snapshots)

	goldenPath := filepath.Join("testdata", "summary_basic.txt")
	if update := os.Getenv("UPDATE_GOLDEN"); update != "" {
		require.NoError(t, os.WriteFile(goldenPath, []byte(got), 0o644))
	}
	want, err := os.ReadFile(goldenPath)
	require.NoError(t, err)
	require.Equal(t, string(want), got)
}

// TestRenderSummary_EmptyRegistry exercises the "(no registered projects)"
// branch so it doesn't silently regress.
func TestRenderSummary_EmptyRegistry(t *testing.T) {
	now := time.Date(2026, 4, 19, 15, 22, 0, 0, time.UTC)
	got := kyle.RenderSummary(now, nil)
	require.Contains(t, got, "no registered projects")
}

// TestRenderSummary_HealthStates ensures the health field collapses
// stale/degraded/ok to the expected one-word labels.
func TestRenderSummary_HealthStates(t *testing.T) {
	now := time.Date(2026, 4, 19, 15, 22, 0, 0, time.UTC)
	cases := []struct {
		snap kyle.ProjectSnapshot
		want string
	}{
		{kyle.ProjectSnapshot{Project: "a", FetchedAt: now}, "OK"},
		{kyle.ProjectSnapshot{Project: "b", FetchedAt: now, LastError: "nope"}, "DEGRADED"},
		{kyle.ProjectSnapshot{Project: "c", FetchedAt: now, Stale: true}, "STALE"},
	}
	for _, tc := range cases {
		got := kyle.RenderSummary(now, []kyle.ProjectSnapshot{tc.snap})
		require.Contains(t, got, "health:     "+tc.want, "snapshot %+v", tc.snap)
	}
	// Marshal check: these structures must be JSON-encodable so the
	// /world endpoint returns them verbatim. If a future field breaks
	// json.Marshal this fails loudly here.
	for _, tc := range cases {
		_, err := json.Marshal(tc.snap)
		require.NoError(t, err)
	}
}

func TestRenderSummary_IncludesTaskFailureAttentionLine(t *testing.T) {
	now := time.Date(2026, 4, 19, 15, 22, 0, 0, time.UTC)
	current := true
	historical := false
	snap := kyle.ProjectSnapshot{
		Project: "diagnostics",
		Tasks: []orchdto.TaskDTO{
			{
				Title:                "merge the thing",
				Status:               "failed",
				CurrentHealth:        "failed",
				LatestFailureSummary: "merge conflict in api.go",
				LatestFailureCurrent: &current,
			},
			{
				Title:                "ask operator",
				Status:               "plan_review",
				CurrentHealth:        "needs_attention",
				LatestFailureSummary: "planner needs scope decision",
				LatestFailureCurrent: &current,
			},
			{
				Title:                "old flakes",
				Status:               "in_progress",
				LatestFailureSummary: "historical test flake",
				LatestFailureCurrent: &historical,
			},
		},
		FetchedAt: now,
	}

	got := kyle.RenderSummary(now, []kyle.ProjectSnapshot{snap})

	require.Contains(t, got, "attention:  failure merge the thing: merge conflict in api.go; needs-attention ask operator: planner needs scope decision")
	require.NotContains(t, got, "old flakes")
}

func TestRenderSummary_IncludesStaleActiveTaskAttentionLine(t *testing.T) {
	now := time.Date(2026, 4, 19, 15, 22, 0, 0, time.UTC)
	snap := kyle.ProjectSnapshot{
		Project: "diagnostics",
		Tasks: []orchdto.TaskDTO{
			{
				Title:     "write the plan",
				Status:    "planning",
				UpdatedAt: now.Add(-5 * time.Hour),
			},
			{
				ID:        "task-2",
				Status:    "in_progress",
				UpdatedAt: now.Add(-26 * time.Hour),
			},
			{
				Title:     "fresh tests",
				Status:    "testing_ready",
				UpdatedAt: now.Add(-30 * time.Minute),
			},
		},
		FetchedAt: now,
	}

	got := kyle.RenderSummary(now, []kyle.ProjectSnapshot{snap})

	require.Contains(t, got, "attention:  stuck write the plan: planning for 5h; stuck task-2: in_progress for 26h")
	require.NotContains(t, got, "fresh tests")
}

func TestRenderSummary_StaleActiveStatuses(t *testing.T) {
	now := time.Date(2026, 4, 19, 15, 22, 0, 0, time.UTC)
	for _, status := range []string{"planning", "test_writing", "in_progress", "testing_ready", "merging", "classifying"} {
		snap := kyle.ProjectSnapshot{
			Project:   "diagnostics",
			Tasks:     []orchdto.TaskDTO{{Title: "old task", Status: status, UpdatedAt: now.Add(-4 * time.Hour)}},
			FetchedAt: now,
		}

		got := kyle.RenderSummary(now, []kyle.ProjectSnapshot{snap})

		require.Contains(t, got, "attention:  stuck old task: "+status+" for 4h")
	}
}

func TestRenderSummary_DoesNotMarkHumanReviewGatesAsStale(t *testing.T) {
	now := time.Date(2026, 4, 19, 15, 22, 0, 0, time.UTC)
	snap := kyle.ProjectSnapshot{
		Project: "diagnostics",
		Tasks: []orchdto.TaskDTO{
			{Title: "review plan", Status: "plan_review", UpdatedAt: now.Add(-24 * time.Hour)},
			{Title: "review tests", Status: "test_review", UpdatedAt: now.Add(-24 * time.Hour)},
		},
		FetchedAt: now,
	}

	got := kyle.RenderSummary(now, []kyle.ProjectSnapshot{snap})

	require.NotContains(t, got, "attention:")
}

func TestRenderSummary_NoAttentionLineWithoutStaleOrFailureTasks(t *testing.T) {
	now := time.Date(2026, 4, 19, 15, 22, 0, 0, time.UTC)
	snap := kyle.ProjectSnapshot{
		Project: "diagnostics",
		Tasks: []orchdto.TaskDTO{
			{Title: "fresh work", Status: "in_progress", UpdatedAt: now.Add(-time.Hour)},
		},
		FetchedAt: now,
	}

	got := kyle.RenderSummary(now, []kyle.ProjectSnapshot{snap})

	require.NotContains(t, got, "attention:")
}
