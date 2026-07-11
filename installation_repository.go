package main

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// InstallationRepository is intentionally separate from JournalService's
// content repositories. Its records describe this device; none belong in a
// cloud cache or a portable revision.
type InstallationRepository struct {
	db *sql.DB
}

func NewInstallationRepository(db *sql.DB) *InstallationRepository {
	return &InstallationRepository{db: db}
}

type DeviceIdentity struct {
	ID    string
	Label string
}

func (s *JournalService) ensureDeviceIdentity() error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM installation_device_identity WHERE id = 1`).Scan(&count); err != nil {
		return err
	}
	if count == 1 {
		return nil
	}
	now := nowString()
	_, err := s.db.Exec(`INSERT INTO installation_device_identity (id, device_id, owner_label, created_at, updated_at)
		VALUES (1, ?, 'This device', ?, ?)`, uuid.NewString(), now, now)
	return err
}

func (r *InstallationRepository) DeviceIdentity() (DeviceIdentity, error) {
	var identity DeviceIdentity
	err := r.db.QueryRow(`SELECT device_id, owner_label FROM installation_device_identity WHERE id = 1`).Scan(&identity.ID, &identity.Label)
	if err != nil {
		return DeviceIdentity{}, err
	}
	return identity, nil
}

func (r *InstallationRepository) UpdateDeviceLabel(label string) (DeviceIdentity, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return DeviceIdentity{}, fmt.Errorf("device label is required")
	}
	if _, err := r.db.Exec(`UPDATE installation_device_identity SET owner_label = ?, updated_at = ? WHERE id = 1`, label, nowString()); err != nil {
		return DeviceIdentity{}, err
	}
	return r.DeviceIdentity()
}

type VaultProviderRecord struct {
	ID                     string
	Name                   string
	Kind                   string
	Endpoint               string
	RootPrefix             string
	CredentialRef          string
	PublishDebounceMS      int
	PublishMaxIntervalMS   int
	RevisionRetentionCount int
	CreatedAt              string
	UpdatedAt              string
}

type CloudJournalMountRecord struct {
	CloudJournalID         string
	ProviderID             string
	VaultRoot              string
	CachePath              string
	LastRevisionID         string
	LastCurrentToken       string
	LeaseID                string
	RevisionRetentionCount int
	SyncStatus             string
	LastSyncError          string
	LastSyncedAt           string
	CreatedAt              string
	UpdatedAt              string
}

type CloudPendingCreateRecord struct {
	CloudJournalID string
	ProviderID     string
	CachePath      string
	Stage          string
	LastError      string
	CreatedAt      string
	UpdatedAt      string
}

func (r *InstallationRepository) UpsertPendingCreate(record CloudPendingCreateRecord) error {
	if err := validateCloudJournalID(record.CloudJournalID); err != nil {
		return err
	}
	if strings.TrimSpace(record.ProviderID) == "" || strings.TrimSpace(record.CachePath) == "" || strings.TrimSpace(record.Stage) == "" {
		return fmt.Errorf("pending create requires provider, cache path, and stage")
	}
	now := nowString()
	_, err := r.db.Exec(`INSERT INTO cloud_pending_creates (cloud_journal_id, provider_id, cache_path, stage, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(cloud_journal_id) DO UPDATE SET provider_id = excluded.provider_id, cache_path = excluded.cache_path,
		stage = excluded.stage, last_error = excluded.last_error, updated_at = excluded.updated_at`,
		record.CloudJournalID, record.ProviderID, record.CachePath, record.Stage, record.LastError, now, now)
	return err
}

func (r *InstallationRepository) RemoveProvider(providerID string, hasUnsyncedWork func(CloudJournalMountRecord) bool) error {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return fmt.Errorf("provider ID is required")
	}
	rows, err := r.db.Query(`SELECT cloud_journal_id, provider_id, vault_root, cache_path, last_revision_id, last_current_token,
		lease_id, revision_retention_count, sync_status, last_sync_error, last_synced_at, created_at, updated_at
		FROM cloud_journal_mounts WHERE provider_id = ?`, providerID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var mounts []CloudJournalMountRecord
	for rows.Next() {
		var mount CloudJournalMountRecord
		if err := rows.Scan(&mount.CloudJournalID, &mount.ProviderID, &mount.VaultRoot, &mount.CachePath, &mount.LastRevisionID,
			&mount.LastCurrentToken, &mount.LeaseID, &mount.RevisionRetentionCount, &mount.SyncStatus, &mount.LastSyncError,
			&mount.LastSyncedAt, &mount.CreatedAt, &mount.UpdatedAt); err != nil {
			return err
		}
		if hasUnsyncedWork != nil && hasUnsyncedWork(mount) {
			return fmt.Errorf("cannot remove provider with unsynced cloud cache")
		}
		mounts = append(mounts, mount)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer rollback(tx)
	for _, mount := range mounts {
		if _, err := tx.Exec(`UPDATE cloud_journal_mounts SET provider_id = '', sync_status = 'provider_missing', updated_at = ? WHERE cloud_journal_id = ?`, nowString(), mount.CloudJournalID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM vault_providers WHERE id = ?`, providerID); err != nil {
		return err
	}
	return tx.Commit()
}
