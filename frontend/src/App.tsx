import {useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type PointerEvent as ReactPointerEvent, type ReactNode} from 'react'
import {EditorContent, useEditor} from '@tiptap/react'
import type {Editor} from '@tiptap/react'
import {
  AlignCenter,
  AlignLeft,
  AlignRight,
  Bold,
  BookMarked,
  BookOpenText,
  BookPlus,
  CheckSquare,
  ChevronDown,
  ChevronRight,
  Download,
  FilePlus,
  FileText,
  Files,
  Folder,
  FolderPlus,
  Highlighter,
  Image as ImageIcon,
  Italic,
  Link2,
  List,
  ListOrdered,
  Lock,
  Minus,
  Plus,
  Redo2,
  Search,
  Settings,
  Shield,
  Strikethrough,
  Table2,
  TableProperties,
  Trash2,
  Underline as UnderlineIcon,
  Undo2,
  Unlock,
  X,
} from 'lucide-react'
import {editorExtensions} from './editor/extensions'
import {OperationCoordinator} from './operations'
import {
  api,
  messageFromError,
  type AppInfo,
  type DocumentResponse,
  type EncryptionStatusResponse,
  type JournalDetailsResponse,
  type JournalDatabaseLocationResponse,
  type ProseMirrorDoc,
  type SpacingPreset,
  type TreeItem,
} from './wails/libraryApi'
import appIcon from './assets/appicon.png'
import {BrowserOpenURL, EventsOn} from '../wailsjs/runtime/runtime'

type SaveState = 'idle' | 'dirty' | 'saving' | 'saved' | 'error'
type HeadingLevel = 1 | 2 | 3 | 4 | 5 | 6
type ParagraphStyle = 'normal' | `heading-${HeadingLevel}`
type HorizontalAlignment = 'left' | 'center' | 'right'

const libraryWidthMin = 260
const libraryWidthDefault = 340
const libraryWidthMax = 620
const maxInlineImageBytes = 20 * 1024 * 1024
const supportedImageTypes = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp'])

type DeleteTarget = {
  item: TreeItem
  inTrash: boolean
} | null

type DuplicateTarget = TreeItem | null

type DecryptTarget = TreeItem | null

type LinkPopoverState = {
  x: number
  y: number
  from: number
  to: number
}

type EncryptionDialogState =
  | {mode: 'create', journalId: string}
  | {mode: 'setup'}
  | {mode: 'unlock', journalId?: string, action?: 'encrypt' | 'decrypt' | 'open'}
  | {mode: 'change'}
  | null

function App() {
  const [tree, setTree] = useState<TreeItem[]>([])
  const [trashId, setTrashId] = useState('')
  const [activeDoc, setActiveDoc] = useState<DocumentResponse | null>(null)
  const [closeRequest, setCloseRequest] = useState(0)
  const [journalDetails, setJournalDetails] = useState<JournalDetailsResponse | null>(null)
  const [selectedItemId, setSelectedItemId] = useState('')
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [renamingId, setRenamingId] = useState('')
  const [searchQuery, setSearchQuery] = useState('')
  const [searchResults, setSearchResults] = useState<Set<string>>(new Set())
  const [saveState, setSaveState] = useState<SaveState>('idle')
  const [status, setStatus] = useState('Ready')
  const [lastError, setLastError] = useState('')
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [aboutOpen, setAboutOpen] = useState(false)
  const [titleFocusDocumentId, setTitleFocusDocumentId] = useState('')
  const [appInfo, setAppInfo] = useState<AppInfo>({
    name: 'Journal',
    version: '0.0.0-dev',
    disclaimer: 'Journal is free and open source software.',
  })
  const [autosaveInterval, setAutosaveInterval] = useState(2000)
  const [databaseLocation, setDatabaseLocation] = useState<JournalDatabaseLocationResponse>({path: '', canReveal: false})
  const [draggedId, setDraggedId] = useState('')
  const [deleteTarget, setDeleteTarget] = useState<DeleteTarget>(null)
  const [duplicateTarget, setDuplicateTarget] = useState<DuplicateTarget>(null)
  const [decryptTarget, setDecryptTarget] = useState<DecryptTarget>(null)
  const [encryptedNotice, setEncryptedNotice] = useState('')
  const [encryptionDialog, setEncryptionDialog] = useState<EncryptionDialogState>(null)
  const [encryptionStatus, setEncryptionStatus] = useState<EncryptionStatusResponse>({
    masterPasswordConfigured: false,
    unlocked: false,
    encryptedJournalIds: [],
  })
  const [libraryWidth, setLibraryWidth] = useState(libraryWidthDefault)
  const autosaveTimer = useRef<number | undefined>(undefined)
  const operationCoordinator = useRef(new OperationCoordinator())
  const selectedJournalIdRef = useRef('')
  const latestDraft = useRef<{id: string, content: ProseMirrorDoc, version: number} | null>(null)
  const sentDraftVersion = useRef(0)
  const draftVersion = useRef(0)
  const activeDocId = useRef('')

  const flattened = useMemo(() => flattenTree(tree), [tree])
  const journalCount = useMemo(() => tree.filter((item) => item.kind === 'journal').length, [tree])
  const defaultJournalId = tree.find((item) => item.kind === 'journal')?.id ?? ''
  const draggedItem = draggedId ? flattened.find((item) => item.id === draggedId) : undefined
  const selectedItem = selectedItemId ? flattened.find((item) => item.id === selectedItemId) ?? null : null
  const selectedJournal = selectedItem?.kind === 'journal' ? selectedItem : null
  const creationParentId = useMemo(
    () => creationParentFor(flattened, selectedItemId, defaultJournalId, trashId),
    [defaultJournalId, flattened, selectedItemId, trashId],
  )

  const applyTree = useCallback((items: TreeItem[], nextTrashId: string) => {
    setTree(orderTree(items, nextTrashId))
    setTrashId(nextTrashId)
  }, [])

  const loadTree = useCallback(async () => {
    const requestVersion = operationCoordinator.current.nextTreeRequest()
    const response = await api.GetLibraryTree()
    if (operationCoordinator.current.isCurrentTreeRequest(requestVersion)) applyTree(response.items, response.trashId)
  }, [applyTree])

  function beginOperation(key: string) {
    return operationCoordinator.current.begin(key)
  }

  function endOperation(key: string) {
    operationCoordinator.current.end(key)
  }

  useEffect(() => {
    activeDocId.current = activeDoc?.id ?? ''
    window.clearTimeout(autosaveTimer.current)
  }, [activeDoc?.id])

  useEffect(() => () => window.clearTimeout(autosaveTimer.current), [])

  useEffect(() => {
    if (!shouldAutoDismissError(lastError)) return undefined
    const handle = window.setTimeout(() => {
      setLastError((current) => current === lastError ? '' : current)
    }, 5000)
    return () => window.clearTimeout(handle)
  }, [lastError])

  useEffect(() => {
    if (!('runtime' in window)) return undefined
    return EventsOn('journal:show-about', () => setAboutOpen(true))
  }, [])

  useEffect(() => {
    selectedJournalIdRef.current = selectedJournal?.id ?? ''
    void api.SetSelectedJournalForMenu(selectedJournal?.id ?? '')
  }, [selectedJournal?.id, encryptionStatus.unlocked, encryptionStatus.encryptedJournalIds.length])

  useEffect(() => {
    if (!('runtime' in window)) return undefined
    return EventsOn('journal:before-close', () => {
      if (activeDoc && !settingsOpen && !journalDetails) setCloseRequest((current) => current + 1)
      else void api.CompleteCloseAfterFlush()
    })
  }, [activeDoc, journalDetails, settingsOpen])

  useEffect(() => {
    let live = true
    const requestVersion = operationCoordinator.current.nextTreeRequest()
    async function boot() {
      try {
        const [treeResponse, settings, info, encryption, location] = await Promise.all([api.GetLibraryTree(), api.GetAppSettings(), api.GetAppInfo(), api.GetEncryptionStatus(), api.GetJournalDatabaseLocation()])
        if (!live) return
        if (operationCoordinator.current.isCurrentTreeRequest(requestVersion)) applyTree(treeResponse.items, treeResponse.trashId)
        setAutosaveInterval(settings.autosaveIntervalMs)
        setLibraryWidth(clampNumber(settings.libraryWidth || libraryWidthDefault, libraryWidthMin, libraryWidthMax))
        setAppInfo(info)
        setEncryptionStatus(encryption)
        setDatabaseLocation(location)
        if (settings.lastDocumentId) {
          try {
            const response = await api.OpenDocument(settings.lastDocumentId)
            if (!live) return
            showDocument(response, 'Opened')
          } catch {
            if (live) setStatus('Ready')
          }
        }
      } catch (error) {
        if (!live) return
        setLastError(messageFromError(error))
        setSaveState('error')
      }
    }
    void boot()
    return () => {
      live = false
    }
  }, [applyTree])

  useEffect(() => {
    const requestVersion = operationCoordinator.current.nextTreeRequest()
    if (!searchQuery.trim()) {
      setSearchResults(new Set())
      void api.GetLibraryTree()
        .then((response) => {
          if (operationCoordinator.current.isCurrentTreeRequest(requestVersion)) applyTree(response.items, response.trashId)
        })
        .catch((error) => {
          if (operationCoordinator.current.isCurrentTreeRequest(requestVersion)) setLastError(messageFromError(error))
        })
      return
    }
    const handle = window.setTimeout(async () => {
      try {
        const response = await api.SearchLibrary(searchQuery)
        if (!operationCoordinator.current.isCurrentTreeRequest(requestVersion)) return
        applyTree(response.items, response.trashId)
        setSearchResults(new Set(response.resultIds))
        setExpanded(expandAllContainers(response.items))
      } catch (error) {
        setLastError(messageFromError(error))
      }
    }, 180)
    return () => window.clearTimeout(handle)
  }, [applyTree, loadTree, searchQuery])

  async function refreshVisibleTree(fallbackItems?: TreeItem[], fallbackTrashId?: string) {
    const requestVersion = operationCoordinator.current.nextTreeRequest()
    const query = searchQuery.trim()
    if (!query) {
      if (fallbackItems && fallbackTrashId) {
        if (operationCoordinator.current.isCurrentTreeRequest(requestVersion)) applyTree(fallbackItems, fallbackTrashId)
      } else {
        const response = await api.GetLibraryTree()
        if (operationCoordinator.current.isCurrentTreeRequest(requestVersion)) applyTree(response.items, response.trashId)
      }
      return
    }

    const response = await api.SearchLibrary(query)
    if (!operationCoordinator.current.isCurrentTreeRequest(requestVersion)) return
    applyTree(response.items, response.trashId)
    setSearchResults(new Set(response.resultIds))
    setExpanded(expandAllContainers(response.items))
  }

  function scheduleAutosaveFlush(id: string, version: number) {
    window.clearTimeout(autosaveTimer.current)
    autosaveTimer.current = window.setTimeout(() => {
      void flushDraft(id, version)
    }, autosaveInterval)
  }

  async function flushDraft(id: string, version: number) {
    if (activeDocId.current === id && draftVersion.current === version) {
      setSaveState('saving')
      setStatus('Saving')
    }
    try {
      const response = await api.FlushDocument(id)
      sentDraftVersion.current = Math.max(sentDraftVersion.current, response.version)
      if (latestDraft.current?.id === id && latestDraft.current.version <= response.version) {
        latestDraft.current = null
      }
      if (activeDocId.current === id && draftVersion.current <= response.version) {
        setActiveDoc((current) => current && current.id === response.id ? {...current, updatedAt: response.updatedAt} : current)
        setSaveState('saved')
        setStatus('Saved')
        void refreshVisibleTree()
      }
    } catch (error) {
      if (activeDocId.current === id && draftVersion.current === version) {
        setSaveState('error')
        setLastError(messageFromError(error))
      }
    }
  }

  async function flushActive() {
    if (!activeDoc) return true
    const draft = latestDraft.current?.id === activeDoc.id ? latestDraft.current : null
    if (saveState !== 'dirty' && !draft) return true
    window.clearTimeout(autosaveTimer.current)
    setSaveState('saving')
    try {
      if (draft && draft.version > sentDraftVersion.current) {
        const draftResponse = await api.UpdateDocumentDraft(draft.id, draft.content, draft.version)
        sentDraftVersion.current = Math.max(sentDraftVersion.current, draftResponse.version)
      }
      const response = await api.FlushDocument(activeDoc.id)
      sentDraftVersion.current = Math.max(sentDraftVersion.current, response.version)
      if (latestDraft.current?.id === activeDoc.id && latestDraft.current.version <= response.version) {
        latestDraft.current = null
      }
      setActiveDoc((current) => current && current.id === response.id ? {...current, updatedAt: response.updatedAt} : current)
      if (!latestDraft.current || latestDraft.current.id !== activeDoc.id) {
        setSaveState('saved')
        setStatus('Saved')
      } else {
        setSaveState('dirty')
        setStatus('Autosave pending')
      }
      void refreshVisibleTree()
      return true
    } catch (error) {
      setSaveState('error')
      setLastError(messageFromError(error))
      return false
    }
  }

  async function updateActiveDraft(content: ProseMirrorDoc) {
    if (!activeDoc) return
    const id = activeDoc.id
    const version = draftVersion.current + 1
    draftVersion.current = version
    latestDraft.current = {id, content, version}
    window.clearTimeout(autosaveTimer.current)
    setSaveState('dirty')
    setStatus('Autosave pending')
    try {
      const response = await api.UpdateDocumentDraft(id, content, version)
      sentDraftVersion.current = Math.max(sentDraftVersion.current, response.version)
      if (activeDocId.current === id && draftVersion.current === version) {
        setStatus('Autosave pending')
        scheduleAutosaveFlush(id, version)
      }
    } catch (error) {
      if (activeDocId.current === id && draftVersion.current === version) {
        setSaveState('error')
        setLastError(messageFromError(error))
      }
    }
  }

  async function updateActiveSpacing(spacingPreset: SpacingPreset) {
    if (!activeDoc || activeDoc.spacingPreset === spacingPreset) return
    const id = activeDoc.id
    setActiveDoc((current) => current && current.id === id ? {...current, spacingPreset} : current)
    setStatus('Spacing updated')
    try {
      const response = await api.UpdateDocumentSpacing(id, spacingPreset)
      setActiveDoc((current) => current && current.id === id ? {...current, updatedAt: response.updatedAt} : current)
      void refreshVisibleTree()
    } catch (error) {
      if (activeDocId.current === id) {
        setActiveDoc((current) => current && current.id === id ? {...current, spacingPreset: activeDoc.spacingPreset} : current)
        setSaveState('error')
        setLastError(messageFromError(error))
      }
    }
  }

  async function openDocument(id: string) {
    if (activeDoc?.id === id) {
      setJournalDetails(null)
      return
    }
    const requestVersion = operationCoordinator.current.nextDocumentRequest()
    if (!(await flushActive())) return
    if (!operationCoordinator.current.isCurrentDocumentRequest(requestVersion)) return
    try {
      setTitleFocusDocumentId('')
      const response = await api.OpenDocument(id)
      if (!operationCoordinator.current.isCurrentDocumentRequest(requestVersion)) return
      showDocument(response, 'Opened')
    } catch (error) {
      setLastError(messageFromError(error))
      setSaveState('error')
    }
  }

  function showDocument(response: DocumentResponse, nextStatus: string) {
    const hasSearch = Boolean(searchQuery.trim())
    latestDraft.current = null
    setActiveDoc(response)
    setJournalDetails(null)
    setSelectedItemId(response.id)
    if (!hasSearch) {
      operationCoordinator.current.invalidateTreeRequests()
      applyTree(response.tree.items, response.tree.trashId)
    }
    setSaveState('saved')
    setStatus(nextStatus)
    revealItem(response.item, hasSearch ? tree : response.tree.items)
  }

  function revealItem(item: TreeItem, items: TreeItem[]) {
    const ancestors = ancestorIDs(items, item.id)
    setExpanded((current) => new Set([...current, ...ancestors]))
  }

  async function createDocument(parentId = '') {
    if (!(await flushActive())) return
    try {
      const response = await api.CreateDocument(parentId)
      setTitleFocusDocumentId(response.id)
      showDocument(response, 'Created document')
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  function requestDuplicateDocument(id: string) {
    const item = flattened.find((entry) => entry.id === id)
    if (!item || item.kind !== 'document' || isDescendantOf(flattened, id, trashId)) return
    setSelectedItemId(id)
    setDuplicateTarget(item)
  }

  async function confirmDuplicateDocument() {
    if (!duplicateTarget) return
    const id = duplicateTarget.id
    setDuplicateTarget(null)
    if (activeDoc?.id === id && !(await flushActive())) return
    try {
      const response = await api.DuplicateDocument(id)
      showDocument(response, 'Duplicated document')
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  async function createFolder(parentId = '') {
    try {
      const response = await api.CreateFolder(parentId || defaultJournalId, 'New Folder')
      await refreshVisibleTree(response.tree.items, response.tree.trashId)
      setSelectedItemId(response.item.id)
      setRenamingId(response.item.id)
      revealItem(response.item, response.tree.items)
      setStatus('Created folder')
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  async function createJournal() {
    try {
      const response = await api.CreateJournal('New Journal')
      await refreshVisibleTree(response.tree.items, response.tree.trashId)
      setSelectedItemId(response.item.id)
      setRenamingId(response.item.id)
      setExpanded((current) => new Set([...current, response.item.id]))
      setStatus('Created journal')
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  async function exportJournalById(journalId: string) {
    if (!journalId) return
    try {
      await api.ExportJournalDirectory(journalId)
      setStatus('Exported journal')
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  async function exportDocumentMarkdown(documentId: string) {
    if (activeDoc?.id === documentId && !(await flushActive())) return
    try {
      await api.ExportDocumentMarkdown(documentId)
      setStatus('Exported document')
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  async function importJournal() {
    if (!(await flushActive())) return
    try {
      const response = await api.ImportMarkdownDirectory()
      if (!response.item?.id) return
      await refreshVisibleTree(response.tree.items, response.tree.trashId)
      setSelectedItemId(response.item.id)
      setExpanded((current) => new Set([...current, response.item.id]))
      setStatus('Imported journal')
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  async function showJournalDetails(journalId: string) {
    try {
      const details = await api.GetJournalDetails(journalId)
      setJournalDetails(details)
      setSelectedItemId(journalId)
      setStatus('Journal details')
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  async function lockEncryptedJournals() {
    if (!encryptionStatus.unlocked || encryptionStatus.encryptedJournalIds.length === 0) return
    if (!(await flushActive())) return
    try {
      const status = await api.LockEncryptedJournals()
      setEncryptionStatus(status)
      await loadTree()
      setExpanded((current) => new Set([...current].filter((id) => !encryptionStatus.encryptedJournalIds.includes(id))))
      if (activeDoc?.item.encryptionState === 'encrypted') clearActiveDocument()
      setStatus('Locked encrypted journals')
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  useEffect(() => {
    if (!('runtime' in window)) return undefined
    const offExport = EventsOn('journal:menu-export-journal', (journalId?: string) => {
      void exportJournalById(typeof journalId === 'string' && journalId ? journalId : selectedJournalIdRef.current)
    })
    const offImport = EventsOn('journal:menu-import-journal', () => {
      void importJournal()
    })
    const offEncrypt = EventsOn('journal:menu-encrypt-journal', (journalId?: string) => void encryptJournal(typeof journalId === 'string' ? journalId : selectedJournalIdRef.current))
    const offDecrypt = EventsOn('journal:menu-decrypt-journal', (journalId?: string) => void decryptJournal(typeof journalId === 'string' ? journalId : selectedJournalIdRef.current))
    const offDetails = EventsOn('journal:menu-journal-details', (journalId?: string) => void showJournalDetails(typeof journalId === 'string' ? journalId : selectedJournalIdRef.current))
    const offDelete = EventsOn('journal:menu-delete-journal', (journalId?: string) => requestDelete(typeof journalId === 'string' ? journalId : selectedJournalIdRef.current))
    const offLock = EventsOn('journal:menu-lock-journals', () => void lockEncryptedJournals())
    return () => {
      offExport?.()
      offImport?.()
      offEncrypt?.()
      offDecrypt?.()
      offDetails?.()
      offDelete?.()
      offLock?.()
    }
  }, [flushActive, encryptionStatus, activeDoc, flattened])

  async function renameItem(id: string, title: string) {
    try {
      const response = await api.RenameItem(id, title)
      await refreshVisibleTree(response.tree.items, response.tree.trashId)
      setRenamingId('')
      if (activeDoc?.id === id) {
        setActiveDoc((current) => current ? {...current, title: response.item.title, updatedAt: response.item.updatedAt, item: response.item} : current)
      }
      setStatus('Renamed')
      return true
    } catch (error) {
      setLastError(messageFromError(error))
      return false
    }
  }

  function requestDelete(id: string) {
    const item = flattened.find((entry) => entry.id === id)
    if (!item || item.systemKey === 'trash') return
    if (item.kind === 'journal' && journalCount <= 1) return
    const inTrash = isDescendantOf(flattened, id, trashId)
    setSelectedItemId(id)
    setDeleteTarget({item, inTrash})
  }

  async function confirmDelete() {
    if (!deleteTarget) return
    const {item, inTrash} = deleteTarget
    const id = item.id
    const operationKey = `delete:${id}`
    if (!beginOperation(operationKey)) return
    setDeleteTarget(null)
    try {
      if (activeDoc && (activeDoc.id === id || isDescendantOf(flattened, activeDoc.id, id))) {
        if (!(await flushActive())) return
      }
      const response = item.kind === 'journal'
        ? await api.DeleteJournal(id)
        : await api.TrashItem({id, expectedInTrash: inTrash})
      await refreshVisibleTree(response.items, response.trashId)
      if (activeDoc && (activeDoc.id === id || isDescendantOf(flattened, activeDoc.id, id))) {
        setActiveDoc(null)
        setSaveState('idle')
      }
      if (journalDetails?.id === id) setJournalDetails(null)
      setSelectedItemId('')
      setStatus(item.kind === 'journal' || inTrash ? 'Deleted permanently' : 'Moved to Trash')
    } catch (error) {
      setLastError(messageFromError(error))
    } finally {
      endOperation(operationKey)
    }
  }

  async function moveItem(id: string, parentId: string, sortOrder = -1) {
    if (id === parentId) return
    const operationKey = `move:${id}`
    if (!beginOperation(operationKey)) return
    const item = flattened.find((entry) => entry.id === id)
    const sourceJournalId = journalIdFor(flattened, id)
    const targetJournalId = parentId ? journalIdFor(flattened, parentId) : ''
    const sourceInTrash = isDescendantOf(flattened, id, trashId)
    try {
      if (activeDoc && (activeDoc.id === id || isDescendantOf(flattened, activeDoc.id, id))) {
        if (!(await flushActive())) return
      }
      const response = await api.MoveItem(id, parentId, sortOrder)
      await refreshVisibleTree(response.items, response.trashId)
      const copied = !sourceInTrash && sourceJournalId && targetJournalId && sourceJournalId !== targetJournalId
      setStatus(parentId === trashId ? 'Moved to Trash' : item?.kind === 'journal' ? 'Reordered journal' : copied ? 'Copied' : 'Moved')
    } catch (error) {
      setLastError(messageFromError(error))
    } finally {
      endOperation(operationKey)
    }
  }

  async function refreshEncryptionStatus() {
    const response = await api.GetEncryptionStatus()
    setEncryptionStatus(response)
    return response
  }

  async function openEncryptedJournal(journalId: string) {
    if (!encryptionStatus.unlocked) {
      setSelectedItemId(journalId)
      setEncryptionDialog({mode: 'unlock', journalId, action: 'open'})
      return
    }
    setExpanded((current) => new Set([...current, journalId]))
  }

  async function encryptJournal(journalId: string) {
    if (!(await flushActive())) return
    if (!encryptionStatus.masterPasswordConfigured) {
      setEncryptionDialog({mode: 'create', journalId})
      return
    }
    if (!encryptionStatus.unlocked) {
      setEncryptionDialog({mode: 'unlock', journalId, action: 'encrypt'})
      return
    }
    try {
      const response = await api.EncryptJournal(journalId)
      await refreshEncryptionStatus()
      await refreshVisibleTree(response.items, response.trashId)
      setExpanded((current) => new Set([...current, journalId]))
      setJournalDetails(null)
      setStatus('Encrypted journal')
      setEncryptedNotice(encryptedJournalTitle(response.items, journalId))
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  async function decryptJournal(journalId: string) {
    if (!encryptionStatus.unlocked) {
      setEncryptionDialog({mode: 'unlock', journalId, action: 'decrypt'})
      return
    }
    const item = flattened.find((entry) => entry.id === journalId)
    if (item) {
      setDecryptTarget(item)
      return
    }
    await performDecryptJournal(journalId)
  }

  async function performDecryptJournal(journalId: string) {
    const closesActive = activeDoc ? journalIdFor(flattened, activeDoc.id) === journalId : false
    if (!(await flushActive())) return
    try {
      const response = await api.DecryptJournal(journalId)
      await refreshEncryptionStatus()
      await refreshVisibleTree(response.items, response.trashId)
      setJournalDetails(null)
      if (closesActive) {
        clearActiveDocument()
      }
      setStatus('Turned off encryption')
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  async function continueEncryptionAction(dialog: NonNullable<EncryptionDialogState>) {
    if (dialog.mode === 'unlock' && dialog.journalId) {
      if (dialog.action === 'encrypt') {
        const response = await api.EncryptJournal(dialog.journalId)
        await refreshEncryptionStatus()
        await refreshVisibleTree(response.items, response.trashId)
        setExpanded((current) => new Set([...current, dialog.journalId ?? '']))
        setStatus('Encrypted journal')
        setEncryptedNotice(encryptedJournalTitle(response.items, dialog.journalId))
      } else if (dialog.action === 'decrypt') {
        const item = flattened.find((entry) => entry.id === dialog.journalId)
        if (item) setDecryptTarget(item)
      } else if (dialog.action === 'open') {
        await loadTree()
        setExpanded((current) => new Set([...current, dialog.journalId ?? '']))
      }
    }
  }

  async function submitMasterPassword(password: string) {
    const dialog = encryptionDialog
    if (!dialog) return
    try {
      if (dialog.mode === 'create') {
        await api.CreateMasterPassword(password)
        await refreshEncryptionStatus()
        setEncryptionDialog(null)
        const response = await api.EncryptJournal(dialog.journalId)
        await refreshEncryptionStatus()
        await refreshVisibleTree(response.items, response.trashId)
        setExpanded((current) => new Set([...current, dialog.journalId]))
        setStatus('Encrypted journal')
        setEncryptedNotice(encryptedJournalTitle(response.items, dialog.journalId))
      } else if (dialog.mode === 'setup') {
        await api.CreateMasterPassword(password)
        await refreshEncryptionStatus()
        setEncryptionDialog(null)
        setStatus('Master password set')
      } else if (dialog.mode === 'unlock') {
        const status = await api.UnlockEncryption(password)
        setEncryptionStatus(status)
        setEncryptionDialog(null)
        await continueEncryptionAction(dialog)
      }
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  async function submitMasterPasswordChange(currentPassword: string, newPassword: string) {
    if (!(await flushActive())) return
    const activeWasEncrypted = activeDoc ? encryptedJournalIds(flattened).has(journalIdFor(flattened, activeDoc.id)) : false
    try {
      const status = await api.ChangeMasterPassword(currentPassword, newPassword)
      setEncryptionStatus(status)
      setEncryptionDialog(null)
      await loadTree()
      if (activeWasEncrypted) {
        clearActiveDocument()
      }
      setStatus('Master password changed')
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  function clearActiveDocument() {
    latestDraft.current = null
    activeDocId.current = ''
    setActiveDoc(null)
    setSelectedItemId('')
    setSaveState('idle')
    setStatus('Ready')
  }

  async function updateAutosaveInterval(value: number) {
    setAutosaveInterval(value)
    try {
      const response = await api.UpdateAppSettings({autosaveIntervalMs: value, libraryWidth})
      setAutosaveInterval(response.autosaveIntervalMs)
      setLibraryWidth(response.libraryWidth)
      setStatus('Settings updated')
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  const persistLibraryWidth = useCallback(async (width: number) => {
    const nextWidth = clampNumber(Math.round(width), libraryWidthMin, libraryWidthMax)
    try {
      const response = await api.UpdateAppSettings({autosaveIntervalMs: autosaveInterval, libraryWidth: nextWidth})
      setAutosaveInterval(response.autosaveIntervalMs)
      setLibraryWidth(response.libraryWidth)
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }, [autosaveInterval])

  function beginLibraryResize(event: ReactPointerEvent<HTMLDivElement>) {
    const startX = event.clientX
    const startWidth = libraryWidth
    let nextWidth = startWidth
    event.currentTarget.setPointerCapture(event.pointerId)

    function onPointerMove(moveEvent: PointerEvent) {
      nextWidth = clampNumber(startWidth + moveEvent.clientX - startX, libraryWidthMin, libraryWidthMax)
      setLibraryWidth(nextWidth)
    }

    function onPointerUp() {
      window.removeEventListener('pointermove', onPointerMove)
      window.removeEventListener('pointerup', onPointerUp)
      window.removeEventListener('pointercancel', onPointerUp)
      document.body.classList.remove('is-resizing-library')
      void persistLibraryWidth(nextWidth)
    }

    event.preventDefault()
    document.body.classList.add('is-resizing-library')
    window.addEventListener('pointermove', onPointerMove)
    window.addEventListener('pointerup', onPointerUp, {once: true})
    window.addEventListener('pointercancel', onPointerUp, {once: true})
  }

  const creationDisabled = Boolean(searchQuery.trim())

  return (
    <main className="app-shell">
      <section className="main-layout" style={{'--library-width': `${libraryWidth}px`} as CSSProperties}>
        <aside
          className="library-panel"
          onDragOver={(event) => event.preventDefault()}
          onDrop={() => {
            if (!draggedId) return
            if (draggedItem?.kind === 'journal') void moveItem(draggedId, '', -1)
            else if (creationParentId) void moveItem(draggedId, creationParentId, -1)
          }}
        >
          <div className="library-head">
            <div className="mini-actions">
              <button type="button" onClick={() => void createJournal()} disabled={creationDisabled} title="New journal"><BookPlus size={15}/></button>
              <button type="button" onClick={() => void createDocument(creationParentId)} disabled={creationDisabled} title="New document"><FilePlus size={15}/></button>
              <button type="button" onClick={() => void createFolder(creationParentId)} disabled={creationDisabled} title="New folder"><FolderPlus size={15}/></button>
              <button type="button" className={settingsOpen ? 'icon-button active' : 'icon-button'} onClick={() => setSettingsOpen((value) => !value)} title="Settings"><Settings size={15}/></button>
            </div>
          </div>

          <label className="search-box">
            <Search size={15}/>
            <input
              value={searchQuery}
              onChange={(event) => setSearchQuery(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Escape') setSearchQuery('')
              }}
              placeholder="Search documents"
            />
            {searchQuery && <button type="button" onClick={() => setSearchQuery('')} title="Clear search"><X size={14}/></button>}
          </label>

          <div className="tree-scroll" role="tree" aria-label="Journals, documents, and folders">
            {tree.length === 0 && searchQuery.trim() ? (
              <div className="empty-library">
                <p>No matching documents.</p>
              </div>
            ) : tree.length === 0 ? (
              <div className="empty-library">
                <p>No journals yet.</p>
                <button type="button" onClick={() => void createJournal()} disabled={creationDisabled}>Create journal</button>
              </div>
            ) : (
              tree.map((item) => (
                <TreeNode
                  key={item.id}
                  item={item}
                  level={0}
                  activeId={activeDoc?.id ?? ''}
                  selectedId={selectedItemId}
                  expanded={expanded}
                  renamingId={renamingId}
                  searchResults={searchResults}
                  trashId={trashId}
                  journalCount={journalCount}
                  draggedId={draggedId}
                  draggedItem={draggedItem}
                  creationDisabled={creationDisabled}
                  onToggle={(id) => setExpanded((current) => toggleSet(current, id))}
                  onSelect={setSelectedItemId}
                  onOpen={(id) => void openDocument(id)}
                  onExportDocument={(id) => void exportDocumentMarkdown(id)}
                  onRenameStart={setRenamingId}
                  onRenameCommit={(id, title) => void renameItem(id, title)}
                  onDelete={requestDelete}
                  onCreateDocument={(id) => void createDocument(id)}
                  onDuplicateDocument={requestDuplicateDocument}
                  onCreateFolder={(id) => void createFolder(id)}
                  onOpenEncryptedJournal={(id) => void openEncryptedJournal(id)}
                  onDragStart={setDraggedId}
                  onDrop={(id, parentId, sortOrder) => void moveItem(id, parentId, sortOrder)}
                />
              ))
            )}
          </div>
        </aside>

        <div
          className="pane-resizer"
          role="separator"
          aria-label="Resize library pane"
          aria-orientation="vertical"
          aria-valuemin={libraryWidthMin}
          aria-valuemax={libraryWidthMax}
          aria-valuenow={libraryWidth}
          tabIndex={0}
          onPointerDown={beginLibraryResize}
          onKeyDown={(event) => {
            if (event.key === 'ArrowLeft') {
              const nextWidth = clampNumber(libraryWidth - 16, libraryWidthMin, libraryWidthMax)
              setLibraryWidth(nextWidth)
              void persistLibraryWidth(nextWidth)
            }
            if (event.key === 'ArrowRight') {
              const nextWidth = clampNumber(libraryWidth + 16, libraryWidthMin, libraryWidthMax)
              setLibraryWidth(nextWidth)
              void persistLibraryWidth(nextWidth)
            }
          }}
        />

        <section className="document-workspace">
          {settingsOpen ? (
            <SettingsPane
              autosaveInterval={autosaveInterval}
              databaseLocation={databaseLocation}
              masterPasswordConfigured={encryptionStatus.masterPasswordConfigured}
              onAutosaveIntervalChange={(value) => void updateAutosaveInterval(value)}
              onRevealDatabase={() => void api.RevealJournalDatabaseFile().catch((error) => setLastError(messageFromError(error)))}
              onSetMasterPassword={() => setEncryptionDialog(encryptionStatus.masterPasswordConfigured ? {mode: 'change'} : {mode: 'setup'})}
              onDone={() => setSettingsOpen(false)}
            />
          ) : journalDetails ? (
            <JournalDetailsPane details={journalDetails} onDone={() => setJournalDetails(null)}/>
          ) : activeDoc ? (
            <EditorPane
              key={activeDoc.id}
              document={activeDoc}
              focusTitle={titleFocusDocumentId === activeDoc.id}
              saveState={saveState}
              status={status}
              onDraft={updateActiveDraft}
              onSpacingPresetChange={(spacingPreset) => void updateActiveSpacing(spacingPreset)}
              onFlush={flushActive}
              onError={setLastError}
              onRename={(title) => renameItem(activeDoc.id, title)}
              onTitleFocused={() => setTitleFocusDocumentId('')}
              onEditorReady={(editor) => {
                ;(window as unknown as {journalEditor?: Editor}).journalEditor = editor
              }}
              closeRequest={closeRequest}
              onCloseFlushed={() => void api.CompleteCloseAfterFlush()}
              onCloseFlushFailed={() => void api.CancelCloseAfterFlushFailure()}
            />
          ) : (
            <div className="empty-editor">
              <FileText size={42}/>
              <h1>Select or create a document</h1>
              <div>
                <button type="button" onClick={() => void createDocument(creationParentId)} disabled={creationDisabled}><Plus size={16}/>Document</button>
                <button type="button" onClick={() => void createFolder(creationParentId)} disabled={creationDisabled}><FolderPlus size={16}/>Folder</button>
              </div>
            </div>
          )}

          {lastError && (
            <div className="error-toast" role="alert">
              <span>{lastError}</span>
              <button type="button" onClick={() => setLastError('')} title="Dismiss"><X size={14}/></button>
            </div>
          )}

          {deleteTarget && (
            <DeleteDialog
              target={deleteTarget}
              onCancel={() => setDeleteTarget(null)}
              onConfirm={() => void confirmDelete()}
            />
          )}

          {duplicateTarget && (
            <DuplicateDialog
              target={duplicateTarget}
              onCancel={() => setDuplicateTarget(null)}
              onConfirm={() => void confirmDuplicateDocument()}
            />
          )}

          {decryptTarget && (
            <DecryptDialog
              target={decryptTarget}
              onCancel={() => setDecryptTarget(null)}
              onConfirm={() => {
                const id = decryptTarget.id
                setDecryptTarget(null)
                void performDecryptJournal(id)
              }}
            />
          )}

          {encryptedNotice && (
            <EncryptedNoticeDialog
              journalTitle={encryptedNotice}
              onClose={() => setEncryptedNotice('')}
            />
          )}

          {encryptionDialog && (
            <EncryptionDialog
              state={encryptionDialog}
              onCancel={() => setEncryptionDialog(null)}
              onSubmitPassword={(password) => void submitMasterPassword(password)}
              onChangePassword={(currentPassword, newPassword) => void submitMasterPasswordChange(currentPassword, newPassword)}
            />
          )}

          {aboutOpen && <AboutDialog appInfo={appInfo} onClose={() => setAboutOpen(false)}/>}
        </section>
      </section>
    </main>
  )
}

type EditorPaneProps = {
  document: DocumentResponse
  focusTitle?: boolean
  saveState: SaveState
  status: string
  onDraft: (content: ProseMirrorDoc) => Promise<void>
  onSpacingPresetChange: (spacingPreset: SpacingPreset) => void
  onFlush: () => Promise<boolean>
  onError: (message: string) => void
  onRename: (title: string) => Promise<boolean>
  onTitleFocused?: () => void
  onEditorReady: (editor: Editor) => void
  closeRequest: number
  onCloseFlushed: () => void
  onCloseFlushFailed: () => void
}

function JournalDetailsPane({details, onDone}: {details: JournalDetailsResponse, onDone: () => void}) {
  const encryption = details.encryptionState === 'encrypted'
    ? details.encryptionLocked ? 'Encrypted and locked' : 'Encrypted and unlocked'
    : 'Not encrypted'
  return (
    <div className="journal-details">
      <div className="journal-details-head">
        <div><p className="journal-details-kicker">Journal</p><h1>{details.title}</h1></div>
        <button type="button" onClick={onDone}>Done</button>
      </div>
      {details.encryptionLocked ? (
        <p className="journal-details-locked">Unlock encrypted Journals to see details</p>
      ) : (
        <dl>
          <div><dt>Encryption status</dt><dd>{encryption}</dd></div>
          <div><dt>Created</dt><dd>{formatTimestamp(details.createdAt)}</dd></div>
          <div><dt>Documents</dt><dd>{details.documentCount}</dd></div>
          <div><dt>Folders</dt><dd>{details.folderCount}</dd></div>
          <div><dt>Images referenced by documents</dt><dd>{details.imageCount}</dd></div>
        </dl>
      )}
    </div>
  )
}

function SettingsPane({autosaveInterval, databaseLocation, masterPasswordConfigured, onAutosaveIntervalChange, onRevealDatabase, onSetMasterPassword, onDone}: {
  autosaveInterval: number
  databaseLocation: JournalDatabaseLocationResponse
  masterPasswordConfigured: boolean
  onAutosaveIntervalChange: (value: number) => void
  onRevealDatabase: () => void
  onSetMasterPassword: () => void
  onDone: () => void
}) {
  return (
    <div className="settings-page">
      <div className="settings-page-head">
        <h1>Settings</h1>
        <button type="button" onClick={onDone}>Done</button>
      </div>
      <section>
        <h2>Editing</h2>
        <div className="settings-row">
          <div><h3>Autosave interval</h3><p>How often edits are saved while you work.</p></div>
          <label><input type="number" min={500} step={250} value={autosaveInterval} onChange={(event) => onAutosaveIntervalChange(Number(event.target.value))}/><span>ms</span></label>
        </div>
        <div className="settings-row">
          <div><h3>Journal database</h3><p className="database-path">{databaseLocation.path}</p></div>
          {databaseLocation.canReveal && <button type="button" onClick={onRevealDatabase}>Show in file manager</button>}
        </div>
      </section>
      <section>
        <h2>Security</h2>
        <div className="settings-row">
          <div><h3>Master password</h3><p>{masterPasswordConfigured ? 'Change the password that unlocks encrypted Journals.' : 'Set a password before encrypting a Journal.'}</p></div>
          <button type="button" onClick={onSetMasterPassword}>{masterPasswordConfigured ? 'Change master password' : 'Set master password'}</button>
        </div>
      </section>
    </div>
  )
}

function EditorPane({document, focusTitle = false, saveState, status, onDraft, onSpacingPresetChange, onFlush, onError, onRename, onTitleFocused, onEditorReady, closeRequest, onCloseFlushed, onCloseFlushFailed}: EditorPaneProps) {
  const [title, setTitle] = useState(document.title)
  const [linkPopover, setLinkPopover] = useState<LinkPopoverState | null>(null)
  const [canCreateLink, setCanCreateLink] = useState(false)
  const [isTableActive, setIsTableActive] = useState(false)
  const [paragraphStyle, setParagraphStyle] = useState<ParagraphStyle>('normal')
  const [horizontalAlignment, setHorizontalAlignment] = useState<HorizontalAlignment>('left')
  const titleInputRef = useRef<HTMLInputElement | null>(null)
  const skipTitleBlurCommit = useRef(false)
  const imageInputRef = useRef<HTMLInputElement | null>(null)
  const editorRef = useRef<Editor | null>(null)
  const draftTimer = useRef<number | undefined>(undefined)
  const pendingDraft = useRef(false)
  const onDraftRef = useRef(onDraft)
  const onFlushRef = useRef(onFlush)
  const onEditorReadyRef = useRef(onEditorReady)
  const handledCloseRequest = useRef(0)

  useEffect(() => {
    onDraftRef.current = onDraft
    onFlushRef.current = onFlush
    onEditorReadyRef.current = onEditorReady
  }, [onDraft, onFlush, onEditorReady])

  const submitDraft = useCallback(async () => {
    const editor = editorRef.current
    if (!editor) return
    window.clearTimeout(draftTimer.current)
    pendingDraft.current = false
    const content = editor.getJSON() as ProseMirrorDoc
    await onDraftRef.current(content)
  }, [])

  const updateEditorControls = useCallback((editor: Editor) => {
    setCanCreateLink(!editor.state.selection.empty)
    setIsTableActive(editor.isActive('table'))
    setParagraphStyle(activeParagraphStyle(editor))
    setHorizontalAlignment(activeHorizontalAlignment(editor))
  }, [])

  const editor = useEditor({
    extensions: editorExtensions,
    content: document.content,
    autofocus: focusTitle ? false : 'end',
    editorProps: {
      attributes: {
        class: `editor-page spacing-${document.spacingPreset ?? 'compact'}`,
      },
      handleClick: (_view, _pos, event) => {
        if (!event.metaKey && !event.ctrlKey) return false
        const target = event.target
        if (!(target instanceof Element)) return false
        const link = target.closest('a[href]')
        const href = link?.getAttribute('href')
        if (!href) return false

        event.preventDefault()
        openExternalLink(href)
        return true
      },
      handleDOMEvents: {
        paste: (view, event) => {
          const files = imageFilesFromClipboard(event.clipboardData)
          if (files.length === 0) return false

          event.preventDefault()
          view.focus()
          void insertImageFiles(files, view.state.selection.from)
          return true
        },
        drop: (view, event) => {
          const droppedFiles = Array.from(event.dataTransfer?.files ?? [])
          if (droppedFiles.length === 0) return false
          event.preventDefault()
          const files = droppedFiles.filter(isSupportedImageFile)
          if (files.length === 0) {
            onError('Unsupported image format.')
            return true
          }
          const position = view.posAtCoords({left: event.clientX, top: event.clientY})
          void insertImageFiles(files, position?.pos)
          return true
        },
        contextmenu: (view, event) => {
          const {from, to} = view.state.selection
          if (from === to) return false
          const position = view.posAtCoords({left: event.clientX, top: event.clientY})
          if (!position || position.pos < from || position.pos > to) return false

          event.preventDefault()
          view.focus()
          setLinkPopover({
            x: event.clientX,
            y: event.clientY,
            from,
            to,
          })
          return true
        },
      },
    },
    onCreate: ({editor}) => {
      editorRef.current = editor
      updateEditorControls(editor)
      onEditorReadyRef.current(editor)
    },
    onSelectionUpdate: ({editor}) => {
      updateEditorControls(editor)
    },
    onUpdate: ({editor}) => {
      editorRef.current = editor
      updateEditorControls(editor)
      pendingDraft.current = true
      window.clearTimeout(draftTimer.current)
      draftTimer.current = window.setTimeout(() => {
        void submitDraft()
      }, 300)
    },
  })

  useEffect(() => {
    setTitle(document.title)
  }, [document.id, document.title])

  useEffect(() => {
    if (!editor) return
    editor.view.dom.classList.remove('spacing-compact', 'spacing-normal', 'spacing-relaxed')
    editor.view.dom.classList.add(`spacing-${document.spacingPreset ?? 'compact'}`)
  }, [document.spacingPreset, editor])

  useEffect(() => {
    if (!focusTitle) return
    const titleInput = titleInputRef.current
    if (!titleInput) return

    titleInput.focus()
    titleInput.select()
    onTitleFocused?.()
  }, [focusTitle, onTitleFocused])

  useEffect(() => () => window.clearTimeout(draftTimer.current), [])

  useEffect(() => {
    if (closeRequest === 0 || closeRequest === handledCloseRequest.current) return
    handledCloseRequest.current = closeRequest
    void (async () => {
      const flushed = await flushEditor()
      if (flushed) onCloseFlushed()
      else onCloseFlushFailed()
    })()
  }, [closeRequest, onCloseFlushed, onCloseFlushFailed])

  async function flushEditor() {
    if (title.trim() !== document.title) {
      const renamed = await commitTitle()
      if (!renamed) return false
    }
    if (pendingDraft.current) await submitDraft()
    return onFlushRef.current()
  }

  function commitTitle(focusEditor = false) {
    const next = title.trim() || 'Untitled'
    setTitle(next)
    const renamed = next === document.title ? Promise.resolve(true) : onRename(next)

    if (!focusEditor || !editor) return renamed
    skipTitleBlurCommit.current = true
    editor.commands.focus('end')
    return renamed
  }

  function openCreateLinkPopover() {
    if (!editor || editor.state.selection.empty) return
    const {from, to} = editor.state.selection
    const start = editor.view.coordsAtPos(from)
    const end = editor.view.coordsAtPos(to)
    setLinkPopover({
      x: Math.min((start.left + end.right) / 2, window.innerWidth - 24),
      y: Math.max(Math.min(start.bottom + 8, window.innerHeight - 24), 24),
      from,
      to,
    })
  }

  function closeLinkPopover() {
    setLinkPopover(null)
    editor?.commands.focus()
  }

  function submitLink(url: string) {
    if (!editor || !linkPopover) return
    const href = normalizeLinkURL(url)
    if (!href) return

    editor
      .chain()
      .focus()
      .setTextSelection({from: linkPopover.from, to: linkPopover.to})
      .setLink({href})
      .run()
    setLinkPopover(null)
  }

  function openImageFilePicker() {
    imageInputRef.current?.click()
  }

  async function insertImageFiles(files: File[], position?: number) {
    if (!editor) return
    let insertPosition = position
    for (const file of files) {
      try {
        if (file.size > maxInlineImageBytes) {
          throw new Error('Image is larger than the 20 MB limit.')
        }
        if (!isSupportedImageFile(file)) {
          throw new Error('Unsupported image format.')
        }
        const dataURL = await fileToDataURL(file)
        const attachment = await api.CreateDocumentAttachment(document.id, file.name, file.type, dataURL)
        if (!attachment.id) continue
        insertAttachmentImage(attachment.id, attachment.originalName || file.name, insertPosition)
        if (typeof insertPosition === 'number') insertPosition += 1
      } catch (error) {
        onError(messageFromError(error))
      }
    }
  }

  function insertAttachmentImage(attachmentId: string, alt: string, position?: number) {
    if (!editor || !attachmentId) return
    const command = editor.chain().focus()
    if (typeof position === 'number') {
      command.setTextSelection(position)
    }
    command.insertContent({
      type: 'attachmentImage',
      attrs: {attachmentId, alt},
    }).run()
  }

  return (
    <>
      <div className="document-head">
        <input
          ref={titleInputRef}
          className="title-input"
          value={title}
          onChange={(event) => setTitle(event.target.value)}
          onBlur={() => {
            if (skipTitleBlurCommit.current) {
              skipTitleBlurCommit.current = false
              return
            }
            commitTitle()
          }}
          onKeyDown={(event) => {
            if (event.key === 'Enter' || event.key === 'Tab') {
              event.preventDefault()
              commitTitle(true)
            }
            if (event.key === 'Escape') {
              event.preventDefault()
              setTitle(document.title)
              skipTitleBlurCommit.current = true
              event.currentTarget.blur()
            }
          }}
          aria-label="Document title"
        />
        <input
          ref={imageInputRef}
          className="hidden-file-input"
          type="file"
          accept="image/png,image/jpeg,image/gif,image/webp"
          onChange={(event) => {
            const files = Array.from(event.currentTarget.files ?? [])
            event.currentTarget.value = ''
            if (files.length === 0) return
            void insertImageFiles(files)
          }}
        />
        <div className="document-toolbar-row">
          <EditorToolbar
            editor={editor}
            canCreateLink={canCreateLink}
            isTableActive={isTableActive}
            paragraphStyle={paragraphStyle}
            horizontalAlignment={horizontalAlignment}
            spacingPreset={document.spacingPreset ?? 'compact'}
            onCreateLink={openCreateLinkPopover}
            onInsertImage={openImageFilePicker}
            onSpacingPresetChange={onSpacingPresetChange}
          />
        </div>
      </div>
      <div className="paper-scroll" onBlur={flushEditor}>
        <EditorContent editor={editor}/>
      </div>
      {linkPopover ? (
        <LinkCreatePopover
          x={linkPopover.x}
          y={linkPopover.y}
          onSubmit={submitLink}
          onCancel={closeLinkPopover}
        />
      ) : null}
      <footer className="editor-status">
        <SaveIndicator state={saveState} label={status}/>
        <span className="document-dates">
          <span>Created {formatTimestamp(document.createdAt)}</span>
          <span>Updated {formatTimestamp(document.updatedAt)}</span>
        </span>
        <span className="word-count">{editor?.storage.characterCount.words() ?? 0} words</span>
      </footer>
    </>
  )
}

type LinkCreatePopoverProps = {
  x: number
  y: number
  onSubmit: (url: string) => void
  onCancel: () => void
}

function LinkCreatePopover({x, y, onSubmit, onCancel}: LinkCreatePopoverProps) {
  const [url, setURL] = useState('')
  const [error, setError] = useState('')
  const inputRef = useRef<HTMLInputElement | null>(null)

  useEffect(() => {
    inputRef.current?.focus()

    function handlePointerDown(event: MouseEvent) {
      const target = event.target
      if (target instanceof Element && target.closest('.link-popover')) return
      onCancel()
    }

    document.addEventListener('pointerdown', handlePointerDown)
    return () => document.removeEventListener('pointerdown', handlePointerDown)
  }, [onCancel])

  return (
    <form
      className="link-popover"
      style={{left: x, top: y}}
      onSubmit={(event) => {
        event.preventDefault()
        const href = normalizeLinkURL(url)
        if (!href) {
          setError('Enter a valid URL.')
          return
        }
        onSubmit(href)
      }}
      onKeyDown={(event) => {
        if (event.key === 'Escape') {
          event.preventDefault()
          onCancel()
        }
      }}
    >
      <input
        ref={inputRef}
        value={url}
        onChange={(event) => {
          setURL(event.target.value)
          setError('')
        }}
        placeholder="https://example.com"
        aria-label="Link URL"
      />
      <button type="submit">Create Link</button>
      {error ? <span className="link-popover-error">{error}</span> : null}
    </form>
  )
}

function formatTimestamp(value: string) {
  if (!value) return 'Unknown'
  const date = new Date(value)
  if (Number.isNaN(date.valueOf())) return value
  return new Intl.DateTimeFormat(undefined, {dateStyle: 'medium', timeStyle: 'short'}).format(date)
}

function imageFilesFromClipboard(data: DataTransfer | null) {
  if (!data) return []

  const files: File[] = []
  for (const item of Array.from(data.items)) {
    if (item.kind !== 'file' || !item.type.startsWith('image/')) continue
    const file = item.getAsFile()
    if (file) files.push(withClipboardImageName(file, files.length + 1))
  }

  if (files.length > 0) return files

  return Array.from(data.files)
    .filter((file) => file.type.startsWith('image/'))
    .map((file, index) => withClipboardImageName(file, index + 1))
}

function withClipboardImageName(file: File, index: number) {
  if (file.name.trim()) return file
  const extension = imageExtensionForType(file.type)
  return new File([file], `Pasted image ${index}.${extension}`, {
    type: file.type,
    lastModified: file.lastModified,
  })
}

function imageExtensionForType(type: string) {
  switch (type) {
    case 'image/jpeg':
      return 'jpg'
    case 'image/gif':
      return 'gif'
    case 'image/webp':
      return 'webp'
    case 'image/png':
    default:
      return 'png'
  }
}

function normalizeLinkURL(value: string) {
  const trimmed = value.trim()
  if (!trimmed) return ''

  const hasScheme = /^[a-z][a-z0-9+.-]*:\/\//i.test(trimmed) || /^(mailto|tel):/i.test(trimmed)
  const isEmailAddress = /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmed)
  const urlText = hasScheme ? trimmed : isEmailAddress ? `mailto:${trimmed}` : `https://${trimmed}`

  try {
    const url = new URL(urlText)
    if (!['http:', 'https:', 'mailto:', 'tel:'].includes(url.protocol)) return ''
    return url.toString()
  } catch {
    return ''
  }
}

function isSupportedImageFile(file: File) {
  return supportedImageTypes.has(file.type)
}

function fileToDataURL(file: File) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      if (typeof reader.result === 'string') {
        resolve(reader.result)
        return
      }
      reject(new Error('Could not read image.'))
    }
    reader.onerror = () => reject(reader.error ?? new Error('Could not read image.'))
    reader.readAsDataURL(file)
  })
}

function openExternalLink(value: string) {
  const href = normalizeLinkURL(value)
  if (!href) return

  if ('runtime' in window) {
    BrowserOpenURL(href)
    return
  }
  window.open(href, '_blank', 'noopener,noreferrer')
}

function activeParagraphStyle(editor: Editor): ParagraphStyle {
  for (const level of [1, 2, 3, 4, 5, 6] as const) {
    if (editor.isActive('heading', {level})) return `heading-${level}`
  }
  return 'normal'
}

function activeHorizontalAlignment(editor: Editor): HorizontalAlignment {
  for (const alignment of ['center', 'right'] as const) {
    if (
      editor.isActive({textAlign: alignment}) ||
      editor.isActive('attachmentImage', {textAlign: alignment}) ||
      editor.isActive('table', {textAlign: alignment})
    ) {
      return alignment
    }
  }
  return 'left'
}

type EditorToolbarProps = {
  editor: Editor | null
  canCreateLink: boolean
  isTableActive: boolean
  paragraphStyle: ParagraphStyle
  horizontalAlignment: HorizontalAlignment
  disabled?: boolean
  spacingPreset: SpacingPreset
  onCreateLink: () => void
  onInsertImage: () => void
  onSpacingPresetChange: (spacingPreset: SpacingPreset) => void
}

function EditorToolbar({editor, canCreateLink, isTableActive, paragraphStyle, horizontalAlignment, disabled = false, spacingPreset, onCreateLink, onInsertImage, onSpacingPresetChange}: EditorToolbarProps) {
  const blocked = disabled || !editor
  const linkBlocked = blocked || !canCreateLink
  const insertTable = () => {
    editor?.chain().focus().insertTable({rows: 3, cols: 3, withHeaderRow: true}).setTableWidthPercent(75).run()
  }
  const setParagraphStyle = (style: ParagraphStyle) => {
    if (!editor) return
    if (style === 'normal') {
      editor.chain().focus().setParagraph().run()
      return
    }

    const level = Number(style.replace('heading-', '')) as HeadingLevel
    editor.chain().focus().setHeading({level}).run()
  }
  const setHorizontalAlignment = (alignment: HorizontalAlignment) => {
    editor?.chain().focus().setTextAlign(alignment).run()
  }
  return (
    <div className="format-toolbar" aria-label="Formatting">
      <div className="toolbar-group">
        <label className="paragraph-style-control" title="Paragraph style">
          <select
            value={paragraphStyle}
            disabled={blocked}
            onChange={(event) => setParagraphStyle(event.target.value as ParagraphStyle)}
            aria-label="Paragraph style"
          >
            <option value="normal">Normal</option>
            <option value="heading-1">Header 1</option>
            <option value="heading-2">Header 2</option>
            <option value="heading-3">Header 3</option>
            <option value="heading-4">Header 4</option>
            <option value="heading-5">Header 5</option>
            <option value="heading-6">Header 6</option>
          </select>
        </label>
        <Tool disabled={blocked} active={editor?.isActive('bold')} label="Bold" onClick={() => editor?.chain().focus().toggleBold().run()}><Bold size={16}/></Tool>
        <Tool disabled={blocked} active={editor?.isActive('italic')} label="Italic" onClick={() => editor?.chain().focus().toggleItalic().run()}><Italic size={16}/></Tool>
        <Tool disabled={blocked} active={editor?.isActive('underline')} label="Underline" onClick={() => editor?.chain().focus().toggleUnderline().run()}><UnderlineIcon size={16}/></Tool>
        <Tool disabled={blocked} active={editor?.isActive('strike')} label="Strike" onClick={() => editor?.chain().focus().toggleStrike().run()}><Strikethrough size={16}/></Tool>
        <Tool disabled={blocked} active={editor?.isActive('highlight')} label="Highlight" onClick={() => editor?.chain().focus().toggleHighlight({color: '#fff1a8'}).run()}><Highlighter size={16}/></Tool>
        <Tool disabled={linkBlocked} active={editor?.isActive('link')} label="Create link" onClick={onCreateLink}><Link2 size={16}/></Tool>
        <Tool disabled={blocked} label="Insert image" onClick={onInsertImage}><ImageIcon size={16}/></Tool>
        <label className="spacing-control" title="Paragraph spacing">
          <select
            value={spacingPreset}
            disabled={blocked}
            onChange={(event) => onSpacingPresetChange(event.target.value as SpacingPreset)}
          >
            <option value="compact">Compact</option>
            <option value="normal">Normal</option>
            <option value="relaxed">Relaxed</option>
          </select>
        </label>
        <Tool disabled={blocked} active={horizontalAlignment === 'left'} label="Align left" onClick={() => setHorizontalAlignment('left')}><AlignLeft size={16}/></Tool>
        <Tool disabled={blocked} active={horizontalAlignment === 'center'} label="Align center" onClick={() => setHorizontalAlignment('center')}><AlignCenter size={16}/></Tool>
        <Tool disabled={blocked} active={horizontalAlignment === 'right'} label="Align right" onClick={() => setHorizontalAlignment('right')}><AlignRight size={16}/></Tool>
        <span className="toolbar-divider"/>
        <Tool disabled={blocked} active={editor?.isActive('bulletList')} label="Bullet list" onClick={() => editor?.chain().focus().toggleBulletList().run()}><List size={16}/></Tool>
        <Tool disabled={blocked} active={editor?.isActive('orderedList')} label="Numbered list" onClick={() => editor?.chain().focus().toggleOrderedList().run()}><ListOrdered size={16}/></Tool>
        <Tool disabled={blocked} active={editor?.isActive('taskList')} label="Task list" onClick={() => editor?.chain().focus().toggleTaskList().run()}><CheckSquare size={16}/></Tool>
        <Tool disabled={blocked} active={isTableActive} label="Insert table" onClick={insertTable}><Table2 size={16}/></Tool>
        <span className="toolbar-divider"/>
        <Tool disabled={blocked} label="Undo" onClick={() => editor?.chain().focus().undo().run()}><Undo2 size={16}/></Tool>
        <Tool disabled={blocked} label="Redo" onClick={() => editor?.chain().focus().redo().run()}><Redo2 size={16}/></Tool>
      </div>
      {isTableActive ? <TableToolbar editor={editor} disabled={blocked}/> : null}
    </div>
  )
}

type TableToolbarProps = {
  disabled: boolean
  editor: Editor | null
}

function TableToolbar({editor, disabled}: TableToolbarProps) {
  const blocked = disabled || !editor
  return (
    <div className="table-toolbar" aria-label="Table tools">
      <span className="table-toolbar-label">Row</span>
      <Tool disabled={blocked} label="Add row below" onClick={() => editor?.chain().focus().addRowAfter().run()}><Plus size={16}/></Tool>
      <Tool disabled={blocked} label="Delete row" onClick={() => editor?.chain().focus().deleteRow().run()}><Minus size={16}/></Tool>
      <span className="toolbar-divider"/>
      <span className="table-toolbar-label">Column</span>
      <Tool disabled={blocked} label="Add column right" onClick={() => editor?.chain().focus().addColumnAfter().run()}><Plus size={16}/></Tool>
      <Tool disabled={blocked} label="Delete column" onClick={() => editor?.chain().focus().deleteColumn().run()}><Minus size={16}/></Tool>
      <span className="toolbar-divider"/>
      <span className="table-toolbar-label">Table</span>
      <Tool disabled={blocked} label="Toggle header row" onClick={() => editor?.chain().focus().toggleHeaderRow().run()}><TableProperties size={16}/></Tool>
      <Tool disabled={blocked} label="Toggle header column" onClick={() => editor?.chain().focus().toggleHeaderColumn().run()}><Table2 size={16}/></Tool>
      <Tool disabled={blocked} label="Delete table" onClick={() => editor?.chain().focus().deleteTable().run()}><Trash2 size={16}/></Tool>
    </div>
  )
}

type ToolProps = {
  active?: boolean
  children: ReactNode
  disabled?: boolean
  label: string
  onClick: () => void
}

function Tool({active = false, children, disabled = false, label, onClick}: ToolProps) {
  return (
    <button
      type="button"
      className={active ? 'tool active' : 'tool'}
      disabled={disabled}
      aria-label={label}
      title={label}
      onMouseDown={(event) => event.preventDefault()}
      onClick={onClick}
    >
      {children}
    </button>
  )
}

type TreeNodeProps = {
  item: TreeItem
  level: number
  activeId: string
  selectedId: string
  expanded: Set<string>
  renamingId: string
  searchResults: Set<string>
  trashId: string
  journalCount: number
  draggedId: string
  draggedItem?: TreeItem
  creationDisabled: boolean
  onToggle: (id: string) => void
  onSelect: (id: string) => void
  onOpen: (id: string) => void
  onExportDocument: (id: string) => void
  onRenameStart: (id: string) => void
  onRenameCommit: (id: string, title: string) => void
  onDelete: (id: string) => void
  onCreateDocument: (id: string) => void
  onDuplicateDocument: (id: string) => void
  onCreateFolder: (id: string) => void
  onOpenEncryptedJournal: (id: string) => void
  onDragStart: (id: string) => void
  onDrop: (id: string, parentId: string, sortOrder: number) => void
}

function TreeNode(props: TreeNodeProps) {
  const {item, level, activeId, selectedId, expanded, renamingId, searchResults, trashId, journalCount, draggedId, draggedItem} = props
  const isJournal = item.kind === 'journal'
  const isFolder = item.kind === 'folder'
  const isContainer = isJournal || isFolder
  const isExpanded = expanded.has(item.id)
  const isTrash = item.id === trashId
  const isEncryptedJournal = isJournal && item.encryptionState === 'encrypted'
  const isLockedJournal = isEncryptedJournal && item.encryptionLocked
  const deleteDisabled = isJournal && journalCount <= 1
  const isMatch = searchResults.has(item.id)
  const [draftTitle, setDraftTitle] = useState(item.title)

  useEffect(() => setDraftTitle(item.title), [item.title])

  const invalidDrop = Boolean(draggedId && (draggedId === item.id || !isContainer || (draggedItem?.kind === 'journal' && !isJournal)))

  return (
    <div className="tree-row-wrap">
      <div
        className={[
          'tree-row',
          activeId === item.id ? 'active' : '',
          selectedId === item.id ? 'selected' : '',
          isMatch ? 'match' : '',
          invalidDrop ? 'reject-drop' : '',
          isEncryptedJournal ? 'encrypted' : '',
          isLockedJournal ? 'locked' : '',
          isFolder || isJournal ? 'has-count' : '',
        ].filter(Boolean).join(' ')}
        style={{paddingLeft: 10 + level * 16}}
        role="treeitem"
        tabIndex={0}
        draggable={!isTrash}
        onClick={() => props.onSelect(item.id)}
        onDragStart={() => props.onDragStart(item.id)}
        onDragOver={(event) => {
          if (!invalidDrop && isContainer) event.preventDefault()
        }}
        onDrop={(event) => {
          event.stopPropagation()
          if (!draggedId || invalidDrop || !isContainer) return
          if (draggedItem?.kind === 'journal') props.onDrop(draggedId, '', item.sortOrder)
          else props.onDrop(draggedId, item.id, -1)
        }}
        onKeyDown={(event) => {
          props.onSelect(item.id)
          if (event.key === 'Enter' && isLockedJournal) props.onOpenEncryptedJournal(item.id)
          else if (event.key === 'Enter' && item.kind === 'document') props.onOpen(item.id)
          if (event.key === 'F2' && !isTrash) props.onRenameStart(item.id)
          if (event.key === 'Delete' && !isTrash && !deleteDisabled) props.onDelete(item.id)
          if (event.key === 'ArrowRight' && isContainer) props.onToggle(item.id)
          if (event.key === 'ArrowLeft' && isContainer) props.onToggle(item.id)
        }}
        onDoubleClick={() => !isTrash && props.onRenameStart(item.id)}
      >
        <button type="button" className="tree-chevron" onClick={(event) => {
          event.stopPropagation()
          if (isLockedJournal) props.onOpenEncryptedJournal(item.id)
          else if (isContainer) props.onToggle(item.id)
        }} title={isExpanded ? 'Collapse' : 'Expand'}>
          {isContainer ? (isExpanded ? <ChevronDown size={15}/> : <ChevronRight size={15}/>) : <span/>}
        </button>
        {isTrash ? <Trash2 size={16}/> : isLockedJournal ? <Shield size={16}/> : isJournal ? isExpanded ? <BookOpenText size={16}/> : <BookMarked size={16}/> : isFolder ? <Folder size={16}/> : <FileText size={16}/>}

        {renamingId === item.id ? (
          <input
            className="rename-input"
            value={draftTitle}
            autoFocus
            onChange={(event) => setDraftTitle(event.target.value)}
            onBlur={() => props.onRenameCommit(item.id, draftTitle)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') event.currentTarget.blur()
              if (event.key === 'Escape') props.onRenameStart('')
            }}
          />
        ) : (
          <button type="button" className="tree-title" title={item.title} onClick={(event) => {
            event.stopPropagation()
            props.onSelect(item.id)
            isLockedJournal ? props.onOpenEncryptedJournal(item.id) : item.kind === 'document' ? props.onOpen(item.id) : props.onToggle(item.id)
          }}>
            {item.title}
          </button>
        )}

        {isFolder && <span className="tree-badge" title={`${item.itemCount} items`}>{item.itemCount}</span>}
        {isJournal && <span className="tree-badge" title={`${item.documentCount} documents`}>{item.documentCount}</span>}

        <div className="tree-actions">
          {isContainer && !isTrash && <button type="button" onClick={(event) => {
            event.stopPropagation()
            props.onCreateDocument(item.id)
          }} disabled={props.creationDisabled || isLockedJournal} title="New document"><Plus size={13}/></button>}
          {item.kind === 'document' && !isTrash && <button type="button" onClick={(event) => {
            event.stopPropagation()
            props.onDuplicateDocument(item.id)
          }} title="Duplicate document"><Files size={13}/></button>}
          {item.kind === 'document' && !isTrash && <button type="button" onClick={(event) => {
            event.stopPropagation()
            props.onExportDocument(item.id)
          }} title="Export document as Markdown"><Download size={13}/></button>}
          {isContainer && !isTrash && <button type="button" onClick={(event) => {
            event.stopPropagation()
            props.onCreateFolder(item.id)
          }} disabled={props.creationDisabled || isLockedJournal} title="New folder"><FolderPlus size={13}/></button>}
          {!isTrash && !isJournal && <button type="button" onClick={(event) => {
            event.stopPropagation()
            props.onDelete(item.id)
          }} disabled={deleteDisabled} title={deleteDisabled ? 'At least one journal is required' : 'Delete'}><Trash2 size={13}/></button>}
        </div>
      </div>
      {isContainer && isExpanded && item.children.map((child) => (
        <TreeNode key={child.id} {...props} item={child} level={level + 1}/>
      ))}
    </div>
  )
}

function EncryptionDialog({state, onCancel, onSubmitPassword, onChangePassword}: {
  state: NonNullable<EncryptionDialogState>
  onCancel: () => void
  onSubmitPassword: (password: string) => void
  onChangePassword: (currentPassword: string, newPassword: string) => void
}) {
  const [password, setPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [newPasswordConfirm, setNewPasswordConfirm] = useState('')
  const isChange = state.mode === 'change'
  const isCreate = state.mode === 'create' || state.mode === 'setup'
  const title = isCreate ? 'Create master password' : state.mode === 'change' ? 'Change master password' : 'Unlock encrypted journals'

  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') onCancel()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [onCancel])

  function submit() {
    if (isChange) {
      if (!currentPassword.trim() || !newPassword.trim() || newPassword !== newPasswordConfirm) return
      onChangePassword(currentPassword, newPassword)
      return
    }
    if (!password.trim()) return
    if (isCreate && password !== confirmPassword) return
    onSubmitPassword(password)
  }

  return (
    <div className="dialog-backdrop" role="presentation" onMouseDown={onCancel}>
      <section className="confirm-dialog encryption-dialog" role="dialog" aria-modal="true" aria-labelledby="encryption-title" onMouseDown={(event) => event.stopPropagation()}>
        <h2 id="encryption-title">{title}</h2>
        {isChange ? (
          <div className="password-fields">
            <input type="password" value={currentPassword} autoFocus placeholder="Current password" onChange={(event) => setCurrentPassword(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter') submit() }}/>
            <input type="password" value={newPassword} placeholder="New password" onChange={(event) => setNewPassword(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter') submit() }}/>
            <input type="password" value={newPasswordConfirm} placeholder="Confirm new password" onChange={(event) => setNewPasswordConfirm(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter') submit() }}/>
          </div>
        ) : (
          <div className="password-fields">
            <input type="password" value={password} autoFocus placeholder="Master password" onChange={(event) => setPassword(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter') submit() }}/>
            {isCreate && <input type="password" value={confirmPassword} placeholder="Confirm password" onChange={(event) => setConfirmPassword(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter') submit() }}/>}
          </div>
        )}
        <div className="dialog-actions">
          <button type="button" onClick={onCancel}>Cancel</button>
          <button type="button" onClick={submit}>{isChange ? 'Change password' : isCreate ? 'Create password' : 'Unlock'}</button>
        </div>
      </section>
    </div>
  )
}

function DeleteDialog({target, onCancel, onConfirm}: {target: NonNullable<DeleteTarget>, onCancel: () => void, onConfirm: () => void}) {
  const action = target.item.kind === 'journal' || target.inTrash ? 'Permanently delete' : 'Move to Trash'
  const detail = target.item.kind === 'journal'
    ? `This will permanently delete this journal and all ${target.item.documentCount} documents inside it. It will not go to Trash.`
    : target.item.kind === 'folder'
      ? target.inTrash ? 'This will permanently delete the folder and everything inside it.' : 'This will move the folder and everything inside it to Trash.'
      : target.inTrash ? 'This document will be permanently removed.' : 'This document will move to Trash.'

  return (
    <div className="dialog-backdrop" role="presentation" onMouseDown={onCancel}>
      <section className="confirm-dialog" role="dialog" aria-modal="true" aria-labelledby="delete-title" onMouseDown={(event) => event.stopPropagation()}>
        <h2 id="delete-title">{action} "{target.item.title}"?</h2>
        <p>{detail}</p>
        <div className="dialog-actions">
          <button type="button" onClick={onCancel}>Cancel</button>
          <button type="button" className={target.item.kind === 'journal' || target.inTrash ? 'danger-action' : ''} onClick={onConfirm}>{action}</button>
        </div>
      </section>
    </div>
  )
}

function DuplicateDialog({target, onCancel, onConfirm}: {target: TreeItem, onCancel: () => void, onConfirm: () => void}) {
  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') onCancel()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [onCancel])

  return (
    <div className="dialog-backdrop" role="presentation" onMouseDown={onCancel}>
      <section className="confirm-dialog" role="dialog" aria-modal="true" aria-labelledby="duplicate-title" onMouseDown={(event) => event.stopPropagation()}>
        <h2 id="duplicate-title">Duplicate "{target.title}"?</h2>
        <p>This will create a new document named "Copy of {target.title}" in the same location.</p>
        <div className="dialog-actions">
          <button type="button" onClick={onCancel}>Cancel</button>
          <button type="button" onClick={onConfirm}>Duplicate</button>
        </div>
      </section>
    </div>
  )
}

function DecryptDialog({target, onCancel, onConfirm}: {target: TreeItem, onCancel: () => void, onConfirm: () => void}) {
  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') onCancel()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [onCancel])

  return (
    <div className="dialog-backdrop" role="presentation" onMouseDown={onCancel}>
      <section className="confirm-dialog" role="dialog" aria-modal="true" aria-labelledby="decrypt-title" onMouseDown={(event) => event.stopPropagation()}>
        <h2 id="decrypt-title">Turn off encryption for "{target.title}"?</h2>
        <p>This will create a plaintext copy of the Journal and remove the encrypted version. Large Journals might take a few minutes.</p>
        <div className="dialog-actions">
          <button type="button" onClick={onCancel}>Cancel</button>
          <button type="button" onClick={onConfirm}>Turn off encryption</button>
        </div>
      </section>
    </div>
  )
}

function EncryptedNoticeDialog({journalTitle, onClose}: {journalTitle: string, onClose: () => void}) {
  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape' || event.key === 'Enter') onClose()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [onClose])

  return (
    <div className="dialog-backdrop" role="presentation" onMouseDown={onClose}>
      <section className="confirm-dialog" role="dialog" aria-modal="true" aria-labelledby="encrypted-notice-title" onMouseDown={(event) => event.stopPropagation()}>
        <h2 id="encrypted-notice-title">Encryption complete</h2>
        <p>"{journalTitle}" has been encrypted. Its contents are excluded from search and will require the master password after Journal is restarted or locked.</p>
        <div className="dialog-actions">
          <button type="button" onClick={onClose}>OK</button>
        </div>
      </section>
    </div>
  )
}

function AboutDialog({appInfo, onClose}: {appInfo: AppInfo, onClose: () => void}) {
  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [onClose])

  return (
    <div className="dialog-backdrop" role="presentation" onMouseDown={onClose}>
      <section className="about-dialog" role="dialog" aria-modal="true" aria-labelledby="about-title" onMouseDown={(event) => event.stopPropagation()}>
        <button type="button" className="about-close" onClick={onClose} title="Close" aria-label="Close"><X size={14}/></button>
        <img src={appIcon} alt="" className="about-icon"/>
        <h2 id="about-title">{appInfo.name}</h2>
        <p className="about-version">Version {appInfo.version}</p>
        <p className="about-disclaimer">{appInfo.disclaimer}</p>
      </section>
    </div>
  )
}

function SaveIndicator({state, label}: {state: SaveState, label: string}) {
  return (
    <div className={`save-indicator ${state}`}>
      <span/>
      {label}
    </div>
  )
}

function orderTree(items: TreeItem[], trashId: string): TreeItem[] {
  return [...items]
    .sort((a, b) => {
      if (a.id === trashId) return 1
      if (b.id === trashId) return -1
      return 0
    })
    .map((item) => ({...item, children: orderTree(item.children, trashId)}))
}

function flattenTree(items: TreeItem[]): TreeItem[] {
  return items.flatMap((item) => [item, ...flattenTree(item.children)])
}

function expandAllContainers(items: TreeItem[]): Set<string> {
  return new Set(flattenTree(items).filter((item) => item.kind === 'journal' || item.kind === 'folder').map((item) => item.id))
}

function ancestorIDs(items: TreeItem[], id: string): string[] {
  const path: string[] = []
  function walk(nodes: TreeItem[], ancestors: string[]): boolean {
    for (const node of nodes) {
      if (node.id === id) {
        path.push(...ancestors)
        return true
      }
      if (walk(node.children, [...ancestors, node.id])) return true
    }
    return false
  }
  walk(items, [])
  return path
}

function journalIdFor(items: TreeItem[], id: string) {
  const byId = new Map(items.map((item) => [item.id, item]))
  let current = byId.get(id)
  while (current) {
    if (current.kind === 'journal') return current.id
    current = current.parentId ? byId.get(current.parentId) : undefined
  }
  return ''
}

function creationParentFor(items: TreeItem[], selectedId: string, fallbackJournalId: string, trashId: string) {
  const byId = new Map(items.map((item) => [item.id, item]))
  let selected = selectedId ? byId.get(selectedId) : undefined
  while (selected) {
    if (selected.id === trashId) return fallbackJournalId
    if (selected.kind === 'journal' || selected.kind === 'folder') return selected.id
    selected = selected.parentId ? byId.get(selected.parentId) : undefined
  }
  return fallbackJournalId
}

function shouldAutoDismissError(message: string) {
  return message.trim().length > 0
}

function encryptedJournalIds(items: TreeItem[]) {
  return new Set(flattenTree(items).filter((item) => item.kind === 'journal' && item.encryptionState === 'encrypted').map((item) => item.id))
}

function encryptedJournalTitle(items: TreeItem[], journalId: string) {
  return flattenTree(items).find((item) => item.id === journalId)?.title ?? 'Journal'
}

function isDescendantOf(items: TreeItem[], id: string, ancestorId: string) {
  const byId = new Map(items.map((item) => [item.id, item]))
  let current = byId.get(id)
  while (current?.parentId) {
    if (current.parentId === ancestorId) return true
    current = byId.get(current.parentId)
  }
  return false
}

function toggleSet(values: Set<string>, id: string) {
  const next = new Set(values)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  return next
}

function clampNumber(value: number, minValue: number, maxValue: number) {
  return Math.min(maxValue, Math.max(minValue, value))
}

export default App
