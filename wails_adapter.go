package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	journalEncryptionItem *menu.MenuItem
	journalDetailsItem    *menu.MenuItem
	deleteJournalItem     *menu.MenuItem
	lockJournalsItem      *menu.MenuItem
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

func (a *App) GetLibraryTree() (TreeResponse, error) {
	return a.commands.library.GetTree()
}

func (a *App) GetJournalDetails(journalID string) (JournalDetailsResponse, error) {
	return a.commands.library.GetJournalDetails(journalID)
}

func (a *App) CreateDocument(parentID string) (DocumentResponse, error) {
	return a.commands.documents.Create(parentID)
}

func (a *App) DuplicateDocument(id string) (DocumentResponse, error) {
	return a.commands.documents.Duplicate(id)
}

func (a *App) CreateFolder(parentID string, title string) (ItemResponse, error) {
	return a.commands.library.CreateFolder(parentID, title)
}

func (a *App) CreateJournal(title string) (ItemResponse, error) {
	return a.commands.library.CreateJournal(title)
}

func (a *App) RenameItem(id string, title string) (ItemResponse, error) {
	return a.commands.library.RenameItem(id, title)
}

func (a *App) MoveItem(id string, newParentID string, newSortOrder int) (TreeResponse, error) {
	return a.commands.library.MoveItem(id, newParentID, newSortOrder)
}

func (a *App) TrashItem(command TrashItemCommand) (TreeResponse, error) {
	return a.commands.library.TrashItem(command)
}

func (a *App) DeleteJournal(id string) (TreeResponse, error) {
	return a.commands.library.DeleteJournal(id)
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
	return a.commands.documents.UpdateDraft(id, content, version)
}

func (a *App) CreateDocumentAttachment(documentID string, name string, mimeType string, dataBase64 string) (DocumentAttachmentResponse, error) {
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
	return a.commands.documents.CreateAttachmentFromPath(documentID, path)
}

func (a *App) GetDocumentAttachmentDataURL(attachmentID string) (DocumentAttachmentDataResponse, error) {
	return a.commands.documents.AttachmentDataURL(attachmentID)
}

func (a *App) UpdateDocumentSpacing(id string, spacingPreset string) (DocumentSaveResponse, error) {
	return a.commands.documents.UpdateSpacing(id, spacingPreset)
}

func (a *App) FlushDocument(id string) (DocumentSaveResponse, error) {
	return a.commands.documents.Flush(id)
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

func (a *App) LockEncryptedJournals() (EncryptionStatusResponse, error) {
	if err := a.commands.encryption.Lock(); err != nil {
		return EncryptionStatusResponse{}, err
	}
	return a.commands.encryption.Status()
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
