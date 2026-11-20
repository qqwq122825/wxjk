import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import { createRenderer, nextTick, reactive } from 'vue'

// Render the actual SFC with synthetic frames and an in-memory renderer.
// No browser, account, real image or server request is used.
const source = fs.readFileSync(new URL('./DeviceDetail.vue', import.meta.url), 'utf8')
const instrumented = source.replace('</script>', 'defineExpose({currentFrame,visibleFrame,activeImageURL,imageFailed,refresh,selectFrame,followLatest})\n</script>')
const { descriptor } = parse(instrumented)
let code = compileScript(descriptor, { id: 'image-preview-test', inlineTemplate: true }).content
for (const name of ['vue', '@lucide/vue']) code = code.replaceAll(JSON.stringify(name), JSON.stringify(import.meta.resolve(name))).replaceAll(`'${name}'`, JSON.stringify(import.meta.resolve(name)))
for (const name of ['./mapURL.js', './imagePreview.js']) code = code.replaceAll(`'${name}'`, JSON.stringify(new URL(name, import.meta.url).href))
const Component = (await import('data:text/javascript;base64,' + Buffer.from(code).toString('base64'))).default
function node(tag, text = '') {
  const n = { tag, tagName:tag.toUpperCase(), text, children: [], props: {}, parent: null, style: {}, selectedIndex:-1, addEventListener(){}, removeEventListener(){}, focus() {}, close() {}, showModal() {}, getRootNode() { return {} }, setAttribute(key,value){this.props[key]=value},removeAttribute(key){delete this.props[key]} }
  Object.defineProperty(n,'options',{get:()=>all(n,child=>child.tag==='option')});return n
}
function all(n, predicate) { return [...(predicate(n) ? [n] : []), ...n.children.flatMap(child => all(child, predicate))] }
const renderer = createRenderer({
  createElement: tag => node(tag), createText: text => node('text', text), createComment: text => node('comment', text),
  setText(n, text) { n.text = text }, setElementText(n, text) { n.text = text; n.children = [] },
  patchProp(n, key, old, value) { n.props[key] = value },
  insert(n, parent, anchor = null) { if (n.parent) n.parent.children.splice(n.parent.children.indexOf(n), 1); n.parent = parent; const index = parent.children.indexOf(anchor); parent.children.splice(index < 0 ? parent.children.length : index, 0, n) },
  remove(n) { if (n.parent) n.parent.children.splice(n.parent.children.indexOf(n), 1); n.parent = null },
  parentNode: n => n.parent, nextSibling: n => n.parent?.children[n.parent.children.indexOf(n) + 1] || null,
  insertStaticContent(content, parent, anchor) { const n = node('static', content); n.parent = parent; const index = parent.children.indexOf(anchor); parent.children.splice(index < 0 ? parent.children.length : index, 0, n); return [n, n] }
})
const frame = (id, second = 1) => ({ id, device_id: 'dev_synthetic', width: 320, height: 640, captured_at: `2026-11-20T01:02:${String(second).padStart(2, '0')}Z`, format: 'jpg', content_type: 'image/jpeg' })
const text = n => n.tag === 'comment' ? '' : n.text + n.children.map(text).join('')
async function flush() { await nextTick(); await new Promise(resolve => setImmediate(resolve)); await nextTick() }
async function fixture() {
  const saved = { fetch: globalThis.fetch, Image: globalThis.Image, create: URL.createObjectURL, revoke: URL.revokeObjectURL, document:globalThis.document, Document:globalThis.Document, ShadowRoot:globalThis.ShadowRoot }
  const requests = [], revoked = []; let counter = 0
  globalThis.document={activeElement:null};globalThis.Document=class {};globalThis.ShadowRoot=class {}
  globalThis.fetch = (url, options) => new Promise((resolve, reject) => requests.push({ url, options, reject, resolve() { resolve({ ok: true, blob: async () => new Blob(['synthetic'], { type: 'image/jpeg' }) }) } }))
  globalThis.Image = class { decode() { return Promise.resolve() } }
  URL.createObjectURL = () => 'blob:synthetic-' + ++counter; URL.revokeObjectURL = url => revoked.push(url)
  const device = reactive({ id: 'dev_synthetic', name: 'synthetic', enabled: true, assignment_id: 'assign_one', assignment_version: 1, latest_image: frame('img_one') })
  const host = node('root'), app = renderer.createApp(Component, { device, images: [] }), vm = app.mount(host)
  await flush()
  const image = () => all(host, n => n.tag === 'img' && n.props.alt === '当前显示的设备窗口图片')[0]
  return { device, host, vm, requests, image, revoked, async close() { app.unmount(); await flush(); globalThis.fetch = saved.fetch; globalThis.Image = saved.Image; URL.createObjectURL = saved.create; URL.revokeObjectURL = saved.revoke;globalThis.document=saved.document;globalThis.Document=saved.Document;globalThis.ShadowRoot=saved.ShadowRoot } }
}

test('real SFC preserves the same image node and old timestamp through a failed replacement', async () => {
  const f = await fixture()
  try {
    f.requests[0].resolve(); await flush(); const originalImage = f.image(), timestamp = f.vm.visibleFrame.captured_at
    assert.equal(originalImage.props.src, 'blob:synthetic-1')
    f.device.latest_image = frame('img_bad', 2); await flush()
    assert.equal(f.image(), originalImage); assert.equal(f.vm.visibleFrame.captured_at, timestamp)
    f.requests[1].reject(new Error('synthetic 404')); await flush()
    assert.equal(f.image(), originalImage); assert.equal(f.vm.visibleFrame.id, 'img_one'); assert.equal(f.vm.imageFailed, true)
    assert.ok(text(f.host).includes('仍显示上一张可用画面')); assert.ok(!text(f.host).includes('图片暂时无法读取请刷新'))
    assert.ok(text(f.host).includes('320 × 640 · JPG'))
  } finally { await f.close() }
})

test('real SFC assignment reset immediately removes the previous image and rejects late responses', async () => {
  const f = await fixture()
  try {
    f.requests[0].resolve(); await flush(); assert.ok(f.image())
    f.device.latest_image = frame('img_pending', 3); await flush()
    f.device.assignment_version = 2; await flush()
    assert.equal(f.vm.visibleFrame, null); assert.equal(f.image(), undefined); assert.equal(f.requests[1].options.signal.aborted, true)
    f.requests[1].resolve(); await flush(); assert.equal(f.vm.visibleFrame, null)
    f.requests[2].resolve(); await flush(); assert.equal(f.vm.visibleFrame.id, 'img_pending')
  } finally { await f.close() }
})
