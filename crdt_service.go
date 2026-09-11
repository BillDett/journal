package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The CRDT coordinator deliberately persists opaque, native Yjs V1 update
// bytes. It does not attempt to decode or merge them in Go; that keeps the
// local Wails transport independent of a future Yjs-compatible server.
const (
	crdtFormatVersion = 1
	maxCRDTUpdateSize = 2 * 1024 * 1024
	maxCRDTBatchSize  = 8 * 1024 * 1024
)

type crdtSession struct {
	documentID string
	generation int
	openedAt   time.Time
}

type crdtEncryptionContext struct {
	encrypted bool
	key       []byte
	keyID     string
}

func (s *JournalService) OpenCRDTSession(documentID string) (CRDTSessionResponse, error) {
	documentID = strings.TrimSpace(documentID)
	item, err := s.getRawRowItemFrom(s.db, documentID)
	if err != nil {
		return CRDTSessionResponse{}, err
	}
	if item.Kind != KindDocument {
		return CRDTSessionResponse{}, fmt.Errorf("item is not a document")
	}
	// Resolve keys before opening the update cursor: SQLite has one connection.
	crypto, err := s.crdtEncryptionContext(item)
	if err != nil {
		return CRDTSessionResponse{}, err
	}

	response := CRDTSessionResponse{
		SessionID:     uuid.NewString(),
		DocumentID:    documentID,
		FormatVersion: crdtFormatVersion,
		Bootstrap:     true,
		Generation:    1,
		Updates:       []string{},
	}
	var snapshot []byte
	var snapshotKeyID sql.NullString
	var throughSeq int64
	err = s.db.QueryRow(
		`SELECT generation, format_version, snapshot, snapshot_key_id, snapshot_through_seq
		 FROM document_crdt_state WHERE document_id = ?`, documentID,
	).Scan(&response.Generation, &response.FormatVersion, &snapshot, &snapshotKeyID, &throughSeq)
	if err == sql.ErrNoRows {
		s.rememberCRDTSession(response.SessionID, crdtSession{documentID: documentID, generation: 1, openedAt: time.Now()})
		return response, nil
	}
	if err != nil {
		return CRDTSessionResponse{}, err
	}
	if response.FormatVersion != crdtFormatVersion {
		return CRDTSessionResponse{}, fmt.Errorf("CRDT format version %d is not supported", response.FormatVersion)
	}
	response.Bootstrap = false
	response.ThroughSeq = throughSeq
	if len(snapshot) > 0 {
		plaintext, err := openCRDTSnapshot(item.ID, crypto, snapshot, snapshotKeyID)
		if err != nil {
			return CRDTSessionResponse{}, err
		}
		response.Snapshot = base64.StdEncoding.EncodeToString(plaintext)
	}
	rows, err := s.db.Query(
		`SELECT seq, update_id, update_blob, key_id FROM document_crdt_updates
		 WHERE document_id = ? AND generation = ? AND seq > ? ORDER BY seq`,
		documentID, response.Generation, throughSeq,
	)
	if err != nil {
		return CRDTSessionResponse{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var seq int64
		var updateID string
		var blob []byte
		var keyID sql.NullString
		if err := rows.Scan(&seq, &updateID, &blob, &keyID); err != nil {
			return CRDTSessionResponse{}, err
		}
		plaintext, err := openCRDTUpdate(crypto, updateID, blob, keyID)
		if err != nil {
			return CRDTSessionResponse{}, err
		}
		response.Updates = append(response.Updates, base64.StdEncoding.EncodeToString(plaintext))
		response.ThroughSeq = seq
	}
	if err := rows.Err(); err != nil {
		return CRDTSessionResponse{}, err
	}
	s.rememberCRDTSession(response.SessionID, crdtSession{documentID: documentID, generation: response.Generation, openedAt: time.Now()})
	return response, nil
}

func (s *JournalService) BootstrapCRDTDocument(command CRDTBootstrapCommand) (CRDTDurabilityResponse, error) {
	session, err := s.crdtSession(command.SessionID)
	if err != nil {
		return CRDTDurabilityResponse{}, err
	}
	snapshot, err := decodeCRDTBytes(command.Snapshot)
	if err != nil {
		return CRDTDurabilityResponse{}, err
	}
	if len(snapshot) == 0 {
		return CRDTDurabilityResponse{}, fmt.Errorf("CRDT bootstrap snapshot is required")
	}
	item, err := s.getRawRowItemFrom(s.db, session.documentID)
	if err != nil {
		return CRDTDurabilityResponse{}, err
	}
	crypto, err := s.crdtEncryptionContext(item)
	if err != nil {
		return CRDTDurabilityResponse{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return CRDTDurabilityResponse{}, err
	}
	defer rollback(tx)
	var existing int
	err = tx.QueryRow(`SELECT 1 FROM document_crdt_state WHERE document_id = ?`, session.documentID).Scan(&existing)
	if err == nil {
		return CRDTDurabilityResponse{}, fmt.Errorf("CRDT document is already initialized")
	}
	if err != sql.ErrNoRows {
		return CRDTDurabilityResponse{}, err
	}
	stored, keyID, err := s.sealCRDTSnapshot(item, crypto, snapshot)
	if err != nil {
		return CRDTDurabilityResponse{}, err
	}
	now := nowString()
	if _, err := tx.Exec(
		`INSERT INTO document_crdt_state
		 (document_id, generation, format_version, snapshot, snapshot_key_id, snapshot_through_seq, materialized_through_seq, updated_at)
		 VALUES (?, ?, ?, ?, ?, 0, 0, ?)`,
		session.documentID, session.generation, crdtFormatVersion, stored, keyID, now,
	); err != nil {
		return CRDTDurabilityResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return CRDTDurabilityResponse{}, err
	}
	return CRDTDurabilityResponse{DocumentID: session.documentID, SaveState: "saved", SavedAt: now}, nil
}

func (s *JournalService) SubmitCRDTUpdates(command CRDTUpdateCommand) (CRDTDurabilityResponse, error) {
	session, err := s.crdtSession(command.SessionID)
	if err != nil {
		return CRDTDurabilityResponse{}, err
	}
	if len(command.Updates) == 0 {
		return s.FlushCRDTSession(command.SessionID)
	}
	updates := make([]struct {
		id   string
		data []byte
	}, 0, len(command.Updates))
	total := 0
	for _, wire := range command.Updates {
		updateID := strings.TrimSpace(wire.ID)
		if updateID == "" {
			return CRDTDurabilityResponse{}, fmt.Errorf("CRDT update ID is required")
		}
		update, err := decodeCRDTBytes(wire.Data)
		if err != nil {
			return CRDTDurabilityResponse{}, err
		}
		if len(update) == 0 || len(update) > maxCRDTUpdateSize {
			return CRDTDurabilityResponse{}, fmt.Errorf("CRDT update has an invalid size")
		}
		total += len(update)
		if total > maxCRDTBatchSize {
			return CRDTDurabilityResponse{}, fmt.Errorf("CRDT update batch is too large")
		}
		updates = append(updates, struct {
			id   string
			data []byte
		}{id: updateID, data: update})
	}
	item, err := s.getRawRowItemFrom(s.db, session.documentID)
	if err != nil {
		return CRDTDurabilityResponse{}, err
	}
	crypto, err := s.crdtEncryptionContext(item)
	if err != nil {
		return CRDTDurabilityResponse{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return CRDTDurabilityResponse{}, err
	}
	defer rollback(tx)
	var generation, formatVersion int
	if err := tx.QueryRow(`SELECT generation, format_version FROM document_crdt_state WHERE document_id = ?`, session.documentID).Scan(&generation, &formatVersion); err != nil {
		if err == sql.ErrNoRows {
			return CRDTDurabilityResponse{}, fmt.Errorf("CRDT document is not initialized")
		}
		return CRDTDurabilityResponse{}, err
	}
	if generation != session.generation || formatVersion != crdtFormatVersion {
		return CRDTDurabilityResponse{}, fmt.Errorf("CRDT session is no longer current")
	}
	now := nowString()
	for _, update := range updates {
		stored, keyID, err := s.sealCRDTUpdate(item, crypto, update.id, update.data)
		if err != nil {
			return CRDTDurabilityResponse{}, err
		}
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO document_crdt_updates (update_id, document_id, generation, update_blob, key_id, created_at)
			 VALUES (?, ?, ?, ?, ?, ?)`, update.id, session.documentID, generation, stored, keyID, now,
		); err != nil {
			return CRDTDurabilityResponse{}, err
		}
	}
	throughSeq, err := durableCRDTSequence(tx, session)
	if err != nil {
		return CRDTDurabilityResponse{}, err
	}
	if _, err := tx.Exec(`UPDATE document_crdt_state SET updated_at = ? WHERE document_id = ?`, now, session.documentID); err != nil {
		return CRDTDurabilityResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return CRDTDurabilityResponse{}, err
	}
	return CRDTDurabilityResponse{DocumentID: session.documentID, SaveState: "saved", ThroughSeq: throughSeq, SavedAt: now}, nil
}

func (s *JournalService) MaterializeCRDTProjection(command CRDTProjectionCommand) error {
	session, err := s.crdtSession(command.SessionID)
	if err != nil {
		return err
	}
	if err := validateProseMirrorDoc(command.Content); err != nil {
		return err
	}
	if command.ThroughSeq < 0 {
		return fmt.Errorf("CRDT projection sequence is invalid")
	}
	stateVector, err := decodeOptionalCRDTBytes(command.StateVector)
	if err != nil {
		return err
	}
	snapshot, err := decodeOptionalCRDTBytes(command.Snapshot)
	if err != nil {
		return err
	}
	if len(snapshot) > maxCRDTBatchSize {
		return fmt.Errorf("CRDT snapshot is too large")
	}
	item, err := s.getRawRowItemFrom(s.db, session.documentID)
	if err != nil {
		return err
	}
	crypto, err := s.crdtEncryptionContext(item)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer rollback(tx)
	var materializedThrough, snapshotThrough int64
	if err := tx.QueryRow(`SELECT materialized_through_seq, snapshot_through_seq FROM document_crdt_state WHERE document_id = ? AND generation = ? AND format_version = ?`, session.documentID, session.generation, crdtFormatVersion).Scan(&materializedThrough, &snapshotThrough); err != nil {
		return err
	}
	if command.ThroughSeq < materializedThrough {
		return fmt.Errorf("CRDT projection is older than the materialized state")
	}
	var latestUpdate int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM document_crdt_updates WHERE document_id = ? AND generation = ?`, session.documentID, session.generation).Scan(&latestUpdate); err != nil {
		return err
	}
	latest := max(snapshotThrough, latestUpdate)
	if command.ThroughSeq > latest {
		return fmt.Errorf("CRDT projection is ahead of durable updates")
	}
	if err := s.saveDocumentProjectionTx(tx, session.documentID, command.Content, crypto); err != nil {
		return err
	}
	if len(snapshot) > 0 {
		storedSnapshot, keyID, err := s.sealCRDTSnapshot(item, crypto, snapshot)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE document_crdt_state SET snapshot = ?, snapshot_key_id = ?, snapshot_through_seq = ? WHERE document_id = ?`, storedSnapshot, keyID, command.ThroughSeq, session.documentID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM document_crdt_updates WHERE document_id = ? AND generation = ? AND seq <= ?`, session.documentID, session.generation, command.ThroughSeq); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(
		`UPDATE document_crdt_state SET materialized_through_seq = ?, materialized_state_vector = ?, updated_at = ? WHERE document_id = ?`,
		command.ThroughSeq, stateVector, nowString(), session.documentID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *JournalService) FlushCRDTSession(sessionID string) (CRDTDurabilityResponse, error) {
	session, err := s.crdtSession(sessionID)
	if err != nil {
		return CRDTDurabilityResponse{}, err
	}
	throughSeq, err := durableCRDTSequence(s.db, session)
	if err != nil {
		return CRDTDurabilityResponse{}, err
	}
	return CRDTDurabilityResponse{DocumentID: session.documentID, SaveState: "saved", ThroughSeq: throughSeq, SavedAt: nowString()}, nil
}

func (s *JournalService) CloseCRDTSession(sessionID string) {
	s.crdtMu.Lock()
	delete(s.crdtSessions, strings.TrimSpace(sessionID))
	s.crdtMu.Unlock()
}

// Compaction removes log rows, but never moves the durable frontier backwards.
func durableCRDTSequence(db queryRower, session crdtSession) (int64, error) {
	var seq int64
	err := db.QueryRow(`SELECT MAX(snapshot_through_seq, COALESCE(
		(SELECT MAX(seq) FROM document_crdt_updates WHERE document_id = ? AND generation = ?), 0))
		FROM document_crdt_state WHERE document_id = ? AND generation = ? AND format_version = ?`,
		session.documentID, session.generation, session.documentID, session.generation, crdtFormatVersion).Scan(&seq)
	return seq, err
}

func (s *JournalService) invalidateEncryptedCRDTSessions() error {
	rows, err := s.db.Query(`SELECT id FROM items WHERE encryption_state = ?`, EncryptionEncrypted)
	if err != nil {
		return err
	}
	encrypted := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		encrypted[id] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.crdtMu.Lock()
	defer s.crdtMu.Unlock()
	for id, session := range s.crdtSessions {
		if encrypted[session.documentID] {
			delete(s.crdtSessions, id)
		}
	}
	return nil
}

func (s *JournalService) rememberCRDTSession(id string, session crdtSession) {
	s.crdtMu.Lock()
	s.crdtSessions[id] = session
	s.crdtMu.Unlock()
}

func (s *JournalService) crdtSession(id string) (crdtSession, error) {
	s.crdtMu.Lock()
	session, ok := s.crdtSessions[strings.TrimSpace(id)]
	s.crdtMu.Unlock()
	if !ok {
		return crdtSession{}, fmt.Errorf("CRDT session is unavailable")
	}
	return session, nil
}

func decodeCRDTBytes(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("CRDT data must be base64 encoded: %w", err)
	}
	return decoded, nil
}

func decodeOptionalCRDTBytes(encoded string) ([]byte, error) { return decodeCRDTBytes(encoded) }

func (s *JournalService) crdtEncryptionContext(item rowItem) (crdtEncryptionContext, error) {
	if item.EncryptionState != EncryptionEncrypted {
		return crdtEncryptionContext{}, nil
	}
	journalID, err := s.encryptionJournalIDForItem(item.ID)
	if err != nil {
		return crdtEncryptionContext{}, err
	}
	key, ok := s.journalKey(journalID)
	if !ok || !item.EncryptionKeyID.Valid {
		return crdtEncryptionContext{}, ErrEncryptionLocked
	}
	return crdtEncryptionContext{encrypted: true, key: key, keyID: item.EncryptionKeyID.String}, nil
}

func (s *JournalService) sealCRDTSnapshot(item rowItem, crypto crdtEncryptionContext, plaintext []byte) ([]byte, any, error) {
	if !crypto.encrypted {
		return plaintext, nil, nil
	}
	sealed, err := sealField(crypto.key, "document_crdt_state", item.ID, "snapshot", crypto.keyID, plaintext)
	return sealed, crypto.keyID, err
}

func openCRDTSnapshot(documentID string, crypto crdtEncryptionContext, stored []byte, keyID sql.NullString) ([]byte, error) {
	if !keyID.Valid {
		return stored, nil
	}
	if !crypto.encrypted {
		return nil, ErrEncryptionLocked
	}
	return openField(crypto.key, "document_crdt_state", documentID, "snapshot", keyID.String, stored)
}

func (s *JournalService) sealCRDTUpdate(item rowItem, crypto crdtEncryptionContext, updateID string, plaintext []byte) ([]byte, any, error) {
	if !crypto.encrypted {
		return plaintext, nil, nil
	}
	sealed, err := sealField(crypto.key, "document_crdt_updates", updateID, "update_blob", crypto.keyID, plaintext)
	return sealed, crypto.keyID, err
}

func openCRDTUpdate(crypto crdtEncryptionContext, updateID string, stored []byte, keyID sql.NullString) ([]byte, error) {
	if !keyID.Valid {
		return stored, nil
	}
	if !crypto.encrypted {
		return nil, ErrEncryptionLocked
	}
	return openField(crypto.key, "document_crdt_updates", updateID, "update_blob", keyID.String, stored)
}

func (s *JournalService) saveDocumentProjectionTx(tx *sql.Tx, id string, content map[string]any, crypto crdtEncryptionContext) error {
	encoded, err := json.Marshal(content)
	if err != nil {
		return err
	}
	contentJSON := string(encoded)
	var ciphertext []byte
	if crypto.encrypted {
		ciphertext, err = sealField(crypto.key, "documents", id, "content_json", crypto.keyID, encoded)
		if err != nil {
			return err
		}
		placeholder, err := json.Marshal(emptyDocument())
		if err != nil {
			return err
		}
		contentJSON = string(placeholder)
	}
	now := nowString()
	if _, err := tx.Exec(`UPDATE documents SET content_json = ?, content_ciphertext = ?, updated_at = ? WHERE item_id = ?`, contentJSON, ciphertext, now, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE items SET updated_at = ? WHERE id = ?`, now, id); err != nil {
		return err
	}
	if err := s.reconcileDocumentAttachmentsTx(tx, id, content); err != nil {
		return err
	}
	return s.syncFTSTx(tx, id)
}

// encryptCRDTDocumentTx moves canonical CRDT payloads under the journal key
// during Journal encryption. It is intentionally transactional with the
// document/body transition so a crash cannot leave a readable CRDT log behind.
func (s *JournalService) encryptCRDTDocumentTx(tx *sql.Tx, documentID string, key []byte, keyID string) error {
	var snapshot []byte
	if err := tx.QueryRow(`SELECT snapshot FROM document_crdt_state WHERE document_id = ?`, documentID).Scan(&snapshot); err == sql.ErrNoRows {
		return nil
	} else if err != nil {
		return err
	}
	if len(snapshot) > 0 {
		sealed, err := sealField(key, "document_crdt_state", documentID, "snapshot", keyID, snapshot)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE document_crdt_state SET snapshot = ?, snapshot_key_id = ? WHERE document_id = ?`, sealed, keyID, documentID); err != nil {
			return err
		}
	} else if _, err := tx.Exec(`UPDATE document_crdt_state SET snapshot_key_id = ? WHERE document_id = ?`, keyID, documentID); err != nil {
		return err
	}

	rows, err := tx.Query(`SELECT update_id, update_blob FROM document_crdt_updates WHERE document_id = ?`, documentID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type update struct {
		id   string
		blob []byte
	}
	var updates []update
	for rows.Next() {
		var row update
		if err := rows.Scan(&row.id, &row.blob); err != nil {
			return err
		}
		updates = append(updates, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, update := range updates {
		sealed, err := sealField(key, "document_crdt_updates", update.id, "update_blob", keyID, update.blob)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE document_crdt_updates SET update_blob = ?, key_id = ? WHERE update_id = ?`, sealed, keyID, update.id); err != nil {
			return err
		}
	}
	return nil
}

// copyCRDTDocumentToPlaintextTx carries a document's authoritative Yjs state
// through the existing decrypt-as-copy workflow. Fresh update IDs are used
// because the source remains present until the transaction commits.
func (s *JournalService) copyCRDTDocumentToPlaintextTx(tx *sql.Tx, sourceID, targetID string, key []byte, keyID string) error {
	var generation, formatVersion int
	var snapshot []byte
	var snapshotKeyID sql.NullString
	var snapshotThrough, materializedThrough int64
	var stateVector []byte
	err := tx.QueryRow(`SELECT generation, format_version, snapshot, snapshot_key_id, snapshot_through_seq, materialized_through_seq, materialized_state_vector
		FROM document_crdt_state WHERE document_id = ?`, sourceID).Scan(&generation, &formatVersion, &snapshot, &snapshotKeyID, &snapshotThrough, &materializedThrough, &stateVector)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if snapshotKeyID.Valid && len(snapshot) > 0 {
		plaintext, err := openField(key, "document_crdt_state", sourceID, "snapshot", snapshotKeyID.String, snapshot)
		if err != nil {
			return err
		}
		snapshot = plaintext
	}
	if _, err := tx.Exec(`INSERT INTO document_crdt_state
		(document_id, generation, format_version, snapshot, snapshot_through_seq, materialized_through_seq, materialized_state_vector, updated_at)
		VALUES (?, ?, ?, ?, 0, 0, ?, ?)`, targetID, generation, formatVersion, snapshot, stateVector, nowString()); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT seq, update_id, update_blob, key_id, created_at FROM document_crdt_updates WHERE document_id = ? ORDER BY seq`, sourceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var copiedSnapshotThrough, copiedMaterializedThrough int64
	for rows.Next() {
		var sourceSeq int64
		var sourceUpdateID string
		var blob []byte
		var updateKeyID sql.NullString
		var createdAt string
		if err := rows.Scan(&sourceSeq, &sourceUpdateID, &blob, &updateKeyID, &createdAt); err != nil {
			return err
		}
		if updateKeyID.Valid {
			plaintext, err := openField(key, "document_crdt_updates", sourceUpdateID, "update_blob", updateKeyID.String, blob)
			if err != nil {
				return err
			}
			blob = plaintext
		}
		result, err := tx.Exec(`INSERT INTO document_crdt_updates (update_id, document_id, generation, update_blob, created_at) VALUES (?, ?, ?, ?, ?)`, uuid.NewString(), targetID, generation, blob, createdAt)
		if err != nil {
			return err
		}
		newSeq, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if sourceSeq <= snapshotThrough {
			copiedSnapshotThrough = newSeq
		}
		if sourceSeq <= materializedThrough {
			copiedMaterializedThrough = newSeq
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE document_crdt_state SET snapshot_through_seq = ?, materialized_through_seq = ? WHERE document_id = ?`, copiedSnapshotThrough, copiedMaterializedThrough, targetID)
	return err
}
