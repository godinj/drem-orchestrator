package serve

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/godinj/drem-orchestrator/internal/personacontrol"
)

type personaControlRequest struct {
	Target string `json:"target"`
	Action string `json:"action"`
}

func personaContainersHandler(controller *personacontrol.Controller) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(controller.ListContainers()) //nolint:errcheck
	})
}

func personaControlHandler(controller *personacontrol.Controller) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		var req personaControlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if req.Target == "" || req.Action == "" {
			writeJSONError(w, http.StatusBadRequest, "target and action are required")
			return
		}

		result, err := controller.Control(r.Context(), req.Target, req.Action)
		if err != nil {
			writePersonaControlError(w, err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result) //nolint:errcheck
	})
}

func writePersonaControlError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, personacontrol.ErrNotConfigured):
		writeJSONError(w, http.StatusServiceUnavailable, "persona container control is not configured")
	case errors.Is(err, personacontrol.ErrUnknownTarget):
		writeJSONError(w, http.StatusBadRequest, "unknown target")
	case errors.Is(err, personacontrol.ErrUnknownAction):
		writeJSONError(w, http.StatusBadRequest, "unknown action")
	default:
		writeJSONError(w, http.StatusInternalServerError, "persona container control failed")
	}
}
