package persona

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Poller owns the inbox scan loop. Construct one with New, then call
// Run(ctx) from the binary's main goroutine. Run blocks until ctx is
// cancelled and finishes any in-flight `claude -p` invocation before
// returning so SIGTERM does not abandon work mid-turn.
type Poller struct {
	cfg          Config
	spawner      Spawner
	prompt       string
	personaLabel slog.Attr
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
	return &Poller{
		cfg:          cfg,
		spawner:      spawner,
		prompt:       string(body),
		personaLabel: slog.String("persona", cfg.Persona),
	}, nil
}

// Run executes the poll loop until ctx is cancelled.
//
// On each tick Run calls scanOnce, which:
//
//  1. Lists the inbox directory for *.md entries (excluding sidecar
//     .failures counters and the .archive subdir).
//  2. Sorts them by mtime ascending so the oldest message is processed
//     first. Deterministic ordering survives container restarts.
//  3. For each file, calls processMessage to invoke claude -p once and
//     either archive the message (success) or bump its .failures sidecar
//     (non-zero exit / spawn error).
//
// If ctx is cancelled mid-tick the function returns nil as soon as the
// current processMessage call returns. The spawner is responsible for
// honouring its ctx deadline (claudeSpawner uses CommandContext).
func (p *Poller) Run(ctx context.Context) error {
	p.cfg.Logger.Info("persona poller starting",
		p.personaLabel,
		slog.String("inbox", p.cfg.InboxDir),
		slog.String("outbox", p.cfg.OutboxDir),
		slog.Duration("poll_interval", p.cfg.PollInterval),
		slog.Duration("claude_timeout", p.cfg.ClaudeTimeout),
	)

	// Process once immediately so the first tick is not delayed by
	// PollInterval on container startup; this also makes tests faster.
	if err := p.scanOnce(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		p.cfg.Logger.Error("scan failed", p.personaLabel, slog.Any("err", err))
	}

	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			p.cfg.Logger.Info("persona poller stopping", p.personaLabel)
			return nil
		case <-ticker.C:
			if err := p.scanOnce(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				p.cfg.Logger.Error("scan failed", p.personaLabel, slog.Any("err", err))
			}
		}
	}
}

// scanOnce lists the inbox, orders by mtime ascending, and processes
// each message sequentially. Errors from processMessage are logged but
// do not abort the tick — the next file still gets its chance.
func (p *Poller) scanOnce(ctx context.Context) error {
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

	for _, f := range files {
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

// processMessage handles a single inbox file end-to-end. The happy path
// is: read -> spawn claude -p -> write reply to outbox -> move to
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

	invocationCtx, cancel := context.WithTimeout(ctx, p.cfg.ClaudeTimeout)
	defer cancel()

	// Pipe the message body via stdin rather than as a positional
	// argument to `-p`. Motivation: csuite messages begin with a YAML
	// frontmatter block (`---\nfrom: kyle\n---\n\n...`) and claude's
	// CLI flag parser interprets any positional arg that starts with a
	// dash as a flag, producing "unknown option '---\nfrom: ...'" and
	// exiting non-zero before any API call. `claude -p` with no body
	// reads stdin (it's the documented form for long inputs), which
	// sidesteps the argv-parsing surface entirely.
	args := []string{
		"claude",
		"--dangerously-skip-permissions",
		"-p",
		"--system-prompt", p.prompt,
		"--output-format", "text",
	}

	start := p.cfg.Now()
	stdout, exitCode, spawnErr := p.spawner.Spawn(invocationCtx, args, bytes.NewReader(body))
	duration := p.cfg.Now().Sub(start)

	if spawnErr != nil {
		// Spawn failure (binary missing, context cancelled mid-launch).
		// Treat as a retriable failure unless ctx cancellation indicates
		// the caller is shutting down, in which case defer to the next
		// container start.
		if ctx.Err() != nil {
			return nil
		}
		return p.recordFailure(name, spawnErr.Error(), -1, duration)
	}

	if exitCode != 0 {
		reason := strings.TrimSpace(string(stdout))
		if reason == "" {
			reason = "non-zero exit"
		}
		return p.recordFailure(name, reason, exitCode, duration)
	}

	return p.recordSuccess(name, body, stdout, duration)
}

// recordSuccess writes stdout to the outbox, moves the inbox file into
// the archive, clears the sidecar failure counter (if any), and updates
// state.md atomically. Any non-fatal error along the way is logged and
// the function continues so the inbox message still gets archived —
// leaving the message in the inbox after a successful reply would cause
// a duplicate spawn on the next tick.
func (p *Poller) recordSuccess(name string, body, stdout []byte, dur time.Duration) error {
	now := p.cfg.Now()
	shortID := shortHash(name, body)
	outName := fmt.Sprintf("%s-%s-reply-%s.md",
		now.UTC().Format("20060102T150405Z"),
		p.cfg.Persona,
		shortID,
	)
	outPath := filepath.Join(p.cfg.OutboxDir, outName)
	if err := os.WriteFile(outPath, stdout, 0o644); err != nil {
		return fmt.Errorf("write outbox %q: %w", outPath, err)
	}

	archivePath := filepath.Join(p.cfg.ArchiveDir, name)
	if err := os.Rename(filepath.Join(p.cfg.InboxDir, name), archivePath); err != nil {
		return fmt.Errorf("archive inbox %q: %w", name, err)
	}
	// Clear sidecar counter (if the prior attempts failed before this
	// tick succeeded).
	_ = os.Remove(failuresPath(p.cfg.InboxDir, name))

	p.cfg.Logger.Info("processed message",
		p.personaLabel,
		slog.String("inbox_file", name),
		slog.String("outbox_file", outName),
		slog.Duration("duration", dur),
	)
	return p.writeState(name, "ok", 0, dur)
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
	p.cfg.Logger.Warn("claude invocation failed",
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
// most-recently-processed message. Atomicity comes from a temp-file +
// rename dance so a concurrent reader never sees a half-written file.
func (p *Poller) writeState(lastFile, status string, exitCode int, dur time.Duration) error {
	now := p.cfg.Now().UTC().Format(time.RFC3339)
	body := fmt.Sprintf(
		"# %s persona state\n\nlast_processed: %s\nlast_status: %s\nlast_exit_code: %d\nlast_duration_ms: %d\nupdated_at: %s\n",
		p.cfg.Persona,
		lastFile,
		status,
		exitCode,
		dur.Milliseconds(),
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
