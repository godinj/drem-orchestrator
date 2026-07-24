package projects_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/projects"
)

// TestRenderConfig_EnablesDirectClassifier asserts that the generated
// drem.toml sets [agents.classifier].direct=true and overrides the
// direct-tool endpoint to gq on drem-net. These are the two flags the
// T1 canary proved necessary inside the containerized orch (see
// plans/install-log.md iteration notes).
func TestRenderConfig_EnablesDirectClassifier(t *testing.T) {
	data := projects.TemplateData{
		ProjectName:  "drem-orchestrator",
		Language:     projects.LanguageGo,
		BareRepoPath: "/home/dev/git/drem-orchestrator.git",
	}
	out, err := projects.RenderConfig(data)
	require.NoError(t, err)
	s := string(out)

	require.Contains(t, s, "bare_repo_path = \"/home/dev/git/drem-orchestrator.git\"")
	require.Contains(t, s, "[agents.classifier]")
	require.Regexp(t, `direct\s*=\s*true`, s)
	require.Contains(t, s, "[direct_tool_agent]")
	require.Contains(t, s, "endpoint = \"http://gq:8090/v1/chat/completions\"")

	// Round-trip through the TOML parser so a broken template is caught
	// at test time, not at container start.
	var parsed struct {
		BareRepoPath string `toml:"bare_repo_path"`
		Project      struct {
			Language string `toml:"language"`
		} `toml:"project"`
		Agents struct {
			Classifier struct {
				Direct   bool   `toml:"direct"`
				Endpoint string `toml:"endpoint"`
				Model    string `toml:"model"`
			} `toml:"classifier"`
			Coder struct {
				Provider string `toml:"provider"`
				Model    string `toml:"model"`
				Effort   string `toml:"effort"`
			} `toml:"coder"`
		} `toml:"agents"`
		DirectToolAgent struct {
			Endpoint                                   string         `toml:"endpoint"`
			Model                                      string         `toml:"model"`
			MaxIterations                              int            `toml:"max_iterations"`
			MaxCumulativeInputTokens                   int            `toml:"max_cumulative_input_tokens"`
			TestMaxCumulativeInputTokens               int            `toml:"test_max_cumulative_input_tokens"`
			ImplementationMaxCumulativeInputTokens     int            `toml:"implementation_max_cumulative_input_tokens"`
			IntegrationMaxCumulativeInputTokens        int            `toml:"integration_max_cumulative_input_tokens"`
			ReviewMaxCumulativeInputTokens             int            `toml:"review_max_cumulative_input_tokens"`
			MaxReadsBeforeMutation                     int            `toml:"max_reads_before_mutation"`
			MaxToolCalls                               int            `toml:"max_tool_calls"`
			MaxInputTokensBeforeMutation               int            `toml:"max_input_tokens_before_mutation"`
			TestMaxInputTokensBeforeMutation           int            `toml:"test_max_input_tokens_before_mutation"`
			ImplementationMaxInputTokensBeforeMutation int            `toml:"implementation_max_input_tokens_before_mutation"`
			IntegrationMaxInputTokensBeforeMutation    int            `toml:"integration_max_input_tokens_before_mutation"`
			TestMaxReadsBeforeMutation                 int            `toml:"test_max_reads_before_mutation"`
			ImplementationMaxReadsBeforeMutation       int            `toml:"implementation_max_reads_before_mutation"`
			IntegrationMaxReadsBeforeMutation          int            `toml:"integration_max_reads_before_mutation"`
			ChatTemplateKwargs                         map[string]any `toml:"chat_template_kwargs"`
			ToolArgumentsFormat                        string         `toml:"tool_arguments_format"`
		} `toml:"direct_tool_agent"`
	}
	require.NoError(t, toml.Unmarshal(out, &parsed))
	require.Equal(t, "/home/dev/git/drem-orchestrator.git", parsed.BareRepoPath)
	require.Equal(t, "go", parsed.Project.Language)
	require.True(t, parsed.Agents.Classifier.Direct)
	require.Equal(t, "sglang-direct", parsed.Agents.Coder.Provider)
	require.Equal(t, "gemma4-26b", parsed.Agents.Coder.Model)
	require.Equal(t, "http://gq:8090/v1/chat/completions", parsed.DirectToolAgent.Endpoint)
	require.Equal(t, 16, parsed.DirectToolAgent.MaxIterations)
	require.Equal(t, 60_000, parsed.DirectToolAgent.MaxCumulativeInputTokens)
	require.Equal(t, 65_000, parsed.DirectToolAgent.TestMaxCumulativeInputTokens)
	require.Equal(t, 90_000, parsed.DirectToolAgent.ImplementationMaxCumulativeInputTokens)
	require.Equal(t, 75_000, parsed.DirectToolAgent.IntegrationMaxCumulativeInputTokens)
	require.Equal(t, 30_000, parsed.DirectToolAgent.ReviewMaxCumulativeInputTokens)
	require.Equal(t, 4, parsed.DirectToolAgent.MaxReadsBeforeMutation)
	require.Equal(t, 12, parsed.DirectToolAgent.MaxToolCalls)
	require.Equal(t, 20_000, parsed.DirectToolAgent.MaxInputTokensBeforeMutation)
	require.Equal(t, 18_000, parsed.DirectToolAgent.TestMaxInputTokensBeforeMutation)
	require.Equal(t, 30_000, parsed.DirectToolAgent.ImplementationMaxInputTokensBeforeMutation)
	require.Equal(t, 24_000, parsed.DirectToolAgent.IntegrationMaxInputTokensBeforeMutation)
	require.Equal(t, 8, parsed.DirectToolAgent.TestMaxReadsBeforeMutation)
	require.Equal(t, 6, parsed.DirectToolAgent.ImplementationMaxReadsBeforeMutation)
	require.Equal(t, 6, parsed.DirectToolAgent.IntegrationMaxReadsBeforeMutation)
	require.Equal(t, map[string]any{"enable_thinking": false}, parsed.DirectToolAgent.ChatTemplateKwargs)
	require.Equal(t, "object", parsed.DirectToolAgent.ToolArgumentsFormat)
	// The warm-classifier endpoint must round-trip so orch picks it up on
	// startup without needing DREM_CLASSIFIER_URL also set. See
	// plans/warm-direct-classifier.md §3 (Modified files).
	require.Equal(t, "http://drem-classifier:8090/classify", parsed.Agents.Classifier.Endpoint)
}

// TestRenderConfig_ContainsWarmClassifierEndpoint is a tighter substring
// check that protects the exact key/value pair against typo regressions.
// The round-trip above catches TOML-level breakage; this catches someone
// renaming the key in-flight.
func TestRenderConfig_ContainsWarmClassifierEndpoint(t *testing.T) {
	out, err := projects.RenderConfig(projects.TemplateData{
		BareRepoPath: "/tmp/x",
		Language:     projects.LanguageGo,
	})
	require.NoError(t, err)
	require.Contains(t, string(out), `endpoint = "http://drem-classifier:8090/classify"`)
}

func TestRenderConfig_UsesExternalInferenceEndpoint(t *testing.T) {
	out, err := projects.RenderConfig(projects.TemplateData{
		BareRepoPath:      "/tmp/canvas.git",
		Language:          projects.LanguageCpp,
		InferenceEndpoint: "http://host.docker.internal:18090/v1/chat/completions",
	})
	require.NoError(t, err)
	require.Contains(t, string(out),
		`endpoint = "http://host.docker.internal:18090/v1/chat/completions"`)
}

func TestRenderConfig_UsesSelectedInferenceModelForEveryDirectRole(t *testing.T) {
	out, err := projects.RenderConfig(projects.TemplateData{
		BareRepoPath:   "/tmp/canvas.git",
		Language:       projects.LanguageCpp,
		InferenceModel: "qwen3.6-27b-code",
	})
	require.NoError(t, err)

	var parsed struct {
		Agents struct {
			Classifier struct {
				Model string `toml:"model"`
			} `toml:"classifier"`
			Coder struct {
				Model string `toml:"model"`
			} `toml:"coder"`
			Reviewer struct {
				Model string `toml:"model"`
			} `toml:"reviewer"`
			Fixer struct {
				Model string `toml:"model"`
			} `toml:"fixer"`
			Merger struct {
				Model string `toml:"model"`
			} `toml:"merger"`
		} `toml:"agents"`
		DirectToolAgent struct {
			Model               string `toml:"model"`
			ToolArgumentsFormat string `toml:"tool_arguments_format"`
		} `toml:"direct_tool_agent"`
	}
	require.NoError(t, toml.Unmarshal(out, &parsed))
	for name, modelName := range map[string]string{
		"classifier":        parsed.Agents.Classifier.Model,
		"coder":             parsed.Agents.Coder.Model,
		"reviewer":          parsed.Agents.Reviewer.Model,
		"fixer":             parsed.Agents.Fixer.Model,
		"merger":            parsed.Agents.Merger.Model,
		"direct_tool_agent": parsed.DirectToolAgent.Model,
	} {
		require.Equal(t, "qwen3.6-27b-code", modelName, name)
	}
	require.Equal(t, "string", parsed.DirectToolAgent.ToolArgumentsFormat)
}

func TestRenderConfig_WritesExplicitDeliveryPolicies(t *testing.T) {
	out, err := projects.RenderConfig(projects.TemplateData{
		BareRepoPath:       "/tmp/canvas.git",
		Language:           projects.LanguageCpp,
		IntegrationPolicy:  "prepare_branch",
		VerificationPolicy: "external_ack",
	})
	require.NoError(t, err)

	var parsed struct {
		Delivery struct {
			IntegrationPolicy  string `toml:"integration_policy"`
			VerificationPolicy string `toml:"verification_policy"`
		} `toml:"delivery"`
	}
	require.NoError(t, toml.Unmarshal(out, &parsed))
	require.Equal(t, "prepare_branch", parsed.Delivery.IntegrationPolicy)
	require.Equal(t, "external_ack", parsed.Delivery.VerificationPolicy)
}

// TestRenderConfig_PlannerPinsCodex asserts the generated drem.toml routes
// planning through the Codex-backed warm planner container.
func TestRenderConfig_PlannerPinsCodex(t *testing.T) {
	data := projects.TemplateData{
		ProjectName:  "drem-orchestrator",
		Language:     projects.LanguageGo,
		BareRepoPath: "/home/dev/git/drem-orchestrator.git",
	}
	out, err := projects.RenderConfig(data)
	require.NoError(t, err)
	s := string(out)

	require.Contains(t, s, "[agents.planner]")
	require.Contains(t, s, `provider = "codex"`)
	require.Contains(t, s, `model    = "gpt-5.4-mini"`)
	require.Contains(t, s, `effort   = "high"`)

	// Round-trip through the TOML parser to catch template-quoting bugs
	// before they reach the orch container at boot.
	var parsed struct {
		Agents struct {
			Planner struct {
				Provider string `toml:"provider"`
				Model    string `toml:"model"`
				Effort   string `toml:"effort"`
			} `toml:"planner"`
		} `toml:"agents"`
	}
	require.NoError(t, toml.Unmarshal(out, &parsed))
	require.Equal(t, "codex", parsed.Agents.Planner.Provider)
	require.Equal(t, "gpt-5.4-mini", parsed.Agents.Planner.Model)
	require.Equal(t, "high", parsed.Agents.Planner.Effort)
}

func TestRenderConfig_MergerPinsSGLangGemma(t *testing.T) {
	data := projects.TemplateData{
		ProjectName:  "drem-orchestrator",
		Language:     projects.LanguageGo,
		BareRepoPath: "/home/dev/git/drem-orchestrator.git",
	}
	out, err := projects.RenderConfig(data)
	require.NoError(t, err)
	s := string(out)

	require.Contains(t, s, "[agents.merger]")
	require.Contains(t, s, `provider = "sglang-direct"`)
	require.Contains(t, s, `model    = "gemma4-26b"`)

	var parsed struct {
		Agents struct {
			Merger struct {
				Provider string `toml:"provider"`
				Model    string `toml:"model"`
			} `toml:"merger"`
		} `toml:"agents"`
	}
	require.NoError(t, toml.Unmarshal(out, &parsed))
	require.Equal(t, "sglang-direct", parsed.Agents.Merger.Provider)
	require.Equal(t, "gemma4-26b", parsed.Agents.Merger.Model)
}

func TestRenderConfig_WorkerRolesPinSGLangGemma(t *testing.T) {
	data := projects.TemplateData{
		ProjectName:  "drem-orchestrator",
		Language:     projects.LanguageGo,
		BareRepoPath: "/home/dev/git/drem-orchestrator.git",
	}
	out, err := projects.RenderConfig(data)
	require.NoError(t, err)

	var parsed struct {
		Agents struct {
			Coder struct {
				Provider string `toml:"provider"`
				Model    string `toml:"model"`
			} `toml:"coder"`
			Reviewer struct {
				Provider string `toml:"provider"`
				Model    string `toml:"model"`
			} `toml:"reviewer"`
			Fixer struct {
				Provider string `toml:"provider"`
				Model    string `toml:"model"`
			} `toml:"fixer"`
		} `toml:"agents"`
	}
	require.NoError(t, toml.Unmarshal(out, &parsed))

	for name, role := range map[string]struct {
		Provider string
		Model    string
	}{
		"coder":    {parsed.Agents.Coder.Provider, parsed.Agents.Coder.Model},
		"reviewer": {parsed.Agents.Reviewer.Provider, parsed.Agents.Reviewer.Model},
		"fixer":    {parsed.Agents.Fixer.Provider, parsed.Agents.Fixer.Model},
	} {
		require.Equal(t, "sglang-direct", role.Provider, name)
		require.Equal(t, "gemma4-26b", role.Model, name)
	}
}

func TestRenderConfig_CppIncludesDefaultGateCommands(t *testing.T) {
	out, err := projects.RenderConfig(projects.TemplateData{
		ProjectName:  "drem-canvas",
		Language:     projects.LanguageCpp,
		BareRepoPath: "/home/dev/git/drem-canvas.git",
	})
	require.NoError(t, err)

	var parsed struct {
		TestCommand    string `toml:"test_command"`
		CompileCommand string `toml:"compile_command"`
		Project        struct {
			Language string `toml:"language"`
		} `toml:"project"`
	}
	require.NoError(t, toml.Unmarshal(out, &parsed))
	require.Equal(t, projects.LanguageCpp, parsed.Project.Language)
	require.Equal(t,
		"cmake -S . -B build && cmake --build build && ctest --test-dir build --output-on-failure",
		parsed.TestCommand)
	require.Equal(t,
		"cmake -S . -B build && cmake --build build",
		parsed.CompileCommand)
}

// TestRenderConfig_RequiresBareRepoPath asserts the nil-guard on the
// one field the template cannot safely default.
func TestRenderConfig_RequiresBareRepoPath(t *testing.T) {
	_, err := projects.RenderConfig(projects.TemplateData{Language: "go"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "BareRepoPath")
}

// TestRenderConfig_RequiresLanguage asserts that the template refuses
// an empty language (it interpolates into [project].language).
func TestRenderConfig_RequiresLanguage(t *testing.T) {
	_, err := projects.RenderConfig(projects.TemplateData{BareRepoPath: "/x"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "Language")
}

// TestWriteProjectConfigAt_WritesAndResolvesPath verifies that the helper
// drops drem.toml in the same directory as compose.yml, at the filename
// the compose template's bind-mount expects.
func TestWriteProjectConfigAt_WritesAndResolvesPath(t *testing.T) {
	homeDir := t.TempDir()
	data := projects.TemplateData{
		Language:     projects.LanguageGo,
		BareRepoPath: "/home/dev/git/drem-orchestrator.git",
	}

	path, err := projects.WriteProjectConfigAt(homeDir, "drem-orchestrator", data)
	require.NoError(t, err)

	expected := filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator", "drem.toml")
	require.Equal(t, expected, path)

	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Regexp(t, `direct\s*=\s*true`, string(contents),
		"written drem.toml must enable the direct classifier")
}

// TestWriteProjectConfigAt_RejectsEmptyName covers the argument-validation
// error path.
func TestWriteProjectConfigAt_RejectsEmptyName(t *testing.T) {
	_, err := projects.WriteProjectConfigAt(t.TempDir(), "", projects.TemplateData{
		Language:     "go",
		BareRepoPath: "/x",
	})
	require.Error(t, err)
}

// TestRender_MountsDremTomlAtConfigFilePath verifies that the compose
// template bind-mounts the absolute ConfigFilePath at the orch CWD.
// Docker compose does not expand ~, so the path must be absolute.
func TestRender_MountsDremTomlAtConfigFilePath(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.ConfigFilePath = "/home/dev/.drem/projects/drem-orchestrator/drem.toml"

	out, err := projects.Render(data)
	require.NoError(t, err)

	s := string(out)
	require.Contains(t, s, "/home/dev/.drem/projects/drem-orchestrator/drem.toml:/var/lib/drem/drem.toml:ro",
		"compose must bind-mount the absolute drem.toml path at the orch CWD")
}

// TestWriteProjectComposeAt_DefaultsConfigFilePath verifies that
// WriteProjectComposeAt fills in ConfigFilePath when the caller leaves
// it blank, so the bind-mount is always present in the rendered compose.
func TestWriteProjectComposeAt_DefaultsConfigFilePath(t *testing.T) {
	homeDir := t.TempDir()
	data := projects.TemplateData{
		Language:     projects.LanguageGo,
		BareRepoPath: "/home/dev/git/drem-orchestrator.git",
	}

	composePath, err := projects.WriteProjectComposeAt(homeDir, "drem-orchestrator", data)
	require.NoError(t, err)

	contents, err := os.ReadFile(composePath)
	require.NoError(t, err)
	expected := filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator", "drem.toml")
	require.Contains(t, string(contents), expected+":/var/lib/drem/drem.toml:ro")
}
