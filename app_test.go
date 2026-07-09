package main

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestService(t *testing.T) *JournalService {
	t.Helper()
	masterKDFN = 1024
	masterKDFR = 8
	masterKDFP = 1
	service, err := OpenJournalService(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatalf("open service: %v", err)
	}
	t.Cleanup(func() {
		_ = service.Close()
	})
	return service
}

func TestStressDatabaseOpensWithJournalService(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "stress.db")
	cmd := exec.Command("go", "run", "./cmd/stressdb",
		"-out", dbPath,
		"-journals", "2",
		"-min-folders", "4",
		"-max-folders", "4",
		"-nested-percent", "75",
		"-min-documents", "5",
		"-max-documents", "5",
		"-min-words", "20",
		"-max-words", "20",
		"-seed", "7",
		"-report-every-docs", "0",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generate stress database: %v\n%s", err, output)
	}

	service, err := OpenJournalService(dbPath)
	if err != nil {
		t.Fatalf("open generated database: %v", err)
	}
	defer service.Close()

	tree, err := service.GetLibraryTree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	if len(tree.Items) != 3 {
		t.Fatalf("expected 2 journals and trash, got %#v", tree.Items)
	}
	if tree.Items[0].DocumentCount != 5 || tree.Items[1].DocumentCount != 5 {
		t.Fatalf("expected generated document counts, got %#v", tree.Items)
	}

	results, err := service.SearchLibrary("Document 0001-000001")
	if err != nil {
		t.Fatalf("search generated database: %v", err)
	}
	if len(results.ResultIDs) != 1 {
		t.Fatalf("expected generated document title search hit, got %#v", results.ResultIDs)
	}
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
	if len(tree.Items) != 2 {
		t.Fatalf("expected default journal and trash on first run, got %d root items", len(tree.Items))
	}
	if tree.Items[0].Kind != KindJournal || tree.Items[0].Title != "New Journal" {
		t.Fatalf("expected default journal root, got %#v", tree.Items[0])
	}
	if tree.Items[1].SystemKey != SystemTrash {
		t.Fatalf("expected trash root, got %#v", tree.Items[1])
	}
	trashResults, err := service.SearchLibrary("Trash")
	if err != nil {
		t.Fatalf("search trash: %v", err)
	}
	if len(trashResults.ResultIDs) != 0 || findTreeItem(trashResults.Items, tree.TrashID) != nil {
		t.Fatalf("expected trash title to be excluded from search, got ids=%#v items=%#v", trashResults.ResultIDs, trashResults.Items)
	}

	settings, err := service.GetAppSettings()
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if settings.AutosaveIntervalMS != defaultAutosaveIntervalMS {
		t.Fatalf("unexpected autosave interval: %d", settings.AutosaveIntervalMS)
	}
	if settings.LibraryWidth != defaultLibraryWidth {
		t.Fatalf("unexpected library width: %d", settings.LibraryWidth)
	}
	if settings.LastDocumentID != "" {
		t.Fatalf("expected no last document on first run, got %q", settings.LastDocumentID)
	}

	settings, err = service.UpdateAppSettings(AppSettingsPatch{AutosaveIntervalMS: 1500, LibraryWidth: maxLibraryWidth + 100})
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if settings.AutosaveIntervalMS != 1500 {
		t.Fatalf("expected autosave interval 1500, got %d", settings.AutosaveIntervalMS)
	}
	if settings.LibraryWidth != maxLibraryWidth {
		t.Fatalf("expected clamped library width %d, got %d", maxLibraryWidth, settings.LibraryWidth)
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
	if doc.SpacingPreset != defaultSpacingPreset {
		t.Fatalf("expected default spacing %q, got %q", defaultSpacingPreset, doc.SpacingPreset)
	}
	if _, err := service.UpdateDocumentSpacing(doc.ID, "compact"); err != nil {
		t.Fatalf("update document spacing: %v", err)
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
	if _, err := service.UpdateDocumentDraft(doc.ID, content, 1); err != nil {
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
	if opened.SpacingPreset != "compact" {
		t.Fatalf("expected compact spacing, got %q", opened.SpacingPreset)
	}

	results, err := service.SearchLibrary("autosave")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results.ResultIDs) != 1 || results.ResultIDs[0] != doc.ID {
		t.Fatalf("expected document search hit, got %#v", results.ResultIDs)
	}
	journal := findTreeItem(results.Items, folder.Item.ParentID)
	if journal == nil || journal.Kind != KindJournal {
		t.Fatalf("expected journal ancestor context, got %#v", results.Items)
	}
	drafts := findTreeItem(results.Items, folder.Item.ID)
	if drafts == nil || drafts.Title != "Drafts" {
		t.Fatalf("expected folder ancestor context, got %#v", results.Items)
	}

	tree, err := service.MoveItemToTrash(doc.ID)
	if err != nil {
		t.Fatalf("move to trash: %v", err)
	}
	trash := findTreeItem(tree.Items, tree.TrashID)
	if trash == nil || len(trash.Children) != 1 {
		t.Fatalf("expected doc under trash, got %#v", trash)
	}
	results, err = service.SearchLibrary("autosave")
	if err != nil {
		t.Fatalf("search after trash: %v", err)
	}
	if len(results.ResultIDs) != 0 || findTreeItem(results.Items, tree.TrashID) != nil {
		t.Fatalf("expected trash to be excluded from search, got ids=%#v items=%#v", results.ResultIDs, results.Items)
	}
	if _, err := service.TrashItem(TrashItemCommand{ID: doc.ID, ExpectedInTrash: true}); err != nil {
		t.Fatalf("delete from trash: %v", err)
	}
	if _, err := service.OpenDocument(doc.ID); err == nil {
		t.Fatal("expected permanent delete to remove document")
	}
}

func TestTrashItemRejectsRepeatedConcurrentRequests(t *testing.T) {
	service := newTestService(t)
	doc, err := service.CreateDocument("")
	if err != nil {
		t.Fatalf("create document: %v", err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := service.TrashItem(TrashItemCommand{ID: doc.ID, ExpectedInTrash: false})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected one accepted trash transition, got %d", successes)
	}
	if _, err := service.OpenDocument(doc.ID); err != nil {
		t.Fatalf("document should remain recoverable after repeated trash request: %v", err)
	}
	if _, err := service.TrashItem(TrashItemCommand{ID: doc.ID, ExpectedInTrash: true}); err != nil {
		t.Fatalf("explicit permanent delete: %v", err)
	}
}

func TestImportRejectsEscapingManifestPath(t *testing.T) {
	service := newTestService(t)
	rootDir := t.TempDir()
	sourceDir := filepath.Join(rootDir, "source")
	outsideDir := filepath.Join(rootDir, "outside")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("create source directory: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("create outside directory: %v", err)
	}
	outsidePath := filepath.Join(outsideDir, "outside.md")
	if err := os.WriteFile(outsidePath, []byte("private"), 0o600); err != nil {
		t.Fatalf("write outside markdown: %v", err)
	}
	manifest := `{"version":1,"journalTitle":"Unsafe","nodes":[{"kind":"document","title":"Outside","file":"../outside/outside.md"}]}`
	if err := os.WriteFile(filepath.Join(sourceDir, journalExportManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if _, err := service.ImportMarkdownDirectory(sourceDir); err == nil {
		t.Fatal("expected import to reject a manifest path outside the selected folder")
	}
}

func TestImportAllowsAssetInsideSelectedFolder(t *testing.T) {
	service := newTestService(t)
	sourceDir := t.TempDir()
	assetDir := filepath.Join(sourceDir, "Note.assets")
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		t.Fatalf("create asset directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assetDir, "image.png"), []byte{0x89, 0x50, 0x4e, 0x47}, 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "Note.md"), []byte("![Example](Note.assets/image.png)\n"), 0o644); err != nil {
		t.Fatalf("write markdown: %v", err)
	}

	response, err := service.ImportMarkdownDirectory(sourceDir)
	if err != nil {
		t.Fatalf("import markdown with local asset: %v", err)
	}
	journal := findTreeItem(response.Tree.Items, response.Item.ID)
	if journal == nil || len(journal.Children) != 1 {
		t.Fatalf("expected imported document, got %#v", journal)
	}
	content, err := service.OpenDocument(journal.Children[0].ID)
	if err != nil {
		t.Fatalf("open imported document: %v", err)
	}
	if len(attachmentIDsFromContent(content.Content)) != 1 {
		t.Fatalf("expected imported asset reference, got %#v", content.Content)
	}
}

func TestMigrationRecordsVersionAndFailsCleanlyForInvalidSchema(t *testing.T) {
	service := newTestService(t)
	var version int
	if err := service.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 1 {
		t.Fatalf("expected schema version 1, got %d", version)
	}

	path := filepath.Join(t.TempDir(), "invalid.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open invalid db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE items (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create invalid schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close invalid db: %v", err)
	}
	if _, err := OpenJournalService(path); err == nil {
		t.Fatal("expected invalid schema migration to return an error")
	}
}

func TestLargeLibraryTreeBuildsWithStableCounts(t *testing.T) {
	service := newTestService(t)
	tree, err := service.GetLibraryTree()
	if err != nil {
		t.Fatalf("get tree: %v", err)
	}
	journalID := tree.Items[0].ID
	now := nowString()
	tx, err := service.db.Begin()
	if err != nil {
		t.Fatalf("begin large library insert: %v", err)
	}
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("large-folder-%04d", i)
		if _, err := tx.Exec(
			`INSERT INTO items (id, parent_id, kind, title, sort_order, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, journalID, KindFolder, fmt.Sprintf("Folder %04d", i), i, now, now,
		); err != nil {
			_ = tx.Rollback()
			t.Fatalf("insert large folder %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit large library insert: %v", err)
	}

	tree, err = service.GetLibraryTree()
	if err != nil {
		t.Fatalf("build large tree: %v", err)
	}
	journal := findTreeItem(tree.Items, journalID)
	if journal == nil || journal.ItemCount != 1000 || len(journal.Children) != 1000 {
		t.Fatalf("expected 1000 folders in large tree, got %#v", journal)
	}
}

func TestDocumentImageAttachmentLifecycle(t *testing.T) {
	service := newTestService(t)

	doc, err := service.CreateDocument("")
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	imageData := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d}
	attachment, err := service.createDocumentAttachment(doc.ID, "note.png", "image/png", imageData)
	if err != nil {
		t.Fatalf("create attachment: %v", err)
	}
	content := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "attachmentImage",
				"attrs": map[string]any{
					"attachmentId": attachment.ID,
					"alt":          "note.png",
				},
			},
		},
	}
	if _, err := service.UpdateDocumentDraft(doc.ID, content, 1); err != nil {
		t.Fatalf("draft with attachment: %v", err)
	}
	if _, err := service.FlushDocument(doc.ID); err != nil {
		t.Fatalf("flush with attachment: %v", err)
	}
	data, err := service.GetDocumentAttachmentDataURL(attachment.ID)
	if err != nil {
		t.Fatalf("get attachment: %v", err)
	}
	if !strings.HasPrefix(data.DataURL, "data:image/png;base64,") {
		t.Fatalf("expected png data url, got %q", data.DataURL)
	}

	if _, err := service.UpdateDocumentDraft(doc.ID, emptyDocument(), 2); err != nil {
		t.Fatalf("draft without attachment: %v", err)
	}
	if _, err := service.FlushDocument(doc.ID); err != nil {
		t.Fatalf("flush without attachment: %v", err)
	}
	if _, err := service.GetDocumentAttachmentDataURL(attachment.ID); err != nil {
		t.Fatalf("expected detached attachment to remain available for undo: %v", err)
	}
	if _, err := service.UpdateDocumentDraft(doc.ID, content, 3); err != nil {
		t.Fatalf("draft restored attachment: %v", err)
	}
	if _, err := service.FlushDocument(doc.ID); err != nil {
		t.Fatalf("flush restored attachment: %v", err)
	}
	var detachedAt sql.NullString
	if err := service.db.QueryRow(`SELECT detached_at FROM document_attachments WHERE id = ?`, attachment.ID).Scan(&detachedAt); err != nil {
		t.Fatalf("detached timestamp after restore: %v", err)
	}
	if detachedAt.Valid {
		t.Fatalf("expected restored attachment to clear detached_at, got %q", detachedAt.String)
	}

	if _, err := service.UpdateDocumentDraft(doc.ID, emptyDocument(), 4); err != nil {
		t.Fatalf("draft removed attachment again: %v", err)
	}
	if _, err := service.FlushDocument(doc.ID); err != nil {
		t.Fatalf("flush removed attachment again: %v", err)
	}
	oldDetachedAt := time.Now().UTC().Add(-25 * time.Hour).Format(time.RFC3339Nano)
	if _, err := service.db.Exec(`UPDATE document_attachments SET detached_at = ? WHERE id = ?`, oldDetachedAt, attachment.ID); err != nil {
		t.Fatalf("age detached attachment: %v", err)
	}
	if err := service.PurgeDetachedAttachments(detachedAttachmentGrace); err != nil {
		t.Fatalf("purge detached attachments: %v", err)
	}
	if _, err := service.GetDocumentAttachmentDataURL(attachment.ID); err == nil {
		t.Fatal("expected aged detached attachment to be purged")
	}
}

func TestEncryptedDocumentImageAttachmentLifecycle(t *testing.T) {
	service := newTestService(t)

	if err := service.CreateMasterPassword("correct horse battery staple"); err != nil {
		t.Fatalf("create master password: %v", err)
	}
	if err := service.UnlockEncryption("correct horse battery staple"); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	status, err := service.GetEncryptionStatus()
	if err != nil {
		t.Fatalf("encryption status: %v", err)
	}
	if !status.Unlocked {
		t.Fatal("expected unlocked encryption status")
	}

	tree, err := service.GetLibraryTree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	journal := tree.Items[0]
	doc, err := service.CreateDocument(journal.ID)
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	if _, err := service.EncryptJournal(journal.ID); err != nil {
		t.Fatalf("encrypt journal: %v", err)
	}

	imageData := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d}
	attachment, err := service.createDocumentAttachment(doc.ID, "secret.png", "image/png", imageData)
	if err != nil {
		t.Fatalf("create encrypted attachment: %v", err)
	}
	content := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "attachmentImage",
				"attrs": map[string]any{
					"attachmentId": attachment.ID,
					"alt":          "secret.png",
				},
			},
		},
	}
	if _, err := service.UpdateDocumentDraft(doc.ID, content, 1); err != nil {
		t.Fatalf("draft encrypted attachment: %v", err)
	}
	if _, err := service.FlushDocument(doc.ID); err != nil {
		t.Fatalf("flush encrypted attachment: %v", err)
	}
	data, err := service.GetDocumentAttachmentDataURL(attachment.ID)
	if err != nil {
		t.Fatalf("get encrypted attachment: %v", err)
	}
	if !strings.HasPrefix(data.DataURL, "data:image/png;base64,") {
		t.Fatalf("expected png data url, got %q", data.DataURL)
	}
}

func TestDuplicateDocumentCopiesDraftSpacingAndAttachments(t *testing.T) {
	service := newTestService(t)

	folder, err := service.CreateFolder("", "Drafts")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	doc, err := service.CreateDocument(folder.Item.ID)
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	if _, err := service.RenameItem(doc.ID, "Launch Notes"); err != nil {
		t.Fatalf("rename document: %v", err)
	}
	if _, err := service.UpdateDocumentSpacing(doc.ID, "relaxed"); err != nil {
		t.Fatalf("update spacing: %v", err)
	}
	attachment, err := service.createDocumentAttachment(doc.ID, "note.png", "image/png", []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d})
	if err != nil {
		t.Fatalf("create attachment: %v", err)
	}
	content := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "Pending duplicate text"},
				},
			},
			map[string]any{
				"type": "attachmentImage",
				"attrs": map[string]any{
					"attachmentId": attachment.ID,
					"alt":          "note.png",
				},
			},
		},
	}
	if _, err := service.UpdateDocumentDraft(doc.ID, content, 1); err != nil {
		t.Fatalf("pending draft: %v", err)
	}

	copied, err := service.DuplicateDocument(doc.ID)
	if err != nil {
		t.Fatalf("duplicate document: %v", err)
	}
	if copied.ID == doc.ID || copied.Title != "Copy of Launch Notes" || copied.Item.ParentID != folder.Item.ID {
		t.Fatalf("unexpected copied document metadata: %#v", copied)
	}
	if copied.SpacingPreset != "relaxed" {
		t.Fatalf("expected copied spacing relaxed, got %q", copied.SpacingPreset)
	}
	if text := extractText(copied.Content); text != "Pending duplicate text" {
		t.Fatalf("expected pending draft text, got %q", text)
	}
	copiedAttachmentIDs := attachmentIDsFromContent(copied.Content)
	if copiedAttachmentIDs[attachment.ID] || len(copiedAttachmentIDs) != 1 {
		t.Fatalf("expected copied content to reference one new attachment, got %#v", copiedAttachmentIDs)
	}
	for copiedAttachmentID := range copiedAttachmentIDs {
		data, err := service.GetDocumentAttachmentDataURL(copiedAttachmentID)
		if err != nil {
			t.Fatalf("get copied attachment: %v", err)
		}
		if !strings.HasPrefix(data.DataURL, "data:image/png;base64,") {
			t.Fatalf("expected copied png data url, got %q", data.DataURL)
		}
	}
}

func TestDuplicateEncryptedDocumentKeepsEncryptedCopyAndAttachments(t *testing.T) {
	service := newTestService(t)

	if err := service.CreateMasterPassword("correct horse battery staple"); err != nil {
		t.Fatalf("create master password: %v", err)
	}
	if err := service.UnlockEncryption("correct horse battery staple"); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	tree, err := service.GetLibraryTree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	journal := tree.Items[0]
	if _, err := service.EncryptJournal(journal.ID); err != nil {
		t.Fatalf("encrypt journal: %v", err)
	}
	doc, err := service.CreateDocument(journal.ID)
	if err != nil {
		t.Fatalf("create encrypted document: %v", err)
	}
	if _, err := service.RenameItem(doc.ID, "Secret Note"); err != nil {
		t.Fatalf("rename encrypted document: %v", err)
	}
	attachment, err := service.createDocumentAttachment(doc.ID, "secret.png", "image/png", []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d})
	if err != nil {
		t.Fatalf("create encrypted attachment: %v", err)
	}
	content := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "Secret duplicate text"},
				},
			},
			map[string]any{
				"type": "attachmentImage",
				"attrs": map[string]any{
					"attachmentId": attachment.ID,
					"alt":          "secret.png",
				},
			},
		},
	}
	if _, err := service.UpdateDocumentDraft(doc.ID, content, 1); err != nil {
		t.Fatalf("pending encrypted draft: %v", err)
	}

	copied, err := service.DuplicateDocument(doc.ID)
	if err != nil {
		t.Fatalf("duplicate encrypted document: %v", err)
	}
	if copied.Title != "Copy of Secret Note" || copied.Item.EncryptionState != EncryptionEncrypted {
		t.Fatalf("unexpected encrypted copy metadata: %#v", copied)
	}
	if text := extractText(copied.Content); text != "Secret duplicate text" {
		t.Fatalf("expected encrypted draft text, got %q", text)
	}
	copiedAttachmentIDs := attachmentIDsFromContent(copied.Content)
	if copiedAttachmentIDs[attachment.ID] || len(copiedAttachmentIDs) != 1 {
		t.Fatalf("expected copied encrypted content to reference one new attachment, got %#v", copiedAttachmentIDs)
	}
	for copiedAttachmentID := range copiedAttachmentIDs {
		if _, err := service.GetDocumentAttachmentDataURL(copiedAttachmentID); err != nil {
			t.Fatalf("get copied encrypted attachment: %v", err)
		}
	}
}

func TestCreateAndRenameDocumentInEncryptedJournal(t *testing.T) {
	service := newTestService(t)

	if err := service.CreateMasterPassword("correct horse battery staple"); err != nil {
		t.Fatalf("create master password: %v", err)
	}
	if err := service.UnlockEncryption("correct horse battery staple"); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	tree, err := service.GetLibraryTree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	journal := tree.Items[0]
	if _, err := service.EncryptJournal(journal.ID); err != nil {
		t.Fatalf("encrypt journal: %v", err)
	}
	doc, err := service.CreateDocument(journal.ID)
	if err != nil {
		t.Fatalf("create encrypted document: %v", err)
	}
	if _, err := service.RenameItem(doc.ID, "Encrypted Draft"); err != nil {
		t.Fatalf("rename encrypted document: %v", err)
	}
	opened, err := service.OpenDocument(doc.ID)
	if err != nil {
		t.Fatalf("open encrypted document: %v", err)
	}
	if opened.Title != "Encrypted Draft" {
		t.Fatalf("expected renamed encrypted document title, got %q", opened.Title)
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

func TestFolderItemCountIncludesDescendants(t *testing.T) {
	service := newTestService(t)

	folder, err := service.CreateFolder("", "Project")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	if _, err := service.CreateDocument(folder.Item.ID); err != nil {
		t.Fatalf("create document: %v", err)
	}
	childFolder, err := service.CreateFolder(folder.Item.ID, "Research")
	if err != nil {
		t.Fatalf("create child folder: %v", err)
	}
	if _, err := service.CreateDocument(childFolder.Item.ID); err != nil {
		t.Fatalf("create nested document: %v", err)
	}

	tree, err := service.GetLibraryTree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	project := findTreeItem(tree.Items, folder.Item.ID)
	if project == nil {
		t.Fatal("expected project folder")
	}
	if project.ItemCount != 3 {
		t.Fatalf("expected folder item badge count 3, got %d", project.ItemCount)
	}
}

func TestJournalCreateReorderAndPermanentDelete(t *testing.T) {
	service := newTestService(t)

	firstTree, err := service.GetLibraryTree()
	if err != nil {
		t.Fatalf("initial tree: %v", err)
	}
	defaultJournal := firstTree.Items[0]
	second, err := service.CreateJournal("Second")
	if err != nil {
		t.Fatalf("create journal: %v", err)
	}
	doc, err := service.CreateDocument(second.Item.ID)
	if err != nil {
		t.Fatalf("create document in second journal: %v", err)
	}

	tree, err := service.MoveItem(second.Item.ID, "", 0)
	if err != nil {
		t.Fatalf("reorder journal: %v", err)
	}
	if tree.Items[0].ID != second.Item.ID || tree.Items[1].ID != defaultJournal.ID {
		t.Fatalf("expected second journal first, got %#v", tree.Items[:2])
	}
	if tree.Items[0].DocumentCount != 1 {
		t.Fatalf("expected journal document badge count 1, got %d", tree.Items[0].DocumentCount)
	}

	tree, err = service.DeleteJournal(second.Item.ID)
	if err != nil {
		t.Fatalf("delete journal: %v", err)
	}
	if findTreeItem(tree.Items, second.Item.ID) != nil {
		t.Fatal("expected deleted journal to be removed permanently")
	}
	if _, err := service.OpenDocument(doc.ID); err == nil {
		t.Fatal("expected journal delete to remove contained document")
	}
}

func TestDeleteLastJournalRejected(t *testing.T) {
	service := newTestService(t)

	tree, err := service.GetLibraryTree()
	if err != nil {
		t.Fatalf("initial tree: %v", err)
	}
	journal := tree.Items[0]
	if _, err := service.DeleteJournal(journal.ID); err == nil {
		t.Fatal("expected deleting the last journal to fail")
	}
	tree, err = service.GetLibraryTree()
	if err != nil {
		t.Fatalf("tree after rejected delete: %v", err)
	}
	if findTreeItem(tree.Items, journal.ID) == nil {
		t.Fatal("expected last journal to remain after rejected delete")
	}
}

func TestCrossJournalDragCopiesFolderTreeWithFreshMetadata(t *testing.T) {
	service := newTestService(t)

	tree, err := service.GetLibraryTree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	sourceJournal := tree.Items[0]
	target, err := service.CreateJournal("Archive")
	if err != nil {
		t.Fatalf("create target journal: %v", err)
	}
	folder, err := service.CreateFolder(sourceJournal.ID, "Project")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	doc, err := service.CreateDocument(folder.Item.ID)
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	if _, err := service.RenameItem(doc.ID, "Original"); err != nil {
		t.Fatalf("rename document: %v", err)
	}
	if _, err := service.db.Exec(`UPDATE items SET created_at = ?, updated_at = ? WHERE id IN (?, ?)`, "2026-01-01T10:00:00Z", "2026-01-01T10:00:00Z", folder.Item.ID, doc.ID); err != nil {
		t.Fatalf("set old timestamps: %v", err)
	}

	copiedTree, err := service.MoveItem(folder.Item.ID, target.Item.ID, -1)
	if err != nil {
		t.Fatalf("copy folder across journals: %v", err)
	}
	original := findTreeItem(copiedTree.Items, folder.Item.ID)
	if original == nil || original.ParentID != sourceJournal.ID {
		t.Fatalf("expected original folder to remain in source journal, got %#v", original)
	}
	targetJournal := findTreeItem(copiedTree.Items, target.Item.ID)
	if targetJournal == nil || len(targetJournal.Children) != 1 {
		t.Fatalf("expected copied folder in target journal, got %#v", targetJournal)
	}
	copiedFolder := targetJournal.Children[0]
	if copiedFolder.ID == folder.Item.ID || copiedFolder.CreatedAt == "2026-01-01T10:00:00Z" || copiedFolder.UpdatedAt == "2026-01-01T10:00:00Z" {
		t.Fatalf("expected copied folder with fresh id/timestamps, got %#v", copiedFolder)
	}
	if len(copiedFolder.Children) != 1 {
		t.Fatalf("expected copied document child, got %#v", copiedFolder.Children)
	}
	copiedDoc := copiedFolder.Children[0]
	if copiedDoc.ID == doc.ID || copiedDoc.Title != "Original" || copiedDoc.CreatedAt == "2026-01-01T10:00:00Z" || copiedDoc.UpdatedAt == "2026-01-01T10:00:00Z" {
		t.Fatalf("expected copied document with same title and fresh metadata, got %#v", copiedDoc)
	}
}

func TestDraftVersionsRejectStaleContent(t *testing.T) {
	service := newTestService(t)

	doc, err := service.CreateDocument("")
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	older := proseMirrorDoc("older draft")
	newer := proseMirrorDoc("newer draft")
	if _, err := service.UpdateDocumentDraft(doc.ID, newer, 2); err != nil {
		t.Fatalf("newer draft: %v", err)
	}
	if response, err := service.UpdateDocumentDraft(doc.ID, older, 1); err != nil {
		t.Fatalf("stale draft should be ignored without error: %v", err)
	} else if response.Version != 2 {
		t.Fatalf("expected stale response to report accepted version 2, got %d", response.Version)
	}
	if _, err := service.FlushDocument(doc.ID); err != nil {
		t.Fatalf("flush: %v", err)
	}
	opened, err := service.OpenDocument(doc.ID)
	if err != nil {
		t.Fatalf("open document: %v", err)
	}
	if text := extractText(opened.Content); text != "newer draft" {
		t.Fatalf("expected newest draft to persist, got %q", text)
	}
	if _, err := service.UpdateDocumentDraft(doc.ID, older, 1); err != nil {
		t.Fatalf("post-flush stale draft should be ignored without error: %v", err)
	}
	if _, err := service.FlushDocument(doc.ID); err != nil {
		t.Fatalf("flush stale: %v", err)
	}
	opened, err = service.OpenDocument(doc.ID)
	if err != nil {
		t.Fatalf("reopen document: %v", err)
	}
	if text := extractText(opened.Content); text != "newer draft" {
		t.Fatalf("expected stale draft not to resurrect, got %q", text)
	}
}

func TestCrossJournalCopyUsesPendingDraftContent(t *testing.T) {
	service := newTestService(t)

	tree, err := service.GetLibraryTree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	sourceJournal := tree.Items[0]
	target, err := service.CreateJournal("Archive")
	if err != nil {
		t.Fatalf("create target journal: %v", err)
	}
	doc, err := service.CreateDocument(sourceJournal.ID)
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	if _, err := service.UpdateDocumentDraft(doc.ID, proseMirrorDoc("pending copy text"), 1); err != nil {
		t.Fatalf("pending draft: %v", err)
	}

	copiedTree, err := service.MoveItem(doc.ID, target.Item.ID, -1)
	if err != nil {
		t.Fatalf("copy document across journals: %v", err)
	}
	targetJournal := findTreeItem(copiedTree.Items, target.Item.ID)
	if targetJournal == nil || len(targetJournal.Children) != 1 {
		t.Fatalf("expected copied document in target journal, got %#v", targetJournal)
	}
	copied, err := service.OpenDocument(targetJournal.Children[0].ID)
	if err != nil {
		t.Fatalf("open copied document: %v", err)
	}
	if text := extractText(copied.Content); text != "pending copy text" {
		t.Fatalf("expected copy to use pending draft, got %q", text)
	}
}

func proseMirrorDoc(text string) map[string]any {
	return map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": text},
				},
			},
		},
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
	if trash.ItemCount != 2 {
		t.Fatalf("expected trash item badge count 2, got %d", trash.ItemCount)
	}
	movedFolder := trash.Children[0]
	if movedFolder.ID != folder.Item.ID || len(movedFolder.Children) != 1 || movedFolder.Children[0].ID != doc.ID {
		t.Fatalf("expected descendant document to remain under moved folder, got %#v", movedFolder)
	}
}

func TestEncryptJournalHidesContentFromPlaintextColumnsAndSearch(t *testing.T) {
	service := newTestService(t)

	tree, err := service.GetLibraryTree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	journal := tree.Items[0]
	folder, err := service.CreateFolder(journal.ID, "Private Folder")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	doc, err := service.CreateDocument(folder.Item.ID)
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	if _, err := service.RenameItem(doc.ID, "Secret Plan"); err != nil {
		t.Fatalf("rename doc: %v", err)
	}
	if _, err := service.UpdateDocumentDraft(doc.ID, proseMirrorDoc("buried treasure"), 1); err != nil {
		t.Fatalf("draft: %v", err)
	}
	if _, err := service.FlushDocument(doc.ID); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if results, err := service.SearchLibrary("treasure"); err != nil {
		t.Fatalf("search before encrypt: %v", err)
	} else if len(results.ResultIDs) != 1 {
		t.Fatalf("expected plaintext search hit before encryption, got %#v", results.ResultIDs)
	}
	if _, err := service.EncryptJournal(journal.ID); err == nil {
		t.Fatal("expected encryption to require a master password")
	}
	if err := service.CreateMasterPassword("correct horse battery staple"); err != nil {
		t.Fatalf("create master password: %v", err)
	}
	encryptedTree, err := service.EncryptJournal(journal.ID)
	if err != nil {
		t.Fatalf("encrypt journal: %v", err)
	}
	encryptedJournal := findTreeItem(encryptedTree.Items, journal.ID)
	if encryptedJournal == nil || encryptedJournal.EncryptionState != EncryptionEncrypted || encryptedJournal.EncryptionLocked {
		t.Fatalf("expected unlocked encrypted journal, got %#v", encryptedJournal)
	}

	var storedTitle string
	var storedContent string
	if err := service.db.QueryRow(`SELECT title FROM items WHERE id = ?`, doc.ID).Scan(&storedTitle); err != nil {
		t.Fatalf("stored title: %v", err)
	}
	if storedTitle == "Secret Plan" {
		t.Fatal("expected document title to be encrypted in storage")
	}
	if err := service.db.QueryRow(`SELECT content_json FROM documents WHERE item_id = ?`, doc.ID).Scan(&storedContent); err != nil {
		t.Fatalf("stored content: %v", err)
	}
	if storedContent == "" || storedContent == `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"buried treasure"}]}]}` {
		t.Fatalf("expected document content_json to be replaced with a placeholder, got %q", storedContent)
	}

	results, err := service.SearchLibrary("treasure")
	if err != nil {
		t.Fatalf("search after encrypt: %v", err)
	}
	if len(results.ResultIDs) != 0 {
		t.Fatalf("expected encrypted content to be excluded from search, got %#v", results.ResultIDs)
	}
	opened, err := service.OpenDocument(doc.ID)
	if err != nil {
		t.Fatalf("open encrypted document while unlocked: %v", err)
	}
	if opened.Title != "Secret Plan" || extractText(opened.Content) != "buried treasure" {
		t.Fatalf("expected decrypted document, got title=%q text=%q", opened.Title, extractText(opened.Content))
	}
}

func TestUnlockAndChangeMasterPasswordRelocksEncryptedJournals(t *testing.T) {
	service := newTestService(t)

	tree, err := service.GetLibraryTree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	journal := tree.Items[0]
	doc, err := service.CreateDocument(journal.ID)
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}
	if _, err := service.UpdateDocumentDraft(doc.ID, proseMirrorDoc("locked text"), 1); err != nil {
		t.Fatalf("draft: %v", err)
	}
	if _, err := service.FlushDocument(doc.ID); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := service.CreateMasterPassword("old password"); err != nil {
		t.Fatalf("create master password: %v", err)
	}
	if _, err := service.EncryptJournal(journal.ID); err != nil {
		t.Fatalf("encrypt journal: %v", err)
	}
	if err := service.ChangeMasterPassword("old password", "new password"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	status, err := service.GetEncryptionStatus()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Unlocked {
		t.Fatal("expected password change to relock encrypted journals")
	}
	lockedTree, err := service.GetLibraryTree()
	if err != nil {
		t.Fatalf("locked tree: %v", err)
	}
	lockedJournal := findTreeItem(lockedTree.Items, journal.ID)
	if lockedJournal == nil || !lockedJournal.EncryptionLocked || len(lockedJournal.Children) != 0 {
		t.Fatalf("expected locked journal with hidden children, got %#v", lockedJournal)
	}
	if _, err := service.OpenDocument(doc.ID); err == nil {
		t.Fatal("expected opening encrypted document to require unlock")
	}
	if err := service.UnlockEncryption("old password"); err == nil {
		t.Fatal("expected old password to fail after password change")
	}
	if err := service.UnlockEncryption("new password"); err != nil {
		t.Fatalf("unlock with new password: %v", err)
	}
	opened, err := service.OpenDocument(doc.ID)
	if err != nil {
		t.Fatalf("open after unlock: %v", err)
	}
	if text := extractText(opened.Content); text != "locked text" {
		t.Fatalf("expected decrypted text after unlock, got %q", text)
	}
}

func TestDecryptJournalRestoresPlaintextSearchAndBlocksBoundaryMoves(t *testing.T) {
	service := newTestService(t)

	tree, err := service.GetLibraryTree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	journal := tree.Items[0]
	other, err := service.CreateJournal("Plain")
	if err != nil {
		t.Fatalf("create other journal: %v", err)
	}
	doc, err := service.CreateDocument(journal.ID)
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}
	if _, err := service.RenameItem(doc.ID, "Move Me"); err != nil {
		t.Fatalf("rename doc: %v", err)
	}
	if _, err := service.UpdateDocumentDraft(doc.ID, proseMirrorDoc("searchable again"), 1); err != nil {
		t.Fatalf("draft: %v", err)
	}
	if _, err := service.FlushDocument(doc.ID); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := service.CreateMasterPassword("password"); err != nil {
		t.Fatalf("create master password: %v", err)
	}
	if _, err := service.EncryptJournal(journal.ID); err != nil {
		t.Fatalf("encrypt journal: %v", err)
	}
	if _, err := service.MoveItem(doc.ID, other.Item.ID, -1); err == nil {
		t.Fatal("expected moving encrypted item into plaintext journal to fail")
	}
	decryptedTree, err := service.DecryptJournal(journal.ID)
	if err != nil {
		t.Fatalf("decrypt journal: %v", err)
	}
	if findTreeItem(decryptedTree.Items, journal.ID) != nil {
		t.Fatal("expected encrypted source journal id to be replaced")
	}
	results, err := service.SearchLibrary("searchable")
	if err != nil {
		t.Fatalf("search after decrypt: %v", err)
	}
	if len(results.ResultIDs) != 1 {
		t.Fatalf("expected decrypted content to be searchable, got %#v", results.ResultIDs)
	}
	opened, err := service.OpenDocument(results.ResultIDs[0])
	if err != nil {
		t.Fatalf("open decrypted copy: %v", err)
	}
	if opened.Title != "Move Me" || extractText(opened.Content) != "searchable again" {
		t.Fatalf("expected plaintext replacement document, got title=%q text=%q", opened.Title, extractText(opened.Content))
	}
}

func TestImportMarkdownDirectoryImportsNestedFiles(t *testing.T) {
	service := newTestService(t)
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "Root Note.md"), []byte("# Root Note\n\nTop level body.\n"), 0o644); err != nil {
		t.Fatalf("write root markdown: %v", err)
	}
	nestedDir := filepath.Join(sourceDir, "Projects")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("make nested folder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "Plan.md"), []byte("1. First\n2. Second\n"), 0o644); err != nil {
		t.Fatalf("write nested markdown: %v", err)
	}

	response, err := service.ImportMarkdownDirectory(sourceDir)
	if err != nil {
		t.Fatalf("import markdown directory: %v", err)
	}
	if response.Item.Kind != KindJournal {
		t.Fatalf("expected imported journal, got %#v", response.Item)
	}
	imported := findTreeItem(response.Tree.Items, response.Item.ID)
	if imported == nil {
		t.Fatalf("expected imported journal in tree, got %#v", response.Tree.Items)
	}
	rootDoc := findTreeItemByTitle(imported.Children, "Root Note")
	if rootDoc == nil || rootDoc.Kind != KindDocument {
		t.Fatalf("expected root markdown document, got %#v", imported.Children)
	}
	projects := findTreeItemByTitle(imported.Children, "Projects")
	if projects == nil || projects.Kind != KindFolder {
		t.Fatalf("expected nested folder, got %#v", imported.Children)
	}
	plan := findTreeItemByTitle(projects.Children, "Plan")
	if plan == nil || plan.Kind != KindDocument {
		t.Fatalf("expected nested markdown document, got %#v", projects.Children)
	}
	opened, err := service.OpenDocument(plan.ID)
	if err != nil {
		t.Fatalf("open nested imported document: %v", err)
	}
	if text := extractText(opened.Content); !strings.Contains(text, "First") || !strings.Contains(text, "Second") {
		t.Fatalf("expected imported ordered list text, got %q", text)
	}
}

func TestExportJournalManifestOmitsDocumentContent(t *testing.T) {
	service := newTestService(t)
	tree, err := service.GetLibraryTree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	journal := tree.Items[0]
	doc, err := service.CreateDocument(journal.ID)
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	if _, err := service.RenameItem(doc.ID, "Exported Note"); err != nil {
		t.Fatalf("rename document: %v", err)
	}
	content := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "Body text should live in markdown."},
				},
			},
		},
	}
	if _, err := service.UpdateDocumentDraft(doc.ID, content, 1); err != nil {
		t.Fatalf("draft: %v", err)
	}
	if _, err := service.FlushDocument(doc.ID); err != nil {
		t.Fatalf("flush: %v", err)
	}
	exportDir := t.TempDir()
	if err := service.ExportJournalToDirectory(journal.ID, exportDir); err != nil {
		t.Fatalf("export journal: %v", err)
	}
	manifestPath := filepath.Join(exportDir, journal.Title, journalExportManifestName)
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if strings.Contains(string(manifest), `"content"`) || strings.Contains(string(manifest), "Body text should live in markdown.") {
		t.Fatalf("manifest should not contain document body JSON: %s", manifest)
	}
	markdown, err := os.ReadFile(filepath.Join(exportDir, journal.Title, "Exported Note.md"))
	if err != nil {
		t.Fatalf("read exported markdown: %v", err)
	}
	if !strings.Contains(string(markdown), "Body text should live in markdown.") {
		t.Fatalf("expected body in markdown, got %q", markdown)
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

func findTreeItemByTitle(items []TreeItem, title string) *TreeItem {
	for i := range items {
		if items[i].Title == title {
			return &items[i]
		}
		if found := findTreeItemByTitle(items[i].Children, title); found != nil {
			return found
		}
	}
	return nil
}
