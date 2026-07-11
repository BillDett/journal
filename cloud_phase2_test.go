package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9WlQZcoAAAAASUVORK5CYII="

func TestPortableCloudEncryptionRecoversFromSnapshot(t *testing.T) {
	_, sourceManager := newCloudCacheTestManager(t)
	cloudJournalID := uuid.NewString()
	source, err := sourceManager.CreateCloudCache(cloudJournalID)
	if err != nil {
		t.Fatalf("create source cloud cache: %v", err)
	}
	if err := source.CreateMasterPassword("not-portable"); !errors.Is(err, ErrPortableEncryptionUnavailable) {
		t.Fatalf("cloud master-password command should be rejected, got %v", err)
	}
	if err := source.InitializeCloudJournalEncryption("correct password"); err != nil {
		t.Fatalf("initialize portable cloud encryption: %v", err)
	}
	document, err := source.CreateDocument(cloudJournalID)
	if err != nil {
		t.Fatalf("create encrypted cloud document: %v", err)
	}
	content := map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "portable secret"}}}}}
	if _, err := source.UpdateDocumentDraft(document.ID, content, 1); err != nil {
		t.Fatalf("save encrypted cloud draft: %v", err)
	}
	if _, err := source.FlushDocument(document.ID); err != nil {
		t.Fatalf("flush encrypted cloud draft: %v", err)
	}
	snapshotDir := t.TempDir()
	snapshotPath := filepath.Join(snapshotDir, cloudCacheDatabaseName)
	descriptor, err := source.SnapshotContentDatabase(context.Background(), snapshotPath)
	if err != nil {
		t.Fatalf("snapshot encrypted cloud cache: %v", err)
	}
	if err := ValidateDatabaseDescriptor(descriptor, cloudJournalID); err != nil {
		t.Fatalf("validate snapshot descriptor: %v", err)
	}

	_, destinationManager := newCloudCacheTestManager(t)
	destination, err := destinationManager.CreateCloudCache(cloudJournalID)
	if err != nil {
		t.Fatalf("create destination cache: %v", err)
	}
	if err := destinationManager.router.Unregister(destination.StoreID()); err != nil {
		t.Fatalf("close destination cache: %v", err)
	}
	if err := destinationManager.StageCacheReplacement(cloudJournalID, snapshotDir); err != nil {
		t.Fatalf("install portable snapshot: %v", err)
	}
	recovered, err := destinationManager.OpenCloudCache(cloudJournalID)
	if err != nil {
		t.Fatalf("open recovered cloud cache: %v", err)
	}
	if err := recovered.UnlockCloudJournal("wrong password"); !errors.Is(err, ErrInvalidMasterPassword) {
		t.Fatalf("wrong portable password should fail clearly, got %v", err)
	}
	if err := recovered.UnlockCloudJournal("correct password"); err != nil {
		t.Fatalf("unlock recovered cloud Journal: %v", err)
	}
	opened, err := recovered.OpenDocument(document.ID)
	if err != nil {
		t.Fatalf("open recovered encrypted document: %v", err)
	}
	if extractText(opened.Content) != "portable secret" {
		t.Fatalf("unexpected recovered document: %#v", opened.Content)
	}
	if err := recovered.ChangeCloudJournalMasterPassword("correct password", "new password"); err != nil {
		t.Fatalf("rewrap portable key: %v", err)
	}
	if err := recovered.UnlockCloudJournal("correct password"); !errors.Is(err, ErrInvalidMasterPassword) {
		t.Fatalf("old password should fail after rewrap, got %v", err)
	}
	if err := recovered.UnlockCloudJournal("new password"); err != nil {
		t.Fatalf("new password should unlock after rewrap: %v", err)
	}
}

func TestPortableCloudEncryptionConvertsExistingCloudContent(t *testing.T) {
	_, manager := newCloudCacheTestManager(t)
	cloudJournalID := uuid.NewString()
	cache, err := manager.CreateCloudCache(cloudJournalID)
	if err != nil {
		t.Fatalf("create cloud cache: %v", err)
	}
	document, err := cache.CreateDocument(cloudJournalID)
	if err != nil {
		t.Fatalf("create plaintext cloud document: %v", err)
	}
	content := map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "convert me"}}}}}
	if _, err := cache.UpdateDocumentDraft(document.ID, content, 1); err != nil {
		t.Fatalf("write plaintext draft: %v", err)
	}
	if _, err := cache.FlushDocument(document.ID); err != nil {
		t.Fatalf("flush plaintext draft: %v", err)
	}
	if err := cache.InitializeCloudJournalEncryption("convert password"); err != nil {
		t.Fatalf("convert cloud Journal to portable encryption: %v", err)
	}
	var plaintext string
	if err := cache.db.QueryRow(`SELECT content_json FROM documents WHERE item_id = ?`, document.ID).Scan(&plaintext); err != nil {
		t.Fatalf("read encrypted document storage: %v", err)
	}
	if plaintext == "" || plaintext == "convert me" {
		t.Fatalf("cloud conversion left plaintext content in database: %q", plaintext)
	}
	opened, err := cache.OpenDocument(document.ID)
	if err != nil {
		t.Fatalf("open converted cloud document: %v", err)
	}
	if extractText(opened.Content) != "convert me" {
		t.Fatalf("unexpected converted document content: %#v", opened.Content)
	}
}

func TestCloudAttachmentBlobCacheAndSnapshotStripPayloads(t *testing.T) {
	_, manager := newCloudCacheTestManager(t)
	cloudJournalID := uuid.NewString()
	cache, err := manager.CreateCloudCache(cloudJournalID)
	if err != nil {
		t.Fatalf("create cloud cache: %v", err)
	}
	document, err := cache.CreateDocument(cloudJournalID)
	if err != nil {
		t.Fatalf("create cloud document: %v", err)
	}
	attachment, err := cache.CreateDocumentAttachment(document.ID, "pixel.png", "image/png", tinyPNGBase64)
	if err != nil {
		t.Fatalf("create cloud attachment: %v", err)
	}
	descriptor, err := cache.EnsureAttachmentLocal(attachment.ID)
	if err != nil {
		t.Fatalf("materialize cloud attachment blob: %v", err)
	}
	if err := ValidateAttachmentDescriptors(cloudJournalID, []AttachmentDescriptor{descriptor}); err != nil {
		t.Fatalf("validate attachment descriptor: %v", err)
	}
	blobPath, err := cache.blobCachePath(descriptor.Digest)
	if err != nil {
		t.Fatalf("blob path: %v", err)
	}
	if _, err := os.Stat(blobPath); err != nil {
		t.Fatalf("expected local blob cache entry: %v", err)
	}
	snapshotDir := t.TempDir()
	snapshotPath := newSnapshotPath(snapshotDir)
	if _, err := cache.SnapshotContentDatabase(context.Background(), snapshotPath); err != nil {
		t.Fatalf("snapshot cloud cache: %v", err)
	}
	db, err := sql.Open("sqlite", snapshotPath)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer db.Close()
	var blobLength, cipherLength sql.NullInt64
	if err := db.QueryRow(`SELECT length(content_blob), length(content_ciphertext) FROM document_attachments WHERE id = ?`, attachment.ID).Scan(&blobLength, &cipherLength); err != nil {
		t.Fatalf("inspect snapshot attachment payload: %v", err)
	}
	if blobLength.Valid || cipherLength.Valid {
		t.Fatalf("snapshot must not embed attachment payloads: %v/%v", blobLength, cipherLength)
	}

	if _, err := cache.db.Exec(`UPDATE document_attachments SET content_blob = NULL, content_ciphertext = NULL WHERE id = ?`, attachment.ID); err != nil {
		t.Fatalf("clear live attachment payload: %v", err)
	}
	dataURL, err := cache.GetDocumentAttachmentDataURL(attachment.ID)
	if err != nil {
		t.Fatalf("read attachment from blob cache: %v", err)
	}
	if dataURL.DataURL != "data:image/png;base64,"+tinyPNGBase64 {
		t.Fatalf("unexpected blob-cache data URL: %s", dataURL.DataURL)
	}
	if err := cache.PutVerifiedBlob(descriptor.Digest, descriptor.Size-1, bytes.NewReader([]byte("short"))); err == nil {
		t.Fatal("partial blob source must be rejected")
	}
	if err := manager.router.Unregister(cache.StoreID()); err != nil {
		t.Fatalf("close cache before restart: %v", err)
	}
	reopened, err := manager.OpenCloudCache(cloudJournalID)
	if err != nil {
		t.Fatalf("reopen cloud cache: %v", err)
	}
	if _, err := reopened.GetDocumentAttachmentDataURL(attachment.ID); err != nil {
		t.Fatalf("blob cache should survive restart: %v", err)
	}
}

func TestAttachmentDescriptorValidationRejectsMalformedValues(t *testing.T) {
	cloudJournalID := uuid.NewString()
	if err := ValidateAttachmentDescriptors(cloudJournalID, []AttachmentDescriptor{{Digest: "sha256:not-a-digest", Size: 1, MimeType: "image/png", Key: "blobs/sha256/not-a-digest"}}); err == nil {
		t.Fatal("malformed digest should be rejected")
	}
	digest := digestBytes([]byte("same"))
	key, err := blobKeyForDigest(digest)
	if err != nil {
		t.Fatalf("blob key: %v", err)
	}
	if err := ValidateAttachmentDescriptors(cloudJournalID, []AttachmentDescriptor{{Digest: digest, Size: 4, MimeType: "image/png", Key: key}, {Digest: digest, Size: 4, MimeType: "image/png", Key: key}}); err == nil {
		t.Fatal("duplicate attachment digest should be rejected")
	}
	if err := ValidateDatabaseDescriptor(DatabaseDescriptor{Digest: digest, Size: -1}, cloudJournalID); err == nil {
		t.Fatal("negative database descriptor size should be rejected")
	}
}

func TestSnapshotFailureLeavesLiveCloudCacheUntouched(t *testing.T) {
	_, manager := newCloudCacheTestManager(t)
	cloudJournalID := uuid.NewString()
	cache, err := manager.CreateCloudCache(cloudJournalID)
	if err != nil {
		t.Fatalf("create cloud cache: %v", err)
	}
	_, err = cache.SnapshotContentDatabase(context.Background(), filepath.Join(t.TempDir(), "missing", "snapshot.db"))
	if err != nil {
		t.Fatalf("nested staging directory should be created: %v", err)
	}
	// A second attempt at the same staging path fails before touching the source.
	staging := filepath.Join(t.TempDir(), "snapshot.db")
	if err := os.WriteFile(staging, []byte("already exists"), 0o600); err != nil {
		t.Fatalf("prepare conflicting staging file: %v", err)
	}
	if _, err := cache.SnapshotContentDatabase(context.Background(), staging); err == nil {
		t.Fatal("existing staging path should fail")
	}
	tree, err := cache.GetLibraryTree()
	if err != nil {
		t.Fatalf("read live cache after snapshot failure: %v", err)
	}
	if len(tree.Items) == 0 {
		t.Fatal("live cache must remain readable after snapshot failure")
	}
}
