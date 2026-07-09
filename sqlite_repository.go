package main

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// SQLiteRepository owns process-local database configuration and lifecycle.
// JournalService owns domain queries and migrations; keeping connection policy
// here prevents future commands from silently choosing different SQLite rules.
type SQLiteRepository struct {
	db *sql.DB
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
	return &SQLiteRepository{db: db}, nil
}

func (r *SQLiteRepository) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}
