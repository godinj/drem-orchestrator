package benchv2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func validHarness() HarnessConfig {
	return HarnessConfig{Name: AdapterDirect, Version: "1", SourceState: "abc", ConfigSHA256: "cfg", HistoryMode: "retain_recent", ToolPolicy: ToolPolicyStructured, KeepRecentExchanges: 4, RetentionThresholdPC: 70, AdapterModelRef: "provider/model", OuterIsolation: "outer_container", TrajectoryNormalizer: "direct-v2"}
}

func validRuntime() RuntimeAttestation {
	return RuntimeAttestation{BackendName: "vllm", BackendVersion: "1", ModelID: "model", ModelSHA256: "model-sha", Quantization: "int4", RuntimeConfigSHA: "runtime-sha", ImageDigest: "sha256:image", EndpointClass: "local", APIFlavor: "openai-chat", InferenceMeasured: true}
}

func validMatrix() MatrixSpec {
	return MatrixSpec{Schema: MatrixSchemaVersion, ID: "matrix", TaskFiles: []string{"task.json"}, Trials: 2, Seeds: []int64{1, 2}, Temperature: .6, TopP: .95, TopK: 20, ContextWindow: 131072, PreserveThinking: true, SeedPolicy: "fixed_per_trial", Harness: validHarness(), Runtime: validRuntime()}
}

func TestStrictDecodersRejectUnknownFields(t *testing.T) {
	var task TaskSpec
	require.Error(t, DecodeStrict([]byte(`{"schema":"canvasbench.task.v2","unknown":true}`), &task))
	var matrix MatrixSpec
	require.Error(t, DecodeStrict([]byte(`{"schema":"canvasbench.matrix.v2","unknown":true}`), &matrix))
	var result TrialResult
	require.Error(t, DecodeStrict([]byte(`{"schema":"canvasbench.result.v2","unknown":true}`), &result))
}

func TestTaskRejectsNonHexOracleArtifactDigest(t *testing.T) {
	task := TaskSpec{
		Schema: TaskSchemaVersion, ID: "oracle", Title: "Oracle", OracleID: "oracle-v1",
		Status: "placeholder", Mode: "direct_worker", InferencePolicy: "required",
		AllowedToolPolicies: []string{ToolPolicyStructured, ToolPolicySandboxed},
		OracleArtifacts:     []OracleArtifactPin{{Path: "oracle.patch", SHA256: strings.Repeat("z", 64)}},
	}
	require.ErrorContains(t, task.Validate(), "invalid oracle artifact pin")
}

func TestAttestationFailsClosed(t *testing.T) {
	matrix := validMatrix()
	require.NoError(t, matrix.Validate())
	matrix.Runtime.InferenceMeasured = false
	require.ErrorContains(t, matrix.Validate(), "unmeasured")
	matrix = validMatrix()
	matrix.Harness.SourceState = ""
	require.ErrorContains(t, matrix.Validate(), "harness attestation")
}

func TestExternalAttestationRequiresPinnedOuterRuntime(t *testing.T) {
	matrix := validMatrix()
	matrix.Harness = selectableExternalHarness()
	matrix.Harness.SourceState = "source"
	matrix.Harness.ConfigSHA256 = "config"
	matrix.Harness.AdapterModelRef = "provider/model"
	matrix.Harness.TrajectoryNormalizer = NormalizerOpenCode
	require.NoError(t, matrix.Validate())
	matrix.Harness.OuterImage = "opencode:latest"
	require.ErrorContains(t, matrix.Validate(), "external harness attestation")
}

func TestManifestAndTaskDigestsValidate(t *testing.T) {
	root := filepath.Join("..", "..", "bench", "canvasbench-v2")
	manifest, tasks, err := LoadManifest(filepath.Join(root, "manifest.json"))
	require.NoError(t, err)
	require.Len(t, tasks, 9)
	require.NoError(t, manifest.Validate())
	for _, task := range tasks[:8] {
		require.Equal(t, "runnable", task.Status)
	}
	require.Equal(t, "placeholder", tasks[8].Status)
	require.Equal(t, "deterministic_exempt", tasks[7].InferencePolicy)
	require.Equal(t, []string{ToolPolicyReplay}, tasks[7].AllowedToolPolicies)
	for _, task := range tasks[:3] {
		require.ElementsMatch(t, []string{ToolPolicyStructured, ToolPolicySandboxed}, task.AllowedToolPolicies)
	}
	for _, task := range append(append([]TaskSpec{}, tasks[3:7]...), tasks[8]) {
		require.ElementsMatch(t, []string{ToolPolicyStructured, ToolPolicySandboxed}, task.AllowedToolPolicies)
	}

	temp := t.TempDir()
	raw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(temp, "manifest.json"), raw, 0o644))
	_, _, err = LoadManifest(filepath.Join(temp, "manifest.json"))
	require.Error(t, err, "moving the manifest without its immutable task corpus must fail")
}

func TestPublishedSchemasAreStrictObjects(t *testing.T) {
	for _, name := range []string{"task", "matrix", "result", "manifest"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "bench", "canvasbench-v2", "schemas", name+".schema.json"))
		require.NoError(t, err)
		var schema map[string]any
		require.NoError(t, DecodeStrict(raw, &schema))
		require.Equal(t, false, schema["additionalProperties"])
	}
}
