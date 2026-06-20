// Package main implements the standalone drem-bridge binary that serves the
// C-Suite HTTP/WS API backed by the disk C-Suite state tree. It imports only
// internal/serve and internal/csuite packages — no orchestrator packages.
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

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/godinj/drem-orchestrator/internal/csuite/diskstore"
	"github.com/godinj/drem-orchestrator/internal/personacontrol"
	"github.com/godinj/drem-orchestrator/internal/serve"
)

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("drem-bridge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	token := fs.String("token", "", "bearer token (env: DREM_BRIDGE_TOKEN)")
	listen := fs.String("listen", "", "listen address (env: DREM_BRIDGE_ADDR, default :8080)")
	dbFlag := fs.String("db", "", "csuite.db path (env: CSUITE_DB, default <csuite-root>/csuite.db)")
	noAuth := fs.Bool("no-auth", false, "disable bearer token authentication (env: DREM_BRIDGE_NO_AUTH=true)")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	cfg := resolveConfig(*token, *listen, *dbFlag, *noAuth)
	if cfg.Token == "" && !cfg.NoAuth {
		fmt.Fprintln(stderr, "error: token is required (set DREM_BRIDGE_TOKEN or --token)")
		return 1
	}
	if cfg.NoAuth {
		fmt.Fprintln(stderr, "warning: authentication disabled; bind only to trusted networks")
	}

	srv, cleanup, err := newBridgeServer(cfg, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer cleanup()

	if err := srv.Start(); err != nil {
		fmt.Fprintf(stderr, "error: start server: %v\n", err)
		return 1
	}
	fmt.Fprintf(stderr, "drem-bridge listening on %s\n", srv.ListenAddr())

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	<-ctx.Done()

	if err := srv.Stop(); err != nil {
		fmt.Fprintf(stderr, "error: stop server: %v\n", err)
		return 1
	}
	return 0
}

func newBridgeServer(cfg bridgeConfig, stderr io.Writer) (*serve.Server, func(), error) {
	cleanup, err := migrateLegacyDB(cfg.DBPath, stderr)
	if err != nil {
		return nil, nil, err
	}

	diskRoot := expandTilde(cfg.DiskRoot)
	store := diskstore.New(diskRoot)
	srv := serve.New(serve.Config{
		Token:          cfg.Token,
		DisableAuth:    cfg.NoAuth,
		Addr:           cfg.Addr,
		Store:          store,
		PersonaControl: personacontrol.NewFromEnv(nil),
	})
	return srv, cleanup, nil
}

func migrateLegacyDB(dbPath string, stderr io.Writer) (func(), error) {
	dbPath = expandTilde(dbPath)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, fmt.Errorf("create DB directory: %w", err)
	}

	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=5000", dbPath)
	gormLog := logger.New(log.New(stderr, "", 0), logger.Config{LogLevel: logger.Silent})
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormLog})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	cleanup := func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	}

	if err := db.AutoMigrate(&csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{}); err != nil {
		cleanup()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return cleanup, nil
}

// bridgeConfig holds resolved configuration values.
type bridgeConfig struct {
	Token    string
	Addr     string
	DBPath   string
	DiskRoot string
	NoAuth   bool
}

// resolveConfig applies flag > env > default precedence.
func resolveConfig(flagToken, flagListen, flagDB string, flagNoAuth bool) bridgeConfig {
	cfg := bridgeConfig{
		Token:    os.Getenv("DREM_BRIDGE_TOKEN"),
		Addr:     os.Getenv("DREM_BRIDGE_ADDR"),
		DBPath:   os.Getenv("CSUITE_DB"),
		DiskRoot: resolveDefaultCsuiteRoot(),
		NoAuth:   parseBoolEnv(os.Getenv("DREM_BRIDGE_NO_AUTH")),
	}
	if flagToken != "" {
		cfg.Token = flagToken
	}
	if flagListen != "" {
		cfg.Addr = flagListen
	}
	if flagDB != "" {
		cfg.DBPath = flagDB
	}
	if flagNoAuth {
		cfg.NoAuth = true
	}
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(cfg.DiskRoot, "csuite.db")
	}
	return cfg
}

func resolveDefaultCsuiteRoot() string {
	if root := os.Getenv("DREM_CSUITE_ROOT"); root != "" {
		return root
	}
	if project := strings.TrimSpace(os.Getenv("DREM_PROJECT")); project != "" {
		return filepath.Join("~", ".drem", "projects", project, "csuite")
	}
	return "~/.drem-csuite"
}

func parseBoolEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}
