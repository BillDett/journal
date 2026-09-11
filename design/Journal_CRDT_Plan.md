# Journal CRDT implementation plan

## Decision in one sentence

Keep Journal a simple, single-process, local-first desktop application by
introducing CRDTs only for document bodies, with the Wails IPC bridge as the
initial local transport. Do **not** open a localhost listener for normal local
editing. Make hosted synchronization and sharing optional layers added only
after the local implementation is dependable.

This plan is based on [Journal_CRDT.md](Journal_CRDT.md), the current
Tiptap/ProseMirror editor, Wails RPC/event boundary, SQLite persistence, and
the existing encryption, attachment, search, import/export, and recovery flows.

## Product guardrails

- Journal remains fully useful with no account, network connection, service,
  port, or background daemon.
- Collaboration is opt-in per Journal; the default is a private local Journal.
- Only rich-text document bodies become CRDTs initially. Titles, hierarchy,
  sort order, permissions, attachment bytes, encryption settings, and deletion
  stay ordinary transactional data with explicit conflict rules.
- `content_json` remains available for search, previews, Markdown export,
  attachment reconciliation, compatibility, and diagnostics, but becomes a
  derived/materialized projection for CRDT-enabled documents.
- CRDT compaction is storage maintenance, not user-visible revision history.
- Never weaken the existing encryption promise merely to enable cloud sharing.
  Initial multi-user cloud collaboration excludes encrypted Journals.
- A Journal is never a separate "Cloud Journal" type. It is local by default
  and may later be made collaborative; turning collaboration on or off does
  not replace its local SQLite copy or create a second Journal storage mode.
- The v1.5 Cloud Backup feature is retired. Collaborative synchronization, not
  a user-configured whole-database cloud backup, is Journal's optional cloud
  capability. Local database copies and Markdown export remain available as
  user-controlled recovery/portability tools.

## Retire the v1.5 Cloud Journal / Cloud Backup concept

The old model uploads and restores whole `journal.db` snapshots through a
user-configured S3-compatible endpoint. It cannot provide multi-writer edits
without conflicts and makes cloud state a separate product concept. This plan
replaces it with an optional collaborative Journal: each participant has a
durable local replica and the collaboration service synchronizes CRDT updates.

This removes from the product:

- the `Cloud Journal` storage classification, mount/cache vocabulary, OCI
  revision publishing, and exclusive cloud edit lease described in `CLOUD.md`;
- the v1.5 Cloud Backup endpoint, credentials, whole-database `Sync Now`,
  restore-from-cloud workflow, and cloud-backup status in Settings; and
- client-managed cloud revision retention as a Journal feature. The hosted
  collaboration service may perform ordinary operational disaster recovery,
  but it is not exposed as a second client-side Journal backup system.

### Clean removal steps

Assume no one has used the v1.5 feature. There is no configuration, remote
artifact, credential, or user-data migration work.

1. Remove the Settings UI, dialogs, close-time cloud-sync prompt, status
   polling, Wails bindings, API contracts, command service, and tests for
   `CloudBackup*`.
2. Remove `cloud_backup.go`, its state tables/triggers, and the cloud-backup
   credential rewrapping path in encryption handling. Preserve the historic
   migration version slots as no-ops so direct 1.4.0 upgrades remain valid.
3. Remove the Cloud Journal/OCI implementation plan from active product scope;
   retain `CLOUD.md` only as an archived superseded design with a short header
   pointing to this plan. Do not implement its cached databases, mounts,
   provider configuration, artifact publishing, or edit leases.
4. Replace all user-facing cloud language with `Make collaborative`,
   `Collaborative Journal`, `Saved locally`, and `Synced`. A user who never
   enables collaboration sees no cloud setup or cloud status at all.

The retirement removes a product backup feature; it does not assert that the
hosted service must never protect its own production data. Server-side
operational backups are an implementation responsibility of the future service
and are not a Journal mode, endpoint configuration, or restore UI.

## Direct upgrade contract: Journal 1.4.0 to CRDT-based 1.6.0

Journal 1.6.0 must support opening a Journal 1.4.0 database directly. The
upgrade is a local database upgrade; it does not require a cloud account,
network connection, Cloud Backup configuration, or a 1.5.0 intermediate
installation.

```text
Journal 1.4.0 database (schema version 1)
                    |
                    v
  retain historic migration slots 2 and 3 as no-ops
                    |
                    v
  migration 4: remove unused Cloud Backup state and add CRDT tables
                    |
                    v
  Journal 1.6.0; documents convert lazily when first opened
```

### Required migration rules

1. Preserve migration version numbers 1, 2, and 3 permanently. In the 1.6.0
   codebase, versions 2 and 3 become historical no-ops; do not remove them
   from `schemaMigrations`. This lets a 1.4.0 database advance monotonically
   from version 1 and lets an unused 1.5.0 database advance from version 3.
2. Add the CRDT schema as migration 4 or later. Fresh 1.6.0 databases must not
   recreate Cloud Backup tables. Migration 4 removes any leftover unused
   `cloud_backup_*` tables/triggers with idempotent `DROP ... IF EXISTS`
   statements, then creates the CRDT tables and indexes.
3. Before applying migration 4, create a transactionally consistent local
   pre-upgrade copy of `journal.db`; abort before changing the database if the
   copy cannot be made. This is a one-time rollback safeguard, not a Cloud
   Backup feature.
4. Make migration 4 atomic and restart-safe. It must not advance
   `PRAGMA user_version` until all schema changes complete, and it must be safe
   to rerun after an interruption.
5. Do not eagerly convert document bodies in migration 4. It only adds CRDT
   capability. A document remains valid 1.4.0 ProseMirror JSON until its first
   1.6.0 open.
6. On first open, atomically convert the document's JSON to a Y.Doc, persist
   the initial native Yjs snapshot, and set its CRDT-authoritative/version
   marker in the same transaction. A crash before the commit leaves the JSON
   document unchanged; a crash after it leaves a complete CRDT document.
7. Encrypted or locked Journals receive only the schema upgrade while locked.
   Their document-body conversion waits until the Journal is unlocked; it must
   never require decrypted content during application startup.
8. If a document cannot be converted because its historic JSON is malformed or
   unsupported, preserve the original JSON untouched, report the document ID
   and reason, and offer a recoverable read-only/legacy path rather than
   silently replacing content.
9. The upgrade is one-way. Once migration 4 records the newer SQLite schema,
   Journal 1.4.0/1.5.0 must fail safely before writing to the database. Once a
   document is CRDT-authoritative, its per-document minimum-version marker is
   an additional defense against accidental legacy writes.

### 1.4.0 upgrade release gates

Test the installed 1.6.0 application against committed, production-shaped
1.4.0 fixture databases—not only a newly created database:

- plaintext documents containing every supported node, mark, table, task, and
  attachment form;
- encrypted and locked Journals, followed by unlock and lazy conversion;
- Trash, imported content, duplicated documents, and large documents;
- a crash before, during, and after schema migration and first-open conversion;
- insufficient disk space for the local pre-upgrade copy;
- opening the upgraded database in 1.4.0/1.5.0, which must fail without making
  a write; and
- byte-for-byte preservation of every still-unconverted `content_json` value.

The Cloud Backup removal has no user-data migration because it is assumed
unused. The CRDT schema/document conversion above is separate and is required
for every direct 1.4.0 → 1.6.0 upgrade.

## Recommended local architecture: no listener

```text
Tiptap + y-prosemirror
          |
          v
       Y.Doc in the Wails renderer
          |
          | Wails RPC (binary update encoded for IPC)
          v
  Go CRDT coordinator, in the Journal process
          |
          +-- SQLite snapshots + update log
          +-- persists renderer-supplied JSON / FTS / attachment projection
          +-- optional future cloud-sync adapter
          |
          | Wails runtime event (remote/other-session update)
          v
       Y.Doc in the Wails renderer
```

The Wails webview and Go application do not share language-level object
references, so there cannot literally be a direct in-memory `Y.Doc` call from
TypeScript to Go. However, Wails bindings are already in-process IPC rather
than TCP. A small CRDT transport over those bindings gives the practical
benefits sought here: no bound port, no listener, no localhost exposure, and
no local authentication token.

Use a transport interface so the editor is independent of the mechanism:

```ts
interface LocalCRDTTransport {
  open(documentId: string): Promise<CRDTBootstrap>
  submit(sessionId: string, updates: Uint8Array[]): Promise<DurabilityAck>
  materialize(sessionId: string, throughSeq: number, json: ProseMirrorDoc): Promise<void>
  flush(sessionId: string): Promise<DurabilityAck>
  close(sessionId: string): Promise<void>
  onUpdate(handler: (message: CRDTUpdateMessage) => void): () => void
}
```

Wails exposes `OpenCRDTSession`, `SubmitCRDTUpdates`,
`MaterializeCRDTProjection`, `FlushCRDTSession`, and `CloseCRDTSession` as
normal Go methods. The backend emits a scoped
`journal:crdt-update` event for updates originating elsewhere. Since current
Wails calls marshal JSON, encode update bytes as base64 or a small
`number[]`-free binary envelope; base64 is less efficient but clearer and a
fine first implementation. Batch updates briefly (for example 25--100 ms)
and cap batch size.

Important implementation constraint: `y-prosemirror` is the code that knows
how to convert a Yjs XML fragment to this application's ProseMirror JSON. In
the initial JS-owned option, the renderer must therefore debounce and submit a
`MaterializeCRDTProjection` command containing `editor.getJSON()` and the last
durably acknowledged CRDT sequence. Go stores that projection and updates FTS
and attachments transactionally. It must not attempt to invent a general
Yjs-to-ProseMirror converter. A Go/Ygo implementation may replace this only
after a tested, schema-compatible conversion strategy exists; until then,
headless operations use the latest projection after an explicit renderer
flush.

### Local implementation options

| Option | Local port/listener | Recommendation | Rationale |
|---|---:|---|---|
| JS-owned `Y.Doc`; persist Yjs updates through Wails RPC | No | **Start here** | Smallest change; one editor session normally owns a document; works offline. |
| Go-owned Ygo document; bridge the Yjs sync protocol through Wails RPC/events | No | Add when cloud sync or multiple local renderer sessions need it | Keeps cloud credentials and remote sync in Go without exposing a port. |
| Embedded loopback Ygo WebSocket server | Yes, loopback only | Compatibility fallback, not default | Convenient off-the-shelf provider integration, but introduces socket lifecycle and localhost attack surface. |
| Frontend connects directly to cloud | No local listener | Prototype only | Makes browser code own credentials, retries, and cloud policy; not the desired long-term boundary. |

The first option is deliberately modest: the frontend `Y.Doc` is authoritative
while it is open; Go durably stores its update stream and produces projections.
It does **not** require Ygo locally. The second option may reuse Ygo later, but
must sit behind the same Wails transport so adding it does not change editor
code. A local WebSocket remains valid if a mature Yjs provider cannot be
adapted cleanly, but it should be a consciously chosen adapter rather than the
architecture’s default.

## Data model and consistency contract

Add a migration that models a durable snapshot plus append-only updates. Exact
column names may vary with the selected CRDT library.

```sql
CREATE TABLE document_crdt_state (
    document_id TEXT PRIMARY KEY REFERENCES documents(item_id) ON DELETE CASCADE,
    generation INTEGER NOT NULL DEFAULT 1,
    format_version INTEGER NOT NULL,
    snapshot BLOB,
    snapshot_through_seq INTEGER NOT NULL DEFAULT 0,
    materialized_through_seq INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL
);

CREATE TABLE document_crdt_updates (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    document_id TEXT NOT NULL REFERENCES documents(item_id) ON DELETE CASCADE,
    generation INTEGER NOT NULL,
    update_blob BLOB NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX document_crdt_updates_by_document
    ON document_crdt_updates(document_id, generation, seq);
```

For encrypted local Journals, encrypt both snapshot and update blobs using the
existing per-Journal key. Bind the document ID, generation, sequence, CRDT
format version, and key ID as authenticated associated data. Do not persist
plaintext CRDT data alongside an encrypted document.

Define these boundaries precisely:

| Boundary | Meaning | Required user-facing state |
|---|---|---|
| Update applied to `Y.Doc` | The editor changed; may still be only renderer memory. | Editing |
| `SubmitCRDTUpdates` acknowledged | Update transaction committed to SQLite. | Saved locally |
| Projection materialized | `content_json`, FTS, and attachment references reflect the CRDT state. | Normally silent; may briefly lag |
| Future remote acknowledgement | Cloud peer has accepted current state. | Synced / Offline — saved locally / Sync error |

The database transaction that appends an update must commit before its
acknowledgement. The renderer materializes only through an acknowledged
sequence, and Go rejects a projection beyond that sequence. On a failed
acknowledgement, retain and retry the unsent frontend updates; never claim
that an in-memory edit is saved.

## Future server compatibility contract

The local-only implementation must use the same durable CRDT representation a
future server will synchronize. Wails IPC is a local transport adapter only;
it must not become part of the persisted CRDT format or the future network
protocol.

### Non-negotiable data rules

- Persist native **Yjs V1** document update bytes and snapshots. Use
  `Y.encodeStateAsUpdate()` for snapshots and the raw payload received from
  the Yjs `update` event for incremental updates. Base64 is only an encoding
  for the Wails JSON boundary; it is never the stored CRDT representation.
- Record an explicit `crdt_format` (for example `yjs-update-v1`) and reject an
  unknown format rather than attempting a best-effort decode. Do not adopt Yjs
  V2 unless every supported local and hosted implementation passes the
  compatibility suite; V1 is the broadly supported default.
- Fix and version the document contract: the Yjs root fragment name, Tiptap
  extension set, ProseMirror schema version, and supported feature flags. A
  CRDT can merge updates correctly while an old editor still cannot render a
  newer node type.
- Keep `content_json` as a derived projection only. Never use it as the
  synchronization payload or regenerate a CRDT document from it after the
  initial exactly-once migration.
- Keep delivery mechanics out of the CRDT payload. Local sequence numbers,
  Wails session IDs, acknowledgements, retries, and future server cursors
  support delivery and diagnostics; they are not causal CRDT state.
- Allocate stable collaborative-Journal, document, and room IDs separately
  from local SQLite `item_id` values. Enabling collaboration maps an existing
  local CRDT document to a room; it does not convert it into a different
  document format.

### Future sync handshake

When a remote server is introduced, use the standard Yjs state-vector/diff
flow rather than a Journal-specific full-document upload:

```text
client state vector  ─────► server
server missing update ────► client
server state vector  ─────► client
client missing update ────► server
```

Yjs updates are commutative and idempotent, so retrying or receiving an update
out of order is safe. State vectors let each side request only the updates it
is missing. The server may store its own delivery cursor, but it must accept
and emit the same native Yjs update format stored in Journal's SQLite log.

### Compatibility release gates

Add a committed fixture corpus and run this matrix in CI before declaring a
local format or server implementation supported:

```text
Journal local Yjs + SQLite  <──►  candidate server  <──►  fresh Yjs/Tiptap client
```

Required cases:

1. A locally created and persisted document is ingested by the candidate
   server, then opened by a fresh client with identical rendered ProseMirror
   JSON.
2. Server-created updates are persisted locally, the application is restarted,
   and the reconstructed document is identical.
3. Two clients make offline edits, submit duplicate and out-of-order updates,
   exchange state-vector diffs, and converge to identical Yjs state and
   ProseMirror JSON.
4. Snapshot plus trailing-update replay, compaction, and a server restart do
   not change the resulting document.
5. Documents containing every supported Journal node, mark, table, task, and
   attachment reference round-trip without loss.
6. An incompatible CRDT encoding or editor schema version fails closed with a
   clear upgrade message; it never silently drops unknown content.
7. If Ygo is adopted, all tests run as a three-way matrix: Yjs local client,
   Ygo server, and a second Yjs client. Passing only same-implementation tests
   is insufficient.

This makes server compatibility a release criterion: a future server becomes
an additional transport/persistence peer for existing local documents, not a
new storage architecture or a document conversion project.

## Implementation risks and required resolutions

The following items are prerequisites or explicit release gates. They are not
post-launch cleanup work.

### 1. JS-owned documents cannot be materialized or compacted headlessly

**Risk:** In the recommended first approach, `y-prosemirror` in the renderer
owns the only proven conversion from the Yjs XML fragment to Journal's
ProseMirror JSON. Go can store raw updates but cannot independently produce a
new JSON projection, FTS result, Markdown export source, or compacted Yjs
snapshot for a closed document. Treating Go as if it can do so would create a
stale derived representation or an unimplementable background job.

**Resolution:** Choose and document one of these before Phase 2:

- the renderer owns `flush → state-vector/snapshot → JSON projection` and
  submits both the native snapshot and derived JSON to Go; headless operations
  explicitly open/hydrate a renderer session and flush it first; or
- add a tested Go-side Yjs and schema-compatible ProseMirror conversion layer,
  then permit Go to materialize and compact headlessly.

Do not implement a bespoke, partial Yjs-to-ProseMirror converter as an
incidental background feature.

### 2. A scalar projection sequence does not prove projection correctness

**Risk:** `editor.getJSON()` can include local edits that have not yet received
a durability acknowledgement, while a second session can contain durable
updates the current renderer has not applied. A projection labelled with only
`materialized_through_seq` can therefore be ahead of or behind the CRDT state
it claims to represent. Incorrect FTS results or attachment reconciliation can
follow.

**Resolution:** Create a serialized projection barrier per document:

```text
pause projection queue
       ↓
flush all buffered updates and await SQLite acknowledgement
       ↓
capture Yjs state vector and ProseMirror JSON from that state
       ↓
atomically store projection + source state vector/checkpoint
       ↓
resume queue
```

Reject a projection that regresses the stored checkpoint. Treat
`content_json`, FTS, and attachment references as rebuildable caches; no
irreversible operation may rely solely on a projection whose canonical CRDT
state has not been checked.

### 3. The proposed cascade schema conflicts with collaborative deletion

**Risk:** `ON DELETE CASCADE` on CRDT rows would erase state immediately, but
an offline collaborator can still have legitimate edits to a document that was
deleted elsewhere.

**Resolution:** Add a document tombstone/generation model before remote sync.
Delete/restore changes metadata first and preserves the CRDT state through a
retention period. Only a coordinated garbage-collection job may physically
delete CRDT rows after the retention and synchronization policy says it is
safe. A client that was editing a tombstoned document must be offered discard,
restore, or save-as-new behavior.

### 4. Multiple Journal processes are unsafe until explicitly handled

**Risk:** Two application processes can use the same SQLite file while each
holds an independent renderer `Y.Doc`. SQLite transaction serialization does
not provide live CRDT notification, protect projections from racing, or ensure
attachment reconciliation sees the newest state.

**Resolution:** Before CRDT documents become the default, either enforce a
single application instance per database or implement cross-process update
notification plus atomic state-vector catch-up. The initial release should
prefer single-instance enforcement; multi-window/process support is a later
feature with its own integration tests.

### 5. Update compatibility and wire-protocol compatibility are different

**Risk:** Native Yjs updates are portable, but provider framing, handshake,
awareness, authentication, reconnect behavior, and authorization are not
defined by the update blob itself. Calling a future service merely
"Yjs-compatible" is too ambiguous.

**Resolution:** In Phase 0, separately version and select:

- Journal's stored CRDT update format;
- the local Wails RPC/event envelope; and
- the future server sync protocol and awareness protocol.

Publish golden handshake/update fixtures for the chosen server protocol. Keep
the existing Yjs/Ygo/second-Yjs-client interoperability matrix as a release
gate, rather than assuming matching update bytes guarantee matching sockets.

### 6. Compaction needs one atomic ownership model

**Risk:** A snapshot can miss an update received while the snapshot is being
constructed. Pruning based on an imprecise cutoff can permanently lose that
update.

**Resolution:** Compaction must serialize per document: flush all buffered
updates, create a snapshot for a known state, atomically commit the snapshot
and its exact cutoff, then prune only rows proven to be covered by that
snapshot. Preserve the prior snapshot and update rows until the replacement
commit succeeds and replay verification passes.

### 7. Encryption needs versioned ciphertext and key-rotation rules

**Risk:** Encrypting each update without recording a cipher/key version and
rotation procedure can make old data unreadable or leave old keys required
forever. Metadata such as document IDs, times, sizes, and update counts also
remains visible even when payloads are encrypted.

**Resolution:** Store encryption algorithm/version, key ID, and nonce/ciphertext
format with every snapshot and update. Define rotation as either re-encrypting
all retained state or keeping old keys until a verified re-encrypted snapshot
and compaction complete. Document the accepted metadata exposure. On lock,
close CRDT sessions, destroy renderer documents where practical, cancel pending
projection writes, and reject any late write using an invalidated session.

### 8. Attachment GC cannot trust a JSON projection alone

**Risk:** A projected document can temporarily omit an attachment reference
that exists in an offline or not-yet-applied CRDT update. Immediate collection
would make a later converged document point to missing bytes.

**Resolution:** From the first CRDT release, attachment removal only marks an
orphan candidate. Garbage collection requires a grace period, a fresh
canonical-state check, and no active/tombstoned CRDT generation that can still
reference the attachment. Remote attachment upload must also be staged before
or alongside publication of its CRDT reference; other clients show a safe
placeholder until the bytes arrive.

### 9. Older Journal versions can create split-brain documents

**Risk:** An older application can treat the materialized `content_json` as
authoritative after a newer application has made Yjs state authoritative.

**Resolution:** Store a database-level CRDT capability/minimum application
version marker when the first document migrates. Every legacy document-body
write path checks it before writing. An older unsupported client must open the
database read-only or fail with an upgrade message; it must never overwrite a
derived projection.

### 10. Collaboration synchronization is not a user backup product

**Risk:** Removing v1.5 Cloud Backup while relying on a collaboration service
can leave users without a recovery path after service loss, account deletion,
or an operational error if durability is not specified.

**Resolution:** Keep Cloud Backup removed from the Journal product, but make
the future service's operational durability explicit: backup/restore drills,
retention, account-deletion behavior, incident recovery ownership, and an
RPO/RTO target. Journal continues to offer its local SQLite database and
export as user-controlled portability paths.

## Implementation phases

### Phase 0 — define boundaries and prove interoperability

1. Add a short architecture decision record naming document body as the only
   initial CRDT scope and recording the no-listener local transport choice.
2. Evaluate the maintained Yjs TypeScript packages with the exact Tiptap
   version already used by Journal: `yjs`, `y-prosemirror`, Tiptap
   Collaboration, and collaboration-aware undo.
3. If Go/Ygo is planned for later sync, write black-box compatibility tests
   now: Yjs updates must round-trip through the proposed Go implementation,
   converge under concurrent changes, and survive snapshots/compaction. Treat
   a schema-compatible Yjs-to-ProseMirror projection in Go as a separate
   feasibility gate, not an assumed Ygo feature.
4. Define an explicit CRDT wire envelope for Wails RPC: protocol version,
   session ID, document ID, generation, monotonic local message ID, encoded
   update bytes, and acknowledgement sequence.
5. Establish limits for payload size, pending updates, retry backoff, and an
   observable diagnostic log that contains metadata only, never document text.

Exit condition: a throwaway Tiptap document survives open, edit, close, and
restart by replaying locally persisted Yjs updates; no TCP listener exists.

### Phase 1 — isolate the current document-body save path

1. Refactor the current `UpdateDocumentDraft` / `FlushDocument` behavior in
   `document_service.go` behind a document-content persistence interface.
2. Keep existing JSON persistence as the default implementation so this phase
   produces no user-visible behavior change.
3. Make `FlushAll`, close handling, export, encryption actions, and
   document deletion invoke that interface rather than manipulate pending
   JSON drafts directly.
4. Preserve current behavior for metadata: title, spacing, moves, folders,
   images, and tree operations remain separate normal commands.
5. Add tests that prove the existing JSON path remains unchanged.

Exit condition: there is one replaceable body-persistence boundary and the
current test suite remains green.

### Phase 2 — local CRDT persistence and exactly-once migration

1. Add the CRDT tables and repository with transactional append, load,
   snapshot, compaction, delete, duplicate, import/export, local recovery,
   and encryption hooks.
2. On first opening a non-CRDT document, convert its ProseMirror JSON once to
   a `Y.Doc`, encode and store the initial snapshot, then set a document CRDT
   format/version marker in the same transaction.
3. From that point initialize Tiptap **only** from the `Y.Doc`; never also
   pass the old `content_json` as editor content. This prevents duplicated
   initial content.
4. Keep migration lazy for ordinary local documents. Before a Journal can be
   published later, migrate all eligible documents with a resumable operation.
5. In the initial JS-owned approach, debounce a renderer-generated
   ProseMirror JSON projection (roughly 1--2 seconds) after durable update
   acknowledgement. Persist it with FTS and attachment reconciliation, and
   force it at explicit durability boundaries. Do not claim a Go-side
   materialization until its schema conversion is proven.
6. Compact only after an atomic snapshot has been committed and recovery can
   replay later updates. Retain enough update history for recovery; keep
   user-visible revision snapshots separate.

Exit condition: CRDT-enabled documents survive abrupt reopen/replay and still
work with search, Markdown import/export, attachments, local encryption, and
local database recovery.

### Phase 3 — Wails CRDT session transport and editor cutover

1. Add a `CRDTCoordinator` in Go that owns SQLite transactions, active session
   metadata, flushes, projection checkpoint validation, and future sync hooks.
   It must not start an HTTP or WebSocket server.
2. Add the Wails methods/events described above. Validate document access,
   generation, message ordering, payload limits, and session ownership.
3. Implement a TypeScript transport adapter that applies the bootstrap before
   enabling editing, batches local Yjs updates, waits for durable acks, and
   applies backend event updates with an origin marker so updates are not
   re-submitted in a loop.
4. Replace the body portion of the current `App.tsx` draft/timer workflow.
   Retain explicit flushes on document change, close, export, lock,
   encryption migration, and application shutdown.
5. Use Yjs-aware undo/redo and disable Tiptap’s normal history for collaborative
   sessions. Verify that undo affects the local author’s changes rather than
   rewinding another session’s edits.
6. Change status wording to `Saving locally`, `Saved locally`, and, only once
   cloud work exists, `Syncing`, `Synced`, `Offline — saved locally`, and
   `Sync error`.

Exit condition: normal typing has the same or better responsiveness and crash
durability as today, and all local editing happens without a bound port.

### Phase 4 — harden local multi-session and lifecycle behavior

1. Support two Journal windows/processes safely. Prefer a database lock or a
   process-level coordinator first; if simultaneous renderer sessions are
   supported, use the Wails event channel and state-vector catch-up rather
   than assuming events are never missed.
2. On open/reconnect, bootstrap from an atomic snapshot plus updates (or a
   state-vector diff), then subscribe to new updates. Include a sequence gap
   check and resync path.
3. Test process crash between editor mutation, RPC receipt, SQLite commit,
   materialization, snapshot, and compaction; also test `SQLITE_BUSY`, full
   disk, and malformed update handling.
4. Ensure lock/unlock, key rotation, database restore, journal deletion,
   import, duplication, and attachment garbage collection respect active CRDT
   sessions. Delay attachment collection and verify against the materialized
   CRDT state.
5. Add deterministic convergence tests for paragraphs, marks, lists, tasks,
   tables, images, undo/redo, offline edits, delayed updates, and schema
   version mismatch.

Exit condition: all destructive and lifecycle operations either flush and
complete safely or fail without silent data loss.

### Phase 5 — remove the v1.5 cloud feature

1. Complete the clean removal steps above after the local CRDT editing path is
   stable. The Cloud Backup feature has no data migration because it is
   assumed unused; retain its historic SQLite migration version slots as
   no-ops for direct 1.4.0 upgrades.
2. Delete the cloud-backup lifecycle calls from application startup, autosave,
   close, encryption, and destructive-operation code paths.
3. Remove `CloudBackupEndpointCommand`, `CloudBackupStatusResponse`,
   `CloudBackupCommands`, `GetCloudBackupStatus*`, `ConfigureCloudBackup`,
   `UnlockCloudBackupCredentials`, `SyncCloudBackup`, `RestoreCloudBackup`,
   and `DisconnectCloudBackup` from the Go and TypeScript boundaries.
4. Remove the Cloud Backup settings pane, dialogs, status indicator, and all
   associated frontend state from `App.tsx`.
5. Update the release notes and `CLOUD.md` to state that the proposed Cloud
   Journal / OCI design is superseded by optional collaborative Journals.

Exit condition: Journal has no Cloud Backup or Cloud Journal UX, API, storage,
credentials, scheduled behavior, or dependency; ordinary local Journals still
operate normally.

### Phase 6 — optional personal multi-device sync

1. Add stable collaborative-Journal/document/room IDs separate from local
   SQLite item IDs. A restored or duplicated database must not accidentally
   attach to a pre-existing collaboration room.
2. Keep cloud sync behind a Go `RemoteSync` interface. The local Wails
   transport remains unchanged.
3. Implement a Go-side remote adapter using a standard Yjs-compatible protocol
   (Ygo if its compatibility testing passes), TLS, durable outbox/retry, and
   state-vector-based catch-up.
4. Use a hosted collaboration service for durable realtime state and
   PostgreSQL/object storage as appropriate. Do not add OCI publication or a
   user-configured cloud-backup product; service-level disaster recovery stays
   an operational concern.
5. Do not introduce an exclusive edit lease for document bodies. Keep strong
   coordination for local recovery, imports, encryption migration, and
   destructive Journal-wide administration.
6. Pilot with two devices owned by one person before inviting another user.

Exit condition: edits made offline on two owned devices converge after a long
disconnect without compromising either local copy.

### Phase 7 — optional multi-user collaboration

1. Add accounts, invitations, Journal membership, editor/viewer roles, audit
   events, revocation, and server-side authorization before exposing sharing.
2. Add non-persistent awareness/presence and collaboration carets. Presence is
   ephemeral and must never be written to SQLite revision history.
3. Keep metadata outside the body CRDT initially. Use optimistic concurrency
   and explicit product rules for rename, move, delete, restore, permissions,
   and document schema upgrades.
4. Design delete-vs-edit semantics before release: typically tombstone the
   document and preserve a recoverable CRDT generation until all active
   devices have reconciled or a retention period expires.
5. Synchronize attachments separately via object storage, with content hashes,
   availability states, and conservative garbage collection.
6. Explicitly reject collaborative cloud enablement for encrypted Journals in
   this release. A true end-to-end encrypted multi-user CRDT design needs its
   own key distribution, revocation, client-side compaction, and recovery
   project.

Exit condition: a shared non-encrypted Journal has clear access controls,
predictable deletion behavior, and tested convergence under real network
failures.

## Files and interfaces expected to change

| Area | Planned responsibility |
|---|---|
| `frontend/package.json` | Add Yjs/Tiptap collaboration dependencies after compatibility validation. |
| Editor extension factory | Accept a `Y.Doc`, awareness/undo manager when enabled, and disable normal history. |
| `frontend/src/App.tsx` | Manage CRDT session lifecycle; remove full-body draft upload as the CRDT path. |
| `frontend/src/wails/libraryApi.ts` | Declare the typed CRDT session, update, flush, and status contracts. |
| `wails_adapter.go` / commands | Expose and validate Wails CRDT IPC; emit scoped update events. |
| New `crdt_service.go` | Coordinate sessions, durability, replay, projection checkpoints, compaction, and future remote sync. |
| `document_service.go` / `autosave.go` | Retain legacy JSON path; route CRDT bodies through coordinator and explicit flush boundary. |
| `sqlite_schema.go` and repository layer | CRDT migrations, encrypted update persistence, snapshot/replay, and projection checkpoints. |
| `fts.go`, attachments, export/import, encryption | Consume a materialized/explicitly flushed document representation. |
| Future `cloud_collab.go` | Optional remote adapter; no direct frontend cloud credentials. |
| `cloud_backup.go`, Cloud Backup Wails/API/UI code | Delete completely; collaboration replaces this product feature. |

## Acceptance checklist before making CRDT local-default

- A local-only Journal has no listening socket and no network requirement.
- A typed character is acknowledged only after SQLite durability, or the UI
  accurately reports the failure and retries without discarding it.
- Reopening after an unclean exit reproduces all acknowledged CRDT updates.
- No migration duplicates existing content, and old/non-CRDT documents remain
  readable until lazily migrated.
- Search, export, attachment reconciliation, encryption, local recovery,
  duplicate, import, delete/restore, and close/shutdown handle pending updates
  and force a renderer projection when the JS-owned approach is active.
- Compaction cannot lose a committed update and does not change revision history.
- The application has coverage for Yjs/protocol compatibility before depending
  on Ygo or any hosted implementation.
- Encrypted documents remain encrypted locally; cloud sharing is unavailable
  rather than silently less secure.

## Why this is the right order

The main risk is not hosting a collaboration server; it is changing a simple
whole-document autosave model without harming Journal’s reliability. Starting
with a local Yjs document persisted through the existing Wails bridge proves
the new durable representation while preserving the app’s defining simplicity.
After that, a Go-owned Ygo document or an external Yjs-compatible server is an
incremental adapter choice, not a second storage architecture.
