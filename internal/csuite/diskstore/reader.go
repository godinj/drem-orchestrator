package diskstore

// Inbox reader for the diskstore — lifted from cmd/drem/csuite_inbox.go,
// csuite_send_format.go and csuite_send_waiter.go so the bridge HTTP
// server can enumerate on-disk inbox files using the same parsing rules
// the CLI uses. A follow-up can dedupe; for now the duplication keeps
// cmd/drem out of the watcher build graph.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// inboxEntry is the parsed form of a single inbox file. Frontmatter == nil
// means the file exists but had no (or malformed) frontmatter — still
// surfaced so the caller can decide what to do with it.
type inboxEntry struct {
	Path        string
	Frontmatter map[string]any
	Body        string
	ModTime     time.Time
}

// listInboxEntries reads all *.md files in dir, parses each file's
// frontmatter + body, and sorts by sent_at ascending (mtime fallback).
// Missing dir is treated as an empty inbox — returns a zero-length
// slice and a nil error.
func listInboxEntries(dir string) ([]inboxEntry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read inbox dir %q: %w", dir, err)
	}

	out := make([]inboxEntry, 0, len(des))
	for _, de := range des {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		// Skip atomic-write tempfiles.
		if strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(dir, name)

		info, statErr := de.Info()
		if statErr != nil {
			continue
		}

		data, readErr := os.ReadFile(path) //nolint:gosec // path under configured tree
		if readErr != nil {
			out = append(out, inboxEntry{
				Path:    path,
				ModTime: info.ModTime(),
			})
			continue
		}

		fm, _ := parseFrontmatterMap(data)
		body, _ := splitBody(data)

		out = append(out, inboxEntry{
			Path:        path,
			Frontmatter: fm,
			Body:        body,
			ModTime:     info.ModTime(),
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		return entrySortKey(out[i]).Before(entrySortKey(out[j]))
	})
	return out, nil
}

// entrySortKey returns the time used to order an inboxEntry: the parsed
// sent_at frontmatter if present + parseable, else the file mtime.
func entrySortKey(e inboxEntry) time.Time {
	if e.Frontmatter != nil {
		switch v := e.Frontmatter["sent_at"].(type) {
		case time.Time:
			if !v.IsZero() {
				return v
			}
		case string:
			if v != "" {
				if t, err := time.Parse(time.RFC3339, v); err == nil {
					return t
				}
			}
		}
	}
	return e.ModTime
}

// parseFrontmatterMap extracts the YAML frontmatter bytes from raw and
// decodes them into a free-form map. Returns an error if delimiters are
// missing.
func parseFrontmatterMap(raw []byte) (map[string]any, error) {
	fmBytes, ok := extractFrontmatterBytes(raw)
	if !ok {
		return nil, fmt.Errorf("reply has no frontmatter")
	}
	out := map[string]any{}
	if err := yaml.Unmarshal(fmBytes, &out); err != nil {
		return nil, fmt.Errorf("parse frontmatter yaml: %w", err)
	}
	return out, nil
}

// splitBody locates the closing frontmatter delimiter and returns the
// trimmed body. Matches the delimiter shape used in
// internal/deliver/classify.go: leading "---\n" and closing "\n---"
// followed by optional trailing newlines.
func splitBody(data []byte) (string, bool) {
	const open = "---\n"
	if len(data) < len(open) || string(data[:len(open)]) != open {
		return "", false
	}
	rest := data[len(open):]
	idx := strings.Index(string(rest), "\n---")
	if idx < 0 {
		return "", false
	}
	after := rest[idx+len("\n---"):]
	s := strings.TrimLeft(string(after), "\n")
	return s, true
}

// extractFrontmatterBytes returns the bytes between the opening "---\n"
// and the closing "\n---" delimiters. Mirrors deliver.extractFrontmatter.
func extractFrontmatterBytes(data []byte) ([]byte, bool) {
	const open = "---\n"
	const closeMarker = "\n---"
	if len(data) < len(open) || string(data[:len(open)]) != open {
		return nil, false
	}
	rest := data[len(open):]
	idx := strings.Index(string(rest), closeMarker)
	if idx < 0 {
		return nil, false
	}
	return rest[:idx], true
}

// frontmatterString extracts a string value from the frontmatter map,
// tolerating both string and time.Time (yaml.v3 auto-parses RFC3339
// timestamps into time.Time when the target is `any`).
func frontmatterString(fm map[string]any, key string) string {
	if fm == nil {
		return ""
	}
	switch v := fm[key].(type) {
	case string:
		return v
	case time.Time:
		return v.Format(time.RFC3339)
	}
	return ""
}

// stableID derives a deterministic UUID for an inbox file. persona is
// the directory name (kyle | mike | alex | seth | operator); corrid is
// the frontmatter `correlation_id:` value (or the substring parsed from
// the filename when frontmatter is missing); filename is the basename
// with no `.archive/` prefix. Survives archive-move because none of
// those three change when the file is renamed.
func stableID(persona, corrid, filename string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(persona+"/"+corrid+"/"+filename))
}

// corridFromFilename parses the trailing `<corrid>` token off the
// filename convention `<ts>-<from>-to-<to>-<corrid>.md`. Returns "" if
// the filename doesn't match the convention. Used as a fallback when
// frontmatter is missing.
func corridFromFilename(name string) string {
	base := strings.TrimSuffix(filepath.Base(name), ".md")
	idx := strings.LastIndex(base, "-")
	if idx < 0 || idx == len(base)-1 {
		return ""
	}
	return base[idx+1:]
}

// entryToMessage converts a parsed inbox file into the
// CsuiteInboxMessage shape the bridge serves. Maps frontmatter
// topic→Subject, body→Body, sent_at→CreatedAt. ID is derived
// deterministically from persona/corrid/filename so it survives
// archive-move and is stable across calls.
//
// persona is the inbox-owning agent (i.e. the directory the file lives
// in: <root>/<persona>/inbox/). FromAgent/ToAgent come from frontmatter.
// Priority and Type default to PriorityNormal / MessageTypeStatus when
// absent or unparseable — the on-disk schema doesn't carry these fields
// today.
func entryToMessage(e inboxEntry, persona string) csuite.CsuiteInboxMessage {
	from := frontmatterString(e.Frontmatter, "from")
	to := frontmatterString(e.Frontmatter, "to")
	if to == "" {
		// Fallback: the file lives in <persona>/inbox/, so the recipient
		// is the persona by definition.
		to = persona
	}
	subject := frontmatterString(e.Frontmatter, "topic")

	createdAt := entrySortKey(e)

	corrid := frontmatterString(e.Frontmatter, "correlation_id")
	if corrid == "" {
		corrid = corridFromFilename(e.Path)
	}
	filename := filepath.Base(e.Path)
	id := stableID(persona, corrid, filename)

	priority := csuite.PriorityNormal
	if p := frontmatterString(e.Frontmatter, "priority"); p != "" {
		if parsed, err := csuite.ParseInboxPriority(p); err == nil {
			priority = parsed
		}
	}
	msgType := csuite.MessageTypeStatus
	if t := frontmatterString(e.Frontmatter, "type"); t != "" {
		if parsed, err := csuite.ParseInboxMessageType(t); err == nil {
			msgType = parsed
		}
	}

	// Archived flag is true when the file lives under inbox/.archive/.
	archived := strings.Contains(e.Path, string(filepath.Separator)+".archive"+string(filepath.Separator))

	return csuite.CsuiteInboxMessage{
		ID:        id,
		FromAgent: from,
		ToAgent:   to,
		Subject:   subject,
		Body:      e.Body,
		Priority:  priority,
		Type:      msgType,
		Archived:  archived,
		CreatedAt: createdAt,
		UpdatedAt: e.ModTime,
	}
}
