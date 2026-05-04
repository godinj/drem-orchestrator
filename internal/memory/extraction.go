package memory

import (
	"regexp"
	"strings"
)

// Regex patterns for extracting structured memories from agent output.
var (
	decisionPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:decided to|chose|approach:)\s*(.+)`),
	}
	blockerPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:blocked by|need|waiting for)\s*(.+)`),
	}
	fileChangePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:created|modified|updated|deleted)\s+(?:file\s+)?(\S+\.\w+)`),
	}
	completionPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:completed|finished|done:)\s*(.+)`),
	}
)

// extractEntry holds a memory type and content extracted from output.
type extractEntry struct {
	memoryType string
	content    string
}

func extractMemoryEntries(output string) []extractEntry {
	var extracted []extractEntry

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if entry, ok := matchPatterns(line, decisionPatterns, "decision"); ok {
			extracted = append(extracted, entry)
			continue
		}
		if entry, ok := matchPatterns(line, blockerPatterns, "blocker"); ok {
			extracted = append(extracted, entry)
			continue
		}
		if entry, ok := matchPatterns(line, fileChangePatterns, "file_change"); ok {
			extracted = append(extracted, entry)
			continue
		}
		if entry, ok := matchPatterns(line, completionPatterns, "completion"); ok {
			extracted = append(extracted, entry)
			continue
		}
	}

	seen := make(map[extractEntry]bool)
	var unique []extractEntry
	for _, e := range extracted {
		if !seen[e] {
			seen[e] = true
			unique = append(unique, e)
		}
	}

	return unique
}

// matchPatterns checks a line against a list of regex patterns. If a match is
// found, it returns the full match text and true.
func matchPatterns(line string, patterns []*regexp.Regexp, memoryType string) (extractEntry, bool) {
	for _, p := range patterns {
		match := p.FindString(line)
		if match != "" {
			return extractEntry{
				memoryType: memoryType,
				content:    strings.TrimSpace(match),
			}, true
		}
	}
	return extractEntry{}, false
}
