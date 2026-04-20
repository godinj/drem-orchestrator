package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uniqNS returns a fresh MetricsNS on every call so parallel subtests do not
// collide in expvar's global registry. The package-level metrics registry
// already dedupes within a namespace, but using distinct namespaces keeps
// the per-test counters isolated so assertions on one test don't leak into
// another.
func uniqNS(prefix string) string {
	n := uniqCounter.Add(1)
	return prefix + "_" + itoaU64(n)
}

var uniqCounter atomic.Int64

func itoaU64(n int64) string {
	// Small hand-rolled conversion so tests stay dependency-light.
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// TestNew_PanicsOnMissingMetricsNS guards the API contract — an unnamed
// namespace would cause expvar collisions that are hard to debug.
func TestNew_PanicsOnMissingMetricsNS(t *testing.T) {
	defer func() {
		require.NotNil(t, recover(), "New must panic when MetricsNS is empty")
	}()
	New(Config{ListenAddr: ":0"})
}

// TestHandle_NoAuthWhenTokenEmpty verifies the dev/unit-test path: with no
// BearerToken configured, handlers run without auth middleware.
func TestHandle_NoAuthWhenTokenEmpty(t *testing.T) {
	s := New(Config{ListenAddr: ":0", MetricsNS: uniqNS("test")})
	s.Handle("POST", "/echo", func(w http.ResponseWriter, r *http.Request) {
		s.WriteJSON(w, http.StatusOK, map[string]string{"got": "ok"})
	})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/echo", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestHandle_401MissingToken verifies the auth middleware rejects requests
// that omit the Authorization header when a token is configured.
func TestHandle_401MissingToken(t *testing.T) {
	s := New(Config{ListenAddr: ":0", MetricsNS: uniqNS("test"), BearerToken: "secret"})
	s.Handle("POST", "/echo", func(w http.ResponseWriter, r *http.Request) {
		s.WriteJSON(w, http.StatusOK, nil)
	})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/echo", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestHandle_401WrongToken verifies constant-time comparison rejects
// incorrect tokens.
func TestHandle_401WrongToken(t *testing.T) {
	s := New(Config{ListenAddr: ":0", MetricsNS: uniqNS("test"), BearerToken: "secret"})
	s.Handle("POST", "/echo", func(w http.ResponseWriter, r *http.Request) {
		s.WriteJSON(w, http.StatusOK, nil)
	})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/echo", bytes.NewReader([]byte("{}")))
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestHandle_200CorrectToken verifies the happy-path through auth
// middleware.
func TestHandle_200CorrectToken(t *testing.T) {
	s := New(Config{ListenAddr: ":0", MetricsNS: uniqNS("test"), BearerToken: "secret"})
	s.Handle("POST", "/echo", func(w http.ResponseWriter, r *http.Request) {
		s.WriteJSON(w, http.StatusOK, map[string]string{"ok": "yes"})
	})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/echo", bytes.NewReader([]byte("{}")))
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestHealthz_NoProbeReturns200 confirms the documented default: without a
// HealthProbe wired in, /healthz reports healthy so the binary doesn't
// falsely gate on an optional collaborator.
func TestHealthz_NoProbeReturns200(t *testing.T) {
	s := New(Config{ListenAddr: ":0", MetricsNS: uniqNS("test")})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), `"ok":true`)
}

// TestHealthz_ProbeOKReturns200 verifies the probe wiring.
func TestHealthz_ProbeOKReturns200(t *testing.T) {
	s := New(Config{
		ListenAddr:  ":0",
		MetricsNS:   uniqNS("test"),
		HealthProbe: func(context.Context) error { return nil },
	})
	s.SetHealthProbeTTL(0) // force refresh every call
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestHealthz_ProbeFailReturns503 verifies that a failing precondition
// surfaces as 503 so upstream callers can fail fast.
func TestHealthz_ProbeFailReturns503(t *testing.T) {
	s := New(Config{
		ListenAddr:  ":0",
		MetricsNS:   uniqNS("test"),
		HealthProbe: func(context.Context) error { return errors.New("upstream down") },
	})
	s.SetHealthProbeTTL(0)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

// TestMetrics_ExposesExpvar verifies /metrics returns expvar JSON including
// the namespaced counters the caller registered implicitly through
// WriteError / RecordOK.
func TestMetrics_ExposesExpvar(t *testing.T) {
	ns := uniqNS("scraper")
	s := New(Config{ListenAddr: ":0", MetricsNS: ns})
	s.Handle("POST", "/op", func(w http.ResponseWriter, r *http.Request) {
		s.RecordOK(12 * time.Millisecond)
		s.WriteJSON(w, http.StatusOK, nil)
	})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// Drive traffic so the counter is non-zero.
	resp, err := http.Post(ts.URL+"/op", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	_ = resp.Body.Close()

	resp, err = http.Get(ts.URL + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw, _ := io.ReadAll(resp.Body)
	out := string(raw)
	assert.Contains(t, out, ns+"_requests_ok", "expected namespaced requests_ok counter")
	assert.Contains(t, out, ns+"_duration_ms_sum")
}

// TestHandleNoAuth_SkipsAuthEvenWithToken confirms HandleNoAuth bypasses
// the bearer check — used for /healthz + /metrics so Prometheus scrapers
// don't need the shared token.
func TestHandleNoAuth_SkipsAuthEvenWithToken(t *testing.T) {
	s := New(Config{ListenAddr: ":0", MetricsNS: uniqNS("test"), BearerToken: "secret"})
	s.HandleNoAuth("GET", "/public", func(w http.ResponseWriter, r *http.Request) {
		s.WriteJSON(w, http.StatusOK, map[string]string{"ok": "yes"})
	})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/public")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestWriteError_BumpsCounters verifies 4xx and 5xx counters increment
// correctly so operator dashboards can distinguish client-side failures
// from server-side bugs.
func TestWriteError_BumpsCounters(t *testing.T) {
	ns := uniqNS("errtest")
	s := New(Config{ListenAddr: ":0", MetricsNS: ns})
	s.Handle("POST", "/bad", func(w http.ResponseWriter, r *http.Request) {
		s.WriteError(w, http.StatusBadRequest, "bad")
	})
	s.Handle("POST", "/oops", func(w http.ResponseWriter, r *http.Request) {
		s.WriteError(w, http.StatusInternalServerError, "oops")
	})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	for _, path := range []string{"/bad", "/oops"} {
		resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader("{}"))
		require.NoError(t, err)
		_ = resp.Body.Close()
	}

	resp, err := http.Get(ts.URL + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.EqualValues(t, 1, decoded[ns+"_requests_4xx"])
	assert.EqualValues(t, 1, decoded[ns+"_requests_5xx"])
}
