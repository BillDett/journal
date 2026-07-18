package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	goRuntime "runtime"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App adapts Wails lifecycle events and RPC calls to application commands.

type App struct {
	ctx                   context.Context
	service               *JournalService
	commands              *Commands
	selectedJournalID     string
	exportJournalItem     *menu.MenuItem
	importJournalItem     *menu.MenuItem
	emptyTrashItem        *menu.MenuItem
	journalEncryptionItem *menu.MenuItem
	journalDetailsItem    *menu.MenuItem
	deleteJournalItem     *menu.MenuItem
	lockJournalsItem      *menu.MenuItem
	closeMu               sync.Mutex
	closeRequested        bool
	allowClose            bool
}

// lockCloudWrite makes the RPC boundary honor Cloud Backup's read-only phase.
// Cloud operations take the matching exclusive lock while they flush and stage
// a snapshot, so an already-started write finishes before the snapshot and a
// later write waits until it is safe to proceed.
func (a *App) lockCloudWrite() func() {
	if a.service == nil {
		return func() {}
	}
	a.service.cloudWriteMu.RLock()
	return a.service.cloudWriteMu.RUnlock
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	dbPath, err := defaultDBPath()
	if err != nil {
		a.showStartupError("Journal could not locate its database", err.Error())
		return
	}
	service, err := OpenJournalService(dbPath)
	if err != nil {
		a.showStartupError("Journal could not open its database", startupDatabaseErrorMessage(dbPath, err))
		return
	}
	if err := service.PurgeDetachedAttachments(detachedAttachmentGrace); err != nil {
		_ = service.Close()
		a.showStartupError("Journal could not finish starting", err.Error())
		return
	}
	a.service = service
	a.commands = NewCommands(service)
	a.service.StartAutosave(ctx)
}

func (a *App) showStartupError(title, message string) {
	_, _ = runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
		Type:          runtime.ErrorDialog,
		Title:         title,
		Message:       message,
		Buttons:       []string{"Quit"},
		DefaultButton: "Quit",
		CancelButton:  "Quit",
	})
	a.closeMu.Lock()
	a.allowClose = true
	a.closeMu.Unlock()
	runtime.Quit(a.ctx)
}

func startupDatabaseErrorMessage(dbPath string, err error) string {
	return fmt.Sprintf("Database: %s\n\n%s\n\nJournal will now quit. If this database was created by a newer version, open it with that version or upgrade Journal. If a migration failed, restore the database from a backup before trying again.", dbPath, err)
}

func (a *App) shutdown(ctx context.Context) {
	if a.service != nil {
		_ = a.service.FlushAll()
		_ = a.service.Close()
	}
}

func (a *App) beforeClose(ctx context.Context) bool {
	a.closeMu.Lock()
	if a.allowClose {
		a.closeMu.Unlock()
		return false
	}
	if a.closeRequested {
		a.closeMu.Unlock()
		return true
	}
	a.closeRequested = true
	a.closeMu.Unlock()
	runtime.EventsEmit(ctx, "journal:before-close")
	return true
}

func (a *App) CompleteCloseAfterFlush() {
	if a.ctx == nil {
		return
	}
	a.closeMu.Lock()
	a.allowClose = true
	a.closeRequested = false
	a.closeMu.Unlock()
	runtime.Quit(a.ctx)
}

func (a *App) CancelCloseAfterFlushFailure() {
	a.closeMu.Lock()
	a.closeRequested = false
	a.closeMu.Unlock()
}

func (a *App) GetLibraryTree() (TreeResponse, error) {
	return a.commands.library.GetTree()
}

func (a *App) GetJournalDetails(journalID string) (JournalDetailsResponse, error) {
	return a.commands.library.GetJournalDetails(journalID)
}

func (a *App) CreateDocument(parentID string) (DocumentResponse, error) {
	defer a.lockCloudWrite()()
	return a.commands.documents.Create(parentID)
}

func (a *App) DuplicateDocument(id string) (DocumentResponse, error) {
	defer a.lockCloudWrite()()
	return a.commands.documents.Duplicate(id)
}

func (a *App) CreateFolder(parentID string, title string) (ItemResponse, error) {
	defer a.lockCloudWrite()()
	return a.commands.library.CreateFolder(parentID, title)
}

func (a *App) CreateJournal(title string) (ItemResponse, error) {
	defer a.lockCloudWrite()()
	return a.commands.library.CreateJournal(title)
}

func (a *App) RenameItem(id string, title string) (ItemResponse, error) {
	defer a.lockCloudWrite()()
	return a.commands.library.RenameItem(id, title)
}

func (a *App) MoveItem(id string, newParentID string, newSortOrder int) (TreeResponse, error) {
	defer a.lockCloudWrite()()
	return a.commands.library.MoveItem(id, newParentID, newSortOrder)
}

func (a *App) TrashItem(command TrashItemCommand) (TreeResponse, error) {
	defer a.lockCloudWrite()()
	return a.commands.library.TrashItem(command)
}

func (a *App) DeleteJournal(id string) (TreeResponse, error) {
	defer a.lockCloudWrite()()
	return a.commands.library.DeleteJournal(id)
}

func (a *App) EmptyTrash() (TreeResponse, error) {
	defer a.lockCloudWrite()()
	return a.commands.library.EmptyTrash()
}

func (a *App) OpenDocument(id string) (DocumentResponse, error) {
	return a.commands.documents.Open(id)
}

func (a *App) ExportDocumentMarkdown(documentID string) error {
	if a.ctx == nil {
		return fmt.Errorf("app is not ready")
	}
	doc, err := a.commands.documents.Open(documentID)
	if err != nil {
		return err
	}
	targetPath, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Export Document as Markdown",
		DefaultFilename: sanitizeFSName(doc.Title, "Untitled") + ".md",
		Filters:         []runtime.FileFilter{{DisplayName: "Markdown (*.md)", Pattern: "*.md"}},
	})
	if err != nil || strings.TrimSpace(targetPath) == "" {
		return err
	}
	return a.commands.documents.ExportMarkdown(documentID, targetPath)
}

func (a *App) UpdateDocumentDraft(id string, content map[string]any, version int64) (DocumentDraftResponse, error) {
	defer a.lockCloudWrite()()
	return a.commands.documents.UpdateDraft(id, content, version)
}

func (a *App) CreateDocumentAttachment(documentID string, name string, mimeType string, dataBase64 string) (DocumentAttachmentResponse, error) {
	defer a.lockCloudWrite()()
	return a.commands.documents.CreateAttachment(documentID, name, mimeType, dataBase64)
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
	defer a.lockCloudWrite()()
	return a.commands.documents.CreateAttachmentFromPath(documentID, path)
}

func (a *App) GetDocumentAttachmentDataURL(attachmentID string) (DocumentAttachmentDataResponse, error) {
	return a.commands.documents.AttachmentDataURL(attachmentID)
}

func (a *App) UpdateDocumentSpacing(id string, spacingPreset string) (DocumentSaveResponse, error) {
	defer a.lockCloudWrite()()
	return a.commands.documents.UpdateSpacing(id, spacingPreset)
}

func (a *App) FlushDocument(id string) (DocumentSaveResponse, error) {
	defer a.lockCloudWrite()()
	return a.commands.documents.Flush(id)
}

func (a *App) SearchLibrary(query string) (SearchResponse, error) {
	return a.commands.library.Search(query)
}

func (a *App) GetEncryptionStatus() (EncryptionStatusResponse, error) {
	return a.commands.encryption.Status()
}

func (a *App) CreateMasterPassword(password string) error {
	defer a.lockCloudWrite()()
	return a.commands.encryption.CreateMasterPassword(password)
}

func (a *App) UnlockEncryption(password string) (EncryptionStatusResponse, error) {
	if err := a.commands.encryption.Unlock(password); err != nil {
		return EncryptionStatusResponse{}, err
	}
	return a.commands.encryption.Status()
}

func (a *App) ChangeMasterPassword(currentPassword string, newPassword string) (EncryptionStatusResponse, error) {
	defer a.lockCloudWrite()()
	if err := a.commands.encryption.ChangeMasterPassword(currentPassword, newPassword); err != nil {
		return EncryptionStatusResponse{}, err
	}
	return a.commands.encryption.Status()
}

func (a *App) EncryptJournal(journalID string) (TreeResponse, error) {
	defer a.lockCloudWrite()()
	return a.commands.encryption.EncryptJournal(journalID)
}

func (a *App) DecryptJournal(journalID string) (TreeResponse, error) {
	defer a.lockCloudWrite()()
	return a.commands.encryption.DecryptJournal(journalID)
}

func (a *App) LockEncryptedJournals() (EncryptionStatusResponse, error) {
	defer a.lockCloudWrite()()
	if err := a.commands.encryption.Lock(); err != nil {
		return EncryptionStatusResponse{}, err
	}
	return a.commands.encryption.Status()
}

func (a *App) GetAppSettings() (AppSettingsResponse, error) {
	return a.commands.settings.Get()
}

func (a *App) GetJournalDatabaseLocation() (JournalDatabaseLocationResponse, error) {
	path, err := defaultDBPath()
	if err != nil {
		return JournalDatabaseLocationResponse{}, err
	}
	return JournalDatabaseLocationResponse{Path: path, CanReveal: goRuntime.GOOS != "linux"}, nil
}

func (a *App) RevealJournalDatabaseFile() error {
	path, err := defaultDBPath()
	if err != nil {
		return err
	}
	switch goRuntime.GOOS {
	case "darwin":
		return exec.Command("open", "-R", path).Start()
	case "windows":
		return exec.Command("explorer.exe", "/select,", path).Start()
	default:
		return fmt.Errorf("revealing the database file is not supported on this operating system")
	}
}

func (a *App) UpdateAppSettings(settings AppSettingsPatch) (AppSettingsResponse, error) {
	defer a.lockCloudWrite()()
	return a.commands.settings.Update(settings)
}

func (a *App) GetCloudBackupStatus() (CloudBackupStatusResponse, error) {
	return a.commands.cloud.Status()
}

func (a *App) GetCloudBackupStatusAfterFlush() (CloudBackupStatusResponse, error) {
	defer a.lockCloudWrite()()
	if err := a.service.FlushAll(); err != nil {
		return CloudBackupStatusResponse{}, err
	}
	return a.commands.cloud.Status()
}

func (a *App) ConfigureCloudBackup(command CloudBackupEndpointCommand) (CloudBackupStatusResponse, error) {
	if a.ctx == nil {
		return CloudBackupStatusResponse{}, fmt.Errorf("app is not ready")
	}
	defer a.lockCloudWrite()()
	return a.commands.cloud.Configure(a.ctx, command)
}

func (a *App) UnlockCloudBackupCredentials(masterPassword string) (CloudBackupStatusResponse, error) {
	return a.commands.cloud.UnlockCredentials(masterPassword)
}

func (a *App) SyncCloudBackup() (CloudBackupStatusResponse, error) {
	if a.ctx == nil {
		return CloudBackupStatusResponse{}, fmt.Errorf("app is not ready")
	}
	return a.commands.cloud.Sync(a.ctx)
}

func (a *App) RestoreCloudBackup(masterPassword string) error {
	if a.ctx == nil {
		return fmt.Errorf("app is not ready")
	}
	if _, err := a.commands.cloud.Restore(a.ctx, masterPassword); err != nil {
		return err
	}
	_, _ = runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
		Type:          runtime.InfoDialog,
		Title:         "Cloud Backup Restored",
		Message:       "The cloud backup is now the local Journal database. Journal will close; reopen it to continue.",
		Buttons:       []string{"Close Journal"},
		DefaultButton: "Close Journal",
	})
	a.closeMu.Lock()
	a.allowClose = true
	a.closeMu.Unlock()
	runtime.Quit(a.ctx)
	return nil
}

func (a *App) DisconnectCloudBackup() error {
	defer a.lockCloudWrite()()
	return a.commands.cloud.Disconnect()
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
	a.updateMenuState()
}

func (a *App) EmitExportJournalMenuAction() {
	if a.ctx == nil || strings.TrimSpace(a.selectedJournalID) == "" {
		return
	}
	runtime.EventsEmit(a.ctx, "journal:menu-export-journal", a.selectedJournalID)
}

func (a *App) EmitJournalEncryptionMenuAction() {
	if a.ctx == nil || a.selectedJournalID == "" || a.journalEncryptionItem == nil {
		return
	}
	event := "journal:menu-encrypt-journal"
	if a.journalEncryptionItem.Label == "Un-Encrypt Journal" {
		event = "journal:menu-decrypt-journal"
	}
	runtime.EventsEmit(a.ctx, event, a.selectedJournalID)
}

func (a *App) EmitJournalDetailsMenuAction() {
	if a.ctx != nil && a.selectedJournalID != "" {
		runtime.EventsEmit(a.ctx, "journal:menu-journal-details", a.selectedJournalID)
	}
}

func (a *App) EmitDeleteJournalMenuAction() {
	if a.ctx != nil && a.selectedJournalID != "" {
		runtime.EventsEmit(a.ctx, "journal:menu-delete-journal", a.selectedJournalID)
	}
}

func (a *App) EmitLockJournalsMenuAction() {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "journal:menu-lock-journals")
	}
}

func (a *App) EmitImportJournalMenuAction() {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "journal:menu-import-journal")
}

func (a *App) EmitEmptyTrashMenuAction() {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "journal:menu-empty-trash")
	}
}

func (a *App) updateMenuState() {
	if a.importJournalItem != nil {
		a.importJournalItem.Enable()
	}
	tree, treeErr := a.commands.library.GetTree()
	status, statusErr := a.commands.encryption.Status()
	var selected *TreeItem
	for i := range tree.Items {
		if tree.Items[i].ID == a.selectedJournalID && tree.Items[i].Kind == KindJournal {
			selected = &tree.Items[i]
			break
		}
	}
	selectedOK := treeErr == nil && selected != nil
	if a.emptyTrashItem != nil {
		hasTrashContents := false
		for _, item := range tree.Items {
			if item.SystemKey == SystemTrash && item.ItemCount > 0 {
				hasTrashContents = true
				break
			}
		}
		if hasTrashContents {
			a.emptyTrashItem.Enable()
		} else {
			a.emptyTrashItem.Disable()
		}
	}
	if a.journalEncryptionItem != nil {
		label := "Encrypt Journal"
		enabled := selectedOK && selected.EncryptionState == EncryptionPlaintext
		if selectedOK && selected.EncryptionState == EncryptionEncrypted {
			label, enabled = "Un-Encrypt Journal", true
		}
		a.journalEncryptionItem.SetLabel(label)
		if enabled {
			a.journalEncryptionItem.Enable()
		} else {
			a.journalEncryptionItem.Disable()
		}
	}
	if a.journalDetailsItem != nil {
		if selectedOK {
			a.journalDetailsItem.Enable()
		} else {
			a.journalDetailsItem.Disable()
		}
	}
	if a.exportJournalItem != nil {
		if selectedOK {
			a.exportJournalItem.Enable()
		} else {
			a.exportJournalItem.Disable()
		}
	}
	if a.deleteJournalItem != nil {
		if selectedOK && len(tree.Items) > 1 {
			a.deleteJournalItem.Enable()
		} else {
			a.deleteJournalItem.Disable()
		}
	}
	if a.lockJournalsItem != nil {
		if statusErr == nil && status.Unlocked && len(status.EncryptedJournalIDs) > 0 {
			a.lockJournalsItem.Enable()
		} else {
			a.lockJournalsItem.Disable()
		}
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
