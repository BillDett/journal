package main

import (
	"database/sql"
	"encoding/json"
)

// Full-text indexing is isolated from document persistence so search policy is
// auditable without reading editor and attachment code.
func (s *JournalService) syncFTS(db dbRunner, id string) error {
	item, err := s.getRawRowItemFrom(db, id)
	if err != nil {
		return err
	}
	if item.EncryptionState == EncryptionEncrypted {
		_, err := db.Exec(`DELETE FROM library_search_fts WHERE item_id = ?`, id)
		return err
	}
	body := ""
	if item.Kind == KindDocument {
		var encoded string
		if err := db.QueryRow(`SELECT content_json FROM documents WHERE item_id = ?`, id).Scan(&encoded); err != nil {
			return err
		}
		var content map[string]any
		if err := json.Unmarshal([]byte(encoded), &content); err != nil {
			return err
		}
		body = extractText(content)
	}
	if _, err := db.Exec(`DELETE FROM library_search_fts WHERE item_id = ?`, id); err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO library_search_fts (item_id, kind, title, body) VALUES (?, ?, ?, ?)`, item.ID, item.Kind, item.Title, body)
	return err
}

func (s *JournalService) syncFTSTx(tx *sql.Tx, id string) error { return s.syncFTS(tx, id) }

func (s *JournalService) deleteFTSDescendantsTx(tx *sql.Tx, id string) error {
	ids, err := descendantIDs(tx, id)
	if err != nil {
		return err
	}
	for _, itemID := range append(ids, id) {
		if _, err := tx.Exec(`DELETE FROM library_search_fts WHERE item_id = ?`, itemID); err != nil {
			return err
		}
	}
	return nil
}
