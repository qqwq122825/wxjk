import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import { createRenderer, nextTick } from 'vue'

// Exercise the real App SFC with a local in-memory Vue renderer and fake server.
// No account, credentials, device data, browser session or network is used.
const original = fs.readFileSync(new URL('./App.vue', import.meta.url), 'utf8')
const exposed = 'imagePagination,historyBusy,clearHistoryBusy,historyRevision,loadHistoryPage,openClearImageHistory,toast,confirmCallback,user,login,modal,form,csrf,needsSetup,ready,isAdmin,requiresPasswordChange,passwordMinLength,passwordMaxLength,passwordPattern,modalError,randomPassword,agentPasswordOK,openModal,closeModal,modalSubmit,submitLogin,loadData,syncRoute,nav,selectedID,tabs,total,devices,stats,clearSession,acceptSession,logout,loading,detail,history,refreshDetail,api'
const instrumented = original.replace('</script>', `defineExpose({${exposed}})\n</script>`)
const { descriptor } = parse(instrumented)
let code = compileScript(descriptor, { id: 'history-pagination-test', inlineTemplate: true }).content
code = code.replaceAll('"vue"', JSON.stringify(import.meta.resolve('vue'))).replaceAll("'vue'", JSON.stringify(import.meta.resolve('vue')))
  .replace("import DeviceDetail from './DeviceDetail.vue'", 'const DeviceDetail = {render(){return null}}')
  .replace("'@lucide/vue'", JSON.stringify(import.meta.resolve('@lucide/vue')))
const Component = (await import('data:text/javascript;base64,' + Buffer.from(code).toString('base64'))).default

function node(tag, text = '') {
  const n = {
    tag, tagName: tag.toUpperCase(), text, children: [], props: {}, parent: null,
    style: { display: '' }, open: false, value: '', selectedIndex: -1,
    addEventListener() {}, removeEventListener() {}, focus() {}, getRootNode() { return {} },
    setAttribute(key, value) { this.props[key] = value }, removeAttribute(key) { delete this.props[key] },
    getAttribute(key) { return this.props[key] },
    showModal() { this.open = true }, close() { this.open = false }
  }
  Object.defineProperty(n, 'options', { get: () => all(n, child => child.tag === 'option') })
  return n
}
function all(n, predicate) { return [...(predicate(n) ? [n] : []), ...n.children.flatMap(child => all(child, predicate))] }
const renderer = createRenderer({
  createElement: tag => node(tag), createText: text => node('text', text), createComment: text => node('comment', text),
  setText(n, text) { n.text = text }, setElementText(n, text) { n.text = text; n.children = [] },
  patchProp(n, key, previous, next) { n.props[key] = next; if (['type', 'value', 'checked', 'multiple'].includes(key)) n[key] = next },
  insert(n, parent, anchor = null) { if (n.parent) { const index = n.parent.children.indexOf(n); if (index >= 0) n.parent.children.splice(index, 1) } n.parent = parent; const index = parent.children.indexOf(anchor); parent.children.splice(index < 0 ? parent.children.length : index, 0, n) },
  remove(n) { if (n.parent) { const index = n.parent.children.indexOf(n); if (index >= 0) n.parent.children.splice(index, 1); n.parent = null } },
  parentNode: n => n.parent, nextSibling: n => n.parent?.children[n.parent.children.indexOf(n) + 1] || null,
  insertStaticContent(content, parent, anchor) { const n = node('static', content); n.parent = parent; const index = parent.children.indexOf(anchor); parent.children.splice(index < 0 ? parent.children.length : index, 0, n); return [n, n] }
})
const text = n => n.tag === 'comment' ? '' : n.text + n.children.map(text).join('')
const dialog = root => all(root, n => n.tag === 'dialog')[0]
const inputs = root => all(root, n => n.tag === 'input')
async function flush() { await nextTick(); await new Promise(resolve => setImmediate(resolve)); await nextTick() }

async function fixture({ role = 'agent', legacyFlag = true, authenticated = true, needsSetup = false, hash = '#/overview' } = {}) {
  const saved = { window: globalThis.window, document: globalThis.document, fetch: globalThis.fetch, Document: globalThis.Document, ShadowRoot: globalThis.ShadowRoot }
  const user = { id: 'usr_fixture', username: 'fixture', display_name: '测试账号', role, must_change_password: legacyFlag, enabled: true }
  const calls = [], win = { location: { protocol: 'https:', hash }, addEventListener() {}, removeEventListener() {}, scrollTo() {} }
  globalThis.window = win; globalThis.document = { title: '', visibilityState: 'hidden', activeElement: null }; globalThis.Document = class {}; globalThis.ShadowRoot = class {}
  globalThis.fetch = async (url, options = {}) => {
    calls.push({ url, method: options.method || 'GET', body: options.body ? JSON.parse(options.body) : undefined, headers: options.headers })
    let data, status = 200
    if (url === '/api/v1/auth/bootstrap') data = { needs_setup: needsSetup, requires_setup_token: false }
    else if (url === '/api/v1/auth/me') { status = authenticated ? 200 : 401; data = authenticated ? { user: { ...user }, csrf_token: 'synthetic-csrf' } : { error: { code: 'UNAUTHORIZED', message: 'synthetic' } } }
    else if (url === '/api/v1/auth/login' || url === '/api/v1/auth/setup') data = { user: { ...user }, csrf_token: 'synthetic-csrf' }
    else if (url === '/api/v1/auth/password') data = { user: { ...user, must_change_password: false } }
    else if (url === '/api/v1/dashboard') data = { total: 0, online: 0, unassigned: 0 }
    else if (url.startsWith('/api/v1/devices?') || url === '/api/v1/admin/agents') data = { items: [], total: 0 }
    else if (/\/images(?:\?|$)/.test(url)) {const requested=Number(new URL(url,'https://synthetic.test').searchParams.get('page')||1);data=pageData(requested)}
    else if (url.startsWith('/api/v1/devices/dev_')) data = deviceData()
    else if (url.startsWith('/api/v1/admin/agents/') && options.method === 'POST') data = { status: 'ok' }
    else throw new Error('Unexpected mock request: ' + url)
    return { ok: status < 400, status, json: async () => data }
  }
  const host = node('root'), app = renderer.createApp(Component), vm = app.mount(host)
  await flush()
  return { host, app, vm, calls, win, async close() { app.unmount(); await flush(); for (const [key, value] of Object.entries(saved)) { if (value === undefined) delete globalThis[key]; else globalThis[key] = value } } }
}


function response(data,status=200){return {ok:status<400,status,json:async()=>data}}
function deviceData(extra={}){return {id:'dev_fixture',name:'测试设备',enabled:true,assignment_id:'assign_fixture',assignment_version:1,latest_image:{id:'img_latest',device_id:'dev_fixture',width:40,height:60},session:{desired_running:true},...extra}}
function pageData(page,extra={}){return {items:Array.from({length:14},(_,i)=>({id:`img_${page}_${i}`,device_id:'dev_fixture',width:40,height:60})),page,page_size:14,total:42,total_pages:3,...extra}}
function pendingResponse(url,options,pending){return new Promise(resolve=>pending.push({url,options,resolve}))}

test('history requests explicit pages, keeps page two during refresh and excludes pagination state from live-detail polls',async()=>{
 const f=await fixture({hash:'#/devices/dev_fixture'})
 try{
  assert.ok(f.calls.some(c=>c.url==='/api/v1/devices/dev_fixture/images?page=1&page_size=14'))
  assert.equal(f.vm.history.length,14);assert.deepEqual(f.vm.imagePagination,{page:1,page_size:14,total:42,total_pages:3})
  await f.vm.loadHistoryPage(2);assert.equal(f.vm.imagePagination.page,2);assert.equal(f.vm.history[0].id,'img_2_0')
  const imageCalls=f.calls.filter(c=>c.url.includes('/images?')).length
  await f.vm.refreshDetail(true);assert.equal(f.calls.filter(c=>c.url.includes('/images?')).length,imageCalls);assert.equal(f.vm.imagePagination.page,2)
  await f.vm.refreshDetail();assert.equal(f.vm.imagePagination.page,2);assert.equal(f.vm.history[0].id,'img_2_0')
  assert.ok(f.calls.some(c=>c.url==='/api/v1/devices/dev_fixture/images?page=2&page_size=14'))
 }finally{await f.close()}
})

test('late page replies cannot replace the latest requested page',async()=>{
 const f=await fixture({hash:'#/devices/dev_fixture'}),originalFetch=globalThis.fetch,pending=[]
 try{
  globalThis.fetch=(url,options={})=>url.includes('/images?')?pendingResponse(url,options,pending):originalFetch(url,options)
  const two=f.vm.loadHistoryPage(2),three=f.vm.loadHistoryPage(3);assert.equal(pending.length,2)
  pending[1].resolve(response(pageData(3)));await three;assert.equal(f.vm.imagePagination.page,3)
  pending[0].resolve(response(pageData(2)));await two;assert.equal(f.vm.imagePagination.page,3);assert.equal(f.vm.history[0].id,'img_3_0');assert.equal(f.vm.historyBusy,false)
 }finally{globalThis.fetch=originalFetch;await f.close()}
})

test('assignment changes reset the page and discard replies from the previous assignment',async()=>{
 const f=await fixture({hash:'#/devices/dev_fixture'}),originalFetch=globalThis.fetch,pending=[]
 try{
  let holdOld=true
  globalThis.fetch=(url,options={})=>{
   if(url.includes('/images?')){if(holdOld)return pendingResponse(url,options,pending);return Promise.resolve(response(pageData(1,{items:[{id:'new_assignment_image',device_id:'dev_fixture'}]})))}
   if(url==='/api/v1/devices/dev_fixture')return Promise.resolve(response(deviceData({assignment_version:2})))
   return originalFetch(url,options)
  }
  const old=f.vm.loadHistoryPage(2);holdOld=false
  await f.vm.refreshDetail(true);assert.equal(f.vm.imagePagination.page,1);assert.equal(f.vm.history[0].id,'new_assignment_image')
  pending[0].resolve(response(pageData(2,{items:[{id:'obsolete_assignment_image'}]})));await old
  assert.equal(f.vm.history[0].id,'new_assignment_image');assert.equal(f.vm.detail.assignment_version,2)
 }finally{globalThis.fetch=originalFetch;await f.close()}
})

test('clear is confirmed, sends DELETE with CSRF, invalidates stale replies and resets preview before refreshing',async()=>{
 const f=await fixture({hash:'#/devices/dev_fixture'}),originalFetch=globalThis.fetch,oldPages=[],postclear=[]
 try{
  let deleting,cleared=false,holdPostClear=true
  globalThis.fetch=(url,options={})=>{
   if(url==='/api/v1/devices/dev_fixture/images'&&options.method==='DELETE'){assert.equal(options.headers['X-CSRF-Token'],'synthetic-csrf');return new Promise(resolve=>{deleting=resolve})}
   if(url.includes('/images?')){if(!cleared)return pendingResponse(url,options,oldPages);if(holdPostClear)return pendingResponse(url,options,postclear);return Promise.resolve(response(pageData(1,{items:[],total:0,total_pages:0})))}
   if(url==='/api/v1/devices/dev_fixture'&&cleared){if(holdPostClear)return pendingResponse(url,options,postclear);return Promise.resolve(response(deviceData({latest_image:null})))}
   return originalFetch(url,options)
  }
  const obsolete=f.vm.loadHistoryPage(2),revision=f.vm.historyRevision
  await f.vm.openClearImageHistory();assert.equal(f.vm.modal.type,'confirm');assert.match(f.vm.modal.message,/所有分页/);assert.match(f.vm.modal.message,/无法恢复/);assert.match(f.vm.modal.message,/继续产生新图片/);assert.equal(deleting,undefined)
  const clear=f.vm.modalSubmit();assert.equal(typeof deleting,'function');assert.equal(f.vm.clearHistoryBusy,true)
  cleared=true;deleting(response({cleared:57,files_removed:57,file_cleanup_failed:0}));await flush()
  assert.deepEqual(f.vm.history,[]);assert.equal(f.vm.detail.latest_image,null);assert.equal(f.vm.imagePagination.page,1);assert.ok(f.vm.historyRevision>revision);assert.equal(f.vm.detail.session.desired_running,true)
  oldPages[0].resolve(response(pageData(2)));await obsolete;assert.deepEqual(f.vm.history,[])
  holdPostClear=false
  for(const p of postclear)p.resolve(response(p.url.includes('/images?')?pageData(1,{items:[],total:0,total_pages:0}):deviceData({latest_image:null})))
  await clear;assert.equal(f.vm.modal,null);assert.equal(f.vm.clearHistoryBusy,false);assert.equal(f.vm.imagePagination.total,0);assert.match(f.vm.toast.message,/已清空 57 张图片/)
 }finally{globalThis.fetch=originalFetch;await f.close()}
})

test('failed DELETE preserves the current gallery and reports the error in the confirmation',async()=>{
 const f=await fixture({hash:'#/devices/dev_fixture'}),originalFetch=globalThis.fetch
 try{
  const old=f.vm.history.map(x=>x.id),latest=f.vm.detail.latest_image.id
  globalThis.fetch=(url,options={})=>options.method==='DELETE'?Promise.resolve(response({error:{code:'FORBIDDEN',message:'禁止清理'}},403)):originalFetch(url,options)
  await f.vm.openClearImageHistory();await f.vm.modalSubmit()
  assert.deepEqual(f.vm.history.map(x=>x.id),old);assert.equal(f.vm.detail.latest_image.id,latest);assert.equal(f.vm.modal.type,'confirm');assert.equal(f.vm.modalError,'禁止清理');assert.equal(f.vm.clearHistoryBusy,false)
 }finally{globalThis.fetch=originalFetch;await f.close()}
})

test('a completed clear from an old session cannot empty new account data or close its dialog',async()=>{
 const f=await fixture({hash:'#/devices/dev_fixture'}),originalFetch=globalThis.fetch
 try{
  let deleting
  globalThis.fetch=(url,options={})=>options.method==='DELETE'?new Promise(resolve=>{deleting=resolve}):originalFetch(url,options)
  await f.vm.openClearImageHistory();const clear=f.vm.modalSubmit()
  f.vm.acceptSession({user:{id:'usr_new',username:'new',display_name:'新账号',role:'agent',enabled:true,must_change_password:false},csrf_token:'new-csrf'})
  f.vm.selectedID='dev_new';f.vm.detail=deviceData({id:'dev_new',latest_image:{id:'new_private_image'}});f.vm.history=[{id:'new_private_image'}]
  await f.vm.openModal('help');deleting(response({cleared:57}));await clear
  assert.equal(f.vm.user.id,'usr_new');assert.equal(f.vm.detail.id,'dev_new');assert.equal(f.vm.history[0].id,'new_private_image');assert.equal(f.vm.modal.type,'help')
 }finally{globalThis.fetch=originalFetch;await f.close()}
})

test('partial file cleanup has an explicit warning instead of claiming complete success',async()=>{
 const f=await fixture({hash:'#/devices/dev_fixture'}),originalFetch=globalThis.fetch
 try{
  globalThis.fetch=(url,options={})=>options.method==='DELETE'?Promise.resolve(response({cleared:42,files_removed:41,file_cleanup_failed:1})):url.includes('/images?')?Promise.resolve(response(pageData(1,{items:[],total:0,total_pages:0}))):url==='/api/v1/devices/dev_fixture'?Promise.resolve(response(deviceData({latest_image:null}))):originalFetch(url,options)
  await f.vm.openClearImageHistory();await f.vm.modalSubmit();assert.equal(f.vm.toast.error,true);assert.match(f.vm.toast.message,/图片列表已清空/);assert.match(f.vm.toast.message,/部分文件清理未完成/)
 }finally{globalThis.fetch=originalFetch;await f.close()}
})

for(const change of ['device','assignment'])test(`confirmation is refused before DELETE when the ${change} changes`,async()=>{
 const f=await fixture({hash:'#/devices/dev_fixture'}),originalFetch=globalThis.fetch
 try{
  let deletes=0
  globalThis.fetch=(url,options={})=>{if(options.method==='DELETE')deletes++;return originalFetch(url,options)}
  await f.vm.openClearImageHistory()
  if(change==='device')f.vm.selectedID='dev_other'
  else f.vm.detail={...f.vm.detail,assignment_version:2}
  await f.vm.modalSubmit();assert.equal(deletes,0);assert.equal(f.vm.clearHistoryBusy,false);assert.match(f.vm.modalError,/设备或归属已更新/)
 }finally{globalThis.fetch=originalFetch;await f.close()}
})
