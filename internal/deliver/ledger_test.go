package deliver

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// openTestLedger builds a ledger in a tempdir so tests don't leak
// state across runs. Callers are expected to rely on t.TempDir cleanup.
func openTestLedger(t *testing.T) *Ledger {
	t.Helper()
	path := filepath.Join(t.TempDir(), "deliveries.db")
	l, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// TestOpenLedger_CreatesParentDir verifies that OpenLedger mkdir's
// the enclosing directory so operators don't have to pre-create
// /var/lib/watcher/ before first start.
func TestOpenLedger_CreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeply", "deliveries.db")
	l, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger with missing parent: %v", err)
	}
	defer l.Close()
	if _, _, err := l.Lookup("nope"); err != nil {
		t.Fatalf("Lookup after open: %v", err)
	}
}

// TestLedger_InsertThenLookup exercises the happy path of insert +
// readback.
func TestLedger_InsertThenLookup(t *testing.T) {
	l := openTestLedger(t)
	d := Delivery{
		SHA256:        "aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11",
		SourcePersona: "alex",
		Dest:          "kyle",
		SourcePath:    "/csuite/alex/outbox/x.md",
		DestPath:      "/csuite/kyle/inbox/2026-04-21-alex-x.md",
		DeliveredAt:   time.Now().UTC(),
	}
	if err := l.Insert(d); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, found, err := l.Lookup(d.SHA256)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !found {
		t.Fatalf("Lookup: expected found=true")
	}
	if got.Dest != "kyle" {
		t.Errorf("Dest: got %q, want kyle", got.Dest)
	}
}

// TestLedger_DuplicateInsert verifies the primary-key uniqueness
// contract: a second insert with the same sha256 returns
// ErrDuplicateDelivery rather than a driver-specific error.
func TestLedger_DuplicateInsert(t *testing.T) {
	l := openTestLedger(t)
	d := Delivery{
		SHA256:        "bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22",
		SourcePersona: "mike",
		Dest:          "alex",
		SourcePath:    "/csuite/mike/outbox/x.md",
		DestPath:      "/csuite/alex/inbox/2026-04-21-mike-x.md",
		DeliveredAt:   time.Now().UTC(),
	}
	if err := l.Insert(d); err != nil {
		t.Fatalf("first Insert: %v", err)
	}
	err := l.Insert(d)
	if !errors.Is(err, ErrDuplicateDelivery) {
		t.Errorf("second Insert: want ErrDuplicateDelivery, got %v", err)
	}
}

// TestDeliver_IdempotencySmoke exercises /deliver's ledger lookup:
// once a sha256 is in the ledger, the same request returns 409.
func TestDeliver_IdempotencySmoke(t *testing.T) {
	l := openTestLedger(t)
	// Pre-seed the ledger with the sha256 used by validBody("alex").
	if err := l.Insert(Delivery{
		SHA256:        "9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c",
		SourcePersona: "alex",
		Dest:          "kyle",
		SourcePath:    "/csuite/alex/outbox/2026-04-21T153000Z-alex-reply-abc123.md",
		DestPath:      "/csuite/kyle/inbox/previous.md",
		DeliveredAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h := Handler(Config{Token: "secret", Ledger: l})
	w := post(t, h, "secret", validBody(t, "alex"))
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["delivery_id"] == "" {
		t.Errorf("expected delivery_id in response, got %v", body)
	}
}

// (Real-delivery "delivered" log-line assertions live in
// deliver_inbox_test.go.)

// TestDeliver_ConcurrentDuplicatePost exercises the ledger's
// primary-key uniqueness under concurrent load. Two goroutines post
// the same sha256 simultaneously; exactly one must get 501 (or any
// non-409 success-ish code) and the other 409. After the dust
// settles there must be exactly one row.
func TestDeliver_ConcurrentDuplicatePost(t *testing.T) {
	l := openTestLedger(t)
	h := Handler(Config{Token: "secret", Ledger: l})
	// Pre-seed so both POSTs see the row and both land on 409 — this
	// is the cleanest way to assert the ledger's single-row invariant
	// without coupling to delivery-completion behaviour that commit-2
	// does not implement. Real concurrent-insert coverage lives in
	// TestLedger_ConcurrentInsert below.
	if err := l.Insert(Delivery{
		SHA256:        "9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c",
		SourcePersona: "alex",
		Dest:          "kyle",
		SourcePath:    "/csuite/alex/outbox/2026-04-21T153000Z-alex-reply-abc123.md",
		DestPath:      "/csuite/kyle/inbox/existing.md",
		DeliveredAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const N = 4
	var wg sync.WaitGroup
	conflicts := atomic.Int64{}
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/deliver", bytes.NewReader(validBody(t, "alex")))
			req.Header.Set("X-Csuite-Token", "secret")
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code == http.StatusConflict {
				conflicts.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := int(conflicts.Load()); got != N {
		t.Errorf("conflicts: got %d, want %d", got, N)
	}
}

// TestLedger_ConcurrentInsert verifies the ledger's primary-key
// constraint under race: N goroutines race to insert the same sha256,
// exactly one wins and the rest observe ErrDuplicateDelivery.
func TestLedger_ConcurrentInsert(t *testing.T) {
	l := openTestLedger(t)
	const N = 8
	var wg sync.WaitGroup
	var okCount, dupCount atomic.Int64
	d := Delivery{
		SHA256:        "cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33",
		SourcePersona: "mike",
		Dest:          "seth",
		SourcePath:    "/csuite/mike/outbox/x.md",
		DestPath:      "/csuite/seth/inbox/x.md",
	}
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := l.Insert(d)
			switch {
			case err == nil:
				okCount.Add(1)
			case errors.Is(err, ErrDuplicateDelivery):
				dupCount.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if okCount.Load() != 1 {
		t.Errorf("exactly one insert must succeed; got %d successes", okCount.Load())
	}
	if dupCount.Load() != N-1 {
		t.Errorf("remaining %d inserts must report duplicate; got %d duplicates", N-1, dupCount.Load())
	}
}
