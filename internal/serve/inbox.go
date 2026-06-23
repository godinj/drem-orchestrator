package serve

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/godinj/drem-orchestrator/internal/csuite"
)

type inboxQueueStore interface {
	ListInboxQueue(agent string, limit int) ([]csuite.InboxQueueItem, error)
	ArchiveInboxItem(agent, id, reason string) error
	IgnoreInboxItem(agent, id, reason string) error
}

type inboxQueueAction string

const (
	inboxQueueActionArchive inboxQueueAction = "archive"
	inboxQueueActionIgnore  inboxQueueAction = "ignore"
)

type inboxQueueActionRequest struct {
	Agent  string `json:"agent"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

func inboxQueueHandler(store any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		s, ok := store.(inboxQueueStore)
		if !ok {
			writeJSONError(w, http.StatusNotImplemented, "inbox queue store unavailable")
			return
		}

		q := r.URL.Query()
		agent := q.Get("agent")
		if agent == "" {
			writeJSONError(w, http.StatusBadRequest, "agent query parameter is required")
			return
		}

		limit := 0
		if ls := q.Get("limit"); ls != "" {
			n, err := strconv.Atoi(ls)
			if err != nil || n < 0 {
				writeJSONError(w, http.StatusBadRequest, "limit must be a non-negative integer")
				return
			}
			limit = n
		}

		if includeArchived := q.Get("include_archived"); includeArchived == "true" {
			writeJSONError(w, http.StatusBadRequest, "include_archived=true is not supported for live inbox queue")
			return
		}

		items, err := s.ListInboxQueue(agent, limit)
		if err != nil {
			writeInboxQueueError(w, err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items) //nolint:errcheck
	})
}

func inboxQueueActionHandler(store any, action inboxQueueAction) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		s, ok := store.(inboxQueueStore)
		if !ok {
			writeJSONError(w, http.StatusNotImplemented, "inbox queue store unavailable")
			return
		}

		var req inboxQueueActionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if req.Agent == "" || req.ID == "" {
			writeJSONError(w, http.StatusBadRequest, "agent and id are required")
			return
		}

		var err error
		switch action {
		case inboxQueueActionArchive:
			err = s.ArchiveInboxItem(req.Agent, req.ID, req.Reason)
		case inboxQueueActionIgnore:
			err = s.IgnoreInboxItem(req.Agent, req.ID, req.Reason)
		default:
			writeJSONError(w, http.StatusInternalServerError, "unknown inbox queue action")
			return
		}
		if err != nil {
			writeInboxQueueError(w, err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck
	})
}

func writeInboxQueueError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, csuite.ErrUnknownPersona):
		writeJSONError(w, http.StatusBadRequest, "unknown persona")
	case errors.Is(err, csuite.ErrInboxItemNotFound):
		writeJSONError(w, http.StatusNotFound, "inbox item not found")
	case errors.Is(err, csuite.ErrInboxItemConflict):
		writeJSONError(w, http.StatusConflict, "inbox item conflict")
	default:
		writeJSONError(w, http.StatusInternalServerError, "store unavailable")
	}
}
