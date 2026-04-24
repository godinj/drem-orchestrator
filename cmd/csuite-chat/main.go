// Package main implements csuite-chat, a terminal chat client for the
// drem-bridge C-Suite API. It connects to the same server the PWA uses
// and provides agent tabs, message threading, and real-time updates.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/godinj/drem-orchestrator/internal/bridgeclient"
	"github.com/godinj/drem-orchestrator/internal/tui/chat"
)

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("csuite-chat", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "", "bridge server address (env: DREM_BRIDGE_ADDR, default localhost:8080)")
	token := fs.String("token", "", "bearer token (env: DREM_BRIDGE_TOKEN)")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	cfg := resolveConfig(*addr, *token)
	if cfg.Token == "" {
		fmt.Fprintln(stderr, "error: token is required (set DREM_BRIDGE_TOKEN or --token)")
		return 1
	}

	baseURL := cfg.Addr
	if !strings.HasPrefix(baseURL, "http") {
		baseURL = "http://" + baseURL
	}

	client := bridgeclient.New(baseURL, cfg.Token)
	model := chat.New(client, client.WSURL())

	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

type chatConfig struct {
	Addr  string
	Token string
}

func resolveConfig(flagAddr, flagToken string) chatConfig {
	cfg := chatConfig{
		Addr:  os.Getenv("DREM_BRIDGE_ADDR"),
		Token: os.Getenv("DREM_BRIDGE_TOKEN"),
	}
	if flagAddr != "" {
		cfg.Addr = flagAddr
	}
	if flagToken != "" {
		cfg.Token = flagToken
	}
	if cfg.Addr == "" {
		cfg.Addr = "localhost:8080"
	}
	return cfg
}
