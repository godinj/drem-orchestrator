package docker_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func TestCSuiteWatcherDockerfileBuildsExistingCommand(t *testing.T) {
	root := repoRoot(t)
	dockerfile := filepath.Join(root, "deploy", "docker", "csuite-watcher.Dockerfile")
	data, err := os.ReadFile(dockerfile)
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}

	const commandPath = "./cmd/csuite-watcher"
	if !strings.Contains(string(data), commandPath) {
		t.Fatalf("csuite-watcher Dockerfile must build %s", commandPath)
	}

	if _, err := os.Stat(filepath.Join(root, "cmd", "csuite-watcher", "main.go")); err != nil {
		t.Fatalf("Dockerfile references %s, but command source is missing: %v", commandPath, err)
	}
}

func TestCSuitePersonaBuildContractUsesOpenCode(t *testing.T) {
	root := repoRoot(t)
	baseDockerfile := filepath.Join(root, "deploy", "docker", "csuite-base.Dockerfile")
	data, err := os.ReadFile(baseDockerfile)
	if err != nil {
		t.Fatalf("read csuite-base Dockerfile: %v", err)
	}

	content := string(data)
	for _, required := range []string{
		"OpenCode CLI",
		"opencode plugin \"@guard22/opencode-multi-auth-codex@${OPENCODE_MULTI_AUTH_CODEX_VERSION}\"",
		"csuite-persona",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("csuite-base Dockerfile must document/install %q for the current persona runtime", required)
		}
	}

	if _, err := os.Stat(filepath.Join(root, "cmd", "csuite-persona", "main.go")); err != nil {
		t.Fatalf("csuite-base stages cmd/csuite-persona, but command source is missing: %v", err)
	}
}

func TestWorkerGoDockerfileExposesGoOnLoginShellPath(t *testing.T) {
	root := repoRoot(t)
	dockerfile := filepath.Join(root, "deploy", "docker", "worker-go.Dockerfile")
	data, err := os.ReadFile(dockerfile)
	if err != nil {
		t.Fatalf("read worker-go Dockerfile: %v", err)
	}

	content := string(data)
	for _, required := range []string{
		"gcc",
		"libc6-dev",
		"ln -sf /usr/local/go/bin/go /usr/local/bin/go",
		"ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("worker-go Dockerfile must expose %q for bash -lc command execution", required)
		}
	}
}
