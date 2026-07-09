package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Shared service helpers and minimal database capability interfaces.

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
