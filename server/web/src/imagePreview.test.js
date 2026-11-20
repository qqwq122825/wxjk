import assert from 'node:assert/strict'
import test from 'node:test'
import { createImagePreview, imageFormat, loadPreviewImage } from './imagePreview.js'

const flush = () => new Promise(resolve => setImmediate(resolve))
function fixture(options = {}) {
  const jobs = [], shown = [], errors = [], disposed = []
  const preview = createImagePreview({
    load(url, signal) {
      return new Promise((resolve, reject) => jobs.push({ url, signal, reject, resolve() { resolve({ url: 'blob:' + url, contentType: 'image/jpeg', dispose() { disposed.push(url) } }) } }))
    },
    onChange(value) { shown.push(value) }, onError(error) { errors.push(error?.message || null) }, ...options
  })
  const request = (id, scope) => preview.request({ id, captured_at: id, width: 10, height: 20 }, '/' + id, scope)
  return { preview, request, jobs, shown, errors, disposed }
}

test('replacement waits for download and decode; visible metadata follows the decoded frame', async () => {
  const f = fixture()
  f.request('one'); assert.equal(f.shown.length, 0)
  f.jobs[0].resolve(); await flush()
  f.request('two'); assert.equal(f.shown.at(-1).frame.id, 'one')
  assert.equal(f.shown.at(-1).frame.captured_at, 'one'); assert.deepEqual(f.disposed, [])
  f.jobs[1].resolve(); await flush()
  assert.equal(f.shown.at(-1).frame.id, 'two'); assert.equal(f.shown.at(-1).frame.content_type, 'image/jpeg')
  assert.deepEqual(f.disposed, ['/one']); f.preview.dispose()
})

test('a failed newer image preserves the last good image instead of showing a blank', async () => {
  const f = fixture()
  f.request('good'); f.jobs[0].resolve(); await flush()
  f.request('missing'); f.jobs[1].reject(new Error('Image request failed (404)')); await flush()
  assert.equal(f.shown.at(-1).frame.id, 'good'); assert.deepEqual(f.disposed, [])
  assert.equal(f.errors.at(-1), 'Image request failed (404)'); f.preview.dispose()
})

test('rapid live updates coalesce without aborting the running download or starving rendering', async () => {
  const f = fixture()
  f.request('one'); f.request('two'); f.request('three')
  assert.equal(f.jobs.length, 1); assert.equal(f.jobs[0].signal.aborted, false)
  f.jobs[0].resolve(); await flush()
  assert.equal(f.shown.at(-1).frame.id, 'one'); assert.equal(f.jobs.length, 2); assert.equal(f.jobs[1].url, '/three')
  f.jobs[1].resolve(); await flush(); assert.equal(f.shown.at(-1).frame.id, 'three'); f.preview.dispose()
})

test('history selection rejects late live results and only displays the selected history on success', async () => {
  const f = fixture()
  f.request('good'); f.jobs[0].resolve(); await flush()
  f.request('live'); f.request('history', 'history:history')
  assert.equal(f.jobs[1].signal.aborted, true)
  f.jobs[1].resolve(); await flush(); assert.equal(f.shown.at(-1).frame.id, 'good')
  assert.ok(f.disposed.includes('/live'))
  f.jobs[2].resolve(); await flush(); assert.equal(f.shown.at(-1).frame.id, 'history'); f.preview.dispose()
})

test('device/assignment reset clears the old frame and ignores late previous-scope responses', async () => {
  const f = fixture()
  f.request('private-one'); f.jobs[0].resolve(); await flush()
  f.request('private-pending'); f.preview.reset()
  assert.equal(f.shown.at(-1), null); assert.equal(f.jobs[1].signal.aborted, true)
  f.request('other-device'); f.jobs[1].resolve(); await flush()
  assert.equal(f.shown.at(-1), null)
  f.jobs[2].resolve(); await flush(); assert.equal(f.shown.at(-1).frame.id, 'other-device')
  assert.ok(f.disposed.includes('/private-one')); assert.ok(f.disposed.includes('/private-pending')); f.preview.dispose()
})

test('unmount disposal prevents later rendering and releases object URLs', async () => {
  const f = fixture()
  f.request('one'); f.jobs[0].resolve(); await flush()
  f.request('two'); f.preview.dispose(); f.jobs[1].resolve(); await flush()
  assert.equal(f.shown.at(-1), null); assert.deepEqual(f.disposed.sort(), ['/one', '/two'])
  f.request('three'); assert.equal(f.jobs.length, 2)
})

test('a hung download times out, keeps the old image and advances to the queued latest frame', async () => {
  const f = fixture({ timeoutMs: 15 })
  f.request('good'); f.jobs[0].resolve(); await flush()
  f.request('hung'); f.request('latest')
  await new Promise(resolve => setTimeout(resolve, 25))
  assert.equal(f.jobs[1].signal.aborted, true); assert.equal(f.errors.at(-1), 'Image request timed out')
  assert.equal(f.shown.at(-1).frame.id, 'good'); assert.equal(f.jobs[2].url, '/latest')
  f.jobs[1].resolve(); await flush(); assert.ok(f.disposed.includes('/hung'))
  f.jobs[2].resolve(); await flush(); assert.equal(f.shown.at(-1).frame.id, 'latest'); f.preview.dispose()
})

test('actual loader awaits browser decoding and uses same-origin authenticated blob URLs', async () => {
  const saved = { fetch: globalThis.fetch, Image: globalThis.Image, create: URL.createObjectURL, revoke: URL.revokeObjectURL }
  const calls = [], revoked = []; let decoded
  globalThis.fetch = async (url, options) => { calls.push({ url, options }); return { ok: true, blob: async () => new Blob(['synthetic'], { type: 'image/jpeg' }) } }
  globalThis.Image = class { decode() { return new Promise(resolve => { decoded = resolve }) } }
  URL.createObjectURL = () => 'blob:synthetic'; URL.revokeObjectURL = url => revoked.push(url)
  try {
    let complete = false
    const controller = new AbortController(), pending = loadPreviewImage('/api/v1/images/synthetic/file', controller.signal).then(value => { complete = true; return value })
    await flush(); assert.equal(complete, false); decoded()
    const value = await pending
    assert.equal(value.url, 'blob:synthetic'); assert.equal(value.contentType, 'image/jpeg')
    assert.equal(calls[0].options.credentials, 'same-origin'); assert.equal(calls[0].options.cache, 'no-store')
    value.dispose(); assert.deepEqual(revoked, ['blob:synthetic'])
  } finally { globalThis.fetch = saved.fetch; globalThis.Image = saved.Image; URL.createObjectURL = saved.create; URL.revokeObjectURL = saved.revoke }
})

test('format labels and download extension distinguish new JPG and legacy PNG', () => {
  assert.equal(imageFormat({ format: 'jpg' }), 'JPG'); assert.equal(imageFormat({ format: 'jpeg' }), 'JPG')
  assert.equal(imageFormat({ content_type: 'image/jpeg' }), 'JPG'); assert.equal(imageFormat({ format: 'png' }), 'PNG')
  assert.equal(imageFormat({ content_type: 'image/png' }), 'PNG'); assert.equal(imageFormat({}), 'PNG')
})
