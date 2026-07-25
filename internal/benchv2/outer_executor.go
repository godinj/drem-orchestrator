package benchv2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var containerID = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

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

func (executor DockerOuterExecutor) Execute(ctx context.Context, spec OuterExecutionSpec) (result OuterExecutionResult, returnErr error) {
	secrets := sensitiveValues(spec.Invocation.SensitiveEnv)
	defer func() {
		redactOuterResult(&result, secrets)
		returnErr = redactOuterError(returnErr, secrets)
	}()
	if _, err := validateOuterSpec(spec); err != nil {
		return OuterExecutionResult{}, err
	}
	envFile, cleanup, err := writeSensitiveEnvFile(spec.Invocation.SensitiveEnv)
	if err != nil {
		return OuterExecutionResult{}, err
	}
	defer cleanup()
	cidDir, err := os.MkdirTemp("", "canvasbench-container-*")
	if err != nil {
		return OuterExecutionResult{}, fmt.Errorf("create outer container identity directory: %w", err)
	}
	defer os.RemoveAll(cidDir)
	cidFile := filepath.Join(cidDir, "container.cid")
	args, err := dockerCommand(spec, envFile, cidFile)
	if err != nil {
		return OuterExecutionResult{}, err
	}
	binary := executor.Binary
	if binary == "" {
		binary = "docker"
	}
	defer forceRemoveOuterContainer(binary, cidFile)
	runCtx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	started := time.Now().UTC()
	cmd := exec.CommandContext(runCtx, binary, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	result = OuterExecutionResult{
		Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: 0,
		StartedAt: started, Duration: time.Since(started), Artifacts: map[string][]byte{},
	}
	var executionErr error
	if err != nil {
		var exitErr *exec.ExitError
		if runCtx.Err() != nil {
			result.ExitCode = -1
			executionErr = fmt.Errorf("outer container timed out: %w", runCtx.Err())
		} else if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -1
			executionErr = fmt.Errorf("start outer container: %w", err)
		}
	}
	if err := rejectSensitiveWorkspaceOutput(spec.HostWorkspace, secrets); err != nil {
		return result, err
	}
	if executionErr != nil {
		return result, executionErr
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
	if len(spec.Invocation.SensitiveEnv) != 0 {
		return nil, fmt.Errorf("sensitive environment requires executor-managed env file")
	}
	return dockerCommand(spec, "", "")
}

func dockerCommand(spec OuterExecutionSpec, sensitiveEnvFile, cidFile string) ([]string, error) {
	workspace, err := validateOuterSpec(spec)
	if err != nil {
		return nil, err
	}
	if len(spec.Invocation.SensitiveEnv) != 0 && sensitiveEnvFile == "" {
		return nil, fmt.Errorf("sensitive environment file is required")
	}
	if len(spec.Invocation.SensitiveEnv) == 0 && sensitiveEnvFile != "" {
		return nil, fmt.Errorf("unexpected sensitive environment file")
	}
	args := []string{
		"run", "--rm", "--init", "--read-only", "--user", outerUser,
		"--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--pids-limit=256",
		"--network", spec.Network.NetworkName,
		"--mount", fmt.Sprintf("type=bind,src=%s,dst=%s", workspace, outerWorkspace),
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=256m",
		"--tmpfs", "/home/bench:rw,nosuid,nodev,size=64m",
		"--tmpfs", "/root:rw,nosuid,nodev,size=64m,uid=65532,gid=65532,mode=0700",
		"--workdir", outerWorkspace,
	}
	if cidFile != "" {
		args = append(args, "--cidfile", cidFile)
	}
	if sensitiveEnvFile != "" {
		args = append(args, "--env-file", sensitiveEnvFile)
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

func forceRemoveOuterContainer(binary, cidFile string) {
	raw, err := os.ReadFile(cidFile)
	if err != nil {
		return
	}
	id := strings.TrimSpace(string(raw))
	if !containerID.MatchString(id) {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = exec.CommandContext(cleanupCtx, binary, "rm", "-f", id).Run()
}

func validateOuterSpec(spec OuterExecutionSpec) (string, error) {
	if !pinnedOCIImage.MatchString(spec.Image) {
		return "", fmt.Errorf("outer image must be pinned by sha256 digest")
	}
	workspace, err := filepath.Abs(spec.HostWorkspace)
	if err != nil || workspace == string(filepath.Separator) || workspace == filepath.Dir(workspace) {
		return "", fmt.Errorf("invalid scoped workspace")
	}
	if spec.ContainerWorkspace != outerWorkspace || spec.Invocation.WorkDir != outerWorkspace {
		return "", fmt.Errorf("outer workspace must be %s", outerWorkspace)
	}
	if spec.Timeout <= 0 {
		return "", fmt.Errorf("outer timeout must be positive")
	}
	if spec.Network.Mode != OuterNetworkIsolatedInference || spec.Network.NetworkName == "" ||
		spec.Network.NetworkName == "host" || spec.Network.NetworkName == "bridge" || spec.Network.NetworkName == "default" {
		return "", fmt.Errorf("external harness requires a named isolated inference network")
	}
	if spec.Invocation.Executable == "" {
		return "", fmt.Errorf("inner harness executable is empty")
	}
	for key, value := range spec.Invocation.Env {
		if !validEnvironmentEntry(key, value) {
			return "", fmt.Errorf("invalid ordinary environment entry %q", key)
		}
		if key == "OPENAI_API_KEY" {
			return "", fmt.Errorf("OPENAI_API_KEY must use sensitive environment delivery")
		}
		if _, sensitive := spec.Invocation.SensitiveEnv[key]; sensitive {
			return "", fmt.Errorf("environment entry %q is both ordinary and sensitive", key)
		}
	}
	for key, value := range spec.Invocation.SensitiveEnv {
		if !validEnvironmentEntry(key, value) || value == "" {
			return "", fmt.Errorf("invalid sensitive environment entry %q", key)
		}
	}
	for _, secret := range sensitiveValues(spec.Invocation.SensitiveEnv) {
		if strings.Contains(spec.Invocation.Executable, secret) || strings.Contains(spec.Invocation.WorkDir, secret) {
			return "", errors.New("sensitive environment value leaked into ordinary invocation fields")
		}
		for _, argument := range spec.Invocation.Args {
			if strings.Contains(argument, secret) {
				return "", errors.New("sensitive environment value leaked into ordinary invocation fields")
			}
		}
		for _, value := range spec.Invocation.Env {
			if strings.Contains(value, secret) {
				return "", errors.New("sensitive environment value leaked into ordinary invocation fields")
			}
		}
	}
	return workspace, nil
}

func validEnvironmentEntry(key, value string) bool {
	return environmentName.MatchString(key) && !strings.ContainsAny(value, "\r\n\x00")
}

func writeSensitiveEnvFile(environment map[string]string) (string, func(), error) {
	if len(environment) == 0 {
		return "", func() {}, nil
	}
	file, err := os.CreateTemp("", "canvasbench-sensitive-env-*")
	if err != nil {
		return "", nil, fmt.Errorf("create sensitive environment file: %w", err)
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := fmt.Fprintf(file, "%s=%s\n", key, environment[key]); err != nil {
			_ = file.Close()
			cleanup()
			return "", nil, fmt.Errorf("write sensitive environment entry %q: %w", key, err)
		}
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("close sensitive environment file: %w", err)
	}
	return path, cleanup, nil
}

func sensitiveValues(environment map[string]string) []string {
	values := make([]string, 0, len(environment))
	for _, value := range environment {
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}

func redactOuterResult(result *OuterExecutionResult, secrets []string) {
	result.Stdout = redactSensitiveBytes(result.Stdout, secrets)
	result.Stderr = redactSensitiveBytes(result.Stderr, secrets)
	for path, raw := range result.Artifacts {
		result.Artifacts[path] = redactSensitiveBytes(raw, secrets)
	}
}

func redactSensitiveBytes(raw []byte, secrets []string) []byte {
	redacted := append([]byte(nil), raw...)
	for _, secret := range secrets {
		redacted = bytes.ReplaceAll(redacted, []byte(secret), []byte("[REDACTED]"))
	}
	return redacted
}

func redactOuterError(err error, secrets []string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	for _, secret := range secrets {
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
	}
	return errors.New(message)
}

func rejectSensitiveWorkspaceOutput(workspace string, secrets []string) error {
	if len(secrets) == 0 {
		return nil
	}
	leaked := false
	err := filepath.WalkDir(workspace, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		contains, err := fileContainsSensitiveValue(path, secrets)
		if err != nil {
			return err
		}
		if contains {
			leaked = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("scan scoped workspace for sensitive output: %w", err)
	}
	if leaked {
		return errors.New("outer harness wrote sensitive credential bytes into scoped workspace")
	}
	return nil
}

func fileContainsSensitiveValue(path string, secrets []string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	longest := 0
	for _, secret := range secrets {
		if len(secret) > longest {
			longest = len(secret)
		}
	}
	buffer := make([]byte, 64*1024)
	var tail []byte
	for {
		count, readErr := file.Read(buffer)
		window := append(append([]byte(nil), tail...), buffer[:count]...)
		for _, secret := range secrets {
			if bytes.Contains(window, []byte(secret)) {
				return true, nil
			}
		}
		keep := longest - 1
		if keep > len(window) {
			keep = len(window)
		}
		tail = append(tail[:0], window[len(window)-keep:]...)
		if errors.Is(readErr, io.EOF) {
			return false, nil
		}
		if readErr != nil {
			return false, readErr
		}
	}
}

func scopedWorkspacePath(workspace, relative string) (string, error) {
	if filepath.IsAbs(relative) || strings.HasPrefix(filepath.Clean(relative), "..") {
		return "", fmt.Errorf("capture path escapes workspace")
	}
	return filepath.Join(workspace, filepath.FromSlash(relative)), nil
}
