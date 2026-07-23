// Package serviceauth resolves the host-local bearer token used between
// project orchestrators and shared warm control-plane services.
package serviceauth

import (
	"fmt"
	"os"
	"strings"
)

const (
	TokenEnv     = "DREM_WARM_AGENT_TOKEN"
	TokenFileEnv = "DREM_WARM_AGENT_TOKEN_FILE"
	LegacyEnv    = "DREM_AGENTMON_TOKEN"
)

// Resolve returns the dedicated warm-service token. A configured file that
// cannot be read or is empty fails closed. The legacy per-project token is a
// compatibility fallback only when neither dedicated source is configured.
func Resolve() (string, error) {
	if token := strings.TrimSpace(os.Getenv(TokenEnv)); token != "" {
		return token, nil
	}
	if path := strings.TrimSpace(os.Getenv(TokenFileEnv)); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read warm-agent token file: %w", err)
		}
		token := strings.TrimSpace(string(raw))
		if token == "" {
			return "", fmt.Errorf("warm-agent token file %q is empty", path)
		}
		return token, nil
	}
	return strings.TrimSpace(os.Getenv(LegacyEnv)), nil
}
