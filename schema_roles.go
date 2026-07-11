package main

import (
	"database/sql"
	"fmt"
)

const contentSchemaVersion = 1
const installationSchemaVersion = 1

// contentSchemaStatements are the portable tables shared by the local library
// database and a single-Journal cloud cache. Installation settings intentionally
// do not appear here.
func contentSchemaStatements() []string {
	return []string{
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
		`CREATE TABLE IF NOT EXISTS content_schema_state (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			version INTEGER NOT NULL
		)`,
	}
}

// installationSchemaStatements are local-device records only. They must never
// be applied to a cloud cache database or copied into a Vault revision.
func installationSchemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS app_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS vault_providers (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			kind TEXT NOT NULL,
			endpoint TEXT NOT NULL,
			root_prefix TEXT NOT NULL,
			credential_ref TEXT NOT NULL,
			publish_debounce_ms INTEGER NOT NULL DEFAULT 30000,
			publish_max_interval_ms INTEGER NOT NULL DEFAULT 300000,
			revision_retention_count INTEGER NOT NULL DEFAULT 50,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS cloud_journal_mounts (
			cloud_journal_id TEXT PRIMARY KEY,
			provider_id TEXT NOT NULL,
			vault_root TEXT NOT NULL,
			cache_path TEXT NOT NULL,
			last_revision_id TEXT NOT NULL DEFAULT '',
			last_current_token TEXT NOT NULL DEFAULT '',
			lease_id TEXT NOT NULL DEFAULT '',
			revision_retention_count INTEGER NOT NULL DEFAULT 0,
			sync_status TEXT NOT NULL DEFAULT 'clean',
			last_sync_error TEXT NOT NULL DEFAULT '',
			last_synced_at TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS cloud_pending_creates (
			cloud_journal_id TEXT PRIMARY KEY,
			provider_id TEXT NOT NULL,
			cache_path TEXT NOT NULL,
			stage TEXT NOT NULL,
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS installation_device_identity (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			device_id TEXT NOT NULL UNIQUE,
			owner_label TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS installation_schema_state (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			version INTEGER NOT NULL
		)`,
	}
}

func applySchemaStatements(db *sql.DB, statements []string) error {
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func ensureSchemaState(db *sql.DB, table string, version int) error {
	if _, err := db.Exec(fmt.Sprintf(`INSERT INTO %s (id, version) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET version = MAX(version, excluded.version)`, table), version); err != nil {
		return err
	}
	return nil
}

func applyContentSchema(db *sql.DB) error {
	if err := applySchemaStatements(db, contentSchemaStatements()); err != nil {
		return err
	}
	return ensureSchemaState(db, "content_schema_state", contentSchemaVersion)
}

func applyInstallationSchema(db *sql.DB) error {
	if err := applySchemaStatements(db, installationSchemaStatements()); err != nil {
		return err
	}
	return ensureSchemaState(db, "installation_schema_state", installationSchemaVersion)
}
