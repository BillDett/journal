package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	journalExportManifestName    = ".journal-export.json"
	journalExportManifestVersion = 1
)

var (
	windowsReservedNames = map[string]bool{
		"CON": true, "PRN": true, "AUX": true, "NUL": true,
		"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
		"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
	}
	headingPattern      = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	orderedItemPattern  = regexp.MustCompile(`^(\d+)\.\s+(.*)$`)
	bulletItemPattern   = regexp.MustCompile(`^[-*+]\s+(.*)$`)
	taskItemPattern     = regexp.MustCompile(`^[-*+]\s+\[( |x|X)\]\s+(.*)$`)
	standaloneImageExpr = regexp.MustCompile(`^!\[(.*?)\]\(([^)]+)\)$`)
	linkPattern         = regexp.MustCompile(`^\[(.+?)\]\((.+?)\)`)
	codePattern         = regexp.MustCompile("^`([^`]+)`")
	boldPattern         = regexp.MustCompile(`^\*\*(.+?)\*\*`)
	strikePattern       = regexp.MustCompile(`^~~(.+?)~~`)
	italicStarPattern   = regexp.MustCompile(`^\*(.+?)\*`)
	italicUnderPattern  = regexp.MustCompile(`^_(.+?)_`)
)

type JournalExportManifest struct {
	Version      int                         `json:"version"`
	JournalID    string                      `json:"journalId"`
	JournalTitle string                      `json:"journalTitle"`
	CreatedAt    string                      `json:"createdAt"`
	UpdatedAt    string                      `json:"updatedAt"`
	Nodes        []JournalExportManifestNode `json:"nodes"`
}

type JournalExportManifestNode struct {
	Kind          string                      `json:"kind"`
	Title         string                      `json:"title"`
	SortOrder     int                         `json:"sortOrder"`
	CreatedAt     string                      `json:"createdAt"`
	UpdatedAt     string                      `json:"updatedAt"`
	File          string                      `json:"file,omitempty"`
	SpacingPreset string                      `json:"spacingPreset,omitempty"`
	Children      []JournalExportManifestNode `json:"children,omitempty"`
}

type JournalExportAttachment struct {
	ID           string `json:"id"`
	File         string `json:"file"`
	OriginalName string `json:"originalName"`
	MimeType     string `json:"mimeType"`
	SizeBytes    int    `json:"sizeBytes"`
}

type exportAttachmentData struct {
	ID           string
	OriginalName string
	MimeType     string
	Data         []byte
}

type importAssetRef struct {
	Alt           string
	OriginalName  string
	Path          string
	PlaceholderID string
}

type parsedMarkdownDocument struct {
	Content map[string]any
	Assets  []importAssetRef
}

func (a *App) ExportJournalDirectory(journalID string) error {
	if a.ctx == nil {
		return fmt.Errorf("app is not ready")
	}
	targetDir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Export Journal",
	})
	if err != nil {
		return err
	}
	if strings.TrimSpace(targetDir) == "" {
		return nil
	}
	return a.service.ExportJournalToDirectory(journalID, targetDir)
}

func (a *App) ImportMarkdownDirectory() (ItemResponse, error) {
	if a.ctx == nil {
		return ItemResponse{}, fmt.Errorf("app is not ready")
	}
	sourceDir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Import Markdown Folder",
	})
	if err != nil {
		return ItemResponse{}, err
	}
	if strings.TrimSpace(sourceDir) == "" {
		return ItemResponse{}, nil
	}
	return a.service.ImportMarkdownDirectory(sourceDir)
}

func (s *JournalService) ExportJournalToDirectory(journalID string, targetDir string) error {
	targetDir = strings.TrimSpace(targetDir)
	if targetDir == "" {
		return fmt.Errorf("target directory is required")
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}

	rootJournal, tree, err := s.exportJournalTree(journalID)
	if err != nil {
		return err
	}
	if rootJournal.EncryptionState == EncryptionEncrypted {
		if _, ok := s.journalKey(journalID); !ok {
			return ErrEncryptionLocked
		}
	}

	rootName := uniqueChildName(targetDir, sanitizeFSName(rootJournal.Title, "Journal"), "")
	rootPath := filepath.Join(targetDir, rootName)
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		return err
	}

	manifest := JournalExportManifest{
		Version:      journalExportManifestVersion,
		JournalID:    rootJournal.ID,
		JournalTitle: rootJournal.Title,
		CreatedAt:    rootJournal.CreatedAt,
		UpdatedAt:    rootJournal.UpdatedAt,
	}
	for _, child := range rootJournal.Children {
		node, err := s.exportTreeNode(child, rootPath, rootPath)
		if err != nil {
			return err
		}
		manifest.Nodes = append(manifest.Nodes, node)
	}

	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(rootPath, journalExportManifestName), encoded, 0o644); err != nil {
		return err
	}

	_ = tree
	return nil
}

func (s *JournalService) ImportMarkdownDirectory(sourceDir string) (ItemResponse, error) {
	sourceDir = strings.TrimSpace(sourceDir)
	if sourceDir == "" {
		return ItemResponse{}, fmt.Errorf("source directory is required")
	}
	info, err := os.Stat(sourceDir)
	if err != nil {
		return ItemResponse{}, err
	}
	if !info.IsDir() {
		return ItemResponse{}, fmt.Errorf("source must be a directory")
	}

	manifestPath := filepath.Join(sourceDir, journalExportManifestName)
	if _, err := os.Stat(manifestPath); err == nil {
		return s.importJournalExportManifest(sourceDir, manifestPath)
	}
	if err != nil && !os.IsNotExist(err) {
		return ItemResponse{}, err
	}
	return s.importMarkdownTree(sourceDir)
}

func (s *JournalService) exportJournalTree(journalID string) (TreeItem, TreeResponse, error) {
	tree, err := s.GetLibraryTree()
	if err != nil {
		return TreeItem{}, TreeResponse{}, err
	}
	journal := findTreeItemByID(tree.Items, journalID)
	if journal == nil || journal.Kind != KindJournal {
		return TreeItem{}, TreeResponse{}, fmt.Errorf("journal not found")
	}
	return *journal, tree, nil
}

func (s *JournalService) exportTreeNode(node TreeItem, currentDir string, rootDir string) (JournalExportManifestNode, error) {
	switch node.Kind {
	case KindFolder:
		dirName := uniqueChildName(currentDir, sanitizeFSName(node.Title, "Folder"), "")
		nextDir := filepath.Join(currentDir, dirName)
		if err := os.MkdirAll(nextDir, 0o755); err != nil {
			return JournalExportManifestNode{}, err
		}
		exported := JournalExportManifestNode{
			Kind:      node.Kind,
			Title:     node.Title,
			SortOrder: node.SortOrder,
			CreatedAt: node.CreatedAt,
			UpdatedAt: node.UpdatedAt,
		}
		for _, child := range node.Children {
			childNode, err := s.exportTreeNode(child, nextDir, rootDir)
			if err != nil {
				return JournalExportManifestNode{}, err
			}
			exported.Children = append(exported.Children, childNode)
		}
		return exported, nil
	case KindDocument:
		doc, err := s.OpenDocument(node.ID)
		if err != nil {
			return JournalExportManifestNode{}, err
		}
		attachments, err := s.exportDocumentAttachments(node.ID, doc.Content)
		if err != nil {
			return JournalExportManifestNode{}, err
		}
		fileName := uniqueChildName(currentDir, sanitizeFSName(node.Title, "Untitled"), ".md")
		filePath := filepath.Join(currentDir, fileName)
		assetDirName := strings.TrimSuffix(fileName, ".md") + ".assets"
		assetDirPath := filepath.Join(currentDir, assetDirName)
		imageRefs := map[string]string{}
		if len(attachments) > 0 {
			if err := os.MkdirAll(assetDirPath, 0o755); err != nil {
				return JournalExportManifestNode{}, err
			}
			for _, attachment := range attachments {
				assetName := uniqueChildName(assetDirPath, sanitizeFSName(trimExtension(attachment.OriginalName), "image"), extensionOrDefault(attachment.OriginalName, mimeExtension(attachment.MimeType)))
				assetPath := filepath.Join(assetDirPath, assetName)
				if err := os.WriteFile(assetPath, attachment.Data, 0o644); err != nil {
					return JournalExportManifestNode{}, err
				}
				relativeFromDoc := filepath.ToSlash(filepath.Join(assetDirName, assetName))
				imageRefs[attachment.ID] = relativeFromDoc
			}
		}
		markdown := renderMarkdownDocument(doc.Content, imageRefs)
		if err := os.WriteFile(filePath, []byte(markdown), 0o644); err != nil {
			return JournalExportManifestNode{}, err
		}
		relativePath, err := filepath.Rel(rootDir, filePath)
		if err != nil {
			return JournalExportManifestNode{}, err
		}
		return JournalExportManifestNode{
			Kind:          node.Kind,
			Title:         doc.Title,
			SortOrder:     node.SortOrder,
			CreatedAt:     node.CreatedAt,
			UpdatedAt:     node.UpdatedAt,
			File:          filepath.ToSlash(relativePath),
			SpacingPreset: normalizeSpacingPreset(doc.SpacingPreset),
		}, nil
	default:
		return JournalExportManifestNode{}, fmt.Errorf("unsupported item kind %q", node.Kind)
	}
}

func (s *JournalService) exportDocumentAttachments(documentID string, content map[string]any) ([]exportAttachmentData, error) {
	referenced := attachmentIDsFromContent(content)
	if len(referenced) == 0 {
		return nil, nil
	}

	item, err := s.getRawRowItemFrom(s.db, documentID)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query(
		`SELECT id, mime_type, original_name, content_blob, content_ciphertext
		 FROM document_attachments WHERE document_id = ? AND detached_at IS NULL`,
		documentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var key []byte
	if item.EncryptionState == EncryptionEncrypted {
		journalID, err := s.journalIDForItem(documentID)
		if err != nil {
			return nil, err
		}
		var ok bool
		key, ok = s.journalKey(journalID)
		if !ok {
			return nil, ErrEncryptionLocked
		}
	}

	var attachments []exportAttachmentData
	for rows.Next() {
		var id, mimeType, originalName string
		var contentBlob, contentCiphertext []byte
		if err := rows.Scan(&id, &mimeType, &originalName, &contentBlob, &contentCiphertext); err != nil {
			return nil, err
		}
		if !referenced[id] {
			continue
		}
		data := contentBlob
		if item.EncryptionState == EncryptionEncrypted {
			if !item.EncryptionKeyID.Valid {
				return nil, fmt.Errorf("encrypted document key is missing")
			}
			plaintext, err := openField(key, "document_attachments", id, "content_blob", item.EncryptionKeyID.String, contentCiphertext)
			if err != nil {
				return nil, err
			}
			data = plaintext
		}
		attachments = append(attachments, exportAttachmentData{
			ID:           id,
			OriginalName: originalName,
			MimeType:     mimeType,
			Data:         slices.Clone(data),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(attachments, func(a, b exportAttachmentData) int {
		return strings.Compare(a.ID, b.ID)
	})
	return attachments, nil
}

func (s *JournalService) importJournalExportManifest(sourceDir string, manifestPath string) (ItemResponse, error) {
	encoded, err := os.ReadFile(manifestPath)
	if err != nil {
		return ItemResponse{}, err
	}
	var manifest JournalExportManifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return ItemResponse{}, err
	}
	if manifest.Version != journalExportManifestVersion {
		return ItemResponse{}, fmt.Errorf("unsupported export manifest version")
	}
	title := normalizeTitle(manifest.JournalTitle, sanitizeFSName(filepath.Base(sourceDir), "Imported Journal"))
	return s.importJournalWithNodes(title, manifest.CreatedAt, manifest.UpdatedAt, manifest.Nodes, sourceDir, true)
}

func (s *JournalService) importMarkdownTree(sourceDir string) (ItemResponse, error) {
	title := normalizeTitle(filepath.Base(sourceDir), "Imported Journal")
	nodes, err := buildMarkdownImportNodes(sourceDir, sourceDir)
	if err != nil {
		return ItemResponse{}, err
	}
	return s.importJournalWithNodes(title, nowString(), nowString(), nodes, sourceDir, false)
}

func (s *JournalService) importJournalWithNodes(title string, createdAt string, updatedAt string, nodes []JournalExportManifestNode, sourceDir string, useManifest bool) (ItemResponse, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return ItemResponse{}, err
	}
	defer rollback(tx)

	journalID, err := s.insertImportedJournalTx(tx, title, createdAt, updatedAt)
	if err != nil {
		return ItemResponse{}, err
	}
	for _, node := range nodes {
		if err := s.importNodeTx(tx, sourceDir, journalID, node, useManifest); err != nil {
			return ItemResponse{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ItemResponse{}, err
	}
	item, err := s.getTreeItem(journalID)
	if err != nil {
		return ItemResponse{}, err
	}
	tree, err := s.GetLibraryTree()
	if err != nil {
		return ItemResponse{}, err
	}
	return ItemResponse{Item: item, Tree: tree}, nil
}

func (s *JournalService) importNodeTx(tx *sql.Tx, sourceDir string, parentID string, node JournalExportManifestNode, useManifest bool) error {
	switch node.Kind {
	case KindFolder:
		folderID, err := s.insertImportedFolderTx(tx, parentID, node.Title, node.SortOrder, node.CreatedAt, node.UpdatedAt)
		if err != nil {
			return err
		}
		for _, child := range node.Children {
			if err := s.importNodeTx(tx, sourceDir, folderID, child, useManifest); err != nil {
				return err
			}
		}
		return nil
	case KindDocument:
		docPath := filepath.Join(sourceDir, filepath.FromSlash(node.File))
		parsed, err := parseMarkdownFile(docPath)
		if err != nil {
			return err
		}
		content := parsed.Content
		var attachments []JournalExportAttachment
		for _, asset := range parsed.Assets {
			attachments = append(attachments, JournalExportAttachment{
				ID:           asset.PlaceholderID,
				File:         filepath.ToSlash(asset.Path),
				OriginalName: asset.OriginalName,
				MimeType:     "",
			})
		}
		documentID, err := s.insertImportedDocumentTx(tx, parentID, node.Title, node.SortOrder, node.CreatedAt, node.UpdatedAt, normalizeSpacingPreset(node.SpacingPreset), emptyDocument())
		if err != nil {
			return err
		}
		idMap := map[string]string{}
		for _, attachment := range attachments {
			attachmentPath := importAssetPath(sourceDir, attachment.File)
			data, err := os.ReadFile(attachmentPath)
			if err != nil {
				return err
			}
			if len(data) > maxImageAttachmentBytes {
				return fmt.Errorf("image %q exceeds the 20 MB limit", attachmentPath)
			}
			mimeType := attachment.MimeType
			if mimeType == "" {
				mimeType = normalizeImageMimeType(attachment.OriginalName, mimeType, data)
			}
			if mimeType == "" {
				return fmt.Errorf("unsupported image format for %q", attachmentPath)
			}
			newID, err := s.insertImportedAttachmentTx(tx, documentID, attachment.OriginalName, mimeType, data)
			if err != nil {
				return err
			}
			idMap[attachment.ID] = newID
		}
		if len(idMap) > 0 {
			content = remapAttachmentIDsInContent(content, idMap)
		}
		encoded, err := json.Marshal(content)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE documents SET content_json = ? WHERE item_id = ?`, string(encoded), documentID); err != nil {
			return err
		}
		if err := s.syncFTSTx(tx, documentID); err != nil {
			return err
		}
		return nil
	default:
		return nil
	}
}

func (s *JournalService) insertImportedJournalTx(tx *sql.Tx, title string, createdAt string, updatedAt string) (string, error) {
	id := uuid.NewString()
	var next sql.NullInt64
	err := tx.QueryRow(
		`SELECT COALESCE(MAX(sort_order), -1) + 1 FROM items WHERE parent_id IS NULL AND kind = ?`,
		KindJournal,
	).Scan(&next)
	if err != nil {
		return "", err
	}
	order := int(next.Int64)
	if createdAt == "" {
		createdAt = nowString()
	}
	if updatedAt == "" {
		updatedAt = createdAt
	}
	if _, err := tx.Exec(
		`INSERT INTO items (id, parent_id, kind, title, sort_order, created_at, updated_at)
		 VALUES (?, NULL, ?, ?, ?, ?, ?)`,
		id, KindJournal, normalizeTitle(title, "Imported Journal"), order, createdAt, updatedAt,
	); err != nil {
		return "", err
	}
	if err := s.syncFTSTx(tx, id); err != nil {
		return "", err
	}
	return id, nil
}

func (s *JournalService) insertImportedFolderTx(tx *sql.Tx, parentID string, title string, sortOrder int, createdAt string, updatedAt string) (string, error) {
	id := uuid.NewString()
	if createdAt == "" {
		createdAt = nowString()
	}
	if updatedAt == "" {
		updatedAt = createdAt
	}
	if _, err := tx.Exec(
		`INSERT INTO items (id, parent_id, kind, title, sort_order, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, parentID, KindFolder, normalizeTitle(title, "New Folder"), sortOrder, createdAt, updatedAt,
	); err != nil {
		return "", err
	}
	if err := s.syncFTSTx(tx, id); err != nil {
		return "", err
	}
	return id, nil
}

func (s *JournalService) insertImportedDocumentTx(tx *sql.Tx, parentID string, title string, sortOrder int, createdAt string, updatedAt string, spacingPreset string, content map[string]any) (string, error) {
	id := uuid.NewString()
	if createdAt == "" {
		createdAt = nowString()
	}
	if updatedAt == "" {
		updatedAt = createdAt
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(
		`INSERT INTO items (id, parent_id, kind, title, sort_order, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, parentID, KindDocument, normalizeTitle(title, "Untitled"), sortOrder, createdAt, updatedAt,
	); err != nil {
		return "", err
	}
	if _, err := tx.Exec(
		`INSERT INTO documents (item_id, schema_version, content_json, spacing_preset, created_at, updated_at)
		 VALUES (?, 1, ?, ?, ?, ?)`,
		id, string(encoded), normalizeSpacingPreset(spacingPreset), createdAt, updatedAt,
	); err != nil {
		return "", err
	}
	if err := s.syncFTSTx(tx, id); err != nil {
		return "", err
	}
	return id, nil
}

func (s *JournalService) insertImportedAttachmentTx(tx *sql.Tx, documentID string, originalName string, mimeType string, data []byte) (string, error) {
	id := uuid.NewString()
	now := nowString()
	if _, err := tx.Exec(
		`INSERT INTO document_attachments (id, document_id, mime_type, original_name, size_bytes, content_blob, content_ciphertext, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, NULL, ?)`,
		id, documentID, mimeType, originalName, len(data), data, now,
	); err != nil {
		return "", err
	}
	return id, nil
}

func buildMarkdownImportNodes(rootDir string, currentDir string) ([]JournalExportManifestNode, error) {
	entries, err := os.ReadDir(currentDir)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int {
		return strings.Compare(strings.ToLower(a.Name()), strings.ToLower(b.Name()))
	})
	var nodes []JournalExportManifestNode
	for index, entry := range entries {
		if shouldSkipImportEntry(entry.Name()) {
			continue
		}
		node, ok, err := buildMarkdownImportNode(rootDir, filepath.Join(currentDir, entry.Name()), entry, index)
		if err != nil {
			return nil, err
		}
		if ok {
			nodes = append(nodes, node)
		}
	}
	return nodes, nil
}

func buildMarkdownImportNode(rootDir string, fullPath string, entry os.DirEntry, sortOrder int) (JournalExportManifestNode, bool, error) {
	info, err := entry.Info()
	if err != nil {
		return JournalExportManifestNode{}, false, err
	}
	createdAt := info.ModTime().UTC().Format(time.RFC3339Nano)
	if entry.IsDir() {
		children, err := buildMarkdownImportNodes(rootDir, fullPath)
		if err != nil {
			return JournalExportManifestNode{}, false, err
		}
		if len(children) == 0 {
			return JournalExportManifestNode{}, false, nil
		}
		return JournalExportManifestNode{
			Kind:      KindFolder,
			Title:     entry.Name(),
			SortOrder: sortOrder,
			CreatedAt: createdAt,
			UpdatedAt: createdAt,
			Children:  children,
		}, true, nil
	}
	if !isMarkdownFilename(entry.Name()) {
		return JournalExportManifestNode{}, false, nil
	}
	relativeFile, err := filepath.Rel(rootDir, fullPath)
	if err != nil {
		return JournalExportManifestNode{}, false, err
	}
	return JournalExportManifestNode{
		Kind:          KindDocument,
		Title:         trimExtension(entry.Name()),
		SortOrder:     sortOrder,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
		File:          filepath.ToSlash(relativeFile),
		SpacingPreset: defaultSpacingPreset,
	}, true, nil
}

func shouldSkipImportEntry(name string) bool {
	if name == journalExportManifestName {
		return true
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	return false
}

func isMarkdownFilename(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown")
}

func renderMarkdownDocument(doc map[string]any, imageRefs map[string]string) string {
	content, _ := doc["content"].([]any)
	var blocks []string
	for _, node := range content {
		rendered := renderMarkdownBlock(node, imageRefs)
		if strings.TrimSpace(rendered) == "" {
			continue
		}
		blocks = append(blocks, rendered)
	}
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, "\n\n") + "\n"
}

func renderMarkdownBlock(node any, imageRefs map[string]string) string {
	typed, ok := node.(map[string]any)
	if !ok {
		return ""
	}
	switch typed["type"] {
	case "paragraph":
		return renderMarkdownInlineNodes(contentSlice(typed["content"]), imageRefs)
	case "heading":
		level := intAttr(typed, "level", 1)
		if level < 1 {
			level = 1
		}
		if level > 6 {
			level = 6
		}
		return strings.Repeat("#", level) + " " + renderMarkdownInlineNodes(contentSlice(typed["content"]), imageRefs)
	case "bulletList":
		return renderMarkdownList(contentSlice(typed["content"]), imageRefs, false, 0)
	case "orderedList":
		start := intAttr(typed, "start", 1)
		if start < 1 {
			start = 1
		}
		return renderMarkdownList(contentSlice(typed["content"]), imageRefs, true, start)
	case "taskList":
		return renderMarkdownTaskList(contentSlice(typed["content"]), imageRefs)
	case "blockquote":
		lines := strings.Split(renderMarkdownChildren(contentSlice(typed["content"]), imageRefs), "\n")
		for i, line := range lines {
			lines[i] = "> " + line
		}
		return strings.Join(lines, "\n")
	case "codeBlock":
		language := stringAttr(typed, "language")
		return "```" + language + "\n" + plainText(typed["content"]) + "\n```"
	case "horizontalRule":
		return "---"
	case "attachmentImage":
		attrs, _ := typed["attrs"].(map[string]any)
		alt, _ := attrs["alt"].(string)
		attachmentID, _ := attrs["attachmentId"].(string)
		path := imageRefs[attachmentID]
		if path == "" {
			return ""
		}
		return "![" + alt + "](" + path + ")"
	case "table":
		return renderMarkdownTable(contentSlice(typed["content"]), imageRefs)
	default:
		if content, ok := typed["content"].([]any); ok {
			return renderMarkdownChildren(content, imageRefs)
		}
		return ""
	}
}

func renderMarkdownChildren(nodes []any, imageRefs map[string]string) string {
	var blocks []string
	for _, child := range nodes {
		rendered := renderMarkdownBlock(child, imageRefs)
		if rendered != "" {
			blocks = append(blocks, rendered)
		}
	}
	return strings.Join(blocks, "\n\n")
}

func renderMarkdownInlineNodes(nodes []any, imageRefs map[string]string) string {
	var parts []string
	for _, node := range nodes {
		typed, ok := node.(map[string]any)
		if !ok {
			continue
		}
		switch typed["type"] {
		case "text":
			text, _ := typed["text"].(string)
			parts = append(parts, applyMarkdownMarks(escapeMarkdownText(text), typed["marks"]))
		case "hardBreak":
			parts = append(parts, "  \n")
		default:
			if content, ok := typed["content"].([]any); ok {
				parts = append(parts, renderMarkdownInlineNodes(content, imageRefs))
			}
		}
	}
	return strings.Join(parts, "")
}

func applyMarkdownMarks(text string, marks any) string {
	typed, ok := marks.([]any)
	if !ok {
		return text
	}
	rendered := text
	for _, markValue := range typed {
		mark, ok := markValue.(map[string]any)
		if !ok {
			continue
		}
		switch mark["type"] {
		case "code":
			rendered = "`" + text + "`"
		case "bold":
			rendered = "**" + rendered + "**"
		case "italic":
			rendered = "*" + rendered + "*"
		case "strike":
			rendered = "~~" + rendered + "~~"
		case "underline":
			rendered = "<u>" + rendered + "</u>"
		case "highlight":
			rendered = "<mark>" + rendered + "</mark>"
		case "link":
			attrs, _ := mark["attrs"].(map[string]any)
			href, _ := attrs["href"].(string)
			rendered = "[" + rendered + "](" + href + ")"
		}
	}
	return rendered
}

func renderMarkdownList(items []any, imageRefs map[string]string, ordered bool, start int) string {
	var lines []string
	for index, item := range items {
		rendered := renderMarkdownListItem(item, imageRefs, ordered, start+index)
		if rendered != "" {
			lines = append(lines, rendered)
		}
	}
	return strings.Join(lines, "\n")
}

func renderMarkdownTaskList(items []any, imageRefs map[string]string) string {
	var lines []string
	for _, item := range items {
		rendered := renderMarkdownTaskItem(item, imageRefs)
		if rendered != "" {
			lines = append(lines, rendered)
		}
	}
	return strings.Join(lines, "\n")
}

func renderMarkdownListItem(item any, imageRefs map[string]string, ordered bool, number int) string {
	typed, ok := item.(map[string]any)
	if !ok {
		return ""
	}
	content := contentSlice(typed["content"])
	prefix := "- "
	if ordered {
		prefix = strconv.Itoa(number) + ". "
	}
	return renderMarkdownListItemWithPrefix(content, imageRefs, prefix)
}

func renderMarkdownTaskItem(item any, imageRefs map[string]string) string {
	typed, ok := item.(map[string]any)
	if !ok {
		return ""
	}
	checked := boolAttr(typed, "checked")
	prefix := "- [ ] "
	if checked {
		prefix = "- [x] "
	}
	return renderMarkdownListItemWithPrefix(contentSlice(typed["content"]), imageRefs, prefix)
}

func renderMarkdownListItemWithPrefix(content []any, imageRefs map[string]string, prefix string) string {
	if len(content) == 0 {
		return prefix
	}
	var lines []string
	indent := strings.Repeat(" ", len(prefix))
	for index, child := range content {
		rendered := renderMarkdownBlock(child, imageRefs)
		if rendered == "" {
			continue
		}
		renderedLines := strings.Split(rendered, "\n")
		if index == 0 {
			renderedLines[0] = prefix + renderedLines[0]
		} else {
			renderedLines[0] = indent + renderedLines[0]
		}
		for lineIndex := 1; lineIndex < len(renderedLines); lineIndex += 1 {
			renderedLines[lineIndex] = indent + renderedLines[lineIndex]
		}
		lines = append(lines, strings.Join(renderedLines, "\n"))
	}
	return strings.Join(lines, "\n")
}

func renderMarkdownTable(rows []any, imageRefs map[string]string) string {
	if len(rows) == 0 {
		return ""
	}
	var tableRows [][]string
	for _, rowValue := range rows {
		row, ok := rowValue.(map[string]any)
		if !ok {
			continue
		}
		var cells []string
		for _, cellValue := range contentSlice(row["content"]) {
			cell, ok := cellValue.(map[string]any)
			if !ok {
				continue
			}
			cells = append(cells, sanitizeTableCell(renderMarkdownChildren(contentSlice(cell["content"]), imageRefs)))
		}
		tableRows = append(tableRows, cells)
	}
	if len(tableRows) == 0 {
		return ""
	}
	header := tableRows[0]
	var lines []string
	lines = append(lines, "| "+strings.Join(header, " | ")+" |")
	delimiter := make([]string, len(header))
	for i := range delimiter {
		delimiter[i] = "---"
	}
	lines = append(lines, "| "+strings.Join(delimiter, " | ")+" |")
	for _, row := range tableRows[1:] {
		lines = append(lines, "| "+strings.Join(row, " | ")+" |")
	}
	return strings.Join(lines, "\n")
}

func sanitizeTableCell(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "|", `\|`)
	return strings.TrimSpace(value)
}

func parseMarkdownFile(path string) (parsedMarkdownDocument, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return parsedMarkdownDocument{}, err
	}
	return parseMarkdownDocument(string(encoded), filepath.Dir(path))
}

func parseMarkdownDocument(source string, baseDir string) (parsedMarkdownDocument, error) {
	ctx := &markdownParseContext{baseDir: baseDir}
	lines := splitMarkdownLines(source)
	index := 0
	content := parseMarkdownBlocks(lines, &index, 0, ctx)
	return parsedMarkdownDocument{
		Content: map[string]any{
			"type":    "doc",
			"content": content,
		},
		Assets: ctx.assets,
	}, nil
}

type markdownParseContext struct {
	baseDir string
	assets  []importAssetRef
	count   int
}

func (ctx *markdownParseContext) nextAssetID() string {
	ctx.count += 1
	return fmt.Sprintf("import-image-%d", ctx.count)
}

func splitMarkdownLines(source string) []string {
	normalized := strings.ReplaceAll(source, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	return strings.Split(normalized, "\n")
}

func parseMarkdownBlocks(lines []string, index *int, baseIndent int, ctx *markdownParseContext) []any {
	var blocks []any
	for *index < len(lines) {
		line := lines[*index]
		if strings.TrimSpace(line) == "" {
			*index += 1
			continue
		}
		if leadingIndent(line) < baseIndent {
			break
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case isFenceLine(trimmed):
			blocks = append(blocks, parseMarkdownCodeBlock(lines, index))
		case isHorizontalRule(trimmed):
			blocks = append(blocks, map[string]any{"type": "horizontalRule"})
			*index += 1
		case headingPattern.MatchString(trimmed):
			blocks = append(blocks, parseMarkdownHeading(trimmed))
			*index += 1
		case isTableStart(lines, *index):
			blocks = append(blocks, parseMarkdownTable(lines, index))
		case strings.HasPrefix(trimmed, ">"):
			blocks = append(blocks, parseMarkdownBlockquote(lines, index, ctx))
		case standaloneImageExpr.MatchString(trimmed):
			blocks = append(blocks, parseMarkdownStandaloneImage(trimmed, ctx))
			*index += 1
		case isTaskListItem(trimmed):
			blocks = append(blocks, parseMarkdownList(lines, index, baseIndent, ctx, "task"))
		case isBulletListItem(trimmed):
			blocks = append(blocks, parseMarkdownList(lines, index, baseIndent, ctx, "bullet"))
		case isOrderedListItem(trimmed):
			blocks = append(blocks, parseMarkdownList(lines, index, baseIndent, ctx, "ordered"))
		default:
			blocks = append(blocks, parseMarkdownParagraph(lines, index, baseIndent))
		}
	}
	return blocks
}

func parseMarkdownCodeBlock(lines []string, index *int) map[string]any {
	open := strings.TrimSpace(lines[*index])
	fence := "```"
	if strings.HasPrefix(open, "~~~") {
		fence = "~~~"
	}
	language := strings.TrimSpace(strings.TrimPrefix(open, fence))
	*index += 1
	var body []string
	for *index < len(lines) {
		line := lines[*index]
		if strings.TrimSpace(line) == fence {
			*index += 1
			break
		}
		body = append(body, line)
		*index += 1
	}
	content := []any{}
	if len(body) > 0 {
		content = append(content, map[string]any{"type": "text", "text": strings.Join(body, "\n")})
	}
	return map[string]any{
		"type": "codeBlock",
		"attrs": map[string]any{
			"language": language,
		},
		"content": content,
	}
}

func parseMarkdownHeading(trimmed string) map[string]any {
	match := headingPattern.FindStringSubmatch(trimmed)
	level := len(match[1])
	if level > 3 {
		level = 3
	}
	return map[string]any{
		"type": "heading",
		"attrs": map[string]any{
			"level": level,
		},
		"content": parseMarkdownInline(match[2]),
	}
}

func parseMarkdownBlockquote(lines []string, index *int, ctx *markdownParseContext) map[string]any {
	var nested []string
	for *index < len(lines) {
		trimmed := strings.TrimSpace(lines[*index])
		if !strings.HasPrefix(trimmed, ">") {
			break
		}
		nested = append(nested, strings.TrimSpace(strings.TrimPrefix(trimmed, ">")))
		*index += 1
	}
	childIndex := 0
	return map[string]any{
		"type":    "blockquote",
		"content": parseMarkdownBlocks(nested, &childIndex, 0, ctx),
	}
}

func parseMarkdownStandaloneImage(trimmed string, ctx *markdownParseContext) map[string]any {
	match := standaloneImageExpr.FindStringSubmatch(trimmed)
	alt := match[1]
	target := match[2]
	assetID := ctx.nextAssetID()
	assetPath := filepath.Join(ctx.baseDir, filepath.FromSlash(target))
	ctx.assets = append(ctx.assets, importAssetRef{
		Alt:           alt,
		OriginalName:  filepath.Base(assetPath),
		Path:          assetPath,
		PlaceholderID: assetID,
	})
	return map[string]any{
		"type": "attachmentImage",
		"attrs": map[string]any{
			"attachmentId": assetID,
			"alt":          alt,
		},
	}
}

func parseMarkdownList(lines []string, index *int, baseIndent int, ctx *markdownParseContext, kind string) map[string]any {
	listType := map[string]string{
		"task":    "taskList",
		"bullet":  "bulletList",
		"ordered": "orderedList",
	}[kind]
	var items []any
	startNumber := 1
	for *index < len(lines) {
		line := lines[*index]
		if strings.TrimSpace(line) == "" {
			*index += 1
			continue
		}
		indent := leadingIndent(line)
		if indent < baseIndent {
			break
		}
		trimmed := strings.TrimSpace(line)
		if kind == "task" && !isTaskListItem(trimmed) {
			break
		}
		if kind == "bullet" && !isBulletListItem(trimmed) {
			break
		}
		if kind == "ordered" && !isOrderedListItem(trimmed) {
			break
		}
		var text string
		var checked bool
		if kind == "task" {
			match := taskItemPattern.FindStringSubmatch(trimmed)
			checked = strings.EqualFold(match[1], "x")
			text = match[2]
		} else if kind == "bullet" {
			match := bulletItemPattern.FindStringSubmatch(trimmed)
			text = match[1]
		} else {
			match := orderedItemPattern.FindStringSubmatch(trimmed)
			if len(items) == 0 {
				if parsed, err := strconv.Atoi(match[1]); err == nil && parsed > 0 {
					startNumber = parsed
				}
			}
			text = match[2]
		}
		*index += 1
		var itemContent []any
		if strings.TrimSpace(text) != "" {
			itemContent = append(itemContent, map[string]any{
				"type":    "paragraph",
				"content": parseMarkdownInline(text),
			})
		}
		childIndent := indent + 2
		childBlocks := parseMarkdownBlocks(lines, index, childIndent, ctx)
		itemContent = append(itemContent, childBlocks...)
		itemType := "listItem"
		itemAttrs := map[string]any{}
		if kind == "task" {
			itemType = "taskItem"
			itemAttrs["checked"] = checked
		}
		item := map[string]any{
			"type":    itemType,
			"content": itemContent,
		}
		if len(itemAttrs) > 0 {
			item["attrs"] = itemAttrs
		}
		items = append(items, item)
	}
	list := map[string]any{
		"type":    listType,
		"content": items,
	}
	if kind == "ordered" && startNumber > 1 {
		list["attrs"] = map[string]any{"start": startNumber}
	}
	return list
}

func parseMarkdownTable(lines []string, index *int) map[string]any {
	header := splitMarkdownTableRow(strings.TrimSpace(lines[*index]))
	*index += 2
	var rows []any
	rows = append(rows, makeMarkdownTableRow(header, true))
	for *index < len(lines) {
		trimmed := strings.TrimSpace(lines[*index])
		if trimmed == "" || !strings.Contains(trimmed, "|") {
			break
		}
		rows = append(rows, makeMarkdownTableRow(splitMarkdownTableRow(trimmed), false))
		*index += 1
	}
	return map[string]any{
		"type":    "table",
		"content": rows,
	}
}

func makeMarkdownTableRow(cells []string, header bool) map[string]any {
	cellType := "tableCell"
	if header {
		cellType = "tableHeader"
	}
	content := make([]any, 0, len(cells))
	for _, cell := range cells {
		content = append(content, map[string]any{
			"type": cellType,
			"content": []any{
				map[string]any{
					"type":    "paragraph",
					"content": parseMarkdownInline(strings.TrimSpace(cell)),
				},
			},
		})
	}
	return map[string]any{
		"type":    "tableRow",
		"content": content,
	}
}

func parseMarkdownParagraph(lines []string, index *int, baseIndent int) map[string]any {
	var parts []string
	for *index < len(lines) {
		line := lines[*index]
		if strings.TrimSpace(line) == "" {
			break
		}
		if leadingIndent(line) < baseIndent {
			break
		}
		trimmed := strings.TrimSpace(line)
		if len(parts) > 0 && (isFenceLine(trimmed) || headingPattern.MatchString(trimmed) || isHorizontalRule(trimmed) || isTableStart(lines, *index) || strings.HasPrefix(trimmed, ">") || standaloneImageExpr.MatchString(trimmed) || isTaskListItem(trimmed) || isBulletListItem(trimmed) || isOrderedListItem(trimmed)) {
			break
		}
		parts = append(parts, trimmed)
		*index += 1
	}
	return map[string]any{
		"type":    "paragraph",
		"content": parseMarkdownInline(strings.Join(parts, " ")),
	}
}

func parseMarkdownInline(source string) []any {
	source = strings.TrimSpace(source)
	if source == "" {
		return []any{}
	}
	var nodes []any
	for len(source) > 0 {
		switch {
		case codePattern.MatchString(source):
			match := codePattern.FindStringSubmatch(source)
			nodes = append(nodes, textNodeWithMarks(match[1], []map[string]any{{"type": "code"}}))
			source = source[len(match[0]):]
		case linkPattern.MatchString(source):
			match := linkPattern.FindStringSubmatch(source)
			children := parseMarkdownInline(match[1])
			nodes = append(nodes, addMarkToInlineNodes(children, map[string]any{
				"type": "link",
				"attrs": map[string]any{
					"href": normalizeLinkURL(match[2]),
				},
			})...)
			source = source[len(match[0]):]
		case boldPattern.MatchString(source):
			match := boldPattern.FindStringSubmatch(source)
			nodes = append(nodes, addMarkToInlineNodes(parseMarkdownInline(match[1]), map[string]any{"type": "bold"})...)
			source = source[len(match[0]):]
		case strikePattern.MatchString(source):
			match := strikePattern.FindStringSubmatch(source)
			nodes = append(nodes, addMarkToInlineNodes(parseMarkdownInline(match[1]), map[string]any{"type": "strike"})...)
			source = source[len(match[0]):]
		case italicStarPattern.MatchString(source):
			match := italicStarPattern.FindStringSubmatch(source)
			nodes = append(nodes, addMarkToInlineNodes(parseMarkdownInline(match[1]), map[string]any{"type": "italic"})...)
			source = source[len(match[0]):]
		case italicUnderPattern.MatchString(source):
			match := italicUnderPattern.FindStringSubmatch(source)
			nodes = append(nodes, addMarkToInlineNodes(parseMarkdownInline(match[1]), map[string]any{"type": "italic"})...)
			source = source[len(match[0]):]
		default:
			specialIndex := nextMarkdownSpecialIndex(source)
			if specialIndex < 0 {
				nodes = append(nodes, map[string]any{"type": "text", "text": source})
				source = ""
				continue
			}
			if specialIndex > 0 {
				nodes = append(nodes, map[string]any{"type": "text", "text": source[:specialIndex]})
				source = source[specialIndex:]
				continue
			}
			nodes = append(nodes, map[string]any{"type": "text", "text": source[:1]})
			source = source[1:]
		}
	}
	return mergeAdjacentTextNodes(nodes)
}

func textNodeWithMarks(text string, marks []map[string]any) map[string]any {
	node := map[string]any{
		"type": "text",
		"text": text,
	}
	if len(marks) > 0 {
		typed := make([]any, 0, len(marks))
		for _, mark := range marks {
			typed = append(typed, mark)
		}
		node["marks"] = typed
	}
	return node
}

func addMarkToInlineNodes(nodes []any, mark map[string]any) []any {
	updated := make([]any, 0, len(nodes))
	for _, nodeValue := range nodes {
		node, ok := nodeValue.(map[string]any)
		if !ok {
			continue
		}
		if node["type"] != "text" {
			updated = append(updated, node)
			continue
		}
		var marks []any
		if existing, ok := node["marks"].([]any); ok {
			marks = append(marks, existing...)
		}
		marks = append(marks, mark)
		cloned := cloneMap(node)
		cloned["marks"] = marks
		updated = append(updated, cloned)
	}
	return updated
}

func mergeAdjacentTextNodes(nodes []any) []any {
	var merged []any
	for _, nodeValue := range nodes {
		node, ok := nodeValue.(map[string]any)
		if !ok {
			continue
		}
		if len(merged) == 0 {
			merged = append(merged, node)
			continue
		}
		last, ok := merged[len(merged)-1].(map[string]any)
		if !ok || last["type"] != "text" || node["type"] != "text" || !marksEqualJSON(last["marks"], node["marks"]) {
			merged = append(merged, node)
			continue
		}
		lastText, _ := last["text"].(string)
		nodeText, _ := node["text"].(string)
		last["text"] = lastText + nodeText
		merged[len(merged)-1] = last
	}
	return merged
}

func marksEqualJSON(a any, b any) bool {
	aJSON, _ := json.Marshal(a)
	bJSON, _ := json.Marshal(b)
	return string(aJSON) == string(bJSON)
}

func nextMarkdownSpecialIndex(source string) int {
	indexes := []int{
		strings.Index(source, "`"),
		strings.Index(source, "["),
		strings.Index(source, "*"),
		strings.Index(source, "_"),
		strings.Index(source, "~"),
	}
	best := -1
	for _, index := range indexes {
		if index < 0 {
			continue
		}
		if best < 0 || index < best {
			best = index
		}
	}
	return best
}

func isFenceLine(trimmed string) bool {
	return trimmed == "```" || strings.HasPrefix(trimmed, "```") || trimmed == "~~~" || strings.HasPrefix(trimmed, "~~~")
}

func isHorizontalRule(trimmed string) bool {
	return trimmed == "---" || trimmed == "***" || trimmed == "___"
}

func isTaskListItem(trimmed string) bool {
	return taskItemPattern.MatchString(trimmed)
}

func isBulletListItem(trimmed string) bool {
	return bulletItemPattern.MatchString(trimmed) && !taskItemPattern.MatchString(trimmed)
}

func isOrderedListItem(trimmed string) bool {
	return orderedItemPattern.MatchString(trimmed)
}

func isTableStart(lines []string, index int) bool {
	if index+1 >= len(lines) {
		return false
	}
	current := strings.TrimSpace(lines[index])
	next := strings.TrimSpace(lines[index+1])
	if !strings.Contains(current, "|") {
		return false
	}
	if next == "" {
		return false
	}
	parts := splitMarkdownTableRow(next)
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		part = strings.TrimSpace(strings.Trim(part, ":"))
		if part == "" {
			return false
		}
		for _, r := range part {
			if r != '-' {
				return false
			}
		}
	}
	return true
}

func splitMarkdownTableRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func leadingIndent(line string) int {
	count := 0
	for _, r := range line {
		if r != ' ' {
			break
		}
		count += 1
	}
	return count
}

func contentSlice(value any) []any {
	if typed, ok := value.([]any); ok {
		return typed
	}
	return nil
}

func stringAttr(node map[string]any, key string) string {
	attrs, _ := node["attrs"].(map[string]any)
	value, _ := attrs[key].(string)
	return value
}

func intAttr(node map[string]any, key string, fallback int) int {
	attrs, _ := node["attrs"].(map[string]any)
	switch typed := attrs[key].(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	}
	return fallback
}

func boolAttr(node map[string]any, key string) bool {
	attrs, _ := node["attrs"].(map[string]any)
	value, _ := attrs[key].(bool)
	return value
}

func escapeMarkdownText(text string) string {
	var builder strings.Builder
	for _, r := range text {
		switch r {
		case '\\', '`', '*', '_', '{', '}', '[', ']', '(', ')', '#', '+', '-', '!', '|', '~':
			builder.WriteByte('\\')
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func plainText(value any) string {
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
	return strings.Join(parts, "")
}

func findTreeItemByID(items []TreeItem, id string) *TreeItem {
	for index := range items {
		if items[index].ID == id {
			return &items[index]
		}
		if child := findTreeItemByID(items[index].Children, id); child != nil {
			return child
		}
	}
	return nil
}

func sanitizeFSName(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	var builder strings.Builder
	for _, r := range value {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			builder.WriteRune('-')
			continue
		}
		if unicode.IsSpace(r) {
			builder.WriteRune(' ')
			continue
		}
		builder.WriteRune(r)
	}
	cleaned := strings.Trim(builder.String(), " .")
	for strings.Contains(cleaned, "  ") {
		cleaned = strings.ReplaceAll(cleaned, "  ", " ")
	}
	if cleaned == "" {
		cleaned = fallback
	}
	if windowsReservedNames[strings.ToUpper(cleaned)] {
		cleaned = cleaned + "-"
	}
	return cleaned
}

func uniqueChildName(parentDir string, base string, ext string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "Untitled"
	}
	candidate := base + ext
	if _, err := os.Stat(filepath.Join(parentDir, candidate)); os.IsNotExist(err) {
		return candidate
	}
	for index := 2; ; index += 1 {
		next := fmt.Sprintf("%s (%d)%s", base, index, ext)
		if _, err := os.Stat(filepath.Join(parentDir, next)); os.IsNotExist(err) {
			return next
		}
	}
}

func trimExtension(name string) string {
	ext := filepath.Ext(name)
	if ext == "" {
		return name
	}
	return strings.TrimSuffix(name, ext)
}

func extensionOrDefault(name string, fallback string) string {
	ext := filepath.Ext(name)
	if ext != "" {
		return ext
	}
	return fallback
}

func mimeExtension(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".bin"
	}
}

func importAssetPath(sourceDir string, file string) string {
	path := filepath.FromSlash(strings.TrimSpace(file))
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(sourceDir, path)
}

func normalizeLinkURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		return value
	}
	if strings.HasPrefix(value, "mailto:") {
		return value
	}
	return "https://" + value
}
