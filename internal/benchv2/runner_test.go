package benchv2

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type harnessAdapterFunc func(context.Context, TrialRequest) (HarnessRun, error)

func (harnessAdapterFunc) Name() string { return "test" }

func (fn harnessAdapterFunc) Run(ctx context.Context, request TrialRequest) (HarnessRun, error) {
	return fn(ctx, request)
}

type hostVerifierFunc func(context.Context, TaskSpec, string, HarnessRun) VerifyOutcome

func (fn hostVerifierFunc) Verify(ctx context.Context, task TaskSpec, workDir string, run HarnessRun) VerifyOutcome {
	return fn(ctx, task, workDir, run)
}

func TestRunnerRequiresAndRecordsReleaseArtifactEvidence(t *testing.T) {
	repo := t.TempDir()
	runGit := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, string(output))
		return strings.TrimSpace(string(output))
	}
	runGit("init", "-b", "main")
	runGit("config", "user.email", "bench@example.com")
	runGit("config", "user.name", "Bench")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "changed.txt"), []byte("before\n"), 0o644))
	runGit("add", "changed.txt")
	runGit("commit", "-m", "fixture")

	task := TaskSpec{
		ID: "release", Status: "runnable", InferencePolicy: "required",
		Fixture:    Fixture{RepoID: "fixture", BaseCommit: runGit("rev-parse", "HEAD"), VisibleBlobs: []BlobPin{{Path: "changed.txt", SHA: runGit("rev-parse", "HEAD:changed.txt")}}},
		WritePaths: []string{"changed.txt"}, RequiredChangedPaths: []string{"changed.txt"}, RequiredMutation: true,
		ReleaseArtifactPath: "build/DremCanvas", Budget: Budget{MaxInputTokens: 100, MaxOutputTokens: 100, TimeoutSeconds: 30},
	}
	matrix := validMatrix()
	adapter := harnessAdapterFunc(func(_ context.Context, request TrialRequest) (HarnessRun, error) {
		require.NoError(t, os.WriteFile(filepath.Join(request.WorkDir, "changed.txt"), []byte("after\n"), 0o644))
		return HarnessRun{
			Telemetry:   Telemetry{TokensIn: 10, TokensOut: 5, PeakRequestInput: 10, MutationObserved: true},
			ServerUsage: ServerUsage{Source: "server_response", RequestsMeasured: 1, RequestsTotal: 1, Complete: true},
		}, nil
	})
	evidence := &ArtifactEvidence{Kind: "release_binary", Path: "build/DremCanvas", SHA256: strings.Repeat("a", 64), SizeBytes: 42}
	runner := Runner{
		Repo: repo, ScratchRoot: t.TempDir(), Adapter: adapter,
		Verifier: hostVerifierFunc(func(context.Context, TaskSpec, string, HarnessRun) VerifyOutcome {
			return VerifyOutcome{Passed: true, Compiled: true, ReleaseArtifact: evidence}
		}),
	}
	result := runner.RunTrial(context.Background(), matrix, task, 1)
	require.Equal(t, "passed", result.Status, result.Gates.Failures)
	require.True(t, result.Gates.ArtifactAttested)
	require.Equal(t, evidence, result.ReleaseArtifact)

	runner.Verifier = hostVerifierFunc(func(context.Context, TaskSpec, string, HarnessRun) VerifyOutcome {
		return VerifyOutcome{Passed: true, Compiled: true}
	})
	result = runner.RunTrial(context.Background(), matrix, task, 1)
	require.Equal(t, "failed", result.Status)
	require.False(t, result.Gates.ArtifactAttested)
	require.Contains(t, result.Gates.Failures, "host verifier did not attest the required Release artifact")
}
