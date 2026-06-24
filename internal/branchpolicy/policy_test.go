package branchpolicy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcceptCleanScope(t *testing.T) {
	repo := newRepo(t)
	writeCommit(t, repo, "internal/foo.go", "package internal\n")
	base := branch(t, repo, "base")
	runGit(t, repo, "checkout", "-b", "feature")
	writeCommit(t, repo, "internal/foo.go", "package internal\n// changed\n")

	res, err := Accept(context.Background(), AcceptanceRequest{RepoDir: repo, BaseRef: base, AllowedScopes: []string{"internal/foo.go"}})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !res.Accepted || len(res.Rejected) != 0 || len(res.AcceptedFiles) != 1 {
		t.Fatalf("expected clean acceptance, got %+v", res)
	}
}

func TestAcceptRejectsArtifactOnly(t *testing.T) {
	repo := newRepo(t)
	writeCommit(t, repo, "README.md", "base\n")
	base := branch(t, repo, "base")
	runGit(t, repo, "checkout", "-b", "feature")
	writeCommit(t, repo, ".drem/worker-trace.log", "trace\n")

	res, err := Accept(context.Background(), AcceptanceRequest{RepoDir: repo, BaseRef: base})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	assertRejected(t, res, "worker_trace")
}

func TestAcceptRejectsPromptPlanAndCredentials(t *testing.T) {
	repo := newRepo(t)
	writeCommit(t, repo, "README.md", "base\n")
	base := branch(t, repo, "base")
	runGit(t, repo, "checkout", "-b", "feature")
	writeFile(t, repo, "prompt.md", "prompt\n")
	writeFile(t, repo, "plans/new-plan.md", "plan\n")
	writeFile(t, repo, ".env", "TOKEN=x\n")
	commit(t, repo, "bad artifacts")

	res, err := Accept(context.Background(), AcceptanceRequest{RepoDir: repo, BaseRef: base})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	assertRejected(t, res, "prompt_artifact")
	assertRejected(t, res, "plan_artifact")
	assertRejected(t, res, "credentials_or_config")
}

func TestAcceptRejectsUnrelatedDeletion(t *testing.T) {
	repo := newRepo(t)
	writeFile(t, repo, "allowed.go", "package p\n")
	writeFile(t, repo, "other.go", "package p\n")
	commit(t, repo, "base")
	base := branch(t, repo, "base")
	runGit(t, repo, "checkout", "-b", "feature")
	if err := os.Remove(filepath.Join(repo, "other.go")); err != nil {
		t.Fatal(err)
	}
	commit(t, repo, "delete unrelated")

	res, err := Accept(context.Background(), AcceptanceRequest{RepoDir: repo, BaseRef: base, AllowedScopes: []string{"allowed.go"}})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	assertRejected(t, res, "unrelated_deletion")
}

func TestPreflightRejectsNonWritableBranchMetadata(t *testing.T) {
	repo := newBareRepo(t)
	work := clone(t, repo)
	writeCommit(t, work, "README.md", "base\n")
	runGit(t, work, "push", "origin", "HEAD:main")

	refsHead := filepath.Join(repo, "refs", "heads")
	old, err := chmodNoWrite(refsHead)
	if err != nil {
		t.Skipf("chmod not supported: %v", err)
	}
	defer func() { _ = os.Chmod(refsHead, old) }()

	err = Preflight(context.Background(), PreflightRequest{BareRepo: repo, Branch: "feature/test", Source: "main"})
	if err == nil || !strings.Contains(err.Error(), ReasonBranchPermission) {
		t.Fatalf("expected %s error, got %v", ReasonBranchPermission, err)
	}
}

func assertRejected(t *testing.T, res AcceptanceResult, reason string) {
	t.Helper()
	if res.Accepted {
		t.Fatalf("expected rejection %q, got accepted %+v", reason, res)
	}
	for _, rej := range res.Rejected {
		if rej.Reason == reason {
			return
		}
	}
	t.Fatalf("expected rejection %q in %+v", reason, res.Rejected)
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "config", "user.email", "test@example.com")
	return dir
}

func newBareRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo.git")
	run(t, "git", "init", "--bare", dir)
	return dir
}

func clone(t *testing.T, bare string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "work")
	run(t, "git", "clone", bare, dir)
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "config", "user.email", "test@example.com")
	return dir
}

func branch(t *testing.T, repo, name string) string {
	t.Helper()
	sha := strings.TrimSpace(runGitOut(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "branch", "-f", name, sha)
	return name
}

func writeCommit(t *testing.T, repo, path, content string) {
	t.Helper()
	writeFile(t, repo, path, content)
	commit(t, repo, "update "+path)
}

func writeFile(t *testing.T, repo, path, content string) {
	t.Helper()
	abs := filepath.Join(repo, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, repo, msg string) {
	t.Helper()
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", msg)
}

func chmodNoWrite(path string) (os.FileMode, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	old := info.Mode().Perm()
	return old, os.Chmod(path, old&^0222)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	runIn(t, dir, "git", args...)
}

func runGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return runInOut(t, dir, "git", args...)
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	runIn(t, "", name, args...)
}

func runIn(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	_ = runInOut(t, dir, name, args...)
}

func runInOut(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}
