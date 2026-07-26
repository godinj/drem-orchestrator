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

func TestAcceptExplicitHeadRefDoesNotRequireCheckout(t *testing.T) {
	repo := newRepo(t)
	writeCommit(t, repo, "README.md", "base\n")
	base := branch(t, repo, "base-explicit")
	runGit(t, repo, "checkout", "-b", "worker-explicit")
	writeCommit(t, repo, "allowed.txt", "worker\n")
	runGit(t, repo, "checkout", "base-explicit")

	res, err := Accept(context.Background(), AcceptanceRequest{
		RepoDir: repo, BaseRef: base, HeadRef: "worker-explicit", AllowedScopes: []string{"allowed.txt"},
	})
	if err != nil {
		t.Fatalf("accept explicit head: %v", err)
	}
	if !res.Accepted || len(res.AcceptedFiles) != 1 || res.AcceptedFiles[0] != "allowed.txt" {
		t.Fatalf("expected worker ref acceptance without checkout, got %+v", res)
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

func TestAcceptRejectsContaminatedArtifactDiffsWithStructuredReasons(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		reason string
	}{
		{name: "agent trace", path: "agent-trace-task.json", reason: "worker_trace"},
		{name: "agent push diagnostic", path: "agent-push-diagnostic.json", reason: "worker_trace"},
		{name: "drem attempt artifact", path: ".drem/attempt-123/output.json", reason: "worker_trace"},
		{name: "high risk config", path: ".ssh/config", reason: "credentials_or_config"},
		{name: "credential secret", path: "config/client-secret.json", reason: "credentials_or_config"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newRepo(t)
			writeCommit(t, repo, "README.md", "base\n")
			base := branch(t, repo, "base")
			runGit(t, repo, "checkout", "-b", "feature")
			writeCommit(t, repo, tt.path, "artifact\n")

			res, err := Accept(context.Background(), AcceptanceRequest{RepoDir: repo, BaseRef: base})
			if err != nil {
				t.Fatalf("accept: %v", err)
			}
			assertRejectedPath(t, res, tt.path, "A", tt.reason)
		})
	}
}

func TestAcceptRejectsPlanJSONDeletionWithStructuredReason(t *testing.T) {
	repo := newRepo(t)
	writeFile(t, repo, "plan.json", "{}\n")
	commit(t, repo, "base")
	base := branch(t, repo, "base")
	runGit(t, repo, "checkout", "-b", "feature")
	if err := os.Remove(filepath.Join(repo, "plan.json")); err != nil {
		t.Fatal(err)
	}
	commit(t, repo, "delete plan")

	res, err := Accept(context.Background(), AcceptanceRequest{RepoDir: repo, BaseRef: base})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	assertRejectedPath(t, res, "plan.json", "D", "plan_artifact")
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

func TestAcceptRejectsDestructiveRewriteInsideDeclaredScope(t *testing.T) {
	repo := newRepo(t)
	manifest := strings.Repeat("set(SOURCE file.cpp)\n", 80)
	writeCommit(t, repo, "tests/cmake/IntegrationSources.cmake", manifest)
	base := branch(t, repo, "base")
	runGit(t, repo, "checkout", "-b", "feature")
	writeCommit(t, repo, "tests/cmake/IntegrationSources.cmake", "#include <gtest/gtest.h>\n")

	res, err := Accept(context.Background(), AcceptanceRequest{
		RepoDir: repo, BaseRef: base,
		AllowedScopes:            []string{"tests/cmake/IntegrationSources.cmake"},
		RejectDestructiveRewrite: true,
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	assertRejectedPath(t, res, "tests/cmake/IntegrationSources.cmake", "M", "destructive_rewrite")
}

func TestAcceptAllowsFocusedRewriteWhenSafeguardEnabled(t *testing.T) {
	repo := newRepo(t)
	baseText := strings.Repeat("set(SOURCE file.cpp)\n", 80)
	writeCommit(t, repo, "manifest.cmake", baseText)
	base := branch(t, repo, "base")
	runGit(t, repo, "checkout", "-b", "feature")
	writeCommit(t, repo, "manifest.cmake", baseText+"set(SOURCE new.cpp)\n")

	res, err := Accept(context.Background(), AcceptanceRequest{
		RepoDir: repo, BaseRef: base, AllowedScopes: []string{"manifest.cmake"}, RejectDestructiveRewrite: true,
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("expected focused edit to be accepted, got %+v", res.Rejected)
	}
}

func TestAcceptRejectsCommentedOutTestContract(t *testing.T) {
	repo := newRepo(t)
	writeCommit(t, repo, "tests/test_feature.cpp", "// existing tests\n")
	base := branch(t, repo, "base")
	runGit(t, repo, "checkout", "-b", "feature")
	writeCommit(t, repo, "tests/test_feature.cpp", "// existing tests\n// AudioClip::divideAtTransients(settings);\n")
	contract := `{"red_mode":"compile_missing_symbol","expected_missing_symbols":["AudioClip::divideAtTransients(const Settings &)"]}`

	res, err := Accept(context.Background(), AcceptanceRequest{
		RepoDir: repo, BaseRef: base, AllowedScopes: []string{"tests/test_feature.cpp"}, TestContract: contract,
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	assertRejected(t, res, "missing_active_contract_assertion")
}

func TestAcceptAllowsActiveCompileRedContractCall(t *testing.T) {
	repo := newRepo(t)
	writeCommit(t, repo, "tests/test_feature.cpp", "// existing tests\n")
	base := branch(t, repo, "base")
	runGit(t, repo, "checkout", "-b", "feature")
	writeCommit(t, repo, "tests/test_feature.cpp", "// existing tests\nclip.divideAtTransients(settings);\n")
	contract := `{"red_mode":"compile_missing_symbol","expected_missing_symbols":["AudioClip::divideAtTransients(const Settings &)"]}`

	res, err := Accept(context.Background(), AcceptanceRequest{
		RepoDir: repo, BaseRef: base, AllowedScopes: []string{"tests/test_feature.cpp"}, TestContract: contract,
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("expected active contract call to pass static admission, got %+v", res.Rejected)
	}
}

func TestAcceptAllowsInferredReturnTypeForPlannedFunctionContract(t *testing.T) {
	repo := newRepo(t)
	writeCommit(t, repo, "tests/test_feature.cpp", "// existing tests\n")
	base := branch(t, repo, "base")
	runGit(t, repo, "checkout", "-b", "feature")
	writeCommit(t, repo, "tests/test_feature.cpp", `
dc::AudioClip::TransientSlicingSettings settings;
const auto result = dc::AudioClip::divideAtTransients(track, clip, settings);
CHECK(result.eventsSplit == 1);
`)
	contract := `{"red_mode":"compile_missing_symbol","expected_missing_symbols":["dc::AudioClip::TransientSlicingSettings","dc::AudioClip::TransientSlicingResult","dc::AudioClip::divideAtTransients(track, clip, settings)"],"semantic_contracts":[{"kind":"cpp_type","state":"planned","symbol":"dc::AudioClip::TransientSlicingSettings"},{"kind":"cpp_type","state":"planned","symbol":"dc::AudioClip::TransientSlicingResult"},{"kind":"cpp_function","state":"planned","signature":"dc::AudioClip::divideAtTransients(track, clip, settings)"}]}`

	res, err := Accept(context.Background(), AcceptanceRequest{
		RepoDir: repo, BaseRef: base, AllowedScopes: []string{"tests/test_feature.cpp"}, TestContract: contract,
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("expected inferred return type with active planned call to pass, got %+v", res.Rejected)
	}
}

func TestAcceptRejectsTypedSetupWithoutPlannedFunctionCall(t *testing.T) {
	repo := newRepo(t)
	writeCommit(t, repo, "tests/test_feature.cpp", "// existing tests\n")
	base := branch(t, repo, "base")
	runGit(t, repo, "checkout", "-b", "feature")
	writeCommit(t, repo, "tests/test_feature.cpp", "dc::AudioClip::TransientSlicingSettings settings;\nCHECK(settings.threshold > 0);\n")
	contract := `{"red_mode":"compile_missing_symbol","expected_missing_symbols":["dc::AudioClip::TransientSlicingSettings","dc::AudioClip::TransientSlicingResult","dc::AudioClip::divideAtTransients(track, clip, settings)"],"semantic_contracts":[{"kind":"cpp_type","state":"planned","symbol":"dc::AudioClip::TransientSlicingSettings"},{"kind":"cpp_type","state":"planned","symbol":"dc::AudioClip::TransientSlicingResult"},{"kind":"cpp_function","state":"planned","signature":"dc::AudioClip::divideAtTransients(track, clip, settings)"}]}`

	res, err := Accept(context.Background(), AcceptanceRequest{
		RepoDir: repo, BaseRef: base, AllowedScopes: []string{"tests/test_feature.cpp"}, TestContract: contract,
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	assertRejectedPath(t, res, "divideAtTransients", "compile_missing_symbol", "missing_active_contract_assertion")
}

func TestAcceptRejectsRegistryActionMentionOnlyInTestName(t *testing.T) {
	repo := newRepo(t)
	writeCommit(t, repo, "tests/test_marker.cpp", "// existing tests\n")
	base := branch(t, repo, "base")
	runGit(t, repo, "checkout", "-b", "feature")
	writeCommit(t, repo, "tests/test_marker.cpp", `
TEST_CASE("marker.add is routed")
{
    fixture.simulateExCommand("mark \"Verse 1\"");
    CHECK(fixture.markers().size() == 1);
}
`)
	contract := `{"red_mode":"runtime_assertion","semantic_contracts":[{"kind":"registry_action","state":"planned","action_id":"marker.add"}]}`

	res, err := Accept(context.Background(), AcceptanceRequest{
		RepoDir: repo, BaseRef: base, AllowedScopes: []string{"tests/test_marker.cpp"}, TestContract: contract,
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	assertRejectedPath(t, res, "marker.add", "registry_action", "missing_active_runtime_assertion")
}

func TestAcceptAllowsSourceBackedRegistryActionAssertion(t *testing.T) {
	repo := newRepo(t)
	writeCommit(t, repo, "tests/test_marker.cpp", "// existing tests\n")
	base := branch(t, repo, "base")
	runGit(t, repo, "checkout", "-b", "feature")
	writeCommit(t, repo, "tests/test_marker.cpp", `
const auto& actions = fixture.engine.getActionRegistry().getAllActions();
CHECK(std::any_of(actions.begin(), actions.end(), [](const auto& action) { return action.id == "marker.add" && static_cast<bool>(action.executeWithArgs); }));
fixture.simulateExCommand("mark \"Verse 1\"");
CHECK(fixture.markers().size() == 1);
`)
	contract := `{"red_mode":"runtime_assertion","semantic_contracts":[{"kind":"registry_action","state":"planned","action_id":"marker.add"}]}`

	res, err := Accept(context.Background(), AcceptanceRequest{
		RepoDir: repo, BaseRef: base, AllowedScopes: []string{"tests/test_marker.cpp"}, TestContract: contract,
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("expected active registry assertion to pass, got %+v", res.Rejected)
	}
}

func TestAcceptUsesOriginalContractBaseAcrossBoundedRepair(t *testing.T) {
	repo := newRepo(t)
	writeCommit(t, repo, "tests/test_feature.cpp", "// existing tests\n")
	originalBase := branch(t, repo, "original-base")
	runGit(t, repo, "checkout", "-b", "feature")
	writeCommit(t, repo, "tests/test_feature.cpp", "const auto result = clip.divideAtTransients(settings);\n")
	repairBase := branch(t, repo, "repair-base")
	writeCommit(t, repo, "tests/test_feature.cpp", "const auto result = clip.divideAtTransients(settings);\nCHECK(result.eventsSplit == 1);\n")
	contract := `{"red_mode":"compile_missing_symbol","semantic_contracts":[{"kind":"cpp_function","state":"planned","signature":"AudioClip::divideAtTransients(const Settings &)"}]}`

	res, err := Accept(context.Background(), AcceptanceRequest{
		RepoDir: repo, BaseRef: repairBase, TestContractBaseRef: originalBase,
		AllowedScopes: []string{"tests/test_feature.cpp"}, TestContract: contract,
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("expected cumulative contract scan with repair-local scope scan, got %+v", res.Rejected)
	}
	if res.BaseRef != repairBase {
		t.Fatalf("scope acceptance base changed: got %q want %q", res.BaseRef, repairBase)
	}
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

func assertRejectedPath(t *testing.T, res AcceptanceResult, path, status, reason string) {
	t.Helper()
	if res.Accepted {
		t.Fatalf("expected rejection %q for %s, got accepted %+v", reason, path, res)
	}
	for _, rej := range res.Rejected {
		if rej.Path == path && rej.Status == status && rej.Reason == reason {
			return
		}
	}
	t.Fatalf("expected rejection path=%q status=%q reason=%q in %+v", path, status, reason, res.Rejected)
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
