package main

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestGenerateStressDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "stress.db")
	cfg := config{
		output:          dbPath,
		journals:        2,
		minFolders:      3,
		maxFolders:      3,
		nestedPercent:   100,
		minDocuments:    4,
		maxDocuments:    4,
		minWords:        12,
		maxWords:        12,
		minWordLen:      3,
		maxWordLen:      8,
		seed:            1,
		paragraphWords:  5,
		autosaveMS:      2000,
		reportEveryDocs: 0,
	}
	if err := generate(cfg); err != nil {
		t.Fatalf("generate: %v", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open generated db: %v", err)
	}
	defer db.Close()

	assertCount(t, db, `SELECT COUNT(*) FROM items WHERE kind = 'journal'`, 2)
	assertCount(t, db, `SELECT COUNT(*) FROM items WHERE kind = 'folder' AND COALESCE(system_key, '') != 'trash'`, 6)
	assertCount(t, db, `SELECT COUNT(*) FROM documents`, 8)
	assertCount(t, db, `SELECT COUNT(*) FROM items WHERE system_key = 'trash'`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM library_search_fts`, 17)
	assertCount(t, db, `SELECT COUNT(*) FROM pragma_foreign_key_check`, 0)

	rows, err := db.Query(`SELECT content_json FROM documents`)
	if err != nil {
		t.Fatalf("select documents: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			t.Fatalf("scan document: %v", err)
		}
		var content map[string]any
		if err := json.Unmarshal([]byte(encoded), &content); err != nil {
			t.Fatalf("document content json: %v", err)
		}
		if content["type"] != "doc" {
			t.Fatalf("expected ProseMirror doc node, got %#v", content)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("document rows: %v", err)
	}

	var hit string
	if err := db.QueryRow(`SELECT item_id FROM library_search_fts WHERE kind = 'document' AND body != '' LIMIT 1`).Scan(&hit); err != nil {
		t.Fatalf("expected searchable document body: %v", err)
	}
}

func assertCount(t *testing.T, db *sql.DB, query string, expected int) {
	t.Helper()
	var actual int
	if err := db.QueryRow(query).Scan(&actual); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	if actual != expected {
		t.Fatalf("count query %q: expected %d, got %d", query, expected, actual)
	}
}
