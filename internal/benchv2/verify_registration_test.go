package benchv2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const registrationCoordinator = `
void ActionCoordinator::registerAllActions()
{
    registerApplicationMenuActions();
    registerAudioProcessActions();
}
`

func writeRegistrationFixture(t *testing.T, coordinator string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"src/ui/ActionCoordinator.cpp":    coordinator,
		"src/ui/ActionCoordinator.h":      `class ActionCoordinator { void registerAudioProcessActions(); };`,
		"src/ui/ActionAudioProcesses.cpp": `void ActionCoordinator::registerAudioProcessActions() { registry.registerAction ({ "audio.divide_transients" }); }`,
		"config/default_keymap.yaml":      "normal:\n  editor:\n    oh: audio.divide_transients\n",
	}
	for relative, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
	}
	return root
}

func TestAudioProcessRegistrationAcceptsExactProductionSeam(t *testing.T) {
	outcome := verifyAudioProcessRegistration(writeRegistrationFixture(t, registrationCoordinator))
	require.True(t, outcome.Passed)
	require.True(t, outcome.Compiled)
	require.Empty(t, outcome.Failures)
}

func TestAudioProcessRegistrationRejectsNonExecutableMentions(t *testing.T) {
	variants := map[string]string{
		"missing":   `void ActionCoordinator::registerAllActions() { registerApplicationMenuActions(); }`,
		"comment":   `void ActionCoordinator::registerAllActions() { /* registerAudioProcessActions(); */ }`,
		"literal":   `void ActionCoordinator::registerAllActions() { auto text = "registerAudioProcessActions();"; }`,
		"outside":   registrationCoordinator + `void other() { registerAudioProcessActions(); }`,
		"duplicate": `void ActionCoordinator::registerAllActions() { registerAudioProcessActions(); registerAudioProcessActions(); }`,
	}
	for name, source := range variants {
		t.Run(name, func(t *testing.T) {
			outcome := verifyAudioProcessRegistration(writeRegistrationFixture(t, source))
			require.False(t, outcome.Passed)
			require.NotEmpty(t, outcome.Failures)
		})
	}
}

func TestAudioProcessRegistrationRejectsBrokenPinnedDependencies(t *testing.T) {
	root := writeRegistrationFixture(t, registrationCoordinator)
	require.NoError(t, os.WriteFile(filepath.Join(root, "src/ui/ActionCoordinator.h"), []byte("class ActionCoordinator {};"), 0o644))
	outcome := verifyAudioProcessRegistration(root)
	require.False(t, outcome.Passed)

	root = writeRegistrationFixture(t, registrationCoordinator)
	require.NoError(t, os.WriteFile(filepath.Join(root, "config/default_keymap.yaml"), []byte("normal: {}\n"), 0o644))
	outcome = verifyAudioProcessRegistration(root)
	require.False(t, outcome.Passed)
}

func TestCase07PinnedCanvasFixture(t *testing.T) {
	repo := os.Getenv("CANVASBENCH_CANVAS_REPO")
	if repo == "" {
		t.Skip("set CANVASBENCH_CANVAS_REPO to verify the pinned Canvas artifact")
	}
	manifestPath := filepath.Join("..", "..", "bench", "canvasbench-v2", "manifest.json")
	_, tasks, err := LoadManifest(manifestPath)
	require.NoError(t, err)
	var task TaskSpec
	for _, candidate := range tasks {
		if candidate.ID == "case-07" {
			task = candidate
			break
		}
	}
	require.Equal(t, "runnable", task.Status)
	prepared, err := PrepareFixture(repo, t.TempDir(), task.Fixture)
	require.NoError(t, err)
	defer prepared.Cleanup()

	failing := verifyAudioProcessRegistration(prepared.WorkDir)
	require.False(t, failing.Passed)
	path := filepath.Join(prepared.WorkDir, "src/ui/ActionCoordinator.cpp")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	needle := "    registerAutomationFollowAction();\n"
	require.Equal(t, 1, strings.Count(string(raw), needle))
	corrected := strings.Replace(string(raw), needle, needle+"    registerAudioProcessActions();\n", 1)
	require.NoError(t, os.WriteFile(path, []byte(corrected), 0o644))

	passing := verifyAudioProcessRegistration(prepared.WorkDir)
	require.True(t, passing.Passed, passing.Failures)
	require.True(t, passing.Compiled)
	changed, err := ChangedPaths(prepared.WorkDir)
	require.NoError(t, err)
	require.Equal(t, []string{"src/ui/ActionCoordinator.cpp"}, changed)
}
