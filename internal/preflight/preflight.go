package preflight

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Result is one runtime preflight check outcome.
type Result struct {
	Name   string
	OK     bool
	Detail string
}

// Options controls which environment assumptions are checked.
type Options struct {
	// WorkDir is optional. When set, Run verifies that the directory is usable
	// for temporary files before future test-gate code writes into it.
	WorkDir string
}

type commandFunc func(context.Context, string, ...string) ([]byte, error)
type lookPathFunc func(string) (string, error)

// Run executes the orchestrator/test-gate runtime preflight checks. It only
// reports structured results; callers decide whether failures are fatal.
func Run(ctx context.Context, opts Options) []Result {
	return run(ctx, opts, exec.LookPath, runCommand)
}

func run(ctx context.Context, opts Options, lookPath lookPathFunc, command commandFunc) []Result {
	results := []Result{
		checkExecutable(lookPath, "go"),
		checkExecutable(lookPath, "git"),
		checkGitConfig(ctx, command, opts.WorkDir, "user.name"),
		checkGitConfig(ctx, command, opts.WorkDir, "user.email"),
		checkGoTestCGO(ctx, command),
	}
	if opts.WorkDir != "" {
		results = append(results, CheckWorkDirWritable(opts.WorkDir))
	}
	return results
}

// CheckWorkDirWritable verifies that path is a directory where a temporary file
// can be created, written, and removed.
func CheckWorkDirWritable(path string) Result {
	name := "workdir writable"
	info, err := os.Stat(path)
	if err != nil {
		return Result{Name: name, OK: false, Detail: err.Error()}
	}
	if !info.IsDir() {
		return Result{Name: name, OK: false, Detail: fmt.Sprintf("%q is not a directory", path)}
	}

	f, err := os.CreateTemp(path, ".drem-preflight-*")
	if err != nil {
		return Result{Name: name, OK: false, Detail: err.Error()}
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.WriteString("ok\n"); err != nil {
		f.Close()
		return Result{Name: name, OK: false, Detail: err.Error()}
	}
	if err := f.Close(); err != nil {
		return Result{Name: name, OK: false, Detail: err.Error()}
	}
	return Result{Name: name, OK: true, Detail: path}
}

func checkExecutable(lookPath lookPathFunc, exe string) Result {
	path, err := lookPath(exe)
	if err != nil {
		return Result{Name: exe + " executable", OK: false, Detail: err.Error()}
	}
	return Result{Name: exe + " executable", OK: true, Detail: path}
}

func checkGitConfig(ctx context.Context, command commandFunc, workDir, key string) Result {
	args := []string{"config", "--get", key}
	if workDir != "" {
		args = append([]string{"-C", workDir}, args...)
	}
	out, err := command(ctx, "git", args...)
	name := "git " + key
	value := strings.TrimSpace(string(out))
	if err != nil {
		return Result{Name: name, OK: false, Detail: strings.TrimSpace(errDetail(err, out))}
	}
	if value == "" {
		return Result{Name: name, OK: false, Detail: "not configured"}
	}
	return Result{Name: name, OK: true, Detail: value}
}

func checkGoTestCGO(ctx context.Context, command commandFunc) Result {
	dir, err := os.MkdirTemp("", "drem-preflight-cgo-*")
	if err != nil {
		return Result{Name: "go test cgo", OK: false, Detail: err.Error()}
	}
	defer os.RemoveAll(dir)

	files := map[string]string{
		"go.mod":            "module drem-preflight-cgo\n\ngo 1.25\n",
		"preflight.go":      "package preflight\n\n// int answer(void) { return 42; }\nimport \"C\"\n\nfunc Answer() int { return int(C.answer()) }\n",
		"preflight_test.go": "package preflight\n\nimport \"testing\"\n\nfunc TestAnswer(t *testing.T) {\n\tif Answer() != 42 {\n\t\tt.Fatal(\"unexpected cgo result\")\n\t}\n}\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			return Result{Name: "go test cgo", OK: false, Detail: err.Error()}
		}
	}

	out, err := command(ctx, "go", "test", "-C", dir, ".")
	if err != nil {
		return Result{Name: "go test cgo", OK: false, Detail: strings.TrimSpace(errDetail(err, out))}
	}
	return Result{Name: "go test cgo", OK: true, Detail: strings.TrimSpace(string(out))}
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func errDetail(err error, out []byte) string {
	detail := strings.TrimSpace(string(out))
	if detail == "" {
		return err.Error()
	}
	return detail + ": " + err.Error()
}
