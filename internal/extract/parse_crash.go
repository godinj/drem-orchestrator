package extract

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// exitStatusRE matches "exit status N" (N != 0) as emitted by `go test`
// and many shells. The whole line need not match; the phrase is searched
// for as a substring.
var exitStatusRE = regexp.MustCompile(`exit status (\d+)`)

// matchCrash recognises the common crash indicators on a log stream:
//
//   - "panic:" (Go runtime panic)
//   - "runtime error:" (Go runtime fault that may precede a panic)
//   - "signal SIGSEGV" / "signal SIGABRT" / "signal SIGKILL" (native faults)
//   - "exit status N" with N != 0 (process exit summary lines)
//
// The returned Crash carries the reason phrase and, for "exit status N"
// lines, the parsed exit code. For panic and signal lines the exit code is
// left at zero because that information is not present in the line.
func matchCrash(src string, line []byte, now time.Time) (Event, bool) {
	s := string(line)

	// Panic lines: "panic: <msg>" or "panic(<arg>)" style.
	if strings.Contains(s, "panic:") {
		reason := strings.TrimSpace(s)
		if idx := strings.Index(reason, "panic:"); idx >= 0 {
			reason = strings.TrimSpace(reason[idx:])
		}
		return &Crash{Timestamp: now, Source: src, Reason: reason}, true
	}

	// Runtime error (often precedes a panic stack trace).
	if strings.Contains(s, "runtime error:") {
		reason := strings.TrimSpace(s)
		if idx := strings.Index(reason, "runtime error:"); idx >= 0 {
			reason = strings.TrimSpace(reason[idx:])
		}
		return &Crash{Timestamp: now, Source: src, Reason: reason}, true
	}

	// Signal-terminated processes.
	if strings.Contains(s, "signal SIG") {
		reason := strings.TrimSpace(s)
		if idx := strings.Index(reason, "signal SIG"); idx >= 0 {
			reason = strings.TrimSpace(reason[idx:])
		}
		return &Crash{Timestamp: now, Source: src, Reason: reason}, true
	}

	// Non-zero exit status lines.
	if m := exitStatusRE.FindStringSubmatch(s); m != nil {
		code, err := strconv.Atoi(m[1])
		if err == nil && code != 0 {
			return &Crash{
				Timestamp: now,
				Source:    src,
				Reason:    "exit status " + m[1],
				ExitCode:  code,
			}, true
		}
	}

	return nil, false
}
