package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const vaultLeaseDuration = 5 * time.Minute
const vaultForceUnlockGrace = 30 * time.Second

type VaultSyncService struct {
	Store    VaultStore
	Provider VaultProvider
	Caches   *CloudCacheManager
	Mounts   *InstallationRepository
	Device   DeviceIdentity
	Now      func() time.Time
}

func (s *VaultSyncService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s *VaultSyncService) Validate(ctx context.Context) (VaultCapabilities, error) {
	return s.Store.Validate(ctx, s.Provider)
}

func (s *VaultSyncService) CreateCloudJournal(ctx context.Context) (*JournalService, string, error) {
	if _, err := s.Validate(ctx); err != nil {
		return nil, "", err
	}
	id := uuid.NewString()
	path, err := s.Caches.CachePath(id)
	if err != nil {
		return nil, "", err
	}
	if err := s.Mounts.UpsertPendingCreate(CloudPendingCreateRecord{CloudJournalID: id, ProviderID: s.Provider.ID, CachePath: path, Stage: "cache"}); err != nil {
		return nil, "", err
	}
	cache, err := s.Caches.CreateCloudCache(id)
	if err != nil {
		return nil, "", err
	}
	if _, err := s.acquireLease(ctx, id, ""); err != nil {
		return cache, id, err
	}
	if err := s.publish(ctx, cache, id, ""); err != nil {
		return cache, id, err
	}
	return cache, id, nil
}

func (s *VaultSyncService) Publish(ctx context.Context, cache *JournalService, cloudJournalID string) error {
	mount, err := s.mount(cloudJournalID)
	if err != nil {
		return err
	}
	return s.publish(ctx, cache, cloudJournalID, mount.LastCurrentToken)
}

func (s *VaultSyncService) publish(ctx context.Context, cache *JournalService, id, expectedToken string) error {
	if cache == nil || cache.StoreKind() != StoreKindCloud {
		return fmt.Errorf("cache_missing")
	}
	lease, err := s.acquireLease(ctx, id, expectedToken)
	if err != nil {
		return err
	}
	current, token, err := s.readCurrent(ctx, id)
	if err != nil && !isVault(err, VaultNotFound) {
		return err
	}
	if err == nil && expectedToken != "" && token != expectedToken {
		return fmt.Errorf("current_pointer_conflict")
	}
	if err := cache.FlushAll(); err != nil {
		return err
	}
	attachments, err := cache.AttachmentDescriptors()
	if err != nil {
		return err
	}
	for _, attachment := range attachments {
		if err := cache.ensureBlobPresent(attachment.Digest, attachment.Size); err != nil {
			return err
		}
		path, err := cache.blobCachePath(attachment.Digest)
		if err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, putErr := s.Store.PutImmutable(ctx, s.Provider, attachment.Key, file, attachment.Digest)
		_ = file.Close()
		if putErr != nil {
			return putErr
		}
	}
	publishedAt := s.now()
	revisionID, err := newVaultRevisionID(publishedAt)
	if err != nil {
		return err
	}
	dbKey, err := vaultDatabaseKey(id, revisionID)
	if err != nil {
		return err
	}
	stageDir := filepath.Join(filepath.Dir(cache.repository.path), "snapshot-staging")
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return err
	}
	stage := newSnapshotPath(stageDir)
	db, err := cache.SnapshotContentDatabase(ctx, stage)
	if err != nil {
		return err
	}
	defer os.Remove(stage)
	db.Key = dbKey
	file, err := os.Open(stage)
	if err != nil {
		return err
	}
	_, err = s.Store.PutImmutable(ctx, s.Provider, dbKey, file, db.Digest)
	_ = file.Close()
	if err != nil {
		return err
	}
	manifestKey, err := vaultManifestKey(id, revisionID)
	if err != nil {
		return err
	}
	revisionNumber := int64(1)
	parent := ""
	if current.RevisionNumber > 0 {
		revisionNumber = current.RevisionNumber + 1
		parent = current.RevisionID
	}
	manifest := VaultRevisionManifest{Format: vaultRevisionFormat, FormatVersion: vaultFormatVersion, CloudJournalID: id, RevisionID: revisionID, RevisionNumber: revisionNumber, ParentRevisionID: parent, CreatedAt: publishedAt, CreatedByDeviceID: s.Device.ID, Database: VaultObjectDescriptor{Key: db.Key, SHA256: db.Digest, Size: db.Size}, Attachments: attachments}
	manifest.Encryption.Enabled = cacheHasPortableEncryption(cache)
	if manifest.Encryption.Enabled {
		manifest.Encryption.MetadataVersion = cloudPortableEncryptionFormatVersion
	}
	bytes, err := canonicalVaultJSON(manifest)
	if err != nil {
		return err
	}
	manifestDescriptor := bytesDescriptor(manifestKey, bytes)
	if _, err := s.Store.PutImmutable(ctx, s.Provider, manifestKey, bytesReader(bytes), manifestDescriptor.SHA256); err != nil {
		return err
	}
	displayName, err := cache.CloudJournalDisplayName(id)
	if err != nil {
		return err
	}
	if err := s.UpdateJournalMetadata(ctx, id, displayName); err != nil {
		return err
	}
	next := VaultCurrent{Format: vaultCurrentFormat, FormatVersion: vaultFormatVersion, CloudJournalID: id, RevisionID: revisionID, RevisionNumber: revisionNumber, RevisionManifest: manifestDescriptor, UpdatedAt: publishedAt, PreviousRevisionID: parent}
	next.PortableEncryption.Enabled = manifest.Encryption.Enabled
	next.PortableEncryption.MetadataVersion = manifest.Encryption.MetadataVersion
	control, err := canonicalVaultJSON(next)
	if err != nil {
		return err
	}
	if token == "" {
		_, err = s.Store.CreateControlIfAbsent(ctx, s.Provider, mustVaultCurrent(id), control)
	} else {
		_, err = s.Store.PutControlIf(ctx, s.Provider, mustVaultCurrent(id), control, token)
	}
	if err != nil {
		return fmt.Errorf("current_pointer_conflict: %w", err)
	}
	confirmed, confirmedToken, err := s.readCurrent(ctx, id)
	if err != nil || confirmed.RevisionID != revisionID {
		return fmt.Errorf("digest_mismatch")
	}
	if err := s.upsertMount(CloudJournalMountRecord{CloudJournalID: id, ProviderID: s.Provider.ID, VaultRoot: s.Provider.Root, CachePath: cache.repository.path, LastRevisionID: revisionID, LastCurrentToken: confirmedToken, LeaseID: lease.LeaseID, SyncStatus: "clean", LastSyncedAt: publishedAt.Format(time.RFC3339Nano)}); err != nil {
		return err
	}
	_, _ = cache.db.Exec(`DELETE FROM cloud_pending_creates WHERE cloud_journal_id = ?`, id)
	return nil
}

func (s *VaultSyncService) UpdateJournalMetadata(ctx context.Context, cloudJournalID, displayName string) error {
	if err := validateCloudJournalID(cloudJournalID); err != nil {
		return err
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || len(displayName) > 512 {
		return fmt.Errorf("invalid journal display name")
	}
	metadata := VaultJournalMetadata{Format: vaultMetadataFormat, FormatVersion: vaultFormatVersion, CloudJournalID: cloudJournalID, DisplayName: displayName, UpdatedAt: s.now()}
	encoded, err := canonicalVaultJSON(metadata)
	if err != nil {
		return err
	}
	key, err := vaultJournalMetadataKey(cloudJournalID)
	if err != nil {
		return err
	}
	current, token, err := s.Store.GetControl(ctx, s.Provider, key)
	if isVault(err, VaultNotFound) {
		_, err = s.Store.CreateControlIfAbsent(ctx, s.Provider, key, encoded)
		return err
	}
	if err != nil {
		return err
	}
	if _, err := parseVaultJournalMetadata(current); err != nil {
		return err
	}
	_, err = s.Store.PutControlIf(ctx, s.Provider, key, encoded, token)
	return err
}

func (s *VaultSyncService) ReconnectCloudJournal(ctx context.Context, id string) (*JournalService, error) {
	if _, err := s.Validate(ctx); err != nil {
		return nil, err
	}
	current, token, err := s.readCurrent(ctx, id)
	if err != nil {
		return nil, err
	}
	manifestBytes, err := readVaultObject(ctx, s.Store, s.Provider, current.RevisionManifest.Key, current.RevisionManifest)
	if err != nil {
		return nil, err
	}
	manifest, err := parseVaultManifest(manifestBytes)
	if err != nil {
		return nil, err
	}
	db := DatabaseDescriptor{Key: manifest.Database.Key, Digest: manifest.Database.SHA256, Size: manifest.Database.Size}
	stageDir, err := s.Caches.CacheDirectory(id)
	if err != nil {
		return nil, err
	}
	stageDir = stageDir + ".recovery-" + uuid.NewString()
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return nil, err
	}
	defer os.RemoveAll(stageDir)
	data, err := readVaultObject(ctx, s.Store, s.Provider, db.Key, VaultObjectDescriptor{Key: db.Key, SHA256: db.Digest, Size: db.Size})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(stageDir, cloudCacheDatabaseName), data, 0o600); err != nil {
		return nil, err
	}
	if _, err := s.Caches.CachePath(id); err != nil {
		return nil, err
	}
	if _, err := s.Caches.OpenCloudCache(id); err != nil {
		if _, createErr := s.Caches.CreateCloudCache(id); createErr != nil {
			return nil, err
		}
	}
	_ = s.Caches.router.Unregister(CloudStoreID(id))
	if err := s.Caches.StageCacheReplacement(id, stageDir); err != nil {
		return nil, err
	}
	cache, err := s.Caches.OpenCloudCache(id)
	if err != nil {
		return nil, err
	}
	lease, leaseErr := s.acquireLease(ctx, id, token)
	status := "clean"
	leaseID := ""
	if leaseErr != nil {
		status = "locked_read_only"
	} else {
		leaseID = lease.LeaseID
	}
	if err := s.upsertMount(CloudJournalMountRecord{CloudJournalID: id, ProviderID: s.Provider.ID, VaultRoot: s.Provider.Root, CachePath: cache.repository.path, LastRevisionID: current.RevisionID, LastCurrentToken: token, LeaseID: leaseID, SyncStatus: status, LastSyncedAt: s.now().Format(time.RFC3339Nano)}); err != nil {
		return nil, err
	}
	return cache, nil
}

func (s *VaultSyncService) acquireLease(ctx context.Context, id, expectedCurrent string) (VaultLease, error) {
	key := mustVaultLease(id)
	now := s.now()
	currentToken := expectedCurrent
	if currentToken == "" {
		_, token, err := s.readCurrent(ctx, id)
		if err == nil {
			currentToken = token
		} else if !isVault(err, VaultNotFound) {
			return VaultLease{}, err
		}
	}
	lease := VaultLease{Format: vaultLeaseFormat, FormatVersion: vaultFormatVersion, CloudJournalID: id, LeaseID: uuid.NewString(), OwnerDeviceID: s.Device.ID, OwnerLabel: s.Device.Label, AcquiredAt: now, ExpiresAt: now.Add(vaultLeaseDuration), CurrentPointerToken: currentToken}
	data, _ := canonicalVaultJSON(lease)
	_, err := s.Store.CreateControlIfAbsent(ctx, s.Provider, key, data)
	if err == nil {
		return lease, nil
	}
	if !isVault(err, VaultAlreadyExists) {
		return VaultLease{}, err
	}
	oldData, token, err := s.Store.GetControl(ctx, s.Provider, key)
	if err != nil {
		return VaultLease{}, err
	}
	old, err := parseVaultLease(oldData)
	if err != nil {
		return VaultLease{}, err
	}
	if old.ExpiresAt.After(now) && old.OwnerDeviceID != s.Device.ID {
		return VaultLease{}, fmt.Errorf("lease_held")
	}
	_, err = s.Store.PutControlIf(ctx, s.Provider, key, data, token)
	if err != nil {
		return VaultLease{}, fmt.Errorf("lease_lost")
	}
	return lease, nil
}

func (s *VaultSyncService) RenewLease(ctx context.Context, cloudJournalID, leaseID string) (VaultLease, error) {
	data, token, err := s.Store.GetControl(ctx, s.Provider, mustVaultLease(cloudJournalID))
	if err != nil {
		return VaultLease{}, err
	}
	lease, err := parseVaultLease(data)
	if err != nil {
		return VaultLease{}, err
	}
	if lease.LeaseID != leaseID || lease.OwnerDeviceID != s.Device.ID || !lease.ExpiresAt.After(s.now()) {
		return VaultLease{}, fmt.Errorf("lease_lost")
	}
	lease.ExpiresAt = s.now().Add(vaultLeaseDuration)
	updated, err := canonicalVaultJSON(lease)
	if err != nil {
		return VaultLease{}, err
	}
	if _, err := s.Store.PutControlIf(ctx, s.Provider, mustVaultLease(cloudJournalID), updated, token); err != nil {
		return VaultLease{}, fmt.Errorf("lease_lost: %w", err)
	}
	return lease, nil
}

func (s *VaultSyncService) ReleaseLease(ctx context.Context, cloudJournalID, leaseID string) error {
	data, token, err := s.Store.GetControl(ctx, s.Provider, mustVaultLease(cloudJournalID))
	if err != nil {
		if isVault(err, VaultNotFound) {
			return nil
		}
		return err
	}
	lease, err := parseVaultLease(data)
	if err != nil {
		return err
	}
	if lease.LeaseID != leaseID || lease.OwnerDeviceID != s.Device.ID {
		return fmt.Errorf("lease_lost")
	}
	lease.ExpiresAt = s.now().Add(-time.Second)
	updated, _ := canonicalVaultJSON(lease)
	_, err = s.Store.PutControlIf(ctx, s.Provider, mustVaultLease(cloudJournalID), updated, token)
	return err
}

func (s *VaultSyncService) ForceUnlock(ctx context.Context, cloudJournalID string) (VaultLease, error) {
	data, token, err := s.Store.GetControl(ctx, s.Provider, mustVaultLease(cloudJournalID))
	if err != nil {
		return VaultLease{}, err
	}
	old, err := parseVaultLease(data)
	if err != nil {
		return VaultLease{}, err
	}
	if old.ExpiresAt.Add(vaultForceUnlockGrace).After(s.now()) {
		return VaultLease{}, fmt.Errorf("lease_held")
	}
	current, currentToken, err := s.readCurrent(ctx, cloudJournalID)
	if err != nil && !isVault(err, VaultNotFound) {
		return VaultLease{}, err
	}
	lease := VaultLease{Format: vaultLeaseFormat, FormatVersion: vaultFormatVersion, CloudJournalID: cloudJournalID, LeaseID: uuid.NewString(), OwnerDeviceID: s.Device.ID, OwnerLabel: s.Device.Label, AcquiredAt: s.now(), ExpiresAt: s.now().Add(vaultLeaseDuration), CurrentPointerToken: currentToken}
	if current.RevisionID == "" {
		lease.CurrentPointerToken = ""
	}
	updated, _ := canonicalVaultJSON(lease)
	if _, err := s.Store.PutControlIf(ctx, s.Provider, mustVaultLease(cloudJournalID), updated, token); err != nil {
		return VaultLease{}, fmt.Errorf("lease_lost: %w", err)
	}
	return lease, nil
}
func (s *VaultSyncService) readCurrent(ctx context.Context, id string) (VaultCurrent, string, error) {
	data, token, err := s.Store.GetControl(ctx, s.Provider, mustVaultCurrent(id))
	if err != nil {
		return VaultCurrent{}, "", err
	}
	current, err := parseVaultCurrent(data)
	return current, token, err
}
func (s *VaultSyncService) mount(id string) (CloudJournalMountRecord, error) {
	var m CloudJournalMountRecord
	err := s.Mounts.db.QueryRow(`SELECT cloud_journal_id, provider_id, vault_root, cache_path, last_revision_id, last_current_token, lease_id, revision_retention_count, sync_status, last_sync_error, last_synced_at, created_at, updated_at FROM cloud_journal_mounts WHERE cloud_journal_id = ?`, id).Scan(&m.CloudJournalID, &m.ProviderID, &m.VaultRoot, &m.CachePath, &m.LastRevisionID, &m.LastCurrentToken, &m.LeaseID, &m.RevisionRetentionCount, &m.SyncStatus, &m.LastSyncError, &m.LastSyncedAt, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}
func (s *VaultSyncService) upsertMount(m CloudJournalMountRecord) error {
	now := nowString()
	_, err := s.Mounts.db.Exec(`INSERT INTO cloud_journal_mounts (cloud_journal_id,provider_id,vault_root,cache_path,last_revision_id,last_current_token,lease_id,revision_retention_count,sync_status,last_sync_error,last_synced_at,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(cloud_journal_id) DO UPDATE SET provider_id=excluded.provider_id,vault_root=excluded.vault_root,cache_path=excluded.cache_path,last_revision_id=excluded.last_revision_id,last_current_token=excluded.last_current_token,lease_id=excluded.lease_id,sync_status=excluded.sync_status,last_synced_at=excluded.last_synced_at,updated_at=excluded.updated_at`, m.CloudJournalID, m.ProviderID, m.VaultRoot, m.CachePath, m.LastRevisionID, m.LastCurrentToken, m.LeaseID, m.RevisionRetentionCount, m.SyncStatus, m.LastSyncError, m.LastSyncedAt, now, now)
	return err
}
func mustVaultCurrent(id string) string { k, _ := vaultCurrentKey(id); return k }
func mustVaultLease(id string) string   { k, _ := vaultLeaseKey(id); return k }
func mustVaultJournalMetadata(id string) string {
	k, _ := vaultJournalMetadataKey(id)
	return k
}
func isVault(err error, kind VaultErrorKind) bool {
	var target *VaultError
	return errors.As(err, &target) && target.Kind == kind
}
func cacheHasPortableEncryption(cache *JournalService) bool {
	_, err := cache.loadPortableCloudEncryption()
	return err == nil
}
