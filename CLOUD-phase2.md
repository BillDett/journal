# Cloud Phase 2: Portable encryption, snapshots, and attachment metadata

## Objective

Make a cloud cache portable as a complete, verified Journal snapshot. A future
device must be able to restore encrypted or unencrypted content from a snapshot
and the Journal master password alone.

This phase does not contact a remote provider; it creates the content and file
primitives that Phase 3 will publish.

## Scope

- Portable per-cloud-Journal encryption metadata and key wrapping.
- Consistent SQLite snapshot creation and validation.
- Attachment digest metadata, staging, and local blob-cache lifecycle.
- Snapshot manifest data models and digest verification helpers.

## Prerequisites

- Phase 1 store routing and content/install schema separation are complete.
- Existing local encryption tests are green before changes begin.

## Work breakdown

### 1. Define portable cloud encryption records

Create a content-schema migration for per-Journal portable encryption metadata.
It must contain:

- cloud Journal ID and encryption format version;
- KDF algorithm and validated parameters;
- salt;
- Journal data-key wrapping nonce and ciphertext;
- a password-verifier or authenticated encrypted sentinel;
- creation/update timestamps.

Rules:

- Generate a random data key per encrypted cloud Journal.
- Derive a wrapping key from the master password; do not reuse installation
  master-key records.
- Encrypt titles, document content, and attachments with the data key.
- Zero or discard sensitive byte buffers where practical.
- Reject cloud encryption when portable metadata is absent or malformed.

### 2. Implement portable unlock and rewrap flows

- `UnlockCloudJournal(storeID, password)` reads only cache content metadata.
- Validate the password against the portable verifier before opening content.
- Load the data key into the cloud-store session only after validation.
- `ChangeCloudJournalMasterPassword` rewraps the existing data key; it must not
  re-encrypt every document or attachment.
- Mark the cache dirty after a successful rewrap so Phase 3 publishes a new
  revision.
- Ensure a local installation master-password change does not silently alter a
  cloud Journal's portable wrapping record.
- Until this portable model and its new-device recovery tests are complete,
  make cloud-encryption create, enable/convert, and password-change commands
  return `portable_encryption_unavailable` at the backend boundary. A disabled
  frontend control alone is not sufficient protection.

### 3. Build consistent SQLite snapshot support

Add `SnapshotContentDatabase(source, stagingPath)`:

1. Flush pending document drafts.
2. Use SQLite backup APIs or a checkpointed consistent read transaction.
3. Write to a new staging file; never copy the live database file directly.
4. Open the staging file read-only and run schema/scope/integrity checks.
5. Calculate byte size and SHA-256 from the exact staged bytes.
6. Return an immutable `DatabaseDescriptor`.

Define cleanup rules for staging files on cancellation, validation failure, and
successful publication handoff.

### 4. Add attachment descriptors and digest identity

Extend cache attachment metadata so every remotely publishable attachment can
produce:

```go
type AttachmentDescriptor struct {
    Digest   string // sha256:<hex> of stored bytes
    Size     int64
    MimeType string
    Key      string // assigned by the Vault layer later
}
```

- Digest the stored bytes, not a rendered data URL.
- Define exactly whether encrypted attachments are digested before or after
  encryption; use the stored remote bytes consistently.
- Backfill digest metadata lazily or in a migration job; do not require loading
  all blobs into memory.
- Retain current local attachment behavior for local Journals.

### 5. Implement local attachment blob cache

Create a cache blob manager under:

```text
cloud-cache/<journal-id>/blobs/sha256/<hex>
```

- Write downloads to a temp path, hash/size verify, then atomically rename.
- Never trust filename or MIME type as identity.
- Track last access and size in local convenience metadata only.
- Provide `EnsureAttachmentLocal` for the editor and `ReadAttachment` for data
  URL generation.
- Do not implement remote attachment deletion in this phase.

### 6. Define portable snapshot structures

Implement codecs and strict validators for the database and attachment portions
of the future revision manifest. Validation must reject missing fields, bad
SHA-256 syntax, duplicate attachment digests, oversized values, wrong Journal
ID, and unknown required format versions.

## Tests

- A fresh cache plus matching password unlocks on a second installation DB.
- A wrong password fails without leaking a usable data key.
- Rewrap changes only portable key wrapping and remains decryptable with the
  new password.
- Cloud-encryption create/convert/password-change commands are rejected before
  portable metadata and recovery support are available.
- Snapshot database opens, passes integrity checks, and has stable digest/size.
- Snapshot failure never modifies the live cache.
- Encrypted and plaintext attachment descriptors use the documented byte form.
- Blob cache rejects digest mismatch, partial download, and path traversal.
- Blob cache works after application restart without network access.

## Completion criteria

- A cache snapshot, attachment descriptors, and portable encryption metadata
  are sufficient inputs for remote publication.
- New-device encrypted recovery is proven using a copied snapshot and matching
  password, with no original local app database.
- Snapshot and blob-cache APIs are deterministic, validated, and cancellable.
