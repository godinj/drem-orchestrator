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
	"time"

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

// loadDeliverToken resolves the shared secret used to authenticate
// POST /deliver (the outbox-routing fast path that matches watcher's
// X-Csuite-Token header against the persona's token). Resolution
// order mirrors cmd/csuite-persona/main.go:81-90 so both sides of
// the call agree on where the token lives when the host shell env
// is not populated:
//
//  1. CSUITE_WATCHER_TOKEN (env) — the legacy host-shell passthrough.
//     Docker compose inherits unset values from the shell at
//     `docker compose up` time; if the operator exported
//     CSUITE_WATCHER_TOKEN before bringing the stack up, this wins.
//  2. CSUITE_WATCHER_TOKEN_FILE (env pointing at a mounted file) —
//     the file-fallback path. The persona service blocks bind-mount
//     ~/.drem/csuite-watcher.token at /run/secrets/csuite-watcher-token
//     read-only and point CSUITE_WATCHER_TOKEN_FILE at that path; the
//     watcher picks up the same file via its own bind-mount. This
//     survives host-shell gaps between compose-ups (scoreboard item
//     33 — pre-fix symptom: every POST /deliver returned 401 because
//     the valueless env-inherit form landed empty, and every
//     persona→persona delivery fell back to the 5-minute rescan).
//
// An empty resolution (neither env var populated, or the file exists
// but trims to "") surfaces as a non-nil error so the caller can
// fail-closed at startup rather than silently booting a watcher that
// would reject every signal.
func loadDeliverToken() (string, error) {
	token := os.Getenv("CSUITE_WATCHER_TOKEN")
	if token == "" {
		if path := os.Getenv("CSUITE_WATCHER_TOKEN_FILE"); path != "" {
			b, rerr := os.ReadFile(path) //nolint:gosec // path is operator-controlled
			if rerr != nil {
				return "", fmt.Errorf("read CSUITE_WATCHER_TOKEN_FILE=%q: %w", path, rerr)
			}
			token = strings.TrimSpace(string(b))
		}
	}
	if token == "" {
		return "", fmt.Errorf("CSUITE_WATCHER_TOKEN not configured: set env or CSUITE_WATCHER_TOKEN_FILE")
	}
	return token, nil
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
// parseRescanInterval parses DREM_WATCHER_RESCAN_INTERVAL. Empty or
// malformed values fall back to the 5-minute default. A non-positive
// value (e.g. "0s") disables the periodic rescan entirely — useful in
// tests and for operators who want manual control via POST /rescan.
func parseRescanInterval(env string) time.Duration {
	const fallback = 5 * time.Minute
	if env == "" {
		return fallback
	}
	d, err := time.ParseDuration(env)
	if err != nil {
		return fallback
	}
	if d <= 0 {
		// Explicit disable.
		return 0
	}
	return d
}

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

	// Scoreboard item 33 (watcher-side mitigation): resolve the /deliver
	// token via env-or-file instead of a bare os.Getenv. The valueless
	// CSUITE_WATCHER_TOKEN: form in the compose template depends on the
	// host shell having the env var exported at `docker compose up` time;
	// if the operator forgot, the env lands empty and every POST /deliver
	// used to return 401 "watcher token not configured". The file-fallback
	// (CSUITE_WATCHER_TOKEN_FILE, backed by a read-only bind-mount the
	// template now wires by default) makes the resolution independent of
	// host shell state. Mirrors cmd/csuite-persona/main.go:81-90.
	deliverToken, err := loadDeliverToken()
	if err != nil {
		fmt.Fprintf(stderr, "error: load deliver token: %v\n", err)
		return 1
	}
	if os.Getenv("CSUITE_WATCHER_TOKEN") != "" {
		fmt.Fprintln(stderr, "csuite-watcher: loaded watcher token from env CSUITE_WATCHER_TOKEN")
	} else {
		fmt.Fprintf(stderr, "csuite-watcher: loaded watcher token from file %q\n",
			os.Getenv("CSUITE_WATCHER_TOKEN_FILE"))
	}

	deliverCfg := deliver.Config{
		Token:      deliverToken,
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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Startup rescan. Kicked off after the HTTP listener is bound so a
	// persona that races to signal during watcher boot sees an
	// accepting endpoint instead of a connection refused. The rescan
	// runs in a goroutine so a slow filesystem walk doesn't hold up
	// the main shutdown-watcher select below.
	go func() {
		res := deliver.RescanOnce(deliverCfg)
		fmt.Fprintf(stderr, "csuite-watcher: startup rescan: scanned=%d delivered=%d skipped=%d quarantined=%d errors=%d\n",
			res.Scanned, res.Delivered, res.Skipped, res.Quarantined, len(res.Errors))
	}()

	// Periodic rescan. Scoreboard item 5: a signal that silently drops
	// (token 401 prior to item 33's fix, transient network hiccup,
	// persona crash between write and signal) used to mean the file
	// sat in the outbox forever — startup rescan only ran once per
	// process lifetime. A periodic rescan every
	// DREM_WATCHER_RESCAN_INTERVAL (default 5 minutes) catches any
	// missed deliveries across every persona pair without operator
	// intervention. The ledger-hit skip inside Rescan keeps the cost
	// bounded as the outbox grows.
	rescanInterval := parseRescanInterval(os.Getenv("DREM_WATCHER_RESCAN_INTERVAL"))
	if rescanInterval > 0 {
		go func() {
			ticker := time.NewTicker(rescanInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					res := deliver.RescanOnce(deliverCfg)
					fmt.Fprintf(stderr, "csuite-watcher: periodic rescan: scanned=%d delivered=%d skipped=%d quarantined=%d errors=%d\n",
						res.Scanned, res.Delivered, res.Skipped, res.Quarantined, len(res.Errors))
				}
			}
		}()
	}

	<-ctx.Done()

	if err := srv.Stop(); err != nil {
		fmt.Fprintf(stderr, "error: stop server: %v\n", err)
		return 1
	}

	return 0
}
