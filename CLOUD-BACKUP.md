# Cloud Backup V1

## 1. Purpose

Cloud Backup stores a verified snapshot of the complete local `journal.db` at
one configured S3-compatible endpoint. It lets a user back up the database from one
device and deliberately restore it on another.

This is **snapshot replication**, not collaborative synchronization. It does
not merge databases, support simultaneous writers, or make a cloud directory
safe for SQLite's live database files.

The intended user promise is:

> I can safely upload a complete Journal snapshot, verify that it reached my
> S3 bucket, and later replace this device's Journal data with that
> snapshot.

## 2. Scope

### V1 includes

- Register one S3-compatible backup endpoint.
- Manually upload a complete, SQLite-consistent snapshot with **Sync Now**.
- Download a complete remote snapshot and replace the local database through
  an explicit restore flow.
- Detect a changed remote snapshot before overwriting it.
- Verify SHA-256 digest and size before accepting an upload or restore.
- Preserve a timestamped local recovery copy before a restore.
- Prompt on application close when local changes are newer than the last
  successful cloud backup.
- Keep the application read-only while a snapshot upload or restore is in
  progress.
- Require a configured master password before Cloud Backup can be configured,
  and a validated Cloud Backup endpoint before a restore can run.

### V1 explicitly does not include

- Real-time synchronization, multi-writer editing, or SQLite database merge.
- Automatic background upload.
- Directly opening a SQLite database from a synced folder, network mount, or
  provider-managed filesystem.
- Per-Journal cloud storage, selective Journal download, or attachment blob
  deduplication.
- Google Drive, WebDAV, SFTP, filesystem paths, or non-S3 endpoint plugins.
- Automatic conflict resolution.

## 3. Product terminology

- **Local database**: the active `journal.db` used by the application.
- **S3-compatible endpoint**: a configured S3 API base URL, bucket, region,
  and prefix that
  holds one Cloud Backup snapshot.
- **Snapshot**: an immutable, staged SQLite backup produced from the active
  database.
- **Manifest**: small JSON control metadata describing the current snapshot.
- **Remote version token**: opaque S3 object version identifier for the
  current manifest.
- **Last synced token**: the remote version token observed after the most
  recent successful upload or restore on this installation.
- **Conflict**: the remote manifest changed since this installation last
  observed it, while the local database has unsynced changes.

## 4. User experience

### 4.1 Settings

Add a **Cloud Backup** section to Settings.

It shows the configured S3 endpoint, bucket/prefix, last successful backup time, remote snapshot
time, local dirty/clean state, and the last observed remote version.

Cloud Backup requires a master password because it encrypts persisted endpoint
credentials. If the user selects Configure Cloud Backup without one, Journal
prompts them to create a master password before continuing. This does not
require an encrypted Journal to exist.

Cloud Backup and encrypted-Journal unlocking are separate states. A backup or
restore operation may prompt for the master password to decrypt its credential
record, but it must not unlock encrypted Journals or reveal their contents.

Actions:

- **Configure Cloud Backup**
- **Sync Now**
- **Restore from Cloud Backup**
- **Disconnect Endpoint**

`Sync Now` is disabled while another backup/restore operation runs. It is
available only after the configured provider passes validation.

### 4.2 Sync Now

When a user chooses **Sync Now**:

1. Journal displays a non-dismissible syncing state and switches content
   editing to read-only.
2. It flushes pending title, editor, and attachment updates.
3. It creates and verifies a consistent staged SQLite snapshot.
4. It checks the remote manifest's version token against the database's
   last synced token.
5. If compatible, it uploads and verifies the new snapshot, then publishes a
   new manifest.
6. It returns to writable state and shows the completed backup time.

If the remote token changed, Journal must not overwrite it. It enters a
conflict state and offers the choices described in section 8.

### 4.3 Restore from Cloud Backup

Restore always requires confirmation because it replaces the complete local
database, including all Journals, Trash contents, master-password setup, and
application settings stored in the database.

Restore is available only after the user has defined a master password and
configured a validated Cloud Backup endpoint locally. This applies to the
first restore on a newly installed device: the user supplies the same master
password used by the remote database and enters the endpoint credentials in
Settings before choosing Restore. No special bootstrap or temporary-credential
flow exists.

The confirmation dialog states the remote snapshot time and endpoint. It also
states that the current local database will first be retained as a recovery
copy. Once the remote snapshot is verified and installed, its database becomes
the local primary copy, including its own persisted Cloud Backup configuration
and encrypted credential record. The locally entered restore configuration is
therefore replaced along with the rest of the local database.

### 4.4 Close behavior

After local drafts have been flushed, Journal compares the local database
fingerprint/state with the last successfully synced state.

When local changes have not been backed up, close presents:

- **Sync and Quit**
- **Quit Without Sync**
- **Cancel**

If upload fails, Journal remains open and explains the error. The user may
retry, cancel, or explicitly choose Quit Without Sync. Journal must never
silently discard an unsynced database because the provider is unavailable.

## 5. Remote layout

V1 stores a current manifest and immutable snapshot objects under one
configured S3 bucket and fixed prefix:

```text
s3://<bucket>/<prefix>/journal-cloud-backup/
  current.json
  snapshots/
    <snapshot-id>/journal.db
```

`snapshot-id` is a UUIDv7 or equivalent time-sortable random identifier. A
snapshot is never modified after upload.

Using immutable S3 snapshot objects means a failed or interrupted upload cannot
replace the last published backup. `current.json` is the only mutable remote
object.

### 5.1 Current manifest

```json
{
  "format": "journal-cloud-backup",
  "formatVersion": 1,
  "snapshotId": "uuidv7",
  "createdAt": "2026-07-17T12:00:00Z",
  "database": {
    "key": "snapshots/<snapshot-id>/journal.db",
    "sha256": "hex",
    "size": 1048576
  },
  "previousSnapshotId": "uuidv7"
}
```

The S3 version token for `current.json` is retained in `journal.db` as part of
the Cloud Backup configuration and supplied as the conditional-write
precondition for the next upload.

### 5.2 Retention

V1 never deletes a published snapshot. Every immutable snapshot remains in the
configured bucket/prefix until the user removes it through their cloud endpoint
or an endpoint-level lifecycle policy. Journal does not list, prune, or manage
snapshot retention.

## 6. S3-compatible endpoint

V1 supports endpoints exposing a compatible S3 object-storage API, including
Amazon S3 and self-hosted or third-party S3-compatible services. It does not
support desktop-synced folders, Google Drive, WebDAV, SFTP, local filesystem
paths, or non-S3 endpoint plugins.

The endpoint configuration is stored in `journal.db`. It contains an HTTPS
endpoint URL, bucket name, S3 signing region, optional object prefix,
path-style versus virtual-hosted-style addressing preference, and display name.
The credential material needed to authenticate to the endpoint is stored in a
separate encrypted record.

Cloud Backup requires a configured master password. Endpoint credentials are
sealed with the same master-password cryptography used for encrypted Journal
material, using a Cloud Backup-specific domain/key context. Cloud operations
may prompt for the master password to decrypt this record into a separate,
short-lived Cloud Backup credential state. This must not unlock encrypted
Journals. Conversely, **Lock Journals** clears Journal encryption keys only;
it must not clear Cloud Backup credentials or disable cloud operations.

The configured S3 principal needs only `GetObject`, `PutObject`, and
`HeadObject` under the configured prefix. Bucket-wide listing, delete, or
administrative permissions are not required.

Bucket versioning is strongly recommended when the endpoint supports it. The
protocol still publishes immutable snapshots and conditionally updates the
manifest; versioning provides a provider-side recovery path for an accidental
manifest overwrite.

The S3 adapter uses SHA-256 checksums for snapshot uploads and verifies object
size/checksum metadata before publishing the manifest. It uses an opaque object
version identifier for conditional `current.json` updates. If an endpoint
cannot perform the required conditional update, Journal must reject it rather
than fall back to last-writer-wins behavior.

The internal storage boundary is deliberately narrow:

```go
type S3BackupStore interface {
    Validate(ctx context.Context, endpoint S3BackupEndpoint) error
    GetObject(ctx context.Context, endpoint S3BackupEndpoint, key string) (io.ReadCloser, S3ObjectMeta, error)
    PutImmutable(ctx context.Context, endpoint S3BackupEndpoint, key string, source io.Reader, sha256 string) (S3ObjectMeta, error)
    HeadObject(ctx context.Context, endpoint S3BackupEndpoint, key string) (S3ObjectMeta, error)
    GetCurrent(ctx context.Context, endpoint S3BackupEndpoint) ([]byte, S3VersionToken, error)
    PutCurrentIf(ctx context.Context, endpoint S3BackupEndpoint, value []byte, expected S3VersionToken) (S3VersionToken, error)
    CreateCurrentIfAbsent(ctx context.Context, endpoint S3BackupEndpoint, value []byte) (S3VersionToken, error)
}
```

The adapter validates HTTPS connectivity, credentials, bucket/region and
addressing mode, prefix access, checksum support, read-after-write behavior,
and conditional manifest replacement before the endpoint can be saved.
Conditional manifest replacement is what prevents accidental last-writer-wins
data loss. Endpoint validation must reject services that advertise S3
compatibility but cannot provide the required semantics.

## 7. Safe upload protocol

1. Acquire the process-wide cloud backup operation lock.
2. Switch the application to read-only and surface a syncing indicator.
3. Flush all pending editor and document writes.
4. Checkpoint and create a SQLite backup into a private staging directory.
   Never upload the active `journal.db`, WAL, or SHM files directly.
5. Run SQLite integrity validation on the staged database.
6. Calculate the staged file's SHA-256 and size.
7. Read `current.json` and its remote version token.
8. If this installation has a last synced token and it differs from the remote
   token, stop and enter conflict state before uploading anything.
9. Upload the staged snapshot to a new immutable key.
10. Read it back or otherwise verify the provider-reported size and digest.
11. Create a new manifest referring to the staged snapshot.
12. Conditionally publish `current.json` using the token read in step 7, or
    conditionally create it when no manifest exists.
13. Read and validate the published manifest.
14. Persist the new manifest token, snapshot ID, digest, and sync time in
    `journal.db`.
15. Remove the staged snapshot and return the application to writable state.

If a step fails before manifest publication, the previous remote manifest is
still valid. An uploaded but unreferenced snapshot is harmless and may be
cleaned up later.

## 8. Conflict policy

Cloud Backup never merges two SQLite files.

When the remote manifest changed since the local installation last synced and
the local database contains changes, automatic upload stops. The UI offers:

- **Restore remote backup**: first make a local recovery copy, then replace
  the local database with the remote snapshot.
- **Keep local copy**: export or save the current local database as a clearly
  named recovery file, then restore remote data.
- **Cancel**: keep working locally without uploading.

V1 does not offer force-overwrite of a changed remote backup. If that proves
necessary later, it must be an explicit advanced operation with a new immutable
snapshot and clear warning.

## 9. Safe restore protocol

1. Acquire the operation lock and switch the app to read-only.
2. Flush pending local drafts.
3. Read and validate the remote manifest.
4. Download the referenced snapshot to a private staging directory.
5. Verify exact size, SHA-256, and SQLite integrity.
6. Create a timestamped recovery backup of the current local database using
   the same SQLite snapshot mechanism.
7. Close the active database connection and stop autosave.
8. Atomically replace the local database with the verified staged snapshot.
9. Reopen it, run normal migrations, and validate that Journal can load its
   tree and settings.
10. If reopening fails, restore the local recovery copy before reporting the
    failure.
11. Record the manifest token and snapshot ID as the local synced state.
12. Return the application to writable state.

The restore UI should reload after successful replacement. Restarting the app
is an acceptable V1 simplification if hot-reopening all application state is
too risky.

## 10. Cloud Backup state in `journal.db`

`journal.db` is the single source of truth for Cloud Backup. A complete backup
therefore includes both Journal content and the information needed to back it
up again after restoration.

V1 stores the following in `journal.db`:

- S3 endpoint URL, bucket, signing region, addressing mode, prefix, and
  endpoint display name;
- an encrypted Cloud Backup credential record, sealed with the master-password
  key;
- last observed manifest version token and snapshot ID;
- last successful local snapshot digest and backup time;
- last known remote manifest time;
- current operation/error status.

The endpoint configuration travels with the database, but the credential record
is unreadable without the database's master password. The configured credential
must still be scoped only to the backup bucket/prefix and not be reused for
unrelated infrastructure. The feature UI must warn about this when credentials
are saved.

There is no separate bootstrap credential path. On a new installation, Journal
creates its normal local database; the user defines the same master password as
the remote database and saves the endpoint configuration in Settings. Restore
uses that configured endpoint, retains the pre-restore local database as a
recovery copy, and then replaces it with the verified remote database. The
remote database's Cloud Backup configuration and encrypted credential record
then become the active configuration.

## 11. Encryption and compatibility

The snapshot contains encrypted Journals, their encrypted attachments, and the
master-password configuration exactly as they exist locally. A device restoring
the snapshot needs the same master password to unlock encrypted content.

Cloud Backup configuration requires a defined master password even when the
user has not encrypted any individual Journal. This is intentional: the master
password is the root key for the persisted S3 credential record, but Cloud
Backup does not depend on Journal unlock state.

The app version performing restore must support the snapshot's SQLite schema.
If the remote database is newer than the local app supports, Journal must show
the existing startup-style compatibility error and leave the recovery copy
available. A newer app may migrate an older restored snapshot normally.

## 12. Failure handling

- **Offline, authentication, quota, or rate-limit failure**: keep the local
  database unchanged and mark backup as unsynced/error.
- **Interrupted upload**: do not update the manifest; the prior snapshot
  remains current.
- **Interrupted restore**: do not replace the active database until staging
  validation succeeds. Atomic replacement and recovery backup prevent partial
  local state.
- **Digest or integrity failure**: reject the snapshot and retain the active
  database.
- **Application crash during sync**: on next launch, remove stale staging
  files and determine status from the local sync record plus the remote
  manifest; never infer a successful backup from an incomplete upload.
- **Credential failure**: preserve local data and show the endpoint as
  unavailable; never delete a remote snapshot as part of disconnecting or
  replacing endpoint configuration.

## 13. Implementation outline

1. Add Cloud Backup configuration, encrypted credential, and sync-state tables
   to `journal.db`, protected by the existing master-password encryption
   primitives.
2. Define the provider contract and implement a single provider adapter.
3. Implement SQLite staged snapshot, integrity, digest, and atomic replacement
   utilities.
4. Implement manifest parsing, conditional publication, and remote validation.
5. Add backup status and Settings UI.
6. Add the read-only operation guard and close-time decision flow.
7. Add restore/recovery UI and tests for interrupted and invalid snapshots.

## 14. V1 acceptance criteria

- A manual backup never uploads the active SQLite file directly.
- A failed upload never changes the currently published remote manifest.
- A restore never replaces local data before downloaded data verifies.
- A changed remote manifest prevents silent overwrite.
- A user can recover the pre-restore local database after a mistaken restore.
- Unsynced local changes are visible at close, with an explicit quit choice.
- The feature works without any per-Journal cloud cache, merge engine, or
  attachment blob protocol.
