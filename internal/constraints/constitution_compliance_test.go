package constraints

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot returns the repository root by walking up from the test file location.
func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("failed to find repo root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestOrchestratorImportBaseline(t *testing.T) {
	root := repoRoot(t)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("failed to load constraints.toml: %v", err)
	}
	if cfg == nil {
		t.Fatal("constraints.toml not found")
	}

	// Find the "Internal import ceiling" max_matches constraint.
	var importConstraint *MaxMatchesConstraint
	for i := range cfg.MaxMatches {
		if cfg.MaxMatches[i].Name == "Internal import ceiling" {
			importConstraint = &cfg.MaxMatches[i]
			break
		}
	}
	if importConstraint == nil {
		t.Fatal("no 'Internal import ceiling' constraint found in constraints.toml")
	}

	// Find the orchestrator exception.
	var orchException *MatchesException
	for i := range importConstraint.Exceptions {
		if importConstraint.Exceptions[i].Path == "internal/orchestrator/" {
			orchException = &importConstraint.Exceptions[i]
			break
		}
	}
	if orchException == nil {
		t.Fatal("no exception for internal/orchestrator/ in Internal import ceiling constraint")
	}

	// The local control-plane slice added the branchpolicy and worktreehost
	// boundary packages deliberately. Keep this assertion aligned with the
	// documented shrink-only exception; the next change should ratchet down.
	const expectedBaseline = 19
	if orchException.BaselineCount != expectedBaseline {
		t.Errorf("internal/orchestrator/ import baseline_count = %d, want %d",
			orchException.BaselineCount, expectedBaseline)
	}
}

func TestArchitectureMDContainsNewOrchestratorFiles(t *testing.T) {
	root := repoRoot(t)
	archPath := filepath.Join(root, "ARCHITECTURE.md")

	content, err := os.ReadFile(archPath)
	if err != nil {
		t.Fatalf("failed to read ARCHITECTURE.md: %v", err)
	}
	archText := string(content)

	// These files were created during the orchestrator file-splitting refactor.
	// ARCHITECTURE.md must contain a description for each one.
	newFiles := []string{
		"classifying.go",
		"context_monitor.go",
		"merge_execution.go",
		"session_spawning.go",
		"task_api.go",
		"test_execution.go",
	}

	for _, f := range newFiles {
		if !strings.Contains(archText, f) {
			t.Errorf("ARCHITECTURE.md missing description for orchestrator file %q", f)
		}
	}
}

func TestArchitectureMDOrchestratorFilesInPackageMap(t *testing.T) {
	root := repoRoot(t)
	archPath := filepath.Join(root, "ARCHITECTURE.md")

	f, err := os.Open(archPath)
	if err != nil {
		t.Fatalf("failed to open ARCHITECTURE.md: %v", err)
	}
	defer f.Close()

	// Verify the new file descriptions appear under the orchestrator/ section,
	// not just anywhere in the document. We look for lines containing the file
	// names that appear after the orchestrator/ package entry.
	scanner := bufio.NewScanner(f)
	inOrchestrator := false
	foundFiles := make(map[string]bool)
	newFiles := []string{
		"classifying.go",
		"context_monitor.go",
		"merge_execution.go",
		"session_spawning.go",
		"task_api.go",
		"test_execution.go",
	}

	for scanner.Scan() {
		line := scanner.Text()
		// Detect when we enter the orchestrator package section.
		if strings.Contains(line, "orchestrator/") && strings.Contains(line, "—") {
			inOrchestrator = true
			continue
		}
		// Detect when we leave (next package entry at same indent level).
		if inOrchestrator && !strings.HasPrefix(line, "  ") && strings.Contains(line, "/") && strings.Contains(line, "—") {
			break
		}
		if inOrchestrator {
			for _, nf := range newFiles {
				if strings.Contains(line, nf) {
					foundFiles[nf] = true
				}
			}
		}
	}

	for _, nf := range newFiles {
		if !foundFiles[nf] {
			t.Errorf("ARCHITECTURE.md orchestrator section missing entry for %q", nf)
		}
	}
}
