import {Table} from '@tiptap/extension-table'
import type {CommandProps} from '@tiptap/core'
import {Plugin, PluginKey} from '@tiptap/pm/state'
import type {EditorView} from '@tiptap/pm/view'

const tableWidthPluginKey = new PluginKey('tableWidthResize')
const defaultTableWidthPercent = 75
const minTableWidthPercent = 25
const maxTableWidthPercent = 100
const tableResizeEdgeWidth = 10

declare module '@tiptap/core' {
  interface Commands<ReturnType> {
    tableWidth: {
      setTableWidthPercent: (widthPercent: number) => ReturnType
    }
  }
}

function clampTableWidth(value: number) {
  if (!Number.isFinite(value)) return defaultTableWidthPercent
  return Math.min(maxTableWidthPercent, Math.max(minTableWidthPercent, Math.round(value)))
}

function tableContentWidth(container: HTMLElement) {
  const style = window.getComputedStyle(container)
  const padding =
    Number.parseFloat(style.paddingLeft || '0') +
    Number.parseFloat(style.paddingRight || '0')
  return Math.max(1, container.getBoundingClientRect().width - padding)
}

function findTableDepth($pos: CommandProps['state']['selection']['$from']) {
  for (let depth = $pos.depth; depth > 0; depth -= 1) {
    if ($pos.node(depth).type.name === 'table') return depth
  }
  return null
}

function setTableWidth({state, dispatch}: CommandProps, widthPercent: number) {
  const depth = findTableDepth(state.selection.$from)
  if (depth === null) return false

  const table = state.selection.$from.node(depth)
  const pos = state.selection.$from.before(depth)
  const nextAttrs = {
    ...table.attrs,
    widthPercent: clampTableWidth(widthPercent),
  }

  if (dispatch) {
    dispatch(state.tr.setNodeMarkup(pos, undefined, nextAttrs))
  }
  return true
}

function findTablePosFromDOM(view: EditorView, table: HTMLTableElement) {
  const tbody = table.tBodies[0]
  if (!tbody) return null

  const domPos = view.posAtDOM(tbody, 0)
  const $pos = view.state.doc.resolve(Math.max(0, Math.min(view.state.doc.content.size, domPos)))
  const depth = findTableDepth($pos)
  if (depth === null) return null

  return $pos.before(depth)
}

function applyTableWidths(view: EditorView) {
  view.state.doc.descendants((node, pos) => {
    if (node.type.name !== 'table') return true

    const dom = view.nodeDOM(pos)
    if (!(dom instanceof HTMLElement)) return false

    const wrapper = dom.classList.contains('tableWrapper') ? dom : dom.closest('.tableWrapper')
    if (!(wrapper instanceof HTMLElement)) return false

    const widthPercent = typeof node.attrs.widthPercent === 'number' ? clampTableWidth(node.attrs.widthPercent) : null
    const textAlign = node.attrs.textAlign === 'center' || node.attrs.textAlign === 'right' ? node.attrs.textAlign : 'left'
    wrapper.style.maxWidth = '100%'
    wrapper.style.width = widthPercent === null ? '' : `${widthPercent}%`
    wrapper.style.marginLeft = textAlign === 'center' || textAlign === 'right' ? 'auto' : ''
    wrapper.style.marginRight = textAlign === 'center' ? 'auto' : ''
    wrapper.dataset.textAlign = textAlign
    if (widthPercent === null) {
      delete wrapper.dataset.tableWidthPercent
    } else {
      wrapper.dataset.tableWidthPercent = String(widthPercent)
    }

    const table = wrapper.querySelector('table')
    if (table instanceof HTMLTableElement) {
      table.style.width = '100%'
    }

    return false
  })
}

function handleTableWidthMouseDown(view: EditorView, event: MouseEvent) {
  const target = event.target
  if (!(target instanceof Element)) return false
  if (target.closest('.column-resize-handle')) return false

  const wrapper = target.closest('.tableWrapper')
  if (!(wrapper instanceof HTMLElement)) return false

  const table = wrapper.querySelector('table')
  if (!(table instanceof HTMLTableElement)) return false

  const rect = wrapper.getBoundingClientRect()
  const isOnRightEdge =
    event.clientX >= rect.right - tableResizeEdgeWidth &&
    event.clientX <= rect.right + tableResizeEdgeWidth &&
    event.clientY >= rect.top &&
    event.clientY <= rect.bottom
  if (!isOnRightEdge) return false

  const tablePos = findTablePosFromDOM(view, table)
  if (tablePos === null) return false
  const resolvedTablePos = tablePos
  const resizeWrapper = wrapper
  const resizeTable = table

  const editorPage = wrapper.closest('.editor-page')
  if (!(editorPage instanceof HTMLElement)) return false

  event.preventDefault()
  event.stopPropagation()

  const startX = event.clientX
  const startWidth = rect.width
  const contentWidth = tableContentWidth(editorPage)
  let nextWidthPercent = clampTableWidth((startWidth / contentWidth) * 100)

  function updateWidth(moveEvent: MouseEvent) {
    const nextWidth = startWidth + moveEvent.clientX - startX
    nextWidthPercent = clampTableWidth((nextWidth / contentWidth) * 100)
    resizeWrapper.style.width = `${nextWidthPercent}%`
    resizeTable.style.width = '100%'
  }

  function stopResize() {
    window.removeEventListener('mousemove', updateWidth)
    window.removeEventListener('mouseup', stopResize)
    document.body.classList.remove('is-resizing-table')

    const node = view.state.doc.nodeAt(resolvedTablePos)
    if (!node || node.type.name !== 'table') return

    view.dispatch(view.state.tr.setNodeMarkup(resolvedTablePos, undefined, {
      ...node.attrs,
      widthPercent: nextWidthPercent,
    }))
  }

  document.body.classList.add('is-resizing-table')
  window.addEventListener('mousemove', updateWidth)
  window.addEventListener('mouseup', stopResize)
  return true
}

export const ResizableTable = Table.extend({
  addAttributes() {
    return {
      ...this.parent?.(),
      widthPercent: {
        default: null,
        parseHTML: (element) => {
          const value = element.getAttribute('data-table-width-percent')
          if (!value) return null
          return clampTableWidth(Number.parseFloat(value))
        },
        renderHTML: (attributes) => {
          if (typeof attributes.widthPercent !== 'number') return {}
          const widthPercent = clampTableWidth(attributes.widthPercent)
          return {
            'data-table-width-percent': String(widthPercent),
          }
        },
      },
    }
  },

  addCommands() {
    return {
      ...this.parent?.(),
      setTableWidthPercent:
        (widthPercent: number) =>
        (props) => setTableWidth(props, widthPercent),
    }
  },

  addProseMirrorPlugins() {
    const parentPlugins = this.parent?.() ?? []
    return [
      ...parentPlugins,
      new Plugin({
        key: tableWidthPluginKey,
        view: (view) => {
          requestAnimationFrame(() => applyTableWidths(view))
          return {
            update: (nextView) => {
              requestAnimationFrame(() => applyTableWidths(nextView))
            },
          }
        },
        props: {
          handleDOMEvents: {
            mousedown: handleTableWidthMouseDown,
          },
        },
      }),
    ]
  },
})
