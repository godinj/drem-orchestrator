package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/benchv2"
	"github.com/stretchr/testify/require"
)

func TestUsageAttestorCLIWiringRequiresPrivateAdminCredentialForExternalHarness(t *testing.T) {
	attestation := benchv2.UsageProxyAttestation{
		SourceState:  "source-state",
		Image:        "ghcr.io/godinj/canvasbench-usage-proxy@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ConfigSHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/admin/v1/attestation", request.URL.Path)
		require.Equal(t, "Bearer admin-secret", request.Header.Get("Authorization"))
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"source_state":"source-state","image":"ghcr.io/godinj/canvasbench-usage-proxy@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","config_sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}`))
	}))
	defer server.Close()
	harness := benchv2.HarnessConfig{
		Name: benchv2.AdapterQwenCode, UsageProxySourceState: attestation.SourceState,
		UsageProxyImage: attestation.Image, UsageProxyConfigSHA: attestation.ConfigSHA256,
	}
	_, err := usageAttestorForHarness(context.Background(), harness, "", "", "")
	require.ErrorContains(t, err, "requires usage proxy")

	tokenFile := filepath.Join(t.TempDir(), "admin.token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("admin-secret\n"), 0o600))
	attestor, err := usageAttestorForHarness(context.Background(), harness, server.URL, "http://usage-proxy:8080/v1", tokenFile)
	require.NoError(t, err)
	require.IsType(t, &benchv2.UsageProxyClient{}, attestor)

	require.NoError(t, os.Chmod(tokenFile, 0o644))
	_, err = usageAttestorForHarness(context.Background(), harness, server.URL, "http://usage-proxy:8080/v1", tokenFile)
	require.ErrorContains(t, err, "only by its owner")
}

func TestDirectHarnessDoesNotRequireUsageProxyCLIFlags(t *testing.T) {
	attestor, err := usageAttestorForHarness(context.Background(), benchv2.HarnessConfig{Name: benchv2.AdapterDirect}, "", "", "")
	require.NoError(t, err)
	require.Nil(t, attestor)
}

func TestUsageAttestorCLIWiringRejectsLiveProxyMismatchBeforeTrialStart(t *testing.T) {
	var trialStarts int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/admin/v1/trials" {
			trialStarts++
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"source_state":"live-source","image":"ghcr.io/godinj/canvasbench-usage-proxy@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","config_sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}`))
	}))
	defer server.Close()
	tokenFile := filepath.Join(t.TempDir(), "admin.token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("admin-secret\n"), 0o600))
	harness := benchv2.HarnessConfig{
		Name: benchv2.AdapterQwenCode, UsageProxySourceState: "matrix-source",
		UsageProxyImage:     "ghcr.io/godinj/canvasbench-usage-proxy@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		UsageProxyConfigSHA: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	_, err := usageAttestorForHarness(context.Background(), harness, server.URL, "http://usage-proxy:8080/v1", tokenFile)
	require.ErrorContains(t, err, "does not match the matrix")
	require.Zero(t, trialStarts)
}
