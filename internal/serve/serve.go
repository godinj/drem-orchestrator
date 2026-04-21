// Package serve implements the bridge HTTP server for the C-Suite mobile client.
// It exposes a bearer-token-authenticated REST API and a WebSocket endpoint for
// real-time message streaming over a single TCP listener.
package serve

import (
	"fmt"
	"net"
	"net/http"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/google/uuid"
)

const defaultAddr = ":8080"

// dashboardStore is the storage interface required by this package.
// Defined at the consumption site per architecture guidelines.
type dashboardStore interface {
	AgentDashboard() ([]csuite.AgentDashboardRow, error)
	CreateMessage(msg *csuite.CsuiteInboxMessage) error
	GetMessagesBetween(agent1, agent2 string, limit int, beforeID uuid.UUID) ([]csuite.CsuiteInboxMessage, error)
	GetMessageCountByAgent(scopedTo string) (int, error)
}

// Config holds the bridge HTTP server configuration.
type Config struct {
	Token string // Bearer token for API authentication
	Addr  string // TCP address to listen on; defaults to ":8080" when empty
	Store dashboardStore

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
	mux.Handle("/api/health", BearerAuth(s.cfg.Token, http.HandlerFunc(healthHandler)))
	mux.Handle("/api/agents", BearerAuth(s.cfg.Token, agentsHandler(s.cfg.Store)))
	mux.Handle("/api/messages", BearerAuth(s.cfg.Token, messagesHandler(s.cfg.Store, s.hub)))
	// WebSocket endpoint handles its own auth (token via query param or header)
	// because browsers cannot set custom headers on WebSocket upgrade requests.
	mux.Handle("/api/ws", wsHandler(s.hub, s.cfg.Store, s.cfg.Token))
	// Outbox-routing endpoints. Both /healthz (unauth liveness) and
	// /deliver (X-Csuite-Token auth) are provided by the optional
	// DeliverHandler. Registering them as specific paths ahead of the
	// "/" catch-all ensures they win the mux lookup.
	if s.cfg.DeliverHandler != nil {
		mux.Handle("/healthz", s.cfg.DeliverHandler)
		mux.Handle("/deliver", s.cfg.DeliverHandler)
	}
	// PWA static assets are served without auth — the browser needs to fetch
	// the manifest, service worker, and app shell before the user can log in.
	// The "/" pattern is a catch-all that serves index.html for unmatched paths.
	mux.Handle("/", pwaHandler())
	return mux
}
