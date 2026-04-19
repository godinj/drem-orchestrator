package extract

import (
	"regexp"
	"strings"
	"time"
)

// gitPushToRE matches git's "To <remote>" header that precedes the
// per-ref update lines in `git push` output. Remote may be a URL, a
// filesystem path, or a named remote.
var gitPushToRE = regexp.MustCompile(`^To\s+(\S.+)$`)

// gitPushRefRE matches the per-ref status line emitted after "To ...",
// for example:
//
//	  abc1234..def5678  feature/foo -> feature/foo
//	* [new branch]      topic       -> topic
//
// The branch name captured is the local ref (left of the arrow).
var gitPushRefRE = regexp.MustCompile(
	`^\s*(?:\*\s*\[[^\]]+\]|[0-9a-f]+\.\.[0-9a-f]+|\+\s*[0-9a-f]+\.\.\.[0-9a-f]+)\s+(\S+)\s*->`,
)

// gitPushCmdRE matches an echoed `git push` command such as
// "git push origin feature/foo" or "git push --set-upstream origin topic".
var gitPushCmdRE = regexp.MustCompile(
	`(?:^|\s)git\s+push\b(?:\s+(?:--?\S+\s+)*(\S+)(?:\s+(\S+))?)?`,
)

// matchPush recognises push activity from three sources:
//
//   - `git push ...` command echoes (most common in bash transcripts)
//   - The "To <remote>" header that opens git's push output
//   - Per-ref status lines that carry the branch name being pushed
//
// A caller that sees `To github.com:foo/bar` followed by a per-ref line
// will receive two Push events; downstream code is expected to dedupe by
// timestamp+branch if that matters.
func matchPush(src string, line []byte, now time.Time) (Event, bool) {
	s := string(line)

	if m := gitPushCmdRE.FindStringSubmatch(s); m != nil {
		// Only trust a command echo when the line actually contains the
		// token "git push"; gitPushCmdRE uses an optional tail so it can
		// otherwise match spuriously.
		if !strings.Contains(s, "git push") {
			return nil, false
		}
		remote := strings.TrimSpace(m[1])
		branch := strings.TrimSpace(m[2])
		// Guard against flags being captured as the remote/branch.
		if strings.HasPrefix(remote, "-") {
			remote = ""
		}
		if strings.HasPrefix(branch, "-") {
			branch = ""
		}
		return &Push{
			Timestamp: now,
			Source:    src,
			Remote:    remote,
			Branch:    branch,
		}, true
	}

	if m := gitPushToRE.FindStringSubmatch(s); m != nil {
		return &Push{
			Timestamp: now,
			Source:    src,
			Remote:    strings.TrimSpace(m[1]),
		}, true
	}

	if m := gitPushRefRE.FindStringSubmatch(s); m != nil {
		return &Push{
			Timestamp: now,
			Source:    src,
			Branch:    strings.TrimSpace(m[1]),
		}, true
	}

	return nil, false
}
