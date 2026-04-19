package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// TestProjectRegisterAndList exercises the full register → list flow with
// a --home-dir override so we never touch the real $HOME.
func TestProjectRegisterAndList(t *testing.T) {
	homeDir := t.TempDir()
	bareRepo := testutil.SetupBareRepo(t)

	var stdout bytes.Buffer
	err := dispatchProject([]string{
		"register",
		"--name", "drem-orchestrator",
		"--bare", bareRepo,
		"--language", "go",
		"--orch-url", "http://localhost:8080",
		"--home-dir", homeDir,
	}, &stdout, io.Discard)
	require.NoError(t, err)
	require.Contains(t, stdout.String(), "registered \"drem-orchestrator\"")

	// The compose file should exist at the expected location.
	expectedPath := filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator", "compose.yml")
	_, err = os.Stat(expectedPath)
	require.NoError(t, err)

	// The registry file should also exist.
	registryPath := filepath.Join(homeDir, ".drem", "projects.toml")
	_, err = os.Stat(registryPath)
	require.NoError(t, err)

	// `list` should include the project in its output.
	var listOut bytes.Buffer
	err = dispatchProject([]string{"list", "--home-dir", homeDir}, &listOut, io.Discard)
	require.NoError(t, err)
	out := listOut.String()
	require.Contains(t, out, "drem-orchestrator")
	require.Contains(t, out, "go")
	require.Contains(t, out, "http://localhost:8080")
}

// TestProjectShow verifies the `show` subcommand after registration.
func TestProjectShow(t *testing.T) {
	homeDir := t.TempDir()
	bareRepo := testutil.SetupBareRepo(t)

	err := dispatchProject([]string{
		"register",
		"--name", "drem-canvas",
		"--bare", bareRepo,
		"--language", "cpp",
		"--orch-url", "http://localhost:8081",
		"--home-dir", homeDir,
	}, io.Discard, io.Discard)
	require.NoError(t, err)

	var out bytes.Buffer
	err = dispatchProject([]string{"show", "drem-canvas", "--home-dir", homeDir}, &out, io.Discard)
	require.NoError(t, err)
	s := out.String()
	require.Contains(t, s, "drem-canvas")
	require.Contains(t, s, "cpp")
	require.Contains(t, s, bareRepo)
	require.Contains(t, s, "http://localhost:8081")
	require.Contains(t, s, "compose.yml")
}

// TestProjectRemove verifies the `remove` subcommand clears the registry
// entry and deletes the compose file.
func TestProjectRemove(t *testing.T) {
	homeDir := t.TempDir()
	bareRepo := testutil.SetupBareRepo(t)

	err := dispatchProject([]string{
		"register",
		"--name", "drem-orchestrator",
		"--bare", bareRepo,
		"--language", "go",
		"--orch-url", "http://localhost:8080",
		"--home-dir", homeDir,
	}, io.Discard, io.Discard)
	require.NoError(t, err)

	composePath := filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator", "compose.yml")
	_, err = os.Stat(composePath)
	require.NoError(t, err)

	var out bytes.Buffer
	err = dispatchProject([]string{"remove", "drem-orchestrator", "--home-dir", homeDir}, &out, io.Discard)
	require.NoError(t, err)
	require.Contains(t, out.String(), "removed")

	_, err = os.Stat(composePath)
	require.True(t, os.IsNotExist(err))

	// `list` after removal should not contain the project.
	var listOut bytes.Buffer
	err = dispatchProject([]string{"list", "--home-dir", homeDir}, &listOut, io.Discard)
	require.NoError(t, err)
	require.False(t, strings.Contains(listOut.String(), "drem-orchestrator"))
}

// TestProjectRegister_RequiresAllFlags ensures we fail loudly on missing
// required flags.
func TestProjectRegister_RequiresAllFlags(t *testing.T) {
	homeDir := t.TempDir()
	err := dispatchProject([]string{
		"register",
		"--name", "x",
		"--home-dir", homeDir,
	}, io.Discard, io.Discard)
	require.Error(t, err)
}

// TestProjectUnknownSubcommand routes to a clean error.
func TestProjectUnknownSubcommand(t *testing.T) {
	err := dispatchProject([]string{"wat"}, io.Discard, io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown")
}
