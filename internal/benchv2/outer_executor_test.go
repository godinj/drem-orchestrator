package benchv2

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testOuterImage = "ghcr.io/godinj/canvasbench-opencode@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func validOuterSpec(workspace string) OuterExecutionSpec {
	return OuterExecutionSpec{
		Image: testOuterImage, HostWorkspace: workspace, ContainerWorkspace: outerWorkspace,
		ReadPaths: []string{"read.cpp"}, WritePaths: []string{"write.cpp"},
		Network:    OuterNetworkPolicy{Mode: OuterNetworkIsolatedInference, NetworkName: "canvasbench-inference"},
		Timeout:    5 * time.Minute,
		Invocation: CommandInvocation{Executable: "opencode", Args: []string{"run", "task"}, WorkDir: outerWorkspace},
	}
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
	require.Contains(t, joined, "src="+workspace+",dst=/workspace,rw")
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
