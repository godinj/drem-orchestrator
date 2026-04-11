package ctxmon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func TestReadOpenCodeJSONLUsage(t *testing.T) {
	tests := []struct {
		name          string
		events        []openCodeStepEvent
		ctxWindow     int
		wantNil       bool
		wantUsedPct   int
		wantTotalIn   int
		wantTotalOut  int
		wantCtxWindow int
	}{
		{
			name:    "no file",
			wantNil: true,
		},
		{
			name:    "empty file",
			events:  []openCodeStepEvent{},
			wantNil: true,
		},
		{
			name: "single step",
			events: []openCodeStepEvent{
				stepFinish(10000, 500),
			},
			ctxWindow:     43904,
			wantUsedPct:   22, // 10000 * 100 / 43904 = 22
			wantTotalIn:   10000,
			wantTotalOut:  500,
			wantCtxWindow: 43904,
		},
		{
			name: "multiple steps growing context",
			events: []openCodeStepEvent{
				stepFinish(5000, 200),
				stepFinish(12000, 300),
				stepFinish(22000, 150),
			},
			ctxWindow:     43904,
			wantUsedPct:   50, // 22000 * 100 / 43904 = 50
			wantTotalIn:   39000,
			wantTotalOut:  650,
			wantCtxWindow: 43904,
		},
		{
			name: "near full context",
			events: []openCodeStepEvent{
				stepFinish(40000, 100),
				stepFinish(43000, 200),
			},
			ctxWindow:     43904,
			wantUsedPct:   97, // 43000 * 100 / 43904 = 97
			wantTotalIn:   83000,
			wantTotalOut:  300,
			wantCtxWindow: 43904,
		},
		{
			name: "over context window caps at 100",
			events: []openCodeStepEvent{
				stepFinish(50000, 100),
			},
			ctxWindow:     43904,
			wantUsedPct:   100,
			wantTotalIn:   50000,
			wantTotalOut:  100,
			wantCtxWindow: 43904,
		},
		{
			name: "default context window",
			events: []openCodeStepEvent{
				stepFinish(10000, 100),
			},
			ctxWindow:     0, // should default to 43904
			wantUsedPct:   22,
			wantTotalIn:   10000,
			wantTotalOut:  100,
			wantCtxWindow: DefaultOpenCodeContextWindow,
		},
		{
			name: "non step_finish events ignored",
			events: func() []openCodeStepEvent {
				other := openCodeStepEvent{}
				other.Type = "text_delta"
				other.Part.Tokens.Input = 99999
				finish := stepFinish(8000, 200)
				return []openCodeStepEvent{other, finish}
			}(),
			ctxWindow:     43904,
			wantUsedPct:   18,
			wantTotalIn:   8000,
			wantTotalOut:  200,
			wantCtxWindow: 43904,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "agent-output.jsonl")

			if tt.name == "no file" {
				// Don't create the file.
			} else {
				writeJSONLEvents(t, logPath, tt.events)
			}

			usage, err := ReadOpenCodeJSONLUsage(logPath, tt.ctxWindow)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantNil {
				if usage != nil {
					t.Fatalf("expected nil, got %+v", usage)
				}
				return
			}

			if usage == nil {
				t.Fatal("expected non-nil usage")
			}
			if usage.UsedPercent != tt.wantUsedPct {
				t.Errorf("UsedPercent = %d, want %d", usage.UsedPercent, tt.wantUsedPct)
			}
			if usage.TotalInputTokens != tt.wantTotalIn {
				t.Errorf("TotalInputTokens = %d, want %d", usage.TotalInputTokens, tt.wantTotalIn)
			}
			if usage.TotalOutputTokens != tt.wantTotalOut {
				t.Errorf("TotalOutputTokens = %d, want %d", usage.TotalOutputTokens, tt.wantTotalOut)
			}
			if usage.ContextWindowSize != tt.wantCtxWindow {
				t.Errorf("ContextWindowSize = %d, want %d", usage.ContextWindowSize, tt.wantCtxWindow)
			}
			if usage.RemainingPercent != 100-tt.wantUsedPct {
				t.Errorf("RemainingPercent = %d, want %d", usage.RemainingPercent, 100-tt.wantUsedPct)
			}
			if usage.TotalCostUSD != 0 {
				t.Errorf("TotalCostUSD = %f, want 0 (local model)", usage.TotalCostUSD)
			}
			if usage.LastUpdated.IsZero() {
				t.Error("LastUpdated should be set")
			}
		})
	}
}

func TestReadOpenCodeDBUsage(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	worktree := "/tmp/test-worktree"

	// Create a minimal OpenCode DB schema.
	db := testutil.NewTestOpenCodeDB(t, dbPath)

	// Insert a session matching our worktree.
	sessionID := "ses_test123"
	_, err := db.Exec(`INSERT INTO session (id, project_id, directory, slug, title, version, time_created, time_updated)
		VALUES (?, 'proj1', ?, 'test', 'Test Session', '1', 1000, 2000)`, sessionID, worktree)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}

	// Insert messages with token data.
	messages := []struct {
		id        string
		tokensIn  int
		tokensOut int
		created   int
	}{
		{"msg1", 5000, 200, 100},
		{"msg2", 12000, 300, 200},
		{"msg3", 22000, 150, 300},
	}
	for _, m := range messages {
		data := map[string]any{
			"role":   "assistant",
			"tokens": map[string]int{"input": m.tokensIn, "output": m.tokensOut},
		}
		dataJSON, _ := json.Marshal(data)
		_, err := db.Exec(`INSERT INTO message (id, session_id, time_created, time_updated, data)
			VALUES (?, ?, ?, ?, ?)`, m.id, sessionID, m.created, m.created, string(dataJSON))
		if err != nil {
			t.Fatalf("insert message %s: %v", m.id, err)
		}
	}
	db.Close()

	usage, err := ReadOpenCodeDBUsage(dbPath, worktree, 43904)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage == nil {
		t.Fatal("expected non-nil usage")
	}

	// Last message had 22000 input tokens → 22000 * 100 / 43904 = 50%
	if usage.UsedPercent != 50 {
		t.Errorf("UsedPercent = %d, want 50", usage.UsedPercent)
	}
	if usage.TotalInputTokens != 39000 {
		t.Errorf("TotalInputTokens = %d, want 39000", usage.TotalInputTokens)
	}
	if usage.TotalOutputTokens != 650 {
		t.Errorf("TotalOutputTokens = %d, want 650", usage.TotalOutputTokens)
	}
	if usage.ContextWindowSize != 43904 {
		t.Errorf("ContextWindowSize = %d, want 43904", usage.ContextWindowSize)
	}
}

func TestReadOpenCodeDBUsage_NoMatchingSession(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")

	db := testutil.NewTestOpenCodeDB(t, dbPath)
	// Insert a session for a different directory.
	_, err := db.Exec(`INSERT INTO session (id, project_id, directory, slug, title, version, time_created, time_updated)
		VALUES ('ses_other', 'proj1', '/other/dir', 'other', 'Other', '1', 1000, 2000)`)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	db.Close()

	usage, err := ReadOpenCodeDBUsage(dbPath, "/tmp/no-match", 43904)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage != nil {
		t.Fatalf("expected nil for non-matching directory, got %+v", usage)
	}
}

func TestReadOpenCodeDBUsage_DBNotFound(t *testing.T) {
	usage, err := ReadOpenCodeDBUsage("/nonexistent/path/opencode.db", "/tmp/wt", 43904)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage != nil {
		t.Fatalf("expected nil for missing DB, got %+v", usage)
	}
}

func TestReadOpenCodeDBUsage_PicksLatestSession(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	worktree := "/tmp/wt"

	db := testutil.NewTestOpenCodeDB(t, dbPath)

	// Insert an older session.
	_, err := db.Exec(`INSERT INTO session (id, project_id, directory, slug, title, version, time_created, time_updated)
		VALUES ('ses_old', 'proj1', ?, 'old', 'Old', '1', 1000, 1500)`, worktree)
	if err != nil {
		t.Fatal(err)
	}
	oldData, _ := json.Marshal(map[string]any{"tokens": map[string]int{"input": 5000, "output": 100}})
	db.Exec(`INSERT INTO message (id, session_id, time_created, time_updated, data)
		VALUES ('msg_old', 'ses_old', 100, 100, ?)`, string(oldData))

	// Insert a newer session with higher token usage.
	_, err = db.Exec(`INSERT INTO session (id, project_id, directory, slug, title, version, time_created, time_updated)
		VALUES ('ses_new', 'proj1', ?, 'new', 'New', '1', 2000, 3000)`, worktree)
	if err != nil {
		t.Fatal(err)
	}
	newData, _ := json.Marshal(map[string]any{"tokens": map[string]int{"input": 30000, "output": 500}})
	db.Exec(`INSERT INTO message (id, session_id, time_created, time_updated, data)
		VALUES ('msg_new', 'ses_new', 200, 200, ?)`, string(newData))

	db.Close()

	usage, err := ReadOpenCodeDBUsage(dbPath, worktree, 43904)
	if err != nil {
		t.Fatal(err)
	}
	if usage == nil {
		t.Fatal("expected non-nil usage")
	}
	// Should pick the newer session (30000 input tokens).
	if usage.TotalInputTokens != 30000 {
		t.Errorf("TotalInputTokens = %d, want 30000 (from newer session)", usage.TotalInputTokens)
	}
}

func TestParseVLLMMetrics(t *testing.T) {
	metricsText := `# HELP vllm:num_requests_running Number of requests in model execution batches.
# TYPE vllm:num_requests_running gauge
vllm:num_requests_running{engine="0",model_name="qwen35-27b-autoround"} 2.0
# HELP vllm:num_requests_waiting Number of requests waiting to be processed.
# TYPE vllm:num_requests_waiting gauge
vllm:num_requests_waiting{engine="0",model_name="qwen35-27b-autoround"} 3.0
# HELP vllm:prompt_tokens_total Number of prefill tokens processed.
# TYPE vllm:prompt_tokens_total counter
vllm:prompt_tokens_total{engine="0",model_name="qwen35-27b-autoround"} 1.263648e+06
# HELP vllm:generation_tokens_total Number of generation tokens processed.
# TYPE vllm:generation_tokens_total counter
vllm:generation_tokens_total{engine="0",model_name="qwen35-27b-autoround"} 45678.0
`

	m, err := ParseVLLMMetrics(metricsText)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if m.PromptTokensTotal != 1263648 {
		t.Errorf("PromptTokensTotal = %d, want 1263648", m.PromptTokensTotal)
	}
	if m.GenerationTokensTotal != 45678 {
		t.Errorf("GenerationTokensTotal = %d, want 45678", m.GenerationTokensTotal)
	}
	if m.RequestsRunning != 2 {
		t.Errorf("RequestsRunning = %d, want 2", m.RequestsRunning)
	}
	if m.RequestsWaiting != 3 {
		t.Errorf("RequestsWaiting = %d, want 3", m.RequestsWaiting)
	}
}

func TestParseVLLMMetrics_Empty(t *testing.T) {
	m, err := ParseVLLMMetrics("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.PromptTokensTotal != 0 {
		t.Errorf("PromptTokensTotal = %d, want 0", m.PromptTokensTotal)
	}
}

func TestParseVLLMMetrics_CommentsOnly(t *testing.T) {
	m, err := ParseVLLMMetrics("# HELP foo\n# TYPE foo gauge\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.PromptTokensTotal != 0 {
		t.Errorf("PromptTokensTotal = %d, want 0", m.PromptTokensTotal)
	}
}

func TestBuildOpenCodeUsage(t *testing.T) {
	tests := []struct {
		name        string
		totalIn     int
		totalOut    int
		lastIn      int
		ctxWindow   int
		wantPct     int
		wantCtxSize int
	}{
		{
			name:        "normal usage",
			totalIn:     50000,
			totalOut:    2000,
			lastIn:      20000,
			ctxWindow:   43904,
			wantPct:     45,
			wantCtxSize: 43904,
		},
		{
			name:        "zero context window uses default",
			totalIn:     10000,
			totalOut:    500,
			lastIn:      10000,
			ctxWindow:   0,
			wantPct:     22,
			wantCtxSize: DefaultOpenCodeContextWindow,
		},
		{
			name:        "negative context window uses default",
			totalIn:     10000,
			totalOut:    500,
			lastIn:      10000,
			ctxWindow:   -1,
			wantPct:     22,
			wantCtxSize: DefaultOpenCodeContextWindow,
		},
		{
			name:        "zero last input",
			totalIn:     10000,
			totalOut:    500,
			lastIn:      0,
			ctxWindow:   43904,
			wantPct:     0,
			wantCtxSize: 43904,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := buildOpenCodeUsage(tt.totalIn, tt.totalOut, tt.lastIn, tt.ctxWindow)
			if u.UsedPercent != tt.wantPct {
				t.Errorf("UsedPercent = %d, want %d", u.UsedPercent, tt.wantPct)
			}
			if u.ContextWindowSize != tt.wantCtxSize {
				t.Errorf("ContextWindowSize = %d, want %d", u.ContextWindowSize, tt.wantCtxSize)
			}
			if u.RemainingPercent != 100-tt.wantPct {
				t.Errorf("RemainingPercent = %d, want %d", u.RemainingPercent, 100-tt.wantPct)
			}
			if u.TotalCostUSD != 0 {
				t.Error("TotalCostUSD should be 0 for local model")
			}
		})
	}
}

func TestOpenCodeLogPath(t *testing.T) {
	got := OpenCodeLogPath("/tmp/worktree")
	want := "/tmp/worktree/.opencode/agent-output.jsonl"
	if got != want {
		t.Errorf("OpenCodeLogPath = %q, want %q", got, want)
	}
}

func TestDefaultOpenCodeDBPath(t *testing.T) {
	path := DefaultOpenCodeDBPath()
	if path == "" {
		t.Skip("could not determine home directory")
	}
	if !filepath.IsAbs(path) {
		t.Errorf("expected absolute path, got %q", path)
	}
	if filepath.Base(path) != "opencode.db" {
		t.Errorf("expected basename opencode.db, got %q", filepath.Base(path))
	}
}

// --- test helpers ---

func stepFinish(inputTokens, outputTokens int) openCodeStepEvent {
	var e openCodeStepEvent
	e.Type = "step_finish"
	e.Part.Tokens.Input = inputTokens
	e.Part.Tokens.Output = outputTokens
	return e
}

func writeJSONLEvents(t *testing.T, path string, events []openCodeStepEvent) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create jsonl file: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			t.Fatalf("encode event: %v", err)
		}
	}
}
