package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// ErrDatabaseInUse means another application currently holds SQLite's lock.
// Callers must stop startup rather than opening a second CRDT coordinator.
var ErrDatabaseInUse = errors.New("Journal database is already in use")

// SQLiteRepository owns process-local database configuration and lifecycle.
// JournalService owns domain queries and migrations; keeping connection policy
// here prevents future commands from silently choosing different SQLite rules.
type SQLiteRepository struct {
	db   *sql.DB
	path string
}

func OpenSQLiteRepository(path string) (*SQLiteRepository, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// SQLite foreign-key settings are connection-local. A single connection
	// makes that invariant reliable and serializes the embedded-app write path.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 1000`); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Local CRDT sessions are intentionally single-process for 1.6. SQLite
	// serialization alone cannot notify another renderer about new Yjs updates
	// or keep their derived projections ordered. Hold an exclusive database
	// lock until Close instead of allowing a second Journal process to edit a
	// stale in-memory Y.Doc.
	if _, err := db.Exec(`PRAGMA locking_mode = EXCLUSIVE`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`BEGIN EXCLUSIVE`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: %v", ErrDatabaseInUse, err)
	}
	if _, err := db.Exec(`COMMIT`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SQLiteRepository{db: db, path: path}, nil
}

// BackupBeforeCRDTMigration makes a consistent, local rollback copy without
// introducing a network backup service. VACUUM INTO is SQLite's own snapshot
// operation, so the copy is not a byte-for-byte filesystem race with writes.
func (r *SQLiteRepository) BackupBeforeCRDTMigration() error {
	if r == nil || r.db == nil || r.path == "" {
		return fmt.Errorf("database repository is not ready for backup")
	}
	target := r.path + ".pre-1.6.0.db"
	if info, err := os.Stat(target); err == nil {
		if info.Size() > 0 {
			return nil
		}
		return fmt.Errorf("pre-upgrade backup exists but is empty: %s", target)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect pre-upgrade backup: %w", err)
	}
	if _, err := r.db.Exec(`VACUUM INTO ?`, target); err != nil {
		return fmt.Errorf("create pre-upgrade backup %s: %w", target, err)
	}
	return nil
}

func (r *SQLiteRepository) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}
