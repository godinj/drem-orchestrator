package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// mutationOnlyTools narrows a corrective turn after bounded reconnaissance to
// the two repository mutation primitives. Merely telling a small model that
// its next call must mutate proved insufficient: Qwen sometimes requested the
// same denied read again. Removing reconnaissance tools from that one request
// makes the phase transition part of the protocol instead of prompt advice.
func mutationOnlyTools(cfg DirectToolAgentConfig, tools []toolDefinition) []toolDefinition {
	writeNeeded := true
	if cfg.ProtectExistingFiles && len(cfg.ScopedFiles) > 0 {
		writeNeeded = false
		for _, scoped := range cfg.ScopedFiles {
			path := scoped
			if !filepath.IsAbs(path) {
				path = filepath.Join(cfg.WorkDir, path)
			}
			if _, err := os.Stat(filepath.Clean(path)); err != nil {
				writeNeeded = true
				break
			}
		}
	}
	filtered := make([]toolDefinition, 0, 2)
	for _, tool := range tools {
		if tool.Function.Name == "edit" || (tool.Function.Name == "write" && writeNeeded) {
			filtered = append(filtered, tool)
		}
	}
	if len(filtered) == 0 {
		return tools
	}
	return filtered
}

// preMutationTools keeps a scoped writer on the structured read/edit/write
// protocol until it has created a checkpoint. Shell is useful for lightweight
// verification after an edit, but before the first mutation small models use
// it to rediscover source already present in the verified context pack.
func preMutationTools(cfg DirectToolAgentConfig, tools []toolDefinition) []toolDefinition {
	if len(cfg.ScopedFiles) == 0 {
		return tools
	}
	filtered := make([]toolDefinition, 0, len(tools))
	for _, tool := range tools {
		if tool.Function.Name != "bash" {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func toolIsAvailable(tools []toolDefinition, name string) bool {
	for _, tool := range tools {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}

// repairMalformedScopedMutation handles a narrow Qwen/SGLang parser defect
// observed in the live Canvas worker canary. The native tool stream can omit
// the closing delimiter after a function name, causing the server to surface
// `edit\n<parameter=path` as the function name while retaining the remaining
// JSON arguments but dropping path. Inferring the path is safe only when the
// worker has exactly one writable file; otherwise the malformed call remains
// blocked and cannot mutate an ambiguous target.
func repairMalformedScopedMutation(call toolCall, scopedFiles []string) (toolCall, bool) {
	const pathMarker = "\n<parameter=path"
	marker := strings.Index(call.Function.Name, pathMarker)
	name := strings.TrimSpace(call.Function.Name)
	changed := false
	if marker >= 0 {
		if strings.TrimSpace(call.Function.Name[marker+len(pathMarker):]) != "" {
			return call, false
		}
		name = strings.TrimSpace(call.Function.Name[:marker])
		changed = true
	}
	if name != "edit" && name != "write" {
		return call, false
	}
	var args map[string]any
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return call, false
	}
	if path, _ := args["path"].(string); strings.TrimSpace(path) == "" {
		if len(scopedFiles) != 1 || strings.TrimSpace(scopedFiles[0]) == "" {
			return call, false
		}
		args["path"] = scopedFiles[0]
		changed = true
	}
	if name == "edit" {
		changed = normalizeStringArgumentAlias(args, "old", []string{"old_string", "old_text", "oldText", "search", "find"}) || changed
		changed = normalizeStringArgumentAlias(args, "new", []string{"new_string", "new_text", "newText", "replacement", "replace"}) || changed
	} else {
		changed = normalizeStringArgumentAlias(args, "content", []string{"file_content", "new_content", "text"}) || changed
	}
	if !changed {
		return call, false
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return call, false
	}
	call.Function.Name = name
	call.Function.Arguments = string(encoded)
	return call, true
}

func normalizeStringArgumentAlias(args map[string]any, canonical string, aliases []string) bool {
	if value, _ := args[canonical].(string); value != "" {
		return false
	}
	for _, alias := range aliases {
		if value, _ := args[alias].(string); value != "" {
			args[canonical] = value
			delete(args, alias)
			return true
		}
	}
	return false
}

func writeTargetsExistingFile(workDir, argsJSON string) bool {
	var args struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(argsJSON), &args) != nil || args.Path == "" {
		return false
	}
	path := args.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}
	_, err := os.Stat(filepath.Clean(path))
	return err == nil
}

func toolCallPath(argsJSON string) string {
	var args struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(argsJSON), &args) != nil {
		return ""
	}
	return filepath.Clean(args.Path)
}

func toolCallTargetsPath(argsJSON, target string) bool {
	path := toolCallPath(argsJSON)
	return path != "" && target != "" && path == filepath.Clean(target)
}

func toolCallTargetsAnyPath(workDir, argsJSON string, targets []string) bool {
	path := normalizeScopedPath(workDir, toolCallPath(argsJSON))
	if path == "" {
		return false
	}
	for _, target := range targets {
		if target != "" && path == normalizeScopedPath(workDir, target) {
			return true
		}
	}
	return false
}

// mutationTargetsOutsideScope rejects structured repository mutations before
// they reach the executor. ScopedFiles is the worker's writable contract, not
// merely prompt guidance: a model may read or test adjacent assembly files,
// but edit/write calls must target one of the explicitly assigned paths.
// Malformed calls are left to the executor so callers still receive the normal
// argument-validation error instead of a misleading scope violation.
func mutationTargetsOutsideScope(workDir, toolName, argsJSON string, targets []string) bool {
	if (toolName != "edit" && toolName != "write") || len(targets) == 0 || toolCallPath(argsJSON) == "" {
		return false
	}
	return !toolCallTargetsAnyPath(workDir, argsJSON, targets)
}

func normalizeScopedPath(workDir, path string) string {
	if path == "" {
		return ""
	}
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return cleaned
	}
	workDir = filepath.Clean(workDir)
	relative, err := filepath.Rel(workDir, cleaned)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return cleaned
	}
	return filepath.Clean(relative)
}

func editErrorIsRecoverable(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "old string not found") ||
		(strings.Contains(message, "old string appears") && strings.Contains(message, "must be unique"))
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if os.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// truncateForStub clamps a result string to n characters so dedup stubs do
// not blow the context when the original result was large. Used when a
// duplicate tool call is elided — the second+ result just points back at the
// first and does not need the full body repeated.
func truncateForStub(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n[...truncated]"
}

// writeTrace emits one JSON-line TraceEvent if the trace writer is configured.
// Failures are logged but never surface — tracing is strictly opt-in diagnostic.
func writeTrace(w io.Writer, ev TraceEvent) {
	if w == nil {
		return
	}
	data, err := json.Marshal(ev)
	if err != nil {
		slog.Warn("direct tool agent: trace marshal failed", "err", err)
		return
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		slog.Warn("direct tool agent: trace write failed", "err", err)
	}
}
