package main

// Commands is the application boundary used by the Wails adapter. It keeps
// transport concerns out of the domain service and gives tests/automation one
// narrow place to exercise user-visible operations.
type Commands struct {
	library    LibraryCommands
	documents  DocumentCommands
	encryption EncryptionCommands
	settings   SettingsCommands
}

func NewCommands(service *JournalService) *Commands {
	return &Commands{
		library:    libraryCommandService{service: service},
		documents:  documentCommandService{service: service},
		encryption: encryptionCommandService{service: service},
		settings:   settingsCommandService{service: service},
	}
}

type LibraryCommands interface {
	GetTree() (TreeResponse, error)
	GetJournalDetails(journalID string) (JournalDetailsResponse, error)
	CreateFolder(parentID, title string) (ItemResponse, error)
	CreateJournal(title string) (ItemResponse, error)
	RenameItem(id, title string) (ItemResponse, error)
	MoveItem(id, newParentID string, newSortOrder int) (TreeResponse, error)
	TrashItem(command TrashItemCommand) (TreeResponse, error)
	DeleteJournal(id string) (TreeResponse, error)
	EmptyTrash() (TreeResponse, error)
	Search(query string) (SearchResponse, error)
	ExportJournal(journalID, targetDir string) error
	ImportMarkdownDirectory(sourceDir string) (ItemResponse, error)
}

type DocumentCommands interface {
	Create(parentID string) (DocumentResponse, error)
	Duplicate(id string) (DocumentResponse, error)
	Open(id string) (DocumentResponse, error)
	ExportMarkdown(id, targetPath string) error
	UpdateDraft(id string, content map[string]any, version int64) (DocumentDraftResponse, error)
	CreateAttachment(id, name, mimeType, dataBase64 string) (DocumentAttachmentResponse, error)
	CreateAttachmentFromPath(id, path string) (DocumentAttachmentResponse, error)
	AttachmentDataURL(id string) (DocumentAttachmentDataResponse, error)
	UpdateSpacing(id, spacingPreset string) (DocumentSaveResponse, error)
	Flush(id string) (DocumentSaveResponse, error)
}

type EncryptionCommands interface {
	Status() (EncryptionStatusResponse, error)
	CreateMasterPassword(password string) error
	Unlock(password string) error
	Lock() error
	ChangeMasterPassword(currentPassword, newPassword string) error
	EncryptJournal(journalID string) (TreeResponse, error)
	DecryptJournal(journalID string) (TreeResponse, error)
}

type SettingsCommands interface {
	Get() (AppSettingsResponse, error)
	Update(settings AppSettingsPatch) (AppSettingsResponse, error)
}

type libraryCommandService struct{ service *JournalService }

func (c libraryCommandService) GetTree() (TreeResponse, error) { return c.service.GetLibraryTree() }
func (c libraryCommandService) GetJournalDetails(journalID string) (JournalDetailsResponse, error) {
	return c.service.GetJournalDetails(journalID)
}
func (c libraryCommandService) CreateFolder(parentID, title string) (ItemResponse, error) {
	return c.service.CreateFolder(parentID, title)
}
func (c libraryCommandService) CreateJournal(title string) (ItemResponse, error) {
	return c.service.CreateJournal(title)
}
func (c libraryCommandService) RenameItem(id, title string) (ItemResponse, error) {
	return c.service.RenameItem(id, title)
}
func (c libraryCommandService) MoveItem(id, newParentID string, newSortOrder int) (TreeResponse, error) {
	return c.service.MoveItem(id, newParentID, newSortOrder)
}
func (c libraryCommandService) TrashItem(command TrashItemCommand) (TreeResponse, error) {
	return c.service.TrashItem(command)
}
func (c libraryCommandService) DeleteJournal(id string) (TreeResponse, error) {
	return c.service.DeleteJournal(id)
}
func (c libraryCommandService) EmptyTrash() (TreeResponse, error) { return c.service.EmptyTrash() }
func (c libraryCommandService) Search(query string) (SearchResponse, error) {
	return c.service.SearchLibrary(query)
}
func (c libraryCommandService) ExportJournal(journalID, targetDir string) error {
	return c.service.ExportJournalToDirectory(journalID, targetDir)
}
func (c libraryCommandService) ImportMarkdownDirectory(sourceDir string) (ItemResponse, error) {
	return c.service.ImportMarkdownDirectory(sourceDir)
}

type documentCommandService struct{ service *JournalService }

func (c documentCommandService) Create(parentID string) (DocumentResponse, error) {
	return c.service.CreateDocument(parentID)
}
func (c documentCommandService) Duplicate(id string) (DocumentResponse, error) {
	return c.service.DuplicateDocument(id)
}
func (c documentCommandService) Open(id string) (DocumentResponse, error) {
	return c.service.OpenDocument(id)
}
func (c documentCommandService) ExportMarkdown(id, targetPath string) error {
	return c.service.ExportDocumentToMarkdown(id, targetPath)
}
func (c documentCommandService) UpdateDraft(id string, content map[string]any, version int64) (DocumentDraftResponse, error) {
	return c.service.UpdateDocumentDraft(id, content, version)
}
func (c documentCommandService) CreateAttachment(id, name, mimeType, dataBase64 string) (DocumentAttachmentResponse, error) {
	return c.service.CreateDocumentAttachment(id, name, mimeType, dataBase64)
}
func (c documentCommandService) CreateAttachmentFromPath(id, path string) (DocumentAttachmentResponse, error) {
	return c.service.CreateDocumentAttachmentFromPath(id, path)
}
func (c documentCommandService) AttachmentDataURL(id string) (DocumentAttachmentDataResponse, error) {
	return c.service.GetDocumentAttachmentDataURL(id)
}
func (c documentCommandService) UpdateSpacing(id, spacingPreset string) (DocumentSaveResponse, error) {
	return c.service.UpdateDocumentSpacing(id, spacingPreset)
}
func (c documentCommandService) Flush(id string) (DocumentSaveResponse, error) {
	return c.service.FlushDocument(id)
}

type encryptionCommandService struct{ service *JournalService }

func (c encryptionCommandService) Status() (EncryptionStatusResponse, error) {
	return c.service.GetEncryptionStatus()
}
func (c encryptionCommandService) CreateMasterPassword(password string) error {
	return c.service.CreateMasterPassword(password)
}
func (c encryptionCommandService) Unlock(password string) error {
	return c.service.UnlockEncryption(password)
}
func (c encryptionCommandService) Lock() error {
	return c.service.LockEncryption()
}
func (c encryptionCommandService) ChangeMasterPassword(currentPassword, newPassword string) error {
	return c.service.ChangeMasterPassword(currentPassword, newPassword)
}
func (c encryptionCommandService) EncryptJournal(journalID string) (TreeResponse, error) {
	return c.service.EncryptJournal(journalID)
}
func (c encryptionCommandService) DecryptJournal(journalID string) (TreeResponse, error) {
	return c.service.DecryptJournal(journalID)
}

type settingsCommandService struct{ service *JournalService }

func (c settingsCommandService) Get() (AppSettingsResponse, error) { return c.service.GetAppSettings() }
func (c settingsCommandService) Update(settings AppSettingsPatch) (AppSettingsResponse, error) {
	return c.service.UpdateAppSettings(settings)
}
