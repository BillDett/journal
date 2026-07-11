# Cloud implementation plan: Phase 5 — Additional providers and hardening

## Objective

Demonstrate that the Journal Vault Protocol is independent of any one transport, then make cloud synchronization safe and supportable under real-world failures. This phase adds one additional provider without changing protocol semantics, establishes provider conformance testing, and hardens cancellation, retries, diagnostics, maintenance, and operational limits.

Phase 5 is complete only when a journal can use either supported provider interchangeably through the same `VaultStore` contract, interrupted work does not corrupt remote state, and a supportable diagnostic record can explain a failed synchronization without exposing journal content or secrets.

## Prerequisites

- Phase 1 provides isolated cloud cache directories, journal identity, mount records, and cache validation.
- Phase 2 provides portable encrypted database snapshots, encrypted attachment blobs, and deterministic digests.
- Phase 3 provides the Journal Vault Protocol, the first provider, leases, pointer-conditional publication, recovery, and a provider test fake.
- Phase 4 provides product-facing sync state, settings, reconnect flows, conflict presentation, and operation progress display.

Do not start provider expansion until the phase 3 provider passes its conformance suite against both the fake store and the real service used in development.

## Scope

This phase delivers:

1. A provider conformance suite that is run unchanged against every `VaultStore` implementation.
2. One second provider implementation. Prefer WebDAV because it is widely self-hostable, maps naturally to the Vault object layout, and exercises different conditional-write semantics from the first S3-compatible provider.
3. A resumable, cancellable cloud-operation runner with bounded concurrency, progress reporting, retry classification, and rate-limit handling.
4. Redacted diagnostics and operational guidance for users and support.
5. Hardening of manual attachment reclamation, including provider-version-aware deletion and restart-safe local reclamation plans.
6. Large-library, fault-injection, and interoperability test coverage.
7. An optional, privacy-preserving discovery/share workflow for providers that
   can enumerate a vault root, without turning Vaults into multi-writer
   collaboration.

The phase must not add multi-writer merging, live collaborative editing, background mutation of an unlocked journal, automatic attachment deletion, provider-wide garbage collection, or provider-specific behavior visible in the Journal Vault data format.

## Fixed decisions

- A provider is a transport adapter, not a new journal format. All providers store exactly the object keys and canonical bytes defined in `CLOUD.md`.
- The second provider must meet the same immutable-object, conditional-pointer, and lease requirements as the first provider. If a server cannot meet a required semantic, it is unsupported rather than silently downgraded.
- The application keeps its bounded revision policy: current revision plus the newest configured retained revisions. Provider-native file history, versioning, lifecycle, or trash features are optional defense in depth only.
- A canceled or failed operation may leave immutable, unreachable objects. It must not advance `current.json`, delete a retained revision, or delete an attachment.
- A complete remote object listing is mandatory for a destructive attachment-reclamation run. If a provider cannot enumerate the configured vault prefix reliably, reclaim is unavailable for that provider.
- Diagnostics are metadata-only and must be safe to attach to a support issue after explicit user review.

## Workstream A: freeze and test the provider contract

### A1. Make provider capabilities explicit

Extend the provider model with a capability record obtained during connection validation. Do not infer capabilities from provider type in the sync service.

```go
type VaultCapabilities struct {
	ConditionalWrite     bool
	ConditionalDelete    bool
	ObjectListing        bool
	RangeRead            bool
	ServerClockAvailable bool
	MaxObjectBytes       int64 // 0 means provider did not report a limit
}

type ValidatedVaultProvider struct {
	Provider     VaultProvider
	Capabilities VaultCapabilities
	ValidatedAt  time.Time
}
```

Connection validation must fail before a cloud journal can be created or reconnected unless `ConditionalWrite` is true. The attachment-reclamation UI must additionally require `ObjectListing` and `ConditionalDelete`.

Keep capability detection adapter-local. Persist only capabilities that affect whether an existing mount can be used; mark them stale after a configurable validation interval or credential change.

### A2. Define a reusable contract suite

Create a provider-neutral test package such as `internal/cloud/vaulttest`. It receives a factory which creates an empty isolated vault prefix and a cleanup callback controlled by the test harness.

```go
type StoreFactory func(t *testing.T) (store VaultStore, provider VaultProvider)

func RunVaultStoreContract(t *testing.T, factory StoreFactory)
```

The contract suite must cover:

- exact object-key construction and prefix confinement; keys supplied by application code cannot escape the selected vault root;
- read-after-write behavior for immutable objects;
- immutable create succeeds once and rejects a second write with different bytes;
- idempotent retry of the same immutable bytes is recognized safely, using digest verification when the transport lacks a native create-only primitive;
- `current.json` creation, conditional replacement, and stale expected-version rejection;
- lease creation, lease renewal, stale lease rejection, and lease replacement only after expiration under the protocol’s conservative time rules;
- object metadata/version values are stable enough for a conditional follow-up mutation;
- cancellation closes in-flight requests and returns an error class that the operation runner does not retry automatically;
- missing objects, authentication failures, unavailable service, rate limiting, request timeouts, and malformed provider responses map to typed errors;
- listing is complete, prefix-scoped, paginated correctly, and never returns keys outside the configured vault root;
- conditional deletion fails when the expected version is stale;
- provider errors never contain credentials, authorization headers, or plaintext journal data.

Run this suite against the in-memory/fake store in unit tests and against each real provider adapter in opt-in integration tests. Add a test configuration mechanism using environment variables or a local disposable service; skip integration tests with an explicit reason when its configuration is absent.

### A3. Normalize provider errors

Expose a narrow typed error vocabulary to `VaultSyncService` rather than leaking HTTP/WebDAV/SDK errors through the application.

```go
type VaultErrorKind string

const (
	VaultUnauthorized      VaultErrorKind = "unauthorized"
	VaultNotFound          VaultErrorKind = "not_found"
	VaultAlreadyExists     VaultErrorKind = "already_exists"
	VaultPrecondition      VaultErrorKind = "precondition_failed"
	VaultRateLimited       VaultErrorKind = "rate_limited"
	VaultUnavailable       VaultErrorKind = "unavailable"
	VaultTimeout           VaultErrorKind = "timeout"
	VaultCanceled          VaultErrorKind = "canceled"
	VaultMalformedResponse VaultErrorKind = "malformed_response"
	VaultUnsupported       VaultErrorKind = "unsupported"
)
```

Each typed error must retain a safe operation label, provider type, remote status code if applicable, retry-after duration when supplied, and the wrapped cause for local logs only. Its user-facing string must not include endpoints containing embedded credentials, raw headers, or response bodies.

## Workstream B: implement the second provider

### B1. Choose and delimit WebDAV support

Implement a WebDAV adapter at `internal/cloud/providers/webdav` behind `VaultStore`. Support HTTPS endpoints and an explicitly configured vault root path. Basic authentication may be accepted only through the existing secret-storage abstraction; never store a password in mount metadata or diagnostic exports.

The provider configuration should contain only:

```go
type WebDAVProviderConfig struct {
	EndpointURL string // normalized HTTPS origin and path; user info prohibited
	VaultRoot   string // normalized relative path beneath the endpoint
	CredentialID string // keychain/secret-store reference, never the credential itself
}
```

Validation must reject non-HTTPS endpoints by default, URL userinfo, query strings, fragments, empty roots, path traversal segments, and roots that cannot be normalized unambiguously. If development needs plain HTTP, guard it behind a compile-time or developer-only setting that cannot be enabled in production builds.

### B2. Map protocol operations carefully

Map `VaultStore` operations to WebDAV requests without changing the logical protocol.

| Vault operation | WebDAV behavior | Required protection |
| --- | --- | --- |
| `PutImmutable` | `PUT` to a new key | Conditional create (`If-None-Match: *`) or verify an existing object's digest before treating a retry as successful. |
| `Get` | `GET` / optional range `GET` | Enforce size limits while streaming; validate expected digest at the protocol layer. |
| `PutCurrentIfVersion` | `PUT current.json` | Use ETag preconditions (`If-Match` / `If-None-Match`) and reject servers that do not honor them. |
| `AcquireOrRenewLease` | Conditional `PUT lease.json` | Read/validate current lease, then use ETag precondition. Never use a non-conditional read-modify-write. |
| `ListPrefix` | `PROPFIND Depth: 1` plus pagination/vendor extension if applicable | Require complete recursive enumeration for the configured blob prefix; otherwise mark maintenance unsupported. |
| `DeleteImmutableIfVersion` | `DELETE` | Use `If-Match` with the discovered ETag; never delete without a matching version. |

Some WebDAV servers have incomplete, vendor-specific, or incorrect ETag/precondition behavior. The adapter must probe the exact server/root during validation using a disposable `.journal-probe/` object namespace, then remove the probe objects. Treat failure to prove conditional semantics as an unsupported provider configuration. Do not attempt an unsafe fallback.

### B3. Provider registration and migration

Register WebDAV through the provider factory/registry introduced in Phase 3. The factory converts persisted non-secret mount configuration into a `VaultProvider` and resolves credentials only when an operation begins.

Existing journals are unchanged. Creating a new cloud journal or reconnecting an existing one must present the provider-specific configuration form, run validation, show the capability result, and save only normalized public configuration plus a secret reference.

Add a migration only if the mount schema needs a provider-config envelope. Version and validate the envelope so new providers can add fields without accepting unknown security-sensitive values:

```json
{
  "providerType": "webdav",
  "schemaVersion": 1,
  "config": {
    "endpointURL": "https://vault.example.net/journal",
    "vaultRoot": "journals/2b2e...",
    "credentialID": "secret-ref"
  }
}
```

Do not embed a provider SDK-specific configuration blob in the journal’s remote vault. Provider configuration is local installation metadata, not portable journal data.

### B4. Interoperability proof

Use a test harness that performs these steps against both supported providers:

1. Create a cloud journal and publish revision 1 with text and attachments.
2. Open it with a fresh local installation/cache and verify database and attachment digests.
3. Publish revision 2, recover from a simulated interruption before pointer advance, then recover and publish again.
4. Attempt a stale-pointer publish and verify a conflict without pointer loss.
5. Retain the configured number of revisions and verify only expired revision manifests/database objects are removed.
6. Generate an attachment reclamation preview and exercise a conditional-delete race to prove a changed object is not removed.

Use compatible test data and assert the resulting canonical `current.json`, revision manifests, and encrypted object digests are valid for either provider. They need not have byte-identical ciphertext across independent journal creations; they must satisfy the same format and validation rules.

## Workstream C: resilient cloud operation runner

### C1. Define operation ownership and state

Centralize remote work in one operation runner per local installation. The UI/RPC adapter submits typed commands; it must not launch arbitrary goroutines for upload, download, maintenance, or reconnect.

```go
type CloudOperationKind string

const (
	CloudOperationSync       CloudOperationKind = "sync"
	CloudOperationReconnect  CloudOperationKind = "reconnect"
	CloudOperationReclaimPlan CloudOperationKind = "reclaim_plan"
	CloudOperationReclaimRun CloudOperationKind = "reclaim_run"
)

type CloudOperationStatus struct {
	OperationID   string
	JournalID     string
	Kind          CloudOperationKind
	State         string // queued, running, canceling, succeeded, failed, canceled
	StartedAt     time.Time
	UpdatedAt     time.Time
	BytesDone     int64
	BytesTotal    int64 // zero when unknown
	ObjectsDone   int
	ObjectsTotal  int
	RetryAt       *time.Time
	SafeMessage   string
}
```

Permit at most one mutating remote operation for a journal. An explicit user sync during a background sync should attach to the existing operation and receive its status, not begin a competing lease/publish attempt. Operations for different journals may run concurrently within a configurable global limit.

Persist only enough local operation state to explain an interrupted attempt and resume safe work. Never persist a live context, unlocked master secret, raw credential, or a claim that remote publication completed until `current.json` has been re-read and validated.

### C2. Cancellation semantics

Give every submitted operation a `context.Context` and an explicit RPC cancellation command. Cancellation must:

- stop starting new object transfers;
- close or cancel active provider requests;
- release local operation ownership after cleanup;
- leave the remote pointer untouched unless it had already advanced and was verified;
- retain local cache and immutable remote uploads for a later retry/recovery;
- return `canceled`, not a generic failure, to the UI.

It is acceptable for an immutable upload to complete after cancellation races with the transport. The recovery rules from Phase 3 must treat it as an unreferenced candidate rather than infer publication.

### C3. Retry and backoff policy

Use a centralized policy keyed by `VaultErrorKind`:

| Error class | Automatic retry | Policy |
| --- | --- | --- |
| canceled, unauthorized, malformed response, unsupported | No | Require a user action or provider fix. |
| precondition failed | No publish retry | Fetch/validate pointer and surface conflict/recovery. |
| already exists | Conditional | Verify immutable digest/version, then continue only if it matches. |
| rate limited | Yes | Honor safe `Retry-After`; otherwise exponential backoff with jitter. |
| unavailable, timeout | Yes, bounded | Exponential backoff with jitter, operation deadline, and progress update. |
| not found | Context dependent | Missing required pointer/revision is corruption or first-create logic; do not blanket retry. |

Defaults should be conservative: short request timeouts, a bounded total sync deadline appropriate to the data size, no busy loop, and an always-visible next retry time. Persist a retry schedule only for user-enabled background synchronization; foreground actions should report a retry option rather than silently continue after the user leaves the operation.

### C4. Transfer limits and backpressure

Stream databases and attachments to disk; do not buffer entire encrypted objects in memory. Add settings with safe defaults for:

- maximum simultaneous cloud operations globally;
- maximum simultaneous object transfers per journal;
- maximum transfer size accepted from a provider before aborting;
- optional background upload/download bandwidth limit;
- local free-space reserve before a snapshot, download, or import begins.

The scheduler should prioritize required pointer/manifest/database reads over bulk attachments, preserve a stable order for attachment transfers, and allow an interactive open/recovery operation to preempt queued background transfers. It must not preempt a pointer update once its conditional request is in flight; instead wait for its known result.

### C5. Idempotency and restart recovery

Assign an operation ID and a content-derived transfer identity for each planned upload/download. On restart:

- mark in-progress operations as interrupted, not successful;
- run normal mount recovery before allowing edits that depend on remote state;
- reuse complete verified local cache objects and immutable remote uploads;
- discard partial temporary files unless the provider/format has an explicitly validated resumable-transfer mechanism;
- never resume an old pointer update from persisted state without fetching the current pointer and reacquiring the journal lease.

## Workstream D: diagnostics, observability, and supportability

### D1. Local structured event log

Add a bounded, rotating local cloud event log for each installation. Record timestamp, operation ID, journal ID, provider type, protocol operation, object class (pointer, lease, manifest, database, blob), byte counts, elapsed time, normalized error kind, retry decision, and safe provider status code.

Do not record document titles, notes, attachment filenames, remote endpoint host unless the user explicitly elects to include it, object payloads, encrypted bytes, master secrets, wrapped keys, authorization headers, cookies, or credentials.

### D2. Diagnostic bundle

Provide a user-invoked “Copy cloud diagnostics” / export action. Build it in memory or a user-selected location and show its exact contents before sharing. Include:

- app and protocol version;
- operating system and architecture;
- provider type and a redacted endpoint identity;
- validated capability flags and validation timestamp;
- mount state, last successful sync time, cache-validation outcome, and local free-space category;
- pointer and lease metadata limited to IDs, revision number, timestamps, and versions/digests as appropriate;
- recent sanitized event records;
- deterministic error codes and recovery recommendation.

The bundle must omit secrets and user content by construction. Add an automated scanner test with sentinel document text, filename, password, endpoint userinfo, and token values, asserting none can appear in the generated archive/text.

### D3. Metrics boundary

Do not add remote telemetry by default. If product telemetry is later authorized, expose only aggregate opt-in events with a separate privacy design. In this phase, diagnostics stay local and user-controlled.

## Workstream E: retention and attachment-maintenance hardening

### E1. Retention safety checks

Keep retention in `VaultSyncService` immediately after a confirmed pointer advance. Before deleting an expired revision’s manifest/database objects:

1. Re-read and validate `current.json`.
2. Compute the retained revision set from the pointer-confirmed revision history.
3. Verify that the candidate is neither current nor among the newest `N - 1` retained revisions.
4. Delete only the immutable revision manifest and revision database snapshot for the candidate, using a conditional version when available.
5. Treat a deletion failure as non-fatal after publication; record it for later retry and never roll back the current pointer.

The cleanup routine must tolerate holes from prior failed cleanup and must not derive attachment liveness from a partial retained-history set.

### E2. Reclamation-plan persistence and validation

Implement the Phase 4 maintenance UI on top of a local immutable reclamation-plan record. A plan contains:

- plan ID, journal ID, cache validation result, provider identity/capabilities, and creation time;
- the exact pointer version and lease identity seen during scan;
- every retained revision manifest ID/digest used as an attachment reference source;
- the complete listed blob-key set with object version and size;
- the sorted candidate list of unreferenced blob digests/keys;
- a fixed expiry (for example, 24 hours) after which it cannot be executed.

Build a preview in this exact order: flush/publish local changes; validate the
cache and acquire/confirm the lease; read and retain the current-pointer token;
derive the retained revision set; fetch every retained manifest; strictly
validate each manifest's format, Journal ID, descriptor digest, and attachment
list; list every object under this Journal's `blobs/sha256/` prefix; then
subtract the union of referenced attachment digests from the complete listed
blob set. Abort the entire preview—and create no executable plan—on an unknown
manifest version, digest mismatch, incomplete page, missing object version, or
any listing/provider error. The safe output for uncertainty is no deletion,
not a partial candidate set.

Executing a plan must require an unlocked journal, valid lease, a clean cache, the same provider configuration, and a fresh pointer read. If any precondition changes, abandon the plan and require a new preview. For each candidate, issue conditional delete with the listed version; a precondition failure means “leave it” and is not retried as an unconditional delete.

Never invoke maintenance automatically, after normal retention, or as part of a sync retry. The successful result must distinguish deleted, already absent, changed/skipped, and failed candidates.

### E3. Large-vault resource controls

List blobs in pages and build reference/candidate sets using disk-backed sorted temporary files when the object count exceeds a chosen memory threshold. Ensure temporary files are inside the per-journal cache, permissions-restricted, cleaned on completion/cancellation, and excluded from snapshot discovery.

Provide a preview count/size before an execution confirmation. Set a configurable maximum deletion batch; require another explicit confirmation for subsequent batches. Default to a small conservative batch for the first release even when a plan contains many candidates.

## Workstream F: optional discovery and share workflow

### F1. Provider discovery

Add discovery only for providers whose validated capabilities include complete,
prefix-confined listing. Discovery is a recovery convenience, not a source of
truth and not a requirement for normal use.

The discovery command must:

1. Require a previously validated provider and the user-selected vault root.
2. List only candidate `journals/<cloud-journal-id>/current.json` objects
   beneath that root; never scan a provider account outside the configured
   root.
3. Read and strictly validate each candidate pointer before presenting it.
4. Present only safe metadata: cloud Journal ID, revision number, update time,
   and an encryption/locked indicator. Do not derive a title from encrypted
   data or assume an optional title hint is present.
5. Let the user select a candidate and run the normal explicit-ID recovery
   flow, including manifest/database verification and lease inspection.

Discovery must be paginated, cancellable, bounded by a displayed result limit,
and robust to duplicate/malformed/vanished objects. A listing error produces
an incomplete result with a retry option; it must never offer a Journal as
recoverable unless its current pointer was read and validated.

### F2. Optional share reference

If product chooses to ship sharing, implement only a portable **vault
reference**, not credential sharing, password sharing, or remote permission
management. The reference is an explicit user-exported JSON/text or QR payload
containing a protocol version, provider kind, normalized non-secret endpoint
and root, and cloud Journal ID. It must never include a credential reference,
authorization material, Journal master password, local cache path, device ID,
lease ID, pointer token, or document content.

Importing a reference must require user review, local configuration/validation
of matching provider credentials, and then use the normal reconnect flow. The
recipient may open read-only if another device holds the lease; sharing does
not weaken the single-writer lease model or grant provider access. Include a
clear warning that the sender must separately grant the recipient least-
privilege access to the vault root through the provider's own access controls.

Version the reference format and reject unknown required versions. Treat it as
untrusted input: normalize endpoints, reject userinfo/path traversal and
oversized fields, and do not automatically contact an endpoint until the user
confirms the import. The initial implementation must not provide invitation
links, server-hosted directories, collaborative editing, or automatic provider
ACL changes.

### F3. Provider-native history visibility

Where a provider exposes its own version history, describe it as external
recovery information only. Journal's retained-history list, restore command,
and `N`-revision policy continue to use only protocol-valid pointer-confirmed
revisions. Do not add a UI action that restores arbitrary provider-native
object versions, because their relation to a complete Journal revision cannot
be validated safely without an explicit future protocol extension.

## Workstream G: documentation and release readiness

### G1. Provider documentation

Document each supported provider with:

- supported endpoint requirements and tested server versions;
- credentials and secret-storage behavior;
- minimum required conditional-write semantics;
- exact vault-root permissions (create/read/write/list/delete only within the selected root);
- recommended provider-side versioning, backups, lifecycle policies, and quota monitoring;
- limitations, especially whether manual attachment reclamation is available;
- migration/reconnect instructions and a recovery checklist.

Make clear that provider-side lifecycle rules must not expire `current.json`, `lease.json`, retained revision manifests, revision database snapshots, or blobs independently of Journal’s protocol. Provider lifecycle controls may retain extra history; they must not silently delete active vault data.

### G2. Upgrade and compatibility policy

Version all local mount schemas, remote canonical documents, encrypted envelope formats, and provider configuration envelopes. Add fixture tests for every released version. A newer client must fail safely and read-only when it encounters an unknown required protocol version; it must not overwrite the remote pointer.

Establish an explicit compatibility matrix in the documentation: app version, protocol version, provider adapter version, and whether read, publish, reconnect, or maintenance is allowed.

### G3. Security review

Perform a focused review of:

- endpoint normalization and SSRF-like local-network surprises in provider configuration;
- TLS validation, certificate errors, redirects, and proxy behavior;
- credential retrieval and diagnostic redaction;
- cache/temp-file permissions and cleanup;
- canonical JSON parsing limits and maliciously large manifests/listings;
- cancellation/race boundaries around lease renewal and pointer update;
- file descriptor, disk-space, and memory exhaustion during large transfers.

Address findings before enabling new-provider support in a stable release. Security-critical unsupported server behavior must fail closed.

## Implementation order

1. Extract and stabilize the capability/error/contract-test layer around the Phase 3 provider.
2. Make the existing provider pass the full contract suite and add real-service integration coverage.
3. Implement and probe WebDAV conditional semantics, then make it pass the same suite.
4. Add the centralized operation runner, cancellation, operation-state persistence, retry policy, and transfer limits.
5. Connect Phase 4 UI state to runner progress, cancellation, error codes, and diagnostics export.
6. Implement retention hardening and disk-scaled, manual attachment-reclamation plans.
7. Add provider discovery and, if approved, the non-secret vault-reference share workflow.
8. Add large-library/fault-injection tests, document provider requirements, and complete the security/release review.

Keep each change behind tests at the abstraction boundary. In particular, do not land an adapter with custom sync branches; resolve semantic differences inside its `VaultStore` implementation or reject the server configuration.

## Test plan

### Unit and contract tests

- Run `RunVaultStoreContract` for fake, first-provider, and WebDAV adapters.
- Test every normalized provider error, retry decision, and redaction path.
- Test operation coalescing for duplicate sync commands, per-journal exclusion, global operation limit, cancellation, and restart marking.
- Test retry scheduling with a deterministic clock/random source; verify `Retry-After` wins when valid.
- Test that canceled operations cannot start a pointer mutation after cancellation is observed.
- Test streaming transfer limits and disk-space checks with controlled readers/filesystems.
- Test retention with holes, stale pointer reads, failed deletes, `N = 2`, and a large revision history.
- Test attachment-reclamation plan expiry, changed pointer, lost lease, incomplete listing, stale object version, deletion batches, and cleanup of temporary index files.
- Test all diagnostic redaction sentinels.
- Test discovery pagination, cancellation, malformed pointers, duplicate IDs,
  root confinement, and a pointer that disappears after listing.
- Test share-reference version rejection, redaction of every secret/local field,
  endpoint normalization, explicit confirmation before contact, and reconnect
  through independently configured recipient credentials.

### Integration and fault-injection tests

- Run the same journal lifecycle against supported S3-compatible and WebDAV test services.
- Drop connections during each publish stage, lease renewal, database download, attachment upload/download, retention deletion, and maintenance deletion.
- Inject slow reads, partial responses, malformed JSON, incorrect ETags, permission changes, server time drift, rate limits, and duplicate responses.
- Simulate process termination before and after pointer advance, then verify fresh-install recovery.
- Simulate two installations competing for publish and verify no lost pointer update or unsafe cleanup.
- Publish and reopen a large library with many attachments while measuring peak memory, open file descriptors, cache growth, total time, and cancellation latency.
- Verify a provider server that ignores preconditions is rejected during validation and cannot create a mount.

### Manual acceptance scenarios

- Connect a supported provider using the settings UI, create a cloud journal, then reconnect the same vault on a second installation.
- Work offline, make local changes, reconnect, observe progress, cancel safely, and retry successfully.
- Cause a conflict from a second installation and confirm the first installation stays read-only until the user resolves/reconnects.
- Export diagnostics and inspect that no journal text, attachment name, password, or credential is present.
- Preview attachment reclamation, alter remote state, and confirm execution refuses the stale plan.
- Discover a vault under an authorized root and recover a selected valid
  Journal; import a share reference on a second installation without receiving
  the sender's credentials or password.

## Completion criteria

- The first provider and WebDAV pass the identical provider conformance suite and integration lifecycle suite.
- The application has no provider-specific protocol branches outside the provider adapters and capability checks.
- Every remote mutation is represented by a typed operation with observable state, cancellation semantics, bounded retry behavior, and safe restart recovery.
- Large transfers and maintenance scans are streaming/bounded; test evidence shows no unbounded memory growth or unsafe temporary files.
- All provider errors and diagnostic bundles are redacted by automated tests.
- Retention deletes only expired revision metadata/database objects after confirmed publish; attachment deletion remains strictly manual and plan-gated.
- The manual reclamation feature refuses incomplete listings, stale plans, missing leases, dirty cache, and unknown manifest formats, preferring leaked blobs to data loss.
- Discovery is root-confined and validates every presented pointer; any shipped
  sharing is limited to a versioned, non-secret vault reference and preserves
  the existing single-writer lease model.
- Provider setup, operational limits, recovery, compatibility, and security constraints are documented for release.
