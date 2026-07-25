package benchv2

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func crossHarnessTask() TaskSpec {
	return TaskSpec{
		Schema: TaskSchemaVersion, ID: "cross-harness", Title: "Cross-harness case",
		Status: "runnable", Mode: "direct_worker", InferencePolicy: "required", Weight: 1,
		AllowedToolPolicies: []string{ToolPolicyStructured, ToolPolicySandboxed},
		Role:                "coder", OracleID: "hidden",
		Fixture: Fixture{RepoID: "repo", BaseCommit: "base", VisibleBlobs: []BlobPin{{Path: "file", SHA: "blob"}}},
		Budget:  Budget{MaxInputTokens: 1, TimeoutSeconds: 1},
	}
}

func TestRunnableTaskSelectsDirectAndExternalContracts(t *testing.T) {
	task := crossHarnessTask()
	require.NoError(t, task.Validate())
	direct, err := SelectAdapter(HarnessConfig{Name: AdapterDirect, ToolPolicy: ToolPolicyStructured}, task, "http://inference")
	require.NoError(t, err)
	require.IsType(t, DirectToolAdapter{}, direct)

	external, err := SelectAdapter(HarnessConfig{
		Name: AdapterOpenCode, Version: "1.17.0", ToolPolicy: ToolPolicySandboxed, OuterIsolation: "outer_container",
	}, task, "http://unused")
	require.NoError(t, err)
	require.IsType(t, ExternalCLIAdapter{}, external)
	require.Equal(t, AdapterOpenCode, external.Name())
}

func TestExternalHarnessNeverFallsBackToDirectToolAgent(t *testing.T) {
	task := crossHarnessTask()
	adapter, err := SelectAdapter(HarnessConfig{
		Name: AdapterOpenCode, Version: "1.17.0", ToolPolicy: ToolPolicySandboxed, OuterIsolation: "outer_container",
	}, task, "http://direct")
	require.NoError(t, err)
	require.NotEqual(t, AdapterDirect, adapter.Name())

	_, err = SelectAdapter(HarnessConfig{Name: "unattested-custom-harness", ToolPolicy: ToolPolicyStructured}, task, "http://direct")
	require.ErrorContains(t, err, "unknown CanvasBench harness")
}

func TestAdapterSelectionRejectsPolicyMismatch(t *testing.T) {
	task := crossHarnessTask()
	_, err := SelectAdapter(HarnessConfig{Name: AdapterOpenCode, ToolPolicy: ToolPolicyStructured}, task, "")
	require.ErrorContains(t, err, ToolPolicySandboxed)
	_, err = SelectAdapter(HarnessConfig{Name: AdapterDirect, ToolPolicy: ToolPolicySandboxed}, task, "")
	require.ErrorContains(t, err, ToolPolicyStructured)
}
