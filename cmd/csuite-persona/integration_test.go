//go:build integration

// Package main — integration_test.go wires the real persona.Poller
// against a real os/exec.CommandContext-backed Spawner, using a stub
// `opencode` binary compiled into a temp directory and prepended to
// $PATH. This catches argv/prompt/ENV regressions that the
// SpawnerFunc-mocked unit tests in internal/csuite/persona cannot see.
//
// The file is build-tag gated (`//go:build integration`) so the main
// test sweep opts in with `go test -tags=integration ./...`. Default
// `go test ./...` does not compile this file, so there is no risk of
// a CI slowdown from the opencode-stub build step. The test additionally
// short-circuits to t.Skip when a Go toolchain is not on PATH.
//
// Why a real subprocess rather than extending the unit-test mocks: the
// Dockerfile swap from csuite-run.sh to csuite-persona introduces a
// fresh PATH and CWD for the OpenCode invocation. If either regresses
// (e.g. an env-var leakage, a $HOME mismatch, a prompt argument
// surprise with exec.Cmd.Run), the unit tests would not notice because
// they never hand the argv to os/exec. This test exercises the real
// os/exec pipeline end-to-end.

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/csuite/persona"
)

// TestIntegration_InboxToOutboxRoundTrip builds a trivial `codex` stub
// binary whose main simply echoes its positional arg, prepends that
// binary's directory to $PATH, starts the poller, and asserts the
// inbox-to-outbox flow works end-to-end.
func TestIntegration_InboxToOutboxRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; skipping integration test")
	}

	root := t.TempDir()
	t.Setenv("HOME", root)
	personaRoot := filepath.Join(root, ".drem-csuite", "seth")
	inbox := filepath.Join(personaRoot, "inbox")
	outbox := filepath.Join(personaRoot, "outbox")
	archive := filepath.Join(inbox, ".archive")
	state := filepath.Join(personaRoot, "state.md")
	prompt := filepath.Join(root, "prompts", "seth.md")
	binDir := filepath.Join(root, "bin")

	for _, d := range []string{inbox, outbox, archive, filepath.Dir(prompt), binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(prompt, []byte("Seth persona prompt."), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	// Build the stub `opencode` binary. Keeping its source in a child
	// tempdir (not binDir) so the compiled binary is the only thing
	// the PATH prefix exposes.
	stubSrc := filepath.Join(root, "stub", "main.go")
	if err := os.MkdirAll(filepath.Dir(stubSrc), 0o755); err != nil {
		t.Fatalf("mkdir stub src: %v", err)
	}
	if err := os.WriteFile(stubSrc, []byte(stubOpenCodeSource), 0o644); err != nil {
		t.Fatalf("write stub source: %v", err)
	}
	stubBin := filepath.Join(binDir, "opencode")
	build := exec.Command("go", "build", "-o", stubBin, stubSrc)
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build stub codex: %v", err)
	}

	// Prepend binDir to PATH for the duration of the test.
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)

	cfg := persona.Config{
		Persona:       "seth",
		InboxDir:      inbox,
		OutboxDir:     outbox,
		StateFile:     state,
		ArchiveDir:    archive,
		PromptFile:    prompt,
		PollInterval:  50 * time.Millisecond,
		ClaudeTimeout: 10 * time.Second,
		MaxFailures:   2,
		Now:           time.Now,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	p, err := persona.New(cfg, persona.NewOpenCodeSpawner())
	if err != nil {
		t.Fatalf("persona.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var runWG sync.WaitGroup
	runWG.Add(1)
	go func() {
		defer runWG.Done()
		_ = p.Run(ctx)
	}()

	// Drop a message into the inbox. The stub binary echoes back its
	// -p argument, so the outbox file body must contain it verbatim.
	msgBody := "integration test ping"
	msgPath := filepath.Join(inbox, "001-ping.md")
	if err := os.WriteFile(msgPath, []byte(msgBody), 0o644); err != nil {
		t.Fatalf("write inbox msg: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var outEntries []string
	for time.Now().Before(deadline) {
		outEntries = nil
		entries, err := os.ReadDir(outbox)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					outEntries = append(outEntries, e.Name())
				}
			}
		}
		if len(outEntries) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	runWG.Wait()

	if len(outEntries) != 1 {
		t.Fatalf("outbox entries: want 1, got %d (%v)", len(outEntries), outEntries)
	}
	body, err := os.ReadFile(filepath.Join(outbox, outEntries[0]))
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if !strings.Contains(string(body), msgBody) {
		t.Fatalf("outbox body %q does not contain the user message %q",
			string(body), msgBody)
	}

	// Inbox must be empty (just the .archive subdir).
	leftovers := []string{}
	entries, err := os.ReadDir(inbox)
	if err != nil {
		t.Fatalf("readdir inbox: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			leftovers = append(leftovers, e.Name())
		}
	}
	if len(leftovers) != 0 {
		t.Fatalf("inbox not empty: %v", leftovers)
	}

	// Archive must contain the original filename.
	archived, err := os.ReadDir(archive)
	if err != nil {
		t.Fatalf("readdir archive: %v", err)
	}
	if len(archived) != 1 || archived[0].Name() != "001-ping.md" {
		var names []string
		for _, a := range archived {
			names = append(names, a.Name())
		}
		t.Fatalf("archive want [001-ping.md], got %v", names)
	}

	// state.md must record the message as last_processed with ok status.
	stateBody, err := os.ReadFile(state)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	for _, needle := range []string{"last_processed: 001-ping.md", "last_status: ok"} {
		if !strings.Contains(string(stateBody), needle) {
			t.Fatalf("state.md missing %q\nfull:\n%s", needle, stateBody)
		}
	}
}

// stubOpenCodeSource is a tiny Go program that mimics the minimum surface
// of `opencode run <prompt>` that the poller uses. It reads the full turn
// prompt from the final argv element and writes a well-formed outbox file so
// the poller suppresses stdout.
const stubOpenCodeSource = `package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	body := ""
	if len(os.Args) > 1 {
		body = os.Args[len(os.Args)-1]
	}
	out := filepath.Join(os.Getenv("HOME"), ".drem-csuite", "seth", "outbox")
	_ = os.MkdirAll(out, 0755)
	name := time.Now().UTC().Format("20060102T150405Z") + "-seth-to-kyle-integration.md"
	_ = os.WriteFile(filepath.Join(out, name), []byte("---\nfrom: seth\nto: kyle\n---\n\n"+body), 0644)
	fmt.Println(` + "`" + `{"type":"done"}` + "`" + `)
}
`
