package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newVaultSyncForTest(t *testing.T, providerRoot string) (*JournalService, *VaultSyncService) {
	t.Helper()
	local := newTestService(t)
	router, err := NewJournalStoreRouter(local.store)
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	caches, err := NewCloudCacheManager(t.TempDir(), router)
	if err != nil {
		t.Fatalf("cache manager: %v", err)
	}
	device, err := NewInstallationRepository(local.db).DeviceIdentity()
	if err != nil {
		t.Fatalf("device identity: %v", err)
	}
	return local, &VaultSyncService{Store: FilesystemVaultStore{}, Provider: VaultProvider{ID: uuid.NewString(), Root: providerRoot}, Caches: caches, Mounts: NewInstallationRepository(local.db), Device: device}
}

func TestFilesystemVaultStoreConditionalAndImmutableContract(t *testing.T) {
	store := FilesystemVaultStore{}
	provider := VaultProvider{ID: "test", Root: t.TempDir()}
	ctx := context.Background()
	capabilities, err := store.Validate(ctx, provider)
	if err != nil {
		t.Fatalf("validate provider: %v", err)
	}
	if !capabilities.ImmutableWrite || !capabilities.ConditionalWrite || !capabilities.ConditionalCreate {
		t.Fatalf("missing required capabilities: %#v", capabilities)
	}
	key := "journals/" + uuid.NewString() + "/blobs/sha256/example"
	data := []byte("immutable")
	meta, err := store.PutImmutable(ctx, provider, key, bytesReader(data), digestBytes(data))
	if err != nil {
		t.Fatalf("put immutable: %v", err)
	}
	if _, err := store.PutImmutable(ctx, provider, key, bytesReader(data), digestBytes(data)); err != nil {
		t.Fatalf("idempotent immutable retry: %v", err)
	}
	if _, err := store.PutImmutable(ctx, provider, key, bytesReader([]byte("different")), digestBytes([]byte("different"))); !isVault(err, VaultAlreadyExists) {
		t.Fatalf("different immutable collision should fail, got %v", err)
	}
	control := "journals/" + uuid.NewString() + "/current.json"
	token, err := store.CreateControlIfAbsent(ctx, provider, control, []byte(`{"v":1}`))
	if err != nil {
		t.Fatalf("create control: %v", err)
	}
	if _, err := store.PutControlIf(ctx, provider, control, []byte(`{"v":2}`), "stale"); !isVault(err, VaultConflict) {
		t.Fatalf("stale conditional control update should fail, got %v", err)
	}
	if _, err := store.PutControlIf(ctx, provider, control, []byte(`{"v":2}`), token); err != nil {
		t.Fatalf("conditional control update: %v", err)
	}
	if err := store.DeleteImmutableIfVersion(ctx, provider, key, meta.Version); err != nil {
		t.Fatalf("conditional immutable delete: %v", err)
	}
}

func TestVaultSyncPublishConflictAndNewDeviceRecovery(t *testing.T) {
	providerRoot := t.TempDir()
	_, sourceSync := newVaultSyncForTest(t, providerRoot)
	ctx := context.Background()
	cache, cloudJournalID, err := sourceSync.CreateCloudJournal(ctx)
	if err != nil {
		t.Fatalf("create cloud Journal: %v", err)
	}
	document, err := cache.CreateDocument(cloudJournalID)
	if err != nil {
		t.Fatalf("create cloud document: %v", err)
	}
	content := proseMirrorDoc("published remotely")
	if _, err := cache.UpdateDocumentDraft(document.ID, content, 1); err != nil {
		t.Fatalf("update draft: %v", err)
	}
	if err := sourceSync.Publish(ctx, cache, cloudJournalID); err != nil {
		t.Fatalf("publish revision: %v", err)
	}
	mount, err := sourceSync.mount(cloudJournalID)
	if err != nil || mount.LastRevisionID == "" || mount.LastCurrentToken == "" || mount.SyncStatus != "clean" {
		t.Fatalf("unexpected mount after publish: %#v / %v", mount, err)
	}
	current, token, err := sourceSync.readCurrent(ctx, cloudJournalID)
	if err != nil {
		t.Fatalf("read current pointer: %v", err)
	}
	if current.RevisionNumber != 2 {
		t.Fatalf("expected second published revision, got %#v", current)
	}
	// Simulate a competing pointer writer. The next publish must preserve local
	// work and surface conflict before uploading a new pointer.
	current.UpdatedAt = current.UpdatedAt.Add(time.Second)
	bytes, err := canonicalVaultJSON(current)
	if err != nil {
		t.Fatalf("encode competing pointer: %v", err)
	}
	if _, err := sourceSync.Store.PutControlIf(ctx, sourceSync.Provider, mustVaultCurrent(cloudJournalID), bytes, token); err != nil {
		t.Fatalf("write competing pointer: %v", err)
	}
	if err := sourceSync.Publish(ctx, cache, cloudJournalID); err == nil || !contains(err.Error(), "current_pointer_conflict") {
		t.Fatalf("stale publish must enter conflict, got %v", err)
	}

	_, recoverySync := newVaultSyncForTest(t, providerRoot)
	recovered, err := recoverySync.ReconnectCloudJournal(ctx, cloudJournalID)
	if err != nil {
		t.Fatalf("recover on new device: %v", err)
	}
	opened, err := recovered.OpenDocument(document.ID)
	if err != nil {
		t.Fatalf("open recovered document: %v", err)
	}
	if extractText(opened.Content) != "published remotely" {
		t.Fatalf("unexpected recovered content: %#v", opened.Content)
	}
	recoveredMount, err := recoverySync.mount(cloudJournalID)
	if err != nil {
		t.Fatalf("load recovered mount: %v", err)
	}
	if recoveredMount.SyncStatus != "locked_read_only" {
		t.Fatalf("new device should respect active source lease, got %#v", recoveredMount)
	}
}

func TestVaultRevisionIDAndJournalMetadataAreReadable(t *testing.T) {
	_, sync := newVaultSyncForTest(t, t.TempDir())
	fixedNow := time.Date(2026, time.July, 11, 15, 45, 30, 123000000, time.UTC)
	sync.Now = func() time.Time { return fixedNow }
	ctx := context.Background()
	cache, cloudJournalID, err := sync.CreateCloudJournal(ctx)
	if err != nil {
		t.Fatalf("create cloud Journal: %v", err)
	}
	mount, err := sync.mount(cloudJournalID)
	if err != nil {
		t.Fatalf("read cloud mount: %v", err)
	}
	wantPrefix := "rev-20260711T154530.123Z-"
	if !strings.HasPrefix(mount.LastRevisionID, wantPrefix) || validateVaultRevisionID(mount.LastRevisionID) != nil {
		t.Fatalf("expected readable timestamp UUID revision ID, got %q", mount.LastRevisionID)
	}

	metadataBytes, _, err := sync.Store.GetControl(ctx, sync.Provider, mustVaultJournalMetadata(cloudJournalID))
	if err != nil {
		t.Fatalf("read journal metadata: %v", err)
	}
	metadata, err := parseVaultJournalMetadata(metadataBytes)
	if err != nil || metadata.DisplayName != "Cloud Journal" || metadata.CloudJournalID != cloudJournalID {
		t.Fatalf("unexpected initial journal metadata: %#v / %v", metadata, err)
	}
	if _, err := cache.RenameItem(cloudJournalID, "Project Atlas"); err != nil {
		t.Fatalf("rename cloud Journal: %v", err)
	}
	if err := sync.Publish(ctx, cache, cloudJournalID); err != nil {
		t.Fatalf("publish renamed cloud Journal: %v", err)
	}
	metadataBytes, _, err = sync.Store.GetControl(ctx, sync.Provider, mustVaultJournalMetadata(cloudJournalID))
	if err != nil {
		t.Fatalf("read renamed journal metadata: %v", err)
	}
	metadata, err = parseVaultJournalMetadata(metadataBytes)
	if err != nil || metadata.DisplayName != "Project Atlas" {
		t.Fatalf("expected renamed journal metadata, got %#v / %v", metadata, err)
	}
}

func TestVaultCodecValidationRejectsMalformedRecords(t *testing.T) {
	id := uuid.NewString()
	manifestKey, _ := vaultManifestKey(id, uuid.NewString())
	if _, err := parseVaultCurrent([]byte(`{"format":"journal-vault-current","formatVersion":99}`)); err == nil {
		t.Fatal("unknown current format version must fail")
	}
	manifest := VaultRevisionManifest{Format: vaultRevisionFormat, FormatVersion: vaultFormatVersion, CloudJournalID: id, RevisionID: uuid.NewString(), RevisionNumber: 1, CreatedAt: time.Now().UTC(), CreatedByDeviceID: uuid.NewString(), Database: VaultObjectDescriptor{Key: manifestKey, SHA256: digestBytes([]byte("db")), Size: 2}}
	// The manifest database key intentionally does not match its revision ID.
	encoded, _ := canonicalVaultJSON(manifest)
	if _, err := parseVaultManifest(encoded); err == nil {
		t.Fatal("mismatched revision/database key must fail")
	}
}

func TestVaultLeaseRenewReleaseAndForceUnlock(t *testing.T) {
	root := t.TempDir()
	_, first := newVaultSyncForTest(t, root)
	ctx := context.Background()
	_, journalID, err := first.CreateCloudJournal(ctx)
	if err != nil {
		t.Fatalf("create cloud Journal: %v", err)
	}
	mount, err := first.mount(journalID)
	if err != nil {
		t.Fatalf("load mount: %v", err)
	}
	renewed, err := first.RenewLease(ctx, journalID, mount.LeaseID)
	if err != nil || renewed.LeaseID != mount.LeaseID {
		t.Fatalf("renew lease: %#v / %v", renewed, err)
	}
	if err := first.ReleaseLease(ctx, journalID, mount.LeaseID); err != nil {
		t.Fatalf("release lease: %v", err)
	}
	_, second := newVaultSyncForTest(t, root)
	if _, err := second.acquireLease(ctx, journalID, ""); err != nil {
		t.Fatalf("acquire released lease: %v", err)
	}
	// Make the second lease old enough to force-unlock deterministically.
	data, token, err := second.Store.GetControl(ctx, second.Provider, mustVaultLease(journalID))
	if err != nil {
		t.Fatalf("read second lease: %v", err)
	}
	lease, err := parseVaultLease(data)
	if err != nil {
		t.Fatalf("parse second lease: %v", err)
	}
	lease.ExpiresAt = time.Now().UTC().Add(-vaultForceUnlockGrace - time.Second)
	expired, _ := canonicalVaultJSON(lease)
	if _, err := second.Store.PutControlIf(ctx, second.Provider, mustVaultLease(journalID), expired, token); err != nil {
		t.Fatalf("expire second lease: %v", err)
	}
	forced, err := first.ForceUnlock(ctx, journalID)
	if err != nil || forced.OwnerDeviceID != first.Device.ID {
		t.Fatalf("force unlock: %#v / %v", forced, err)
	}
}

func contains(value, fragment string) bool { return strings.Contains(value, fragment) }
