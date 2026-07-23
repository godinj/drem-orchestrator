package serviceauth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolvePrecedenceAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte("file-token\n"), 0o600))
	t.Setenv(TokenFileEnv, path)
	t.Setenv(LegacyEnv, "legacy-token")

	token, err := Resolve()
	require.NoError(t, err)
	require.Equal(t, "file-token", token)

	t.Setenv(TokenEnv, "explicit-token")
	token, err = Resolve()
	require.NoError(t, err)
	require.Equal(t, "explicit-token", token)
}

func TestResolveConfiguredFileFailsClosed(t *testing.T) {
	t.Setenv(TokenFileEnv, filepath.Join(t.TempDir(), "missing"))
	t.Setenv(LegacyEnv, "legacy-token")
	_, err := Resolve()
	require.Error(t, err)
}
