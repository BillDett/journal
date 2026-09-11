package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEncryptedExportDoesNotDeadlock(t *testing.T) {
	// Run the regression in a child so a connection-pool deadlock produces a
	// stack trace and failure instead of also hanging database cleanup.
	if os.Getenv("JOURNAL_ENCRYPTED_EXPORT_TEST") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestEncryptedExportDoesNotDeadlock$", "-test.timeout=10s", "-test.v")
		cmd.Env = append(os.Environ(), "JOURNAL_ENCRYPTED_EXPORT_TEST=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("encrypted export failed: %v\n%s", err, output)
		}
		return
	}
	for _, mode := range []string{"document", "journal", "trashed-document", "locked"} {
		t.Run(mode, func(t *testing.T) {
			s := newTestService(t)
			journal, err := s.CreateJournal("Private")
			if err != nil {
				t.Fatal(err)
			}
			folder, err := s.CreateFolder(journal.Item.ID, "Encrypted folder")
			if err != nil {
				t.Fatal(err)
			}
			doc, _, fixture := newCRDTTestDocument(t, s, folder.Item.ID)
			image := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
			attachment, err := s.createDocumentAttachment(doc.ID, "photo.png", "image/png", image)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`UPDATE document_attachments SET id = 'snapshot-image' WHERE id = ?`, attachment.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.CreateMasterPassword("export test password"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.EncryptJournal(journal.Item.ID); err != nil {
				t.Fatal(err)
			}
			if mode == "locked" {
				if err := s.LockEncryption(); err != nil {
					t.Fatal(err)
				}
				if _, err := s.exportDocumentAttachments(doc.ID, fixture.Content); !errors.Is(err, ErrEncryptionLocked) {
					t.Fatalf("locked export: %v", err)
				}
				if _, err := s.GetLibraryTree(); err != nil {
					t.Fatal(err)
				}
				return
			}
			if mode == "trashed-document" {
				if _, err := s.TrashItem(TrashItemCommand{ID: doc.ID, ExpectedInTrash: false}); err != nil {
					t.Fatal(err)
				}
			}
			out := t.TempDir()
			if mode == "journal" {
				if err := s.ExportJournalToDirectory(journal.Item.ID, out); err != nil {
					t.Fatal(err)
				}
			} else if err := s.ExportDocumentToMarkdown(doc.ID, filepath.Join(out, "note.md")); err != nil {
				t.Fatal(err)
			}
			var markdownPaths []string
			if err := filepath.WalkDir(out, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !entry.IsDir() && filepath.Ext(path) == ".md" {
					markdownPaths = append(markdownPaths, path)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if len(markdownPaths) != 1 {
				t.Fatalf("exported markdown files: %v", markdownPaths)
			}
			markdown, err := os.ReadFile(markdownPaths[0])
			if err != nil {
				t.Fatal(err)
			}
			assetDir := strings.TrimSuffix(markdownPaths[0], ".md") + ".assets"
			asset, err := os.ReadFile(filepath.Join(assetDir, "photo.png"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(asset, image) {
				t.Fatal("exported image is not the decrypted original")
			}
			if !strings.Contains(string(markdown), "Before replay") || !strings.Contains(string(markdown), filepath.Base(assetDir)+"/photo.png") {
				t.Fatalf("incorrect markdown: %s", markdown)
			}
			// Normal database operations and shutdown must still be possible.
			if _, err := s.GetLibraryTree(); err != nil {
				t.Fatal(err)
			}
			if err := s.FlushAll(); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
