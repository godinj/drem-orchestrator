package benchv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	takeKeymapPatch     = "take-cycling-canonical-keymap.patch"
	takeCapstoneMutants = "take-cycling-capstone-mutants.json"
	takeKeymapFile      = "config/default_keymap.yaml"
)

type takeCapstoneMutantCorpus struct {
	Schema  string       `json:"schema"`
	Mutants []takeMutant `json:"mutants"`
}

func (verifier BuiltinVerifier) verifyTakeCyclingCapstone(ctx context.Context, task TaskSpec, candidate string) VerifyOutcome {
	testsPatch, err := verifier.readOracleArtifact(task, takeTestsPatch)
	if err != nil {
		return takeCycleFailure(err)
	}
	implementationPatch, err := verifier.readOracleArtifact(task, takeImplPatch)
	if err != nil {
		return takeCycleFailure(err)
	}
	keymapPatch, err := verifier.readOracleArtifact(task, takeKeymapPatch)
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
	// Independently grade the candidate test contract: red on the exact base,
	// green on hidden production, and sensitive to every hidden mutant.
	if err := copyCandidateFiles(candidate, workDir, []string{takeTestFile}); err != nil {
		return takeCycleFailure(err)
	}
	if output, gateErr := verifier.runTakeCycleGate(ctx, workDir); validateNativeRed(output, gateErr) != nil {
		return takeCycleFailure(fmt.Errorf("candidate tests are not compile-valid red tests: %s", tailOutput(output)))
	}
	if err := applyPatchBytes(workDir, implementationPatch); err != nil {
		return takeCycleFailure(err)
	}
	if err := applyPatchBytes(workDir, keymapPatch); err != nil {
		return takeCycleFailure(err)
	}
	if output, err := verifier.runTakeCycleGate(ctx, workDir); err != nil {
		return takeCycleFailure(fmt.Errorf("hidden canonical production did not satisfy candidate tests: %w: %s", err, tailOutput(output)))
	}
	if err := verifier.verifyTakeCapstoneMutants(ctx, task, workDir); err != nil {
		return takeCycleFailure(err)
	}

	// Independently grade production with hidden tests, then restore the
	// candidate test file so changed-file and Release evidence cover the exact
	// submitted six-path artifact.
	if err := restoreTakeCapstoneBase(workDir, task.Fixture.BaseCommit); err != nil {
		return takeCycleFailure(err)
	}
	if err := copyCandidateFiles(candidate, workDir, []string{
		takeHeaderFile, takeSourceFile, takeActionsFile, takeRegisterFile, takeKeymapFile,
	}); err != nil {
		return takeCycleFailure(err)
	}
	if err := verifyTakeCycleStructure(workDir); err != nil {
		return takeCycleFailure(err)
	}
	if outcome := verifyKeymap(workDir); !outcome.Passed {
		return outcome
	}
	if err := applyPatchBytes(workDir, testsPatch); err != nil {
		return takeCycleFailure(err)
	}
	if output, err := verifier.runTakeCycleGate(ctx, workDir); err != nil {
		return takeCycleFailure(fmt.Errorf("candidate production failed hidden canonical tests: %w: %s", err, tailOutput(output)))
	}
	if err := copyCandidateFiles(candidate, workDir, []string{takeTestFile}); err != nil {
		return takeCycleFailure(err)
	}
	if output, err := verifier.runTakeCycleChangedGate(ctx, workDir); err != nil {
		return takeCycleFailure(fmt.Errorf("exact candidate failed Canvas changed-file checks: %w: %s", err, tailOutput(output)))
	}
	evidence, err := verifier.runReleaseGate(ctx, workDir, task.ReleaseArtifactPath)
	if err != nil {
		return takeCycleFailure(fmt.Errorf("exact candidate Release artifact: %w", err))
	}
	return VerifyOutcome{Passed: true, Compiled: true, ReleaseArtifact: evidence}
}

func restoreTakeCapstoneBase(workDir, base string) error {
	if err := restoreTakeCycleBaseFiles(workDir, base, []string{
		takeTestFile, takeHeaderFile, takeSourceFile, takeRegisterFile, takeKeymapFile,
	}); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(workDir, filepath.FromSlash(takeActionsFile))); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (verifier BuiltinVerifier) verifyTakeCapstoneMutants(ctx context.Context, task TaskSpec, workDir string) error {
	raw, err := verifier.readOracleArtifact(task, takeCapstoneMutants)
	if err != nil {
		return err
	}
	var corpus takeCapstoneMutantCorpus
	if err := DecodeStrict(raw, &corpus); err != nil || corpus.Schema != "canvasbench.take-cycling-capstone-mutants.v1" || len(corpus.Mutants) == 0 {
		return fmt.Errorf("invalid take-cycling capstone mutant corpus")
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
		killed := false
		switch mutant.Gate {
		case "native":
			_, gateErr := verifier.runTakeCycleGate(ctx, workDir)
			killed = gateErr != nil
		case "keymap":
			killed = !verifyKeymap(workDir).Passed
		default:
			return fmt.Errorf("mutant %s has invalid gate", mutant.ID)
		}
		if err := os.WriteFile(path, original, 0o644); err != nil {
			return err
		}
		if !killed {
			return fmt.Errorf("capstone oracle did not kill mutant %s", mutant.ID)
		}
	}
	return nil
}

func (verifier BuiltinVerifier) runReleaseGate(ctx context.Context, workDir, relativePath string) (*ArtifactEvidence, error) {
	if verifier.ReleaseGate != nil {
		return verifier.ReleaseGate(ctx, workDir, relativePath)
	}
	cmd := exec.CommandContext(ctx, filepath.Join(workDir, "scripts", "dev"), "build", "release")
	cmd.Dir = workDir
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("build failed: %w: %s", err, tailOutput(string(output)))
	}
	return hashReleaseArtifact(workDir, relativePath)
}

func hashReleaseArtifact(workDir, relativePath string) (*ArtifactEvidence, error) {
	path := filepath.Join(workDir, filepath.FromSlash(relativePath))
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return nil, err
	}
	return &ArtifactEvidence{
		Kind: "release_binary", Path: filepath.ToSlash(relativePath),
		SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: size,
	}, nil
}
