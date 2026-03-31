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
	"syscall"

	"github.com/BurntSushi/toml"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/godinj/drem-orchestrator/internal/csuite"
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

	if cfg.BearerToken == "" {
		fmt.Fprintln(stderr, "error: bearer_token must be set in [serve] config")
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

	srv := serve.New(serve.Config{
		Token: cfg.BearerToken,
		Addr:  listenAddr,
		Store: store,
	})

	if err := srv.Start(); err != nil {
		fmt.Fprintf(stderr, "error: start server: %v\n", err)
		return 1
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	<-ctx.Done()

	if err := srv.Stop(); err != nil {
		fmt.Fprintf(stderr, "error: stop server: %v\n", err)
		return 1
	}

	return 0
}
