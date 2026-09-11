package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// Generated with native Yjs and y-prosemirror; frontend tests also decode this
// fixture and compare its replayed document to RecoveredContent.
type crdtFixture struct {
	Snapshot         string         `json:"snapshot"`
	Update           string         `json:"update"`
	FullSnapshot     string         `json:"fullSnapshot"`
	Content          map[string]any `json:"content"`
	RecoveredContent map[string]any `json:"recoveredContent"`
}

func loadCRDTFixture(t *testing.T) crdtFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/crdt-document.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture crdtFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func newCRDTTestDocument(t *testing.T, s *JournalService, parent string) (DocumentResponse, CRDTSessionResponse, crdtFixture) {
	t.Helper()
	f := loadCRDTFixture(t)
	d, err := s.CreateDocument(parent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateDocumentDraft(d.ID, f.Content, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FlushDocument(d.ID); err != nil {
		t.Fatal(err)
	}
	c, err := s.OpenCRDTSession(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BootstrapCRDTDocument(CRDTBootstrapCommand{SessionID: c.SessionID, Snapshot: f.Snapshot}); err != nil {
		t.Fatal(err)
	}
	return d, c, f
}

func TestCRDTFlushPreservesCompactedSequence(t *testing.T) {
	s := newTestService(t)
	d, c, f := newCRDTTestDocument(t, s, "")
	a, err := s.SubmitCRDTUpdates(CRDTUpdateCommand{SessionID: c.SessionID, Updates: []CRDTWireUpdate{{ID: "update", Data: f.Update}}})
	if err != nil {
		t.Fatal(err)
	}
	projection := CRDTProjectionCommand{SessionID: c.SessionID, ThroughSeq: a.ThroughSeq, Snapshot: f.FullSnapshot, Content: f.RecoveredContent}
	if err := s.MaterializeCRDTProjection(projection); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		flushed, err := s.FlushCRDTSession(c.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if flushed.ThroughSeq != a.ThroughSeq {
			t.Fatalf("flush after compaction = %d, want %d", flushed.ThroughSeq, a.ThroughSeq)
		}
		projection.ThroughSeq = flushed.ThroughSeq
		if err := s.MaterializeCRDTProjection(projection); err != nil {
			t.Fatal(err)
		}
	}
	projection.ThroughSeq = 0
	projection.Content = f.Content
	if err := s.MaterializeCRDTProjection(projection); err == nil {
		t.Fatal("stale projection reported success")
	}
	opened, err := s.OpenDocument(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(opened.Content, f.RecoveredContent) {
		t.Fatal("stale projection replaced durable content")
	}
	// Empty submissions are also barriers and must retain the snapshot frontier.
	empty, err := s.SubmitCRDTUpdates(CRDTUpdateCommand{SessionID: c.SessionID})
	if err != nil || empty.ThroughSeq != a.ThroughSeq {
		t.Fatalf("empty submission: %#v, %v", empty, err)
	}
}

func TestCRDTEncryptionLockPreservesPlaintextSession(t *testing.T) {
	s := newTestService(t)
	_, plain, f := newCRDTTestDocument(t, s, "")
	j, err := s.CreateJournal("Encrypted")
	if err != nil {
		t.Fatal(err)
	}
	_, encrypted, _ := newCRDTTestDocument(t, s, j.Item.ID)
	if err := s.CreateMasterPassword("test password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EncryptJournal(j.Item.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.LockEncryption(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitCRDTUpdates(CRDTUpdateCommand{SessionID: plain.SessionID, Updates: []CRDTWireUpdate{{ID: "plain", Data: f.Update}}}); err != nil {
		t.Fatalf("plaintext saving after lock: %v", err)
	}
	if _, err := s.crdtSession(encrypted.SessionID); err == nil {
		t.Fatal("encrypted session survived lock")
	}
}

func TestCRDTEncryptedReplayDoesNotHoldConnectionForKeyLookup(t *testing.T) {
	s := newTestService(t)
	d, c, f := newCRDTTestDocument(t, s, "")
	j, err := s.journalIDForItem(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateMasterPassword("test password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EncryptJournal(j); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitCRDTUpdates(CRDTUpdateCommand{SessionID: c.SessionID, Updates: []CRDTWireUpdate{{ID: "encrypted", Data: f.Update}}}); err != nil {
		t.Fatal(err)
	}
	// The process timeout makes a regression fail without hanging the test suite.
	// Also verify replay after a real database close/reopen, as in crash recovery.
	path := s.repository.path
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenJournalService(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := reopened.UnlockEncryption("test password"); err != nil {
		t.Fatal(err)
	}
	session, err := reopened.OpenCRDTSession(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if session.Snapshot != f.Snapshot || !reflect.DeepEqual(session.Updates, []string{f.Update}) {
		t.Fatalf("wrong encrypted replay: %#v", session)
	}
	if _, err := reopened.GetLibraryTree(); err != nil {
		t.Fatal(err)
	}
}

func TestCRDTDecryptPreservesAttachmentIDsInSnapshotAndUpdates(t *testing.T) {
	s := newTestService(t)
	d, c, f := newCRDTTestDocument(t, s, "")
	image := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	for _, id := range []string{"snapshot-image", "pending-image", "detached-image"} {
		a, err := s.createDocumentAttachment(d.ID, id+".png", "image/png", image)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`UPDATE document_attachments SET id = ? WHERE id = ?`, id, a.ID); err != nil {
			t.Fatal(err)
		}
	}
	// The pending update reattaches an image that the old JSON still considers detached.
	if _, err := s.db.Exec(`UPDATE document_attachments SET detached_at = ? WHERE id != 'snapshot-image'`, nowString()); err != nil {
		t.Fatal(err)
	}
	j, err := s.journalIDForItem(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateMasterPassword("test password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EncryptJournal(j); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitCRDTUpdates(CRDTUpdateCommand{SessionID: c.SessionID, Updates: []CRDTWireUpdate{{ID: "image-update", Data: f.Update}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecryptJournal(j); err != nil {
		t.Fatal(err)
	}
	var target string
	if err := s.db.QueryRow(`SELECT document_id FROM document_crdt_state`).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if target == d.ID {
		t.Fatal("source document was not replaced")
	}
	session, err := s.OpenCRDTSession(target)
	if err != nil {
		t.Fatal(err)
	}
	if session.Snapshot != f.Snapshot || !reflect.DeepEqual(session.Updates, []string{f.Update}) {
		t.Fatal("decryption changed the canonical CRDT payloads")
	}
	for _, id := range []string{"snapshot-image", "pending-image", "detached-image"} {
		if _, err := s.GetDocumentAttachmentDataURL(id); err != nil {
			t.Fatalf("image %s missing: %v", id, err)
		}
		var owner string
		var ciphertext []byte
		if err := s.db.QueryRow(`SELECT document_id, content_ciphertext FROM document_attachments WHERE id = ?`, id).Scan(&owner, &ciphertext); err != nil {
			t.Fatal(err)
		}
		if owner != target || len(ciphertext) > 0 {
			t.Fatalf("image %s not moved to plaintext target", id)
		}
	}
	if err := s.MaterializeCRDTProjection(CRDTProjectionCommand{SessionID: session.SessionID, ThroughSeq: session.ThroughSeq, Snapshot: f.FullSnapshot, Content: f.RecoveredContent}); err != nil {
		t.Fatal(err)
	}
	var detached sql.NullString
	if err := s.db.QueryRow(`SELECT detached_at FROM document_attachments WHERE id = 'pending-image'`).Scan(&detached); err != nil {
		t.Fatal(err)
	}
	if detached.Valid {
		t.Fatal("replayed image still detached")
	}
	if err := s.PurgeDetachedAttachments(0); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"snapshot-image", "pending-image"} {
		if _, err := s.GetDocumentAttachmentDataURL(id); err != nil {
			t.Fatalf("referenced image purged: %v", err)
		}
	}
}
