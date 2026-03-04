package supervisor

import "testing"

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain object",
			input: `{"key": "value"}`,
			want:  `{"key": "value"}`,
		},
		{
			name:  "object with surrounding text",
			input: `Here is the result: {"key": "value"} that's it`,
			want:  `{"key": "value"}`,
		},
		{
			name:  "markdown code fence",
			input: "```json\n{\"key\": \"value\"}\n```",
			want:  `{"key": "value"}`,
		},
		{
			name:  "nested objects",
			input: `{"outer": {"inner": "value"}, "arr": [1, 2]}`,
			want:  `{"outer": {"inner": "value"}, "arr": [1, 2]}`,
		},
		{
			name:  "array",
			input: `Some text [{"a": 1}, {"b": 2}] more text`,
			want:  `[{"a": 1}, {"b": 2}]`,
		},
		{
			name:  "escaped quotes in strings",
			input: `{"msg": "he said \"hello\""}`,
			want:  `{"msg": "he said \"hello\""}`,
		},
		{
			name:  "braces inside strings",
			input: `{"code": "func() { return }"}`,
			want:  `{"code": "func() { return }"}`,
		},
		{
			name:  "no json",
			input: "just plain text",
			want:  "",
		},
		{
			name:  "empty input",
			input: "",
			want:  "",
		},
		{
			name:  "unclosed object",
			input: `{"key": "value"`,
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSON(tt.input)
			if got != tt.want {
				t.Errorf("extractJSON(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTruncateForPrompt(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "short string",
			input:  "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "exact length",
			input:  "hello",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "truncated",
			input:  "hello world",
			maxLen: 5,
			want:   "hello...",
		},
		{
			name:   "empty",
			input:  "",
			maxLen: 5,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateForPrompt(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateForPrompt(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}
