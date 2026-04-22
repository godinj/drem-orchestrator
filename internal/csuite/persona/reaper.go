package persona

// reaper.go implements the startup scan that reconciles orphan
// `.failures` sidecar files against their companion `.md` anchors.
// Scoreboard item 3 / attack plan §3 Group A: when Claude's Bash tool
// moves an inbox file into .archive/ BEFORE the poller sees a
// successful exit code (some persona prompts instruct Claude to `mv`
// the inbox file into .archive/ itself), the poller's recordFailure
// path — which was triggered by a prior failed attempt — leaves a
// `<name>.failures` counter sidecar in the inbox with no anchor.md to
// pair with. These orphan counters never get cleaned up automatically
// and they never surface to an operator.
//
// Two seen in Seth's inbox as of 2026-04-22:
//   - 20260421T235306Z-kyle-tui-retry-storm-design.md.failures
//   - 20260422-072000-kyle-containerization-pivot-gap-synthesis.md.failures
//
// Both had their .md anchors sitting in .archive/ already (Claude's
// retry attempt succeeded on a later tick but the stale counter
// lingered).
//
// # Reaper policy
//
// For each `<name>.failures` sidecar in the inbox:
//   a) If `<name>` is ALSO in the inbox → active retry in progress; leave.
//   b) If `<name>` is in `<archive>/` → successful retry; reap (delete
//      the orphan counter).
//   c) If `<name>` is nowhere → true failure that outran our retention;
//      emit ERROR log with the counter value so an operator notices.
//
// The reaper runs once at poller startup. A periodic run inside the
// poll loop is not necessary: new orphan counters only appear as a
// result of recordFailure + Claude-Bash-rename races, and those new
// orphans are caught on the next container restart. Operators who
// want a fresh scan can `docker compose restart csuite-<persona>`.

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// ReapResult summarises a single reaper run. All fields are populated
// so operators can grep for either the scanned-but-empty case or the
// active-sidecars-with-anchors case without ambiguity.
type ReapResult struct {
	// Scanned is the number of .failures files seen in the inbox.
	Scanned int
	// Reaped counts sidecars deleted because their .md anchor was in
	// the archive (the successful-retry-orphan case).
	Reaped int
	// Active counts sidecars left in place because their .md anchor
	// is still in the inbox (an active retry in progress).
	Active int
	// TrueFailures counts sidecars where the anchor is nowhere to be
	// found (in neither inbox nor archive). Those get an ERROR log
	// per sidecar.
	TrueFailures int
}

// ReapOnceOnStartup walks cfg.InboxDir for `*.md.failures` sidecars
// and applies the reaper policy described above. Errors in individual
// files are logged and do not abort the walk.
//
// Exported so cmd/csuite-persona can call it before starting the
// poller; the poller itself does not invoke this (deliberate — keeps
// the poll-loop surface thin and removes the risk of a reap running
// concurrently with an active retry).
func ReapOnceOnStartup(cfg Config) ReapResult {
	res := ReapResult{}
	if cfg.Logger == nil {
		// Safeguard: the public entrypoint should always have a
		// Logger per the Config contract, but tests may call with a
		// minimal Config.
		return res
	}
	entries, err := os.ReadDir(cfg.InboxDir)
	if err != nil {
		cfg.Logger.Warn("reaper: read inbox",
			slog.String("persona", cfg.Persona),
			slog.String("inbox", cfg.InboxDir),
			slog.Any("err", err))
		return res
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".failures") {
			continue
		}
		res.Scanned++
		anchorName := strings.TrimSuffix(name, ".failures")
		sidecarPath := filepath.Join(cfg.InboxDir, name)

		// (a) anchor still in inbox → active retry; leave alone.
		if _, err := os.Stat(filepath.Join(cfg.InboxDir, anchorName)); err == nil {
			res.Active++
			continue
		}

		// (b) anchor landed in archive → orphan; reap.
		archivePath := filepath.Join(cfg.ArchiveDir, anchorName)
		if _, err := os.Stat(archivePath); err == nil {
			if err := os.Remove(sidecarPath); err != nil {
				cfg.Logger.Warn("reaper: remove orphan sidecar",
					slog.String("persona", cfg.Persona),
					slog.String("sidecar", name),
					slog.Any("err", err))
				continue
			}
			cfg.Logger.Info("reaper: reaped orphan sidecar",
				slog.String("persona", cfg.Persona),
				slog.String("sidecar", name),
				slog.String("anchor_location", "archive"))
			res.Reaped++
			continue
		}

		// (b') anchor landed in archive as <name>.failed → also a
		// true reap (the failure-retention path completed and the
		// anchor is archived under its failed name).
		failedPath := filepath.Join(cfg.ArchiveDir, anchorName+".failed")
		if _, err := os.Stat(failedPath); err == nil {
			if err := os.Remove(sidecarPath); err != nil {
				cfg.Logger.Warn("reaper: remove failed-anchor sidecar",
					slog.String("persona", cfg.Persona),
					slog.String("sidecar", name),
					slog.Any("err", err))
				continue
			}
			cfg.Logger.Info("reaper: reaped sidecar (anchor failed-archived)",
				slog.String("persona", cfg.Persona),
				slog.String("sidecar", name))
			res.Reaped++
			continue
		}

		// (c) anchor nowhere to be found — true failure beyond
		// retention. Emit ERROR with the counter value so an operator
		// notices, but leave the sidecar in place (manual review).
		counter := readFailures(sidecarPath)
		cfg.Logger.Error("reaper: true-failure orphan sidecar (anchor missing everywhere)",
			slog.String("persona", cfg.Persona),
			slog.String("sidecar", name),
			slog.Int("failure_count", counter))
		res.TrueFailures++
	}

	cfg.Logger.Info("reaper: startup scan complete",
		slog.String("persona", cfg.Persona),
		slog.Int("scanned", res.Scanned),
		slog.Int("reaped", res.Reaped),
		slog.Int("active", res.Active),
		slog.Int("true_failures", res.TrueFailures))
	return res
}

// summary returns a short human-readable summary for logging /
// tests. Currently unused in production but kept for tests that want
// to assert on a single field.
func (r ReapResult) summary() string {
	return fmt.Sprintf("scanned=%d reaped=%d active=%d true_failures=%d",
		r.Scanned, r.Reaped, r.Active, r.TrueFailures)
}
