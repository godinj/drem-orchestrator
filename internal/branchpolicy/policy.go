package branchpolicy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/gitexec"
)

const (
	ReasonBranchPermission  = "branch_permission"
	ReasonBranchOwnership   = "branch_ownership"
	ReasonBranchContaminate = "branch_contamination"
)

type PreflightRequest struct {
	BareRepo string
	Branch   string
	Source   string
}

type AcceptanceRequest struct {
	RepoDir                  string
	BaseRef                  string
	HeadRef                  string
	AllowedScopes            []string
	RejectDestructiveRewrite bool
	TestContract             string
}

type AcceptanceResult struct {
	Accepted      bool        `json:"accepted"`
	BaseRef       string      `json:"base_ref"`
	BaseSHA       string      `json:"base_sha,omitempty"`
	HeadSHA       string      `json:"head_sha,omitempty"`
	AcceptedFiles []string    `json:"accepted_files,omitempty"`
	Rejected      []Rejection `json:"rejected,omitempty"`
}

type Rejection struct {
	Path   string `json:"path"`
	Status string `json:"status,omitempty"`
	Reason string `json:"reason"`
}

func Preflight(ctx context.Context, req PreflightRequest) error {
	if strings.TrimSpace(req.BareRepo) == "" || strings.TrimSpace(req.Branch) == "" || strings.TrimSpace(req.Source) == "" {
		return fmt.Errorf("%s: bare repo, branch, and source are required", ReasonBranchOwnership)
	}
	if _, err := gitexec.RunGit(ctx, req.BareRepo, "rev-parse", "--git-dir"); err != nil {
		return fmt.Errorf("%s: resolve bare repo: %w", ReasonBranchOwnership, err)
	}
	if _, err := gitexec.RunGit(ctx, req.BareRepo, "rev-parse", "--verify", "refs/heads/"+req.Source); err != nil {
		return fmt.Errorf("%s: source branch %q missing: %w", ReasonBranchOwnership, req.Source, err)
	}

	if err := requireWritable(filepath.Join(req.BareRepo, "refs", "heads")); err != nil {
		return fmt.Errorf("%s: refs/heads not writable: %w", ReasonBranchPermission, err)
	}
	if err := requireWritable(filepath.Join(req.BareRepo, "logs", "refs", "heads")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%s: logs/refs/heads not writable: %w", ReasonBranchPermission, err)
	}

	branchRef := filepath.Join(req.BareRepo, "refs", "heads", filepath.FromSlash(req.Branch))
	if _, err := os.Stat(branchRef); err == nil {
		if err := requireWritable(branchRef); err != nil {
			return fmt.Errorf("%s: branch ref not writable: %w", ReasonBranchPermission, err)
		}
		if _, err := gitexec.RunGit(ctx, req.BareRepo, "merge-base", "--is-ancestor", req.Source, req.Branch); err != nil {
			return fmt.Errorf("%s: branch %q is not based on %q: %w", ReasonBranchOwnership, req.Branch, req.Source, err)
		}
	}
	return nil
}

func Accept(ctx context.Context, req AcceptanceRequest) (AcceptanceResult, error) {
	res := AcceptanceResult{BaseRef: req.BaseRef}
	if strings.TrimSpace(req.RepoDir) == "" || strings.TrimSpace(req.BaseRef) == "" {
		return res, fmt.Errorf("repo dir and base ref are required")
	}
	baseSHA, err := gitexec.RunGit(ctx, req.RepoDir, "rev-parse", req.BaseRef)
	if err != nil {
		return res, fmt.Errorf("resolve base ref: %w", err)
	}
	headRef := strings.TrimSpace(req.HeadRef)
	if headRef == "" {
		headRef = "HEAD"
	}
	headSHA, err := gitexec.RunGit(ctx, req.RepoDir, "rev-parse", headRef)
	if err != nil {
		return res, fmt.Errorf("resolve HEAD: %w", err)
	}
	res.BaseSHA = baseSHA
	res.HeadSHA = headSHA

	out, err := gitexec.RunGit(ctx, req.RepoDir, "diff", "--name-status", req.BaseRef+".."+headRef)
	if err != nil {
		return res, fmt.Errorf("diff against base: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		res.Accepted = true
		return res, nil
	}

	scopes := normalizeScopes(req.AllowedScopes)
	statuses := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		status, path := parseNameStatus(line)
		if path == "" {
			continue
		}
		if reason := rejectReason(path, status, scopes); reason != "" {
			res.Rejected = append(res.Rejected, Rejection{Path: path, Status: status, Reason: reason})
			continue
		}
		statuses[path] = status
		res.AcceptedFiles = append(res.AcceptedFiles, path)
	}
	if req.RejectDestructiveRewrite {
		rejections, err := destructiveRewriteRejections(ctx, req, headRef, statuses)
		if err != nil {
			return res, err
		}
		res.Rejected = append(res.Rejected, rejections...)
	}
	if strings.TrimSpace(req.TestContract) != "" {
		rejections, err := testCheckpointRejections(ctx, req, headRef)
		if err != nil {
			return res, err
		}
		res.Rejected = append(res.Rejected, rejections...)
	}
	res.Accepted = len(res.Rejected) == 0
	return res, nil
}

func destructiveRewriteRejections(ctx context.Context, req AcceptanceRequest, headRef string, statuses map[string]string) ([]Rejection, error) {
	out, err := gitexec.RunGit(ctx, req.RepoDir, "diff", "--numstat", req.BaseRef+".."+headRef)
	if err != nil {
		return nil, fmt.Errorf("measure diff against base: %w", err)
	}
	var rejected []Rejection
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] == "-" || fields[1] == "-" {
			continue
		}
		path := filepath.ToSlash(fields[len(fields)-1])
		if statuses[path] == "A" || statuses[path] == "D" {
			continue
		}
		var added, deleted int
		if _, err := fmt.Sscan(fields[0], &added); err != nil {
			continue
		}
		if _, err := fmt.Sscan(fields[1], &deleted); err != nil {
			continue
		}
		if deleted < 20 || added*2 >= deleted {
			continue
		}
		base, err := gitexec.RunGit(ctx, req.RepoDir, "show", req.BaseRef+":"+path)
		if err != nil {
			continue
		}
		baseLines := strings.Count(base, "\n")
		if baseLines == 0 || float64(deleted)/float64(baseLines) < 0.60 {
			continue
		}
		rejected = append(rejected, Rejection{Path: path, Status: statuses[path], Reason: "destructive_rewrite"})
	}
	return rejected, nil
}

func requireWritable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0222 == 0 {
		return fmt.Errorf("%s has no write bits", path)
	}
	return nil
}

func parseNameStatus(line string) (string, string) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", ""
	}
	status := fields[0]
	if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
		return status[:1], filepath.ToSlash(fields[len(fields)-1])
	}
	return status[:1], filepath.ToSlash(fields[1])
}

func rejectReason(path, status string, scopes []string) string {
	lower := strings.ToLower(filepath.ToSlash(path))
	switch {
	case isWorkerTrace(lower):
		return "worker_trace"
	case isPromptArtifact(lower):
		return "prompt_artifact"
	case isPlanArtifact(lower):
		return "plan_artifact"
	case isCredentialOrConfig(lower):
		return "credentials_or_config"
	case status == "D" && !inScope(lower, scopes):
		return "unrelated_deletion"
	case len(scopes) > 0 && !inScope(lower, scopes):
		return "outside_accepted_scope"
	}
	return ""
}

func isWorkerTrace(path string) bool {
	base := filepath.Base(path)
	return strings.HasPrefix(path, ".drem/traces/") || strings.HasPrefix(path, ".drem/workers/") || strings.HasPrefix(path, ".drem/attempts/") || strings.HasPrefix(path, ".drem/attempt-") || strings.HasPrefix(base, "agent-trace-") || base == "agent-push-diagnostic.json" || strings.Contains(path, "worker-trace") || strings.Contains(path, "attempt-trace")
}

func isPromptArtifact(path string) bool {
	base := filepath.Base(path)
	return base == "prompt.md" || strings.HasPrefix(path, "prompts/") || strings.Contains(path, "/prompts/")
}

func isPlanArtifact(path string) bool {
	return path == "plan.json" || strings.HasPrefix(path, "plans/")
}

func isCredentialOrConfig(path string) bool {
	base := filepath.Base(path)
	return strings.HasPrefix(path, ".claude/") || strings.HasPrefix(path, ".config/") || strings.HasPrefix(path, ".ssh/") || strings.HasPrefix(path, ".aws/") || strings.HasPrefix(path, ".gnupg/") || strings.HasPrefix(path, ".docker/") || base == ".env" || strings.HasPrefix(base, ".env.") || strings.Contains(path, "credential") || strings.Contains(path, "token") || strings.Contains(path, "secret")
}

func normalizeScopes(scopes []string) []string {
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.Trim(strings.ToLower(filepath.ToSlash(scope)), "/")
		if scope != "" {
			out = append(out, scope)
		}
	}
	return out
}

func inScope(path string, scopes []string) bool {
	if len(scopes) == 0 {
		return true
	}
	path = strings.Trim(strings.ToLower(filepath.ToSlash(path)), "/")
	for _, scope := range scopes {
		if path == scope || strings.HasPrefix(path, strings.TrimSuffix(scope, "/")+"/") {
			return true
		}
	}
	return false
}
