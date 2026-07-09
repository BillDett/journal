package main

import "fmt"

// SchemaMigration is deliberately small: migrations are ordered, atomic where
// SQLite permits it, and recorded only after their work succeeds.
type SchemaMigration struct {
	Version int
	Name    string
	Apply   func(*JournalService) error
}

var schemaMigrations = []SchemaMigration{
	{Version: 1, Name: "initial library, search, encryption, and attachments", Apply: (*JournalService).migrateV1},
}

func (s *JournalService) migrate() error {
	var current int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&current); err != nil {
		return err
	}
	latest := schemaMigrations[len(schemaMigrations)-1].Version
	if current > latest {
		return fmt.Errorf("database schema version %d is newer than this application supports (%d)", current, latest)
	}
	for _, migration := range schemaMigrations {
		if migration.Version <= current {
			continue
		}
		if err := migration.Apply(s); err != nil {
			return fmt.Errorf("apply migration %d (%s): %w", migration.Version, migration.Name, err)
		}
		if _, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, migration.Version)); err != nil {
			return fmt.Errorf("record migration %d: %w", migration.Version, err)
		}
		current = migration.Version
	}
	return nil
}
