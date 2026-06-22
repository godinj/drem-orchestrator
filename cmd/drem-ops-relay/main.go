// Command drem-ops-relay forwards read-only orchestrator events to Mike's
// C-Suite inbox using the existing disk protocol.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/godinj/drem-orchestrator/internal/opsrelay"
	"github.com/godinj/drem-orchestrator/pkg/orchclient"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	var types csvFlag
	var targets csvFlag
	fs := flag.NewFlagSet("drem-ops-relay", flag.ContinueOnError)
	orchURL := fs.String("orch-url", getenv("DREM_ORCH_URL", ""), "orchestrator HTTP URL")
	csuiteRoot := fs.String("csuite-root", defaultCsuiteRoot(), "C-Suite home root")
	cursor := fs.String("cursor", getenv("DREM_OPS_RELAY_CURSOR", "~/.drem/ops-relay.cursor"), "cursor state path")
	project := fs.String("project", getenv("DREM_PROJECT", ""), "project label to include in messages")
	recipient := fs.String("to", "mike", "C-Suite recipient")
	limit := fs.Int("limit", 100, "maximum events to fetch")
	interval := fs.Duration("interval", 0, "poll interval; 0 runs once and exits")
	fs.Var(&types, "type", "event type to include; repeat or comma-separate; default includes all")
	fs.Var(&targets, "new-value", "status target/new_value to include; repeat or comma-separate; default includes all")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*orchURL) == "" {
		fmt.Fprintln(os.Stderr, "error: --orch-url or DREM_ORCH_URL is required")
		return 2
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	cfg := opsrelay.Config{
		Source:         orchclient.New(*orchURL),
		CsuiteRoot:     expandTilde(*csuiteRoot),
		CursorPath:     expandTilde(*cursor),
		Limit:          *limit,
		OrchURL:        *orchURL,
		Project:        *project,
		Recipient:      *recipient,
		IncludeTypes:   types.set(),
		IncludeTargets: targets.set(),
		Now:            time.Now,
	}
	if *interval <= 0 {
		return pollAndPrint(ctx, cfg)
	}
	if code := pollAndPrint(ctx, cfg); code != 0 {
		return code
	}
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0
		case <-ticker.C:
			if code := pollAndPrint(ctx, cfg); code != 0 {
				return code
			}
		}
	}
}

func pollAndPrint(ctx context.Context, cfg opsrelay.Config) int {
	res, err := opsrelay.PollOnce(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("fetched=%d written=%d cursor=%s\n", res.Fetched, res.Written, res.Cursor.UTC().Format(time.RFC3339Nano))
	for _, path := range res.Paths {
		fmt.Println(path)
	}
	return 0
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func defaultCsuiteRoot() string {
	if root := os.Getenv("DREM_CSUITE_ROOT"); root != "" {
		return root
	}
	if project := strings.TrimSpace(os.Getenv("DREM_PROJECT")); project != "" {
		return filepath.Join("~", ".drem", "projects", project, "csuite")
	}
	return "~/.drem-csuite"
}

func expandTilde(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

type csvFlag []string

func (f *csvFlag) String() string { return strings.Join(*f, ",") }

func (f *csvFlag) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*f = append(*f, part)
		}
	}
	return nil
}

func (f csvFlag) set() map[string]struct{} {
	if len(f) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(f))
	for _, v := range f {
		out[v] = struct{}{}
	}
	return out
}
