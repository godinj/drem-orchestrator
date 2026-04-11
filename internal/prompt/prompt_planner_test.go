package prompt

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// promptDir returns the directory containing this test file (internal/prompt/).
func promptDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}

func TestPlannerInstructionsCallableAndNonEmpty(t *testing.T) {
	sections := plannerInstructions()
	if len(sections) == 0 {
		t.Fatal("plannerInstructions() returned zero sections")
	}
	// Verify meaningful content exists (empty strings are valid markdown spacers).
	total := 0
	for _, s := range sections {
		total += len(s)
	}
	if total < 100 {
		t.Errorf("plannerInstructions() total content length %d, expected substantial output", total)
	}
}

func TestPromptGoLineCountUnder800(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(promptDir(), "prompt.go"))
	if err != nil {
		t.Fatalf("reading prompt.go: %v", err)
	}
	lines := bytes.Count(data, []byte("\n"))
	const ceiling = 800
	if lines > ceiling {
		t.Errorf("prompt.go has %d lines, must be <= %d", lines, ceiling)
	}
}

func TestPrepInstructionsNotDefined(t *testing.T) {
	// After the refactor, prepInstructions should not exist anywhere in prompt.go.
	// This is a runtime check complementing compile-time absence.
	data, err := os.ReadFile(filepath.Join(promptDir(), "prompt.go"))
	if err != nil {
		t.Fatalf("reading prompt.go: %v", err)
	}
	if bytes.Contains(data, []byte("func prepInstructions(")) {
		t.Error("prompt.go still defines prepInstructions — it should have been deleted")
	}
}
