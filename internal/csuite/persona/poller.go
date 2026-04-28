package persona

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/internal/artifactregistry"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/google/uuid"
)

// sha256Bytes returns the hex-encoded SHA-256 of data. Used to build
// the watcher signal payload so the watcher's idempotency key maps 1:1
// to the on-disk outbox file.
func sha256Bytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Poller owns the inbox scan loop. Construct one with New, then call
// Run(ctx) from the binary's main goroutine. Run blocks until ctx is
// cancelled and finishes any in-flight model invocation before
// returning so SIGTERM does not abandon work mid-turn.
type Poller struct {
	cfg                Config
	spawner            Spawner
	prompt             string
	personaLabel       slog.Attr
	bootID             string
	startedAt          time.Time
	quietUntil         time.Time
	currentInbox       string
	currentTurnStarted time.Time
	lastProcessed      string
	lastExitCode       int
	lastError          string
}

// New validates cfg, loads the persona prompt once, and returns a
// ready-to-Run Poller. Callers must have already called
// cfg.ApplyDefaults.
func New(cfg Config, spawner Spawner) (*Poller, error) {
	if spawner == nil {
		return nil, errors.New("persona: Spawner is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	body, err := os.ReadFile(cfg.PromptFile)
	if err != nil {
		return nil, fmt.Errorf("persona: read prompt %q: %w", cfg.PromptFile, err)
	}
	startedAt := cfg.Now().UTC()
	p := &Poller{
		cfg:          cfg,
		spawner:      spawner,
		prompt:       string(body),
		personaLabel: slog.String("persona", cfg.Persona),
		bootID:       uuid.NewString(),
		startedAt:    startedAt,
	}
	if cfg.StartupQuietPeriod > 0 {
		p.quietUntil = startedAt.Add(cfg.StartupQuietPeriod)
	}
	return p, nil
}

// Run executes the poll loop until ctx is cancelled.
//
// On each tick Run calls scanOnce, which:
//
//  1. Lists the inbox directory for *.md entries (excluding sidecar
//     .failures counters and the .archive subdir).
//  2. Sorts them by mtime ascending so the oldest message is processed
//     first. Deterministic ordering survives container restarts.
//  3. For each file, calls processMessage to invoke OpenCode once and
//     either archive the message (success) or bump its .failures sidecar
//     (non-zero exit / spawn error).
//
// If ctx is cancelled mid-tick the function returns nil as soon as the
// current processMessage call returns. The spawner is responsible for
// honouring its ctx deadline (the production subprocess spawner uses
// CommandContext).
func (p *Poller) Run(ctx context.Context) error {
	p.cfg.Logger.Info("persona poller starting",
		p.personaLabel,
		slog.String("inbox", p.cfg.InboxDir),
		slog.String("outbox", p.cfg.OutboxDir),
		slog.Duration("poll_interval", p.cfg.PollInterval),
		slog.Duration("claude_timeout", p.cfg.ClaudeTimeout),
		slog.Duration("startup_quiet_period", p.cfg.StartupQuietPeriod),
		slog.Bool("startup_drain", p.cfg.StartupDrain),
		slog.Int("max_messages_per_scan", p.cfg.MaxMessagesPerScan),
		slog.Int("max_messages_at_boot", p.cfg.MaxMessagesAtBoot),
	)

	if err := p.writeRuntimeState("starting"); err != nil {
		p.cfg.Logger.Warn("write runtime state", p.personaLabel, slog.Any("err", err))
	}
	if err := p.handleStartup(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		p.lastError = err.Error()
		_ = p.writeRuntimeState("error")
		p.cfg.Logger.Error("startup handling failed", p.personaLabel, slog.Any("err", err))
	} else if err := p.writeRuntimeState("idle"); err != nil {
		p.cfg.Logger.Warn("write runtime state", p.personaLabel, slog.Any("err", err))
	}

	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = p.writeRuntimeState("stopping")
			p.cfg.Logger.Info("persona poller stopping", p.personaLabel)
			return nil
		case <-ticker.C:
			if err := p.scanOnce(ctx, p.cfg.MaxMessagesPerScan); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				p.lastError = err.Error()
				_ = p.writeRuntimeState("error")
				p.cfg.Logger.Error("scan failed", p.personaLabel, slog.Any("err", err))
			}
		}
	}
}

func (p *Poller) handleStartup(ctx context.Context) error {
	if p.cfg.StartupQuietPeriod > 0 {
		if err := p.writeRuntimeState("boot_quiet"); err != nil {
			p.cfg.Logger.Warn("write runtime state", p.personaLabel, slog.Any("err", err))
		}
		timer := time.NewTimer(p.cfg.StartupQuietPeriod)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
	}
	if p.cfg.StartupDrain {
		return p.drainStartupInbox()
	}
	if p.cfg.MaxMessagesAtBoot > 0 {
		return p.scanOnce(ctx, p.cfg.MaxMessagesAtBoot)
	}
	return nil
}

// scanOnce lists the inbox, orders by mtime ascending, and processes
// each message sequentially. Errors from processMessage are logged but
// do not abort the tick — the next file still gets its chance.
func (p *Poller) scanOnce(ctx context.Context, limit int) error {
	entries, err := os.ReadDir(p.cfg.InboxDir)
	if err != nil {
		return fmt.Errorf("read inbox %q: %w", p.cfg.InboxDir, err)
	}

	type candidate struct {
		name  string
		mtime time.Time
	}
	var files []candidate
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		info, err := ent.Info()
		if err != nil {
			p.cfg.Logger.Warn("stat inbox entry", p.personaLabel,
				slog.String("file", name),
				slog.Any("err", err))
			continue
		}
		files = append(files, candidate{name: name, mtime: info.ModTime()})
	}
	// Deterministic ordering: oldest first; tie-break by name so two
	// files with identical mtimes still pick a stable order.
	sort.Slice(files, func(i, j int) bool {
		if !files[i].mtime.Equal(files[j].mtime) {
			return files[i].mtime.Before(files[j].mtime)
		}
		return files[i].name < files[j].name
	})

	for i, f := range files {
		if limit > 0 && i >= limit {
			break
		}
		if ctx.Err() != nil {
			return nil
		}
		if err := p.processMessage(ctx, f.name); err != nil {
			p.cfg.Logger.Error("processMessage",
				p.personaLabel,
				slog.String("file", f.name),
				slog.Any("err", err))
		}
	}
	return nil
}

func (p *Poller) drainStartupInbox() error {
	ignoredDir := filepath.Join(p.cfg.InboxDir, ".ignored")
	if err := os.MkdirAll(ignoredDir, 0o755); err != nil {
		return fmt.Errorf("create ignored dir %q: %w", ignoredDir, err)
	}
	entries, err := os.ReadDir(p.cfg.InboxDir)
	if err != nil {
		return fmt.Errorf("read inbox %q: %w", p.cfg.InboxDir, err)
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".md") {
			continue
		}
		src := filepath.Join(p.cfg.InboxDir, ent.Name())
		dst := filepath.Join(ignoredDir, ent.Name())
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("drain inbox %q: %w", ent.Name(), err)
		}
		_ = os.Remove(failuresPath(p.cfg.InboxDir, ent.Name()))
		p.lastProcessed = ent.Name()
	}
	return nil
}

// processMessage handles a single inbox file end-to-end. The happy path
// is: read -> spawn OpenCode -> write reply to outbox -> move to
// archive -> update state.md. Failures short-circuit to recordFailure
// which bumps a per-message counter and eventually archives the file
// as `<name>.failed`.
func (p *Poller) processMessage(ctx context.Context, name string) error {
	inboxPath := filepath.Join(p.cfg.InboxDir, name)
	body, err := os.ReadFile(inboxPath)
	if err != nil {
		// Most likely the file disappeared between ReadDir and here
		// (another hand touched it); log and let the next tick re-scan.
		return fmt.Errorf("read %q: %w", inboxPath, err)
	}

	// Snapshot the outbox BEFORE Claude runs. The diff after Claude
	// returns tells us whether Claude used its Write tool to emit a
	// well-formed `<ts>-<persona>-to-<recipient>-*.md` outbox file
	// during the turn — in which case the poller must NOT write a
	// redundant stub (G3 dual-write fix: the stub never carries
	// frontmatter and the watcher quarantines it on arrival). See
	// plans/containerization-pivot-attack-plan.md §3 Group A item 2.
	outboxBefore, snapErr := snapshotOutbox(p.cfg.OutboxDir)
	if snapErr != nil {
		// A failure to snapshot falls back to the old stub-writing
		// behaviour; log and continue. Missing-dir is already handled
		// by snapshotOutbox itself.
		p.cfg.Logger.Warn("outbox snapshot failed",
			p.personaLabel,
			slog.Any("err", snapErr))
	}

	invocationCtx, cancel := context.WithTimeout(ctx, p.cfg.ClaudeTimeout)
	defer cancel()

	// Pass the full turn prompt as OpenCode's final positional argument.
	// Codex subscription auth is supplied by the OpenCode plugin reading the
	// bind-mounted /home/drem/.codex/auth.json file.
	turnPrompt := p.codexTurnPrompt(body)
	p.reportPromptAdmission(ctx, name, body)
	p.currentInbox = name
	p.currentTurnStarted = p.cfg.Now().UTC()
	p.lastError = ""
	if err := p.writeRuntimeState("processing"); err != nil {
		p.cfg.Logger.Warn("write runtime state", p.personaLabel, slog.Any("err", err))
	}
	defer func() {
		p.currentInbox = ""
		p.currentTurnStarted = time.Time{}
	}()

	args := []string{
		"opencode",
		"run",
		"--format", "json",
		"--agent", "build",
		"--dir", "/home/drem",
	}
	model := firstNonEmpty(os.Getenv("DREM_OPENCODE_MODEL"), os.Getenv("DREM_CODEX_MODEL"), "openai/gpt-5.5")
	if model != "" {
		args = append(args, "--model", model)
	}
	variant := firstNonEmpty(os.Getenv("DREM_OPENCODE_VARIANT"), os.Getenv("DREM_CODEX_EFFORT"), "high")
	if variant != "" {
		args = append(args, "--variant", variant)
	}
	args = append(args, string(turnPrompt))

	start := p.cfg.Now()
	stdout, exitCode, spawnErr := p.spawner.Spawn(invocationCtx, args, nil)
	duration := p.cfg.Now().Sub(start)

	if spawnErr != nil {
		// Spawn failure (binary missing, context cancelled mid-launch).
		// Treat as a retriable failure unless ctx cancellation indicates
		// the caller is shutting down, in which case defer to the next
		// container start.
		if ctx.Err() != nil {
			return nil
		}
		p.lastExitCode = -1
		p.lastError = spawnErr.Error()
		err := p.recordFailure(name, spawnErr.Error(), -1, duration)
		_ = p.writeRuntimeState("error")
		return err
	}

	if exitCode != 0 {
		reason := strings.TrimSpace(string(stdout))
		if reason == "" {
			reason = "non-zero exit"
		}
		p.lastExitCode = exitCode
		p.lastError = reason
		err := p.recordFailure(name, reason, exitCode, duration)
		_ = p.writeRuntimeState("error")
		return err
	}

	p.lastExitCode = 0
	p.lastError = ""
	err = p.recordSuccess(name, body, stdout, duration, outboxBefore)
	if err != nil {
		p.lastError = err.Error()
		_ = p.writeRuntimeState("error")
		return err
	}
	p.lastProcessed = name
	_ = p.writeRuntimeState("idle")
	return nil
}

type runtimeState struct {
	Persona               string `json:"persona"`
	Status                string `json:"status"`
	BootID                string `json:"boot_id"`
	StartedAt             string `json:"started_at"`
	QuietUntil            string `json:"quiet_until,omitempty"`
	CurrentInboxFilename  string `json:"current_inbox_filename,omitempty"`
	CurrentTurnStartedAt  string `json:"current_turn_started_at,omitempty"`
	LastProcessedFilename string `json:"last_processed_filename,omitempty"`
	LastExitCode          int    `json:"last_exit_code"`
	LastError             string `json:"last_error,omitempty"`
}

func (p *Poller) writeRuntimeState(status string) error {
	if p.cfg.RuntimeStateFile == "" {
		return nil
	}
	state := runtimeState{
		Persona:               p.cfg.Persona,
		Status:                status,
		BootID:                p.bootID,
		StartedAt:             p.startedAt.UTC().Format(time.RFC3339),
		CurrentInboxFilename:  p.currentInbox,
		LastProcessedFilename: p.lastProcessed,
		LastExitCode:          p.lastExitCode,
		LastError:             p.lastError,
	}
	if !p.quietUntil.IsZero() {
		state.QuietUntil = p.quietUntil.UTC().Format(time.RFC3339)
	}
	if !p.currentTurnStarted.IsZero() {
		state.CurrentTurnStartedAt = p.currentTurnStarted.UTC().Format(time.RFC3339)
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	dir := filepath.Dir(p.cfg.RuntimeStateFile)
	tmp, err := os.CreateTemp(dir, ".runtime-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create runtime state tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write runtime state tempfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close runtime state tempfile: %w", err)
	}
	if err := os.Rename(tmpPath, p.cfg.RuntimeStateFile); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename runtime state tempfile: %w", err)
	}
	return nil
}

func (p *Poller) reportPromptAdmission(ctx context.Context, inboxName string, body []byte) {
	if p.cfg.ArtifactAdmissionReporter == nil {
		return
	}
	req := artifactregistry.AdmissionRequest{
		ProjectID:              os.Getenv("DREM_PROJECT"),
		Persona:                p.cfg.Persona,
		AgentRole:              "csuite_persona",
		WorkflowStage:          "persona_turn_prompt_assembly",
		EvidenceTrustThreshold: artifactregistry.TrustLow,
		ReportOnly:             true,
	}
	artifacts := []artifactregistry.Artifact{
		{
			ID:             uuid.New(),
			ArtifactType:   "persona_prompt",
			ContentURI:     "file:" + p.cfg.PromptFile,
			ContentHash:    sha256Bytes([]byte(p.prompt)),
			Title:          p.cfg.Persona + " persona prompt",
			Owner:          p.cfg.Persona,
			PersonaScope:   p.cfg.Persona,
			WorkflowScope:  "persona_turn_prompt_assembly",
			Status:         artifactregistry.StatusCandidate,
			AuthorityClass: artifactregistry.AuthorityTransient,
			EvidenceTrust:  artifactregistry.TrustUnknown,
			Confidence:     artifactregistry.TrustUnknown,
			Metadata: model.JSONField{
				"report_only":    true,
				"candidate_kind": "persona_prompt",
			},
		},
		{
			ID:             uuid.New(),
			ArtifactType:   "csuite_inbox_message",
			ContentURI:     "file:" + filepath.Join(p.cfg.InboxDir, inboxName),
			ContentHash:    sha256Bytes(body),
			Title:          inboxName,
			Owner:          "csuite",
			PersonaScope:   p.cfg.Persona,
			WorkflowScope:  "persona_turn_prompt_assembly",
			Status:         artifactregistry.StatusCandidate,
			AuthorityClass: artifactregistry.AuthorityTransient,
			EvidenceTrust:  artifactregistry.TrustUnknown,
			Confidence:     artifactregistry.TrustUnknown,
			Metadata: model.JSONField{
				"report_only":    true,
				"candidate_kind": "inbox_message",
				"inbox_file":     inboxName,
			},
		},
	}
	result, err := p.cfg.ArtifactAdmissionReporter.AdmitArtifacts(ctx, req, artifacts)
	if err != nil {
		p.cfg.Logger.Warn("context firewall admission report failed",
			p.personaLabel,
			slog.String("inbox_file", inboxName),
			slog.Any("err", err))
		return
	}
	p.cfg.Logger.Info("context firewall admission report recorded",
		p.personaLabel,
		slog.String("inbox_file", inboxName),
		slog.String("context_packet_id", result.Packet.ID.String()),
		slog.Int("decisions", len(result.Decisions)),
		slog.Bool("report_only", true))
}

func (p *Poller) codexTurnPrompt(body []byte) []byte {
	var b bytes.Buffer
	b.WriteString(p.prompt)
	b.WriteString("\n\n---\n\n")
	b.WriteString("Instruction precedence for this turn:\n")
	b.WriteString("1. The current inbox message is the active operator or C-Suite directive. If it explicitly authorizes or orders a scoped action, follow it even when older prompt text, state.md, plan docs, or notes describe a more cautious default.\n")
	b.WriteString("2. Older role boundaries, canon notes, world-state text, and memory files are defaults. Do not cite them back as blockers when the current directive intentionally overrides them.\n")
	b.WriteString("3. Preserve hard safety constraints unless the operator explicitly scopes the break-glass action: no secrets disclosure, no destructive git or Docker commands, no force push, no credential changes, and no restarting sglang.\n")
	b.WriteString("4. When a directive asks you to update notes, memory, prompts, or state, update every relevant durable surface you can access, then report exactly what changed.\n\n")
	b.WriteString("Process this C-Suite inbox message. Write any reply as a well-formed markdown file in your persona outbox; do not rely on stdout for delivery.\n\n")
	b.Write(body)
	return b.Bytes()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// recordSuccess writes stdout to the outbox (unless Claude already
// wrote a well-formed outbox file during the turn — see G3 dual-write
// fix below), moves the inbox file into the archive, clears the sidecar
// failure counter (if any), and updates state.md atomically. Any
// non-fatal error along the way is logged and the function continues so
// the inbox message still gets archived — leaving the message in the
// inbox after a successful reply would cause a duplicate spawn on the
// next tick.
//
// # G3 dual-write fix (plans/containerization-pivot-attack-plan.md §3)
//
// Before this fix, the poller ALWAYS wrapped model stdout into a
// `<ts>-<persona>-reply-<hash>.md` stub and dropped it in the outbox.
// When the persona prompt told it to use the Write tool to emit a
// properly-framed `<ts>-<persona>-to-<recipient>-<subject>.md` file
// with YAML frontmatter, the persona turn produced TWO outbox files:
//   - the frontmatter-bearing file (routes correctly through watcher)
//   - the poller stub (no frontmatter → quarantined)
//
// The dual-write was a fail-open race: either file could be signaled,
// and the stub being signaled meant the delivery hit quarantine. The
// fix: diff the outbox against the pre-invocation snapshot. If the persona wrote
// any well-formed outbox file during the turn, suppress the stub
// entirely. If the persona wrote nothing, STILL suppress the stub (a
// quarantined stub is worse than no output — the operator can inspect
// state.md to see turn completion).
//
// Persona-written files are picked up by the watcher's startup/periodic
// rescan (internal/deliver/rescan.go) so no signal is strictly required,
// but we also proactively signal each new file from this poller tick
// to keep the latency story honest.
//
// After the outbox write lands (if it lands), recordSuccess fsyncs the
// file and fires an HTTP signal to the csuite-watcher in a short-lived
// goroutine (see plans/csuite-watcher-outbox-routing.md §1-3). Signal
// failures never propagate into the poller loop — the watcher's rescan
// path is designed to recover any signal we drop on the floor.
func (p *Poller) recordSuccess(name string, body, stdout []byte, dur time.Duration, outboxBefore map[string]struct{}) error {
	now := p.cfg.Now()

	// G3: detect persona-written outbox files that appeared during the
	// turn. `claudeWritten` is the list of newly-present filenames that
	// parse as well-formed outbox messages (YAML frontmatter + matching
	// `<from>-to-<to>` filename shape). Any such file means the poller
	// stub is redundant and must be suppressed.
	claudeWritten, diffErr := newWellFormedOutboxFiles(p.cfg.OutboxDir, p.cfg.Persona, outboxBefore)
	if diffErr != nil {
		p.cfg.Logger.Warn("outbox diff after model invocation",
			p.personaLabel,
			slog.Any("err", diffErr))
	}

	// Signal each persona-written file. The watcher's rescan path will
	// eventually pick up any signal we drop, but proactive signaling
	// shortens the latency.
	signaler := p.signaler()
	_, signalDisabled := signaler.(disabledSignaler)

	for _, outName := range claudeWritten {
		outPath := filepath.Join(p.cfg.OutboxDir, outName)
		p.cfg.Logger.Info("persona wrote outbox file directly",
			p.personaLabel,
			slog.String("inbox_file", name),
			slog.String("outbox_file", outName),
		)
		if !signalDisabled && signaler != nil {
			fileBody, rerr := readOutboxFile(outPath)
			if rerr != nil {
				p.cfg.Logger.Warn("read claude-written outbox file",
					p.personaLabel,
					slog.String("outbox_file", outName),
					slog.Any("err", rerr))
				continue
			}
			sha := sha256Bytes(fileBody)
			emittedAt := now.UTC().Format(time.RFC3339)
			outPathForSignal := outPath
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				_ = signaler.Signal(ctx, p.cfg.Persona, outPathForSignal, sha, emittedAt)
			}()
		}
	}

	// G3: if the persona wrote at least one outbox file, suppress the stub
	// entirely. We still archive the inbox message and update state.md
	// so the poll loop advances.
	stubSuppressed := len(claudeWritten) > 0

	// G3 second leg: even when the persona wrote NOTHING to outbox, do not
	// fall back to writing a stub. A stub with no frontmatter is
	// guaranteed to hit watcher quarantine; better to record "turn
	// produced no outbox message" in state.md and move on. Operators
	// inspecting a container that has stopped producing outbox files
	// have state.md + container logs to diagnose the silent-turn case.
	stubSuppressedNoOutput := !stubSuppressed && isProbablyStdoutStub(stdout)
	if stubSuppressedNoOutput {
		stubSuppressed = true
		p.cfg.Logger.Info("turn produced no frontmatter-bearing outbox file; stub suppressed",
			p.personaLabel,
			slog.String("inbox_file", name),
			slog.Int("stdout_bytes", len(stdout)),
		)
	}

	var outName, outPath string
	sha := ""
	emittedAt := now.UTC().Format(time.RFC3339)

	if !stubSuppressed {
		// Legacy path: stub-wrap stdout. This branch is now only
		// entered when stdout itself happens to start with "---\n" and
		// contain a closing "\n---" (i.e. Claude emitted a
		// frontmatter-framed reply to stdout directly, which the
		// watcher CAN classify). Rare in practice; kept for backward
		// compatibility with any persona prompt that documents "reply
		// inline with frontmatter on stdout".
		shortID := shortHash(name, body)
		outName = fmt.Sprintf("%s-%s-reply-%s.md",
			now.UTC().Format("20060102T150405Z"),
			p.cfg.Persona,
			shortID,
		)
		outPath = filepath.Join(p.cfg.OutboxDir, outName)
		if err := os.WriteFile(outPath, stdout, 0o644); err != nil {
			return fmt.Errorf("write outbox %q: %w", outPath, err)
		}

		// fsync BEFORE any signal goroutine launches. The watcher will
		// open outPath immediately after receiving the signal; if we
		// signal first and crash before fsync, the watcher either sees a
		// short read or a mismatched sha256 against our signaled value.
		fsync := p.cfg.Fsyncer
		if fsync == nil {
			fsync = osFsyncer{}
		}
		if err := fsync.Fsync(outPath); err != nil {
			p.cfg.Logger.Warn("fsync outbox",
				p.personaLabel,
				slog.String("outbox_file", outName),
				slog.Any("err", err))
		}

		// Compute sha256 of what we just wrote. The signal payload's
		// hash must reference the on-disk contents, not an older
		// value — fsync above guarantees the bytes are durable before
		// we read them back.
		sha = sha256Bytes(stdout)
	}

	// Fire the signal for the stub (if we wrote one) BEFORE attempting
	// the inbox-archive rename. The archive rename can race with a
	// Claude subprocess that uses its Bash tool to `mv` the inbox file
	// into .archive itself (some persona prompts instruct that
	// explicitly). If we archived first and Claude had already moved
	// the file, os.Rename returns ENOENT, the function short-circuits,
	// and the signal never fires — blocking the watcher from ever
	// routing the reply the persona just wrote. The signal only depends
	// on the outbox write + fsync above, which is exactly what a
	// watcher POST already needs. Decoupling here preserves the plan
	// §Signal-failure-isolation guarantee: no post-write bookkeeping
	// can starve the signal.
	if !stubSuppressed && !signalDisabled && signaler != nil {
		// Pre-seed state.md with an "ok" record and no signal outcome
		// yet so the main poll loop can return while the goroutine
		// finishes. Final outcome is written when the signal completes.
		if err := p.writeStateExt(name, "ok", 0, dur, ""); err != nil {
			p.cfg.Logger.Warn("write state (pending signal)",
				p.personaLabel,
				slog.Any("err", err))
		}
		outboxAbs := outPath
		go func() {
			// Separate context: the signal call survives a main-loop
			// cancellation so an in-flight retry can complete. Timeout
			// is bounded by the Signaler's backoff budget (~13s worst
			// case) plus a little slack for the final HTTP attempt.
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			outcome := signaler.Signal(ctx, p.cfg.Persona, outboxAbs, sha, emittedAt)
			if err := p.writeStateExt(name, "ok", 0, dur, outcome); err != nil {
				p.cfg.Logger.Warn("write state (post-signal)",
					p.personaLabel,
					slog.Any("err", err))
			}
		}()
	}

	// Attempt to archive the inbox file. An ENOENT here is benign:
	// The child model process may have already `mv`d the file into
	// .archive via its Bash tool (see persona prompts for seth/alex
	// which instruct exactly that). Treating ENOENT as an error would
	// cause the whole recordSuccess to unwind and drop state.md — even
	// though the happy-path work (write outbox + fsync + signal) has
	// already completed. Any other error (permission denied, target
	// dir missing) still propagates so an operator notices.
	archivePath := filepath.Join(p.cfg.ArchiveDir, name)
	archived := true
	if err := os.Rename(filepath.Join(p.cfg.InboxDir, name), archivePath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("archive inbox %q: %w", name, err)
		}
		archived = false
		p.cfg.Logger.Info("inbox file already moved before archive",
			p.personaLabel,
			slog.String("inbox_file", name),
		)
	}
	// Clear sidecar counter (if the prior attempts failed before this
	// tick succeeded). Idempotent — no-op when the sidecar is absent.
	_ = os.Remove(failuresPath(p.cfg.InboxDir, name))

	p.cfg.Logger.Info("processed message",
		p.personaLabel,
		slog.String("inbox_file", name),
		slog.String("outbox_file", outName),
		slog.Bool("stub_suppressed", stubSuppressed),
		slog.Int("claude_written_outbox_files", len(claudeWritten)),
		slog.Bool("archived", archived),
		slog.Duration("duration", dur),
	)

	// Short-circuit: when signaling is disabled, or we suppressed the
	// stub (so no signal goroutine will fire), write the terminal
	// state.md record inline.
	if stubSuppressed || signalDisabled || signaler == nil {
		finalOutcome := SignalDisabled
		if stubSuppressed && !signalDisabled && signaler != nil {
			// We DID fire signals for each persona-written file above,
			// but those are fire-and-forget (no state.md feedback
			// channel). Use a neutral marker so operators can tell
			// "stub was suppressed; persona wrote its own files" apart
			// from "signaling was disabled".
			finalOutcome = SignalOK
		}
		return p.writeStateExt(name, "ok", 0, dur, finalOutcome)
	}
	return nil
}

// newWellFormedOutboxFiles lists outbox filenames that appeared after
// the model invocation (absent from `before`) AND match the
// `<ts>-<persona>-to-<recipient>-*.md` filename shape AND parse as
// valid frontmatter. Returns the names (not paths), sorted for
// determinism.
func newWellFormedOutboxFiles(outboxDir, persona string, before map[string]struct{}) ([]string, error) {
	entries, err := os.ReadDir(outboxDir)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		if _, existed := before[name]; existed {
			continue
		}
		if !filenameLooksLikePersonaToRecipient(name, persona) {
			// New file with an unrecognised filename shape: ignore (do
			// not treat as a persona-written outbox — conservative).
			continue
		}
		// Validate frontmatter by peek-reading the first few KiB.
		path := filepath.Join(outboxDir, name)
		if !fileHasFrontmatter(path) {
			continue
		}
		found = append(found, name)
	}
	sort.Strings(found)
	return found, nil
}

// snapshotOutbox returns a set of outbox filenames currently on disk.
// A missing outbox directory is treated as empty rather than an error
// so first-run personas don't blow up the snapshot.
func snapshotOutbox(outboxDir string) (map[string]struct{}, error) {
	set := make(map[string]struct{})
	entries, err := os.ReadDir(outboxDir)
	if err != nil {
		if os.IsNotExist(err) {
			return set, nil
		}
		return set, err
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		set[ent.Name()] = struct{}{}
	}
	return set, nil
}

// filenameLooksLikePersonaToRecipient returns true if name matches the
// convention `<ts>-<persona>-to-<recipient>-<subject>.md` for the given
// persona. The recipient token is not validated against a closed list
// here — classify.go already enforces the recipient whitelist downstream.
func filenameLooksLikePersonaToRecipient(name, persona string) bool {
	// Cheap substring match: we want "-<persona>-to-" somewhere in
	// the name. The watcher's own classifier reads the YAML `to:`
	// field for the authoritative routing decision, so the filename
	// check is purely a trigger to decide "did Claude write a real
	// message file?".
	return strings.Contains(name, "-"+persona+"-to-")
}

// fileHasFrontmatter returns true if the first bytes of path start
// with "---\n" and contain a matching "\n---" close. Matches the
// watcher's extractFrontmatter contract without importing that package
// (keeping the persona package free of deliver/ dependencies).
func fileHasFrontmatter(path string) bool {
	f, err := os.Open(path) //nolint:gosec // path under configured outbox
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8*1024)
	n, err := f.Read(buf)
	if err != nil && err.Error() != "EOF" {
		return false
	}
	data := buf[:n]
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return false
	}
	rest := data[len("---\n"):]
	return bytes.Contains(rest, []byte("\n---"))
}

// readOutboxFile reads a file from disk in full. Used when we need to
// hash a persona-written outbox file for the signal payload.
func readOutboxFile(path string) ([]byte, error) {
	return os.ReadFile(path) //nolint:gosec // path under configured outbox
}

// isProbablyStdoutStub returns true if stdout looks like an
// unstructured reply (no frontmatter). A frontmatter-framed stdout is
// rare but possible; when present we keep the legacy stub-wrap path so
// the reply still routes.
func isProbablyStdoutStub(stdout []byte) bool {
	if !bytes.HasPrefix(stdout, []byte("---\n")) {
		return true
	}
	return !bytes.Contains(stdout[len("---\n"):], []byte("\n---"))
}

// signaler returns the configured Signaler, defaulting to the
// disabled no-op when the caller left the field nil.
func (p *Poller) signaler() Signaler {
	if p.cfg.Signaler == nil {
		return disabledSignaler{}
	}
	return p.cfg.Signaler
}

// recordFailure bumps the message's sidecar counter and, if the counter
// has reached MaxFailures, moves the message to <archive>/<name>.failed
// so the loop moves on. Otherwise the message stays in the inbox and is
// retried on the next tick.
func (p *Poller) recordFailure(name, reason string, exitCode int, dur time.Duration) error {
	sidecar := failuresPath(p.cfg.InboxDir, name)
	n := readFailures(sidecar) + 1
	if err := writeFailures(sidecar, n); err != nil {
		p.cfg.Logger.Warn("write failures sidecar",
			p.personaLabel,
			slog.String("file", name),
			slog.Any("err", err))
	}
	p.cfg.Logger.Warn("model invocation failed",
		p.personaLabel,
		slog.String("file", name),
		slog.Int("exit_code", exitCode),
		slog.Int("failure_count", n),
		slog.String("reason", reason),
		slog.Duration("duration", dur),
	)

	if n >= p.cfg.MaxFailures {
		failedPath := filepath.Join(p.cfg.ArchiveDir, name+".failed")
		if err := os.Rename(filepath.Join(p.cfg.InboxDir, name), failedPath); err != nil {
			return fmt.Errorf("archive failed message %q: %w", name, err)
		}
		_ = os.Remove(sidecar)
		p.cfg.Logger.Error("message archived as failed",
			p.personaLabel,
			slog.String("file", name),
			slog.Int("attempts", n),
		)
		return p.writeState(name+".failed", "exhausted", exitCode, dur)
	}
	return p.writeState(name, "retrying", exitCode, dur)
}

// writeState atomically replaces state.md with a fresh record of the
// most-recently-processed message. Shorthand for writeStateExt with an
// empty signal outcome — retained so existing call sites (failure
// paths) don't have to care about the signal field.
func (p *Poller) writeState(lastFile, status string, exitCode int, dur time.Duration) error {
	return p.writeStateExt(lastFile, status, exitCode, dur, "")
}

// writeStateExt is the full-fat state writer. Empty signal outcomes
// render as last_signal_status: pending so an operator tailing
// state.md can tell the difference between "no signal yet" and
// "signaling is disabled".
func (p *Poller) writeStateExt(lastFile, status string, exitCode int, dur time.Duration, signal SignalOutcome) error {
	now := p.cfg.Now().UTC().Format(time.RFC3339)
	sigLabel := string(signal)
	if sigLabel == "" {
		sigLabel = "pending"
	}
	body := fmt.Sprintf(
		"# %s persona state\n\nlast_processed: %s\nlast_status: %s\nlast_exit_code: %d\nlast_duration_ms: %d\nlast_signal_status: %s\nupdated_at: %s\n",
		p.cfg.Persona,
		lastFile,
		status,
		exitCode,
		dur.Milliseconds(),
		sigLabel,
		now,
	)
	dir := filepath.Dir(p.cfg.StateFile)
	tmp, err := os.CreateTemp(dir, ".state-*.md.tmp")
	if err != nil {
		return fmt.Errorf("create state tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write state tempfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close state tempfile: %w", err)
	}
	if err := os.Rename(tmpPath, p.cfg.StateFile); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename state tempfile: %w", err)
	}
	return nil
}

// --------------------------------------------------------------------
// Sidecar helpers.
// --------------------------------------------------------------------

// failuresPath returns the sidecar path used to count consecutive
// failures against a given inbox file. It sits next to the message so a
// human running `ls inbox/` can see the count at a glance.
func failuresPath(inboxDir, name string) string {
	return filepath.Join(inboxDir, name+".failures")
}

func readFailures(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func writeFailures(path string, n int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(n)+"\n"), 0o644)
}

// shortHash returns a stable 10-char hex id that identifies a message
// in the outbox filename. The inbox filename + first 256 bytes of the
// body is more than enough entropy for our volumes.
func shortHash(name string, body []byte) string {
	h := sha1.New()
	_, _ = h.Write([]byte(name))
	if len(body) > 256 {
		body = body[:256]
	}
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))[:10]
}
