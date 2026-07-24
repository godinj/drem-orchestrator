package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMaterializedIntegrationContextRetainsReadScopeAndNarrowsWriteScope(t *testing.T) {
	plan := planEntry{
		Title: "assemble", Phase: "integration",
		Files:         []string{"src/ui/ActionCoordinator.cpp", "cmake/DremCanvasSources.cmake"},
		WritableFiles: []string{"cmake/DremCanvasSources.cmake"},
	}
	ctx, err := materializedSubtaskContext(plan, []planEntry{plan}, 0, nil)
	require.NoError(t, err)
	require.Equal(t, plan.Files, ctx["estimated_files"])
	require.Equal(t, plan.WritableFiles, ctx["writable_files"])
}
