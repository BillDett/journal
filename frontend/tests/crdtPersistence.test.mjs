import assert from 'node:assert/strict'
import {readFile} from 'node:fs/promises'
import test from 'node:test'
import ts from 'typescript'
import * as Y from 'yjs'
import {yDocToProsemirrorJSON} from 'y-prosemirror'

// Use the project's compiler so these tests also run on Node versions without
// built-in TypeScript support. These modules have no browser dependencies.
async function loadTS(path) {
  const source = await readFile(new URL(path, import.meta.url), 'utf8')
  const {outputText} = ts.transpileModule(source, {compilerOptions: {target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022}})
  return import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)
}
const {CRDTPersistence} = await loadTS('../src/editor/crdtPersistence.ts')
const {flushBeforeTransition} = await loadTS('../src/operations.ts')
const tick = () => new Promise(setImmediate)
function deferred() {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return {promise, resolve, reject}
}
function fixture(t, overrides = {}) {
  const saved = [], projected = [], submitted = [], errors = []
  let content = 'original', seq = 4
  const queue = new CRDTPersistence({
    throughSeq: seq,
    submit: async updates => { submitted.push(updates); return {throughSeq: ++seq} },
    capture: () => ({content, snapshot: content, stateVector: content}),
    materialize: async p => { projected.push(p) },
    onSaved: c => saved.push(c),
    onError: e => errors.push(e),
    ...overrides,
  })
  t.after(() => queue.dispose())
  return {queue, saved, projected, submitted, errors, edit(text) { content = text; queue.enqueue({id: text, data: text}) }}
}

test('Settings waits for an in-flight save before destroying the editor', async t => {
  const request = deferred()
  const f = fixture(t, {submit: () => request.promise})
  f.edit('latest')
  const writing = f.queue.persist()
  await tick()
  let settingsOpen = false
  const transition = flushBeforeTransition(
    async () => { await f.queue.flush(); return true },
    () => { settingsOpen = true; f.queue.dispose() },
  )
  await tick()
  assert.equal(settingsOpen, false)
  assert.deepEqual(f.saved, [])
  assert.deepEqual(f.projected, [])
  request.resolve({throughSeq: 5})
  await Promise.all([writing, transition])
  assert.equal(settingsOpen, true)
  assert.equal(f.projected[0].content, 'latest')
  assert.equal(f.projected[0].throughSeq, 5)
  assert.deepEqual(f.saved, ['latest'])
})

test('failed save keeps Settings closed and retry preserves update IDs', async t => {
  const attempts = []
  let fail = true
  const f = fixture(t, {submit: async updates => {
    attempts.push(updates.map(u => u.id))
    if (fail) throw new Error('disk full')
    return {throughSeq: 5}
  }})
  f.edit('pending')
  let settingsOpen = false
  const flush = async () => { try { await f.queue.flush(); return true } catch { return false } }
  assert.equal(await flushBeforeTransition(flush, () => { settingsOpen = true }), false)
  assert.equal(settingsOpen, false)
  assert.deepEqual(f.saved, [])
  fail = false
  assert.equal(await flushBeforeTransition(flush, () => { settingsOpen = true }), true)
  assert.equal(settingsOpen, true)
  assert.deepEqual(attempts, [['pending'], ['pending']])
})

test('an earlier failed in-flight submission is retried by a queued flush', async t => {
  const request = deferred()
  const attempts = []
  const f = fixture(t, {submit: updates => {
    attempts.push(updates.map(u => u.id))
    return attempts.length === 1 ? request.promise : Promise.resolve({throughSeq: 5})
  }})
  f.edit('pending')
  const writing = f.queue.persist()
  const failed = assert.rejects(writing, /offline/)
  await tick()
  const flushed = f.queue.flush()
  request.reject(new Error('offline'))
  await Promise.all([failed, flushed])
  assert.deepEqual(attempts, [['pending'], ['pending']])
  assert.deepEqual(f.saved, ['pending'])
})

test('edits arriving during IPC get their own acknowledged snapshot', async t => {
  const firstProjection = deferred()
  const projections = []
  const f = fixture(t, {materialize: p => {
    projections.push(p)
    return projections.length === 1 ? firstProjection.promise : Promise.resolve()
  }})
  f.edit('first')
  const flushed = f.queue.flush()
  await tick()
  f.edit('second')
  assert.deepEqual(f.saved, [])
  firstProjection.resolve()
  await flushed
  assert.deepEqual(projections.map(p => [p.content,p.snapshot,p.throughSeq]), [['first','first',5], ['second','second',6]])
  assert.deepEqual(f.saved, ['second'])
})

test('concurrent flushes never overlap their materializations', async t => {
  const firstProjection = deferred()
  let calls = 0
  const f = fixture(t, {materialize: () => ++calls === 1 ? firstProjection.promise : Promise.resolve()})
  f.edit('latest')
  const first = f.queue.flush()
  const second = f.queue.flush()
  await tick()
  assert.equal(calls, 1)
  firstProjection.resolve()
  await Promise.all([first, second])
  assert.equal(calls, 2)
})

test('continuous typing persists within 80 ms independently of projection debounce', async t => {
  t.mock.timers.enable({apis: ['setTimeout']})
  const f = fixture(t)
  f.edit('a')
  f.queue.scheduleProjection(2000)
  t.mock.timers.tick(40)
  f.edit('ab')
  f.queue.scheduleProjection(2000)
  t.mock.timers.tick(40)
  await tick()
  assert.deepEqual(f.submitted.map(batch => batch.map(u => u.id)), [['a','ab']])
  assert.deepEqual(f.projected, [])
  f.edit('abc')
  f.queue.scheduleProjection(2000)
  t.mock.timers.tick(80)
  await tick()
  assert.equal(f.submitted.length, 2)
  t.mock.timers.tick(1919)
  await tick()
  assert.deepEqual(f.projected, [])
  t.mock.timers.tick(1)
  await tick()
  assert.deepEqual(f.saved, ['abc'])
})

test('flush uses the frontier loaded from a compacted session without new edits', async t => {
  const f = fixture(t)
  await f.queue.flush()
  assert.equal(f.projected[0].throughSeq, 4)
  assert.deepEqual(f.submitted, [])
})

test('shared Go fixture replays actual Yjs edits and image references', async () => {
  const f = JSON.parse(await readFile(new URL('../../testdata/crdt-document.json', import.meta.url), 'utf8'))
  const doc = new Y.Doc()
  try {
    Y.applyUpdate(doc, Buffer.from(f.snapshot, 'base64'))
    assert.deepEqual(yDocToProsemirrorJSON(doc, 'default'), f.content)
    Y.applyUpdate(doc, Buffer.from(f.update, 'base64'))
    assert.deepEqual(yDocToProsemirrorJSON(doc, 'default'), f.recoveredContent)
    assert.equal(f.recoveredContent.content.length, 3)
    assert.equal(f.recoveredContent.content[2].attrs.attachmentId, 'pending-image')
    // Retries stay idempotent, including a replay after snapshot compaction.
    Y.applyUpdate(doc, Buffer.from(f.fullSnapshot, 'base64'))
    Y.applyUpdate(doc, Buffer.from(f.update, 'base64'))
    assert.deepEqual(yDocToProsemirrorJSON(doc, 'default'), f.recoveredContent)
  } finally { doc.destroy() }
})
