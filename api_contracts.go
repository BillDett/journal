package main

// This file is the stable contract between the Wails adapter and the
// application commands. Keep transport-friendly request and response shapes
// here so frontend and backend changes have a small, obvious review surface.

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

// TrashItemCommand makes the destructive state transition explicit. The
// expected state prevents a stale or repeated client request from escalating a
// reversible move into permanent deletion.
type TrashItemCommand struct {
	ID              string `json:"id"`
	ExpectedInTrash bool   `json:"expectedInTrash"`
}
