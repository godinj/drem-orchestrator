package memory

import (
	"fmt"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func renderMemorySummary(memories []model.Memory) string {
	grouped := make(map[string][]model.Memory)
	for _, mem := range memories {
		grouped[mem.MemoryType] = append(grouped[mem.MemoryType], mem)
	}

	var sections []string

	knownTypes := []struct {
		key   string
		title string
	}{
		{"decision", "Decisions"},
		{"file_change", "File Changes"},
		{"lesson", "Lessons"},
		{"blocker", "Blockers"},
		{"completion", "Completed"},
	}
	covered := make(map[string]bool)

	for _, kt := range knownTypes {
		mems, ok := grouped[kt.key]
		if !ok {
			continue
		}
		covered[kt.key] = true
		var items []string
		for _, mem := range mems {
			items = append(items, fmt.Sprintf("- %s", mem.Content))
		}
		sections = append(sections, fmt.Sprintf("## %s\n%s", kt.title, strings.Join(items, "\n")))
	}

	for mtype, mems := range grouped {
		if covered[mtype] {
			continue
		}
		var items []string
		for _, mem := range mems {
			items = append(items, fmt.Sprintf("- %s", mem.Content))
		}
		title := titleCase(strings.ReplaceAll(mtype, "_", " "))
		sections = append(sections, fmt.Sprintf("## %s\n%s", title, strings.Join(items, "\n")))
	}

	return strings.Join(sections, "\n\n")
}

func renderAgentContext(memorySummary string, taskMemories, projectMemories []model.Memory, maxChars int) string {
	var parts []string

	if memorySummary != "" {
		parts = append(parts, "# Agent Memory Summary\n\n"+memorySummary)
	}

	if len(taskMemories) > 0 {
		parts = append(parts, "# Recent Task Memories\n\n"+renderMemoryItems(taskMemories))
	}

	if len(projectMemories) > 0 {
		parts = append(parts, "# Project-Wide Context\n\n"+renderMemoryItems(projectMemories))
	}

	context := strings.Join(parts, "\n\n---\n\n")
	if len(context) > maxChars {
		context = context[:maxChars]
	}

	return context
}

func renderMemoryItems(memories []model.Memory) string {
	var items []string
	// Reverse to chronological order.
	for i := len(memories) - 1; i >= 0; i-- {
		mem := memories[i]
		items = append(items, fmt.Sprintf("- [%s] %s", mem.MemoryType, mem.Content))
	}
	return strings.Join(items, "\n")
}

// titleCase capitalises the first letter of each space-separated word.
func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}
