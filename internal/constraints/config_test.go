package constraints

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name     string
		toml     string
		wantNil  bool
		wantErr  bool
		validate func(t *testing.T, cfg *Config)
	}{
		{
			name: "all constraint types",
			toml: `
context_files = ["ARCHITECTURE.md"]

[[command]]
name   = "gofmt"
run    = "gofmt -l ."
expect = "empty_output"

[[command]]
name = "go vet"
run  = "go vet ./..."

[[max_lines]]
name    = "file length"
glob    = "internal/**/*.go"
exclude = ["*_test.go"]
limit   = 800

[[max_matches]]
name    = "exports"
glob    = "internal/**/*.go"
exclude = ["*_test.go"]
pattern = "^func [A-Z]"
limit   = 20
scope   = "file"

[[no_match]]
name         = "no db init"
glob         = "internal/**/*_test.go"
exclude_path = ["internal/testutil/"]
pattern      = "gorm\\.Open"
`,
			validate: func(t *testing.T, cfg *Config) {
				if len(cfg.ContextFiles) != 1 || cfg.ContextFiles[0] != "ARCHITECTURE.md" {
					t.Errorf("unexpected context_files: %v", cfg.ContextFiles)
				}
				if len(cfg.Commands) != 2 {
					t.Fatalf("expected 2 commands, got %d", len(cfg.Commands))
				}
				if cfg.Commands[0].Name != "gofmt" {
					t.Errorf("unexpected command name: %s", cfg.Commands[0].Name)
				}
				if cfg.Commands[0].Expect != "empty_output" {
					t.Errorf("unexpected expect: %s", cfg.Commands[0].Expect)
				}
				if cfg.Commands[0].Run != "gofmt -l ." {
					t.Errorf("unexpected run: %s", cfg.Commands[0].Run)
				}
				if len(cfg.MaxLines) != 1 {
					t.Fatalf("expected 1 max_lines, got %d", len(cfg.MaxLines))
				}
				if cfg.MaxLines[0].Limit != 800 {
					t.Errorf("unexpected limit: %d", cfg.MaxLines[0].Limit)
				}
				if len(cfg.MaxLines[0].Exclude) != 1 || cfg.MaxLines[0].Exclude[0] != "*_test.go" {
					t.Errorf("unexpected exclude: %v", cfg.MaxLines[0].Exclude)
				}
				if len(cfg.MaxMatches) != 1 {
					t.Fatalf("expected 1 max_matches, got %d", len(cfg.MaxMatches))
				}
				if cfg.MaxMatches[0].Pattern != "^func [A-Z]" {
					t.Errorf("unexpected pattern: %s", cfg.MaxMatches[0].Pattern)
				}
				if cfg.MaxMatches[0].Scope != "file" {
					t.Errorf("unexpected scope: %s", cfg.MaxMatches[0].Scope)
				}
				if len(cfg.NoMatch) != 1 {
					t.Fatalf("expected 1 no_match, got %d", len(cfg.NoMatch))
				}
				if len(cfg.NoMatch[0].ExcludePath) != 1 || cfg.NoMatch[0].ExcludePath[0] != "internal/testutil/" {
					t.Errorf("unexpected exclude_path: %v", cfg.NoMatch[0].ExcludePath)
				}
			},
		},
		{
			name: "config with exceptions",
			toml: `
[[max_lines]]
name  = "file length"
glob  = "**/*.go"
limit = 500

[[max_lines.exception]]
path           = "big_file.go"
rule           = "shrink-only"
baseline_lines = 1200

[[max_matches]]
name    = "exports"
glob    = "**/*.go"
pattern = "^func [A-Z]"
limit   = 10

[[max_matches.exception]]
path           = "big_dir/"
rule           = "shrink-only"
baseline_count = 30
`,
			validate: func(t *testing.T, cfg *Config) {
				if len(cfg.MaxLines[0].Exceptions) != 1 {
					t.Fatalf("expected 1 max_lines exception, got %d", len(cfg.MaxLines[0].Exceptions))
				}
				exc := cfg.MaxLines[0].Exceptions[0]
				if exc.Path != "big_file.go" {
					t.Errorf("unexpected exception path: %s", exc.Path)
				}
				if exc.Rule != "shrink-only" {
					t.Errorf("unexpected exception rule: %s", exc.Rule)
				}
				if exc.BaselineLines != 1200 {
					t.Errorf("unexpected baseline_lines: %d", exc.BaselineLines)
				}

				if len(cfg.MaxMatches[0].Exceptions) != 1 {
					t.Fatalf("expected 1 max_matches exception, got %d", len(cfg.MaxMatches[0].Exceptions))
				}
				mexc := cfg.MaxMatches[0].Exceptions[0]
				if mexc.Path != "big_dir/" {
					t.Errorf("unexpected exception path: %s", mexc.Path)
				}
				if mexc.BaselineCount != 30 {
					t.Errorf("unexpected baseline_count: %d", mexc.BaselineCount)
				}
			},
		},
		{
			name: "missing optional fields get defaults",
			toml: `
[[command]]
name = "test"
run  = "echo hello"

[[max_matches]]
name    = "count"
glob    = "*.go"
pattern = "func"
limit   = 5
`,
			validate: func(t *testing.T, cfg *Config) {
				if cfg.Commands[0].Expect != "exit_zero" {
					t.Errorf("expected default expect 'exit_zero', got %q", cfg.Commands[0].Expect)
				}
				if cfg.MaxMatches[0].Scope != "file" {
					t.Errorf("expected default scope 'file', got %q", cfg.MaxMatches[0].Scope)
				}
			},
		},
		{
			name:    "nonexistent file returns nil",
			toml:    "", // won't be written
			wantNil: true,
		},
		{
			name:    "invalid TOML returns error",
			toml:    "this is not [valid toml",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()

			if tt.name == "nonexistent file returns nil" {
				// Don't create the file.
				cfg, err := LoadConfig(dir)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if cfg != nil {
					t.Fatalf("expected nil config, got %+v", cfg)
				}
				return
			}

			dremDir := filepath.Join(dir, ".drem")
			if err := os.MkdirAll(dremDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dremDir, "constraints.toml"), []byte(tt.toml), 0o644); err != nil {
				t.Fatal(err)
			}

			cfg, err := LoadConfig(dir)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if cfg != nil {
					t.Fatalf("expected nil config, got %+v", cfg)
				}
				return
			}
			if cfg == nil {
				t.Fatal("expected non-nil config")
			}
			if tt.validate != nil {
				tt.validate(t, cfg)
			}
		})
	}
}
