import type {JSONContent} from '@tiptap/core'

export type ProseMirrorDoc = JSONContent & {
  type: 'doc'
}

export type SpacingPreset = 'compact' | 'normal' | 'relaxed'

export type TreeItem = {
  id: string
  parentId: string
  kind: 'journal' | 'folder' | 'document'
  title: string
  sortOrder: number
  systemKey: string
  createdAt: string
  updatedAt: string
  encryptionState: 'plaintext' | 'encrypted'
  encryptionKeyId: string
  encryptionLocked: boolean
  documentCount: number
  itemCount: number
  children: TreeItem[]
}

export type TreeResponse = {
  items: TreeItem[]
  trashId: string
}

export type ItemResponse = {
  item: TreeItem
  tree: TreeResponse
}

export type JournalDetailsResponse = {
  id: string
  title: string
  encryptionState: 'plaintext' | 'encrypted'
  encryptionLocked: boolean
  createdAt: string
  documentCount: number
  folderCount: number
  imageCount: number
}

export type DocumentResponse = {
  id: string
  title: string
  content: ProseMirrorDoc
  spacingPreset: SpacingPreset
  schemaVersion: number
  createdAt: string
  updatedAt: string
  item: TreeItem
  tree: TreeResponse
  saveState: string
}

export type DocumentAttachmentResponse = {
  id: string
  documentId: string
  mimeType: string
  originalName: string
  sizeBytes: number
}

export type DocumentAttachmentDataResponse = {
  id: string
  dataUrl: string
}

export type SearchResponse = {
  query: string
  items: TreeItem[]
  resultIds: string[]
  trashId: string
}

export type AppSettingsResponse = {
  autosaveIntervalMs: number
  lastDocumentId: string
  libraryWidth: number
}

export type AppSettingsPatch = {
  autosaveIntervalMs: number
  libraryWidth: number
}

export type JournalDatabaseLocationResponse = {
  path: string
  canReveal: boolean
}

export type TrashItemCommand = {
  id: string
  expectedInTrash: boolean
}

export type AppInfo = {
  name: string
  version: string
  disclaimer: string
}

export type EncryptionStatusResponse = {
  masterPasswordConfigured: boolean
  unlocked: boolean
  encryptedJournalIds: string[]
}

type BackendAPI = {
  GetAppInfo: () => Promise<AppInfo>
  GetLibraryTree: () => Promise<TreeResponse>
  GetJournalDetails: (journalId: string) => Promise<JournalDetailsResponse>
  CreateDocument: (parentId: string) => Promise<DocumentResponse>
  DuplicateDocument: (id: string) => Promise<DocumentResponse>
  CreateFolder: (parentId: string, title: string) => Promise<ItemResponse>
  CreateJournal: (title: string) => Promise<ItemResponse>
  RenameItem: (id: string, title: string) => Promise<ItemResponse>
  MoveItem: (id: string, newParentId: string, newSortOrder: number) => Promise<TreeResponse>
  TrashItem: (command: TrashItemCommand) => Promise<TreeResponse>
  DeleteJournal: (id: string) => Promise<TreeResponse>
  EmptyTrash: () => Promise<TreeResponse>
  OpenDocument: (id: string) => Promise<DocumentResponse>
  ExportDocumentMarkdown: (documentId: string) => Promise<void>
  UpdateDocumentDraft: (id: string, content: ProseMirrorDoc, version: number) => Promise<{id: string, saveState: string, version: number}>
  CreateDocumentAttachment: (documentId: string, name: string, mimeType: string, dataBase64: string) => Promise<DocumentAttachmentResponse>
  PickDocumentImage: (documentId: string) => Promise<DocumentAttachmentResponse>
  GetDocumentAttachmentDataURL: (attachmentId: string) => Promise<DocumentAttachmentDataResponse>
  UpdateDocumentSpacing: (id: string, spacingPreset: SpacingPreset) => Promise<{id: string, saveState: string, savedAt: string, updatedAt: string, version: number}>
  FlushDocument: (id: string) => Promise<{id: string, saveState: string, savedAt: string, updatedAt: string, version: number}>
  SearchLibrary: (query: string) => Promise<SearchResponse>
  GetEncryptionStatus: () => Promise<EncryptionStatusResponse>
  CreateMasterPassword: (password: string) => Promise<void>
  UnlockEncryption: (password: string) => Promise<EncryptionStatusResponse>
  ChangeMasterPassword: (currentPassword: string, newPassword: string) => Promise<EncryptionStatusResponse>
  EncryptJournal: (journalId: string) => Promise<TreeResponse>
  DecryptJournal: (journalId: string) => Promise<TreeResponse>
  LockEncryptedJournals: () => Promise<EncryptionStatusResponse>
  ExportJournalDirectory: (journalId: string) => Promise<void>
  ImportMarkdownDirectory: () => Promise<ItemResponse>
  SetSelectedJournalForMenu: (journalId: string) => Promise<void>
  CompleteCloseAfterFlush: () => Promise<void>
  CancelCloseAfterFlushFailure: () => Promise<void>
  GetJournalDatabaseLocation: () => Promise<JournalDatabaseLocationResponse>
  RevealJournalDatabaseFile: () => Promise<void>
  GetAppSettings: () => Promise<AppSettingsResponse>
  UpdateAppSettings: (settings: AppSettingsPatch) => Promise<AppSettingsResponse>
}

type WailsWindow = Window & {
  go?: {
    main?: {
      App?: BackendAPI
    }
  }
}

const backend = (window as WailsWindow).go?.main?.App

export const api: BackendAPI = backend ?? missingBackend()

function missingBackend(): BackendAPI {
  const fail = async () => {
    throw new Error('Journal backend is unavailable. Run the app with Wails to use the local database.')
  }
  return {
    GetAppInfo: async () => ({
      name: 'Journal',
      version: '0.0.0-dev',
      disclaimer: 'Journal is free and open source software.',
    }),
    GetLibraryTree: fail,
    GetJournalDetails: fail,
    CreateDocument: fail,
    DuplicateDocument: fail,
    CreateFolder: fail,
    CreateJournal: fail,
    RenameItem: fail,
    MoveItem: fail,
    TrashItem: fail,
    DeleteJournal: fail,
    EmptyTrash: fail,
    OpenDocument: fail,
    ExportDocumentMarkdown: fail,
    UpdateDocumentDraft: fail,
    CreateDocumentAttachment: fail,
    PickDocumentImage: fail,
    GetDocumentAttachmentDataURL: fail,
    UpdateDocumentSpacing: fail,
    FlushDocument: fail,
    SearchLibrary: fail,
    GetEncryptionStatus: fail,
    CreateMasterPassword: fail,
    UnlockEncryption: fail,
    ChangeMasterPassword: fail,
    EncryptJournal: fail,
    DecryptJournal: fail,
    LockEncryptedJournals: fail,
    ExportJournalDirectory: fail,
    ImportMarkdownDirectory: fail,
    SetSelectedJournalForMenu: fail,
    CompleteCloseAfterFlush: fail,
    CancelCloseAfterFlushFailure: fail,
    GetJournalDatabaseLocation: fail,
    RevealJournalDatabaseFile: fail,
    GetAppSettings: fail,
    UpdateAppSettings: fail,
  }
}

export function messageFromError(error: unknown) {
  if (error instanceof Error) return error.message
  if (typeof error === 'string') return error
  return 'Unexpected error'
}
