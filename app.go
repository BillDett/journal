package main

import "time"

const (
	KindJournal  = "journal"
	KindFolder   = "folder"
	KindDocument = "document"
	SystemTrash  = "trash"

	defaultAutosaveIntervalMS = 2000
	settingLastDocumentID     = "last_document_id"
	settingLibraryWidth       = "library_width"
	defaultLibraryWidth       = 340
	minLibraryWidth           = 260
	maxLibraryWidth           = 620
	defaultSpacingPreset      = "compact"
	maxImageAttachmentBytes   = 20 * 1024 * 1024
	detachedAttachmentGrace   = 24 * time.Hour

	defaultAppName    = "Journal"
	defaultAppVersion = "0.0.0-dev"
	appDisclaimer     = "Journal is free and open source software."
)

var appVersion = ""

// Service implementation is partitioned by responsibility across this package.
