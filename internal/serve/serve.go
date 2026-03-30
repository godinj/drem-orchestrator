// Package serve implements the bridge HTTP server for the C-Suite mobile client.
// It exposes a bearer-token-authenticated REST API over a single TCP listener.
package serve

import (
	"net"
	"net/http"

	"github.com/godinj/drem-orchestrator/internal/csuite"
)

const defaultAddr = ":8080"

// dashboardStore is the storage interface required by this package.
// Defined at the consumption site per architecture guidelines.
type dashboardStore interface {
	AgentDashboard() ([]csuite.AgentDashboardRow, error)
}

// Config holds the bridge HTTP server configuration.
type Config struct {
	Token string // Bearer token for API authentication
	Addr  string // TCP address to listen on; defaults to ":8080" when empty
	Store dashboardStore
}

// Server is the bridge HTTP server. Zero value is not usable; use New.
type Server struct {
	cfg      Config
	listener net.Listener
	srv      *http.Server
}

// New creates a Server from cfg. Call Start to begin accepting requests.
// If cfg.Addr is empty it defaults to defaultAddr.
func New(cfg Config) *Server {
	if cfg.Addr == "" {
		cfg.Addr = defaultAddr
	}
	return &Server{cfg: cfg}
}

// Start binds the listener and begins serving HTTP in the background.
// ListenAddr returns the actual bound address after Start returns nil.
func (s *Server) Start() error {
	return nil
}

// Stop gracefully shuts down the server.
func (s *Server) Stop() error {
	return nil
}

// ListenAddr returns the address the server is bound to after Start returns.
// Returns an empty string before Start is called.
func (s *Server) ListenAddr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) buildMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/api/health", BearerAuth(s.cfg.Token, http.HandlerFunc(healthHandler)))
	mux.Handle("/api/agents", BearerAuth(s.cfg.Token, agentsHandler(s.cfg.Store)))
	return mux
}
