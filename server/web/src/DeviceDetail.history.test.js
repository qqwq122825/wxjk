import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import { createRenderer, h, nextTick, reactive, shallowRef } from 'vue'

// Render the actual SFC with synthetic frames and an in-memory renderer.
// No browser, account, real image or server request is used.
const source = fs.readFileSync(new URL('./DeviceDetail.vue', import.meta.url), 'utf8')
const instrumented = source.replace('</script>', 'defineExpose({currentFrame,visibleFrame,activeImageURL,selectFrame,frameCounter,historyTotal})\n</script>')
const { descriptor } = parse(instrumented)
let code = compileScript(descriptor, { id: 'image-history-test', inlineTemplate: true }).content
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
const frame = index => ({ id:'img_history_'+index, device_id:'dev_history', width:320, height:640, captured_at:new Date(Date.UTC(2026,10,22,5,0,index)).toISOString(), format:'jpg',content_type:'image/jpeg' })
const text = n => n.tag === 'comment' ? '' : n.text + n.children.map(text).join('')
async function flush() { await nextTick(); await new Promise(resolve => setImmediate(resolve)); await nextTick() }
async function fixture(page=1){
  const saved={fetch:globalThis.fetch,Image:globalThis.Image,create:URL.createObjectURL,revoke:URL.revokeObjectURL,document:globalThis.document,Document:globalThis.Document,ShadowRoot:globalThis.ShadowRoot}
  const requests=[],revoked=[],events=[];let sequence=0
  globalThis.document={activeElement:null};globalThis.Document=class{};globalThis.ShadowRoot=class{}
  globalThis.fetch=(url,options)=>new Promise((resolve,reject)=>requests.push({url,options,reject,resolve(){resolve({ok:true,blob:async()=>new Blob(['synthetic'],{type:'image/jpeg'})})}}))
  globalThis.Image=class{decode(){return Promise.resolve()}}
  URL.createObjectURL=()=>`blob:history-${++sequence}`;URL.revokeObjectURL=url=>revoked.push(url)
  const props=reactive({device:{id:'dev_history',name:'synthetic',enabled:true,assignment_id:'assign_one',assignment_version:1,latest_image:frame(100)},images:Array.from({length:14},(_,i)=>frame((page-1)*14+i+1)),imagePagination:{page,page_size:14,total:61,total_pages:5},historyBusy:false,clearHistoryBusy:false,historyRevision:0,onHistoryPage:page=>events.push(['page',page]),onClearHistory:()=>events.push(['clear'])})
  const host=node('root'),child=shallowRef(null),app=renderer.createApp({setup:()=>()=>h(Component,{...props,ref:child})});app.mount(host);await flush()
  const section=()=>all(host,n=>n.props['aria-label']==='图片历史')[0]
  const thumbnails=()=>all(section(),n=>n.tag==='button'&&String(n.props['aria-label']).startsWith('查看第 '))
  const button=label=>all(host,n=>n.tag==='button'&&n.props['aria-label']===label)[0]
  return{props,host,requests,revoked,events,vm:child.value,section,thumbnails,button,async close(){app.unmount();await flush();globalThis.fetch=saved.fetch;globalThis.Image=saved.Image;URL.createObjectURL=saved.create;URL.revokeObjectURL=saved.revoke;globalThis.document=saved.document;globalThis.Document=saved.Document;globalThis.ShadowRoot=saved.ShadowRoot}}
}

test('history page uses exactly the API page and global numbers without prepending live latest',async()=>{
  const f=await fixture(2)
  try{
    assert.equal(f.thumbnails().length,14)
    assert.ok(f.thumbnails()[0].props['aria-label'].startsWith('查看第 15 张'))
    assert.ok(f.thumbnails().at(-1).props['aria-label'].startsWith('查看第 28 张'))
    assert.ok(text(f.section()).includes('61 张'))
    assert.ok(text(f.section()).includes('第 2 / 5 页'))
    assert.equal(f.vm.frameCounter,'最新 / 61')
    assert.equal(f.button('较早一张').props.disabled,true)
    assert.equal(f.button('较新一张').props.disabled,true)
    assert.equal(f.vm.currentFrame.id,'img_history_100')
    f.requests[0].resolve();await flush();assert.equal(f.vm.visibleFrame.id,'img_history_100')
  }finally{await f.close()}
})

test('page controls emit requested page and clear only emits intent, never deletion',async()=>{
  const f=await fixture()
  try{
    assert.equal(f.button('上一页图片').props.disabled,true)
    f.button('下一页图片').props.onClick();f.button('一键清空当前设备图片').props.onClick();await flush()
    assert.deepEqual(f.events,[['page',2],['clear']])
    assert.equal(f.props.images.length,14)
    assert.equal(f.requests.length,1)
    assert.ok(f.requests.every(r=>r.options.method!=='DELETE'))
    f.props.historyBusy=true;await flush()
    assert.equal(f.section().props['aria-busy'],true)
    assert.equal(f.button('下一页图片').props.disabled,true)
    assert.equal(f.button('一键清空当前设备图片').props.disabled,true)
    f.button('下一页图片').props.onClick();f.button('一键清空当前设备图片').props.onClick()
    assert.deepEqual(f.events,[['page',2],['clear']])
    f.props.historyBusy=false;f.props.clearHistoryBusy=true;await flush()
    assert.ok(text(f.section()).includes('正在清空…'))
    assert.equal(f.button('一键清空当前设备图片').props.disabled,true)
  }finally{await f.close()}
})

test('history revision clears selected decoded blob and aborts late deleted-frame work',async()=>{
  const f=await fixture()
  try{
    f.requests[0].resolve();await flush();assert.equal(f.vm.visibleFrame.id,'img_history_100')
    f.vm.selectFrame(f.props.images[3]);await flush();const pending=f.requests.at(-1)
    f.props.images=[];f.props.device.latest_image=null;f.props.imagePagination.total=0;f.props.imagePagination.total_pages=0;f.props.historyRevision++;await flush()
    assert.equal(f.vm.currentFrame,undefined)
    assert.equal(f.vm.visibleFrame,null)
    assert.equal(f.vm.activeImageURL,'')
    assert.equal(pending.options.signal.aborted,true)
    assert.ok(f.revoked.includes('blob:history-1'))
    assert.equal(f.button('一键清空当前设备图片').props.disabled,true)
    assert.ok(text(f.section()).includes('第 1 / 1 页'))
    pending.resolve();await flush();assert.equal(f.vm.visibleFrame,null)
    f.props.device.latest_image=frame(101);await flush();f.requests.at(-1).resolve();await flush()
    assert.equal(f.vm.visibleFrame.id,'img_history_101')
  }finally{await f.close()}
})

test('page updates preserve decoded live preview and API zero total is not stale page length',async()=>{
  const f=await fixture()
  try{
    f.requests[0].resolve();await flush();const shown=f.vm.activeImageURL
    f.props.imagePagination.page=3;f.props.images=Array.from({length:14},(_,i)=>frame(29+i));await flush()
    assert.equal(f.vm.activeImageURL,shown)
    assert.equal(f.requests.length,1)
    assert.ok(f.thumbnails()[0].props['aria-label'].startsWith('查看第 29 张'))
    f.props.imagePagination.total=0;await flush();assert.equal(f.vm.historyTotal,0)
    assert.equal(f.button('一键清空当前设备图片').props.disabled,true)
  }finally{await f.close()}
})
