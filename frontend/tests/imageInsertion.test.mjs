import assert from 'node:assert/strict'
import {readFile} from 'node:fs/promises'
import test from 'node:test'
import ts from 'typescript'

const source = await readFile(new URL('../src/editor/imageInsertion.ts', import.meta.url), 'utf8')
const {outputText} = ts.transpileModule(source, {compilerOptions: {target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022}})
const {insertImageAttachments} = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)
const files = [{name: 'first.png'}, {name: 'second.png'}]
function editor() {
  const calls = []
  const instance = {
    isDestroyed: false, isEditable: true,
    state: {doc: {content: {size: 10}}},
    calls,
    chain() {
      assert.equal(this.isDestroyed, false, 'must not access a destroyed command manager')
      return {
        focus() { return this },
        setTextSelection(position) { calls.push(['position', position]); return this },
        insertContent(content) { calls.push(['insert', content.attrs]); return this },
        run() { return true },
      }
    },
  }
  return instance
}
const upload = async file => ({id: file.name, originalName: file.name})

test('a retained drop handler resolves the replacement editor at invocation', async () => {
  const oldEditor = editor(), replacement = editor()
  const ref = {current: oldEditor}
  const errors = []
  const handler = () => insertImageAttachments(files, {getEditor: () => ref.current, upload, onError: e => errors.push(e)}, 3)
  oldEditor.isDestroyed = true
  ref.current = replacement
  await handler()
  assert.deepEqual(oldEditor.calls, [])
  assert.deepEqual(replacement.calls.filter(c => c[0] === 'position'), [['position',3],['position',4]])
  assert.equal(replacement.calls.filter(c => c[0] === 'insert').length, 2)
  assert.deepEqual(errors, [])
})

for (const change of ['destroyed', 'replaced', 'unmounted', 'read-only']) {
  test(`image loading cancels when its editor is ${change}`, async () => {
    const target = editor(), replacement = editor()
    let current = target, uploads = 0
    await insertImageAttachments(files, {
      getEditor: () => current,
      upload: async () => {
        uploads += 1
        if (change === 'destroyed') target.isDestroyed = true
        if (change === 'replaced') current = replacement
        if (change === 'unmounted') current = null
        if (change === 'read-only') target.isEditable = false
        return {id: 'image', originalName: 'image.png'}
      },
      onError: assert.fail,
    })
    assert.equal(uploads, 1)
    assert.deepEqual(target.calls, [])
    assert.deepEqual(replacement.calls, [])
  })
}

test('upload failures are reported without calling editor commands', async () => {
  const target = editor(), errors = []
  await insertImageAttachments([files[0]], {
    getEditor: () => target,
    upload: async () => { throw new Error('attachment failed') },
    onError: error => errors.push(error.message),
  })
  assert.deepEqual(errors, ['attachment failed'])
  assert.deepEqual(target.calls, [])
})

test('an editor destroyed before the drop does not start an upload', async () => {
  const target = editor()
  target.isDestroyed = true
  await insertImageAttachments(files, {getEditor: () => target, upload: assert.fail, onError: assert.fail})
  assert.deepEqual(target.calls, [])
})

test('a drop position remains valid if the document shrinks during upload', async () => {
  const target = editor()
  await insertImageAttachments([files[0]], {
    getEditor: () => target,
    upload: async file => { target.state.doc.content.size = 2; return upload(file) },
    onError: assert.fail,
  }, 8)
  assert.deepEqual(target.calls[0], ['position',2])
})
