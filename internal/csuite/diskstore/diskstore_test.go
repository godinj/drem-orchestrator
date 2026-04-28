package diskstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/google/uuid"
)

// seedFile writes raw content to <root>/<persona>/<subdir>/<name>. Used
// by tests to plant a hand-rolled inbox file alongside the convention
// the writer produces.
func seedFile(t *testing.T, root, persona, subdir, name, content string) string {
	t.Helper()
	dir := filepath.Join(root, persona, subdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// frontmatterFile produces a well-formed inbox file matching the
// drem csuite send convention. sentAt should be UTC; the function
// formats it RFC3339 with second precision.
func frontmatterFile(from, to, topic, body, corrid string, sentAt time.Time) string {
	return "---\n" +
		"from: " + from + "\n" +
		"to: " + to + "\n" +
		"topic: " + topic + "\n" +
		"sent_at: " + sentAt.UTC().Format(time.RFC3339) + "\n" +
		"correlation_id: " + corrid + "\n" +
		"---\n\n" + body + "\n"
}

// stateFile writes a minimal state.md so DiskSnapshotSource produces a
// non-default agent record. The bridge AgentDashboard test uses this
// to assert the right number of rows come back.
func stateFile(t *testing.T, root, persona string, heartbeat time.Time) {
	t.Helper()
	path := filepath.Join(root, persona, "state.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	body := "---\n" +
		"last_heartbeat: " + heartbeat.UTC().Format(time.RFC3339) + "\n" +
		"context_percent: 42\n" +
		"current_activity: testing\n" +
		"---\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestStore_AgentDashboard_PopulatedInboxes(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	for _, p := range knownPersonas {
		stateFile(t, root, p, now.Add(-1*time.Minute))
	}

	// Write 2 inbox files for mike and 1 for alex.
	t1 := now.Add(-10 * time.Minute)
	t2 := now.Add(-5 * time.Minute)
	seedFile(t, root, "mike", "inbox", "20260101T000000Z-operator-to-mike-aaaaaaaa.md",
		frontmatterFile("operator", "mike", "first", "hello mike", "aaaaaaaa", t1))
	seedFile(t, root, "mike", "inbox", "20260101T000100Z-operator-to-mike-bbbbbbbb.md",
		frontmatterFile("operator", "mike", "second", "again", "bbbbbbbb", t2))
	seedFile(t, root, "alex", "inbox", "20260101T000200Z-operator-to-alex-cccccccc.md",
		frontmatterFile("operator", "alex", "alex topic", "yo", "cccccccc", t1))

	// Adjust mtimes so latestInbox check is meaningful.
	mikePath := filepath.Join(root, "mike", "inbox", "20260101T000100Z-operator-to-mike-bbbbbbbb.md")
	if err := os.Chtimes(mikePath, t2, t2); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	store := New(root)
	rows, err := store.AgentDashboard()
	if err != nil {
		t.Fatalf("AgentDashboard: %v", err)
	}
	if len(rows) != len(knownPersonas) {
		t.Fatalf("want %d rows, got %d", len(knownPersonas), len(rows))
	}

	byName := make(map[string]csuite.AgentDashboardRow, len(rows))
	for _, r := range rows {
		byName[r.Agent.Name] = r
	}

	if got := byName["mike"].UnreadCount; got != 2 {
		t.Errorf("mike UnreadCount: want 2, got %d", got)
	}
	if got := byName["alex"].UnreadCount; got != 1 {
		t.Errorf("alex UnreadCount: want 1, got %d", got)
	}
	if got := byName["seth"].UnreadCount; got != 0 {
		t.Errorf("seth UnreadCount: want 0, got %d", got)
	}
	if byName["mike"].LatestInbox == nil {
		t.Errorf("mike LatestInbox: want non-nil")
	}
	if byName["seth"].LatestInbox != nil {
		t.Errorf("seth LatestInbox: want nil, got %v", byName["seth"].LatestInbox)
	}
}

func TestStore_AgentDashboard_EmptyTree(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	rows, err := store.AgentDashboard()
	if err != nil {
		t.Fatalf("AgentDashboard: %v", err)
	}
	if len(rows) != len(knownPersonas) {
		t.Fatalf("want %d rows, got %d", len(knownPersonas), len(rows))
	}
	// Confirm order matches knownPersonas.
	for i, p := range knownPersonas {
		if rows[i].Agent.Name != p {
			t.Errorf("row %d: want %q, got %q", i, p, rows[i].Agent.Name)
		}
		if rows[i].UnreadCount != 0 {
			t.Errorf("row %d UnreadCount: want 0, got %d", i, rows[i].UnreadCount)
		}
		if rows[i].LatestInbox != nil {
			t.Errorf("row %d LatestInbox: want nil, got %v", i, rows[i].LatestInbox)
		}
	}
}

func TestStore_PersonaModelConfig(t *testing.T) {
	root := t.TempDir()
	store := New(root)

	model, err := store.PersonaModel("seth")
	if err != nil {
		t.Fatalf("PersonaModel missing config: %v", err)
	}
	if model != csuite.DefaultPersonaModel {
		t.Fatalf("missing config model = %q, want %q", model, csuite.DefaultPersonaModel)
	}

	if err := store.SetPersonaModel("seth", csuite.PersonaModelGPT55); err != nil {
		t.Fatalf("SetPersonaModel: %v", err)
	}
	model, err = store.PersonaModel("seth")
	if err != nil {
		t.Fatalf("PersonaModel after write: %v", err)
	}
	if model != csuite.PersonaModelGPT55 {
		t.Fatalf("model = %q, want %q", model, csuite.PersonaModelGPT55)
	}

	raw, err := os.ReadFile(filepath.Join(root, "seth", "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(raw), `"model": "openai/gpt-5.5"`) {
		t.Fatalf("config did not contain model: %s", raw)
	}
}

func TestStore_PersonaModelsInvalidConfigFallsBack(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "seth", "", "config.json", `{"model":"not-real"}`)

	models, err := New(root).PersonaModels()
	if err != nil {
		t.Fatalf("PersonaModels: %v", err)
	}
	if models["seth"] != csuite.DefaultPersonaModel {
		t.Fatalf("seth model = %q, want fallback %q", models["seth"], csuite.DefaultPersonaModel)
	}
}

func TestStore_CreateMessage_OperatorToPersona(t *testing.T) {
	root := t.TempDir()
	store := New(root)

	msg := &csuite.CsuiteInboxMessage{
		FromAgent: "operator",
		ToAgent:   "mike",
		Subject:   "hi",
		Body:      "hello mike\nsecond line\n",
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if msg.ID == uuid.Nil {
		t.Errorf("msg.ID was not set")
	}
	if msg.CreatedAt.IsZero() {
		t.Errorf("msg.CreatedAt was not set")
	}

	// Find the file under <root>/mike/inbox/.
	inboxDir := filepath.Join(root, "mike", "inbox")
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var written string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") && !strings.HasPrefix(e.Name(), ".") {
			written = e.Name()
		}
	}
	if written == "" {
		t.Fatalf("no .md file found in %s", inboxDir)
	}
	if !strings.Contains(written, "-operator-to-mike-") {
		t.Errorf("filename %q missing -operator-to-mike- substring", written)
	}

	// Round-trip: read back and confirm body matches.
	raw, err := os.ReadFile(filepath.Join(inboxDir, written))
	if err != nil {
		t.Fatalf("readfile: %v", err)
	}
	body, ok := splitBody(raw)
	if !ok {
		t.Fatalf("splitBody failed; file content:\n%s", raw)
	}
	if body != "hello mike\nsecond line\n" {
		t.Errorf("body roundtrip: want %q, got %q", "hello mike\nsecond line\n", body)
	}
	// Frontmatter ends with `---\n` on its own line (splitBody contract).
	if !strings.Contains(string(raw), "\n---\n") {
		t.Errorf("frontmatter close not on its own line; content:\n%s", raw)
	}
}

func TestStore_CreateMessage_PersonaToOperator(t *testing.T) {
	root := t.TempDir()
	store := New(root)

	msg := &csuite.CsuiteInboxMessage{
		FromAgent: "mike",
		ToAgent:   "operator",
		Subject:   "reply",
		Body:      "ack",
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	inboxDir := filepath.Join(root, "operator", "inbox")
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 file in operator inbox, got %d", len(entries))
	}
	name := entries[0].Name()
	if !strings.Contains(name, "-mike-to-operator-") {
		t.Errorf("filename %q missing -mike-to-operator- substring", name)
	}
}

func TestStore_CreateMessage_RejectUnknownTo(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	msg := &csuite.CsuiteInboxMessage{
		FromAgent: "operator",
		ToAgent:   "ross",
		Subject:   "hi",
		Body:      "x",
	}
	err := store.CreateMessage(msg)
	if err == nil {
		t.Fatalf("want error for unknown ToAgent, got nil")
	}
	if !strings.Contains(err.Error(), "unknown ToAgent") {
		t.Errorf("want 'unknown ToAgent' in error, got %v", err)
	}
}

func TestStore_CreateMessage_RejectEmptyFields(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	cases := []struct {
		name string
		msg  csuite.CsuiteInboxMessage
	}{
		{"empty subject", csuite.CsuiteInboxMessage{FromAgent: "operator", ToAgent: "mike", Subject: "", Body: "x"}},
		{"empty from", csuite.CsuiteInboxMessage{FromAgent: "", ToAgent: "mike", Subject: "s", Body: "x"}},
		{"empty to", csuite.CsuiteInboxMessage{FromAgent: "operator", ToAgent: "", Subject: "s", Body: "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := c.msg
			err := store.CreateMessage(&m)
			if err == nil {
				t.Fatalf("want error, got nil")
			}
		})
	}
}

func TestStore_GetMessagesBetween_OperatorPersona_BothDirections(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	t1 := now.Add(-30 * time.Minute)
	t2 := now.Add(-20 * time.Minute)
	t3 := now.Add(-10 * time.Minute)

	// operator -> mike (lands in mike/inbox/, from: operator)
	seedFile(t, root, "mike", "inbox", "20260101T000000Z-operator-to-mike-aaaaaaaa.md",
		frontmatterFile("operator", "mike", "topic1", "msg1", "aaaaaaaa", t1))
	seedFile(t, root, "mike", "inbox", "20260101T000200Z-operator-to-mike-cccccccc.md",
		frontmatterFile("operator", "mike", "topic3", "msg3", "cccccccc", t3))
	// mike -> operator (lands in operator/inbox/, from: mike)
	seedFile(t, root, "operator", "inbox", "20260101T000100Z-mike-to-operator-bbbbbbbb.md",
		frontmatterFile("mike", "operator", "reply2", "msg2", "bbbbbbbb", t2))
	// Noise: alex -> operator (different from filter — should not appear).
	seedFile(t, root, "operator", "inbox", "20260101T000300Z-alex-to-operator-dddddddd.md",
		frontmatterFile("alex", "operator", "alex reply", "alex msg", "dddddddd", now))

	store := New(root)
	msgs, err := store.GetMessagesBetween("operator", "mike", 0, uuid.Nil)
	if err != nil {
		t.Fatalf("GetMessagesBetween: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d", len(msgs))
	}
	// DESC by CreatedAt: t3, t2, t1.
	if msgs[0].Subject != "topic3" || msgs[1].Subject != "reply2" || msgs[2].Subject != "topic1" {
		t.Errorf("ordering wrong: got [%s, %s, %s]", msgs[0].Subject, msgs[1].Subject, msgs[2].Subject)
	}
}

func TestStore_GetMessagesBetween_ArgOrderAgnostic(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	seedFile(t, root, "alex", "inbox", "20260101T000000Z-operator-to-alex-aaaaaaaa.md",
		frontmatterFile("operator", "alex", "t1", "m1", "aaaaaaaa", now.Add(-3*time.Minute)))
	seedFile(t, root, "operator", "inbox", "20260101T000100Z-alex-to-operator-bbbbbbbb.md",
		frontmatterFile("alex", "operator", "t2", "m2", "bbbbbbbb", now.Add(-1*time.Minute)))

	store := New(root)
	a, err := store.GetMessagesBetween("operator", "alex", 0, uuid.Nil)
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	b, err := store.GetMessagesBetween("alex", "operator", 0, uuid.Nil)
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	if len(a) != len(b) {
		t.Fatalf("len mismatch: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Errorf("row %d: ID mismatch %s vs %s", i, a[i].ID, b[i].ID)
		}
	}
}

func TestStore_GetMessagesBetween_PersonaToPersona(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	// mike -> alex (lands in alex/inbox/, from: mike)
	seedFile(t, root, "alex", "inbox", "20260101T000000Z-mike-to-alex-aaaaaaaa.md",
		frontmatterFile("mike", "alex", "hi alex", "m1", "aaaaaaaa", now.Add(-2*time.Minute)))
	// alex -> mike (lands in mike/inbox/, from: alex)
	seedFile(t, root, "mike", "inbox", "20260101T000100Z-alex-to-mike-bbbbbbbb.md",
		frontmatterFile("alex", "mike", "hi mike", "m2", "bbbbbbbb", now.Add(-1*time.Minute)))
	// Noise: operator -> mike (should not appear in mike<->alex).
	seedFile(t, root, "mike", "inbox", "20260101T000200Z-operator-to-mike-cccccccc.md",
		frontmatterFile("operator", "mike", "noise", "n", "cccccccc", now))

	store := New(root)
	msgs, err := store.GetMessagesBetween("mike", "alex", 0, uuid.Nil)
	if err != nil {
		t.Fatalf("GetMessagesBetween: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages, got %d (subjects: %v)", len(msgs), subjectsOf(msgs))
	}
}

func subjectsOf(msgs []csuite.CsuiteInboxMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Subject
	}
	return out
}

func TestStore_GetMessagesBetween_CursorPagination(t *testing.T) {
	root := t.TempDir()
	base := time.Now().UTC().Truncate(time.Second).Add(-1 * time.Hour)
	for i := 0; i < 5; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		corrid := []string{"aaaa1111", "bbbb2222", "cccc3333", "dddd4444", "eeee5555"}[i]
		topic := []string{"t0", "t1", "t2", "t3", "t4"}[i]
		seedFile(t, root, "mike", "inbox", "20260101T0000"+string(rune('0'+i))+"0Z-operator-to-mike-"+corrid+".md",
			frontmatterFile("operator", "mike", topic, "body", corrid, ts))
	}
	store := New(root)
	first, err := store.GetMessagesBetween("operator", "mike", 2, uuid.Nil)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first page len: want 2, got %d", len(first))
	}
	// Newest two: t4, t3.
	if first[0].Subject != "t4" || first[1].Subject != "t3" {
		t.Errorf("first page subjects: %v", subjectsOf(first))
	}
	cursor := first[1].ID

	second, err := store.GetMessagesBetween("operator", "mike", 2, cursor)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("second page len: want 2, got %d (subjects: %v)", len(second), subjectsOf(second))
	}
	if second[0].Subject != "t2" || second[1].Subject != "t1" {
		t.Errorf("second page subjects: %v", subjectsOf(second))
	}
	// Stable IDs across calls.
	again, err := store.GetMessagesBetween("operator", "mike", 2, uuid.Nil)
	if err != nil {
		t.Fatalf("again: %v", err)
	}
	if again[0].ID != first[0].ID || again[1].ID != first[1].ID {
		t.Errorf("IDs not stable across calls")
	}
}

func TestStore_GetMessagesBetween_DefaultLimit50(t *testing.T) {
	root := t.TempDir()
	base := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Hour)
	for i := 0; i < 60; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		// Ensure unique 8-hex corrids.
		corrid := uuid.NewSHA1(uuid.NameSpaceURL, []byte{byte(i)}).String()[:8]
		name := "20260101T" + padNum(i, 6) + "Z-operator-to-mike-" + corrid + ".md"
		seedFile(t, root, "mike", "inbox", name,
			frontmatterFile("operator", "mike", "t"+padNum(i, 2), "b", corrid, ts))
	}
	store := New(root)
	msgs, err := store.GetMessagesBetween("operator", "mike", 0, uuid.Nil)
	if err != nil {
		t.Fatalf("GetMessagesBetween: %v", err)
	}
	if len(msgs) != 50 {
		t.Fatalf("default limit: want 50, got %d", len(msgs))
	}
}

// padNum returns the decimal string of n, left-padded with '0' to width.
// Used for unique filenames in TestStore_GetMessagesBetween_DefaultLimit50.
func padNum(n, width int) string {
	s := ""
	if n == 0 {
		s = "0"
	}
	for n > 0 {
		s = string(rune('0'+(n%10))) + s
		n /= 10
	}
	for len(s) < width {
		s = "0" + s
	}
	return s
}

func TestStore_GetMessageCountByAgent(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	seedFile(t, root, "mike", "inbox", "20260101T000000Z-operator-to-mike-aaaaaaaa.md",
		frontmatterFile("operator", "mike", "t1", "b", "aaaaaaaa", now))
	seedFile(t, root, "mike", "inbox", "20260101T000100Z-alex-to-mike-bbbbbbbb.md",
		frontmatterFile("alex", "mike", "t2", "b", "bbbbbbbb", now))
	seedFile(t, root, "mike", "inbox/.archive", "20260101T000200Z-operator-to-mike-cccccccc.md",
		frontmatterFile("operator", "mike", "t3", "b", "cccccccc", now))
	// Operator inbox should not affect mike's count.
	seedFile(t, root, "operator", "inbox", "20260101T000300Z-mike-to-operator-dddddddd.md",
		frontmatterFile("mike", "operator", "t4", "b", "dddddddd", now))

	store := New(root)
	count, err := store.GetMessageCountByAgent("mike")
	if err != nil {
		t.Fatalf("GetMessageCountByAgent: %v", err)
	}
	if count != 3 {
		t.Errorf("mike count: want 3, got %d", count)
	}
	opCount, err := store.GetMessageCountByAgent("operator")
	if err != nil {
		t.Fatalf("operator count: %v", err)
	}
	if opCount != 1 {
		t.Errorf("operator count: want 1, got %d", opCount)
	}
}

func TestStore_StableIDAcrossArchiveMove(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	name := "20260101T000000Z-operator-to-mike-aaaaaaaa.md"
	livePath := seedFile(t, root, "mike", "inbox", name,
		frontmatterFile("operator", "mike", "t", "b", "aaaaaaaa", now))

	store := New(root)
	msgs, err := store.GetMessagesBetween("operator", "mike", 0, uuid.Nil)
	if err != nil {
		t.Fatalf("read live: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 msg, got %d", len(msgs))
	}
	idLive := msgs[0].ID

	// Move to .archive/.
	archiveDir := filepath.Join(root, "mike", "inbox", ".archive")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		t.Fatalf("mkdir archive: %v", err)
	}
	if err := os.Rename(livePath, filepath.Join(archiveDir, name)); err != nil {
		t.Fatalf("rename: %v", err)
	}

	msgs2, err := store.GetMessagesBetween("operator", "mike", 0, uuid.Nil)
	if err != nil {
		t.Fatalf("read archived: %v", err)
	}
	if len(msgs2) != 1 {
		t.Fatalf("want 1 msg post-archive, got %d", len(msgs2))
	}
	if msgs2[0].ID != idLive {
		t.Errorf("ID not stable: live=%s archived=%s", idLive, msgs2[0].ID)
	}
}

func TestStore_FilenameMatchesPersonaPollerConvention(t *testing.T) {
	// internal/csuite/persona/poller.go:546 — filenameLooksLikePersonaToRecipient
	// substring-matches "-<persona>-to-" against the filename. Verify the
	// writer's output satisfies that for both directions.
	root := t.TempDir()
	store := New(root)

	// operator -> mike: filename must contain "-operator-to-".
	op := &csuite.CsuiteInboxMessage{FromAgent: "operator", ToAgent: "mike", Subject: "hi", Body: "x"}
	if err := store.CreateMessage(op); err != nil {
		t.Fatalf("CreateMessage operator->mike: %v", err)
	}
	mikeInbox := filepath.Join(root, "mike", "inbox")
	mikeFiles, err := os.ReadDir(mikeInbox)
	if err != nil {
		t.Fatalf("readdir mike: %v", err)
	}
	if len(mikeFiles) != 1 {
		t.Fatalf("want 1 mike file, got %d", len(mikeFiles))
	}
	if !strings.Contains(mikeFiles[0].Name(), "-operator-to-") {
		t.Errorf("mike file %q missing -operator-to-", mikeFiles[0].Name())
	}

	// mike -> operator: filename must contain "-mike-to-".
	rep := &csuite.CsuiteInboxMessage{FromAgent: "mike", ToAgent: "operator", Subject: "rp", Body: "y"}
	if err := store.CreateMessage(rep); err != nil {
		t.Fatalf("CreateMessage mike->operator: %v", err)
	}
	opInbox := filepath.Join(root, "operator", "inbox")
	opFiles, err := os.ReadDir(opInbox)
	if err != nil {
		t.Fatalf("readdir operator: %v", err)
	}
	if len(opFiles) != 1 {
		t.Fatalf("want 1 operator file, got %d", len(opFiles))
	}
	if !strings.Contains(opFiles[0].Name(), "-mike-to-") {
		t.Errorf("operator file %q missing -mike-to-", opFiles[0].Name())
	}
}
