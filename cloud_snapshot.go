package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const maxSnapshotDescriptorBytes int64 = 1 << 40

type DatabaseDescriptor struct {
	Key    string `json:"key"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type AttachmentDescriptor struct {
	Digest   string `json:"digest"`
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
	Key      string `json:"key"`
}

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateSHA256Digest(value string) error {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return fmt.Errorf("invalid SHA-256 digest")
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err
}

func blobKeyForDigest(digest string) (string, error) {
	if err := validateSHA256Digest(digest); err != nil {
		return "", err
	}
	return "blobs/sha256/" + strings.TrimPrefix(digest, "sha256:"), nil
}

func (s *JournalService) AttachmentDescriptors() ([]AttachmentDescriptor, error) {
	rows, err := s.db.Query(`SELECT id, mime_type, content_blob, content_ciphertext, stored_digest, stored_size
		FROM document_attachments WHERE detached_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var descriptors []AttachmentDescriptor
	for rows.Next() {
		var id, mimeType string
		var contentBlob, contentCiphertext []byte
		var digest sql.NullString
		var storedSize sql.NullInt64
		if err := rows.Scan(&id, &mimeType, &contentBlob, &contentCiphertext, &digest, &storedSize); err != nil {
			return nil, err
		}
		stored := contentBlob
		if len(contentCiphertext) > 0 {
			stored = contentCiphertext
		}
		if len(stored) == 0 {
			if !digest.Valid || !storedSize.Valid {
				return nil, fmt.Errorf("attachment %s has no portable stored bytes", id)
			}
			stored, err = s.readCachedBlob(digest.String, storedSize.Int64)
			if err != nil {
				return nil, err
			}
		}
		computed := digestBytes(stored)
		if digest.Valid && (digest.String != computed || !storedSize.Valid || storedSize.Int64 != int64(len(stored))) {
			return nil, fmt.Errorf("attachment %s stored digest mismatch", id)
		}
		if !digest.Valid || !storedSize.Valid {
			if _, err := s.db.Exec(`UPDATE document_attachments SET stored_digest = ?, stored_size = ? WHERE id = ?`, computed, len(stored), id); err != nil {
				return nil, err
			}
		}
		key, err := blobKeyForDigest(computed)
		if err != nil {
			return nil, err
		}
		if seen[computed] {
			continue
		}
		seen[computed] = true
		descriptors = append(descriptors, AttachmentDescriptor{Digest: computed, Size: int64(len(stored)), MimeType: mimeType, Key: key})
	}
	return descriptors, rows.Err()
}

// SnapshotContentDatabase creates a consistent staging database. For cloud
// stores it removes attachment payload columns after verified blob-cache
// materialization, so revisions reference blobs instead of embedding them.
func (s *JournalService) SnapshotContentDatabase(ctx context.Context, stagingPath string) (DatabaseDescriptor, error) {
	if err := ctx.Err(); err != nil {
		return DatabaseDescriptor{}, err
	}
	if err := s.FlushAll(); err != nil {
		return DatabaseDescriptor{}, err
	}
	stagingPath = strings.TrimSpace(stagingPath)
	if stagingPath == "" {
		return DatabaseDescriptor{}, fmt.Errorf("snapshot staging path is required")
	}
	if err := os.MkdirAll(filepath.Dir(stagingPath), 0o700); err != nil {
		return DatabaseDescriptor{}, err
	}
	if _, err := os.Stat(stagingPath); err == nil {
		return DatabaseDescriptor{}, fmt.Errorf("snapshot staging file already exists")
	} else if !os.IsNotExist(err) {
		return DatabaseDescriptor{}, err
	}
	if s.StoreKind() == StoreKindCloud {
		descriptors, err := s.AttachmentDescriptors()
		if err != nil {
			return DatabaseDescriptor{}, err
		}
		for _, descriptor := range descriptors {
			if err := s.ensureBlobPresent(descriptor.Digest, descriptor.Size); err != nil {
				return DatabaseDescriptor{}, err
			}
		}
	}
	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(FULL)`); err != nil {
		return DatabaseDescriptor{}, err
	}
	escaped := strings.ReplaceAll(stagingPath, "'", "''")
	if _, err := s.db.Exec(`VACUUM INTO '` + escaped + `'`); err != nil {
		return DatabaseDescriptor{}, err
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(stagingPath)
		return DatabaseDescriptor{}, err
	}
	if err := validateSnapshotDatabase(stagingPath, s.StoreKind(), s.StoreID()); err != nil {
		_ = os.Remove(stagingPath)
		return DatabaseDescriptor{}, err
	}
	if s.StoreKind() == StoreKindCloud {
		if err := stripSnapshotAttachmentPayloads(stagingPath); err != nil {
			_ = os.Remove(stagingPath)
			return DatabaseDescriptor{}, err
		}
		if err := validateSnapshotDatabase(stagingPath, StoreKindCloud, s.StoreID()); err != nil {
			_ = os.Remove(stagingPath)
			return DatabaseDescriptor{}, err
		}
	}
	digest, size, err := digestFile(stagingPath)
	if err != nil {
		_ = os.Remove(stagingPath)
		return DatabaseDescriptor{}, err
	}
	return DatabaseDescriptor{Digest: digest, Size: size}, nil
}

func validateSnapshotDatabase(path string, kind StoreKind, storeID StoreID) error {
	store, err := openSQLiteJournalStore(path, storeID, kind)
	if err != nil {
		return err
	}
	defer store.Close()
	var result string
	if err := store.Database().QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("snapshot integrity check failed: %s", result)
	}
	if kind == StoreKindCloud {
		service := newJournalService(store)
		cloudJournalID := strings.TrimPrefix(string(storeID), "cloud:")
		return service.ValidateCloudJournalScope(cloudJournalID)
	}
	return nil
}

func stripSnapshotAttachmentPayloads(path string) error {
	store, err := openSQLiteJournalStore(path, "snapshot", StoreKindCloud)
	if err != nil {
		return err
	}
	defer store.Close()
	if _, err := store.Database().Exec(`UPDATE document_attachments SET content_blob = NULL, content_ciphertext = NULL`); err != nil {
		return err
	}
	_, err = store.Database().Exec(`VACUUM`)
	return err
}

func digestFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	if size < 0 || size > maxSnapshotDescriptorBytes {
		return "", 0, fmt.Errorf("snapshot size is invalid")
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), size, nil
}

func ValidateDatabaseDescriptor(descriptor DatabaseDescriptor, cloudJournalID string) error {
	if err := validateSHA256Digest(descriptor.Digest); err != nil {
		return err
	}
	if descriptor.Size < 0 || descriptor.Size > maxSnapshotDescriptorBytes {
		return fmt.Errorf("invalid database descriptor size")
	}
	if descriptor.Key != "" && !strings.HasPrefix(descriptor.Key, "revisions/") {
		return fmt.Errorf("database descriptor key is invalid for cloud Journal %s", cloudJournalID)
	}
	return nil
}

func ValidateAttachmentDescriptors(cloudJournalID string, descriptors []AttachmentDescriptor) error {
	if err := validateCloudJournalID(cloudJournalID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, descriptor := range descriptors {
		if err := validateSHA256Digest(descriptor.Digest); err != nil {
			return err
		}
		if descriptor.Size < 0 || strings.TrimSpace(descriptor.MimeType) == "" || seen[descriptor.Digest] {
			return fmt.Errorf("invalid or duplicate attachment descriptor")
		}
		key, err := blobKeyForDigest(descriptor.Digest)
		if err != nil || descriptor.Key != key {
			return fmt.Errorf("attachment descriptor key is invalid")
		}
		seen[descriptor.Digest] = true
	}
	return nil
}

func newSnapshotPath(dir string) string { return filepath.Join(dir, "journal-"+uuid.NewString()+".db") }
