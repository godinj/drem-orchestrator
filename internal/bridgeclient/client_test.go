package bridgeclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRESTClientContracts(t *testing.T) {
	tests := []struct {
		name         string
		method       string
		path         string
		query        map[string]string
		wantContent  bool
		bodyContains []string
		respond      func(http.ResponseWriter)
		call         func(context.Context, *Client) error
	}{
		{
			name:   "GetAgents",
			method: http.MethodGet,
			path:   "/api/agents",
			respond: func(w http.ResponseWriter) {
				writeJSON(t, w, http.StatusOK, []Agent{{Name: "seth", Status: "online"}})
			},
			call: func(ctx context.Context, c *Client) error {
				agents, err := c.GetAgents(ctx)
				if err != nil {
					return err
				}
				if len(agents) != 1 || agents[0].Name != "seth" || agents[0].Status != "online" {
					return fmt.Errorf("decoded agents = %#v", agents)
				}
				return nil
			},
		},
		{
			name:   "GetMessages",
			method: http.MethodGet,
			path:   "/api/messages",
			query: map[string]string{
				"from":      "operator",
				"to":        "alex",
				"limit":     "25",
				"before_id": "msg-1",
			},
			respond: func(w http.ResponseWriter) {
				writeJSON(t, w, http.StatusOK, []Message{{ID: "msg-2", Subject: "hello"}})
			},
			call: func(ctx context.Context, c *Client) error {
				msgs, err := c.GetMessages(ctx, "operator", "alex", 25, "msg-1")
				if err != nil {
					return err
				}
				if len(msgs) != 1 || msgs[0].ID != "msg-2" || msgs[0].Subject != "hello" {
					return fmt.Errorf("decoded messages = %#v", msgs)
				}
				return nil
			},
		},
		{
			name:   "GetInboxQueue",
			method: http.MethodGet,
			path:   "/api/inbox",
			query: map[string]string{
				"agent": "mike",
				"limit": "10",
			},
			respond: func(w http.ResponseWriter) {
				writeJSON(t, w, http.StatusOK, []InboxQueueItem{{ID: "inbox-1", Filename: "one.md"}})
			},
			call: func(ctx context.Context, c *Client) error {
				items, err := c.GetInboxQueue(ctx, "mike", 10)
				if err != nil {
					return err
				}
				if len(items) != 1 || items[0].ID != "inbox-1" || items[0].Filename != "one.md" {
					return fmt.Errorf("decoded inbox queue = %#v", items)
				}
				return nil
			},
		},
		{
			name:         "ArchiveInboxItem",
			method:       http.MethodPost,
			path:         "/api/inbox/archive",
			wantContent:  true,
			bodyContains: []string{`"agent":"ross"`, `"id":"inbox-2"`, `"reason":"handled"`},
			respond: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusOK)
			},
			call: func(ctx context.Context, c *Client) error {
				return c.ArchiveInboxItem(ctx, "ross", "inbox-2", "handled")
			},
		},
		{
			name:         "IgnoreInboxItem",
			method:       http.MethodPost,
			path:         "/api/inbox/ignore",
			wantContent:  true,
			bodyContains: []string{`"agent":"alex"`, `"id":"inbox-3"`, `"reason":"noise"`},
			respond: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusOK)
			},
			call: func(ctx context.Context, c *Client) error {
				return c.IgnoreInboxItem(ctx, "alex", "inbox-3", "noise")
			},
		},
		{
			name:   "GetPersonaContainers",
			method: http.MethodGet,
			path:   "/api/personas/containers",
			respond: func(w http.ResponseWriter) {
				writeJSON(t, w, http.StatusOK, PersonaContainersResponse{Available: true, Items: []PersonaContainer{{Target: "seth", Status: "running"}}})
			},
			call: func(ctx context.Context, c *Client) error {
				containers, err := c.GetPersonaContainers(ctx)
				if err != nil {
					return err
				}
				if !containers.Available || len(containers.Items) != 1 || containers.Items[0].Target != "seth" {
					return fmt.Errorf("decoded persona containers = %#v", containers)
				}
				return nil
			},
		},
		{
			name:         "ControlPersonaContainer",
			method:       http.MethodPost,
			path:         "/api/personas/control",
			wantContent:  true,
			bodyContains: []string{`"target":"mike"`, `"action":"restart"`},
			respond: func(w http.ResponseWriter) {
				writeJSON(t, w, http.StatusOK, PersonaControlResult{Status: "ok", Target: "mike", Action: "restart", Services: []string{"drem-mike"}})
			},
			call: func(ctx context.Context, c *Client) error {
				result, err := c.ControlPersonaContainer(ctx, PersonaControlRequest{Target: "mike", Action: "restart"})
				if err != nil {
					return err
				}
				if result.Status != "ok" || result.Target != "mike" || result.Action != "restart" || len(result.Services) != 1 {
					return fmt.Errorf("decoded persona control result = %#v", result)
				}
				return nil
			},
		},
		{
			name:   "GetPersonaModels",
			method: http.MethodGet,
			path:   "/api/personas/models",
			respond: func(w http.ResponseWriter) {
				writeJSON(t, w, http.StatusOK, map[string]string{"seth": "opus"})
			},
			call: func(ctx context.Context, c *Client) error {
				models, err := c.GetPersonaModels(ctx)
				if err != nil {
					return err
				}
				if models["seth"] != "opus" {
					return fmt.Errorf("decoded persona models = %#v", models)
				}
				return nil
			},
		},
		{
			name:         "SetPersonaModel",
			method:       http.MethodPut,
			path:         "/api/personas/model",
			wantContent:  true,
			bodyContains: []string{`"target":"ross"`, `"model":"sonnet"`},
			respond: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusOK)
			},
			call: func(ctx context.Context, c *Client) error {
				return c.SetPersonaModel(ctx, "ross", "sonnet")
			},
		},
		{
			name:         "SendMessage",
			method:       http.MethodPost,
			path:         "/api/messages",
			wantContent:  true,
			bodyContains: []string{`"from_agent":"operator"`, `"to_agent":"seth"`, `"subject":"Decision"`},
			respond: func(w http.ResponseWriter) {
				writeJSON(t, w, http.StatusCreated, Message{ID: "msg-3", FromAgent: "operator", ToAgent: "seth"})
			},
			call: func(ctx context.Context, c *Client) error {
				msg, err := c.SendMessage(ctx, SendRequest{FromAgent: "operator", ToAgent: "seth", Subject: "Decision"})
				if err != nil {
					return err
				}
				if msg.ID != "msg-3" || msg.FromAgent != "operator" || msg.ToAgent != "seth" {
					return fmt.Errorf("decoded message = %#v", msg)
				}
				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tt.method {
					t.Errorf("method = %s, want %s", r.Method, tt.method)
				}
				if r.URL.Path != tt.path {
					t.Errorf("path = %s, want %s", r.URL.Path, tt.path)
				}
				for key, want := range tt.query {
					if got := r.URL.Query().Get(key); got != want {
						t.Errorf("query %s = %q, want %q", key, got, want)
					}
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
					t.Errorf("authorization = %q", got)
				}
				if tt.wantContent {
					if got := r.Header.Get("Content-Type"); got != "application/json" {
						t.Errorf("content-type = %q", got)
					}
					body := readBody(t, r)
					for _, want := range tt.bodyContains {
						if !strings.Contains(body, want) {
							t.Errorf("body %q missing %s", body, want)
						}
					}
				}
				tt.respond(w)
			}))
			defer server.Close()

			client := New(server.URL+"/", "test-token")
			if err := tt.call(context.Background(), client); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRESTClientErrorsOnUnexpectedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agents" {
			t.Errorf("path = %s", r.URL.Path)
		}
		http.Error(w, "bad bridge", http.StatusTeapot)
	}))
	defer server.Close()

	_, err := New(server.URL, "test-token").GetAgents(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unexpected status 418") || !strings.Contains(err.Error(), "bad bridge") {
		t.Fatalf("error = %q", err.Error())
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("write json: %v", err)
	}
}

func readBody(t *testing.T, r *http.Request) string {
	t.Helper()
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}
