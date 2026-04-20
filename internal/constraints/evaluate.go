package constraints

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/constraints/depth"
)

type BaselineStatus string

const (
	BaselineOK      BaselineStatus = "ok"
	BaselineFailed  BaselineStatus = "failed"
	BaselineMissing BaselineStatus = "missing"
)

type DeltaResult struct {
	Skipped        bool
	SkipReason     string
	BaselineStatus BaselineStatus
	Comparison     ComparisonResult
	FeatureReport  *Report
	BaselineReport *Report
}

func EvaluateDelta(cfg *Config, featureDir, baselineDir string) (*DeltaResult, error) {
	featureReport, err := Evaluate(cfg, featureDir)
	if err != nil {
		return nil, fmt.Errorf("evaluate feature constraints: %w", err)
	}

	baselineCfg, err := LoadConfig(baselineDir)
	if err != nil {
		return &DeltaResult{
			Skipped:        true,
			SkipReason:     fmt.Sprintf("baseline config load failed: %v", err),
			BaselineStatus: BaselineFailed,
			FeatureReport:  featureReport,
		}, nil
	}
	if baselineCfg == nil {
		return &DeltaResult{
			Skipped:        true,
			SkipReason:     "no constraints config in baseline",
			BaselineStatus: BaselineMissing,
			FeatureReport:  featureReport,
		}, nil
	}

	baselineReport, err := Evaluate(baselineCfg, baselineDir)
	if err != nil {
		return &DeltaResult{
			Skipped:        true,
			SkipReason:     fmt.Sprintf("baseline evaluation failed: %v", err),
			BaselineStatus: BaselineFailed,
			FeatureReport:  featureReport,
		}, nil
	}

	comparison := CompareReports(baselineReport, featureReport)
	return &DeltaResult{
		BaselineStatus: BaselineOK,
		Comparison:     comparison,
		FeatureReport:  featureReport,
		BaselineReport: baselineReport,
	}, nil
}

// Evaluate runs all constraints in the config against the given worktree root.
func Evaluate(cfg *Config, worktreeRoot string) (*Report, error) {
	var results []Result

	for _, c := range cfg.Commands {
		r, err := evalCommand(c, worktreeRoot)
		if err != nil {
			return nil, fmt.Errorf("evaluating command %q: %w", c.Name, err)
		}
		results = append(results, r)
	}

	fileResults, err := evalFileConstraints(cfg, worktreeRoot, nil)
	if err != nil {
		return nil, err
	}
	results = append(results, fileResults...)

	return buildReport(results), nil
}

// EvaluateFiles checks only file-based constraints (max_lines, max_matches,
// no_match) against a specific set of file paths. Used by the plan validator
// to check estimated_files without running command constraints. Files are
// relative to worktreeRoot.
func EvaluateFiles(cfg *Config, worktreeRoot string, files []string) (*Report, error) {
	fileSet := make(map[string]bool, len(files))
	for _, f := range files {
		fileSet[f] = true
	}

	results, err := evalFileConstraints(cfg, worktreeRoot, fileSet)
	if err != nil {
		return nil, err
	}

	return buildReport(results), nil
}

func buildReport(results []Result) *Report {
	report := &Report{Results: results}
	for _, r := range results {
		switch {
		case r.Skipped:
			report.Skipped++
		case r.Passed:
			report.Passed++
		default:
			report.Failed++
		}
	}
	return report
}

// commandTool returns the probable command binary name from a shell
// invocation string — the first bare token after any leading `KEY=value`
// environment assignments. Returns "" when the run string is empty or
// when no bare token can be identified. Callers use the result with
// exec.LookPath to detect missing toolchains and surface SKIP rather
// than falling through to `bash: command not found`.
//
// Examples:
//
//	"go vet ./..."                         → "go"
//	"gofmt -l ./internal/ ./cmd/"          → "gofmt"
//	"GOFLAGS=-mod=vendor go vet ./..."     → "go"
//	"bash -c 'echo hi'"                    → "bash"
//	""                                     → ""
func commandTool(run string) string {
	for _, tok := range strings.Fields(run) {
		if strings.Contains(tok, "=") {
			// env assignment prefix (FOO=bar, KEY=value)
			continue
		}
		return tok
	}
	return ""
}

func evalCommand(c commandConstraint, worktreeRoot string) (Result, error) {
	result := Result{
		Name: c.Name,
		Type: "command",
	}

	// When the constraint shells out to a tool that isn't on PATH
	// (e.g. `go vet` inside the orch container, which intentionally
	// ships without the Go toolchain), report SKIP instead of FAIL.
	// Otherwise bash surfaces a literal "command not found" error
	// that looks indistinguishable from a real constraint violation.
	if tool := commandTool(c.Run); tool != "" {
		if _, err := exec.LookPath(tool); err != nil {
			result.Skipped = true
			result.SkipReason = fmt.Sprintf("tool unavailable: %s", tool)
			return result, nil
		}
	}

	cmd := exec.Command("bash", "-c", c.Run)
	cmd.Dir = worktreeRoot

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	switch c.Expect {
	case "empty_output":
		output := strings.TrimSpace(stdout.String())
		if output == "" {
			result.Passed = true
		} else {
			result.Passed = false
			result.Messages = []string{output}
		}
	default: // "exit_zero"
		if err == nil {
			result.Passed = true
		} else {
			result.Passed = false
			combined := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
			if combined == "" {
				combined = err.Error()
			}
			result.Messages = []string{combined}
		}
	}

	return result, nil
}

func evalFileConstraints(cfg *Config, worktreeRoot string, fileSet map[string]bool) ([]Result, error) {
	var results []Result

	for _, c := range cfg.MaxLines {
		r, err := evalMaxLines(c, worktreeRoot, fileSet)
		if err != nil {
			return nil, fmt.Errorf("evaluating max_lines %q: %w", c.Name, err)
		}
		results = append(results, r)
	}

	for _, c := range cfg.MaxMatches {
		r, err := evalMaxMatches(c, worktreeRoot, fileSet)
		if err != nil {
			return nil, fmt.Errorf("evaluating max_matches %q: %w", c.Name, err)
		}
		results = append(results, r)
	}

	for _, c := range cfg.NoMatch {
		r, err := evalNoMatch(c, worktreeRoot, fileSet)
		if err != nil {
			return nil, fmt.Errorf("evaluating no_match %q: %w", c.Name, err)
		}
		results = append(results, r)
	}

	for _, c := range cfg.Depth {
		r, err := evalDepth(c, worktreeRoot, fileSet)
		if err != nil {
			return nil, fmt.Errorf("evaluating depth %q: %w", c.Name, err)
		}
		results = append(results, r)
	}

	return results, nil
}

// globFiles finds files matching a glob pattern with ** support relative to root.
// It returns paths relative to root.
func globFiles(root, pattern string, exclude []string) ([]string, error) {
	var matches []string

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip hidden directories.
			if d.Name() != "." && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		if !matchDoublestarGlob(relPath, pattern) {
			return nil
		}

		for _, excl := range exclude {
			if matchExcludePattern(relPath, excl) {
				return nil
			}
		}

		matches = append(matches, relPath)
		return nil
	})

	return matches, err
}

// matchDoublestarGlob matches a relative path against a glob pattern that may
// contain **. The ** matches zero or more directory components.
func matchDoublestarGlob(path, pattern string) bool {
	// Split pattern on "**" segments.
	parts := strings.Split(pattern, "**")

	if len(parts) == 1 {
		// No **, use standard matching.
		ok, _ := filepath.Match(pattern, path)
		return ok
	}

	// For patterns like "internal/**/*.go":
	// parts[0] = "internal/"
	// parts[1] = "/*.go"
	return matchDoublestarParts(path, parts, 0)
}

func matchDoublestarParts(path string, parts []string, idx int) bool {
	if idx == len(parts) {
		return path == ""
	}

	part := parts[idx]

	if idx == 0 && part != "" {
		// First part must match as prefix.
		prefix := strings.TrimSuffix(part, string(filepath.Separator))
		if !strings.HasPrefix(path, prefix) {
			return false
		}
		if len(path) == len(prefix) {
			return matchDoublestarParts("", parts, idx+1)
		}
		if path[len(prefix)] != filepath.Separator {
			return false
		}
		return matchDoublestarParts(path[len(prefix)+1:], parts, idx+1)
	}

	if idx == len(parts)-1 {
		// Last part after **: match against the filename or trailing path.
		suffix := strings.TrimPrefix(part, string(filepath.Separator))
		if suffix == "" {
			return true
		}
		// Try matching suffix against progressively shorter tails.
		pathParts := strings.Split(path, string(filepath.Separator))
		for i := 0; i < len(pathParts); i++ {
			tail := strings.Join(pathParts[i:], string(filepath.Separator))
			ok, _ := filepath.Match(suffix, tail)
			if ok {
				return true
			}
		}
		return false
	}

	// Middle part: find any position in the path where it matches.
	segment := strings.Trim(part, string(filepath.Separator))
	if segment == "" {
		return matchDoublestarParts(path, parts, idx+1)
	}

	pathParts := strings.Split(path, string(filepath.Separator))
	for i := 0; i < len(pathParts); i++ {
		ok, _ := filepath.Match(segment, pathParts[i])
		if ok {
			remaining := ""
			if i+1 < len(pathParts) {
				remaining = strings.Join(pathParts[i+1:], string(filepath.Separator))
			}
			if matchDoublestarParts(remaining, parts, idx+1) {
				return true
			}
		}
	}
	return false
}

// matchExcludePattern matches a file path against an exclude pattern.
// Exclude patterns without path separators are matched against the filename only.
func matchExcludePattern(path, pattern string) bool {
	if strings.Contains(pattern, string(filepath.Separator)) || strings.Contains(pattern, "/") {
		ok, _ := filepath.Match(pattern, path)
		return ok
	}
	ok, _ := filepath.Match(pattern, filepath.Base(path))
	return ok
}

func evalMaxLines(c MaxLinesConstraint, worktreeRoot string, fileSet map[string]bool) (Result, error) {
	result := Result{
		Name:   c.Name,
		Type:   "max_lines",
		Passed: true,
	}

	files, err := globFiles(worktreeRoot, c.Glob, c.Exclude)
	if err != nil {
		return result, fmt.Errorf("globbing %q: %w", c.Glob, err)
	}

	for _, relPath := range files {
		if fileSet != nil && !fileSet[relPath] {
			continue
		}

		data, err := os.ReadFile(filepath.Join(worktreeRoot, relPath))
		if err != nil {
			return result, fmt.Errorf("reading %s: %w", relPath, err)
		}

		lineCount := bytes.Count(data, []byte("\n"))

		// Check for shrink-only exception.
		if exc, ok := findLinesException(c.Exceptions, relPath); ok {
			if lineCount > exc.BaselineLines {
				result.Passed = false
				result.Messages = append(result.Messages,
					fmt.Sprintf("%s has %d lines, exceeds shrink-only baseline of %d",
						relPath, lineCount, exc.BaselineLines))
			}
			continue
		}

		if lineCount > c.Limit {
			result.Passed = false
			result.Messages = append(result.Messages,
				fmt.Sprintf("%s has %d lines, exceeds limit of %d",
					relPath, lineCount, c.Limit))
		}
	}

	return result, nil
}

func findLinesException(exceptions []LinesException, path string) (LinesException, bool) {
	for _, e := range exceptions {
		if e.Path == path {
			return e, true
		}
	}
	return LinesException{}, false
}

func evalMaxMatches(c MaxMatchesConstraint, worktreeRoot string, fileSet map[string]bool) (Result, error) {
	result := Result{
		Name:   c.Name,
		Type:   "max_matches",
		Passed: true,
	}

	re, err := regexp.Compile("(?m)" + c.Pattern)
	if err != nil {
		return result, fmt.Errorf("compiling pattern %q: %w", c.Pattern, err)
	}

	files, err := globFiles(worktreeRoot, c.Glob, c.Exclude)
	if err != nil {
		return result, fmt.Errorf("globbing %q: %w", c.Glob, err)
	}

	if c.Scope == "directory" {
		return evalMaxMatchesDirectory(c, re, files, worktreeRoot, fileSet, result)
	}

	// scope = "file" (default)
	for _, relPath := range files {
		if fileSet != nil && !fileSet[relPath] {
			continue
		}

		data, err := os.ReadFile(filepath.Join(worktreeRoot, relPath))
		if err != nil {
			return result, fmt.Errorf("reading %s: %w", relPath, err)
		}

		count := len(re.FindAll(data, -1))

		if exc, ok := findMatchesException(c.Exceptions, relPath); ok {
			if count > exc.BaselineCount {
				result.Passed = false
				result.Messages = append(result.Messages,
					fmt.Sprintf("%s has %d matches, exceeds shrink-only baseline of %d",
						relPath, count, exc.BaselineCount))
			}
			continue
		}

		if count > c.Limit {
			result.Passed = false
			result.Messages = append(result.Messages,
				fmt.Sprintf("%s has %d matches, exceeds limit of %d",
					relPath, count, c.Limit))
		}
	}

	return result, nil
}

func evalMaxMatchesDirectory(c MaxMatchesConstraint, re *regexp.Regexp, files []string, worktreeRoot string, fileSet map[string]bool, result Result) (Result, error) {
	if c.CountMode == "unique" {
		// Collect unique matching lines per directory to count distinct import paths.
		// Splitting a file within a package does not increase the count because shared
		// lines (e.g. the same import appearing in two files) are deduplicated.
		dirSets := make(map[string]map[string]bool)

		for _, relPath := range files {
			if fileSet != nil && !fileSet[relPath] {
				continue
			}

			data, err := os.ReadFile(filepath.Join(worktreeRoot, relPath))
			if err != nil {
				return result, fmt.Errorf("reading %s: %w", relPath, err)
			}

			dir := filepath.Dir(relPath) + "/"
			if dirSets[dir] == nil {
				dirSets[dir] = make(map[string]bool)
			}
			for _, line := range strings.Split(string(data), "\n") {
				if re.MatchString(line) {
					dirSets[dir][strings.TrimSpace(line)] = true
				}
			}
		}

		for dir, set := range dirSets {
			count := len(set)
			if exc, ok := findMatchesException(c.Exceptions, dir); ok {
				if count > exc.BaselineCount {
					result.Passed = false
					result.Messages = append(result.Messages,
						fmt.Sprintf("%s has %d matches, exceeds shrink-only baseline of %d",
							dir, count, exc.BaselineCount))
				}
				continue
			}

			if count > c.Limit {
				result.Passed = false
				result.Messages = append(result.Messages,
					fmt.Sprintf("%s has %d matches, exceeds limit of %d",
						dir, count, c.Limit))
			}
		}

		return result, nil
	}

	// Aggregate match counts per directory (default: total occurrences).
	dirCounts := make(map[string]int)

	for _, relPath := range files {
		if fileSet != nil && !fileSet[relPath] {
			continue
		}

		data, err := os.ReadFile(filepath.Join(worktreeRoot, relPath))
		if err != nil {
			return result, fmt.Errorf("reading %s: %w", relPath, err)
		}

		count := len(re.FindAll(data, -1))
		dir := filepath.Dir(relPath) + "/"
		dirCounts[dir] += count
	}

	for dir, count := range dirCounts {
		if exc, ok := findMatchesException(c.Exceptions, dir); ok {
			if count > exc.BaselineCount {
				result.Passed = false
				result.Messages = append(result.Messages,
					fmt.Sprintf("%s has %d matches, exceeds shrink-only baseline of %d",
						dir, count, exc.BaselineCount))
			}
			continue
		}

		if count > c.Limit {
			result.Passed = false
			result.Messages = append(result.Messages,
				fmt.Sprintf("%s has %d matches, exceeds limit of %d",
					dir, count, c.Limit))
		}
	}

	return result, nil
}

func findMatchesException(exceptions []MatchesException, path string) (MatchesException, bool) {
	for _, e := range exceptions {
		if e.Path == path {
			return e, true
		}
	}
	return MatchesException{}, false
}

func evalDepth(c depthConstraint, worktreeRoot string, fileSet map[string]bool) (Result, error) {
	result := Result{
		Name:   c.Name,
		Type:   "depth",
		Passed: true,
	}

	files, err := globFiles(worktreeRoot, c.Glob, c.Exclude)
	if err != nil {
		return result, fmt.Errorf("globbing %q: %w", c.Glob, err)
	}

	// Group files by directory to identify package paths.
	// Key: directory path (e.g. "internal/orchestrator"), value: list of file names.
	pkgFiles := make(map[string][]string)
	for _, relPath := range files {
		dir := filepath.Dir(relPath)
		pkgFiles[dir] = append(pkgFiles[dir], filepath.Base(relPath))
	}

	for dir, names := range pkgFiles {
		// Skip packages that only have test files.
		hasNonTest := false
		for _, name := range names {
			if !strings.HasSuffix(name, "_test.go") {
				hasNonTest = true
				break
			}
		}
		if !hasNonTest {
			continue
		}

		// In plan validation mode, only evaluate packages containing at least
		// one file in fileSet.
		if fileSet != nil {
			relevant := false
			for _, name := range names {
				if fileSet[filepath.Join(dir, name)] {
					relevant = true
					break
				}
			}
			if !relevant {
				continue
			}
		}

		report, err := depth.Analyze(worktreeRoot, dir)
		if err != nil {
			return result, fmt.Errorf("analyzing depth of %s: %w", dir, err)
		}

		exc, grandfathered := findDepthException(c.Exceptions, dir)

		// Check export ratio.
		if grandfathered {
			if report.ExportRatio > exc.BaselineRatio {
				result.Passed = false
				result.Messages = append(result.Messages,
					fmt.Sprintf("%s has export ratio %.4f, exceeds shrink-only baseline of %.4f",
						dir, report.ExportRatio, exc.BaselineRatio))
			}
		} else {
			if report.ExportRatio > c.MaxExportRatio {
				result.Passed = false
				result.Messages = append(result.Messages,
					fmt.Sprintf("%s has export ratio %.4f, exceeds limit of %.4f",
						dir, report.ExportRatio, c.MaxExportRatio))
			}
		}

		// Check pass-through count.
		ptCount := len(report.PassThroughFuncs)
		if grandfathered {
			if ptCount > exc.BaselinePassThrus {
				result.Passed = false
				result.Messages = append(result.Messages,
					fmt.Sprintf("%s has %d pass-throughs, exceeds shrink-only baseline of %d",
						dir, ptCount, exc.BaselinePassThrus))
			}
		} else {
			if ptCount > c.MaxPassThroughs {
				result.Passed = false
				result.Messages = append(result.Messages,
					fmt.Sprintf("%s has %d pass-throughs, exceeds limit of %d",
						dir, ptCount, c.MaxPassThroughs))
			}
		}
	}

	return result, nil
}

func findDepthException(exceptions []depthException, dir string) (depthException, bool) {
	// Match against directory path with trailing slash (e.g. "internal/tui/").
	dirWithSlash := dir + "/"
	for _, e := range exceptions {
		if e.Path == dir || e.Path == dirWithSlash {
			return e, true
		}
	}
	return depthException{}, false
}

func evalNoMatch(c noMatchConstraint, worktreeRoot string, fileSet map[string]bool) (Result, error) {
	result := Result{
		Name:   c.Name,
		Type:   "no_match",
		Passed: true,
	}

	re, err := regexp.Compile(c.Pattern)
	if err != nil {
		return result, fmt.Errorf("compiling pattern %q: %w", c.Pattern, err)
	}

	files, err := globFiles(worktreeRoot, c.Glob, nil)
	if err != nil {
		return result, fmt.Errorf("globbing %q: %w", c.Glob, err)
	}

	for _, relPath := range files {
		if fileSet != nil && !fileSet[relPath] {
			continue
		}

		// Check exclude_path.
		excluded := false
		for _, ep := range c.ExcludePath {
			if strings.HasPrefix(relPath, ep) {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}

		data, err := os.ReadFile(filepath.Join(worktreeRoot, relPath))
		if err != nil {
			return result, fmt.Errorf("reading %s: %w", relPath, err)
		}

		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			if re.MatchString(line) {
				result.Passed = false
				result.Messages = append(result.Messages,
					fmt.Sprintf("%s: %s", relPath, strings.TrimSpace(line)))
			}
		}
	}

	return result, nil
}
