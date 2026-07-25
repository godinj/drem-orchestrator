package benchv2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	TaskSchemaVersion     = "canvasbench.task.v2"
	MatrixSchemaVersion   = "canvasbench.matrix.v2"
	ResultSchemaVersion   = "canvasbench.result.v2"
	ManifestSchemaVersion = "canvasbench.manifest.v2"
	ToolPolicyStructured  = "structured_only"
	ToolPolicySandboxed   = "sandboxed_shell"
	ToolPolicyReplay      = "deterministic_replay"
)

type ManifestCase struct {
	ID       string `json:"id"`
	TaskFile string `json:"task_file"`
	SHA256   string `json:"sha256"`
	Weight   int    `json:"weight"`
	Status   string `json:"status"`
	HardGate bool   `json:"hard_gate"`
}

type ManifestSpec struct {
	Schema    string         `json:"schema"`
	SuiteID   string         `json:"suite_id"`
	Threshold float64        `json:"threshold"`
	Cases     []ManifestCase `json:"cases"`
}

type BlobPin struct {
	Path string `json:"path"`
	SHA  string `json:"git_blob_sha"`
}

type OracleArtifactPin struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Fixture struct {
	RepoID       string    `json:"repo_id"`
	BaseCommit   string    `json:"base_commit"`
	VisibleBlobs []BlobPin `json:"visible_blobs"`
	SeedPatch    string    `json:"seed_patch,omitempty"`
	SeedPatchSHA string    `json:"seed_patch_sha256,omitempty"`
}

type Budget struct {
	MaxInputTokens  int `json:"max_input_tokens"`
	MaxOutputTokens int `json:"max_output_tokens"`
	MaxToolCalls    int `json:"max_tool_calls"`
	MaxIterations   int `json:"max_iterations"`
	TimeoutSeconds  int `json:"timeout_seconds"`
}

type TaskSpec struct {
	Schema               string              `json:"schema"`
	ID                   string              `json:"id"`
	Title                string              `json:"title"`
	Description          string              `json:"description"`
	Status               string              `json:"status"`
	Mode                 string              `json:"mode"`
	InferencePolicy      string              `json:"inference_policy"`
	AllowedToolPolicies  []string            `json:"allowed_tool_policies"`
	Weight               int                 `json:"weight"`
	Role                 string              `json:"role"`
	SystemPrompt         string              `json:"system_prompt"`
	UserMessage          string              `json:"user_message"`
	Fixture              Fixture             `json:"fixture"`
	ReadPaths            []string            `json:"read_paths"`
	WritePaths           []string            `json:"write_paths"`
	RequiredChangedPaths []string            `json:"required_changed_paths"`
	ResultArtifact       string              `json:"result_artifact,omitempty"`
	ReleaseArtifactPath  string              `json:"release_artifact_path,omitempty"`
	RequiredMutation     bool                `json:"required_mutation"`
	OracleID             string              `json:"oracle_id"`
	OracleArtifacts      []OracleArtifactPin `json:"oracle_artifacts,omitempty"`
	Budget               Budget              `json:"budget"`
}

type HarnessConfig struct {
	Name                  string `json:"name"`
	Version               string `json:"version"`
	SourceState           string `json:"source_state"`
	ConfigSHA256          string `json:"config_sha256"`
	HistoryMode           string `json:"history_mode"`
	ToolPolicy            string `json:"tool_policy"`
	KeepRecentExchanges   int    `json:"keep_recent_exchanges"`
	RetentionThresholdPC  int    `json:"retention_threshold_pct"`
	AdapterModelRef       string `json:"adapter_model_ref"`
	OuterIsolation        string `json:"outer_isolation"`
	OuterImage            string `json:"outer_image,omitempty"`
	OuterNetworkPolicy    string `json:"outer_network_policy,omitempty"`
	OuterNetworkName      string `json:"outer_network_name,omitempty"`
	TrajectoryNormalizer  string `json:"trajectory_normalizer"`
	InferenceEnvContract  string `json:"inference_env_contract,omitempty"`
	UsageProxySourceState string `json:"usage_proxy_source_state,omitempty"`
	UsageProxyImage       string `json:"usage_proxy_image,omitempty"`
	UsageProxyConfigSHA   string `json:"usage_proxy_config_sha256,omitempty"`
}

type RuntimeAttestation struct {
	BackendName       string `json:"backend_name"`
	BackendVersion    string `json:"backend_version"`
	ModelID           string `json:"model_id"`
	ModelSHA256       string `json:"model_sha256"`
	Quantization      string `json:"quantization"`
	RuntimeConfigSHA  string `json:"runtime_config_sha256"`
	ImageDigest       string `json:"image_digest"`
	EndpointClass     string `json:"endpoint_class"`
	APIFlavor         string `json:"api_flavor"`
	InferenceMeasured bool   `json:"inference_measured"`
}

type MatrixSpec struct {
	Schema           string             `json:"schema"`
	ID               string             `json:"id"`
	TaskFiles        []string           `json:"task_files"`
	Trials           int                `json:"trials"`
	Seeds            []int64            `json:"seeds"`
	Temperature      float64            `json:"temperature"`
	TopP             float64            `json:"top_p"`
	TopK             int                `json:"top_k"`
	ContextWindow    int                `json:"context_window"`
	PreserveThinking bool               `json:"preserve_thinking"`
	SeedPolicy       string             `json:"seed_policy"`
	Harness          HarnessConfig      `json:"harness"`
	Runtime          RuntimeAttestation `json:"runtime"`
}

type Telemetry struct {
	TokensIn               int   `json:"tokens_in"`
	TokensOut              int   `json:"tokens_out"`
	Iterations             int   `json:"iterations"`
	ToolCalls              int   `json:"tool_calls"`
	FoldedBytes            int   `json:"folded_bytes"`
	PeakRequestInput       int   `json:"peak_request_input"`
	PeakContextPct         int   `json:"peak_context_pct"`
	UniqueReads            int   `json:"unique_reads"`
	RedundantReads         int   `json:"redundant_reads"`
	DeniedCalls            int   `json:"denied_calls"`
	FirstMutationIteration int   `json:"first_mutation_iteration"`
	FirstMutationMs        int64 `json:"first_mutation_ms"`
	DurationMs             int64 `json:"duration_ms"`
	MutationObserved       bool  `json:"mutation_observed"`
	CheckpointObserved     bool  `json:"checkpoint_observed"`
}

type ServerUsage struct {
	Source           string                 `json:"source"`
	CorrelationID    string                 `json:"correlation_id,omitempty"`
	RequestsMeasured int                    `json:"requests_measured"`
	RequestsRejected int                    `json:"requests_rejected,omitempty"`
	Rejections       []ServerUsageRejection `json:"rejections,omitempty"`
	RequestsTotal    int                    `json:"requests_total"`
	PromptTokens     int                    `json:"prompt_tokens"`
	CompletionTokens int                    `json:"completion_tokens"`
	Complete         bool                   `json:"complete"`
}

type ServerUsageRejection struct {
	HTTPStatus int `json:"http_status"`
	Count      int `json:"count"`
}

type Gates struct {
	VerifierPassed         bool     `json:"verifier_passed"`
	Compiled               bool     `json:"compiled"`
	ScopePassed            bool     `json:"scope_passed"`
	ReadScopePassed        bool     `json:"read_scope_passed"`
	OracleIsolated         bool     `json:"oracle_isolated"`
	Attested               bool     `json:"attested"`
	RequiredMutationPassed bool     `json:"required_mutation_passed"`
	ArtifactAttested       bool     `json:"artifact_attested"`
	ChangedPaths           []string `json:"changed_paths"`
	Failures               []string `json:"failures"`
}

type ArtifactEvidence struct {
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

type TrialResult struct {
	Schema          string             `json:"schema"`
	RunID           string             `json:"run_id"`
	MatrixID        string             `json:"matrix_id"`
	TaskID          string             `json:"task_id"`
	Trial           int                `json:"trial"`
	Seed            int64              `json:"seed"`
	Status          string             `json:"status"`
	Score           float64            `json:"score"`
	Harness         HarnessConfig      `json:"harness"`
	Runtime         RuntimeAttestation `json:"runtime"`
	Fixture         Fixture            `json:"fixture"`
	Telemetry       Telemetry          `json:"telemetry"`
	ServerUsage     ServerUsage        `json:"server_usage"`
	Trajectory      ATIFTrajectory     `json:"trajectory"`
	Gates           Gates              `json:"gates"`
	ReleaseArtifact *ArtifactEvidence  `json:"release_artifact,omitempty"`
	StopReason      string             `json:"stop_reason"`
	Error           string             `json:"error,omitempty"`
	CodexTokens     *int               `json:"codex_tokens,omitempty"`
}

func DecodeStrictFile(path string, value any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return DecodeStrict(raw, value)
}

func LoadManifest(path string) (ManifestSpec, []TaskSpec, error) {
	var manifest ManifestSpec
	if err := DecodeStrictFile(path, &manifest); err != nil {
		return manifest, nil, err
	}
	if err := manifest.Validate(); err != nil {
		return manifest, nil, err
	}
	root := filepath.Dir(path)
	tasks := make([]TaskSpec, 0, len(manifest.Cases))
	for _, item := range manifest.Cases {
		taskPath := filepath.Join(root, filepath.FromSlash(item.TaskFile))
		raw, err := os.ReadFile(taskPath)
		if err != nil {
			return manifest, nil, err
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(raw))
		if digest != item.SHA256 {
			return manifest, nil, fmt.Errorf("manifest digest mismatch for %s", item.ID)
		}
		var task TaskSpec
		if err := DecodeStrict(raw, &task); err != nil {
			return manifest, nil, err
		}
		if task.ID != item.ID || task.Weight != item.Weight || task.Status != item.Status {
			return manifest, nil, fmt.Errorf("manifest metadata mismatch for %s", item.ID)
		}
		for _, artifact := range task.OracleArtifacts {
			artifactRaw, err := os.ReadFile(filepath.Join(root, "oracles", artifact.Path))
			if err != nil {
				return manifest, nil, fmt.Errorf("read oracle artifact for %s: %w", item.ID, err)
			}
			artifactDigest := fmt.Sprintf("%x", sha256.Sum256(artifactRaw))
			if artifactDigest != artifact.SHA256 {
				return manifest, nil, fmt.Errorf("oracle artifact digest mismatch for %s", item.ID)
			}
		}
		if task.Fixture.SeedPatch != "" && !filepath.IsAbs(task.Fixture.SeedPatch) {
			task.Fixture.SeedPatch = filepath.Join(filepath.Dir(taskPath), task.Fixture.SeedPatch)
		}
		if err := task.Validate(); err != nil {
			return manifest, nil, err
		}
		tasks = append(tasks, task)
	}
	return manifest, tasks, nil
}

func DecodeStrict(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON")
	}
	return nil
}

func (task TaskSpec) Validate() error {
	if task.Schema != TaskSchemaVersion || task.ID == "" || task.Title == "" || task.OracleID == "" {
		return fmt.Errorf("invalid task identity or schema")
	}
	if task.Status != "runnable" && task.Status != "placeholder" {
		return fmt.Errorf("task %s has invalid status %q", task.ID, task.Status)
	}
	if task.InferencePolicy != "required" && task.InferencePolicy != "deterministic_exempt" {
		return fmt.Errorf("task %s has invalid inference_policy", task.ID)
	}
	if task.Mode == "deterministic_replay" && task.InferencePolicy != "deterministic_exempt" {
		return fmt.Errorf("deterministic replay must declare deterministic_exempt")
	}
	allowed := map[string]bool{}
	for _, policy := range task.AllowedToolPolicies {
		if policy != ToolPolicyStructured && policy != ToolPolicySandboxed && policy != ToolPolicyReplay {
			return fmt.Errorf("task %s has invalid allowed tool policy %q", task.ID, policy)
		}
		if allowed[policy] {
			return fmt.Errorf("task %s repeats allowed tool policy %q", task.ID, policy)
		}
		allowed[policy] = true
	}
	if task.Mode == "deterministic_replay" && (len(allowed) != 1 || !allowed[ToolPolicyReplay]) {
		return fmt.Errorf("deterministic replay must allow only deterministic_replay")
	}
	if task.InferencePolicy == "required" && (!allowed[ToolPolicyStructured] || !allowed[ToolPolicySandboxed]) {
		return fmt.Errorf("inference task %s must allow structured_only and sandboxed_shell", task.ID)
	}
	if task.Status == "runnable" {
		if task.Fixture.RepoID == "" || task.Fixture.BaseCommit == "" || len(task.Fixture.VisibleBlobs) == 0 {
			return fmt.Errorf("runnable task %s lacks pinned fixture", task.ID)
		}
		if task.Budget.MaxInputTokens <= 0 || task.Budget.TimeoutSeconds <= 0 {
			return fmt.Errorf("runnable task %s lacks fixed budgets", task.ID)
		}
	}
	seenBlobs := map[string]bool{}
	for _, blob := range task.Fixture.VisibleBlobs {
		digest, decodeErr := hex.DecodeString(blob.SHA)
		clean := filepath.Clean(blob.Path)
		if blob.Path == "" || filepath.IsAbs(blob.Path) || clean == "." || clean == ".." ||
			strings.HasPrefix(clean, ".."+string(filepath.Separator)) || decodeErr != nil || len(digest) != 20 ||
			blob.SHA != strings.ToLower(blob.SHA) || seenBlobs[blob.Path] {
			return fmt.Errorf("task %s has an invalid visible blob pin", task.ID)
		}
		seenBlobs[blob.Path] = true
	}
	seenArtifacts := map[string]bool{}
	for _, artifact := range task.OracleArtifacts {
		digest, decodeErr := hex.DecodeString(artifact.SHA256)
		if artifact.Path == "" || filepath.Base(artifact.Path) != artifact.Path || decodeErr != nil || len(digest) != sha256.Size || seenArtifacts[artifact.Path] {
			return fmt.Errorf("task %s has an invalid oracle artifact pin", task.ID)
		}
		seenArtifacts[artifact.Path] = true
	}
	if task.ReleaseArtifactPath != "" {
		clean := filepath.Clean(task.ReleaseArtifactPath)
		if filepath.IsAbs(task.ReleaseArtifactPath) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("task %s has an invalid release artifact path", task.ID)
		}
	}
	if task.OracleID == "take-cycling-capstone-canonical-v1" && task.ReleaseArtifactPath == "" {
		return fmt.Errorf("task %s must require a Release artifact", task.ID)
	}
	return nil
}

func (manifest ManifestSpec) Validate() error {
	if manifest.Schema != ManifestSchemaVersion || manifest.SuiteID == "" || manifest.Threshold != 90 || len(manifest.Cases) != 9 {
		return fmt.Errorf("invalid CanvasBench v2 manifest identity or threshold")
	}
	weight := 0
	hard := map[string]bool{}
	seen := map[string]bool{}
	for _, item := range manifest.Cases {
		if item.ID == "" || item.TaskFile == "" || len(item.SHA256) != 64 || seen[item.ID] {
			return fmt.Errorf("invalid or duplicate manifest case")
		}
		seen[item.ID] = true
		weight += item.Weight
		if item.HardGate {
			hard[item.ID] = true
		}
	}
	if weight != 100 || !hard["case-08"] || !hard["case-09"] {
		return fmt.Errorf("manifest weights or mandatory hard gates are invalid")
	}
	return nil
}

func (matrix MatrixSpec) Validate() error {
	if matrix.Schema != MatrixSchemaVersion || matrix.ID == "" || matrix.Trials <= 0 || len(matrix.TaskFiles) == 0 ||
		matrix.Temperature < 0 || matrix.TopP <= 0 || matrix.TopP > 1 || matrix.TopK < 0 || matrix.ContextWindow <= 0 ||
		matrix.SeedPolicy != "fixed_per_trial" {
		return fmt.Errorf("invalid matrix identity, schema, or trials")
	}
	if len(matrix.Seeds) < matrix.Trials {
		return fmt.Errorf("matrix needs one fixed seed per trial")
	}
	return ValidateAttestation(matrix.Harness, matrix.Runtime)
}

func ValidateAttestation(h HarnessConfig, r RuntimeAttestation) error {
	if h.Name == "" || h.Version == "" || h.SourceState == "" || h.ConfigSHA256 == "" || h.ToolPolicy == "" ||
		h.AdapterModelRef == "" || h.OuterIsolation == "" || h.TrajectoryNormalizer == "" {
		return fmt.Errorf("missing harness attestation")
	}
	if r.BackendName == "" || r.BackendVersion == "" || r.ModelID == "" || r.ModelSHA256 == "" ||
		r.Quantization == "" || r.RuntimeConfigSHA == "" || r.ImageDigest == "" || r.EndpointClass == "" || r.APIFlavor == "" {
		return fmt.Errorf("missing runtime/model/image attestation")
	}
	if !r.InferenceMeasured {
		return fmt.Errorf("inference telemetry is unmeasured")
	}
	if h.Name == AdapterMiniSWE && !validMiniSWEModelRef(h.AdapterModelRef) {
		return fmt.Errorf("mini-SWE adapter model reference must use openai/<served-model>")
	}
	if h.Name == AdapterOpenCode || h.Name == AdapterQwenCode || h.Name == AdapterMiniSWE || h.Name == AdapterPi {
		if h.ToolPolicy != ToolPolicySandboxed || h.OuterIsolation != "outer_container" || !pinnedOCIImage.MatchString(h.OuterImage) ||
			h.OuterNetworkPolicy != OuterNetworkIsolatedInference || h.OuterNetworkName == "" || h.OuterNetworkName == "host" ||
			h.OuterNetworkName == "bridge" || h.OuterNetworkName == "default" || h.TrajectoryNormalizer != normalizerForAdapter(h.Name) ||
			h.InferenceEnvContract != inferenceEnvContractForAdapter(h.Name) || h.UsageProxySourceState == "" ||
			!pinnedOCIImage.MatchString(h.UsageProxyImage) || !validSHA256Text(h.UsageProxyConfigSHA) {
			return fmt.Errorf("external harness attestation is incomplete or mismatched")
		}
	}
	return nil
}

func validMiniSWEModelRef(value string) bool {
	return strings.HasPrefix(value, "openai/") && strings.TrimPrefix(value, "openai/") != ""
}

func validSHA256Text(value string) bool {
	digest, err := hex.DecodeString(value)
	return err == nil && len(digest) == sha256.Size && value == strings.ToLower(value)
}
