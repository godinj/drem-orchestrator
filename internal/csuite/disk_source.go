package csuite

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// knownAgents is the fixed set of C-Suite agents to monitor.
var knownAgents = []string{"kyle", "mike", "alex", "ross", "seth"}

// Heartbeat freshness thresholds for deriving AgentMonStatus.
const (
	onlineThreshold = 5 * time.Minute
	staleThreshold  = 30 * time.Minute
)

// DiskSnapshotSource reads agent state from the filesystem
// (~/.drem-csuite/*/state.md) to satisfy the SnapshotSource interface.
// It replaces the DB-backed DashboardQuery for environments where agent
// state lives on disk rather than in a database.
type DiskSnapshotSource struct {
	dir string // root directory, e.g. ~/.drem-csuite
}

// NewDiskSnapshotSource creates a DiskSnapshotSource rooted at dir.
// If dir is empty, it defaults to ~/.drem-csuite.
func NewDiskSnapshotSource(dir string) *DiskSnapshotSource {
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".drem-csuite")
	}
	return &DiskSnapshotSource{dir: dir}
}

// AgentSummaries returns one AgentSummary per known agent, populated from
// their on-disk state.md frontmatter and inbox message counts. Missing or
// malformed files produce an offline agent with zeroed fields — this method
// never returns an error.
func (d *DiskSnapshotSource) AgentSummaries() ([]AgentSummary, error) {
	counts, _ := d.UnreadMessageCounts()
	now := time.Now()
	summaries := make([]AgentSummary, len(knownAgents))

	for i, name := range knownAgents {
		agent := d.readAgent(name, now)
		summaries[i] = AgentSummary{
			CsuiteAgent: agent,
			UnreadCount: counts[name],
		}
	}
	return summaries, nil
}

// UnreadMessageCounts returns the number of .md files in each known agent's
// inbox/ directory. Non-.md files, subdirectories, and .signal files are
// ignored. Missing directories yield zero. This method never returns an error.
func (d *DiskSnapshotSource) UnreadMessageCounts() (map[string]int, error) {
	counts := make(map[string]int, len(knownAgents))
	for _, name := range knownAgents {
		counts[name] = d.countInbox(name)
	}
	return counts, nil
}

// readAgent parses a single agent's state.md frontmatter and derives status.
func (d *DiskSnapshotSource) readAgent(name string, now time.Time) CsuiteAgent {
	agent := CsuiteAgent{
		ID:     uuid.NewSHA1(uuid.NameSpaceDNS, []byte("csuite."+name)),
		Name:   name,
		Status: AgentMonOffline,
	}

	statePath := filepath.Join(d.dir, name, "state.md")
	data, err := os.ReadFile(statePath)
	if err != nil {
		return agent
	}

	fm := parseFrontmatter(string(data))
	if fm == nil {
		return agent
	}

	if hbStr, ok := fm["last_heartbeat"]; ok {
		if t, err := time.Parse(time.RFC3339, hbStr); err == nil {
			agent.HeartbeatAt = &t
			agent.Status = statusFromHeartbeat(t, now)
		}
	}

	if cpStr, ok := fm["context_percent"]; ok {
		if v, err := strconv.Atoi(cpStr); err == nil {
			agent.ContextPercent = v
		}
	}

	if act, ok := fm["current_activity"]; ok {
		agent.CurrentActivity = act
	}

	return agent
}

// countInbox counts .md files (not .md.signal, not subdirectories) in the
// agent's inbox/ directory.
func (d *DiskSnapshotSource) countInbox(name string) int {
	inboxPath := filepath.Join(d.dir, name, "inbox")
	entries, err := os.ReadDir(inboxPath)
	if err != nil {
		return 0
	}

	count := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasSuffix(n, ".md") && !strings.HasSuffix(n, ".md.signal") {
			count++
		}
	}
	return count
}

// statusFromHeartbeat derives AgentMonStatus from heartbeat age.
func statusFromHeartbeat(heartbeat, now time.Time) AgentMonStatus {
	age := now.Sub(heartbeat)
	switch {
	case age <= onlineThreshold:
		return AgentMonOnline
	case age <= staleThreshold:
		return AgentMonStale
	default:
		return AgentMonOffline
	}
}

// parseFrontmatter extracts key-value pairs from YAML-style frontmatter
// delimited by "---" lines. Returns nil if no valid frontmatter is found.
func parseFrontmatter(content string) map[string]string {
	lines := strings.Split(content, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return nil
	}

	result := make(map[string]string)
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			break
		}
		idx := strings.Index(trimmed, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:idx])
		val := strings.TrimSpace(trimmed[idx+1:])
		if key != "" {
			result[key] = val
		}
	}

	if len(result) == 0 {
		return nil
	}
	return result
}
