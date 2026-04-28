package persona_test

// poller_test.go exercises the Wave-2 csuite-persona polling loop end-to-end
// without invoking the real `opencode` binary. All filesystem activity is rooted
// at t.TempDir() so each test is hermetic; the Spawner interface is mocked
// via persona.SpawnerFunc so the argv, prompt, and exit-code paths are all
// observable.
//
// The tests here assert the external contract documented in the package-level
// doc comment (internal/csuite/persona/persona.go):
//
//   - Deterministic mtime-ordered pickup of *.md inbox files.
//   - Argv shape for `opencode run --format json --agent build`.
//   - Outbox filename carrying persona + timestamp.
//   - State-file atomic replacement (no partial writes).
//   - Inbox -> archive transition on success.
//   - Sidecar .failures counter + .failed archival after MaxFailures.
//   - SIGTERM-style context cancellation returns cleanly within one poll cycle.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/artifactregistry"
	"github.com/godinj/drem-orchestrator/internal/csuite/persona"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// ---------------------------------------------------------------------------
// Test harness.
// ---------------------------------------------------------------------------

// testFS is the fully-populated directory tree used by every test. Each
// sub-path corresponds to the field of the same name on persona.Config.
type testFS struct {
	root       string
	inboxDir   string
	outboxDir  string
	stateFile  string
	archiveDir string
	promptFile string
}

// newTestFS builds a tmpdir layout that mirrors what the compose bind-mounts
// create at runtime. Returning the paths rather than a persona.Config lets
// individual tests override just the piece they care about (e.g. poll
// interval).
func newTestFS(t *testing.T, promptBody string) testFS {
	t.Helper()
	root := t.TempDir()
	inbox := filepath.Join(root, "inbox")
	outbox := filepath.Join(root, "outbox")
	archive := filepath.Join(inbox, ".archive")
	ignored := filepath.Join(inbox, ".ignored")
	state := filepath.Join(root, "state.md")
	prompt := filepath.Join(root, "prompts", "seth.md")

	for _, d := range []string{inbox, outbox, archive, ignored, filepath.Dir(prompt)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(prompt, []byte(promptBody), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	return testFS{
		root:       root,
		inboxDir:   inbox,
		outboxDir:  outbox,
		stateFile:  state,
		archiveDir: archive,
		promptFile: prompt,
	}
}

// baseConfig returns a persona.Config wired against fs with sensible test
// defaults. PollInterval is intentionally tight so the loop spins quickly.
func baseConfig(fs testFS) persona.Config {
	return persona.Config{
		Persona:       "seth",
		InboxDir:      fs.inboxDir,
		OutboxDir:     fs.outboxDir,
		StateFile:     fs.stateFile,
		ArchiveDir:    fs.archiveDir,
		PromptFile:    fs.promptFile,
		PollInterval:  20 * time.Millisecond,
		ClaudeTimeout: time.Second,
		MaxFailures:   3,
		Now:           time.Now,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// writeInboxMessage drops a message into the inbox with an explicit mtime so
// tests can verify mtime-ordered pickup.
func writeInboxMessage(t *testing.T, fs testFS, name, body string, when time.Time) {
	t.Helper()
	path := filepath.Join(fs.inboxDir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if !when.IsZero() {
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}
}

// spawnCall records a single invocation of the Spawner interface so tests
// can assert on argv shape, prompt content, and invocation ordering.
type spawnCall struct {
	argv   []string
	stdin  []byte
	ctxErr error
}

func (c spawnCall) promptArg() string {
	if len(c.argv) == 0 {
		return ""
	}
	return c.argv[len(c.argv)-1]
}

// recorderSpawner returns a Spawner that records every call into calls and
// returns stdout/exitCode supplied by handler. handler is called under a
// mutex so concurrent tests stay deterministic.
func recorderSpawner(calls *[]spawnCall, mu *sync.Mutex, handler func(call spawnCall) ([]byte, int, error)) persona.Spawner {
	return persona.SpawnerFunc(func(ctx context.Context, args []string, stdin io.Reader) ([]byte, int, error) {
		var body []byte
		if stdin != nil {
			b, err := io.ReadAll(stdin)
			if err != nil {
				return nil, -1, err
			}
			body = b
		}
		call := spawnCall{argv: append([]string(nil), args...), stdin: body, ctxErr: ctx.Err()}
		mu.Lock()
		*calls = append(*calls, call)
		mu.Unlock()
		if handler == nil {
			return []byte("ok"), 0, nil
		}
		return handler(call)
	})
}

type failingAdmissionReporter struct{}

func (failingAdmissionReporter) AdmitArtifacts(context.Context, artifactregistry.AdmissionRequest, []artifactregistry.Artifact) (*artifactregistry.AdmissionResult, error) {
	return nil, errors.New("admission reporter unavailable")
}

// runPollerUntil launches p.Run in a goroutine and cancels ctx when cond
// returns true or after timeout expires. It returns the timeout error so
// callers can t.Fatal with a useful message.
func runPollerUntil(t *testing.T, p *persona.Poller, cond func() bool, timeout time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = p.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			cancel()
			<-done
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	return fmt.Errorf("condition not met within %s", timeout)
}

func runtimeStatus(t *testing.T, path string) string {
	t.Helper()
	return fmt.Sprint(runtimeStateMap(t, path)["status"])
}

func runtimeStateMap(t *testing.T, path string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}
	}
	if err != nil {
		t.Fatalf("read runtime state: %v", err)
	}
	var state map[string]any
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("decode runtime state: %v", err)
	}
	return state
}

// ---------------------------------------------------------------------------
// Tests.
// ---------------------------------------------------------------------------

// TestPoller_PicksUpInboxFile asserts the happy path: drop a .md file into
// the inbox, observe a single spawner invocation with the correct argv, an
// outbox file, and the original message moved to the archive.
//
// Post-G3-fix: the poller suppresses the stub unless stdout itself is
// frontmatter-framed. This test supplies a frontmatter-framed stdout
// body so the legacy stub path stays exercised; the stub suppression
// path is covered in TestPoller_SuppressesStubWhenClaudeWroteOutbox.
func TestPoller_PicksUpInboxFile(t *testing.T) {
	fs := newTestFS(t, "Seth system prompt body.")
	cfg := baseConfig(fs)

	const stdoutFM = "---\nfrom: seth\nto: kyle\n---\n\nhello from claude\n"
	var calls []spawnCall
	var mu sync.Mutex
	spawner := recorderSpawner(&calls, &mu, func(_ spawnCall) ([]byte, int, error) {
		return []byte(stdoutFM), 0, nil
	})
	p, err := persona.New(cfg, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "001-ping.md", "hi persona", time.Now())

	err = runPollerUntil(t, p, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(calls) >= 1
	}, 2*time.Second)
	if err != nil {
		t.Fatalf("waiting for first spawn: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("want 1 spawn, got %d", len(calls))
	}
	argv := calls[0].argv
	// OpenCode receives the full turn prompt as the final positional arg.
	wantArgv := []string{
		"opencode",
		"run",
		"--format", "json",
		"--agent", "build",
		"--dir", "/home/drem",
		"--model", "openai/gpt-5.5",
		"--variant", "high",
	}
	if len(argv) != len(wantArgv)+1 || !equalArgv(argv[:len(wantArgv)], wantArgv) {
		t.Fatalf("argv mismatch\nwant: %v\ngot:  %v", wantArgv, argv)
	}
	if calls[0].stdin != nil {
		t.Fatalf("stdin must be nil for opencode positional prompt")
	}
	if !strings.Contains(argv[len(argv)-1], "hi persona") {
		t.Fatalf("prompt argv mismatch: got %q", argv[len(argv)-1])
	}
	if !strings.Contains(argv[len(argv)-1], "Instruction precedence for this turn") {
		t.Fatalf("turn prompt must include operator directive precedence block: got %q", argv[len(argv)-1])
	}
	if !strings.Contains(argv[len(argv)-1], "Do not cite them back as blockers") {
		t.Fatalf("turn prompt must prevent stale canon refusal loops: got %q", argv[len(argv)-1])
	}

	// Outbox must contain exactly one file whose name includes the persona.
	outEntries := dirEntries(t, fs.outboxDir)
	if len(outEntries) != 1 {
		t.Fatalf("outbox entries: want 1, got %d (%v)", len(outEntries), outEntries)
	}
	if !strings.Contains(outEntries[0], "seth-reply-") {
		t.Fatalf("outbox filename %q must include 'seth-reply-'", outEntries[0])
	}

	// Inbox must be empty (.archive dir does not count as an entry).
	if got := visibleInboxFiles(t, fs.inboxDir); len(got) != 0 {
		t.Fatalf("inbox want empty, got %v", got)
	}
	// Archive must contain the original filename.
	archived := dirEntries(t, fs.archiveDir)
	if len(archived) != 1 || archived[0] != "001-ping.md" {
		t.Fatalf("archive want [001-ping.md], got %v", archived)
	}
}

// TestPoller_OrderingByMtime drops two files with distinct mtimes and asserts
// the older one is processed first. Ordering is load-bearing because the
// poller is the only subscriber to the inbox — any reordering would change
// the persona's perceived conversation history.
func TestPoller_OrderingByMtime(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)

	var calls []spawnCall
	var mu sync.Mutex
	spawner := recorderSpawner(&calls, &mu, nil)
	p, err := persona.New(cfg, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	past := time.Now().Add(-time.Minute)
	// "z.md" has the older mtime; "a.md" is newer. Pure alphabetical order
	// would flip the pair, so a passing test confirms mtime wins.
	writeInboxMessage(t, fs, "z.md", "old", past)
	writeInboxMessage(t, fs, "a.md", "new", time.Now())

	err = runPollerUntil(t, p, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(calls) >= 2
	}, 2*time.Second)
	if err != nil {
		t.Fatalf("waiting for both spawns: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(calls[0].promptArg(), "old") {
		t.Fatalf("first spawn should carry the older message, got prompt=%q argv=%v",
			calls[0].promptArg(), calls[0].argv)
	}
	if !strings.Contains(calls[1].promptArg(), "new") {
		t.Fatalf("second spawn should carry the newer message, got prompt=%q argv=%v",
			calls[1].promptArg(), calls[1].argv)
	}
}

func TestPoller_StartupQuietPeriodSuppressesImmediateProcessing(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)
	cfg.StartupQuietPeriod = time.Hour
	cfg.RuntimeStateFile = filepath.Join(fs.root, "runtime.json")

	var calls []spawnCall
	var mu sync.Mutex
	p, err := persona.New(cfg, recorderSpawner(&calls, &mu, nil))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "quiet.md", "wait", time.Now())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = p.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtimeStatus(t, cfg.RuntimeStateFile) == "boot_quiet" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := runtimeStatus(t, cfg.RuntimeStateFile); got != "boot_quiet" {
		t.Fatalf("runtime status = %q, want boot_quiet", got)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 0 {
		t.Fatalf("startup quiet period should suppress spawns, got %d", len(calls))
	}
}

func TestPoller_MaxMessagesAtBootLimitsFirstScan(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)
	cfg.PollInterval = time.Hour
	cfg.MaxMessagesAtBoot = 1

	var calls []spawnCall
	var mu sync.Mutex
	p, err := persona.New(cfg, recorderSpawner(&calls, &mu, nil))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "001.md", "one", time.Now().Add(-time.Minute))
	writeInboxMessage(t, fs, "002.md", "two", time.Now())

	err = runPollerUntil(t, p, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(calls) >= 1
	}, 2*time.Second)
	if err != nil {
		t.Fatalf("waiting for boot spawn: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("boot scan should process one message, got %d", len(calls))
	}
	if got := visibleInboxFiles(t, fs.inboxDir); len(got) != 1 || got[0] != "002.md" {
		t.Fatalf("remaining inbox = %v, want [002.md]", got)
	}
}

func TestPoller_MaxMessagesPerScanLimitsLaterScans(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)
	cfg.PollInterval = 20 * time.Millisecond
	cfg.MaxMessagesAtBoot = 0
	cfg.MaxMessagesPerScan = 1

	var calls []spawnCall
	var mu sync.Mutex
	p, err := persona.New(cfg, recorderSpawner(&calls, &mu, nil))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "001.md", "one", time.Now().Add(-time.Minute))
	writeInboxMessage(t, fs, "002.md", "two", time.Now())

	err = runPollerUntil(t, p, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(calls) >= 1
	}, 2*time.Second)
	if err != nil {
		t.Fatalf("waiting for scan spawn: %v", err)
	}
	mu.Lock()
	gotCalls := len(calls)
	mu.Unlock()
	if gotCalls != 1 {
		t.Fatalf("first normal scan should process one message, got %d", gotCalls)
	}
	if got := visibleInboxFiles(t, fs.inboxDir); len(got) != 1 || got[0] != "002.md" {
		t.Fatalf("remaining inbox = %v, want [002.md]", got)
	}
}

func TestPoller_StartupDrainMovesFilesWithoutSpawning(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)
	cfg.PollInterval = time.Hour
	cfg.StartupDrain = true

	var calls []spawnCall
	var mu sync.Mutex
	p, err := persona.New(cfg, recorderSpawner(&calls, &mu, nil))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "drain.md", "ignore me", time.Now())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = p.Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if entries := dirEntries(t, filepath.Join(fs.inboxDir, ".ignored")); len(entries) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 0 {
		t.Fatalf("startup drain should not spawn, got %d", len(calls))
	}
	if got := dirEntries(t, filepath.Join(fs.inboxDir, ".ignored")); len(got) != 1 || got[0] != "drain.md" {
		t.Fatalf("ignored dir = %v, want [drain.md]", got)
	}
}

func TestPoller_RuntimeStateProcessingAndIdle(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)
	cfg.RuntimeStateFile = filepath.Join(fs.root, "runtime.json")
	cfg.MaxMessagesAtBoot = 1
	processingSeen := make(chan struct{}, 1)

	spawner := persona.SpawnerFunc(func(_ context.Context, _ []string, _ io.Reader) ([]byte, int, error) {
		if st := runtimeStateMap(t, cfg.RuntimeStateFile); st["status"] == "processing" && st["current_inbox_filename"] == "state.md" {
			processingSeen <- struct{}{}
		}
		return []byte("ok"), 0, nil
	})
	p, err := persona.New(cfg, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "state.md", "body", time.Now())

	err = runPollerUntil(t, p, func() bool { return runtimeStatus(t, cfg.RuntimeStateFile) == "idle" }, 2*time.Second)
	if err != nil {
		t.Fatalf("waiting for idle runtime state: %v", err)
	}
	select {
	case <-processingSeen:
	default:
		t.Fatal("spawner did not observe processing runtime state")
	}
	st := runtimeStateMap(t, cfg.RuntimeStateFile)
	if st["last_processed_filename"] != "state.md" {
		t.Fatalf("last_processed_filename = %v, want state.md", st["last_processed_filename"])
	}
}

// TestPoller_FrontmatterBodyGoesInFinalOpenCodeArg asserts that the
// frontmatter-bearing message body is isolated to OpenCode's final prompt
// argument and never appears in the flag prefix.
func TestPoller_FrontmatterBodyGoesInFinalOpenCodeArg(t *testing.T) {
	fs := newTestFS(t, "Alex system prompt.")
	cfg := baseConfig(fs)
	cfg.Persona = "alex"

	var calls []spawnCall
	var mu sync.Mutex
	spawner := recorderSpawner(&calls, &mu, func(_ spawnCall) ([]byte, int, error) {
		return []byte("alive"), 0, nil
	})
	p, err := persona.New(cfg, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const body = "---\nfrom: kyle\nto: alex\nts: 2026-04-21T13:00:00Z\n---\n\nReply with one word: alive"
	writeInboxMessage(t, fs, "frontmatter.md", body, time.Now())

	err = runPollerUntil(t, p, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(calls) >= 1
	}, 2*time.Second)
	if err != nil {
		t.Fatalf("waiting for spawn: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("want 1 spawn, got %d", len(calls))
	}
	call := calls[0]

	// Primary invariant: frontmatter can only appear in the final
	// OpenCode prompt argument, never in the flag prefix.
	for _, a := range call.argv[:len(call.argv)-1] {
		if strings.Contains(a, "from: kyle") {
			t.Fatalf("frontmatter metadata leaked into argv element %q", a)
		}
		if strings.HasPrefix(a, "---") {
			t.Fatalf("flag argv element %q starts with frontmatter", a)
		}
	}

	// And must appear verbatim in the final prompt argument.
	if !strings.Contains(call.argv[len(call.argv)-1], body) {
		t.Fatalf("frontmatter body not in prompt argv\nwant: %q\ngot:  %q", body, call.argv[len(call.argv)-1])
	}

	// Secondary invariant: the argv uses OpenCode build mode and keeps
	// the large prompt as the final argument.
	want := []string{"opencode", "run", "--format", "json", "--agent", "build", "--dir", "/home/drem", "--model", "openai/gpt-5.5", "--variant", "high"}
	if len(call.argv) != len(want)+1 || !equalArgv(call.argv[:len(want)], want) {
		t.Fatalf("argv shape regressed\nwant: %v\ngot:  %v", want, call.argv)
	}
}

func TestPoller_ReportOnlyAdmissionRecordsPromptAndInboxCandidates(t *testing.T) {
	fs := newTestFS(t, "Seth system prompt body.")
	db := testutil.NewTestDBWithModels(t, artifactregistry.Models()...)
	cfg := baseConfig(fs)
	cfg.ArtifactAdmissionReporter = artifactregistry.NewRegistry(db)

	var calls []spawnCall
	var mu sync.Mutex
	spawner := recorderSpawner(&calls, &mu, func(_ spawnCall) ([]byte, int, error) {
		return []byte("ok"), 0, nil
	})
	p, err := persona.New(cfg, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "001-ping.md", "hi persona", time.Now())

	err = runPollerUntil(t, p, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(calls) >= 1
	}, 2*time.Second)
	if err != nil {
		t.Fatalf("waiting for spawn: %v", err)
	}

	mu.Lock()
	if len(calls) != 1 {
		mu.Unlock()
		t.Fatalf("want 1 spawn, got %d", len(calls))
	}
	promptArg := calls[0].promptArg()
	mu.Unlock()
	if !strings.Contains(promptArg, "Seth system prompt body.") || !strings.Contains(promptArg, "hi persona") {
		t.Fatalf("spawn prompt content changed unexpectedly: %q", promptArg)
	}

	var packets []artifactregistry.ContextPacket
	if err := db.Find(&packets).Error; err != nil {
		t.Fatalf("load packets: %v", err)
	}
	if len(packets) != 1 {
		t.Fatalf("want one admission packet, got %d", len(packets))
	}
	if packets[0].Persona != "seth" || packets[0].AgentRole != "csuite_persona" || packets[0].WorkflowStage != "persona_turn_prompt_assembly" {
		t.Fatalf("unexpected packet: %#v", packets[0])
	}
	if packets[0].Metadata["report_only"] != true {
		t.Fatalf("packet must be report-only, got metadata %#v", packets[0].Metadata)
	}

	var decisions []artifactregistry.ContextAdmissionDecision
	if err := db.Order("created_at ASC").Find(&decisions).Error; err != nil {
		t.Fatalf("load decisions: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("want two decisions, got %d (%#v)", len(decisions), decisions)
	}
	for _, decision := range decisions {
		if decision.ContextPacketID != packets[0].ID {
			t.Fatalf("decision packet id mismatch: %#v", decision)
		}
		if decision.Decision != artifactregistry.DecisionExcludeInadmissible {
			t.Fatalf("candidate should be report-only inadmissible, got %#v", decision)
		}
		if decision.Metadata["report_only"] != true || decision.Metadata["content_uri"] == "" {
			t.Fatalf("decision must carry report-only candidate metadata, got %#v", decision.Metadata)
		}
	}

	if got := visibleInboxFiles(t, fs.inboxDir); len(got) != 0 {
		t.Fatalf("inbox want empty after report-only admission, got %v", got)
	}
}

func TestPoller_ReportOnlyAdmissionFailureDoesNotBlockTurn(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)
	cfg.ArtifactAdmissionReporter = failingAdmissionReporter{}

	var calls []spawnCall
	var mu sync.Mutex
	spawner := recorderSpawner(&calls, &mu, func(_ spawnCall) ([]byte, int, error) {
		return []byte("ok"), 0, nil
	})
	p, err := persona.New(cfg, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "001-ping.md", "hi persona", time.Now())

	err = runPollerUntil(t, p, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(calls) >= 1
	}, 2*time.Second)
	if err != nil {
		t.Fatalf("waiting for spawn: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("admission report failure must not block spawn, got %d calls", len(calls))
	}
}

// TestPoller_StateFileAtomicUpdate writes a poison state file, then triggers
// a successful processing pass and asserts the new state.md is a complete
// well-formed document (no partial write artifact, no leftover tempfile).
func TestPoller_StateFileAtomicUpdate(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)
	// Pre-seed state.md with content the test can detect if the update is
	// non-atomic (e.g. the poller truncates then writes without rename).
	if err := os.WriteFile(fs.stateFile, []byte("POISON\n"), 0o644); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	var calls []spawnCall
	var mu sync.Mutex
	spawner := recorderSpawner(&calls, &mu, nil)
	p, err := persona.New(cfg, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "msg.md", "hello", time.Now())
	err = runPollerUntil(t, p, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(calls) >= 1
	}, 2*time.Second)
	if err != nil {
		t.Fatalf("waiting for spawn: %v", err)
	}

	data, err := os.ReadFile(fs.stateFile)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	body := string(data)
	if strings.Contains(body, "POISON") {
		t.Fatalf("state.md still contains poisoned content: %q", body)
	}
	for _, needle := range []string{"last_processed: msg.md", "last_status: ok", "last_exit_code: 0", "updated_at: "} {
		if !strings.Contains(body, needle) {
			t.Fatalf("state.md missing %q\nfull: %s", needle, body)
		}
	}
	// No tempfile should be lingering next to state.md.
	entries, err := os.ReadDir(filepath.Dir(fs.stateFile))
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".state-") {
			t.Fatalf("leftover tempfile %q in %s", e.Name(), filepath.Dir(fs.stateFile))
		}
	}
}

// TestPoller_FailureRetryThenArchive drives the sidecar counter to MaxFailures
// and verifies the message is renamed to .failed, the sidecar is cleaned up,
// and the loop does not retry it.
func TestPoller_FailureRetryThenArchive(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)
	cfg.MaxFailures = 2

	var calls int32
	spawner := persona.SpawnerFunc(func(_ context.Context, args []string, _ io.Reader) ([]byte, int, error) {
		atomic.AddInt32(&calls, 1)
		return []byte("bad"), 7, nil
	})
	p, err := persona.New(cfg, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "broken.md", "please fail", time.Now())

	err = runPollerUntil(t, p, func() bool {
		// After MaxFailures attempts the file should be archived as .failed
		// and the original should be gone from the inbox.
		_, err := os.Stat(filepath.Join(fs.archiveDir, "broken.md.failed"))
		return err == nil
	}, 3*time.Second)
	if err != nil {
		t.Fatalf("waiting for .failed archival: %v (calls=%d, inbox=%v)",
			err, atomic.LoadInt32(&calls), dirEntries(t, fs.inboxDir))
	}

	// Sidecar must be gone after archival.
	if _, err := os.Stat(filepath.Join(fs.inboxDir, "broken.md.failures")); !os.IsNotExist(err) {
		t.Fatalf("sidecar .failures should be gone, stat err=%v", err)
	}

	if got := atomic.LoadInt32(&calls); got < 2 {
		t.Fatalf("want >=2 spawner calls before archival, got %d", got)
	}

	// Subsequent ticks should not re-invoke the spawner for the failed file.
	before := atomic.LoadInt32(&calls)
	time.Sleep(cfg.PollInterval * 3)
	if atomic.LoadInt32(&calls) != before {
		t.Fatalf("spawner was called again after archival")
	}
}

// TestPoller_SpawnErrorIsRetried covers the "claude binary missing" branch:
// the Spawner returns an error (not a non-zero exit). The poller must treat
// this as a retriable failure and eventually archive the message.
func TestPoller_SpawnErrorIsRetried(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)
	cfg.MaxFailures = 2

	spawner := persona.SpawnerFunc(func(_ context.Context, _ []string, _ io.Reader) ([]byte, int, error) {
		return nil, -1, fmt.Errorf("exec: \"claude\": not found")
	})
	p, err := persona.New(cfg, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "doomed.md", "body", time.Now())

	err = runPollerUntil(t, p, func() bool {
		_, err := os.Stat(filepath.Join(fs.archiveDir, "doomed.md.failed"))
		return err == nil
	}, 3*time.Second)
	if err != nil {
		t.Fatalf("spawn-error branch never archived: %v", err)
	}
}

// TestPoller_ContextCancelReturnsCleanly asserts the Run goroutine exits
// promptly when ctx is cancelled between polls. The guarantee matters for
// SIGTERM: docker stop waits 10s by default, so the loop must come to rest
// well before that.
func TestPoller_ContextCancelReturnsCleanly(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)
	cfg.PollInterval = 500 * time.Millisecond // long tick so cancel wins

	spawner := persona.SpawnerFunc(func(_ context.Context, _ []string, _ io.Reader) ([]byte, int, error) {
		t.Fatalf("spawner must not be called when inbox is empty")
		return nil, 0, nil
	})
	p, err := persona.New(cfg, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned err=%v; want nil on clean shutdown", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of cancel")
	}
}

// TestPoller_ValidateRejectsMissingInbox exercises the fail-fast behaviour of
// Validate so a bind-mount misconfiguration crashes the container
// immediately instead of polling forever.
func TestPoller_ValidateRejectsMissingInbox(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)
	cfg.InboxDir = filepath.Join(fs.root, "does-not-exist")

	if _, err := persona.New(cfg, persona.SpawnerFunc(stubSpawner)); err == nil {
		t.Fatalf("New should reject missing inbox dir")
	}
}

// TestPoller_ValidateRejectsUnknownPersona guards against typos in the
// CSUITE_AGENT env var propagating into the default-path computation.
func TestPoller_ValidateRejectsUnknownPersona(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)
	cfg.Persona = "nobody"
	if _, err := persona.New(cfg, persona.SpawnerFunc(stubSpawner)); err == nil {
		t.Fatalf("New should reject unknown persona")
	}
}

// TestConfig_ApplyDefaults asserts the Persona-derived path defaults resolve
// to /home/drem/.drem-csuite/<persona>/... and /opt/csuite/prompts/<persona>.md.
// These defaults are the contract with the Dockerfile entrypoint.
func TestConfig_ApplyDefaults(t *testing.T) {
	cfg := persona.Config{Persona: "mike"}
	cfg.ApplyDefaults()
	if cfg.InboxDir != "/home/drem/.drem-csuite/mike/inbox" {
		t.Errorf("InboxDir = %q", cfg.InboxDir)
	}
	if cfg.OutboxDir != "/home/drem/.drem-csuite/mike/outbox" {
		t.Errorf("OutboxDir = %q", cfg.OutboxDir)
	}
	if cfg.StateFile != "/home/drem/.drem-csuite/mike/state.md" {
		t.Errorf("StateFile = %q", cfg.StateFile)
	}
	if cfg.ArchiveDir != "/home/drem/.drem-csuite/mike/inbox/.archive" {
		t.Errorf("ArchiveDir = %q", cfg.ArchiveDir)
	}
	if cfg.RuntimeStateFile != "/home/drem/.drem-csuite/mike/runtime.json" {
		t.Errorf("RuntimeStateFile = %q", cfg.RuntimeStateFile)
	}
	if cfg.PromptFile != "/opt/csuite/prompts/mike.md" {
		t.Errorf("PromptFile = %q", cfg.PromptFile)
	}
	if cfg.PollInterval != persona.DefaultPollInterval {
		t.Errorf("PollInterval = %v", cfg.PollInterval)
	}
	if cfg.ClaudeTimeout != persona.DefaultClaudeTimeout {
		t.Errorf("ClaudeTimeout = %v", cfg.ClaudeTimeout)
	}
	if cfg.MaxFailures != persona.DefaultMaxFailures {
		t.Errorf("MaxFailures = %v", cfg.MaxFailures)
	}
}

// TestPoller_SignalFiresWhenInboxAlreadyArchived covers the race between
// the poller's archive rename and a Claude-in-subprocess that uses its
// Bash tool to move the inbox file into .archive itself (some persona
// prompts instruct exactly that, e.g. seth's system prompt). The
// post-write bookkeeping must not gate the signal — otherwise the
// ENOENT on os.Rename would short-circuit recordSuccess before the
// HTTP signal goroutine ever launches, and the watcher would never
// route the reply unless a rescan pass happened to kick in. Plan
// §Signal-failure-isolation pins the guarantee that outbox write +
// fsync is sufficient state for a signal.
func TestPoller_SignalFiresWhenInboxAlreadyArchived(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)
	rec := &recordingSignaler{onCall: func(_ signalCall) persona.SignalOutcome {
		return persona.SignalOK
	}}
	cfg.Signaler = rec

	// Spawner pretends to be Claude: it reads the inbox body, then
	// "uses its Bash tool" to move the inbox file into .archive before
	// returning its reply. The poller's subsequent os.Rename then sees
	// ENOENT on the source path.
	//
	// Post-G3: returns a frontmatter-framed stdout so the poller keeps
	// the legacy stub-write path (non-frontmatter stdout now
	// suppresses the stub — see TestPoller_SuppressesStubWhenStdoutHasNoFrontmatter).
	spawner := persona.SpawnerFunc(func(_ context.Context, _ []string, _ io.Reader) ([]byte, int, error) {
		srcPath := filepath.Join(fs.inboxDir, "claude-moved-me.md")
		dstPath := filepath.Join(fs.archiveDir, "claude-moved-me.md")
		if err := os.Rename(srcPath, dstPath); err != nil {
			return nil, 1, fmt.Errorf("test spawner: pre-archive: %v", err)
		}
		return []byte("---\nfrom: seth\nto: kyle\n---\n\nreply-body"), 0, nil
	})
	p, err := persona.New(cfg, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "claude-moved-me.md", "hello", time.Now())

	// Signal MUST land even though the archive rename fails with
	// ENOENT — the signal code runs before the archive attempt.
	err = runPollerUntil(t, p, func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.calls) >= 1
	}, 2*time.Second)
	if err != nil {
		t.Fatalf("waiting for signal after Claude-moved inbox: %v", err)
	}

	rec.mu.Lock()
	calls := append([]signalCall(nil), rec.calls...)
	rec.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("want 1 signal call, got %d", len(calls))
	}
	// The outbox file must exist and the signal's sha256 must match
	// its on-disk contents.
	outEntries := dirEntries(t, fs.outboxDir)
	if len(outEntries) != 1 {
		t.Fatalf("outbox entries: want 1, got %d (%v)", len(outEntries), outEntries)
	}
	if calls[0].persona != "seth" {
		t.Errorf("persona = %q, want seth", calls[0].persona)
	}
	wantSHA := sha256Hex([]byte("---\nfrom: seth\nto: kyle\n---\n\nreply-body"))
	if calls[0].sha256 != wantSHA {
		t.Errorf("sha256 = %q, want %q", calls[0].sha256, wantSHA)
	}
	// Archive directory must still have the file Claude moved; the
	// poller's second archive attempt is a no-op here.
	archived := dirEntries(t, fs.archiveDir)
	if len(archived) != 1 || archived[0] != "claude-moved-me.md" {
		t.Errorf("archive dir = %v, want [claude-moved-me.md]", archived)
	}
}

// ---------------------------------------------------------------------------
// G3 dual-write fix tests.
// ---------------------------------------------------------------------------

// TestPoller_SuppressesStubWhenClaudeWroteOutbox is the primary
// regression proof for the G3 dual-write fix (scoreboard item 2).
// When the spawner simulates Claude's Write tool by creating a
// well-formed `<ts>-<persona>-to-<recipient>-*.md` file in the outbox
// during the turn, the poller MUST NOT also write a redundant stub.
// Pre-fix, the stub was always emitted and quarantined; post-fix the
// outbox carries exactly one file — the one Claude wrote.
func TestPoller_SuppressesStubWhenClaudeWroteOutbox(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)

	const claudeWritten = "20260422T140000Z-seth-to-alex-scoreboard.md"
	const claudeBody = "---\nfrom: seth\nto: alex\ntimestamp: 2026-04-22T14:00:00Z\nsubject: scoreboard\n---\n\nbody here\n"

	spawner := persona.SpawnerFunc(func(_ context.Context, _ []string, _ io.Reader) ([]byte, int, error) {
		// Simulate Claude's Write tool emitting a frontmatter-bearing
		// outbox file during the turn.
		if err := os.WriteFile(filepath.Join(fs.outboxDir, claudeWritten), []byte(claudeBody), 0o644); err != nil {
			return nil, 1, fmt.Errorf("simulated Write: %v", err)
		}
		// Claude's stdout is a plain-text "turn summary" with no
		// frontmatter — this was the shape that was being quarantined.
		return []byte("Turn complete. Wrote scoreboard reply to Alex."), 0, nil
	})

	p, err := persona.New(cfg, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "trigger.md", "hi", time.Now())

	err = runPollerUntil(t, p, func() bool {
		// Wait for state.md to land — signals "turn is done".
		data, _ := os.ReadFile(fs.stateFile)
		return strings.Contains(string(data), "last_processed: trigger.md")
	}, 2*time.Second)
	if err != nil {
		t.Fatalf("waiting for state.md: %v", err)
	}

	// Outbox must contain EXACTLY the file Claude wrote — no stub.
	entries := dirEntries(t, fs.outboxDir)
	if len(entries) != 1 {
		t.Fatalf("outbox entries: want 1 (claude's file only), got %d: %v", len(entries), entries)
	}
	if entries[0] != claudeWritten {
		t.Fatalf("outbox[0] = %q, want %q (and NO stub)", entries[0], claudeWritten)
	}
	// Cross-check: none of the entries should look like a stub.
	for _, n := range entries {
		if strings.Contains(n, "-reply-") {
			t.Errorf("outbox contains stub %q — G3 fix regressed", n)
		}
	}
}

// TestPoller_SuppressesStubWhenStdoutHasNoFrontmatter covers the
// second leg of the G3 fix: when Claude emits NO well-formed outbox
// file AND stdout is a plain-text turn summary, the poller must not
// fall back to writing a stub. Quarantined stubs were the whole fail
// mode this fix eliminates.
func TestPoller_SuppressesStubWhenStdoutHasNoFrontmatter(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)

	spawner := persona.SpawnerFunc(func(_ context.Context, _ []string, _ io.Reader) ([]byte, int, error) {
		return []byte("Turn complete. Nothing to say this turn."), 0, nil
	})
	p, err := persona.New(cfg, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "silent.md", "are you there?", time.Now())

	err = runPollerUntil(t, p, func() bool {
		data, _ := os.ReadFile(fs.stateFile)
		return strings.Contains(string(data), "last_processed: silent.md")
	}, 2*time.Second)
	if err != nil {
		t.Fatalf("waiting for state.md: %v", err)
	}

	// Outbox MUST be empty — no stub for quarantine-bound non-frontmatter output.
	entries := dirEntries(t, fs.outboxDir)
	if len(entries) != 0 {
		t.Fatalf("outbox must be empty (stub suppressed), got %v", entries)
	}
	// Inbox must still be archived — the turn ran to completion even though
	// no outbox file was emitted.
	archived := dirEntries(t, fs.archiveDir)
	if len(archived) != 1 || archived[0] != "silent.md" {
		t.Fatalf("archive = %v, want [silent.md]", archived)
	}
}

// TestPoller_KeepsStubWhenStdoutIsFrontmatter is the narrow path that
// preserves backward compatibility with any persona prompt that
// documents "reply inline on stdout with a full frontmatter block."
// Stdout that starts with "---\n" and contains a closing "\n---"
// remains eligible for stub-wrapping — the watcher can classify it.
func TestPoller_KeepsStubWhenStdoutIsFrontmatter(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)

	const fmStdout = "---\nfrom: seth\nto: kyle\n---\n\ninline reply"
	spawner := persona.SpawnerFunc(func(_ context.Context, _ []string, _ io.Reader) ([]byte, int, error) {
		return []byte(fmStdout), 0, nil
	})
	p, err := persona.New(cfg, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "ping.md", "hi", time.Now())

	err = runPollerUntil(t, p, func() bool {
		data, _ := os.ReadFile(fs.stateFile)
		return strings.Contains(string(data), "last_processed: ping.md")
	}, 2*time.Second)
	if err != nil {
		t.Fatalf("waiting for state.md: %v", err)
	}

	entries := dirEntries(t, fs.outboxDir)
	if len(entries) != 1 {
		t.Fatalf("want 1 outbox entry (stub from fm-stdout), got %d: %v", len(entries), entries)
	}
	if !strings.Contains(entries[0], "seth-reply-") {
		t.Errorf("outbox[0] = %q, want persona-reply-* filename", entries[0])
	}
}

// ---------------------------------------------------------------------------
// Helpers.
// ---------------------------------------------------------------------------

func stubSpawner(_ context.Context, _ []string, _ io.Reader) ([]byte, int, error) {
	return nil, 0, nil
}

func equalArgv(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// dirEntries returns the names of non-directory entries in dir, sorted.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	return names
}

// visibleInboxFiles returns .md entries in the inbox (excluding sidecar
// .failures counters and the .archive dir). The poller's "is this message
// gone?" check hinges on this matching ReadDir's own filtering.
func visibleInboxFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	return names
}
