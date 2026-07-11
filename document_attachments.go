package main

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Document image attachment validation, storage, copying, and garbage collection.

func (s *JournalService) CreateDocumentAttachmentFromPath(documentID string, path string) (DocumentAttachmentResponse, error) {
	info, err := os.Stat(path)
	if err != nil {
		return DocumentAttachmentResponse{}, err
	}
	if info.Size() > maxImageAttachmentBytes {
		return DocumentAttachmentResponse{}, fmt.Errorf("image is larger than the 20 MB limit")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return DocumentAttachmentResponse{}, err
	}
	return s.createDocumentAttachment(documentID, filepath.Base(path), "", data)
}

func (s *JournalService) CreateDocumentAttachment(documentID string, name string, mimeType string, dataBase64 string) (DocumentAttachmentResponse, error) {
	dataBase64 = strings.TrimSpace(dataBase64)
	if comma := strings.Index(dataBase64, ","); comma >= 0 && strings.Contains(dataBase64[:comma], "base64") {
		dataBase64 = dataBase64[comma+1:]
	}
	data, err := base64.StdEncoding.DecodeString(dataBase64)
	if err != nil {
		return DocumentAttachmentResponse{}, fmt.Errorf("image data must be base64 encoded")
	}
	return s.createDocumentAttachment(documentID, name, mimeType, data)
}

func (s *JournalService) createDocumentAttachment(documentID string, name string, mimeType string, data []byte) (DocumentAttachmentResponse, error) {
	documentID = strings.TrimSpace(documentID)
	if documentID == "" {
		return DocumentAttachmentResponse{}, fmt.Errorf("document id is required")
	}
	if len(data) == 0 {
		return DocumentAttachmentResponse{}, fmt.Errorf("image is empty")
	}
	if len(data) > maxImageAttachmentBytes {
		return DocumentAttachmentResponse{}, fmt.Errorf("image is larger than the 20 MB limit")
	}
	item, err := s.getRawRowItemFrom(s.db, documentID)
	if err != nil {
		return DocumentAttachmentResponse{}, err
	}
	if item.Kind != KindDocument {
		return DocumentAttachmentResponse{}, fmt.Errorf("item is not a document")
	}
	mimeType = normalizeImageMimeType(name, mimeType, data)
	if mimeType == "" {
		return DocumentAttachmentResponse{}, fmt.Errorf("unsupported image format")
	}
	id := uuid.NewString()
	now := nowString()
	contentBlob := data
	var contentCiphertext []byte
	if item.EncryptionState == EncryptionEncrypted {
		journalID, err := s.journalIDForItem(documentID)
		if err != nil {
			return DocumentAttachmentResponse{}, err
		}
		key, ok := s.journalKey(journalID)
		if !ok {
			return DocumentAttachmentResponse{}, ErrEncryptionLocked
		}
		if !item.EncryptionKeyID.Valid {
			return DocumentAttachmentResponse{}, fmt.Errorf("encrypted document key is missing")
		}
		contentCiphertext, err = sealField(key, "document_attachments", id, "content_blob", item.EncryptionKeyID.String, data)
		if err != nil {
			return DocumentAttachmentResponse{}, err
		}
		contentBlob = nil
	}
	storedBytes := contentBlob
	if len(contentCiphertext) > 0 {
		storedBytes = contentCiphertext
	}
	storedDigest := digestBytes(storedBytes)
	storedSize := len(storedBytes)
	name = strings.TrimSpace(filepath.Base(name))
	if name == "." || name == string(filepath.Separator) {
		name = ""
	}
	if _, err := s.db.Exec(
		`INSERT INTO document_attachments (id, document_id, mime_type, original_name, size_bytes, content_blob, content_ciphertext, stored_digest, stored_size, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, documentID, mimeType, name, len(data), contentBlob, contentCiphertext, storedDigest, storedSize, now,
	); err != nil {
		return DocumentAttachmentResponse{}, err
	}
	return DocumentAttachmentResponse{ID: id, DocumentID: documentID, MimeType: mimeType, OriginalName: name, SizeBytes: len(data)}, nil
}

func (s *JournalService) GetDocumentAttachmentDataURL(attachmentID string) (DocumentAttachmentDataResponse, error) {
	attachmentID = strings.TrimSpace(attachmentID)
	if attachmentID == "" {
		return DocumentAttachmentDataResponse{}, fmt.Errorf("attachment id is required")
	}
	var documentID, mimeType string
	var contentBlob []byte
	var contentCiphertext []byte
	var storedDigest sql.NullString
	var storedSize sql.NullInt64
	if err := s.db.QueryRow(
		`SELECT document_id, mime_type, content_blob, content_ciphertext, stored_digest, stored_size FROM document_attachments WHERE id = ?`,
		attachmentID,
	).Scan(&documentID, &mimeType, &contentBlob, &contentCiphertext, &storedDigest, &storedSize); err != nil {
		return DocumentAttachmentDataResponse{}, err
	}
	item, err := s.getRawRowItemFrom(s.db, documentID)
	if err != nil {
		return DocumentAttachmentDataResponse{}, err
	}
	data := contentBlob
	if len(data) == 0 && len(contentCiphertext) == 0 && s.StoreKind() == StoreKindCloud {
		if !storedDigest.Valid || !storedSize.Valid {
			return DocumentAttachmentDataResponse{}, fmt.Errorf("attachment blob metadata is missing")
		}
		stored, err := s.readCachedBlob(storedDigest.String, storedSize.Int64)
		if err != nil {
			return DocumentAttachmentDataResponse{}, err
		}
		if item.EncryptionState == EncryptionEncrypted {
			contentCiphertext = stored
		} else {
			data = stored
		}
	}
	if item.EncryptionState == EncryptionEncrypted {
		journalID, err := s.journalIDForItem(documentID)
		if err != nil {
			return DocumentAttachmentDataResponse{}, err
		}
		key, ok := s.journalKey(journalID)
		if !ok {
			return DocumentAttachmentDataResponse{}, ErrEncryptionLocked
		}
		if !item.EncryptionKeyID.Valid {
			return DocumentAttachmentDataResponse{}, fmt.Errorf("encrypted document key is missing")
		}
		data, err = openField(key, "document_attachments", attachmentID, "content_blob", item.EncryptionKeyID.String, contentCiphertext)
		if err != nil {
			return DocumentAttachmentDataResponse{}, err
		}
	}
	return DocumentAttachmentDataResponse{
		ID:      attachmentID,
		DataURL: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data),
	}, nil
}

func normalizeImageMimeType(name string, mimeType string, data []byte) string {
	candidates := []string{strings.ToLower(strings.TrimSpace(mimeType))}
	if len(data) > 0 {
		candidates = append(candidates, strings.ToLower(http.DetectContentType(data)))
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		candidates = append(candidates, "image/png")
	case ".jpg", ".jpeg":
		candidates = append(candidates, "image/jpeg")
	case ".gif":
		candidates = append(candidates, "image/gif")
	case ".webp":
		candidates = append(candidates, "image/webp")
	}
	for _, candidate := range candidates {
		switch candidate {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
			return candidate
		}
	}
	return ""
}

func attachmentIDsFromContent(content map[string]any) map[string]bool {
	ids := map[string]bool{}
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if typed["type"] == "attachmentImage" {
				if attrs, ok := typed["attrs"].(map[string]any); ok {
					if id, ok := attrs["attachmentId"].(string); ok && strings.TrimSpace(id) != "" {
						ids[strings.TrimSpace(id)] = true
					}
				}
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(content)
	return ids
}

func remapAttachmentIDsInContent(content map[string]any, idMap map[string]string) map[string]any {
	if len(idMap) == 0 {
		return cloneMap(content)
	}
	cloned := cloneMap(content)
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if typed["type"] == "attachmentImage" {
				if attrs, ok := typed["attrs"].(map[string]any); ok {
					if id, ok := attrs["attachmentId"].(string); ok {
						if replacement, ok := idMap[id]; ok {
							attrs["attachmentId"] = replacement
						}
					}
				}
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(cloned)
	return cloned
}

func (s *JournalService) reconcileDocumentAttachmentsTx(tx *sql.Tx, documentID string, content map[string]any) error {
	referenced := attachmentIDsFromContent(content)
	rows, err := tx.Query(`SELECT id FROM document_attachments WHERE document_id = ?`, documentID)
	if err != nil {
		return err
	}
	var detached []string
	var attached []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		if referenced[id] {
			attached = append(attached, id)
		} else {
			detached = append(detached, id)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range attached {
		if _, err := tx.Exec(`UPDATE document_attachments SET detached_at = NULL WHERE id = ?`, id); err != nil {
			return err
		}
	}
	now := nowString()
	for _, id := range detached {
		if _, err := tx.Exec(`UPDATE document_attachments SET detached_at = COALESCE(detached_at, ?) WHERE id = ?`, now, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *JournalService) PurgeDetachedAttachments(gracePeriod time.Duration) error {
	if gracePeriod < 0 {
		gracePeriod = 0
	}
	cutoff := time.Now().UTC().Add(-gracePeriod).Format(time.RFC3339Nano)
	_, err := s.db.Exec(`DELETE FROM document_attachments WHERE detached_at IS NOT NULL AND detached_at <= ?`, cutoff)
	return err
}

func (s *JournalService) copyDocumentAttachmentsTx(tx *sql.Tx, sourceDocumentID string, targetDocumentID string, key []byte, keyID string) (map[string]string, error) {
	rows, err := tx.Query(
		`SELECT id, mime_type, original_name, size_bytes, content_blob, content_ciphertext
		 FROM document_attachments WHERE document_id = ? AND detached_at IS NULL`,
		sourceDocumentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	idMap := map[string]string{}
	now := nowString()
	for rows.Next() {
		var oldID, mimeType, originalName string
		var sizeBytes int
		var contentBlob []byte
		var contentCiphertext []byte
		if err := rows.Scan(&oldID, &mimeType, &originalName, &sizeBytes, &contentBlob, &contentCiphertext); err != nil {
			return nil, err
		}
		newID := uuid.NewString()
		idMap[oldID] = newID
		if key != nil {
			plaintext, err := openField(key, "document_attachments", oldID, "content_blob", keyID, contentCiphertext)
			if err != nil {
				return nil, err
			}
			contentBlob = plaintext
			contentCiphertext = nil
			sizeBytes = len(plaintext)
		}
		if _, err := tx.Exec(
			`INSERT INTO document_attachments (id, document_id, mime_type, original_name, size_bytes, content_blob, content_ciphertext, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			newID, targetDocumentID, mimeType, originalName, sizeBytes, contentBlob, contentCiphertext, now,
		); err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return idMap, nil
}

func (s *JournalService) copyDocumentAttachmentsForDuplicateTx(tx *sql.Tx, sourceDocumentID string, targetDocumentID string, encrypted bool, key []byte, keyID string) (map[string]string, error) {
	rows, err := tx.Query(
		`SELECT id, mime_type, original_name, size_bytes, content_blob, content_ciphertext
		 FROM document_attachments WHERE document_id = ? AND detached_at IS NULL`,
		sourceDocumentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	idMap := map[string]string{}
	now := nowString()
	for rows.Next() {
		var oldID, mimeType, originalName string
		var sizeBytes int
		var contentBlob []byte
		var contentCiphertext []byte
		if err := rows.Scan(&oldID, &mimeType, &originalName, &sizeBytes, &contentBlob, &contentCiphertext); err != nil {
			return nil, err
		}
		newID := uuid.NewString()
		idMap[oldID] = newID
		if encrypted {
			plaintext, err := openField(key, "document_attachments", oldID, "content_blob", keyID, contentCiphertext)
			if err != nil {
				return nil, err
			}
			contentCiphertext, err = sealField(key, "document_attachments", newID, "content_blob", keyID, plaintext)
			if err != nil {
				return nil, err
			}
			contentBlob = nil
			sizeBytes = len(plaintext)
		} else {
			contentCiphertext = nil
		}
		if _, err := tx.Exec(
			`INSERT INTO document_attachments (id, document_id, mime_type, original_name, size_bytes, content_blob, content_ciphertext, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			newID, targetDocumentID, mimeType, originalName, sizeBytes, contentBlob, contentCiphertext, now,
		); err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return idMap, nil
}
