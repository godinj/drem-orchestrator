// Package serve implements the bridge HTTP server for the C-Suite mobile client.
// It exposes a bearer-token-authenticated REST API and a WebSocket endpoint for
// real-time message streaming over a single TCP listener.
package serve

import (
	"fmt"
	"net"
	"net/http"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/godinj/drem-orchestrator/internal/personacontrol"
	"github.com/google/uuid"
)

const defaultAddr = ":8080"

// dashboardStore is the storage interface required by this package.
// Defined at the consumption site per architecture guidelines.
type dashboardStore interface {
	AgentDashboard() ([]csuite.AgentDashboardRow, error)
	CreateMessage(msg *csuite.CsuiteInboxMessage) error
	GetMessagesBetween(agent1, agent2 string, limit int, beforeID uuid.UUID) ([]csuite.CsuiteInboxMessage, error)
	GetAcksByAgent(agent string, limit int) ([]csuite.CsuiteInboxMessage, error)
	GetMessageCountByAgent(scopedTo string) (int, error)
}

// Config holds the bridge HTTP server configuration.
type Config struct {
	Token          string // Bearer token for API authentication
	DisableAuth    bool   // When true, API and WebSocket auth checks are bypassed
	Addr           string // TCP address to listen on; defaults to ":8080" when empty
	Store          dashboardStore
	PersonaControl *personacontrol.Controller

	// DeliverHandler is an optional http.Handler mounted at /healthz
	// and /deliver. When non-nil the bridge Server composes the
	// outbox-routing endpoints alongside its own /api/* tree. Left nil
	// (e.g. in host-mode integration tests) the bridge operates without
	// routing support. See internal/deliver for the handler factory.
	DeliverHandler http.Handler
}

// Server is the bridge HTTP server. Zero value is not usable; use New.
type Server struct {
	cfg      Config
	listener net.Listener
	srv      *http.Server
	hub      *Hub
}

// New creates a Server from cfg. Call Start to begin accepting requests.
// If cfg.Addr is empty it defaults to defaultAddr.
func New(cfg Config) *Server {
	if cfg.Addr == "" {
		cfg.Addr = defaultAddr
	}
	if cfg.PersonaControl == nil {
		cfg.PersonaControl = personacontrol.NewFromEnv(nil)
	}
	return &Server{cfg: cfg, hub: NewHub()}
}

// Start binds the listener and begins serving HTTP in the background.
// ListenAddr returns the actual bound address after Start returns nil.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.Addr, err)
	}
	s.listener = ln
	s.srv = &http.Server{Handler: s.buildMux()}
	go s.srv.Serve(ln) //nolint:errcheck
	return nil
}

// Stop gracefully shuts down the server.
func (s *Server) Stop() error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Close()
}

// ListenAddr returns the address the server is bound to after Start returns.
// Returns an empty string before Start is called.
func (s *Server) ListenAddr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Hub returns the server's WebSocket hub, allowing callers to inspect
// connection state (e.g. for testing).
func (s *Server) Hub() *Hub {
	return s.hub
}

func (s *Server) buildMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/api/health", s.auth(http.HandlerFunc(healthHandler)))
	mux.Handle("/api/agents", s.auth(agentsHandler(s.cfg.Store)))
	mux.Handle("/api/inbox", s.auth(inboxQueueHandler(s.cfg.Store)))
	mux.Handle("/api/inbox/archive", s.auth(inboxQueueActionHandler(s.cfg.Store, inboxQueueActionArchive)))
	mux.Handle("/api/inbox/ignore", s.auth(inboxQueueActionHandler(s.cfg.Store, inboxQueueActionIgnore)))
	mux.Handle("/api/personas/containers", s.auth(personaContainersHandler(s.cfg.PersonaControl)))
	mux.Handle("/api/personas/control", s.auth(personaControlHandler(s.cfg.PersonaControl)))
	mux.Handle("/api/messages", s.auth(messagesHandler(s.cfg.Store, s.hub)))
	mux.Handle("/api/acks", s.auth(acksHandler(s.cfg.Store)))
	// WebSocket endpoint handles its own auth (token via query param or header)
	// because browsers cannot set custom headers on WebSocket upgrade requests.
	mux.Handle("/api/ws", wsHandler(s.hub, s.cfg.Store, s.cfg.Token, s.cfg.DisableAuth))
	// PRD-compatible alias for the mobile client. Keep /api/ws for existing
	// csuite-chat clients.
	mux.Handle("/ws", wsHandler(s.hub, s.cfg.Store, s.cfg.Token, s.cfg.DisableAuth))
	// Outbox-routing endpoints. /healthz (unauth liveness), /deliver
	// (X-Csuite-Token auth), and /rescan (X-Csuite-Token auth) are
	// all provided by the optional DeliverHandler. Registering them
	// as specific paths ahead of the "/" catch-all ensures they win
	// the mux lookup.
	//
	// The /v1/deliveries and /v1/queue audit endpoints (plan
	// csuite-audit-cli.md §V1 endpoint surface) are also served by
	// DeliverHandler but use their own bearer-token auth against
	// AuditToken rather than X-Csuite-Token.
	if s.cfg.DeliverHandler != nil {
		mux.Handle("/healthz", s.cfg.DeliverHandler)
		mux.Handle("/deliver", s.cfg.DeliverHandler)
		mux.Handle("/rescan", s.cfg.DeliverHandler)
		mux.Handle("/v1/deliveries", s.cfg.DeliverHandler)
		mux.Handle("/v1/queue", s.cfg.DeliverHandler)
	}
	// PWA static assets are served without auth — the browser needs to fetch
	// the manifest, service worker, and app shell before the user can log in.
	// The "/" pattern is a catch-all that serves index.html for unmatched paths.
	mux.Handle("/", pwaHandler())
	return mux
}

func (s *Server) auth(next http.Handler) http.Handler {
	if s.cfg.DisableAuth {
		return next
	}
	return BearerAuth(s.cfg.Token, next)
}
