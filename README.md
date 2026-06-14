<p align="center">
  <img src="build/appicon.png" alt="Journal application icon" width="128">
</p>

<h1 align="center">Journal</h1>

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

Generate a stress-test SQLite database:

```sh
go run ./cmd/stressdb -out /tmp/journal-stress.db -journals 5 -min-folders 50 -max-folders 100 -nested-percent 40 -min-documents 500 -max-documents 1000 -min-words 200 -max-words 1000
```

The generator writes the same `items`, `documents`, `library_search_fts`, and `app_settings` schema used by Journal. Use `-overwrite` to replace an existing output file. Useful profiles:

```sh
# Many large documents
go run ./cmd/stressdb -out /tmp/journal-large-docs.db -journals 2 -min-documents 100 -max-documents 100 -min-words 10000 -max-words 25000 -overwrite

# Folder-heavy nested library
go run ./cmd/stressdb -out /tmp/journal-folders.db -journals 1 -min-folders 10000 -max-folders 10000 -nested-percent 80 -min-documents 10 -max-documents 10 -overwrite
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
build/bin/Journal.app
```

Build a Windows 64-bit executable:

```sh
wails build -platform windows/amd64
```

The Windows executable is written to:

```text
build/bin/Journal.exe
```

To build a Windows installer, add the NSIS flag:

```sh
wails build -platform windows/amd64 -nsis
```

## Standalone macOS Build for Apple Silicon

Build a standalone macOS app bundle for Apple Silicon Macs from the repository root:

```sh
wails build -clean -platform darwin/arm64
```

This builds the React frontend, embeds it in the Go/Wails application, and writes the macOS app bundle to:

```text
build/bin/Journal.app
```

Run it from Finder, or from Terminal:

```sh
open build/bin/Journal.app
```

The actual executable inside the bundle is:

```text
build/bin/Journal.app/Contents/MacOS/Journal
```

To install the app locally, copy `build/bin/Journal.app` into `/Applications`.

## Data Storage

By default, Journal stores its SQLite database under the operating system user config directory:

```text
<user-config-dir>/Journal/journal.db
```

On macOS, this is typically:

```text
/Users/<your-user>/Library/Application Support/Journal/journal.db
```

On Windows, this is typically:

```text
C:\Users\<your-user>\AppData\Roaming\Journal\journal.db
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
