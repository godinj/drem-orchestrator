package merge

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// ConflictClass categorises a merge conflict by severity.
type ConflictClass int

const (
	// Trivial conflicts can be auto-resolved (docs, README, CHANGELOG, etc.).
	Trivial ConflictClass = iota
	// NonTrivial conflicts require human intervention (source code, config).
	NonTrivial
)

// String returns the human-readable label for a ConflictClass.
func (c ConflictClass) String() string {
	switch c {
	case Trivial:
		return "trivial"
	case NonTrivial:
		return "non-trivial"
	default:
		return "unknown"
	}
}

// ClassifiedConflict pairs a conflicting file path with its classification.
type ClassifiedConflict struct {
	Path        string
	Class       ConflictClass
	Description string
}

// trivialNames are filenames (case-sensitive) that are always trivial
// regardless of extension.
var trivialNames = map[string]bool{
	"LICENSE":    true,
	".gitignore": true,
}

// trivialExtensions are file extensions whose conflicts are safe for
// automatic resolution.
var trivialExtensions = map[string]bool{
	".md":  true,
	".txt": true,
}

// isTrivial determines whether a path represents a trivial conflict based on
// filename, extension, and directory patterns.
func isTrivial(path string) (bool, string) {
	base := filepath.Base(path)

	if trivialNames[base] {
		return true, base + " — safe for auto-resolution"
	}

	ext := filepath.Ext(base)
	if trivialExtensions[ext] {
		return true, "documentation file — safe for auto-resolution"
	}

	return false, "source/config file — requires manual resolution"
}

// ClassifyConflicts categorises each conflicting file path as Trivial or
// NonTrivial based on file extension and path patterns.
func ClassifyConflicts(paths []string) []ClassifiedConflict {
	result := make([]ClassifiedConflict, 0, len(paths))
	for _, p := range paths {
		trivial, desc := isTrivial(p)
		cls := NonTrivial
		if trivial {
			cls = Trivial
		}
		result = append(result, ClassifiedConflict{Path: p, Class: cls, Description: desc})
	}
	return result
}

// AutoResolveResult describes the outcome of an automatic conflict resolution
// attempt.
type AutoResolveResult struct {
	Resolved bool
	Message  string
}

// AutoResolveConflicts attempts to resolve all conflicts automatically.
// It only proceeds when every conflict is Trivial; if any NonTrivial conflict
// is present the worktree is left untouched.
func AutoResolveConflicts(worktreePath string, classified []ClassifiedConflict) AutoResolveResult {
	if len(classified) == 0 {
		return AutoResolveResult{Resolved: true, Message: "no conflicts to resolve"}
	}

	for _, cc := range classified {
		if cc.Class == NonTrivial {
			return AutoResolveResult{
				Resolved: false,
				Message:  fmt.Sprintf("non-trivial conflict in %s — skipping auto-resolution", cc.Path),
			}
		}
	}

	for _, cc := range classified {
		if err := gitCmd(worktreePath, "checkout", "--theirs", cc.Path); err != nil {
			return AutoResolveResult{Resolved: false, Message: fmt.Sprintf("git checkout --theirs %s: %v", cc.Path, err)}
		}
		if err := gitCmd(worktreePath, "add", cc.Path); err != nil {
			return AutoResolveResult{Resolved: false, Message: fmt.Sprintf("git add %s: %v", cc.Path, err)}
		}
	}

	if err := gitCmd(worktreePath, "commit", "--no-edit"); err != nil {
		return AutoResolveResult{Resolved: false, Message: fmt.Sprintf("git commit: %v", err)}
	}

	return AutoResolveResult{Resolved: true, Message: "all trivial conflicts auto-resolved"}
}

// gitCmd runs a git command in the given directory.
func gitCmd(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

// FormatClassifiedConflicts produces a structured, human-readable summary of
// classified conflicts suitable for inclusion in merge failure events.
func FormatClassifiedConflicts(classified []ClassifiedConflict) string {
	if len(classified) == 0 {
		return ""
	}
	var b strings.Builder
	for i, cc := range classified {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s [%s]: %s", cc.Path, cc.Class, cc.Description)
	}
	return b.String()
}
