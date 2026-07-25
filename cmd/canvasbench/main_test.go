package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/benchv2"
	"github.com/stretchr/testify/require"
)

func TestUsageAttestorCLIWiringRequiresPrivateAdminCredentialForExternalHarness(t *testing.T) {
	harness := benchv2.HarnessConfig{Name: benchv2.AdapterQwenCode}
	_, err := usageAttestorForHarness(harness, "", "", "")
	require.ErrorContains(t, err, "requires usage proxy")

	tokenFile := filepath.Join(t.TempDir(), "admin.token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("admin-secret\n"), 0o600))
	attestor, err := usageAttestorForHarness(harness, "http://127.0.0.1:18091", "http://usage-proxy:8080/v1", tokenFile)
	require.NoError(t, err)
	require.IsType(t, &benchv2.UsageProxyClient{}, attestor)

	require.NoError(t, os.Chmod(tokenFile, 0o644))
	_, err = usageAttestorForHarness(harness, "http://127.0.0.1:18091", "http://usage-proxy:8080/v1", tokenFile)
	require.ErrorContains(t, err, "only by its owner")
}

func TestDirectHarnessDoesNotRequireUsageProxyCLIFlags(t *testing.T) {
	attestor, err := usageAttestorForHarness(benchv2.HarnessConfig{Name: benchv2.AdapterDirect}, "", "", "")
	require.NoError(t, err)
	require.Nil(t, attestor)
}
