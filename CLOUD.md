# Cloud Journals Design Specification

## Goal

Add cloud-managed Journals while keeping the current local Journal behavior
intact.

The core product requirement is:

- A user can store selected Journals in cloud storage.
- A cloud Journal can be opened and edited from any device that has Journal
  installed and is properly configured or credentialed for the cloud location.
- Simultaneous editing is not required.
- It is acceptable, and preferred for the first implementation, to lock a cloud
  Journal to one device at a time.
- Cloud Journals must remain recoverable if all local devices and local
  `journal.db` files are lost.

This design requires cloud Journals to be stored as OCI 1.1 artifact-based
content in an OCI-compatible registry. The application edits a local cached
SQLite database and publishes complete Journal revisions as OCI artifacts. It
does not edit a remote database in place.

## Standards Basis

This design relies on OCI 1.1 registry behavior:

- OCI image manifests can represent non-container artifacts by using
  `artifactType`, config descriptors, layer descriptors, annotations, and
  optional `subject` links.
- OCI descriptors carry content digest and size, so Journal revision integrity
  should use registry descriptors instead of custom checksum files.
- OCI distribution APIs provide content push/pull by digest or tag, tag listing,
  and OCI 1.1 referrers lookup. The first implementation deliberately avoids
  relying on referrers because registry support is not widespread enough.

References:

- OCI Image Manifest Specification 1.1:
  <https://github.com/opencontainers/image-spec/blob/v1.1.0/manifest.md>
- OCI Distribution Specification 1.1:
  <https://github.com/opencontainers/distribution-spec/blob/v1.1.0/spec.md>

## Registry Compatibility And Concurrency Caveat

OCI registry APIs are a good fit for immutable revision blobs and digest-based
integrity checks, but they are not a complete distributed database protocol.

The first implementation must treat registry behavior as provider-capability
driven, not as uniformly available across all OCI-compatible registries.

Required first-pass provider validation:

- Pull manifest by tag.
- Pull manifest by digest.
- Push blobs.
- Push manifests under tags.
- List tags, including pagination.
- Resolve manifest digest after a push.
- Return enough response information to distinguish authentication,
  authorization, missing repository, missing tag, missing blob, and rate limit
  errors.

Optional provider capabilities:

- Delete tags or manifests.
- Conditional manifest or tag updates using ETag, `If-Match`, or equivalent
  behavior.
- Referrers API support.

The first implementation should maintain a small compatibility matrix for
tested providers. The first documented provider is quay.io. Provider validation
should record the capabilities observed for that configured provider so the rest
of the app can make conservative choices.

Mutable OCI tags are not compare-and-swap variables in the general case. A
client can re-read `current`, observe the expected `base_digest`, and still lose
a race when another client updates the same tag immediately afterward. Advisory
locks and `base_digest` checks reduce this risk, but they do not eliminate it
unless the selected registry supports conditional tag or manifest updates. The
UI and conflict handling must assume that rare last-writer-wins races are
possible on registries without conditional update support.

## Non-Goals

- Real-time collaboration.
- Multi-writer merge conflict resolution.
- Running SQLite directly from Dropbox, iCloud Drive, OneDrive, Google Drive, or
  another cloud-synced path.
- Filesystem cloud packages such as `.journalcloud` directories.
- A hosted Journal service owned by this application.
- Replacing local-only storage with an embedded OCI registry or required local
  content-addressable blob store.
- Converting a Journal from local to cloud or cloud to local after creation.
- Supporting non-OCI cloud storage for cloud Journals in the first
  implementation.

## First-Pass Product Decisions

- When a user creates a Journal, they choose whether it is local or cloud.
- A Journal's storage type is immutable after creation. Local Journals remain
  local, and cloud Journals remain cloud-managed.
- Cloud provider configuration lives in Settings.
- The app supports multiple configured cloud providers at the same time.
- Every cloud provider is an OCI-compatible registry location.
- If at least one OCI registry provider is configured, the create-Journal flow
  lets the user choose one provider for a cloud Journal.
- If no OCI registry provider is configured, the create-Journal flow explains
  that a provider must be configured in Settings before a cloud Journal can be
  created.
- Local and cloud Journals use the same Journal master password concept.
- If two devices are configured with different master passwords, an encrypted
  cloud Journal created or encrypted on one device may not be openable on the
  other device even when registry credentials are correct.
- The first OCI registry explicitly tested and documented should be quay.io.
- OCI referrers should not be used in the first implementation.
- OCI-compatible artifact signing is not needed in the first implementation,
  but may be considered longer term.

## Foundational Implementation Decisions

These decisions must be implemented before any user-facing cloud Journal is
created. They are not later cleanup work.

- Cloud Journal encryption portability is part of the artifact format. The app
  must not create encrypted cloud Journals until the portable cloud encryption
  schema is implemented and tested with new-device recovery.
- Local-only users must keep the simplest possible data-management model: one
  SQLite database under the app config directory, with no local OCI registry or
  required blob-store layout.
- Local and cloud Journal content must use one journal-content schema. Cloud
  Journal cache databases should be full journal-content databases, not reduced
  or second-class databases with a divergent table model.
- App-installation state must be separated from portable Journal content.
  Provider settings, mount records, UI settings, and local convenience state
  belong to the local app database, not to portable cloud Journal artifacts.
- OCI is the cloud publication and recovery layer. It is not the universal
  storage engine for local-only Journals in the first implementation.
- API routing must identify both the store and the item/document inside that
  store. Plain item IDs are not enough once multiple SQLite databases are open.
- Cloud attachment externalization must include a local blob-cache lifecycle.
  The editor cannot assume every attachment payload is stored inline in
  `document_attachments`.
- Registry capability detection must run before creating or reconnecting a cloud
  Journal. If a required capability is absent, cloud creation for that provider
  must fail before local or remote partial state is created.

## Terminology

- **Local app database**: The existing installation-level SQLite database stored
  at `<user-config-dir>/Journal/journal.db`.
- **Local Journal**: A Journal stored and managed only in the local app
  database.
- **Cloud Journal**: A Journal stored as OCI artifacts in an OCI-compatible
  registry and edited through a local cached SQLite database.
- **OCI provider**: A configured registry plus repository/namespace and
  credentials.
- **OCI repository**: The registry repository that stores Journal artifacts, for
  example `ghcr.io/alice/journals`.
- **Cached Journal database**: A local copy of one cloud Journal's SQLite
  database used for editing.
- **Revision artifact**: An OCI artifact manifest representing one immutable
  published version of a cloud Journal.
- **Current tag**: A mutable OCI tag pointing to the latest accepted revision
  artifact for a cloud Journal.
- **Lease lock artifact**: A small OCI artifact representing the current
  advisory edit lock for a cloud Journal.
- **Mounted cloud Journal**: A cloud Journal known to the local app database and
  shown in the app's library tree.

## Storage Topology

The application keeps the current local-only storage experience simple:
local-only users continue to have one SQLite database at:

```text
<user-config-dir>/Journal/journal.db
```

Cloud support adds cached SQLite databases only for cloud-managed Journals. It
does not replace local storage with an embedded OCI registry, a required local
blob store, or an OCI layout directory.

The key split is schema ownership:

- **Journal content schema**: portable tables that describe Journals, folders,
  documents, attachments, search index data, and Journal encryption metadata.
  This schema is shared by local and cloud Journal databases.
- **App installation schema**: non-portable tables for settings, OCI providers,
  cloud mounts, cache state, window/sidebar preferences, and other convenience
  state. This schema lives only in the local app database.

For local Journals, the local app database contains both schema groups. For
cloud Journals, each cached cloud database contains only the journal-content
schema plus cloud Journal metadata.

Example:

```text
<user-config-dir>/Journal/
  journal.db
    - local journals
    - app settings
    - OCI provider settings
    - cloud journal mount metadata

  cloud-cache/
    <cloud-journal-id-a>/
      journal.db
      state.json

    <cloud-journal-id-b>/
      journal.db
      state.json
```

If a user has three locally managed Journals and two cloud-managed Journals,
there are three SQLite databases on that device:

```text
1. <user-config-dir>/Journal/journal.db
   - all three local Journals
   - app settings
   - OCI provider settings
   - cloud mount records

2. <user-config-dir>/Journal/cloud-cache/<cloud-journal-a>/journal.db
   - cloud Journal A

3. <user-config-dir>/Journal/cloud-cache/<cloud-journal-b>/journal.db
   - cloud Journal B
```

There is not one shared SQLite database for all cloud Journals. Each cloud
Journal is an independent portability, locking, backup, and recovery unit.

Cloud cached `journal.db` files are still full journal-content databases. They
are not reduced, special-purpose, or incompatible databases. They are scoped to
one cloud Journal so that each cloud Journal can be published, locked, restored,
and recovered independently.

### Rejected Local Storage Alternative

The app should not make every Journal an OCI artifact backed by a self-hosted
registry inside the app backend in the first implementation.

Reasons:

- It would make local-only users manage a directory of content-addressed blobs
  instead of one SQLite file.
- It would introduce local garbage collection, blob integrity, and index
  corruption modes that the current local app does not have.
- The editor would still need a mutable SQLite working database for autosave,
  search, encryption, tree operations, and attachment lookup, so the design
  would add an artifact store without removing SQLite.
- It would make backup and support harder for users who do not use cloud
  Journals.

OCI may be considered later for explicit local snapshot/export workflows, but it
must not be a prerequisite for ordinary local Journal storage.

## Current Local App Database Responsibilities

The existing local app database continues to own:

- Local Journals.
- Local folders and documents inside local Journals.
- Local document attachments.
- Local full-text search for local Journals.
- App settings such as autosave interval, sidebar width, and last active local
  document.
- OCI provider configuration.
- Cloud mount records.
- Local cache metadata for cloud Journals.

The local app database must not be required to recover cloud Journal contents on
a new device. It may only store convenience metadata about cloud Journals.

In other words, the local app database remains the only database a local-only
user needs to understand or back up. Cloud-specific state is additive and should
not complicate the local-only path.

## OCI Provider Settings

OCI providers are configured per device in the local app database. Provider
configuration is not part of a cloud Journal artifact. The app must support more
than one configured provider at the same time.

Examples of valid configurations:

- `ghcr.io/alice/journals`
- `registry.example.com/journal/private`
- `localhost:5000/dev/journals`
- Two providers using the same registry host but different repositories.
- Two providers using different registry hosts.

Add a table to the local app database:

```sql
CREATE TABLE cloud_providers (
  id TEXT PRIMARY KEY,
  display_name TEXT NOT NULL,
  provider_type TEXT NOT NULL,
  registry TEXT NOT NULL,
  repository TEXT NOT NULL,
  credential_ref TEXT NOT NULL DEFAULT '',
  publish_debounce_ms INTEGER NOT NULL DEFAULT 300000,
  publish_min_interval_ms INTEGER NOT NULL DEFAULT 300000,
  publish_max_interval_ms INTEGER NOT NULL DEFAULT 1800000,
  revision_retention_count INTEGER NOT NULL DEFAULT 50,
  status TEXT NOT NULL DEFAULT 'unknown',
  capabilities_json TEXT NOT NULL DEFAULT '{}',
  last_rate_limited_at TEXT NOT NULL DEFAULT '',
  last_rate_limit_retry_after_ms INTEGER NOT NULL DEFAULT 0,
  last_validated_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
```

Initial `provider_type` values:

- `oci-registry`: An OCI-compatible registry and repository.

Rules:

- `id` is a local UUID generated by this app installation.
- `display_name` is user-facing and does not need to be globally unique, though
  the UI should disambiguate duplicates.
- `registry` is the registry host, for example `ghcr.io`.
- `repository` is the repository path, for example `alice/journals`.
- `credential_ref` points to OS keychain material or another local credential
  store entry. Raw registry tokens should not be stored directly in
  `journal.db`.
- `publish_debounce_ms`, `publish_min_interval_ms`, and
  `publish_max_interval_ms` define the default publish cadence for cloud
  Journals stored through this provider.
- `revision_retention_count` controls how many revision tags should be retained
  per cloud Journal for this provider. The default is 50.
- `capabilities_json` records provider behavior observed during validation, such
  as tag listing support, tag deletion support, conditional update support, and
  whether the provider returned useful manifest digests after pushes.
- `last_rate_limited_at` and `last_rate_limit_retry_after_ms` record the most
  recent HTTP 429 response observed for this provider.
- More than one provider row may exist at the same time.
- More than one provider row may use the same registry host.
- A cloud Journal mount references exactly one provider via `provider_id`.
- Removing a provider from Settings must not delete registry artifacts. It only
  removes local provider configuration and may make mounted cloud Journals under
  that provider unavailable until the provider is configured again and the
  Journal is reconnected or remapped.

Settings must support:

- Add OCI provider.
- Rename provider display name.
- Validate registry authentication and repository access.
- Show provider capability validation results.
- Configure provider publish timing.
- Configure provider revision retention.
- Remove provider from this device.

The app should also persist a stable local device identity in app settings or a
small device table. Lock payloads use this identity as `ownerDeviceId`. The
device ID is local convenience state only; losing it must not prevent recovery
of cloud Journal contents.

## Cloud Mount Registry

Add a table to the local app database:

```sql
CREATE TABLE cloud_journal_mounts (
  cloud_journal_id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  provider_id TEXT NOT NULL,
  registry TEXT NOT NULL,
  repository TEXT NOT NULL,
  local_cache_path TEXT NOT NULL,
  current_tag TEXT NOT NULL DEFAULT '',
  current_digest TEXT NOT NULL DEFAULT '',
  base_digest TEXT NOT NULL DEFAULT '',
  lock_tag TEXT NOT NULL DEFAULT '',
  lock_digest TEXT NOT NULL DEFAULT '',
  read_only INTEGER NOT NULL DEFAULT 0,
  publish_debounce_ms INTEGER NOT NULL DEFAULT 0,
  publish_min_interval_ms INTEGER NOT NULL DEFAULT 0,
  publish_max_interval_ms INTEGER NOT NULL DEFAULT 0,
  revision_retention_count INTEGER NOT NULL DEFAULT 0,
  dirty INTEGER NOT NULL DEFAULT 0,
  sync_status TEXT NOT NULL DEFAULT 'clean',
  last_sync_error TEXT NOT NULL DEFAULT '',
  publish_in_progress INTEGER NOT NULL DEFAULT 0,
  last_publish_started_at TEXT NOT NULL DEFAULT '',
  last_publish_finished_at TEXT NOT NULL DEFAULT '',
  last_opened_at TEXT NOT NULL DEFAULT '',
  last_synced_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
```

This table is convenience state only. It can be rebuilt by reconnecting to a
registry provider and discovering Journal artifacts.

Rules:

- `dirty` means the cached cloud Journal database has local changes not yet
  represented by the remote `current` revision.
- `sync_status` should be a compact machine-readable state such as `clean`,
  `dirty`, `syncing`, `rate_limited`, `conflict`, `offline`, `auth_failed`, or
  `error`.
- `last_sync_error` is display/debug context only. It must not be used as the
  source of truth for whether data is recoverable.
- If `provider_id` is removed locally, the duplicated `registry` and
  `repository` values are used to help reconnect or remap the mount, but the app
  still needs valid credentials before it can sync.

## OCI Artifact Model

Each cloud Journal revision is an OCI artifact manifest.

The artifact uses:

- OCI manifest media type:
  `application/vnd.oci.image.manifest.v1+json`
- Journal artifact type:
  `application/vnd.journal.cloud.revision.v1`
- Journal config media type:
  `application/vnd.journal.cloud.config.v1+json`
- SQLite layer media type:
  `application/vnd.journal.sqlite.v1`
- Attachment layer media type:
  `application/vnd.journal.attachment.v1`

The SQLite database is stored as an OCI layer blob. Image attachments are stored
as separate content-addressed OCI blobs/layers instead of being embedded inside
the cloud Journal database. The Journal metadata is stored in the OCI config
blob and mirrored selectively in annotations for discovery.

The app must act as an OCI registry client, not as a Docker-style image puller.
Opening a cloud Journal means pulling the current manifest and config, then
pulling selected blobs by descriptor digest. The app should not invoke a
whole-image pull operation that downloads every layer up front.

Required pull pattern:

```text
1. GET /v2/<repository>/manifests/journal-<cloudJournalId>-current
2. Read the config, SQLite layer descriptor, and attachment descriptors.
3. GET /v2/<repository>/blobs/<sqlite-layer-digest>
4. Open the cached SQLite database.
5. Later, when an image is rendered or prefetched:
   GET /v2/<repository>/blobs/<document_attachments.oci_digest>
```

The OCI Distribution API treats manifests and blobs as separately retrievable
objects. The revision config and/or `document_attachments` rows provide the
attachment digest index, and the registry blob endpoint retrieves only the
needed blob. This is what makes lazy image download possible. If a future
registry client library only exposes Docker-style "pull the whole image"
behavior, it is not sufficient for this design.

This split is required for bandwidth efficiency. If the entire cloud Journal,
including images, were stored as one opaque SQLite blob, a small text edit could
force a large image-heavy database to be pushed again. By storing attachments as
separate content-addressed blobs, unchanged images naturally deduplicate in the
registry and do not need to be uploaded for each revision.

Example revision artifact:

```json
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "artifactType": "application/vnd.journal.cloud.revision.v1",
  "config": {
    "mediaType": "application/vnd.journal.cloud.config.v1+json",
    "digest": "sha256:...",
    "size": 1234
  },
  "layers": [
    {
      "mediaType": "application/vnd.journal.sqlite.v1",
      "digest": "sha256:...",
      "size": 1234567,
      "annotations": {
        "org.opencontainers.image.title": "journal.db"
      }
    },
    {
      "mediaType": "application/vnd.journal.attachment.v1",
      "digest": "sha256:...",
      "size": 345678,
      "annotations": {
        "com.journal.attachmentId": "uuid",
        "com.journal.attachmentName": "photo.jpg",
        "com.journal.attachmentMimeType": "image/jpeg"
      }
    }
  ],
  "annotations": {
    "org.opencontainers.image.created": "2026-07-04T12:10:00Z",
    "com.journal.cloudJournalId": "uuid",
    "com.journal.revision": "0000000002",
    "com.journal.title": "My Journal",
    "com.journal.schemaVersion": "1"
  }
}
```

### Revision Config Blob

The revision config blob is JSON:

```json
{
  "format": "journal-oci-revision",
  "formatVersion": 1,
  "cloudJournalId": "uuid",
  "title": "My Journal",
  "revision": 2,
  "createdAt": "2026-07-04T12:10:00Z",
  "updatedAt": "2026-07-04T12:10:00Z",
  "minimumAppVersion": "1.0.0",
  "schemaVersion": 1,
  "rootJournalId": "uuid",
  "encryption": {
    "state": "plaintext"
  },
  "attachments": [
    {
      "id": "uuid",
      "digest": "sha256:...",
      "mediaType": "application/vnd.journal.attachment.v1",
      "mimeType": "image/jpeg",
      "sizeBytes": 345678,
      "originalName": "photo.jpg"
    }
  ]
}
```

The config blob replaces the old custom `manifest.json` concept. It is stored
as content addressed registry data and referenced by the OCI artifact manifest.
The `attachments` list records the attachment blobs required by this revision.
It is also acceptable for the SQLite database to contain the same digest
metadata; the config list is the recovery-friendly index.

### Tag Scheme

Use deterministic tags for Journal discovery and current revision lookup.

Tags must remain within OCI tag syntax and length limits. The examples below use
UUIDs without braces.

```text
journal-<cloudJournalId>-current
journal-<cloudJournalId>-rev-0000000001
journal-<cloudJournalId>-rev-0000000002
journal-<cloudJournalId>-lock
```

Rules:

- Revision tags are immutable by application policy.
- The current tag is mutable and points to the latest accepted revision.
- The lock tag is mutable and points to the latest lock artifact.
- A publish must never mutate a revision tag after it has been pushed.
- A publish may update the current tag only after conflict checks pass.

## Components No Longer Implemented

Because OCI provides descriptors, manifests, tags, blobs, and registry APIs, the
app must not implement the old filesystem package components:

- No `.journalcloud` directory format.
- No custom top-level `manifest.json`.
- No custom `revisions/` directory.
- No custom `tmp/` upload directory.
- No `journal.db.sha256` sidecar files.
- No filesystem atomic rename protocol for cloud publishing.
- No cloud folder scan for `.journalcloud` packages.
- No filesystem cloud sync status inference.

Integrity is checked using OCI descriptor digests and sizes.

## Journal Database Contents

The implementation should distinguish between a reusable journal-content schema
and the local app installation schema.

### Journal Content Schema

The journal-content schema is the normal Journal data model. It should be shared
by:

- the existing local app database for local Journals;
- each cached cloud Journal database;
- the SQLite layer stored in each cloud revision artifact.

Journal content tables:

- `items`
- `documents`
- `document_attachments`
- `journal_encryption_keys`
- `encryption_master`, or an equivalent portable per-Journal encryption metadata
  table if the encryption model is refined
- `library_search_fts`, optional and disposable
- `cloud_journal_metadata`, only for cloud Journal databases

The local app database may contain many local Journals in this schema. A cached
cloud Journal database contains the same journal-content schema but is scoped to
one cloud Journal. This keeps the database format full-fledged and reusable
while keeping each cloud Journal independently publishable and recoverable.

### App Installation Schema

The app installation schema is not portable Journal content. It should remain in
the local app database and should not be published as part of a cloud Journal
revision.

App installation tables include:

- app settings such as autosave interval, sidebar width, and last active local
  document;
- OCI provider configuration;
- cloud mount records;
- local cache metadata for cloud Journals;
- provider validation and rate-limit state;
- local UI convenience state.

The migration code should be split so journal-content migrations can run against
both the local app database and cached cloud Journal databases, while
app-installation migrations run only against the local app database. This avoids
making cloud databases second-class while still preventing provider settings and
UI state from leaking into cloud revision artifacts.

### Cloud Journal Scope Validation

A cloud Journal database must represent exactly one cloud Journal content unit.
Validation should allow:

- exactly one top-level Journal root;
- zero or one top-level system Trash row, if the shared journal-content schema
  requires one;
- no other top-level roots;
- no unrelated top-level Journals.

The system Trash row, if present, is part of that cloud Journal's content
database. It is not the app-wide local Trash.

The cloud database should not contain app-installation state such as:

- global autosave interval;
- library pane width;
- app-wide last opened document;
- provider settings;
- mount records for other cloud Journals;
- unrelated local Journals.

### Cloud Attachment Storage

Local and cloud Journals use the same `document_attachments` table. The
difference is where the payload bytes live.

Local Journals use inline attachment storage:

- `storage_kind = 'inline'`
- `content_blob` contains plaintext bytes, or `content_ciphertext` contains
  encrypted bytes
- OCI descriptor fields are empty

Cloud Journals use external OCI blob storage:

- `storage_kind = 'oci'`
- `content_blob` and `content_ciphertext` are `NULL` after the attachment has
  been externalized
- OCI descriptor fields identify the remote blob and decryption metadata

This keeps one logical attachment schema while letting cloud Journals avoid
pushing unchanged image bytes after every text edit.

Extend the shared `document_attachments` table with cloud descriptor fields:

```sql
ALTER TABLE document_attachments ADD COLUMN storage_kind TEXT NOT NULL DEFAULT 'inline';
ALTER TABLE document_attachments ADD COLUMN oci_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE document_attachments ADD COLUMN oci_media_type TEXT NOT NULL DEFAULT '';
ALTER TABLE document_attachments ADD COLUMN oci_size_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE document_attachments ADD COLUMN oci_encryption_alg TEXT NOT NULL DEFAULT '';
ALTER TABLE document_attachments ADD COLUMN oci_encryption_key_id TEXT NOT NULL DEFAULT '';
ALTER TABLE document_attachments ADD COLUMN oci_encryption_nonce BLOB NULL;
```

Rules:

- `id` remains the attachment node ID referenced by ProseMirror JSON.
- `storage_kind` is `inline` for local inline bytes and `oci` for cloud-managed
  OCI blobs.
- `oci_digest` is the OCI blob digest for the attachment payload when
  `storage_kind = 'oci'`.
- `oci_media_type` is normally `application/vnd.journal.attachment.v1`.
- `oci_size_bytes` is the remote OCI blob size.
- For plaintext cloud Journals, the OCI attachment blob contains the original
  image bytes.
- For encrypted cloud Journals, the OCI attachment blob contains encrypted image
  bytes, and the `oci_encryption_*` fields store the metadata needed to decrypt
  them.
- Encrypted attachment blobs must use a specified algorithm and associated-data
  scheme. The first implementation should use the same XChaCha20-Poly1305
  primitive as local Journal encryption, with associated data derived from the
  cloud Journal ID, attachment ID, OCI digest, media type, and encryption key ID.
- `document_attachments.content_blob` and `content_ciphertext` should be `NULL`
  for cloud-managed attachments after the attachment has been externalized.
- The attachment digest may also be included in the revision config blob so a
  new device can discover required blobs before opening every image.
- Attachments should be downloaded lazily when rendered, but the app may
  prefetch them when opening a Journal.
- Missing attachment blobs should not prevent the document text from opening;
  the editor should show a clear missing-image placeholder.

Attachment lookup must route through the cloud session. The current local API
shape `GetDocumentAttachmentDataURL(attachmentID)` is not enough when attachment
IDs are only unique inside a specific SQLite database. The API must include a
store reference, or attachment IDs returned to the frontend must be namespaced at
the API boundary.

Local attachment cache state should live outside the published cloud Journal
database, for example in `cloud-cache/<cloud-journal-id>/state.json` or a local
app database table. Cache state may include local blob paths, `missing`,
`present`, `fetching`, or `error` status, and the last fetch error. It must not
be required for new-device recovery and must not be included in OCI revision
artifacts.

When a new image is inserted into a cloud Journal, the app must durably store the
payload locally before returning success. The first implementation should write
the pending payload into the local attachment cache, create or update the
`document_attachments` row with `storage_kind = 'oci'`, and mark the cloud
Journal dirty. During publish, the app uploads the cached payload as an OCI blob,
verifies the digest and size, updates `oci_digest`, `oci_media_type`, and
`oci_size_bytes`, and then includes that descriptor in the revision config and
manifest. If publish fails, the cached payload and dirty database remain local
and retryable.

### Cloud Metadata Table

Each cloud Journal database should include metadata proving that it is a
self-contained cloud Journal database.

```sql
CREATE TABLE cloud_journal_metadata (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
```

Required keys:

- `cloud_journal_id`: Stable UUID for this cloud Journal.
- `schema_version`: Cloud Journal database schema version.
- `root_journal_id`: ID of the single root Journal row in `items`.
- `created_at`: ISO-8601 timestamp.
- `updated_at`: ISO-8601 timestamp.
- `minimum_app_version`: Minimum Journal app version that can safely open this
  database.

The service should validate on open that:

- Exactly one top-level Journal root exists.
- `root_journal_id` exists and has kind `journal`.
- No unrelated top-level Journals or non-system root rows are present.
- Required metadata keys exist.
- The schema version is supported.

## Revision Publication Protocol

Publishing a cloud Journal update must not corrupt or replace the last good
revision if the app crashes or the network fails.

Required protocol:

1. Flush all pending document drafts in the cached Journal database.
2. Close or checkpoint the cached SQLite database so no WAL-only changes remain
   unpublished.
3. Copy the cached `journal.db` to a local staging file.
4. Read the remote current tag and record its manifest digest.
5. Confirm the remote current digest still equals the session `base_digest`.
6. Find attachment descriptors referenced by this revision.
7. Upload any new attachment blobs that are not already present in the registry.
   Unchanged attachment blobs keep the same digest and do not need to be pushed
   again.
8. Upload the staged SQLite database as an OCI blob.
9. Build and upload the revision config JSON as an OCI blob. The config should
   include the required attachment descriptors for this revision.
10. Build an OCI artifact manifest that references the config blob, SQLite blob,
    and attachment blobs.
11. Push the revision manifest under a new immutable revision tag:

   ```text
   journal-<cloudJournalId>-rev-0000000003
   ```

12. Pull or inspect the revision tag and verify that the registry reports the
    expected manifest digest.
13. Re-read the remote current tag and confirm it still equals `base_digest`.
14. Update the current tag to point to the new revision manifest. If the
    provider supports conditional tag or manifest updates, use the last observed
    `current` descriptor as the condition:

    ```text
    journal-<cloudJournalId>-current
    ```

15. Re-read the current tag and verify that it resolves to the new manifest
    digest.
16. Update local mount state:
    - `base_digest = new manifest digest`
    - `current_digest = new manifest digest`
    - `last_synced_at = now`

If the app crashes before step 14, the old current tag still points to the
previous good revision. The new revision tag may exist but is not current.

If step 5 or step 13 fails, another device or recovery operation changed the
current revision. The local app must not publish over that new head. It should
open a conflict flow.

OCI registries generally allow mutable tag updates, but not all registries
provide compare-and-swap semantics for tags. Therefore `base_digest` conflict
checks are mandatory before and after pushing the revision artifact.

If the provider does not support conditional updates, step 14 still has a
residual race window. The implementation must record that provider limitation,
keep advisory locks active, and preserve a local unsynced recovery copy whenever
post-update verification detects that `current` does not point to the manifest
the app just published. This is a data-safety limitation of the provider, not a
normal merge conflict.

## Locking Model

Cloud Journals use an advisory lease lock represented as an OCI artifact tagged:

```text
journal-<cloudJournalId>-lock
```

Only the device holding a valid lock may open a cloud Journal read-write.

Lock artifact type:

```text
application/vnd.journal.cloud.lock.v1
```

The lock artifact may use an empty layer descriptor and a config blob containing
the lock payload.

Initial lock config schema:

```json
{
  "format": "journal-oci-lock",
  "formatVersion": 1,
  "cloudJournalId": "uuid",
  "lockId": "uuid",
  "ownerDeviceId": "uuid",
  "ownerDeviceName": "MacBook Pro",
  "ownerAppVersion": "1.0.0",
  "baseRevisionDigest": "sha256:...",
  "createdAt": "2026-07-04T12:15:00Z",
  "expiresAt": "2026-07-04T12:20:00Z",
  "lastRenewedAt": "2026-07-04T12:16:00Z"
}
```

### Lock Acquisition

To acquire a lock:

1. Resolve the current tag.
2. Resolve the lock tag, if present.
3. If no lock tag exists, push a lock artifact and tag it as the lock tag.
4. If the lock exists and belongs to the current device, renew it.
5. If the lock exists and is unexpired for another device, open read-only or
   show a locked message.
6. If the lock exists but is expired, allow a force-takeover flow.
7. After writing the lock tag, re-read it and verify that it resolves to this
   device's lock artifact digest.
8. Confirm the lock payload's `baseRevisionDigest` still matches the current
   tag digest observed by the session. If it does not, treat the session as
   stale and reopen from the new current revision before allowing writes.

OCI tag updates are not a perfect distributed lock. The lock is a product-level
guard, not the only data safety mechanism. Revision conflict detection by
`base_digest` remains mandatory before publishing.

Lock times are based on device clocks, so the app should tolerate modest clock
skew and display lock times as advisory. Force unlock remains available when a
device is lost or its clock is wrong, but it must always preserve local and
remote data until the user explicitly chooses a destructive recovery action.

### Lock Renewal

While a cloud Journal is open read-write:

- Renew the lock periodically, for example every 60 seconds.
- Use a lease duration long enough to tolerate short sleep/network stalls, for
  example 5 minutes.
- Renewal pushes a new lock artifact and updates the lock tag.
- After renewal, re-read the lock tag and verify ownership.
- If renewal fails repeatedly, show sync risk and switch to read-only or prevent
  additional edits after flushing local work.

### Lock Release

OCI registries do not consistently support deleting tags in a portable way. The
first implementation should release a lock by publishing an expired lock artifact
owned by the same device, or by updating the lock tag to a lock payload with:

```json
{
  "released": true,
  "expiresAt": "<time in the past>"
}
```

If the registry supports tag deletion and the lock tag still resolves to this
device's lock digest, deletion may be used as an optimization. Failure to release
is acceptable because locks expire.

The released lock payload must include the normal lock identity fields
(`format`, `formatVersion`, `cloudJournalId`, `lockId`, `ownerDeviceId`) in
addition to `released` and the past `expiresAt`, so clients can validate that
the release came from the device that held the lock.

### Force Unlock

If a lock is expired or belongs to a lost device, the app must support force
unlock.

Force unlock should:

- Show the owner device name and last renewed time.
- Explain that unsynced work from that device may be lost.
- Require explicit confirmation.
- Push a new lock artifact for the current device.
- Update the lock tag to point to the new lock artifact.
- Keep revision conflict checks enabled during publish.

## New Device Recovery

The design must support this scenario:

1. User loses every device that had Journal installed.
2. User installs Journal on a brand new device.
3. User configures the same OCI registry provider or providers.
4. User reconnects or discovers cloud Journal artifacts.
5. User unlocks encrypted cloud Journals with the same Journal master password
   used by the device that created or encrypted them.
6. User resumes editing.

### Recovery Flow

The new device should support:

1. `Reconnect Cloud Journal`.
2. Choose one configured OCI provider to reconnect.
3. List repository tags, following pagination until the listing is complete.
4. Find tags matching:

   ```text
   journal-*-current
   ```

5. Resolve each current tag to an OCI manifest.
6. Validate `artifactType`, config media type, annotations, and config JSON.
7. Download the SQLite layer for the selected Journal.
8. Verify layer digest and size using the OCI descriptor.
9. Record the attachment descriptors listed in the revision config and/or
   `document_attachments` OCI descriptor fields.
10. Open the cached SQLite database and validate schema.
11. Create a cloud mount record in the local app database.
12. If locked:
    - If lock is expired, offer force unlock.
    - If lock is unexpired, allow read-only open or force unlock with stronger
      warning.
13. Open the cached Journal database.

Attachment blobs do not all need to be downloaded during reconnect. The app may
download them lazily by `oci_digest` when images are rendered, while preserving
enough metadata to show missing-image placeholders and retry downloads.

If a provider cannot list tags but can resolve explicit tags, the reconnect flow
should offer a manual recovery path where the user enters a cloud Journal ID or
full current tag. Automatic discovery can be unavailable without making the
stored Journal unrecoverable.

### Recovery Requirements

The new device must not need:

- The old local app database.
- The old local cloud cache.
- Old device IDs.
- Old app settings.
- Any encryption material stored only on the old device.

For encrypted cloud Journals, the OCI revision config and/or cloud Journal
database must contain all wrapped key metadata required to decrypt Journal
content using the user's Journal master password. Correct registry credentials
alone are not sufficient.

## Encryption Requirements

The current app supports Journal-level encryption at the application layer. Cloud
Journals should preserve that model.

For cloud Journals, encryption must be portable across devices that use the same
Journal master password. A cloud Journal must be decryptable from:

```text
OCI revision artifact + user's Journal master password
```

The implementation must not require the original device's local
`encryption_master` row unless that row is also present inside the cloud Journal
database or revision config.

Portable cloud encryption is required before encrypted cloud Journals are
available. If the first user-facing cloud release does not include portable
cloud encryption, the UI must disable encryption actions for cloud Journals and
must reject creation of an encrypted cloud Journal before any artifact is
published.

Required cloud encryption model:

- Each encrypted cloud Journal has a random Journal data key.
- The Journal data key is wrapped by a key derived from the user's Journal
  master password.
- The KDF salt, KDF parameters, verifier, wrapped key nonce, and wrapped key
  ciphertext are stored inside the cloud Journal database or revision config.
- These records are scoped to the cloud Journal. They must not depend on the
  local app database's app-wide `encryption_master` row.
- Changing the local app master password must not silently rewrap or invalidate
  existing cloud Journals. Rewrapping a cloud Journal key for a new master
  password must be an explicit cloud Journal operation that publishes a new
  revision.
- The local app may cache unlocked keys only in memory.
- Local app database records may remember that a mounted cloud Journal is
  encrypted, but must not be the only source of key metadata.
- Cloud Journals use the same master password UX as local encrypted Journals.
- The app must not introduce a separate per-cloud-Journal password in the first
  implementation.
- If the current device has a different Journal master password than the one
  used to wrap the cloud Journal data key, opening the encrypted cloud Journal
  fails with a clear "master password does not match this cloud Journal" error.
- On a new device, unlocking the cloud Journal with the matching master password
  imports no secret into persistent local storage beyond normal mount metadata.

## Backend Architecture

The current `JournalService` owns a single SQLite database. Cloud Journals
require a routing layer that can dispatch operations to a store, while keeping
the journal-content behavior shared.

The goal is not to create separate local and cloud Journal models. The goal is
to make one Journal content service usable against either:

- the local app database, for local Journals;
- a cached cloud Journal database, for one cloud Journal.

Cloud behavior such as locking, dirty state, publication, and registry recovery
wraps the same journal-content service.

Recommended structure:

```text
App
  LibraryCoordinator
    LocalStore
      JournalService over <user-config-dir>/Journal/journal.db
    CloudProviderRegistry
    CloudMountRegistry
    CloudJournalSession(s)
      JournalService over cloud-cache/<cloud-journal-id>/journal.db
      OCIRegistryClient
      LockManager
      RevisionPublisher
```

Recommended service split:

```text
JournalContentService
  - owns item/document/attachment/search/encryption operations
  - runs journal-content migrations
  - has no provider, mount, or app settings responsibilities

AppInstallationService
  - owns app settings
  - owns provider settings
  - owns cloud mount records
  - owns local cache metadata
```

The existing `JournalService` can be refactored toward this split incrementally.
The important invariant is that journal operations do not need to know whether a
Journal is local or cloud except through store routing and write-permission
checks.

### ID Routing

The frontend currently passes plain item IDs and document IDs. With multiple
databases, IDs must route to the right store.

Recommended approach:

- Add a stable `storeId` or `sourceId` to all tree and document responses.
- Keep raw item IDs unchanged inside each SQLite database.
- Prefer structured references for new API methods:

```go
type ItemRef struct {
    StoreID string `json:"storeId"`
    ItemID  string `json:"itemId"`
}
```

- Use composite IDs at the API boundary only as a short-lived compatibility
  bridge:

```text
local:<item-id>
cloud:<cloud-journal-id>:<item-id>
```

Composite IDs must never be stored inside SQLite content, ProseMirror JSON, or
OCI revision metadata. They are transport-only identifiers. If composite IDs are
used temporarily, the backend should centralize parsing and formatting and tests
should cover drag/drop, trash, search results, menu actions, attachment lookup,
and encryption flows.

### Tree Composition

`GetLibraryTree` should return both local and mounted cloud Journals.

Rules:

- Local Journals come from the local app database.
- Mounted cloud Journals come from their cached Journal database when available.
- Cloud Journals that are known but unavailable should appear as locked,
  offline, missing, auth-failed, or needs-download rows.
- Search should handle local and cloud stores separately, then merge results.
- Each cloud Journal should expose storage status:
  - `local`
  - `cloud`
  - `syncing`
  - `dirty`
  - `lockedByThisDevice`
  - `lockedByOtherDevice`
  - `readOnly`
  - `offline`
  - `authFailed`
  - `syncError`

Tree and document responses should expose storage type explicitly:

```go
type TreeItem struct {
    // existing fields...
    StoreID string `json:"storeId"`
    StorageKind string `json:"storageKind"` // "local" or "cloud"
    CloudStatus string `json:"cloudStatus,omitempty"`
}
```

Local Journals should use a stable local store ID such as `local`. Cloud
Journals should use a store ID derived from `cloud_journal_id`.

### Create Journal API

The create-Journal API should accept storage type explicitly.

```go
type CreateJournalRequest struct {
    Title string `json:"title"`
    StorageKind string `json:"storageKind"` // "local" or "cloud"
    CloudProviderID string `json:"cloudProviderId,omitempty"`
}
```

Rules:

- `storageKind = "local"` creates the Journal in the local app database.
- `storageKind = "cloud"` requires `cloudProviderId`.
- If `cloudProviderId` is missing, invalid, or unavailable, the backend rejects
  the request before creating any Journal.
- The storage kind is immutable after creation.
- There is no backend operation to convert an existing Journal between local and
  cloud storage in the first implementation.

### Write Operations

Document write operations should route by document reference:

- `CreateDocument`
- `CreateFolder`
- `RenameItem`
- `MoveItem`
- `MoveItemToTrash`
- `PermanentlyDeleteItem`
- `UpdateDocumentDraft`
- `CreateDocumentAttachment`
- `UpdateDocumentSpacing`
- `FlushDocument`

For cloud Journals, write operations must fail if:

- The Journal is not mounted.
- The cached database is unavailable.
- The session is read-only.
- The lock is missing, expired, or owned by another device.

### Cloud Dirty State

A cloud Journal becomes dirty when its cached SQLite database has local changes
not yet published as an OCI revision artifact.

Dirty state should be set after successful `FlushDocument` and after structural
changes such as rename, move, create, delete, spacing update, or attachment
creation.

Publishing may happen:

- On a timer.
- On document switch.
- On app shutdown.
- On explicit `Sync Now`.

The first implementation should queue a publish after local flush with a
configurable debounce, and always attempt a final publish on close/shutdown.

### Cloud Publish Scheduling

OCI registries are generally optimized for pulling artifacts more than frequent
small pushes. The app must not attempt to publish a new OCI revision after every
local autosave. Local autosave and cloud publish are separate loops:

- Local autosave persists user edits into the cached SQLite database frequently.
- Cloud publish coalesces many local saves into a smaller number of OCI revision
  pushes.

Each provider has default publish timing settings:

- `publish_debounce_ms`: how long the app waits after the most recent local
  change before starting a publish.
- `publish_min_interval_ms`: minimum time between successful publish starts for
  the same Journal.
- `publish_max_interval_ms`: maximum time local changes should remain dirty
  while the registry is reachable and the lock is valid.

Each cloud Journal mount may override those settings. A zero value in
`cloud_journal_mounts` means "use the provider default."

Recommended initial defaults:

```text
publish_debounce_ms:     300000  // 5 minutes
publish_min_interval_ms: 300000  // 5 minutes
publish_max_interval_ms: 1800000 // 30 minutes
```

Rules:

- Never run more than one publish at a time for a Journal.
- If edits happen while a publish is running, mark the Journal dirty again and
  schedule another publish after the current publish finishes and the debounce
  window passes.
- If a publish takes longer than the debounce interval, do not start a second
  publish concurrently.
- If the registry is slow, increase the next publish delay with bounded backoff.
- If a push fails, keep local dirty state and retry later without blocking local
  editing as long as lock policy allows editing.
- If the registry returns HTTP 429, treat it as provider rate limiting, not as a
  generic network failure. The app should pause automatic publishing for that
  provider or Journal, honor `Retry-After` when present, and retry after a
  bounded backoff.
- When rate limiting is detected, the app should alert the user that the cloud
  provider is rate limiting pushes and recommend increasing
  `publish_min_interval_ms` for that provider.
- `Sync Now` bypasses debounce but must still respect "one publish at a time."
- App shutdown should attempt a final publish, but if it cannot finish promptly
  the app should preserve the dirty cached database and resume publishing on the
  next launch.
- The UI should show the difference between "saved locally" and "published to
  registry."

Settings should expose provider-level publish timing in plain terms, for
example:

```text
Publish after no changes for: 5 minutes
Publish at least every:       30 minutes while dirty
```

Advanced per-Journal overrides may be added later, but the backend schema should
support them from the start.

Settings should also show the most recent rate-limit event for a provider, if
any, including the time it occurred and any `Retry-After` value returned by the
registry.

## OCI Registry Client Interface

Implement registry storage behind an interface. The implementation may use an
OCI client library or direct Distribution API calls, but the rest of the app
should depend on this interface.

```go
type OCIRegistryClient interface {
    ValidateProvider(ctx context.Context, provider CloudProvider) error

    ListTags(ctx context.Context, provider CloudProvider) ([]string, error)
    ResolveTag(ctx context.Context, provider CloudProvider, tag string) (OCIDescriptor, error)

    PullManifest(ctx context.Context, provider CloudProvider, ref string) (OCIManifest, OCIDescriptor, error)
    PushManifest(ctx context.Context, provider CloudProvider, tag string, manifest OCIManifest) (OCIDescriptor, error)

    BlobExists(ctx context.Context, provider CloudProvider, desc OCIDescriptor) (bool, error)
    PullBlob(ctx context.Context, provider CloudProvider, desc OCIDescriptor, targetPath string) error
    PushBlob(ctx context.Context, provider CloudProvider, mediaType string, path string) (OCIDescriptor, error)
    PushJSONBlob(ctx context.Context, provider CloudProvider, mediaType string, value any) (OCIDescriptor, error)
}
```

Rules:

- Every blob pull must verify digest and size.
- Every manifest pull must verify digest where the registry returns one.
- Push operations should be idempotent when a blob already exists.
- Attachment upload should check for existing blobs by digest first. Existing
  attachment blobs must be reused rather than uploaded again.
- Registry auth failures must be reported distinctly from missing Journal
  artifacts.
- HTTP 429 responses must be surfaced as a typed rate-limit error that preserves
  the response status and `Retry-After` value when present.
- The implementation must tolerate registries that do not support tag deletion.
- The implementation must not rely on the OCI 1.1 referrers API in the first
  iteration. Referrers support is not yet widespread enough. Use deterministic
  tags for discovery and relationships.

## Frontend Requirements

The frontend should continue to present a unified library tree.

Required first-pass UI additions:

- Create Journal flow with a storage choice:
  - Local.
  - Cloud.
- OCI provider selector in the create-Journal flow when providers exist.
- Empty-provider message in the create-Journal flow when no OCI provider is
  configured, pointing the user to Settings.
- Settings screen for adding, removing, and validating OCI providers.
- Settings must allow multiple providers to be configured and listed at once.
- Settings must allow provider publish timing to be changed.
- Settings must allow provider revision retention to be changed.
- Reconnect cloud Journal flow for new-device recovery.
- Download/open mounted cloud Journal.
- Sync now.
- Show sync status.
- Show lock status.
- Manual re-check for lock release on read-only locked cloud Journals.
- Force unlock.

Explicitly unsupported in the first pass:

- Changing a local Journal into a cloud Journal.
- Changing a cloud Journal into a local Journal.
- Moving a cloud Journal between providers from the Journal tree.

Cloud Journal rows should visually distinguish:

- Local Journal.
- Cloud Journal available for editing.
- Cloud Journal read-only because another device owns the lock.
- Cloud Journal unavailable because registry auth failed.
- Cloud Journal unavailable because cache is missing and the registry cannot be
  reached.
- Cloud Journal publish delayed because the provider is rate limiting pushes.
- Cloud Journal with sync error.

Cloud Journals locked by another device should remain visible in the tree and
open read-only when possible. The UI must provide a manual `Check Lock` or
equivalent action that re-resolves the lock tag and updates the Journal state if
the lock has been released or expired.

Editor behavior should remain mostly unchanged. Autosave still writes locally
first. Cloud publish status is separate from document save status:

- Document save state: draft has been flushed into local cached SQLite.
- Cloud sync state: cached SQLite has been published as an OCI revision artifact
  and the current tag has been updated.

The UI should not claim cloud sync is complete merely because local autosave
succeeded.

## Create Journal Flow

1. User chooses `New Journal`.
2. App shows a storage choice: `Local` or `Cloud`.
3. If `Local` is selected:
   - App creates the Journal in the existing local app database.
   - The Journal is permanently local.
   - Existing local Journal behavior applies.
4. If `Cloud` is selected and one or more OCI providers are configured:
   - User selects one configured OCI provider.
   - App creates a local pending cloud-create record before starting network
     writes.
   - App creates a cached Journal database using the shared journal-content
     schema with one root Journal.
   - App writes revision `0000000001` as an OCI artifact.
   - App tags the revision artifact as:

     ```text
     journal-<cloudJournalId>-rev-0000000001
     journal-<cloudJournalId>-current
     ```

   - App acquires the lock tag:

     ```text
     journal-<cloudJournalId>-lock
     ```

   - App creates a mount record in the local app database.
   - App marks the pending cloud-create record complete.
   - App opens the cloud Journal for editing.
   - The Journal is permanently cloud-managed.
5. If `Cloud` is selected and no OCI providers are configured:
   - App does not create a Journal.
   - App explains that an OCI provider must be configured in Settings first.
   - App offers a direct path to Settings.

If multiple providers are configured, the create-Journal flow must not pick a
default silently. It should require an explicit provider selection, while it may
preselect the most recently used provider.

If creation fails after remote artifacts have been written but before the mount
is complete, the pending record should let the next launch either finish the
mount or show a recoverable partial-create state. The app must not silently
discard the local cached database or hide recoverable remote artifacts.

## Sync And Publish Flow

For an already mounted cloud Journal:

1. Flush pending editor drafts into cached SQLite.
2. Ensure current device owns a valid lock.
3. Resolve the remote current tag.
4. If remote current digest differs from local `base_digest`, stop and show
   conflict/recovery.
5. Publish new revision artifact.
6. Update the current tag to the new revision artifact.
7. Update local mount `base_digest` and `current_digest`.
8. Clear dirty state only after the app has verified that the remote current tag
   resolves to the new revision manifest.

If the registry is unavailable:

- Keep local cached data.
- Keep dirty state.
- Continue editing only if the device still owns a valid lock or the app is in a
  documented offline grace period.
- Show sync error and retry later.

The offline grace period must be explicit. It should define how long editing may
continue without successful lock renewal, what UI warning is shown, and when the
session becomes read-only to prevent accumulating unrecoverable divergence.

## Conflict Handling

Even without simultaneous editing support, conflicts can occur:

- A stale lock was force-unlocked on another device.
- A registry tag was moved manually.
- A registry restored an older tag state.
- Two devices both believed a lock had expired.
- A registry accepted concurrent tag updates without compare-and-swap
  protection.

First implementation conflict policy:

- Never merge automatically.
- Never overwrite a remote current tag if it changed from local `base_digest`.
- Preserve the local cached database as an unsynced recovery copy.
- Show the user:
  - remote current digest
  - local base digest
  - local unsynced copy path
  - options to keep local as a new cloud Journal, discard local changes, or
    export local copy.

Optional later feature:

- Document-level merge for non-overlapping changes using ProseMirror JSON and
  item-level updated timestamps.

## Search

Local app search should query:

- The local app database FTS for local Journals.
- Each mounted and available cloud Journal cache FTS for cloud Journals.

Search results should be merged into one tree response.

Encrypted cloud Journals should follow existing encryption search behavior:

- Persistent FTS must not store decrypted content.
- Locked encrypted Journals are omitted from content search.
- Unlocked encrypted Journals may be searched only with a future in-memory
  session index. The first cloud implementation can omit encrypted content from
  search entirely.

## Backup And Revision Retention

OCI revision artifacts are immutable by application policy. Retention prevents
accidental data loss and helps recovery.

Recommended first policy:

- Keep the latest `revision_retention_count` revision tags per cloud Journal.
- The provider-level default is 50 retained revisions.
- A cloud Journal mount may override the provider retention count. A zero value
  in `cloud_journal_mounts.revision_retention_count` means "use the provider
  default."
- Never delete or untag the artifact referenced by the current tag.
- Never delete or untag revisions while the Journal is dirty or locked by
  another device.
- Never garbage-collect attachment blobs still referenced by any retained
  revision config.
- Cleanup only after a successful publish.

Settings must expose provider-level revision retention.

Registry caveat:

- OCI registries vary in garbage collection behavior. Removing a tag may not
  immediately delete blobs, and some registries may retain untagged blobs until
  administrator garbage collection.

Product caveat:

- Cloud Journals provide synchronized, versioned storage in the configured OCI
  registry. They are not a guaranteed archival backup unless the selected
  registry's retention, billing, access-control, and backup policies provide
  that guarantee. The app should avoid language that implies permanent backup
  beyond the retained revision tags.

## Error Handling Requirements

The implementation must handle:

- Missing repository.
- Authentication failure.
- Authorization failure.
- Registry does not support required OCI APIs.
- Tag listing unavailable.
- Current tag missing.
- Current tag points to unsupported media type.
- Unsupported artifact type.
- Invalid revision config JSON.
- Unsupported database schema version.
- Missing SQLite layer.
- Missing attachment blob.
- Descriptor digest mismatch.
- Descriptor size mismatch.
- Partial blob upload.
- Missing lock tag.
- Lock owned by another device.
- Expired lock.
- Registry unavailable.
- Registry rate limiting pushes with HTTP 429.
- Slow publish exceeding the configured debounce interval.
- Publish already in progress when another local change is saved.
- Disk full in local cache.
- App shutdown during publish.
- Device sleep during lock renewal.
- Concurrent tag update.

All destructive recovery choices must be explicit. The default behavior should
preserve local and remote data.

## Migration Plan

Phase 1: Shared Journal Content Service And Store Routing

- Introduce store IDs or item references.
- Route existing local operations through the coordinator.
- Split migrations into journal-content migrations and app-installation
  migrations.
- Keep the journal-content schema shared by local Journals and cached cloud
  Journal databases.
- Keep app settings, provider state, mount records, and UI convenience state in
  the app-installation schema only.
- Define per-cloud Journal root and Trash validation.
- Keep behavior identical for local Journals.
- Add tests proving local behavior is unchanged.

Phase 2: Portable Cloud Journal Format

- Define cloud Journal scope validation for a full journal-content database.
- Define cloud attachment metadata, local blob cache state, and lazy fetch
  behavior.
- Define portable cloud encryption records and new-device unlock behavior.
- Add tests proving encrypted cloud Journal recovery works with only registry
  artifacts and the matching master password.

Phase 3: OCI Provider And Artifact Format

- Add `cloud_providers`.
- Implement OCI provider validation.
- Record provider capabilities.
- Implement revision config schema.
- Implement revision artifact push/pull.
- Implement digest verification.

Phase 4: Mount And Open

- Add `cloud_journal_mounts`.
- Add pending cloud-create recovery state.
- Add OCI tag discovery/open flow for new-device recovery.
- Open cached cloud Journal DB through the shared journal-content service.
- Compose local and cloud Journals in `GetLibraryTree`.

Phase 5: Locking

- Implement lock artifact create, renew, release, and force unlock.
- Add read-only mode when another device owns the lock.
- Add stale lock recovery.
- Add explicit offline grace-period behavior.

Phase 6: Publishing

- Track dirty cloud sessions.
- Publish revision artifacts after local flush and on shutdown.
- Add conflict detection by `base_digest`.
- Use conditional current-tag updates where provider capabilities allow.
- Update current tags only after conflict checks pass, and preserve local
  recovery copies when verification detects a race.

Phase 7: UI

- Add create-Journal storage choice and cloud status indicators.
- Add Settings OCI provider management.
- Show provider validation and capability status.
- Add sync now, reconnect, and force unlock flows.
- Separate local save state from OCI publish state.
- Add configurable provider publish timing.

## Test Plan

Backend tests:

- Local Journals still work with the coordinator.
- Journal-content migrations run against both local app databases and cached
  cloud Journal databases.
- App-installation migrations do not run against cached cloud Journal databases
  and do not leak app settings, provider state, or mount records into cloud
  revision artifacts.
- Cloud Journal validation accepts exactly one root Journal and the allowed
  per-cloud system Trash shape.
- Add multiple OCI providers.
- Configure provider publish timing.
- Configure provider revision retention, defaulting to 50.
- Validate and record provider capabilities.
- Create cloud Journal revision artifact.
- Resume or recover a partially completed cloud Journal create.
- Reconnect cloud Journal on a fresh local app database using only registry
  access.
- Reconnect by explicit cloud Journal ID or current tag when tag listing is
  unavailable.
- Open, edit, flush, publish, and reopen cloud Journal.
- Verify pulled SQLite layer digest and size.
- Verify unchanged image attachments are not uploaded again after a text-only
  edit.
- Verify cloud Journal open pulls the manifest/config and SQLite blob without
  downloading every attachment blob.
- Verify a second publish does not start while a previous publish is still in
  progress.
- Verify HTTP 429 records provider rate-limit state, honors `Retry-After`, and
  recommends increasing `publish_min_interval_ms`.
- Verify local edits remain saved when registry publish is delayed or fails.
- Verify changing provider publish timing affects the next scheduled publish.
- Verify cloud Journal images can be downloaded lazily by
  `document_attachments.oci_digest`.
- Verify missing attachment blobs show placeholders without preventing document
  text from opening.
- Crash simulation before current tag update leaves previous revision current.
- Remote current tag changed since `base_digest` blocks publish.
- Provider without conditional tag updates preserves a local recovery copy if
  post-update verification detects a race.
- Lock acquisition succeeds with no valid lock.
- Lock acquisition fails with active lock from another device.
- Locked cloud Journal remains visible and opens read-only when possible.
- Manual lock recheck updates read-only state after lock release or expiry.
- Expired lock can be force-taken.
- Lock release publishes released/expired lock state.
- Lost-device recovery works with no old local app database.
- Encrypted cloud Journal unlock works on a new local app database with only the
  registry artifact and matching master password.
- Encrypted cloud Journal unlock fails clearly with a different master password.
- Changing the local app master password does not silently invalidate or rewrap
  mounted encrypted cloud Journals.

Frontend tests or manual verification:

- Local Journals look and behave as before.
- Multiple OCI providers can be configured in Settings.
- Create Journal requires explicit provider selection for cloud Journals.
- Cloud Journal appears in the tree with cloud status.
- Read-only lock state prevents edits.
- Dirty local save and OCI publish status are shown separately.
- Force unlock flow is understandable and hard to trigger accidentally.
- Reconnect flow can discover current tags.
- Sync errors preserve local edits.

## First Iteration Decisions

- Test and document quay.io first.
- Treat cloud support as provider-capability based. Do not imply that every
  OCI-compatible registry has identical locking, tag listing, tag deletion, or
  conditional update behavior.
- Implement portable cloud encryption before enabling encrypted cloud Journals,
  or explicitly disable encryption for cloud Journals in the first release.
- Keep revision retention configurable per provider, defaulting to 50 revisions.
- Show locked cloud Journals in the tree as read-only when possible.
- Provide a manual action to re-check whether a lock has been released.
- Do not rely on OCI referrers in the first implementation.
- Do not implement OCI-compatible artifact signing in the first implementation.
  Signing can be reconsidered longer term.

## Final Design Rule

OCI registry storage holds complete, immutable, recoverable Journal revision
artifacts. The app edits only local cached SQLite databases. The OCI revision
artifact referenced by the current tag, not any local device state, is the
source of truth for a cloud Journal.
