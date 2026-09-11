# Journal Application Plan

## Source Documents

This plan is based on:

- `REQUIREMENTS.md`
- Design notes from the earlier prototype

The production application should keep the prototype's proven Wails, Go, React, TypeScript, Tiptap, and ProseMirror foundation, but replace the prototype's explicit local JSON file workflow with a SQLite-backed document library and automatic persistence model.

## Requirements Summary

Journal is a small desktop web application for macOS and Linux. It provides a basic word processor plus self-contained file management.

The UI has three primary areas:

1. A large editor area taking most of the center and right side.
2. A narrow outliner on the left side.
3. A toolbar or menu bar across the top.

Documents are stored as ProseMirror JSON in a SQLite database. Each document has an editable title. Creating a new document immediately creates a database entry named `Untitled`, opens it in the editor, and autosaves later changes without requiring explicit save commands.

The outliner displays folders and documents. Users can open documents, create folders, rename folders, delete documents and folders, drag documents between folders, and search across all documents. Deleted items move to a system-defined Trash folder unless they are already in Trash, where deletion permanently removes them after confirmation.

## Prototype Comparison

The prototype already validates several useful choices:

- Wails is a good fit for a local desktop shell.
- Go is a good trusted backend for persistence and platform integration.
- React and TypeScript are a good fit for the editor UI.
- Tiptap OSS provides the rich text editing layer.
- ProseMirror JSON is a suitable canonical document format.
- A three-column desktop layout works well for a writing application.

The production app must change these prototype assumptions:

| Area | Prototype | Production Journal |
| --- | --- | --- |
| Persistence | Manual open/save JSON files | SQLite database |
| Save behavior | Explicit save/save-as | Automatic background save |
| Outliner | Current document heading outline | Document/folder library tree |
| Document lifecycle | File dialogs | Database-backed create/open/update |
| Deletion | Not modeled | Trash, restore, permanent delete |
| Search | Not implemented | Search across all documents |
| JSON inspector | Always visible debug panel | Debug-only or removed from default UI |

## Recommended Architecture

Use the same high-level split as the prototype, with stricter production boundaries:

```text
Journal Wails App
├── Go backend
│   ├── SQLite repository
│   ├── schema migrations
│   ├── document/folder service
│   ├── lightweight autosave worker
│   ├── ProseMirror validation
│   ├── search text extraction
│   ├── application preferences
│   └── Wails API methods
└── React frontend
    ├── app state model
    ├── outliner tree
    ├── toolbar/menu commands
    ├── Tiptap editor
    ├── autosave coordinator
    ├── search/filter UI
    └── confirmation/error surfaces
```

The backend owns persistence, migrations, tree invariants, Trash rules, and validation. The frontend owns editing behavior, command presentation, selection state, drag/drop interactions, and user-facing save/search status.

Autosave should be implemented as a lightweight Go goroutine managed by the backend. The frontend reports document edits to the backend, and the backend autosave worker periodically flushes pending document changes to SQLite. The save interval is an application setting and can be changed without rebuilding the app.

## Technology Choices

### Desktop Shell

Use Wails v2, matching the prototype.

Reasons:

- Works naturally with Go backend services.
- Provides native desktop packaging.
- Allows the frontend to remain a normal React/Vite application.
- Already proven by the prototype.

Target macOS and Linux first. Keep in mind that Linux Wails builds should happen on Linux with the required WebKitGTK dependencies.

### Backend

Use Go for:

- SQLite access.
- Data model and tree operations.
- Document validation.
- Search indexing.
- Wails-bound application methods.

Use a layered backend structure:

```text
internal/db
internal/repository
internal/service
internal/prosemirror
internal/search
```

### Database

Use SQLite as the canonical storage layer.

Use pure Go SQLite for the MVP, with FTS enabled for full-text document search.

Recommended driver:

- `modernc.org/sqlite`

Reasons:

- Avoids CGO requirements for macOS and Linux packaging.
- Keeps the backend implementation pure Go.
- Supports the self-contained desktop application goal.
- Provides SQLite FTS support needed for MVP search.

If FTS support or packaging behavior becomes a blocker, reassess the driver choice before implementing higher-level repository code. The MVP should not fall back to plain `LIKE` search unless FTS is proven unavailable in the target build.

### Frontend

Use React, TypeScript, Vite, Tiptap OSS, and lucide-react, continuing from the prototype.

Keep the Tiptap extension set close to the prototype initially:

- StarterKit
- Link
- Underline
- Highlight
- TaskList and TaskItem
- Table support
- Placeholder
- Typography
- TextAlign
- CharacterCount

Defer import/export, comments, collaboration, plugins, and advanced asset handling until the core document library is stable.

## Data Model

Use a tree-oriented model with separate document content.

```sql
items (
  id TEXT PRIMARY KEY,
  parent_id TEXT NULL REFERENCES items(id),
  kind TEXT NOT NULL CHECK (kind IN ('folder', 'document')),
  title TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0,
  system_key TEXT NULL UNIQUE,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
)

documents (
  item_id TEXT PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
  schema_version INTEGER NOT NULL,
  content_json TEXT NOT NULL,
  search_text TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
)

library_search_fts (
  item_id UNINDEXED,
  kind UNINDEXED,
  title,
  body
)

app_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL
)
```

Initial rules:

- Documents must have a row in both `items` and `documents`.
- Folders exist only in `items`.
- `app_settings` stores user-configurable settings such as the autosave interval.
- `library_search_fts` is an FTS virtual table maintained by repository transactions.
- Folder rows are indexed by title with an empty body.
- Document rows are indexed by title and extracted ProseMirror plain text.
- `items.system_key = 'trash'` identifies the system Trash folder.
- Trash is created by bootstrap migration if missing.
- Trash cannot be renamed, moved, deleted, or permanently removed.
- User-created documents and folders can be moved into Trash.
- Deleting an item already inside Trash permanently deletes it and all descendants.
- Dragging an item out of Trash restores it to the selected destination.

Consider adding these tables after the MVP is stable:

```text
recent_items
document_revisions
```

## Backend API

Expose intent-oriented Wails methods rather than raw persistence operations:

```text
GetLibraryTree() TreeResponse
CreateDocument(parentID *string) DocumentResponse
CreateFolder(parentID *string, title string) ItemResponse
RenameItem(id string, title string) ItemResponse
MoveItem(id string, newParentID *string, newSortOrder int) TreeResponse
MoveItemToTrash(id string) TreeResponse
PermanentlyDeleteItem(id string) TreeResponse
OpenDocument(id string) DocumentResponse
UpdateDocumentDraft(id string, content map[string]any) DocumentDraftResponse
FlushDocument(id string) DocumentSaveResponse
SearchLibrary(query string) SearchResponse
GetAppSettings() AppSettingsResponse
UpdateAppSettings(settings AppSettingsPatch) AppSettingsResponse
```

Important backend behavior:

- Every tree mutation runs in a transaction.
- Illegal operations return typed errors.
- ProseMirror JSON is validated before saving.
- Search text is extracted from ProseMirror JSON during saves.
- `UpdateDocumentDraft` records the latest pending editor content in backend memory.
- The autosave goroutine flushes pending drafts to SQLite on the configured interval.
- `FlushDocument` forces an immediate save and is used before document switches and app shutdown.
- Switching documents should be safe because the frontend can flush pending edits through `FlushDocument` before `OpenDocument`.

## Frontend State Model

Keep state explicit and small:

```text
libraryTree
selectedDocumentID
activeDocument
expandedItemIDs
renamingItemID
activeDragState
editorDirty
saveState
searchQuery
searchResults
pendingOperation
lastError
```

Autosave should be coordinated in one place, not scattered through editor components.

The frontend should not directly implement timed database writes. It should send content changes to the backend as draft updates, show save status from backend responses/events, and call `FlushDocument` for immediate persistence before document switches. The backend goroutine owns the repeating save interval.

Recommended save states:

```text
idle
dirty
saving
saved
error
```

When a user selects another document:

1. If the current document has pending edits, flush the save immediately.
2. If the flush fails, keep the user on the current document, show a retryable error, and do not replace editor content.
3. Load the selected document from the backend.
4. Replace the editor content without marking the document dirty.
5. Focus the editor or title according to the interaction that opened it.

## UI Design

The main window should be a quiet desktop writing tool, not a dashboard or landing page.

Layout:

```text
┌────────────────────────────────────────────────────────────┐
│ Toolbar / Menu Bar                                         │
├──────────────┬─────────────────────────────────────────────┤
│ Outliner     │ Document title                              │
│              │ Editor                                      │
│ Folders      │                                             │
│ Documents    │ Autosave status                             │
│ Trash        │                                             │
└──────────────┴─────────────────────────────────────────────┘
```

Toolbar commands:

- New document
- New folder
- Delete or move to Trash
- Search toggle
- Basic formatting commands
- Undo/redo

Outliner behavior:

- Shows top-level folders/documents plus Trash.
- Supports expand/collapse.
- Supports selecting documents.
- Supports rename for folders and documents.
- Supports drag/drop for documents and folders.
- Shows confirmation dialogs for delete and permanent delete.
- Prevents invalid Trash operations in the UI, while backend still enforces them.
- Handles long titles with truncation and tooltip/full-name affordance.
- Supports deep nesting with stable indentation, horizontal overflow prevention, and vertical scrolling.
- Allows the sidebar to be resized within a defined minimum and maximum width.
- Provides keyboard navigation for tree items, rename, delete, expand/collapse, and search focus.

Editor behavior:

- New document title starts as `Untitled`.
- Title is editable at any time.
- Body changes are sent to the backend as pending drafts and saved by the backend autosave goroutine.
- Save failures are visible and retryable.
- The editor remains usable while normal saves are pending.
- Empty or whitespace-only titles are rejected or normalized back to `Untitled`.
- Duplicate titles are allowed, but the UI should remain clear by using selection, folder context, and stable item IDs.
- Creating a document focuses the title when initiated as a naming action, otherwise focuses the editor body so the user can begin typing.
- Tables and wide editor content should scroll or constrain horizontally without breaking the main layout.

## UI Edge Cases and Interaction Rules

First-run and empty-library behavior:

- Bootstrap creates Trash, but user-created content may be empty.
- The empty editor area should show a quiet empty state with available create-document/create-folder actions.
- Toolbar actions that require an active document should be disabled until a document is open.
- Creating the first document should create the database row, select it in the outliner, open it in the editor, and start autosave coordination immediately.

Title and rename behavior:

- Title edits commit on Enter, blur, or explicit confirmation, and cancel on Escape.
- Rename failures leave the previous title visible and show a retryable error.
- Starting a rename while search is active should either keep the filtered context stable or temporarily suspend filtering until the rename is committed.
- Search index updates caused by title changes must not remove the item from view while the rename control is still active.

Document switching and deletion behavior:

- Failed forced saves block document switching so unsaved editor content is not replaced.
- Deleting the currently open document moves it to Trash, clears or replaces the editor selection according to the resulting tree state, and never leaves the editor bound to a permanently deleted item.
- Permanently deleting the currently open document requires confirmation and then clears the active editor state.
- If a folder containing the active document is deleted or permanently deleted, the editor follows the same active-document cleanup rules.

Trash behavior:

- Delete confirmations should distinguish move-to-Trash from permanent delete.
- Folder delete confirmations should communicate that descendants are included, ideally with a document/folder count.
- Dragging an item into Trash should use the same behavior as delete.
- Restoring an item from Trash is a normal move to the selected destination; if the original parent no longer exists, restore to the chosen folder or top level.

Drag/drop behavior:

- Invalid drops are visibly rejected before the backend call.
- Folders cannot be dropped into themselves or their descendants.
- Items cannot be dropped into documents.
- Trash cannot be moved, renamed, deleted, or used as a child of another item.
- Drop indicators should show whether the operation will insert before/after an item or move into a folder.
- Hovering over a collapsed folder during drag should auto-expand after a short delay.
- Sort order should remain deterministic after every move, including moves into empty folders and top level.

Search UI behavior:

- Live search should be debounced enough to avoid UI churn while typing.
- Search should show an explicit no-results state.
- Escape should clear the search field when focused, and clearing the field restores the full expanded hierarchy.
- If the user searches while the active document has unsaved edits, search results may reflect the last saved content until the next flush; the UI should avoid implying unsaved body text is already indexed.
- Matching descendants should reveal ancestor folders. Matching folders should remain expandable so users can inspect contained items in context.

Responsive and layout behavior:

- The toolbar should wrap, collapse, or group commands predictably at narrow window widths.
- Autosave status should remain visible without shifting editor content or distracting from typing.
- The app should maintain usable minimum dimensions for macOS and Linux windows.
- Long documents, large tables, and deeply nested outliner trees should not make the toolbar or title editor unreachable.

## Search Design

Search belongs to the outliner and is part of the MVP. It should use SQLite full-text search, not a simple title/body substring scan.

Behavior:

- Clicking the search icon opens a search field.
- Typing filters automatically.
- Clearing the field restores the full hierarchy.
- Search should match document titles, folder titles, and document body text through the SQLite FTS index.
- Search input updates should be debounced in the frontend while still feeling live.
- Empty queries should not call the FTS endpoint unnecessarily.
- No-result searches should display an explicit empty state in the outliner.

Implementation:

- Extract plain text from ProseMirror JSON whenever content is saved.
- Store it in `documents.search_text`.
- Maintain an FTS virtual table for folder titles, document titles, and extracted document text.
- Keep the FTS index synchronized transactionally with document title/content updates.
- Use regular tree data to reconstruct ancestor context for search results.

Filtered tree display should include:

- Matching documents.
- Matching folders.
- Ancestor folders needed to show matched descendants in context.

## Implementation Phases

### Phase 1: Project Bootstrap

- Initialize the production Wails app under `/Users/billdettelback/dev/journal`.
- Bring forward the prototype's React/Tiptap editor foundation.
- Remove file open/save/save-as workflows.
- Keep the three-area desktop layout.
- Add initial app build and test commands.

### Phase 2: SQLite Foundation

- Add pure Go SQLite driver, preferably `modernc.org/sqlite`.
- Add database path resolution.
- Add schema migrations.
- Add FTS schema for document title/body search.
- Add `app_settings` storage with a default autosave interval.
- Add bootstrap creation of system Trash.
- Add repository tests for migration and bootstrap behavior.

### Phase 3: Library Service

- Implement create document.
- Implement create folder.
- Implement rename.
- Implement move and reorder.
- Implement move to Trash.
- Implement permanent delete.
- Implement restore by moving out of Trash.
- Add tests for all tree and Trash invariants.

### Phase 4: Document Service

- Implement blank ProseMirror document creation.
- Implement open document.
- Implement draft persistence and forced flush behavior.
- Validate document root and schema version.
- Extract search text.
- Add tests with fixture ProseMirror documents.

### Phase 5: Wails API Bindings

- Expose backend service methods to the frontend.
- Generate TypeScript bindings.
- Add a thin frontend API wrapper.
- Keep backend errors structured enough for UI handling.

### Phase 6: Outliner UI

- Render folder/document tree.
- Add selection behavior.
- Add create folder/document commands.
- Add rename UI.
- Add delete confirmations.
- Add Trash display and permanent delete behavior.
- Add drag/drop for moving documents and folders.
- Add sidebar resize constraints, long-title handling, stable scrolling, and deep-nesting behavior.
- Add keyboard navigation and visible focus states for tree interactions.
- Add invalid-drop feedback, drop position indicators, and drag auto-expand behavior.

### Phase 7: Editor and Autosave

- Load selected document into Tiptap.
- Add editable title.
- Add backend draft update API.
- Add lightweight Go autosave goroutine.
- Make the autosave interval configurable through app settings.
- Save title changes.
- Flush pending saves before document switches.
- Add save status display.
- Add retry handling for failed saves.
- Block document switches when a forced flush fails.
- Handle deletion or permanent deletion of the active document.
- Add layout handling for long documents, wide tables, and narrow windows.

### Phase 8: Search

- Add search icon and search input in the outliner.
- Add backend FTS search endpoint.
- Add live filtering.
- Preserve ancestor context for matched results.
- Restore full hierarchy when search is cleared.
- Add debouncing, no-results state, Escape-to-clear behavior, and stable rename behavior while search is active.

### Phase 9: Polish and Hardening

- Add keyboard shortcuts.
- Improve focus management.
- Add empty states.
- Add accessible labels and dialogs.
- Hide or remove the prototype JSON inspector from default UI.
- Add app settings UI for the autosave interval.
- Verify first-run behavior, empty-library affordances, minimum window size, toolbar overflow, and autosave status placement.

### Phase 10: Packaging and Verification

- Run Go tests.
- Run frontend typecheck/build.
- Manually smoke test in Wails dev mode.
- Build macOS package locally.
- Document Linux build requirements and perform Linux builds on Linux.

## Testing Plan

Backend tests:

- Migration creates all tables.
- Bootstrap creates exactly one Trash folder.
- Trash cannot be renamed, moved, deleted, or permanently removed.
- Creating a document creates both item and document rows.
- Folder subtree moves work.
- Delete outside Trash moves subtree to Trash.
- Delete inside Trash permanently deletes subtree.
- Invalid ProseMirror JSON is rejected.
- Search text extraction handles paragraphs, headings, lists, tasks, and tables.
- FTS search returns folder title, document title, and document body matches.
- FTS index rows are updated when titles change, content changes, items move to Trash, and items are permanently deleted.
- Autosave worker flushes pending drafts on the configured interval.
- Autosave interval can be changed through app settings.
- Forced flush saves pending changes before document switches.

Frontend tests:

- Toolbar commands call the correct API methods.
- Outliner renders nested folders/documents.
- Rename updates visible state.
- Search filters and clears correctly.
- Autosave state transitions behave correctly.
- Failed forced saves block document switching and preserve editor content.
- Empty and whitespace-only title edits are handled consistently.
- Long titles, deep nesting, and narrow sidebar widths remain usable.
- Invalid drag/drop targets are rejected before backend calls.
- Deleting or permanently deleting the active document clears or replaces editor state safely.
- Search debounce, no-results, Escape clear, and rename-while-searching behavior work correctly.

End-to-end smoke tests:

- Launch app.
- Verify first-run or empty-library state.
- Create a document.
- Edit title and body.
- Switch documents and verify save.
- Create folders and move documents.
- Delete to Trash.
- Restore from Trash.
- Permanently delete from Trash.
- Search for body text across documents.

## Key Risks

Autosave correctness is the highest-risk area. The app must not lose edits when switching documents, closing the app, or hitting a save error.

Tree invariants are the second major risk. Trash rules, recursive folder behavior, and drag/drop moves should be enforced in the backend and heavily tested.

Outliner interaction complexity is a UI correctness risk. Long titles, deep nesting, active search filters, rename controls, and drag/drop feedback can easily produce confusing or destructive actions unless the interaction rules are explicit and tested.

SQLite driver choice can affect Linux packaging and FTS support. Because pure Go SQLite plus FTS is part of the MVP, validate `modernc.org/sqlite` FTS behavior early with a repository test before building higher-level search UI.

FTS index synchronization is a correctness risk. Title changes, document content saves, deletes, permanent deletes, and migrations must keep search results consistent with the library tree.

Autosave timing is a lifecycle risk. The goroutine should be lightweight, stoppable on app shutdown, race-safe around pending drafts, and configurable through persisted app settings.

## MVP Definition

The first complete version is done when a user can:

- Launch Journal on macOS.
- Create documents.
- Edit document title and body.
- Trust that changes autosave through the backend goroutine.
- Recover cleanly from visible, retryable save errors without losing the active editor content.
- Change the autosave interval in app settings.
- Create folders.
- Move documents between folders.
- Delete documents and folders to Trash.
- Restore items from Trash.
- Permanently delete items from Trash.
- Search across all documents using SQLite full-text search.
- Quit and relaunch with all data intact.
