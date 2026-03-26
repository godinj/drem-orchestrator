package csuite_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/csuite"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// setupDiskDir creates a temporary CSUITE_DIR with the given agent state files
// and inbox messages. Returns the root directory path.
func setupDiskDir(t *testing.T, agents map[string]string, inboxCounts map[string]int) string {
	t.Helper()
	root := t.TempDir()

	for name, stateContent := range agents {
		agentDir := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(agentDir, "inbox"), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", agentDir, err)
		}
		if stateContent != "" {
			if err := os.WriteFile(filepath.Join(agentDir, "state.md"), []byte(stateContent), 0o644); err != nil {
				t.Fatalf("write state.md: %v", err)
			}
		}
	}

	for name, count := range inboxCounts {
		inboxDir := filepath.Join(root, name, "inbox")
		// Ensure inbox dir exists even if agent not in agents map.
		if err := os.MkdirAll(inboxDir, 0o755); err != nil {
			t.Fatalf("mkdir inbox: %v", err)
		}
		for i := 0; i < count; i++ {
			fname := filepath.Join(inboxDir, time.Now().Format("20060102-150405")+"-test"+string(rune('a'+i))+".md")
			if err := os.WriteFile(fname, []byte("test message"), 0o644); err != nil {
				t.Fatalf("write inbox message: %v", err)
			}
		}
	}

	return root
}

// ---------------------------------------------------------------------------
// Interface compliance
// ---------------------------------------------------------------------------

func TestDiskSnapshotSource_ImplementsSnapshotSource(t *testing.T) {
	src := csuite.NewDiskSnapshotSource(t.TempDir())
	// Compile-time interface check.
	var _ csuite.SnapshotSource = src
}

// ---------------------------------------------------------------------------
// AgentSummaries
// ---------------------------------------------------------------------------

func TestDiskSnapshotSource_AgentSummaries_AllOnline(t *testing.T) {
	now := time.Now().UTC()
	freshHeartbeat := now.Format(time.RFC3339)

	root := setupDiskDir(t, map[string]string{
		"kyle": "---\nlast_heartbeat: " + freshHeartbeat + "\ncontext_percent: 55\ncurrent_activity: planning\n---\n",
		"mike": "---\nlast_heartbeat: " + freshHeartbeat + "\ncontext_percent: 25\ncurrent_activity: monitoring\n---\n",
		"alex": "---\nlast_heartbeat: " + freshHeartbeat + "\ncontext_percent: 80\ncurrent_activity: coding\n---\n",
		"ross": "---\nlast_heartbeat: " + freshHeartbeat + "\ncontext_percent: 10\ncurrent_activity: testing\n---\n",
		"seth": "---\nlast_heartbeat: " + freshHeartbeat + "\ncontext_percent: 40\ncurrent_activity: reviewing\n---\n",
	}, nil)

	src := csuite.NewDiskSnapshotSource(root)
	summaries, err := src.AgentSummaries()
	if err != nil {
		t.Fatalf("AgentSummaries() error: %v", err)
	}

	if len(summaries) != 5 {
		t.Fatalf("expected 5 summaries, got %d", len(summaries))
	}

	byName := make(map[string]csuite.AgentSummary)
	for _, s := range summaries {
		byName[s.Name] = s
	}

	kyle := byName["kyle"]
	if kyle.Status != csuite.AgentMonOnline {
		t.Errorf("kyle: expected online, got %s", kyle.Status)
	}
	if kyle.ContextPercent != 55 {
		t.Errorf("kyle: expected context_percent 55, got %d", kyle.ContextPercent)
	}
	if kyle.CurrentActivity != "planning" {
		t.Errorf("kyle: expected activity 'planning', got %q", kyle.CurrentActivity)
	}
}

func TestDiskSnapshotSource_AgentSummaries_StaleHeartbeat(t *testing.T) {
	stale := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)

	root := setupDiskDir(t, map[string]string{
		"kyle": "---\nlast_heartbeat: " + stale + "\ncontext_percent: 55\ncurrent_activity: planning\n---\n",
		"mike": "",
		"alex": "",
		"ross": "",
		"seth": "",
	}, nil)

	src := csuite.NewDiskSnapshotSource(root)
	summaries, err := src.AgentSummaries()
	if err != nil {
		t.Fatalf("AgentSummaries() error: %v", err)
	}

	byName := make(map[string]csuite.AgentSummary)
	for _, s := range summaries {
		byName[s.Name] = s
	}

	if byName["kyle"].Status != csuite.AgentMonStale {
		t.Errorf("kyle: expected stale (10m old heartbeat), got %s", byName["kyle"].Status)
	}
}

func TestDiskSnapshotSource_AgentSummaries_OfflineHeartbeat(t *testing.T) {
	old := time.Now().UTC().Add(-45 * time.Minute).Format(time.RFC3339)

	root := setupDiskDir(t, map[string]string{
		"kyle": "---\nlast_heartbeat: " + old + "\ncontext_percent: 55\ncurrent_activity: planning\n---\n",
		"mike": "",
		"alex": "",
		"ross": "",
		"seth": "",
	}, nil)

	src := csuite.NewDiskSnapshotSource(root)
	summaries, err := src.AgentSummaries()
	if err != nil {
		t.Fatalf("AgentSummaries() error: %v", err)
	}

	byName := make(map[string]csuite.AgentSummary)
	for _, s := range summaries {
		byName[s.Name] = s
	}

	if byName["kyle"].Status != csuite.AgentMonOffline {
		t.Errorf("kyle: expected offline (45m old heartbeat), got %s", byName["kyle"].Status)
	}
}

func TestDiskSnapshotSource_AgentSummaries_MissingStateFile(t *testing.T) {
	root := setupDiskDir(t, map[string]string{
		"kyle": "",
		"mike": "",
		"alex": "",
		"ross": "",
		"seth": "",
	}, nil)

	// Remove kyle's state.md to simulate missing file.
	os.Remove(filepath.Join(root, "kyle", "state.md"))

	src := csuite.NewDiskSnapshotSource(root)
	summaries, err := src.AgentSummaries()
	if err != nil {
		t.Fatalf("AgentSummaries() error: %v", err)
	}

	byName := make(map[string]csuite.AgentSummary)
	for _, s := range summaries {
		byName[s.Name] = s
	}

	kyle := byName["kyle"]
	if kyle.Status != csuite.AgentMonOffline {
		t.Errorf("missing state.md: expected offline, got %s", kyle.Status)
	}
	if kyle.ContextPercent != 0 {
		t.Errorf("missing state.md: expected context_percent 0, got %d", kyle.ContextPercent)
	}
	if kyle.CurrentActivity != "" {
		t.Errorf("missing state.md: expected empty activity, got %q", kyle.CurrentActivity)
	}
}

func TestDiskSnapshotSource_AgentSummaries_MalformedFrontmatter(t *testing.T) {
	root := setupDiskDir(t, map[string]string{
		"kyle": "not valid frontmatter at all",
		"mike": "",
		"alex": "",
		"ross": "",
		"seth": "",
	}, nil)

	src := csuite.NewDiskSnapshotSource(root)
	summaries, err := src.AgentSummaries()
	if err != nil {
		t.Fatalf("AgentSummaries() error: %v", err)
	}

	byName := make(map[string]csuite.AgentSummary)
	for _, s := range summaries {
		byName[s.Name] = s
	}

	kyle := byName["kyle"]
	if kyle.Status != csuite.AgentMonOffline {
		t.Errorf("malformed: expected offline, got %s", kyle.Status)
	}
}

func TestDiskSnapshotSource_AgentSummaries_MissingAgentDir(t *testing.T) {
	// Create only kyle's directory; others should still appear as offline.
	root := t.TempDir()
	kyleDir := filepath.Join(root, "kyle")
	os.MkdirAll(filepath.Join(kyleDir, "inbox"), 0o755)

	now := time.Now().UTC().Format(time.RFC3339)
	os.WriteFile(filepath.Join(kyleDir, "state.md"),
		[]byte("---\nlast_heartbeat: "+now+"\ncontext_percent: 10\ncurrent_activity: test\n---\n"), 0o644)

	src := csuite.NewDiskSnapshotSource(root)
	summaries, err := src.AgentSummaries()
	if err != nil {
		t.Fatalf("AgentSummaries() error: %v", err)
	}

	if len(summaries) != 5 {
		t.Fatalf("expected 5 summaries (all known agents), got %d", len(summaries))
	}

	byName := make(map[string]csuite.AgentSummary)
	for _, s := range summaries {
		byName[s.Name] = s
	}

	if byName["kyle"].Status != csuite.AgentMonOnline {
		t.Errorf("kyle: expected online, got %s", byName["kyle"].Status)
	}
	if byName["mike"].Status != csuite.AgentMonOffline {
		t.Errorf("mike: expected offline (no dir), got %s", byName["mike"].Status)
	}
}

// ---------------------------------------------------------------------------
// UnreadMessageCounts
// ---------------------------------------------------------------------------

func TestDiskSnapshotSource_UnreadMessageCounts_Basic(t *testing.T) {
	root := setupDiskDir(t, map[string]string{
		"kyle": "",
		"mike": "",
		"alex": "",
		"ross": "",
		"seth": "",
	}, map[string]int{
		"kyle": 3,
		"mike": 0,
		"alex": 1,
	})

	src := csuite.NewDiskSnapshotSource(root)
	counts, err := src.UnreadMessageCounts()
	if err != nil {
		t.Fatalf("UnreadMessageCounts() error: %v", err)
	}

	if counts["kyle"] != 3 {
		t.Errorf("kyle: expected 3, got %d", counts["kyle"])
	}
	if counts["mike"] != 0 {
		t.Errorf("mike: expected 0, got %d", counts["mike"])
	}
	if counts["alex"] != 1 {
		t.Errorf("alex: expected 1, got %d", counts["alex"])
	}
}

func TestDiskSnapshotSource_UnreadMessageCounts_IgnoresNonMd(t *testing.T) {
	root := setupDiskDir(t, map[string]string{
		"kyle": "",
		"mike": "",
		"alex": "",
		"ross": "",
		"seth": "",
	}, map[string]int{
		"kyle": 2,
	})

	// Add a .signal file and an archive directory — neither should be counted.
	inboxDir := filepath.Join(root, "kyle", "inbox")
	os.WriteFile(filepath.Join(inboxDir, "msg.md.signal"), []byte("signal"), 0o644)
	os.MkdirAll(filepath.Join(inboxDir, "archive"), 0o755)

	src := csuite.NewDiskSnapshotSource(root)
	counts, err := src.UnreadMessageCounts()
	if err != nil {
		t.Fatalf("UnreadMessageCounts() error: %v", err)
	}

	if counts["kyle"] != 2 {
		t.Errorf("kyle: expected 2 (ignoring .signal and archive), got %d", counts["kyle"])
	}
}

func TestDiskSnapshotSource_UnreadMessageCounts_MissingInboxDir(t *testing.T) {
	root := t.TempDir()
	// No agent directories at all.

	src := csuite.NewDiskSnapshotSource(root)
	counts, err := src.UnreadMessageCounts()
	if err != nil {
		t.Fatalf("UnreadMessageCounts() error: %v", err)
	}

	for _, name := range []string{"kyle", "mike", "alex", "ross", "seth"} {
		if counts[name] != 0 {
			t.Errorf("%s: expected 0, got %d", name, counts[name])
		}
	}
}

// ---------------------------------------------------------------------------
// Heartbeat boundary tests
// ---------------------------------------------------------------------------

func TestDiskSnapshotSource_HeartbeatBoundaries(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name     string
		age      time.Duration
		expected csuite.AgentMonStatus
	}{
		{"just_now", 0, csuite.AgentMonOnline},
		{"4m59s", 4*time.Minute + 59*time.Second, csuite.AgentMonOnline},
		{"5m01s", 5*time.Minute + 1*time.Second, csuite.AgentMonStale},
		{"29m59s", 29*time.Minute + 59*time.Second, csuite.AgentMonStale},
		{"30m01s", 30*time.Minute + 1*time.Second, csuite.AgentMonOffline},
		{"1h", time.Hour, csuite.AgentMonOffline},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hb := now.Add(-tt.age).Format(time.RFC3339)
			root := setupDiskDir(t, map[string]string{
				"kyle": "---\nlast_heartbeat: " + hb + "\ncontext_percent: 10\ncurrent_activity: test\n---\n",
				"mike": "",
				"alex": "",
				"ross": "",
				"seth": "",
			}, nil)

			src := csuite.NewDiskSnapshotSource(root)
			summaries, err := src.AgentSummaries()
			if err != nil {
				t.Fatalf("AgentSummaries() error: %v", err)
			}

			for _, s := range summaries {
				if s.Name == "kyle" {
					if s.Status != tt.expected {
						t.Errorf("heartbeat age %v: expected %s, got %s", tt.age, tt.expected, s.Status)
					}
					return
				}
			}
			t.Fatal("kyle not found in summaries")
		})
	}
}

// ---------------------------------------------------------------------------
// AgentSummaries includes UnreadCount
// ---------------------------------------------------------------------------

func TestDiskSnapshotSource_AgentSummaries_IncludesUnreadCount(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)

	root := setupDiskDir(t, map[string]string{
		"kyle": "---\nlast_heartbeat: " + now + "\ncontext_percent: 55\ncurrent_activity: planning\n---\n",
		"mike": "",
		"alex": "",
		"ross": "",
		"seth": "",
	}, map[string]int{
		"kyle": 5,
		"alex": 2,
	})

	src := csuite.NewDiskSnapshotSource(root)
	summaries, err := src.AgentSummaries()
	if err != nil {
		t.Fatalf("AgentSummaries() error: %v", err)
	}

	byName := make(map[string]csuite.AgentSummary)
	for _, s := range summaries {
		byName[s.Name] = s
	}

	if byName["kyle"].UnreadCount != 5 {
		t.Errorf("kyle: expected unread 5, got %d", byName["kyle"].UnreadCount)
	}
	if byName["alex"].UnreadCount != 2 {
		t.Errorf("alex: expected unread 2, got %d", byName["alex"].UnreadCount)
	}
	if byName["mike"].UnreadCount != 0 {
		t.Errorf("mike: expected unread 0, got %d", byName["mike"].UnreadCount)
	}
}

// ---------------------------------------------------------------------------
// Empty CSUITE_DIR
// ---------------------------------------------------------------------------

func TestDiskSnapshotSource_EmptyDir(t *testing.T) {
	root := t.TempDir()
	src := csuite.NewDiskSnapshotSource(root)

	summaries, err := src.AgentSummaries()
	if err != nil {
		t.Fatalf("AgentSummaries() error: %v", err)
	}
	if len(summaries) != 5 {
		t.Fatalf("expected 5 summaries even with empty dir, got %d", len(summaries))
	}
	for _, s := range summaries {
		if s.Status != csuite.AgentMonOffline {
			t.Errorf("%s: expected offline, got %s", s.Name, s.Status)
		}
	}

	counts, err := src.UnreadMessageCounts()
	if err != nil {
		t.Fatalf("UnreadMessageCounts() error: %v", err)
	}
	for _, name := range []string{"kyle", "mike", "alex", "ross", "seth"} {
		if counts[name] != 0 {
			t.Errorf("%s: expected 0, got %d", name, counts[name])
		}
	}
}
