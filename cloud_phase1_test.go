package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newCloudAppForTest(t *testing.T) *App {
	t.Helper()
	service := newTestService(t)
	router, err := NewJournalStoreRouter(service.store)
	if err != nil {
		t.Fatalf("create store router: %v", err)
	}
	caches, err := NewCloudCacheManager(t.TempDir(), router)
	if err != nil {
		t.Fatalf("create cloud cache manager: %v", err)
	}
	storeCommands, err := NewStoreCommandRouter(router, service)
	if err != nil {
		t.Fatalf("create store command router: %v", err)
	}
	return &App{
		ctx:           context.Background(),
		service:       service,
		stores:        router,
		storeCommands: storeCommands,
		cloudCaches:   caches,
		installation:  NewInstallationRepository(service.db),
		cloudServices: map[string]*JournalService{},
		commands:      NewCommands(service),
	}
}

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

func TestRemoteVaultProviderConfigurationAndVisibility(t *testing.T) {
	service := newTestService(t)
	repository := NewInstallationRepository(service.db)

	if _, err := repository.UpsertProvider(VaultProviderRecord{Name: "Missing endpoint", Kind: "s3", RootPrefix: "journal"}); err == nil {
		t.Fatal("expected S3 provider without an endpoint to be rejected")
	}
	s3, err := repository.UpsertProvider(VaultProviderRecord{Name: "Archive", Kind: "s3", Endpoint: "https://s3.example.test", RootPrefix: "journal-bucket/archive", CredentialRef: "keychain:archive"})
	if err != nil {
		t.Fatalf("save S3 provider: %v", err)
	}
	if s3.Endpoint != "https://s3.example.test" || s3.RootPrefix != "journal-bucket/archive" || s3.CredentialRef != "keychain:archive" {
		t.Fatalf("S3 configuration was not preserved: %#v", s3)
	}
	if _, err := repository.UpsertProvider(VaultProviderRecord{Name: "Team Vault", Kind: "webdav", Endpoint: "https://dav.example.test/vault", RootPrefix: "/journal", CredentialRef: "keychain:team-vault"}); err != nil {
		t.Fatalf("save WebDAV provider: %v", err)
	}
	if _, err := repository.UpsertProvider(VaultProviderRecord{Name: "Internal filesystem", Kind: "filesystem", RootPrefix: t.TempDir()}); err != nil {
		t.Fatalf("save development filesystem provider: %v", err)
	}

	providers, err := (&App{installation: repository}).ListVaultProviders()
	if err != nil {
		t.Fatalf("list visible providers: %v", err)
	}
	if len(providers) != 2 {
		t.Fatalf("expected only remote providers to be visible, got %#v", providers)
	}
	for _, provider := range providers {
		if provider.Kind == "filesystem" {
			t.Fatalf("filesystem provider must not be exposed through the application API: %#v", provider)
		}
	}
}

func TestCreateLocalCloudJournalProvisioningDoesNotNeedProviderSetup(t *testing.T) {
	app := newCloudAppForTest(t)

	created, err := app.CreateLocalCloudJournal()
	if err != nil {
		t.Fatalf("create local cloud Journal: %v", err)
	}
	if created.CloudJournalID == "" || created.Mount.ProviderID != localVaultProviderID {
		t.Fatalf("unexpected local cloud Journal result: %#v", created)
	}
	provider, err := app.installation.Provider(localVaultProviderID)
	if err != nil {
		t.Fatalf("read internal local provider: %v", err)
	}
	if provider.Kind != "filesystem" || provider.RootPrefix == "" {
		t.Fatalf("unexpected internal provider: %#v", provider)
	}
	visible, err := app.ListVaultProviders()
	if err != nil {
		t.Fatalf("list user-visible providers: %v", err)
	}
	if len(visible) != 0 {
		t.Fatalf("internal local provider must remain hidden, got %#v", visible)
	}
}

func TestCloudRenameReturnsAggregateLibraryTree(t *testing.T) {
	app := newCloudAppForTest(t)
	local, err := app.CreateJournal("Local Journal")
	if err != nil {
		t.Fatalf("create local Journal: %v", err)
	}
	cloud, err := app.CreateLocalCloudJournal()
	if err != nil {
		t.Fatalf("create cloud Journal: %v", err)
	}
	renamed, err := app.RenameItem(cloud.CloudJournalID, "Renamed Cloud Journal")
	if err != nil {
		t.Fatalf("rename cloud Journal: %v", err)
	}
	if findTreeItem(renamed.Tree.Items, local.Item.ID) == nil {
		t.Fatalf("aggregate tree lost local Journal after cloud rename: %#v", renamed.Tree.Items)
	}
	cloudItem := findTreeItem(renamed.Tree.Items, cloud.CloudJournalID)
	if cloudItem == nil || cloudItem.Title != "Renamed Cloud Journal" {
		t.Fatalf("aggregate tree did not include renamed cloud Journal: %#v", renamed.Tree.Items)
	}
	sync, err := app.syncForProvider(localVaultProviderID)
	if err != nil {
		t.Fatalf("load local Vault sync: %v", err)
	}
	metadataBytes, _, err := sync.Store.GetControl(app.ctx, sync.Provider, mustVaultJournalMetadata(cloud.CloudJournalID))
	if err != nil {
		t.Fatalf("read renamed journal metadata: %v", err)
	}
	metadata, err := parseVaultJournalMetadata(metadataBytes)
	if err != nil || metadata.DisplayName != "Renamed Cloud Journal" {
		t.Fatalf("journal metadata was not updated with rename: %#v / %v", metadata, err)
	}
}

func TestMountedCloudJournalsAppearInFreshAppTree(t *testing.T) {
	app := newCloudAppForTest(t)
	created, err := app.CreateLocalCloudJournal()
	if err != nil {
		t.Fatalf("create cloud Journal: %v", err)
	}

	// Use new routing and cache-manager instances to model an application
	// restart, while retaining the same installation database and cache files.
	router, err := NewJournalStoreRouter(app.service.store)
	if err != nil {
		t.Fatalf("create fresh store router: %v", err)
	}
	caches := &CloudCacheManager{root: app.cloudCaches.root, router: router}
	storeCommands, err := NewStoreCommandRouter(router, app.service)
	if err != nil {
		t.Fatalf("create fresh store command router: %v", err)
	}
	fresh := &App{
		ctx:           context.Background(),
		service:       app.service,
		stores:        router,
		storeCommands: storeCommands,
		cloudCaches:   caches,
		installation:  app.installation,
		cloudServices: map[string]*JournalService{},
		commands:      NewCommands(app.service),
	}

	tree, err := fresh.GetLibraryTree()
	if err != nil {
		t.Fatalf("get fresh app library tree: %v", err)
	}
	if findTreeItem(tree.Items, created.CloudJournalID) == nil {
		t.Fatalf("fresh app tree omitted mounted cloud Journal: %#v", tree.Items)
	}
}

func TestOpenLocalDocumentReturnsCloudJournalsInAggregateTree(t *testing.T) {
	app := newCloudAppForTest(t)
	local, err := app.CreateJournal("Local Journal")
	if err != nil {
		t.Fatalf("create local Journal: %v", err)
	}
	document, err := app.CreateDocument(local.Item.ID)
	if err != nil {
		t.Fatalf("create local document: %v", err)
	}
	cloud, err := app.CreateLocalCloudJournal()
	if err != nil {
		t.Fatalf("create cloud Journal: %v", err)
	}

	opened, err := app.OpenDocument(document.ID)
	if err != nil {
		t.Fatalf("open local document: %v", err)
	}
	if findTreeItem(opened.Tree.Items, cloud.CloudJournalID) == nil {
		t.Fatalf("opening a local document omitted cloud Journal from aggregate tree: %#v", opened.Tree.Items)
	}
}

func TestCloudDocumentAttachmentRPCUsesOwningStore(t *testing.T) {
	app := newCloudAppForTest(t)
	cloud, err := app.CreateLocalCloudJournal()
	if err != nil {
		t.Fatalf("create cloud Journal: %v", err)
	}
	document, err := app.CreateDocument(cloud.CloudJournalID)
	if err != nil {
		t.Fatalf("create cloud document: %v", err)
	}
	attachment, err := app.CreateDocumentAttachment(document.ID, "pixel.png", "image/png", "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScL5KwAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatalf("create cloud attachment through RPC: %v", err)
	}
	content := map[string]any{
		"type": "doc",
		"content": []any{map[string]any{
			"type":  "attachmentImage",
			"attrs": map[string]any{"attachmentId": attachment.ID, "alt": "pixel.png"},
		}},
	}
	if _, err := app.UpdateDocumentDraft(document.ID, content, 1); err != nil {
		t.Fatalf("save cloud draft through RPC: %v", err)
	}
	if _, err := app.FlushDocument(document.ID); err != nil {
		t.Fatalf("flush cloud draft through RPC: %v", err)
	}
	mount, err := app.installationMount(cloud.CloudJournalID)
	if err != nil || mount.SyncStatus != "dirty" {
		t.Fatalf("expected cloud mutation to await sync, got %#v / %v", mount, err)
	}
	synced, err := app.SyncCloudJournal(cloud.CloudJournalID)
	if err != nil || synced.SyncStatus != "clean" {
		t.Fatalf("expected cloud publish to restore clean status, got %#v / %v", synced, err)
	}
	data, err := app.GetDocumentAttachmentDataURL(attachment.ID)
	if err != nil || data.DataURL == "" {
		t.Fatalf("read cloud attachment through RPC: %#v / %v", data, err)
	}
}

func TestCloudWriteReacquiresExpiredLocalLease(t *testing.T) {
	app := newCloudAppForTest(t)
	cloud, err := app.CreateLocalCloudJournal()
	if err != nil {
		t.Fatalf("create cloud Journal: %v", err)
	}
	document, err := app.CreateDocument(cloud.CloudJournalID)
	if err != nil {
		t.Fatalf("create cloud document: %v", err)
	}
	sync, err := app.syncForProvider(localVaultProviderID)
	if err != nil {
		t.Fatalf("load local Vault sync: %v", err)
	}
	data, token, err := sync.Store.GetControl(app.ctx, sync.Provider, mustVaultLease(cloud.CloudJournalID))
	if err != nil {
		t.Fatalf("read local Vault lease: %v", err)
	}
	lease, err := parseVaultLease(data)
	if err != nil {
		t.Fatalf("parse local Vault lease: %v", err)
	}
	oldLeaseID := lease.LeaseID
	lease.ExpiresAt = time.Now().UTC().Add(-time.Second)
	expired, err := canonicalVaultJSON(lease)
	if err != nil {
		t.Fatalf("encode expired lease: %v", err)
	}
	if _, err := sync.Store.PutControlIf(app.ctx, sync.Provider, mustVaultLease(cloud.CloudJournalID), expired, token); err != nil {
		t.Fatalf("expire local Vault lease: %v", err)
	}
	if _, err := app.CreateDocumentAttachment(document.ID, "pixel.png", "image/png", "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScL5KwAAAABJRU5ErkJggg=="); err != nil {
		t.Fatalf("write after lease expiry: %v", err)
	}
	mount, err := sync.mount(cloud.CloudJournalID)
	if err != nil || mount.LeaseID == "" || mount.LeaseID == oldLeaseID {
		t.Fatalf("expected a replacement lease after expiry, got %#v / %v", mount, err)
	}
}

func TestSyncCleanCloudJournalDoesNotCreateEmptyRevision(t *testing.T) {
	app := newCloudAppForTest(t)
	cloud, err := app.CreateLocalCloudJournal()
	if err != nil {
		t.Fatalf("create cloud Journal: %v", err)
	}
	before, err := app.installationMount(cloud.CloudJournalID)
	if err != nil || before.SyncStatus != "clean" || before.LastRevisionID == "" {
		t.Fatalf("unexpected initial mount: %#v / %v", before, err)
	}
	synced, err := app.SyncCloudJournal(cloud.CloudJournalID)
	if err != nil || synced.LastRevisionID != before.LastRevisionID || synced.SyncStatus != "clean" {
		t.Fatalf("clean sync should be a no-op, got %#v / %v", synced, err)
	}
	after, err := app.installationMount(cloud.CloudJournalID)
	if err != nil || after.LastCurrentToken != before.LastCurrentToken {
		t.Fatalf("clean sync changed the current revision: %#v / %v", after, err)
	}
}

func TestRemovingProviderRetainsAllMounts(t *testing.T) {
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
	if err := repository.RemoveProvider(dirtyProviderID, func(mount CloudJournalMountRecord) bool { return mount.SyncStatus != "clean" }); err != nil {
		t.Fatalf("remove dirty provider: %v", err)
	}
	var dirtyProvider, dirtyStatus string
	if err := service.db.QueryRow(`SELECT provider_id, sync_status FROM cloud_journal_mounts WHERE provider_id = '' AND cache_path = '/cache/dirty'`).Scan(&dirtyProvider, &dirtyStatus); err != nil {
		t.Fatalf("read retained dirty mount: %v", err)
	}
	if dirtyProvider != "" || dirtyStatus != "provider_missing" {
		t.Fatalf("expected dirty mount to be retained as provider_missing, got %q/%q", dirtyProvider, dirtyStatus)
	}
}
