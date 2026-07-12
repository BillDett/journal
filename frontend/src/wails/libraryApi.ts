import type {JSONContent} from '@tiptap/core'

export type ProseMirrorDoc = JSONContent & {
  type: 'doc'
}

export type SpacingPreset = 'compact' | 'normal' | 'relaxed'

export type TreeItem = {
  id: string
  storeId: string
  storageKind: 'local' | 'cloud'
  cloudStatus: string
  readOnly: boolean
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

export type VaultProviderRequest = { id: string, name: string, kind: string, endpoint: string, root: string, credentialRef: string, publishDebounceMs: number, publishMaxIntervalMs: number, revisionRetentionCount: number }
export type VaultProviderResponse = Omit<VaultProviderRequest, 'credentialRef'> & { validated: boolean }
export type CloudMountResponse = { cloudJournalId: string, providerId: string, cachePath: string, lastRevisionId: string, syncStatus: string, lastSyncError: string, lastSyncedAt: string, readOnly: boolean, statusReason: string }
export type CloudJournalResponse = { cloudJournalId: string, mount: CloudMountResponse, tree: TreeResponse }

export type TreeResponse = {
  items: TreeItem[]
  trashId: string
}

export type ItemResponse = {
  item: TreeItem
  tree: TreeResponse
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
  CreateDocument: (parentId: string) => Promise<DocumentResponse>
  DuplicateDocument: (id: string) => Promise<DocumentResponse>
  CreateFolder: (parentId: string, title: string) => Promise<ItemResponse>
  CreateJournal: (title: string) => Promise<ItemResponse>
  RenameItem: (id: string, title: string) => Promise<ItemResponse>
  MoveItem: (id: string, newParentId: string, newSortOrder: number) => Promise<TreeResponse>
  TrashItem: (command: TrashItemCommand) => Promise<TreeResponse>
  DeleteJournal: (id: string) => Promise<TreeResponse>
  OpenDocument: (id: string) => Promise<DocumentResponse>
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
  ExportJournalDirectory: (journalId: string) => Promise<void>
  ImportMarkdownDirectory: () => Promise<ItemResponse>
  SetSelectedJournalForMenu: (journalId: string) => Promise<void>
  GetAppSettings: () => Promise<AppSettingsResponse>
  UpdateAppSettings: (settings: AppSettingsPatch) => Promise<AppSettingsResponse>
  ListVaultProviders: () => Promise<VaultProviderResponse[]>
  SaveVaultProvider: (provider: VaultProviderRequest) => Promise<VaultProviderResponse>
  ValidateVaultProvider: (providerId: string) => Promise<VaultProviderResponse>
  RemoveVaultProvider: (providerId: string) => Promise<void>
  ListCloudMounts: () => Promise<CloudMountResponse[]>
  CreateCloudJournal: (providerId: string) => Promise<CloudJournalResponse>
  CreateLocalCloudJournal: () => Promise<CloudJournalResponse>
  ReconnectCloudJournal: (providerId: string, cloudJournalId: string) => Promise<CloudJournalResponse>
  SyncCloudJournal: (cloudJournalId: string) => Promise<CloudMountResponse>
  ReleaseCloudLease: (cloudJournalId: string) => Promise<void>
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
    CreateDocument: fail,
    DuplicateDocument: fail,
    CreateFolder: fail,
    CreateJournal: fail,
    RenameItem: fail,
    MoveItem: fail,
    TrashItem: fail,
    DeleteJournal: fail,
    OpenDocument: fail,
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
    ExportJournalDirectory: fail,
    ImportMarkdownDirectory: fail,
    SetSelectedJournalForMenu: fail,
    GetAppSettings: fail,
    UpdateAppSettings: fail,
    ListVaultProviders: fail,
    SaveVaultProvider: fail,
    ValidateVaultProvider: fail,
    RemoveVaultProvider: fail,
    ListCloudMounts: fail,
    CreateCloudJournal: fail,
    CreateLocalCloudJournal: fail,
    ReconnectCloudJournal: fail,
    SyncCloudJournal: fail,
    ReleaseCloudLease: fail,
  }
}

export function messageFromError(error: unknown) {
  if (error instanceof Error) return error.message
  if (typeof error === 'string') return error
  return 'Unexpected error'
}
