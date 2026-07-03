import {Extension, type CommandProps} from '@tiptap/core'

const indentedTypes = ['paragraph', 'heading']
const indentStepRem = 2
const maxIndentLevel = 6

declare module '@tiptap/core' {
  interface Commands<ReturnType> {
    blockIndent: {
      indentBlock: () => ReturnType
      outdentBlock: () => ReturnType
    }
  }
}

function normalizedIndent(value: unknown) {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

function updateSelectedBlockIndent({state, dispatch}: CommandProps, delta: number) {
  const {selection, tr} = state
  let touched = false
  let changed = false

  if (selection.empty) {
    for (let depth = selection.$from.depth; depth > 0; depth -= 1) {
      const node = selection.$from.node(depth)
      if (!indentedTypes.includes(node.type.name)) continue

      touched = true
      const current = normalizedIndent(node.attrs.indent)
      const next = Math.min(maxIndentLevel, Math.max(0, current + delta))
      if (next === current) return true

      tr.setNodeMarkup(selection.$from.before(depth), undefined, {
        ...node.attrs,
        indent: next,
      })
      dispatch?.(tr)
      return true
    }

    return false
  }

  state.doc.nodesBetween(selection.from, selection.to, (node, pos) => {
    if (!indentedTypes.includes(node.type.name)) return true

    touched = true
    const current = normalizedIndent(node.attrs.indent)
    const next = Math.min(maxIndentLevel, Math.max(0, current + delta))
    if (next === current) return false

    tr.setNodeMarkup(pos, undefined, {
      ...node.attrs,
      indent: next,
    })
    changed = true
    return false
  })

  if (!changed) return touched

  dispatch?.(tr)
  return true
}

export const BlockIndent = Extension.create({
  name: 'blockIndent',

  addGlobalAttributes() {
    return [
      {
        types: indentedTypes,
        attributes: {
          indent: {
            default: 0,
            keepOnSplit: true,
            parseHTML: (element: HTMLElement) => {
              const attr = Number.parseInt(element.getAttribute('data-indent') || '', 10)
              if (Number.isFinite(attr)) return Math.min(maxIndentLevel, Math.max(0, attr))

              const margin = Number.parseFloat(element.style.marginLeft || '')
              if (!Number.isFinite(margin)) return 0

              return Math.min(maxIndentLevel, Math.max(0, Math.round(margin / indentStepRem)))
            },
            renderHTML: (attributes: Record<string, unknown>) => {
              const indent = normalizedIndent(attributes.indent)
              if (indent <= 0) return {}

              return {
                'data-indent': String(indent),
                style: `margin-left: ${indent * indentStepRem}rem`,
              }
            },
          },
        },
      },
    ]
  },

  addCommands() {
    return {
      indentBlock:
        () =>
        (props) => updateSelectedBlockIndent(props, 1),
      outdentBlock:
        () =>
        (props) => updateSelectedBlockIndent(props, -1),
    }
  },

  addKeyboardShortcuts() {
    return {
      Tab: ({editor}) => {
        if (editor.isActive('table')) {
          if (editor.commands.goToNextCell()) return true
          return editor.chain().addRowAfter().goToNextCell().run()
        }

        if (editor.isActive('taskItem')) {
          return editor.commands.sinkListItem('taskItem') || true
        }

        if (editor.isActive('listItem')) {
          return editor.commands.sinkListItem('listItem') || true
        }

        return editor.commands.indentBlock()
      },
      'Shift-Tab': ({editor}) => {
        if (editor.isActive('table')) {
          return editor.commands.goToPreviousCell()
        }

        if (editor.isActive('taskItem')) {
          return editor.commands.liftListItem('taskItem') || true
        }

        if (editor.isActive('listItem')) {
          return editor.commands.liftListItem('listItem') || true
        }

        return editor.commands.outdentBlock()
      },
    }
  },
})
