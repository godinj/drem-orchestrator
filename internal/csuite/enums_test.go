package csuite

import "testing"

func TestAgentMonStatusString(t *testing.T) {
	tests := []struct {
		status AgentMonStatus
		want   string
	}{
		{AgentMonOnline, "online"},
		{AgentMonOffline, "offline"},
		{AgentMonStale, "stale"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := tc.status.String()
			if got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseAgentMonStatus(t *testing.T) {
	tests := []struct {
		input string
		want  AgentMonStatus
	}{
		{"online", AgentMonOnline},
		{"offline", AgentMonOffline},
		{"stale", AgentMonStale},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseAgentMonStatus(tc.input)
			if err != nil {
				t.Fatalf("ParseAgentMonStatus(%q) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseAgentMonStatus(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseAgentMonStatus_Error(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"invalid value", "running"},
		{"empty string", ""},
		{"case sensitive", "Online"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAgentMonStatus(tc.input)
			if err == nil {
				t.Errorf("ParseAgentMonStatus(%q) expected error, got nil", tc.input)
			}
		})
	}
}

func TestInboxPriorityString(t *testing.T) {
	tests := []struct {
		priority InboxPriority
		want     string
	}{
		{PriorityLow, "low"},
		{PriorityNormal, "normal"},
		{PriorityHigh, "high"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := tc.priority.String()
			if got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseInboxPriority(t *testing.T) {
	tests := []struct {
		input string
		want  InboxPriority
	}{
		{"low", PriorityLow},
		{"normal", PriorityNormal},
		{"high", PriorityHigh},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseInboxPriority(tc.input)
			if err != nil {
				t.Fatalf("ParseInboxPriority(%q) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseInboxPriority(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseInboxPriority_Error(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"invalid value", "urgent"},
		{"empty string", ""},
		{"case sensitive", "High"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseInboxPriority(tc.input)
			if err == nil {
				t.Errorf("ParseInboxPriority(%q) expected error, got nil", tc.input)
			}
		})
	}
}

func TestInboxMessageTypeString(t *testing.T) {
	tests := []struct {
		msgType InboxMessageType
		want    string
	}{
		{MessageTypeStatus, "status"},
		{MessageTypeRequest, "request"},
		{MessageTypeAlert, "alert"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := tc.msgType.String()
			if got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseInboxMessageType(t *testing.T) {
	tests := []struct {
		input string
		want  InboxMessageType
	}{
		{"status", MessageTypeStatus},
		{"request", MessageTypeRequest},
		{"alert", MessageTypeAlert},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseInboxMessageType(tc.input)
			if err != nil {
				t.Fatalf("ParseInboxMessageType(%q) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseInboxMessageType(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseInboxMessageType_Error(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"invalid value", "notification"},
		{"empty string", ""},
		{"case sensitive", "Alert"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseInboxMessageType(tc.input)
			if err == nil {
				t.Errorf("ParseInboxMessageType(%q) expected error, got nil", tc.input)
			}
		})
	}
}
