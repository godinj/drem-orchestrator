// Package compose_test parses deploy/compose/global.yml at test time so a
// malformed service block (typo, indentation, missing service) trips the
// Go test suite instead of `docker compose up`. Today this only covers the
// drem-classifier service — extend the services-table as other global
// services grow tests.
package compose_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// composeDoc is the subset of the compose schema we assert on. Fields we
// don't care about unmarshal into the catch-all map.
type composeDoc struct {
	Services map[string]composeService `yaml:"services"`
}

type composeService struct {
	Image         string            `yaml:"image"`
	ContainerName string            `yaml:"container_name"`
	Networks      []string          `yaml:"networks"`
	// depends_on in compose accepts either a flat list of strings or a map
	// keyed by service name. We unmarshal to `any` and normalize at
	// assertion time.
	DependsOn   any               `yaml:"depends_on"`
	Labels      map[string]string `yaml:"labels"`
	Healthcheck map[string]any    `yaml:"healthcheck"`
	Environment map[string]string `yaml:"environment"`
	Build       map[string]any    `yaml:"build"`
}

// dependsOnServices returns the set of service names a compose entry
// depends on, regardless of whether depends_on was specified as a list or
// as a map.
func dependsOnServices(v any) map[string]bool {
	out := map[string]bool{}
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				out[s] = true
			}
		}
	case map[string]any:
		for k := range x {
			out[k] = true
		}
	}
	return out
}

// TestGlobalYAML_ParsesCleanly asserts the file is valid YAML and shaped
// like a compose document.
func TestGlobalYAML_ParsesCleanly(t *testing.T) {
	data := readGlobalYAML(t)
	var doc composeDoc
	require.NoError(t, yaml.Unmarshal(data, &doc), "global.yml must parse as yaml")
	require.NotEmpty(t, doc.Services, "global.yml must declare at least one service")
}

// TestGlobalYAML_DeclaresDremClassifier is the smoke test plans/warm-direct-
// classifier.md §7 calls for: the new drem-classifier service must be on
// drem-net, depend on sglang, and ship a /healthz-shaped healthcheck.
func TestGlobalYAML_DeclaresDremClassifier(t *testing.T) {
	data := readGlobalYAML(t)
	var doc composeDoc
	require.NoError(t, yaml.Unmarshal(data, &doc))

	svc, ok := doc.Services["drem-classifier"]
	require.True(t, ok, "drem-classifier service must be declared in global.yml")

	assert.Equal(t, "localhost:5000/drem-classifier:latest", svc.Image)
	assert.Equal(t, "drem-classifier", svc.ContainerName)
	assert.Contains(t, svc.Networks, "drem-net", "drem-classifier must attach drem-net")
	deps := dependsOnServices(svc.DependsOn)
	assert.True(t, deps["sglang"], "drem-classifier must depend_on sglang")
	assert.Equal(t, "global", svc.Labels["drem.scope"])
	assert.Equal(t, "drem-classifier", svc.Labels["drem.service"])
	require.NotNil(t, svc.Healthcheck, "drem-classifier must ship a healthcheck")

	// The healthcheck test entry must reference /healthz so a misconfigured
	// endpoint (e.g. swapped to /health) fails the test before deploy.
	testField, ok := svc.Healthcheck["test"]
	require.True(t, ok, "healthcheck must declare a test command")
	assert.Contains(t, stringifyYAMLNode(testField), "/healthz")
}

// readGlobalYAML resolves the absolute path to global.yml regardless of
// which directory `go test` was invoked from, then reads the bytes.
func readGlobalYAML(t *testing.T) []byte {
	t.Helper()
	// The test file sits next to global.yml, so CWD at test time is the
	// package directory.
	path := filepath.Join(".", "global.yml")
	data, err := os.ReadFile(path)
	require.NoError(t, err, "read global.yml")
	return data
}

// stringifyYAMLNode flattens a decoded YAML value (which may be []any,
// string, or yaml.Node) to a single string for substring assertions.
func stringifyYAMLNode(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		s := ""
		for _, e := range x {
			s += stringifyYAMLNode(e) + " "
		}
		return s
	case map[string]any:
		s := ""
		for _, e := range x {
			s += stringifyYAMLNode(e) + " "
		}
		return s
	default:
		return ""
	}
}
