import {useCallback, useEffect, useMemo, useRef, useState, type ReactNode} from 'react'
import {EditorContent, useEditor} from '@tiptap/react'
import type {Editor} from '@tiptap/react'
import {
  Bold,
  CheckSquare,
  ChevronDown,
  ChevronRight,
  FileText,
  Folder,
  FolderPlus,
  Highlighter,
  Italic,
  List,
  ListOrdered,
  Plus,
  Redo2,
  Search,
  Settings2,
  Strikethrough,
  Table2,
  Trash2,
  Type,
  Underline as UnderlineIcon,
  Undo2,
  X,
} from 'lucide-react'
import {editorExtensions} from './editor/extensions'
import {
  api,
  messageFromError,
  type DocumentResponse,
  type ProseMirrorDoc,
  type TreeItem,
} from './wails/libraryApi'

type SaveState = 'idle' | 'dirty' | 'saving' | 'saved' | 'error'

type DeleteTarget = {
  item: TreeItem
  inTrash: boolean
} | null

function App() {
  const [tree, setTree] = useState<TreeItem[]>([])
  const [trashId, setTrashId] = useState('')
  const [activeDoc, setActiveDoc] = useState<DocumentResponse | null>(null)
  const [selectedItemId, setSelectedItemId] = useState('')
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [renamingId, setRenamingId] = useState('')
  const [searchOpen, setSearchOpen] = useState(false)
  const [searchQuery, setSearchQuery] = useState('')
  const [searchResults, setSearchResults] = useState<Set<string>>(new Set())
  const [saveState, setSaveState] = useState<SaveState>('idle')
  const [status, setStatus] = useState('Ready')
  const [lastError, setLastError] = useState('')
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [autosaveInterval, setAutosaveInterval] = useState(2000)
  const [draggedId, setDraggedId] = useState('')
  const [deleteTarget, setDeleteTarget] = useState<DeleteTarget>(null)

  const flattened = useMemo(() => flattenTree(tree), [tree])
  const selectedItem = flattened.find((item) => item.id === selectedItemId)
  const activeItem = activeDoc ? flattened.find((item) => item.id === activeDoc.id) : undefined
  const commandItem = selectedItem ?? activeItem

  const applyTree = useCallback((items: TreeItem[], nextTrashId: string) => {
    setTree(items)
    setTrashId(nextTrashId)
    setExpanded((current) => {
      const next = new Set(current)
      next.add(nextTrashId)
      return next
    })
  }, [])

  const loadTree = useCallback(async () => {
    const response = await api.GetLibraryTree()
    applyTree(response.items, response.trashId)
  }, [applyTree])

  useEffect(() => {
    let live = true
    async function boot() {
      try {
        const [treeResponse, settings] = await Promise.all([api.GetLibraryTree(), api.GetAppSettings()])
        if (!live) return
        applyTree(treeResponse.items, treeResponse.trashId)
        setAutosaveInterval(settings.autosaveIntervalMs)
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
        setExpanded(expandAllFolders(response.items))
      } catch (error) {
        setLastError(messageFromError(error))
      }
    }, 180)
    return () => window.clearTimeout(handle)
  }, [applyTree, loadTree, searchQuery])

  async function flushActive() {
    if (!activeDoc || saveState !== 'dirty') return true
    setSaveState('saving')
    try {
      await api.FlushDocument(activeDoc.id)
      setSaveState('saved')
      setStatus('Saved')
      return true
    } catch (error) {
      setSaveState('error')
      setLastError(messageFromError(error))
      return false
    }
  }

  async function openDocument(id: string) {
    if (activeDoc?.id === id) return
    if (!(await flushActive())) return
    try {
      const response = await api.OpenDocument(id)
      setActiveDoc(response)
      setSelectedItemId(id)
      applyTree(response.tree.items, response.tree.trashId)
      setSaveState('saved')
      setStatus('Opened')
      revealItem(response.item, response.tree.items)
    } catch (error) {
      setLastError(messageFromError(error))
      setSaveState('error')
    }
  }

  function revealItem(item: TreeItem, items: TreeItem[]) {
    const ancestors = ancestorIDs(items, item.id)
    setExpanded((current) => new Set([...current, ...ancestors]))
  }

  async function createDocument(parentId = '') {
    if (!(await flushActive())) return
    try {
      const response = await api.CreateDocument(parentId)
      setActiveDoc(response)
      setSelectedItemId(response.id)
      applyTree(response.tree.items, response.tree.trashId)
      setSaveState('saved')
      setStatus('Created document')
      setRenamingId(response.id)
      revealItem(response.item, response.tree.items)
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  async function createFolder(parentId = '') {
    try {
      const response = await api.CreateFolder(parentId, 'New Folder')
      applyTree(response.tree.items, response.tree.trashId)
      setSelectedItemId(response.item.id)
      setRenamingId(response.item.id)
      revealItem(response.item, response.tree.items)
      setStatus('Created folder')
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  async function renameItem(id: string, title: string) {
    try {
      const response = await api.RenameItem(id, title)
      applyTree(response.tree.items, response.tree.trashId)
      setRenamingId('')
      if (activeDoc?.id === id) {
        setActiveDoc((current) => current ? {...current, title: response.item.title, item: response.item} : current)
      }
      setStatus('Renamed')
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  function requestDelete(id: string) {
    const item = flattened.find((entry) => entry.id === id)
    if (!item || item.systemKey === 'trash') return
    const inTrash = isDescendantOf(flattened, id, trashId)
    setSelectedItemId(id)
    setDeleteTarget({item, inTrash})
  }

  async function confirmDelete() {
    if (!deleteTarget) return
    const {item, inTrash} = deleteTarget
    const id = item.id
    setDeleteTarget(null)
    const verb = inTrash ? 'permanently delete' : 'move to Trash'
    try {
      const response = inTrash ? await api.PermanentlyDeleteItem(id) : await api.MoveItemToTrash(id)
      applyTree(response.items, response.trashId)
      if (activeDoc && (activeDoc.id === id || isDescendantOf(flattened, activeDoc.id, id))) {
        setActiveDoc(null)
        setSaveState('idle')
      }
      setSelectedItemId('')
      setStatus(inTrash ? 'Deleted permanently' : 'Moved to Trash')
    } catch (error) {
      setLastError(messageFromError(error))
    }
  }

  async function moveItem(id: string, parentId: string) {
    if (id === parentId) return
    try {
      const response = await api.MoveItem(id, parentId, -1)
      applyTree(response.items, response.trashId)
      setStatus(parentId === trashId ? 'Moved to Trash' : 'Moved')
    } catch (error) {
      setLastError(messageFromError(error))
    }
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

  const documentParent = commandItem?.parentId ?? ''
  const createParent = commandItem?.kind === 'folder' ? commandItem.id : documentParent

  return (
    <main className="app-shell">
      <header className="app-toolbar">
        <div className="brand-lockup">
          <span className="app-mark">J</span>
          <strong>Journal</strong>
        </div>

        <div className="toolbar-cluster" aria-label="Library actions">
          <button type="button" onClick={() => void createDocument(createParent)} title="New document">
            <Plus size={16}/>
            <span>Document</span>
          </button>
          <button type="button" onClick={() => void createFolder(createParent)} title="New folder">
            <FolderPlus size={16}/>
            <span>Folder</span>
          </button>
          <button type="button" onClick={() => commandItem && requestDelete(commandItem.id)} disabled={!commandItem || commandItem.systemKey === 'trash'} title="Delete">
            <Trash2 size={16}/>
          </button>
        </div>

        <EditorToolbar editor={null} disabled/>

        <div className="toolbar-tail">
          <button type="button" className={searchOpen ? 'icon-button active' : 'icon-button'} onClick={() => setSearchOpen((value) => !value)} title="Search library">
            <Search size={16}/>
          </button>
          <button type="button" className={settingsOpen ? 'icon-button active' : 'icon-button'} onClick={() => setSettingsOpen((value) => !value)} title="Autosave settings">
            <Settings2 size={16}/>
          </button>
          <SaveIndicator state={saveState} label={status}/>
        </div>
      </header>

      <section className="main-layout">
        <aside
          className="library-panel"
          onDragOver={(event) => event.preventDefault()}
          onDrop={() => draggedId && void moveItem(draggedId, '')}
        >
          <div className="library-head">
            <strong>Library</strong>
            <div className="mini-actions">
              <button type="button" onClick={() => void createDocument('')} title="New top-level document"><Plus size={15}/></button>
              <button type="button" onClick={() => void createFolder('')} title="New top-level folder"><FolderPlus size={15}/></button>
            </div>
          </div>

          {searchOpen && (
            <label className="search-box">
              <Search size={15}/>
              <input
                value={searchQuery}
                autoFocus
                onChange={(event) => setSearchQuery(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === 'Escape') setSearchQuery('')
                }}
                placeholder="Search documents"
              />
              {searchQuery && <button type="button" onClick={() => setSearchQuery('')} title="Clear search"><X size={14}/></button>}
            </label>
          )}

          <div className="tree-scroll" role="tree" aria-label="Documents and folders">
            {tree.length === 0 ? (
              <div className="empty-library">
                <p>No documents yet.</p>
                <button type="button" onClick={() => void createDocument('')}>Create document</button>
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
                  draggedId={draggedId}
                  onToggle={(id) => setExpanded((current) => toggleSet(current, id))}
                  onSelect={setSelectedItemId}
                  onOpen={(id) => void openDocument(id)}
                  onRenameStart={setRenamingId}
                  onRenameCommit={(id, title) => void renameItem(id, title)}
                  onDelete={requestDelete}
                  onCreateDocument={(id) => void createDocument(id)}
                  onCreateFolder={(id) => void createFolder(id)}
                  onDragStart={setDraggedId}
                  onDrop={(id, parentId) => void moveItem(id, parentId)}
                />
              ))
            )}
            {searchQuery.trim() && tree.length === 0 && (
              <div className="empty-library">
                <p>No matching documents.</p>
              </div>
            )}
          </div>
        </aside>

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
            </div>
          )}

          {activeDoc ? (
            <EditorPane
              key={activeDoc.id}
              document={activeDoc}
              saveState={saveState}
              onDraft={async (content) => {
                setSaveState('dirty')
                try {
                  await api.UpdateDocumentDraft(activeDoc.id, content)
                  setStatus('Autosave pending')
                } catch (error) {
                  setSaveState('error')
                  setLastError(messageFromError(error))
                }
              }}
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
              <p>Your library is stored locally in SQLite and saved automatically.</p>
              <div>
                <button type="button" onClick={() => void createDocument('')}><Plus size={16}/>Document</button>
                <button type="button" onClick={() => void createFolder('')}><FolderPlus size={16}/>Folder</button>
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
        </section>
      </section>
    </main>
  )
}

type EditorPaneProps = {
  document: DocumentResponse
  saveState: SaveState
  onDraft: (content: ProseMirrorDoc) => Promise<void>
  onFlush: () => void
  onRename: (title: string) => void
  onEditorReady: (editor: Editor) => void
}

function EditorPane({document, saveState, onDraft, onFlush, onRename, onEditorReady}: EditorPaneProps) {
  const [title, setTitle] = useState(document.title)
  const draftTimer = useRef<number | undefined>(undefined)

  const editor = useEditor({
    extensions: editorExtensions,
    content: document.content,
    autofocus: 'end',
    editorProps: {
      attributes: {
        class: 'editor-page',
      },
    },
    onCreate: ({editor}) => onEditorReady(editor),
    onUpdate: ({editor}) => {
      const content = editor.getJSON() as ProseMirrorDoc
      window.clearTimeout(draftTimer.current)
      draftTimer.current = window.setTimeout(() => {
        void onDraft(content)
      }, 220)
    },
  })

  useEffect(() => () => window.clearTimeout(draftTimer.current), [])

  useEffect(() => {
    setTitle(document.title)
  }, [document.id, document.title])

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
      <div className="paper-scroll" onBlur={onFlush}>
        <EditorContent editor={editor}/>
      </div>
      <footer className="editor-status">
        <span>{saveState === 'dirty' ? 'Pending autosave' : saveState === 'saving' ? 'Saving' : saveState === 'error' ? 'Save failed' : 'Saved'}</span>
        <span>{editor?.storage.characterCount.words() ?? 0} words</span>
      </footer>
    </>
  )
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
  draggedId: string
  onToggle: (id: string) => void
  onSelect: (id: string) => void
  onOpen: (id: string) => void
  onRenameStart: (id: string) => void
  onRenameCommit: (id: string, title: string) => void
  onDelete: (id: string) => void
  onCreateDocument: (id: string) => void
  onCreateFolder: (id: string) => void
  onDragStart: (id: string) => void
  onDrop: (id: string, parentId: string) => void
}

function TreeNode(props: TreeNodeProps) {
  const {item, level, activeId, selectedId, expanded, renamingId, searchResults, trashId, draggedId} = props
  const isFolder = item.kind === 'folder'
  const isExpanded = expanded.has(item.id)
  const isTrash = item.id === trashId
  const isMatch = searchResults.has(item.id)
  const [draftTitle, setDraftTitle] = useState(item.title)

  useEffect(() => setDraftTitle(item.title), [item.title])

  const invalidDrop = draggedId && (draggedId === item.id || item.kind === 'document')

  return (
    <div className="tree-row-wrap">
      <div
        className={[
          'tree-row',
          activeId === item.id ? 'active' : '',
          selectedId === item.id ? 'selected' : '',
          isMatch ? 'match' : '',
          invalidDrop ? 'reject-drop' : '',
        ].filter(Boolean).join(' ')}
        style={{paddingLeft: 10 + level * 16}}
        role="treeitem"
        tabIndex={0}
        draggable={!isTrash}
        onClick={() => props.onSelect(item.id)}
        onDragStart={() => props.onDragStart(item.id)}
        onDragOver={(event) => {
          if (isFolder) event.preventDefault()
        }}
        onDrop={(event) => {
          event.stopPropagation()
          if (draggedId && isFolder) props.onDrop(draggedId, item.id)
        }}
        onKeyDown={(event) => {
          props.onSelect(item.id)
          if (event.key === 'Enter' && item.kind === 'document') props.onOpen(item.id)
          if (event.key === 'F2' && !isTrash) props.onRenameStart(item.id)
          if (event.key === 'Delete' && !isTrash) props.onDelete(item.id)
          if (event.key === 'ArrowRight' && isFolder) props.onToggle(item.id)
          if (event.key === 'ArrowLeft' && isFolder) props.onToggle(item.id)
        }}
        onDoubleClick={() => !isTrash && props.onRenameStart(item.id)}
      >
        <button type="button" className="tree-chevron" onClick={(event) => {
          event.stopPropagation()
          if (isFolder) props.onToggle(item.id)
        }} title={isExpanded ? 'Collapse' : 'Expand'}>
          {isFolder ? (isExpanded ? <ChevronDown size={15}/> : <ChevronRight size={15}/>) : <span/>}
        </button>
        {isFolder ? <Folder size={16}/> : <FileText size={16}/>}

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
            item.kind === 'document' ? props.onOpen(item.id) : props.onToggle(item.id)
          }}>
            {item.title}
          </button>
        )}

        <div className="tree-actions">
          {isFolder && !isTrash && <button type="button" onClick={(event) => {
            event.stopPropagation()
            props.onCreateDocument(item.id)
          }} title="New document"><Plus size={13}/></button>}
          {isFolder && !isTrash && <button type="button" onClick={(event) => {
            event.stopPropagation()
            props.onCreateFolder(item.id)
          }} title="New folder"><FolderPlus size={13}/></button>}
          {!isTrash && <button type="button" onClick={(event) => {
            event.stopPropagation()
            props.onDelete(item.id)
          }} title="Delete"><Trash2 size={13}/></button>}
        </div>
      </div>
      {isFolder && isExpanded && item.children.map((child) => (
        <TreeNode key={child.id} {...props} item={child} level={level + 1}/>
      ))}
    </div>
  )
}

function DeleteDialog({target, onCancel, onConfirm}: {target: NonNullable<DeleteTarget>, onCancel: () => void, onConfirm: () => void}) {
  const action = target.inTrash ? 'Permanently delete' : 'Move to Trash'
  const detail = target.item.kind === 'folder'
    ? target.inTrash ? 'This will permanently delete the folder and everything inside it.' : 'This will move the folder and everything inside it to Trash.'
    : target.inTrash ? 'This document will be permanently removed.' : 'This document will move to Trash.'

  return (
    <div className="dialog-backdrop" role="presentation" onMouseDown={onCancel}>
      <section className="confirm-dialog" role="dialog" aria-modal="true" aria-labelledby="delete-title" onMouseDown={(event) => event.stopPropagation()}>
        <h2 id="delete-title">{action} "{target.item.title}"?</h2>
        <p>{detail}</p>
        <div className="dialog-actions">
          <button type="button" onClick={onCancel}>Cancel</button>
          <button type="button" className={target.inTrash ? 'danger-action' : ''} onClick={onConfirm}>{action}</button>
        </div>
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

function flattenTree(items: TreeItem[]): TreeItem[] {
  return items.flatMap((item) => [item, ...flattenTree(item.children)])
}

function expandAllFolders(items: TreeItem[]): Set<string> {
  return new Set(flattenTree(items).filter((item) => item.kind === 'folder').map((item) => item.id))
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
