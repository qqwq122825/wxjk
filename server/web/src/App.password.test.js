import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import { createRenderer, nextTick } from 'vue'

// Exercise the real App SFC with a local in-memory Vue renderer and fake server.
// No account, credentials, device data, browser session or network is used.
const original = fs.readFileSync(new URL('./App.vue', import.meta.url), 'utf8')
const exposed = 'user,login,modal,form,csrf,needsSetup,ready,isAdmin,requiresPasswordChange,passwordMinLength,passwordMaxLength,passwordPattern,modalError,randomPassword,agentPasswordOK,openModal,closeModal,modalSubmit,submitLogin,loadData,syncRoute,nav,selectedID,tabs,total,devices,stats,clearSession,acceptSession,logout,loading,detail,history,refreshDetail,api'
const instrumented = original.replace('</script>', `defineExpose({${exposed}})\n</script>`)
const { descriptor } = parse(instrumented)
let code = compileScript(descriptor, { id: 'password-policy-test', inlineTemplate: true }).content
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
    calls.push({ url, method: options.method || 'GET', body: options.body ? JSON.parse(options.body) : undefined })
    let data, status = 200
    if (url === '/api/v1/auth/bootstrap') data = { needs_setup: needsSetup, requires_setup_token: false }
    else if (url === '/api/v1/auth/me') { status = authenticated ? 200 : 401; data = authenticated ? { user: { ...user }, csrf_token: 'synthetic-csrf' } : { error: { code: 'UNAUTHORIZED', message: 'synthetic' } } }
    else if (url === '/api/v1/auth/login' || url === '/api/v1/auth/setup') data = { user: { ...user }, csrf_token: 'synthetic-csrf' }
    else if (url === '/api/v1/auth/password') data = { user: { ...user, must_change_password: false } }
    else if (url === '/api/v1/dashboard') data = { total: 0, online: 0, unassigned: 0 }
    else if (url.startsWith('/api/v1/devices?') || url === '/api/v1/admin/agents') data = { items: [], total: 0 }
    else if (url.endsWith('/images')) data = { items: [] }
    else if (url.startsWith('/api/v1/devices/dev_')) data = { id: 'dev_fixture', name: '测试设备', enabled: true }
    else if (url.startsWith('/api/v1/admin/agents/') && options.method === 'POST') data = { status: 'ok' }
    else throw new Error('Unexpected mock request: ' + url)
    return { ok: status < 400, status, json: async () => data }
  }
  const host = node('root'), app = renderer.createApp(Component), vm = app.mount(host)
  await flush()
  return { host, app, vm, calls, win, async close() { app.unmount(); await flush(); for (const [key, value] of Object.entries(saved)) { if (value === undefined) delete globalThis[key]; else globalThis[key] = value } } }
}

test('legacy agent refresh ignores only the password flag and loads scoped data', async () => {
  const f = await fixture()
  try {
    assert.equal(f.vm.ready, true); assert.equal(f.vm.user.must_change_password, true)
    assert.equal(f.vm.requiresPasswordChange, false); assert.equal(f.vm.isAdmin, false)
    assert.equal(f.vm.modal, null); assert.equal(dialog(f.host).open, false)
    assert.ok(f.calls.some(c => c.url === '/api/v1/dashboard'))
    assert.ok(f.calls.some(c => c.url.startsWith('/api/v1/devices?')))
    assert.ok(f.vm.tabs.every(t => !['agents', 'pending', 'audit'].includes(t.id)))
    assert.ok(!f.calls.some(c => c.url.startsWith('/api/v1/admin/')))
  } finally { await f.close() }
})

test('agent normal login accepts an existing long password without forced dialog', async () => {
  const f = await fixture({ authenticated: false })
  try {
    const loginPassword = inputs(f.host).find(n => n.props.autocomplete === 'current-password')
    assert.equal(loginPassword.props.minlength, 1); assert.equal(loginPassword.props.maxlength, '128')
    assert.equal(loginPassword.props.pattern, undefined)
    f.vm.login.username = 'fixture'; f.vm.login.password = 'ExistingLongPassword123!'
    await f.vm.submitLogin(); await flush()
    assert.equal(f.vm.modal, null); assert.equal(f.vm.requiresPasswordChange, false)
    assert.equal(f.calls.find(c => c.url === '/api/v1/auth/login').body.password, 'ExistingLongPassword123!')
    assert.ok(f.calls.some(c => c.url === '/api/v1/dashboard'))
  } finally { await f.close() }
})

test('agent legacy flag does not block deep-link refresh or expose admin routes', async () => {
  const f = await fixture({ hash: '#/devices/dev_fixture' })
  try {
    assert.ok(f.calls.some(c => c.url === '/api/v1/devices/dev_fixture'))
    f.win.location.hash = '#/agents'; f.vm.syncRoute(); await flush()
    assert.equal(f.vm.nav, 'overview'); assert.equal(f.vm.selectedID, '')
    assert.ok(!f.calls.some(c => c.url.startsWith('/api/v1/admin/')))
  } finally { await f.close() }
})

test('admin flag still forces password change, blocks loading, and keeps 10+ policy', async () => {
  const f = await fixture({ role: 'admin' })
  try {
    assert.equal(f.vm.requiresPasswordChange, true); assert.equal(f.vm.modal.type, 'password')
    assert.equal(dialog(f.host).open, true)
    assert.ok(!f.calls.some(c => c.url === '/api/v1/dashboard'))
    f.vm.closeModal(); await flush(); assert.equal(f.vm.modal.type, 'password')
    const passwordInputs = inputs(dialog(f.host)).filter(n => n.props.autocomplete === 'new-password')
    assert.equal(passwordInputs.length, 2)
    for (const input of passwordInputs) { assert.equal(input.props.minlength, 10); assert.equal(input.props.maxlength, 128); assert.equal(input.props.pattern, undefined) }
    assert.equal(all(dialog(f.host), n => n.tag === 'button' && text(n) === '取消').length, 0)
  } finally { await f.close() }
})

test('agent voluntary change has six alphanumeric inputs and is cancellable', async () => {
  const f = await fixture()
  try {
    await f.vm.openModal('password'); await flush()
    for (const input of inputs(dialog(f.host)).filter(n => n.props.autocomplete === 'new-password')) {
      assert.equal(input.props.minlength, 6); assert.equal(input.props.maxlength, 6); assert.equal(input.props.pattern, '[A-Za-z0-9]{6}')
    }
    assert.equal(inputs(dialog(f.host)).find(n => n.props.autocomplete === 'current-password').props.maxlength, '128')
    assert.equal(all(dialog(f.host), n => n.tag === 'button' && text(n) === '取消').length, 1)
    f.vm.closeModal(); await flush(); assert.equal(f.vm.modal, null)
  } finally { await f.close() }
})

test('create/reset fields and cryptographic random generator produce exactly six alphanumerics', async () => {
  const f = await fixture({ role: 'admin', legacyFlag: false })
  try {
    for (const type of ['agent', 'reset']) {
      await f.vm.openModal(type, { id: 'usr_agent', name: '测试代理' }); await flush()
      const input = inputs(dialog(f.host)).find(n => n.props.autocomplete === 'new-password')
      assert.equal(input.props.minlength, '6'); assert.equal(input.props.maxlength, '6'); assert.equal(input.props.pattern, '[A-Za-z0-9]{6}')
      for (let i = 0; i < 100; i++) { f.vm.randomPassword(); assert.match(f.vm.form.password, /^[A-Za-z0-9]{6}$/) }
      f.vm.closeModal()
    }
    for (const password of ['123456', 'abcdef', 'ABCDEF', 'A1b2C3']) assert.equal(f.vm.agentPasswordOK(password), true)
    for (const password of ['', '12345', '1234567', '12345!', '中文1234', 'abcdef' + String.fromCharCode(10), 'abcdef' + String.fromCharCode(13)]) assert.equal(f.vm.agentPasswordOK(password), false)
  } finally { await f.close() }
})

test('programmatic create/reset rejects invalid passwords before sending requests', async () => {
  const f = await fixture({ role: 'admin', legacyFlag: false })
  try {
    for (const type of ['agent', 'reset']) {
      await f.vm.openModal(type, { id: 'usr_agent', name: '测试代理' })
      f.vm.form.password = 'TooLongPassword!'; await f.vm.modalSubmit()
      assert.ok(f.vm.modalError.includes('6 位字母或数字'))
    }
    assert.ok(!f.calls.some(c => c.method === 'POST' && c.url.startsWith('/api/v1/admin/agents')))
  } finally { await f.close() }
})

test('agent voluntary change enforces six alphanumeric characters, not mandatory mixing', async () => {
  const f = await fixture()
  try {
    await f.vm.openModal('password')
    f.vm.form = { current_password: 'ExistingLongPassword123!', new_password: 'abcdef!', confirm_password: 'abcdef!' }
    await f.vm.modalSubmit(); assert.ok(f.vm.modalError.includes('6 位字母或数字'))
    assert.ok(!f.calls.some(c => c.url === '/api/v1/auth/password'))
    f.vm.form.new_password = 'abcdef'; f.vm.form.confirm_password = 'abcdef'
    await f.vm.modalSubmit(); await flush()
    const call = f.calls.find(c => c.url === '/api/v1/auth/password')
    assert.equal(call.body.new_password, 'abcdef'); assert.equal(call.body.current_password, 'ExistingLongPassword123!')
    assert.equal(f.vm.modal, null)
  } finally { await f.close() }
})

test('admin first-time setup keeps ten-character minimum and 128 maximum', async () => {
  const f = await fixture({ authenticated: false, needsSetup: true, role: 'admin', legacyFlag: false })
  try {
    const password = inputs(f.host).find(n => n.props.autocomplete === 'new-password')
    assert.equal(password.props.minlength, 10); assert.equal(password.props.maxlength, '128')
    assert.equal(password.props.pattern, undefined)
  } finally { await f.close() }
})

function deferredResponse(url, options, pending) {
  return new Promise(resolve => pending.push({url,signal:options.signal,resolve}))
}
function response(data, status = 200) { return {ok:status<400,status,json:async()=>data} }
function agentSession() { return {user:{id:'usr_agent_session',username:'cs001',display_name:'cs',role:'agent',enabled:true,must_change_password:false},csrf_token:'synthetic-agent-csrf'} }

test('switching accounts clears stale totals, aborts old requests and immediately loads the agent', async () => {
  const f=await fixture({role:'admin',legacyFlag:false}), originalFetch=globalThis.fetch, oldRequests=[], newRequests=[]
  try {
    globalThis.fetch=async (url,options={})=>url==='/api/v1/dashboard'?response({total:1}):url.startsWith('/api/v1/devices?')?response({items:[{id:'dev_old_admin',name:'old admin device',enabled:true}],total:1}):originalFetch(url,options)
    await f.vm.loadData();assert.equal(f.vm.total,1);assert.equal(f.vm.devices.length,1)
    globalThis.fetch=(url,options={})=>url==='/api/v1/dashboard'||url.startsWith('/api/v1/devices?')?deferredResponse(url,options,oldRequests):originalFetch(url,options)
    const oldLoad=f.vm.loadData();assert.equal(oldRequests.length,2)
    f.vm.clearSession();assert.equal(f.vm.total,0);assert.equal(f.vm.loading,false);assert.ok(oldRequests.every(r=>r.signal.aborted))
    f.vm.acceptSession(agentSession())
    globalThis.fetch=(url,options={})=>url==='/api/v1/dashboard'||url.startsWith('/api/v1/devices?')?deferredResponse(url,options,newRequests):originalFetch(url,options)
    const agentLoad=f.vm.loadData();assert.equal(newRequests.length,2);assert.equal(f.vm.loading,true)
    await oldLoad;await flush()
    await f.vm.loadData();assert.equal(newRequests.length,2);assert.equal(f.vm.loading,true,'old finally cannot unlock new request')
    for(const r of newRequests)r.resolve(response(r.url==='/api/v1/dashboard'?{total:1}:{items:[{id:'dev_agent',name:'agent device',enabled:true}],total:1}))
    await agentLoad;assert.equal(f.vm.user.username,'cs001');assert.equal(f.vm.stats.total,1);assert.equal(f.vm.devices[0].id,'dev_agent');assert.equal(f.vm.total,1)
    for(const r of oldRequests)r.resolve(response({error:{code:'LOGIN_REQUIRED',message:'obsolete'}},401))
    await flush();assert.equal(f.vm.user.username,'cs001');assert.equal(f.vm.devices[0].id,'dev_agent')
    console.log('SESSION_FIXED_AGENT_STATE='+JSON.stringify({username:f.vm.user.username,dashboard_total:f.vm.stats.total,list_items:f.vm.devices.length,pagination_total:f.vm.total,new_agent_requests:newRequests.length,old_requests_aborted:oldRequests.every(r=>r.signal.aborted)}))
  } finally {globalThis.fetch=originalFetch;await f.close()}
})

test('acceptSession alone isolates previous account data and pending detail requests', async () => {
  const f=await fixture({role:'admin',legacyFlag:false}), originalFetch=globalThis.fetch, oldRequests=[], newRequests=[]
  try {
    f.vm.total=17;f.vm.devices=[{id:'dev_old',name:'old'}];f.vm.stats={total:17};f.vm.selectedID='dev_old'
    globalThis.fetch=(url,options={})=>url.includes('/devices/dev_')?deferredResponse(url,options,oldRequests):originalFetch(url,options)
    const oldDetail=f.vm.refreshDetail();assert.equal(oldRequests.length,2)
    f.vm.acceptSession(agentSession());assert.equal(f.vm.total,0);assert.equal(f.vm.devices.length,0);assert.deepEqual(f.vm.stats,{})
    assert.ok(oldRequests.every(r=>r.signal.aborted));f.vm.selectedID='dev_new'
    globalThis.fetch=(url,options={})=>url.includes('/devices/dev_')?deferredResponse(url,options,newRequests):originalFetch(url,options)
    const current=f.vm.refreshDetail();assert.equal(newRequests.length,2)
    await oldDetail
    await f.vm.refreshDetail();assert.equal(newRequests.length,2,'old detail finally cannot unlock current request')
    for(const r of newRequests)r.resolve(response(r.url.endsWith('/images')?{items:[]}:{id:'dev_new',name:'new device'}))
    await current
    for(const r of oldRequests)r.resolve(response(r.url.endsWith('/images')?{items:[{id:'private_old_image'}]}:{id:'dev_old',name:'old device'}))
    await flush();assert.equal(f.vm.detail.id,'dev_new');assert.deepEqual(f.vm.history,[])
  } finally {globalThis.fetch=originalFetch;await f.close()}
})

test('API deadlines abort hung fetches, release loading and allow a retry', async () => {
  const f=await fixture(), originalFetch=globalThis.fetch, originalSetTimeout=globalThis.setTimeout, deadlines=[], pending=[]
  try {
    globalThis.setTimeout=(fn,delay,...args)=>delay===15000?(deadlines.push(fn),987654):originalSetTimeout(fn,delay,...args)
    globalThis.fetch=(url,options={})=>url==='/api/v1/dashboard'||url.startsWith('/api/v1/devices?')?deferredResponse(url,options,pending):originalFetch(url,options)
    const hung=f.vm.loadData();assert.equal(deadlines.length,2);for(const fire of deadlines)fire()
    await hung;assert.equal(f.vm.loading,false);assert.ok(pending.every(r=>r.signal.aborted))
    globalThis.fetch=originalFetch;await f.vm.loadData();assert.equal(f.vm.loading,false);assert.equal(f.vm.total,0)
  } finally {globalThis.setTimeout=originalSetTimeout;globalThis.fetch=originalFetch;await f.close()}
})

test('component unmount aborts outstanding requests without waiting for fetch completion', async () => {
  const f=await fixture(), originalFetch=globalThis.fetch, pending=[]
  globalThis.fetch=(url,options={})=>url==='/api/v1/dashboard'||url.startsWith('/api/v1/devices?')?deferredResponse(url,options,pending):originalFetch(url,options)
  const load=f.vm.loadData();assert.equal(pending.length,2)
  await f.close();await load;assert.ok(pending.every(r=>r.signal.aborted))
})

test('obsolete logout and assignment-dialog requests cannot clear or reopen a new session', async () => {
  const f=await fixture({role:'admin',legacyFlag:false}), originalFetch=globalThis.fetch, pending=[]
  try {
    globalThis.fetch=(url,options={})=>url==='/api/v1/auth/logout'||url==='/api/v1/admin/agents'?deferredResponse(url,options,pending):originalFetch(url,options)
    const opening=f.vm.openModal('assign',{ids:['dev_old']});assert.equal(pending.length,1)
    f.vm.acceptSession(agentSession());await opening;assert.equal(f.vm.modal,null);assert.equal(dialog(f.host).open,false)
    const logout=f.vm.logout();assert.equal(pending.length,2)
    f.vm.acceptSession(agentSession());await logout;assert.equal(f.vm.user.username,'cs001')
    for(const r of pending)r.resolve(response({items:[{id:'private_old_agent'}]}));await flush()
    assert.equal(f.vm.user.username,'cs001');assert.equal(f.vm.modal,null);assert.equal(dialog(f.host).open,false)
  } finally {globalThis.fetch=originalFetch;await f.close()}
})

test('API deadline also bounds a stalled JSON response body', async () => {
  const f=await fixture(), originalFetch=globalThis.fetch, originalSetTimeout=globalThis.setTimeout, deadlines=[]
  let signal
  try {
    globalThis.setTimeout=(fn,delay,...args)=>delay===15000?(deadlines.push(fn),987655):originalSetTimeout(fn,delay,...args)
    globalThis.fetch=async (url,options={})=>{signal=options.signal;return {ok:true,status:200,json:()=>new Promise(()=>{})}}
    const body=f.vm.api('/synthetic-stalled-body')
    const rejected=assert.rejects(body,error=>error.code==='REQUEST_TIMEOUT')
    await flush();assert.equal(deadlines.length,1);deadlines[0]();await rejected
    assert.equal(signal.aborted,true);assert.equal(f.vm.user.role,'agent')
  } finally {globalThis.setTimeout=originalSetTimeout;globalThis.fetch=originalFetch;await f.close()}
})
