package projects_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/projects"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// TestLoad_MissingFileReturnsEmptyRegistry ensures that Load returns an
// empty registry (not an error) when projects.toml does not yet exist.
func TestLoad_MissingFileReturnsEmptyRegistry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope", "projects.toml")

	r, err := projects.Load(path)
	require.NoError(t, err)
	require.NotNil(t, r)
	require.Empty(t, r.Projects)
	require.Equal(t, path, r.Path)
}

// TestSaveLoadRoundTrip writes a populated registry, re-reads it from
// disk, and verifies every field survives serialisation.
func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	bareRepo := testutil.SetupBareRepo(t)
	path := filepath.Join(dir, "projects.toml")

	orig := &projects.Registry{
		Path: path,
		Projects: []projects.Project{
			{
				Name:         "drem-orchestrator",
				BareRepoPath: bareRepo,
				Language:     projects.LanguageGo,
				OrchURL:      "http://localhost:8080",
				ContainerImageOverrides: map[string]string{
					"coder": "drem-worker-go:v1.2.3",
				},
			},
		},
	}
	require.NoError(t, orig.Save())

	roundTrip, err := projects.Load(path)
	require.NoError(t, err)
	require.Len(t, roundTrip.Projects, 1)
	got := roundTrip.Projects[0]
	require.Equal(t, "drem-orchestrator", got.Name)
	require.Equal(t, bareRepo, got.BareRepoPath)
	require.Equal(t, projects.LanguageGo, got.Language)
	require.Equal(t, "http://localhost:8080", got.OrchURL)
	require.Equal(t, "drem-worker-go:v1.2.3", got.ContainerImageOverrides["coder"])
}

// TestAdd_RejectsDuplicate verifies that registering the same name twice
// is an error.
func TestAdd_RejectsDuplicate(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	r := &projects.Registry{Path: filepath.Join(t.TempDir(), "projects.toml")}

	p := projects.Project{
		Name:         "drem-orchestrator",
		BareRepoPath: bareRepo,
		Language:     projects.LanguageGo,
		OrchURL:      "http://localhost:8080",
	}
	require.NoError(t, r.Add(p))

	err := r.Add(p)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already exists")
}

// TestAdd_RejectsInvalidLanguage verifies that only "go" and "cpp" are
// accepted.
func TestAdd_RejectsInvalidLanguage(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	r := &projects.Registry{Path: filepath.Join(t.TempDir(), "projects.toml")}

	err := r.Add(projects.Project{
		Name:         "bad",
		BareRepoPath: bareRepo,
		Language:     "rust",
		OrchURL:      "http://localhost:8080",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported language")
}

// TestAdd_RejectsNonBareRepo verifies that BareRepoPath must actually be
// a bare repository layout (HEAD + objects/).
func TestAdd_RejectsNonBareRepo(t *testing.T) {
	r := &projects.Registry{Path: filepath.Join(t.TempDir(), "projects.toml")}
	notBare := t.TempDir()

	err := r.Add(projects.Project{
		Name:         "bad",
		BareRepoPath: notBare,
		Language:     projects.LanguageGo,
		OrchURL:      "http://localhost:8080",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a bare repo")
}

// TestAdd_RejectsMissingRequiredField covers the Name/OrchURL/etc required
// field paths in validateProject.
func TestAdd_RejectsMissingRequiredField(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	r := &projects.Registry{Path: filepath.Join(t.TempDir(), "projects.toml")}

	err := r.Add(projects.Project{
		Name:         "",
		BareRepoPath: bareRepo,
		Language:     projects.LanguageGo,
		OrchURL:      "http://localhost:8080",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "name is required")
}

// TestRemove_UnknownProject returns an error rather than silently
// succeeding.
func TestRemove_UnknownProject(t *testing.T) {
	r := &projects.Registry{Path: filepath.Join(t.TempDir(), "projects.toml")}
	err := r.Remove("nope")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

// TestRemove_Success verifies the happy path, and that Get returns false
// after removal.
func TestRemove_Success(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	r := &projects.Registry{Path: filepath.Join(t.TempDir(), "projects.toml")}

	require.NoError(t, r.Add(projects.Project{
		Name:         "drem-orchestrator",
		BareRepoPath: bareRepo,
		Language:     projects.LanguageGo,
		OrchURL:      "http://localhost:8080",
	}))

	require.NoError(t, r.Remove("drem-orchestrator"))
	_, ok := r.Get("drem-orchestrator")
	require.False(t, ok)
}

// TestDefaultPath smoke-tests the helper.
func TestDefaultPath(t *testing.T) {
	got := projects.DefaultPath()
	require.NotEmpty(t, got)

	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		require.Equal(t, filepath.Join(home, ".drem", "projects.toml"), got)
	}
}
