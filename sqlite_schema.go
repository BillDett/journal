package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// SQLite schema creation, upgrades, and application-setting primitives.

func (s *JournalService) migrateV1() error {
	if _, err := s.db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return err
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS items (
			id TEXT PRIMARY KEY,
			parent_id TEXT NULL REFERENCES items(id) ON DELETE CASCADE,
			kind TEXT NOT NULL CHECK (kind IN ('journal', 'folder', 'document')),
			title TEXT NOT NULL,
			sort_order INTEGER NOT NULL DEFAULT 0,
			system_key TEXT NULL UNIQUE,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			encryption_state TEXT NOT NULL DEFAULT 'plaintext',
			encryption_key_id TEXT NULL,
			title_ciphertext BLOB NULL
		)`,
		`CREATE TABLE IF NOT EXISTS documents (
			item_id TEXT PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
			schema_version INTEGER NOT NULL,
			content_json TEXT NOT NULL,
			content_ciphertext BLOB NULL,
			spacing_preset TEXT NOT NULL DEFAULT 'compact',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS library_search_fts USING fts5(
			item_id UNINDEXED,
			kind UNINDEXED,
			title,
			body
		)`,
		`CREATE TABLE IF NOT EXISTS app_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS document_attachments (
			id TEXT PRIMARY KEY,
			document_id TEXT NOT NULL REFERENCES documents(item_id) ON DELETE CASCADE,
			mime_type TEXT NOT NULL,
			original_name TEXT NOT NULL DEFAULT '',
			size_bytes INTEGER NOT NULL,
			content_blob BLOB NULL,
			content_ciphertext BLOB NULL,
			created_at TEXT NOT NULL,
			detached_at TEXT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_document_attachments_document ON document_attachments(document_id)`,
		`CREATE TABLE IF NOT EXISTS encryption_master (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			kdf TEXT NOT NULL,
			kdf_params_json TEXT NOT NULL,
			salt BLOB NOT NULL,
			verifier_nonce BLOB NOT NULL,
			verifier_ciphertext BLOB NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS journal_encryption_keys (
			key_id TEXT PRIMARY KEY,
			journal_id TEXT NOT NULL UNIQUE REFERENCES items(id) ON DELETE CASCADE,
			wrapped_key_nonce BLOB NOT NULL,
			wrapped_key_ciphertext BLOB NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_items_parent_sort ON items(parent_id, sort_order, title)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return err
		}
	}
	if err := s.migrateItemsKindConstraint(); err != nil {
		return err
	}
	if err := s.ensureEncryptionColumns(); err != nil {
		return err
	}
	if err := s.ensureAttachmentColumns(); err != nil {
		return err
	}
	if err := s.ensureTrash(); err != nil {
		return err
	}
	if err := s.ensureDefaultJournal(); err != nil {
		return err
	}
	if err := s.ensureSetting("autosave_interval_ms", fmt.Sprintf("%d", defaultAutosaveIntervalMS)); err != nil {
		return err
	}
	if err := s.ensureSetting(settingLibraryWidth, fmt.Sprintf("%d", defaultLibraryWidth)); err != nil {
		return err
	}
	return s.ensureSetting(settingLastDocumentID, "")
}

func (s *JournalService) ensureTrash() error {
	var id string
	err := s.db.QueryRow(`SELECT id FROM items WHERE system_key = ?`, SystemTrash).Scan(&id)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	now := nowString()
	id = uuid.NewString()
	_, err = s.db.Exec(
		`INSERT INTO items (id, parent_id, kind, title, sort_order, system_key, created_at, updated_at)
		 VALUES (?, NULL, ?, 'Trash', 999999, ?, ?, ?)`,
		id, KindFolder, SystemTrash, now, now,
	)
	if err != nil {
		return err
	}
	return s.syncFTS(s.db, id)
}

func (s *JournalService) migrateItemsKindConstraint() error {
	var schema string
	if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'items'`).Scan(&schema); err != nil {
		return err
	}
	if strings.Contains(schema, "'journal'") {
		return nil
	}

	if _, err := s.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer func() {
		_, _ = s.db.Exec(`PRAGMA foreign_keys = ON`)
	}()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer rollback(tx)

	statements := []string{
		`ALTER TABLE documents RENAME TO documents_legacy`,
		`ALTER TABLE items RENAME TO items_legacy`,
		`CREATE TABLE items (
			id TEXT PRIMARY KEY,
			parent_id TEXT NULL REFERENCES items(id) ON DELETE CASCADE,
			kind TEXT NOT NULL CHECK (kind IN ('journal', 'folder', 'document')),
			title TEXT NOT NULL,
			sort_order INTEGER NOT NULL DEFAULT 0,
			system_key TEXT NULL UNIQUE,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			encryption_state TEXT NOT NULL DEFAULT 'plaintext',
			encryption_key_id TEXT NULL,
			title_ciphertext BLOB NULL
		)`,
		`CREATE TABLE documents (
			item_id TEXT PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
			schema_version INTEGER NOT NULL,
			content_json TEXT NOT NULL,
			content_ciphertext BLOB NULL,
			spacing_preset TEXT NOT NULL DEFAULT 'compact',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`INSERT INTO items (id, parent_id, kind, title, sort_order, system_key, created_at, updated_at, encryption_state)
		 SELECT id, parent_id, kind, title, sort_order, system_key, created_at, updated_at, 'plaintext' FROM items_legacy`,
		`INSERT INTO documents (item_id, schema_version, content_json, created_at, updated_at)
		 SELECT item_id, schema_version, content_json, created_at, updated_at FROM documents_legacy`,
		`DROP TABLE documents_legacy`,
		`DROP TABLE items_legacy`,
		`CREATE INDEX IF NOT EXISTS idx_items_parent_sort ON items(parent_id, sort_order, title)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *JournalService) ensureEncryptionColumns() error {
	itemColumns, err := tableColumns(s.db, "items")
	if err != nil {
		return err
	}
	itemStatements := map[string]string{
		"encryption_state":  `ALTER TABLE items ADD COLUMN encryption_state TEXT NOT NULL DEFAULT 'plaintext'`,
		"encryption_key_id": `ALTER TABLE items ADD COLUMN encryption_key_id TEXT NULL`,
		"title_ciphertext":  `ALTER TABLE items ADD COLUMN title_ciphertext BLOB NULL`,
	}
	for name, statement := range itemStatements {
		if !itemColumns[name] {
			if _, err := s.db.Exec(statement); err != nil {
				return err
			}
		}
	}
	documentColumns, err := tableColumns(s.db, "documents")
	if err != nil {
		return err
	}
	if !documentColumns["content_ciphertext"] {
		if _, err := s.db.Exec(`ALTER TABLE documents ADD COLUMN content_ciphertext BLOB NULL`); err != nil {
			return err
		}
	}
	if !documentColumns["spacing_preset"] {
		if _, err := s.db.Exec(`ALTER TABLE documents ADD COLUMN spacing_preset TEXT NOT NULL DEFAULT 'compact'`); err != nil {
			return err
		}
	}
	return nil
}

func (s *JournalService) ensureAttachmentColumns() error {
	attachmentColumns, err := tableColumns(s.db, "document_attachments")
	if err != nil {
		return err
	}
	if !attachmentColumns["detached_at"] {
		if _, err := s.db.Exec(`ALTER TABLE document_attachments ADD COLUMN detached_at TEXT NULL`); err != nil {
			return err
		}
	}
	return nil
}

func tableColumns(db queryer, table string) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func (s *JournalService) ensureDefaultJournal() error {
	journalID, err := s.firstJournalID()
	if err != nil {
		return err
	}
	if journalID == "" {
		now := nowString()
		journalID = uuid.NewString()
		order, err := s.nextJournalSortOrder()
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(
			`INSERT INTO items (id, parent_id, kind, title, sort_order, created_at, updated_at)
			 VALUES (?, NULL, ?, 'New Journal', ?, ?, ?)`,
			journalID, KindJournal, order, now, now,
		); err != nil {
			return err
		}
		if err := s.syncFTS(s.db, journalID); err != nil {
			return err
		}
	}

	_, err = s.db.Exec(
		`UPDATE items
		 SET parent_id = ?
		 WHERE parent_id IS NULL
		   AND kind != ?
		   AND COALESCE(system_key, '') != ?`,
		journalID, KindJournal, SystemTrash,
	)
	return err
}

func (s *JournalService) ensureSetting(key, value string) error {
	now := nowString()
	_, err := s.db.Exec(
		`INSERT INTO app_settings (key, value, updated_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(key) DO NOTHING`,
		key, value, now,
	)
	return err
}

func (s *JournalService) settingValue(key string) string {
	var value string
	if err := s.db.QueryRow(`SELECT value FROM app_settings WHERE key = ?`, key).Scan(&value); err != nil {
		return ""
	}
	return value
}

func (s *JournalService) rememberLastDocument(id string) error {
	now := nowString()
	_, err := s.db.Exec(
		`INSERT INTO app_settings (key, value, updated_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		settingLastDocumentID, id, now,
	)
	return err
}
