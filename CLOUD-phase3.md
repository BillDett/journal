# Cloud Phase 3: Journal Vault protocol and first provider

## Objective

Implement the Journal Vault protocol end-to-end for one provider type. A user
can create a cloud Journal, publish immutable revisions, recover it by cloud
Journal ID on a new device, and safely handle lease and pointer conflicts.

Choose one provider with all required capabilities. Recommended first target:
an S3-compatible provider with conditional control-object writes and immediate
read-after-write consistency. Do not begin a second provider in this phase.

## Scope

- Vault current-pointer, revision-manifest, and lease codecs.
- First `VaultStore` implementation and provider capability validation.
- Create, pull, lease, publish, retry, conflict, and recovery workflows.
- Local provider/mount/pending-create persistence.

## Out of scope

- Provider settings UI and broad tree/search integration (Phase 4).
- Second provider, advanced cancellation, provider discovery, and diagnostic
  export (Phase 5).
- Manual attachment reclamation; retain blobs append-only.

## Work breakdown

### 1. Implement strict Vault codecs

Create package/file ownership such as `vault/format.go`.

Implement typed models and validation for:

- `current.json`, including cloud Journal ID, revision ID/number, descriptor,
  previous revision ID, and portable encryption metadata version;
- immutable revision manifest, including database and attachment descriptors;
- `lease.json`, including lease ID, device ID, owner label,
  acquisition/expiry times, and observed current-pointer token.

Validation requirements:

- reject unknown required format versions;
- require canonical `sha256:<hex>` digests and non-negative sizes;
- require descriptor keys beneath the expected Journal prefix;
- reject duplicate attachment digests/keys and mismatched Journal IDs;
- bound JSON document size and string lengths before unmarshalling;
- serialize deterministically for test fixtures and digest comparison.

Add one authoritative vault-key builder. It accepts only a validated cloud
Journal ID, revision ID, and SHA-256 digest and produces the exact logical keys
from `CLOUD.md` section 5. No caller may concatenate provider roots or object
keys directly. Generate a non-reused UUIDv7 (or equivalent time-sortable random
identifier) for each candidate revision and derive a strictly monotonic
`revisionNumber` from the pointer-confirmed parent. A manifest's
`parentRevisionId` and a pointer's `previousRevisionId` must both name that
parent. Permit a manifest title hint only when the active encryption policy
allows that specific metadata to be remote-visible.

### 2. Build the provider adapter

Implement the Phase 3 provider behind `VaultStore` only. Keep SDK/API types
inside the adapter package.

Required operations to implement and test against a disposable test prefix:

- credential/root validation;
- immutable put with collision verification;
- immutable get/head with size/digest verification;
- control get with an opaque version token;
- conditional control create;
- conditional control replacement;
- categorized error translation for auth, unavailable, precondition conflict,
  rate limit, and provider capability failure.
- the optional `VaultMaintenanceStore` operations for complete prefix listing
  and version-aware immutable deletion when the selected first provider can
  safely support them; advertise their absence as capabilities rather than
  emulating deletion unsafely.

Capability validation must perform harmless probe objects under a private
validation prefix and clean them up only when deletion is available. A failed
probe must prevent cloud creation before a cache, mount, or Journal Vault
object is created. Map adapter failures to the public typed statuses in
`CLOUD.md` (`provider_unavailable`, `provider_auth_failed`,
`provider_capability_missing`, `sync_rate_limited`, and so on), while retaining
only sanitized transport detail for diagnostics.

Define one translation table at the service boundary: unavailable maps to
`provider_unavailable`; authentication/authorization to `provider_auth_failed`;
missing required semantics to `provider_capability_missing`; an unexpired
other-device lease to `lease_held`; failed/expired self lease to `lease_lost`;
stale conditional pointer writes to `current_pointer_conflict`; descriptor
integrity failures to `digest_mismatch`; and rate limits to
`sync_rate_limited`. Do not make UI or retry decisions from provider response
text.

### 3. Implement VaultSyncService

Compose a service with dependencies:

```go
type VaultSyncService struct {
    Store VaultStore
    Caches CloudCacheManager
    Mounts MountRepository
    Clock Clock
    Device DeviceIdentity
}
```

It owns network operations, state transitions, retry scheduling, and typed
errors. `JournalService` remains responsible only for one cache database.

Define mount states and legal transitions:

```text
clean -> dirty -> syncing -> clean
dirty -> offline|auth_failed|rate_limited|conflict
clean|dirty -> locked_read_only
any -> cache_corrupt
```

Introduce `CloudJournalStore` as the store wrapper selected by `StoreRouter`.
Before forwarding any content mutation to `JournalService`, it must require an
existing, scope-valid cache, a writable mount, a non-conflict state, and a
currently valid local lease. Return typed `cache_missing`, `cache_corrupt`,
`lease_lost`, or `current_pointer_conflict` errors rather than relying on UI
disablement. Read paths may continue from a valid cache while offline or
another device owns the lease.

Own automatic-publication scheduling here, immediately after a durable local
autosave marks a writable mount dirty. Read the provider/mount debounce and
maximum-dirty-interval values; default them to 30 seconds and five minutes.
Coalesce repeated edits, reset the debounce on new edits, and force one publish
when the dirty interval reaches its maximum. **Sync now** bypasses only the
debounce; it still enters the same lease/current-token/integrity state machine.
Do not schedule automatically when the mount is locked, read-only, conflicted,
or already has a pending retry that supersedes the new timer.

### 4. Lease implementation

Implement acquire, renew, release, and inspect exactly as specified in
`CLOUD.md` section 11.

- Use provider conditional create/replace; never rely on a local mutex.
- Use server/provider time where available; otherwise account for clock skew
  conservatively.
- Persist lease ID and expiry locally only as convenience data.
- On acquire, read `current.json` after obtaining the lease and write that
  observed pointer token into both the lease document and mount state before
  allowing a publish.
- Renew at one-third of the five-minute default duration with jitter; renewal
  runs independently from publication.
- When renewal is uncertain, block new cloud writes before expiry and preserve
  local drafts/cache.
- Release on explicit user action and best-effort app/store shutdown by
  conditionally replacing this device's lease with an already-expired document;
  do not require deletion and never mutate another device's lease.
- Force unlock may replace only an expired lease after the configured safety
  grace period, must use the observed lease token conditionally, and must warn
  that the former owner can have unsynced work.

### 5. Publish implementation

Implement the section 10 sequence as an explicit state machine, not one large
method:

1. preflight mount/lease/current token;
2. flush cache and stage SQLite snapshot;
3. collect/upload missing attachment blobs;
4. upload snapshot and immutable manifest;
5. read-back validate immutable objects;
6. conditionally update current pointer;
7. re-read and validate the current pointer, verify it names the candidate
   revision and record its returned token;
8. persist clean mount state only after that verification;
9. trigger per-Journal bounded revision cleanup asynchronously when the
   provider exposes safe maintenance capabilities.

Persist a resumable local operation record before remote work begins. It must
identify the candidate revision, staged paths, and last completed step. An
interrupted candidate may leak remote immutable objects; it must never be
treated as current without a successful pointer update.

For every immutable object, compare the exact streamed-byte SHA-256 and size
to its descriptor before upload, verify a successful put through returned
metadata/read-back, and verify an existing collision before reusing it. On
download, stream to staging, compare the resulting digest/size to the manifest
descriptor, and only then install it in a cache. Validate `current.json` and
`lease.json` with their strict codecs on every read; a control document is not
trusted merely because the provider returned it. Preflight must compare the
fresh current-pointer token to the mount's observed token before any candidate
upload begins.

### 6. Pull and recovery implementation

Implement `ReconnectCloudJournal(providerID, cloudJournalID)`:

- resolve and validate current pointer;
- download manifest/database into a staging cache;
- verify descriptor digests and Journal scope;
- atomically install the cache and create mount metadata;
- inspect/acquire lease and open read-only when another owner is active.

Do not require provider listing for explicit-ID recovery.

Implement `OpenMountedCloudJournal(cloudJournalID)` separately from reconnect:
read and validate the remote pointer on each open, compare it to the mounted
revision/token, and stage/download a replacement cache when the cache is
missing or behind. Verify every descriptor before an atomic cache swap, then
inspect/acquire the lease. Open read-write only while this device has a valid
lease; otherwise open a valid cache read-only with the owner/expiry state.

Implement cloud creation as a persisted state machine: validate provider;
create pending record; generate IDs and a portable cache; conditionally acquire
the initial lease; publish revision number 1; re-read the pointer; then commit
the mount. Retry can resume the recorded safe stage. Discard preserves remote
objects unless exclusive ownership of an incomplete creation can be proven.

### 7. Conflict and retry behavior

- Conditional pointer failure creates `current_pointer_conflict` and stops
  automatic publication.
- Preserve cache and staged local recovery data.
- Retry transient provider errors with bounded exponential backoff and jitter.
- Respect provider `Retry-After` when available.
- Do not retry authentication, capability, integrity, or conflict errors until
  explicit user action changes their cause.

## Test matrix

- Provider capability probes: success, missing conditional write, bad auth,
  missing root permission, and stale control token.
- Lease acquire/renew/release/expiry/competing device/force unlock.
- Publish happy path, upload retry, pointer conflict, and crash after each
  persisted operation step.
- Immutable candidate upload without pointer update never changes recovery.
- Vault-key builder rejects traversal/cross-Journal keys; revision IDs are
  unique and revision numbers/parent links are monotonic and valid.
- Cloud create validates the provider before creating cache or Journal Vault
  state; pending-create retry/discard preserves recoverable work safely.
- Opening an existing mount replaces a stale/missing cache only after complete
  staged verification and opens read-only without a valid lease.
- Cloud write RPCs are rejected by `CloudJournalStore` for missing/corrupt
  cache, read-only mount, lost lease, and conflict state.
- Pull detects database, manifest, and attachment digest mismatch.
- Offline, authentication, and rate-limit failures preserve dirty cache work;
  uploads deduplicate an already verified attachment blob; revision cleanup
  never deletes blobs from expired or unreachable candidate revisions.
- Autosave publication coalesces at the 30-second default, forces at the
  five-minute maximum dirty interval, and **Sync now** still rejects a lost
  lease/current-token conflict. Every immutable upload/download and every
  control-document read is integrity/codec validated before use.
- Device identity is stable across restart, is present in lease/manifests where
  required, and is absent from portable cache/revision content.
- Serialized revision objects contain no installation provider configuration,
  credential reference, cache path, UI preference, or device-local state.
- New-device recovery works with explicit Journal ID and no original app DB.
- Encrypted recovery works with matching password and fails clearly otherwise.
- Retention keeps current plus N-1 revisions; failed cleanup leaks safely.

## Completion criteria

- A developer can create, edit, publish, delete local cache state, reconnect,
  and recover the same cloud Journal through the first provider.
- No write occurs without a valid lease and stable current-pointer token.
- All remote state transitions are crash-safe, observable, and covered by
  deterministic fake-provider tests plus integration tests against the chosen
  provider.
