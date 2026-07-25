package benchv2

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type PreparedFixture struct {
	WorkDir string
	repo    string
}

func PrepareFixture(repo, scratchRoot string, fixture Fixture) (*PreparedFixture, error) {
	if err := verifyFixture(repo, fixture); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(scratchRoot, 0o755); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(scratchRoot, "canvasbench-")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(dir); err != nil {
		return nil, err
	}
	prepared := &PreparedFixture{WorkDir: dir, repo: repo}
	if out, err := git(repo, "worktree", "add", "--detach", dir, fixture.BaseCommit); err != nil {
		return nil, fmt.Errorf("create fixture worktree: %w: %s", err, out)
	}
	fail := func(err error) (*PreparedFixture, error) {
		_ = prepared.Cleanup()
		return nil, err
	}
	if fixture.SeedPatch != "" {
		raw, err := os.ReadFile(fixture.SeedPatch)
		if err != nil {
			return fail(err)
		}
		digest := sha256.Sum256(raw)
		if hex.EncodeToString(digest[:]) != fixture.SeedPatchSHA {
			return fail(fmt.Errorf("seed patch digest mismatch"))
		}
		cmd := exec.Command("git", "apply", "--whitespace=nowarn", fixture.SeedPatch)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fail(fmt.Errorf("apply seed patch: %w: %s", err, out))
		}
	}
	return prepared, nil
}

func (p *PreparedFixture) Cleanup() error {
	if p == nil || p.WorkDir == "" {
		return nil
	}
	_, err := git(p.repo, "worktree", "remove", "--force", p.WorkDir)
	if err != nil {
		_ = os.RemoveAll(p.WorkDir)
	}
	return err
}

func verifyFixture(repo string, fixture Fixture) error {
	resolved, err := git(repo, "rev-parse", fixture.BaseCommit+"^{commit}")
	if err != nil || strings.TrimSpace(resolved) != fixture.BaseCommit {
		return fmt.Errorf("base commit is unavailable or not exact")
	}
	for _, pin := range fixture.VisibleBlobs {
		if filepath.IsAbs(pin.Path) || strings.HasPrefix(filepath.Clean(pin.Path), "..") {
			return fmt.Errorf("invalid visible path %q", pin.Path)
		}
		value, err := git(repo, "rev-parse", fixture.BaseCommit+":"+filepath.ToSlash(pin.Path))
		if err != nil || strings.TrimSpace(value) != pin.SHA {
			return fmt.Errorf("visible blob mismatch for %s", pin.Path)
		}
	}
	return nil
}

func git(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func ChangedPaths(workDir string) ([]string, error) {
	out, err := git(workDir, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if len(line) < 4 {
			continue
		}
		paths = append(paths, filepath.ToSlash(strings.TrimSpace(line[3:])))
	}
	return paths, nil
}
