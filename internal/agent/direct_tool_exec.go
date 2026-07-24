// direct_tool_exec.go implements the workDir-sandboxed tool dispatcher used by
// the direct tool-calling agent loop in direct_tool_agent.go.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	maxReadLines        = 200
	maxScopedReadLines  = 800
	maxBashOutput       = 2000 // characters
	maxGrepMatches      = 50
	maxGlobResults      = 100
	maxToolCallsPerTurn = 8 // cap parallel tool calls; prevents degenerate generation explosions
	maxFileReads        = 3 // warn after N reads of the same file path
)

// toolExecutor bundles the working directory and config needed to run tools.
type toolExecutor struct {
	workDir     string
	bashTimeout time.Duration
	scopedFiles map[string]struct{}
}

var quotedIncludePattern = regexp.MustCompile(`(?m)^\s*#\s*include\s*"([^"]+)"`)

func isCPlusPlusPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".c", ".cc", ".cpp", ".cxx", ".h", ".hh", ".hpp", ".hxx":
		return true
	default:
		return false
	}
}

func quotedIncludes(content string) map[string]struct{} {
	includes := make(map[string]struct{})
	for _, match := range quotedIncludePattern.FindAllStringSubmatch(content, -1) {
		if len(match) == 2 {
			includes[filepath.ToSlash(filepath.Clean(match[1]))] = struct{}{}
		}
	}
	return includes
}

func pathSuffixMatches(candidate, include string) bool {
	candidate = filepath.ToSlash(filepath.Clean(candidate))
	include = filepath.ToSlash(filepath.Clean(include))
	return candidate == include || strings.HasSuffix(candidate, "/"+include)
}

// validateNewQuotedIncludes prevents a scoped C/C++ writer from introducing
// a repository-local header name that does not exist in the exact worktree or
// the immutable writable scope. This catches model-invented/obsolete headers
// at the mutation boundary, before they consume a compile/rework cycle. Existing
// includes are grandfathered so an unrelated edit cannot be blocked by legacy
// source, and a planned header/source pair may be written in either order.
func (te *toolExecutor) validateNewQuotedIncludes(path, before, after string) error {
	if !isCPlusPlusPath(path) {
		return nil
	}
	oldIncludes := quotedIncludes(before)
	for include := range quotedIncludes(after) {
		if _, existed := oldIncludes[include]; existed {
			continue
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), filepath.FromSlash(include))); err == nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(te.workDir, filepath.FromSlash(include))); err == nil {
			continue
		}
		known := false
		for scoped := range te.scopedFiles {
			if pathSuffixMatches(scoped, include) {
				known = true
				break
			}
		}
		if !known {
			cmd := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard")
			cmd.Dir = te.workDir
			if output, err := cmd.Output(); err == nil {
				for _, repositoryPath := range strings.Split(string(output), "\n") {
					if pathSuffixMatches(repositoryPath, include) {
						known = true
						break
					}
				}
			}
		}
		if !known {
			return fmt.Errorf("[HARNESS] rejected new quoted include %q in %s: no matching exact-worktree or planned scoped file exists; use an include already grounded by the verified source pack or a neighboring source file", include, filepath.Base(path))
		}
	}
	return nil
}

// execute dispatches a tool call by name and returns the result string.
func (te *toolExecutor) execute(name string, argsJSON string) (string, error) {
	switch name {
	case "read":
		return te.execRead(argsJSON)
	case "edit":
		return te.execEdit(argsJSON)
	case "write":
		return te.execWrite(argsJSON)
	case "bash":
		return te.execBash(argsJSON)
	case "grep":
		return te.execGrep(argsJSON)
	case "glob":
		return te.execGlob(argsJSON)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// resolvePath resolves a path relative to the working directory and validates
// it stays within bounds.
func (te *toolExecutor) resolvePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	var resolved string
	if filepath.IsAbs(p) {
		resolved = filepath.Clean(p)
	} else {
		resolved = filepath.Clean(filepath.Join(te.workDir, p))
	}
	if !strings.HasPrefix(resolved, te.workDir) {
		return "", fmt.Errorf("path %q is outside working directory %q", p, te.workDir)
	}
	return resolved, nil
}

func (te *toolExecutor) execRead(argsJSON string) (string, error) {
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse read args: %w", err)
	}
	resolved, err := te.resolvePath(args.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", args.Path, err)
	}
	lines := strings.Split(string(data), "\n")

	offset := args.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= len(lines) {
		return fmt.Sprintf("[file has %d lines, offset %d is past end]", len(lines), offset), nil
	}
	lines = lines[offset:]

	readLimit := maxReadLines
	if args.Offset == 0 {
		if _, scoped := te.scopedFiles[resolved]; scoped {
			readLimit = maxScopedReadLines
		}
	}
	limit := args.Limit
	if limit <= 0 {
		limit = readLimit
	}
	if limit > readLimit {
		limit = readLimit
	}
	truncated := false
	if len(lines) > limit {
		lines = lines[:limit]
		truncated = true
	}

	// Format with line numbers. Use '|' separator so models do not confuse it
	// with file indentation when constructing edit strings. A tab-separator
	// triggers a deterministic edit-retry loop where the model copies
	// "<N>\t\t<content>" into edit.old and strips only "<N>", leaving an extra
	// phantom tab that never matches the file.
	var buf strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&buf, "%4d|%s\n", offset+i+1, line)
	}
	if truncated {
		fmt.Fprintf(&buf, "[... truncated at %d lines]", limit)
	}
	return buf.String(), nil
}

func (te *toolExecutor) execEdit(argsJSON string) (string, error) {
	var args struct {
		Path string `json:"path"`
		Old  string `json:"old"`
		New  string `json:"new"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse edit args: %w", err)
	}
	resolved, err := te.resolvePath(args.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("read %s for edit: %w", args.Path, err)
	}
	content := string(data)
	if !strings.Contains(content, args.Old) {
		return "", fmt.Errorf("old string not found in %s", args.Path)
	}
	count := strings.Count(content, args.Old)
	if count > 1 {
		return "", fmt.Errorf("old string appears %d times in %s (must be unique)", count, args.Path)
	}
	newContent := strings.Replace(content, args.Old, args.New, 1)
	if err := te.validateNewQuotedIncludes(resolved, content, newContent); err != nil {
		return "", err
	}
	if err := os.WriteFile(resolved, []byte(newContent), 0o644); err != nil {
		return "", fmt.Errorf("write %s after edit: %w", args.Path, err)
	}
	return fmt.Sprintf("edited %s: replaced %d bytes with %d bytes", args.Path, len(args.Old), len(args.New)), nil
}

func (te *toolExecutor) execWrite(argsJSON string) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse write args: %w", err)
	}
	resolved, err := te.resolvePath(args.Path)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(resolved)
	before := ""
	if existing, err := os.ReadFile(resolved); err == nil {
		before = string(existing)
	}
	if err := te.validateNewQuotedIncludes(resolved, before, args.Content); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create directory %s: %w", dir, err)
	}
	if err := os.WriteFile(resolved, []byte(args.Content), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", args.Path, err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path), nil
}

// bashFileOpRedirect detects bash commands that should use structured tools.
// Returns a non-empty redirect message if the command matches a file-op pattern.
func bashFileOpRedirect(cmd string) string {
	trimmed := strings.TrimSpace(cmd)
	lower := strings.ToLower(trimmed)

	if strings.Contains(trimmed, "<<") && strings.Contains(trimmed, ">") {
		return "[HARNESS] bash heredocs are not supported for writing files — they break on backticks, quotes, and special characters. Use the write tool instead:\n" +
			"  write({\"path\": \"<file>\", \"content\": \"<content>\"})\n" +
			"The write tool handles all escaping safely. Do NOT retry this command with bash."
	}
	if (strings.HasPrefix(lower, "echo ") || strings.HasPrefix(lower, "printf ")) &&
		strings.Contains(trimmed, "> ") && !strings.Contains(trimmed, ">&") {
		return "[HARNESS] Do not use echo/printf to write files. Use the write tool instead:\n" +
			"  write({\"path\": \"<file>\", \"content\": \"<content>\"})"
	}
	if strings.Contains(lower, "| tee ") {
		return "[HARNESS] Do not use tee to write files. Use the write tool instead."
	}
	if strings.Contains(lower, "sed -i") || strings.Contains(lower, "sed -e") {
		return "[HARNESS] Do not use sed for file editing. Use the edit tool instead:\n" +
			"  edit({\"path\": \"<file>\", \"old\": \"<exact text>\", \"new\": \"<replacement>\"})"
	}
	if (strings.HasPrefix(lower, "cat ") || strings.HasPrefix(lower, "head ") || strings.HasPrefix(lower, "tail ")) &&
		!strings.Contains(trimmed, "|") && !strings.Contains(trimmed, ">") {
		return "[HARNESS] Do not use cat/head/tail to read files. Use the read tool instead:\n" +
			"  read({\"path\": \"<file>\", \"offset\": N, \"limit\": M})\n" +
			"The read tool returns formatted output with line numbers for precise editing."
	}

	return ""
}

func (te *toolExecutor) execBash(argsJSON string) (string, error) {
	var args struct {
		Cmd string `json:"cmd"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse bash args: %w", err)
	}

	if redirect := bashFileOpRedirect(args.Cmd); redirect != "" {
		slog.Warn("direct tool agent: bash file-op intercepted",
			"cmd_prefix", truncateForStub(args.Cmd, 80),
		)
		return redirect, nil
	}

	timeout := te.bashTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", args.Cmd)
	cmd.Dir = te.workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	var result strings.Builder
	if stdout.Len() > 0 {
		result.WriteString(stdout.String())
	}
	if stderr.Len() > 0 {
		if result.Len() > 0 {
			result.WriteString("\n")
		}
		result.WriteString("STDERR:\n")
		result.WriteString(stderr.String())
	}

	output := result.String()
	if len(output) > maxBashOutput {
		output = output[:maxBashOutput] + "\n[... truncated]"
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return output + "\n[timeout after " + timeout.String() + "]", nil
		}
		return output + "\n[exit error: " + err.Error() + "]", nil
	}
	if output == "" {
		return "(exit 0, no output)", nil
	}
	return output, nil
}

func (te *toolExecutor) execGrep(argsJSON string) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Glob    string `json:"glob"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse grep args: %w", err)
	}

	rgArgs := []string{"-n", "--max-count", fmt.Sprintf("%d", maxGrepMatches)}
	if args.Glob != "" {
		rgArgs = append(rgArgs, "--glob", args.Glob)
	}
	rgArgs = append(rgArgs, args.Pattern)

	searchPath := te.workDir
	if args.Path != "" {
		resolved, err := te.resolvePath(args.Path)
		if err != nil {
			return "", err
		}
		searchPath = resolved
	}
	rgArgs = append(rgArgs, searchPath)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "rg", rgArgs...)
	cmd.Dir = te.workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	output := stdout.String()
	if len(output) > maxBashOutput {
		output = output[:maxBashOutput] + "\n[... truncated]"
	}

	// ripgrep returns exit code 1 for no matches — not an error.
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "[no matches]", nil
		}
		if stderr.Len() > 0 {
			return "", fmt.Errorf("grep failed: %s", stderr.String())
		}
		return "", fmt.Errorf("grep failed: %w", err)
	}
	return output, nil
}

func (te *toolExecutor) execGlob(argsJSON string) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse glob args: %w", err)
	}

	baseDir := te.workDir
	if args.Path != "" {
		resolved, err := te.resolvePath(args.Path)
		if err != nil {
			return "", err
		}
		baseDir = resolved
	}

	fullPattern := filepath.Join(baseDir, args.Pattern)

	var matches []string
	if strings.Contains(args.Pattern, "**") {
		matches = walkGlob(baseDir, args.Pattern)
	} else {
		var err error
		matches, err = filepath.Glob(fullPattern)
		if err != nil {
			return "", fmt.Errorf("glob %s: %w", args.Pattern, err)
		}
	}

	if len(matches) == 0 {
		return "[no matches]", nil
	}

	var relMatches []string
	for _, m := range matches {
		rel, err := filepath.Rel(te.workDir, m)
		if err != nil {
			rel = m
		}
		relMatches = append(relMatches, rel)
	}

	if len(relMatches) > maxGlobResults {
		relMatches = relMatches[:maxGlobResults]
		return strings.Join(relMatches, "\n") + fmt.Sprintf("\n[... truncated at %d results]", maxGlobResults), nil
	}
	return strings.Join(relMatches, "\n"), nil
}

// walkGlob walks a directory tree and returns paths matching a double-star glob
// pattern. It uses filepath.Match on the relative path segments.
func walkGlob(baseDir, pattern string) []string {
	var matches []string
	parts := strings.SplitN(pattern, "**", 2)
	suffix := ""
	if len(parts) > 1 {
		suffix = strings.TrimPrefix(parts[1], "/")
		suffix = strings.TrimPrefix(suffix, string(filepath.Separator))
	}

	_ = filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if len(matches) >= maxGlobResults {
			return filepath.SkipAll
		}
		if suffix == "" {
			matches = append(matches, path)
			return nil
		}
		rel, relErr := filepath.Rel(baseDir, path)
		if relErr != nil {
			return nil
		}
		matched, _ := filepath.Match(suffix, filepath.Base(rel))
		if !matched {
			matched, _ = filepath.Match(suffix, rel)
		}
		if matched {
			matches = append(matches, path)
		}
		return nil
	})
	return matches
}
