package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Library persistence queries, hierarchy traversal, and encrypted display projection.

func (s *JournalService) loadItems() ([]rowItem, error) {
	rows, err := s.db.Query(
		`SELECT id, parent_id, kind, title, sort_order, system_key, created_at, updated_at,
		        encryption_state, encryption_key_id, title_ciphertext
		 FROM items
		 ORDER BY parent_id IS NOT NULL, parent_id, sort_order, title COLLATE NOCASE`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []rowItem
	for rows.Next() {
		var item rowItem
		if err := rows.Scan(&item.ID, &item.ParentID, &item.Kind, &item.Title, &item.SortOrder, &item.SystemKey, &item.CreatedAt, &item.UpdatedAt, &item.EncryptionState, &item.EncryptionKeyID, &item.TitleCiphertext); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.prepareItemsForDisplay(items)
}

func (s *JournalService) getRowItem(id string) (rowItem, error) {
	return s.getRowItemFrom(s.db, id)
}

func (s *JournalService) getRowItemFrom(db queryRower, id string) (rowItem, error) {
	item, err := s.getRawRowItemFrom(db, id)
	if err != nil {
		return rowItem{}, err
	}
	return s.prepareItemForDisplay(item)
}

func (s *JournalService) getRawRowItemFrom(db queryRower, id string) (rowItem, error) {
	var item rowItem
	err := db.QueryRow(
		`SELECT id, parent_id, kind, title, sort_order, system_key, created_at, updated_at,
		        encryption_state, encryption_key_id, title_ciphertext
		 FROM items WHERE id = ?`,
		id,
	).Scan(&item.ID, &item.ParentID, &item.Kind, &item.Title, &item.SortOrder, &item.SystemKey, &item.CreatedAt, &item.UpdatedAt, &item.EncryptionState, &item.EncryptionKeyID, &item.TitleCiphertext)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rowItem{}, fmt.Errorf("item not found")
		}
		return rowItem{}, err
	}
	return item, nil
}

func (s *JournalService) prepareItemsForDisplay(items []rowItem) ([]rowItem, error) {
	byID := map[string]rowItem{}
	for _, item := range items {
		byID[item.ID] = item
	}
	lockedRoots := map[string]bool{}
	for _, item := range items {
		if item.Kind == KindJournal && item.EncryptionState == EncryptionEncrypted {
			if _, ok := s.journalKey(item.ID); !ok {
				lockedRoots[item.ID] = true
			}
		}
	}
	display := make([]rowItem, 0, len(items))
	for _, item := range items {
		rootID := rootJournalIDForRows(byID, item.ID)
		if item.Kind == KindJournal && item.EncryptionState == EncryptionEncrypted {
			item.EncryptionLocked = lockedRoots[item.ID]
		}
		if item.Kind != KindJournal && item.EncryptionState == EncryptionEncrypted {
			if lockedRoots[rootID] {
				continue
			}
		}
		prepared, err := s.prepareItemForDisplayWithRoot(item, rootID)
		if err != nil {
			return nil, err
		}
		display = append(display, prepared)
	}
	return display, nil
}

func (s *JournalService) prepareItemForDisplay(item rowItem) (rowItem, error) {
	if item.EncryptionState != EncryptionEncrypted {
		if item.EncryptionState == "" {
			item.EncryptionState = EncryptionPlaintext
		}
		return item, nil
	}
	rootID, err := s.journalIDForItem(item.ID)
	if err != nil {
		return rowItem{}, err
	}
	return s.prepareItemForDisplayWithRoot(item, rootID)
}

func (s *JournalService) prepareItemForDisplayWithRoot(item rowItem, rootID string) (rowItem, error) {
	if item.EncryptionState == "" {
		item.EncryptionState = EncryptionPlaintext
	}
	if item.EncryptionState != EncryptionEncrypted {
		return item, nil
	}
	if item.Kind == KindJournal {
		if _, ok := s.journalKey(item.ID); !ok {
			item.EncryptionLocked = true
		}
		return item, nil
	}
	key, ok := s.journalKey(rootID)
	if !ok {
		item.EncryptionLocked = true
		return item, nil
	}
	if !item.EncryptionKeyID.Valid || len(item.TitleCiphertext) == 0 {
		return rowItem{}, fmt.Errorf("encrypted item title is missing")
	}
	plaintext, err := openField(key, "items", item.ID, "title", item.EncryptionKeyID.String, item.TitleCiphertext)
	if err != nil {
		return rowItem{}, err
	}
	item.Title = string(plaintext)
	return item, nil
}

func rootJournalIDForRows(items map[string]rowItem, id string) string {
	for id != "" {
		item, ok := items[id]
		if !ok {
			return ""
		}
		if item.Kind == KindJournal {
			return item.ID
		}
		if !item.ParentID.Valid {
			return ""
		}
		id = item.ParentID.String
	}
	return ""
}

func (s *JournalService) getTreeItem(id string) (TreeItem, error) {
	item, err := s.getRowItem(id)
	if err != nil {
		return TreeItem{}, err
	}
	return treeItemFromRow(item), nil
}

func (s *JournalService) trashID() (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM items WHERE system_key = ?`, SystemTrash).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s *JournalService) isInTrash(id string) (bool, error) {
	trashID, err := s.trashID()
	if err != nil {
		return false, err
	}
	for id != "" {
		if id == trashID {
			return true, nil
		}
		item, err := s.getRawRowItemFrom(s.db, id)
		if err != nil {
			return false, err
		}
		if !item.ParentID.Valid {
			return false, nil
		}
		id = item.ParentID.String
	}
	return false, nil
}

func (s *JournalService) nextSortOrder(parentID string) (int, error) {
	return nextSortOrderFrom(s.db, parentID)
}

func (s *JournalService) nextJournalSortOrder() (int, error) {
	var next sql.NullInt64
	err := s.db.QueryRow(
		`SELECT COALESCE(MAX(sort_order), -1) + 1 FROM items WHERE parent_id IS NULL AND kind = ?`,
		KindJournal,
	).Scan(&next)
	if err != nil {
		return 0, err
	}
	return int(next.Int64), nil
}

func nextSortOrderFrom(db queryRower, parentID string) (int, error) {
	var next sql.NullInt64
	err := db.QueryRow(
		`SELECT COALESCE(MAX(sort_order), -1) + 1 FROM items WHERE parent_id = ?`,
		parentID,
	).Scan(&next)
	if err != nil {
		return 0, err
	}
	return int(next.Int64), nil
}

func descendantIDs(db queryer, id string) ([]string, error) {
	rows, err := db.Query(
		`WITH RECURSIVE descendants(id) AS (
			SELECT id FROM items WHERE parent_id = ?
			UNION ALL
			SELECT items.id FROM items JOIN descendants ON items.parent_id = descendants.id
		)
		SELECT id FROM descendants`,
		id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var next string
		if err := rows.Scan(&next); err != nil {
			return nil, err
		}
		ids = append(ids, next)
	}
	return ids, rows.Err()
}

func (s *JournalService) firstJournalID() (string, error) {
	var id string
	err := s.db.QueryRow(
		`SELECT id FROM items WHERE kind = ? AND parent_id IS NULL ORDER BY sort_order, created_at LIMIT 1`,
		KindJournal,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s *JournalService) journalCount() (int, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM items WHERE kind = ? AND parent_id IS NULL`, KindJournal).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *JournalService) journalIDForItem(id string) (string, error) {
	return s.journalIDForItemFrom(s.db, id)
}

func (s *JournalService) journalIDForItemFrom(db queryRower, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", nil
	}
	for id != "" {
		item, err := s.getRawRowItemFrom(db, id)
		if err != nil {
			return "", err
		}
		if item.Kind == KindJournal {
			return item.ID, nil
		}
		if !item.ParentID.Valid {
			return "", nil
		}
		id = item.ParentID.String
	}
	return "", nil
}
