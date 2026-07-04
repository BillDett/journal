package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	_ "modernc.org/sqlite"
)

const (
	KindJournal  = "journal"
	KindFolder   = "folder"
	KindDocument = "document"
	SystemTrash  = "trash"

	defaultAutosaveIntervalMS = 2000
	settingLastDocumentID     = "last_document_id"
	settingLibraryWidth       = "library_width"
	defaultLibraryWidth       = 340
	minLibraryWidth           = 260
	maxLibraryWidth           = 620
	defaultSpacingPreset      = "compact"
	maxImageAttachmentBytes   = 20 * 1024 * 1024
	detachedAttachmentGrace   = 24 * time.Hour

	defaultAppName    = "Journal"
	defaultAppVersion = "0.0.0-dev"
	appDisclaimer     = "Journal is free and open source software."
)

var appVersion = ""

type App struct {
	ctx               context.Context
	service           *JournalService
	selectedJournalID string
	exportJournalItem *menu.MenuItem
	importJournalItem *menu.MenuItem
}

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
	a.service.StartAutosave(ctx)
}

func (a *App) shutdown(ctx context.Context) {
	if a.service != nil {
		_ = a.service.FlushAll()
		_ = a.service.Close()
	}
}

func (a *App) GetLibraryTree() (TreeResponse, error) {
	return a.service.GetLibraryTree()
}

func (a *App) CreateDocument(parentID string) (DocumentResponse, error) {
	return a.service.CreateDocument(parentID)
}

func (a *App) CreateFolder(parentID string, title string) (ItemResponse, error) {
	return a.service.CreateFolder(parentID, title)
}

func (a *App) CreateJournal(title string) (ItemResponse, error) {
	return a.service.CreateJournal(title)
}

func (a *App) RenameItem(id string, title string) (ItemResponse, error) {
	return a.service.RenameItem(id, title)
}

func (a *App) MoveItem(id string, newParentID string, newSortOrder int) (TreeResponse, error) {
	return a.service.MoveItem(id, newParentID, newSortOrder)
}

func (a *App) MoveItemToTrash(id string) (TreeResponse, error) {
	return a.service.MoveItemToTrash(id)
}

func (a *App) PermanentlyDeleteItem(id string) (TreeResponse, error) {
	return a.service.PermanentlyDeleteItem(id)
}

func (a *App) DeleteJournal(id string) (TreeResponse, error) {
	return a.service.DeleteJournal(id)
}

func (a *App) OpenDocument(id string) (DocumentResponse, error) {
	return a.service.OpenDocument(id)
}

func (a *App) UpdateDocumentDraft(id string, content map[string]any, version int64) (DocumentDraftResponse, error) {
	return a.service.UpdateDocumentDraft(id, content, version)
}

func (a *App) CreateDocumentAttachment(documentID string, name string, mimeType string, dataBase64 string) (DocumentAttachmentResponse, error) {
	return a.service.CreateDocumentAttachment(documentID, name, mimeType, dataBase64)
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
	return a.service.CreateDocumentAttachmentFromPath(documentID, path)
}

func (a *App) GetDocumentAttachmentDataURL(attachmentID string) (DocumentAttachmentDataResponse, error) {
	return a.service.GetDocumentAttachmentDataURL(attachmentID)
}

func (a *App) UpdateDocumentSpacing(id string, spacingPreset string) (DocumentSaveResponse, error) {
	return a.service.UpdateDocumentSpacing(id, spacingPreset)
}

func (a *App) FlushDocument(id string) (DocumentSaveResponse, error) {
	return a.service.FlushDocument(id)
}

func (a *App) SearchLibrary(query string) (SearchResponse, error) {
	return a.service.SearchLibrary(query)
}

func (a *App) GetEncryptionStatus() (EncryptionStatusResponse, error) {
	return a.service.GetEncryptionStatus()
}

func (a *App) CreateMasterPassword(password string) error {
	return a.service.CreateMasterPassword(password)
}

func (a *App) UnlockEncryption(password string) (EncryptionStatusResponse, error) {
	if err := a.service.UnlockEncryption(password); err != nil {
		return EncryptionStatusResponse{}, err
	}
	return a.service.GetEncryptionStatus()
}

func (a *App) ChangeMasterPassword(currentPassword string, newPassword string) (EncryptionStatusResponse, error) {
	if err := a.service.ChangeMasterPassword(currentPassword, newPassword); err != nil {
		return EncryptionStatusResponse{}, err
	}
	return a.service.GetEncryptionStatus()
}

func (a *App) EncryptJournal(journalID string) (TreeResponse, error) {
	return a.service.EncryptJournal(journalID)
}

func (a *App) DecryptJournal(journalID string) (TreeResponse, error) {
	return a.service.DecryptJournal(journalID)
}

func (a *App) GetAppSettings() (AppSettingsResponse, error) {
	return a.service.GetAppSettings()
}

func (a *App) UpdateAppSettings(settings AppSettingsPatch) (AppSettingsResponse, error) {
	return a.service.UpdateAppSettings(settings)
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

type TreeItem struct {
	ID               string     `json:"id"`
	ParentID         string     `json:"parentId"`
	Kind             string     `json:"kind"`
	Title            string     `json:"title"`
	SortOrder        int        `json:"sortOrder"`
	SystemKey        string     `json:"systemKey"`
	CreatedAt        string     `json:"createdAt"`
	UpdatedAt        string     `json:"updatedAt"`
	EncryptionState  string     `json:"encryptionState"`
	EncryptionKeyID  string     `json:"encryptionKeyId"`
	EncryptionLocked bool       `json:"encryptionLocked"`
	DocumentCount    int        `json:"documentCount"`
	ItemCount        int        `json:"itemCount"`
	Children         []TreeItem `json:"children"`
}

type TreeResponse struct {
	Items   []TreeItem `json:"items"`
	TrashID string     `json:"trashId"`
}

type ItemResponse struct {
	Item TreeItem     `json:"item"`
	Tree TreeResponse `json:"tree"`
}

type DocumentResponse struct {
	ID            string         `json:"id"`
	Title         string         `json:"title"`
	Content       map[string]any `json:"content"`
	SpacingPreset string         `json:"spacingPreset"`
	SchemaVersion int            `json:"schemaVersion"`
	CreatedAt     string         `json:"createdAt"`
	UpdatedAt     string         `json:"updatedAt"`
	Item          TreeItem       `json:"item"`
	Tree          TreeResponse   `json:"tree"`
	SaveState     string         `json:"saveState"`
}

type DocumentDraftResponse struct {
	ID        string `json:"id"`
	SaveState string `json:"saveState"`
	Version   int64  `json:"version"`
}

type DocumentSaveResponse struct {
	ID        string `json:"id"`
	SaveState string `json:"saveState"`
	SavedAt   string `json:"savedAt"`
	UpdatedAt string `json:"updatedAt"`
	Version   int64  `json:"version"`
}

type DocumentAttachmentResponse struct {
	ID           string `json:"id"`
	DocumentID   string `json:"documentId"`
	MimeType     string `json:"mimeType"`
	OriginalName string `json:"originalName"`
	SizeBytes    int    `json:"sizeBytes"`
}

type DocumentAttachmentDataResponse struct {
	ID      string `json:"id"`
	DataURL string `json:"dataUrl"`
}

type SearchResponse struct {
	Query     string     `json:"query"`
	Items     []TreeItem `json:"items"`
	ResultIDs []string   `json:"resultIds"`
	TrashID   string     `json:"trashId"`
}

type AppSettingsResponse struct {
	AutosaveIntervalMS int    `json:"autosaveIntervalMs"`
	LastDocumentID     string `json:"lastDocumentId"`
	LibraryWidth       int    `json:"libraryWidth"`
}

type AppSettingsPatch struct {
	AutosaveIntervalMS int `json:"autosaveIntervalMs"`
	LibraryWidth       int `json:"libraryWidth"`
}

type rowItem struct {
	ID               string
	ParentID         sql.NullString
	Kind             string
	Title            string
	SortOrder        int
	SystemKey        sql.NullString
	CreatedAt        string
	UpdatedAt        string
	EncryptionState  string
	EncryptionKeyID  sql.NullString
	TitleCiphertext  []byte
	EncryptionLocked bool
}

type pendingDraft struct {
	Content   map[string]any
	UpdatedAt time.Time
	Version   int64
}

type JournalService struct {
	db               *sql.DB
	mu               sync.Mutex
	cryptoMu         sync.Mutex
	pending          map[string]pendingDraft
	lastDraftVersion map[string]int64
	masterKey        []byte
	journalKeys      map[string][]byte
}

func OpenJournalService(path string) (*JournalService, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	service := &JournalService{
		db:               db,
		pending:          map[string]pendingDraft{},
		lastDraftVersion: map[string]int64{},
		journalKeys:      map[string][]byte{},
	}
	if err := service.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return service, nil
}

func (s *JournalService) Close() error {
	return s.db.Close()
}

func (s *JournalService) migrate() error {
	if _, err := s.db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return err
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS items (
			id TEXT PRIMARY KEY,
			parent_id TEXT NULL REFERENCES items(id) ON DELETE CASCADE,
			kind TEXT NOT NULL CHECK (kind IN ('journal', 'folder', 'document')),
			title TEXT NOT NULL,
			sort_order INTEGER NOT NULL DEFAULT 0,
			system_key TEXT NULL UNIQUE,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			encryption_state TEXT NOT NULL DEFAULT 'plaintext',
			encryption_key_id TEXT NULL,
			title_ciphertext BLOB NULL
		)`,
		`CREATE TABLE IF NOT EXISTS documents (
			item_id TEXT PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
			schema_version INTEGER NOT NULL,
			content_json TEXT NOT NULL,
			content_ciphertext BLOB NULL,
			spacing_preset TEXT NOT NULL DEFAULT 'compact',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS library_search_fts USING fts5(
			item_id UNINDEXED,
			kind UNINDEXED,
			title,
			body
		)`,
		`CREATE TABLE IF NOT EXISTS app_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS document_attachments (
			id TEXT PRIMARY KEY,
			document_id TEXT NOT NULL REFERENCES documents(item_id) ON DELETE CASCADE,
			mime_type TEXT NOT NULL,
			original_name TEXT NOT NULL DEFAULT '',
			size_bytes INTEGER NOT NULL,
			content_blob BLOB NULL,
			content_ciphertext BLOB NULL,
			created_at TEXT NOT NULL,
			detached_at TEXT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_document_attachments_document ON document_attachments(document_id)`,
		`CREATE TABLE IF NOT EXISTS encryption_master (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			kdf TEXT NOT NULL,
			kdf_params_json TEXT NOT NULL,
			salt BLOB NOT NULL,
			verifier_nonce BLOB NOT NULL,
			verifier_ciphertext BLOB NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS journal_encryption_keys (
			key_id TEXT PRIMARY KEY,
			journal_id TEXT NOT NULL UNIQUE REFERENCES items(id) ON DELETE CASCADE,
			wrapped_key_nonce BLOB NOT NULL,
			wrapped_key_ciphertext BLOB NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_items_parent_sort ON items(parent_id, sort_order, title)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return err
		}
	}
	if err := s.migrateItemsKindConstraint(); err != nil {
		return err
	}
	if err := s.ensureEncryptionColumns(); err != nil {
		return err
	}
	if err := s.ensureAttachmentColumns(); err != nil {
		return err
	}
	if err := s.ensureTrash(); err != nil {
		return err
	}
	if err := s.ensureDefaultJournal(); err != nil {
		return err
	}
	if err := s.ensureSetting("autosave_interval_ms", fmt.Sprintf("%d", defaultAutosaveIntervalMS)); err != nil {
		return err
	}
	if err := s.ensureSetting(settingLibraryWidth, fmt.Sprintf("%d", defaultLibraryWidth)); err != nil {
		return err
	}
	return s.ensureSetting(settingLastDocumentID, "")
}

func (s *JournalService) ensureTrash() error {
	var id string
	err := s.db.QueryRow(`SELECT id FROM items WHERE system_key = ?`, SystemTrash).Scan(&id)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	now := nowString()
	id = uuid.NewString()
	_, err = s.db.Exec(
		`INSERT INTO items (id, parent_id, kind, title, sort_order, system_key, created_at, updated_at)
		 VALUES (?, NULL, ?, 'Trash', 999999, ?, ?, ?)`,
		id, KindFolder, SystemTrash, now, now,
	)
	if err != nil {
		return err
	}
	return s.syncFTS(s.db, id)
}

func (s *JournalService) migrateItemsKindConstraint() error {
	var schema string
	if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'items'`).Scan(&schema); err != nil {
		return err
	}
	if strings.Contains(schema, "'journal'") {
		return nil
	}

	if _, err := s.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer func() {
		_, _ = s.db.Exec(`PRAGMA foreign_keys = ON`)
	}()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer rollback(tx)

	statements := []string{
		`ALTER TABLE documents RENAME TO documents_legacy`,
		`ALTER TABLE items RENAME TO items_legacy`,
		`CREATE TABLE items (
			id TEXT PRIMARY KEY,
			parent_id TEXT NULL REFERENCES items(id) ON DELETE CASCADE,
			kind TEXT NOT NULL CHECK (kind IN ('journal', 'folder', 'document')),
			title TEXT NOT NULL,
			sort_order INTEGER NOT NULL DEFAULT 0,
			system_key TEXT NULL UNIQUE,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			encryption_state TEXT NOT NULL DEFAULT 'plaintext',
			encryption_key_id TEXT NULL,
			title_ciphertext BLOB NULL
		)`,
		`CREATE TABLE documents (
			item_id TEXT PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
			schema_version INTEGER NOT NULL,
			content_json TEXT NOT NULL,
			content_ciphertext BLOB NULL,
			spacing_preset TEXT NOT NULL DEFAULT 'compact',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`INSERT INTO items (id, parent_id, kind, title, sort_order, system_key, created_at, updated_at, encryption_state)
		 SELECT id, parent_id, kind, title, sort_order, system_key, created_at, updated_at, 'plaintext' FROM items_legacy`,
		`INSERT INTO documents (item_id, schema_version, content_json, created_at, updated_at)
		 SELECT item_id, schema_version, content_json, created_at, updated_at FROM documents_legacy`,
		`DROP TABLE documents_legacy`,
		`DROP TABLE items_legacy`,
		`CREATE INDEX IF NOT EXISTS idx_items_parent_sort ON items(parent_id, sort_order, title)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *JournalService) ensureEncryptionColumns() error {
	itemColumns, err := tableColumns(s.db, "items")
	if err != nil {
		return err
	}
	itemStatements := map[string]string{
		"encryption_state":  `ALTER TABLE items ADD COLUMN encryption_state TEXT NOT NULL DEFAULT 'plaintext'`,
		"encryption_key_id": `ALTER TABLE items ADD COLUMN encryption_key_id TEXT NULL`,
		"title_ciphertext":  `ALTER TABLE items ADD COLUMN title_ciphertext BLOB NULL`,
	}
	for name, statement := range itemStatements {
		if !itemColumns[name] {
			if _, err := s.db.Exec(statement); err != nil {
				return err
			}
		}
	}
	documentColumns, err := tableColumns(s.db, "documents")
	if err != nil {
		return err
	}
	if !documentColumns["content_ciphertext"] {
		if _, err := s.db.Exec(`ALTER TABLE documents ADD COLUMN content_ciphertext BLOB NULL`); err != nil {
			return err
		}
	}
	if !documentColumns["spacing_preset"] {
		if _, err := s.db.Exec(`ALTER TABLE documents ADD COLUMN spacing_preset TEXT NOT NULL DEFAULT 'compact'`); err != nil {
			return err
		}
	}
	return nil
}

func (s *JournalService) ensureAttachmentColumns() error {
	attachmentColumns, err := tableColumns(s.db, "document_attachments")
	if err != nil {
		return err
	}
	if !attachmentColumns["detached_at"] {
		if _, err := s.db.Exec(`ALTER TABLE document_attachments ADD COLUMN detached_at TEXT NULL`); err != nil {
			return err
		}
	}
	return nil
}

func tableColumns(db queryer, table string) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func (s *JournalService) ensureDefaultJournal() error {
	journalID, err := s.firstJournalID()
	if err != nil {
		return err
	}
	if journalID == "" {
		now := nowString()
		journalID = uuid.NewString()
		order, err := s.nextJournalSortOrder()
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(
			`INSERT INTO items (id, parent_id, kind, title, sort_order, created_at, updated_at)
			 VALUES (?, NULL, ?, 'New Journal', ?, ?, ?)`,
			journalID, KindJournal, order, now, now,
		); err != nil {
			return err
		}
		if err := s.syncFTS(s.db, journalID); err != nil {
			return err
		}
	}

	_, err = s.db.Exec(
		`UPDATE items
		 SET parent_id = ?
		 WHERE parent_id IS NULL
		   AND kind != ?
		   AND COALESCE(system_key, '') != ?`,
		journalID, KindJournal, SystemTrash,
	)
	return err
}

func (s *JournalService) ensureSetting(key, value string) error {
	now := nowString()
	_, err := s.db.Exec(
		`INSERT INTO app_settings (key, value, updated_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(key) DO NOTHING`,
		key, value, now,
	)
	return err
}

func (s *JournalService) settingValue(key string) string {
	var value string
	if err := s.db.QueryRow(`SELECT value FROM app_settings WHERE key = ?`, key).Scan(&value); err != nil {
		return ""
	}
	return value
}

func (s *JournalService) rememberLastDocument(id string) error {
	now := nowString()
	_, err := s.db.Exec(
		`INSERT INTO app_settings (key, value, updated_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		settingLastDocumentID, id, now,
	)
	return err
}

func (s *JournalService) StartAutosave(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = s.FlushAll()
				return
			case <-ticker.C:
				interval := s.autosaveInterval()
				for _, id := range s.pendingIDsOlderThan(interval) {
					_, _ = s.FlushDocument(id)
				}
			}
		}
	}()
}

func (s *JournalService) GetLibraryTree() (TreeResponse, error) {
	items, err := s.loadItems()
	if err != nil {
		return TreeResponse{}, err
	}
	trashID, err := s.trashID()
	if err != nil {
		return TreeResponse{}, err
	}
	return TreeResponse{Items: buildTree(items, nil), TrashID: trashID}, nil
}

func (s *JournalService) CreateDocument(parentID string) (DocumentResponse, error) {
	parentID, err := s.resolveCreateParent(parentID)
	if err != nil {
		return DocumentResponse{}, err
	}
	now := nowString()
	id := uuid.NewString()
	content := emptyDocument()
	encoded, _ := json.Marshal(content)
	order, err := s.nextSortOrder(parentID)
	if err != nil {
		return DocumentResponse{}, err
	}
	_, keyID, key, encrypted, err := s.encryptionContextForParent(parentID)
	if err != nil {
		return DocumentResponse{}, err
	}
	title := "Untitled"
	contentJSON := string(encoded)
	var titleCiphertext []byte
	var contentCiphertext []byte
	encryptionState := EncryptionPlaintext
	encryptionKeyID := sql.NullString{}
	if encrypted {
		encryptionState = EncryptionEncrypted
		encryptionKeyID = sql.NullString{String: keyID, Valid: true}
		titleCiphertext, err = sealField(key, "items", id, "title", keyID, []byte(title))
		if err != nil {
			return DocumentResponse{}, err
		}
		contentCiphertext, err = sealField(key, "documents", id, "content_json", keyID, encoded)
		if err != nil {
			return DocumentResponse{}, err
		}
		title = encryptedTitlePlaceholder(KindDocument)
		placeholder, _ := json.Marshal(emptyDocument())
		contentJSON = string(placeholder)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return DocumentResponse{}, err
	}
	defer rollback(tx)

	if _, err := tx.Exec(
		`INSERT INTO items (id, parent_id, kind, title, sort_order, created_at, updated_at, encryption_state, encryption_key_id, title_ciphertext)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, parentID, KindDocument, title, order, now, now, encryptionState, encryptionKeyID, titleCiphertext,
	); err != nil {
		return DocumentResponse{}, err
	}
	if _, err := tx.Exec(
		`INSERT INTO documents (item_id, schema_version, content_json, content_ciphertext, spacing_preset, created_at, updated_at)
		 VALUES (?, 1, ?, ?, ?, ?, ?)`,
		id, contentJSON, contentCiphertext, defaultSpacingPreset, now, now,
	); err != nil {
		return DocumentResponse{}, err
	}
	if err := s.syncFTSTx(tx, id); err != nil {
		return DocumentResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return DocumentResponse{}, err
	}
	return s.OpenDocument(id)
}

func (s *JournalService) CreateFolder(parentID string, title string) (ItemResponse, error) {
	parentID, err := s.resolveCreateParent(parentID)
	if err != nil {
		return ItemResponse{}, err
	}
	title = normalizeTitle(title, "New Folder")
	now := nowString()
	id := uuid.NewString()
	order, err := s.nextSortOrder(parentID)
	if err != nil {
		return ItemResponse{}, err
	}
	_, keyID, key, encrypted, err := s.encryptionContextForParent(parentID)
	if err != nil {
		return ItemResponse{}, err
	}
	storedTitle := title
	var titleCiphertext []byte
	encryptionState := EncryptionPlaintext
	encryptionKeyID := sql.NullString{}
	if encrypted {
		encryptionState = EncryptionEncrypted
		encryptionKeyID = sql.NullString{String: keyID, Valid: true}
		titleCiphertext, err = sealField(key, "items", id, "title", keyID, []byte(title))
		if err != nil {
			return ItemResponse{}, err
		}
		storedTitle = encryptedTitlePlaceholder(KindFolder)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return ItemResponse{}, err
	}
	defer rollback(tx)
	if _, err := tx.Exec(
		`INSERT INTO items (id, parent_id, kind, title, sort_order, created_at, updated_at, encryption_state, encryption_key_id, title_ciphertext)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, parentID, KindFolder, storedTitle, order, now, now, encryptionState, encryptionKeyID, titleCiphertext,
	); err != nil {
		return ItemResponse{}, err
	}
	if err := s.syncFTSTx(tx, id); err != nil {
		return ItemResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return ItemResponse{}, err
	}
	item, err := s.getTreeItem(id)
	if err != nil {
		return ItemResponse{}, err
	}
	tree, err := s.GetLibraryTree()
	return ItemResponse{Item: item, Tree: tree}, err
}

func (s *JournalService) CreateJournal(title string) (ItemResponse, error) {
	title = normalizeTitle(title, "New Journal")
	now := nowString()
	id := uuid.NewString()
	order, err := s.nextJournalSortOrder()
	if err != nil {
		return ItemResponse{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return ItemResponse{}, err
	}
	defer rollback(tx)
	if _, err := tx.Exec(
		`INSERT INTO items (id, parent_id, kind, title, sort_order, created_at, updated_at)
		 VALUES (?, NULL, ?, ?, ?, ?, ?)`,
		id, KindJournal, title, order, now, now,
	); err != nil {
		return ItemResponse{}, err
	}
	if err := s.syncFTSTx(tx, id); err != nil {
		return ItemResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return ItemResponse{}, err
	}
	item, err := s.getTreeItem(id)
	if err != nil {
		return ItemResponse{}, err
	}
	tree, err := s.GetLibraryTree()
	return ItemResponse{Item: item, Tree: tree}, err
}

func (s *JournalService) RenameItem(id string, title string) (ItemResponse, error) {
	title = normalizeTitle(title, "Untitled")
	item, err := s.getRawRowItemFrom(s.db, id)
	if err != nil {
		return ItemResponse{}, err
	}
	if item.SystemKey.String == SystemTrash {
		return ItemResponse{}, fmt.Errorf("trash cannot be renamed")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return ItemResponse{}, err
	}
	defer rollback(tx)
	now := nowString()
	if item.EncryptionState == EncryptionEncrypted && item.Kind != KindJournal {
		journalID, err := s.journalIDForItemFrom(tx, id)
		if err != nil {
			return ItemResponse{}, err
		}
		key, ok := s.journalKey(journalID)
		if !ok {
			return ItemResponse{}, ErrEncryptionLocked
		}
		if !item.EncryptionKeyID.Valid {
			return ItemResponse{}, fmt.Errorf("encrypted item key is missing")
		}
		titleCiphertext, err := sealField(key, "items", id, "title", item.EncryptionKeyID.String, []byte(title))
		if err != nil {
			return ItemResponse{}, err
		}
		if _, err := tx.Exec(`UPDATE items SET title = ?, title_ciphertext = ?, updated_at = ? WHERE id = ?`, encryptedTitlePlaceholder(item.Kind), titleCiphertext, now, id); err != nil {
			return ItemResponse{}, err
		}
	} else {
		if _, err := tx.Exec(`UPDATE items SET title = ?, updated_at = ? WHERE id = ?`, title, now, id); err != nil {
			return ItemResponse{}, err
		}
	}
	if err := s.syncFTSTx(tx, id); err != nil {
		return ItemResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return ItemResponse{}, err
	}
	next, err := s.getTreeItem(id)
	if err != nil {
		return ItemResponse{}, err
	}
	tree, err := s.GetLibraryTree()
	return ItemResponse{Item: next, Tree: tree}, err
}

func (s *JournalService) MoveItem(id string, newParentID string, newSortOrder int) (TreeResponse, error) {
	id = strings.TrimSpace(id)
	newParentID = strings.TrimSpace(newParentID)
	item, err := s.getRowItem(id)
	if err != nil {
		return TreeResponse{}, err
	}
	if item.Kind == KindJournal {
		return s.moveJournal(id, newSortOrder)
	}
	if err := s.validateMove(id, newParentID); err != nil {
		return TreeResponse{}, err
	}
	trashID, err := s.trashID()
	if err != nil {
		return TreeResponse{}, err
	}
	if newParentID != trashID {
		sourceKeyID, err := s.encryptionBoundaryKeyID(id)
		if err != nil {
			return TreeResponse{}, err
		}
		targetKeyID, err := s.encryptionBoundaryKeyID(newParentID)
		if err != nil {
			return TreeResponse{}, err
		}
		if sourceKeyID != targetKeyID {
			return TreeResponse{}, fmt.Errorf("items cannot be moved between encrypted and plaintext journals")
		}
	}

	sourceJournalID, err := s.journalIDForItem(id)
	if err != nil {
		return TreeResponse{}, err
	}
	targetJournalID, err := s.journalIDForItem(newParentID)
	if err != nil {
		return TreeResponse{}, err
	}
	inTrash, err := s.isInTrash(id)
	if err != nil {
		return TreeResponse{}, err
	}
	if !inTrash && sourceJournalID != "" && targetJournalID != "" && sourceJournalID != targetJournalID {
		tx, err := s.db.Begin()
		if err != nil {
			return TreeResponse{}, err
		}
		defer rollback(tx)
		if _, err := s.copyItemToParentTx(tx, id, newParentID, nowString()); err != nil {
			return TreeResponse{}, err
		}
		if err := tx.Commit(); err != nil {
			return TreeResponse{}, err
		}
		return s.GetLibraryTree()
	}

	tx, err := s.db.Begin()
	if err != nil {
		return TreeResponse{}, err
	}
	defer rollback(tx)

	now := nowString()
	if _, err := tx.Exec(
		`UPDATE items SET parent_id = ?, updated_at = ? WHERE id = ?`,
		newParentID, now, id,
	); err != nil {
		return TreeResponse{}, err
	}
	if err := reorderSiblings(tx, newParentID, id, newSortOrder); err != nil {
		return TreeResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return TreeResponse{}, err
	}
	return s.GetLibraryTree()
}

func (s *JournalService) MoveItemToTrash(id string) (TreeResponse, error) {
	trashID, err := s.trashID()
	if err != nil {
		return TreeResponse{}, err
	}
	if id == trashID {
		return TreeResponse{}, fmt.Errorf("trash cannot be deleted")
	}
	inTrash, err := s.isInTrash(id)
	if err != nil {
		return TreeResponse{}, err
	}
	if inTrash {
		return s.PermanentlyDeleteItem(id)
	}
	return s.MoveItem(id, trashID, -1)
}

func (s *JournalService) PermanentlyDeleteItem(id string) (TreeResponse, error) {
	deletedIDs, _ := descendantIDs(s.db, id)
	deletedIDs = append(deletedIDs, id)
	trashID, err := s.trashID()
	if err != nil {
		return TreeResponse{}, err
	}
	if id == trashID {
		return TreeResponse{}, fmt.Errorf("trash cannot be permanently deleted")
	}
	inTrash, err := s.isInTrash(id)
	if err != nil {
		return TreeResponse{}, err
	}
	if !inTrash {
		return TreeResponse{}, fmt.Errorf("item must be in trash before permanent deletion")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return TreeResponse{}, err
	}
	defer rollback(tx)
	if err := s.deleteFTSDescendantsTx(tx, id); err != nil {
		return TreeResponse{}, err
	}
	if _, err := tx.Exec(`DELETE FROM items WHERE id = ?`, id); err != nil {
		return TreeResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return TreeResponse{}, err
	}
	s.removePendingIDs(deletedIDs)
	return s.GetLibraryTree()
}

func (s *JournalService) DeleteJournal(id string) (TreeResponse, error) {
	deletedIDs, _ := descendantIDs(s.db, id)
	deletedIDs = append(deletedIDs, id)
	item, err := s.getRowItem(id)
	if err != nil {
		return TreeResponse{}, err
	}
	if item.Kind != KindJournal {
		return TreeResponse{}, fmt.Errorf("item is not a journal")
	}
	count, err := s.journalCount()
	if err != nil {
		return TreeResponse{}, err
	}
	if count <= 1 {
		return TreeResponse{}, fmt.Errorf("at least one journal is required")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return TreeResponse{}, err
	}
	defer rollback(tx)
	if err := s.deleteFTSDescendantsTx(tx, id); err != nil {
		return TreeResponse{}, err
	}
	if _, err := tx.Exec(`DELETE FROM items WHERE id = ?`, id); err != nil {
		return TreeResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return TreeResponse{}, err
	}
	s.removePendingIDs(deletedIDs)
	if s.settingValue(settingLastDocumentID) != "" {
		if _, err := s.getRowItem(s.settingValue(settingLastDocumentID)); err != nil {
			_ = s.rememberLastDocument("")
		}
	}
	return s.GetLibraryTree()
}

func (s *JournalService) OpenDocument(id string) (DocumentResponse, error) {
	rawItem, err := s.getRawRowItemFrom(s.db, id)
	if err != nil {
		return DocumentResponse{}, err
	}
	if rawItem.Kind != KindDocument {
		return DocumentResponse{}, fmt.Errorf("item is not a document")
	}

	var encoded string
	var schemaVersion int
	var spacingPreset string
	if rawItem.EncryptionState == EncryptionEncrypted {
		journalID, err := s.journalIDForItem(id)
		if err != nil {
			return DocumentResponse{}, err
		}
		key, ok := s.journalKey(journalID)
		if !ok {
			return DocumentResponse{}, ErrEncryptionLocked
		}
		if !rawItem.EncryptionKeyID.Valid {
			return DocumentResponse{}, fmt.Errorf("encrypted document key is missing")
		}
		var contentCiphertext []byte
		err = s.db.QueryRow(
			`SELECT schema_version, content_ciphertext, spacing_preset FROM documents WHERE item_id = ?`,
			id,
		).Scan(&schemaVersion, &contentCiphertext, &spacingPreset)
		if err != nil {
			return DocumentResponse{}, err
		}
		plaintext, err := openField(key, "documents", id, "content_json", rawItem.EncryptionKeyID.String, contentCiphertext)
		if err != nil {
			return DocumentResponse{}, err
		}
		encoded = string(plaintext)
	} else {
		err = s.db.QueryRow(
			`SELECT schema_version, content_json, spacing_preset FROM documents WHERE item_id = ?`,
			id,
		).Scan(&schemaVersion, &encoded, &spacingPreset)
		if err != nil {
			return DocumentResponse{}, err
		}
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(encoded), &content); err != nil {
		return DocumentResponse{}, err
	}
	item, err := s.prepareItemForDisplay(rawItem)
	if err != nil {
		return DocumentResponse{}, err
	}
	tree, err := s.GetLibraryTree()
	if err != nil {
		return DocumentResponse{}, err
	}
	if err := s.rememberLastDocument(id); err != nil {
		return DocumentResponse{}, err
	}
	return DocumentResponse{
		ID:            id,
		Title:         item.Title,
		Content:       content,
		SpacingPreset: normalizeSpacingPreset(spacingPreset),
		SchemaVersion: schemaVersion,
		CreatedAt:     item.CreatedAt,
		UpdatedAt:     item.UpdatedAt,
		Item:          treeItemFromRow(item),
		Tree:          tree,
		SaveState:     "saved",
	}, nil
}

func (s *JournalService) UpdateDocumentDraft(id string, content map[string]any, version int64) (DocumentDraftResponse, error) {
	if version < 1 {
		return DocumentDraftResponse{}, fmt.Errorf("draft version must be positive")
	}
	if err := validateProseMirrorDoc(content); err != nil {
		return DocumentDraftResponse{}, err
	}
	item, err := s.getRowItem(id)
	if err != nil {
		return DocumentDraftResponse{}, err
	}
	if item.Kind != KindDocument {
		return DocumentDraftResponse{}, fmt.Errorf("item is not a document")
	}
	s.mu.Lock()
	if version <= s.lastDraftVersion[id] {
		currentVersion := s.lastDraftVersion[id]
		s.mu.Unlock()
		return DocumentDraftResponse{ID: id, SaveState: "dirty", Version: currentVersion}, nil
	}
	s.pending[id] = pendingDraft{Content: cloneMap(content), UpdatedAt: time.Now(), Version: version}
	s.lastDraftVersion[id] = version
	s.mu.Unlock()
	return DocumentDraftResponse{ID: id, SaveState: "dirty", Version: version}, nil
}

func (s *JournalService) CreateDocumentAttachmentFromPath(documentID string, path string) (DocumentAttachmentResponse, error) {
	info, err := os.Stat(path)
	if err != nil {
		return DocumentAttachmentResponse{}, err
	}
	if info.Size() > maxImageAttachmentBytes {
		return DocumentAttachmentResponse{}, fmt.Errorf("image is larger than the 20 MB limit")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return DocumentAttachmentResponse{}, err
	}
	return s.createDocumentAttachment(documentID, filepath.Base(path), "", data)
}

func (s *JournalService) CreateDocumentAttachment(documentID string, name string, mimeType string, dataBase64 string) (DocumentAttachmentResponse, error) {
	dataBase64 = strings.TrimSpace(dataBase64)
	if comma := strings.Index(dataBase64, ","); comma >= 0 && strings.Contains(dataBase64[:comma], "base64") {
		dataBase64 = dataBase64[comma+1:]
	}
	data, err := base64.StdEncoding.DecodeString(dataBase64)
	if err != nil {
		return DocumentAttachmentResponse{}, fmt.Errorf("image data must be base64 encoded")
	}
	return s.createDocumentAttachment(documentID, name, mimeType, data)
}

func (s *JournalService) createDocumentAttachment(documentID string, name string, mimeType string, data []byte) (DocumentAttachmentResponse, error) {
	documentID = strings.TrimSpace(documentID)
	if documentID == "" {
		return DocumentAttachmentResponse{}, fmt.Errorf("document id is required")
	}
	if len(data) == 0 {
		return DocumentAttachmentResponse{}, fmt.Errorf("image is empty")
	}
	if len(data) > maxImageAttachmentBytes {
		return DocumentAttachmentResponse{}, fmt.Errorf("image is larger than the 20 MB limit")
	}
	item, err := s.getRawRowItemFrom(s.db, documentID)
	if err != nil {
		return DocumentAttachmentResponse{}, err
	}
	if item.Kind != KindDocument {
		return DocumentAttachmentResponse{}, fmt.Errorf("item is not a document")
	}
	mimeType = normalizeImageMimeType(name, mimeType, data)
	if mimeType == "" {
		return DocumentAttachmentResponse{}, fmt.Errorf("unsupported image format")
	}
	id := uuid.NewString()
	now := nowString()
	contentBlob := data
	var contentCiphertext []byte
	if item.EncryptionState == EncryptionEncrypted {
		journalID, err := s.journalIDForItem(documentID)
		if err != nil {
			return DocumentAttachmentResponse{}, err
		}
		key, ok := s.journalKey(journalID)
		if !ok {
			return DocumentAttachmentResponse{}, ErrEncryptionLocked
		}
		if !item.EncryptionKeyID.Valid {
			return DocumentAttachmentResponse{}, fmt.Errorf("encrypted document key is missing")
		}
		contentCiphertext, err = sealField(key, "document_attachments", id, "content_blob", item.EncryptionKeyID.String, data)
		if err != nil {
			return DocumentAttachmentResponse{}, err
		}
		contentBlob = nil
	}
	name = strings.TrimSpace(filepath.Base(name))
	if name == "." || name == string(filepath.Separator) {
		name = ""
	}
	if _, err := s.db.Exec(
		`INSERT INTO document_attachments (id, document_id, mime_type, original_name, size_bytes, content_blob, content_ciphertext, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, documentID, mimeType, name, len(data), contentBlob, contentCiphertext, now,
	); err != nil {
		return DocumentAttachmentResponse{}, err
	}
	return DocumentAttachmentResponse{ID: id, DocumentID: documentID, MimeType: mimeType, OriginalName: name, SizeBytes: len(data)}, nil
}

func (s *JournalService) GetDocumentAttachmentDataURL(attachmentID string) (DocumentAttachmentDataResponse, error) {
	attachmentID = strings.TrimSpace(attachmentID)
	if attachmentID == "" {
		return DocumentAttachmentDataResponse{}, fmt.Errorf("attachment id is required")
	}
	var documentID, mimeType string
	var contentBlob []byte
	var contentCiphertext []byte
	if err := s.db.QueryRow(
		`SELECT document_id, mime_type, content_blob, content_ciphertext FROM document_attachments WHERE id = ?`,
		attachmentID,
	).Scan(&documentID, &mimeType, &contentBlob, &contentCiphertext); err != nil {
		return DocumentAttachmentDataResponse{}, err
	}
	item, err := s.getRawRowItemFrom(s.db, documentID)
	if err != nil {
		return DocumentAttachmentDataResponse{}, err
	}
	data := contentBlob
	if item.EncryptionState == EncryptionEncrypted {
		journalID, err := s.journalIDForItem(documentID)
		if err != nil {
			return DocumentAttachmentDataResponse{}, err
		}
		key, ok := s.journalKey(journalID)
		if !ok {
			return DocumentAttachmentDataResponse{}, ErrEncryptionLocked
		}
		if !item.EncryptionKeyID.Valid {
			return DocumentAttachmentDataResponse{}, fmt.Errorf("encrypted document key is missing")
		}
		data, err = openField(key, "document_attachments", attachmentID, "content_blob", item.EncryptionKeyID.String, contentCiphertext)
		if err != nil {
			return DocumentAttachmentDataResponse{}, err
		}
	}
	return DocumentAttachmentDataResponse{
		ID:      attachmentID,
		DataURL: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data),
	}, nil
}

func (s *JournalService) UpdateDocumentSpacing(id string, spacingPreset string) (DocumentSaveResponse, error) {
	spacingPreset = normalizeSpacingPreset(spacingPreset)
	item, err := s.getRowItem(id)
	if err != nil {
		return DocumentSaveResponse{}, err
	}
	if item.Kind != KindDocument {
		return DocumentSaveResponse{}, fmt.Errorf("item is not a document")
	}
	now := nowString()
	tx, err := s.db.Begin()
	if err != nil {
		return DocumentSaveResponse{}, err
	}
	defer rollback(tx)
	if _, err := tx.Exec(`UPDATE documents SET spacing_preset = ?, updated_at = ? WHERE item_id = ?`, spacingPreset, now, id); err != nil {
		return DocumentSaveResponse{}, err
	}
	if _, err := tx.Exec(`UPDATE items SET updated_at = ? WHERE id = ?`, now, id); err != nil {
		return DocumentSaveResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return DocumentSaveResponse{}, err
	}
	s.mu.Lock()
	version := s.lastDraftVersion[id]
	s.mu.Unlock()
	return DocumentSaveResponse{ID: id, SaveState: "saved", SavedAt: now, UpdatedAt: now, Version: version}, nil
}

func (s *JournalService) FlushDocument(id string) (DocumentSaveResponse, error) {
	s.mu.Lock()
	draft, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if !ok {
		item, err := s.getRowItem(id)
		if err != nil {
			return DocumentSaveResponse{}, err
		}
		s.mu.Lock()
		version := s.lastDraftVersion[id]
		s.mu.Unlock()
		return DocumentSaveResponse{ID: id, SaveState: "saved", SavedAt: nowString(), UpdatedAt: item.UpdatedAt, Version: version}, nil
	}
	updatedAt, err := s.saveDocumentContent(id, draft.Content)
	if err != nil {
		s.mu.Lock()
		current, exists := s.pending[id]
		if !exists || current.Version < draft.Version {
			s.pending[id] = draft
		}
		if s.lastDraftVersion[id] < draft.Version {
			s.lastDraftVersion[id] = draft.Version
		}
		s.mu.Unlock()
		return DocumentSaveResponse{}, err
	}
	s.mu.Lock()
	if s.lastDraftVersion[id] < draft.Version {
		s.lastDraftVersion[id] = draft.Version
	}
	s.mu.Unlock()
	return DocumentSaveResponse{ID: id, SaveState: "saved", SavedAt: updatedAt, UpdatedAt: updatedAt, Version: draft.Version}, nil
}

func (s *JournalService) FlushAll() error {
	ids := s.pendingIDsOlderThan(0)
	for _, id := range ids {
		if _, err := s.FlushDocument(id); err != nil {
			return err
		}
	}
	return nil
}

func (s *JournalService) SearchLibrary(query string) (SearchResponse, error) {
	query = strings.TrimSpace(query)
	trashID, err := s.trashID()
	if err != nil {
		return SearchResponse{}, err
	}
	if query == "" {
		tree, err := s.GetLibraryTree()
		if err != nil {
			return SearchResponse{}, err
		}
		return SearchResponse{Items: tree.Items, TrashID: tree.TrashID}, nil
	}
	rows, err := s.db.Query(
		`SELECT item_id FROM library_search_fts WHERE library_search_fts MATCH ? ORDER BY rank`,
		ftsPhrase(query),
	)
	if err != nil {
		return SearchResponse{}, err
	}
	defer rows.Close()
	matches := map[string]bool{}
	resultIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return SearchResponse{}, err
		}
		if !matches[id] {
			matches[id] = true
			resultIDs = append(resultIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		return SearchResponse{}, err
	}

	items, err := s.loadItems()
	if err != nil {
		return SearchResponse{}, err
	}
	include := map[string]bool{}
	byID := map[string]rowItem{}
	for _, item := range items {
		byID[item.ID] = item
	}
	filteredMatches := map[string]bool{}
	filteredResultIDs := make([]string, 0, len(resultIDs))
	for _, id := range resultIDs {
		if rowIsInTrash(byID, id, trashID) {
			continue
		}
		filteredMatches[id] = true
		filteredResultIDs = append(filteredResultIDs, id)
	}
	for id := range filteredMatches {
		for {
			item, ok := byID[id]
			if !ok {
				break
			}
			include[item.ID] = true
			if !item.ParentID.Valid {
				break
			}
			id = item.ParentID.String
		}
	}
	filtered := make([]rowItem, 0, len(include))
	for _, item := range items {
		if include[item.ID] {
			filtered = append(filtered, item)
		}
	}
	return SearchResponse{
		Query:     query,
		Items:     buildTree(filtered, filteredMatches),
		ResultIDs: filteredResultIDs,
		TrashID:   trashID,
	}, nil
}

func (s *JournalService) GetAppSettings() (AppSettingsResponse, error) {
	return AppSettingsResponse{
		AutosaveIntervalMS: s.autosaveIntervalMS(),
		LastDocumentID:     s.settingValue(settingLastDocumentID),
		LibraryWidth:       s.libraryWidth(),
	}, nil
}

func (s *JournalService) UpdateAppSettings(settings AppSettingsPatch) (AppSettingsResponse, error) {
	autosaveIntervalMS := settings.AutosaveIntervalMS
	if autosaveIntervalMS < 500 {
		autosaveIntervalMS = defaultAutosaveIntervalMS
	}
	libraryWidth := s.libraryWidth()
	if settings.LibraryWidth > 0 {
		libraryWidth = clampInt(settings.LibraryWidth, minLibraryWidth, maxLibraryWidth)
	}

	now := nowString()
	if _, err := s.db.Exec(
		`INSERT INTO app_settings (key, value, updated_at)
		 VALUES ('autosave_interval_ms', ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		fmt.Sprintf("%d", autosaveIntervalMS), now,
	); err != nil {
		return AppSettingsResponse{}, err
	}
	if _, err := s.db.Exec(
		`INSERT INTO app_settings (key, value, updated_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		settingLibraryWidth, fmt.Sprintf("%d", libraryWidth), now,
	); err != nil {
		return AppSettingsResponse{}, err
	}
	return s.GetAppSettings()
}

func (s *JournalService) saveDocumentContent(id string, content map[string]any) (string, error) {
	if err := validateProseMirrorDoc(content); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	item, err := s.getRawRowItemFrom(s.db, id)
	if err != nil {
		return "", err
	}
	contentJSON := string(encoded)
	var contentCiphertext []byte
	if item.EncryptionState == EncryptionEncrypted {
		journalID, err := s.journalIDForItem(id)
		if err != nil {
			return "", err
		}
		key, ok := s.journalKey(journalID)
		if !ok {
			return "", ErrEncryptionLocked
		}
		if !item.EncryptionKeyID.Valid {
			return "", fmt.Errorf("encrypted document key is missing")
		}
		contentCiphertext, err = sealField(key, "documents", id, "content_json", item.EncryptionKeyID.String, encoded)
		if err != nil {
			return "", err
		}
		placeholder, _ := json.Marshal(emptyDocument())
		contentJSON = string(placeholder)
	}
	now := nowString()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer rollback(tx)
	if item.EncryptionState == EncryptionEncrypted {
		if _, err := tx.Exec(
			`UPDATE documents SET content_json = ?, content_ciphertext = ?, updated_at = ? WHERE item_id = ?`,
			contentJSON, contentCiphertext, now, id,
		); err != nil {
			return "", err
		}
	} else {
		if _, err := tx.Exec(
			`UPDATE documents SET content_json = ?, content_ciphertext = NULL, updated_at = ? WHERE item_id = ?`,
			contentJSON, now, id,
		); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(`UPDATE items SET updated_at = ? WHERE id = ?`, now, id); err != nil {
		return "", err
	}
	if err := s.reconcileDocumentAttachmentsTx(tx, id, content); err != nil {
		return "", err
	}
	if err := s.syncFTSTx(tx, id); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return now, nil
}

func (s *JournalService) syncFTS(db dbRunner, id string) error {
	item, err := s.getRawRowItemFrom(db, id)
	if err != nil {
		return err
	}
	if item.EncryptionState == EncryptionEncrypted {
		_, err := db.Exec(`DELETE FROM library_search_fts WHERE item_id = ?`, id)
		return err
	}
	body := ""
	if item.Kind == KindDocument {
		var encoded string
		if err := db.QueryRow(`SELECT content_json FROM documents WHERE item_id = ?`, id).Scan(&encoded); err != nil {
			return err
		}
		var content map[string]any
		if err := json.Unmarshal([]byte(encoded), &content); err != nil {
			return err
		}
		body = extractText(content)
	}
	if _, err := db.Exec(`DELETE FROM library_search_fts WHERE item_id = ?`, id); err != nil {
		return err
	}
	_, err = db.Exec(
		`INSERT INTO library_search_fts (item_id, kind, title, body) VALUES (?, ?, ?, ?)`,
		item.ID, item.Kind, item.Title, body,
	)
	return err
}

func (s *JournalService) syncFTSTx(tx *sql.Tx, id string) error {
	return s.syncFTS(tx, id)
}

func (s *JournalService) deleteFTSDescendantsTx(tx *sql.Tx, id string) error {
	ids, err := descendantIDs(tx, id)
	if err != nil {
		return err
	}
	ids = append(ids, id)
	for _, itemID := range ids {
		if _, err := tx.Exec(`DELETE FROM library_search_fts WHERE item_id = ?`, itemID); err != nil {
			return err
		}
	}
	return nil
}

func (s *JournalService) resolveCreateParent(parentID string) (string, error) {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return s.firstJournalID()
	}
	if err := s.validateParent(parentID); err != nil {
		return "", err
	}
	return parentID, nil
}

func (s *JournalService) validateParent(parentID string) error {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return fmt.Errorf("parent journal or folder is required")
	}
	parent, err := s.getRowItem(parentID)
	if err != nil {
		return err
	}
	if parent.SystemKey.String == SystemTrash {
		return fmt.Errorf("trash cannot contain new items directly")
	}
	if parent.Kind != KindFolder && parent.Kind != KindJournal {
		return fmt.Errorf("parent must be a journal or folder")
	}
	return nil
}

func (s *JournalService) validateMove(id string, newParentID string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("item id is required")
	}
	item, err := s.getRowItem(id)
	if err != nil {
		return err
	}
	if item.SystemKey.String == SystemTrash || item.Kind == KindJournal {
		return fmt.Errorf("item cannot be moved here")
	}
	trashID, err := s.trashID()
	if err != nil {
		return err
	}
	if newParentID != trashID {
		if err := s.validateParent(newParentID); err != nil {
			return err
		}
	}
	if id == newParentID {
		return fmt.Errorf("folder cannot be moved into itself")
	}
	descendants, err := descendantIDs(s.db, id)
	if err != nil {
		return err
	}
	for _, descendantID := range descendants {
		if descendantID == newParentID {
			return fmt.Errorf("folder cannot be moved into its descendant")
		}
	}
	return nil
}

func (s *JournalService) loadItems() ([]rowItem, error) {
	rows, err := s.db.Query(
		`SELECT id, parent_id, kind, title, sort_order, system_key, created_at, updated_at,
		        encryption_state, encryption_key_id, title_ciphertext
		 FROM items
		 ORDER BY parent_id IS NOT NULL, parent_id, sort_order, title COLLATE NOCASE`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []rowItem
	for rows.Next() {
		var item rowItem
		if err := rows.Scan(&item.ID, &item.ParentID, &item.Kind, &item.Title, &item.SortOrder, &item.SystemKey, &item.CreatedAt, &item.UpdatedAt, &item.EncryptionState, &item.EncryptionKeyID, &item.TitleCiphertext); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.prepareItemsForDisplay(items)
}

func (s *JournalService) getRowItem(id string) (rowItem, error) {
	return s.getRowItemFrom(s.db, id)
}

func (s *JournalService) getRowItemFrom(db queryRower, id string) (rowItem, error) {
	item, err := s.getRawRowItemFrom(db, id)
	if err != nil {
		return rowItem{}, err
	}
	return s.prepareItemForDisplay(item)
}

func (s *JournalService) getRawRowItemFrom(db queryRower, id string) (rowItem, error) {
	var item rowItem
	err := db.QueryRow(
		`SELECT id, parent_id, kind, title, sort_order, system_key, created_at, updated_at,
		        encryption_state, encryption_key_id, title_ciphertext
		 FROM items WHERE id = ?`,
		id,
	).Scan(&item.ID, &item.ParentID, &item.Kind, &item.Title, &item.SortOrder, &item.SystemKey, &item.CreatedAt, &item.UpdatedAt, &item.EncryptionState, &item.EncryptionKeyID, &item.TitleCiphertext)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rowItem{}, fmt.Errorf("item not found")
		}
		return rowItem{}, err
	}
	return item, nil
}

func (s *JournalService) prepareItemsForDisplay(items []rowItem) ([]rowItem, error) {
	byID := map[string]rowItem{}
	for _, item := range items {
		byID[item.ID] = item
	}
	lockedRoots := map[string]bool{}
	for _, item := range items {
		if item.Kind == KindJournal && item.EncryptionState == EncryptionEncrypted {
			if _, ok := s.journalKey(item.ID); !ok {
				lockedRoots[item.ID] = true
			}
		}
	}
	display := make([]rowItem, 0, len(items))
	for _, item := range items {
		rootID := rootJournalIDForRows(byID, item.ID)
		if item.Kind == KindJournal && item.EncryptionState == EncryptionEncrypted {
			item.EncryptionLocked = lockedRoots[item.ID]
		}
		if item.Kind != KindJournal && item.EncryptionState == EncryptionEncrypted {
			if lockedRoots[rootID] {
				continue
			}
		}
		prepared, err := s.prepareItemForDisplayWithRoot(item, rootID)
		if err != nil {
			return nil, err
		}
		display = append(display, prepared)
	}
	return display, nil
}

func (s *JournalService) prepareItemForDisplay(item rowItem) (rowItem, error) {
	if item.EncryptionState != EncryptionEncrypted {
		if item.EncryptionState == "" {
			item.EncryptionState = EncryptionPlaintext
		}
		return item, nil
	}
	rootID, err := s.journalIDForItem(item.ID)
	if err != nil {
		return rowItem{}, err
	}
	return s.prepareItemForDisplayWithRoot(item, rootID)
}

func (s *JournalService) prepareItemForDisplayWithRoot(item rowItem, rootID string) (rowItem, error) {
	if item.EncryptionState == "" {
		item.EncryptionState = EncryptionPlaintext
	}
	if item.EncryptionState != EncryptionEncrypted {
		return item, nil
	}
	if item.Kind == KindJournal {
		if _, ok := s.journalKey(item.ID); !ok {
			item.EncryptionLocked = true
		}
		return item, nil
	}
	key, ok := s.journalKey(rootID)
	if !ok {
		item.EncryptionLocked = true
		return item, nil
	}
	if !item.EncryptionKeyID.Valid || len(item.TitleCiphertext) == 0 {
		return rowItem{}, fmt.Errorf("encrypted item title is missing")
	}
	plaintext, err := openField(key, "items", item.ID, "title", item.EncryptionKeyID.String, item.TitleCiphertext)
	if err != nil {
		return rowItem{}, err
	}
	item.Title = string(plaintext)
	return item, nil
}

func rootJournalIDForRows(items map[string]rowItem, id string) string {
	for id != "" {
		item, ok := items[id]
		if !ok {
			return ""
		}
		if item.Kind == KindJournal {
			return item.ID
		}
		if !item.ParentID.Valid {
			return ""
		}
		id = item.ParentID.String
	}
	return ""
}

func (s *JournalService) getTreeItem(id string) (TreeItem, error) {
	item, err := s.getRowItem(id)
	if err != nil {
		return TreeItem{}, err
	}
	return treeItemFromRow(item), nil
}

func (s *JournalService) trashID() (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM items WHERE system_key = ?`, SystemTrash).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s *JournalService) isInTrash(id string) (bool, error) {
	trashID, err := s.trashID()
	if err != nil {
		return false, err
	}
	for id != "" {
		if id == trashID {
			return true, nil
		}
		item, err := s.getRawRowItemFrom(s.db, id)
		if err != nil {
			return false, err
		}
		if !item.ParentID.Valid {
			return false, nil
		}
		id = item.ParentID.String
	}
	return false, nil
}

func (s *JournalService) nextSortOrder(parentID string) (int, error) {
	return nextSortOrderFrom(s.db, parentID)
}

func (s *JournalService) nextJournalSortOrder() (int, error) {
	var next sql.NullInt64
	err := s.db.QueryRow(
		`SELECT COALESCE(MAX(sort_order), -1) + 1 FROM items WHERE parent_id IS NULL AND kind = ?`,
		KindJournal,
	).Scan(&next)
	if err != nil {
		return 0, err
	}
	return int(next.Int64), nil
}

func (s *JournalService) autosaveInterval() time.Duration {
	return time.Duration(s.autosaveIntervalMS()) * time.Millisecond
}

func (s *JournalService) autosaveIntervalMS() int {
	var value string
	err := s.db.QueryRow(`SELECT value FROM app_settings WHERE key = 'autosave_interval_ms'`).Scan(&value)
	if err != nil {
		return defaultAutosaveIntervalMS
	}
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil || parsed < 500 {
		return defaultAutosaveIntervalMS
	}
	return parsed
}

func (s *JournalService) libraryWidth() int {
	var value string
	err := s.db.QueryRow(`SELECT value FROM app_settings WHERE key = ?`, settingLibraryWidth).Scan(&value)
	if err != nil {
		return defaultLibraryWidth
	}
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil {
		return defaultLibraryWidth
	}
	return clampInt(parsed, minLibraryWidth, maxLibraryWidth)
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func (s *JournalService) pendingIDsOlderThan(age time.Duration) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-age)
	ids := make([]string, 0, len(s.pending))
	for id, draft := range s.pending {
		if age == 0 || draft.UpdatedAt.Before(cutoff) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func buildTree(items []rowItem, matches map[string]bool) []TreeItem {
	children := map[string][]rowItem{}
	var roots []rowItem
	for _, item := range items {
		if item.ParentID.Valid {
			children[item.ParentID.String] = append(children[item.ParentID.String], item)
		} else {
			roots = append(roots, item)
		}
	}
	var build func(rowItem) TreeItem
	build = func(item rowItem) TreeItem {
		node := treeItemFromRow(item)
		sortChildRows(children[item.ID])
		for _, child := range children[item.ID] {
			childNode := build(child)
			node.DocumentCount += childNode.DocumentCount
			node.ItemCount += childNode.ItemCount + 1
			node.Children = append(node.Children, childNode)
		}
		if item.Kind == KindDocument {
			node.DocumentCount = 1
		}
		return node
	}
	sortRootRows(roots)
	tree := make([]TreeItem, 0, len(roots))
	for _, root := range roots {
		tree = append(tree, build(root))
	}
	return tree
}

func sortRootRows(rows []rowItem) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].SystemKey.String == SystemTrash {
			return false
		}
		if rows[j].SystemKey.String == SystemTrash {
			return true
		}
		if rows[i].Kind == KindJournal && rows[j].Kind == KindJournal {
			if rows[i].SortOrder == rows[j].SortOrder {
				return strings.ToLower(rows[i].Title) < strings.ToLower(rows[j].Title)
			}
			return rows[i].SortOrder < rows[j].SortOrder
		}
		if rows[i].Kind == KindJournal {
			return true
		}
		if rows[j].Kind == KindJournal {
			return false
		}
		return updatedRowsLess(rows[i], rows[j])
	})
}

func sortChildRows(rows []rowItem) {
	sort.SliceStable(rows, func(i, j int) bool {
		return updatedRowsLess(rows[i], rows[j])
	})
}

func updatedRowsLess(a, b rowItem) bool {
	if a.UpdatedAt == b.UpdatedAt {
		return strings.ToLower(a.Title) < strings.ToLower(b.Title)
	}
	return a.UpdatedAt > b.UpdatedAt
}

func treeItemFromRow(item rowItem) TreeItem {
	parentID := ""
	if item.ParentID.Valid {
		parentID = item.ParentID.String
	}
	systemKey := ""
	if item.SystemKey.Valid {
		systemKey = item.SystemKey.String
	}
	encryptionKeyID := ""
	if item.EncryptionKeyID.Valid {
		encryptionKeyID = item.EncryptionKeyID.String
	}
	encryptionState := item.EncryptionState
	if encryptionState == "" {
		encryptionState = EncryptionPlaintext
	}
	return TreeItem{
		ID:               item.ID,
		ParentID:         parentID,
		Kind:             item.Kind,
		Title:            item.Title,
		SortOrder:        item.SortOrder,
		SystemKey:        systemKey,
		CreatedAt:        item.CreatedAt,
		UpdatedAt:        item.UpdatedAt,
		EncryptionState:  encryptionState,
		EncryptionKeyID:  encryptionKeyID,
		EncryptionLocked: item.EncryptionLocked,
		Children:         []TreeItem{},
	}
}

func rowIsInTrash(items map[string]rowItem, id string, trashID string) bool {
	for id != "" {
		if id == trashID {
			return true
		}
		item, ok := items[id]
		if !ok || !item.ParentID.Valid {
			return false
		}
		id = item.ParentID.String
	}
	return false
}

func (s *JournalService) moveJournal(id string, requestedOrder int) (TreeResponse, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return TreeResponse{}, err
	}
	defer rollback(tx)
	if _, err := tx.Exec(`UPDATE items SET parent_id = NULL, updated_at = ? WHERE id = ?`, nowString(), id); err != nil {
		return TreeResponse{}, err
	}
	if err := reorderJournals(tx, id, requestedOrder); err != nil {
		return TreeResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return TreeResponse{}, err
	}
	return s.GetLibraryTree()
}

func reorderSiblings(tx *sql.Tx, parentID string, movedID string, requestedOrder int) error {
	rows, err := tx.Query(
		`SELECT id FROM items WHERE parent_id IS ? AND id != ? ORDER BY sort_order, title COLLATE NOCASE`,
		nullParent(parentID), movedID,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if requestedOrder < 0 || requestedOrder > len(ids) {
		requestedOrder = len(ids)
	}
	ids = append(ids, "")
	copy(ids[requestedOrder+1:], ids[requestedOrder:])
	ids[requestedOrder] = movedID
	for index, id := range ids {
		if _, err := tx.Exec(`UPDATE items SET sort_order = ? WHERE id = ?`, index, id); err != nil {
			return err
		}
	}
	return nil
}

func reorderJournals(tx *sql.Tx, movedID string, requestedOrder int) error {
	rows, err := tx.Query(
		`SELECT id FROM items WHERE parent_id IS NULL AND kind = ? AND id != ? ORDER BY sort_order, title COLLATE NOCASE`,
		KindJournal, movedID,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if requestedOrder < 0 || requestedOrder > len(ids) {
		requestedOrder = len(ids)
	}
	ids = append(ids, "")
	copy(ids[requestedOrder+1:], ids[requestedOrder:])
	ids[requestedOrder] = movedID
	for index, id := range ids {
		if _, err := tx.Exec(`UPDATE items SET sort_order = ? WHERE id = ?`, index, id); err != nil {
			return err
		}
	}
	return nil
}

func nextSortOrderFrom(db queryRower, parentID string) (int, error) {
	var next sql.NullInt64
	err := db.QueryRow(
		`SELECT COALESCE(MAX(sort_order), -1) + 1 FROM items WHERE parent_id = ?`,
		parentID,
	).Scan(&next)
	if err != nil {
		return 0, err
	}
	return int(next.Int64), nil
}

func descendantIDs(db queryer, id string) ([]string, error) {
	rows, err := db.Query(
		`WITH RECURSIVE descendants(id) AS (
			SELECT id FROM items WHERE parent_id = ?
			UNION ALL
			SELECT items.id FROM items JOIN descendants ON items.parent_id = descendants.id
		)
		SELECT id FROM descendants`,
		id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var next string
		if err := rows.Scan(&next); err != nil {
			return nil, err
		}
		ids = append(ids, next)
	}
	return ids, rows.Err()
}

func (s *JournalService) firstJournalID() (string, error) {
	var id string
	err := s.db.QueryRow(
		`SELECT id FROM items WHERE kind = ? AND parent_id IS NULL ORDER BY sort_order, created_at LIMIT 1`,
		KindJournal,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s *JournalService) journalCount() (int, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM items WHERE kind = ? AND parent_id IS NULL`, KindJournal).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *JournalService) journalIDForItem(id string) (string, error) {
	return s.journalIDForItemFrom(s.db, id)
}

func (s *JournalService) journalIDForItemFrom(db queryRower, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", nil
	}
	for id != "" {
		item, err := s.getRawRowItemFrom(db, id)
		if err != nil {
			return "", err
		}
		if item.Kind == KindJournal {
			return item.ID, nil
		}
		if !item.ParentID.Valid {
			return "", nil
		}
		id = item.ParentID.String
	}
	return "", nil
}

func (s *JournalService) copyItemToParentTx(tx *sql.Tx, sourceID string, parentID string, now string) (string, error) {
	source, err := s.getRowItemFrom(tx, sourceID)
	if err != nil {
		return "", err
	}
	if source.Kind != KindFolder && source.Kind != KindDocument {
		return "", fmt.Errorf("only folders and documents can be copied")
	}
	newID := uuid.NewString()
	order, err := nextSortOrderFrom(tx, parentID)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(
		`INSERT INTO items (id, parent_id, kind, title, sort_order, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		newID, parentID, source.Kind, source.Title, order, now, now,
	); err != nil {
		return "", err
	}
	if source.Kind == KindDocument {
		var schemaVersion int
		var encoded string
		var spacingPreset string
		if err := tx.QueryRow(`SELECT schema_version, content_json, spacing_preset FROM documents WHERE item_id = ?`, sourceID).Scan(&schemaVersion, &encoded, &spacingPreset); err != nil {
			return "", err
		}
		if draft, ok := s.pendingDraftSnapshot(sourceID); ok {
			data, err := json.Marshal(draft.Content)
			if err != nil {
				return "", err
			}
			encoded = string(data)
		}
		if _, err := tx.Exec(
			`INSERT INTO documents (item_id, schema_version, content_json, spacing_preset, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			newID, schemaVersion, encoded, normalizeSpacingPreset(spacingPreset), now, now,
		); err != nil {
			return "", err
		}
		attachmentIDMap, err := s.copyDocumentAttachmentsTx(tx, sourceID, newID, nil, "")
		if err != nil {
			return "", err
		}
		if len(attachmentIDMap) > 0 {
			var content map[string]any
			if err := json.Unmarshal([]byte(encoded), &content); err != nil {
				return "", err
			}
			encodedContent, err := json.Marshal(remapAttachmentIDsInContent(content, attachmentIDMap))
			if err != nil {
				return "", err
			}
			if _, err := tx.Exec(`UPDATE documents SET content_json = ? WHERE item_id = ?`, string(encodedContent), newID); err != nil {
				return "", err
			}
		}
	}
	if err := s.syncFTSTx(tx, newID); err != nil {
		return "", err
	}
	if source.Kind == KindFolder {
		rows, err := tx.Query(`SELECT id FROM items WHERE parent_id = ? ORDER BY sort_order, title COLLATE NOCASE`, sourceID)
		if err != nil {
			return "", err
		}
		var childIDs []string
		for rows.Next() {
			var childID string
			if err := rows.Scan(&childID); err != nil {
				_ = rows.Close()
				return "", err
			}
			childIDs = append(childIDs, childID)
		}
		if err := rows.Close(); err != nil {
			return "", err
		}
		if err := rows.Err(); err != nil {
			return "", err
		}
		for _, childID := range childIDs {
			if _, err := s.copyItemToParentTx(tx, childID, newID, now); err != nil {
				return "", err
			}
		}
	}
	return newID, nil
}

func (s *JournalService) removePendingIDs(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, itemID := range ids {
		delete(s.pending, itemID)
		delete(s.lastDraftVersion, itemID)
	}
}

func (s *JournalService) pendingDraftSnapshot(id string) (pendingDraft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	draft, ok := s.pending[id]
	if !ok {
		return pendingDraft{}, false
	}
	draft.Content = cloneMap(draft.Content)
	return draft, true
}

func validateProseMirrorDoc(content map[string]any) error {
	if content == nil {
		return fmt.Errorf("document content is required")
	}
	if content["type"] != "doc" {
		return fmt.Errorf("expected a ProseMirror doc node at the top level")
	}
	if value, ok := content["content"]; ok {
		if _, ok := value.([]any); !ok {
			return fmt.Errorf("document content must be an array")
		}
	}
	return nil
}

func emptyDocument() map[string]any {
	return map[string]any{
		"type":    "doc",
		"content": []any{},
	}
}

func extractText(value any) string {
	var parts []string
	var walk func(any)
	walk = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			if text, ok := typed["text"].(string); ok {
				parts = append(parts, text)
			}
			if content, ok := typed["content"].([]any); ok {
				for _, child := range content {
					walk(child)
				}
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return strings.Join(parts, " ")
}

func cloneMap(value map[string]any) map[string]any {
	data, _ := json.Marshal(value)
	var cloned map[string]any
	_ = json.Unmarshal(data, &cloned)
	return cloned
}

func normalizeImageMimeType(name string, mimeType string, data []byte) string {
	candidates := []string{strings.ToLower(strings.TrimSpace(mimeType))}
	if len(data) > 0 {
		candidates = append(candidates, strings.ToLower(http.DetectContentType(data)))
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		candidates = append(candidates, "image/png")
	case ".jpg", ".jpeg":
		candidates = append(candidates, "image/jpeg")
	case ".gif":
		candidates = append(candidates, "image/gif")
	case ".webp":
		candidates = append(candidates, "image/webp")
	}
	for _, candidate := range candidates {
		switch candidate {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
			return candidate
		}
	}
	return ""
}

func attachmentIDsFromContent(content map[string]any) map[string]bool {
	ids := map[string]bool{}
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if typed["type"] == "attachmentImage" {
				if attrs, ok := typed["attrs"].(map[string]any); ok {
					if id, ok := attrs["attachmentId"].(string); ok && strings.TrimSpace(id) != "" {
						ids[strings.TrimSpace(id)] = true
					}
				}
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(content)
	return ids
}

func remapAttachmentIDsInContent(content map[string]any, idMap map[string]string) map[string]any {
	if len(idMap) == 0 {
		return cloneMap(content)
	}
	cloned := cloneMap(content)
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if typed["type"] == "attachmentImage" {
				if attrs, ok := typed["attrs"].(map[string]any); ok {
					if id, ok := attrs["attachmentId"].(string); ok {
						if replacement, ok := idMap[id]; ok {
							attrs["attachmentId"] = replacement
						}
					}
				}
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(cloned)
	return cloned
}

func (s *JournalService) reconcileDocumentAttachmentsTx(tx *sql.Tx, documentID string, content map[string]any) error {
	referenced := attachmentIDsFromContent(content)
	rows, err := tx.Query(`SELECT id FROM document_attachments WHERE document_id = ?`, documentID)
	if err != nil {
		return err
	}
	var detached []string
	var attached []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		if referenced[id] {
			attached = append(attached, id)
		} else {
			detached = append(detached, id)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range attached {
		if _, err := tx.Exec(`UPDATE document_attachments SET detached_at = NULL WHERE id = ?`, id); err != nil {
			return err
		}
	}
	now := nowString()
	for _, id := range detached {
		if _, err := tx.Exec(`UPDATE document_attachments SET detached_at = COALESCE(detached_at, ?) WHERE id = ?`, now, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *JournalService) PurgeDetachedAttachments(gracePeriod time.Duration) error {
	if gracePeriod < 0 {
		gracePeriod = 0
	}
	cutoff := time.Now().UTC().Add(-gracePeriod).Format(time.RFC3339Nano)
	_, err := s.db.Exec(`DELETE FROM document_attachments WHERE detached_at IS NOT NULL AND detached_at <= ?`, cutoff)
	return err
}

func (s *JournalService) copyDocumentAttachmentsTx(tx *sql.Tx, sourceDocumentID string, targetDocumentID string, key []byte, keyID string) (map[string]string, error) {
	rows, err := tx.Query(
		`SELECT id, mime_type, original_name, size_bytes, content_blob, content_ciphertext
		 FROM document_attachments WHERE document_id = ? AND detached_at IS NULL`,
		sourceDocumentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	idMap := map[string]string{}
	now := nowString()
	for rows.Next() {
		var oldID, mimeType, originalName string
		var sizeBytes int
		var contentBlob []byte
		var contentCiphertext []byte
		if err := rows.Scan(&oldID, &mimeType, &originalName, &sizeBytes, &contentBlob, &contentCiphertext); err != nil {
			return nil, err
		}
		newID := uuid.NewString()
		idMap[oldID] = newID
		if key != nil {
			plaintext, err := openField(key, "document_attachments", oldID, "content_blob", keyID, contentCiphertext)
			if err != nil {
				return nil, err
			}
			contentBlob = plaintext
			contentCiphertext = nil
			sizeBytes = len(plaintext)
		}
		if _, err := tx.Exec(
			`INSERT INTO document_attachments (id, document_id, mime_type, original_name, size_bytes, content_blob, content_ciphertext, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			newID, targetDocumentID, mimeType, originalName, sizeBytes, contentBlob, contentCiphertext, now,
		); err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return idMap, nil
}

func normalizeTitle(title string, fallback string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return fallback
	}
	return title
}

func normalizeSpacingPreset(spacingPreset string) string {
	switch strings.TrimSpace(strings.ToLower(spacingPreset)) {
	case "compact", "normal", "relaxed":
		return strings.TrimSpace(strings.ToLower(spacingPreset))
	default:
		return defaultSpacingPreset
	}
}

func ftsPhrase(query string) string {
	escaped := strings.ReplaceAll(query, `"`, `""`)
	return `"` + escaped + `"`
}

func nowString() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func nullParent(parentID string) sql.NullString {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: parentID, Valid: true}
}

func defaultDBPath() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv("JOURNAL_DB_PATH")); explicit != "" {
		return explicit, nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "Journal", "journal.db"), nil
}

func rollback(tx *sql.Tx) {
	_ = tx.Rollback()
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

type queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}

type dbRunner interface {
	execer
	queryRower
}
