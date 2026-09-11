# Journal CRDT Architecture Analysis

## Purpose

This document explores how CRDT-based collaborative editing could be introduced into the Journal application at:

https://github.com/BillDett/journal

The goal is to preserve Journal's local-first, SQLite-backed desktop architecture while enabling optional hosted cloud collaboration.

The recommended direction is:

- Keep Journal fully usable offline.
- Use a local CRDT server embedded in the Go/Wails application.
- Persist CRDT state locally in SQLite.
- Use Yjs-compatible synchronization between Tiptap and the local CRDT service.
- Allow the local CRDT service to synchronize with an optional hosted CRDT server.
- Keep ProseMirror JSON as a derived/materialized representation for search, export, migration, and compatibility.
- Introduce cloud collaboration as synchronization between peers rather than as a separate document-storage architecture.

---

# Recommended architecture

Conceptually:

```text
                         JOURNAL DESKTOP
┌─────────────────────────────────────────────────────────────┐
│                                                             │
│ React / Tiptap                                              │
│      │                                                      │
│      │ Yjs / ProseMirror binding                            │
│      │                                                      │
│      ▼                                                      │
│   Y.Doc                                                     │
│      │                                                      │
│      │ ws://127.0.0.1:<ephemeral-port>                      │
│      ▼                                                      │
│ Embedded Ygo collaboration server                           │
│      │                                                      │
│      ├──────── CRDT updates/snapshots ──────► SQLite        │
│      │                                       journal.db     │
│      │                                                      │
│      └──── for cloud journals only ───────────────┐         │
│                                                   │         │
└───────────────────────────────────────────────────┼─────────┘
                                                    │
                                                  WSS/TLS
                                                    │
                                                    ▼
                                     ┌─────────────────────────┐
                                     │ Hosted CRDT server      │
                                     │                         │
                                     │ Ygo or Hocuspocus       │
                                     │                         │
                                     │ CRDT persistence        │
                                     │ Authentication / ACL    │
                                     │ Presence                │
                                     └────────────┬────────────┘
                                                  │
                               ┌──────────────────┴───────────┐
                               ▼                              ▼
                          PostgreSQL                    Object storage
                         CRDT/metadata                   attachments
```

The key idea is:

> Local and cloud editing should use exactly the same CRDT document format and synchronization protocol. Cloud should become optional synchronization, not a different kind of Journal document.

This fits the direction already described in `CLOUD.md`, which proposes splitting the current monolithic `JournalService` into store-independent journal-content behavior plus routing between local and cloud stores.

---

# 1. Ygo is a strong fit for Journal

A particularly good fit for Journal's Go/Wails architecture is **Ygo**, a pure-Go implementation of Yjs-compatible CRDTs.

It currently provides capabilities including:

- Yjs-compatible synchronization
- awareness/presence
- WebSocket collaboration
- Hocuspocus compatibility
- pure-Go SQLite persistence using `modernc.org/sqlite`
- versioned persistence
- snapshots and compaction
- an embeddable offline-first client
- no Node runtime
- no CGO requirement

That means Journal does not need something like:

```text
journal.exe
    +
node.exe
    +
hocuspocus server
```

Instead the collaboration server can be another Go component inside the existing Wails process.

Conceptually:

```text
Journal application

main
 ├── JournalContentService
 ├── EncryptionService
 ├── AttachmentService
 ├── CRDTService
 │    ├── Ygo WebSocket server
 │    ├── SQLite persistence
 │    └── cloud sync client
 └── Wails
```

A Journal-specific Ygo persistence adapter would likely be preferable to pointing Ygo at a second standalone database. That would allow CRDT state to live inside `journal.db`, preserving Journal's single-file-storage model and allowing encryption, backup, deletion, and migrations to participate in the application's existing data lifecycle.

---

# 2. Fundamental change to Journal's editing model

Today the document editing flow is approximately:

```text
Tiptap edit
   ↓
300 ms debounce
   ↓
editor.getJSON()
   ↓
UpdateDocumentDraft()
   ↓
pending Go draft
   ↓
FlushDocument()
   ↓
documents.content_json
```

The application serializes the complete ProseMirror document and sends the whole document to Go, where it is ultimately stored in SQLite.

A CRDT architecture changes the flow to:

```text
Tiptap edit
     ↓
Yjs transaction
     ↓
small binary CRDT update
     ↓
local Ygo server
     ↓
SQLite

            +
            ↓
      optional cloud sync
```

The full ProseMirror JSON representation should stop being the authoritative mutable representation while a document is collaborative.

Instead:

```text
documents.content_json
```

changes from:

> authoritative current document state

to:

> most recently materialized ProseMirror representation of the authoritative CRDT state

That JSON is still highly useful for:

- search indexing
- Markdown export
- attachment reconciliation
- migration
- debugging
- backward-compatible read paths
- snapshots and previews

But it should not be the synchronization primitive.

---

# 3. SQLite storage changes

The current schema already contains document state such as:

```text
documents
---------
item_id
schema_version
content_json
content_ciphertext
spacing_preset
...
```

The CRDT architecture would add storage along these lines:

```sql
document_crdt_state
-------------------
document_id
generation
snapshot
snapshot_through_seq
format_version
updated_at

document_crdt_updates
---------------------
seq
document_id
generation
update_blob
created_at
```

For example:

```sql
CREATE TABLE document_crdt_state (
    document_id TEXT PRIMARY KEY
        REFERENCES documents(item_id) ON DELETE CASCADE,

    generation INTEGER NOT NULL DEFAULT 1,
    format_version INTEGER NOT NULL DEFAULT 1,

    snapshot BLOB,
    snapshot_through_seq INTEGER NOT NULL DEFAULT 0,

    updated_at TEXT NOT NULL
);

CREATE TABLE document_crdt_updates (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,

    document_id TEXT NOT NULL
        REFERENCES documents(item_id) ON DELETE CASCADE,

    generation INTEGER NOT NULL,
    update_blob BLOB NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX crdt_updates_document
ON document_crdt_updates(document_id, seq);
```

The exact schema might differ depending on how directly Journal adopts Ygo's persistence interfaces, but conceptually the durable representation should be:

```text
snapshot
   +
updates after snapshot
```

rather than a whole-document overwrite on every save.

---

# 4. Compaction

CRDT update logs should not grow forever.

Example:

```text
CRDT snapshot #10000

update 10001
update 10002
...
update 11837
```

Eventually:

```text
snapshot 10000
      +
updates 10001-11837

          ↓ compact

snapshot 11837
```

Old updates can then be pruned according to a retention policy.

However, user-facing revision history should remain distinct from CRDT compaction.

Recommended separation:

```text
CRDT persistence
    short/intermediate synchronization history

Journal revision history
    explicit user-visible snapshots:
        Tuesday 10:42
        Tuesday 14:31
        Wednesday 09:12
```

Compacting CRDT state should never implicitly erase user-visible Journal history.

---

# 5. Frontend changes

Journal already uses Tiptap/ProseMirror, so the editor itself does not need to be replaced.

Likely additions:

```text
yjs
y-prosemirror
@tiptap/extension-collaboration
@tiptap/extension-collaboration-caret
```

The current editor extension list would become dependent on an active collaborative session.

Conceptually:

```ts
editorExtensions({
    ydoc,
    awareness
})
```

rather than:

```ts
extensions: editorExtensions
```

Collaborative undo also changes.

Tiptap's normal undo/redo should be disabled in favor of Yjs-aware undo behavior:

```ts
StarterKit.configure({
    undoRedo: false,
}),
```

plus:

```ts
Collaboration.configure({
    document: ydoc,
})
```

Collaborative undo should mean:

> undo my last logical change

rather than:

> restore the whole document to a prior global state

That avoids accidentally undoing edits created by another collaborator.

---

# 6. Critical migration issue: do not initialize both JSON and Y.Doc

Today Journal initializes Tiptap with something equivalent to:

```ts
content: document.content
```

Once Yjs owns the document, Journal must not independently initialize both the Y.Doc and the editor with the same content.

A bad migration could cause:

```text
SQLite JSON:
"Hello Bill"

        ↓ seed Y.Doc

Y.Doc:
"Hello Bill"

        ↓ editor initializes content again

"Hello BillHello Bill"
```

The correct migration should be:

```text
existing ProseMirror JSON
          ↓
prosemirrorJSONToYDoc()
          ↓
persist initial Yjs state
          ↓
mark document CRDT-enabled
          ↓
from then on initialize Tiptap from Y.Doc only
```

This should be treated as an exactly-once migration.

---

# 7. Lazy migration is preferable

There is no need to convert every document during an application upgrade.

Instead:

```text
Open document
       │
       ├── CRDT state exists
       │       ↓
       │    load normally
       │
       └── no CRDT state
               ↓
          read content_json
               ↓
          create Y.Doc
               ↓
          persist CRDT bootstrap
               ↓
          set crdt_format_version
```

This reduces migration risk.

Before publishing an existing Journal to collaborative cloud storage, however, migrating all documents in that Journal first would likely be prudent.

---

# 8. Materialized ProseMirror JSON

`documents.content_json` should continue to be updated, but as a derived representation.

For example:

```text
CRDT edits
   │
   │ immediate
   ▼
CRDT persistence
   │
   │ debounced
   ▼
ProseMirror JSON materializer
   │
   ├── content_json
   ├── FTS
   └── attachment reference reconciliation
```

This decouples typing from expensive whole-document work.

Search might lag edits by one or two seconds, which is acceptable.

For operations such as:

- application close
- document close
- export
- backup
- encryption migration

Journal should explicitly:

```text
flush CRDT
materialize JSON
then continue
```

---

# 9. Existing autosave logic largely disappears for document bodies

Today Journal maintains whole-document draft state and explicit flushing.

With Yjs/Ygo, document-body autosave changes substantially.

Yjs/Ygo handles:

```text
unsynchronized changes
update ordering
retries
convergence
```

Journal continues to handle:

```text
durable-local status
remote-sync status
JSON materialization
```

Normal non-CRDT saves still remain for metadata such as:

```text
title
spacing preset
folder moves
rename
delete
journal metadata
```

---

# 10. Save status should distinguish local durability from cloud sync

With cloud collaboration:

```text
Saved locally
        ≠
Synced to cloud
```

Recommended UI states:

| State | Meaning |
|---|---|
| **Saving…** | Local CRDT update is not yet durably persisted |
| **Saved locally** | SQLite contains the change |
| **Syncing…** | Local state is durable; remote server is behind |
| **Synced** | Local and remote CRDT state are current |
| **Offline — saved locally** | Safe on this machine, waiting for cloud |
| **Sync error** | Local copy is safe, remote synchronization failed |

This is one of the strongest benefits of a local-first architecture: Internet failure does not interrupt editing.

---

# 11. Local durability needs careful handling

A local CRDT server may batch or debounce writes.

For Journal's aggressive autosave expectations, local persistence should happen quickly, perhaps approximately:

```text
100–300 ms
```

with explicit flushes for:

```text
document close
application shutdown
export
backup
encryption
```

A hard process crash could otherwise lose changes that exist only in the in-memory CRDT.

Journal should distinguish:

```text
update accepted by local WebSocket

from

update durably committed to SQLite
```

Those are not necessarily equivalent.

A useful embedded-server API would be:

```text
FlushRoom(documentID)
```

so the application can establish explicit durability boundaries.

---

# 12. Local WebSocket security

The local collaboration server should bind only to:

```text
127.0.0.1:<random ephemeral port>
```

not:

```text
0.0.0.0:<port>
```

Each application launch should generate a cryptographically random session token.

Conceptually:

```text
ws://127.0.0.1:51829/collab/<room>
Authorization: <random-session-token>
```

Origin checks should also be considered.

Other programs on the user's computer can access localhost, so localhost alone should not be treated as authentication.

---

# 13. Existing cloud locking model should change

The current `CLOUD.md` design assumes an exclusive cloud-Journal lease.

That model works for whole-SQLite-file synchronization but conflicts with simultaneous editing.

Under CRDT synchronization:

```text
MacBook
    \
     \
      cloud CRDT room ← desktop PC
     /
iPad/browser
```

all clients are legitimate writers.

Therefore the advisory lock should no longer govern ordinary document-body editing.

Locks or stronger coordination may still remain useful for:

- destructive administrative actions
- encryption migrations
- imports
- database-wide operations
- some tree mutations
- backup/restore operations

---

# 14. Existing CLOUD.md refactor remains valuable

The proposed separation of concerns in the current cloud design is useful regardless of CRDT adoption.

Conceptually:

```text
LibraryCoordinator

    LocalStore

    CloudJournalSession(s)

JournalContentService
    shared document/item behavior

AppInstallationService
    provider/app/cloud configuration
```

This refactoring should likely happen before CRDT work.

It prevents collaboration concerns from becoming entangled with the existing monolithic service architecture.

---

# 15. OCI storage can remain useful

The existing OCI/cloud artifact design does not need to disappear.

It should change roles.

Instead of:

```text
OCI artifact
    = live synchronization mechanism
```

use:

```text
Cloud collaboration server
    = live state

OCI snapshots
    = immutable Journal backups
      / portability
      / disaster recovery
```

Recommended separation:

```text
Realtime
--------
Yjs/Ygo

Durable working state
---------------------
SQLite locally
Postgres remotely

Attachments
-----------
object storage

Backups
-------
OCI Journal snapshots
```

If CRDT state lives inside `journal.db`, existing SQLite backup mechanisms automatically preserve it.

---

# 16. Cloud synchronization placement

There are two reasonable architectures.

## Option A: frontend connects to both

```text
            ┌── local Ygo
Y.Doc ──────┤
            └── remote Ygo
```

This may be easiest for a prototype.

## Option B: preferred final design

```text
Tiptap/Yjs
     │
     ▼
Local Ygo server
     │
     ├── SQLite
     │
     └── Ygo sync client
             │
             ▼
        Cloud server
```

This is preferable for Journal because provider credentials and cloud behavior remain in Go.

The frontend always deals with:

```text
"connect to my local document server"
```

It does not need to know whether the Journal is:

```text
local-only

or

local + cloud synchronized
```

That keeps the frontend architecture simpler and preserves Journal's local-first character.

---

# 17. Hosted server choice

A natural first hosted implementation would be:

```text
journal-collab-server
    written in Go
    using Ygo
```

Advantages:

- same language as Journal
- likely code reuse between local and hosted services
- no Node requirement
- consistent protocol
- easier operational model

For small-scale hosting:

```text
Ygo
+
SQLite on persistent storage
```

could be sufficient.

For multi-user production:

```text
Ygo
+
PostgreSQL persistence
+
object storage
```

would likely be preferable.

Because the protocol is Yjs-compatible, Hocuspocus could remain an alternate hosted server if needed later.

That protocol independence is worth preserving.

A significant caution is that Ygo is newer than Yjs/Hocuspocus.

Journal should therefore include a compatibility test suite covering:

```text
Yjs ↔ Ygo
```

before depending on it as a core storage layer.

---

# 18. Do not make the entire Journal hierarchy a CRDT initially

Restrict CRDT synchronization initially to:

> document content

Avoid immediately turning all Journal metadata into one giant collaborative CRDT.

Keep these transactional at first:

```text
journals
folders
titles
ordering
permissions
attachments
deletion
```

Those data types have different consistency requirements from rich text.

For shared Journals, metadata can use an ordinary revision field such as:

```text
item_revision
```

plus optimistic concurrency.

---

# 19. Metadata conflict rules should be explicit

Example:

```text
Alice: rename "Trip" → "Korea Trip"

Bob:   rename "Trip" → "2026 Trip"
```

Possible policies:

```text
last write wins
```

or:

```text
reject second write
ask client to refresh
```

Simple names could reasonably use last-write-wins.

More serious operations should use stronger revision checks:

```text
move
delete
restore
permissions
encryption
```

---

# 20. Deletion while another user edits

Example:

```text
Alice                     Bob

editing doc A              deletes doc A
    │                         │
    └──────── cloud ──────────┘
```

Use tombstones rather than immediately destroying the document state.

For example:

```text
document.deleted_at = timestamp
```

Alice's client should be informed:

> This document was deleted by another collaborator. Your local changes have been preserved.

Possible actions:

```text
Discard
Restore document
Save my edits as a new document
```

Deleting a row should not immediately destroy unsynchronized CRDT history.

---

# 21. Attachments need a separate synchronization strategy

Journal already has a useful separation:

```text
attachment bytes
+
document node containing attachment ID
```

Keep that model.

For local Journals:

```text
attachment bytes → SQLite
node → CRDT
```

For cloud Journals:

```text
attachment bytes
    ↓
local SQLite
    ↓
upload service/object storage
    ↓
stable attachment ID

CRDT contains attachment ID
```

Other clients resolve:

```text
attachment://<uuid>
```

through the cloud attachment service.

---

# 22. Attachment garbage collection becomes more dangerous

Today an attachment can potentially be removed once the current document representation no longer references it.

Collaborative editing changes that.

Example:

```text
Alice removes image X

Bob, offline, still has image X referenced

Alice's JSON materializes

attachment reconciliation:
"X isn't used → delete it"
```

Bob later reconnects and restores the old paragraph through CRDT synchronization.

The attachment node returns, but the underlying bytes are gone.

Recommended approach:

```text
unreferenced
    ↓
mark orphan candidate
    ↓
wait grace period
    ↓
verify against current CRDT materialization
    ↓
garbage collect
```

Unreferenced attachments should probably be retained for days rather than immediately deleted.

Disk space is cheaper than user data.

---

# 23. Encryption is the largest complication

The current Journal encryption approach encrypts one document payload.

CRDT persistence instead creates:

```text
snapshot
update
update
update
...
```

For local encrypted Journals, a custom persistence adapter can encrypt each CRDT update.

Conceptually:

```text
Yjs update
   ↓
existing journal data key
   ↓
AEAD encryption
   ↓
SQLite BLOB
```

Snapshots should also be encrypted.

Associated data should include stable contextual fields such as:

```text
document ID
generation
update sequence
key ID
```

to prevent ciphertext from being silently moved between records.

---

# 24. Cloud encryption is much harder

A conventional hosted Yjs/Ygo server normally needs to understand CRDT state to:

```text
merge updates
calculate state vectors
produce diffs
compact state
```

If all updates are opaque encrypted blobs:

```text
client encrypts all CRDT updates
        ↓
server cannot decrypt
```

the conventional server architecture breaks down.

Possible approaches:

| Approach | Difficulty | Privacy |
|---|---:|---|
| No collaborative cloud for encrypted Journals initially | Low | Preserves current guarantees |
| Cloud server can decrypt collaborative Journals | Medium | Server-readable |
| True end-to-end encrypted CRDT | High | Strongest |

For a first collaborative release, the safest policy is:

> Do not support collaborative cloud editing for encrypted Journals yet.

Do not silently weaken Journal's existing encryption expectations in order to add collaboration.

---

# 25. True E2EE collaboration is a separate project

A future E2EE architecture could use a shared Journal data key:

```text
shared Journal data key
        ↓
wrapped separately for Alice
wrapped separately for Bob
wrapped separately for Carol
```

The server could then behave more like an encrypted event mailbox.

But this introduces substantial complexity:

```text
encrypted snapshots
client-side compaction
history
key rotation
member removal
new-member key distribution
revocation
server-side search limitations
```

This should be treated as a later phase rather than part of initial CRDT adoption.

---

# 26. Schema-version compatibility

CRDT convergence does not solve editor-schema compatibility.

Example:

Journal 1.4 supports:

```text
attachmentImage
table
taskItem
```

Journal 1.6 adds:

```text
calloutBox
```

Alice on 1.6 inserts a `calloutBox`.

Bob on 1.4 receives the CRDT update.

His ProseMirror schema may not understand that node.

Every collaboration connection should therefore advertise something like:

```text
app_version
editor_schema_version
crdt_format_version
```

The server should determine:

```text
fully compatible
read-only compatible
incompatible
```

before allowing normal editing.

---

# 27. Older Journal versions are dangerous after migration

Once CRDT state becomes authoritative, older Journal versions must not continue treating `content_json` as authoritative.

Otherwise:

```text
new version:
CRDT says A

old version:
edits materialized JSON → B

new version reopens:
CRDT says A
JSON says B
```

Now there are two competing sources of truth.

Once a Journal crosses the CRDT migration boundary, it should record something like:

```text
minimum_app_version
```

Older clients should be prevented from opening it read/write.

This should be considered mandatory.

---

# 28. Document duplication

Do not simply copy CRDT rows.

Recommended flow:

```text
source Y.Doc
      ↓
materialize ProseMirror JSON
      ↓
remap attachments if necessary
      ↓
create completely new Y.Doc
      ↓
new document / room ID
```

That ensures the duplicate has an independent collaborative identity and history.

---

# 29. Imports and exports

Markdown import:

```text
Markdown
   ↓
ProseMirror JSON
   ↓
new Y.Doc
   ↓
CRDT snapshot
```

Export:

```text
Y.Doc
   ↓
latest ProseMirror JSON
   ↓
Markdown
```

This is another reason to retain the materialized JSON layer.

---

# 30. Separate local document IDs from cloud room IDs

Do not use the local SQLite `item_id` directly as a cloud room identifier.

Prefer:

```text
local document ID:
a479...

cloud document ID:
ec52...

cloud room ID:
random opaque UUID
```

with an explicit mapping table.

This prevents problems when:

```text
a Journal is copied
a backup is restored
a database is imported
a document is duplicated
```

It also avoids exposing meaningful local database identifiers in network URLs.

---

# 31. Presence

Presence should remain ephemeral.

Examples:

```text
Bill
    cursor paragraph 12

Alice
    selecting paragraph 8

Bob
    online
```

Use Yjs awareness for this.

Do not store presence in SQLite.

Presence should expire naturally when connections disappear.

---

# 32. File-by-file change summary

| Current area | Proposed change |
|---|---|
| `frontend/package.json` | Add Yjs/Tiptap collaboration dependencies |
| `editor/extensions.ts` | Turn extension list into factory accepting Y.Doc/awareness; CRDT-aware undo |
| `App.tsx` | Replace full-document draft autosave with collaborative session lifecycle |
| `document_service.go` | Stop treating `content_json` as document-body authority |
| `autosave.go` | Become CRDT durability/materialization coordinator |
| `sqlite_schema.go` | Add CRDT snapshot/update/mapping tables |
| `sqlite_repository.go` | Add Journal-specific Ygo persistence adapter |
| encryption services | Encrypt CRDT snapshots/updates locally |
| attachment services | Add delayed GC and cloud attachment resolution |
| cloud layer | Replace live OCI-file publication with CRDT synchronization |
| `CLOUD.md` locking | Remove exclusive edit lease for ordinary editing |
| cloud backup | Keep; now backs up CRDT state as well |
| new `crdt_service.go` | Embedded loopback Ygo server |
| new `cloud_collab.go` | Sync local rooms with hosted rooms |

---

# 33. What should and should not be CRDT-controlled initially

| Data | CRDT? |
|---|---|
| Document rich-text body | **Yes** |
| Cursor/presence | Awareness protocol |
| Title | No |
| Folder name | No |
| Folder hierarchy | No |
| Sort order | No |
| Delete/restore | No |
| Permissions | No |
| Encryption settings | No |
| Attachment bytes | No |
| Attachment references in document | **Yes** |
| Comments eventually | Possibly CRDT anchors |
| Search index | No; derived data |

This boundary keeps the architecture understandable.

---

# 34. Main advantages

The largest advantage is that local and cloud collaboration stop being different storage systems.

The application becomes:

```text
local Journal
    works entirely offline

cloud Journal
    works entirely offline too
    but synchronizes whenever connectivity exists

shared Journal
    same thing
    plus multiple peers
```

Additional advantages:

- No "last whole-document save wins" behavior.
- No cloud edit lease for document bodies.
- Editing remains immediate because it always occurs locally.
- Simultaneous editing becomes natural.
- Offline edits are first-class.
- SQLite remains the local durable store.
- Existing Tiptap editor can largely remain.
- Existing JSON, FTS, Markdown, and attachment infrastructure can remain as derived systems.
- Go can remain the sole native backend runtime.
- Hosted server technology remains replaceable because Yjs compatibility forms the protocol boundary.
- Cloud synchronization becomes an optional capability rather than a separate storage mode.

---

# 35. Main disadvantages

The architecture introduces meaningful complexity.

Important costs include:

- CRDT persistence is more complex than a JSON column.
- Authoritative and derived representations must be kept conceptually separate.
- Backup/revision history must be distinct from CRDT history.
- Attachments need independent synchronization.
- Encryption becomes significantly harder.
- Old-client compatibility must be actively enforced.
- Metadata conflicts still exist.
- CRDT storage grows until compacted.
- Synchronization bugs are more difficult to debug than simple database updates.
- Ygo is newer than Yjs/Hocuspocus, making interoperability testing important.
- Testing must include distributed and offline failure cases, not just editor behavior.

---

# 36. Edge cases to test before release

1. Alice and Bob type at exactly the same position.
2. One user deletes text while another formats it.
3. Concurrent edits occur inside tables and task lists.
4. A laptop remains offline for a week and reconnects.
5. The server dies immediately after receiving an update.
6. The desktop process crashes before a local persistence batch flush.
7. A device sleeps in the middle of synchronization.
8. A cloud token expires during editing.
9. Permissions change from editor to viewer while the user is connected.
10. A document is deleted while another device edits it.
11. A folder is moved while another device renames it.
12. An attachment is inserted before another client has downloaded it.
13. An image is removed and later restored by an offline client.
14. An older application version opens a newer CRDT document.
15. A new ProseMirror node type reaches an older client.
16. CRDT compaction occurs while clients remain connected.
17. A backup snapshot occurs during active remote editing.
18. Encryption is enabled while a document is open.
19. The master password changes while cloud synchronization is active.
20. SQLite becomes full or returns `SQLITE_BUSY` during an update.
21. Two Journal application instances run on the same computer.
22. A malicious local process attempts to connect to the loopback WebSocket.
23. A restored database contains an old cloud-room mapping.
24. The user duplicates a cloud document.
25. The user restores an old backup and reconnects it to the existing cloud room.

These should be considered part of the design, not merely QA cleanup.

---

# 37. Recommended implementation phases

## Phase 1 — Service/store refactor

Implement the store separation already contemplated by `CLOUD.md`.

No user-visible changes.

Primary goal:

```text
JournalContentService
        ↓
store-independent document behavior

LibraryCoordinator
        ↓
routes local/cloud backing stores
```

This gives CRDT support a clean architectural boundary.

---

## Phase 2 — Local CRDT only

Introduce:

```text
Tiptap
↕
Yjs
↕
embedded Ygo
↕
SQLite
```

Keep the application entirely local.

Prove that the following still work correctly:

```text
editing
autosave
restart
search
attachments
import/export
encryption
backup
```

This is the critical architectural phase.

---

## Phase 3 — Two-device synchronization

Run a development hosted Ygo server and synchronize a Journal between two devices owned by one user.

Example:

```text
Bill's Mac
       ↕
Bill's cloud account
       ↕
Bill's desktop
```

Do not add multi-user sharing yet.

The goal is to replace the current "only one device writes at a time" cloud model.

---

## Phase 4 — Collaboration

Add:

```text
users
journal membership
editor/viewer roles
awareness
remote cursors
```

Now multiple people can edit the same document.

---

## Phase 5 — Collaborative metadata/tree operations

Improve conflict semantics for:

```text
rename
move
delete
restore
permissions
```

based on real usage.

---

## Phase 6 — Encrypted cloud collaboration

Only after the rest of the collaboration stack is stable should Journal consider true end-to-end encrypted shared collaboration.

Treat this as a separate architectural project.

---

# Preferred end state

```text
                       React
                        │
                      Tiptap
                        │
                 Tiptap Collaboration
                        │
                       Yjs
                        │
             localhost WebSocket
                        │
              ┌─────────▼─────────┐
              │ Embedded Ygo      │
              │ collaboration     │
              │ service           │
              └─────┬────────┬────┘
                    │        │
             always │        │ cloud journals
                    │        │
                    ▼        ▼
              journal.db   Ygo sync client
              SQLite            │
                                │
                               TLS
                                │
                     ┌──────────▼──────────┐
                     │ Journal Collab      │
                     │ Server              │
                     │                     │
                     │ Ygo                 │
                     │ Auth/ACL            │
                     │ PostgreSQL          │
                     │ Object storage      │
                     └─────────────────────┘
```

---

# Overall recommendation

For Journal, a CRDT architecture is a better long-term fit for cloud editing than synchronizing whole SQLite database revisions.

The existing cloud design is elegant for immutable whole-database synchronization, but that approach naturally requires locks and conflict avoidance because the database file is the synchronization unit.

With CRDT-based collaboration, the synchronization unit becomes the edit operation.

The first implementation target should therefore **not** be the hosted server.

The first target should be replacing Journal's existing:

```text
editor.getJSON()
    ↓
UpdateDocumentDraft
    ↓
FlushDocument
```

pipeline with:

```text
Tiptap/Yjs
    ↓
embedded Ygo
    ↓
SQLite
```

while continuing to materialize `content_json`.

Once the local CRDT architecture is stable, a hosted Ygo-compatible server becomes an incremental synchronization layer rather than a second application architecture.

That is the architectural hinge:

> If the local CRDT boundary is designed correctly, local-only Journal remains every bit as local, portable, and self-contained as it is today, while cloud collaboration becomes a capability that can be switched on rather than a separate storage model.
