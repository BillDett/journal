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
	repository, err := OpenSQLiteRepository(path)
	if err != nil {
		return nil, err
	}

	service := &JournalService{
		repository:       repository,
		db:               repository.db,
		pending:          map[string]pendingDraft{},
		lastDraftVersion: map[string]int64{},
		journalKeys:      map[string][]byte{},
	}
	if err := service.migrate(); err != nil {
		_ = repository.Close()
		return nil, err
	}
	return service, nil
}

func (s *JournalService) Close() error {
	return s.repository.Close()
}
