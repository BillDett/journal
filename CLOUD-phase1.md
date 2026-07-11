# Cloud Phase 1: Content, database, and store boundaries

## Objective

Make Journal capable of addressing a specific content store and database without
changing local-only behavior or performing any network I/O. At completion, the
application can host the existing local library and an unopened cloud cache
through the same Journal content service.

This phase implements section 17, Phase 1 of [CLOUD.md](CLOUD.md).

## Scope

- Split portable Journal-content schema from installation-only schema.
- Introduce explicit store routing for every item/document operation.
- Create and validate one-Journal cloud cache databases.
- Add local installation tables for provider, mount, and pending-create state.
- Preserve all current local Journal data and public behavior.

## Out of scope

- Network providers, Vault objects, sync, leases, cloud creation UI, and
  encryption portability.
- Moving an existing local Journal into a cloud Journal.
- Combining multiple cloud Journals into one SQLite cache.

## Design decisions to lock before coding

1. Use `StoreID` as an opaque backend identifier. It must not be inferred from
   an item ID.
2. Use `local` for the installation database and `cloud:<cloud-journal-id>`
   for a cloud cache store.
3. Keep SQLite item UUIDs unchanged. Composite IDs are transport routing only.
4. A cloud cache database contains one cloud Journal root and one system Trash.
5. A cloud cache never contains provider credentials, UI settings, or mount
   metadata.
6. Storage kind is assigned only at Journal creation and is immutable. There is
   no migration, copy, or API path that converts an existing local Journal to
   cloud storage (or a cloud Journal to local storage).

## Work breakdown

### 1. Inventory and classify existing schema

Create `schema_content.go` and `schema_installation.go` (or equivalent) with
named migration groups.

- Move `items`, `documents`, `document_attachments`, Journal encryption
  records, and optional FTS into the content migration group.
- Move `app_settings` and all new provider/mount tables into the installation
  migration group.
- Keep current local `journal.db` compatible by applying both groups there.
- Make cache creation apply only the content group plus cloud metadata.
- Add a schema version table or `PRAGMA user_version` policy independently for
  installation and content databases; do not reuse one version value for two
  different database roles.

### 2. Add explicit store abstractions

Define narrow interfaces; do not expose raw `*sql.DB` to Wails methods.

```go
type StoreID string

type JournalStore interface {
    ID() StoreID
    Database() *sql.DB // internal service use only
    Kind() StoreKind   // Local or Cloud
    Close() error
}

type StoreRouter interface {
    Resolve(ctx context.Context, id StoreID) (JournalStore, error)
    Local() JournalStore
}
```

- Refactor `JournalService` construction to accept a `JournalStore` or an
  explicit content database dependency.
- Keep one service instance per opened store; do not switch databases inside a
  mutable global service.
- Add a `StoreScopedID` request field or change RPC methods to take `storeID`
  separately from an item ID.
- Reject an item operation when the item is absent from the selected store.

### 3. Define cloud cache metadata and layout

Implement deterministic cache paths:

```text
<config>/Journal/cloud-cache/<cloud-journal-id>/
  journal.db
  vault-state.json
  blobs/sha256/
```

`cloud_journal_metadata` belongs in the cache content database and contains at
least `cloud_journal_id`, content-format version, and creation metadata. It
must not contain endpoint or credential data.

Define `vault-state.json` as a replaceable, non-authoritative local convenience
record only (for example, last observed pointer/revision and transient cache
health). It must be regenerated from the installation mount record and remote
pointer when missing, malformed, or stale; no operation may treat it as proof
of a published revision or valid lease.

Add installation migrations for:

- `vault_providers` exactly as described in `CLOUD.md` section 7;
- `cloud_journal_mounts`;
- `cloud_pending_creates` with cache path, provider ID, cloud Journal ID,
  stage, last error, and timestamps.

Add installation-scoped device identity storage (a generated stable UUID and
user-editable safe display label). Generate it once per installation before a
cloud create/lease operation, resolve it only from the installation database,
and never copy it into a cache, revision database, or provider configuration.

Implement repository methods with explicit lifecycle rules: removing a provider
removes local provider configuration only; it never sends a remote delete.
Reject provider removal while an associated mount has unsynced work unless the
user first preserves that cache through an explicit recovery/export workflow.
Do not delete a cache as an implicit side effect of provider removal.

When removal is allowed for a clean mount, retain its cloud Journal ID, vault
root, cache path, and last-known state, and transition the mount to an explicit
`provider_missing`/unavailable state rather than deleting it or leaving a
broken foreign key. It can be reconnected only by selecting and validating a
new local provider configuration for the same vault root; that reassociation
must not be implicit.

### 4. Enforce cloud-root invariants

Implement `ValidateCloudJournalScope(store)` and run it when opening any cloud
cache.

- Exactly one top-level `kind = journal` row exists.
- The cache Journal ID equals `cloud_journal_metadata.cloud_journal_id`.
- Exactly one system Trash exists and is outside the Journal root as required
  by the selected content schema.
- No unrelated top-level Journal, app setting, provider, or mount row exists.
- The cache database is rejected before use if validation fails.

### 5. Route current operations

Introduce store-aware equivalents for all existing backend operations:

- tree, search, create, rename, move, trash, delete;
- document open/draft/flush/spacing;
- attachment create/read/reconcile;
- encryption status and Journal encryption actions.

At the routing boundary, reject a composite cloud item/document ID whose cloud
Journal component does not equal the selected store. Apply the cloud-root and
system-Trash scope predicate to every cloud content query/mutation, not merely
when the cache opens, so a malformed RPC cannot address another root if cache
contents later become corrupt.

The local Wails path initially passes `storeID = local`. Preserve old frontend
behavior behind adapter compatibility methods while the frontend migrates.

### 6. Cache lifecycle primitives

Implement non-network helpers:

- `CreateCloudCache(cloudJournalID)` creates a staging database with content
  migrations and validates it before atomically installing it.
- `OpenCloudCache(cloudJournalID)` opens, validates, and registers a store.
- `RemoveCloudCache` refuses when the mount is dirty or pending-create.
- `StageCacheReplacement` supports later download/recovery work by validating a
  staged database before atomically replacing an inactive cache.

## API and model changes

- Add `storageKind`, `storeID`, `cloudStatus`, and `readOnly` to tree response
  models, initially populated only for local rows.
- Add typed errors: `store_not_found`, `store_item_not_found`,
  `cloud_scope_invalid`, `cache_corrupt`, and `cache_missing`.
- Add internal `StoreRouter` dependency to the Wails application composition
  root; do not expose it directly to React.

## Migration and compatibility plan

1. Back up or transactionally migrate the current installation database.
2. Apply installation plus content migrations to the local database.
3. Verify all existing Journals appear as `storageKind = local`.
4. Create a test cloud cache only through the new cache factory.
5. Do not add UI controls that create cloud Journals in this phase.

## Tests

- Existing local tests run unchanged against the `local` store.
- Content migrations create a valid cache database without installation tables.
- Installation migrations do not run against a cache database.
- Cache validation rejects multiple roots, missing Trash, wrong root ID, and
  installation-only tables where prohibited.
- The same item UUID in a local store and cloud store cannot cross-read or
  cross-write.
- Cache replacement leaves the original cache intact on validation failure.
- A missing, malformed, or stale `vault-state.json` cannot grant write access
  or change the selected remote revision; it is rebuilt from authoritative
  cache/mount/remote data.
- Store closure and reopen do not leak SQLite handles.
- Attempts to convert storage kind, remove a provider with dirty work, or
  remove a cache as a provider-removal side effect are rejected without data
  loss or remote requests.
- Removing a provider from a clean mount retains a visible
  `provider_missing` mount/cache that requires explicit validated reconnect.

## Completion criteria

- Local Journal behavior, tests, and packaged app remain unchanged.
- Every content operation has an explicit store path internally.
- A validated one-Journal cloud cache can be created and opened offline.
- No provider credential, mount, or UI data can be written into a cache DB.
- All migrations and cache-invariant tests pass.
