package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Document creation, draft handling, persistence, and spacing commands.

func (s *JournalService) CreateDocument(parentID string) (DocumentResponse, error) {
	parentID, err := s.resolveCreateParent(parentID)
	if err != nil {
		return DocumentResponse{}, err
	}
	now := nowString()
	id := uuid.NewString()
	content := emptyDocument()
	encoded, _ := json.Marshal(content)
	order, err := s.nextSortOrder(parentID)
	if err != nil {
		return DocumentResponse{}, err
	}
	_, keyID, key, encrypted, err := s.encryptionContextForParent(parentID)
	if err != nil {
		return DocumentResponse{}, err
	}
	title := "Untitled"
	contentJSON := string(encoded)
	var titleCiphertext []byte
	var contentCiphertext []byte
	encryptionState := EncryptionPlaintext
	encryptionKeyID := sql.NullString{}
	if encrypted {
		encryptionState = EncryptionEncrypted
		encryptionKeyID = sql.NullString{String: keyID, Valid: true}
		titleCiphertext, err = sealField(key, "items", id, "title", keyID, []byte(title))
		if err != nil {
			return DocumentResponse{}, err
		}
		contentCiphertext, err = sealField(key, "documents", id, "content_json", keyID, encoded)
		if err != nil {
			return DocumentResponse{}, err
		}
		title = encryptedTitlePlaceholder(KindDocument)
		placeholder, _ := json.Marshal(emptyDocument())
		contentJSON = string(placeholder)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return DocumentResponse{}, err
	}
	defer rollback(tx)

	if _, err := tx.Exec(
		`INSERT INTO items (id, parent_id, kind, title, sort_order, created_at, updated_at, encryption_state, encryption_key_id, title_ciphertext)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, parentID, KindDocument, title, order, now, now, encryptionState, encryptionKeyID, titleCiphertext,
	); err != nil {
		return DocumentResponse{}, err
	}
	if _, err := tx.Exec(
		`INSERT INTO documents (item_id, schema_version, content_json, content_ciphertext, spacing_preset, created_at, updated_at)
		 VALUES (?, 1, ?, ?, ?, ?, ?)`,
		id, contentJSON, contentCiphertext, defaultSpacingPreset, now, now,
	); err != nil {
		return DocumentResponse{}, err
	}
	if err := s.syncFTSTx(tx, id); err != nil {
		return DocumentResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return DocumentResponse{}, err
	}
	return s.OpenDocument(id)
}

func (s *JournalService) DuplicateDocument(id string) (DocumentResponse, error) {
	id = strings.TrimSpace(id)
	rawItem, err := s.getRawRowItemFrom(s.db, id)
	if err != nil {
		return DocumentResponse{}, err
	}
	if rawItem.Kind != KindDocument {
		return DocumentResponse{}, fmt.Errorf("item is not a document")
	}
	parentID := ""
	if rawItem.ParentID.Valid {
		parentID = rawItem.ParentID.String
	}
	displayItem, err := s.prepareItemForDisplay(rawItem)
	if err != nil {
		return DocumentResponse{}, err
	}
	title := "Copy of " + displayItem.Title
	now := nowString()
	newID := uuid.NewString()
	order, err := s.nextSortOrder(parentID)
	if err != nil {
		return DocumentResponse{}, err
	}

	var schemaVersion int
	var encoded string
	var contentCiphertext []byte
	var spacingPreset string
	var key []byte
	keyID := ""
	if rawItem.EncryptionState == EncryptionEncrypted {
		journalID, err := s.encryptionJournalIDForItem(id)
		if err != nil {
			return DocumentResponse{}, err
		}
		var ok bool
		key, ok = s.journalKey(journalID)
		if !ok {
			return DocumentResponse{}, ErrEncryptionLocked
		}
		if !rawItem.EncryptionKeyID.Valid {
			return DocumentResponse{}, fmt.Errorf("encrypted document key is missing")
		}
		keyID = rawItem.EncryptionKeyID.String
		if err := s.db.QueryRow(
			`SELECT schema_version, content_ciphertext, spacing_preset FROM documents WHERE item_id = ?`,
			id,
		).Scan(&schemaVersion, &contentCiphertext, &spacingPreset); err != nil {
			return DocumentResponse{}, err
		}
		plaintext, err := openField(key, "documents", id, "content_json", keyID, contentCiphertext)
		if err != nil {
			return DocumentResponse{}, err
		}
		encoded = string(plaintext)
	} else {
		if err := s.db.QueryRow(
			`SELECT schema_version, content_json, spacing_preset FROM documents WHERE item_id = ?`,
			id,
		).Scan(&schemaVersion, &encoded, &spacingPreset); err != nil {
			return DocumentResponse{}, err
		}
	}
	if draft, ok := s.pendingDraftSnapshot(id); ok {
		data, err := json.Marshal(draft.Content)
		if err != nil {
			return DocumentResponse{}, err
		}
		encoded = string(data)
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(encoded), &content); err != nil {
		return DocumentResponse{}, err
	}

	storedTitle := title
	contentJSON := encoded
	var titleCiphertext []byte
	encryptionState := rawItem.EncryptionState
	if encryptionState == "" {
		encryptionState = EncryptionPlaintext
	}
	encryptionKeyID := sql.NullString{}
	if encryptionState == EncryptionEncrypted {
		encryptionKeyID = sql.NullString{String: keyID, Valid: true}
		titleCiphertext, err = sealField(key, "items", newID, "title", keyID, []byte(title))
		if err != nil {
			return DocumentResponse{}, err
		}
		contentCiphertext, err = sealField(key, "documents", newID, "content_json", keyID, []byte(encoded))
		if err != nil {
			return DocumentResponse{}, err
		}
		storedTitle = encryptedTitlePlaceholder(KindDocument)
		placeholder, _ := json.Marshal(emptyDocument())
		contentJSON = string(placeholder)
	} else {
		contentCiphertext = nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return DocumentResponse{}, err
	}
	defer rollback(tx)

	if _, err := tx.Exec(
		`INSERT INTO items (id, parent_id, kind, title, sort_order, created_at, updated_at, encryption_state, encryption_key_id, title_ciphertext)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newID, parentID, KindDocument, storedTitle, order, now, now, encryptionState, encryptionKeyID, titleCiphertext,
	); err != nil {
		return DocumentResponse{}, err
	}
	if _, err := tx.Exec(
		`INSERT INTO documents (item_id, schema_version, content_json, content_ciphertext, spacing_preset, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		newID, schemaVersion, contentJSON, contentCiphertext, normalizeSpacingPreset(spacingPreset), now, now,
	); err != nil {
		return DocumentResponse{}, err
	}
	attachmentIDMap, err := s.copyDocumentAttachmentsForDuplicateTx(tx, id, newID, encryptionState == EncryptionEncrypted, key, keyID)
	if err != nil {
		return DocumentResponse{}, err
	}
	if len(attachmentIDMap) > 0 {
		encodedContent, err := json.Marshal(remapAttachmentIDsInContent(content, attachmentIDMap))
		if err != nil {
			return DocumentResponse{}, err
		}
		if encryptionState == EncryptionEncrypted {
			contentCiphertext, err = sealField(key, "documents", newID, "content_json", keyID, encodedContent)
			if err != nil {
				return DocumentResponse{}, err
			}
			if _, err := tx.Exec(`UPDATE documents SET content_ciphertext = ? WHERE item_id = ?`, contentCiphertext, newID); err != nil {
				return DocumentResponse{}, err
			}
		} else if _, err := tx.Exec(`UPDATE documents SET content_json = ? WHERE item_id = ?`, string(encodedContent), newID); err != nil {
			return DocumentResponse{}, err
		}
	}
	if err := s.syncFTSTx(tx, newID); err != nil {
		return DocumentResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return DocumentResponse{}, err
	}
	return s.OpenDocument(newID)
}

func (s *JournalService) OpenDocument(id string) (DocumentResponse, error) {
	rawItem, err := s.getRawRowItemFrom(s.db, id)
	if err != nil {
		return DocumentResponse{}, err
	}
	if rawItem.Kind != KindDocument {
		return DocumentResponse{}, fmt.Errorf("item is not a document")
	}

	var encoded string
	var schemaVersion int
	var spacingPreset string
	if rawItem.EncryptionState == EncryptionEncrypted {
		journalID, err := s.encryptionJournalIDForItem(id)
		if err != nil {
			return DocumentResponse{}, err
		}
		key, ok := s.journalKey(journalID)
		if !ok {
			return DocumentResponse{}, ErrEncryptionLocked
		}
		if !rawItem.EncryptionKeyID.Valid {
			return DocumentResponse{}, fmt.Errorf("encrypted document key is missing")
		}
		var contentCiphertext []byte
		err = s.db.QueryRow(
			`SELECT schema_version, content_ciphertext, spacing_preset FROM documents WHERE item_id = ?`,
			id,
		).Scan(&schemaVersion, &contentCiphertext, &spacingPreset)
		if err != nil {
			return DocumentResponse{}, err
		}
		plaintext, err := openField(key, "documents", id, "content_json", rawItem.EncryptionKeyID.String, contentCiphertext)
		if err != nil {
			return DocumentResponse{}, err
		}
		encoded = string(plaintext)
	} else {
		err = s.db.QueryRow(
			`SELECT schema_version, content_json, spacing_preset FROM documents WHERE item_id = ?`,
			id,
		).Scan(&schemaVersion, &encoded, &spacingPreset)
		if err != nil {
			return DocumentResponse{}, err
		}
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(encoded), &content); err != nil {
		return DocumentResponse{}, err
	}
	item, err := s.prepareItemForDisplay(rawItem)
	if err != nil {
		return DocumentResponse{}, err
	}
	tree, err := s.GetLibraryTree()
	if err != nil {
		return DocumentResponse{}, err
	}
	if err := s.rememberLastDocument(id); err != nil {
		return DocumentResponse{}, err
	}
	return DocumentResponse{
		ID:            id,
		Title:         item.Title,
		Content:       content,
		SpacingPreset: normalizeSpacingPreset(spacingPreset),
		SchemaVersion: schemaVersion,
		CreatedAt:     item.CreatedAt,
		UpdatedAt:     item.UpdatedAt,
		Item:          treeItemFromRow(item),
		Tree:          tree,
		SaveState:     "saved",
	}, nil
}

func (s *JournalService) UpdateDocumentDraft(id string, content map[string]any, version int64) (DocumentDraftResponse, error) {
	if version < 1 {
		return DocumentDraftResponse{}, fmt.Errorf("draft version must be positive")
	}
	if err := validateProseMirrorDoc(content); err != nil {
		return DocumentDraftResponse{}, err
	}
	item, err := s.getRowItem(id)
	if err != nil {
		return DocumentDraftResponse{}, err
	}
	if item.Kind != KindDocument {
		return DocumentDraftResponse{}, fmt.Errorf("item is not a document")
	}
	s.mu.Lock()
	if version <= s.lastDraftVersion[id] {
		currentVersion := s.lastDraftVersion[id]
		s.mu.Unlock()
		return DocumentDraftResponse{ID: id, SaveState: "dirty", Version: currentVersion}, nil
	}
	s.pending[id] = pendingDraft{Content: cloneMap(content), UpdatedAt: time.Now(), Version: version}
	s.lastDraftVersion[id] = version
	s.mu.Unlock()
	return DocumentDraftResponse{ID: id, SaveState: "dirty", Version: version}, nil
}

func (s *JournalService) UpdateDocumentSpacing(id string, spacingPreset string) (DocumentSaveResponse, error) {
	spacingPreset = normalizeSpacingPreset(spacingPreset)
	item, err := s.getRowItem(id)
	if err != nil {
		return DocumentSaveResponse{}, err
	}
	if item.Kind != KindDocument {
		return DocumentSaveResponse{}, fmt.Errorf("item is not a document")
	}
	now := nowString()
	tx, err := s.db.Begin()
	if err != nil {
		return DocumentSaveResponse{}, err
	}
	defer rollback(tx)
	if _, err := tx.Exec(`UPDATE documents SET spacing_preset = ?, updated_at = ? WHERE item_id = ?`, spacingPreset, now, id); err != nil {
		return DocumentSaveResponse{}, err
	}
	if _, err := tx.Exec(`UPDATE items SET updated_at = ? WHERE id = ?`, now, id); err != nil {
		return DocumentSaveResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return DocumentSaveResponse{}, err
	}
	s.mu.Lock()
	version := s.lastDraftVersion[id]
	s.mu.Unlock()
	return DocumentSaveResponse{ID: id, SaveState: "saved", SavedAt: now, UpdatedAt: now, Version: version}, nil
}

func (s *JournalService) FlushDocument(id string) (DocumentSaveResponse, error) {
	s.mu.Lock()
	draft, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if !ok {
		item, err := s.getRowItem(id)
		if err != nil {
			return DocumentSaveResponse{}, err
		}
		s.mu.Lock()
		version := s.lastDraftVersion[id]
		s.mu.Unlock()
		return DocumentSaveResponse{ID: id, SaveState: "saved", SavedAt: nowString(), UpdatedAt: item.UpdatedAt, Version: version}, nil
	}
	updatedAt, err := s.saveDocumentContent(id, draft.Content)
	if err != nil {
		s.mu.Lock()
		current, exists := s.pending[id]
		if !exists || current.Version < draft.Version {
			s.pending[id] = draft
		}
		if s.lastDraftVersion[id] < draft.Version {
			s.lastDraftVersion[id] = draft.Version
		}
		s.mu.Unlock()
		return DocumentSaveResponse{}, err
	}
	s.mu.Lock()
	if s.lastDraftVersion[id] < draft.Version {
		s.lastDraftVersion[id] = draft.Version
	}
	s.mu.Unlock()
	return DocumentSaveResponse{ID: id, SaveState: "saved", SavedAt: updatedAt, UpdatedAt: updatedAt, Version: draft.Version}, nil
}

func (s *JournalService) FlushAll() error {
	ids := s.pendingIDsOlderThan(0)
	for _, id := range ids {
		if _, err := s.FlushDocument(id); err != nil {
			return err
		}
	}
	return nil
}

func (s *JournalService) saveDocumentContent(id string, content map[string]any) (string, error) {
	if err := validateProseMirrorDoc(content); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	item, err := s.getRawRowItemFrom(s.db, id)
	if err != nil {
		return "", err
	}
	contentJSON := string(encoded)
	var contentCiphertext []byte
	if item.EncryptionState == EncryptionEncrypted {
		journalID, err := s.encryptionJournalIDForItem(id)
		if err != nil {
			return "", err
		}
		key, ok := s.journalKey(journalID)
		if !ok {
			return "", ErrEncryptionLocked
		}
		if !item.EncryptionKeyID.Valid {
			return "", fmt.Errorf("encrypted document key is missing")
		}
		contentCiphertext, err = sealField(key, "documents", id, "content_json", item.EncryptionKeyID.String, encoded)
		if err != nil {
			return "", err
		}
		placeholder, _ := json.Marshal(emptyDocument())
		contentJSON = string(placeholder)
	}
	now := nowString()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer rollback(tx)
	if item.EncryptionState == EncryptionEncrypted {
		if _, err := tx.Exec(
			`UPDATE documents SET content_json = ?, content_ciphertext = ?, updated_at = ? WHERE item_id = ?`,
			contentJSON, contentCiphertext, now, id,
		); err != nil {
			return "", err
		}
	} else {
		if _, err := tx.Exec(
			`UPDATE documents SET content_json = ?, content_ciphertext = NULL, updated_at = ? WHERE item_id = ?`,
			contentJSON, now, id,
		); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(`UPDATE items SET updated_at = ? WHERE id = ?`, now, id); err != nil {
		return "", err
	}
	if err := s.reconcileDocumentAttachmentsTx(tx, id, content); err != nil {
		return "", err
	}
	if err := s.syncFTSTx(tx, id); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return now, nil
}
