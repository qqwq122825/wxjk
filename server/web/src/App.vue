<script setup>
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import DeviceDetail from './DeviceDetail.vue'
import { Activity, ArrowUpRight, Camera, Check, ChevronLeft, ChevronRight, CircleAlert, CircleCheck, Command, Eye, EyeOff, KeyRound, LayoutDashboard, Link, LoaderCircle, LogOut, MapPin, Menu, Plus, Radio, RefreshCw, Search, Server, ShieldCheck, Smartphone, Users, Wifi, WifiOff, X } from '@lucide/vue'

const user = ref(null), csrf = ref(''), ready = ref(false), needsSetup = ref(false), needsSetupToken = ref(false)
const isLocalHTTP = window.location.protocol === 'http:'
const nav = ref('overview'), mobileNav = ref(false), stats = ref({}), devices = ref([]), agents = ref([]), commands = ref([]), audits = ref([])
const total = ref(0), page = ref(1), search = ref(''), filter = ref('all'), selected = ref([]), loading = ref(false), actionBusy = ref(false)
const selectedID = ref(''), detail = ref(null), history = ref([]), detailError = ref('')
const imagePagination = ref(emptyImagePagination()), historyBusy = ref(false), clearHistoryBusy = ref(false), historyRevision = ref(0)
const login = ref({username:'',password:'',setup_token:''}), loginError = ref(''), showPassword = ref(false)
const modal = ref(null), modalEl = ref(null), modalError = ref(''), modalBusy = ref(false)
const serverReachable = ref(true)
const form = ref({}), toast = ref(null), overviewTab = ref('all'), confirmCallback = ref(null)
const isAdmin = computed(() => user.value?.role === 'admin')
const requiresPasswordChange = computed(() => user.value?.role !== 'agent' && !!user.value?.must_change_password)
const passwordMinLength = computed(() => user.value?.role === 'agent' ? 6 : 10)
const passwordMaxLength = computed(() => user.value?.role === 'agent' ? 6 : 128)
const passwordPattern = computed(() => user.value?.role === 'agent' ? '[A-Za-z0-9]{6}' : undefined)
const passwordHint = computed(() => user.value?.role === 'agent' ? '6 位字母或数字' : '至少 10 位')
const tabs = computed(() => [
  {id:'overview', name:'工作台', icon:LayoutDashboard},
  {id:'devices', name:isAdmin.value?'全部设备':'我的设备', icon:Smartphone},
  ...(isAdmin.value?[{id:'pending',name:'待分配设备',icon:Link,badge:stats.value.unassigned},{id:'agents',name:'代理管理',icon:Users}]:[]),
  {id:'commands',name:'任务记录',icon:Command},
  ...(isAdmin.value?[{id:'audit',name:'操作日志',icon:ShieldCheck}]:[])
])
const pageTitle = computed(() => selectedID.value ? '设备详情' : tabs.value.find(t=>t.id===nav.value)?.name || '工作台')
const commandNames = {GET_IMAGE:'获取窗口图片',GET_LOCATION:'获取当前位置',START_IMAGE_SESSION:'开始实时采集',STOP_IMAGE_SESSION:'停止实时采集'}
const statusNames = {queued:'等待手机',received:'已接收',running:'执行中',waiting_foreground:'等待 App 活动',waiting_permission:'等待授权',succeeded:'已完成',failed:'执行失败',expired:'已过期',canceled:'已取消'}
const auditNames = {'images.clear':'清空设备图片','agent.create':'创建代理账号','agent.update':'修改代理账号','agent.password_reset':'重置代理密码','device.assign':'调整设备归属','device.enabled':'调整设备状态','device.rename':'修改设备备注','command.GET_IMAGE':'请求窗口图片','command.GET_LOCATION':'请求当前位置','command.START_IMAGE_SESSION':'开始实时采集','command.STOP_IMAGE_SESSION':'停止实时采集'}
const stateTone = s => ['succeeded','running'].includes(s)?'green':['failed','expired','canceled'].includes(s)?'red':'amber'
const shortID = id => id ? id.slice(-8).toUpperCase() : '—'
const date = (v,full=false) => v ? new Intl.DateTimeFormat('zh-CN',{timeZone:'Asia/Shanghai',...(full?{month:'2-digit',day:'2-digit'}:{}),hour:'2-digit',minute:'2-digit',second:'2-digit',hour12:false}).format(new Date(v)) : '—'
const age = v => {if(!v)return '尚无记录';let n=Math.max(0,Math.floor((Date.now()-new Date(v))/1000));return n<60?`${n} 秒前`:n<3600?`${Math.floor(n/60)} 分钟前`:n<86400?`${Math.floor(n/3600)} 小时前`:`${Math.floor(n/86400)} 天前`}
const day = computed(() => new Intl.DateTimeFormat('zh-CN',{timeZone:'Asia/Shanghai',year:'numeric',month:'long',day:'numeric',weekday:'long'}).format(new Date()))
let interval, detailPoll, detailPollAt=0, detailRefreshBusy=null, searchTimer, toastTimer, inFlight=null, sessionEpoch=0, detailReturnRoute='#/devices'
let historyEpoch=0,historyViewEpoch=0,historyRequest=null,clearHistoryRequest=null
const pendingRequests = new Set(), API_TIMEOUT_MS = 15000
function emptyImagePagination(){return {page:1,page_size:14,total:0,total_pages:1}}
function imageScope(device=detail.value){return [device?.id||selectedID.value,device?.assignment_id||'',device?.assignment_version??''].join(':')}
function resetImageHistory(){historyEpoch++;historyViewEpoch++;historyRequest=null;clearHistoryRequest=null;historyBusy.value=false;clearHistoryBusy.value=false;history.value=[];imagePagination.value=emptyImagePagination();historyRevision.value++;detailRefreshBusy=null}
function applyImageHistory(data,requestedPage){const items=Array.isArray(data.items)?data.items:[];const total=Math.max(0,Number(data.total??items.length)||0),pageSize=Math.max(1,Number(data.page_size)||14);history.value=items;imagePagination.value={page:Math.max(1,Number(data.page)||requestedPage),page_size:pageSize,total,total_pages:Math.max(1,Number(data.total_pages)||Math.ceil(total/pageSize))}}
function cancelSessionWork(){
  sessionEpoch++;inFlight=null;detailRefreshBusy=null;loading.value=false;clearTimeout(searchTimer);resetImageHistory()
  const previous=[...pendingRequests];pendingRequests.clear()
  for(const controller of previous)controller.abort()
}
function clearSession(){cancelSessionWork();modalEl.value?.close();user.value=null;csrf.value='';selectedID.value='';detail.value=null;history.value=[];detailError.value='';devices.value=[];agents.value=[];commands.value=[];audits.value=[];stats.value={};total.value=0;selected.value=[];modal.value=null;form.value={};modalBusy.value=false;actionBusy.value=false;confirmCallback.value=null;serverReachable.value=false;nav.value='overview';overviewTab.value='all';filter.value='all';search.value='';page.value=1}
function acceptSession(data){clearSession();user.value=data.user;csrf.value=data.csrf_token;syncRoute(false)}

async function api(path,options={}) {
  const requestEpoch=sessionEpoch,controller=new AbortController()
  let timedOut=false,abortRequest
  const canceled=new Promise((resolve,reject)=>{abortRequest=()=>{const e=new Error(timedOut?'请求超时，请重试':'登录会话已更新');e.code=timedOut?'REQUEST_TIMEOUT':'SESSION_CHANGED';reject(e)};controller.signal.addEventListener('abort',abortRequest,{once:true})})
  const timer=setTimeout(()=>{timedOut=true;controller.abort()},API_TIMEOUT_MS)
  pendingRequests.add(controller)
  const headers={...(options.body?{'Content-Type':'application/json'}:{}),...(options.method&&options.method!=='GET'?{'X-CSRF-Token':csrf.value}:{}),...options.headers}
  try{
    const response=await Promise.race([fetch('/api/v1'+path,{credentials:'same-origin',...options,headers,signal:controller.signal,body:options.body?JSON.stringify(options.body):undefined}),canceled])
    const data=await Promise.race([response.json().catch(()=>({})),canceled])
    if(requestEpoch!==sessionEpoch){const e=new Error('登录会话已更新');e.code='SESSION_CHANGED';throw e}
    if(!response.ok){if(response.status===401&&user.value)clearSession();const e=new Error(data.error?.message||'请求失败，请稍后重试');e.code=data.error?.code;e.status=response.status;throw e}
    return data
  }finally{clearTimeout(timer);controller.signal.removeEventListener('abort',abortRequest);pendingRequests.delete(controller)}
}
function notify(message,error=false){toast.value={message,error};clearTimeout(toastTimer);toastTimer=setTimeout(()=>toast.value=null,4500)}
async function bootstrap(){try{const b=await api('/auth/bootstrap');needsSetup.value=b.needs_setup;needsSetupToken.value=b.requires_setup_token;try{const me=await api('/auth/me');acceptSession(me);if(requiresPasswordChange.value)openModal('password')}catch{};if(user.value&&!requiresPasswordChange.value)await loadData()}catch(e){loginError.value=e.message}finally{ready.value=true}}
async function submitLogin(){loginError.value='';actionBusy.value=true;try{const d=await api(needsSetup.value?'/auth/setup':'/auth/login',{method:'POST',body:login.value});acceptSession(d);login.value.password='';login.value.setup_token='';needsSetup.value=false;if(requiresPasswordChange.value)openModal('password');else await loadData()}catch(e){loginError.value=e.message}finally{actionBusy.value=false}}
async function logout(){cancelSessionWork();const requestEpoch=sessionEpoch;try{await api('/auth/logout',{method:'POST'})}catch{};if(requestEpoch!==sessionEpoch)return;clearSession();window.location.hash='/overview'}
async function loadData(silent=false){
  if(!user.value||requiresPasswordChange.value||inFlight)return
  const requestEpoch=sessionEpoch,request={epoch:sessionEpoch}
  inFlight=request;if(!silent)loading.value=true
  try{
    const state=nav.value==='pending'?'unassigned':nav.value==='overview'?overviewTab.value:filter.value
    const query=new URLSearchParams({page:String(page.value),page_size:'50',state,search:search.value})
    const [dashboard,list]=await Promise.all([api('/dashboard'),api('/devices?'+query)])
    if(requestEpoch!==sessionEpoch||inFlight!==request||!user.value)return
    serverReachable.value=true
    stats.value=dashboard;devices.value=list.items;total.value=list.total
    if(isAdmin.value&&(nav.value==='agents'||modal.value?.type==='assign')){const data=await api('/admin/agents');if(requestEpoch!==sessionEpoch)return;agents.value=data.items}
    if(nav.value==='commands'){const data=await api('/commands?page=1&page_size=100');if(requestEpoch!==sessionEpoch)return;commands.value=data.items}
    if(nav.value==='audit'&&isAdmin.value){const data=await api('/admin/audit');if(requestEpoch!==sessionEpoch)return;audits.value=data.items}
    if(selectedID.value)await refreshDetail()
  }catch(e){if(requestEpoch===sessionEpoch&&inFlight===request&&user.value){serverReachable.value=false;if(!silent&&e.code!=='SESSION_CHANGED')notify(e.message,true)}}finally{if(inFlight===request){inFlight=null;loading.value=false}}
}
const deviceHref = id => '#/devices/'+encodeURIComponent(id)
function setRoute(hash){if(window.location.hash===hash)syncRoute();else window.location.hash=hash}
function go(id){setRoute('#/'+id)}
function syncRoute(fetchData=true){
  const route=window.location.hash.slice(1)||'/overview'
  const match=/^\/devices\/(dev_[A-Za-z0-9_-]{1,100})$/.exec(route)
  const previousID=selectedID.value
  mobileNav.value=false
  if(match){
    if(!previousID)detailReturnRoute='#/'+(['devices','pending'].includes(nav.value)?nav.value:'devices')
    if(previousID!==match[1]){resetImageHistory();selectedID.value=match[1];detail.value=null;detailError.value=''}
    document.title=(detail.value?.name||'设备详情')+' · JK Monitor'
    if(fetchData&&user.value&&!requiresPasswordChange.value)refreshDetail()
  }else{
    const target=route.slice(1)
    const allowed=tabs.value.some(tab=>tab.id===target)?target:'overview'
    resetImageHistory();selectedID.value='';detail.value=null;detailError.value=''
    if(nav.value!==allowed){page.value=1;selected.value=[];search.value='';filter.value='all'}
    nav.value=allowed
    document.title=pageTitle.value+' · JK Monitor'
    if(fetchData)loadData()
  }
  if(previousID!==selectedID.value)window.scrollTo({top:0,behavior:'instant'})
}
function routeChanged(){syncRoute()}
watch([filter,page,overviewTab],()=>loadData())
watch(search,()=>{clearTimeout(searchTimer);searchTimer=setTimeout(()=>{page.value=1;loadData()},300)})
async function refreshDetail(latestOnly=false){
  const id=selectedID.value,requestEpoch=sessionEpoch
  if(!id||clearHistoryBusy.value||(detailRefreshBusy?.epoch===requestEpoch&&detailRefreshBusy.id===id))return
  const withHistory=!latestOnly&&!historyBusy.value,requestedPage=imagePagination.value.page
  const request={epoch:requestEpoch,id,scope:imageScope()};detailRefreshBusy=request
  const historyToken=withHistory?{epoch:historyEpoch,id,session:requestEpoch,page:requestedPage}:null
  if(historyToken){historyRequest=historyToken;historyBusy.value=true}
  try{
    const query=new URLSearchParams({page:String(requestedPage),page_size:'14'})
    const [d,h]=await Promise.all([api('/devices/'+id),withHistory?api('/devices/'+id+'/images?'+query):Promise.resolve(null)])
    if(selectedID.value!==id||requestEpoch!==sessionEpoch||detailRefreshBusy!==request)return
    const assignmentChanged=!!detail.value&&imageScope(d)!==request.scope
    detail.value=d;detailError.value=''
    if(assignmentChanged){resetImageHistory();await loadHistoryPage(1)}
    else if(h&&historyRequest===historyToken&&historyToken.epoch===historyEpoch){applyImageHistory(h,requestedPage)}
    document.title=d.name+' · JK Monitor'
  }catch(e){
    if(requestEpoch!==sessionEpoch||selectedID.value!==id||detailRefreshBusy!==request)return
    if(e.status===404){closeModal();closeDetail();notify('设备不存在，或访问权限已更新',true)}else detailError.value=e.message
  }finally{
    if(detailRefreshBusy===request)detailRefreshBusy=null
    if(historyToken&&historyRequest===historyToken){historyRequest=null;historyBusy.value=false}
  }
}
async function loadHistoryPage(value){
  const id=selectedID.value,requestedPage=Math.max(1,Math.floor(Number(value)||1))
  if(!id||clearHistoryBusy.value)return
  const request={epoch:++historyEpoch,id,session:sessionEpoch,scope:imageScope(),page:requestedPage};historyRequest=request;historyBusy.value=true
  try{
    const query=new URLSearchParams({page:String(requestedPage),page_size:'14'})
    const data=await api('/devices/'+id+'/images?'+query)
    if(historyRequest!==request||request.epoch!==historyEpoch||request.session!==sessionEpoch||selectedID.value!==id||imageScope()!==request.scope)return
    applyImageHistory(data,requestedPage)
    if(imagePagination.value.page>imagePagination.value.total_pages){await loadHistoryPage(imagePagination.value.total_pages)}
  }catch(e){if(historyRequest===request&&request.session===sessionEpoch&&selectedID.value===id&&e.code!=='SESSION_CHANGED')notify(e.message,true)}
  finally{if(historyRequest===request){historyRequest=null;historyBusy.value=false}}
}
async function openClearImageHistory(){
  if(!detail.value||clearHistoryBusy.value)return
  const id=selectedID.value,name=detail.value.name,requestEpoch=sessionEpoch,view=historyViewEpoch,scope=imageScope()
  await openModal('confirm',{title:'清空设备图片',message:`确定清空「${name}」的全部可访问图片吗？包含所有分页和当前画面，删除后无法恢复。设备、代理归属、定位及采集任务不受影响；若仍在实时采集，之后会继续产生新图片。`})
  if(requestEpoch!==sessionEpoch||view!==historyViewEpoch||selectedID.value!==id)return
  confirmCallback.value=async()=>{
    if(requestEpoch!==sessionEpoch||view!==historyViewEpoch||selectedID.value!==id||imageScope()!==scope)throw new Error('设备或归属已更新，请关闭后重新确认')
    const request={id,session:requestEpoch,view};clearHistoryRequest=request;clearHistoryBusy.value=true
    historyEpoch++;historyRequest=null;historyBusy.value=false;detailRefreshBusy=null
    let cleared=false
    try{
      const result=await api('/devices/'+id+'/images',{method:'DELETE'})
      if(clearHistoryRequest!==request||requestEpoch!==sessionEpoch||selectedID.value!==id||imageScope()!==scope)return
      detail.value={...detail.value,latest_image:null,last_image_at:null};resetImageHistory();cleared=true
      const count=Math.max(0,Number(result.cleared)||0),cleanupFailed=Number(result.file_cleanup_failed)||0
      notify(cleanupFailed?`图片列表已清空（${count} 张），部分文件清理未完成`:`已清空 ${count} 张图片`,cleanupFailed>0)
    }finally{if(clearHistoryRequest===request){clearHistoryRequest=null;clearHistoryBusy.value=false}}
    if(cleared&&requestEpoch===sessionEpoch&&selectedID.value===id)await refreshDetail()
  }
}

function openDevice(d){setRoute(deviceHref(d.id))}
function closeDetail(){setRoute(detailReturnRoute)}
async function openModal(type,data={}){const requestEpoch=sessionEpoch;modalError.value='';form.value={...data};modal.value={type,...data};if(type==='assign'){try{const data=await api('/admin/agents');if(requestEpoch!==sessionEpoch)return;agents.value=data.items}catch(e){if(requestEpoch!==sessionEpoch)return;modalError.value=e.message}};await nextTick();if(requestEpoch===sessionEpoch&&modal.value?.type===type&&modalEl.value&&!modalEl.value.open)modalEl.value.showModal()}
function closeModal(){if(requiresPasswordChange.value&&modal.value?.type==='password')return;modalEl.value?.close();modal.value=null;modalError.value='';confirmCallback.value=null}
async function modalSubmit(){const requestEpoch=sessionEpoch,submittedModal=modal.value;modalError.value='';modalBusy.value=true;try{
  const type=modal.value.type
  if(['agent','reset'].includes(type)&&!agentPasswordOK(form.value.password))throw new Error('代理密码必须是 6 位字母或数字')
  if(type==='password'&&user.value?.role==='agent'&&!agentPasswordOK(form.value.new_password))throw new Error('代理密码必须是 6 位字母或数字')
  if(type==='agent'){await api('/admin/agents',{method:'POST',body:form.value});notify('代理账号已创建，可直接登录')}
  if(type==='reset'){await api('/admin/agents/'+modal.value.id+'/password',{method:'POST',body:{password:form.value.password}});notify('初始密码已重置，原登录已失效')}
  if(type==='assign'){if(!form.value.agent_id)throw new Error('请选择接收设备的代理');const ids=modal.value.ids;let done=0;try{for(const id of ids){await api('/admin/devices/'+id+'/assignment',{method:'POST',body:{agent_id:form.value.agent_id}});done++}}catch(e){throw new Error(`已分配 ${done}/${ids.length} 台：${e.message}`)};selected.value=[];notify(`已分配 ${done} 台设备`)}
  if(type==='rename'){await api('/devices/'+modal.value.id,{method:'PATCH',body:form.value});notify('设备信息已保存')}
  if(type==='password'){if(form.value.new_password!==form.value.confirm_password)throw new Error('两次输入的新密码不一致');const d=await api('/auth/password',{method:'POST',body:form.value});user.value=d.user;notify('密码已更新')}
  if(type==='confirm'&&confirmCallback.value)await confirmCallback.value()
  if(requestEpoch!==sessionEpoch||modal.value!==submittedModal)return
  modalEl.value?.close();modal.value=null;confirmCallback.value=null;form.value={};await loadData()
 }catch(e){if(requestEpoch===sessionEpoch&&modal.value===submittedModal)modalError.value=e.message}finally{if(requestEpoch===sessionEpoch)modalBusy.value=false}}
function agentPasswordOK(value){return typeof value==='string'&&value.length===6&&/^[A-Za-z0-9]{6}$/.test(value)}
function randomPassword(){const chars='ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';let password='';while(password.length<6){for(const byte of crypto.getRandomValues(new Uint8Array(12))){if(byte<248)password+=chars[byte%chars.length];if(password.length===6)break}}form.value.password=password}
async function toggleAgent(agent){await openModal('confirm',{title:agent.enabled?'禁用代理账号':'启用代理账号',message:agent.enabled?`禁用「${agent.display_name}」后，其登录会话和发起的采集任务将撤销，名下设备仍由总台管理。`:`恢复「${agent.display_name}」的登录权限。`});confirmCallback.value=async()=>{await api('/admin/agents/'+agent.id,{method:'PATCH',body:{enabled:!agent.enabled}});notify(agent.enabled?'账号已禁用':'账号已启用')}}
async function reclaim(d){await openModal('confirm',{title:'回收设备',message:`将「${d.name}」收回总台待分配列表，并撤销原代理访问权限和相关任务。`});confirmCallback.value=async()=>{await api('/admin/devices/'+d.id+'/assignment',{method:'POST',body:{agent_id:''}});notify('设备已收回总台')}}
async function toggleDevice(d){await openModal('confirm',{title:d.enabled?'停用设备':'启用设备',message:d.enabled?'设备停用后，服务器拒绝其上报并撤销当前采集任务。':'重新允许该设备连接和上报。'});confirmCallback.value=async()=>{await api('/admin/devices/'+d.id+'/enabled',{method:'POST',body:{enabled:!d.enabled}});notify('设备状态已更新')}}
async function sendCommand(type,options={}){if(!detail.value||actionBusy.value)return;actionBusy.value=true;try{const result=await api('/devices/'+detail.value.id+'/commands',{method:'POST',body:{type,...options}});notify(result.reused?'该设备已有实时任务':type==='STOP_IMAGE_SESSION'?'停止指令已保存，等待手机确认':'指令已排队，等待手机回复');await refreshDetail();await loadData(true)}catch(e){notify(e.message,true)}finally{actionBusy.value=false}}
function toggleSelection(id){selected.value=selected.value.includes(id)?selected.value.filter(x=>x!==id):[...selected.value,id]}
function toggleAll(){selected.value=selected.value.length===devices.value.length?[]:devices.value.map(d=>d.id)}
async function copy(text){try{await navigator.clipboard.writeText(text);notify('已复制')}catch{notify('无法复制，请手动选择文本',true)}}
onMounted(()=>{syncRoute(false);bootstrap();window.addEventListener('hashchange',routeChanged);interval=setInterval(()=>{if(document.visibilityState==='visible')loadData(true)},3000);detailPoll=setInterval(()=>{if(document.visibilityState==='visible'&&selectedID.value&&detail.value?.session?.desired_running&&Date.now()>=detailPollAt){detailPollAt=Date.now()+Math.max(500,detail.value.capture_options?.interval_ms||3000);refreshDetail(true)}},250)})
onUnmounted(()=>{cancelSessionWork();window.removeEventListener('hashchange',routeChanged);clearInterval(interval);clearInterval(detailPoll);clearTimeout(searchTimer);clearTimeout(toastTimer)})
</script>

<template>
  <div v-if="!ready" class="boot"><LoaderCircle class="spin"/><span>正在连接工作台</span></div>
  <main v-else-if="!user" class="auth-page">
    <div class="auth-brand"><div class="brand-mark"><Activity/></div><b>JK Monitor</b><span>设备管理平台</span></div>
    <div class="auth-layout">
      <section class="auth-intro"><div class="eyebrow"><span class="dot"/> CONNECTED WORKSPACE</div><h1>统一管理设备，<br>让每次连接<br><em>井然有序。</em></h1><p>设备接入、账号分配与远程采集，<br>在一个工作台完成。</p><div class="auth-features"><span><ShieldCheck/>账号与设备独立授权</span><span><Radio/>手机自主执行采集任务</span></div><div class="orb orb-one"></div><div class="orb orb-two"></div></section>
      <section class="auth-card"><div class="auth-icon"><ShieldCheck/></div><h2>{{needsSetup?'创建你的总台':'欢迎回来'}}</h2><p>{{needsSetup?'首次使用，设置总台管理员账号。':'登录后查看分配给你的设备与任务。'}}</p>
        <form @submit.prevent="submitLogin"><label>登录账号<input v-model.trim="login.username" autocomplete="username" placeholder="请输入登录账号" minlength="3" maxlength="32" required></label><label>登录密码<div class="password-input"><input v-model="login.password" :type="showPassword?'text':'password'" :autocomplete="needsSetup?'new-password':'current-password'" :minlength="needsSetup?10:1" maxlength="128" :placeholder="needsSetup?'设置至少 10 位密码':'请输入登录密码'" required><button type="button" @click="showPassword=!showPassword" :aria-label="showPassword?'隐藏密码':'显示密码'"><EyeOff v-if="showPassword"/><Eye v-else/></button></div></label><label v-if="needsSetup&&needsSetupToken">部署初始化口令<input v-model="login.setup_token" type="password" autocomplete="off" placeholder="请输入部署时设置的 SETUP_TOKEN" required></label><div v-if="loginError" class="form-error"><CircleAlert/>{{loginError}}</div><button class="btn primary auth-submit" :disabled="actionBusy"><LoaderCircle v-if="actionBusy" class="spin"/>{{needsSetup?'创建总台并进入':'登录工作台'}}<ArrowUpRight v-if="!actionBusy"/></button></form><div class="auth-foot"><span class="dot"/>{{needsSetup?'管理员可创建并管理代理账号':'设备与图片由服务器按账号隔离'}}</div>
      </section>
    </div><div class="auth-bottom">JK Monitor <span>·</span> 统一设备管理</div>
  </main>

  <div v-else class="app-shell">
    <div v-if="mobileNav" class="nav-scrim" @click="mobileNav=false"></div>
    <aside class="sidebar" :class="{expanded:mobileNav}"><div class="side-brand"><div class="brand-mark"><Activity/></div><div><strong>JK Monitor</strong><span>设备管理平台</span></div></div><div class="workspace"><div class="workspace-icon"><ShieldCheck v-if="isAdmin"/><Users v-else/></div><div><b>{{isAdmin?'总台控制台':'代理工作台'}}</b><span>{{isAdmin?'ADMIN WORKSPACE':'AGENT WORKSPACE'}}</span></div><ChevronRight/></div><div class="nav-caption">工作空间</div><nav><button v-for="t in tabs" :key="t.id" :class="{active:selectedID?t.id==='devices':nav===t.id}" @click="go(t.id)"><component :is="t.icon"/><span>{{t.name}}</span><i v-if="t.badge">{{t.badge}}</i></button></nav><div class="side-bottom"><div class="system-status"><span class="dot"/><span>设备接入服务</span><span>{{serverReachable?'已运行':'正在重连'}}</span></div><button class="help-button" @click="openModal('help')"><Server/><span>连接与使用说明</span><ArrowUpRight/></button><div class="side-user"><div class="avatar">{{user.display_name?.slice(0,1)||'U'}}</div><div><b>{{user.display_name}}</b><span>{{isAdmin?'总台管理员':'代理账号'}}</span></div><button class="icon-btn" title="退出登录" aria-label="退出登录" @click="logout"><LogOut/></button></div></div></aside>

    <div class="main-shell"><header class="topbar"><div class="breadcrumb"><button class="icon-btn mobile-only" aria-label="打开导航" @click="mobileNav=true"><Menu/></button><span>{{isAdmin?'总台':'我的工作台'}}</span><ChevronRight/><b>{{pageTitle}}</b></div><div class="top-actions"><span class="top-date">{{day}}</span><span class="separator"></span><button class="icon-btn" title="修改密码" aria-label="修改密码" @click="openModal('password')"><KeyRound/></button><div class="small-avatar">{{user.display_name?.slice(0,1)}}</div></div></header>
      <main class="content"><div v-if="!serverReachable" class="connection-warning" role="status"><CircleAlert/>与服务器连接中断，当前状态可能不是最新，正在自动重试。</div>
      <DeviceDetail v-if="selectedID" :key="selectedID" :device="detail" :images="history" :image-pagination="imagePagination" :history-busy="historyBusy" :clear-history-busy="clearHistoryBusy" :history-revision="historyRevision" :error="detailError" :action-busy="actionBusy" :is-admin="isAdmin" @back="closeDetail" @refresh="refreshDetail" @history-page="loadHistoryPage" @clear-history="openClearImageHistory" @command="sendCommand" @copy="copy" @rename="openModal('rename',{id:detail.id,name:detail.name,note:detail.note})" @assign="openModal('assign',{ids:[detail.id]})" @reclaim="reclaim(detail)" @toggle="toggleDevice(detail)"/>
      <template v-else><div class="page-heading"><div><div class="eyebrow">{{nav==='overview'?'WORKSPACE OVERVIEW':nav==='agents'?'AGENT MANAGEMENT':nav==='commands'?'TASK ACTIVITY':nav==='audit'?'AUDIT LOG':'DEVICE MANAGEMENT'}}</div><h1>{{nav==='overview'?(isAdmin?'设备运行概览':'我的设备概览'):pageTitle}}<span v-if="nav==='pending'" class="count-pill">{{stats.unassigned||0}}</span></h1><p>{{nav==='overview'?'所有连接与任务，在这里一目了然。':nav==='pending'?'新设备首先进入总台，下发后代理即可查看和操作。':nav==='agents'?'创建代理账号，为每个人分配独立的设备访问权限。':nav==='commands'?'跟踪手机的接收确认、执行状态与返回结果。':nav==='audit'?'每一次账号和设备管理操作都有记录。':'查看设备状态、管理归属，或打开设备进行远程采集。'}}</p></div><div class="heading-actions"><button class="btn" :disabled="loading" @click="loadData()"><RefreshCw :class="{spin:loading}"/>刷新</button><button v-if="nav==='agents'" class="btn primary" @click="openModal('agent')"><Plus/>创建代理</button></div></div>

      <template v-if="nav==='overview'">
        <section class="stat-grid"><div class="stat-card"><div class="stat-label">设备总数<Smartphone/></div><div class="stat-number">{{stats.total||0}}<span>台</span></div><div class="stat-note">{{isAdmin?'所有已登记的设备':'当前分配给你的设备'}}</div></div><div class="stat-card"><div class="stat-label">在线设备<div class="stat-icon green"><Wifi/></div></div><div class="stat-number">{{stats.online||0}}<span>台</span></div><div class="stat-note"><span class="dot"/>当前连接正常</div></div><div class="stat-card"><div class="stat-label">{{isAdmin?'待分配设备':'离线设备'}}<div class="stat-icon amber"><Link v-if="isAdmin"/><WifiOff v-else/></div></div><div class="stat-number">{{isAdmin?(stats.unassigned||0):(stats.offline||0)}}<span>台</span></div><button v-if="isAdmin" class="stat-link" @click="go('pending')">前往分配<ArrowUpRight/></button><div v-else class="stat-note">恢复连接后自动更新</div></div><div class="stat-card"><div class="stat-label">实时采集任务<div class="stat-icon purple"><Radio/></div></div><div class="stat-number">{{stats.active_tasks||0}}<span>个</span></div><div class="stat-note">包含待确认与执行中的任务</div></div></section>
        <section class="overview-grid"><div class="card device-panel"><div class="card-heading"><div><h2>设备动态<span class="live-chip"><span class="dot"/>自动更新</span></h2><p>最近接入的设备与连接状态</p></div><button class="text-button" @click="go('devices')">查看全部<ChevronRight/></button></div><div class="tab-line"><button :class="{active:overviewTab==='all'}" @click="overviewTab='all'">全部设备</button><button :class="{active:overviewTab==='online'}" @click="overviewTab='online'">在线</button><button v-if="isAdmin" :class="{active:overviewTab==='unassigned'}" @click="overviewTab='unassigned'">待分配</button></div><div v-if="!devices.length" class="empty-state"><div class="empty-icon"><Smartphone/></div><h3>等待第一台设备连接</h3><p>手机 App 加载新版 dylib 后自动连接，<br>上线后设备会自动出现在这里。</p><button class="btn" @click="openModal('help')">查看接入说明<ArrowUpRight/></button></div><div v-else class="activity-list"><a v-for="d in devices.slice(0,6)" :key="d.id" class="activity-device" :href="deviceHref(d.id)" @click.exact.prevent="openDevice(d)"><div class="phone-icon"><Smartphone/></div><div class="activity-name"><b>{{d.name}}</b><span>{{d.id}}</span></div><span class="badge" :class="d.connected?'green':'neutral'"><span class="dot"/>{{!d.enabled?'已停用':d.connected?'在线':'无连接'}}</span><div class="activity-meta"><b>{{d.agent_name||'总台待分配'}}</b><span>{{age(d.last_seen_at)}}</span></div><ChevronRight/></a></div></div>
          <div class="overview-side"><div class="card connection-card"><div class="connection-art"><div class="connection-orbit"></div><div class="connection-node"><Smartphone/></div><span class="connection-spark"><Check/></span></div><span class="eyebrow">DEVICE WORKFLOW</span><h2>连接之后，各归其位。</h2><p>设备统一接入总台，分配给代理后，<br>对应账号即可查看自己的设备。</p><div class="workflow"><span><i>1</i>设备上线</span><ChevronRight/><span><i>2</i>总台分配</span><ChevronRight/><span><i>3</i>代理查看</span></div><button v-if="isAdmin" class="btn" @click="go('pending')">管理待分配设备<ArrowUpRight/></button></div><div class="card tip-card"><div class="tip-icon"><Radio/></div><div><h3>采集任务交给手机</h3><p>手机确认任务后，关闭网页也会继续。熄屏或 App 不活动时暂停，恢复活动后继续。</p></div></div></div></section>
      </template>

      <section v-if="nav==='devices'||nav==='pending'" class="card table-card"><div class="table-toolbar"><div class="search-field"><Search/><input v-model="search" placeholder="搜索设备名称、ID 或备注" aria-label="搜索设备"></div><div class="toolbar-actions"><select v-if="nav!=='pending'" v-model="filter" aria-label="筛选设备状态"><option value="all">全部状态</option><option value="online">在线设备</option><option value="offline">当前无连接</option><option v-if="isAdmin" value="unassigned">待分配</option></select><button v-if="isAdmin" class="btn primary" :disabled="!selected.length" @click="openModal('assign',{ids:[...selected]})"><Link/>分配设备<span v-if="selected.length">{{selected.length}}</span></button></div></div><div class="table-wrap"><table><thead><tr><th v-if="isAdmin" class="checkbox-cell"><input type="checkbox" aria-label="选择本页全部设备" :checked="selected.length===devices.length&&devices.length>0" @change="toggleAll"></th><th>设备信息</th><th>连接状态</th><th>{{isAdmin?'所属代理':'设备备注'}}</th><th>最近联系</th><th>采集状态</th><th class="align-right">操作</th></tr></thead><tbody><tr v-for="d in devices" :key="d.id"><td v-if="isAdmin" class="checkbox-cell"><input type="checkbox" :aria-label="'选择设备 '+d.name" :checked="selected.includes(d.id)" @change="toggleSelection(d.id)"></td><td><div class="device-cell"><div class="phone-icon"><Smartphone/></div><div><a class="device-title" :href="deviceHref(d.id)" @click.exact.prevent="openDevice(d)">{{d.name}}</a><span class="mono">{{shortID(d.id)}}<i>·</i>{{d.platform||'iOS'}} {{d.os_version}}</span></div></div></td><td><span class="badge" :class="!d.enabled?'red':d.connected?'green':'neutral'"><span class="dot"/>{{!d.enabled?'已停用':d.connected?'在线':'无连接'}}</span></td><td><span v-if="isAdmin" :class="{'pending-label':!d.agent_id}">{{d.agent_name||'待分配'}}</span><span v-else class="muted">{{d.note||'—'}}</span></td><td><span>{{age(d.last_seen_at)}}</span><small>{{date(d.last_seen_at,true)}}</small></td><td><span class="capture-label"><Radio v-if="d.active_session_id"/><span>{{d.active_session_id?'有实时任务':'待命'}}</span></span></td><td><div class="row-actions"><a class="text-button" :href="deviceHref(d.id)" @click.exact.prevent="openDevice(d)">查看<ArrowUpRight/></a><button v-if="isAdmin" class="text-button secondary" @click="openModal('assign',{ids:[d.id]})">{{d.agent_id?'转移':'分配'}}</button></div></td></tr></tbody></table></div><div v-if="!devices.length" class="empty-state"><div class="empty-icon"><Search v-if="search"/><Link v-else-if="nav==='pending'"/><Smartphone v-else/></div><h3>{{search?'没有找到匹配的设备':nav==='pending'?'暂时没有待分配设备':'暂时没有设备'}}</h3><p>{{search?'试试其他名称、设备 ID 或备注。':nav==='pending'?'新设备接入后会自动进入此列表。':isAdmin?'设备首次连接后会自动登记。':'总台分配设备后，会自动出现在这里。'}}</p></div><div class="table-footer"><span>共 {{total}} 台设备<span v-if="selected.length"> · 已选择 {{selected.length}} 台</span></span><div><button class="icon-btn" :disabled="page<=1" aria-label="上一页" @click="page--"><ChevronLeft/></button><span>第 {{page}} 页</span><button class="icon-btn" :disabled="page*50>=total" aria-label="下一页" @click="page++"><ChevronRight/></button></div></div></section>

      <section v-if="nav==='agents'" class="card table-card"><div class="card-heading"><div><h2>代理账号 <span class="count-label">{{agents.length}}</span></h2><p>每个代理仅能访问自己名下的设备与数据</p></div></div><div class="table-wrap"><table><thead><tr><th>代理名称</th><th>登录账号</th><th>账号状态</th><th>名下设备</th><th>创建时间</th><th class="align-right">操作</th></tr></thead><tbody><tr v-for="a in agents" :key="a.id"><td><div class="agent-cell"><div class="avatar light">{{a.display_name.slice(0,1)}}</div><b>{{a.display_name}}</b></div></td><td class="mono">{{a.username}}</td><td><span class="badge" :class="a.enabled?'green':'red'"><span class="dot"/>{{a.enabled?'正常':'已禁用'}}</span></td><td><b>{{a.devices}}</b><span class="muted"> 台</span></td><td>{{date(a.created_at,true)}}</td><td><div class="row-actions"><button class="text-button" @click="openModal('reset',{id:a.id,name:a.display_name})">重置密码</button><button class="text-button" :class="a.enabled?'danger':''" @click="toggleAgent(a)">{{a.enabled?'禁用':'启用'}}</button></div></td></tr></tbody></table></div><div v-if="!agents.length" class="empty-state"><div class="empty-icon"><Users/></div><h3>创建第一个代理账号</h3><p>代理登录后，即可管理总台分配给自己的设备。</p><button class="btn primary" @click="openModal('agent')"><Plus/>创建代理</button></div></section>

      <section v-if="nav==='commands'" class="card table-card"><div class="card-heading"><div><h2>采集与控制任务</h2><p>指令入库后交由手机执行，不依赖网页持续打开</p></div><span class="live-chip"><span class="dot"/>每 3 秒更新</span></div><div class="table-wrap"><table><thead><tr><th>任务类型</th><th>目标设备</th><th>状态</th><th>创建时间</th><th>手机确认</th><th>结果说明</th></tr></thead><tbody><tr v-for="c in commands" :key="c.id"><td><div class="task-cell"><div class="task-icon"><MapPin v-if="c.type==='GET_LOCATION'"/><Radio v-else-if="c.type.includes('SESSION')"/><Camera v-else/></div><div><b>{{commandNames[c.type]||c.type}}</b><small class="mono">{{shortID(c.id)}}</small></div></div></td><td><a class="mono text-button" :href="deviceHref(c.device_id)" @click.exact.prevent="openDevice({id:c.device_id})">{{shortID(c.device_id)}}<ArrowUpRight/></a></td><td><span class="badge" :class="stateTone(c.status)">{{statusNames[c.status]||c.status}}</span></td><td>{{date(c.created_at,true)}}</td><td>{{c.acked_at?date(c.acked_at):'等待回复'}}</td><td class="muted result-cell">{{c.error_message||(c.status==='succeeded'?(c.type==='START_IMAGE_SESSION'?'手机已确认任务':c.type==='STOP_IMAGE_SESSION'?'手机已确认停止':'数据已保存'):'—')}}</td></tr></tbody></table></div><div v-if="!commands.length" class="empty-state"><div class="empty-icon"><Command/></div><h3>暂无任务记录</h3><p>从设备详情发起截图或定位请求后，在这里跟踪结果。</p></div></section>

      <section v-if="nav==='audit'" class="card table-card"><div class="card-heading"><div><h2>最近操作</h2><p>记录账号、设备归属和采集请求的管理操作</p></div><span class="count-label">最近 100 条</span></div><div class="table-wrap"><table><thead><tr><th>操作内容</th><th>操作人</th><th>目标编号</th><th>操作时间</th></tr></thead><tbody><tr v-for="a in audits" :key="a.id"><td><span class="audit-action"><ShieldCheck/>{{auditNames[a.action]||a.action}}</span></td><td>{{a.actor_name||'系统'}}</td><td class="mono">{{a.target_id}}</td><td>{{date(a.created_at,true)}}</td></tr></tbody></table></div><div v-if="!audits.length" class="empty-state"><div class="empty-icon"><ShieldCheck/></div><h3>暂无操作记录</h3><p>后续管理操作会自动记录在这里。</p></div></section>
      </template>
      <footer class="page-footer"><span>JK Monitor <i>·</i> 设备管理平台</span><span><span class="dot"/>数据按账号权限展示 <span class="footer-clock">{{date(stats.server_time)}} CST</span></span></footer>
      </main>
    </div>
  </div>

  <dialog ref="modalEl" class="modal" @cancel.prevent="closeModal"><template v-if="modal"><header class="modal-header"><div><span class="eyebrow">{{modal.type==='assign'?'DEVICE ASSIGNMENT':modal.type==='agent'?'NEW AGENT':modal.type==='password'?'ACCOUNT SECURITY':'JK MONITOR'}}</span><h2>{{({agent:'创建代理账号',assign:'分配设备',reset:'重置代理密码',rename:'编辑设备信息',password:requiresPasswordChange?'修改初始密码':'修改登录密码',help:'连接与使用说明'})[modal.type]||modal.title}}</h2></div><button v-if="!requiresPasswordChange" type="button" class="icon-btn" aria-label="关闭对话框" @click="closeModal"><X/></button></header>
    <form v-if="['agent','assign','reset','rename','password'].includes(modal.type)" @submit.prevent="modalSubmit"><div class="modal-body"><template v-if="modal.type==='agent'"><p class="modal-description">创建后，将账号和初始密码交给对应代理。</p><label>代理名称<input v-model.trim="form.display_name" placeholder="例如：华东运营组" maxlength="40" required></label><label>登录账号<input v-model.trim="form.username" placeholder="3 至 32 位字母、数字或 ._-" pattern="[A-Za-z0-9_.\-]{3,32}" autocomplete="off" required></label><label>初始密码<div class="password-generator"><input v-model="form.password" placeholder="6 位字母或数字" minlength="6" maxlength="6" pattern="[A-Za-z0-9]{6}" autocomplete="new-password" required><button type="button" @click="randomPassword">随机生成</button></div></label><div class="info-box"><ShieldCheck/>代理密码固定为 6 位字母或数字，登录后无需强制修改。</div></template>
      <template v-if="modal.type==='assign'"><div class="assignment-summary"><div class="phone-icon"><Smartphone/></div><div><b>已选择 {{modal.ids.length}} 台设备</b><span>分配后，对应代理即可看到自己的设备</span></div></div><label>接收代理<select v-model="form.agent_id" required><option disabled value="">请选择代理账号</option><option v-for="a in agents.filter(a=>a.enabled)" :key="a.id" :value="a.id">{{a.display_name}} · {{a.username}}</option></select></label><div v-if="!agents.some(a=>a.enabled)" class="info-box"><CircleAlert/>请先在代理管理中创建一个正常账号。</div><p class="modal-description">转移已分配设备时，原代理的访问权限会立即撤销，旧采集任务会收到停止指令。</p></template>
      <template v-if="modal.type==='reset'"><p class="modal-description">为「{{modal.name}}」设置新的初始密码，原登录会话将失效。</p><label>新初始密码<div class="password-generator"><input v-model="form.password" placeholder="6 位字母或数字" minlength="6" maxlength="6" pattern="[A-Za-z0-9]{6}" autocomplete="new-password" required><button type="button" @click="randomPassword">随机生成</button></div></label></template>
      <template v-if="modal.type==='rename'"><label>设备名称<input v-model.trim="form.name" maxlength="100" required></label><label>备注<textarea v-model="form.note" maxlength="500" rows="3" placeholder="填写设备用途或便于识别的信息"></textarea></label></template>
      <template v-if="modal.type==='password'"><div v-if="requiresPasswordChange" class="info-box"><KeyRound/>请先修改初始密码，再进入工作台。</div><label>当前密码<input v-model="form.current_password" type="password" autocomplete="current-password" maxlength="128" required></label><label>新密码<input v-model="form.new_password" type="password" autocomplete="new-password" :minlength="passwordMinLength" :maxlength="passwordMaxLength" :pattern="passwordPattern" :placeholder="passwordHint" required></label><label>确认新密码<input v-model="form.confirm_password" type="password" autocomplete="new-password" :minlength="passwordMinLength" :maxlength="passwordMaxLength" :pattern="passwordPattern" required></label></template>
      <div v-if="modalError" class="form-error"><CircleAlert/>{{modalError}}</div></div><footer class="modal-footer"><button v-if="!requiresPasswordChange" type="button" class="btn" @click="closeModal">取消</button><button class="btn primary" :disabled="modalBusy"><LoaderCircle v-if="modalBusy" class="spin"/>{{modal.type==='assign'?'确认分配':modal.type==='agent'?'创建账号':'保存'}}</button></footer></form>
    <template v-else-if="modal.type==='confirm'"><div class="modal-body"><p class="confirm-copy">{{modal.message}}</p><div v-if="modalError" class="form-error"><CircleAlert/>{{modalError}}</div></div><footer class="modal-footer"><button class="btn" @click="closeModal">取消</button><button class="btn primary" :disabled="modalBusy" @click="modalSubmit"><LoaderCircle v-if="modalBusy" class="spin"/>确认</button></footer></template>
    <template v-else-if="modal.type==='help'"><div class="modal-body help-content"><div class="info-box"><Server/>手机端只需要服务器地址，不填写代理账号。</div><h3>设备接入</h3><p>将服务器部署为 HTTPS。手机 App 加载 dylib 后，3 秒后自动连接内置的 wxjk.jk92.cc，并自动上报首张图；不再弹出地址框。</p><h3>分配与查看</h3><p>总台在待分配列表选择设备，下发给代理。代理登录自己的账号后，即可查看对应设备。</p><h3>手机自主采集</h3><p>开始实时采集后，等待手机确认。任务由手机保存并执行，关闭网页不会停止。App 不活动时暂停，恢复后继续；需要结束时点击停止任务。</p><h3>当前采集能力</h3><p>图片是你自己 App 当前活动窗口的 PNG。定位仅在手机端声明支持后开放；图片与位置均按设备归属授权。</p><div v-if="isLocalHTTP" class="info-box amber"><CircleAlert/>当前网页为 HTTP 本地预览，手机端正式接入需要 HTTPS 地址。</div></div><footer class="modal-footer"><button class="btn primary" @click="closeModal">知道了</button></footer></template>

  </template></dialog>
  <div v-if="toast" class="toast" :class="{error:toast.error}" role="status"><CircleAlert v-if="toast.error"/><CircleCheck v-else/>{{toast.message}}</div>
</template>
