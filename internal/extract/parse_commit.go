package extract

import (
	"regexp"
	"strings"
	"time"
)

// gitCommitSummaryRE matches git's own commit-summary line of the form
//
//	[master abc1234] Add a thing
//	[feature/foo 1234567] Fix the other thing
//
// The branch may contain slashes, dots, hyphens, and underscores. The sha
// is 7+ lowercase hex characters (git's default short-sha length).
var gitCommitSummaryRE = regexp.MustCompile(
	`\[([A-Za-z0-9_./\-]+)\s+([0-9a-f]{7,40})\]\s*(.*)$`,
)

// gitCommitCmdRE matches an echoed `git commit` command with an optional
// message. Used to detect commit *intent* in Bash echoes where git's own
// stdout has not yet been captured. The returned SHA/branch are empty in
// this case because the command echo precedes the commit itself.
var gitCommitCmdRE = regexp.MustCompile(
	`(?:^|\s)git\s+commit\b(?:.*-m\s*['"]([^'"]*)['"])?`,
)

// matchCommit recognises both git's own commit summary (which carries the
// sha and branch) and an echoed `git commit` command (which carries the
// message). The summary case is strictly more informative so it is tried
// first; the command-echo case is a fallback that lets a bash-heavy
// transcript still produce a Commit event.
func matchCommit(src string, line []byte, now time.Time) (Event, bool) {
	s := string(line)

	if m := gitCommitSummaryRE.FindStringSubmatch(s); m != nil {
		return &Commit{
			Timestamp: now,
			Source:    src,
			Branch:    m[1],
			SHA:       m[2],
			Message:   strings.TrimSpace(m[3]),
		}, true
	}

	if m := gitCommitCmdRE.FindStringSubmatch(s); m != nil {
		// Avoid confusing `git commit-tree` or other `commit-` subcommands
		// with `git commit`. FindStringSubmatch already requires the word
		// boundary via \b so this guard is belt-and-braces only.
		return &Commit{
			Timestamp: now,
			Source:    src,
			Message:   m[1],
		}, true
	}

	return nil, false
}
