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
