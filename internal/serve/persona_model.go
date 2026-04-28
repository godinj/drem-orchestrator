package serve

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/godinj/drem-orchestrator/internal/csuite"
)

type personaModelStore interface {
	PersonaModels() (map[string]string, error)
	SetPersonaModel(persona, model string) error
}

type personaModelRequest struct {
	Target string `json:"target"`
	Model  string `json:"model"`
}

func personaModelsHandler(store any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s, ok := store.(personaModelStore)
		if !ok {
			writeJSONError(w, http.StatusNotImplemented, "persona model store unavailable")
			return
		}
		models, err := s.PersonaModels()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "store unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(models) //nolint:errcheck
	})
}

func personaModelHandler(store any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s, ok := store.(personaModelStore)
		if !ok {
			writeJSONError(w, http.StatusNotImplemented, "persona model store unavailable")
			return
		}

		var req personaModelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if req.Target == "" || req.Model == "" {
			writeJSONError(w, http.StatusBadRequest, "target and model are required")
			return
		}
		model, err := csuite.NormalizePersonaModel(req.Model)
		if err != nil {
			writePersonaModelError(w, err)
			return
		}

		if err := s.SetPersonaModel(req.Target, model); err != nil {
			writePersonaModelError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"target": req.Target, "model": model}) //nolint:errcheck
	})
}

func writePersonaModelError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, csuite.ErrUnknownPersona):
		writeJSONError(w, http.StatusBadRequest, "unknown target")
	case errors.Is(err, csuite.ErrInvalidPersonaModel):
		writeJSONError(w, http.StatusBadRequest, "invalid model")
	default:
		writeJSONError(w, http.StatusInternalServerError, "store unavailable")
	}
}
