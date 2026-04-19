package extract

import (
	"regexp"
	"strings"
	"time"
)

// goTestOKRE matches `go test` per-package success lines:
//
//	ok  	github.com/foo/bar	0.123s
//	ok  	github.com/foo/bar	(cached)
var goTestOKRE = regexp.MustCompile(`^ok\s+(\S+)\s+(.+)$`)

// goTestFAILRE matches `go test` per-package failure lines:
//
//	FAIL	github.com/foo/bar	0.123s
//	FAIL	github.com/foo/bar [build failed]
var goTestFAILRE = regexp.MustCompile(`^FAIL\s+(\S+)(?:\s+(.+))?$`)

// goTestSubtestFailRE matches per-test failure lines emitted when running
// `go test -v`:
//
//	--- FAIL: TestSomething (0.01s)
var goTestSubtestFailRE = regexp.MustCompile(`^---\s+FAIL:\s+(\S+)`)

// goTestSubtestPassRE matches per-test success lines emitted with -v:
//
//	--- PASS: TestSomething (0.01s)
var goTestSubtestPassRE = regexp.MustCompile(`^---\s+PASS:\s+(\S+)`)

// matchTestResult recognises `go test` output lines and the small number
// of adjacent conventions (--- PASS, --- FAIL, final PASS/FAIL token).
// It does not attempt to recognise every test harness on earth; callers
// that use a more exotic harness can add matchers alongside this one.
func matchTestResult(src string, line []byte, now time.Time) (Event, bool) {
	s := strings.TrimSpace(string(line))

	if m := goTestSubtestFailRE.FindStringSubmatch(s); m != nil {
		return &TestResult{
			Timestamp: now,
			Source:    src,
			Passed:    false,
			Summary:   m[1],
		}, true
	}
	if m := goTestSubtestPassRE.FindStringSubmatch(s); m != nil {
		return &TestResult{
			Timestamp: now,
			Source:    src,
			Passed:    true,
			Summary:   m[1],
		}, true
	}
	if m := goTestOKRE.FindStringSubmatch(s); m != nil {
		return &TestResult{
			Timestamp: now,
			Source:    src,
			Passed:    true,
			Summary:   strings.TrimSpace(m[1] + " " + m[2]),
		}, true
	}
	if m := goTestFAILRE.FindStringSubmatch(s); m != nil {
		summary := m[1]
		if len(m) > 2 && m[2] != "" {
			summary = summary + " " + m[2]
		}
		return &TestResult{
			Timestamp: now,
			Source:    src,
			Passed:    false,
			Summary:   strings.TrimSpace(summary),
		}, true
	}

	// Bare PASS/FAIL summary lines.
	switch s {
	case "PASS":
		return &TestResult{Timestamp: now, Source: src, Passed: true, Summary: "PASS"}, true
	case "FAIL":
		return &TestResult{Timestamp: now, Source: src, Passed: false, Summary: "FAIL"}, true
	}

	return nil, false
}
