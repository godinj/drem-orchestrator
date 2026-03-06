// Package taskimport parses a Markdown task file and bulk-creates tasks.
//
// The expected format uses headings for task hierarchy:
//
//	# Task Title
//
//	Priority: 10
//	Labels: auth, backend
//	Depends-on: Other Task Title
//
//	Description text here, spanning
//	multiple lines.
//
//	## Subtask Title
//
//	Priority: 8
//	Depends-on: Another Subtask
//
//	Subtask description.
//
// Top-level headings (#) create parent tasks. Second-level headings (##)
// under a parent create subtasks linked to that parent. Metadata lines
// (Priority, Labels, Depends-on) at the start of a section are parsed
// and stripped from the description.
package taskimport

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ParsedTask holds the data extracted from a Markdown task section.
type ParsedTask struct {
	Title       string
	Description string
	Priority    int
	Labels      []string
	DependsOn   []string // titles of tasks this depends on
	Subtasks    []ParsedTask
}

// Parse reads a Markdown task file and returns the parsed tasks.
func Parse(r io.Reader) ([]ParsedTask, error) {
	scanner := bufio.NewScanner(r)
	var tasks []ParsedTask
	var current *ParsedTask
	var currentSub *ParsedTask
	var bodyLines []string
	inSubtask := false

	flush := func() {
		if currentSub != nil {
			currentSub.Description = trimBody(bodyLines)
			bodyLines = nil
			currentSub = nil
		} else if current != nil && !inSubtask {
			current.Description = trimBody(bodyLines)
			bodyLines = nil
		}
	}

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "## ") {
			flush()
			title := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			if current == nil {
				return nil, fmt.Errorf("subtask %q without a parent task", title)
			}
			inSubtask = true
			current.Subtasks = append(current.Subtasks, ParsedTask{Title: title})
			currentSub = &current.Subtasks[len(current.Subtasks)-1]
			bodyLines = nil
			continue
		}

		if strings.HasPrefix(line, "# ") {
			flush()
			if current != nil {
				tasks = append(tasks, *current)
			}
			title := strings.TrimSpace(strings.TrimPrefix(line, "# "))
			current = &ParsedTask{Title: title}
			currentSub = nil
			inSubtask = false
			bodyLines = nil
			continue
		}

		bodyLines = append(bodyLines, line)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read task file: %w", err)
	}

	// Flush final section.
	flush()
	if current != nil {
		tasks = append(tasks, *current)
	}

	// Extract metadata from descriptions.
	for i := range tasks {
		if err := extractMeta(&tasks[i]); err != nil {
			return nil, fmt.Errorf("task %q: %w", tasks[i].Title, err)
		}
		for j := range tasks[i].Subtasks {
			if err := extractMeta(&tasks[i].Subtasks[j]); err != nil {
				return nil, fmt.Errorf("subtask %q: %w", tasks[i].Subtasks[j].Title, err)
			}
		}
	}

	return tasks, nil
}

// extractMeta parses Key: Value lines from the start of the description
// and populates the corresponding ParsedTask fields.
func extractMeta(t *ParsedTask) error {
	lines := strings.Split(t.Description, "\n")
	var remaining []string
	metaDone := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !metaDone && trimmed == "" {
			continue
		}
		if !metaDone {
			if k, v, ok := parseMetaLine(trimmed); ok {
				switch strings.ToLower(k) {
				case "priority":
					p, err := strconv.Atoi(strings.TrimSpace(v))
					if err != nil {
						return fmt.Errorf("invalid priority %q: %w", v, err)
					}
					t.Priority = p
				case "labels":
					for _, l := range strings.Split(v, ",") {
						l = strings.TrimSpace(l)
						if l != "" {
							t.Labels = append(t.Labels, l)
						}
					}
				case "depends-on":
					for _, d := range strings.Split(v, ",") {
						d = strings.TrimSpace(d)
						if d != "" {
							t.DependsOn = append(t.DependsOn, d)
						}
					}
				default:
					// Unknown meta key — treat as body text.
					metaDone = true
					remaining = append(remaining, line)
				}
				continue
			}
			metaDone = true
		}
		remaining = append(remaining, line)
	}

	t.Description = trimBody(remaining)
	return nil
}

// parseMetaLine checks if a line matches "Key: Value" format.
func parseMetaLine(line string) (key, value string, ok bool) {
	idx := strings.Index(line, ":")
	if idx < 1 {
		return "", "", false
	}
	key = line[:idx]
	// Only recognize known meta keys.
	switch strings.ToLower(key) {
	case "priority", "labels", "depends-on":
	default:
		return "", "", false
	}
	value = strings.TrimSpace(line[idx+1:])
	return key, value, true
}

func trimBody(lines []string) string {
	// Join, trim leading/trailing blank lines.
	text := strings.Join(lines, "\n")
	return strings.TrimSpace(text)
}
