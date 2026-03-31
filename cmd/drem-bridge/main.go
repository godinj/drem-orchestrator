// Package main implements the standalone drem-bridge binary that serves the
// C-Suite HTTP/WS API backed by csuite.db. It imports only internal/serve and
// internal/csuite — no orchestrator packages.
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
	"github.com/godinj/drem-orchestrator/internal/serve"
)

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("drem-bridge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	token := fs.String("token", "", "bearer token (env: DREM_BRIDGE_TOKEN)")
	listen := fs.String("listen", "", "listen address (env: DREM_BRIDGE_ADDR, default :8080)")
	dbFlag := fs.String("db", "", "csuite.db path (env: CSUITE_DB, default ~/.drem-csuite/csuite.db)")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	cfg := resolveConfig(*token, *listen, *dbFlag)
	if cfg.Token == "" {
		fmt.Fprintln(stderr, "error: token is required (set DREM_BRIDGE_TOKEN or --token)")
		return 1
	}

	dbPath := expandTilde(cfg.DBPath)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		fmt.Fprintf(stderr, "error: create DB directory: %v\n", err)
		return 1
	}

	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=5000", dbPath)
	gormLog := logger.New(log.New(stderr, "", 0), logger.Config{LogLevel: logger.Silent})
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormLog})
	if err != nil {
		fmt.Fprintf(stderr, "error: open database: %v\n", err)
		return 1
	}
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	}()

	if err := db.AutoMigrate(&csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{}); err != nil {
		fmt.Fprintf(stderr, "error: migrate: %v\n", err)
		return 1
	}

	store := csuite.NewStore(db)
	srv := serve.New(serve.Config{Token: cfg.Token, Addr: cfg.Addr, Store: store})
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

// bridgeConfig holds resolved configuration values.
type bridgeConfig struct {
	Token  string
	Addr   string
	DBPath string
}

// resolveConfig applies flag > env > default precedence.
func resolveConfig(flagToken, flagListen, flagDB string) bridgeConfig {
	cfg := bridgeConfig{
		Token:  os.Getenv("DREM_BRIDGE_TOKEN"),
		Addr:   os.Getenv("DREM_BRIDGE_ADDR"),
		DBPath: os.Getenv("CSUITE_DB"),
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
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "~/.drem-csuite/csuite.db"
	}
	return cfg
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
