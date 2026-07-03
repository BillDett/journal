import {Extension} from '@tiptap/core'
import type {Node as ProseMirrorNode} from '@tiptap/pm/model'
import {Plugin, PluginKey} from '@tiptap/pm/state'
import type {Transaction} from '@tiptap/pm/state'
import {canJoin} from '@tiptap/pm/transform'

const joinableListTypes = new Set(['orderedList', 'bulletList', 'taskList'])

function hasMatchingListAttributes(left: ProseMirrorNode, right: ProseMirrorNode) {
  if (left.type.name !== 'orderedList') return true
  return left.attrs.type === right.attrs.type
}

function findAdjacentListJoin(doc: ProseMirrorNode) {
  let joinPos: number | null = null

  doc.descendants((node, pos, parent, index) => {
    if (joinPos !== null || !parent) return false

    const next = parent.maybeChild(index + 1)
    if (
      next &&
      joinableListTypes.has(node.type.name) &&
      node.type === next.type &&
      hasMatchingListAttributes(node, next)
    ) {
      joinPos = pos + node.nodeSize
      return false
    }

    return true
  })

  return joinPos
}

function normalizeAdjacentLists(tr: Transaction) {
  for (;;) {
    const joinPos = findAdjacentListJoin(tr.doc)
    if (joinPos === null || !canJoin(tr.doc, joinPos)) break
    tr.join(joinPos)
  }

  return tr
}

export const ListNormalization = Extension.create({
  name: 'listNormalization',

  addProseMirrorPlugins() {
    return [
      new Plugin({
        key: new PluginKey('listNormalization'),
        appendTransaction: (transactions, _oldState, newState) => {
          if (!transactions.some((transaction) => transaction.docChanged)) return null

          const tr = normalizeAdjacentLists(newState.tr)
          return tr.docChanged ? tr : null
        },
      }),
    ]
  },
})
