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

func TestTaskRejectsMalformedVisibleBlobPin(t *testing.T) {
	task := TaskSpec{
		Schema: TaskSchemaVersion, ID: "fixture", Title: "Fixture", OracleID: "fixture-v1",
		Status: "runnable", Mode: "direct_worker", InferencePolicy: "required",
		AllowedToolPolicies: []string{ToolPolicyStructured, ToolPolicySandboxed},
		Fixture:             Fixture{RepoID: "repo", BaseCommit: strings.Repeat("a", 40), VisibleBlobs: []BlobPin{{Path: "file.cpp", SHA: strings.Repeat("b", 41)}}},
		Budget:              Budget{MaxInputTokens: 1, TimeoutSeconds: 1},
	}
	require.ErrorContains(t, task.Validate(), "invalid visible blob pin")
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

func TestMatrixRejectsInvalidInferencePolicy(t *testing.T) {
	for _, mutate := range []func(*MatrixSpec){
		func(matrix *MatrixSpec) { matrix.Temperature = -0.1 },
		func(matrix *MatrixSpec) { matrix.TopP = 0 },
		func(matrix *MatrixSpec) { matrix.TopP = 1.1 },
		func(matrix *MatrixSpec) { matrix.TopK = -1 },
		func(matrix *MatrixSpec) { matrix.ContextWindow = 0 },
	} {
		matrix := validMatrix()
		mutate(&matrix)
		require.ErrorContains(t, matrix.Validate(), "invalid matrix")
	}
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
	matrix.Harness = selectableExternalHarness()
	matrix.Harness.SourceState = "source"
	matrix.Harness.ConfigSHA256 = "config"
	matrix.Harness.AdapterModelRef = "provider/model"
	matrix.Harness.InferenceEnvContract = "assumed-common-env"
	require.ErrorContains(t, matrix.Validate(), "external harness attestation")
}

func TestManifestAndTaskDigestsValidate(t *testing.T) {
	root := filepath.Join("..", "..", "bench", "canvasbench-v2")
	manifest, tasks, err := LoadManifest(filepath.Join(root, "manifest.json"))
	require.NoError(t, err)
	require.Len(t, tasks, 9)
	require.NoError(t, manifest.Validate())
	for _, task := range tasks {
		require.Equal(t, "runnable", task.Status)
	}
	require.Equal(t, "build/DremCanvas", tasks[8].ReleaseArtifactPath)
	require.Equal(t, "deterministic_exempt", tasks[7].InferencePolicy)
	require.Equal(t, []string{ToolPolicyReplay}, tasks[7].AllowedToolPolicies)
	for _, index := range []int{0, 1, 2, 3, 4, 5, 8} {
		require.Equal(t, "da8d567ea85a6ffc08e7a1ec0d3d7e49802306fc", tasks[index].Fixture.BaseCommit)
	}
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

func TestTaskRejectsEscapingReleaseArtifactPath(t *testing.T) {
	task := TaskSpec{
		Schema: TaskSchemaVersion, ID: "release", Title: "Release", OracleID: "release-v1",
		Status: "placeholder", Mode: "direct_worker", InferencePolicy: "required",
		AllowedToolPolicies: []string{ToolPolicyStructured, ToolPolicySandboxed},
		ReleaseArtifactPath: "../DremCanvas",
	}
	require.ErrorContains(t, task.Validate(), "invalid release artifact path")
}

func TestCapstoneRequiresReleaseArtifact(t *testing.T) {
	task := TaskSpec{
		Schema: TaskSchemaVersion, ID: "case-09", Title: "Capstone", OracleID: "take-cycling-capstone-canonical-v1",
		Status: "placeholder", Mode: "direct_worker", InferencePolicy: "required",
		AllowedToolPolicies: []string{ToolPolicyStructured, ToolPolicySandboxed},
	}
	require.ErrorContains(t, task.Validate(), "must require a Release artifact")
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

func TestCase9DoesNotClaimUnmeasuredUIOrReworkEvidence(t *testing.T) {
	root := filepath.Join("..", "..", "bench", "canvasbench-v2")
	_, tasks, err := LoadManifest(filepath.Join(root, "manifest.json"))
	require.NoError(t, err)
	case9 := tasks[8]
	publicContract := strings.ToLower(strings.Join([]string{case9.Description, case9.SystemPrompt, case9.UserMessage}, "\n"))
	for _, forbidden := range []string{"computer use", "ui proof", "ui verification", "ownership-aware rework", "orchestrated rework"} {
		require.NotContains(t, publicContract, forbidden)
	}
	raw := string(MarshalResult(TrialResult{Schema: ResultSchemaVersion, TaskID: "case-09"}))
	require.NotContains(t, raw, "computer_use")
	require.NotContains(t, raw, "rework")
}
