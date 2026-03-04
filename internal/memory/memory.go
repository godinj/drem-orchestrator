// Package memory provides agent memory persistence, retrieval, compaction,
// and extraction from agent output. Memories are stored in the database via
// GORM and can be queried per-agent, per-task, or project-wide.
package memory

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// Manager handles agent memory persistence and retrieval.
type Manager struct {
	db *gorm.DB
}

// NewManager creates a Manager backed by the given database.
func NewManager(db *gorm.DB) *Manager {
	return &Manager{db: db}
}

// StoreMemory creates and persists a Memory record. CreatedAt is set to now.
func (m *Manager) StoreMemory(agentID uuid.UUID, content, memoryType string, taskID *uuid.UUID, metadata map[string]any) (*model.Memory, error) {
	mem := &model.Memory{
		ID:         uuid.New(),
		AgentID:    agentID,
		TaskID:     taskID,
		Content:    content,
		MemoryType: memoryType,
		Metadata:   model.JSONField(metadata),
		CreatedAt:  time.Now(),
	}

	if err := m.db.Create(mem).Error; err != nil {
		return nil, fmt.Errorf("store memory: %w", err)
	}
	return mem, nil
}

// GetMemories retrieves memories with optional filters, ordered by CreatedAt
// descending. If limit is <= 0, it defaults to 50.
func (m *Manager) GetMemories(agentID *uuid.UUID, taskID *uuid.UUID, memoryType string, limit int) ([]model.Memory, error) {
	if limit <= 0 {
		limit = 50
	}

	q := m.db.Model(&model.Memory{}).Order("created_at DESC")

	if agentID != nil {
		q = q.Where("agent_id = ?", *agentID)
	}
	if taskID != nil {
		q = q.Where("task_id = ?", *taskID)
	}
	if memoryType != "" {
		q = q.Where("memory_type = ?", memoryType)
	}

	q = q.Limit(limit)

	var memories []model.Memory
	if err := q.Find(&memories).Error; err != nil {
		return nil, fmt.Errorf("get memories: %w", err)
	}
	return memories, nil
}

// GetProjectMemories retrieves memories across all agents in a project by
// joining through the Agent table on ProjectID. Results are ordered by
// CreatedAt descending.
func (m *Manager) GetProjectMemories(projectID uuid.UUID, memoryTypes []string, limit int) ([]model.Memory, error) {
	if limit <= 0 {
		limit = 50
	}

	q := m.db.Model(&model.Memory{}).
		Joins("JOIN agents ON memories.agent_id = agents.id").
		Where("agents.project_id = ?", projectID).
		Order("memories.created_at DESC")

	if len(memoryTypes) > 0 {
		q = q.Where("memories.memory_type IN ?", memoryTypes)
	}

	q = q.Limit(limit)

	var memories []model.Memory
	if err := q.Find(&memories).Error; err != nil {
		return nil, fmt.Errorf("get project memories: %w", err)
	}
	return memories, nil
}

// CompactAgentMemory compacts an agent's memories into a summary:
//  1. Get all non-archived memories for the agent
//  2. Group by MemoryType
//  3. Build a markdown summary grouped by type
//  4. Update the Agent's MemorySummary field in DB
//  5. Rename old memory types to archived_<type>
//  6. Return the summary string
func (m *Manager) CompactAgentMemory(agentID uuid.UUID) (string, error) {
	// 1. Fetch all non-archived, non-summary memories ordered chronologically
	var memories []model.Memory
	err := m.db.
		Where("agent_id = ?", agentID).
		Where("memory_type NOT LIKE ?", "archived_%").
		Where("memory_type != ?", "conversation_summary").
		Order("created_at ASC").
		Find(&memories).Error
	if err != nil {
		return "", fmt.Errorf("compact agent memory: fetch: %w", err)
	}

	if len(memories) == 0 {
		return "", nil
	}

	// 2. Group by MemoryType
	grouped := make(map[string][]model.Memory)
	for _, mem := range memories {
		grouped[mem.MemoryType] = append(grouped[mem.MemoryType], mem)
	}

	// 3. Build structured summary
	var sections []string

	// Process known types in a stable order
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

	// Include any other types not covered above
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

	summaryText := strings.Join(sections, "\n\n")

	// 4. Update Agent.MemorySummary
	if err := m.db.Model(&model.Agent{}).Where("id = ?", agentID).Update("memory_summary", summaryText).Error; err != nil {
		return "", fmt.Errorf("compact agent memory: update agent: %w", err)
	}

	// 5. Archive old memories by renaming type to archived_<type>
	for _, mem := range memories {
		archivedType := fmt.Sprintf("archived_%s", mem.MemoryType)
		if err := m.db.Model(&model.Memory{}).Where("id = ?", mem.ID).Update("memory_type", archivedType).Error; err != nil {
			return "", fmt.Errorf("compact agent memory: archive memory %s: %w", mem.ID, err)
		}
	}

	return summaryText, nil
}

// BuildAgentContext builds a context string suitable for prompt injection.
// It combines:
//  1. Agent's MemorySummary (if set)
//  2. Recent task-specific memories (last 20)
//  3. Project-wide decisions and lessons (last 10)
//
// The result is truncated to maxTokens (estimated at ~4 chars per token).
// If maxTokens is <= 0, it defaults to 8000.
func (m *Manager) BuildAgentContext(agentID, taskID uuid.UUID, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = 8000
	}
	maxChars := maxTokens * 4

	var parts []string

	// 1. Agent's MemorySummary
	var agent model.Agent
	if err := m.db.Where("id = ?", agentID).First(&agent).Error; err != nil {
		return "", fmt.Errorf("build agent context: load agent: %w", err)
	}

	if agent.MemorySummary != "" {
		parts = append(parts, "# Agent Memory Summary\n\n"+agent.MemorySummary)
	}

	// 2. Recent task-specific memories (last 20)
	var taskMemories []model.Memory
	err := m.db.
		Where("agent_id = ?", agentID).
		Where("task_id = ?", taskID).
		Where("memory_type NOT LIKE ?", "archived_%").
		Where("memory_type != ?", "conversation_summary").
		Order("created_at DESC").
		Limit(20).
		Find(&taskMemories).Error
	if err != nil {
		return "", fmt.Errorf("build agent context: task memories: %w", err)
	}

	if len(taskMemories) > 0 {
		var items []string
		// Reverse to chronological order
		for i := len(taskMemories) - 1; i >= 0; i-- {
			mem := taskMemories[i]
			items = append(items, fmt.Sprintf("- [%s] %s", mem.MemoryType, mem.Content))
		}
		parts = append(parts, "# Recent Task Memories\n\n"+strings.Join(items, "\n"))
	}

	// 3. Project-wide decisions and lessons (last 10)
	var projectMemories []model.Memory
	err = m.db.Model(&model.Memory{}).
		Joins("JOIN agents ON memories.agent_id = agents.id").
		Where("agents.project_id = ?", agent.ProjectID).
		Where("agents.id != ?", agentID).
		Where("memories.memory_type IN ?", []string{"decision", "lesson"}).
		Order("memories.created_at DESC").
		Limit(10).
		Find(&projectMemories).Error
	if err != nil {
		return "", fmt.Errorf("build agent context: project memories: %w", err)
	}

	if len(projectMemories) > 0 {
		var items []string
		// Reverse to chronological order
		for i := len(projectMemories) - 1; i >= 0; i-- {
			mem := projectMemories[i]
			items = append(items, fmt.Sprintf("- [%s] %s", mem.MemoryType, mem.Content))
		}
		parts = append(parts, "# Project-Wide Context\n\n"+strings.Join(items, "\n"))
	}

	context := strings.Join(parts, "\n\n---\n\n")

	// 4. Truncate if over maxTokens
	if len(context) > maxChars {
		context = context[:maxChars]
	}

	return context, nil
}

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

// ExtractMemoriesFromOutput parses agent output for structured memories using
// regex patterns. For each match it calls StoreMemory with the appropriate
// type. Returns all created memories.
func (m *Manager) ExtractMemoriesFromOutput(agentID, taskID uuid.UUID, output string) ([]model.Memory, error) {
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

	// Deduplicate
	seen := make(map[extractEntry]bool)
	var unique []extractEntry
	for _, e := range extracted {
		if !seen[e] {
			seen[e] = true
			unique = append(unique, e)
		}
	}

	// Store each extracted memory
	tid := &taskID
	var memories []model.Memory
	for _, e := range unique {
		mem, err := m.StoreMemory(agentID, e.content, e.memoryType, tid, nil)
		if err != nil {
			return memories, fmt.Errorf("extract memories: store %q: %w", e.memoryType, err)
		}
		memories = append(memories, *mem)
	}

	return memories, nil
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
