package orchclient

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewUnixListsProjectsOverFilesystemSocket(t *testing.T) {
	socketDir, err := os.MkdirTemp("/tmp", "drem-orchclient-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "orch.sock")
	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/projects", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"canvas-local"}]`))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	projects, err := NewUnix(socketPath).ListProjects(t.Context())
	require.NoError(t, err)
	require.Len(t, projects, 1)
	require.Equal(t, "canvas-local", projects[0].Name)
}
