package orchestrator

import (
	"testing"
)

func TestHasExistingWork(t *testing.T) {
	tests := []struct {
		name           string
		estimatedFiles []string
		changedFiles   []string
		commitMessages []string
		subtaskTitle   string
		want           bool
	}{
		{
			name:           "work exists — full file overlap + matching commit message",
			estimatedFiles: []string{"internal/orchestrator/dedup.go"},
			changedFiles:   []string{"internal/orchestrator/dedup.go", "other.go"},
			commitMessages: []string{"Implement hasExistingWork dedup helper"},
			subtaskTitle:   "Implement hasExistingWork dedup helper",
			want:           true,
		},
		{
			name:           "work exists — partial file match + keyword match in commit",
			estimatedFiles: []string{"internal/foo/bar.go", "internal/foo/baz.go"},
			changedFiles:   []string{"internal/foo/bar.go"},
			commitMessages: []string{"Add bar implementation for foo feature"},
			subtaskTitle:   "Implement foo bar and baz",
			want:           true,
		},
		{
			name:           "work not exists — no file overlap",
			estimatedFiles: []string{"internal/new/file.go"},
			changedFiles:   []string{"internal/other/file.go"},
			commitMessages: []string{"unrelated commit"},
			subtaskTitle:   "Implement new file",
			want:           false,
		},
		{
			name:           "work not exists — file overlap but no commit match",
			estimatedFiles: []string{"internal/shared/config.go"},
			changedFiles:   []string{"internal/shared/config.go"},
			commitMessages: []string{"completely unrelated change"},
			subtaskTitle:   "Add validation logic",
			want:           false,
		},
		{
			name:           "empty estimated files — nil",
			estimatedFiles: nil,
			changedFiles:   []string{"anything.go"},
			commitMessages: []string{"anything"},
			subtaskTitle:   "Some task",
			want:           false,
		},
		{
			name:           "empty estimated files — empty slice",
			estimatedFiles: []string{},
			changedFiles:   []string{"anything.go"},
			commitMessages: []string{"anything"},
			subtaskTitle:   "Some task",
			want:           false,
		},
		{
			name:           "empty changed files",
			estimatedFiles: []string{"file.go"},
			changedFiles:   []string{},
			commitMessages: []string{},
			subtaskTitle:   "Some task",
			want:           false,
		},
		{
			name:           "case-insensitive keyword matching",
			estimatedFiles: []string{"internal/dedup.go"},
			changedFiles:   []string{"internal/dedup.go"},
			commitMessages: []string{"implement DEDUP helper"},
			subtaskTitle:   "Implement dedup helper",
			want:           true,
		},
		{
			name:           "multi-word keyword overlap — insufficient matches",
			estimatedFiles: []string{"internal/orchestrator/foo.go"},
			changedFiles:   []string{"internal/orchestrator/foo.go"},
			commitMessages: []string{"refactor the main loop"},
			subtaskTitle:   "Implement orchestrator scheduling loop",
			want:           false, // only "loop" matches — need at least 2 keywords
		},
		{
			name:           "multi-word keyword overlap — sufficient matches",
			estimatedFiles: []string{"internal/orchestrator/foo.go"},
			changedFiles:   []string{"internal/orchestrator/foo.go"},
			commitMessages: []string{"refactor orchestrator scheduling"},
			subtaskTitle:   "Implement orchestrator scheduling loop",
			want:           true, // "orchestrator" + "scheduling" = 2 keywords
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasExistingWork(tt.estimatedFiles, tt.changedFiles, tt.commitMessages, tt.subtaskTitle)
			if got != tt.want {
				t.Errorf("hasExistingWork() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractKeywords(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  []string
	}{
		{
			name:  "basic extraction",
			title: "Implement dedup helper",
			want:  []string{"implement", "dedup", "helper"},
		},
		{
			name:  "filters stop words",
			title: "Add the validation for input and output",
			want:  []string{"add", "validation", "input", "output"},
		},
		{
			name:  "filters short words",
			title: "A go fix to db",
			want:  []string{"fix"},
		},
		{
			name:  "empty string",
			title: "",
			want:  []string{},
		},
		{
			name:  "all stop words",
			title: "the for and a to in of with",
			want:  []string{},
		},
		{
			name:  "case normalization",
			title: "IMPLEMENT Dedup HELPER",
			want:  []string{"implement", "dedup", "helper"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractKeywords(tt.title)
			if len(got) != len(tt.want) {
				t.Fatalf("extractKeywords(%q) returned %d keywords %v, want %d keywords %v",
					tt.title, len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("extractKeywords(%q)[%d] = %q, want %q", tt.title, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestKeywordOverlap(t *testing.T) {
	tests := []struct {
		name      string
		keywords1 []string
		keywords2 []string
		want      int
	}{
		{
			name:      "full overlap",
			keywords1: []string{"implement", "dedup", "helper"},
			keywords2: []string{"implement", "dedup", "helper"},
			want:      3,
		},
		{
			name:      "partial overlap",
			keywords1: []string{"implement", "dedup", "helper"},
			keywords2: []string{"implement", "foo", "helper"},
			want:      2,
		},
		{
			name:      "no overlap",
			keywords1: []string{"implement", "dedup"},
			keywords2: []string{"refactor", "config"},
			want:      0,
		},
		{
			name:      "empty first slice",
			keywords1: []string{},
			keywords2: []string{"implement", "dedup"},
			want:      0,
		},
		{
			name:      "empty second slice",
			keywords1: []string{"implement", "dedup"},
			keywords2: []string{},
			want:      0,
		},
		{
			name:      "both nil",
			keywords1: nil,
			keywords2: nil,
			want:      0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := keywordOverlap(tt.keywords1, tt.keywords2)
			if got != tt.want {
				t.Errorf("keywordOverlap(%v, %v) = %d, want %d",
					tt.keywords1, tt.keywords2, got, tt.want)
			}
		})
	}
}
