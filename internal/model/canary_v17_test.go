package model

import "testing"

func TestCanaryV17Marker_Fields(t *testing.T) {
	tests := []struct {
		name      string
		marker    CanaryV17Marker
		wantLabel string
		wantAt    string
	}{
		{
			name:      "zero value has empty fields",
			marker:    CanaryV17Marker{},
			wantLabel: "",
			wantAt:    "",
		},
		{
			name:      "populated value retains fields",
			marker:    CanaryV17Marker{Label: "v17-canary", At: "2026-04-21T00:00:00Z"},
			wantLabel: "v17-canary",
			wantAt:    "2026-04-21T00:00:00Z",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.marker.Label != tc.wantLabel {
				t.Errorf("Label = %q, want %q", tc.marker.Label, tc.wantLabel)
			}
			if tc.marker.At != tc.wantAt {
				t.Errorf("At = %q, want %q", tc.marker.At, tc.wantAt)
			}
		})
	}
}
