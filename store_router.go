package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
)

// StoreID identifies the database that owns a content operation. Item UUIDs
// remain database-local, so callers must never use an item ID as a store ID.
type StoreID string

const LocalStoreID StoreID = "local"

type StoreKind string

const (
	StoreKindLocal StoreKind = "local"
	StoreKindCloud StoreKind = "cloud"
)

func CloudStoreID(cloudJournalID string) StoreID {
	return StoreID("cloud:" + strings.TrimSpace(cloudJournalID))
}

// JournalStore owns one content database. Database is intentionally an
// internal backend dependency; Wails methods route through commands/services.
type JournalStore interface {
	ID() StoreID
	Database() *sql.DB
	Kind() StoreKind
	Close() error
}

type sqliteJournalStore struct {
	id         StoreID
	kind       StoreKind
	repository *SQLiteRepository
}

func (s *sqliteJournalStore) ID() StoreID       { return s.id }
func (s *sqliteJournalStore) Database() *sql.DB { return s.repository.db }
func (s *sqliteJournalStore) Kind() StoreKind   { return s.kind }
func (s *sqliteJournalStore) Close() error      { return s.repository.Close() }

func openSQLiteJournalStore(path string, id StoreID, kind StoreKind) (JournalStore, error) {
	repository, err := OpenSQLiteRepository(path)
	if err != nil {
		return nil, err
	}
	return &sqliteJournalStore{id: id, kind: kind, repository: repository}, nil
}

// JournalStoreRouter resolves an already-open local or cloud store. It does
// not open arbitrary paths, which keeps database selection separate from RPC
// input and prevents one cloud Journal from addressing another cache.
type JournalStoreRouter struct {
	mu     sync.RWMutex
	local  JournalStore
	stores map[StoreID]JournalStore
}

func NewJournalStoreRouter(local JournalStore) (*JournalStoreRouter, error) {
	if local == nil || local.Kind() != StoreKindLocal || local.ID() != LocalStoreID {
		return nil, fmt.Errorf("local store is required")
	}
	return &JournalStoreRouter{
		local:  local,
		stores: map[StoreID]JournalStore{LocalStoreID: local},
	}, nil
}

func (r *JournalStoreRouter) Local() JournalStore { return r.local }

func (r *JournalStoreRouter) Resolve(_ context.Context, id StoreID) (JournalStore, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	store, ok := r.stores[id]
	if !ok {
		return nil, fmt.Errorf("store_not_found: %s", id)
	}
	return store, nil
}

func (r *JournalStoreRouter) Register(store JournalStore) error {
	if store == nil || store.ID() == LocalStoreID || store.Kind() != StoreKindCloud {
		return fmt.Errorf("invalid cloud store")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.stores[store.ID()]; exists {
		return fmt.Errorf("store already registered: %s", store.ID())
	}
	r.stores[store.ID()] = store
	return nil
}

func (r *JournalStoreRouter) Unregister(id StoreID) error {
	if id == LocalStoreID {
		return fmt.Errorf("cannot unregister local store")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	store, ok := r.stores[id]
	if !ok {
		return fmt.Errorf("store_not_found: %s", id)
	}
	delete(r.stores, id)
	return store.Close()
}

func (r *JournalStoreRouter) IsRegistered(id StoreID) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.stores[id]
	return ok
}

// ParseStoreScopedItemID supports the transport form documented in CLOUD.md
// while keeping the underlying service APIs free to accept storeID + itemID as
// separate fields in later phases.
func ParseStoreScopedItemID(value string) (StoreID, string, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	switch {
	case len(parts) == 2 && parts[0] == "local" && parts[1] != "":
		return LocalStoreID, parts[1], nil
	case len(parts) == 3 && parts[0] == "cloud" && parts[1] != "" && parts[2] != "":
		return CloudStoreID(parts[1]), parts[2], nil
	default:
		return "", "", fmt.Errorf("invalid store-scoped item ID")
	}
}
