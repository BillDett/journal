# Journal-Level Encryption Implementation Plan

## Goal

Add optional encryption for selected Journals while continuing to use
`modernc.org/sqlite` and avoiding cgo. Encryption is applied at the application
layer to sensitive columns, not to the SQLite database file itself.

Encryption remains off by default. Users can keep some Journals plaintext and
encrypt others. Plaintext Journals keep the current fast SQLite FTS search.
Encrypted Journals never appear in persistent search results, whether locked or
unlocked.

## Product Decisions

- Journal names remain plaintext, including encrypted Journal names.
- Opening an encrypted Journal prompts for the master password if the app has
  not already been unlocked in the current session.
- There is one master password for all encrypted Journals in an installation.
- The first request to encrypt any Journal creates the master password.
- Users can change the master password from settings after entering the current
  master password.
- Encrypted Journals use a visibly different outliner treatment, such as a lock
  icon before the Journal name.
- Encryption can be turned off for a Journal. The preferred implementation is a
  safe plaintext-copy replacement flow rather than in-place mutation.

## Security Model

This is not full database encryption. It protects Journal-owned private content
from casual inspection in a SQLite browser, while preserving cgo-free builds and
standard SQLite behavior.

Encrypted:

- Document body JSON.
- Folder names inside encrypted Journals.
- Document names inside encrypted Journals.
- Any future user-authored fields inside encrypted Journals.

Plaintext:

- Top-level Journal names.
- SQLite schema, table names, and indexes.
- Item IDs, parent IDs, kinds, sort order, system keys, and timestamps.
- Row counts and hierarchy shape.
- Plaintext Journal content.
- App-level non-secret settings.

The UI and documentation should describe this as Journal-level content
encryption, not whole-database encryption.

## Key Model

Use one master password to protect all encrypted Journals, but do not encrypt
content directly with the password-derived key.

Recommended hierarchy:

```text
master password
  -> Argon2id or scrypt KDF with installation salt
  -> master key
  -> wraps random per-Journal data keys
  -> per-Journal data keys encrypt fields for that Journal
```

Benefits:

- Changing the master password only re-wraps Journal data keys.
- Encrypting or decrypting one Journal does not require rewriting other
  encrypted Journals.
- Future export/import or sharing can be modeled around a Journal data key.

Store key metadata in SQLite because it is not useful without the password:

```sql
CREATE TABLE encryption_master (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  kdf TEXT NOT NULL,
  kdf_params_json TEXT NOT NULL,
  salt BLOB NOT NULL,
  verifier_nonce BLOB NOT NULL,
  verifier_ciphertext BLOB NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE journal_encryption_keys (
  key_id TEXT PRIMARY KEY,
  journal_id TEXT NOT NULL UNIQUE REFERENCES items(id) ON DELETE CASCADE,
  wrapped_key_nonce BLOB NOT NULL,
  wrapped_key_ciphertext BLOB NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
```

The verifier should be an authenticated encrypted fixed payload, not a password
hash used directly as an encryption key. A successful verifier decrypt proves
that the user entered the right password-derived master key.

Keep the derived master key and unwrapped Journal keys in memory only. Clear
them on app shutdown and when the user explicitly locks the app if that feature
is added later.

When the final encrypted Journal is turned back into plaintext, keep the master
password metadata by default. This keeps the next encryption request simple and
avoids adding a password-reset concept. A future "remove encryption setup" action
could delete `encryption_master`, but it should be separate from decrypting a
Journal.

## Encryption Format

Use authenticated encryption with unique random nonces per field write.
Preferred options:

- `XChaCha20-Poly1305` from `golang.org/x/crypto/chacha20poly1305`.
- `AES-256-GCM` from the standard library if avoiding another dependency is
  more important than nonce misuse resistance.

Use associated data to bind ciphertext to context:

```text
journal:v1:<table>:<row-id>:<column-name>:<key-id>
```

This prevents ciphertext copied from one row or column from decrypting as valid
content somewhere else.

## Schema Changes

Add explicit ciphertext columns instead of overloading plaintext columns with
JSON envelopes. This keeps queries and migrations clearer.

```sql
ALTER TABLE items ADD COLUMN encryption_state TEXT NOT NULL DEFAULT 'plaintext'
  CHECK (encryption_state IN ('plaintext', 'encrypted'));
ALTER TABLE items ADD COLUMN encryption_key_id TEXT NULL;
ALTER TABLE items ADD COLUMN title_ciphertext BLOB NULL;

ALTER TABLE documents ADD COLUMN content_ciphertext BLOB NULL;
```

Rules:

- Top-level Journal rows keep `title` plaintext even when
  `encryption_state = 'encrypted'`.
- The encrypted Journal root is the security boundary. Descendants should also
  be marked `encryption_state = 'encrypted'` for simple filtering and integrity
  checks, but the root Journal key row remains the source of truth.
- Descendant folder and document rows in encrypted Journals store display names
  in `title_ciphertext`; their `title` should hold a non-sensitive placeholder.
- Encrypted document rows store body JSON in `content_ciphertext`; their
  `content_json` should hold a harmless placeholder document.
- `encryption_key_id` is set on encrypted Journal roots and may also be copied
  to descendants for easier reads. The Journal root remains the source of truth.

Use helper functions rather than ad hoc encryption calls throughout the service:

- `journalEncryptionState(tx, journalID)`
- `encryptItemTitle(key, itemID, title)`
- `decryptItemTitle(key, itemID, ciphertext)`
- `encryptDocumentContent(key, itemID, content)`
- `decryptDocumentContent(key, itemID, ciphertext)`
- `syncSearchForItem(...)` that refuses to index encrypted descendants.

## Search Behavior

Persistent search only indexes plaintext Journals.

When a Journal is encrypted:

- Delete all descendant rows from `library_search_fts`.
- Prevent future `syncFTS` calls from indexing its descendants.
- Global search omits the encrypted Journal and all descendants.

When a Journal is decrypted:

- Rebuild FTS rows for the plaintext replacement Journal.

Even after the user unlocks encrypted Journals, do not write decrypted titles or
bodies into `library_search_fts`. If encrypted search is added later, build it as
an in-memory session-only index.

## Backend API Plan

Add APIs around the existing Wails app surface:

```go
type EncryptionStatusResponse struct {
    MasterPasswordConfigured bool
    Unlocked bool
    EncryptedJournalIDs []string
}

func (a *App) GetEncryptionStatus() (EncryptionStatusResponse, error)
func (a *App) CreateMasterPassword(password string) error
func (a *App) UnlockEncryption(password string) error
func (a *App) ChangeMasterPassword(currentPassword, newPassword string) error
func (a *App) EncryptJournal(journalID string) (TreeResponse, error)
func (a *App) DecryptJournal(journalID string) (TreeResponse, error)
```

`EncryptJournal` should return a typed error if no master password exists so the
frontend can show the create-password dialog.

The service should reject content reads and writes for encrypted descendants
when the corresponding key is not unlocked.

The first encryption request should not create the master password and encrypt
the Journal in the same blind call. Use an explicit two-step flow:

1. User requests `Encrypt Journal`.
2. Backend reports that no master password exists.
3. Frontend shows create-password and confirmation fields.
4. Backend creates and unlocks the master key.
5. Frontend retries or continues with `EncryptJournal`.

This gives the UI a clear place to handle cancellation and password validation.

## Frontend Plan

Outliner:

- Render encrypted Journal roots with a lock icon or locked visual treatment.
- Keep the plaintext Journal name visible.
- If locked, clicking the Journal prompts for the master password.
- After unlock, children and document contents are displayed normally.
- If locked, either hide descendants or show non-sensitive placeholders. Hiding
  descendants is simpler and leaks less metadata through the UI, although the DB
  still contains hierarchy metadata.

Settings:

- Add `Change Master Password`.
- Require current password first.
- Disable or hide the action until a master password exists.

Journal actions:

- Add `Encrypt Journal`.
- Add `Turn Off Encryption` for encrypted Journals.
- Confirmation copy should explain that search will be disabled while encrypted
  and restored if encryption is turned off.

Search:

- Search results should naturally omit encrypted Journals because backend FTS
  omits them.
- Consider showing a small non-result message such as "Encrypted Journals are
  excluded from search" only if the search UI has room and the user has encrypted
  Journals. Avoid noisy persistent warnings.

## Encrypt Journal Flow

Preconditions:

- Target item exists and is a top-level Journal.
- Target Journal is not already encrypted.
- Target Journal is not the system Trash and is not inside Trash.
- Autosave is flushed.
- No pending draft writes are allowed during migration.
- Master key is available in memory, prompting if needed.

Steps:

1. Start an immediate transaction.
2. Generate a random per-Journal data key.
3. Wrap the data key with the current master key.
4. Fetch the Journal root and all descendants.
5. For every descendant folder/document item, encrypt `title` into
   `title_ciphertext` and replace `title` with a placeholder.
6. For every descendant document, encrypt `content_json` into
   `content_ciphertext` and replace `content_json` with an empty placeholder doc.
7. Mark descendants `encryption_state = 'encrypted'` and set
   `encryption_key_id`.
8. Delete FTS rows for the Journal root and all descendants.
9. Insert `journal_encryption_keys`.
10. Mark the Journal root `encryption_state = 'encrypted'` and set
   `encryption_key_id`.
11. Commit.
12. Optionally checkpoint WAL and run `VACUUM` after commit to reduce plaintext
   remnants in the active database file. This cannot be part of the transaction.
13. Refresh the tree.

Do not mark the Journal encrypted until all encrypted values have been written.
If any step fails, roll back the transaction and leave the Journal plaintext.

## Open Encrypted Journal Flow

1. User clicks an encrypted Journal.
2. If the master key is not in memory, show the unlock dialog.
3. Derive the master key and decrypt the verifier.
4. Unwrap the Journal data key.
5. Load descendants and decrypt titles/content as needed.
6. Keep the unwrapped Journal key in memory for the session.

The password prompt should happen once per session. After a successful unlock,
other encrypted Journals can be opened without another password prompt.

## Change Master Password Flow

Changing the master password should not rewrite Journal content.

Steps:

1. Ask for current password and new password.
2. Derive current master key and verify it.
3. Derive new master key with a new salt and KDF params.
4. Start a transaction.
5. For each encrypted Journal, decrypt its wrapped data key with the old master
   key and wrap it with the new master key.
6. Replace `encryption_master` with the new salt, params, and verifier.
7. Commit.
8. Clear the in-memory master key and all unwrapped Journal data keys.
9. Mark encrypted Journals as locked in the frontend state.
10. If the currently open document belongs to an encrypted Journal, close it,
    clear the selected document, and show the first-time page as if no document
    was opened.

If any Journal key cannot be unwrapped, abort before writing the new master
metadata. This protects against making some encrypted Journals inaccessible.

After a successful password change, do not keep encrypted Journals unlocked with
the new in-memory key. Require the user to unlock again with the new master
password before opening encrypted Journal contents.

## Turn Off Encryption Flow

Preferred approach: create a plaintext replacement Journal, verify it, then
remove the encrypted Journal.

Steps:

1. Require the master key and the target Journal data key.
2. Flush autosave and block edits for that Journal.
3. Start a transaction.
4. Create a new plaintext Journal with a temporary title and equivalent sort
   position.
5. Recursively copy folders/documents, decrypting titles and content into the
   normal plaintext columns.
6. Rebuild FTS rows for the copied plaintext items.
7. Verify copied item/document counts match the source.
8. Rename the encrypted source Journal to a temporary retired title.
9. Rename the plaintext copy to the original Journal name.
10. Delete the encrypted source Journal and its `journal_encryption_keys` row.
11. Commit.
12. Optionally checkpoint WAL and run `VACUUM` after commit to reduce encrypted
    and older plaintext remnants from deleted rows in the active database file.
13. Refresh the tree.

This approach avoids rewriting encrypted rows in place and gives the transaction
a clear source and destination.

Alternative approach: decrypt in place. It is less code and preserves item IDs,
but a bug can leave mixed plaintext/ciphertext rows under a Journal that now
claims to be plaintext. If this approach is used, keep the whole operation in one
transaction and only clear `encryption_state` after every row has been converted
and FTS has been rebuilt.

Tradeoff:

- Copy replacement is safer for data shape and rollback, but changes item IDs.
  That can affect last-opened document, expanded tree state, selection state, and
  any future links between documents.
- In-place decrypt preserves IDs, but requires stronger invariants and recovery
  checks.

Given Journal currently relies heavily on item IDs for selection and last-opened
state, the copy replacement should include an old-to-new ID map and update
settings like `last_document_id`. If future document links are added, they will
need the same remap.

## Autosave And Editing

Encryption migration must coordinate with the autosave worker.

Requirements:

- `FlushAll` before encrypting or decrypting a Journal.
- Per-Journal migration lock to reject or queue draft updates during migration.
- Open documents inside a migrating Journal should be closed, made read-only, or
  blocked from saving until migration finishes.
- Pending drafts must be keyed by current item ID. If decrypting via copy
  replacement, pending drafts for old IDs must be flushed before copy and then
  discarded or remapped after success.

## Integrity And Recovery

Add validation helpers:

- Encrypted Journal roots must have a matching key row.
- Plaintext Journals must not have encrypted descendants.
- Encrypted descendants must have ciphertext for sensitive fields.
- Encrypted descendants must not have persistent FTS rows.
- Placeholder plaintext values for encrypted descendants must be valid but
  non-sensitive.

Run these checks:

- After encrypting a Journal.
- After turning off encryption.
- In tests.
- Optionally at startup with repair recommendations, not automatic destructive
  repair.

## Edge Cases

- Lost password means encrypted Journal contents are unrecoverable.
- Empty password should be rejected.
- Password confirmation should be required on create and change.
- Changing the master password locks all encrypted Journals, even if they were
  already unlocked. If the open document is inside an encrypted Journal, close
  it and show the first-time page as if no document was opened. The next attempt
  to open an encrypted Journal should prompt for the new master password.
- Deleting an encrypted Journal should delete wrapped key metadata.
- Moving items between encrypted and plaintext Journals is unsupported. This
  includes moving encrypted items into plaintext Journals and moving plaintext
  items into encrypted Journals. Users should use explicit copy/export/decrypt
  workflows instead of drag/drop moves across encryption boundaries.
- Duplicating an encrypted Journal should require unlock and should create a new
  per-Journal data key for the copy.
- Creating a document/folder inside an encrypted Journal should immediately store
  encrypted title/content placeholders.
- Renaming the encrypted Journal root remains plaintext.
- Renaming encrypted descendants requires the Journal key.
- Trash behavior needs a clear boundary. Recommended: items moved from encrypted
  Journals remain encrypted in Trash and are hidden from search. Restoring across
  encryption boundaries should be blocked initially.
- Existing plaintext FTS rows must be purged during encryption, otherwise search
  leaks old titles and body tokens.
- Old plaintext bytes can remain in SQLite free pages, WAL files, backups, Time
  Machine, or filesystem snapshots after encryption. To reduce local leakage,
  checkpoint WAL and run `VACUUM` after successful encryption, but document that
  prior backups may still contain plaintext.
- Export/import should state whether encrypted Journals are exported encrypted or
  decrypted. Do not silently export decrypted content without an explicit action.
- Stress databases and tests need generation paths for plaintext and encrypted
  Journals.

## Testing Plan

Backend tests:

- Create master password and unlock succeeds/fails correctly.
- Encrypt one Journal while another remains plaintext.
- Plaintext Journal search still works.
- Encrypted Journal does not appear in search before or after unlock.
- SQLite browser-style queries cannot read encrypted descendant titles/content
  from plaintext columns.
- Opening encrypted Journal decrypts titles/content after unlock.
- Change master password preserves access and rejects the old password.
- Turn off encryption restores plaintext content and FTS search.
- Migration rollback leaves the source Journal unchanged on injected errors.
- Moving, deleting, duplicating, and Trash operations obey encryption boundary
  rules.

Frontend tests/manual QA:

- Lock icon treatment is visible and not confused with Trash or folder icons.
- Password dialogs handle cancel, wrong password, and retry.
- First encryption request creates the master password.
- Unlock prompt appears once per session.
- Settings password change flow handles success and error states.
- Search UI remains understandable when encrypted Journals are omitted.

## Suggested Implementation Order

1. Add schema migrations and encryption metadata types with no UI changes.
2. Add crypto helpers and unit tests for KDF, verifier, wrapping, and field
   encryption.
3. Add service-level encryption state detection and unlock APIs.
4. Add FTS exclusion rules for encrypted Journal descendants.
5. Implement encrypt-Journal migration.
6. Update frontend outliner rendering and unlock/create-password dialogs.
7. Implement change-master-password.
8. Implement turn-off-encryption, preferably with copy replacement and ID remap.
9. Add boundary rules for move, Trash, duplicate, create, rename, and delete.
10. Add migration integrity checks and broader workflow tests.
