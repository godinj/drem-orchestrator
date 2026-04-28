package serve

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/godinj/drem-orchestrator/internal/csuite/diskstore"
)

func TestPersonaModelEndpoints(t *testing.T) {
	root := t.TempDir()
	mux := New(Config{Token: "secret", Store: diskstore.New(root)}).buildMux()

	putPersonaModel(t, mux, `{"target":"seth","model":"openai/gpt-5.5"}`, http.StatusOK)

	req := httptest.NewRequest(http.MethodGet, "/api/personas/models", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var models map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &models); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if models["seth"] != csuite.PersonaModelGPT55 {
		t.Fatalf("seth model = %q, want %q", models["seth"], csuite.PersonaModelGPT55)
	}
	if models["mike"] != csuite.DefaultPersonaModel {
		t.Fatalf("mike model = %q, want fallback %q", models["mike"], csuite.DefaultPersonaModel)
	}

	raw, err := os.ReadFile(filepath.Join(root, "seth", "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"model": "openai/gpt-5.5"`)) {
		t.Fatalf("config missing model: %s", raw)
	}
}

func TestPersonaModelEndpointValidationFailures(t *testing.T) {
	mux := New(Config{Token: "secret", Store: diskstore.New(t.TempDir())}).buildMux()

	putPersonaModel(t, mux, `{"target":"ross","model":"openai/gpt-5.5"}`, http.StatusBadRequest)
	putPersonaModel(t, mux, `{"target":"seth","model":"not-real"}`, http.StatusBadRequest)
}

func putPersonaModel(t *testing.T, mux http.Handler, body string, want int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/personas/model", bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("PUT status = %d, want %d; body: %s", w.Code, want, w.Body.String())
	}
}
