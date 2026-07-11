package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

func (s *JournalService) blobCacheDirectory() (string, error) {
	if s.StoreKind() != StoreKindCloud || s.repository == nil {
		return "", fmt.Errorf("attachment blob cache is available only for cloud stores")
	}
	return filepath.Join(filepath.Dir(s.repository.path), "blobs", "sha256"), nil
}

func (s *JournalService) blobCachePath(digest string) (string, error) {
	if err := validateSHA256Digest(digest); err != nil {
		return "", err
	}
	directory, err := s.blobCacheDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, strings.TrimPrefix(digest, "sha256:")), nil
}

// PutVerifiedBlob streams bytes to a private temporary file, validates digest
// and size, then atomically installs the content-addressed cache entry.
func (s *JournalService) PutVerifiedBlob(digest string, size int64, source io.Reader) error {
	if source == nil || size < 0 {
		return fmt.Errorf("blob source and size are required")
	}
	path, err := s.blobCachePath(digest)
	if err != nil {
		return err
	}
	if existing, err := s.readCachedBlob(digest, size); err == nil && len(existing) > 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".tmp-" + uuid.NewString()
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporary) }()
	written, err := io.Copy(file, io.LimitReader(source, size+1))
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if written != size {
		return fmt.Errorf("blob size mismatch")
	}
	computed, actualSize, err := digestFile(temporary)
	if err != nil {
		return err
	}
	if actualSize != size || computed != digest {
		return fmt.Errorf("blob digest mismatch")
	}
	if err := os.Rename(temporary, path); err != nil {
		if _, readErr := s.readCachedBlob(digest, size); readErr == nil {
			return nil
		}
		return err
	}
	return nil
}

func (s *JournalService) readCachedBlob(digest string, size int64) ([]byte, error) {
	path, err := s.blobCachePath(digest)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size || digestBytes(data) != digest {
		return nil, fmt.Errorf("cached blob digest mismatch")
	}
	return data, nil
}

func (s *JournalService) ensureBlobPresent(digest string, size int64) error {
	if _, err := s.readCachedBlob(digest, size); err == nil {
		return nil
	}
	rows, err := s.db.Query(`SELECT content_blob, content_ciphertext FROM document_attachments WHERE stored_digest = ?`, digest)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var blob, ciphertext []byte
		if err := rows.Scan(&blob, &ciphertext); err != nil {
			return err
		}
		stored := blob
		if len(ciphertext) > 0 {
			stored = ciphertext
		}
		if len(stored) > 0 {
			return s.PutVerifiedBlob(digest, size, bytes.NewReader(stored))
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return fmt.Errorf("attachment blob is not available locally")
}

// EnsureAttachmentLocal materializes an attachment's stored-byte form into the
// cloud blob cache. Phase 3 will supply missing blobs from the Vault; Phase 2
// deliberately never treats a missing local blob as recoverable from a remote
// path by itself.
func (s *JournalService) EnsureAttachmentLocal(attachmentID string) (AttachmentDescriptor, error) {
	attachmentID = strings.TrimSpace(attachmentID)
	if attachmentID == "" {
		return AttachmentDescriptor{}, fmt.Errorf("attachment id is required")
	}
	if s.StoreKind() != StoreKindCloud {
		return AttachmentDescriptor{}, fmt.Errorf("attachment blob cache is available only for cloud stores")
	}
	if _, err := s.db.Exec(`UPDATE document_attachments SET stored_digest = NULL, stored_size = NULL WHERE id = ? AND (stored_digest IS NULL OR stored_size IS NULL)`, attachmentID); err != nil {
		return AttachmentDescriptor{}, err
	}
	descriptors, err := s.AttachmentDescriptors()
	if err != nil {
		return AttachmentDescriptor{}, err
	}
	for _, descriptor := range descriptors {
		if err := s.ensureBlobPresent(descriptor.Digest, descriptor.Size); err != nil {
			return AttachmentDescriptor{}, err
		}
	}
	var digest string
	if err := s.db.QueryRow(`SELECT stored_digest FROM document_attachments WHERE id = ?`, attachmentID).Scan(&digest); err != nil {
		return AttachmentDescriptor{}, err
	}
	for _, descriptor := range descriptors {
		if descriptor.Digest == digest {
			return descriptor, nil
		}
	}
	return AttachmentDescriptor{}, fmt.Errorf("attachment is detached or unavailable")
}
