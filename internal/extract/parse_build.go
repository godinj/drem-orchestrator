package extract

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// goBuildErrRE matches `go build` compiler errors of the form
//
//	./foo.go:12:3: undefined: bar
//	path/to/foo.go:12:3: undefined: bar
//
// Column information is optional because older or alternative compilers
// sometimes omit it.
var goBuildErrRE = regexp.MustCompile(
	`^([^\s:]+\.go):(\d+)(?::\d+)?:\s+(.+)$`,
)

// cppBuildErrRE matches GCC/Clang error lines of the form
//
//	main.cpp:15:8: error: 'foo' was not declared in this scope
//	src/lib.c:42: error: expected ';' before '}' token
//
// Column is optional. File extensions recognised: .c, .cc, .cpp, .cxx,
// .h, .hpp, .hxx, .hh.
var cppBuildErrRE = regexp.MustCompile(
	`^([^\s:]+\.(?:c|cc|cpp|cxx|h|hpp|hxx|hh)):(\d+)(?::\d+)?:\s+(?:fatal\s+)?error:\s+(.+)$`,
)

// cargoBuildErrRE matches `cargo build` / rustc short-form errors of the
// form
//
//	error[E0425]: cannot find value `foo` in this scope
//
// Cargo also emits "error: " lines without an error code; those are
// matched by cargoBuildErrPlainRE. A file/line pair for the underlying
// site comes on a subsequent " --> " line which is parsed separately.
var cargoBuildErrRE = regexp.MustCompile(
	`^error(?:\[[A-Z]\d+\])?:\s+(.+)$`,
)

// matchBuildError recognises compiler errors emitted by the toolchains
// we are likely to see inside worker containers: Go, C/C++, and Rust/
// Cargo. The first matcher to fire wins; the order prefers languages
// with the most specific regex shapes so that a C++ error is never
// misclassified as a cargo error or vice versa.
func matchBuildError(src string, line []byte, now time.Time) (Event, bool) {
	s := strings.TrimSpace(string(line))

	// Go first because its regex is the most constrained (requires .go).
	if m := goBuildErrRE.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[2])
		return &BuildError{
			Timestamp: now,
			Source:    src,
			Tool:      "go",
			File:      m[1],
			Line:      n,
			Message:   strings.TrimSpace(m[3]),
		}, true
	}

	// C/C++ next; its extension list is distinct from Go's.
	if m := cppBuildErrRE.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[2])
		return &BuildError{
			Timestamp: now,
			Source:    src,
			Tool:      "cpp",
			File:      m[1],
			Line:      n,
			Message:   strings.TrimSpace(m[3]),
		}, true
	}

	// Cargo/rustc: "error[Exxxx]: ..." or "error: ...".
	if m := cargoBuildErrRE.FindStringSubmatch(s); m != nil {
		return &BuildError{
			Timestamp: now,
			Source:    src,
			Tool:      "cargo",
			Message:   strings.TrimSpace(m[1]),
		}, true
	}

	return nil, false
}
