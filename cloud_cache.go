package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const cloudCacheDirectoryName = "cloud-cache"
const cloudCacheDatabaseName = "journal.db"

// CloudCacheManager owns only local cache paths. It deliberately has no
// provider dependency: Phase 1 can create/open/validate a cache offline.
type CloudCacheManager struct {
	root   string
	router *JournalStoreRouter
}

func NewCloudCacheManager(configDir string, router *JournalStoreRouter) (*CloudCacheManager, error) {
	if strings.TrimSpace(configDir) == "" {
		return nil, fmt.Errorf("cloud cache config directory is required")
	}
	if router == nil {
		return nil, fmt.Errorf("store router is required")
	}
	return &CloudCacheManager{root: filepath.Join(configDir, "Journal", cloudCacheDirectoryName), router: router}, nil
}

func (m *CloudCacheManager) CacheDirectory(cloudJournalID string) (string, error) {
	if err := validateCloudJournalID(cloudJournalID); err != nil {
		return "", err
	}
	return filepath.Join(m.root, cloudJournalID), nil
}

// CloudJournalDisplayName returns the intentionally public title stored on a
// cloud Journal root. Journal names remain readable even when its document
// content is protected by portable encryption.
func (s *JournalService) CloudJournalDisplayName(cloudJournalID string) (string, error) {
	if err := validateCloudJournalID(cloudJournalID); err != nil {
		return "", err
	}
	item, err := s.getRawRowItemFrom(s.db, cloudJournalID)
	if err != nil {
		return "", err
	}
	if item.Kind != KindJournal || item.ParentID.Valid {
		return "", fmt.Errorf("cloud Journal root is missing")
	}
	title := strings.TrimSpace(item.Title)
	if title == "" || len(title) > 512 {
		return "", fmt.Errorf("invalid cloud Journal title")
	}
	return title, nil
}

func (m *CloudCacheManager) CachePath(cloudJournalID string) (string, error) {
	directory, err := m.CacheDirectory(cloudJournalID)
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, cloudCacheDatabaseName), nil
}

// CreateCloudCache initializes a validated cache in a sibling staging
// directory, then installs it atomically. It never overwrites an existing
// cache, including one with unsynced work from a later phase.
func (m *CloudCacheManager) CreateCloudCache(cloudJournalID string) (*JournalService, error) {
	targetDir, err := m.CacheDirectory(cloudJournalID)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(targetDir); err == nil {
		return nil, fmt.Errorf("cloud cache already exists: %s", cloudJournalID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(m.root, 0o700); err != nil {
		return nil, err
	}
	stageDir := targetDir + ".staging-" + uuid.NewString()
	if err := os.Mkdir(stageDir, 0o700); err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(stageDir) }()

	stagePath := filepath.Join(stageDir, cloudCacheDatabaseName)
	store, err := openSQLiteJournalStore(stagePath, CloudStoreID(cloudJournalID), StoreKindCloud)
	if err != nil {
		return nil, err
	}
	service := newJournalService(store)
	if err := service.migrateCloudCacheSchema(cloudJournalID); err != nil {
		_ = service.Close()
		return nil, err
	}
	if err := service.ValidateCloudJournalScope(cloudJournalID); err != nil {
		_ = service.Close()
		return nil, err
	}
	if err := service.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(stageDir, targetDir); err != nil {
		return nil, err
	}
	return m.OpenCloudCache(cloudJournalID)
}

func (m *CloudCacheManager) OpenCloudCache(cloudJournalID string) (*JournalService, error) {
	path, err := m.CachePath(cloudJournalID)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("cache_missing: %s", cloudJournalID)
		}
		return nil, err
	}
	store, err := openSQLiteJournalStore(path, CloudStoreID(cloudJournalID), StoreKindCloud)
	if err != nil {
		return nil, err
	}
	service := newJournalService(store)
	if err := service.ValidateCloudJournalScope(cloudJournalID); err != nil {
		_ = service.Close()
		return nil, err
	}
	if err := m.router.Register(store); err != nil {
		_ = service.Close()
		return nil, err
	}
	return service, nil
}

// RemoveCloudCache is deliberately state-gated. Callers must supply the mount
// state from the installation database; the cache manager never guesses that
// it is safe to remove a pending or dirty cache.
func (m *CloudCacheManager) RemoveCloudCache(cloudJournalID, syncStatus string, pendingCreate bool) error {
	if pendingCreate || strings.TrimSpace(syncStatus) != "clean" {
		return fmt.Errorf("refusing to remove dirty or pending cloud cache")
	}
	directory, err := m.CacheDirectory(cloudJournalID)
	if err != nil {
		return err
	}
	if err := m.router.Unregister(CloudStoreID(cloudJournalID)); err != nil && !strings.HasPrefix(err.Error(), "store_not_found") {
		return err
	}
	return os.RemoveAll(directory)
}

// StageCacheReplacement validates a fully downloaded staging cache before it
// replaces the active cache. If the final rename fails, it restores the prior
// cache directory instead of leaving an empty mount.
func (m *CloudCacheManager) StageCacheReplacement(cloudJournalID, stagedDirectory string) error {
	targetDir, err := m.CacheDirectory(cloudJournalID)
	if err != nil {
		return err
	}
	stagedDirectory = strings.TrimSpace(stagedDirectory)
	if stagedDirectory == "" {
		return fmt.Errorf("staged cache directory is required")
	}
	if m.router.IsRegistered(CloudStoreID(cloudJournalID)) {
		return fmt.Errorf("cloud cache must be inactive before replacement")
	}
	stagedPath := filepath.Join(stagedDirectory, cloudCacheDatabaseName)
	if _, err := os.Stat(stagedPath); err != nil {
		return fmt.Errorf("staged cache missing database: %w", err)
	}
	store, err := openSQLiteJournalStore(stagedPath, CloudStoreID(cloudJournalID), StoreKindCloud)
	if err != nil {
		return err
	}
	service := newJournalService(store)
	if err := service.ValidateCloudJournalScope(cloudJournalID); err != nil {
		_ = service.Close()
		return err
	}
	if err := service.Close(); err != nil {
		return err
	}

	backupDir := targetDir + ".previous-" + uuid.NewString()
	if err := os.Rename(targetDir, backupDir); err != nil {
		return err
	}
	if err := os.Rename(stagedDirectory, targetDir); err != nil {
		_ = os.Rename(backupDir, targetDir)
		return err
	}
	_ = os.RemoveAll(backupDir)
	return nil
}

func (s *JournalService) migrateCloudCacheSchema(cloudJournalID string) error {
	if err := validateCloudJournalID(cloudJournalID); err != nil {
		return err
	}
	if s.StoreKind() != StoreKindCloud {
		return fmt.Errorf("cloud cache migration requires cloud store")
	}
	if _, err := s.db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return err
	}
	if err := applyContentSchema(s.db); err != nil {
		return err
	}
	if err := s.ensureEncryptionColumns(); err != nil {
		return err
	}
	if err := s.ensureAttachmentColumns(); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS cloud_journal_metadata (
		cloud_journal_id TEXT PRIMARY KEY,
		content_format_version INTEGER NOT NULL,
		created_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO cloud_journal_metadata (cloud_journal_id, content_format_version, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT(cloud_journal_id) DO NOTHING`, cloudJournalID, contentSchemaVersion, nowString()); err != nil {
		return err
	}
	if err := s.ensureCloudTrash(); err != nil {
		return err
	}
	return s.ensureCloudJournalRoot(cloudJournalID)
}

func (s *JournalService) ensureCloudTrash() error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM items WHERE system_key = ?`, SystemTrash).Scan(&count); err != nil {
		return err
	}
	if count > 1 {
		return fmt.Errorf("cloud_scope_invalid: multiple system Trash rows")
	}
	if count == 1 {
		return nil
	}
	now := nowString()
	id := uuid.NewString()
	if _, err := s.db.Exec(`INSERT INTO items (id, parent_id, kind, title, sort_order, system_key, created_at, updated_at)
		VALUES (?, NULL, ?, 'Trash', 999999, ?, ?, ?)`, id, KindFolder, SystemTrash, now, now); err != nil {
		return err
	}
	return s.syncFTS(s.db, id)
}

func (s *JournalService) ensureCloudJournalRoot(cloudJournalID string) error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM items WHERE id = ? AND kind = ? AND parent_id IS NULL`, cloudJournalID, KindJournal).Scan(&count); err != nil {
		return err
	}
	if count == 1 {
		return nil
	}
	if count > 1 {
		return fmt.Errorf("cloud_scope_invalid: duplicate cloud Journal root")
	}
	now := nowString()
	if _, err := s.db.Exec(`INSERT INTO items (id, parent_id, kind, title, sort_order, created_at, updated_at)
		VALUES (?, NULL, ?, 'Cloud Journal', 0, ?, ?)`, cloudJournalID, KindJournal, now, now); err != nil {
		return err
	}
	return s.syncFTS(s.db, cloudJournalID)
}

// ValidateCloudJournalScope rejects a cache before it becomes routable. It
// verifies role separation, one Journal root, one system Trash, and that every
// non-Trash item belongs to the Journal-root subtree.
func (s *JournalService) ValidateCloudJournalScope(cloudJournalID string) error {
	if err := validateCloudJournalID(cloudJournalID); err != nil {
		return err
	}
	if s.StoreKind() != StoreKindCloud {
		return fmt.Errorf("cloud_scope_invalid: non-cloud store")
	}
	for _, table := range []string{"app_settings", "vault_providers", "cloud_journal_mounts", "cloud_pending_creates", "installation_device_identity"} {
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("cloud_scope_invalid: installation table %s in cache", table)
		}
	}
	var metadataID string
	var metadataCount int
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(MIN(cloud_journal_id), '') FROM cloud_journal_metadata`).Scan(&metadataCount, &metadataID); err != nil {
		return fmt.Errorf("cloud_scope_invalid: cloud metadata: %w", err)
	}
	if metadataCount != 1 || metadataID != cloudJournalID {
		return fmt.Errorf("cloud_scope_invalid: metadata Journal ID mismatch")
	}
	var journalCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM items WHERE kind = ? AND parent_id IS NULL AND COALESCE(system_key, '') != ?`, KindJournal, SystemTrash).Scan(&journalCount); err != nil {
		return err
	}
	if journalCount != 1 {
		return fmt.Errorf("cloud_scope_invalid: expected one Journal root")
	}
	var rootID string
	if err := s.db.QueryRow(`SELECT id FROM items WHERE kind = ? AND parent_id IS NULL AND COALESCE(system_key, '') != ?`, KindJournal, SystemTrash).Scan(&rootID); err != nil {
		return err
	}
	if rootID != cloudJournalID {
		return fmt.Errorf("cloud_scope_invalid: Journal root mismatch")
	}
	var trashCount, invalidCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM items WHERE system_key = ? AND kind = ? AND parent_id IS NULL`, SystemTrash, KindFolder).Scan(&trashCount); err != nil {
		return err
	}
	if trashCount != 1 {
		return fmt.Errorf("cloud_scope_invalid: expected one system Trash")
	}
	query := `WITH RECURSIVE journal_items(id) AS (
		SELECT id FROM items WHERE id = ?
		UNION ALL
		SELECT child.id FROM items child JOIN journal_items parent ON child.parent_id = parent.id
	)
	SELECT COUNT(*) FROM items
	WHERE COALESCE(system_key, '') != ? AND id NOT IN (SELECT id FROM journal_items)`
	if err := s.db.QueryRow(query, cloudJournalID, SystemTrash).Scan(&invalidCount); err != nil {
		return err
	}
	if invalidCount != 0 {
		return fmt.Errorf("cloud_scope_invalid: item outside cloud Journal root")
	}
	return nil
}

func (s *JournalService) ensureCloudStoreScope() error {
	if s.StoreKind() != StoreKindCloud {
		return nil
	}
	var cloudJournalID string
	if err := s.db.QueryRow(`SELECT cloud_journal_id FROM cloud_journal_metadata`).Scan(&cloudJournalID); err != nil {
		return fmt.Errorf("cloud_scope_invalid: cloud metadata: %w", err)
	}
	return s.ValidateCloudJournalScope(cloudJournalID)
}

func (s *JournalService) validateCloudItemAccess(db queryRower, itemID string) error {
	if s.StoreKind() != StoreKindCloud {
		return nil
	}
	var cloudJournalID string
	if err := db.QueryRow(`SELECT cloud_journal_id FROM cloud_journal_metadata`).Scan(&cloudJournalID); err != nil {
		return fmt.Errorf("cloud_scope_invalid: cloud metadata: %w", err)
	}
	var allowed int
	query := `WITH RECURSIVE journal_items(id) AS (
		SELECT id FROM items WHERE id = ?
		UNION ALL
		SELECT child.id FROM items child JOIN journal_items parent ON child.parent_id = parent.id
	)
	SELECT EXISTS(
		SELECT 1 FROM journal_items WHERE id = ?
		UNION ALL
		SELECT 1 FROM items WHERE id = ? AND system_key = ?
	)`
	if err := db.QueryRow(query, cloudJournalID, itemID, itemID, SystemTrash).Scan(&allowed); err != nil {
		return err
	}
	if allowed == 0 {
		return fmt.Errorf("cloud_scope_invalid: item outside cloud Journal root")
	}
	return nil
}

func validateCloudJournalID(cloudJournalID string) error {
	parsed, err := uuid.Parse(strings.TrimSpace(cloudJournalID))
	if err != nil || parsed == uuid.Nil {
		return fmt.Errorf("invalid cloud Journal ID")
	}
	return nil
}
