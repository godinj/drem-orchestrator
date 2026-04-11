package ctxmon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseLlamaServerSlots(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantLen   int
		wantErr   bool
		checkSlot func(t *testing.T, slots []LlamaSlot)
	}{
		{
			name:    "empty array",
			input:   `[]`,
			wantLen: 0,
		},
		{
			name:    "invalid JSON",
			input:   `not json`,
			wantErr: true,
		},
		{
			name: "single idle slot",
			input: `[{
				"id": 0,
				"n_ctx": 131072,
				"n_past": 0,
				"n_predict": -1,
				"state": 0,
				"is_processing": false,
				"id_task": -1
			}]`,
			wantLen: 1,
			checkSlot: func(t *testing.T, slots []LlamaSlot) {
				s := slots[0]
				if s.ID != 0 {
					t.Errorf("ID = %d, want 0", s.ID)
				}
				if s.NCtx != 131072 {
					t.Errorf("NCtx = %d, want 131072", s.NCtx)
				}
				if s.NPast != 0 {
					t.Errorf("NPast = %d, want 0", s.NPast)
				}
				if s.State != LlamaSlotIdle {
					t.Errorf("State = %d, want %d (idle)", s.State, LlamaSlotIdle)
				}
				if s.IsActive {
					t.Error("IsActive should be false")
				}
			},
		},
		{
			name: "active slot with context usage",
			input: `[{
				"id": 0,
				"n_ctx": 73728,
				"n_past": 45000,
				"n_predict": 2048,
				"state": 1,
				"is_processing": true,
				"id_task": 42
			}]`,
			wantLen: 1,
			checkSlot: func(t *testing.T, slots []LlamaSlot) {
				s := slots[0]
				if s.NCtx != 73728 {
					t.Errorf("NCtx = %d, want 73728", s.NCtx)
				}
				if s.NPast != 45000 {
					t.Errorf("NPast = %d, want 45000", s.NPast)
				}
				if s.State != LlamaSlotProcessing {
					t.Errorf("State = %d, want %d (processing)", s.State, LlamaSlotProcessing)
				}
				if !s.IsActive {
					t.Error("IsActive should be true")
				}
				if s.TaskID != 42 {
					t.Errorf("TaskID = %d, want 42", s.TaskID)
				}
			},
		},
		{
			name: "multiple slots mixed state",
			input: `[
				{"id": 0, "n_ctx": 73728, "n_past": 30000, "state": 1, "is_processing": true, "id_task": 1, "n_predict": -1},
				{"id": 1, "n_ctx": 73728, "n_past": 0, "state": 0, "is_processing": false, "id_task": -1, "n_predict": -1},
				{"id": 2, "n_ctx": 73728, "n_past": 55000, "state": 1, "is_processing": true, "id_task": 3, "n_predict": -1}
			]`,
			wantLen: 3,
			checkSlot: func(t *testing.T, slots []LlamaSlot) {
				if slots[0].NPast != 30000 {
					t.Errorf("slot 0 NPast = %d, want 30000", slots[0].NPast)
				}
				if slots[1].NPast != 0 {
					t.Errorf("slot 1 NPast = %d, want 0", slots[1].NPast)
				}
				if slots[2].NPast != 55000 {
					t.Errorf("slot 2 NPast = %d, want 55000", slots[2].NPast)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slots, err := ParseLlamaServerSlots([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(slots) != tt.wantLen {
				t.Fatalf("len(slots) = %d, want %d", len(slots), tt.wantLen)
			}
			if tt.checkSlot != nil {
				tt.checkSlot(t, slots)
			}
		})
	}
}

func TestParseLlamaServerMetrics(t *testing.T) {
	metricsText := `# HELP llamacpp:kv_cache_usage_ratio KV-cache usage. 1 means 100 percent usage.
# TYPE llamacpp:kv_cache_usage_ratio gauge
llamacpp:kv_cache_usage_ratio 0.42
# HELP llamacpp:kv_cache_tokens_count KV-cache tokens count.
# TYPE llamacpp:kv_cache_tokens_count gauge
llamacpp:kv_cache_tokens_count 30966
# HELP llamacpp:requests_processing Number of requests processing.
# TYPE llamacpp:requests_processing gauge
llamacpp:requests_processing 2
# HELP llamacpp:requests_pending Number of requests pending.
# TYPE llamacpp:requests_pending gauge
llamacpp:requests_pending 1
# HELP llamacpp:tokens_predicted_total Number of generation tokens processed.
# TYPE llamacpp:tokens_predicted_total counter
llamacpp:tokens_predicted_total 98765
# HELP llamacpp:prompt_tokens_total Number of prompt tokens processed.
# TYPE llamacpp:prompt_tokens_total counter
llamacpp:prompt_tokens_total 543210
# HELP llamacpp:slots_idle Number of idle slots.
# TYPE llamacpp:slots_idle gauge
llamacpp:slots_idle 1
# HELP llamacpp:slots_processing Number of processing slots.
# TYPE llamacpp:slots_processing gauge
llamacpp:slots_processing 2
`
	m, err := ParseLlamaServerMetrics(metricsText)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if m.KVCacheUsageRatio != 0.42 {
		t.Errorf("KVCacheUsageRatio = %f, want 0.42", m.KVCacheUsageRatio)
	}
	if m.KVCacheTokens != 30966 {
		t.Errorf("KVCacheTokens = %d, want 30966", m.KVCacheTokens)
	}
	if m.RequestsProcessing != 2 {
		t.Errorf("RequestsProcessing = %d, want 2", m.RequestsProcessing)
	}
	if m.RequestsPending != 1 {
		t.Errorf("RequestsPending = %d, want 1", m.RequestsPending)
	}
	if m.TokensPredictedTotal != 98765 {
		t.Errorf("TokensPredictedTotal = %d, want 98765", m.TokensPredictedTotal)
	}
	if m.TokensEvaluatedTotal != 543210 {
		t.Errorf("TokensEvaluatedTotal = %d, want 543210", m.TokensEvaluatedTotal)
	}
	if m.SlotsIdle != 1 {
		t.Errorf("SlotsIdle = %d, want 1", m.SlotsIdle)
	}
	if m.SlotsProcessing != 2 {
		t.Errorf("SlotsProcessing = %d, want 2", m.SlotsProcessing)
	}
}

func TestParseLlamaServerMetrics_Empty(t *testing.T) {
	m, err := ParseLlamaServerMetrics("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.KVCacheUsageRatio != 0 {
		t.Errorf("KVCacheUsageRatio = %f, want 0", m.KVCacheUsageRatio)
	}
	if m.TokensPredictedTotal != 0 {
		t.Errorf("TokensPredictedTotal = %d, want 0", m.TokensPredictedTotal)
	}
}

func TestParseLlamaServerMetrics_CommentsOnly(t *testing.T) {
	m, err := ParseLlamaServerMetrics("# HELP foo\n# TYPE foo gauge\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.KVCacheUsageRatio != 0 {
		t.Errorf("KVCacheUsageRatio = %f, want 0", m.KVCacheUsageRatio)
	}
}

func TestLlamaServerSlotsUsage(t *testing.T) {
	tests := []struct {
		name           string
		slots          []LlamaSlot
		wantRatio      float64
		wantTokens     int64
		wantProcessing int
		wantIdle       int
	}{
		{
			name:  "no slots",
			slots: nil,
			// All zeros.
		},
		{
			name: "single active slot half full",
			slots: []LlamaSlot{
				{ID: 0, NCtx: 73728, NPast: 36864, State: LlamaSlotProcessing},
			},
			wantRatio:      0.5,
			wantTokens:     36864,
			wantProcessing: 1,
			wantIdle:       0,
		},
		{
			name: "mixed active and idle",
			slots: []LlamaSlot{
				{ID: 0, NCtx: 73728, NPast: 30000, State: LlamaSlotProcessing},
				{ID: 1, NCtx: 73728, NPast: 0, State: LlamaSlotIdle},
				{ID: 2, NCtx: 73728, NPast: 50000, State: LlamaSlotProcessing},
			},
			wantRatio:      float64(80000) / float64(73728*3),
			wantTokens:     80000,
			wantProcessing: 2,
			wantIdle:       1,
		},
		{
			name: "is_processing flag used",
			slots: []LlamaSlot{
				{ID: 0, NCtx: 73728, NPast: 20000, State: LlamaSlotIdle, IsActive: true},
			},
			wantRatio:      float64(20000) / float64(73728),
			wantTokens:     20000,
			wantProcessing: 1,
			wantIdle:       0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := LlamaServerSlotsUsage(tt.slots)
			if m.KVCacheTokens != tt.wantTokens {
				t.Errorf("KVCacheTokens = %d, want %d", m.KVCacheTokens, tt.wantTokens)
			}
			if m.SlotsProcessing != tt.wantProcessing {
				t.Errorf("SlotsProcessing = %d, want %d", m.SlotsProcessing, tt.wantProcessing)
			}
			if m.SlotsIdle != tt.wantIdle {
				t.Errorf("SlotsIdle = %d, want %d", m.SlotsIdle, tt.wantIdle)
			}
			// Allow small floating-point delta for ratio comparison.
			if diff := m.KVCacheUsageRatio - tt.wantRatio; diff > 0.001 || diff < -0.001 {
				t.Errorf("KVCacheUsageRatio = %f, want %f", m.KVCacheUsageRatio, tt.wantRatio)
			}
		})
	}
}

func TestLlamaServerSlotToUsage(t *testing.T) {
	tests := []struct {
		name        string
		slot        LlamaSlot
		ctxWindow   int
		wantPct     int
		wantCtxSize int
		wantIn      int
	}{
		{
			name:        "use slot n_ctx",
			slot:        LlamaSlot{NCtx: 73728, NPast: 36864},
			ctxWindow:   0,
			wantPct:     50, // 36864 * 100 / 73728 = 50
			wantCtxSize: 73728,
			wantIn:      36864,
		},
		{
			name:        "override context window",
			slot:        LlamaSlot{NCtx: 131072, NPast: 36864},
			ctxWindow:   73728,
			wantPct:     50,
			wantCtxSize: 73728,
			wantIn:      36864,
		},
		{
			name:        "empty slot",
			slot:        LlamaSlot{NCtx: 73728, NPast: 0},
			ctxWindow:   0,
			wantPct:     0,
			wantCtxSize: 73728,
			wantIn:      0,
		},
		{
			name:        "nearly full slot",
			slot:        LlamaSlot{NCtx: 73728, NPast: 72000},
			ctxWindow:   0,
			wantPct:     97, // 72000 * 100 / 73728 = 97
			wantCtxSize: 73728,
			wantIn:      72000,
		},
		{
			name:        "over capacity caps at 100",
			slot:        LlamaSlot{NCtx: 73728, NPast: 80000},
			ctxWindow:   0,
			wantPct:     100,
			wantCtxSize: 73728,
			wantIn:      80000,
		},
		{
			name:        "zero n_ctx uses default",
			slot:        LlamaSlot{NCtx: 0, NPast: 10000},
			ctxWindow:   0,
			wantPct:     22, // 10000 * 100 / 43904 = 22
			wantCtxSize: DefaultOpenCodeContextWindow,
			wantIn:      10000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := LlamaServerSlotToUsage(tt.slot, tt.ctxWindow)
			if u.UsedPercent != tt.wantPct {
				t.Errorf("UsedPercent = %d, want %d", u.UsedPercent, tt.wantPct)
			}
			if u.ContextWindowSize != tt.wantCtxSize {
				t.Errorf("ContextWindowSize = %d, want %d", u.ContextWindowSize, tt.wantCtxSize)
			}
			if u.TotalInputTokens != tt.wantIn {
				t.Errorf("TotalInputTokens = %d, want %d", u.TotalInputTokens, tt.wantIn)
			}
			if u.RemainingPercent != 100-tt.wantPct {
				t.Errorf("RemainingPercent = %d, want %d", u.RemainingPercent, 100-tt.wantPct)
			}
			if u.TotalCostUSD != 0 {
				t.Error("TotalCostUSD should be 0 for local model")
			}
			if u.LastUpdated.IsZero() {
				t.Error("LastUpdated should be set")
			}
		})
	}
}

func TestReadLlamaServerSlots_HTTP(t *testing.T) {
	slotsJSON := []LlamaSlot{
		{ID: 0, NCtx: 73728, NPast: 30000, State: LlamaSlotProcessing, IsActive: true, TaskID: 1},
		{ID: 1, NCtx: 73728, NPast: 0, State: LlamaSlotIdle, TaskID: -1},
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/slots" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(slotsJSON)
	}))
	defer ts.Close()

	slots, err := ReadLlamaServerSlots(ts.URL + "/slots")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slots) != 2 {
		t.Fatalf("len(slots) = %d, want 2", len(slots))
	}
	if slots[0].NPast != 30000 {
		t.Errorf("slot 0 NPast = %d, want 30000", slots[0].NPast)
	}
	if slots[1].State != LlamaSlotIdle {
		t.Errorf("slot 1 State = %d, want %d (idle)", slots[1].State, LlamaSlotIdle)
	}
}

func TestReadLlamaServerSlots_HTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	_, err := ReadLlamaServerSlots(ts.URL + "/slots")
	if err == nil {
		t.Fatal("expected error for 500 status")
	}
}

func TestReadLlamaServerSlotsUsage(t *testing.T) {
	tests := []struct {
		name      string
		slots     []LlamaSlot
		ctxWindow int
		wantNil   bool
		wantPct   int
		wantIn    int
	}{
		{
			name:    "empty slots",
			slots:   []LlamaSlot{},
			wantNil: true,
		},
		{
			name: "single active slot",
			slots: []LlamaSlot{
				{ID: 0, NCtx: 73728, NPast: 36864, State: LlamaSlotProcessing, IsActive: true},
			},
			ctxWindow: 0,
			wantPct:   50,
			wantIn:    36864,
		},
		{
			name: "picks active slot over idle with higher usage",
			slots: []LlamaSlot{
				{ID: 0, NCtx: 73728, NPast: 60000, State: LlamaSlotIdle},
				{ID: 1, NCtx: 73728, NPast: 30000, State: LlamaSlotProcessing, IsActive: true},
			},
			ctxWindow: 0,
			wantPct:   40, // 30000 * 100 / 73728 = 40
			wantIn:    30000,
		},
		{
			name: "picks most-utilized active slot",
			slots: []LlamaSlot{
				{ID: 0, NCtx: 73728, NPast: 20000, State: LlamaSlotProcessing, IsActive: true},
				{ID: 1, NCtx: 73728, NPast: 50000, State: LlamaSlotProcessing, IsActive: true},
			},
			ctxWindow: 0,
			wantPct:   67, // 50000 * 100 / 73728 = 67
			wantIn:    50000,
		},
		{
			name: "falls back to highest usage if no active slot",
			slots: []LlamaSlot{
				{ID: 0, NCtx: 73728, NPast: 10000, State: LlamaSlotIdle},
				{ID: 1, NCtx: 73728, NPast: 40000, State: LlamaSlotIdle},
			},
			ctxWindow: 0,
			wantPct:   54, // 40000 * 100 / 73728 = 54
			wantIn:    40000,
		},
		{
			name: "context window override",
			slots: []LlamaSlot{
				{ID: 0, NCtx: 131072, NPast: 36864, State: LlamaSlotProcessing, IsActive: true},
			},
			ctxWindow: 73728,
			wantPct:   50,
			wantIn:    36864,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slotsJSON, _ := json.Marshal(tt.slots)
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write(slotsJSON)
			}))
			defer ts.Close()

			usage, err := ReadLlamaServerSlotsUsage(ts.URL+"/slots", tt.ctxWindow)
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
			if usage.UsedPercent != tt.wantPct {
				t.Errorf("UsedPercent = %d, want %d", usage.UsedPercent, tt.wantPct)
			}
			if usage.TotalInputTokens != tt.wantIn {
				t.Errorf("TotalInputTokens = %d, want %d", usage.TotalInputTokens, tt.wantIn)
			}
			if usage.TotalCostUSD != 0 {
				t.Error("TotalCostUSD should be 0 for local model")
			}
		})
	}
}

func TestReadLlamaServerSlotsUsage_ConnectionError(t *testing.T) {
	_, err := ReadLlamaServerSlotsUsage("http://127.0.0.1:1/slots", 0)
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}
