package main_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// readmePath returns the absolute path to cmd/csuite-watcher/README.md,
// located relative to this test file.
func readmePath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "README.md")
}

func TestReadmeExists(t *testing.T) {
	path := readmePath(t)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("cmd/csuite-watcher/README.md does not exist; documentation is required")
	}
}

func readReadme(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(readmePath(t))
	if err != nil {
		t.Fatalf("cannot read README.md: %v", err)
	}
	return string(data)
}

// TestReadmeContainsCLIUsage verifies the README documents the event subcommand invocation pattern.
func TestReadmeContainsCLIUsage(t *testing.T) {
	content := readReadme(t)
	if !strings.Contains(content, "csuite-watcher event") {
		t.Error("README must document the CLI usage pattern 'csuite-watcher event'")
	}
}

// TestReadmeDocumentsJSONFields verifies all required JSON payload fields are documented.
func TestReadmeDocumentsJSONFields(t *testing.T) {
	content := readReadme(t)
	required := []string{
		"type",
		"task_id",
		"from_status",
		"to_status",
		"details",
		"timestamp",
	}
	for _, field := range required {
		if !strings.Contains(content, field) {
			t.Errorf("README must document JSON field %q", field)
		}
	}
}

// TestReadmeContainsCompleteJSONExample verifies a complete JSON example is present.
// It checks that the README includes a JSON object with all six required fields.
func TestReadmeContainsCompleteJSONExample(t *testing.T) {
	content := readReadme(t)
	required := []string{
		`"type"`,
		`"task_id"`,
		`"from_status"`,
		`"to_status"`,
		`"details"`,
		`"timestamp"`,
	}
	for _, field := range required {
		if !strings.Contains(content, field) {
			t.Errorf("README must include a complete JSON example containing field %s", field)
		}
	}
}

// TestReadmeDocumentsDBFlag verifies the --db flag and its default path are documented.
func TestReadmeDocumentsDBFlag(t *testing.T) {
	content := readReadme(t)
	if !strings.Contains(content, "--db") {
		t.Error("README must document the --db flag")
	}
	if !strings.Contains(content, "~/.drem-csuite/csuite.db") {
		t.Error("README must document the default --db path (~/.drem-csuite/csuite.db)")
	}
}

// TestReadmeDocumentsRoutingFlag verifies the --routing flag is documented.
func TestReadmeDocumentsRoutingFlag(t *testing.T) {
	content := readReadme(t)
	if !strings.Contains(content, "--routing") {
		t.Error("README must document the --routing flag")
	}
}

// TestReadmeDocumentsExitCodes verifies exit codes 0 (success) and non-zero (error) are documented.
func TestReadmeDocumentsExitCodes(t *testing.T) {
	content := readReadme(t)
	if !strings.Contains(content, "exit") && !strings.Contains(content, "Exit") {
		t.Error("README must document exit codes")
	}
	if !strings.Contains(content, "0") {
		t.Error("README must document exit code 0 for success")
	}
	// non-zero is implied by documenting error conditions; verify "error" appears
	if !strings.Contains(content, "error") && !strings.Contains(content, "Error") {
		t.Error("README must document non-zero exit on error")
	}
}
