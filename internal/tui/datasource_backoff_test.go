package tui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestNextDataBackoff_Ladder verifies the retry ladder called out in the
// containerization prompt (1s → 2s → 5s → 10s cap). The helper is pure
// so this covers the behaviour without exercising tea.Cmd plumbing.
func TestNextDataBackoff_Ladder(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero starts at 1s", 0, 1 * time.Second},
		{"negative treated as zero", -1 * time.Second, 1 * time.Second},
		{"1s doubles to 2s", 1 * time.Second, 2 * time.Second},
		{"2s climbs to 5s", 2 * time.Second, 5 * time.Second},
		{"5s caps at 10s", 5 * time.Second, 10 * time.Second},
		{"10s stays at 10s", 10 * time.Second, 10 * time.Second},
		{"20s clamps to 10s", 20 * time.Second, 10 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, nextDataBackoff(tc.in))
		})
	}
}
