package projects

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// StateSnapshot holds per-project state that isn't in the registry and
// must survive a regen of compose.yml / drem.toml. Callers produce one
// via ReadStateFromDisk before re-rendering, and feed the fields into
// TemplateData so the rendered output preserves running services'
// auth + addressing.
//
// The fields here are deliberately narrow: only state that the template
// cannot regenerate from (Project registry entry + hard-coded defaults)
// belongs here. See plans/drem-project-register-update.md §5.1.
type StateSnapshot struct {
	// SharedToken is services.orch.environment.DREM_AGENTMON_TOKEN from
	// the on-disk compose.yml. Empty when the file is missing or doesn't
	// declare the env key. The update CLI MUST refuse to regenerate a
	// compose.yml with an empty token unless --regenerate-token is
	// passed — regenerating silently would break auth for orch +
	// agentmon + csuite-watcher.
	SharedToken string

	// ObservedOrchHostPort is the host-side port from the first entry
	// of services.orch.ports in the on-disk compose.yml, parsed as
	// "[<addr>:]host:container". Zero when the file is missing, has no
	// ports block, or the entry fails to parse. Serves as a fallback
	// when Project.OrchHostPort in the registry is also zero (the
	// current drem-orchestrator case).
	ObservedOrchHostPort int
}

// composeOnDisk is a partial YAML shape that captures just the fields
// ReadStateFromDisk extracts. Intentionally NOT the full compose schema
// — extra keys in the on-disk file are ignored.
type composeOnDisk struct {
	Services struct {
		Orch struct {
			Environment map[string]string `yaml:"environment"`
			Ports       []string          `yaml:"ports"`
		} `yaml:"orch"`
	} `yaml:"services"`
}

// ReadStateFromDisk parses ~/.drem/projects/<projectName>/compose.yml
// and returns a StateSnapshot. Missing compose.yml yields a zero
// snapshot without error — the caller decides fail-closed semantics
// based on required-fields (e.g. SharedToken must be non-empty for
// update to proceed without --regenerate-token). Unparseable YAML is
// an error.
//
// homeDir=="" falls back to os.UserHomeDir. projectName=="" is an
// error (programming mistake; mirrors WriteProjectComposeAt).
func ReadStateFromDisk(homeDir, projectName string) (StateSnapshot, error) {
	if projectName == "" {
		return StateSnapshot{}, errors.New("projectName is required")
	}
	if homeDir == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return StateSnapshot{}, fmt.Errorf("resolve home dir: %w", err)
		}
		homeDir = h
	}
	composePath := filepath.Join(homeDir, ".drem", "projects", projectName, "compose.yml")
	data, err := os.ReadFile(composePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StateSnapshot{}, nil
		}
		return StateSnapshot{}, fmt.Errorf("read compose %q: %w", composePath, err)
	}
	var parsed composeOnDisk
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return StateSnapshot{}, fmt.Errorf("parse compose %q: %w", composePath, err)
	}
	return StateSnapshot{
		SharedToken:          parsed.Services.Orch.Environment["DREM_AGENTMON_TOKEN"],
		ObservedOrchHostPort: extractHostPort(parsed.Services.Orch.Ports),
	}, nil
}

// extractHostPort pulls the host-side port out of the first entry in a
// docker-compose ports list. Accepts "host:container",
// "addr:host:container", or a bare number. Returns zero on any parse
// failure or empty list — the caller decides whether zero is a problem.
func extractHostPort(ports []string) int {
	if len(ports) == 0 {
		return 0
	}
	parts := strings.Split(ports[0], ":")
	// "container" -> parts[0]; "host:container" -> parts[0]; "addr:host:container" -> parts[1].
	var hostStr string
	switch len(parts) {
	case 1:
		hostStr = parts[0]
	case 2:
		hostStr = parts[0]
	case 3:
		hostStr = parts[1]
	default:
		return 0
	}
	n, err := strconv.Atoi(hostStr)
	if err != nil {
		return 0
	}
	return n
}
