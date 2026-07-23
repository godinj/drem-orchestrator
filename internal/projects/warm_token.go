package projects

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const warmAgentTokenFilename = "warm-agent.token"

func WarmAgentTokenPath(homeDir string) (string, error) {
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
	}
	return filepath.Join(homeDir, ".drem", warmAgentTokenFilename), nil
}

// EnsureWarmAgentToken creates the shared control-plane token once with mode
// 0600 and preserves an existing non-empty token.
func EnsureWarmAgentToken(homeDir string) (string, error) {
	path, err := WarmAgentTokenPath(homeDir)
	if err != nil {
		return "", err
	}
	if raw, readErr := os.ReadFile(path); readErr == nil {
		if strings.TrimSpace(string(raw)) == "" {
			return "", fmt.Errorf("warm-agent token file %q is empty", path)
		}
		return path, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return "", fmt.Errorf("read warm-agent token: %w", readErr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create warm-agent token directory: %w", err)
	}
	token, err := NewSharedToken()
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return EnsureWarmAgentToken(homeDir)
		}
		return "", fmt.Errorf("create warm-agent token: %w", err)
	}
	if _, err := file.WriteString(token + "\n"); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write warm-agent token: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close warm-agent token: %w", err)
	}
	return path, nil
}
