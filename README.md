# Journal

Journal is a local desktop writing application built with Wails, Go, React, TypeScript, Tiptap, ProseMirror, and SQLite.

It provides a basic rich-text editor with a document library stored in a local SQLite database. Documents are saved as ProseMirror JSON and autosaved in the background.

## Current Features

- SQLite-backed document and folder library
- Editable document titles
- Tiptap rich-text editor with headings, formatting, lists, tasks, tables, autolinking, highlights, and word count
- Automatic draft persistence through the Go backend
- Folder and document creation, rename, move, and delete
- Trash folder with permanent delete behavior for items already in Trash
- Drag and drop into folders or back to the top level
- Full-text search across folder titles, document titles, and saved document body text using SQLite FTS
- Configurable autosave interval in the app UI

## Project Layout

```text
.
├── app.go                  # Wails-bound Go backend and SQLite service
├── app_test.go             # Backend persistence and library tests
├── main.go                 # Wails app entrypoint
├── frontend/               # React/Vite/Tiptap frontend
├── build/                  # Wails packaging assets
├── design/                 # Static UI design reference
├── PLAN.md                 # Implementation plan
└── REQUIREMENTS.md         # Product requirements
```

## Requirements

- Go 1.23 or newer
- Node.js and npm
- Wails v2 CLI

Linux builds should be produced on Linux with the Wails WebKitGTK dependencies installed.

## Development

Install frontend dependencies:

```sh
cd frontend
npm install
```

Run the app in Wails development mode:

```sh
wails dev
```

Run backend tests:

```sh
go test ./...
```

Build the frontend only:

```sh
cd frontend
npm run build
```

Build the desktop application:

```sh
wails build
```

On macOS, the packaged app is written to:

```text
build/bin/journal.app
```

## Standalone macOS Build for Apple Silicon

Build a standalone macOS app bundle for Apple Silicon Macs from the repository root:

```sh
wails build -clean -platform darwin/arm64
```

This builds the React frontend, embeds it in the Go/Wails application, and writes the macOS app bundle to:

```text
build/bin/journal.app
```

Run it from Finder, or from Terminal:

```sh
open build/bin/journal.app
```

The actual executable inside the bundle is:

```text
build/bin/journal.app/Contents/MacOS/journal
```

To install the app locally, copy `build/bin/journal.app` into `/Applications`.

## Data Storage

By default, Journal stores its SQLite database under the operating system user config directory:

```text
<user-config-dir>/Journal/journal.db
```

For development or tests, set `JOURNAL_DB_PATH` to use a specific database file:

```sh
JOURNAL_DB_PATH=/tmp/journal-dev.db wails dev
```

The database file and SQLite sidecar files are intentionally ignored by Git.

## Verification

The current implementation has been verified with:

```sh
go test ./...
cd frontend && npm run build
wails build
```
