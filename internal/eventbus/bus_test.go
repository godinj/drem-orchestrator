package eventbus_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/godinj/drem-orchestrator/internal/eventbus"
)

// newTempDB returns a path for a temp SQLite file that does not yet exist.
// The directory is cleaned up automatically at the end of t.
func newTempDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "test.db")
}

// openRaw opens the SQLite file at path directly via database/sql so tests
// can run PRAGMA queries without going through the Bus API.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("openRaw %q: %v", path, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// columnNames queries PRAGMA table_info and returns the set of column names.
func columnNames(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()

	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info row: %v", err)
		}
		cols[name] = true
	}
	return cols
}

// ---------------------------------------------------------------------------
// New: database creation
// ---------------------------------------------------------------------------

// TestNew_CreatesFileAtPath verifies that New creates a SQLite file at the
// exact path provided.
func TestNew_CreatesFileAtPath(t *testing.T) {
	dbPath := newTempDB(t)

	_, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Errorf("New did not create a file at %q", dbPath)
	}
}

// TestNew_InvalidPath verifies that New returns a non-nil error when the
// parent directory does not exist.
func TestNew_InvalidPath(t *testing.T) {
	dbPath := "/nonexistent/directory/that/will/never/exist/test.db"

	_, err := eventbus.New(dbPath)
	if err == nil {
		t.Error("expected error for invalid path, got nil")
	}
}

// ---------------------------------------------------------------------------
// New: WAL mode
// ---------------------------------------------------------------------------

// TestNew_WALModeEnabled verifies that New enables WAL journal mode on the
// database. WAL mode is required for concurrent reads alongside writes.
// A temp file (not in-memory) is used because WAL mode has no effect on
// in-memory databases.
func TestNew_WALModeEnabled(t *testing.T) {
	dbPath := newTempDB(t)

	_, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	db := openRaw(t, dbPath)
	row := db.QueryRow("PRAGMA journal_mode")
	var mode string
	if err := row.Scan(&mode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want %q", mode, "wal")
	}
}

// ---------------------------------------------------------------------------
// New: events table
// ---------------------------------------------------------------------------

// TestNew_EventsTableExists verifies that New creates an `events` table.
func TestNew_EventsTableExists(t *testing.T) {
	dbPath := newTempDB(t)

	_, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	db := openRaw(t, dbPath)
	cols := columnNames(t, db, "events")
	if len(cols) == 0 {
		t.Error("events table does not exist or has no columns")
	}
}

// TestNew_EventsTableColumns verifies that the `events` table has all
// required columns with the correct names.
func TestNew_EventsTableColumns(t *testing.T) {
	dbPath := newTempDB(t)

	_, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	db := openRaw(t, dbPath)
	cols := columnNames(t, db, "events")

	required := []string{
		"id",
		"event_type",
		"source",
		"task_id",
		"from_status",
		"to_status",
		"details",
		"created_at",
	}
	for _, col := range required {
		if !cols[col] {
			t.Errorf("events table missing column %q", col)
		}
	}
}

// ---------------------------------------------------------------------------
// New: event_deliveries table
// ---------------------------------------------------------------------------

// TestNew_EventDeliveriesTableExists verifies that New creates an
// `event_deliveries` table.
func TestNew_EventDeliveriesTableExists(t *testing.T) {
	dbPath := newTempDB(t)

	_, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	db := openRaw(t, dbPath)
	cols := columnNames(t, db, "event_deliveries")
	if len(cols) == 0 {
		t.Error("event_deliveries table does not exist or has no columns")
	}
}

// TestNew_EventDeliveriesTableColumns verifies that the `event_deliveries`
// table has all required columns with the correct names.
func TestNew_EventDeliveriesTableColumns(t *testing.T) {
	dbPath := newTempDB(t)

	_, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	db := openRaw(t, dbPath)
	cols := columnNames(t, db, "event_deliveries")

	required := []string{
		"event_id",
		"agent",
		"delivered_at",
		"acked_at",
	}
	for _, col := range required {
		if !cols[col] {
			t.Errorf("event_deliveries table missing column %q", col)
		}
	}
}

// TestNew_EventDeliveriesCompositeKey verifies that the `event_deliveries`
// table uses a composite primary key on (event_id, agent).
func TestNew_EventDeliveriesCompositeKey(t *testing.T) {
	dbPath := newTempDB(t)

	_, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	db := openRaw(t, dbPath)
	rows, err := db.Query("PRAGMA table_info(event_deliveries)")
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()

	// pk > 0 means the column is part of the primary key; the value is its
	// position within the composite key (1-based).
	pkCols := make(map[string]int)
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		if pk > 0 {
			pkCols[name] = pk
		}
	}

	if _, ok := pkCols["event_id"]; !ok {
		t.Error("event_id is not part of the primary key in event_deliveries")
	}
	if _, ok := pkCols["agent"]; !ok {
		t.Error("agent is not part of the primary key in event_deliveries")
	}
	if len(pkCols) != 2 {
		t.Errorf("expected 2 primary key columns, got %d: %v", len(pkCols), pkCols)
	}
}

// ---------------------------------------------------------------------------
// Publish: UUID auto-generation
// ---------------------------------------------------------------------------

// TestPublish_UUIDAutoGenerated verifies that Publish sets a non-zero UUID on
// an Event whose ID field was not set before the call. This mirrors the
// reflection-based callback pattern used by internal/db/db.go.
func TestPublish_UUIDAutoGenerated(t *testing.T) {
	dbPath := newTempDB(t)

	bus, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e := &eventbus.Event{
		Type:    "task.status_changed",
		Source:  "orchestrator",
		Details: `{"note":"test"}`,
	}
	// ID is empty before Publish.
	if e.ID != "" {
		t.Fatal("precondition: event ID should be empty before Publish")
	}

	if err := bus.Publish(e); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if e.ID == "" {
		t.Error("Publish did not generate a UUID for the event")
	}
}

// TestPublish_PresetUUIDPreserved verifies that Publish does not overwrite a
// UUID that was explicitly set by the caller.
func TestPublish_PresetUUIDPreserved(t *testing.T) {
	dbPath := newTempDB(t)

	bus, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	preset := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	e := &eventbus.Event{
		ID:      preset,
		Type:    "task.created",
		Source:  "orchestrator",
		Details: `{}`,
	}

	if err := bus.Publish(e); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if e.ID != preset {
		t.Errorf("Publish changed a preset UUID: got %v, want %v", e.ID, preset)
	}
}

// ---------------------------------------------------------------------------
// Poll / Deliver / Ack integration
// ---------------------------------------------------------------------------

// TestPoll_ReturnsUndeliveredEvents verifies that Poll returns events that
// have been published but not yet delivered to the requesting agent.
func TestPoll_ReturnsUndeliveredEvents(t *testing.T) {
	dbPath := newTempDB(t)

	bus, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e := &eventbus.Event{Type: "task.status_changed", Source: "orchestrator", Details: "{}"}
	if err := bus.Publish(e); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	events, err := bus.Poll("watcher-agent")
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(events) == 0 {
		t.Error("Poll returned no events; expected at least one undelivered event")
	}
}

// TestDeliver_MarksEventDelivered verifies that after Deliver, the event no
// longer appears in subsequent Poll calls for the same agent.
func TestDeliver_MarksEventDelivered(t *testing.T) {
	dbPath := newTempDB(t)

	bus, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e := &eventbus.Event{Type: "task.status_changed", Source: "orchestrator", Details: "{}"}
	if err := bus.Publish(e); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if err := bus.Deliver(e.ID, "watcher-agent"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	events, err := bus.Poll("watcher-agent")
	if err != nil {
		t.Fatalf("Poll after Deliver: %v", err)
	}
	for _, ev := range events {
		if ev.ID == e.ID {
			t.Errorf("delivered event %v still returned by Poll", e.ID)
		}
	}
}

// TestAck_SetsAckedAt verifies that Ack records an acked_at timestamp in
// event_deliveries for the given (event_id, agent) pair.
func TestAck_SetsAckedAt(t *testing.T) {
	dbPath := newTempDB(t)

	bus, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e := &eventbus.Event{Type: "task.created", Source: "orchestrator", Details: "{}"}
	if err := bus.Publish(e); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := bus.Deliver(e.ID, "watcher-agent"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if err := bus.Ack("watcher-agent", []string{e.ID}); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	// Verify acked_at is set via raw SQL.
	db := openRaw(t, dbPath)
	var ackedAt sql.NullString
	row := db.QueryRow(
		"SELECT acked_at FROM event_deliveries WHERE event_id = ? AND agent = ?",
		e.ID, "watcher-agent",
	)
	if err := row.Scan(&ackedAt); err != nil {
		t.Fatalf("query acked_at: %v", err)
	}
	if !ackedAt.Valid || ackedAt.String == "" {
		t.Error("acked_at is not set after Ack")
	}
}

// ---------------------------------------------------------------------------
// UnackedDeliveries
// ---------------------------------------------------------------------------

// TestUnackedDeliveries_ReturnsDeliveredButNotAcked verifies that
// UnackedDeliveries returns events that have been delivered to the agent
// but not yet acknowledged.
func TestUnackedDeliveries_ReturnsDeliveredButNotAcked(t *testing.T) {
	dbPath := newTempDB(t)

	bus, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e := &eventbus.Event{Type: "task.status_changed", Source: "orchestrator", Details: "{}"}
	if err := bus.Publish(e); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := bus.Deliver(e.ID, "watcher-agent"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	events, err := bus.UnackedDeliveries("watcher-agent")
	if err != nil {
		t.Fatalf("UnackedDeliveries: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].ID != e.ID {
		t.Errorf("event ID = %q, want %q", events[0].ID, e.ID)
	}
}

// TestUnackedDeliveries_ExcludesAckedEvents verifies that acked events
// no longer appear in UnackedDeliveries results.
func TestUnackedDeliveries_ExcludesAckedEvents(t *testing.T) {
	dbPath := newTempDB(t)

	bus, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Publish and deliver 3 events.
	ids := make([]string, 3)
	for i := 0; i < 3; i++ {
		e := &eventbus.Event{Type: "task.status_changed", Source: "orchestrator", Details: "{}"}
		if err := bus.Publish(e); err != nil {
			t.Fatalf("Publish[%d]: %v", i, err)
		}
		if err := bus.Deliver(e.ID, "watcher-agent"); err != nil {
			t.Fatalf("Deliver[%d]: %v", i, err)
		}
		ids[i] = e.ID
	}

	// Ack the first two.
	if err := bus.Ack("watcher-agent", ids[:2]); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	remaining, err := bus.UnackedDeliveries("watcher-agent")
	if err != nil {
		t.Fatalf("UnackedDeliveries after ack: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("got %d unacked events, want 1", len(remaining))
	}
	if remaining[0].ID != ids[2] {
		t.Errorf("remaining event ID = %q, want %q", remaining[0].ID, ids[2])
	}
}

// TestUnackedDeliveries_EmptyWhenNoneDelivered verifies that
// UnackedDeliveries returns an empty slice (not nil/error) when no
// deliveries exist for the agent.
func TestUnackedDeliveries_EmptyWhenNoneDelivered(t *testing.T) {
	dbPath := newTempDB(t)

	bus, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	events, err := bus.UnackedDeliveries("watcher-agent")
	if err != nil {
		t.Fatalf("UnackedDeliveries on empty bus: %v", err)
	}
	if events == nil {
		t.Error("UnackedDeliveries returned nil slice, want empty slice")
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0", len(events))
	}
}

// TestUnackedDeliveries_EmptyAfterAllAcked verifies that after acking all
// deliveries, UnackedDeliveries returns an empty slice.
func TestUnackedDeliveries_EmptyAfterAllAcked(t *testing.T) {
	dbPath := newTempDB(t)

	bus, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e := &eventbus.Event{Type: "task.created", Source: "orchestrator", Details: "{}"}
	if err := bus.Publish(e); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := bus.Deliver(e.ID, "watcher-agent"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if err := bus.Ack("watcher-agent", []string{e.ID}); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	remaining, err := bus.UnackedDeliveries("watcher-agent")
	if err != nil {
		t.Fatalf("UnackedDeliveries after full ack: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("got %d events after acking all, want 0", len(remaining))
	}
}

// TestUnackedDeliveries_AgentIsolation verifies that UnackedDeliveries
// for one agent does not return another agent's deliveries.
func TestUnackedDeliveries_AgentIsolation(t *testing.T) {
	dbPath := newTempDB(t)

	bus, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e := &eventbus.Event{Type: "task.status_changed", Source: "orchestrator", Details: "{}"}
	if err := bus.Publish(e); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// Deliver only to watcher-agent.
	if err := bus.Deliver(e.ID, "watcher-agent"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	// other-agent has no deliveries.
	events, err := bus.UnackedDeliveries("other-agent")
	if err != nil {
		t.Fatalf("UnackedDeliveries for other-agent: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("other-agent got %d events, want 0", len(events))
	}
}

// TestUnackedDeliveries_OrderedByCreatedAt verifies that events are
// returned in ascending created_at order.
func TestUnackedDeliveries_OrderedByCreatedAt(t *testing.T) {
	dbPath := newTempDB(t)

	bus, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for i := 0; i < 3; i++ {
		e := &eventbus.Event{Type: "task.status_changed", Source: "orchestrator", Details: "{}"}
		if err := bus.Publish(e); err != nil {
			t.Fatalf("Publish[%d]: %v", i, err)
		}
		if err := bus.Deliver(e.ID, "watcher-agent"); err != nil {
			t.Fatalf("Deliver[%d]: %v", i, err)
		}
	}

	events, err := bus.UnackedDeliveries("watcher-agent")
	if err != nil {
		t.Fatalf("UnackedDeliveries: %v", err)
	}
	for i := 1; i < len(events); i++ {
		if events[i].CreatedAt.Before(events[i-1].CreatedAt) {
			t.Errorf("events[%d].CreatedAt (%v) before events[%d].CreatedAt (%v)",
				i, events[i].CreatedAt, i-1, events[i-1].CreatedAt)
		}
	}
}

// ---------------------------------------------------------------------------
// Ack: batch and idempotent behavior
// ---------------------------------------------------------------------------

// TestAck_Idempotent verifies that double-acking the same event does not
// return an error.
func TestAck_Idempotent(t *testing.T) {
	dbPath := newTempDB(t)

	bus, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e := &eventbus.Event{Type: "task.created", Source: "orchestrator", Details: "{}"}
	if err := bus.Publish(e); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := bus.Deliver(e.ID, "watcher-agent"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	if err := bus.Ack("watcher-agent", []string{e.ID}); err != nil {
		t.Fatalf("first Ack: %v", err)
	}
	if err := bus.Ack("watcher-agent", []string{e.ID}); err != nil {
		t.Errorf("second Ack (idempotent) returned error: %v", err)
	}
}

// TestAck_EmptySliceIsNoop verifies that calling Ack with an empty eventIDs
// slice is a no-op and does not return an error.
func TestAck_EmptySliceIsNoop(t *testing.T) {
	dbPath := newTempDB(t)

	bus, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := bus.Ack("watcher-agent", []string{}); err != nil {
		t.Errorf("Ack with empty slice returned error: %v", err)
	}
}

// TestAck_BatchMultipleEvents verifies that Ack can acknowledge multiple
// events in a single call.
func TestAck_BatchMultipleEvents(t *testing.T) {
	dbPath := newTempDB(t)

	bus, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ids := make([]string, 3)
	for i := 0; i < 3; i++ {
		e := &eventbus.Event{Type: "task.status_changed", Source: "orchestrator", Details: "{}"}
		if err := bus.Publish(e); err != nil {
			t.Fatalf("Publish[%d]: %v", i, err)
		}
		if err := bus.Deliver(e.ID, "watcher-agent"); err != nil {
			t.Fatalf("Deliver[%d]: %v", i, err)
		}
		ids[i] = e.ID
	}

	// Ack all three at once.
	if err := bus.Ack("watcher-agent", ids); err != nil {
		t.Fatalf("Ack batch: %v", err)
	}

	remaining, err := bus.UnackedDeliveries("watcher-agent")
	if err != nil {
		t.Fatalf("UnackedDeliveries: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("got %d unacked events after batch ack, want 0", len(remaining))
	}
}
