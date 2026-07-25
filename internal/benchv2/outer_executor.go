package benchv2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	OuterNetworkIsolatedInference = "isolated_inference"
	outerWorkspace                = "/workspace"
	outerUser                     = "65532:65532"
)

var pinnedOCIImage = regexp.MustCompile(`^[A-Za-z0-9._/:@-]+@sha256:[0-9a-f]{64}$`)

type OuterNetworkPolicy struct {
	Mode        string `json:"mode"`
	NetworkName string `json:"network_name"`
}

type OuterExecutionSpec struct {
	Image               string
	HostWorkspace       string
	ContainerWorkspace  string
	ReadPaths           []string
	WritePaths          []string
	Network             OuterNetworkPolicy
	Timeout             time.Duration
	Invocation          CommandInvocation
	CaptureRelativePath string
}

type OuterExecutionResult struct {
	Stdout    []byte
	Stderr    []byte
	ExitCode  int
	StartedAt time.Time
	Duration  time.Duration
	Artifacts map[string][]byte
}

type OuterExecutor interface {
	Execute(context.Context, OuterExecutionSpec) (OuterExecutionResult, error)
}

// DockerOuterExecutor runs only an OCI container assembled by DockerCommand.
// It never executes the harness binary directly on the host.
type DockerOuterExecutor struct {
	Binary string
}

func (executor DockerOuterExecutor) Execute(ctx context.Context, spec OuterExecutionSpec) (OuterExecutionResult, error) {
	args, err := DockerCommand(spec)
	if err != nil {
		return OuterExecutionResult{}, err
	}
	binary := executor.Binary
	if binary == "" {
		binary = "docker"
	}
	runCtx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	started := time.Now().UTC()
	cmd := exec.CommandContext(runCtx, binary, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	result := OuterExecutionResult{
		Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: 0,
		StartedAt: started, Duration: time.Since(started), Artifacts: map[string][]byte{},
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else if runCtx.Err() != nil {
			result.ExitCode = -1
			return result, fmt.Errorf("outer container timed out: %w", runCtx.Err())
		} else {
			result.ExitCode = -1
			return result, fmt.Errorf("start outer container: %w", err)
		}
	}
	if spec.CaptureRelativePath != "" {
		capture, err := scopedWorkspacePath(spec.HostWorkspace, spec.CaptureRelativePath)
		if err != nil {
			return result, err
		}
		raw, err := os.ReadFile(capture)
		if err != nil {
			return result, fmt.Errorf("capture outer artifact: %w", err)
		}
		result.Artifacts[filepath.ToSlash(spec.CaptureRelativePath)] = raw
	}
	return result, nil
}

// DockerCommand returns a hardened `docker run` argv. The only host mount is
// the per-trial temporary worktree, never a home directory, Docker socket, or
// project registry. HostWorkspace is the disposable declared-file projection,
// not the full fixture. The image must be content-addressed.
func DockerCommand(spec OuterExecutionSpec) ([]string, error) {
	if !pinnedOCIImage.MatchString(spec.Image) {
		return nil, fmt.Errorf("outer image must be pinned by sha256 digest")
	}
	workspace, err := filepath.Abs(spec.HostWorkspace)
	if err != nil || workspace == string(filepath.Separator) || workspace == filepath.Dir(workspace) {
		return nil, fmt.Errorf("invalid scoped workspace")
	}
	if spec.ContainerWorkspace != outerWorkspace || spec.Invocation.WorkDir != outerWorkspace {
		return nil, fmt.Errorf("outer workspace must be %s", outerWorkspace)
	}
	if spec.Timeout <= 0 {
		return nil, fmt.Errorf("outer timeout must be positive")
	}
	if spec.Network.Mode != OuterNetworkIsolatedInference || spec.Network.NetworkName == "" ||
		spec.Network.NetworkName == "host" || spec.Network.NetworkName == "bridge" || spec.Network.NetworkName == "default" {
		return nil, fmt.Errorf("external harness requires a named isolated inference network")
	}
	if spec.Invocation.Executable == "" {
		return nil, fmt.Errorf("inner harness executable is empty")
	}
	args := []string{
		"run", "--rm", "--init", "--read-only", "--user", outerUser,
		"--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--pids-limit=256",
		"--network", spec.Network.NetworkName,
		"--mount", fmt.Sprintf("type=bind,src=%s,dst=%s,rw", workspace, outerWorkspace),
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=256m",
		"--tmpfs", "/home/bench:rw,nosuid,nodev,size=64m",
		"--workdir", outerWorkspace,
	}
	env := map[string]string{}
	for key, value := range spec.Invocation.Env {
		env[key] = value
	}
	// Adapter-provided environment cannot override the isolation markers.
	env["HOME"] = "/home/bench"
	env["CANVASBENCH_OUTER_ISOLATION"] = "1"
	env["CANVASBENCH_READ_PATHS"] = strings.Join(spec.ReadPaths, ":")
	env["CANVASBENCH_WRITE_PATHS"] = strings.Join(spec.WritePaths, ":")
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--env", key+"="+env[key])
	}
	args = append(args, spec.Image, spec.Invocation.Executable)
	args = append(args, spec.Invocation.Args...)
	return args, nil
}

func scopedWorkspacePath(workspace, relative string) (string, error) {
	if filepath.IsAbs(relative) || strings.HasPrefix(filepath.Clean(relative), "..") {
		return "", fmt.Errorf("capture path escapes workspace")
	}
	return filepath.Join(workspace, filepath.FromSlash(relative)), nil
}
