package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func newCloudCacheTestManager(t *testing.T) (*JournalService, *CloudCacheManager) {
	t.Helper()
	local := newTestService(t)
	router, err := NewJournalStoreRouter(local.store)
	if err != nil {
		t.Fatalf("create store router: %v", err)
	}
	manager, err := NewCloudCacheManager(t.TempDir(), router)
	if err != nil {
		t.Fatalf("create cloud cache manager: %v", err)
	}
	return local, manager
}

func tableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table', 'view') AND name = ?`, table).Scan(&count); err != nil {
		t.Fatalf("inspect table %s: %v", table, err)
	}
	return count != 0
}

func TestCloudCacheUsesOnlyPortableContentSchema(t *testing.T) {
	_, manager := newCloudCacheTestManager(t)
	cloudJournalID := uuid.NewString()
	cache, err := manager.CreateCloudCache(cloudJournalID)
	if err != nil {
		t.Fatalf("create cloud cache: %v", err)
	}
	if cache.StoreID() != CloudStoreID(cloudJournalID) || cache.StoreKind() != StoreKindCloud {
		t.Fatalf("unexpected cloud store: %s/%s", cache.StoreID(), cache.StoreKind())
	}
	for _, table := range []string{"items", "documents", "document_attachments", "encryption_master", "journal_encryption_keys", "content_schema_state", "cloud_journal_metadata"} {
		if !tableExists(t, cache.db, table) {
			t.Fatalf("expected portable content table %s", table)
		}
	}
	for _, table := range []string{"app_settings", "vault_providers", "cloud_journal_mounts", "cloud_pending_creates", "installation_device_identity"} {
		if tableExists(t, cache.db, table) {
			t.Fatalf("installation table %s must not exist in a cache", table)
		}
	}
	if err := cache.ValidateCloudJournalScope(cloudJournalID); err != nil {
		t.Fatalf("validate fresh cache: %v", err)
	}
	tree, err := cache.GetLibraryTree()
	if err != nil {
		t.Fatalf("get cloud tree: %v", err)
	}
	if len(tree.Items) != 2 {
		t.Fatalf("expected cloud Journal and Trash, got %#v", tree.Items)
	}
	for _, item := range tree.Items {
		if item.StoreID != string(CloudStoreID(cloudJournalID)) || item.StorageKind != string(StoreKindCloud) {
			t.Fatalf("missing cloud store metadata: %#v", item)
		}
	}
}

func TestCloudCacheScopeRejectsAdditionalRoot(t *testing.T) {
	_, manager := newCloudCacheTestManager(t)
	cloudJournalID := uuid.NewString()
	cache, err := manager.CreateCloudCache(cloudJournalID)
	if err != nil {
		t.Fatalf("create cloud cache: %v", err)
	}
	if _, err := cache.db.Exec(`INSERT INTO items (id, parent_id, kind, title, sort_order, created_at, updated_at)
		VALUES (?, NULL, ?, 'Unexpected', 1, ?, ?)`, uuid.NewString(), KindJournal, nowString(), nowString()); err != nil {
		t.Fatalf("insert invalid root: %v", err)
	}
	if err := cache.ValidateCloudJournalScope(cloudJournalID); err == nil {
		t.Fatal("expected additional cloud Journal root to be rejected")
	}
	if _, err := cache.CreateJournal("Another Journal"); err == nil {
		t.Fatal("cloud cache must reject a second Journal root")
	}
}

func TestCloudCacheScopeRejectsAdditionalMetadata(t *testing.T) {
	_, manager := newCloudCacheTestManager(t)
	cloudJournalID := uuid.NewString()
	cache, err := manager.CreateCloudCache(cloudJournalID)
	if err != nil {
		t.Fatalf("create cloud cache: %v", err)
	}
	if _, err := cache.db.Exec(`INSERT INTO cloud_journal_metadata (cloud_journal_id, content_format_version, created_at) VALUES (?, ?, ?)`, uuid.NewString(), contentSchemaVersion, nowString()); err != nil {
		t.Fatalf("insert invalid metadata: %v", err)
	}
	if err := cache.ValidateCloudJournalScope(cloudJournalID); err == nil {
		t.Fatal("expected additional cloud metadata to be rejected")
	}
}

func TestStoreRouterSeparatesLocalAndCloudStores(t *testing.T) {
	local, manager := newCloudCacheTestManager(t)
	cloudJournalID := uuid.NewString()
	cache, err := manager.CreateCloudCache(cloudJournalID)
	if err != nil {
		t.Fatalf("create cloud cache: %v", err)
	}
	localItem, err := local.CreateJournal("Local only")
	if err != nil {
		t.Fatalf("create local Journal: %v", err)
	}
	if _, err := cache.getRowItem(localItem.Item.ID); err == nil {
		t.Fatal("cloud cache read unexpectedly resolved a local item ID")
	}
	storeID, itemID, err := ParseStoreScopedItemID("cloud:" + cloudJournalID + ":" + cloudJournalID)
	if err != nil || storeID != CloudStoreID(cloudJournalID) || itemID != cloudJournalID {
		t.Fatalf("parse cloud scoped ID: %s %s %v", storeID, itemID, err)
	}
	if _, _, err := ParseStoreScopedItemID("cloud:missing-item"); err == nil {
		t.Fatal("invalid scoped ID should fail")
	}
	store, err := manager.router.Resolve(context.Background(), CloudStoreID(cloudJournalID))
	if err != nil || store != cache.store {
		t.Fatalf("resolve cloud store: %v", err)
	}
	commands, err := NewStoreCommandRouter(manager.router, local)
	if err != nil {
		t.Fatalf("create store command router: %v", err)
	}
	if err := commands.Register(cache); err != nil {
		t.Fatalf("register cloud command service: %v", err)
	}
	if _, err := commands.CreateFolder(context.Background(), CloudStoreID(cloudJournalID), cloudJournalID, "Cloud folder"); err != nil {
		t.Fatalf("create folder through cloud command router: %v", err)
	}
	if _, err := commands.CreateFolder(context.Background(), LocalStoreID, cloudJournalID, "Cross-store folder"); err == nil {
		t.Fatal("local command router must reject a cloud item ID")
	}
}

func TestInstallationSchemaCreatesStableDeviceIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	service, err := OpenJournalService(path)
	if err != nil {
		t.Fatalf("open local service: %v", err)
	}
	repository := NewInstallationRepository(service.db)
	first, err := repository.DeviceIdentity()
	if err != nil {
		t.Fatalf("read device identity: %v", err)
	}
	if first.ID == "" || first.Label == "" {
		t.Fatalf("invalid generated identity: %#v", first)
	}
	for _, table := range []string{"vault_providers", "cloud_journal_mounts", "cloud_pending_creates", "installation_schema_state"} {
		if !tableExists(t, service.db, table) {
			t.Fatalf("expected installation table %s", table)
		}
	}
	if err := service.Close(); err != nil {
		t.Fatalf("close local service: %v", err)
	}
	reopened, err := OpenJournalService(path)
	if err != nil {
		t.Fatalf("reopen local service: %v", err)
	}
	defer reopened.Close()
	second, err := NewInstallationRepository(reopened.db).DeviceIdentity()
	if err != nil {
		t.Fatalf("read reopened device identity: %v", err)
	}
	if first != second {
		t.Fatalf("device identity changed across restart: %#v != %#v", first, second)
	}
}

func TestRemovingProviderRetainsCleanMountAndRejectsDirtyMount(t *testing.T) {
	service := newTestService(t)
	repository := NewInstallationRepository(service.db)
	now := nowString()
	providerID := uuid.NewString()
	if _, err := service.db.Exec(`INSERT INTO vault_providers (id, name, kind, endpoint, root_prefix, credential_ref, created_at, updated_at)
		VALUES (?, 'Provider', 's3', 'https://example.test', 'vault', 'secret-ref', ?, ?)`, providerID, now, now); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	cleanID := uuid.NewString()
	if _, err := service.db.Exec(`INSERT INTO cloud_journal_mounts (cloud_journal_id, provider_id, vault_root, cache_path, created_at, updated_at)
		VALUES (?, ?, 'vault', '/cache/clean', ?, ?)`, cleanID, providerID, now, now); err != nil {
		t.Fatalf("insert clean mount: %v", err)
	}
	if err := repository.RemoveProvider(providerID, func(mount CloudJournalMountRecord) bool { return mount.SyncStatus != "clean" }); err != nil {
		t.Fatalf("remove clean provider: %v", err)
	}
	var mountedProvider, status string
	if err := service.db.QueryRow(`SELECT provider_id, sync_status FROM cloud_journal_mounts WHERE cloud_journal_id = ?`, cleanID).Scan(&mountedProvider, &status); err != nil {
		t.Fatalf("read retained mount: %v", err)
	}
	if mountedProvider != "" || status != "provider_missing" {
		t.Fatalf("expected provider_missing mount, got %q/%q", mountedProvider, status)
	}

	dirtyProviderID := uuid.NewString()
	if _, err := service.db.Exec(`INSERT INTO vault_providers (id, name, kind, endpoint, root_prefix, credential_ref, created_at, updated_at)
		VALUES (?, 'Dirty provider', 's3', 'https://example.test', 'vault', 'secret-ref', ?, ?)`, dirtyProviderID, now, now); err != nil {
		t.Fatalf("insert dirty provider: %v", err)
	}
	if _, err := service.db.Exec(`INSERT INTO cloud_journal_mounts (cloud_journal_id, provider_id, vault_root, cache_path, sync_status, created_at, updated_at)
		VALUES (?, ?, 'vault', '/cache/dirty', 'dirty', ?, ?)`, uuid.NewString(), dirtyProviderID, now, now); err != nil {
		t.Fatalf("insert dirty mount: %v", err)
	}
	if err := repository.RemoveProvider(dirtyProviderID, func(mount CloudJournalMountRecord) bool { return mount.SyncStatus != "clean" }); err == nil {
		t.Fatal("expected dirty provider removal to be rejected")
	}
}
