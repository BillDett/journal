package main

import (
	"context"
	"fmt"
)

// CloudJournalStore is the write-permission gate for a routed cloud cache.
// Phase 4 binds its command surface to this type; keeping the check here
// prevents UI state from becoming the only protection against remote writes.
type CloudJournalStore struct {
	Journal        *JournalService
	Sync           *VaultSyncService
	CloudJournalID string
}

func (s *CloudJournalStore) EnsureWritable(ctx context.Context) error {
	if s.Journal == nil || s.Journal.StoreKind() != StoreKindCloud {
		return fmt.Errorf("cache_missing")
	}
	if err := s.Journal.ValidateCloudJournalScope(s.CloudJournalID); err != nil {
		return fmt.Errorf("cache_corrupt: %w", err)
	}
	if s.Sync == nil {
		return fmt.Errorf("lease_lost")
	}
	mount, err := s.Sync.mount(s.CloudJournalID)
	if err != nil {
		return fmt.Errorf("cache_missing: %w", err)
	}
	if mount.SyncStatus == "conflict" {
		return fmt.Errorf("current_pointer_conflict")
	}
	if mount.SyncStatus == "locked_read_only" || mount.SyncStatus == "provider_missing" {
		return fmt.Errorf("lease_lost")
	}
	data, _, err := s.Sync.Store.GetControl(ctx, s.Sync.Provider, mustVaultLease(s.CloudJournalID))
	if err != nil {
		return fmt.Errorf("lease_lost: %w", err)
	}
	lease, err := parseVaultLease(data)
	if err != nil || lease.OwnerDeviceID != s.Sync.Device.ID || !lease.ExpiresAt.After(s.Sync.now()) {
		return fmt.Errorf("lease_lost")
	}
	return nil
}
