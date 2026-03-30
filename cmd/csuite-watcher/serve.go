package main

import (
	"fmt"
	"io"
	"os"

	"github.com/BurntSushi/toml"
)

// serveTomlConfig is the TOML structure for the [serve] section of drem.toml.
type serveTomlConfig struct {
	ListenAddr  string `toml:"listen_addr"`
	BearerToken string `toml:"bearer_token"`
	DBPath      string `toml:"db_path"`
}

// serveDremToml is used to unmarshal only the [serve] section from drem.toml.
type serveDremToml struct {
	Serve serveTomlConfig `toml:"serve"`
}

// loadServeConfig reads the [serve] section from a drem.toml file.
// If the file does not exist, it returns a zero-value config (no error) so
// defaults apply — consistent with loadWatcherConfig behaviour.
func loadServeConfig(path string) (serveTomlConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return serveTomlConfig{}, nil
		}
		return serveTomlConfig{}, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg serveDremToml
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return serveTomlConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg.Serve, nil
}

// runServe handles the serve subcommand. Not yet implemented.
func runServe(_ []string, _ io.Writer) int {
	return 1
}
