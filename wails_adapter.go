package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App adapts Wails lifecycle events and RPC calls to application commands.

type App struct {
	ctx               context.Context
	service           *JournalService
	stores            *JournalStoreRouter
	storeCommands     *StoreCommandRouter
	cloudCaches       *CloudCacheManager
	installation      *InstallationRepository
	cloudServices     map[string]*JournalService
	cloudMu           sync.Mutex
	cloudWriteMu      sync.Mutex
	commands          *Commands
	selectedJournalID string
	exportJournalItem *menu.MenuItem
	importJournalItem *menu.MenuItem
}

const localVaultProviderID = "journal-local-vault"

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	dbPath, err := defaultDBPath()
	if err != nil {
		panic(err)
	}
	service, err := OpenJournalService(dbPath)
	if err != nil {
		panic(err)
	}
	if err := service.PurgeDetachedAttachments(detachedAttachmentGrace); err != nil {
		panic(err)
	}
	a.service = service
	router, err := NewJournalStoreRouter(service.store)
	if err != nil {
		panic(err)
	}
	cacheConfigDir := filepath.Dir(dbPath)
	if filepath.Base(cacheConfigDir) == "Journal" {
		cacheConfigDir = filepath.Dir(cacheConfigDir)
	}
	cloudCaches, err := NewCloudCacheManager(cacheConfigDir, router)
	if err != nil {
		panic(err)
	}
	a.stores = router
	a.cloudCaches = cloudCaches
	storeCommands, err := NewStoreCommandRouter(router, service)
	if err != nil {
		panic(err)
	}
	a.storeCommands = storeCommands
	a.installation = NewInstallationRepository(service.db)
	a.cloudServices = map[string]*JournalService{}
	a.commands = NewCommands(service)
	a.service.StartAutosave(ctx)
}

func (a *App) shutdown(ctx context.Context) {
	if a.service != nil {
		_ = a.service.FlushAll()
		_ = a.service.Close()
	}
}

func (a *App) GetLibraryTree() (TreeResponse, error) {
	local, err := a.commands.library.GetTree()
	if err != nil {
		return TreeResponse{}, err
	}
	mounts, err := a.installation.ListMounts()
	if err != nil {
		return TreeResponse{}, err
	}
	for _, mount := range mounts {
		cache, openErr := a.cloudService(mount)
		if openErr != nil {
			local.Items = append(local.Items, cloudPlaceholder(mount, openErr.Error()))
			continue
		}
		cloudTree, treeErr := cache.GetLibraryTree()
		if treeErr != nil {
			local.Items = append(local.Items, cloudPlaceholder(mount, treeErr.Error()))
			continue
		}
		for _, item := range cloudTree.Items {
			if item.SystemKey == SystemTrash {
				continue
			}
			decorateCloudTree(&item, mount)
			local.Items = append(local.Items, item)
		}
	}
	return local, nil
}

func (a *App) ListVaultProviders() ([]VaultProviderResponse, error) {
	providers, err := a.installation.ListProviders()
	if err != nil {
		return nil, err
	}
	out := make([]VaultProviderResponse, 0, len(providers))
	for _, p := range providers {
		// Filesystem is an internal development transport, not a user-selectable Vault.
		if p.Kind == "filesystem" {
			continue
		}
		out = append(out, providerResponse(p, false))
	}
	return out, nil
}
func (a *App) SaveVaultProvider(request VaultProviderRequest) (VaultProviderResponse, error) {
	p, err := a.installation.UpsertProvider(VaultProviderRecord{ID: request.ID, Name: request.Name, Kind: request.Kind, Endpoint: request.Endpoint, RootPrefix: request.Root, CredentialRef: request.CredentialRef, PublishDebounceMS: request.PublishDebounceMS, PublishMaxIntervalMS: request.PublishMaxIntervalMS, RevisionRetentionCount: request.RevisionRetentionCount})
	if err != nil {
		return VaultProviderResponse{}, err
	}
	return providerResponse(p, false), nil
}
func (a *App) ValidateVaultProvider(providerID string) (VaultProviderResponse, error) {
	p, err := a.installation.Provider(providerID)
	if err != nil {
		return VaultProviderResponse{}, err
	}
	sync, err := a.syncForProvider(providerID)
	if err != nil {
		return VaultProviderResponse{}, err
	}
	if _, err := sync.Validate(a.ctx); err != nil {
		return VaultProviderResponse{}, err
	}
	return providerResponse(p, true), nil
}
func (a *App) RemoveVaultProvider(providerID string) error {
	return a.installation.RemoveProvider(providerID, nil)
}
func providerResponse(p VaultProviderRecord, validated bool) VaultProviderResponse {
	return VaultProviderResponse{ID: p.ID, Name: p.Name, Kind: p.Kind, Endpoint: p.Endpoint, Root: p.RootPrefix, PublishDebounceMS: p.PublishDebounceMS, PublishMaxIntervalMS: p.PublishMaxIntervalMS, RevisionRetentionCount: p.RevisionRetentionCount, Validated: validated}
}
func (a *App) ListCloudMounts() ([]CloudMountResponse, error) {
	mounts, err := a.installation.ListMounts()
	if err != nil {
		return nil, err
	}
	out := make([]CloudMountResponse, 0, len(mounts))
	for _, m := range mounts {
		out = append(out, mountResponse(m))
	}
	return out, nil
}
func mountResponse(m CloudJournalMountRecord) CloudMountResponse {
	readOnly := m.SyncStatus == "locked_read_only" || m.SyncStatus == "provider_missing" || m.SyncStatus == "conflict"
	return CloudMountResponse{CloudJournalID: m.CloudJournalID, ProviderID: m.ProviderID, CachePath: m.CachePath, LastRevisionID: m.LastRevisionID, SyncStatus: m.SyncStatus, LastSyncError: m.LastSyncError, LastSyncedAt: m.LastSyncedAt, ReadOnly: readOnly, StatusReason: m.SyncStatus}
}
func (a *App) CreateCloudJournal(providerID string) (CloudJournalResponse, error) {
	sync, err := a.syncForProvider(providerID)
	if err != nil {
		return CloudJournalResponse{}, err
	}
	cache, id, err := sync.CreateCloudJournal(a.ctx)
	if err != nil {
		return CloudJournalResponse{}, err
	}
	a.cloudMu.Lock()
	a.cloudServices[id] = cache
	a.cloudMu.Unlock()
	_ = a.storeCommands.Register(cache)
	mount, err := sync.mount(id)
	if err != nil {
		return CloudJournalResponse{}, err
	}
	tree, err := a.GetLibraryTree()
	return CloudJournalResponse{CloudJournalID: id, Mount: mountResponse(mount), Tree: tree}, err
}

// CreateLocalCloudJournal provisions the app-owned filesystem Vault used for
// local cloud-workflow testing. It is deliberately not exposed as a provider
// setting: users should not need to configure a path or credentials for it.
func (a *App) CreateLocalCloudJournal() (CloudJournalResponse, error) {
	provider, err := a.ensureLocalVaultProvider()
	if err != nil {
		return CloudJournalResponse{}, err
	}
	return a.CreateCloudJournal(provider.ID)
}

func (a *App) ensureLocalVaultProvider() (VaultProviderRecord, error) {
	if a.installation == nil || a.cloudCaches == nil {
		return VaultProviderRecord{}, fmt.Errorf("local Vault is unavailable before app startup")
	}
	root := filepath.Join(filepath.Dir(a.cloudCaches.root), "local-vault")
	return a.installation.UpsertProvider(VaultProviderRecord{
		ID:                     localVaultProviderID,
		Name:                   "Local Vault",
		Kind:                   "filesystem",
		RootPrefix:             root,
		PublishDebounceMS:      30000,
		PublishMaxIntervalMS:   300000,
		RevisionRetentionCount: 50,
	})
}

func (a *App) ReconnectCloudJournal(providerID, cloudJournalID string) (CloudJournalResponse, error) {
	sync, err := a.syncForProvider(providerID)
	if err != nil {
		return CloudJournalResponse{}, err
	}
	cache, err := sync.ReconnectCloudJournal(a.ctx, cloudJournalID)
	if err != nil {
		return CloudJournalResponse{}, err
	}
	a.cloudMu.Lock()
	a.cloudServices[cloudJournalID] = cache
	a.cloudMu.Unlock()
	_ = a.storeCommands.Register(cache)
	mount, err := sync.mount(cloudJournalID)
	if err != nil {
		return CloudJournalResponse{}, err
	}
	tree, err := a.GetLibraryTree()
	return CloudJournalResponse{CloudJournalID: cloudJournalID, Mount: mountResponse(mount), Tree: tree}, err
}
func (a *App) SyncCloudJournal(cloudJournalID string) (CloudMountResponse, error) {
	mount, err := a.installationMount(cloudJournalID)
	if err != nil {
		return CloudMountResponse{}, err
	}
	// A clean mount already points at the latest published revision. Publishing
	// again would create an empty revision, so report its current state instead.
	if mount.SyncStatus != "dirty" {
		return mountResponse(mount), nil
	}
	sync, err := a.syncForProvider(mount.ProviderID)
	if err != nil {
		return CloudMountResponse{}, err
	}
	cache, err := a.cloudService(mount)
	if err != nil {
		return CloudMountResponse{}, err
	}
	if err := sync.Publish(a.ctx, cache, cloudJournalID); err != nil {
		return CloudMountResponse{}, err
	}
	mount, err = sync.mount(cloudJournalID)
	return mountResponse(mount), err
}
func (a *App) ReleaseCloudLease(cloudJournalID string) error {
	m, err := a.installationMount(cloudJournalID)
	if err != nil {
		return err
	}
	sync, err := a.syncForProvider(m.ProviderID)
	if err != nil {
		return err
	}
	return sync.ReleaseLease(a.ctx, cloudJournalID, m.LeaseID)
}
func (a *App) installationMount(id string) (CloudJournalMountRecord, error) {
	mounts, err := a.installation.ListMounts()
	if err != nil {
		return CloudJournalMountRecord{}, err
	}
	for _, m := range mounts {
		if m.CloudJournalID == id {
			return m, nil
		}
	}
	return CloudJournalMountRecord{}, fmt.Errorf("cache_missing")
}

func cloudPlaceholder(mount CloudJournalMountRecord, reason string) TreeItem {
	return TreeItem{ID: mount.CloudJournalID, StoreID: string(CloudStoreID(mount.CloudJournalID)), StorageKind: string(StoreKindCloud), CloudStatus: mount.SyncStatus, ReadOnly: true, Kind: KindJournal, Title: "Cloud Journal", CreatedAt: mount.CreatedAt, UpdatedAt: mount.UpdatedAt, EncryptionState: EncryptionPlaintext, Children: []TreeItem{}}
}
func decorateCloudTree(item *TreeItem, mount CloudJournalMountRecord) {
	item.CloudStatus = mount.SyncStatus
	item.ReadOnly = mount.SyncStatus != "clean" && mount.SyncStatus != "dirty"
	for i := range item.Children {
		decorateCloudTree(&item.Children[i], mount)
	}
}

func (a *App) cloudService(mount CloudJournalMountRecord) (*JournalService, error) {
	a.cloudMu.Lock()
	defer a.cloudMu.Unlock()
	if service := a.cloudServices[mount.CloudJournalID]; service != nil {
		return service, nil
	}
	if mount.ProviderID == "" {
		return nil, fmt.Errorf("provider missing")
	}
	service, err := a.cloudCaches.OpenCloudCache(mount.CloudJournalID)
	if err != nil {
		return nil, err
	}
	if err := a.storeCommands.Register(service); err != nil {
		_ = service.Close()
		return nil, err
	}
	a.cloudServices[mount.CloudJournalID] = service
	return service, nil
}

func (a *App) contentServiceForItem(id string) *JournalService {
	if _, err := a.service.getRawRowItemFrom(a.service.db, id); err == nil {
		return a.service
	}
	a.cloudMu.Lock()
	defer a.cloudMu.Unlock()
	for _, service := range a.cloudServices {
		if _, err := service.getRawRowItemFrom(service.db, id); err == nil {
			return service
		}
	}
	return a.service
}

func (a *App) requireWritable(service *JournalService) error {
	if service.StoreKind() != StoreKindCloud {
		return nil
	}
	a.cloudWriteMu.Lock()
	defer a.cloudWriteMu.Unlock()
	mount, err := a.installationMount(strings.TrimPrefix(string(service.StoreID()), "cloud:"))
	if err != nil {
		return err
	}
	sync, err := a.syncForProvider(mount.ProviderID)
	if err != nil {
		return err
	}
	return (&CloudJournalStore{Journal: service, Sync: sync, CloudJournalID: mount.CloudJournalID}).EnsureWritable(a.ctx)
}

func (a *App) syncForProvider(providerID string) (*VaultSyncService, error) {
	p, err := a.installation.Provider(providerID)
	if err != nil {
		return nil, err
	}
	if p.Kind != "filesystem" {
		return nil, fmt.Errorf("provider_capability_missing")
	}
	device, err := a.installation.DeviceIdentity()
	if err != nil {
		return nil, err
	}
	return &VaultSyncService{Store: FilesystemVaultStore{}, Provider: VaultProvider{ID: p.ID, Root: p.RootPrefix}, Caches: a.cloudCaches, Mounts: a.installation, Device: device}, nil
}

func (a *App) CreateDocument(parentID string) (DocumentResponse, error) {
	service := a.contentServiceForItem(parentID)
	if err := a.requireWritable(service); err != nil {
		return DocumentResponse{}, err
	}
	response, err := service.CreateDocument(parentID)
	if err == nil {
		err = a.markCloudDirty(service)
	}
	return a.withAggregateDocumentTree(response, err)
}

func (a *App) DuplicateDocument(id string) (DocumentResponse, error) {
	service := a.contentServiceForItem(id)
	if err := a.requireWritable(service); err != nil {
		return DocumentResponse{}, err
	}
	response, err := service.DuplicateDocument(id)
	if err == nil {
		err = a.markCloudDirty(service)
	}
	return a.withAggregateDocumentTree(response, err)
}

func (a *App) CreateFolder(parentID string, title string) (ItemResponse, error) {
	service := a.contentServiceForItem(parentID)
	if err := a.requireWritable(service); err != nil {
		return ItemResponse{}, err
	}
	response, err := service.CreateFolder(parentID, title)
	if err == nil {
		err = a.markCloudDirty(service)
	}
	return a.withAggregateItemTree(response, err)
}

func (a *App) CreateJournal(title string) (ItemResponse, error) {
	response, err := a.commands.library.CreateJournal(title)
	return a.withAggregateItemTree(response, err)
}

func (a *App) RenameItem(id string, title string) (ItemResponse, error) {
	service := a.contentServiceForItem(id)
	if err := a.requireWritable(service); err != nil {
		return ItemResponse{}, err
	}
	response, err := service.RenameItem(id, title)
	if err == nil {
		err = a.updateCloudJournalMetadata(service, response.Item)
	}
	if err == nil {
		err = a.markCloudDirty(service)
	}
	return a.withAggregateItemTree(response, err)
}

func (a *App) MoveItem(id string, newParentID string, newSortOrder int) (TreeResponse, error) {
	return a.commands.library.MoveItem(id, newParentID, newSortOrder)
}

func (a *App) TrashItem(command TrashItemCommand) (TreeResponse, error) {
	service := a.contentServiceForItem(command.ID)
	if err := a.requireWritable(service); err != nil {
		return TreeResponse{}, err
	}
	if _, err := service.TrashItem(command); err != nil {
		return TreeResponse{}, err
	}
	if err := a.markCloudDirty(service); err != nil {
		return TreeResponse{}, err
	}
	return a.GetLibraryTree()
}

func (a *App) DeleteJournal(id string) (TreeResponse, error) {
	return a.commands.library.DeleteJournal(id)
}

func (a *App) OpenDocument(id string) (DocumentResponse, error) {
	response, err := a.contentServiceForItem(id).OpenDocument(id)
	return a.withAggregateDocumentTree(response, err)
}

// Wails callers render one unified library. JournalService responses contain
// only their owning store's tree, so normalize each item/document mutation to
// the aggregate tree before crossing the RPC boundary.
func (a *App) withAggregateItemTree(response ItemResponse, operationErr error) (ItemResponse, error) {
	if operationErr != nil {
		return ItemResponse{}, operationErr
	}
	tree, err := a.GetLibraryTree()
	if err != nil {
		return ItemResponse{}, err
	}
	response.Tree = tree
	return response, nil
}

func (a *App) withAggregateDocumentTree(response DocumentResponse, operationErr error) (DocumentResponse, error) {
	if operationErr != nil {
		return DocumentResponse{}, operationErr
	}
	tree, err := a.GetLibraryTree()
	if err != nil {
		return DocumentResponse{}, err
	}
	response.Tree = tree
	return response, nil
}

func (a *App) markCloudDirty(service *JournalService) error {
	if service == nil || service.StoreKind() != StoreKindCloud {
		return nil
	}
	cloudJournalID := strings.TrimPrefix(string(service.StoreID()), "cloud:")
	return a.installation.SetMountSyncStatus(cloudJournalID, "dirty", "")
}

func (a *App) updateCloudJournalMetadata(service *JournalService, item TreeItem) error {
	if service == nil || service.StoreKind() != StoreKindCloud || item.Kind != KindJournal {
		return nil
	}
	cloudJournalID := strings.TrimPrefix(string(service.StoreID()), "cloud:")
	mount, err := a.installationMount(cloudJournalID)
	if err != nil {
		return err
	}
	sync, err := a.syncForProvider(mount.ProviderID)
	if err != nil {
		return err
	}
	return sync.UpdateJournalMetadata(a.ctx, cloudJournalID, item.Title)
}

func (a *App) UpdateDocumentDraft(id string, content map[string]any, version int64) (DocumentDraftResponse, error) {
	service := a.contentServiceForItem(id)
	if err := a.requireWritable(service); err != nil {
		return DocumentDraftResponse{}, err
	}
	return service.UpdateDocumentDraft(id, content, version)
}

func (a *App) CreateDocumentAttachment(documentID string, name string, mimeType string, dataBase64 string) (DocumentAttachmentResponse, error) {
	service := a.contentServiceForItem(documentID)
	if err := a.requireWritable(service); err != nil {
		return DocumentAttachmentResponse{}, err
	}
	response, err := service.CreateDocumentAttachment(documentID, name, mimeType, dataBase64)
	if err == nil {
		err = a.markCloudDirty(service)
	}
	return response, err
}

func (a *App) PickDocumentImage(documentID string) (DocumentAttachmentResponse, error) {
	if a.ctx == nil {
		return DocumentAttachmentResponse{}, fmt.Errorf("app is not ready")
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Insert Image",
		Filters: []runtime.FileFilter{{
			DisplayName: "Images (*.png, *.jpg, *.jpeg, *.gif, *.webp)",
			Pattern:     "*.png;*.jpg;*.jpeg;*.gif;*.webp",
		}},
	})
	if err != nil {
		return DocumentAttachmentResponse{}, err
	}
	if strings.TrimSpace(path) == "" {
		return DocumentAttachmentResponse{}, nil
	}
	service := a.contentServiceForItem(documentID)
	if err := a.requireWritable(service); err != nil {
		return DocumentAttachmentResponse{}, err
	}
	response, err := service.CreateDocumentAttachmentFromPath(documentID, path)
	if err == nil {
		err = a.markCloudDirty(service)
	}
	return response, err
}

func (a *App) GetDocumentAttachmentDataURL(attachmentID string) (DocumentAttachmentDataResponse, error) {
	service, err := a.contentServiceForAttachment(attachmentID)
	if err != nil {
		return DocumentAttachmentDataResponse{}, err
	}
	return service.GetDocumentAttachmentDataURL(attachmentID)
}

func (a *App) UpdateDocumentSpacing(id string, spacingPreset string) (DocumentSaveResponse, error) {
	service := a.contentServiceForItem(id)
	if err := a.requireWritable(service); err != nil {
		return DocumentSaveResponse{}, err
	}
	response, err := service.UpdateDocumentSpacing(id, spacingPreset)
	if err == nil {
		err = a.markCloudDirty(service)
	}
	return response, err
}

func (a *App) FlushDocument(id string) (DocumentSaveResponse, error) {
	service := a.contentServiceForItem(id)
	if err := a.requireWritable(service); err != nil {
		return DocumentSaveResponse{}, err
	}
	response, err := service.FlushDocument(id)
	if err == nil {
		err = a.markCloudDirty(service)
	}
	return response, err
}

func (a *App) contentServiceForAttachment(attachmentID string) (*JournalService, error) {
	attachmentID = strings.TrimSpace(attachmentID)
	if attachmentID == "" {
		return nil, fmt.Errorf("attachment id is required")
	}
	if attachmentExists(a.service, attachmentID) {
		return a.service, nil
	}
	a.cloudMu.Lock()
	defer a.cloudMu.Unlock()
	for _, service := range a.cloudServices {
		if attachmentExists(service, attachmentID) {
			return service, nil
		}
	}
	return nil, fmt.Errorf("attachment not found")
}

func attachmentExists(service *JournalService, attachmentID string) bool {
	if service == nil {
		return false
	}
	var found int
	return service.db.QueryRow(`SELECT 1 FROM document_attachments WHERE id = ?`, attachmentID).Scan(&found) == nil
}

func (a *App) SearchLibrary(query string) (SearchResponse, error) {
	return a.commands.library.Search(query)
}

func (a *App) GetEncryptionStatus() (EncryptionStatusResponse, error) {
	return a.commands.encryption.Status()
}

func (a *App) CreateMasterPassword(password string) error {
	return a.commands.encryption.CreateMasterPassword(password)
}

func (a *App) UnlockEncryption(password string) (EncryptionStatusResponse, error) {
	if err := a.commands.encryption.Unlock(password); err != nil {
		return EncryptionStatusResponse{}, err
	}
	return a.commands.encryption.Status()
}

func (a *App) ChangeMasterPassword(currentPassword string, newPassword string) (EncryptionStatusResponse, error) {
	if err := a.commands.encryption.ChangeMasterPassword(currentPassword, newPassword); err != nil {
		return EncryptionStatusResponse{}, err
	}
	return a.commands.encryption.Status()
}

func (a *App) EncryptJournal(journalID string) (TreeResponse, error) {
	return a.commands.encryption.EncryptJournal(journalID)
}

func (a *App) DecryptJournal(journalID string) (TreeResponse, error) {
	return a.commands.encryption.DecryptJournal(journalID)
}

func (a *App) GetAppSettings() (AppSettingsResponse, error) {
	return a.commands.settings.Get()
}

func (a *App) UpdateAppSettings(settings AppSettingsPatch) (AppSettingsResponse, error) {
	return a.commands.settings.Update(settings)
}

func (a *App) GetAppInfo() AppInfo {
	return appInfo()
}

func (a *App) ShowAbout() {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "journal:show-about")
}

func (a *App) SetSelectedJournalForMenu(journalID string) {
	a.selectedJournalID = strings.TrimSpace(journalID)
	a.updateFileMenuState()
}

func (a *App) EmitExportJournalMenuAction() {
	if a.ctx == nil || strings.TrimSpace(a.selectedJournalID) == "" {
		return
	}
	runtime.EventsEmit(a.ctx, "journal:menu-export-journal", a.selectedJournalID)
}

func (a *App) EmitImportJournalMenuAction() {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "journal:menu-import-journal")
}

func (a *App) updateFileMenuState() {
	enabled := strings.TrimSpace(a.selectedJournalID) != ""
	if a.exportJournalItem != nil {
		if enabled {
			a.exportJournalItem.Enable()
		} else {
			a.exportJournalItem.Disable()
		}
	}
	if a.importJournalItem != nil {
		a.importJournalItem.Enable()
	}
	if a.ctx != nil {
		runtime.MenuUpdateApplicationMenu(a.ctx)
	}
}

type AppInfo struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Disclaimer string `json:"disclaimer"`
}

type wailsProjectInfo struct {
	Name string `json:"name"`
	Info struct {
		ProductName    string `json:"productName"`
		ProductVersion string `json:"productVersion"`
		Comments       string `json:"comments"`
	} `json:"info"`
}

func appInfo() AppInfo {
	info := AppInfo{
		Name:       defaultAppName,
		Version:    defaultAppVersion,
		Disclaimer: appDisclaimer,
	}

	var project wailsProjectInfo
	if err := json.Unmarshal(wailsConfig, &project); err == nil {
		if name := strings.TrimSpace(project.Info.ProductName); name != "" {
			info.Name = name
		} else if name := strings.TrimSpace(project.Name); name != "" {
			info.Name = name
		}
		if version := strings.TrimSpace(project.Info.ProductVersion); version != "" {
			info.Version = version
		}
		if disclaimer := strings.TrimSpace(project.Info.Comments); disclaimer != "" {
			info.Disclaimer = disclaimer
		}
	}

	if version := strings.TrimSpace(appVersion); version != "" {
		info.Version = version
	}
	return info
}
