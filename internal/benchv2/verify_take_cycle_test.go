package benchv2

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTakeCycleOracleArtifactsAreContentAddressed(t *testing.T) {
	root := t.TempDir()
	raw := []byte("canonical")
	require.NoError(t, os.WriteFile(filepath.Join(root, takeTestsPatch), raw, 0o644))
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	task := TaskSpec{OracleArtifacts: []OracleArtifactPin{{Path: takeTestsPatch, SHA256: digest}}}
	verifier := BuiltinVerifier{OracleRoot: root}
	actual, err := verifier.readOracleArtifact(task, takeTestsPatch)
	require.NoError(t, err)
	require.Equal(t, raw, actual)
	require.NoError(t, os.WriteFile(filepath.Join(root, takeTestsPatch), []byte("tampered"), 0o644))
	_, err = verifier.readOracleArtifact(task, takeTestsPatch)
	require.ErrorContains(t, err, "digest mismatch")
}

func TestApplyTakeMutantRequiresOneExactMatch(t *testing.T) {
	mutant := takeMutant{ID: "change", Path: "file", Old: "before", New: "after"}
	actual, err := applyTakeMutant("prefix before suffix", mutant)
	require.NoError(t, err)
	require.Equal(t, "prefix after suffix", actual)
	_, err = applyTakeMutant("before before", mutant)
	require.ErrorContains(t, err, "exactly once")
}

func TestNativeRedFailureDistinguishesCompileFailure(t *testing.T) {
	require.True(t, nativeRedFailure("99% tests passed, 3 tests failed out of 605"))
	require.False(t, nativeRedFailure("FAILED: [code=1] source.cpp.o\ntests failed out of 605"))
	require.False(t, nativeRedFailure("100% tests passed"))
}

func TestTrivialCandidateTestCannotSatisfyRedContract(t *testing.T) {
	// An empty or assertion-free candidate test leaves the clean base green.
	// Case 4 and the independent test side of case 6 must reject that outcome.
	require.ErrorContains(t, validateNativeRed("100% tests passed", nil), "unexpectedly passed")
	require.NoError(t, validateNativeRed("99% tests passed, 1 tests failed out of 605", fmt.Errorf("test failed")))
}

func TestTakeCycleGateInjectionAvoidsNativeExecution(t *testing.T) {
	called := false
	verifier := BuiltinVerifier{NativeGate: func(_ context.Context, workDir string) (string, error) {
		called = true
		require.Equal(t, "/fixture", workDir)
		return "ok", nil
	}}
	output, err := verifier.runTakeCycleGate(context.Background(), "/fixture")
	require.NoError(t, err)
	require.Equal(t, "ok", output)
	require.True(t, called)
}

func TestTakeCycleChangedGateInjectionAvoidsCanvasExecution(t *testing.T) {
	called := false
	verifier := BuiltinVerifier{ChangedGate: func(_ context.Context, workDir string) (string, error) {
		called = true
		require.Equal(t, "/fixture", workDir)
		return "ok", nil
	}}
	output, err := verifier.runTakeCycleChangedGate(context.Background(), "/fixture")
	require.NoError(t, err)
	require.Equal(t, "ok", output)
	require.True(t, called)
}

func TestTakeCycleStructureRejectsFreeHelperAndAcceptsMembers(t *testing.T) {
	root := t.TempDir()
	write := func(relative, body string) {
		path := filepath.Join(root, filepath.FromSlash(relative))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	write("src/vim/adapters/EditorAdapter.h", "private:\n void takeNext(int);\n void takePrev(int count);\n void cycleSelectedTake(int delta);\n")
	write("src/vim/adapters/EditorAdapter.cpp", "#include \"fragments/EditorAdapterTakeActions.inc\"\n")
	write("src/vim/adapters/fragments/EditorAdapterActionHandlers.inc", "")
	write("src/vim/adapters/fragments/EditorAdapterTakeActions.inc", "void EditorAdapter::takeNext(int) {}\nvoid EditorAdapter::takePrev(int) {}\nvoid EditorAdapter::cycleSelectedTake(int) {}\n")
	write("src/vim/adapters/fragments/EditorAdapterActionRegistration.inc", "{ \"take.next\", \"Next Take\", \"Take\", \"\", &EditorAdapter::takeNext }\n{ \"take.prev\", \"Previous Take\", \"Take\", \"\", &EditorAdapter::takePrev }\n")
	require.NoError(t, verifyTakeCycleStructure(root))
	write("src/vim/adapters/fragments/EditorAdapterActionHandlers.inc", "void EditorAdapter::takeNext(int) {}\n")
	require.ErrorContains(t, verifyTakeCycleStructure(root), "only in EditorAdapterTakeActions.inc")
	write("src/vim/adapters/fragments/EditorAdapterActionHandlers.inc", "")
	write("src/vim/adapters/fragments/EditorAdapterTakeActions.inc", "static void cycleSelectedTake (EditorAdapter&) {}\nvoid EditorAdapter::takeNext(int) {}\nvoid EditorAdapter::takePrev(int) {}\nvoid EditorAdapter::cycleSelectedTake(int) {}\n")
	require.ErrorContains(t, verifyTakeCycleStructure(root), "must be an EditorAdapter member")
}
