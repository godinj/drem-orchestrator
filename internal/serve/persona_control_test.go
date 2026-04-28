package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/personacontrol"
)

func TestPersonaControlValidationFailures(t *testing.T) {
	mux := New(Config{
		Token:          "secret",
		PersonaControl: personacontrol.New("compose.yml", &serveRecordingExecutor{}),
	}).buildMux()

	postPersonaControl(t, mux, `{"target":"ross","action":"stop"}`, http.StatusBadRequest)
	postPersonaControl(t, mux, `{"target":"mike","action":"rebuild"}`, http.StatusBadRequest)
}

func TestPersonaControlNotConfigured(t *testing.T) {
	mux := New(Config{
		Token:          "secret",
		PersonaControl: personacontrol.New("", &serveRecordingExecutor{}),
	}).buildMux()

	postPersonaControl(t, mux, `{"target":"mike","action":"stop"}`, http.StatusServiceUnavailable)

	req := httptest.NewRequest(http.MethodGet, "/api/personas/containers", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["available"] != false || body["reason"] != "not configured" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestPersonaControlExecutesAllowlistedArgv(t *testing.T) {
	exec := &serveRecordingExecutor{}
	mux := New(Config{
		Token:          "secret",
		PersonaControl: personacontrol.New("compose.yml", exec),
	}).buildMux()

	postPersonaControl(t, mux, `{"target":"all","action":"recreate"}`, http.StatusOK)

	want := []string{"docker", "compose", "-f", "compose.yml", "up", "-d", "--no-deps", "--force-recreate", "csuite-mike", "csuite-alex", "csuite-seth", "csuite-kyle"}
	if !reflect.DeepEqual(exec.argv, want) {
		t.Fatalf("argv = %#v, want %#v", exec.argv, want)
	}
}

func postPersonaControl(t *testing.T, mux http.Handler, body string, want int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/personas/control", bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("POST status = %d, want %d; body: %s", w.Code, want, w.Body.String())
	}
}

type serveRecordingExecutor struct {
	argv []string
}

func (e *serveRecordingExecutor) Run(_ context.Context, argv []string) error {
	e.argv = append([]string(nil), argv...)
	return nil
}
