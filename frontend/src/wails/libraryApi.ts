import type {JSONContent} from '@tiptap/core'

export type ProseMirrorDoc = JSONContent & {
  type: 'doc'
}

export type TreeItem = {
  id: string
  parentId: string
  kind: 'journal' | 'folder' | 'document'
  title: string
  sortOrder: number
  systemKey: string
  createdAt: string
  updatedAt: string
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

export type DocumentResponse = {
  id: string
  title: string
  content: ProseMirrorDoc
  schemaVersion: number
  createdAt: string
  updatedAt: string
  item: TreeItem
  tree: TreeResponse
  saveState: string
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
}

export type AppSettingsPatch = {
  autosaveIntervalMs: number
}

type BackendAPI = {
  GetLibraryTree: () => Promise<TreeResponse>
  CreateDocument: (parentId: string) => Promise<DocumentResponse>
  CreateFolder: (parentId: string, title: string) => Promise<ItemResponse>
  CreateJournal: (title: string) => Promise<ItemResponse>
  RenameItem: (id: string, title: string) => Promise<ItemResponse>
  MoveItem: (id: string, newParentId: string, newSortOrder: number) => Promise<TreeResponse>
  MoveItemToTrash: (id: string) => Promise<TreeResponse>
  PermanentlyDeleteItem: (id: string) => Promise<TreeResponse>
  DeleteJournal: (id: string) => Promise<TreeResponse>
  OpenDocument: (id: string) => Promise<DocumentResponse>
  UpdateDocumentDraft: (id: string, content: ProseMirrorDoc, version: number) => Promise<{id: string, saveState: string, version: number}>
  FlushDocument: (id: string) => Promise<{id: string, saveState: string, savedAt: string, updatedAt: string, version: number}>
  SearchLibrary: (query: string) => Promise<SearchResponse>
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
    GetLibraryTree: fail,
    CreateDocument: fail,
    CreateFolder: fail,
    CreateJournal: fail,
    RenameItem: fail,
    MoveItem: fail,
    MoveItemToTrash: fail,
    PermanentlyDeleteItem: fail,
    DeleteJournal: fail,
    OpenDocument: fail,
    UpdateDocumentDraft: fail,
    FlushDocument: fail,
    SearchLibrary: fail,
    GetAppSettings: fail,
    UpdateAppSettings: fail,
  }
}

export function messageFromError(error: unknown) {
  if (error instanceof Error) return error.message
  if (typeof error === 'string') return error
  return 'Unexpected error'
}
