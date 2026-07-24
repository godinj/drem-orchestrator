package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/ratelimit"
)

var profileNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func runtimeProjectName(bareRepoPath string) string {
	if explicit := strings.TrimSpace(os.Getenv("DREM_PROJECT")); explicit != "" {
		return explicit
	}
	return projectNameFromBareRepo(bareRepoPath)
}

// AgentConfig holds per-agent-type CLI flags for agent invocations.
type AgentConfig struct {
	Model    string `toml:"model"`
	Effort   string `toml:"effort"`
	Provider string `toml:"provider"`
	Direct   bool   `toml:"direct"` // classifier only: bypass OpenCode, call SGLang API directly
	// ContainerImage, when non-empty, overrides the language-derived
	// default container image used by the spawner-routed agent path in
	// internal/agent/image_resolver.go. Ignored by the legacy subprocess
	// path.
	ContainerImage string `toml:"container_image"`
	// Endpoint routes classify jobs to a warm drem-classifier container
	// instead of running the direct SGLang call inline in orch. Only the
	// classifier role honors this today (see plans/warm-direct-classifier.md);
	// planner + prep follow the same pattern in their own plans. Empty
	// keeps the inline direct path as the rollback-safe default.
	// DREM_CLASSIFIER_URL takes precedence over this key.
	Endpoint string `toml:"endpoint"`
}

// ProjectTOMLConfig mirrors the [project] section of drem.toml. Today it
// only carries the project language, which steers the per-language
// worker image picked for coder agents. Other project-scoped settings
// live at the top level of drem.toml for historical reasons; new ones
// should land here so the schema groups related fields.
type ProjectTOMLConfig struct {
	Language string `toml:"language"`
}

// AgentsConfig holds per-agent-type configuration keyed by role.
type AgentsConfig struct {
	Classifier            AgentConfig `toml:"classifier"`
	Planner               AgentConfig `toml:"planner"`
	Coder                 AgentConfig `toml:"coder"`
	Reviewer              AgentConfig `toml:"reviewer"`
	Fixer                 AgentConfig `toml:"fixer"`
	Researcher            AgentConfig `toml:"researcher"`
	Prep                  AgentConfig `toml:"prep"`
	Merger                AgentConfig `toml:"merger"`
	Supervisor            AgentConfig `toml:"supervisor"`
	InteractiveSupervisor AgentConfig `toml:"interactive_supervisor"`
}

// ForAgentType returns the AgentCLIConfig for the given headless agent type.
func (a AgentsConfig) ForAgentType(at model.AgentType) model.AgentCLIConfig {
	var ac AgentConfig
	switch at {
	case model.AgentClassifier:
		ac = a.Classifier
	case model.AgentPlanner:
		ac = a.Planner
	case model.AgentCoder:
		ac = a.Coder
	case model.AgentReviewer:
		ac = a.Reviewer
	case model.AgentFixer:
		ac = a.Fixer
	case model.AgentResearcher:
		ac = a.Researcher
	case model.AgentPrep:
		ac = a.Prep
	case model.AgentMerger:
		ac = a.Merger
	default:
		ac = AgentConfig{Effort: "medium"}
	}
	return model.AgentCLIConfig{Provider: model.ProviderType(ac.Provider), Model: ac.Model, Effort: ac.Effort}
}

// SupervisorCLIConfig returns the AgentCLIConfig for synchronous supervisor calls.
func (a AgentsConfig) SupervisorCLIConfig() model.AgentCLIConfig {
	return model.AgentCLIConfig{Provider: model.ProviderType(a.Supervisor.Provider), Model: a.Supervisor.Model, Effort: a.Supervisor.Effort}
}

// InteractiveSupervisorCLIConfig returns the AgentCLIConfig for interactive supervisor sessions.
func (a AgentsConfig) InteractiveSupervisorCLIConfig() model.AgentCLIConfig {
	return model.AgentCLIConfig{Provider: model.ProviderType(a.InteractiveSupervisor.Provider), Model: a.InteractiveSupervisor.Model, Effort: a.InteractiveSupervisor.Effort}
}

// DirectToolAgentTOMLConfig mirrors the [direct_tool_agent] TOML block used
// to configure the direct SGLang tool-calling agent path for coder,
// reviewer, and fixer roles.
type DirectToolAgentTOMLConfig struct {
	Enabled                                    bool           `toml:"enabled"`
	Endpoint                                   string         `toml:"endpoint"`
	Model                                      string         `toml:"model"`
	MaxTokens                                  int            `toml:"max_tokens"`
	Temperature                                float64        `toml:"temperature"`
	Timeout                                    time.Duration  `toml:"timeout"`
	MaxIterations                              int            `toml:"max_iterations"`
	MaxCumulativeInputTokens                   int            `toml:"max_cumulative_input_tokens"`
	MaxReadsBeforeMutation                     int            `toml:"max_reads_before_mutation"`
	MaxToolCalls                               int            `toml:"max_tool_calls"`
	MaxInputTokensBeforeMutation               int            `toml:"max_input_tokens_before_mutation"`
	TestMaxCumulativeInputTokens               int            `toml:"test_max_cumulative_input_tokens"`
	ImplementationMaxCumulativeInputTokens     int            `toml:"implementation_max_cumulative_input_tokens"`
	IntegrationMaxCumulativeInputTokens        int            `toml:"integration_max_cumulative_input_tokens"`
	ReviewMaxCumulativeInputTokens             int            `toml:"review_max_cumulative_input_tokens"`
	TestMaxReadsBeforeMutation                 int            `toml:"test_max_reads_before_mutation"`
	ImplementationMaxReadsBeforeMutation       int            `toml:"implementation_max_reads_before_mutation"`
	IntegrationMaxReadsBeforeMutation          int            `toml:"integration_max_reads_before_mutation"`
	TestMaxInputTokensBeforeMutation           int            `toml:"test_max_input_tokens_before_mutation"`
	ImplementationMaxInputTokensBeforeMutation int            `toml:"implementation_max_input_tokens_before_mutation"`
	IntegrationMaxInputTokensBeforeMutation    int            `toml:"integration_max_input_tokens_before_mutation"`
	BashTimeout                                time.Duration  `toml:"bash_timeout"`
	ContextLimit                               int            `toml:"context_limit"`
	ChatTemplateKwargs                         map[string]any `toml:"chat_template_kwargs"`
	ToolArgumentsFormat                        string         `toml:"tool_arguments_format"`
}

// DeliveryTOMLConfig selects the explicit delivery and verification policy.
// Defaults preserve the all-in-one behavior while still using typed evidence.
type DeliveryTOMLConfig struct {
	IntegrationPolicy  model.IntegrationPolicy  `toml:"integration_policy"`
	VerificationPolicy model.VerificationPolicy `toml:"verification_policy"`
}

// ReviewPolicyTOMLConfig controls approval-gate automation. The safe-auto
// policy delegates review to SGLang but advances only an explicit, validated
// approve recommendation; malformed, unavailable, revise, and reject results
// remain parked for Codex/operator attention.
type ReviewPolicyTOMLConfig struct {
	Plan  model.ReviewGatePolicy `toml:"plan"`
	Tests model.ReviewGatePolicy `toml:"tests"`
}

// Config holds all runtime configuration for the Drem Orchestrator.
type Config struct {
	DatabasePath        string        `toml:"database_path"`
	BareRepoPath        string        `toml:"bare_repo_path"`
	DefaultBranch       string        `toml:"default_branch"`
	ClaudeBin           string        `toml:"claude_bin"`
	OpenCodeBin         string        `toml:"opencode_bin"`
	CodexBin            string        `toml:"codex_bin"`
	MaxConcurrentAgents int           `toml:"max_concurrent_agents"`
	TickInterval        time.Duration `toml:"tick_interval"`
	HeartbeatInterval   time.Duration `toml:"heartbeat_interval"`
	StaleTimeout        time.Duration `toml:"stale_timeout"`
	SupervisorEnabled   bool          `toml:"supervisor_enabled"`
	SupervisorTimeout   time.Duration `toml:"supervisor_timeout"`
	ContextWarnPercent  int           `toml:"context_warn_percent"`
	ContextStopPercent  int           `toml:"context_stop_percent"`
	LogPath             string        `toml:"log_path"`
	TestCommand         string        `toml:"test_command"`
	CompileCommand      string        `toml:"compile_command"`
	ScopedTests         *bool         `toml:"scoped_tests"` // pointer for default-true detection
	TestTimeout         time.Duration `toml:"test_timeout"`
	ContextFixerPercent int           `toml:"context_fixer_percent"`
	// TmuxSocket and TmuxConfigFile are retained for backward compatibility
	// so existing drem.toml files that still set these keys keep loading.
	// Their values are ignored — the tmux dashboard path was removed during
	// the containerization migration (prompt 21).
	TmuxSocket            string        `toml:"tmux_socket,omitempty"`
	TmuxConfigFile        string        `toml:"tmux_config_file,omitempty"`
	MaxDispatchRate       int           `toml:"max_dispatch_rate"`
	DispatchWindow        time.Duration `toml:"dispatch_window"`
	OpenCodeContextWindow int           `toml:"opencode_context_window"`
	SkipConstraintGate    bool          `toml:"skip_constraint_gate"`
	// OrchHTTPPort is the port the orchestrator's read-only HTTP API
	// listens on. Kyle and the TUI both read from it (see
	// docs/prd-containerization.md). Default: 8080. An empty string or
	// zero value disables the listener entirely.
	OrchHTTPPort string `toml:"orch_http_port"`
	// AgentmonToken is the per-project shared secret used by agentmon
	// to authenticate POST /internal/logs. Stays empty on dev hosts that
	// have not yet been containerized; ingestion then fails closed.
	AgentmonToken string `toml:"agentmon_token"`
	// ProjectLanguage is returned verbatim by GET /projects. Kyle uses
	// it to pick the correct worker image (drem-worker-go vs -cpp).
	ProjectLanguage string                    `toml:"project_language"`
	Project         ProjectTOMLConfig         `toml:"project"`
	Agents          AgentsConfig              `toml:"agents"`
	DirectToolAgent DirectToolAgentTOMLConfig `toml:"direct_tool_agent"`
	Delivery        DeliveryTOMLConfig        `toml:"delivery"`
	ReviewPolicy    ReviewPolicyTOMLConfig    `toml:"review_policy"`
	Profiles        map[string]ProfileConfig  `toml:"profiles"`
	// Tmux is accepted but ignored for one release so existing drem.toml
	// files that still carry a [tmux] table keep loading. Prompt 17
	// removes the tolerance once the subprocess+tmux path is deleted.
	Tmux map[string]any `toml:"tmux,omitempty"`
}

// EffectiveProjectLanguage returns the project language, preferring the
// structured [project].language field and falling back to the legacy
// top-level project_language key. Keeping this in one helper lets every
// caller (spawner routing, project registry sync, TUI) read the same
// value.
func (c Config) EffectiveProjectLanguage() string {
	if c.Project.Language != "" {
		return c.Project.Language
	}
	return c.ProjectLanguage
}

// ContainerImageOverrides collapses the per-agent ContainerImage fields
// into the flat map form consumed by agent.ImageResolver.Overrides.
// Agent types whose override is empty are omitted.
func (a AgentsConfig) ContainerImageOverrides() map[string]string {
	out := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	add("classifier", a.Classifier.ContainerImage)
	add("planner", a.Planner.ContainerImage)
	add("coder", a.Coder.ContainerImage)
	add("reviewer", a.Reviewer.ContainerImage)
	add("fixer", a.Fixer.ContainerImage)
	add("researcher", a.Researcher.ContainerImage)
	add("prep", a.Prep.ContainerImage)
	add("merger", a.Merger.ContainerImage)
	add("supervisor", a.Supervisor.ContainerImage)
	return out
}

// DefaultConfig returns a Config populated with sensible default values.
func DefaultConfig() Config {
	scopedDefault := true
	return Config{
		DatabasePath:        "./drem.db",
		BareRepoPath:        "",
		DefaultBranch:       "master",
		ClaudeBin:           "claude",
		OpenCodeBin:         "opencode",
		CodexBin:            "codex",
		MaxConcurrentAgents: 5,
		TickInterval:        5 * time.Second,
		HeartbeatInterval:   30 * time.Second,
		StaleTimeout:        5 * time.Minute,
		SupervisorEnabled:   true,
		SupervisorTimeout:   2 * time.Minute,
		ContextWarnPercent:  75,
		ContextStopPercent:  90,
		LogPath:             "./drem.log",
		TestTimeout:         5 * time.Minute,
		ScopedTests:         &scopedDefault,
		ContextFixerPercent: 85,
		TmuxSocket:          "drem",
		TmuxConfigFile:      "master/.tmux.conf",
		MaxDispatchRate:     ratelimit.DefaultMaxDispatches,
		DispatchWindow:      ratelimit.DefaultDispatchWindow,
		OrchHTTPPort:        "8080",
		ProjectLanguage:     "go",
		Delivery: DeliveryTOMLConfig{
			IntegrationPolicy:  model.IntegrationAutoMerge,
			VerificationPolicy: model.VerificationLocalAutomated,
		},
		ReviewPolicy: ReviewPolicyTOMLConfig{
			Plan:  model.ReviewGateManual,
			Tests: model.ReviewGateManual,
		},
		Agents: AgentsConfig{
			Classifier:            AgentConfig{Effort: "medium"},
			Planner:               AgentConfig{Effort: "medium"},
			Coder:                 AgentConfig{Effort: "medium"},
			Reviewer:              AgentConfig{Effort: "medium"},
			Fixer:                 AgentConfig{Effort: "medium"},
			Researcher:            AgentConfig{Effort: "medium"},
			Prep:                  AgentConfig{Effort: "medium"},
			Merger:                AgentConfig{Effort: "medium"},
			Supervisor:            AgentConfig{Effort: "low"},
			InteractiveSupervisor: AgentConfig{Effort: "medium"},
		},
	}
}

// LoadConfig reads configuration from a TOML file at the given path. If
// the file does not exist, the default configuration is returned. Values
// in the file override the defaults.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read config %q: %w", path, err)
	}

	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %q: %w", path, err)
	}
	if _, err := model.ParseIntegrationPolicy(string(cfg.Delivery.IntegrationPolicy)); err != nil {
		return cfg, fmt.Errorf("delivery.integration_policy: %w", err)
	}
	if _, err := model.ParseVerificationPolicy(string(cfg.Delivery.VerificationPolicy)); err != nil {
		return cfg, fmt.Errorf("delivery.verification_policy: %w", err)
	}
	if _, err := model.ParseReviewGatePolicy(string(cfg.ReviewPolicy.Plan)); err != nil {
		return cfg, fmt.Errorf("review_policy.plan: %w", err)
	}
	if _, err := model.ParseReviewGatePolicy(string(cfg.ReviewPolicy.Tests)); err != nil {
		return cfg, fmt.Errorf("review_policy.tests: %w", err)
	}
	switch cfg.DirectToolAgent.ToolArgumentsFormat {
	case "", "string", "object":
	default:
		return cfg, fmt.Errorf("direct_tool_agent.tool_arguments_format: must be string or object")
	}

	for name := range cfg.Profiles {
		if err := ValidateProfileName(name); err != nil {
			return cfg, fmt.Errorf("invalid profile name %q: %w", name, err)
		}
	}

	return cfg, nil
}

// ValidateProfileName reports whether name is a valid profile identifier.
// Valid names contain only ASCII letters, digits, hyphens, and underscores
// and must be non-empty.
func ValidateProfileName(name string) error {
	if !profileNameRE.MatchString(name) {
		return fmt.Errorf("profile name %q must match ^[a-zA-Z0-9_-]+$", name)
	}
	return nil
}
