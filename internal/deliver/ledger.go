package deliver

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// DefaultDBPath is the on-disk location of the watcher's delivery
// ledger inside the container. The path is overridable via
// CSUITE_WATCHER_DB_PATH for tests and for operators who pin the
// named volume elsewhere. Lives under /var/lib/watcher/ to keep the
// watcher's private state separate from the shared /csuite/ tree
// (see plan §4 "bind-mount topology change").
const DefaultDBPath = "/var/lib/watcher/deliveries.db"

// Delivery is a single row in the delivery ledger. The sha256 column
// is the primary key, which gives us idempotency for free: a second
// POST with the same body hash hits a uniqueness violation and the
// handler replies 409.
type Delivery struct {
	SHA256        string    `gorm:"column:sha256;primaryKey"`
	SourcePersona string    `gorm:"column:source_persona;not null"`
	Dest          string    `gorm:"column:dest;not null"`
	SourcePath    string    `gorm:"column:source_path;not null"`
	DestPath      string    `gorm:"column:dest_path;not null"`
	DeliveredAt   time.Time `gorm:"column:delivered_at;not null"`
}

// TableName pins the table name so the gorm pluralizer cannot drift.
func (Delivery) TableName() string { return "deliveries" }

// Ledger is the SQLite-backed delivery ledger. Zero value is unusable;
// construct with OpenLedger.
type Ledger struct {
	db *gorm.DB
}

// OpenLedger opens (or creates) the ledger at dbPath, auto-creating
// parent directories and applying WAL journal mode for safe
// concurrent access from multiple goroutines within the watcher
// process. The caller owns the *Ledger; no goroutines are started.
//
// WAL mode matters because the plan's per-destination mutex
// serialises write phases within a single destination but leaves
// cross-destination writes concurrent, and SQLite's default rollback
// journal cannot accept overlapping writers.
func OpenLedger(dbPath string) (*Ledger, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, fmt.Errorf("create ledger directory: %w", err)
	}
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=5000", dbPath)
	gl := gormlogger.New(log.New(os.Stderr, "", 0), gormlogger.Config{LogLevel: gormlogger.Silent})
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gl})
	if err != nil {
		return nil, fmt.Errorf("open ledger %q: %w", dbPath, err)
	}
	if err := db.AutoMigrate(&Delivery{}); err != nil {
		return nil, fmt.Errorf("migrate ledger: %w", err)
	}
	return &Ledger{db: db}, nil
}

// Close releases the underlying database handle.
func (l *Ledger) Close() error {
	if l == nil || l.db == nil {
		return nil
	}
	sqlDB, err := l.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Lookup returns the existing Delivery row keyed by sha256. The second
// return value is true if a row was found. Any other error (including
// driver failures) is returned as err.
func (l *Ledger) Lookup(sha256 string) (Delivery, bool, error) {
	var d Delivery
	err := l.db.First(&d, "sha256 = ?", sha256).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Delivery{}, false, nil
	}
	if err != nil {
		return Delivery{}, false, err
	}
	return d, true, nil
}

// Insert writes a new Delivery row. A duplicate sha256 violates the
// primary key and is surfaced as ErrDuplicateDelivery so callers can
// respond with 409 without string-matching the driver error.
func (l *Ledger) Insert(d Delivery) error {
	if d.DeliveredAt.IsZero() {
		d.DeliveredAt = time.Now().UTC()
	}
	err := l.db.Create(&d).Error
	if err == nil {
		return nil
	}
	// gorm surfaces SQLite UNIQUE constraint violations via the
	// underlying driver. Matching on error text keeps us free of
	// driver-specific types but is fragile; probe with a lookup to
	// confirm before declaring duplicate.
	var existing Delivery
	if look := l.db.First(&existing, "sha256 = ?", d.SHA256).Error; look == nil {
		return ErrDuplicateDelivery
	}
	return fmt.Errorf("insert delivery: %w", err)
}

// ErrDuplicateDelivery signals that an Insert hit an existing
// sha256. Callers translate this to HTTP 409.
var ErrDuplicateDelivery = errors.New("duplicate delivery")
