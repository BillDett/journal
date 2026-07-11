package main

import (
	"database/sql"
	"sync"
	"time"
)

// JournalService owns shared application state and repository lifecycle.

type rowItem struct {
	ID               string
	ParentID         sql.NullString
	Kind             string
	Title            string
	SortOrder        int
	SystemKey        sql.NullString
	CreatedAt        string
	UpdatedAt        string
	EncryptionState  string
	EncryptionKeyID  sql.NullString
	TitleCiphertext  []byte
	EncryptionLocked bool
}

type pendingDraft struct {
	Content   map[string]any
	UpdatedAt time.Time
	Version   int64
}

type JournalService struct {
	repository       *SQLiteRepository
	store            JournalStore
	db               *sql.DB
	mu               sync.Mutex
	operationMu      sync.Mutex
	cryptoMu         sync.Mutex
	pending          map[string]pendingDraft
	lastDraftVersion map[string]int64
	masterKey        []byte
	journalKeys      map[string][]byte
}

func OpenJournalService(path string) (*JournalService, error) {
	store, err := openSQLiteJournalStore(path, LocalStoreID, StoreKindLocal)
	if err != nil {
		return nil, err
	}
	service := newJournalService(store)
	if err := service.migrate(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return service, nil
}

func newJournalService(store JournalStore) *JournalService {
	repository, _ := store.(*sqliteJournalStore)
	service := &JournalService{
		store:            store,
		db:               store.Database(),
		pending:          map[string]pendingDraft{},
		lastDraftVersion: map[string]int64{},
		journalKeys:      map[string][]byte{},
	}
	if repository != nil {
		service.repository = repository.repository
	}
	return service
}

func (s *JournalService) Close() error {
	if s.store != nil {
		return s.store.Close()
	}
	if s.repository != nil {
		return s.repository.Close()
	}
	return nil
}

func (s *JournalService) StoreID() StoreID {
	if s.store == nil {
		return LocalStoreID
	}
	return s.store.ID()
}

func (s *JournalService) StoreKind() StoreKind {
	if s.store == nil {
		return StoreKindLocal
	}
	return s.store.Kind()
}
