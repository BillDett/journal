# ADR 001: local CRDT transport has no listener

## Status

Accepted for Journal 1.6.

## Decision

Journal document bodies use native Yjs V1 updates. The renderer owns the
`Y.Doc` and sends snapshots, incremental updates, and materialized
ProseMirror projections to the Go process through Wails IPC. The local
application does not start an HTTP, TCP, or WebSocket listener.

SQLite stores opaque Yjs update bytes and encrypted payloads for encrypted
Journals. Base64 is used only at the JSON IPC boundary. It is not part of the
stored CRDT representation or a future server protocol.

## Consequences

- Local editing remains offline, single-process, and free of localhost attack
  surface or port management.
- Future collaboration must use the same native Yjs V1 update format and sit
  behind a transport adapter; it must not redefine document storage.
- Go does not convert Yjs documents to ProseMirror. The renderer supplies the
  derived JSON projection after acknowledged writes.
- A future multi-window or remote adapter needs state-vector catch-up and
  scoped update events before it is enabled.
