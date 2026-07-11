package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/chacha20poly1305"
)

const cloudPortableEncryptionFormatVersion = 1

var ErrPortableEncryptionUnavailable = fmt.Errorf("portable_encryption_unavailable")

type portableCloudEncryptionRecord struct {
	CloudJournalID       string
	FormatVersion        int
	KeyID                string
	KDF                  string
	KDFParamsJSON        string
	Salt                 []byte
	WrappedKeyNonce      []byte
	WrappedKeyCiphertext []byte
	VerifierNonce        []byte
	VerifierCiphertext   []byte
}

// InitializeCloudJournalEncryption configures a fresh cloud Journal with a
// portable data key. Conversion of pre-existing plaintext cloud content is
// deliberately rejected here; it requires the explicit verified conversion
// workflow that accompanies remote publication in Phase 3.
func (s *JournalService) InitializeCloudJournalEncryption(password string) error {
	if s.StoreKind() != StoreKindCloud {
		return ErrPortableEncryptionUnavailable
	}
	password = strings.TrimSpace(password)
	if password == "" {
		return fmt.Errorf("master password cannot be empty")
	}
	cloudJournalID, err := s.cloudJournalID()
	if err != nil {
		return err
	}
	if _, err := s.loadPortableCloudEncryption(); err == nil {
		return fmt.Errorf("cloud Journal encryption is already configured")
	} else if err != sql.ErrNoRows {
		return err
	}
	params := defaultKDFParams()
	salt, err := randomBytes(16)
	if err != nil {
		return err
	}
	wrappingKey, err := deriveMasterKey(password, salt, params)
	if err != nil {
		return err
	}
	defer zeroBytes(wrappingKey)
	dataKey, err := randomBytes(chacha20poly1305.KeySize)
	if err != nil {
		return err
	}
	keyID := uuid.NewString()
	wrappedNonce, wrappedKey, err := sealDetached(wrappingKey, dataKey, journalKeyAD(cloudJournalID, keyID))
	if err != nil {
		return err
	}
	verifierNonce, verifier, err := sealDetached(wrappingKey, []byte(masterVerifierPayload), cloudVerifierAD(cloudJournalID))
	if err != nil {
		return err
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return err
	}
	now := nowString()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err := tx.Exec(`INSERT INTO cloud_portable_encryption
		(cloud_journal_id, format_version, key_id, kdf, kdf_params_json, salt, wrapped_key_nonce, wrapped_key_ciphertext, verifier_nonce, verifier_ciphertext, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, cloudJournalID, cloudPortableEncryptionFormatVersion, keyID, masterKDF, string(paramsJSON), salt, wrappedNonce, wrappedKey, verifierNonce, verifier, now, now); err != nil {
		return err
	}
	if err := s.encryptCloudJournalContentTx(tx, cloudJournalID, dataKey, keyID, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE items SET encryption_state = ?, encryption_key_id = ?, updated_at = ? WHERE id = ?`, EncryptionEncrypted, keyID, now, cloudJournalID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.rememberJournalKey(cloudJournalID, dataKey)
	zeroBytes(dataKey)
	return s.markCloudCacheDirty()
}

func (s *JournalService) encryptCloudJournalContentTx(tx *sql.Tx, journalID string, dataKey []byte, keyID string, now string) error {
	descendants, err := descendantIDs(tx, journalID)
	if err != nil {
		return err
	}
	for _, id := range descendants {
		item, err := s.getRawRowItemFrom(tx, id)
		if err != nil {
			return err
		}
		if item.EncryptionState == EncryptionEncrypted {
			return fmt.Errorf("cloud Journal contains mixed encryption state")
		}
		titleCiphertext, err := sealField(dataKey, "items", item.ID, "title", keyID, []byte(item.Title))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE items SET title = ?, title_ciphertext = ?, encryption_state = ?, encryption_key_id = ?, updated_at = ? WHERE id = ?`,
			encryptedTitlePlaceholder(item.Kind), titleCiphertext, EncryptionEncrypted, keyID, now, item.ID); err != nil {
			return err
		}
		if item.Kind != KindDocument {
			continue
		}
		var encoded string
		if err := tx.QueryRow(`SELECT content_json FROM documents WHERE item_id = ?`, item.ID).Scan(&encoded); err != nil {
			return err
		}
		contentCiphertext, err := sealField(dataKey, "documents", item.ID, "content_json", keyID, []byte(encoded))
		if err != nil {
			return err
		}
		placeholder, _ := json.Marshal(emptyDocument())
		if _, err := tx.Exec(`UPDATE documents SET content_json = ?, content_ciphertext = ?, updated_at = ? WHERE item_id = ?`, string(placeholder), contentCiphertext, now, item.ID); err != nil {
			return err
		}
		rows, err := tx.Query(`SELECT id, content_blob FROM document_attachments WHERE document_id = ?`, item.ID)
		if err != nil {
			return err
		}
		var attachments []struct {
			id   string
			data []byte
		}
		for rows.Next() {
			var attachment struct {
				id   string
				data []byte
			}
			if err := rows.Scan(&attachment.id, &attachment.data); err != nil {
				_ = rows.Close()
				return err
			}
			if len(attachment.data) == 0 {
				_ = rows.Close()
				return fmt.Errorf("attachment %s is not available for cloud encryption conversion", attachment.id)
			}
			attachments = append(attachments, attachment)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, attachment := range attachments {
			ciphertext, err := sealField(dataKey, "document_attachments", attachment.id, "content_blob", keyID, attachment.data)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE document_attachments SET content_blob = NULL, content_ciphertext = ?, stored_digest = ?, stored_size = ? WHERE id = ?`, ciphertext, digestBytes(ciphertext), len(ciphertext), attachment.id); err != nil {
				return err
			}
		}
	}
	return s.deleteFTSDescendantsTx(tx, journalID)
}

func (s *JournalService) UnlockCloudJournal(password string) error {
	if s.StoreKind() != StoreKindCloud {
		return ErrPortableEncryptionUnavailable
	}
	record, err := s.loadPortableCloudEncryption()
	if err != nil {
		if err == sql.ErrNoRows {
			return ErrPortableEncryptionUnavailable
		}
		return err
	}
	wrappingKey, err := derivePortableCloudWrappingKey(password, record)
	if err != nil {
		return err
	}
	defer zeroBytes(wrappingKey)
	verifier, err := openDetached(wrappingKey, record.VerifierNonce, record.VerifierCiphertext, cloudVerifierAD(record.CloudJournalID))
	if err != nil || string(verifier) != masterVerifierPayload {
		return ErrInvalidMasterPassword
	}
	dataKey, err := openDetached(wrappingKey, record.WrappedKeyNonce, record.WrappedKeyCiphertext, journalKeyAD(record.CloudJournalID, record.KeyID))
	if err != nil {
		return ErrInvalidMasterPassword
	}
	s.rememberJournalKey(record.CloudJournalID, dataKey)
	zeroBytes(dataKey)
	return nil
}

func (s *JournalService) ChangeCloudJournalMasterPassword(currentPassword, newPassword string) error {
	if s.StoreKind() != StoreKindCloud {
		return ErrPortableEncryptionUnavailable
	}
	newPassword = strings.TrimSpace(newPassword)
	if newPassword == "" {
		return fmt.Errorf("new master password cannot be empty")
	}
	record, err := s.loadPortableCloudEncryption()
	if err != nil {
		return err
	}
	oldKey, err := derivePortableCloudWrappingKey(currentPassword, record)
	if err != nil {
		return err
	}
	defer zeroBytes(oldKey)
	if verifier, err := openDetached(oldKey, record.VerifierNonce, record.VerifierCiphertext, cloudVerifierAD(record.CloudJournalID)); err != nil || string(verifier) != masterVerifierPayload {
		return ErrInvalidMasterPassword
	}
	dataKey, err := openDetached(oldKey, record.WrappedKeyNonce, record.WrappedKeyCiphertext, journalKeyAD(record.CloudJournalID, record.KeyID))
	if err != nil {
		return ErrInvalidMasterPassword
	}
	defer zeroBytes(dataKey)
	params := defaultKDFParams()
	salt, err := randomBytes(16)
	if err != nil {
		return err
	}
	newKey, err := deriveMasterKey(newPassword, salt, params)
	if err != nil {
		return err
	}
	defer zeroBytes(newKey)
	wrappedNonce, wrappedKey, err := sealDetached(newKey, dataKey, journalKeyAD(record.CloudJournalID, record.KeyID))
	if err != nil {
		return err
	}
	verifierNonce, verifier, err := sealDetached(newKey, []byte(masterVerifierPayload), cloudVerifierAD(record.CloudJournalID))
	if err != nil {
		return err
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE cloud_portable_encryption SET kdf = ?, kdf_params_json = ?, salt = ?, wrapped_key_nonce = ?, wrapped_key_ciphertext = ?, verifier_nonce = ?, verifier_ciphertext = ?, updated_at = ? WHERE cloud_journal_id = ?`,
		masterKDF, string(paramsJSON), salt, wrappedNonce, wrappedKey, verifierNonce, verifier, nowString(), record.CloudJournalID); err != nil {
		return err
	}
	s.rememberJournalKey(record.CloudJournalID, dataKey)
	return s.markCloudCacheDirty()
}

func (s *JournalService) cloudJournalID() (string, error) {
	var id string
	if err := s.db.QueryRow(`SELECT cloud_journal_id FROM cloud_journal_metadata`).Scan(&id); err != nil {
		return "", err
	}
	return id, nil
}

func (s *JournalService) loadPortableCloudEncryption() (portableCloudEncryptionRecord, error) {
	var record portableCloudEncryptionRecord
	err := s.db.QueryRow(`SELECT cloud_journal_id, format_version, key_id, kdf, kdf_params_json, salt, wrapped_key_nonce, wrapped_key_ciphertext, verifier_nonce, verifier_ciphertext FROM cloud_portable_encryption`).Scan(
		&record.CloudJournalID, &record.FormatVersion, &record.KeyID, &record.KDF, &record.KDFParamsJSON, &record.Salt, &record.WrappedKeyNonce, &record.WrappedKeyCiphertext, &record.VerifierNonce, &record.VerifierCiphertext)
	if err != nil {
		return portableCloudEncryptionRecord{}, err
	}
	if record.FormatVersion != cloudPortableEncryptionFormatVersion || record.KDF != masterKDF || strings.TrimSpace(record.KeyID) == "" {
		return portableCloudEncryptionRecord{}, fmt.Errorf("portable cloud encryption metadata is malformed")
	}
	if _, err := parseKDFParams(record.KDFParamsJSON); err != nil || len(record.Salt) < 16 || len(record.WrappedKeyNonce) != chacha20poly1305.NonceSizeX || len(record.VerifierNonce) != chacha20poly1305.NonceSizeX {
		return portableCloudEncryptionRecord{}, fmt.Errorf("portable cloud encryption metadata is malformed")
	}
	return record, nil
}

func derivePortableCloudWrappingKey(password string, record portableCloudEncryptionRecord) ([]byte, error) {
	password = strings.TrimSpace(password)
	if password == "" {
		return nil, ErrInvalidMasterPassword
	}
	params, err := parseKDFParams(record.KDFParamsJSON)
	if err != nil {
		return nil, err
	}
	return deriveMasterKey(password, record.Salt, params)
}

func cloudVerifierAD(cloudJournalID string) []byte {
	return []byte("journal:v2:cloud-verifier:" + cloudJournalID)
}

func (s *JournalService) markCloudCacheDirty() error {
	if s.StoreKind() != StoreKindCloud || s.repository == nil {
		return nil
	}
	statePath := filepath.Join(filepath.Dir(s.repository.path), "vault-state.json")
	return os.WriteFile(statePath, []byte(`{"cacheDirty":true}`), 0o600)
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
