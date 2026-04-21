package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// readBareRepoConfig returns the value of the given git config key on
// the bare repo at barePath, or "" when unset. Test-only helper.
func readBareRepoConfig(t *testing.T, barePath, key string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir="+barePath, "config", "--get", key)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return ""
		}
		t.Fatalf("git config --get %s failed: %v", key, err)
	}
	return strings.TrimSpace(string(out))
}

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

// registerForUpdate is a helper: fresh-register a project under
// homeDir so subsequent --update tests have a registry entry + an
// on-disk compose.yml + drem.toml to read.
func registerForUpdate(t *testing.T, homeDir string) string {
	t.Helper()
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
	return bareRepo
}

// readFile is a tiny helper that asserts-on-error for fixture reads.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}

// TestProjectUpdate_PreservesSharedToken asserts that running
// --update preserves the SharedToken present in the on-disk
// compose.yml. Regenerating with a fresh token would break
// orch/agentmon auth mid-operation — see plans/drem-project-register-update.md §3.
func TestProjectUpdate_PreservesSharedToken(t *testing.T) {
	homeDir := t.TempDir()
	registerForUpdate(t, homeDir)
	composePath := filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator", "compose.yml")

	// Capture the fresh-registered token.
	before := string(readFile(t, composePath))
	require.Contains(t, before, "DREM_AGENTMON_TOKEN")

	// Run --update with --force (so drift, if any, doesn't block).
	var out bytes.Buffer
	err := dispatchProject([]string{
		"register", "--update", "drem-orchestrator",
		"--force", "--home-dir", homeDir,
	}, &out, io.Discard)
	require.NoError(t, err)

	after := string(readFile(t, composePath))
	// The token must appear unchanged — extract both copies via a
	// simple substring search and compare.
	require.Contains(t, after, "DREM_AGENTMON_TOKEN")
	// Byte-identical check: the extracted DREM_AGENTMON_TOKEN line
	// must match between before and after.
	beforeLine := findLine(before, "DREM_AGENTMON_TOKEN")
	afterLine := findLine(after, "DREM_AGENTMON_TOKEN")
	require.Equal(t, beforeLine, afterLine, "SharedToken must be byte-identical pre/post --update")
	require.Contains(t, out.String(), "SharedToken preserved")
}

// findLine returns the first line of content that includes needle.
// Strips leading/trailing whitespace for stable comparison.
func findLine(content, needle string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, needle) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// TestProjectUpdate_DryRunDoesNotWrite asserts --dry-run never
// modifies the on-disk files, even when it would otherwise regenerate.
func TestProjectUpdate_DryRunDoesNotWrite(t *testing.T) {
	homeDir := t.TempDir()
	registerForUpdate(t, homeDir)
	composePath := filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator", "compose.yml")
	tomlPath := filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator", "drem.toml")

	composeBefore := readFile(t, composePath)
	tomlBefore := readFile(t, tomlPath)

	var out bytes.Buffer
	err := dispatchProject([]string{
		"register", "--update", "drem-orchestrator",
		"--dry-run", "--home-dir", homeDir,
	}, &out, io.Discard)
	require.NoError(t, err)
	require.Contains(t, out.String(), "no files written")

	require.Equal(t, composeBefore, readFile(t, composePath), "compose must be unchanged after --dry-run")
	require.Equal(t, tomlBefore, readFile(t, tomlPath), "drem.toml must be unchanged after --dry-run")
}

// TestProjectUpdate_FailsWhenTokenMissing asserts that --update
// against a compose.yml stripped of DREM_AGENTMON_TOKEN fails loudly
// rather than silently regenerating a new token. Operators must
// explicitly opt into rotation via --regenerate-token.
func TestProjectUpdate_FailsWhenTokenMissing(t *testing.T) {
	homeDir := t.TempDir()
	registerForUpdate(t, homeDir)
	composePath := filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator", "compose.yml")

	// Strip DREM_AGENTMON_TOKEN from the on-disk compose.
	contents := string(readFile(t, composePath))
	var filtered []string
	for _, line := range strings.Split(contents, "\n") {
		if !strings.Contains(line, "DREM_AGENTMON_TOKEN") {
			filtered = append(filtered, line)
		}
	}
	require.NoError(t, os.WriteFile(composePath, []byte(strings.Join(filtered, "\n")), 0o644))

	err := dispatchProject([]string{
		"register", "--update", "drem-orchestrator",
		"--home-dir", homeDir,
	}, io.Discard, io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--regenerate-token")
}

// TestProjectUpdate_RegenerateTokenRotates asserts that passing
// --regenerate-token intentionally rewrites the token, producing a
// different DREM_AGENTMON_TOKEN value in the new compose.yml.
func TestProjectUpdate_RegenerateTokenRotates(t *testing.T) {
	homeDir := t.TempDir()
	registerForUpdate(t, homeDir)
	composePath := filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator", "compose.yml")

	before := findLine(string(readFile(t, composePath)), "DREM_AGENTMON_TOKEN")

	var out bytes.Buffer
	err := dispatchProject([]string{
		"register", "--update", "drem-orchestrator",
		"--regenerate-token", "--force", "--home-dir", homeDir,
	}, &out, io.Discard)
	require.NoError(t, err)

	after := findLine(string(readFile(t, composePath)), "DREM_AGENTMON_TOKEN")
	require.NotEqual(t, before, after, "--regenerate-token must produce a new token value")
	require.Contains(t, out.String(), "SharedToken rotated")
}

// TestProjectUpdate_ForceOverwritesDrift asserts that a hand-patched
// env var on the on-disk compose gets overwritten when --force is
// passed. Default (no --force) would warn and exit 0 without writing;
// this test covers the explicit-acknowledgement path.
func TestProjectUpdate_ForceOverwritesDrift(t *testing.T) {
	homeDir := t.TempDir()
	registerForUpdate(t, homeDir)
	composePath := filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator", "compose.yml")

	// Hand-patch: add a GIT_CONFIG_COUNT env var that the template does
	// NOT emit (matches the current drem-orchestrator on-disk compose
	// situation per plans/drem-project-register-update.md §9).
	contents := readFile(t, composePath)
	patched := strings.Replace(
		string(contents),
		`DREM_AGENTMON_TOKEN:`,
		"GIT_CONFIG_COUNT: \"1\"\n      DREM_AGENTMON_TOKEN:",
		1)
	require.NoError(t, os.WriteFile(composePath, []byte(patched), 0o644))

	var out bytes.Buffer
	err := dispatchProject([]string{
		"register", "--update", "drem-orchestrator",
		"--force", "--home-dir", homeDir,
	}, &out, io.Discard)
	require.NoError(t, err)

	require.Contains(t, out.String(), "drift detected")
	require.Contains(t, out.String(), "GIT_CONFIG_COUNT")
	require.NotContains(t, string(readFile(t, composePath)), "GIT_CONFIG_COUNT",
		"--force must remove the hand-patched env var")
}

// TestProjectUpdate_FailOnDriftErrors asserts that --fail-on-drift
// turns a drift warning into a non-zero exit code. Useful for CI
// that wants to block merges that would drift the on-disk compose.
func TestProjectUpdate_FailOnDriftErrors(t *testing.T) {
	homeDir := t.TempDir()
	registerForUpdate(t, homeDir)
	composePath := filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator", "compose.yml")

	// Hand-patch to introduce drift.
	contents := readFile(t, composePath)
	patched := strings.Replace(
		string(contents),
		`DREM_AGENTMON_TOKEN:`,
		"GIT_CONFIG_COUNT: \"1\"\n      DREM_AGENTMON_TOKEN:",
		1)
	require.NoError(t, os.WriteFile(composePath, []byte(patched), 0o644))

	err := dispatchProject([]string{
		"register", "--update", "drem-orchestrator",
		"--fail-on-drift", "--home-dir", homeDir,
	}, io.Discard, io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "drift detected")

	// Files must not be written in the error path.
	require.Contains(t, string(readFile(t, composePath)), "GIT_CONFIG_COUNT",
		"--fail-on-drift must NOT write anything")
}

// TestProjectUpdate_IsIdempotent asserts that running --update twice
// in a row produces byte-identical compose.yml + drem.toml on the
// second run. The running-services-keep-their-state promise of the
// plan.
func TestProjectUpdate_IsIdempotent(t *testing.T) {
	homeDir := t.TempDir()
	registerForUpdate(t, homeDir)
	composePath := filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator", "compose.yml")
	tomlPath := filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator", "drem.toml")

	// First --update: rewrites the files (even if no drift, we still
	// overwrite with byte-identical content).
	err := dispatchProject([]string{
		"register", "--update", "drem-orchestrator",
		"--force", "--home-dir", homeDir,
	}, io.Discard, io.Discard)
	require.NoError(t, err)

	composeAfter1 := readFile(t, composePath)
	tomlAfter1 := readFile(t, tomlPath)

	// Second --update: must produce identical output.
	err = dispatchProject([]string{
		"register", "--update", "drem-orchestrator",
		"--force", "--home-dir", homeDir,
	}, io.Discard, io.Discard)
	require.NoError(t, err)

	require.Equal(t, composeAfter1, readFile(t, composePath), "second --update must produce byte-identical compose")
	require.Equal(t, tomlAfter1, readFile(t, tomlPath), "second --update must produce byte-identical drem.toml")
}

// TestProjectUpdate_LeavesComposeOverrideAlone asserts that --update
// never touches compose.override.yml or operator-owned sidecar files
// in the per-project directory. The update path scopes strictly to
// compose.yml + drem.toml.
func TestProjectUpdate_LeavesComposeOverrideAlone(t *testing.T) {
	homeDir := t.TempDir()
	registerForUpdate(t, homeDir)
	projectRoot := filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator")

	// Drop sentinel files in the project dir.
	const sentinelOverride = "services:\n  orch:\n    volumes:\n      - /operator-owned:/path:ro\n"
	const sentinelScript = "#!/bin/bash\necho operator-owned\n"
	overridePath := filepath.Join(projectRoot, "compose.override.yml")
	scriptPath := filepath.Join(projectRoot, "csuite-run.sh")
	require.NoError(t, os.WriteFile(overridePath, []byte(sentinelOverride), 0o644))
	require.NoError(t, os.WriteFile(scriptPath, []byte(sentinelScript), 0o755))

	err := dispatchProject([]string{
		"register", "--update", "drem-orchestrator",
		"--force", "--home-dir", homeDir,
	}, io.Discard, io.Discard)
	require.NoError(t, err)

	require.Equal(t, []byte(sentinelOverride), readFile(t, overridePath),
		"compose.override.yml must be byte-identical after --update")
	require.Equal(t, []byte(sentinelScript), readFile(t, scriptPath),
		"csuite-run.sh must be byte-identical after --update")
}

// TestProjectUpdate_NotRegisteredErrors asserts that --update against
// a project name not in the registry fails with a clear error.
func TestProjectUpdate_NotRegisteredErrors(t *testing.T) {
	homeDir := t.TempDir()
	err := dispatchProject([]string{
		"register", "--update", "nonexistent-project",
		"--home-dir", homeDir,
	}, io.Discard, io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found in registry")
}

// TestProjectUpdate_RejectsUpdateFlagsOnFreshRegister is a guard —
// passing --force without --update (or similar) should not silently
// invoke the update path when the user actually wanted to fresh-
// register. The flag parser routes on any update-flag presence to
// cmdProjectRegisterUpdate, which then fails loudly because the
// project isn't registered yet.
func TestProjectUpdate_RejectsUpdateFlagsOnFreshRegister(t *testing.T) {
	homeDir := t.TempDir()
	bareRepo := testutil.SetupBareRepo(t)
	err := dispatchProject([]string{
		"register",
		"--name", "new-project",
		"--bare", bareRepo,
		"--language", "go",
		"--orch-url", "http://localhost:8080",
		"--force", // should NOT be used on fresh register
		"--home-dir", homeDir,
	}, io.Discard, io.Discard)
	// Expected: error because --force routes to update path and
	// new-project is not in the registry yet.
	require.Error(t, err)
}

// TestProjectRegister_SetsBareRepoDenyCurrentBranch asserts that a
// fresh `drem project register` configures
// receive.denyCurrentBranch=ignore on the target bare repo. The
// setting lets the worker watchdog's final push succeed against a
// shared-workspace bare repo (host worktrees checked out under the
// bare). See plans/bare-repo-denyCurrentBranch.md.
func TestProjectRegister_SetsBareRepoDenyCurrentBranch(t *testing.T) {
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

	require.Equal(t, "ignore",
		readBareRepoConfig(t, bareRepo, "receive.denyCurrentBranch"))
}

// TestProjectRegisterUpdate_IsIdempotentOnBareRepoConfig asserts that
// the `--update` path also applies the bare-repo config (idempotent
// reapplication is safe and keeps migrators from old installs
// auto-covered when they regenerate alongside template drift).
func TestProjectRegisterUpdate_IsIdempotentOnBareRepoConfig(t *testing.T) {
	homeDir := t.TempDir()
	bareRepo := registerForUpdate(t, homeDir)

	// Fresh register already set the value — confirm baseline.
	require.Equal(t, "ignore",
		readBareRepoConfig(t, bareRepo, "receive.denyCurrentBranch"))

	// --update should reapply cleanly without error.
	err := dispatchProject([]string{
		"register", "--update", "drem-orchestrator",
		"--force", "--home-dir", homeDir,
	}, io.Discard, io.Discard)
	require.NoError(t, err)

	require.Equal(t, "ignore",
		readBareRepoConfig(t, bareRepo, "receive.denyCurrentBranch"))
}
