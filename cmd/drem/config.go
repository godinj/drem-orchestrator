package main

import (
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

// Config holds all runtime configuration for the Drem Orchestrator.
type Config struct {
	DatabasePath        string        `toml:"database_path"`
	BareRepoPath        string        `toml:"bare_repo_path"`
	DefaultBranch       string        `toml:"default_branch"`
	ClaudeBin           string        `toml:"claude_bin"`
	MaxConcurrentAgents int           `toml:"max_concurrent_agents"`
	TickInterval        time.Duration `toml:"tick_interval"`
	HeartbeatInterval   time.Duration `toml:"heartbeat_interval"`
	StaleTimeout        time.Duration `toml:"stale_timeout"`
	SupervisorEnabled   bool          `toml:"supervisor_enabled"`
	SupervisorTimeout   time.Duration `toml:"supervisor_timeout"`
}

// DefaultConfig returns a Config populated with sensible default values.
func DefaultConfig() Config {
	return Config{
		DatabasePath:        "./drem.db",
		BareRepoPath:        "",
		DefaultBranch:       "master",
		ClaudeBin:           "claude",
		MaxConcurrentAgents: 5,
		TickInterval:        5 * time.Second,
		HeartbeatInterval:   30 * time.Second,
		StaleTimeout:        5 * time.Minute,
		SupervisorEnabled:   true,
		SupervisorTimeout:   2 * time.Minute,
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

	return cfg, nil
}
