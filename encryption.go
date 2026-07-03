package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"
)

const (
	EncryptionPlaintext = "plaintext"
	EncryptionEncrypted = "encrypted"

	masterKDF      = "scrypt"
	masterKeyBytes = chacha20poly1305.KeySize

	masterVerifierPayload = "journal-encryption-master-v1"
)

var (
	masterKDFN = 32768
	masterKDFR = 8
	masterKDFP = 1
)

var (
	ErrMasterPasswordRequired = errors.New("master password is required")
	ErrEncryptionLocked       = errors.New("encrypted journals are locked")
	ErrInvalidMasterPassword  = errors.New("invalid master password")
)

type EncryptionStatusResponse struct {
	MasterPasswordConfigured bool     `json:"masterPasswordConfigured"`
	Unlocked                 bool     `json:"unlocked"`
	EncryptedJournalIDs      []string `json:"encryptedJournalIds"`
}

type kdfParams struct {
	N      int `json:"n"`
	R      int `json:"r"`
	P      int `json:"p"`
	KeyLen int `json:"keyLen"`
}

type masterRecord struct {
	KDF                string
	KDFParamsJSON      string
	Salt               []byte
	VerifierNonce      []byte
	VerifierCiphertext []byte
}

type wrappedJournalKey struct {
	KeyID      string
	JournalID  string
	Nonce      []byte
	Ciphertext []byte
}

func (s *JournalService) GetEncryptionStatus() (EncryptionStatusResponse, error) {
	configured, err := s.masterPasswordConfigured()
	if err != nil {
		return EncryptionStatusResponse{}, err
	}
	ids, err := s.encryptedJournalIDs()
	if err != nil {
		return EncryptionStatusResponse{}, err
	}
	s.cryptoMu.Lock()
	unlocked := len(s.masterKey) > 0
	s.cryptoMu.Unlock()
	return EncryptionStatusResponse{
		MasterPasswordConfigured: configured,
		Unlocked:                 unlocked,
		EncryptedJournalIDs:      ids,
	}, nil
}

func (s *JournalService) CreateMasterPassword(password string) error {
	password = strings.TrimSpace(password)
	if password == "" {
		return fmt.Errorf("master password cannot be empty")
	}
	configured, err := s.masterPasswordConfigured()
	if err != nil {
		return err
	}
	if configured {
		return fmt.Errorf("master password is already configured")
	}
	params := defaultKDFParams()
	salt, err := randomBytes(16)
	if err != nil {
		return err
	}
	key, err := deriveMasterKey(password, salt, params)
	if err != nil {
		return err
	}
	nonce, ciphertext, err := sealDetached(key, []byte(masterVerifierPayload), []byte("journal:v1:master-verifier"))
	if err != nil {
		return err
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return err
	}
	now := nowString()
	if _, err := s.db.Exec(
		`INSERT INTO encryption_master (id, kdf, kdf_params_json, salt, verifier_nonce, verifier_ciphertext, created_at, updated_at)
		 VALUES (1, ?, ?, ?, ?, ?, ?, ?)`,
		masterKDF, string(paramsJSON), salt, nonce, ciphertext, now, now,
	); err != nil {
		return err
	}
	s.cryptoMu.Lock()
	s.masterKey = cloneBytes(key)
	s.journalKeys = map[string][]byte{}
	s.cryptoMu.Unlock()
	return nil
}

func (s *JournalService) UnlockEncryption(password string) error {
	key, err := s.verifyMasterPassword(password)
	if err != nil {
		return err
	}
	wrapped, err := s.loadWrappedJournalKeys(s.db)
	if err != nil {
		return err
	}
	journalKeys := map[string][]byte{}
	for _, row := range wrapped {
		dataKey, err := openDetached(key, row.Nonce, row.Ciphertext, journalKeyAD(row.JournalID, row.KeyID))
		if err != nil {
			return ErrInvalidMasterPassword
		}
		journalKeys[row.JournalID] = dataKey
	}
	s.cryptoMu.Lock()
	s.masterKey = cloneBytes(key)
	s.journalKeys = journalKeys
	s.cryptoMu.Unlock()
	return nil
}

func (s *JournalService) ChangeMasterPassword(currentPassword string, newPassword string) error {
	newPassword = strings.TrimSpace(newPassword)
	if newPassword == "" {
		return fmt.Errorf("new master password cannot be empty")
	}
	oldKey, err := s.verifyMasterPassword(currentPassword)
	if err != nil {
		return err
	}
	wrapped, err := s.loadWrappedJournalKeys(s.db)
	if err != nil {
		return err
	}
	unwrapped := map[string][]byte{}
	for _, row := range wrapped {
		dataKey, err := openDetached(oldKey, row.Nonce, row.Ciphertext, journalKeyAD(row.JournalID, row.KeyID))
		if err != nil {
			return fmt.Errorf("encrypted journal key could not be verified")
		}
		unwrapped[row.KeyID] = dataKey
	}

	params := defaultKDFParams()
	salt, err := randomBytes(16)
	if err != nil {
		return err
	}
	newKey, err := deriveMasterKey(newPassword, salt, params)
	if err != nil {
		return err
	}
	verifierNonce, verifierCiphertext, err := sealDetached(newKey, []byte(masterVerifierPayload), []byte("journal:v1:master-verifier"))
	if err != nil {
		return err
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer rollback(tx)

	now := nowString()
	if _, err := tx.Exec(
		`UPDATE encryption_master
		 SET kdf = ?, kdf_params_json = ?, salt = ?, verifier_nonce = ?, verifier_ciphertext = ?, updated_at = ?
		 WHERE id = 1`,
		masterKDF, string(paramsJSON), salt, verifierNonce, verifierCiphertext, now,
	); err != nil {
		return err
	}
	for _, row := range wrapped {
		dataKey := unwrapped[row.KeyID]
		nonce, ciphertext, err := sealDetached(newKey, dataKey, journalKeyAD(row.JournalID, row.KeyID))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`UPDATE journal_encryption_keys
			 SET wrapped_key_nonce = ?, wrapped_key_ciphertext = ?, updated_at = ?
			 WHERE key_id = ?`,
			nonce, ciphertext, now, row.KeyID,
		); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.cryptoMu.Lock()
	s.masterKey = nil
	s.journalKeys = map[string][]byte{}
	s.cryptoMu.Unlock()
	if last := s.settingValue(settingLastDocumentID); last != "" {
		if encrypted, _ := s.itemIsEncrypted(last); encrypted {
			_ = s.rememberLastDocument("")
		}
	}
	return nil
}

func (s *JournalService) EncryptJournal(journalID string) (TreeResponse, error) {
	journalID = strings.TrimSpace(journalID)
	configured, err := s.masterPasswordConfigured()
	if err != nil {
		return TreeResponse{}, err
	}
	if !configured {
		return TreeResponse{}, ErrMasterPasswordRequired
	}
	masterKey, ok := s.masterKeySnapshot()
	if !ok {
		return TreeResponse{}, ErrEncryptionLocked
	}
	if err := s.FlushAll(); err != nil {
		return TreeResponse{}, err
	}
	journal, err := s.getRawRowItemFrom(s.db, journalID)
	if err != nil {
		return TreeResponse{}, err
	}
	if journal.Kind != KindJournal || journal.ParentID.Valid {
		return TreeResponse{}, fmt.Errorf("item is not a top-level journal")
	}
	if journal.SystemKey.String == SystemTrash {
		return TreeResponse{}, fmt.Errorf("trash cannot be encrypted")
	}
	if journal.EncryptionState == EncryptionEncrypted {
		return TreeResponse{}, fmt.Errorf("journal is already encrypted")
	}
	keyID := uuid.NewString()
	dataKey, err := randomBytes(chacha20poly1305.KeySize)
	if err != nil {
		return TreeResponse{}, err
	}
	wrapNonce, wrappedKey, err := sealDetached(masterKey, dataKey, journalKeyAD(journalID, keyID))
	if err != nil {
		return TreeResponse{}, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return TreeResponse{}, err
	}
	defer rollback(tx)

	now := nowString()
	if _, err := tx.Exec(
		`INSERT INTO journal_encryption_keys (key_id, journal_id, wrapped_key_nonce, wrapped_key_ciphertext, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		keyID, journalID, wrapNonce, wrappedKey, now, now,
	); err != nil {
		return TreeResponse{}, err
	}
	descendants, err := descendantIDs(tx, journalID)
	if err != nil {
		return TreeResponse{}, err
	}
	for _, id := range descendants {
		item, err := s.getRawRowItemFrom(tx, id)
		if err != nil {
			return TreeResponse{}, err
		}
		titleCiphertext, err := sealField(dataKey, "items", item.ID, "title", keyID, []byte(item.Title))
		if err != nil {
			return TreeResponse{}, err
		}
		if _, err := tx.Exec(
			`UPDATE items
			 SET title = ?, title_ciphertext = ?, encryption_state = ?, encryption_key_id = ?, updated_at = ?
			 WHERE id = ?`,
			encryptedTitlePlaceholder(item.Kind), titleCiphertext, EncryptionEncrypted, keyID, now, item.ID,
		); err != nil {
			return TreeResponse{}, err
		}
		if item.Kind == KindDocument {
			var encoded string
			if err := tx.QueryRow(`SELECT content_json FROM documents WHERE item_id = ?`, item.ID).Scan(&encoded); err != nil {
				return TreeResponse{}, err
			}
			contentCiphertext, err := sealField(dataKey, "documents", item.ID, "content_json", keyID, []byte(encoded))
			if err != nil {
				return TreeResponse{}, err
			}
			placeholder, _ := json.Marshal(emptyDocument())
			if _, err := tx.Exec(
				`UPDATE documents
				 SET content_json = ?, content_ciphertext = ?, updated_at = ?
				 WHERE item_id = ?`,
				string(placeholder), contentCiphertext, now, item.ID,
			); err != nil {
				return TreeResponse{}, err
			}
			attachmentRows, err := tx.Query(`SELECT id, content_blob FROM document_attachments WHERE document_id = ?`, item.ID)
			if err != nil {
				return TreeResponse{}, err
			}
			var attachments []struct {
				id   string
				data []byte
			}
			for attachmentRows.Next() {
				var attachment struct {
					id   string
					data []byte
				}
				if err := attachmentRows.Scan(&attachment.id, &attachment.data); err != nil {
					_ = attachmentRows.Close()
					return TreeResponse{}, err
				}
				attachments = append(attachments, attachment)
			}
			if err := attachmentRows.Close(); err != nil {
				return TreeResponse{}, err
			}
			if err := attachmentRows.Err(); err != nil {
				return TreeResponse{}, err
			}
			for _, attachment := range attachments {
				attachmentCiphertext, err := sealField(dataKey, "document_attachments", attachment.id, "content_blob", keyID, attachment.data)
				if err != nil {
					return TreeResponse{}, err
				}
				if _, err := tx.Exec(
					`UPDATE document_attachments SET content_blob = NULL, content_ciphertext = ? WHERE id = ?`,
					attachmentCiphertext, attachment.id,
				); err != nil {
					return TreeResponse{}, err
				}
			}
		}
	}
	if err := s.deleteFTSDescendantsTx(tx, journalID); err != nil {
		return TreeResponse{}, err
	}
	if _, err := tx.Exec(
		`UPDATE items
		 SET encryption_state = ?, encryption_key_id = ?, updated_at = ?
		 WHERE id = ?`,
		EncryptionEncrypted, keyID, now, journalID,
	); err != nil {
		return TreeResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return TreeResponse{}, err
	}
	s.rememberJournalKey(journalID, dataKey)
	_ = s.checkpointAndVacuum()
	return s.GetLibraryTree()
}

func (s *JournalService) DecryptJournal(journalID string) (TreeResponse, error) {
	journalID = strings.TrimSpace(journalID)
	if err := s.FlushAll(); err != nil {
		return TreeResponse{}, err
	}
	journal, err := s.getRawRowItemFrom(s.db, journalID)
	if err != nil {
		return TreeResponse{}, err
	}
	if journal.Kind != KindJournal || journal.ParentID.Valid {
		return TreeResponse{}, fmt.Errorf("item is not a top-level journal")
	}
	if journal.EncryptionState != EncryptionEncrypted {
		return TreeResponse{}, fmt.Errorf("journal is not encrypted")
	}
	key, ok := s.journalKey(journalID)
	if !ok {
		return TreeResponse{}, ErrEncryptionLocked
	}
	if !journal.EncryptionKeyID.Valid {
		return TreeResponse{}, fmt.Errorf("encrypted journal key is missing")
	}
	lastDocumentID := s.settingValue(settingLastDocumentID)

	tx, err := s.db.Begin()
	if err != nil {
		return TreeResponse{}, err
	}
	defer rollback(tx)

	now := nowString()
	newJournalID := uuid.NewString()
	if _, err := tx.Exec(
		`INSERT INTO items (id, parent_id, kind, title, sort_order, system_key, created_at, updated_at, encryption_state)
		 VALUES (?, NULL, ?, ?, ?, NULL, ?, ?, ?)`,
		newJournalID, KindJournal, journal.Title, journal.SortOrder, journal.CreatedAt, now, EncryptionPlaintext,
	); err != nil {
		return TreeResponse{}, err
	}
	if err := s.syncFTSTx(tx, newJournalID); err != nil {
		return TreeResponse{}, err
	}
	idMap := map[string]string{journalID: newJournalID}
	if err := s.copyEncryptedChildrenToPlaintextTx(tx, journalID, newJournalID, key, journal.EncryptionKeyID.String, now, idMap); err != nil {
		return TreeResponse{}, err
	}
	if err := s.deleteFTSDescendantsTx(tx, journalID); err != nil {
		return TreeResponse{}, err
	}
	if _, err := tx.Exec(`DELETE FROM items WHERE id = ?`, journalID); err != nil {
		return TreeResponse{}, err
	}
	if lastDocumentID != "" {
		if replacementID, ok := idMap[lastDocumentID]; ok {
			if _, err := tx.Exec(
				`INSERT INTO app_settings (key, value, updated_at)
				 VALUES (?, ?, ?)
				 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
				settingLastDocumentID, replacementID, now,
			); err != nil {
				return TreeResponse{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return TreeResponse{}, err
	}
	s.removePendingIDs(mapKeys(idMap))
	s.cryptoMu.Lock()
	delete(s.journalKeys, journalID)
	s.cryptoMu.Unlock()
	_ = s.checkpointAndVacuum()
	return s.GetLibraryTree()
}

func (s *JournalService) copyEncryptedChildrenToPlaintextTx(tx *sql.Tx, sourceParentID string, targetParentID string, key []byte, keyID string, now string, idMap map[string]string) error {
	rows, err := tx.Query(`SELECT id FROM items WHERE parent_id = ? ORDER BY sort_order, title COLLATE NOCASE`, sourceParentID)
	if err != nil {
		return err
	}
	var childIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		childIDs = append(childIDs, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, childID := range childIDs {
		source, err := s.getRawRowItemFrom(tx, childID)
		if err != nil {
			return err
		}
		titleBytes, err := openField(key, "items", source.ID, "title", keyID, source.TitleCiphertext)
		if err != nil {
			return err
		}
		newID := uuid.NewString()
		idMap[source.ID] = newID
		if _, err := tx.Exec(
			`INSERT INTO items (id, parent_id, kind, title, sort_order, system_key, created_at, updated_at, encryption_state)
			 VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?)`,
			newID, targetParentID, source.Kind, string(titleBytes), source.SortOrder, source.CreatedAt, source.UpdatedAt, EncryptionPlaintext,
		); err != nil {
			return err
		}
		if source.Kind == KindDocument {
			var schemaVersion int
			var spacingPreset string
			var contentCiphertext []byte
			if err := tx.QueryRow(
				`SELECT schema_version, spacing_preset, content_ciphertext FROM documents WHERE item_id = ?`,
				source.ID,
			).Scan(&schemaVersion, &spacingPreset, &contentCiphertext); err != nil {
				return err
			}
			encoded, err := openField(key, "documents", source.ID, "content_json", keyID, contentCiphertext)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(
				`INSERT INTO documents (item_id, schema_version, content_json, spacing_preset, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?)`,
				newID, schemaVersion, string(encoded), normalizeSpacingPreset(spacingPreset), source.CreatedAt, source.UpdatedAt,
			); err != nil {
				return err
			}
			attachmentIDMap, err := s.copyDocumentAttachmentsTx(tx, source.ID, newID, key, keyID)
			if err != nil {
				return err
			}
			if len(attachmentIDMap) > 0 {
				var content map[string]any
				if err := json.Unmarshal(encoded, &content); err != nil {
					return err
				}
				encodedContent, err := json.Marshal(remapAttachmentIDsInContent(content, attachmentIDMap))
				if err != nil {
					return err
				}
				if _, err := tx.Exec(`UPDATE documents SET content_json = ? WHERE item_id = ?`, string(encodedContent), newID); err != nil {
					return err
				}
			}
			if err := s.syncFTSTx(tx, newID); err != nil {
				return err
			}
		} else if err := s.syncFTSTx(tx, newID); err != nil {
			return err
		}
		if source.Kind == KindFolder {
			if err := s.copyEncryptedChildrenToPlaintextTx(tx, source.ID, newID, key, keyID, now, idMap); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *JournalService) encryptionContextForParent(parentID string) (string, string, []byte, bool, error) {
	journalID, err := s.journalIDForItem(parentID)
	if err != nil {
		return "", "", nil, false, err
	}
	if journalID == "" {
		return "", "", nil, false, nil
	}
	journal, err := s.getRawRowItemFrom(s.db, journalID)
	if err != nil {
		return "", "", nil, false, err
	}
	if journal.EncryptionState != EncryptionEncrypted {
		return journalID, "", nil, false, nil
	}
	if !journal.EncryptionKeyID.Valid {
		return "", "", nil, false, fmt.Errorf("encrypted journal key is missing")
	}
	key, ok := s.journalKey(journalID)
	if !ok {
		return "", "", nil, true, ErrEncryptionLocked
	}
	return journalID, journal.EncryptionKeyID.String, key, true, nil
}

func (s *JournalService) encryptionBoundaryKeyID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", nil
	}
	item, err := s.getRawRowItemFrom(s.db, id)
	if err != nil {
		return "", err
	}
	if item.EncryptionState == EncryptionEncrypted && item.EncryptionKeyID.Valid {
		return item.EncryptionKeyID.String, nil
	}
	journalID, err := s.journalIDForItem(id)
	if err != nil {
		return "", err
	}
	if journalID == "" {
		return "", nil
	}
	journal, err := s.getRawRowItemFrom(s.db, journalID)
	if err != nil {
		return "", err
	}
	if journal.EncryptionState == EncryptionEncrypted && journal.EncryptionKeyID.Valid {
		return journal.EncryptionKeyID.String, nil
	}
	return "", nil
}

func (s *JournalService) verifyMasterPassword(password string) ([]byte, error) {
	password = strings.TrimSpace(password)
	if password == "" {
		return nil, ErrInvalidMasterPassword
	}
	record, err := s.loadMasterRecord()
	if err != nil {
		return nil, err
	}
	params, err := parseKDFParams(record.KDFParamsJSON)
	if err != nil {
		return nil, err
	}
	key, err := deriveMasterKey(password, record.Salt, params)
	if err != nil {
		return nil, err
	}
	plaintext, err := openDetached(key, record.VerifierNonce, record.VerifierCiphertext, []byte("journal:v1:master-verifier"))
	if err != nil || string(plaintext) != masterVerifierPayload {
		return nil, ErrInvalidMasterPassword
	}
	return key, nil
}

func (s *JournalService) masterPasswordConfigured() (bool, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM encryption_master WHERE id = 1`).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *JournalService) loadMasterRecord() (masterRecord, error) {
	var record masterRecord
	err := s.db.QueryRow(
		`SELECT kdf, kdf_params_json, salt, verifier_nonce, verifier_ciphertext
		 FROM encryption_master WHERE id = 1`,
	).Scan(&record.KDF, &record.KDFParamsJSON, &record.Salt, &record.VerifierNonce, &record.VerifierCiphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return masterRecord{}, ErrMasterPasswordRequired
	}
	if err != nil {
		return masterRecord{}, err
	}
	if record.KDF != masterKDF {
		return masterRecord{}, fmt.Errorf("unsupported encryption KDF %q", record.KDF)
	}
	return record, nil
}

func (s *JournalService) loadWrappedJournalKeys(db queryer) ([]wrappedJournalKey, error) {
	rows, err := db.Query(
		`SELECT key_id, journal_id, wrapped_key_nonce, wrapped_key_ciphertext
		 FROM journal_encryption_keys
		 ORDER BY created_at, key_id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []wrappedJournalKey
	for rows.Next() {
		var key wrappedJournalKey
		if err := rows.Scan(&key.KeyID, &key.JournalID, &key.Nonce, &key.Ciphertext); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *JournalService) encryptedJournalIDs() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT id FROM items
		 WHERE kind = ? AND parent_id IS NULL AND encryption_state = ?
		 ORDER BY sort_order, title COLLATE NOCASE`,
		KindJournal, EncryptionEncrypted,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *JournalService) itemIsEncrypted(id string) (bool, error) {
	item, err := s.getRawRowItemFrom(s.db, id)
	if err != nil {
		return false, err
	}
	return item.EncryptionState == EncryptionEncrypted, nil
}

func (s *JournalService) journalKey(journalID string) ([]byte, bool) {
	s.cryptoMu.Lock()
	defer s.cryptoMu.Unlock()
	key, ok := s.journalKeys[journalID]
	if !ok {
		return nil, false
	}
	return cloneBytes(key), true
}

func (s *JournalService) masterKeySnapshot() ([]byte, bool) {
	s.cryptoMu.Lock()
	defer s.cryptoMu.Unlock()
	if len(s.masterKey) == 0 {
		return nil, false
	}
	return cloneBytes(s.masterKey), true
}

func (s *JournalService) rememberJournalKey(journalID string, key []byte) {
	s.cryptoMu.Lock()
	defer s.cryptoMu.Unlock()
	if s.journalKeys == nil {
		s.journalKeys = map[string][]byte{}
	}
	s.journalKeys[journalID] = cloneBytes(key)
}

func defaultKDFParams() kdfParams {
	return kdfParams{N: masterKDFN, R: masterKDFR, P: masterKDFP, KeyLen: masterKeyBytes}
}

func parseKDFParams(encoded string) (kdfParams, error) {
	var params kdfParams
	if err := json.Unmarshal([]byte(encoded), &params); err != nil {
		return kdfParams{}, err
	}
	if params.N <= 0 || params.R <= 0 || params.P <= 0 || params.KeyLen != masterKeyBytes {
		return kdfParams{}, fmt.Errorf("invalid encryption KDF parameters")
	}
	return params, nil
}

func deriveMasterKey(password string, salt []byte, params kdfParams) ([]byte, error) {
	return scrypt.Key([]byte(password), salt, params.N, params.R, params.P, params.KeyLen)
}

func randomBytes(size int) ([]byte, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return nil, err
	}
	return value, nil
}

func sealField(key []byte, table string, rowID string, column string, keyID string, plaintext []byte) ([]byte, error) {
	nonce, ciphertext, err := sealDetached(key, plaintext, fieldAD(table, rowID, column, keyID))
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(nonce)+len(ciphertext))
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out, nil
}

func openField(key []byte, table string, rowID string, column string, keyID string, sealed []byte) ([]byte, error) {
	if len(sealed) < chacha20poly1305.NonceSizeX {
		return nil, fmt.Errorf("encrypted field is malformed")
	}
	nonce := sealed[:chacha20poly1305.NonceSizeX]
	ciphertext := sealed[chacha20poly1305.NonceSizeX:]
	return openDetached(key, nonce, ciphertext, fieldAD(table, rowID, column, keyID))
}

func sealDetached(key []byte, plaintext []byte, associatedData []byte) ([]byte, []byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, nil, err
	}
	nonce, err := randomBytes(chacha20poly1305.NonceSizeX)
	if err != nil {
		return nil, nil, err
	}
	return nonce, aead.Seal(nil, nonce, plaintext, associatedData), nil
}

func openDetached(key []byte, nonce []byte, ciphertext []byte, associatedData []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != chacha20poly1305.NonceSizeX {
		return nil, fmt.Errorf("encrypted nonce has invalid length")
	}
	return aead.Open(nil, nonce, ciphertext, associatedData)
}

func fieldAD(table string, rowID string, column string, keyID string) []byte {
	return []byte("journal:v1:" + table + ":" + rowID + ":" + column + ":" + keyID)
}

func journalKeyAD(journalID string, keyID string) []byte {
	return []byte("journal:v1:journal-key:" + journalID + ":" + keyID)
}

func encryptedTitlePlaceholder(kind string) string {
	switch kind {
	case KindFolder:
		return "Encrypted Folder"
	case KindDocument:
		return "Encrypted Document"
	default:
		return "Encrypted Item"
	}
}

func mapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func (s *JournalService) checkpointAndVacuum() error {
	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return err
	}
	_, err := s.db.Exec(`VACUUM`)
	return err
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	cloned := make([]byte, len(value))
	copy(cloned, value)
	return cloned
}
