# Cloud Phase 4: Product integration and user workflows

## Objective

Expose the completed cloud backend as a coherent desktop feature. Users can
configure providers, create and reconnect cloud Journals, understand their
state, sync manually, resolve conflicts, and manage bounded revision history.

## Prerequisites

- Phase 3 end-to-end create/pull/publish/recovery works through backend APIs.
- All backend state changes return typed status and typed errors.

## Work breakdown

### 1. Define Wails RPC contracts

Add transport-friendly request/response models in the API contracts file.

Required operations:

- provider list/create/update/remove/validate;
- cloud Journal create and pending-create resume/discard;
- mounted cloud Journal list/open/reconnect;
- sync now, retry, release lease, inspect lease, force unlock;
- conflict detail and explicit conflict-resolution commands;
- revision retention get/update and clean old revisions;
- list retained revisions and restore a selected retained revision as a new
  publication;
- attachment-reclamation preview and confirmed execution when supported.

Every mutation returns the affected mount/tree status. Do not expose provider
SDK errors or raw credentials across the Wails boundary.

### 2. Compose local and cloud library trees

Update tree APIs to compose:

- local Journals from the installation store;
- mounted cloud Journals from available cache stores;
- unavailable cloud mount placeholders when cache cannot open.

For cloud rows expose `storageKind`, `cloudStatus`, `readOnly`, and a concise
status reason. Preserve existing tree ordering and local Journal behavior.

Opening a cloud Journal must route through `StoreRouter`; it must never open a
cloud item using the local store by accident.

### 3. Provider settings UI

Add a Settings section with:

- provider name, kind, endpoint, root prefix, credential entry/reference;
- validate button and capability summary;
- edit and remove actions;
- warnings that removal affects only local configuration;
- per-provider publish debounce, max interval, and default revision count.

When a provider is removed, retain affected clean mounts as visible
`provider_missing` entries with a reconnect action; do not make them disappear
from the tree or imply that their remote Vault was deleted. A dirty mount must
direct the user to preserve/export its recovery copy before provider removal.

Initialize the publication defaults to a 30-second debounce and a five-minute
maximum dirty interval. Validate revision retention input at both RPC and UI
boundaries: a per-mount positive value overrides the provider default; the
effective value is always at least two and defaults to 50.

Credential values must not be rendered after save and must use platform secure
storage or a documented secure reference strategy.

### 4. Create and reconnect workflows

Create Journal dialog:

- Local remains the default and existing flow.
- Cloud requires selecting a validated provider.
- Show provider capability/auth errors before initiation.
- Disable cloud encryption until Phase 2 portable encryption is available.
- Show pending-create progress and recovery actions if app restarts.

Reconnect dialog:

- select a provider;
- enter a cloud Journal ID;
- show download/validation progress;
- report wrong master password, corrupt remote data, missing Journal, or lease
  owner without exposing secrets.

Add a diagnostic mount view that shows the local cache location, last
pointer-confirmed revision, lease/read-only state, and whether unsynced local
recovery work exists. It must provide a non-destructive way to locate or export
that recovery copy without implying that the cache is the remote source of
truth.

### 5. Editing, sync, and read-only behavior

- Show independent indicators for local save and cloud publication.
- Debounced automatic publish starts only after local save is durable.
- **Sync now** bypasses debounce but not lease, conflict, or integrity checks.
- Locked/read-only journals disable every write affordance, including title,
  drag/drop, attachments, encryption change, and destructive actions.
- Lease renewal risk is prominent before editing becomes blocked.
- Offline dirty state preserves normal editing but clearly states that remote
  recovery is behind.

### 6. Conflict and maintenance UI

Conflict screen must show local base revision and current remote revision plus:

- pull/discard local cache;
- keep local content as a new cloud Journal;
- export local recovery copy;
- cancel without destructive change.

Revision retention controls:

- set effective N with a minimum of two;
- show that current revision is always retained;
- offer **Clean old revisions** as a best-effort action.

Implement the associated backend command in this phase, not only the control:
after a verified pointer-confirmed publication (or explicit cleanup request),
require a valid write lease and a clean/verified cache, list the Journal's
revision manifests through `VaultMaintenanceStore`, read the current pointer
before every delete batch, and walk the current manifest's
`parentRevisionId` chain to identify pointer-confirmed history. Keep current
plus the newest `N - 1` confirmed predecessors ordered by `revisionNumber`.
Delete only out-of-set manifest/database pairs, never blobs; stop immediately
if the pointer token changes. Missing listing/deletion capability or any
failure leaves excess objects and must not make publication fail.

Expose retained history as read-only metadata. **Restore** downloads/validates
the chosen retained revision into staging, makes it the editable cache only
after normal lease/preflight checks, and publishes its content as a new
revision with the next monotonic revision number. It must never point
`current.json` backward to the historical revision object.

Attachment maintenance:

- preview each candidate's key, digest, and size plus count/total bytes; do
  not invent attachment names for orphaned blobs;
- allow preview-only scans when complete listing is available but conditional
  deletion is not; label the plan non-executable and keep **Remove** disabled;
- require explicit confirmation for the exact plan;
- disable deletion when provider maintenance capabilities are missing;
- display partial success as unreclaimed storage, never as a data-loss error.

### 7. Search and accessibility

- Search each available store independently and merge results with store-aware
  navigation.
- Exclude unavailable caches and locked encrypted content in the first cloud
  release; search only unlocked, available stores and label the result source.
- Ensure statuses and destructive confirmation dialogs are keyboard accessible,
  announced to screen readers, and do not rely on color alone.

## Tests

- React/component tests for each sync status and read-only disablement.
- End-to-end flows using a fake Vault provider: create, edit, publish,
  reconnect, locked open, offline dirty, conflict, and recovery.
- Verify provider removal does not delete remote state or an unsynced cache.
- Verify clean provider removal leaves a visible `provider_missing` mount that
  reconnects only after explicit provider validation.
- Verify every cloud action carries store ID and cannot affect local Journal IDs.
- Test crash/reload while creation, download, publication, or maintenance plan
  is active.
- Test effective retention defaults/overrides, cleanup pointer-token change,
  failed listing/deletion, retained-history display, and restore producing a
  new monotonic revision rather than a backward pointer move.
- Test diagnostic mount state exposes recovery-copy location/status without
  content or credentials.
- Test local/cloud tree composition and merged store-aware search, including
  unavailable mounts and the defined exclusion behavior for locked encrypted
  content.

## Completion criteria

- A non-technical user can configure the first provider, create a cloud
  Journal, sync it, recover it on another device, and understand any failure.
- Local-only workflows remain visually and behaviorally simple.
- Cloud state is never represented as “synced” before pointer-confirmed publish.
