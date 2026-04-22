package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/godinj/drem-orchestrator/internal/deliver"
	"github.com/godinj/drem-orchestrator/internal/serve"
)

// serveTomlConfig is the TOML structure for the [serve] section of drem.toml.
type serveTomlConfig struct {
	ListenAddr  string `toml:"listen_addr"`
	BearerToken string `toml:"bearer_token"`
	DBPath      string `toml:"db_path"`
}

// serveDremToml is used to unmarshal only the [serve] section from drem.toml.
type serveDremToml struct {
	Serve serveTomlConfig `toml:"serve"`
}

// loadServeConfig reads the [serve] section from a drem.toml file.
// If the file does not exist, it returns a zero-value config (no error) so
// defaults apply — consistent with loadWatcherConfig behaviour.
func loadServeConfig(path string) (serveTomlConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return serveTomlConfig{}, nil
		}
		return serveTomlConfig{}, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg serveDremToml
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return serveTomlConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg.Serve, nil
}

// DefaultAuditTokenPath is the operator-facing default location of
// the bearer token used to authenticate the `drem csuite audit`
// CLI. The CLI reads the same default when --token is not set, so
// these two constants must stay in sync. Overridable via
// DREM_AUDIT_TOKEN_PATH.
const DefaultAuditTokenPath = "~/.drem/csuite-watcher.token"

// auditTokenPath returns the expanded path to the audit token file,
// honouring DREM_AUDIT_TOKEN_PATH when set.
func auditTokenPath() string {
	if v := os.Getenv("DREM_AUDIT_TOKEN_PATH"); v != "" {
		return expandTilde(v)
	}
	return expandTilde(DefaultAuditTokenPath)
}

// loadAuditToken reads the audit token from path, enforcing file
// permissions of 0600 (owner-read/write only). Anything else —
// missing file, world-readable, or the expanded path does not exist —
// surfaces as a non-nil error so the caller can fail closed.
//
// The returned token is a trimmed string. Empty token files are
// treated as an error so a misconfigured watcher cannot
// accidentally serve an audit endpoint that matches every client's
// empty bearer.
func loadAuditToken(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	// Refuse to start if the file is group- or world-readable. The
	// check is "any permission bit beyond 0600" so a rare 0400 file
	// still works — the concerning cases are 0604, 0640, 0644, etc.
	mode := info.Mode().Perm()
	if mode&0o077 != 0 {
		return "", fmt.Errorf("token file %s has permissions %#o; must be 0600", path, mode)
	}
	data, err := os.ReadFile(path) //nolint:gosec // path pinned by caller
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", fmt.Errorf("token file %s is empty", path)
	}
	return tok, nil
}

// applyServeEnvOverrides mutates cfg, replacing any field whose corresponding
// env var is set. Empty env vars are ignored so the toml value (or zero-value
// default) survives.
//
// Mapping:
//
//	DREM_BEARER_TOKEN → cfg.BearerToken
//	DREM_LISTEN_ADDR  → cfg.ListenAddr
//	DREM_DB_PATH      → cfg.DBPath
func applyServeEnvOverrides(cfg *serveTomlConfig) {
	if v := os.Getenv("DREM_BEARER_TOKEN"); v != "" {
		cfg.BearerToken = v
	}
	if v := os.Getenv("DREM_LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("DREM_DB_PATH"); v != "" {
		cfg.DBPath = v
	}
}

// runServe handles the serve subcommand: loads config, opens the csuite DB,
// creates a Store, starts the bridge HTTP server, and blocks until SIGTERM or SIGINT.
func runServe(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "drem.toml", "path to the drem.toml config file")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	cfg, err := loadServeConfig(expandTilde(*configPath))
	if err != nil {
		fmt.Fprintf(stderr, "error: load config: %v\n", err)
		return 1
	}

	// 12-factor env-var overrides. Precedence: env > toml > default.
	// The container compose service passes these instead of mounting drem.toml,
	// so the binary must work without a config file when env is populated.
	applyServeEnvOverrides(&cfg)

	if cfg.BearerToken == "" {
		fmt.Fprintln(stderr, "error: bearer_token must be set in [serve] config or DREM_BEARER_TOKEN env var")
		return 1
	}

	dbPath := expandTilde(cfg.DBPath)
	if dbPath == "" {
		dbPath = expandTilde("~/.drem-csuite/csuite.db")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		fmt.Fprintf(stderr, "error: create DB directory: %v\n", err)
		return 1
	}

	gormLogger := logger.New(log.New(stderr, "", 0), logger.Config{LogLevel: logger.Silent})
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: gormLogger})
	if err != nil {
		fmt.Fprintf(stderr, "error: open database: %v\n", err)
		return 1
	}

	if err := db.AutoMigrate(&csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{}); err != nil {
		fmt.Fprintf(stderr, "error: migrate: %v\n", err)
		return 1
	}

	store := csuite.NewStore(db)

	listenAddr := cfg.ListenAddr
	if listenAddr == "" {
		listenAddr = ":8080"
	}

	// Outbox routing wiring — the /deliver and /healthz endpoints
	// described in plans/csuite-watcher-outbox-routing.md. The token
	// is intentionally a different secret from DREM_BEARER_TOKEN
	// (bridge API audience) so a leak of one does not compromise the
	// other.
	deliverDBPath := os.Getenv("CSUITE_WATCHER_DB_PATH")
	if deliverDBPath == "" {
		deliverDBPath = deliver.DefaultDBPath
	}
	deliverDBPath = expandTilde(deliverDBPath)
	ledger, err := deliver.OpenLedger(deliverDBPath)
	if err != nil {
		fmt.Fprintf(stderr, "error: open delivery ledger: %v\n", err)
		return 1
	}
	defer ledger.Close() //nolint:errcheck

	// Audit token for the /v1/* endpoints. Read from a 0600 file at
	// ~/.drem/csuite-watcher.token (overridable via DREM_AUDIT_TOKEN_PATH).
	// Missing or non-0600 files cause serve to fail-closed with a
	// diagnostic message; the operator must initialise the token
	// out-of-band. See plans/csuite-audit-cli.md §Auth flow.
	auditToken, err := loadAuditToken(auditTokenPath())
	if err != nil {
		fmt.Fprintf(stderr, "error: load audit token: %v\n", err)
		return 1
	}

	deliverCfg := deliver.Config{
		Token:      os.Getenv("CSUITE_WATCHER_TOKEN"),
		Ledger:     ledger,
		AuditToken: auditToken,
	}
	deliverHandler := deliver.Handler(deliverCfg)

	srv := serve.New(serve.Config{
		Token:          cfg.BearerToken,
		Addr:           listenAddr,
		Store:          store,
		DeliverHandler: deliverHandler,
	})

	if err := srv.Start(); err != nil {
		fmt.Fprintf(stderr, "error: start server: %v\n", err)
		return 1
	}

	// Startup rescan. Kicked off after the HTTP listener is bound so a
	// persona that races to signal during watcher boot sees an
	// accepting endpoint instead of a connection refused. The rescan
	// runs in a goroutine so a slow filesystem walk doesn't hold up
	// the main shutdown-watcher select below. Exactly-once per
	// process-start — the operator can re-run it via POST /rescan.
	go func() {
		res := deliver.RescanOnce(deliverCfg)
		fmt.Fprintf(stderr, "csuite-watcher: startup rescan: scanned=%d delivered=%d skipped=%d quarantined=%d errors=%d\n",
			res.Scanned, res.Delivered, res.Skipped, res.Quarantined, len(res.Errors))
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	<-ctx.Done()

	if err := srv.Stop(); err != nil {
		fmt.Fprintf(stderr, "error: stop server: %v\n", err)
		return 1
	}

	return 0
}
