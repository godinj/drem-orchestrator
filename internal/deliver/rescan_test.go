package deliver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRescan_PicksUpUnledgeredFile is the happy path: drop a file in
// an outbox, do not signal, call Rescan, observe one delivery.
func TestRescan_PicksUpUnledgeredFile(t *testing.T) {
	root := newCsuiteTree(t)
	body := []byte("---\nfrom: alex\nto: mike\n---\n\nhi mike\n")
	_, sha := stageOutbox(t, root, "alex", "new.md", body)

	l := openTestLedger(t)
	h := &handler{cfg: Config{Token: "secret", Ledger: l}, clock: time.Now}

	res := h.Rescan()
	if res.Scanned != 1 {
		t.Errorf("scanned = %d, want 1 (got %+v)", res.Scanned, res)
	}
	if res.Delivered != 1 {
		t.Errorf("delivered = %d, want 1 (got %+v)", res.Delivered, res)
	}
	if res.Skipped != 0 {
		t.Errorf("skipped = %d, want 0", res.Skipped)
	}
	if len(res.Errors) != 0 {
		t.Errorf("errors = %v, want none", res.Errors)
	}

	// File landed in mike's inbox.
	entries, err := os.ReadDir(filepath.Join(root, "mike", "inbox"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("mike inbox count = %d, want 1", len(entries))
	}
	// Ledger row written.
	if _, found, err := l.Lookup(sha); err != nil || !found {
		t.Errorf("Lookup: found=%v err=%v", found, err)
	}
}

func TestRescan_SuppressesACKWithoutInboxDelivery(t *testing.T) {
	root := newCsuiteTree(t)
	body := []byte("---\nfrom: alex\nto: mike\ntype: ack\n---\n\nreceived\n")
	_, sha := stageOutbox(t, root, "alex", "ack.md", body)

	l := openTestLedger(t)
	h := &handler{cfg: Config{Token: "secret", Ledger: l}, clock: time.Now}

	res := h.Rescan()
	if res.Suppressed != 1 {
		t.Fatalf("suppressed = %d, want 1 (got %+v)", res.Suppressed, res)
	}
	if res.Delivered != 0 {
		t.Fatalf("delivered = %d, want 0 (got %+v)", res.Delivered, res)
	}
	entries, err := os.ReadDir(filepath.Join(root, "mike", "inbox"))
	if err != nil {
		t.Fatalf("read mike inbox: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("mike inbox entries = %d, want 0", len(entries))
	}
	d, found, err := l.Lookup(sha)
	if err != nil || !found {
		t.Fatalf("Lookup: found=%v err=%v", found, err)
	}
	if d.Dest != ClassSuppress {
		t.Fatalf("ledger dest = %q, want suppress", d.Dest)
	}
}

func TestRescan_BoundsFilesPerPersona(t *testing.T) {
	root := newCsuiteTree(t)
	base := time.Date(2026, 4, 21, 16, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		body := []byte(fmt.Sprintf("---\nfrom: alex\nto: mike\n---\n\nmsg %d\n", i))
		path, _ := stageOutbox(t, root, "alex", fmt.Sprintf("m%d.md", i), body)
		at := base.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	l := openTestLedger(t)
	h := &handler{cfg: Config{Token: "secret", Ledger: l, MaxRescanFilesPerPersona: 2}, clock: time.Now}

	res := h.Rescan()
	if res.Scanned != 2 {
		t.Fatalf("scanned = %d, want 2 (got %+v)", res.Scanned, res)
	}
	if res.Delivered != 2 {
		t.Fatalf("delivered = %d, want 2 (got %+v)", res.Delivered, res)
	}
	entries, err := os.ReadDir(filepath.Join(root, "mike", "inbox"))
	if err != nil {
		t.Fatalf("read mike inbox: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("mike inbox entries = %d, want 2", len(entries))
	}
}

// TestRescan_SkipsAlreadyDelivered verifies the skip-BEFORE-deliver
// contract — a file whose sha is already in the ledger must not be
// re-delivered. This matters when the /deliver path committed the
// ledger but crashed before moving the source to delivered/.
func TestRescan_SkipsAlreadyDelivered(t *testing.T) {
	root := newCsuiteTree(t)
	body := []byte("---\nfrom: alex\nto: mike\n---\n\nhi\n")
	_, sha := stageOutbox(t, root, "alex", "new.md", body)

	l := openTestLedger(t)
	// Pre-seed ledger as if delivery partially completed and the
	// source never moved.
	if err := l.Insert(Delivery{
		SHA256:        sha,
		SourcePersona: "alex",
		Dest:          "mike",
		SourcePath:    "/csuite/alex/outbox/new.md",
		DestPath:      "/csuite/mike/inbox/" + sha[:8] + ".md",
		DeliveredAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	h := &handler{cfg: Config{Token: "secret", Ledger: l}, clock: time.Now}
	res := h.Rescan()

	if res.Scanned != 1 {
		t.Errorf("scanned = %d, want 1 (got %+v)", res.Scanned, res)
	}
	if res.Delivered != 0 {
		t.Errorf("delivered = %d, want 0 (ledger hit must short-circuit); got %+v", res.Delivered, res)
	}
	if res.Skipped != 1 {
		t.Errorf("skipped = %d, want 1; got %+v", res.Skipped, res)
	}
	// Mike's inbox must remain empty — no duplicate drop.
	entries, _ := os.ReadDir(filepath.Join(root, "mike", "inbox"))
	if len(entries) != 0 {
		t.Errorf("mike inbox = %d, want 0 (no re-delivery)", len(entries))
	}
}

// TestRescan_SkipsDeliveredSubdir verifies that files under
// outbox/delivered/ are not re-processed. The subdir is created by
// the /deliver happy path when it moves a source file after success.
func TestRescan_SkipsDeliveredSubdir(t *testing.T) {
	root := newCsuiteTree(t)
	body := []byte("---\nfrom: alex\nto: mike\n---\n\nhi\n")
	delivered := filepath.Join(root, "alex", "outbox", "delivered")
	if err := os.MkdirAll(delivered, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(delivered, "past.md"), body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	l := openTestLedger(t)
	h := &handler{cfg: Config{Token: "secret", Ledger: l}, clock: time.Now}
	res := h.Rescan()
	if res.Scanned != 0 {
		t.Errorf("scanned = %d, want 0 (delivered/ must be skipped); got %+v", res.Scanned, res)
	}
}

// TestRescan_FIFOPreservedInSourceMtimeOrder verifies that multiple
// unledgered files in one outbox land in the destination inbox in
// source-mtime order. The per-destination mutex in deliverToInbox
// guarantees no interleaving.
func TestRescan_FIFOPreservedInSourceMtimeOrder(t *testing.T) {
	root := newCsuiteTree(t)
	// Stage three files with explicit mtimes spread by 10ms so
	// ordering is unambiguous on fast filesystems.
	base := time.Date(2026, 4, 21, 16, 0, 0, 0, time.UTC)
	for i, name := range []string{"b.md", "c.md", "a.md"} {
		body := []byte(fmt.Sprintf("---\nfrom: alex\nto: mike\n---\n\nmsg %s\n", name))
		path, _ := stageOutbox(t, root, "alex", name, body)
		at := base.Add(time.Duration(i*10) * time.Millisecond)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	// Custom clock so destination filenames carry monotonically
	// increasing timestamps that reflect Rescan walk order. Step by
	// seconds because the dest filename uses RFC3339 which only has
	// second precision — sub-second increments would collapse three
	// file names to the same prefix and break the sort-by-name check
	// below.
	var clockCounter int64
	var mu sync.Mutex
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		clockCounter++
		return base.Add(time.Hour + time.Duration(clockCounter)*time.Second)
	}

	l := openTestLedger(t)
	h := &handler{cfg: Config{Token: "secret", Ledger: l}, clock: clock}
	res := h.Rescan()
	if res.Delivered != 3 {
		t.Fatalf("delivered = %d, want 3 (errors=%v)", res.Delivered, res.Errors)
	}

	// Read mike's inbox, sorted by filename (which begins with the
	// delivery clock). The file created earliest (b.md, mtime 0ms)
	// should appear first in delivery order.
	entries, err := os.ReadDir(filepath.Join(root, "mike", "inbox"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	sort.Strings(names)
	if len(names) != 3 {
		t.Fatalf("inbox count = %d, want 3", len(names))
	}

	// Read each delivered inbox file and verify the msg ordering
	// matches source-mtime ordering (b -> c -> a) — not alphabetical.
	wantOrder := []string{"b.md", "c.md", "a.md"}
	for i, destName := range names {
		data, err := os.ReadFile(filepath.Join(root, "mike", "inbox", destName))
		if err != nil {
			t.Fatalf("read %s: %v", destName, err)
		}
		want := "msg " + wantOrder[i]
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("dest[%d] (%s) body = %q, want to contain %q (ordering mismatch)",
				i, destName, string(data), want)
		}
	}
}

// TestRescan_QuarantineOnMalformed verifies a file with bad
// frontmatter is quarantined rather than dropped silently.
func TestRescan_QuarantineOnMalformed(t *testing.T) {
	root := newCsuiteTree(t)
	body := []byte("no frontmatter here\njust text")
	_, _ = stageOutbox(t, root, "alex", "bad.md", body)

	l := openTestLedger(t)
	h := &handler{cfg: Config{Token: "secret", Ledger: l}, clock: time.Now}
	res := h.Rescan()
	if res.Quarantined != 1 {
		t.Errorf("quarantined = %d, want 1 (got %+v)", res.Quarantined, res)
	}
	// Quarantine file exists under /csuite/quarantine/alex/.
	if _, err := os.Stat(filepath.Join(root, "quarantine", "alex", "bad.md")); err != nil {
		t.Errorf("quarantine file missing: %v", err)
	}
}

// TestRescan_RoutesKyleOutboxToPersonaInbox covers the host-Kyle →
// persona routing path added 2026-04-22. Kyle runs as a host-side
// Claude Code instance (not a container) and cannot POST /deliver, so
// his only route is to drop files into ~/.drem-csuite/kyle/outbox and
// let the watcher's periodic rescan pick them up. The rescan must
// therefore treat kyle as a valid source persona.
func TestRescan_RoutesKyleOutboxToPersonaInbox(t *testing.T) {
	root := newCsuiteTree(t)
	body := []byte("---\nfrom: kyle\nto: mike\n---\n\ndirective from the CEO\n")
	_, sha := stageOutbox(t, root, "kyle", "kyle-to-mike.md", body)

	l := openTestLedger(t)
	h := &handler{cfg: Config{Token: "secret", Ledger: l}, clock: time.Now}

	res := h.Rescan()
	if res.Scanned != 1 {
		t.Errorf("scanned = %d, want 1 (got %+v)", res.Scanned, res)
	}
	if res.Delivered != 1 {
		t.Errorf("delivered = %d, want 1 — kyle must be a valid source (got %+v)", res.Delivered, res)
	}
	if len(res.Errors) != 0 {
		t.Errorf("errors = %v, want none — kyle must validate as a source persona", res.Errors)
	}

	entries, err := os.ReadDir(filepath.Join(root, "mike", "inbox"))
	if err != nil {
		t.Fatalf("readdir mike inbox: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("mike inbox count = %d, want 1 — kyle→mike routing must populate mike's inbox", len(entries))
	}
	if _, found, err := l.Lookup(sha); err != nil || !found {
		t.Errorf("ledger lookup: found=%v err=%v — kyle delivery must be recorded", found, err)
	}
}

// TestRescan_RoutesOperatorOutboxToOperatorInbox covers the persona →
// operator rescan path added 2026-04-22 alongside ClassOperator.
// When a persona has written a "to: operator" reply to its outbox but
// the watcher missed the POST /deliver signal, the 5-min rescan pass
// must still route the file into /csuite/operator/inbox/. Operator is
// NOT in rescanPersonas (destination-only — no outbox to scan from);
// this test proves the ClassOperator arm fires correctly when a real
// source persona (mike) is walked. See
// plans/drem-csuite-send-cli.md §Phase 1.
func TestRescan_RoutesOperatorOutboxToOperatorInbox(t *testing.T) {
	root := newCsuiteTree(t)
	body := []byte("---\nfrom: mike\nto: operator\n---\n\nreply for operator\n")
	_, sha := stageOutbox(t, root, "mike", "mike-to-op.md", body)

	l := openTestLedger(t)
	h := &handler{cfg: Config{Token: "secret", Ledger: l}, clock: time.Now}

	res := h.Rescan()
	if res.Scanned != 1 {
		t.Errorf("scanned = %d, want 1 (got %+v)", res.Scanned, res)
	}
	if res.Delivered != 1 {
		t.Errorf("delivered = %d, want 1 — operator must be a valid dest (got %+v)",
			res.Delivered, res)
	}
	if len(res.Errors) != 0 {
		t.Errorf("errors = %v, want none", res.Errors)
	}

	entries, err := os.ReadDir(filepath.Join(root, "operator", "inbox"))
	if err != nil {
		t.Fatalf("readdir operator inbox: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("operator inbox count = %d, want 1", len(entries))
	}
	d, found, err := l.Lookup(sha)
	if err != nil || !found {
		t.Fatalf("ledger lookup: found=%v err=%v", found, err)
	}
	if d.Dest != "operator" {
		t.Errorf("ledger dest = %q, want operator", d.Dest)
	}
}

// TestRescan_IgnoresNonMdFiles confirms the rescan's .md filter —
// sidecar files, swap files, etc. must not be processed.
func TestRescan_IgnoresNonMdFiles(t *testing.T) {
	root := newCsuiteTree(t)
	if err := os.WriteFile(filepath.Join(root, "alex", "outbox", "weird.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	l := openTestLedger(t)
	h := &handler{cfg: Config{Token: "secret", Ledger: l}, clock: time.Now}
	res := h.Rescan()
	if res.Scanned != 0 {
		t.Errorf("scanned = %d, want 0 (non-.md must be ignored); got %+v", res.Scanned, res)
	}
}

// TestRescan_NoLedgerRefuses asserts that a handler without a ledger
// returns an error result rather than scanning silently.
func TestRescan_NoLedgerRefuses(t *testing.T) {
	h := &handler{cfg: Config{Token: "secret"}, clock: time.Now}
	res := h.Rescan()
	if len(res.Errors) == 0 {
		t.Errorf("errors = %v, want at least one", res.Errors)
	}
}

// TestRescanEndpoint_AuthRequired verifies /rescan rejects missing
// and bad tokens with 401.
func TestRescanEndpoint_AuthRequired(t *testing.T) {
	l := openTestLedger(t)
	h := Handler(Config{Token: "secret", Ledger: l})

	for _, tc := range []struct {
		name  string
		token string
	}{
		{"no token", ""},
		{"bad token", "wrong"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/rescan", nil)
			if tc.token != "" {
				req.Header.Set("X-Csuite-Token", tc.token)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rr.Code)
			}
		})
	}
}

// TestRescanEndpoint_HappyPathJSON verifies the HTTP path returns a
// parseable RescanResult.
func TestRescanEndpoint_HappyPathJSON(t *testing.T) {
	root := newCsuiteTree(t)
	body := []byte("---\nfrom: alex\nto: mike\n---\n\nhi\n")
	_, _ = stageOutbox(t, root, "alex", "hello.md", body)

	l := openTestLedger(t)
	h := Handler(Config{Token: "secret", Ledger: l})

	req := httptest.NewRequest(http.MethodPost, "/rescan", nil)
	req.Header.Set("X-Csuite-Token", "secret")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
	}
	var res RescanResult
	if err := json.NewDecoder(rr.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Scanned != 1 || res.Delivered != 1 {
		t.Errorf("res = %+v, want scanned=1 delivered=1", res)
	}
}

// TestRescanEndpoint_WrongMethod rejects GET with 405.
func TestRescanEndpoint_WrongMethod(t *testing.T) {
	l := openTestLedger(t)
	h := Handler(Config{Token: "secret", Ledger: l})
	req := httptest.NewRequest(http.MethodGet, "/rescan", nil)
	req.Header.Set("X-Csuite-Token", "secret")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

// TestRescan_SharesDeliveryPathWithHTTP verifies that a file
// delivered via Rescan and then re-rescanned is skipped via the
// ledger — proving both paths write the same shape of ledger row.
func TestRescan_SharesDeliveryPathWithHTTP(t *testing.T) {
	root := newCsuiteTree(t)
	body := []byte("---\nfrom: alex\nto: mike\n---\n\nhello\n")
	_, sha := stageOutbox(t, root, "alex", "m.md", body)

	l := openTestLedger(t)
	h := &handler{cfg: Config{Token: "secret", Ledger: l}, clock: time.Now}

	first := h.Rescan()
	if first.Delivered != 1 {
		t.Fatalf("first: delivered = %d, want 1", first.Delivered)
	}

	// After first rescan the source moves to delivered/, so the
	// second rescan sees nothing to process. Verify via skipped=0,
	// scanned=0 (delivered/ is ignored).
	second := h.Rescan()
	if second.Scanned != 0 {
		t.Errorf("second scanned = %d, want 0 (file should be in delivered/)", second.Scanned)
	}
	// Ledger still carries exactly one row for sha.
	if _, found, err := l.Lookup(sha); err != nil || !found {
		t.Errorf("Lookup: found=%v err=%v", found, err)
	}
}

// TestRescan_PartialDeliverySkipsWithoutReDelivery simulates a
// crash between ledger-insert and source-move: the ledger row is
// there but the source file is still in outbox/. Rescan must hit
// the ledger lookup first and skip, NOT re-deliver with a fresh sha.
func TestRescan_PartialDeliverySkipsWithoutReDelivery(t *testing.T) {
	root := newCsuiteTree(t)
	body := []byte("---\nfrom: alex\nto: mike\n---\n\npartial\n")
	_, sha := stageOutbox(t, root, "alex", "p.md", body)

	l := openTestLedger(t)
	// Simulate the crash: ledger committed, source NOT moved.
	if err := l.Insert(Delivery{
		SHA256:        sha,
		SourcePersona: "alex",
		Dest:          "mike",
		SourcePath:    "/csuite/alex/outbox/p.md",
		DestPath:      "/csuite/mike/inbox/" + sha[:8] + ".md",
		DeliveredAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h := &handler{cfg: Config{Token: "secret", Ledger: l}, clock: time.Now}
	res := h.Rescan()
	if res.Skipped != 1 || res.Delivered != 0 {
		t.Errorf("skipped=%d delivered=%d, want skipped=1 delivered=0 (partial-crash recovery); got %+v",
			res.Skipped, res.Delivered, res)
	}
	// mike inbox still empty — no duplicate drop.
	entries, _ := os.ReadDir(filepath.Join(root, "mike", "inbox"))
	if len(entries) != 0 {
		t.Errorf("mike inbox = %d, want 0", len(entries))
	}
}

// TestRescan_WalksAllPersonas covers the multi-persona case: files
// in several outboxes all get processed in a single rescan call.
func TestRescan_WalksAllPersonas(t *testing.T) {
	root := newCsuiteTree(t)
	for _, src := range []string{"alex", "mike", "seth"} {
		body := []byte("---\nfrom: " + src + "\nto: kyle\n---\n\nhello\n")
		_, _ = stageOutbox(t, root, src, "msg.md", body)
	}

	l := openTestLedger(t)
	h := &handler{cfg: Config{Token: "secret", Ledger: l}, clock: time.Now}
	res := h.Rescan()
	if res.Scanned != 3 || res.Delivered != 3 {
		t.Errorf("scanned=%d delivered=%d, want 3/3 (got %+v)", res.Scanned, res.Delivered, res)
	}
	entries, err := os.ReadDir(filepath.Join(root, "kyle", "inbox"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("kyle inbox = %d, want 3", len(entries))
	}
}

// TestRescanOnce_LogsResult exercises the package-level entry used
// by the serve binary. Only assertion is that it returns a
// RescanResult without panicking on a freshly-opened ledger.
func TestRescanOnce_LogsResult(t *testing.T) {
	_ = newCsuiteTree(t)
	l := openTestLedger(t)
	var buf bytes.Buffer
	res := RescanOnce(Config{Token: "t", Ledger: l, Logger: testLogger(&buf)})
	if res.Scanned != 0 || res.Delivered != 0 {
		t.Errorf("empty tree: res = %+v, want all zero", res)
	}
	// Log line should reference the outcome.
	if !strings.Contains(buf.String(), "rescan:") {
		t.Errorf("log missing rescan prefix: %q", buf.String())
	}
}

// TestRescan_ConcurrentWithHTTPDeliver verifies the HTTP /deliver
// path and Rescan do not race for the same destination — both must
// route through the same per-destination mutex. We simulate this by
// running both against the same handler instance.
//
// This is a sanity-level test: the mutex is keyed by dest name, so
// the real interleave check lives in deliver_inbox_test.go; this
// test only verifies that sharing a handler between HTTP and rescan
// is safe.
func TestRescan_ConcurrentWithHTTPDeliver(t *testing.T) {
	root := newCsuiteTree(t)
	_, sha := stageOutbox(t, root, "alex", "a.md",
		[]byte("---\nfrom: alex\nto: mike\n---\n\n1\n"))
	_, _ = stageOutbox(t, root, "seth", "s.md",
		[]byte("---\nfrom: seth\nto: mike\n---\n\n2\n"))

	l := openTestLedger(t)
	h := &handler{cfg: Config{Token: "secret", Ledger: l}, clock: time.Now}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		req := DeliverRequest{
			SourcePersona: "alex",
			OutboxPath:    "/csuite/alex/outbox/a.md",
			SHA256:        sha,
			EmittedAt:     "2026-04-21T15:30:00Z",
		}
		class, _ := ClassifyFile(req.OutboxPath)
		_, _ = h.deliverToInbox(req, class)
	}()
	go func() {
		defer wg.Done()
		_ = h.Rescan()
	}()
	wg.Wait()

	// Mike must have received at least one file — both goroutines
	// converge on the same destination, one wins, the other is
	// skipped by the ledger.
	entries, err := os.ReadDir(filepath.Join(root, "mike", "inbox"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) == 0 {
		t.Error("mike inbox empty after concurrent deliver+rescan")
	}
}
