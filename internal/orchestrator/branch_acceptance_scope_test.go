package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestBranchAcceptanceScopesUsesWritableFilesAndPreservesLegacyFallback(t *testing.T) {
	narrow := &model.Task{Context: model.JSONField{
		"estimated_files": []any{"src/ui/ActionCoordinator.cpp", "cmake/DremCanvasSources.cmake"},
		"writable_files":  []any{"cmake/DremCanvasSources.cmake"},
	}}
	require.Equal(t, []string{"cmake/DremCanvasSources.cmake"}, branchAcceptanceScopes(narrow))

	legacy := &model.Task{Context: model.JSONField{"estimated_files": []any{"src/legacy.cpp"}}}
	require.Equal(t, []string{"src/legacy.cpp"}, branchAcceptanceScopes(legacy))
}
