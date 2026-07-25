package benchv2

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type ScopedAgentWorkspace struct {
	WorkDir          string
	fixture          string
	readOnly         map[string][]byte
	writable         map[string]bool
	writableOriginal map[string]scopedFileSnapshot
	internal         map[string]bool
}

type scopedFileSnapshot struct {
	exists bool
	raw    []byte
}

func PrepareScopedAgentWorkspace(fixture string, readPaths, writePaths, internalPaths []string) (*ScopedAgentWorkspace, error) {
	root, err := os.MkdirTemp("", "canvasbench-agent-view-")
	if err != nil {
		return nil, err
	}
	// The outer container runs as a fixed unprivileged uid which does not own
	// the host-created projection. Make the projection traversable and grant
	// writes only along declared writable paths; repository scope is enforced
	// again by Validate before any output is copied back.
	if err := os.Chmod(root, 0o755); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	workspace := &ScopedAgentWorkspace{
		WorkDir: root, fixture: fixture, readOnly: map[string][]byte{}, writable: map[string]bool{},
		writableOriginal: map[string]scopedFileSnapshot{}, internal: map[string]bool{},
	}
	fail := func(err error) (*ScopedAgentWorkspace, error) {
		_ = workspace.Cleanup()
		return nil, err
	}
	for _, path := range writePaths {
		relative, err := cleanScopedRelative(path)
		if err != nil {
			return fail(err)
		}
		workspace.writable[relative] = true
		if err := makeWritableParent(root, relative); err != nil {
			return fail(err)
		}
	}
	for _, path := range internalPaths {
		relative, err := cleanScopedRelative(path)
		if err != nil {
			return fail(err)
		}
		workspace.internal[relative] = true
		if err := makeWritableParent(root, relative); err != nil {
			return fail(err)
		}
	}
	seen := map[string]bool{}
	for _, path := range append(append([]string(nil), readPaths...), writePaths...) {
		relative, err := cleanScopedRelative(path)
		if err != nil {
			return fail(err)
		}
		if seen[relative] {
			continue
		}
		seen[relative] = true
		source := filepath.Join(fixture, filepath.FromSlash(relative))
		info, err := os.Lstat(source)
		if os.IsNotExist(err) && workspace.writable[relative] {
			workspace.writableOriginal[relative] = scopedFileSnapshot{}
			if err := os.MkdirAll(filepath.Dir(filepath.Join(root, filepath.FromSlash(relative))), 0o755); err != nil {
				return fail(err)
			}
			continue
		}
		if err != nil {
			return fail(fmt.Errorf("materialize %s: %w", relative, err))
		}
		if !info.Mode().IsRegular() {
			return fail(fmt.Errorf("scoped path %s is not a regular file", relative))
		}
		raw, err := os.ReadFile(source)
		if err != nil {
			return fail(err)
		}
		destination := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return fail(err)
		}
		if err := os.WriteFile(destination, raw, info.Mode().Perm()); err != nil {
			return fail(err)
		}
		if workspace.writable[relative] {
			workspace.writableOriginal[relative] = scopedFileSnapshot{exists: true, raw: append([]byte(nil), raw...)}
			if err := os.Chmod(destination, 0o666); err != nil {
				return fail(err)
			}
		} else {
			workspace.readOnly[relative] = raw
		}
	}
	return workspace, nil
}

func (workspace *ScopedAgentWorkspace) MutationObserved() (bool, error) {
	if workspace == nil || workspace.WorkDir == "" {
		return false, fmt.Errorf("scoped workspace is unavailable")
	}
	for relative, original := range workspace.writableOriginal {
		raw, err := os.ReadFile(filepath.Join(workspace.WorkDir, filepath.FromSlash(relative)))
		if os.IsNotExist(err) {
			if original.exists {
				return true, nil
			}
			continue
		}
		if err != nil {
			return false, err
		}
		if !original.exists || !bytes.Equal(raw, original.raw) {
			return true, nil
		}
	}
	return false, nil
}

func makeWritableParent(root, relative string) error {
	parent := filepath.Dir(filepath.Join(root, filepath.FromSlash(relative)))
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	for {
		if err := os.Chmod(parent, 0o777); err != nil {
			return err
		}
		if parent == root {
			return nil
		}
		next := filepath.Dir(parent)
		if next == parent || !strings.HasPrefix(next, root) {
			return fmt.Errorf("writable path escapes projection")
		}
		parent = next
	}
}

func (workspace *ScopedAgentWorkspace) ValidateAndApply() error {
	if err := workspace.Validate(); err != nil {
		return err
	}
	return workspace.Apply()
}

func (workspace *ScopedAgentWorkspace) Validate() error {
	if workspace == nil || workspace.WorkDir == "" {
		return fmt.Errorf("scoped workspace is unavailable")
	}
	actual := map[string]bool{}
	err := filepath.WalkDir(workspace.WorkDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == workspace.WorkDir || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("agent workspace contains non-regular output")
		}
		relative, err := filepath.Rel(workspace.WorkDir, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if !workspace.writable[relative] && !workspace.internal[relative] {
			expected, exists := workspace.readOnly[relative]
			if !exists {
				return fmt.Errorf("undeclared output %s", relative)
			}
			raw, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(raw, expected) {
				return fmt.Errorf("read-only path changed: %s", relative)
			}
		}
		actual[relative] = true
		return nil
	})
	if err != nil {
		return err
	}
	for relative := range workspace.readOnly {
		if !actual[relative] {
			return fmt.Errorf("read-only path removed: %s", relative)
		}
	}
	return nil
}

func (workspace *ScopedAgentWorkspace) Apply() error {
	paths := make([]string, 0, len(workspace.writable))
	for path := range workspace.writable {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, relative := range paths {
		source := filepath.Join(workspace.WorkDir, filepath.FromSlash(relative))
		destination := filepath.Join(workspace.fixture, filepath.FromSlash(relative))
		raw, err := os.ReadFile(source)
		if os.IsNotExist(err) {
			if removeErr := os.Remove(destination); removeErr != nil && !os.IsNotExist(removeErr) {
				return removeErr
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(destination, raw, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (workspace *ScopedAgentWorkspace) Cleanup() error {
	if workspace == nil || workspace.WorkDir == "" {
		return nil
	}
	return os.RemoveAll(workspace.WorkDir)
}

func cleanScopedRelative(path string) (string, error) {
	cleaned := filepath.ToSlash(filepath.Clean(path))
	if cleaned == "." || filepath.IsAbs(path) || strings.HasPrefix(cleaned, "../") || cleaned == ".git" || strings.HasPrefix(cleaned, ".git/") || strings.Contains(strings.ToLower(cleaned), "oracle") {
		return "", fmt.Errorf("invalid agent-visible path %q", path)
	}
	return cleaned, nil
}
