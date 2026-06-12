package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const (
	KindFolder   = "folder"
	KindDocument = "document"
	SystemTrash  = "trash"

	defaultAutosaveIntervalMS = 2000
)

type App struct {
	ctx     context.Context
	service *JournalService
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

func (a *App) OpenDocument(id string) (DocumentResponse, error) {
	return a.service.OpenDocument(id)
}

func (a *App) UpdateDocumentDraft(id string, content map[string]any) (DocumentDraftResponse, error) {
	return a.service.UpdateDocumentDraft(id, content)
}

func (a *App) FlushDocument(id string) (DocumentSaveResponse, error) {
	return a.service.FlushDocument(id)
}

func (a *App) SearchLibrary(query string) (SearchResponse, error) {
	return a.service.SearchLibrary(query)
}

func (a *App) GetAppSettings() (AppSettingsResponse, error) {
	return a.service.GetAppSettings()
}

func (a *App) UpdateAppSettings(settings AppSettingsPatch) (AppSettingsResponse, error) {
	return a.service.UpdateAppSettings(settings)
}

type TreeItem struct {
	ID        string     `json:"id"`
	ParentID  string     `json:"parentId"`
	Kind      string     `json:"kind"`
	Title     string     `json:"title"`
	SortOrder int        `json:"sortOrder"`
	SystemKey string     `json:"systemKey"`
	CreatedAt string     `json:"createdAt"`
	UpdatedAt string     `json:"updatedAt"`
	Children  []TreeItem `json:"children"`
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
	SchemaVersion int            `json:"schemaVersion"`
	Item          TreeItem       `json:"item"`
	Tree          TreeResponse   `json:"tree"`
	SaveState     string         `json:"saveState"`
}

type DocumentDraftResponse struct {
	ID        string `json:"id"`
	SaveState string `json:"saveState"`
}

type DocumentSaveResponse struct {
	ID        string `json:"id"`
	SaveState string `json:"saveState"`
	SavedAt   string `json:"savedAt"`
}

type SearchResponse struct {
	Query     string     `json:"query"`
	Items     []TreeItem `json:"items"`
	ResultIDs []string   `json:"resultIds"`
	TrashID   string     `json:"trashId"`
}

type AppSettingsResponse struct {
	AutosaveIntervalMS int `json:"autosaveIntervalMs"`
}

type AppSettingsPatch struct {
	AutosaveIntervalMS int `json:"autosaveIntervalMs"`
}

type rowItem struct {
	ID        string
	ParentID  sql.NullString
	Kind      string
	Title     string
	SortOrder int
	SystemKey sql.NullString
	CreatedAt string
	UpdatedAt string
}

type pendingDraft struct {
	Content   map[string]any
	UpdatedAt time.Time
}

type JournalService struct {
	db      *sql.DB
	mu      sync.Mutex
	pending map[string]pendingDraft
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

	service := &JournalService{db: db, pending: map[string]pendingDraft{}}
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
			kind TEXT NOT NULL CHECK (kind IN ('folder', 'document')),
			title TEXT NOT NULL,
			sort_order INTEGER NOT NULL DEFAULT 0,
			system_key TEXT NULL UNIQUE,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS documents (
			item_id TEXT PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
			schema_version INTEGER NOT NULL,
			content_json TEXT NOT NULL,
			search_text TEXT NOT NULL DEFAULT '',
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
		`CREATE INDEX IF NOT EXISTS idx_items_parent_sort ON items(parent_id, sort_order, title)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return err
		}
	}
	if err := s.ensureTrash(); err != nil {
		return err
	}
	return s.ensureSetting("autosave_interval_ms", fmt.Sprintf("%d", defaultAutosaveIntervalMS))
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
	if err := s.validateParent(parentID); err != nil {
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

	tx, err := s.db.Begin()
	if err != nil {
		return DocumentResponse{}, err
	}
	defer rollback(tx)

	if _, err := tx.Exec(
		`INSERT INTO items (id, parent_id, kind, title, sort_order, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, nullParent(parentID), KindDocument, "Untitled", order, now, now,
	); err != nil {
		return DocumentResponse{}, err
	}
	if _, err := tx.Exec(
		`INSERT INTO documents (item_id, schema_version, content_json, search_text, created_at, updated_at)
		 VALUES (?, 1, ?, '', ?, ?)`,
		id, string(encoded), now, now,
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
	if err := s.validateParent(parentID); err != nil {
		return ItemResponse{}, err
	}
	title = normalizeTitle(title, "New Folder")
	now := nowString()
	id := uuid.NewString()
	order, err := s.nextSortOrder(parentID)
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
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, nullParent(parentID), KindFolder, title, order, now, now,
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
	item, err := s.getRowItem(id)
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
	if _, err := tx.Exec(`UPDATE items SET title = ?, updated_at = ? WHERE id = ?`, title, now, id); err != nil {
		return ItemResponse{}, err
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
	if err := s.validateMove(id, newParentID); err != nil {
		return TreeResponse{}, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return TreeResponse{}, err
	}
	defer rollback(tx)

	now := nowString()
	if _, err := tx.Exec(
		`UPDATE items SET parent_id = ?, updated_at = ? WHERE id = ?`,
		nullParent(newParentID), now, id,
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
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
	return s.GetLibraryTree()
}

func (s *JournalService) OpenDocument(id string) (DocumentResponse, error) {
	item, err := s.getRowItem(id)
	if err != nil {
		return DocumentResponse{}, err
	}
	if item.Kind != KindDocument {
		return DocumentResponse{}, fmt.Errorf("item is not a document")
	}

	var encoded string
	var schemaVersion int
	err = s.db.QueryRow(
		`SELECT schema_version, content_json FROM documents WHERE item_id = ?`,
		id,
	).Scan(&schemaVersion, &encoded)
	if err != nil {
		return DocumentResponse{}, err
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(encoded), &content); err != nil {
		return DocumentResponse{}, err
	}
	tree, err := s.GetLibraryTree()
	if err != nil {
		return DocumentResponse{}, err
	}
	return DocumentResponse{
		ID:            id,
		Title:         item.Title,
		Content:       content,
		SchemaVersion: schemaVersion,
		Item:          treeItemFromRow(item),
		Tree:          tree,
		SaveState:     "saved",
	}, nil
}

func (s *JournalService) UpdateDocumentDraft(id string, content map[string]any) (DocumentDraftResponse, error) {
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
	s.pending[id] = pendingDraft{Content: cloneMap(content), UpdatedAt: time.Now()}
	s.mu.Unlock()
	return DocumentDraftResponse{ID: id, SaveState: "dirty"}, nil
}

func (s *JournalService) FlushDocument(id string) (DocumentSaveResponse, error) {
	s.mu.Lock()
	draft, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if !ok {
		return DocumentSaveResponse{ID: id, SaveState: "saved", SavedAt: nowString()}, nil
	}
	if err := s.saveDocumentContent(id, draft.Content); err != nil {
		s.mu.Lock()
		s.pending[id] = draft
		s.mu.Unlock()
		return DocumentSaveResponse{}, err
	}
	return DocumentSaveResponse{ID: id, SaveState: "saved", SavedAt: nowString()}, nil
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
	for id := range matches {
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
	trashID, err := s.trashID()
	if err != nil {
		return SearchResponse{}, err
	}
	return SearchResponse{
		Query:     query,
		Items:     buildTree(filtered, matches),
		ResultIDs: resultIDs,
		TrashID:   trashID,
	}, nil
}

func (s *JournalService) GetAppSettings() (AppSettingsResponse, error) {
	return AppSettingsResponse{AutosaveIntervalMS: s.autosaveIntervalMS()}, nil
}

func (s *JournalService) UpdateAppSettings(settings AppSettingsPatch) (AppSettingsResponse, error) {
	value := settings.AutosaveIntervalMS
	if value < 500 {
		value = defaultAutosaveIntervalMS
	}
	now := nowString()
	if _, err := s.db.Exec(
		`INSERT INTO app_settings (key, value, updated_at)
		 VALUES ('autosave_interval_ms', ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		fmt.Sprintf("%d", value), now,
	); err != nil {
		return AppSettingsResponse{}, err
	}
	return s.GetAppSettings()
}

func (s *JournalService) saveDocumentContent(id string, content map[string]any) error {
	if err := validateProseMirrorDoc(content); err != nil {
		return err
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return err
	}
	now := nowString()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err := tx.Exec(
		`UPDATE documents SET content_json = ?, search_text = ?, updated_at = ? WHERE item_id = ?`,
		string(encoded), extractText(content), now, id,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE items SET updated_at = ? WHERE id = ?`, now, id); err != nil {
		return err
	}
	if err := s.syncFTSTx(tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *JournalService) syncFTS(db dbRunner, id string) error {
	item, err := s.getRowItemFrom(db, id)
	if err != nil {
		return err
	}
	body := ""
	if item.Kind == KindDocument {
		if err := db.QueryRow(`SELECT search_text FROM documents WHERE item_id = ?`, id).Scan(&body); err != nil {
			return err
		}
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

func (s *JournalService) validateParent(parentID string) error {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return nil
	}
	parent, err := s.getRowItem(parentID)
	if err != nil {
		return err
	}
	if parent.Kind != KindFolder {
		return fmt.Errorf("parent must be a folder")
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
	if item.SystemKey.String == SystemTrash {
		return fmt.Errorf("trash cannot be moved")
	}
	if err := s.validateParent(newParentID); err != nil {
		return err
	}
	if newParentID == "" {
		return nil
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
		`SELECT id, parent_id, kind, title, sort_order, system_key, created_at, updated_at
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
		if err := rows.Scan(&item.ID, &item.ParentID, &item.Kind, &item.Title, &item.SortOrder, &item.SystemKey, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *JournalService) getRowItem(id string) (rowItem, error) {
	return s.getRowItemFrom(s.db, id)
}

func (s *JournalService) getRowItemFrom(db queryRower, id string) (rowItem, error) {
	var item rowItem
	err := db.QueryRow(
		`SELECT id, parent_id, kind, title, sort_order, system_key, created_at, updated_at
		 FROM items WHERE id = ?`,
		id,
	).Scan(&item.ID, &item.ParentID, &item.Kind, &item.Title, &item.SortOrder, &item.SystemKey, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rowItem{}, fmt.Errorf("item not found")
		}
		return rowItem{}, err
	}
	return item, nil
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
		item, err := s.getRowItem(id)
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
	var next sql.NullInt64
	err := s.db.QueryRow(
		`SELECT COALESCE(MAX(sort_order), -1) + 1 FROM items WHERE parent_id IS ?`,
		nullParent(parentID),
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
		for _, child := range children[item.ID] {
			node.Children = append(node.Children, build(child))
		}
		return node
	}
	sortRows(roots)
	tree := make([]TreeItem, 0, len(roots))
	for _, root := range roots {
		tree = append(tree, build(root))
	}
	return tree
}

func sortRows(rows []rowItem) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].SortOrder == rows[j].SortOrder {
			return strings.ToLower(rows[i].Title) < strings.ToLower(rows[j].Title)
		}
		return rows[i].SortOrder < rows[j].SortOrder
	})
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
	return TreeItem{
		ID:        item.ID,
		ParentID:  parentID,
		Kind:      item.Kind,
		Title:     item.Title,
		SortOrder: item.SortOrder,
		SystemKey: systemKey,
		CreatedAt: item.CreatedAt,
		UpdatedAt: item.UpdatedAt,
		Children:  []TreeItem{},
	}
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

func normalizeTitle(title string, fallback string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return fallback
	}
	return title
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
