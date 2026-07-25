package benchv2

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testOuterImage = "ghcr.io/godinj/canvasbench-opencode@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testUsageProxyImage = "ghcr.io/godinj/canvasbench-usage-proxy@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func validOuterSpec(workspace string) OuterExecutionSpec {
	return OuterExecutionSpec{
		Image: testOuterImage, HostWorkspace: workspace, ContainerWorkspace: outerWorkspace,
		ReadPaths: []string{"read.cpp"}, WritePaths: []string{"write.cpp"},
		Network:    OuterNetworkPolicy{Mode: OuterNetworkIsolatedInference, NetworkName: "canvasbench-inference"},
		Timeout:    5 * time.Minute,
		Invocation: CommandInvocation{Executable: "opencode", Args: []string{"run", "task"}, WorkDir: outerWorkspace},
	}
}

func TestDockerOuterExecutorKeepsSensitiveEnvironmentOutOfArgvAndCleansFile(t *testing.T) {
	workspace := t.TempDir()
	spec := validOuterSpec(workspace)
	const secret = "unguessable-trial-secret"
	spec.Invocation.Env = map[string]string{"OPENAI_BASE_URL": "http://usage-proxy/v1"}
	spec.Invocation.SensitiveEnv = map[string]string{"OPENAI_API_KEY": secret}
	_, err := DockerCommand(spec)
	require.ErrorContains(t, err, "executor-managed env file")
	require.NotContains(t, err.Error(), secret)

	toolDir := t.TempDir()
	tool := filepath.Join(toolDir, "fake-docker")
	argsCapture := filepath.Join(toolDir, "args")
	envCapture := filepath.Join(toolDir, "env")
	require.NoError(t, os.WriteFile(tool, []byte(`#!/bin/sh
printf '%s\n' "$@" > "$CANVASBENCH_TEST_ARGS"
previous=""
env_file=""
for argument in "$@"; do
  if [ "$previous" = "--env-file" ]; then env_file="$argument"; fi
  previous="$argument"
done
test -n "$env_file" || exit 91
cp "$env_file" "$CANVASBENCH_TEST_ENV"
if stat -c '%a' "$env_file" > "$CANVASBENCH_TEST_ENV.mode" 2>/dev/null; then :; else stat -f '%Lp' "$env_file" > "$CANVASBENCH_TEST_ENV.mode"; fi
printf 'stdout:%s\n' "$(cat "$env_file")"
printf 'stderr:%s\n' "$(cat "$env_file")" >&2
exit 7
`), 0o700))
	t.Setenv("CANVASBENCH_TEST_ARGS", argsCapture)
	t.Setenv("CANVASBENCH_TEST_ENV", envCapture)
	result, err := (DockerOuterExecutor{Binary: tool}).Execute(context.Background(), spec)
	require.NoError(t, err)
	require.Equal(t, 7, result.ExitCode)
	argsRaw, err := os.ReadFile(argsCapture)
	require.NoError(t, err)
	require.NotContains(t, string(argsRaw), secret)
	require.Contains(t, string(argsRaw), "--env-file")
	envRaw, err := os.ReadFile(envCapture)
	require.NoError(t, err)
	require.Equal(t, "OPENAI_API_KEY="+secret+"\n", string(envRaw))
	modeRaw, err := os.ReadFile(envCapture + ".mode")
	require.NoError(t, err)
	require.Equal(t, "600", strings.TrimSpace(string(modeRaw)))
	require.NotContains(t, string(result.Stdout), secret)
	require.NotContains(t, string(result.Stderr), secret)
	require.Contains(t, string(result.Stdout), "[REDACTED]")
	lines := strings.Split(strings.TrimSpace(string(argsRaw)), "\n")
	for index, line := range lines {
		if line == "--env-file" && index+1 < len(lines) {
			_, statErr := os.Stat(lines[index+1])
			require.ErrorIs(t, statErr, os.ErrNotExist)
		}
	}
}

func TestDockerOuterExecutorCleansSensitiveFileWhenProcessCannotStart(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	spec := validOuterSpec(t.TempDir())
	const secret = "start-error-secret"
	spec.Invocation.SensitiveEnv = map[string]string{"OPENAI_API_KEY": secret}
	result, err := (DockerOuterExecutor{Binary: filepath.Join(t.TempDir(), "missing-docker")}).Execute(context.Background(), spec)
	require.Error(t, err)
	require.NotContains(t, err.Error(), secret)
	require.NotContains(t, string(result.Stdout), secret)
	entries, readErr := os.ReadDir(tempRoot)
	require.NoError(t, readErr)
	require.Empty(t, entries)
}

func TestDockerOuterExecutorRejectsSensitiveBytesWrittenIntoScopedWorkspace(t *testing.T) {
	workspace := t.TempDir()
	spec := validOuterSpec(workspace)
	const secret = "workspace-leak-secret"
	spec.Invocation.SensitiveEnv = map[string]string{"OPENAI_API_KEY": secret}
	tool := filepath.Join(t.TempDir(), "fake-docker")
	require.NoError(t, os.WriteFile(tool, []byte(`#!/bin/sh
previous=""
env_file=""
for argument in "$@"; do
  if [ "$previous" = "--env-file" ]; then env_file="$argument"; fi
  previous="$argument"
done
cp "$env_file" "$CANVASBENCH_TEST_WORKSPACE/write.cpp"
printf 'worker completed\n'
`), 0o700))
	t.Setenv("CANVASBENCH_TEST_WORKSPACE", workspace)
	result, err := (DockerOuterExecutor{Binary: tool}).Execute(context.Background(), spec)
	require.ErrorContains(t, err, "wrote sensitive credential bytes")
	require.NotContains(t, err.Error(), secret)
	require.NotContains(t, string(result.Stdout), secret)
}

func TestSensitiveWorkspaceScanFindsCredentialAcrossReadBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.bin")
	const secret = "cross-boundary-secret"
	raw := append([]byte(strings.Repeat("x", 64*1024-len(secret)/2)), []byte(secret)...)
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	contains, err := fileContainsSensitiveValue(path, []string{secret})
	require.NoError(t, err)
	require.True(t, contains)
}

func TestDockerCommandIsPinnedNonRootAndNarrowlyMounted(t *testing.T) {
	workspace := t.TempDir()
	args, err := DockerCommand(validOuterSpec(workspace))
	require.NoError(t, err)
	joined := strings.Join(args, " ")
	require.Contains(t, joined, "--read-only")
	require.Contains(t, joined, "--user "+outerUser)
	require.Contains(t, joined, "--cap-drop=ALL")
	require.Contains(t, joined, "no-new-privileges:true")
	require.Contains(t, joined, "--network canvasbench-inference")
	require.Contains(t, joined, "src="+workspace+",dst=/workspace")
	require.NotContains(t, joined, "dst=/workspace,rw")
	require.Contains(t, joined, testOuterImage+" opencode run task")
	for _, forbidden := range []string{"--privileged", "/var/run/docker.sock", "--network host", "src=/,", "src=/Users/", "--user 0"} {
		require.NotContains(t, joined, forbidden)
	}
}

func TestDockerCommandRejectsUnpinnedImageAndBroadNetwork(t *testing.T) {
	spec := validOuterSpec(t.TempDir())
	spec.Image = "ghcr.io/godinj/canvasbench:latest"
	_, err := DockerCommand(spec)
	require.ErrorContains(t, err, "pinned")
	spec = validOuterSpec(t.TempDir())
	spec.Network.NetworkName = "host"
	_, err = DockerCommand(spec)
	require.ErrorContains(t, err, "isolated")
}

func TestDockerCommandProtectsIsolationEnvironment(t *testing.T) {
	spec := validOuterSpec(t.TempDir())
	spec.Invocation.Env = map[string]string{
		"HOME": "/host/home", "CANVASBENCH_OUTER_ISOLATION": "0",
		"CANVASBENCH_READ_PATHS": "secret", "SAFE": "value",
	}
	args, err := DockerCommand(spec)
	require.NoError(t, err)
	joined := strings.Join(args, " ")
	require.Contains(t, joined, "HOME=/home/bench")
	require.Contains(t, joined, "CANVASBENCH_OUTER_ISOLATION=1")
	require.Contains(t, joined, "CANVASBENCH_READ_PATHS=read.cpp")
	require.Contains(t, joined, "SAFE=value")
	require.NotContains(t, joined, "/host/home")
}

func TestDockerCommandRejectsSensitiveValueInOrdinaryInvocationFields(t *testing.T) {
	spec := validOuterSpec(t.TempDir())
	const secret = "argv-leak-secret"
	spec.Invocation.SensitiveEnv = map[string]string{"OPENAI_API_KEY": secret}
	spec.Invocation.Args = append(spec.Invocation.Args, "--token="+secret)
	_, err := dockerCommand(spec, filepath.Join(t.TempDir(), "env-file"), "")
	require.ErrorContains(t, err, "leaked into ordinary invocation")
	require.NotContains(t, err.Error(), secret)

	spec = validOuterSpec(t.TempDir())
	spec.Invocation.Env = map[string]string{"OPENAI_API_KEY": secret}
	_, err = DockerCommand(spec)
	require.ErrorContains(t, err, "must use sensitive environment")
	require.NotContains(t, err.Error(), secret)
}

func TestDockerOuterExecutorForceRemovesTimedOutContainer(t *testing.T) {
	toolDir := t.TempDir()
	tool := filepath.Join(toolDir, "fake-docker")
	cleanupCapture := filepath.Join(toolDir, "cleanup")
	require.NoError(t, os.WriteFile(tool, []byte(`#!/bin/sh
if [ "$1" = "rm" ]; then
  printf '%s\n' "$@" > "$CANVASBENCH_TEST_CLEANUP"
  exit 0
fi
previous=""
cid_file=""
for argument in "$@"; do
  if [ "$previous" = "--cidfile" ]; then cid_file="$argument"; fi
  previous="$argument"
done
test -n "$cid_file" || exit 91
printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n' > "$cid_file"
exec sleep 30
`), 0o700))
	t.Setenv("CANVASBENCH_TEST_CLEANUP", cleanupCapture)
	spec := validOuterSpec(t.TempDir())
	spec.Timeout = 300 * time.Millisecond
	_, err := (DockerOuterExecutor{Binary: tool}).Execute(context.Background(), spec)
	require.ErrorContains(t, err, "outer container timed out")
	cleanup, readErr := os.ReadFile(cleanupCapture)
	require.NoError(t, readErr)
	require.Equal(t, "rm\n-f\n"+strings.Repeat("a", 64)+"\n", string(cleanup))
}
