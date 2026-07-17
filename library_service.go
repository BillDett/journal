package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Library commands: journals, folders, moves, trash transitions, and search.

func (s *JournalService) GetLibraryTree() (TreeResponse, error) {
	items, err := s.loadItems()
	if err != nil {
		return TreeResponse{}, err
	}
	trashID, err := s.trashID()
	if err != nil {
		return TreeResponse{}, err
	}
	return TreeResponse{Items: buildTree(items, nil), TrashID: trashID}, nil
}

func (s *JournalService) GetJournalDetails(journalID string) (JournalDetailsResponse, error) {
	journalID = strings.TrimSpace(journalID)
	item, err := s.getTreeItem(journalID)
	if err != nil {
		return JournalDetailsResponse{}, err
	}
	if item.Kind != KindJournal || item.ParentID != "" {
		return JournalDetailsResponse{}, fmt.Errorf("item is not a journal")
	}
	details := JournalDetailsResponse{
		ID: journalID, Title: item.Title, EncryptionState: item.EncryptionState,
		EncryptionLocked: item.EncryptionLocked, CreatedAt: item.CreatedAt,
	}
	if item.EncryptionLocked {
		return details, nil
	}
	if err := s.db.QueryRow(`WITH RECURSIVE descendants(id) AS (
		SELECT id FROM items WHERE id = ?
		UNION ALL
		SELECT items.id FROM items JOIN descendants ON items.parent_id = descendants.id
	)
	SELECT
		COALESCE(SUM(CASE WHEN kind = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN kind = ? THEN 1 ELSE 0 END), 0)
	FROM items WHERE id IN descendants`, journalID, KindDocument, KindFolder).Scan(&details.DocumentCount, &details.FolderCount); err != nil {
		return JournalDetailsResponse{}, err
	}
	if err := s.db.QueryRow(`WITH RECURSIVE descendants(id) AS (
		SELECT id FROM items WHERE id = ?
		UNION ALL
		SELECT items.id FROM items JOIN descendants ON items.parent_id = descendants.id
	)
	SELECT COUNT(*) FROM document_attachments
	WHERE detached_at IS NULL AND document_id IN (SELECT id FROM descendants)`, journalID).Scan(&details.ImageCount); err != nil {
		return JournalDetailsResponse{}, err
	}
	return details, nil
}

func (s *JournalService) CreateFolder(parentID string, title string) (ItemResponse, error) {
	parentID, err := s.resolveCreateParent(parentID)
	if err != nil {
		return ItemResponse{}, err
	}
	title = normalizeTitle(title, "New Folder")
	now := nowString()
	id := uuid.NewString()
	order, err := s.nextSortOrder(parentID)
	if err != nil {
		return ItemResponse{}, err
	}
	_, keyID, key, encrypted, err := s.encryptionContextForParent(parentID)
	if err != nil {
		return ItemResponse{}, err
	}
	storedTitle := title
	var titleCiphertext []byte
	encryptionState := EncryptionPlaintext
	encryptionKeyID := sql.NullString{}
	if encrypted {
		encryptionState = EncryptionEncrypted
		encryptionKeyID = sql.NullString{String: keyID, Valid: true}
		titleCiphertext, err = sealField(key, "items", id, "title", keyID, []byte(title))
		if err != nil {
			return ItemResponse{}, err
		}
		storedTitle = encryptedTitlePlaceholder(KindFolder)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return ItemResponse{}, err
	}
	defer rollback(tx)
	if _, err := tx.Exec(
		`INSERT INTO items (id, parent_id, kind, title, sort_order, created_at, updated_at, encryption_state, encryption_key_id, title_ciphertext)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, parentID, KindFolder, storedTitle, order, now, now, encryptionState, encryptionKeyID, titleCiphertext,
	); err != nil {
		return ItemResponse{}, err
	}
	if err := s.syncFTSTx(tx, id); err != nil {
		return ItemResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return ItemResponse{}, err
	}
	item, err := s.getTreeItem(id)
	if err != nil {
		return ItemResponse{}, err
	}
	tree, err := s.GetLibraryTree()
	return ItemResponse{Item: item, Tree: tree}, err
}

func (s *JournalService) CreateJournal(title string) (ItemResponse, error) {
	title = normalizeTitle(title, "New Journal")
	now := nowString()
	id := uuid.NewString()
	order, err := s.nextJournalSortOrder()
	if err != nil {
		return ItemResponse{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return ItemResponse{}, err
	}
	defer rollback(tx)
	if _, err := tx.Exec(
		`INSERT INTO items (id, parent_id, kind, title, sort_order, created_at, updated_at)
		 VALUES (?, NULL, ?, ?, ?, ?, ?)`,
		id, KindJournal, title, order, now, now,
	); err != nil {
		return ItemResponse{}, err
	}
	if err := s.syncFTSTx(tx, id); err != nil {
		return ItemResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return ItemResponse{}, err
	}
	item, err := s.getTreeItem(id)
	if err != nil {
		return ItemResponse{}, err
	}
	tree, err := s.GetLibraryTree()
	return ItemResponse{Item: item, Tree: tree}, err
}

func (s *JournalService) RenameItem(id string, title string) (ItemResponse, error) {
	title = normalizeTitle(title, "Untitled")
	item, err := s.getRawRowItemFrom(s.db, id)
	if err != nil {
		return ItemResponse{}, err
	}
	if item.SystemKey.String == SystemTrash {
		return ItemResponse{}, fmt.Errorf("trash cannot be renamed")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return ItemResponse{}, err
	}
	defer rollback(tx)
	now := nowString()
	if item.EncryptionState == EncryptionEncrypted && item.Kind != KindJournal {
		journalID, err := s.journalIDForItemFrom(tx, id)
		if err != nil {
			return ItemResponse{}, err
		}
		key, ok := s.journalKey(journalID)
		if !ok {
			return ItemResponse{}, ErrEncryptionLocked
		}
		if !item.EncryptionKeyID.Valid {
			return ItemResponse{}, fmt.Errorf("encrypted item key is missing")
		}
		titleCiphertext, err := sealField(key, "items", id, "title", item.EncryptionKeyID.String, []byte(title))
		if err != nil {
			return ItemResponse{}, err
		}
		if _, err := tx.Exec(`UPDATE items SET title = ?, title_ciphertext = ?, updated_at = ? WHERE id = ?`, encryptedTitlePlaceholder(item.Kind), titleCiphertext, now, id); err != nil {
			return ItemResponse{}, err
		}
	} else {
		if _, err := tx.Exec(`UPDATE items SET title = ?, updated_at = ? WHERE id = ?`, title, now, id); err != nil {
			return ItemResponse{}, err
		}
	}
	if err := s.syncFTSTx(tx, id); err != nil {
		return ItemResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return ItemResponse{}, err
	}
	next, err := s.getTreeItem(id)
	if err != nil {
		return ItemResponse{}, err
	}
	tree, err := s.GetLibraryTree()
	return ItemResponse{Item: next, Tree: tree}, err
}

func (s *JournalService) MoveItem(id string, newParentID string, newSortOrder int) (TreeResponse, error) {
	id = strings.TrimSpace(id)
	newParentID = strings.TrimSpace(newParentID)
	item, err := s.getRowItem(id)
	if err != nil {
		return TreeResponse{}, err
	}
	if item.Kind == KindJournal {
		return s.moveJournal(id, newSortOrder)
	}
	if err := s.validateMove(id, newParentID); err != nil {
		return TreeResponse{}, err
	}
	trashID, err := s.trashID()
	if err != nil {
		return TreeResponse{}, err
	}
	if newParentID != trashID {
		sourceKeyID, err := s.encryptionBoundaryKeyID(id)
		if err != nil {
			return TreeResponse{}, err
		}
		targetKeyID, err := s.encryptionBoundaryKeyID(newParentID)
		if err != nil {
			return TreeResponse{}, err
		}
		if sourceKeyID != targetKeyID {
			return TreeResponse{}, fmt.Errorf("items cannot be moved between encrypted and plaintext journals")
		}
	}

	sourceJournalID, err := s.journalIDForItem(id)
	if err != nil {
		return TreeResponse{}, err
	}
	targetJournalID, err := s.journalIDForItem(newParentID)
	if err != nil {
		return TreeResponse{}, err
	}
	inTrash, err := s.isInTrash(id)
	if err != nil {
		return TreeResponse{}, err
	}
	if !inTrash && sourceJournalID != "" && targetJournalID != "" && sourceJournalID != targetJournalID {
		tx, err := s.db.Begin()
		if err != nil {
			return TreeResponse{}, err
		}
		defer rollback(tx)
		if _, err := s.copyItemToParentTx(tx, id, newParentID, nowString()); err != nil {
			return TreeResponse{}, err
		}
		if err := tx.Commit(); err != nil {
			return TreeResponse{}, err
		}
		return s.GetLibraryTree()
	}

	tx, err := s.db.Begin()
	if err != nil {
		return TreeResponse{}, err
	}
	defer rollback(tx)

	now := nowString()
	if _, err := tx.Exec(
		`UPDATE items SET parent_id = ?, updated_at = ? WHERE id = ?`,
		newParentID, now, id,
	); err != nil {
		return TreeResponse{}, err
	}
	if err := reorderSiblings(tx, newParentID, id, newSortOrder); err != nil {
		return TreeResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return TreeResponse{}, err
	}
	return s.GetLibraryTree()
}

func (s *JournalService) TrashItem(command TrashItemCommand) (TreeResponse, error) {
	id := strings.TrimSpace(command.ID)
	if id == "" {
		return TreeResponse{}, fmt.Errorf("item id is required")
	}
	s.operationMu.Lock()
	defer s.operationMu.Unlock()

	trashID, err := s.trashID()
	if err != nil {
		return TreeResponse{}, err
	}
	if id == trashID {
		return TreeResponse{}, fmt.Errorf("trash cannot be deleted")
	}
	inTrash, err := s.isInTrash(id)
	if err != nil {
		return TreeResponse{}, err
	}
	if inTrash {
		if !command.ExpectedInTrash {
			return TreeResponse{}, fmt.Errorf("item is already in trash; confirm permanent deletion explicitly")
		}
		return s.PermanentlyDeleteItem(id)
	}
	if command.ExpectedInTrash {
		return TreeResponse{}, fmt.Errorf("item is no longer in trash")
	}
	return s.MoveItem(id, trashID, -1)
}

func (s *JournalService) MoveItemToTrash(id string) (TreeResponse, error) {
	return s.TrashItem(TrashItemCommand{ID: id, ExpectedInTrash: false})
}

func (s *JournalService) PermanentlyDeleteItem(id string) (TreeResponse, error) {
	deletedIDs, _ := descendantIDs(s.db, id)
	deletedIDs = append(deletedIDs, id)
	trashID, err := s.trashID()
	if err != nil {
		return TreeResponse{}, err
	}
	if id == trashID {
		return TreeResponse{}, fmt.Errorf("trash cannot be permanently deleted")
	}
	inTrash, err := s.isInTrash(id)
	if err != nil {
		return TreeResponse{}, err
	}
	if !inTrash {
		return TreeResponse{}, fmt.Errorf("item must be in trash before permanent deletion")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return TreeResponse{}, err
	}
	defer rollback(tx)
	if err := s.deleteFTSDescendantsTx(tx, id); err != nil {
		return TreeResponse{}, err
	}
	if _, err := tx.Exec(`DELETE FROM items WHERE id = ?`, id); err != nil {
		return TreeResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return TreeResponse{}, err
	}
	s.removePendingIDs(deletedIDs)
	return s.GetLibraryTree()
}

func (s *JournalService) DeleteJournal(id string) (TreeResponse, error) {
	deletedIDs, _ := descendantIDs(s.db, id)
	deletedIDs = append(deletedIDs, id)
	item, err := s.getRowItem(id)
	if err != nil {
		return TreeResponse{}, err
	}
	if item.Kind != KindJournal {
		return TreeResponse{}, fmt.Errorf("item is not a journal")
	}
	count, err := s.journalCount()
	if err != nil {
		return TreeResponse{}, err
	}
	if count <= 1 {
		return TreeResponse{}, fmt.Errorf("at least one journal is required")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return TreeResponse{}, err
	}
	defer rollback(tx)
	if err := s.deleteFTSDescendantsTx(tx, id); err != nil {
		return TreeResponse{}, err
	}
	if _, err := tx.Exec(`DELETE FROM items WHERE id = ?`, id); err != nil {
		return TreeResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return TreeResponse{}, err
	}
	s.removePendingIDs(deletedIDs)
	if s.settingValue(settingLastDocumentID) != "" {
		if _, err := s.getRowItem(s.settingValue(settingLastDocumentID)); err != nil {
			_ = s.rememberLastDocument("")
		}
	}
	return s.GetLibraryTree()
}

func (s *JournalService) SearchLibrary(query string) (SearchResponse, error) {
	query = strings.TrimSpace(query)
	trashID, err := s.trashID()
	if err != nil {
		return SearchResponse{}, err
	}
	if query == "" {
		tree, err := s.GetLibraryTree()
		if err != nil {
			return SearchResponse{}, err
		}
		return SearchResponse{Items: tree.Items, TrashID: tree.TrashID}, nil
	}
	rows, err := s.db.Query(
		`SELECT item_id FROM library_search_fts WHERE library_search_fts MATCH ? ORDER BY rank`,
		ftsPhrase(query),
	)
	if err != nil {
		return SearchResponse{}, err
	}
	defer rows.Close()
	matches := map[string]bool{}
	resultIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return SearchResponse{}, err
		}
		if !matches[id] {
			matches[id] = true
			resultIDs = append(resultIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		return SearchResponse{}, err
	}

	items, err := s.loadItems()
	if err != nil {
		return SearchResponse{}, err
	}
	include := map[string]bool{}
	byID := map[string]rowItem{}
	for _, item := range items {
		byID[item.ID] = item
	}
	filteredMatches := map[string]bool{}
	filteredResultIDs := make([]string, 0, len(resultIDs))
	for _, id := range resultIDs {
		if rowIsInTrash(byID, id, trashID) {
			continue
		}
		filteredMatches[id] = true
		filteredResultIDs = append(filteredResultIDs, id)
	}
	for id := range filteredMatches {
		for {
			item, ok := byID[id]
			if !ok {
				break
			}
			include[item.ID] = true
			if !item.ParentID.Valid {
				break
			}
			id = item.ParentID.String
		}
	}
	filtered := make([]rowItem, 0, len(include))
	for _, item := range items {
		if include[item.ID] {
			filtered = append(filtered, item)
		}
	}
	return SearchResponse{
		Query:     query,
		Items:     buildTree(filtered, filteredMatches),
		ResultIDs: filteredResultIDs,
		TrashID:   trashID,
	}, nil
}

func (s *JournalService) resolveCreateParent(parentID string) (string, error) {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return s.firstJournalID()
	}
	if err := s.validateParent(parentID); err != nil {
		return "", err
	}
	return parentID, nil
}

func (s *JournalService) validateParent(parentID string) error {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return fmt.Errorf("parent journal or folder is required")
	}
	parent, err := s.getRowItem(parentID)
	if err != nil {
		return err
	}
	if parent.SystemKey.String == SystemTrash {
		return fmt.Errorf("trash cannot contain new items directly")
	}
	if parent.Kind != KindFolder && parent.Kind != KindJournal {
		return fmt.Errorf("parent must be a journal or folder")
	}
	return nil
}

func (s *JournalService) validateMove(id string, newParentID string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("item id is required")
	}
	item, err := s.getRowItem(id)
	if err != nil {
		return err
	}
	if item.SystemKey.String == SystemTrash || item.Kind == KindJournal {
		return fmt.Errorf("item cannot be moved here")
	}
	trashID, err := s.trashID()
	if err != nil {
		return err
	}
	if newParentID != trashID {
		if err := s.validateParent(newParentID); err != nil {
			return err
		}
	}
	if id == newParentID {
		return fmt.Errorf("folder cannot be moved into itself")
	}
	descendants, err := descendantIDs(s.db, id)
	if err != nil {
		return err
	}
	for _, descendantID := range descendants {
		if descendantID == newParentID {
			return fmt.Errorf("folder cannot be moved into its descendant")
		}
	}
	return nil
}

func (s *JournalService) moveJournal(id string, requestedOrder int) (TreeResponse, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return TreeResponse{}, err
	}
	defer rollback(tx)
	if _, err := tx.Exec(`UPDATE items SET parent_id = NULL, updated_at = ? WHERE id = ?`, nowString(), id); err != nil {
		return TreeResponse{}, err
	}
	if err := reorderJournals(tx, id, requestedOrder); err != nil {
		return TreeResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return TreeResponse{}, err
	}
	return s.GetLibraryTree()
}

func reorderSiblings(tx *sql.Tx, parentID string, movedID string, requestedOrder int) error {
	rows, err := tx.Query(
		`SELECT id FROM items WHERE parent_id IS ? AND id != ? ORDER BY sort_order, title COLLATE NOCASE`,
		nullParent(parentID), movedID,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if requestedOrder < 0 || requestedOrder > len(ids) {
		requestedOrder = len(ids)
	}
	ids = append(ids, "")
	copy(ids[requestedOrder+1:], ids[requestedOrder:])
	ids[requestedOrder] = movedID
	for index, id := range ids {
		if _, err := tx.Exec(`UPDATE items SET sort_order = ? WHERE id = ?`, index, id); err != nil {
			return err
		}
	}
	return nil
}

func reorderJournals(tx *sql.Tx, movedID string, requestedOrder int) error {
	rows, err := tx.Query(
		`SELECT id FROM items WHERE parent_id IS NULL AND kind = ? AND id != ? ORDER BY sort_order, title COLLATE NOCASE`,
		KindJournal, movedID,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if requestedOrder < 0 || requestedOrder > len(ids) {
		requestedOrder = len(ids)
	}
	ids = append(ids, "")
	copy(ids[requestedOrder+1:], ids[requestedOrder:])
	ids[requestedOrder] = movedID
	for index, id := range ids {
		if _, err := tx.Exec(`UPDATE items SET sort_order = ? WHERE id = ?`, index, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *JournalService) copyItemToParentTx(tx *sql.Tx, sourceID string, parentID string, now string) (string, error) {
	source, err := s.getRowItemFrom(tx, sourceID)
	if err != nil {
		return "", err
	}
	if source.Kind != KindFolder && source.Kind != KindDocument {
		return "", fmt.Errorf("only folders and documents can be copied")
	}
	newID := uuid.NewString()
	order, err := nextSortOrderFrom(tx, parentID)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(
		`INSERT INTO items (id, parent_id, kind, title, sort_order, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		newID, parentID, source.Kind, source.Title, order, now, now,
	); err != nil {
		return "", err
	}
	if source.Kind == KindDocument {
		var schemaVersion int
		var encoded string
		var spacingPreset string
		if err := tx.QueryRow(`SELECT schema_version, content_json, spacing_preset FROM documents WHERE item_id = ?`, sourceID).Scan(&schemaVersion, &encoded, &spacingPreset); err != nil {
			return "", err
		}
		if draft, ok := s.pendingDraftSnapshot(sourceID); ok {
			data, err := json.Marshal(draft.Content)
			if err != nil {
				return "", err
			}
			encoded = string(data)
		}
		if _, err := tx.Exec(
			`INSERT INTO documents (item_id, schema_version, content_json, spacing_preset, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			newID, schemaVersion, encoded, normalizeSpacingPreset(spacingPreset), now, now,
		); err != nil {
			return "", err
		}
		attachmentIDMap, err := s.copyDocumentAttachmentsTx(tx, sourceID, newID, nil, "")
		if err != nil {
			return "", err
		}
		if len(attachmentIDMap) > 0 {
			var content map[string]any
			if err := json.Unmarshal([]byte(encoded), &content); err != nil {
				return "", err
			}
			encodedContent, err := json.Marshal(remapAttachmentIDsInContent(content, attachmentIDMap))
			if err != nil {
				return "", err
			}
			if _, err := tx.Exec(`UPDATE documents SET content_json = ? WHERE item_id = ?`, string(encodedContent), newID); err != nil {
				return "", err
			}
		}
	}
	if err := s.syncFTSTx(tx, newID); err != nil {
		return "", err
	}
	if source.Kind == KindFolder {
		rows, err := tx.Query(`SELECT id FROM items WHERE parent_id = ? ORDER BY sort_order, title COLLATE NOCASE`, sourceID)
		if err != nil {
			return "", err
		}
		var childIDs []string
		for rows.Next() {
			var childID string
			if err := rows.Scan(&childID); err != nil {
				_ = rows.Close()
				return "", err
			}
			childIDs = append(childIDs, childID)
		}
		if err := rows.Close(); err != nil {
			return "", err
		}
		if err := rows.Err(); err != nil {
			return "", err
		}
		for _, childID := range childIDs {
			if _, err := s.copyItemToParentTx(tx, childID, newID, now); err != nil {
				return "", err
			}
		}
	}
	return newID, nil
}
