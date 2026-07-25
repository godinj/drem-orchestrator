package benchv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	takeTestsPatch   = "take-cycling-canonical-tests.patch"
	takeImplPatch    = "take-cycling-canonical-implementation.patch"
	takeMutants      = "take-cycling-mutants.json"
	takeDiagnostics  = "take-cycling-bad-artifact-diagnostics.txt"
	takeTestFile     = "tests/integration/LaneVersionCommandTest.cpp"
	takeHeaderFile   = "src/vim/adapters/EditorAdapter.h"
	takeSourceFile   = "src/vim/adapters/EditorAdapter.cpp"
	takeHandlerFile  = "src/vim/adapters/fragments/EditorAdapterActionHandlers.inc"
	takeActionsFile  = "src/vim/adapters/fragments/EditorAdapterTakeActions.inc"
	takeRegisterFile = "src/vim/adapters/fragments/EditorAdapterActionRegistration.inc"
)

type takeMutantCorpus struct {
	Schema  string       `json:"schema"`
	Mutants []takeMutant `json:"mutants"`
}

type takeMutant struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

func (verifier BuiltinVerifier) verifyTakeCycling(ctx context.Context, task TaskSpec, candidate string) VerifyOutcome {
	testsPatch, err := verifier.readOracleArtifact(task, takeTestsPatch)
	if err != nil {
		return takeCycleFailure(err)
	}
	implementationPatch, err := verifier.readOracleArtifact(task, takeImplPatch)
	if err != nil {
		return takeCycleFailure(err)
	}
	workDir, cleanup, err := createTakeCycleWorktree(candidate, task.Fixture.BaseCommit)
	if err != nil {
		return takeCycleFailure(err)
	}
	defer cleanup()
	if err := prepareTakeCycleDependencies(candidate, workDir); err != nil {
		return takeCycleFailure(err)
	}

	switch task.OracleID {
	case "take-cycling-red-tests-canonical-v1":
		if err := copyCandidateFiles(candidate, workDir, []string{takeTestFile}); err != nil {
			return takeCycleFailure(err)
		}
		if output, gateErr := verifier.runTakeCycleGate(ctx, workDir); validateNativeRed(output, gateErr) != nil {
			return takeCycleFailure(fmt.Errorf("candidate tests are not compile-valid red tests: %s", tailOutput(output)))
		}
		if err := applyPatchBytes(workDir, implementationPatch); err != nil {
			return takeCycleFailure(err)
		}
		if output, err := verifier.runTakeCycleGate(ctx, workDir); err != nil {
			return takeCycleFailure(fmt.Errorf("canonical implementation did not satisfy candidate tests: %w: %s", err, tailOutput(output)))
		}
		if err := verifier.verifyTakeMutants(ctx, task, workDir); err != nil {
			return takeCycleFailure(err)
		}
	case "take-cycling-implementation-canonical-v1":
		if err := copyCandidateFiles(candidate, workDir, []string{
			takeHeaderFile,
			takeSourceFile,
			takeActionsFile,
			takeRegisterFile,
		}); err != nil {
			return takeCycleFailure(err)
		}
		if err := verifyTakeCycleStructure(workDir); err != nil {
			return takeCycleFailure(err)
		}
		if err := applyPatchBytes(workDir, testsPatch); err != nil {
			return takeCycleFailure(err)
		}
		if output, err := verifier.runTakeCycleGate(ctx, workDir); err != nil {
			return takeCycleFailure(fmt.Errorf("candidate implementation failed canonical tests: %w: %s", err, tailOutput(output)))
		}
		if output, err := verifier.runTakeCycleChangedGate(ctx, workDir); err != nil {
			return takeCycleFailure(fmt.Errorf("candidate implementation failed Canvas changed-file checks: %w: %s", err, tailOutput(output)))
		}
	case "take-cycling-bad-artifact-861eebff-v1":
		diagnostics, err := verifier.readOracleArtifact(task, takeDiagnostics)
		if err != nil {
			return takeCycleFailure(err)
		}
		if !strings.Contains(task.UserMessage, string(diagnostics)) {
			return takeCycleFailure(fmt.Errorf("content-addressed compiler diagnostics are not supplied verbatim to the candidate"))
		}
		if err := copyCandidateFiles(candidate, workDir, []string{
			takeHeaderFile,
			takeSourceFile,
			takeHandlerFile,
			takeActionsFile,
			takeRegisterFile,
		}); err != nil {
			return takeCycleFailure(err)
		}
		if err := verifyTakeCycleStructure(workDir); err != nil {
			return takeCycleFailure(err)
		}
		if err := applyPatchBytes(workDir, testsPatch); err != nil {
			return takeCycleFailure(err)
		}
		if output, err := verifier.runTakeCycleGate(ctx, workDir); err != nil {
			return takeCycleFailure(fmt.Errorf("candidate production failed hidden canonical tests: %w: %s", err, tailOutput(output)))
		}
		if output, err := verifier.runTakeCycleChangedGate(ctx, workDir); err != nil {
			return takeCycleFailure(fmt.Errorf("candidate production failed Canvas changed-file checks: %w: %s", err, tailOutput(output)))
		}

		if err := restoreTakeCycleBaseFiles(workDir, task.Fixture.BaseCommit, []string{
			takeTestFile,
			takeHeaderFile,
			takeSourceFile,
			takeHandlerFile,
			takeRegisterFile,
		}); err != nil {
			return takeCycleFailure(err)
		}
		if err := os.Remove(filepath.Join(workDir, filepath.FromSlash(takeActionsFile))); err != nil && !os.IsNotExist(err) {
			return takeCycleFailure(err)
		}
		if err := copyCandidateFiles(candidate, workDir, []string{takeTestFile}); err != nil {
			return takeCycleFailure(err)
		}
		if output, gateErr := verifier.runTakeCycleGate(ctx, workDir); validateNativeRed(output, gateErr) != nil {
			return takeCycleFailure(fmt.Errorf("candidate repaired tests are not compile-valid red tests: %s", tailOutput(output)))
		}
		if err := applyPatchBytes(workDir, implementationPatch); err != nil {
			return takeCycleFailure(err)
		}
		if output, err := verifier.runTakeCycleGate(ctx, workDir); err != nil {
			return takeCycleFailure(fmt.Errorf("candidate repaired tests failed canonical production: %w: %s", err, tailOutput(output)))
		}
		if err := verifier.verifyTakeMutants(ctx, task, workDir); err != nil {
			return takeCycleFailure(err)
		}
	default:
		return takeCycleFailure(fmt.Errorf("unknown take-cycling oracle"))
	}
	return VerifyOutcome{Passed: true, Compiled: true}
}

func (verifier BuiltinVerifier) readOracleArtifact(task TaskSpec, name string) ([]byte, error) {
	var expected string
	for _, artifact := range task.OracleArtifacts {
		if artifact.Path == name {
			expected = artifact.SHA256
			break
		}
	}
	if expected == "" {
		return nil, fmt.Errorf("hidden oracle %s is not content-addressed", name)
	}
	raw, err := os.ReadFile(filepath.Join(verifier.OracleRoot, name))
	if err != nil {
		return nil, fmt.Errorf("read hidden oracle %s: %w", name, err)
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != expected {
		return nil, fmt.Errorf("hidden oracle %s digest mismatch", name)
	}
	return raw, nil
}

func (verifier BuiltinVerifier) verifyTakeMutants(ctx context.Context, task TaskSpec, workDir string) error {
	raw, err := verifier.readOracleArtifact(task, takeMutants)
	if err != nil {
		return err
	}
	var corpus takeMutantCorpus
	if err := DecodeStrict(raw, &corpus); err != nil || corpus.Schema != "canvasbench.take-cycling-mutants.v1" || len(corpus.Mutants) == 0 {
		return fmt.Errorf("invalid take-cycling mutant corpus")
	}
	for _, mutant := range corpus.Mutants {
		path := filepath.Join(workDir, filepath.FromSlash(mutant.Path))
		original, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mutated, err := applyTakeMutant(string(original), mutant)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(mutated), 0o644); err != nil {
			return err
		}
		output, gateErr := verifier.runTakeCycleGate(ctx, workDir)
		restoreErr := os.WriteFile(path, original, 0o644)
		if restoreErr != nil {
			return restoreErr
		}
		if gateErr == nil {
			return fmt.Errorf("candidate tests did not kill mutant %s: %s", mutant.ID, tailOutput(output))
		}
	}
	return nil
}

func applyTakeMutant(original string, mutant takeMutant) (string, error) {
	if mutant.ID == "" || mutant.Path == "" || mutant.Old == "" || strings.Count(original, mutant.Old) != 1 {
		return "", fmt.Errorf("mutant %s does not match canonical source exactly once", mutant.ID)
	}
	return strings.Replace(original, mutant.Old, mutant.New, 1), nil
}

func (verifier BuiltinVerifier) runTakeCycleGate(ctx context.Context, workDir string) (string, error) {
	if verifier.NativeGate != nil {
		return verifier.NativeGate(ctx, workDir)
	}
	cmd := exec.CommandContext(ctx, filepath.Join(workDir, "scripts", "dev"), "test", "--filter", `(Take cycling|take\.)`)
	cmd.Dir = workDir
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func (verifier BuiltinVerifier) runTakeCycleChangedGate(ctx context.Context, workDir string) (string, error) {
	if verifier.ChangedGate != nil {
		return verifier.ChangedGate(ctx, workDir)
	}
	cmd := exec.CommandContext(ctx, filepath.Join(workDir, "scripts", "dev"), "check", "changed")
	cmd.Dir = workDir
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func createTakeCycleWorktree(candidate, base string) (string, func(), error) {
	parent, err := os.MkdirTemp("", "canvasbench-take-oracle-")
	if err != nil {
		return "", func() {}, err
	}
	workDir := filepath.Join(parent, "canvas")
	cmd := exec.Command("git", "-C", candidate, "worktree", "add", "--detach", workDir, base)
	if output, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(parent)
		return "", func() {}, fmt.Errorf("create hidden verification worktree: %w: %s", err, output)
	}
	cleanup := func() {
		remove := exec.Command("git", "-C", candidate, "worktree", "remove", "--force", workDir)
		if remove.Run() != nil {
			_ = os.RemoveAll(workDir)
		}
		_ = os.Remove(parent)
	}
	return workDir, cleanup, nil
}

func prepareTakeCycleDependencies(candidate, workDir string) error {
	target := filepath.Join(workDir, "libs", "skia")
	if _, err := os.Stat(filepath.Join(target, "lib", "libskia.a")); err == nil {
		return nil
	}
	commonRaw, err := exec.Command("git", "-C", candidate, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return err
	}
	common := strings.TrimSpace(string(commonRaw))
	if !filepath.IsAbs(common) {
		common = filepath.Join(candidate, common)
	}
	candidates := []string{filepath.Join(common, ".cache", "skia")}
	if filepath.Base(common) == ".git" {
		candidates = append(candidates, filepath.Join(filepath.Dir(common), "libs", "skia"))
	}
	for _, source := range candidates {
		if _, err := os.Stat(filepath.Join(source, "lib", "libskia.a")); err == nil {
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.Symlink(source, target)
		}
	}
	return fmt.Errorf("shared Canvas Skia dependency is unavailable")
}

func copyCandidateFiles(candidate, workDir string, paths []string) error {
	for _, relative := range paths {
		raw, err := os.ReadFile(filepath.Join(candidate, filepath.FromSlash(relative)))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(workDir, filepath.FromSlash(relative)), raw, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func restoreTakeCycleBaseFiles(workDir, base string, paths []string) error {
	for _, relative := range paths {
		cmd := exec.Command("git", "-C", workDir, "show", base+":"+filepath.ToSlash(relative))
		raw, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("restore pinned base file %s: %w", relative, err)
		}
		if err := os.WriteFile(filepath.Join(workDir, filepath.FromSlash(relative)), raw, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func applyPatchBytes(workDir string, patch []byte) error {
	cmd := exec.Command("git", "apply", "--whitespace=nowarn", "-")
	cmd.Dir = workDir
	cmd.Stdin = strings.NewReader(string(patch))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("apply hidden canonical patch: %w: %s", err, output)
	}
	return nil
}

func verifyTakeCycleStructure(workDir string) error {
	header, err := readFixtureFile(workDir, takeHeaderFile)
	if err != nil {
		return err
	}
	source, err := readFixtureFile(workDir, takeSourceFile)
	if err != nil {
		return err
	}
	handlers, err := readFixtureFile(workDir, takeHandlerFile)
	if err != nil {
		return err
	}
	takeActions, err := readFixtureFile(workDir, takeActionsFile)
	if err != nil {
		return err
	}
	registration, err := readFixtureFile(workDir, takeRegisterFile)
	if err != nil {
		return err
	}
	privateStart := strings.Index(header, "private:")
	if privateStart < 0 {
		return fmt.Errorf("EditorAdapter private section is missing")
	}
	privateHeader := header[privateStart:]
	for _, declaration := range []string{
		`\bvoid\s+takeNext\s*\(\s*int(?:\s+\w+)?\s*\)\s*;`,
		`\bvoid\s+takePrev\s*\(\s*int(?:\s+\w+)?\s*\)\s*;`,
		`\bvoid\s+cycleSelectedTake\s*\(\s*int(?:\s+\w+)?\s*\)\s*;`,
	} {
		if len(regexp.MustCompile(declaration).FindAllStringIndex(privateHeader, -1)) != 1 {
			return fmt.Errorf("missing exact private take-cycling declaration")
		}
	}
	if strings.Count(source, `#include "fragments/EditorAdapterTakeActions.inc"`) != 1 {
		return fmt.Errorf("EditorAdapter.cpp must include the focused take-actions fragment exactly once")
	}
	for _, misplaced := range []string{"EditorAdapter::takeNext", "EditorAdapter::takePrev", "EditorAdapter::cycleSelectedTake", "cycleSelectedTake (EditorAdapter&", "static void cycleSelectedTake"} {
		if strings.Contains(source, misplaced) || strings.Contains(handlers, misplaced) {
			return fmt.Errorf("take-cycling definitions must live only in EditorAdapterTakeActions.inc")
		}
	}
	if strings.Contains(takeActions, "cycleSelectedTake (EditorAdapter&") || strings.Contains(takeActions, "static void cycleSelectedTake") {
		return fmt.Errorf("cycleSelectedTake must be an EditorAdapter member")
	}
	for _, definition := range []string{`\bvoid\s+EditorAdapter::takeNext\s*\(`, `\bvoid\s+EditorAdapter::takePrev\s*\(`, `\bvoid\s+EditorAdapter::cycleSelectedTake\s*\(`} {
		if len(regexp.MustCompile(definition).FindAllStringIndex(takeActions, -1)) != 1 {
			return fmt.Errorf("missing exact member definition %s", definition)
		}
	}
	for _, action := range []string{
		`\{\s*"take\.next"[^\n]*&EditorAdapter::takeNext\s*\}`,
		`\{\s*"take\.prev"[^\n]*&EditorAdapter::takePrev\s*\}`,
	} {
		if len(regexp.MustCompile(action).FindAllStringIndex(registration, -1)) != 1 {
			return fmt.Errorf("missing exact action registration")
		}
	}
	return nil
}

func nativeRedFailure(output string) bool {
	return strings.Contains(output, "tests failed out of") && !strings.Contains(output, "FAILED: [code=")
}

func validateNativeRed(output string, gateErr error) error {
	if gateErr == nil {
		return fmt.Errorf("red test unexpectedly passed on the clean base")
	}
	if !nativeRedFailure(output) {
		return fmt.Errorf("red test did not reach a compile-valid test failure")
	}
	return nil
}

func takeCycleFailure(err error) VerifyOutcome {
	return VerifyOutcome{Failures: []string{err.Error()}}
}

func tailOutput(output string) string {
	const limit = 4000
	if len(output) <= limit {
		return strings.TrimSpace(output)
	}
	return strings.TrimSpace(output[len(output)-limit:])
}
