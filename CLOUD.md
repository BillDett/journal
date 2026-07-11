# Cloud Journals: Journal Vault Protocol

## 1. Purpose and product contract

Journal Vault adds cloud-managed Journals without changing the simple local
Journal experience. A Vault is a remote, versioned store for one Journal. It
is a publication and recovery system, not a live remote database.

### Core requirements

- A user can create selected Journals in remote storage.
- A cloud Journal can be opened and edited on any configured device.
- Simultaneous editing is not required. The first implementation permits one
  write-capable device at a time.
- The app edits a local cached SQLite database; it never opens SQLite directly
  from a cloud-synchronized path or remote mount.
- A cloud Journal remains recoverable when every local device and every local
  `journal.db` file has been lost.
- Each successful publication creates an immutable, independently recoverable
  revision. A failed publication must not replace the last known-good revision.
- Local-only users continue to use one SQLite database and do not need a vault,
  a blob store, or provider configuration.

### Non-goals for the first implementation

- Real-time collaboration or multi-writer merge.
- Editing a cached Journal after its lease has expired or been lost.
- Running SQLite in Dropbox, iCloud Drive, OneDrive, Google Drive, Syncthing,
  or any other synchronized directory.
- A Journal-operated hosted service.
- Conversion between local and cloud storage after Journal creation.
- Arbitrary provider plugins. The first release supports only providers that
  satisfy the required Vault capability profile.

### Product decisions

- Journal creation requires a choice of `local` or `cloud`; the choice is
  immutable.
- A cloud Journal belongs to exactly one configured Vault provider and Vault
  root.
- A cloud Journal has one local cache database per device.
- A cloud Journal can be visible while unavailable, locked, offline, or
  read-only. Its UI state must explain why it cannot be edited.
- Local autosave and remote publication are separate operations with separate
  status indicators.
- Journal encryption is supported for cloud Journals only after portable
  per-Journal encryption records and new-device recovery are implemented.

## 2. Terminology

- **Local app database**: installation-level SQLite database at
  `<user-config-dir>/Journal/journal.db`.
- **Local Journal**: a Journal managed entirely in the local app database.
- **Cloud Journal**: a Journal stored remotely as a Journal Vault and edited
  through a local cache database.
- **Vault provider**: configured credentials plus a storage endpoint and root
  prefix. It may be backed by object storage, WebDAV, SFTP, or a compatible
  HTTPS endpoint.
- **Vault root**: the provider-owned prefix containing Journal Vaults.
- **Vault object**: a remotely addressed immutable blob or mutable control
  document.
- **Revision**: one immutable published Journal snapshot.
- **Current pointer**: the conditional mutable control document identifying the
  latest accepted revision.
- **Lease**: a short-lived advisory control document granting one device write
  access to a cloud Journal.
- **Mount**: local installation metadata describing a discovered cloud Journal
  and its cache.

## 3. Architectural rules

1. The current pointer, not a local cache, is the remote source of truth.
2. The app writes a complete immutable revision before updating the current
   pointer.
3. A provider must support conditional updates for control documents before it
   is accepted for cloud Journal creation.
4. The app must validate a provider before creating local cache state or remote
   objects.
5. Journal content is portable; installation settings and credentials are not.
6. Every remotely stored object is verified by size and SHA-256 digest before it
   is accepted into a cache or treated as published.
7. Attachments are stored as content-addressed blobs, not repeatedly embedded
   in every cloud database snapshot.

## 4. Storage topology

The local-only topology remains unchanged:

```text
<user-config-dir>/Journal/
  journal.db                 # local Journals and installation state
```

Cloud support adds independent caches:

```text
<user-config-dir>/Journal/
  journal.db
  cloud-cache/
    <cloud-journal-id>/
      journal.db             # local editable cache
      vault-state.json       # non-authoritative local convenience state
      blobs/sha256/<digest>  # optional local attachment cache
```

Each cloud cache database contains exactly one cloud Journal's portable content
schema. It is a full journal-content database, not a reduced format.

### Schema ownership

**Portable journal-content schema** is shared by local Journals and cloud
caches:

- `items`
- `documents`
- `document_attachments` metadata
- portable Journal encryption tables
- optional/disposable `library_search_fts`
- `cloud_journal_metadata`

**Installation schema** exists only in the local app database:

- provider configuration and credential references
- cloud mount records
- cache locations and local sync state
- UI preferences and last-opened convenience state

Credentials, local cache paths, device IDs, provider defaults, and UI state
must never be included in a remote revision.

## 5. Journal Vault object model

Every cloud Journal has a deterministic object prefix. Object keys shown below
are logical keys; a provider maps them to its own API and namespace.

```text
<vault-root>/journals/<cloud-journal-id>/
  current.json
  lease.json
  revisions/<revision-id>/manifest.json
  revisions/<revision-id>/journal.db
  blobs/sha256/<digest>
```

`revision-id` is a UUIDv7 or equivalent time-sortable random identifier. It is
never reused. Object keys are immutable except `current.json` and `lease.json`.

### 5.1 Current pointer

`current.json` is the only mutable Journal content control object. Its provider
version token (ETag, generation, revision ID, or equivalent) is used for
compare-and-swap updates.

```json
{
  "format": "journal-vault-current",
  "formatVersion": 1,
  "cloudJournalId": "uuid",
  "revisionId": "uuidv7",
  "revisionNumber": 42,
  "revisionManifest": {
    "key": "revisions/<revision-id>/manifest.json",
    "sha256": "hex",
    "size": 1234
  },
  "updatedAt": "2026-07-10T12:00:00Z",
  "previousRevisionId": "uuidv7",
  "portableEncryption": {
    "enabled": true,
    "metadataVersion": 1
  }
}
```

The first revision omits `previousRevisionId`. A current pointer update must be
conditional on the token observed when the writer acquired its lease or last
successfully synchronized.

### 5.2 Revision manifest

Each revision has an immutable JSON manifest.

```json
{
  "format": "journal-vault-revision",
  "formatVersion": 1,
  "cloudJournalId": "uuid",
  "revisionId": "uuidv7",
  "revisionNumber": 42,
  "parentRevisionId": "uuidv7",
  "createdAt": "2026-07-10T12:00:00Z",
  "createdByDeviceId": "uuid",
  "database": {
    "key": "revisions/<revision-id>/journal.db",
    "sha256": "hex",
    "size": 1048576
  },
  "attachments": [
    {
      "digest": "sha256:hex",
      "key": "blobs/sha256/<hex>",
      "size": 52341,
      "mimeType": "image/png"
    }
  ],
  "encryption": {
    "enabled": true,
    "metadataVersion": 1
  }
}
```

The manifest references all live attachment blobs needed to open the revision.
It may contain an optional title hint only when that hint is permitted by the
Journal's encryption state.

### 5.3 Attachment blobs

Attachment objects use SHA-256 of their stored bytes. Uploading an attachment
is idempotent: if an object at the expected key already matches the digest and
size, the client reuses it. The cache may fetch blobs lazily when the editor
renders an attachment.

### 5.4 Lease document

`lease.json` is a small mutable advisory lock. It is not a replacement for
conditional current-pointer updates.

```json
{
  "format": "journal-vault-lease",
  "formatVersion": 1,
  "cloudJournalId": "uuid",
  "leaseId": "uuid",
  "ownerDeviceId": "uuid",
  "ownerLabel": "Bill's MacBook",
  "acquiredAt": "2026-07-10T12:00:00Z",
  "expiresAt": "2026-07-10T12:05:00Z",
  "currentPointerToken": "provider-version-token"
}
```

Lease duration defaults to five minutes. Renewal occurs at one-third of the
duration, with jitter. A device that cannot renew before expiry must stop
cloud writes, preserve its cache, and switch the Journal to read-only or
explicit sync-risk state.

## 6. Provider capability profile

Journal Vault uses a narrow provider interface. A provider is eligible only if
it supports every required operation.

```go
type VaultStore interface {
    Validate(ctx context.Context, provider VaultProvider) (VaultCapabilities, error)

    GetObject(ctx context.Context, provider VaultProvider, key string) (Reader, ObjectMeta, error)
    PutImmutable(ctx context.Context, provider VaultProvider, key string, source io.Reader, expected ObjectDigest) (ObjectMeta, error)
    HeadObject(ctx context.Context, provider VaultProvider, key string) (ObjectMeta, error)

    GetControl(ctx context.Context, provider VaultProvider, key string) ([]byte, ControlToken, error)
    PutControlIf(ctx context.Context, provider VaultProvider, key string, value []byte, expected ControlToken) (ControlToken, error)
    CreateControlIfAbsent(ctx context.Context, provider VaultProvider, key string, value []byte) (ControlToken, error)
}
```

Required capabilities:

- authenticated read, write, and metadata lookup under a configured prefix;
- immutable object creation or equivalent collision detection;
- conditional replacement of a small control object using an opaque token;
- conditional create of a missing control object;
- strong enough read-after-write consistency to read an object immediately
  after a successful write;
- stable object identity/version token for `current.json` and `lease.json`.

Optional capabilities:

- prefix listing, used for discovery and retention cleanup;
- version-aware immutable object deletion, used only for revision retention and
  explicitly confirmed attachment reclamation;
- provider-native version history;
- server-side encryption and lifecycle policies.

Providers that support maintenance expose a separate optional interface. A
provider remains eligible for normal cloud Journal operation without it.

```go
type VaultMaintenanceStore interface {
    ListPrefix(ctx context.Context, provider VaultProvider, prefix string) ([]ObjectMeta, error)
    DeleteImmutableIfVersion(ctx context.Context, provider VaultProvider, key string, expected ObjectVersion) error
}
```

`DeleteImmutableIfVersion` must fail when the object version no longer matches
the version returned by `ListPrefix` or `HeadObject`. A provider without a
complete listing and version-aware deletion supports preview-only maintenance;
the app must disable deletion.

The application must not claim that a generic file transfer endpoint is a
valid provider until this capability check passes. S3-compatible storage and
WebDAV implementations with ETag/`If-Match` are expected first targets. An
SFTP implementation is allowed only if it can provide a safe conditional
control-update mechanism; atomic rename alone is not sufficient for a general
multi-device compare-and-swap guarantee.

## 7. Local installation records

The local app database stores providers and mounts.

```sql
CREATE TABLE vault_providers (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,                 -- "s3", "webdav", or future kind
  endpoint TEXT NOT NULL,
  root_prefix TEXT NOT NULL,
  credential_ref TEXT NOT NULL,
  publish_debounce_ms INTEGER NOT NULL DEFAULT 30000,
  publish_max_interval_ms INTEGER NOT NULL DEFAULT 300000,
  revision_retention_count INTEGER NOT NULL DEFAULT 50,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE cloud_journal_mounts (
  cloud_journal_id TEXT PRIMARY KEY,
  provider_id TEXT NOT NULL,
  vault_root TEXT NOT NULL,
  cache_path TEXT NOT NULL,
  last_revision_id TEXT NOT NULL DEFAULT '',
  last_current_token TEXT NOT NULL DEFAULT '',
  lease_id TEXT NOT NULL DEFAULT '',
  revision_retention_count INTEGER NOT NULL DEFAULT 0,
  sync_status TEXT NOT NULL DEFAULT 'clean',
  last_sync_error TEXT NOT NULL DEFAULT '',
  last_synced_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
```

Provider removal removes only local configuration. It must never delete remote
Vault contents or silently remove a cache containing unsynced work.

## 8. Database and routing requirements

The existing Journal content service must operate against an explicit store,
not a global implicit database. Every backend operation identifies:

```text
store ID + item/document ID
```

Suggested transport IDs:

```text
local:<item-id>
cloud:<cloud-journal-id>:<item-id>
```

These are routing identifiers only; the underlying database continues to use
ordinary item UUIDs. A cloud cache must reject accesses outside its one cloud
Journal root and its system Trash.

`GetLibraryTree` composes local Journals with mounted cloud Journals. Cloud
tree entries include:

```go
StorageKind string // "local" or "cloud"
CloudStatus string // clean, dirty, syncing, offline, locked, conflict, error
ReadOnly    bool
```

Search runs separately against the local database and each available cloud
cache, then merges results. Encrypted Journals follow existing search rules;
the first cloud version may omit encrypted content from search.

## 9. Cloud Journal lifecycle

### 9.1 Provider validation

Before cloud creation or reconnection, the app validates credentials, root
access, immutable upload behavior, conditional control writes, and immediate
read-back. Failed validation creates no mount and no remote Journal state.

### 9.2 Create cloud Journal

1. User selects Cloud and a validated provider.
2. App creates a local pending-create record.
3. App generates `cloudJournalId`, device ID, and an empty cache database with
   the portable Journal content schema.
4. App acquires the initial lease by conditionally creating `lease.json`.
5. App publishes revision `0001` using the publication protocol.
6. App writes the local mount record only after the current pointer has been
   successfully published and verified.
7. App opens the cache read-write while its lease remains valid.

If any remote step fails, the local pending-create record retains the cache
path and recovery details. The UI offers retry or discard; discard must never
delete a remote object unless it can prove the object belongs exclusively to
that incomplete creation.

### 9.3 Open a mounted Journal

1. Read and validate `current.json`.
2. If the cache is missing or behind the current revision, download the
   revision manifest and database snapshot into a staging directory.
3. Verify every downloaded object before atomically replacing the active cache.
4. Acquire or inspect the lease. Open read-write only with a valid local lease;
   otherwise open read-only when a cache is available.
5. Record the observed current-pointer token and revision ID locally.

### 9.4 New-device recovery

Recovery requires only provider credentials, the vault root, the cloud Journal
ID (or optional provider discovery), and the Journal master password for an
encrypted Journal.

1. Configure and validate the provider.
2. Enter a cloud Journal ID or discover `current.json` objects when listing is
   available.
3. Resolve and validate the current pointer and revision manifest.
4. Download the database snapshot and required metadata into a staging cache.
5. Verify digests, replace the active cache atomically, and create a mount.
6. Open read-only or acquire a lease for editing.

No local app database from the original device is required.

## 10. Publication protocol

Publishing coalesces local autosaves. It must not run after every keystroke.
The default debounce is 30 seconds; the default maximum dirty interval is five
minutes. Manual **Sync now** bypasses debounce but not lease or conflict checks.

### 10.1 Publish one revision

1. Confirm the mount is writable and the lease belongs to this device.
2. Confirm the lease has not expired and renew it when necessary.
3. Read `current.json` and verify its token still equals the mount's observed
   token. If not, enter conflict state before uploading a new current pointer.
4. Flush all pending document drafts into the cache database.
5. Create a consistent SQLite snapshot in a staging directory using SQLite
   backup/checkpoint APIs; never upload the actively edited database file.
6. Enumerate referenced attachments and calculate stored-byte digests.
7. Upload missing immutable attachment blobs, verifying each result.
8. Upload the staged database snapshot to its immutable revision key.
9. Upload the immutable revision manifest.
10. Read back and validate the manifest and database metadata.
11. Conditionally replace `current.json` with the new revision, using the token
    read in step 3.
12. If the conditional update succeeds, mark the mount clean and persist the
    new revision ID/token. If it fails, preserve the local cache and enter
    conflict state; do not delete uploaded immutable objects.

### 10.2 Failure behavior

- Network, authentication, rate-limit, or provider failures leave local cache
  edits intact and mark the mount dirty/error.
- A successful immutable upload without a current-pointer update is harmless:
  it is an unreachable candidate revision, not published Journal state.
- The last pointer-confirmed revision remains recoverable after every failure.
- Publication retry uses bounded exponential backoff with user-visible status.

### 10.3 Conflict behavior

A conflict occurs when the current pointer changed after this device last
synchronized, a lease cannot be confirmed, or a conditional pointer update
fails. Journal does not merge SQLite databases.

The app must:

1. preserve the local cache as an unsynced recovery copy;
2. stop automatic publishing;
3. display the remote revision and local base revision;
4. offer: discard local cache and pull remote, keep local cache as a new cloud
   Journal, or export the local cache; and
5. require explicit user action before resuming writes.

## 11. Lease protocol

### Acquire

1. Read `lease.json` if present.
2. If absent, conditionally create it.
3. If present but expired, conditionally replace it using its observed token.
4. If present and unexpired for another device, open read-only and display
   owner/expiry when available.
5. Read `current.json` and store its token in the acquired lease and mount.

### Renew

Renew only when the stored lease ID and owner device ID match. Use the lease
control token as the conditional precondition. On repeated renewal failure,
block new writes, leave drafts in the cache, and notify the user before expiry.

### Release

Release is best effort. Prefer conditionally replacing the lease with an
already-expired lease owned by the releasing device. Deletion is optional and
must not be required for correctness.

### Force unlock

Force unlock is available only after the displayed expiry plus a safety grace
period. It conditionally replaces the expired lease and warns that another
device may hold unsynced local work.

## 12. Encryption portability

Cloud encryption must be per-Journal and self-contained in portable Journal
content and/or the revision manifest. It must not depend on installation-level
master-key records from the creator's local app database.

Required model:

- Each encrypted cloud Journal has a random data key.
- The data key is wrapped with a key derived from the Journal master password.
- KDF parameters, salt, wrapped-key nonce, and wrapped key ciphertext are
  portable Journal metadata.
- Titles, document content, and attachment payloads follow the existing
  encrypted-at-rest model, using the cloud Journal data key.
- A new device can decrypt using: Vault revision + matching master password.
- Changing a master password requires an explicit cloud-Journal key rewrap and
  publication of a new revision.

Until this model and recovery tests exist, cloud encryption creation and
encryption conversion actions must be disabled and rejected by the backend.

## 13. Bounded revision retention

Journal Vault retains a bounded number of complete, pointer-confirmed revisions
for each Journal. Retention runs independently for each Journal prefix.

`vault_providers.revision_retention_count` is the default. A positive
`cloud_journal_mounts.revision_retention_count` overrides it for one Journal.
The effective value must be at least two and defaults to 50.

The retained set always includes the revision named by `current.json` and the
newest `N - 1` prior pointer-confirmed revisions. A current revision is never
eligible for deletion, even if it is years old.

### 13.1 Cleanup after publication

After a new revision has been uploaded, verified, and made current by a
successful conditional update of `current.json`, the publishing device may
perform best-effort cleanup for that one Journal:

1. Re-read `current.json` and retain its revision ID and control token.
2. List that Journal's revision manifests and order them by monotonic
   `revisionNumber`, not client timestamps.
3. Keep the current revision and the newest N pointer-confirmed revisions.
4. Before each deletion batch, re-read `current.json`. If its token changed,
   stop cleanup without deleting additional objects.
5. Delete only the manifest and SQLite snapshot of revisions outside the
   retained set.
6. Record any deletion failure locally and retry after a later successful
   publication or an explicit **Clean old revisions** action.

Cleanup is never required for a successful publication. If listing or deletion
fails, the result is excess storage, not lost current Journal data. Duplicate
deletion attempts from two devices are harmless when provider deletion is
idempotent.

### 13.2 Attachment and orphan policy

Attachment blobs are append-only in the first implementation. Journal does not
automatically delete attachment objects, including attachments referenced only
by expired revisions or candidate revisions that never became current.

An interrupted publish can therefore leave an unreachable revision manifest,
snapshot, or attachment blob. The next successful cleanup may remove the old
manifest/snapshot only when it is outside the retained revision count.
Unreachable attachment blobs remain by design. This trades storage leakage for
the much safer rule that an attachment can never be removed by a mistaken
reachability calculation.

### 13.3 Manual attachment reclamation

Journal may later offer **Vault maintenance → Preview reclaimable attachments**
as an explicit, per-Journal maintenance operation. It is not part of normal
sync, automatic revision retention, or publication cleanup.

The operation is available only when all of these conditions hold:

- the provider implements `VaultMaintenanceStore`;
- the cloud Journal cache is clean and has no pending drafts;
- this device holds the Journal's valid write lease;
- the app can list every blob under this Journal's `blobs/sha256/` prefix; and
- every retained revision manifest can be read, format-validated, and digest
  validated.

The app builds a local, immutable reclamation plan:

1. Flush and publish all local changes, then re-read `current.json` and retain
   its control token.
2. Determine the retained revision set exactly as in section 13.1.
3. Download and validate every retained revision manifest.
4. Build a set of attachment digests referenced by those manifests.
5. List every attachment blob under this one Journal's blob prefix.
6. Classify a blob as reclaimable only if no retained manifest references its
   digest and its object version is known.
7. Present the plan with object key, digest, size, and total reclaimable bytes.
   Attachment names are not guaranteed for orphaned blobs and must not be
   invented.

The user must explicitly confirm the exact plan. Before any deletion, the app
re-reads `current.json`. If its token changed, the lease is no longer valid, or
the plan has expired, the app discards the plan and requires a new scan.

For each confirmed blob, call `DeleteImmutableIfVersion` with the version
captured in the plan. A failed or conditional-delete conflict leaves the blob
in place and is reported as unreclaimed storage. The operation never deletes
database snapshots, manifests, local caches, or an attachment referenced by a
retained revision.

Unknown manifest formats, incomplete listings, provider errors, or digest
validation failures disable deletion for the whole Journal. The safe outcome is
always a leaked blob, never a guessed deletion.

### 13.4 Restore and history semantics

Revision history is bounded. The UI may show and restore only retained
revisions. Restoring a historical revision creates a new revision with a new
monotonic `revisionNumber`; it must never repoint `current.json` backward to a
revision that may have been expired.

Provider-native version retention may preserve more historical objects than
Journal's own policy. Journal Vault is synchronized versioned storage, not a
promise that provider billing or archival retention exactly matches N.

## 14. Frontend requirements

- Settings UI to add, validate, edit, and remove Vault providers.
- Create-Journal flow with Local/Cloud choice and provider selector.
- Clear no-provider and provider-validation errors.
- Cloud Journal tree status: clean, dirty, syncing, offline, locked,
  read-only, conflict, or error.
- Separate local save state from remote publication state.
- **Sync now**, retry, reconnect/recover, release lease, and force-unlock
  actions when applicable.
- Conflict recovery UI with explicit, non-destructive options.
- Cloud Journal cache location and unsynced recovery-copy information in a
  diagnostic view.
- Per-Journal **Preview reclaimable attachments** and a separately confirmed
  **Remove selected reclaimable attachments** action when provider maintenance
  capabilities are available.

The UI must never label a Journal “synced” merely because local autosave
succeeded. It is synced only after a successful conditional current-pointer
update and verification.

## 15. Backend interfaces and responsibilities

Suggested components:

```text
App / Wails adapter
  -> StoreRouter
      -> LocalJournalStore
      -> CloudJournalStore
           -> JournalService over one cache database
           -> VaultSyncService
                -> VaultStore provider adapter
```

Responsibilities:

- `StoreRouter`: validates composite routing IDs and selects a database/store.
- `JournalService`: unchanged content behavior for a specific database.
- `CloudJournalStore`: validates cloud Journal scope and write permissions.
- `VaultSyncService`: lease acquisition, download, publish, retry, and
  conflict-state transitions.
- `VaultStore`: provider API adaptation and capability validation only.
- Local app database: provider settings, mounts, and non-authoritative cache
  state only.

Cloud write operations must reject requests when the cache is missing, a mount
is read-only, a valid lease is absent, or the mount is in conflict state.

## 16. Error model

Use typed errors/statuses rather than parsing provider messages:

```text
provider_unavailable
provider_auth_failed
provider_capability_missing
lease_held
lease_lost
current_pointer_conflict
cache_missing
cache_corrupt
digest_mismatch
portable_encryption_unavailable
sync_rate_limited
```

Provider response text may be recorded as diagnostic context but must not
drive application state transitions.

## 17. Implementation phases

### Phase 1: content/database boundaries

- Separate portable Journal content from installation state.
- Make `JournalService` operate on an explicit database/store.
- Add store-aware routing IDs and cloud Journal root validation.
- Define cloud cache layout and mount records.

### Phase 2: portable encryption and snapshots

- Implement portable per-Journal cloud encryption records.
- Add consistent SQLite staging snapshot creation.
- Add attachment digest metadata and local blob-cache lifecycle.
- Prove new-device encrypted recovery with only remote Vault state.

### Phase 3: Vault protocol and one provider

- Implement manifest/current-pointer/lease codecs and validators.
- Implement capability validation and a first `VaultStore` provider adapter.
- Implement create, pull, lease, publish, retry, and conflict flows.
- Implement recovery by explicit cloud Journal ID.

### Phase 4: product integration

- Compose cloud mounts into the tree and search.
- Add provider settings, sync status, manual sync, reconnect, and recovery UI.
- Add retention controls and best-effort cleanup.

### Phase 5: additional providers and hardening

- Add a second provider implementation to prove the abstraction.
- Add cancellation, bandwidth limits, rate-limit backoff, and diagnostic export.
- Consider provider discovery/listing, provider-native version retention, and
  optional share workflows.

## 18. Required tests

### Protocol and storage tests

- Manifest/current/lease JSON validation and forward-version rejection.
- Digest/size verification on every upload and download.
- Conditional current-pointer update success and conflict failure.
- Lease acquire, renewal, expiry, competing writer, and force-unlock cases.
- Immutable upload retry/idempotency and unreachable candidate revision safety.
- Provider capability rejection before any partial cloud create state.
- Retention keeps exactly the current revision and the configured newest N - 1
  prior pointer-confirmed revisions.
- Cleanup stops if the current-pointer token changes and never deletes the
  current revision.
- Failed listing or deletion leaks old revision objects but never prevents a
  completed publication or deletes the active Journal.
- Attachment blobs remain available after revision expiry, including blobs from
  failed candidate publications.
- Attachment reclamation preview identifies only blobs absent from every
  retained, validated revision manifest; unknown or incomplete scans delete
  nothing.
- Attachment reclamation requires a clean cache, valid Journal lease, stable
  current-pointer token, explicit confirmation, and version-aware deletion.
- Restoring a retained revision creates a new monotonic revision rather than
  moving the current pointer backward.

### Journal workflow tests

- Create, edit, flush, publish, reopen, and recover a cloud Journal.
- New-device recovery after deleting all local app and cache databases.
- Provider offline/auth/rate-limit failures preserve unsynced cache work.
- Cached Journal never writes remotely without a valid lease.
- Conflict preserves local work and prevents automatic overwrite.
- Local and cloud Journals compose correctly in tree and search.
- Attachment lazy fetch and digest deduplication.
- Encrypted cloud recovery succeeds with the matching password and fails
  clearly with a different password.
- Installation settings and credentials are absent from all revision objects.

## 19. Final design rule

Journal Vault storage contains complete, immutable, verified Journal revisions,
content-addressed attachment blobs, and two conditional control documents:
`current.json` and `lease.json`. The app edits only a local cached SQLite
database. A pointer-confirmed remote revision—not any local device state—is the
recoverable source of truth for a cloud Journal.
