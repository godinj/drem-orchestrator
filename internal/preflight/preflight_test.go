package preflight

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestRunReturnsStructuredResults(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	lookPath := func(exe string) (string, error) {
		return "/bin/" + exe, nil
	}
	command := func(_ context.Context, name string, args ...string) ([]byte, error) {
		got := append([]string{name}, args...)
		switch {
		case len(got) == 6 && got[0] == "git" && got[1] == "-C" && got[3] == "config" && got[4] == "--get" && got[5] == "user.name":
			return []byte("Drem Operator\n"), nil
		case len(got) == 6 && got[0] == "git" && got[1] == "-C" && got[3] == "config" && got[4] == "--get" && got[5] == "user.email":
			return []byte("operator@example.test\n"), nil
		case len(got) == 5 && got[0] == "go" && got[1] == "test" && got[2] == "-C" && got[4] == ".":
			return []byte("ok\n"), nil
		default:
			t.Fatalf("unexpected command: %v", got)
			return nil, nil
		}
	}

	results := run(ctx, Options{WorkDir: t.TempDir()}, lookPath, command)
	if len(results) != 6 {
		t.Fatalf("expected 6 results, got %d: %#v", len(results), results)
	}
	for _, result := range results {
		if result.Name == "" {
			t.Fatalf("result has empty name: %#v", result)
		}
		if !result.OK {
			t.Fatalf("result %q was not OK: %s", result.Name, result.Detail)
		}
		if result.Detail == "" {
			t.Fatalf("result %q has empty detail", result.Name)
		}
	}
}

func TestExecutableCheckReportsMissingBinary(t *testing.T) {
	t.Parallel()

	result := checkExecutable(func(string) (string, error) {
		return "", errors.New("not found")
	}, "go")

	if result.OK {
		t.Fatalf("expected missing executable to fail")
	}
	if result.Name != "go executable" {
		t.Fatalf("unexpected name: %q", result.Name)
	}
	if !strings.Contains(result.Detail, "not found") {
		t.Fatalf("unexpected detail: %q", result.Detail)
	}
}

func TestGitConfigRequiresNonEmptyValue(t *testing.T) {
	t.Parallel()

	result := checkGitConfig(context.Background(), func(context.Context, string, ...string) ([]byte, error) {
		return []byte("\n"), nil
	}, "", "user.email")

	if result.OK {
		t.Fatalf("expected empty git config to fail")
	}
	if result.Detail != "not configured" {
		t.Fatalf("unexpected detail: %q", result.Detail)
	}
}

func TestGitConfigIncludesCommandFailureDetail(t *testing.T) {
	t.Parallel()

	result := checkGitConfig(context.Background(), func(context.Context, string, ...string) ([]byte, error) {
		return []byte("missing key"), errors.New("exit status 1")
	}, "", "user.name")

	if result.OK {
		t.Fatalf("expected git config failure")
	}
	if !strings.Contains(result.Detail, "missing key") || !strings.Contains(result.Detail, "exit status 1") {
		t.Fatalf("unexpected detail: %q", result.Detail)
	}
}

func TestWorkDirWritable(t *testing.T) {
	t.Parallel()

	result := CheckWorkDirWritable(t.TempDir())
	if !result.OK {
		t.Fatalf("expected temp dir to be writable: %s", result.Detail)
	}
}

func TestWorkDirWritableRejectsFile(t *testing.T) {
	t.Parallel()

	file, err := os.CreateTemp(t.TempDir(), "not-dir-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	result := CheckWorkDirWritable(file.Name())
	if result.OK {
		t.Fatalf("expected file path to fail writability check")
	}
	if !strings.Contains(result.Detail, "not a directory") {
		t.Fatalf("unexpected detail: %q", result.Detail)
	}
}

func TestGoTestCGOCommandFailure(t *testing.T) {
	t.Parallel()

	result := checkGoTestCGO(context.Background(), func(context.Context, string, ...string) ([]byte, error) {
		return []byte("cgo: C compiler not found"), errors.New("exit status 1")
	})

	if result.OK {
		t.Fatalf("expected cgo smoke test failure")
	}
	if !strings.Contains(result.Detail, "C compiler not found") {
		t.Fatalf("unexpected detail: %q", result.Detail)
	}
}
