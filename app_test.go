package main

import (
	"path/filepath"
	"testing"
)

func newTestService(t *testing.T) *JournalService {
	t.Helper()
	service, err := OpenJournalService(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatalf("open service: %v", err)
	}
	t.Cleanup(func() {
		_ = service.Close()
	})
	return service
}

func TestBootstrapCreatesTrashAndSettings(t *testing.T) {
	service := newTestService(t)

	tree, err := service.GetLibraryTree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	if tree.TrashID == "" {
		t.Fatal("expected trash id")
	}
	if len(tree.Items) != 1 {
		t.Fatalf("expected only trash on first run, got %d root items", len(tree.Items))
	}
	if tree.Items[0].SystemKey != SystemTrash {
		t.Fatalf("expected trash root, got %#v", tree.Items[0])
	}

	settings, err := service.GetAppSettings()
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if settings.AutosaveIntervalMS != defaultAutosaveIntervalMS {
		t.Fatalf("unexpected autosave interval: %d", settings.AutosaveIntervalMS)
	}
	if settings.LastDocumentID != "" {
		t.Fatalf("expected no last document on first run, got %q", settings.LastDocumentID)
	}

	rows, err := service.db.Query(`PRAGMA table_info(documents)`)
	if err != nil {
		t.Fatalf("documents schema: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan documents schema: %v", err)
		}
		if name == "search_text" {
			t.Fatal("documents table should not include search_text")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("documents schema rows: %v", err)
	}
}

func TestDocumentLifecycleSearchAndTrash(t *testing.T) {
	service := newTestService(t)

	folder, err := service.CreateFolder("", "Drafts")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	doc, err := service.CreateDocument(folder.Item.ID)
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	if doc.CreatedAt == "" || doc.UpdatedAt == "" {
		t.Fatalf("expected document timestamps, got created=%q updated=%q", doc.CreatedAt, doc.UpdatedAt)
	}
	settings, err := service.GetAppSettings()
	if err != nil {
		t.Fatalf("settings after create: %v", err)
	}
	if settings.LastDocumentID != doc.ID {
		t.Fatalf("expected last document %q, got %q", doc.ID, settings.LastDocumentID)
	}
	if _, err := service.RenameItem(doc.ID, "Launch Notes"); err != nil {
		t.Fatalf("rename doc: %v", err)
	}
	content := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "SQLite autosave is working"},
				},
			},
		},
	}
	if _, err := service.UpdateDocumentDraft(doc.ID, content); err != nil {
		t.Fatalf("draft: %v", err)
	}
	if _, err := service.FlushDocument(doc.ID); err != nil {
		t.Fatalf("flush: %v", err)
	}
	opened, err := service.OpenDocument(doc.ID)
	if err != nil {
		t.Fatalf("open document: %v", err)
	}
	if opened.UpdatedAt == doc.UpdatedAt {
		t.Fatalf("expected updated timestamp to advance after flush")
	}

	results, err := service.SearchLibrary("autosave")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results.ResultIDs) != 1 || results.ResultIDs[0] != doc.ID {
		t.Fatalf("expected document search hit, got %#v", results.ResultIDs)
	}
	if len(results.Items) == 0 || results.Items[0].Title != "Drafts" {
		t.Fatalf("expected ancestor folder context, got %#v", results.Items)
	}

	tree, err := service.MoveItemToTrash(doc.ID)
	if err != nil {
		t.Fatalf("move to trash: %v", err)
	}
	trash := findTreeItem(tree.Items, tree.TrashID)
	if trash == nil || len(trash.Children) != 1 {
		t.Fatalf("expected doc under trash, got %#v", trash)
	}
	if _, err := service.MoveItemToTrash(doc.ID); err != nil {
		t.Fatalf("delete from trash: %v", err)
	}
	if _, err := service.OpenDocument(doc.ID); err == nil {
		t.Fatal("expected permanent delete to remove document")
	}
}

func TestFolderContentsSortByLastUpdatedDescending(t *testing.T) {
	service := newTestService(t)

	folder, err := service.CreateFolder("", "Project")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	older, err := service.CreateDocument(folder.Item.ID)
	if err != nil {
		t.Fatalf("create older document: %v", err)
	}
	newer, err := service.CreateDocument(folder.Item.ID)
	if err != nil {
		t.Fatalf("create newer document: %v", err)
	}
	if _, err := service.db.Exec(`UPDATE items SET updated_at = ? WHERE id = ?`, "2026-01-01T10:00:00Z", older.ID); err != nil {
		t.Fatalf("set older timestamp: %v", err)
	}
	if _, err := service.db.Exec(`UPDATE items SET updated_at = ? WHERE id = ?`, "2026-01-02T10:00:00Z", newer.ID); err != nil {
		t.Fatalf("set newer timestamp: %v", err)
	}

	tree, err := service.GetLibraryTree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	project := findTreeItem(tree.Items, folder.Item.ID)
	if project == nil {
		t.Fatal("expected project folder")
	}
	if len(project.Children) != 2 {
		t.Fatalf("expected two children, got %#v", project.Children)
	}
	if project.Children[0].ID != newer.ID || project.Children[1].ID != older.ID {
		t.Fatalf("expected newest document first, got %#v", project.Children)
	}
}

func TestMoveRejectsDescendantTarget(t *testing.T) {
	service := newTestService(t)

	parent, err := service.CreateFolder("", "Parent")
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	child, err := service.CreateFolder(parent.Item.ID, "Child")
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if _, err := service.MoveItem(parent.Item.ID, child.Item.ID, -1); err == nil {
		t.Fatal("expected move into descendant to fail")
	}
}

func TestMoveFolderToTrashKeepsDescendants(t *testing.T) {
	service := newTestService(t)

	folder, err := service.CreateFolder("", "Project")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	doc, err := service.CreateDocument(folder.Item.ID)
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}

	tree, err := service.MoveItemToTrash(folder.Item.ID)
	if err != nil {
		t.Fatalf("move folder to trash: %v", err)
	}
	trash := findTreeItem(tree.Items, tree.TrashID)
	if trash == nil || len(trash.Children) != 1 {
		t.Fatalf("expected folder under trash, got %#v", trash)
	}
	movedFolder := trash.Children[0]
	if movedFolder.ID != folder.Item.ID || len(movedFolder.Children) != 1 || movedFolder.Children[0].ID != doc.ID {
		t.Fatalf("expected descendant document to remain under moved folder, got %#v", movedFolder)
	}
}

func findTreeItem(items []TreeItem, id string) *TreeItem {
	for i := range items {
		if items[i].ID == id {
			return &items[i]
		}
		if found := findTreeItem(items[i].Children, id); found != nil {
			return found
		}
	}
	return nil
}
