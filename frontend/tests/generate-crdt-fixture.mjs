// Run from any directory with `node frontend/tests/generate-crdt-fixture.mjs`.
import {writeFile} from 'node:fs/promises'
import * as Y from 'yjs'
import {getSchema, Node} from '@tiptap/core'
import StarterKit from '@tiptap/starter-kit'
import {prosemirrorJSONToYXmlFragment, yDocToProsemirrorJSON} from 'y-prosemirror'

const schema = getSchema([StarterKit, Node.create({
  name: 'attachmentImage', group: 'block', atom: true,
  addAttributes() { return {attachmentId: {default: ''}} },
})])
const doc = new Y.Doc()
doc.clientID = 42
const root = doc.getXmlFragment('default')
prosemirrorJSONToYXmlFragment(schema, {type: 'doc', content: [
  {type: 'paragraph', content: [{type: 'text', text: 'Before replay'}]},
  {type: 'attachmentImage', attrs: {attachmentId: 'snapshot-image'}},
]}, root)
const b64 = bytes => Buffer.from(bytes).toString('base64')
const snapshot = b64(Y.encodeStateAsUpdate(doc))
const content = yDocToProsemirrorJSON(doc, 'default')
const updates = []
doc.on('update', update => updates.push(update))
doc.transact(() => {
  root.get(0).get(0).insert(13, ' plus recovered edits')
  const image = new Y.XmlElement('attachmentImage')
  image.setAttribute('attachmentId', 'pending-image')
  root.insert(root.length, [image])
})
await writeFile(new URL('../../testdata/crdt-document.json', import.meta.url), JSON.stringify({
  snapshot, update: b64(Y.mergeUpdates(updates)), fullSnapshot: b64(Y.encodeStateAsUpdate(doc)),
  content, recoveredContent: yDocToProsemirrorJSON(doc, 'default'),
}, null, 2) + '\n')
doc.destroy()
