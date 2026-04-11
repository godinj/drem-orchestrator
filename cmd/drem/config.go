package main

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/ratelimit"
)

var profileNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// AgentConfig holds per-agent-type CLI flags for agent invocations.
type AgentConfig struct {
	Model    string `toml:"model"`
	Effort   string `toml:"effort"`
	Provider string `toml:"provider"`
}

// AgentsConfig holds per-agent-type configuration keyed by role.
type AgentsConfig struct {
	Classifier            AgentConfig `toml:"classifier"`
	Planner               AgentConfig `toml:"planner"`
	Coder                 AgentConfig `toml:"coder"`
	Reviewer              AgentConfig `toml:"reviewer"`
	Fixer                 AgentConfig `toml:"fixer"`
	Researcher            AgentConfig `toml:"researcher"`
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
	default:
		ac = AgentConfig{Effort: "medium"}
	}
	return model.AgentCLIConfig{Provider: model.ProviderType(ac.Provider), Model: ac.Model, Effort: ac.Effort}
}

// SupervisorCLIConfig returns the AgentCLIConfig for synchronous supervisor calls.
func (a AgentsConfig) SupervisorCLIConfig() model.AgentCLIConfig {
	return model.AgentCLIConfig{Model: a.Supervisor.Model, Effort: a.Supervisor.Effort}
}

// InteractiveSupervisorCLIConfig returns the AgentCLIConfig for interactive supervisor sessions.
func (a AgentsConfig) InteractiveSupervisorCLIConfig() model.AgentCLIConfig {
	return model.AgentCLIConfig{Model: a.InteractiveSupervisor.Model, Effort: a.InteractiveSupervisor.Effort}
}

// Config holds all runtime configuration for the Drem Orchestrator.
type Config struct {
	DatabasePath          string                   `toml:"database_path"`
	BareRepoPath          string                   `toml:"bare_repo_path"`
	DefaultBranch         string                   `toml:"default_branch"`
	ClaudeBin             string                   `toml:"claude_bin"`
	OpenCodeBin           string                   `toml:"opencode_bin"`
	MaxConcurrentAgents   int                      `toml:"max_concurrent_agents"`
	TickInterval          time.Duration            `toml:"tick_interval"`
	HeartbeatInterval     time.Duration            `toml:"heartbeat_interval"`
	StaleTimeout          time.Duration            `toml:"stale_timeout"`
	SupervisorEnabled     bool                     `toml:"supervisor_enabled"`
	SupervisorTimeout     time.Duration            `toml:"supervisor_timeout"`
	ContextWarnPercent    int                      `toml:"context_warn_percent"`
	ContextStopPercent    int                      `toml:"context_stop_percent"`
	LogPath               string                   `toml:"log_path"`
	TestCommand           string                   `toml:"test_command"`
	CompileCommand        string                   `toml:"compile_command"`
	ScopedTests           *bool                    `toml:"scoped_tests"` // pointer for default-true detection
	TestTimeout           time.Duration            `toml:"test_timeout"`
	ContextFixerPercent   int                      `toml:"context_fixer_percent"`
	TmuxSocket            string                   `toml:"tmux_socket"`
	TmuxConfigFile        string                   `toml:"tmux_config_file"`
	MaxDispatchRate       int                      `toml:"max_dispatch_rate"`
	DispatchWindow        time.Duration            `toml:"dispatch_window"`
	OpenCodeContextWindow int                      `toml:"opencode_context_window"`
	SkipConstraintGate    bool                     `toml:"skip_constraint_gate"`
	Agents                AgentsConfig             `toml:"agents"`
	Profiles              map[string]ProfileConfig `toml:"profiles"`
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
		Agents: AgentsConfig{
			Classifier:            AgentConfig{Effort: "medium"},
			Planner:               AgentConfig{Effort: "medium"},
			Coder:                 AgentConfig{Effort: "medium"},
			Reviewer:              AgentConfig{Effort: "medium"},
			Fixer:                 AgentConfig{Effort: "medium"},
			Researcher:            AgentConfig{Effort: "medium"},
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
