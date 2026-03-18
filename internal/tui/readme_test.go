package tui

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// readmeRelPath is the relative path from internal/tui/ to the repo root README.
const readmeRelPath = "../../README.md"

func readREADME(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(readmeRelPath)
	if err != nil {
		t.Fatalf("failed to read README.md: %v", err)
	}
	return string(data)
}

// TestREADME_NoQuitKeybinding verifies the keybindings table does not list
// q as a Quit action. The TUI should not exit on q; users kill the tmux session.
func TestREADME_NoQuitKeybinding(t *testing.T) {
	content := readREADME(t)

	// Match a markdown table row like: | `q` | Quit |
	// Allow optional whitespace and surrounding text on the same line.
	quitRowRe := regexp.MustCompile("(?i)\\|\\s*`q`\\s*\\|.*[Qq]uit")
	if quitRowRe.MatchString(content) {
		t.Error("README keybindings table should not have a row mapping `q` to Quit")
	}
}

// TestREADME_DocumentsTmuxExit verifies the README explains that killing the
// tmux session is the way to exit the TUI. We look specifically in the
// "Keybindings" or "TUI" sections for explicit exit-the-TUI guidance mentioning
// tmux, not incidental references to tmux in agent lifecycle documentation.
func TestREADME_DocumentsTmuxExit(t *testing.T) {
	content := readREADME(t)

	// Extract the TUI Dashboard section (from "## TUI Dashboard" to the next ##
	// heading at the same level or end of file).
	tuiSectionRe := regexp.MustCompile(`(?s)## TUI Dashboard\b(.*?)(\n## [^#]|\z)`)
	match := tuiSectionRe.FindString(content)
	if match == "" {
		t.Fatal("README should contain a '## TUI Dashboard' section")
	}

	// Within the TUI section, look for a sentence/paragraph that tells the user
	// how to exit: it should mention "tmux" alongside a directive word like
	// "kill", "exit", "close", or "terminate".
	lower := strings.ToLower(match)
	hasTmux := strings.Contains(lower, "tmux")
	exitRe := regexp.MustCompile(`(?i)\b(kill|exit|close|terminat)`)
	hasExit := exitRe.MatchString(match)

	if !hasTmux || !hasExit {
		t.Error("TUI Dashboard section should explain that the way to exit is by killing/exiting the tmux session")
	}
}
