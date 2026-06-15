import {useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type PointerEvent as ReactPointerEvent, type ReactNode} from 'react'
import {EditorContent, useEditor} from '@tiptap/react'
import type {Editor} from '@tiptap/react'
import {
  Bold,
  Book,
  BookPlus,
  CheckSquare,
  ChevronDown,
  ChevronRight,
  FilePlus,
  FileText,
  Folder,
  FolderPlus,
  Highlighter,
  Italic,
  KeyRound,
  List,
  ListOrdered,
  Lock,
  Plus,
  Redo2,
  Search,
  Settings,
  Strikethrough,
  Table2,
  Trash2,
  Type,
  Underline as UnderlineIcon,
  Undo2,
  Unlock,
  X,
} from 'lucide-react'
import {editorExtensions} from './editor/extensions'
import {
  api,
  messageFromError,
  type AppInfo,
  type DocumentResponse,
  type EncryptionStatusResponse,
  type ProseMirrorDoc,
  type TreeItem,
} from './wails/libraryApi'
import appIcon from './assets/appicon.png'
import {EventsOn} from '../wailsjs/runtime/runtime'

type SaveState = 'idle' | 'dirty' | 'saving' | 'saved' | 'error'

type DeleteTarget = {
  item: TreeItem
  inTrash: boolean
} | null

type DecryptTarget = TreeItem | null

type EncryptionDialogState =
  | {mode: 'create', journalId: string}
  | {mode: 'unlock', journalId?: string, action?: 'encrypt' | 'decrypt' | 'open'}
  | {mode: 'change'}
  | null

function App() {
  const [tree, setTree] = useState<TreeItem[]>([])
  const [trashId, setTrashId] = useState('')
  const [activeDoc, setActiveDoc] = useState<DocumentResponse | null>(null)
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
  const [appInfo, setAppInfo] = useState<AppInfo>({
    name: 'Journal',
    version: '0.0.0-dev',
    disclaimer: 'Journal is free and open source software.',
  })
  const [autosaveInterval, setAutosaveInterval] = useState(2000)
  const [draggedId, setDraggedId] = useState('')
  const [deleteTarget, setDeleteTarget] = useState<DeleteTarget>(null)
  const [decryptTarget, setDecryptTarget] = useState<DecryptTarget>(null)
  const [encryptedNotice, setEncryptedNotice] = useState('')
  const [encryptionDialog, setEncryptionDialog] = useState<EncryptionDialogState>(null)
  const [encryptionStatus, setEncryptionStatus] = useState<EncryptionStatusResponse>({
    masterPasswordConfigured: false,
    unlocked: false,
    encryptedJournalIds: [],
  })
  const [libraryWidth, setLibraryWidth] = useState(300)
  const autosaveTimer = useRef<number | undefined>(undefined)
  const latestDraft = useRef<{id: string, content: ProseMirrorDoc, version: number} | null>(null)
  const sentDraftVersion = useRef(0)
  const draftVersion = useRef(0)
  const activeDocId = useRef('')

  const flattened = useMemo(() => flattenTree(tree), [tree])
  const journalCount = useMemo(() => tree.filter((item) => item.kind === 'journal').length, [tree])
  const defaultJournalId = tree.find((item) => item.kind === 'journal')?.id ?? ''
  const draggedItem = draggedId ? flattened.find((item) => item.id === draggedId) : undefined

  const applyTree = useCallback((items: TreeItem[], nextTrashId: string) => {
    setTree(orderTree(items, nextTrashId))
    setTrashId(nextTrashId)
  }, [])

  const loadTree = useCallback(async () => {
    const response = await api.GetLibraryTree()
    applyTree(response.items, response.trashId)
  }, [applyTree])

  useEffect(() => {
    activeDocId.current = activeDoc?.id ?? ''
    window.clearTimeout(autosaveTimer.current)
  }, [activeDoc?.id])

  useEffect(() => () => window.clearTimeout(autosaveTimer.current), [])

  useEffect(() => {
    if (!lastError.toLowerCase().includes('invalid master password')) return undefined
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
    let live = true
    async function boot() {
      try {
        const [treeResponse, settings, info, encryption] = await Promise.all([api.GetLibraryTree(), api.GetAppSettings(), api.GetAppInfo(), api.GetEncryptionStatus()])
        if (!live) return
        applyTree(treeResponse.items, treeResponse.trashId)
        setAutosaveInterval(settings.autosaveIntervalMs)
        setAppInfo(info)
        setEncryptionStatus(encryption)
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
    if (!searchQuery.trim()) {
      setSearchResults(new Set())
      void loadTree()
      return
    }
    const handle = window.setTimeout(async () => {
      try {
        const response = await api.SearchLibrary(searchQuery)
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
    const query = searchQuery.trim()
    if (!query) {
      if (fallbackItems && fallbackTrashId) {
        applyTree(fallbackItems, fallbackTrashId)
      } else {
        await loadTree()
      }
      return
    }

    const response = await api.SearchLibrary(query)
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

  async function openDocument(id: string) {
    if (activeDoc?.id === id) return
    if (!(await flushActive())) return
    try {
      const response = await api.OpenDocument(id)
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
    setSelectedItemId(response.id)
    if (!hasSearch) {
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
      showDocument(response, 'Created document')
      setRenamingId(response.id)
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

  async function renameItem(id: string, title: string) {
    try {
      const response = await api.RenameItem(id, title)
      await refreshVisibleTree(response.tree.items, response.tree.trashId)
      setRenamingId('')
      if (activeDoc?.id === id) {
        setActiveDoc((current) => current ? {...current, title: response.item.title, updatedAt: response.item.updatedAt, item: response.item} : current)
      }
      setStatus('Renamed')
    } catch (error) {
      setLastError(messageFromError(error))
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
    setDeleteTarget(null)
    try {
      if (activeDoc && (activeDoc.id === id || isDescendantOf(flattened, activeDoc.id, id))) {
        if (!(await flushActive())) return
      }
      const response = item.kind === 'journal'
        ? await api.DeleteJournal(id)
        : inTrash ? await api.PermanentlyDeleteItem(id) : await api.MoveItemToTrash(id)
      await refreshVisibleTree(response.items, response.trashId)
      if (activeDoc && (activeDoc.id === id || isDescendantOf(flattened, activeDoc.id, id))) {
        setActiveDoc(null)
        setSaveState('idle')
      }
      setSelectedItemId('')
      setStatus(item.kind === 'journal' || inTrash ? 'Deleted permanently' : 'Moved to Trash')
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  async function moveItem(id: string, parentId: string, sortOrder = -1) {
    if (id === parentId) return
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
      const response = await api.UpdateAppSettings({autosaveIntervalMs: value})
      setAutosaveInterval(response.autosaveIntervalMs)
      setStatus('Settings updated')
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  function beginLibraryResize(event: ReactPointerEvent<HTMLDivElement>) {
    const startX = event.clientX
    const startWidth = libraryWidth
    event.currentTarget.setPointerCapture(event.pointerId)

    function onPointerMove(moveEvent: PointerEvent) {
      const nextWidth = Math.min(520, Math.max(220, startWidth + moveEvent.clientX - startX))
      setLibraryWidth(nextWidth)
    }

    function onPointerUp() {
      window.removeEventListener('pointermove', onPointerMove)
      window.removeEventListener('pointerup', onPointerUp)
      window.removeEventListener('pointercancel', onPointerUp)
      document.body.classList.remove('is-resizing-library')
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
            else if (defaultJournalId) void moveItem(draggedId, defaultJournalId, -1)
          }}
        >
          <div className="library-head">
            <div className="mini-actions">
              <button type="button" onClick={() => void createJournal()} disabled={creationDisabled} title="New journal"><BookPlus size={15}/></button>
              <button type="button" onClick={() => void createDocument(defaultJournalId)} disabled={creationDisabled} title="New document"><FilePlus size={15}/></button>
              <button type="button" onClick={() => void createFolder(defaultJournalId)} disabled={creationDisabled} title="New folder"><FolderPlus size={15}/></button>
              <button type="button" className={settingsOpen ? 'icon-button active' : 'icon-button'} onClick={() => setSettingsOpen((value) => !value)} title="Autosave settings"><Settings size={15}/></button>
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
                  onRenameStart={setRenamingId}
                  onRenameCommit={(id, title) => void renameItem(id, title)}
                  onDelete={requestDelete}
                  onCreateDocument={(id) => void createDocument(id)}
                  onCreateFolder={(id) => void createFolder(id)}
                  onEncryptJournal={(id) => void encryptJournal(id)}
                  onDecryptJournal={(id) => void decryptJournal(id)}
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
          aria-valuemin={220}
          aria-valuemax={520}
          aria-valuenow={libraryWidth}
          tabIndex={0}
          onPointerDown={beginLibraryResize}
          onKeyDown={(event) => {
            if (event.key === 'ArrowLeft') setLibraryWidth((width) => Math.max(220, width - 16))
            if (event.key === 'ArrowRight') setLibraryWidth((width) => Math.min(520, width + 16))
          }}
        />

        <section className="document-workspace">
          {settingsOpen && (
            <div className="settings-strip">
              <label>
                Autosave interval
                <input
                  type="number"
                  min={500}
                  step={250}
                  value={autosaveInterval}
                  onChange={(event) => void updateAutosaveInterval(Number(event.target.value))}
                />
                <span>ms</span>
              </label>
              {encryptionStatus.masterPasswordConfigured && (
                <button type="button" onClick={() => setEncryptionDialog({mode: 'change'})}><KeyRound size={14}/>Change master password</button>
              )}
            </div>
          )}

          {activeDoc ? (
            <EditorPane
              key={activeDoc.id}
              document={activeDoc}
              saveState={saveState}
              status={status}
              onDraft={updateActiveDraft}
              onFlush={() => void flushActive()}
              onRename={(title) => void renameItem(activeDoc.id, title)}
              onEditorReady={(editor) => {
                ;(window as unknown as {journalEditor?: Editor}).journalEditor = editor
              }}
            />
          ) : (
            <div className="empty-editor">
              <FileText size={42}/>
              <h1>Select or create a document</h1>
              <div>
                <button type="button" onClick={() => void createDocument(defaultJournalId)} disabled={creationDisabled}><Plus size={16}/>Document</button>
                <button type="button" onClick={() => void createFolder(defaultJournalId)} disabled={creationDisabled}><FolderPlus size={16}/>Folder</button>
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
  saveState: SaveState
  status: string
  onDraft: (content: ProseMirrorDoc) => Promise<void>
  onFlush: () => void
  onRename: (title: string) => void
  onEditorReady: (editor: Editor) => void
}

function EditorPane({document, saveState, status, onDraft, onFlush, onRename, onEditorReady}: EditorPaneProps) {
  const [title, setTitle] = useState(document.title)
  const editorRef = useRef<Editor | null>(null)
  const draftTimer = useRef<number | undefined>(undefined)
  const pendingDraft = useRef(false)
  const onDraftRef = useRef(onDraft)
  const onFlushRef = useRef(onFlush)
  const onEditorReadyRef = useRef(onEditorReady)

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

  const editor = useEditor({
    extensions: editorExtensions,
    content: document.content,
    autofocus: 'end',
    editorProps: {
      attributes: {
        class: 'editor-page',
      },
    },
    onCreate: ({editor}) => {
      editorRef.current = editor
      onEditorReadyRef.current(editor)
    },
    onUpdate: ({editor}) => {
      editorRef.current = editor
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

  useEffect(() => () => window.clearTimeout(draftTimer.current), [])

  function flushEditor() {
    void (async () => {
      if (pendingDraft.current) {
        await submitDraft()
      }
      onFlushRef.current()
    })()
  }

  function commitTitle() {
    const next = title.trim() || 'Untitled'
    setTitle(next)
    onRename(next)
  }

  return (
    <>
      <div className="document-head">
        <input
          className="title-input"
          value={title}
          onChange={(event) => setTitle(event.target.value)}
          onBlur={commitTitle}
          onKeyDown={(event) => {
            if (event.key === 'Enter') event.currentTarget.blur()
            if (event.key === 'Escape') {
              setTitle(document.title)
              event.currentTarget.blur()
            }
          }}
          aria-label="Document title"
        />
        <EditorToolbar editor={editor}/>
      </div>
      <div className="paper-scroll" onBlur={flushEditor}>
        <EditorContent editor={editor}/>
      </div>
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

function formatTimestamp(value: string) {
  if (!value) return 'Unknown'
  const date = new Date(value)
  if (Number.isNaN(date.valueOf())) return value
  return new Intl.DateTimeFormat(undefined, {dateStyle: 'medium', timeStyle: 'short'}).format(date)
}

type EditorToolbarProps = {
  editor: Editor | null
  disabled?: boolean
}

function EditorToolbar({editor, disabled = false}: EditorToolbarProps) {
  const blocked = disabled || !editor
  return (
    <div className="format-toolbar" aria-label="Formatting">
      <Tool disabled={blocked} active={editor?.isActive('heading', {level: 1})} label="Heading" onClick={() => editor?.chain().focus().toggleHeading({level: 1}).run()}><Type size={16}/></Tool>
      <Tool disabled={blocked} active={editor?.isActive('bold')} label="Bold" onClick={() => editor?.chain().focus().toggleBold().run()}><Bold size={16}/></Tool>
      <Tool disabled={blocked} active={editor?.isActive('italic')} label="Italic" onClick={() => editor?.chain().focus().toggleItalic().run()}><Italic size={16}/></Tool>
      <Tool disabled={blocked} active={editor?.isActive('underline')} label="Underline" onClick={() => editor?.chain().focus().toggleUnderline().run()}><UnderlineIcon size={16}/></Tool>
      <Tool disabled={blocked} active={editor?.isActive('strike')} label="Strike" onClick={() => editor?.chain().focus().toggleStrike().run()}><Strikethrough size={16}/></Tool>
      <Tool disabled={blocked} active={editor?.isActive('highlight')} label="Highlight" onClick={() => editor?.chain().focus().toggleHighlight({color: '#fff1a8'}).run()}><Highlighter size={16}/></Tool>
      <span className="toolbar-divider"/>
      <Tool disabled={blocked} active={editor?.isActive('bulletList')} label="Bullet list" onClick={() => editor?.chain().focus().toggleBulletList().run()}><List size={16}/></Tool>
      <Tool disabled={blocked} active={editor?.isActive('orderedList')} label="Numbered list" onClick={() => editor?.chain().focus().toggleOrderedList().run()}><ListOrdered size={16}/></Tool>
      <Tool disabled={blocked} active={editor?.isActive('taskList')} label="Task list" onClick={() => editor?.chain().focus().toggleTaskList().run()}><CheckSquare size={16}/></Tool>
      <Tool disabled={blocked} label="Table" onClick={() => editor?.chain().focus().insertTable({rows: 3, cols: 3, withHeaderRow: true}).run()}><Table2 size={16}/></Tool>
      <span className="toolbar-divider"/>
      <Tool disabled={blocked} label="Undo" onClick={() => editor?.chain().focus().undo().run()}><Undo2 size={16}/></Tool>
      <Tool disabled={blocked} label="Redo" onClick={() => editor?.chain().focus().redo().run()}><Redo2 size={16}/></Tool>
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
  onRenameStart: (id: string) => void
  onRenameCommit: (id: string, title: string) => void
  onDelete: (id: string) => void
  onCreateDocument: (id: string) => void
  onCreateFolder: (id: string) => void
  onEncryptJournal: (id: string) => void
  onDecryptJournal: (id: string) => void
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
        {isTrash ? <Trash2 size={16}/> : isEncryptedJournal ? <Lock size={16}/> : isJournal ? <Book size={16}/> : isFolder ? <Folder size={16}/> : <FileText size={16}/>}

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
          {isContainer && !isTrash && <button type="button" onClick={(event) => {
            event.stopPropagation()
            props.onCreateFolder(item.id)
          }} disabled={props.creationDisabled || isLockedJournal} title="New folder"><FolderPlus size={13}/></button>}
          {isJournal && !isTrash && !isEncryptedJournal && <button type="button" onClick={(event) => {
            event.stopPropagation()
            props.onEncryptJournal(item.id)
          }} title="Encrypt journal"><Lock size={13}/></button>}
          {isJournal && !isTrash && isEncryptedJournal && <button type="button" onClick={(event) => {
            event.stopPropagation()
            props.onDecryptJournal(item.id)
          }} title="Turn off encryption"><Unlock size={13}/></button>}
          {!isTrash && <button type="button" onClick={(event) => {
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
  const title = state.mode === 'create' ? 'Create master password' : state.mode === 'change' ? 'Change master password' : 'Unlock encrypted journals'

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
    if (state.mode === 'create' && password !== confirmPassword) return
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
            {state.mode === 'create' && <input type="password" value={confirmPassword} placeholder="Confirm password" onChange={(event) => setConfirmPassword(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter') submit() }}/>}
          </div>
        )}
        <div className="dialog-actions">
          <button type="button" onClick={onCancel}>Cancel</button>
          <button type="button" onClick={submit}>{isChange ? 'Change password' : state.mode === 'create' ? 'Create password' : 'Unlock'}</button>
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

export default App
