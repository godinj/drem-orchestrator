// Package diskstore implements internal/serve.dashboardStore against
// the on-disk csuite tree at <root> (default /csuite). Reads and writes
// use the same filename + frontmatter convention `drem csuite send` and
// the persona poller agree on, so a TUI POST is indistinguishable from
// a CLI send.
package diskstore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/google/uuid"
)

// knownPersonas is the fixed set of C-Suite agents whose inbox tree
// the bridge serves. Mirrors internal/csuite/disk_source.go's
// knownAgents to keep the AgentDashboard ordering stable.
var knownPersonas = csuite.KnownPersonas

type personaConfig struct {
	Model string `json:"model,omitempty"`
}

// Store implements internal/serve.dashboardStore against the on-disk
// csuite tree. It composes DiskSnapshotSource for AgentDashboard and
// re-implements message CRUD against the per-persona inbox/ trees.
type Store struct {
	root   string
	source *csuite.DiskSnapshotSource
}

// New constructs a Store rooted at root. If root is empty, resolution
// order is DREM_CSUITE_ROOT env var, then "/csuite" (the in-container
// default — the watcher service bind-mounts the project C-Suite root there).
func New(root string) *Store {
	if root == "" {
		root = os.Getenv("DREM_CSUITE_ROOT")
	}
	if root == "" {
		root = "/csuite"
	}
	return &Store{
		root:   root,
		source: csuite.NewDiskSnapshotSource(root),
	}
}

// Root returns the absolute root directory the Store reads from. Useful
// for test diagnostics.
func (s *Store) Root() string { return s.root }

// PersonaModels returns each known persona's configured model. Missing or
// invalid config falls back to csuite.DefaultPersonaModel so the bridge stays
// readable even if an operator edits config.json by hand.
func (s *Store) PersonaModels() (map[string]string, error) {
	models := make(map[string]string, len(knownPersonas))
	for _, persona := range knownPersonas {
		model, err := s.PersonaModel(persona)
		if err != nil && !errors.Is(err, csuite.ErrInvalidPersonaModel) {
			return nil, err
		}
		models[persona] = model
	}
	return models, nil
}

// PersonaModel reads <root>/<persona>/config.json and returns its model. A
// missing config or empty model falls back to csuite.DefaultPersonaModel.
func (s *Store) PersonaModel(persona string) (string, error) {
	if !csuite.IsKnownPersona(persona) {
		return "", csuite.ErrUnknownPersona
	}
	data, err := os.ReadFile(s.personaConfigPath(persona))
	if errors.Is(err, os.ErrNotExist) {
		return csuite.DefaultPersonaModel, nil
	}
	if err != nil {
		return "", fmt.Errorf("diskstore: read persona config: %w", err)
	}
	var cfg personaConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return csuite.DefaultPersonaModel, fmt.Errorf("diskstore: parse persona config: %w", err)
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return csuite.DefaultPersonaModel, nil
	}
	model, err := csuite.NormalizePersonaModel(cfg.Model)
	if err != nil {
		return csuite.DefaultPersonaModel, err
	}
	return model, nil
}

// SetPersonaModel writes <root>/<persona>/config.json atomically.
func (s *Store) SetPersonaModel(persona, model string) error {
	if !csuite.IsKnownPersona(persona) {
		return csuite.ErrUnknownPersona
	}
	model, err := csuite.NormalizePersonaModel(model)
	if err != nil {
		return err
	}
	path := s.personaConfigPath(persona)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("diskstore: create persona config dir: %w", err)
	}
	data, err := json.MarshalIndent(personaConfig{Model: model}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("diskstore: create persona config temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("diskstore: write persona config temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("diskstore: fsync persona config temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("diskstore: close persona config temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("diskstore: replace persona config: %w", err)
	}
	return nil
}

func (s *Store) personaConfigPath(persona string) string {
	return filepath.Join(s.root, persona, "config.json")
}

// AgentDashboard returns one row per known persona. UnreadCount mirrors
// the inbox file count from DiskSnapshotSource. LatestInbox is the max
// mtime across <root>/<persona>/inbox/*.md and the .archive sibling.
func (s *Store) AgentDashboard() ([]csuite.AgentDashboardRow, error) {
	summaries, err := s.source.AgentSummaries()
	if err != nil {
		return nil, fmt.Errorf("diskstore: agent summaries: %w", err)
	}
	rows := make([]csuite.AgentDashboardRow, len(summaries))
	for i, sum := range summaries {
		row := csuite.AgentDashboardRow{
			Agent:       sum.CsuiteAgent,
			UnreadCount: sum.UnreadCount,
		}
		if t := s.latestInboxMtime(sum.Name); !t.IsZero() {
			ts := t
			row.LatestInbox = &ts
		}
		rows[i] = row
	}
	return rows, nil
}

// latestInboxMtime returns the max mtime across <root>/<persona>/inbox/
// and <root>/<persona>/inbox/.archive/. Returns the zero time if no
// candidates exist.
func (s *Store) latestInboxMtime(persona string) time.Time {
	var latest time.Time
	scan := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, ".") {
				continue
			}
			info, err := os.Stat(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			if info.ModTime().After(latest) {
				latest = info.ModTime()
			}
		}
	}
	inboxDir := filepath.Join(s.root, persona, "inbox")
	scan(inboxDir)
	scan(filepath.Join(inboxDir, ".archive"))
	return latest
}

// CreateMessage stages msg as a new file under the To agent's inbox/
// directory using the same convention `drem csuite send` writes. On
// success msg.ID is replaced with the deterministic stableID for the
// new file and msg.CreatedAt is set to the on-disk sent_at timestamp,
// so the HTTP handler can echo a stable envelope back to the caller.
func (s *Store) CreateMessage(msg *csuite.CsuiteInboxMessage) error {
	if msg.FromAgent == "" || msg.ToAgent == "" || msg.Subject == "" {
		return fmt.Errorf("diskstore: from_agent, to_agent, and subject must not be empty")
	}
	if !s.acceptsTo(msg.ToAgent) {
		return fmt.Errorf("diskstore: unknown ToAgent %q", msg.ToAgent)
	}

	corrid, err := newCorrelationID()
	if err != nil {
		return fmt.Errorf("diskstore: generate correlation id: %w", err)
	}

	now := time.Now()
	if !msg.CreatedAt.IsZero() {
		now = msg.CreatedAt
	}
	clock := now.UTC()

	path, err := WriteInboxFile(WriterConfig{
		CsuiteHomeRoot: s.root,
		From:           msg.FromAgent,
		To:             msg.ToAgent,
		Topic:          msg.Subject,
		Body:           msg.Body,
		Now:            func() time.Time { return clock },
		CorrelationID:  corrid,
	})
	if err != nil {
		return fmt.Errorf("diskstore: write inbox file: %w", err)
	}

	filename := filepath.Base(path)
	msg.ID = stableID(msg.ToAgent, corrid, filename)
	// Round-trip CreatedAt to the same precision that ends up on disk
	// (the writer formats sent_at as RFC3339, second precision in UTC).
	msg.CreatedAt = clock.Truncate(time.Second)
	return nil
}

// acceptsTo returns true when ToAgent is a known persona or the
// operator. Matches the deliver pipeline's recipient whitelist.
func (s *Store) acceptsTo(to string) bool {
	if to == "operator" {
		return true
	}
	for _, p := range knownPersonas {
		if p == to {
			return true
		}
	}
	return false
}

// GetMessagesBetween returns the merged conversation between agent1 and
// agent2, newest-first, capped at limit (default 50 when 0). When
// beforeID is non-zero, only messages strictly older (by sort key) than
// the message with that ID are returned — cursor pagination matching
// the gorm-backed Store contract.
//
// Resolution rules:
//   - operator <-> persona: read the persona's inbox (filtered from:operator)
//     plus the operator inbox (filtered from:<persona>).
//   - persona <-> persona: read each persona's inbox filtered by the other's
//     `from:` value.
//   - the order of agent1 and agent2 is irrelevant — the merge is symmetric.
func (s *Store) GetMessagesBetween(agent1, agent2 string, limit int, beforeID uuid.UUID) ([]csuite.CsuiteInboxMessage, error) {
	const defaultLimit = 50
	if limit <= 0 {
		limit = defaultLimit
	}

	a, b := agent1, agent2
	// Normalise so operator (when present) is always 'a'. Simplifies the
	// branch logic below; the merge result is order-agnostic anyway.
	if b == "operator" {
		a, b = b, a
	}

	var msgs []csuite.CsuiteInboxMessage
	switch {
	case a == "operator":
		// operator <-> persona b
		msgs = append(msgs, s.readPersonaInbox(b, "operator")...) // operator -> persona
		msgs = append(msgs, s.readPersonaInbox("operator", b)...) // persona  -> operator
	default:
		// persona a <-> persona b
		msgs = append(msgs, s.readPersonaInbox(b, a)...) // a -> b lands in b/inbox
		msgs = append(msgs, s.readPersonaInbox(a, b)...) // b -> a lands in a/inbox
	}

	// Sort DESC by CreatedAt (newest first); stable sub-order by ID for
	// determinism when timestamps collide (RFC3339 second precision).
	sort.SliceStable(msgs, func(i, j int) bool {
		if msgs[i].CreatedAt.Equal(msgs[j].CreatedAt) {
			return msgs[i].ID.String() > msgs[j].ID.String()
		}
		return msgs[i].CreatedAt.After(msgs[j].CreatedAt)
	})

	if beforeID != uuid.Nil {
		cursorIdx := -1
		for i, m := range msgs {
			if m.ID == beforeID {
				cursorIdx = i
				break
			}
		}
		if cursorIdx >= 0 {
			cursorTs := msgs[cursorIdx].CreatedAt
			filtered := msgs[:0]
			for _, m := range msgs {
				if m.CreatedAt.Before(cursorTs) {
					filtered = append(filtered, m)
				}
			}
			msgs = filtered
		}
	}

	if len(msgs) > limit {
		msgs = msgs[:limit]
	}
	return msgs, nil
}

// readPersonaInbox returns all messages in <root>/<inboxOwner>/inbox/
// (live + .archive/) whose frontmatter `from:` field matches fromFilter.
// When fromFilter is "", every message is returned (caller does its own
// filtering).
func (s *Store) readPersonaInbox(inboxOwner, fromFilter string) []csuite.CsuiteInboxMessage {
	out := make([]csuite.CsuiteInboxMessage, 0)
	dirs := []string{
		filepath.Join(s.root, inboxOwner, "inbox"),
		filepath.Join(s.root, inboxOwner, "inbox", ".archive"),
	}
	for _, dir := range dirs {
		entries, err := listInboxEntries(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			from := frontmatterString(e.Frontmatter, "from")
			if fromFilter != "" && from != fromFilter {
				continue
			}
			out = append(out, entryToMessage(e, inboxOwner))
		}
	}
	return out
}

// ListInboxQueue returns live, unarchived inbox items for restart review.
func (s *Store) ListInboxQueue(agent string, limit int) ([]csuite.InboxQueueItem, error) {
	if !s.acceptsTo(agent) {
		return nil, csuite.ErrUnknownPersona
	}
	entries, err := listInboxEntries(filepath.Join(s.root, agent, "inbox"))
	if err != nil {
		return nil, err
	}
	items := make([]csuite.InboxQueueItem, 0, len(entries))
	for _, entry := range entries {
		msg := entryToMessage(entry, agent)
		items = append(items, csuite.InboxQueueItem{
			ID:        msg.ID,
			Filename:  filepath.Base(entry.Path),
			FromAgent: msg.FromAgent,
			ToAgent:   msg.ToAgent,
			Subject:   msg.Subject,
			Body:      msg.Body,
			CreatedAt: msg.CreatedAt,
			UpdatedAt: msg.UpdatedAt,
		})
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// ArchiveInboxItem moves a live inbox item into inbox/.archive.
func (s *Store) ArchiveInboxItem(agent, id, reason string) error {
	return s.moveInboxItem(agent, id, ".archive")
}

// IgnoreInboxItem moves a live inbox item into inbox/.ignored.
func (s *Store) IgnoreInboxItem(agent, id, reason string) error {
	return s.moveInboxItem(agent, id, ".ignored")
}

func (s *Store) moveInboxItem(agent, id, destDirName string) error {
	if !s.acceptsTo(agent) {
		return csuite.ErrUnknownPersona
	}
	wantID, err := uuid.Parse(id)
	if err != nil {
		return csuite.ErrInboxItemNotFound
	}
	inboxDir := filepath.Join(s.root, agent, "inbox")
	entries, err := listInboxEntries(inboxDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entryToMessage(entry, agent).ID != wantID {
			continue
		}
		destDir := filepath.Join(inboxDir, destDirName)
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			return err
		}
		return os.Rename(entry.Path, filepath.Join(destDir, filepath.Base(entry.Path)))
	}
	return csuite.ErrInboxItemNotFound
}

// GetMessageCountByAgent returns the number of inbox files (live +
// .archive) under <root>/<scopedTo>/inbox/. Outbox files are NOT
// counted to avoid double-counting with the recipient's inbox copy.
func (s *Store) GetMessageCountByAgent(scopedTo string) (int, error) {
	if scopedTo == "" {
		return 0, fmt.Errorf("diskstore: scopedTo must not be empty")
	}
	count := 0
	scan := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasSuffix(name, ".md") && !strings.HasPrefix(name, ".") {
				count++
			}
		}
	}
	inboxDir := filepath.Join(s.root, scopedTo, "inbox")
	scan(inboxDir)
	scan(filepath.Join(inboxDir, ".archive"))
	return count, nil
}

// newCorrelationID returns a fresh 8-char hex correlation ID matching
// the format `drem csuite send` uses (4 random bytes -> 8 hex chars).
func newCorrelationID() (string, error) {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}
