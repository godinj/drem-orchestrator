// Package constraints provides a project-agnostic constraint system that
// parses .drem/constraints.toml and evaluates constraints against a worktree.
package constraints

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config is the top-level structure of .drem/constraints.toml.
type Config struct {
	ContextFiles []string               `toml:"context_files"`
	Commands     []commandConstraint    `toml:"command"`
	MaxLines     []MaxLinesConstraint   `toml:"max_lines"`
	MaxMatches   []MaxMatchesConstraint `toml:"max_matches"`
	NoMatch      []noMatchConstraint    `toml:"no_match"`
	Depth        []depthConstraint      `toml:"depth"`
}

// commandConstraint runs an arbitrary shell command and checks its result.
type commandConstraint struct {
	Name   string `toml:"name"`
	Run    string `toml:"run"`
	Expect string `toml:"expect"` // "exit_zero" (default) or "empty_output"
}

// MaxLinesConstraint limits the number of lines in files matching a glob.
type MaxLinesConstraint struct {
	Name       string           `toml:"name"`
	Glob       string           `toml:"glob"`
	Exclude    []string         `toml:"exclude"`
	Limit      int              `toml:"limit"`
	Exceptions []LinesException `toml:"exception"`
}

// LinesException allows a specific file to exceed the normal line limit
// under a shrink-only rule.
type LinesException struct {
	Path          string `toml:"path"`
	Rule          string `toml:"rule"` // "shrink-only"
	BaselineLines int    `toml:"baseline_lines"`
}

// MaxMatchesConstraint limits the number of regex matches in files matching a glob.
type MaxMatchesConstraint struct {
	Name       string             `toml:"name"`
	Glob       string             `toml:"glob"`
	Exclude    []string           `toml:"exclude"`
	Pattern    string             `toml:"pattern"`
	Limit      int                `toml:"limit"`
	Scope      string             `toml:"scope"` // "file" (default) or "directory"
	Exceptions []MatchesException `toml:"exception"`
}

// MatchesException allows a specific file or directory to exceed the normal
// match limit under a shrink-only rule.
type MatchesException struct {
	Path          string `toml:"path"`
	Rule          string `toml:"rule"` // "shrink-only"
	BaselineCount int    `toml:"baseline_count"`
}

// noMatchConstraint asserts that a regex pattern does not appear in any file
// matching the glob.
type noMatchConstraint struct {
	Name        string   `toml:"name"`
	Glob        string   `toml:"glob"`
	ExcludePath []string `toml:"exclude_path"`
	Pattern     string   `toml:"pattern"`
}

// depthConstraint enforces module depth heuristics on Go packages.
type depthConstraint struct {
	Name            string           `toml:"name"`
	Glob            string           `toml:"glob"`              // glob to find Go packages (matches directories)
	Exclude         []string         `toml:"exclude"`           // patterns to exclude
	MaxExportRatio  float64          `toml:"max_export_ratio"`  // ceiling for exported symbols / total LOC
	MaxPassThroughs int              `toml:"max_pass_throughs"` // max pass-through functions per package
	Exceptions      []depthException `toml:"exception"`
}

// depthException grandfathers a package for depth constraints.
type depthException struct {
	Path              string  `toml:"path"`
	Rule              string  `toml:"rule"`                // "grandfathered"
	BaselineRatio     float64 `toml:"baseline_ratio"`      // current export ratio (shrink-only)
	BaselinePassThrus int     `toml:"baseline_pass_thrus"` // current pass-through count (shrink-only)
}

// LoadConfig reads and parses .drem/constraints.toml from the given worktree root.
// Returns nil config (no error) if the file does not exist.
func LoadConfig(worktreeRoot string) (*Config, error) {
	path := filepath.Join(worktreeRoot, ".drem", "constraints.toml")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading constraints config: %w", err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing constraints config: %w", err)
	}

	// Apply defaults.
	for i := range cfg.Commands {
		if cfg.Commands[i].Expect == "" {
			cfg.Commands[i].Expect = "exit_zero"
		}
	}
	for i := range cfg.MaxMatches {
		if cfg.MaxMatches[i].Scope == "" {
			cfg.MaxMatches[i].Scope = "file"
		}
	}

	return &cfg, nil
}
