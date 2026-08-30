import {CallMedia,normalizeDialTarget} from "/assets/call-audio.js";

const state={csrf:"",socket:null,snapshot:null,diagnostics:new Map(),runtime:null,lineCatalog:null,providerConfig:null,egressConfig:null,egressDraft:null,egressApply:null,diagnosticSnapshot:null,egressDiagnosticSnapshot:null,egressDiagnosticResults:new Map(),euiccs:null,euiccLoading:false,euiccDownloads:new Map(),view:"overview",pendingMessage:null,messageSending:false,currentCall:null,providerStatuses:new Map(),cellularStatuses:new Map(),cellularData:new Map(),callStatusLoading:false,callHistory:[],callHistoryLoading:false,dtmfSending:false,dtmfEcho:""};
const el=id=>document.getElementById(id);
const loginPanel=el("login-panel"),consolePanel=el("console"),notice=el("notice"),connection=el("connection");

function badgeClass(ok,warning=false){return ok?"good":warning?"warn":"bad"}
function setConnection(text,kind="neutral"){connection.textContent=text;connection.className=`badge ${kind}`}
function showNotice(message){notice.textContent=message;notice.classList.toggle("hidden",!message)}
function operationID(prefix){return `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2,8)}`}
function fmtTime(value){if(!value)return"—";const date=new Date(value);return Number.isNaN(date.valueOf())?String(value):date.toLocaleString()}

async function jsonRequest(path,options={}){
  const headers=new Headers(options.headers||{});headers.set("Accept","application/json");
  if(options.body){headers.set("Content-Type","application/json");headers.set("X-MDD-CSRF-Token",state.csrf)}
  const response=await fetch(path,{...options,headers,credentials:"same-origin"});
  const text=await response.text();let payload={};try{payload=text?JSON.parse(text):{}}catch{payload={detail:text}}
  if(!response.ok){const error=new Error(payload.code||payload.detail||`HTTP ${response.status}`);error.status=response.status;error.code=payload.code||"";error.kind=payload.kind||"";error.layer=payload.layer||"";error.detail=payload.detail||"";throw error}
  return payload;
}

async function initialize(){
  try{const status=await jsonRequest("/api/auth/status");if(status.authenticated){state.csrf=status.csrf;openConsole();return}}
  catch(error){el("login-error").textContent=`认证服务不可用：${error.message}`}
  loginPanel.classList.remove("hidden");
}

el("login-form").addEventListener("submit",async event=>{
  event.preventDefault();el("login-error").textContent="";
  try{const result=await jsonRequest("/api/auth/login",{method:"POST",body:JSON.stringify({username:el("username").value,password:el("password").value})});state.csrf=result.csrf;el("password").value="";openConsole()}
  catch(error){el("login-error").textContent=error.message}
});

el("logout").addEventListener("click",async()=>{
  try{await jsonRequest("/api/auth/logout",{method:"POST",body:"{}"})}catch{}
  if(state.socket)state.socket.close();location.reload();
});

function openConsole(){loginPanel.classList.add("hidden");consolePanel.classList.remove("hidden");el("logout").classList.remove("hidden");restorePendingMessage();connectState();loadRuntime();loadLineCatalog();loadProviderConfig();loadEgressConfig();loadDiagnostics();loadEUICCs()}
function connectState(){
  if(state.socket)state.socket.close();
  const scheme=location.protocol==="https:"?"wss":"ws";const socket=new WebSocket(`${scheme}://${location.host}/v1/browser/ws`);state.socket=socket;
  setConnection("连接中","warn");
  socket.onopen=()=>{setConnection("实时已连接","good");renderClientChecks()};
  socket.onmessage=event=>{try{const snapshot=JSON.parse(event.data);if(snapshot.type!=="browser.snapshot")throw new Error("unknown snapshot");state.snapshot=snapshot;render(snapshot);renderClientChecks()}catch(error){showNotice(`状态数据无效：${error.message}`)}};
  socket.onerror=()=>{setConnection("实时连接异常","bad");renderClientChecks()};
  socket.onclose=event=>{if(state.socket!==socket)return;setConnection(event.code===4401?"登录已过期":"实时已断开","bad");renderClientChecks();setTimeout(()=>{if(state.socket===socket)connectState()},3000)};
}

for(const button of document.querySelectorAll("[data-view]")){button.addEventListener("click",()=>selectView(button.dataset.view))}
el("refresh-diagnostics").addEventListener("click",loadDiagnostics);
el("refresh-egress-diagnostics").addEventListener("click",loadEgressDiagnostics);
el("refresh-call-status").addEventListener("click",refreshCallStatuses);
el("call-form").addEventListener("submit",beginCall);
el("call-end").addEventListener("click",hangupCall);
el("call-mute").addEventListener("click",toggleCallMute);
el("call-backspace").addEventListener("click",()=>{if(!state.currentCall)el("call-number").value=el("call-number").value.slice(0,-1)});
for(const button of document.querySelectorAll("[data-dial-key]")){button.addEventListener("click",()=>{if(!state.currentCall)el("call-number").value+=button.dataset.dialKey})}
for(const button of document.querySelectorAll("[data-dtmf-key]")){button.addEventListener("click",()=>sendCallDTMF(button.dataset.dtmfKey))}
el("refresh-call-history").addEventListener("click",loadCallHistory);
el("clear-call-history").addEventListener("click",clearCallHistory);
el("message-form").addEventListener("submit",sendMessage);
el("message-discard").addEventListener("click",discardPendingMessage);
el("message-line").addEventListener("change",refreshSelectedCellularMessages);
el("message-refresh").addEventListener("click",refreshSelectedCellularMessages);
el("refresh-euiccs").addEventListener("click",loadEUICCs);
el("refresh-line-config").addEventListener("click",loadLineCatalog);
el("line-config-line").addEventListener("change",renderLineEditor);
el("line-config-enabled").addEventListener("change",syncLineIdentityRequirements);
el("line-config-form").addEventListener("submit",saveLineConfig);
el("refresh-provider-config").addEventListener("click",loadProviderConfig);
el("apply-provider-config").addEventListener("click",applyProviderConfig);
el("refresh-egress-config").addEventListener("click",loadEgressConfig);
el("save-egress-config").addEventListener("click",saveEgressConfig);
el("apply-egress-config").addEventListener("click",applyEgressConfig);
el("add-egress-profile").addEventListener("click",addEgressProfile);
el("add-egress-exit").addEventListener("click",addEgressExit);
el("egress-config-enabled").addEventListener("change",event=>{if(state.egressDraft)state.egressDraft.enabled=event.target.checked});
window.addEventListener("pagehide",()=>state.currentCall?.media?.close());
window.addEventListener("keydown",event=>{if(!state.currentCall||state.currentCall.phase!=="active"||event.metaKey||event.ctrlKey||event.altKey||/^(INPUT|TEXTAREA|SELECT)$/.test(event.target?.tagName||""))return;if(/^[0-9*#]$/.test(event.key)){event.preventDefault();void sendCallDTMF(event.key)}});

function selectView(view){state.view=view;for(const button of document.querySelectorAll("[data-view]"))button.classList.toggle("active",button.dataset.view===view);for(const section of document.querySelectorAll(".view"))section.classList.toggle("hidden",section.id!==`view-${view}`);if(view==="settings"){if(!state.runtime)loadRuntime();loadLineCatalog();loadProviderConfig();loadEgressConfig()}if(view==="diagnostics")loadDiagnostics();if(view==="calls"){refreshCallStatuses();loadCallHistory()}if(view==="messages")refreshSelectedCellularMessages();if(view==="esim")loadEUICCs()}

async function loadEUICCs(){if(state.euiccLoading)return;state.euiccLoading=true;const button=el("refresh-euiccs");button.disabled=true;try{const payload=await jsonRequest("/v1/euiccs");state.euiccs=Array.isArray(payload.euiccs)?payload.euiccs:[];renderEUICCs()}catch(error){state.euiccs=null;el("euiccs").replaceChildren(errorCard(`eUICC 清单读取失败：${error.code||error.message}`))}finally{state.euiccLoading=false;button.disabled=false}}

function renderEUICCs(){
  const root=el("euiccs");root.replaceChildren();const entries=state.euiccs||[];
  if(!entries.length){root.append(empty("当前没有已识别的 eUICC"));return}
  for(const entry of entries){
    const card=document.createElement("article");card.className="card";card.dataset.eid=entry.euicc.eid;
    const head=document.createElement("div");head.className="card-head";
    const title=document.createElement("div"),heading=document.createElement("h3"),meta=document.createElement("div"),capabilities=document.createElement("div");
    heading.textContent=`${entry.slot_label?`${entry.slot_label} · `:""}EID ${entry.euicc.eid}`;meta.className="muted";meta.textContent=`${entry.agent_id} · ${entry.reader_name}${entry.slot_id?` · ${entry.slot_id}`:""} · 插入代际 ${entry.session_generation}`;
    capabilities.className="badges";capabilities.append(euiccCapability(entry.euicc.profile_management,"Profile 管理"),euiccCapability(entry.euicc.profile_download,"Profile 下载"),euiccCapability(entry.euicc.profile_discovery,"SM-DS 查询"),euiccCapability(entry.euicc.notification_inventory,"卡内通知"),euiccCapability(entry.euicc.notification_delivery,"通知发送"),euiccCapability(entry.euicc.notification_removal,"确认后移除"));
    title.append(heading,meta);head.append(title,capabilities);card.append(head);
    const toolbar=document.createElement("div");toolbar.className="toolbar";
    const download=actionButton("下载 Profile",()=>showEUICCDownloadForm(entry,card));download.disabled=!entry.euicc.profile_download;const discovery=actionButton("查询待下载 Profile",()=>showEUICCDiscoveryForm(entry,card));discovery.className="secondary";discovery.disabled=!entry.euicc.profile_discovery;const notifications=actionButton("查看卡内通知",()=>loadEUICCNotifications(entry,card,notifications));notifications.className="secondary";notifications.dataset.notificationInventoryButton="";notifications.disabled=!entry.euicc.notification_inventory;toolbar.append(download,discovery,notifications);card.append(toolbar);
    const profiles=document.createElement("div");profiles.className="readers";
    if(!entry.euicc.profiles_available){profiles.append(empty("Profile 清单当前不可用"))}
    else if(!(entry.euicc.profiles||[]).length){profiles.append(empty("空白 eUICC：没有 Profile"))}
    else{for(const profile of entry.euicc.profiles){profiles.append(euiccProfileRow(entry,profile))}}
    card.append(profiles);
    const result=document.createElement("div");result.className="result hidden";result.dataset.euiccResult="";card.append(result);
    const remembered=state.euiccDownloads.get(entry.euicc.eid),reported=entry.euicc.download;
    if(reported&&(!remembered||reported.operation_id===remembered.operation_id)){state.euiccDownloads.set(entry.euicc.eid,reported);saveEUICCDownloads()}
    renderEUICCDownloadStatus(card,entry);
    root.append(card)
  }
}

function euiccCapability(enabled,label){const badge=document.createElement("span");badge.className=`badge ${enabled?"good":"neutral"}`;badge.textContent=enabled?label:`${label}只读`;return badge}

function euiccProfileRow(entry,profile){const row=document.createElement("div");row.className="reader";const head=document.createElement("div");head.className="card-head";const identity=document.createElement("div"),name=document.createElement("strong"),meta=document.createElement("div"),stateBadge=document.createElement("span");name.textContent=profile.nickname||profile.profile_name||profile.service_provider_name||profile.iccid;meta.className="muted";meta.textContent=[profile.iccid,profile.service_provider_name,profile.profile_name].filter((value,index,array)=>value&&array.indexOf(value)===index).join(" · ");stateBadge.className=`badge ${profile.state==="enabled"?"good":"neutral"}`;stateBadge.textContent=profile.state==="enabled"?"已启用":"已停用";identity.append(name,meta);head.append(identity,stateBadge);row.append(head);const action=profile.state==="enabled"?"disable":"enable",button=document.createElement("button");button.className=action==="disable"?"secondary":"";button.textContent=action==="disable"?"停用 Profile":"启用 Profile";button.disabled=!entry.euicc.profile_management;button.addEventListener("click",()=>changeEUICCProfile(entry,profile,action,button));const rename=document.createElement("button");rename.className="secondary";rename.textContent="修改昵称";rename.disabled=!entry.euicc.profile_management;rename.addEventListener("click",()=>renameEUICCProfile(entry,profile,rename));const toolbar=document.createElement("div");toolbar.className="toolbar";toolbar.append(button,rename);row.append(toolbar);return row}

async function changeEUICCProfile(entry,profile,action,button){const verb=action==="enable"?"启用":"停用",result=button.closest(".card").querySelector("[data-euicc-result]");if(!confirm(`${verb} Profile ${profile.iccid}？\n\n操作只会发送到当前 EID、ICCID 和插入代际完全匹配的 Agent。`))return;button.disabled=true;result.classList.remove("hidden");result.style.color="#344054";result.textContent=`正在${verb}；等待 Agent 返回明确提交结果…`;try{const payload=await jsonRequest(`/v1/euiccs/${encodeURIComponent(entry.euicc.eid)}/profiles/${encodeURIComponent(profile.iccid)}/${action}`,{method:"POST",body:JSON.stringify({operation_id:operationID(`ui-euicc-${action}`),expected_state:profile.state})});if(payload.outcome==="already_applied"){result.textContent=`当前状态已经是${verb}后的目标状态；没有重复写卡。`}else if(payload.outcome==="refresh_pending"){result.textContent=`写卡命令已提交；Agent 正在重读该卡片。请刷新确认新状态。`}else{result.style.color="#925c00";result.textContent="写卡结果不确定；Agent 正在重读该卡片。不要更换目标后重试，请先刷新当前状态。"}setTimeout(loadEUICCs,1500)}catch(error){result.style.color="#b42318";result.textContent=`${verb}失败：${error.code||error.message}`;button.disabled=false}}

function openEUICCNicknameDialog(profile){return new Promise(resolve=>{const backdrop=document.createElement("div"),dialog=document.createElement("form"),heading=document.createElement("h3"),identity=document.createElement("div"),label=document.createElement("label"),input=document.createElement("input"),hint=document.createElement("span"),error=document.createElement("div"),actions=document.createElement("div"),save=document.createElement("button"),cancel=document.createElement("button");backdrop.className="euicc-modal-backdrop";dialog.className="panel euicc-modal";dialog.setAttribute("role","dialog");dialog.setAttribute("aria-modal","true");heading.textContent="修改 Profile 昵称";identity.className="muted";identity.textContent=profile.iccid;input.value=profile.nickname||"";input.placeholder="留空可清除昵称";hint.className="muted";hint.textContent="最多 64 个 UTF-8 字节；保存前仍会核对卡片上的旧昵称。";label.append(document.createTextNode("昵称"),input,hint);error.className="error";actions.className="toolbar";save.type="submit";save.textContent="保存";cancel.type="button";cancel.className="secondary";cancel.textContent="取消";actions.append(save,cancel);dialog.append(heading,identity,label,error,actions);backdrop.append(dialog);document.body.append(backdrop);let settled=false;const finish=value=>{if(settled)return;settled=true;backdrop.remove();resolve(value)};cancel.addEventListener("click",()=>finish(null));backdrop.addEventListener("click",event=>{if(event.target===backdrop)finish(null)});dialog.addEventListener("keydown",event=>{if(event.key==="Escape"){event.preventDefault();finish(null)}});dialog.addEventListener("submit",event=>{event.preventDefault();const nickname=input.value.trim();if(new TextEncoder().encode(nickname).length>64){error.textContent="Profile 昵称不能超过 64 个 UTF-8 字节";return}finish(nickname)});input.focus()})}

async function renameEUICCProfile(entry,profile,button){const nickname=await openEUICCNicknameDialog(profile);if(nickname===null)return;const result=button.closest(".card").querySelector("[data-euicc-result]");button.disabled=true;result.classList.remove("hidden");result.style.color="#344054";result.textContent="正在修改昵称；等待 Agent 返回明确提交结果…";try{const payload=await jsonRequest(`/v1/euiccs/${encodeURIComponent(entry.euicc.eid)}/profiles/${encodeURIComponent(profile.iccid)}/nickname`,{method:"POST",body:JSON.stringify({operation_id:operationID("ui-euicc-nickname"),nickname,expected_nickname:profile.nickname||""})});if(payload.outcome==="already_applied"){result.textContent="卡片上的昵称已经相同；没有重复写卡。"}else if(payload.outcome==="refresh_pending"){result.textContent="昵称命令已提交；Agent 正在重读该卡片。"}else{result.style.color="#925c00";result.textContent="昵称写入结果不确定；请先刷新卡片状态，不要直接重试。"}setTimeout(loadEUICCs,1500)}catch(error){result.style.color="#b42318";result.textContent=`修改昵称失败：${error.code||error.message}`;button.disabled=false}}

function showEUICCDiscoveryForm(entry,card){const prior=card.querySelector("[data-euicc-discovery-form]");if(prior){prior.remove();return}const form=document.createElement("form"),smds=document.createElement("input"),imei=document.createElement("input"),hint=document.createElement("div"),actions=document.createElement("div"),submit=document.createElement("button"),cancel=document.createElement("button");form.dataset.euiccDiscoveryForm="";form.className="panel";smds.placeholder="SM-DS（留空使用 lpa.ds.gsma.com）";smds.autocomplete="off";imei.placeholder="IMEI（可选，15 位数字）";imei.inputMode="numeric";imei.maxLength=15;hint.className="muted";hint.textContent="仅查询该 EID 的待处理事件；不会下载、写卡、保存参数或自动重试。最长等待 120 秒。";submit.type="submit";submit.textContent="开始查询";cancel.type="button";cancel.className="secondary";cancel.textContent="取消";actions.className="toolbar";actions.append(submit,cancel);form.append(smds,imei,hint,actions);card.querySelector("[data-euicc-result]").before(form);cancel.addEventListener("click",()=>form.remove());form.addEventListener("submit",async event=>{event.preventDefault();const identity=imei.value.trim();if(identity&&!/^\d{15}$/.test(identity)){showNotice("IMEI 必须为空或 15 位数字");return}submit.disabled=true;cancel.disabled=true;const result=card.querySelector("[data-euicc-result]");result.classList.remove("hidden");result.style.color="#344054";result.textContent="正在通过 Agent 和当前 eUICC 查询 SM-DS…";try{const payload=await jsonRequest(`/v1/euiccs/${encodeURIComponent(entry.euicc.eid)}/discovery`,{method:"POST",body:JSON.stringify({operation_id:operationID("ui-euicc-discovery"),smds:smds.value.trim(),imei:identity})});result.replaceChildren();const title=document.createElement("strong");title.textContent=`SM-DS ${payload.smds}：${payload.entries?.length||0} 个待处理事件`;result.append(title);for(const item of payload.entries||[]){const row=document.createElement("div");row.className="muted";row.textContent=`${item.rsp_server_address} · Event ${item.event_id}`;result.append(row)}form.remove()}catch(error){result.style.color="#b42318";result.textContent=`SM-DS 查询失败：${error.code||error.message}`;submit.disabled=false;cancel.disabled=false}})}

async function loadEUICCNotifications(entry,card,button){const result=card.querySelector("[data-euicc-result]");button.disabled=true;result.classList.remove("hidden");result.style.color="#344054";result.textContent="正在读取当前 eUICC 的卡内通知…";try{const payload=await jsonRequest(`/v1/euiccs/${encodeURIComponent(entry.euicc.eid)}/notifications`);const entries=payload.entries||[];result.replaceChildren();const title=document.createElement("strong");title.textContent=`卡内通知：${entries.length} 条`;result.append(title);for(const item of entries){const row=document.createElement("div"),text=document.createElement("span"),send=document.createElement("button");row.className="toolbar";text.className="muted";text.textContent=`#${item.sequence_number} · ${item.event}${item.iccid?` · ${item.iccid}`:""} · ${item.address}`;send.className="secondary";send.textContent="发送并确认移除";send.disabled=!entry.euicc.notification_delivery;send.addEventListener("click",()=>deliverEUICCNotification(entry,item,card,send));row.append(text,send);result.append(row)}}catch(error){result.style.color="#b42318";result.textContent=`卡内通知读取失败：${error.code||error.message}`}finally{button.disabled=false}}

async function deliverEUICCNotification(entry,item,card,button){if(!confirm(`发送卡内通知 #${item.sequence_number} 到 ${item.address}？\n\n仅在运营商服务器明确确认后才会从卡内移除；网络结果不确定时不会自动重发。`))return;const result=card.querySelector("[data-euicc-result]");button.disabled=true;result.style.color="#344054";result.textContent=`正在发送通知 #${item.sequence_number}；不会自动重试…`;try{await jsonRequest(`/v1/euiccs/${encodeURIComponent(entry.euicc.eid)}/notifications/${encodeURIComponent(item.sequence_number)}/deliver`,{method:"POST",body:JSON.stringify({confirmed:true,event:item.event,iccid:item.iccid||"",address:item.address})});result.textContent=`通知 #${item.sequence_number} 已获运营商服务器确认并从卡内移除。`;const inventory=card.querySelector("[data-notification-inventory-button]");if(inventory)await loadEUICCNotifications(entry,card,inventory)}catch(error){result.style.color="#b42318";if(error.code==="euicc_notification_delivery_outcome_unknown"){result.textContent="发送结果不确定；系统没有自动重发。请先重新查看卡内通知，再决定是否操作。"}else if(error.code==="euicc_notification_acknowledged_not_removed"){showAcknowledgedNotificationRemoval(entry,item,card,result)}else{result.textContent=`通知发送失败：${error.code||error.message}`;button.disabled=false}}}

function showAcknowledgedNotificationRemoval(entry,item,card,result){result.replaceChildren();const text=document.createElement("span"),remove=document.createElement("button");text.textContent="运营商服务器已确认，但卡内通知未能移除。不要重发。";remove.className="secondary";remove.textContent="仅移除已确认记录";remove.disabled=!entry.euicc.notification_removal;remove.addEventListener("click",()=>removeAcknowledgedEUICCNotification(entry,item,card,remove));result.append(text,remove)}

async function removeAcknowledgedEUICCNotification(entry,item,card,button){if(!confirm(`仅从卡内移除已确认的通知 #${item.sequence_number}？\n\n该操作不会再次发送通知；只应用于运营商服务器已经明确确认的本次结果。`))return;button.disabled=true;const result=card.querySelector("[data-euicc-result]");result.style.color="#344054";result.textContent=`正在仅移除已确认通知 #${item.sequence_number}…`;try{await jsonRequest(`/v1/euiccs/${encodeURIComponent(entry.euicc.eid)}/notifications/${encodeURIComponent(item.sequence_number)}/remove`,{method:"POST",body:JSON.stringify({confirmed:true,receiver_acknowledged:true,event:item.event,iccid:item.iccid||"",address:item.address})});result.textContent=`已从卡内移除通知 #${item.sequence_number}；没有再次发送。`;const inventory=card.querySelector("[data-notification-inventory-button]");if(inventory)await loadEUICCNotifications(entry,card,inventory)}catch(error){result.style.color="#b42318";result.textContent=error.code==="euicc_notification_removal_outcome_unknown"?"卡内移除结果不确定；请重新查看通知清单，不要重复发送。":`卡内移除失败：${error.code||error.message}`;button.disabled=false}}

function parseEUICCActivationCode(raw){let value=String(raw||"").trim();if(/^LPA:/i.test(value))value=value.slice(4);const parts=value.split("$");if(parts.length<3||parts[0]!=="1"||!parts[1])return null;return{value:`LPA:${value}`,smdp:parts[1],matching_id:parts[2]}}

async function decodeEUICCQRImage(file){
  if(!file||!String(file.type||"").startsWith("image/"))throw new Error("qr_not_image");
  if(file.size>16*1024*1024)throw new Error("qr_image_too_large");
  const {default:decodeQR}=await import("/assets/qr/decode.js"),bitmap=await createImageBitmap(file);
  try{
    if(bitmap.width>20000||bitmap.height>20000)throw new Error("qr_image_too_large");
    const attempted=new Set();
    for(const maximum of [1500,800,3000]){
      const scale=Math.min(1,maximum/Math.max(bitmap.width,bitmap.height)),width=Math.max(1,Math.round(bitmap.width*scale)),height=Math.max(1,Math.round(bitmap.height*scale)),key=`${width}x${height}`;
      if(attempted.has(key))continue;attempted.add(key);
      const canvas=document.createElement("canvas");canvas.width=width;canvas.height=height;
      const context=canvas.getContext("2d",{willReadFrequently:true});if(!context)throw new Error("qr_canvas_unavailable");
      context.drawImage(bitmap,0,0,width,height);const pixels=context.getImageData(0,0,width,height);
      try{const decoded=decodeQR({data:pixels.data,width,height});if(String(decoded||"").trim())return String(decoded).trim()}catch{}
      if(scale===1)break;
    }
    return"";
  }finally{bitmap.close?.()}
}

function showEUICCDownloadForm(entry,card){
  const existing=card.querySelector("[data-download-form]");if(existing){existing.remove();return}
  const form=document.createElement("form");form.dataset.downloadForm="";form.className="line-config-form";
  const fieldset=document.createElement("fieldset"),legend=document.createElement("legend");legend.textContent="下载到当前 eUICC";fieldset.append(legend);
  const grid=document.createElement("div");grid.className="form-grid";
  const activation=document.createElement("textarea");activation.rows=3;activation.maxLength=2048;activation.placeholder="LPA:1$smdp.example.com$MATCHING-ID";
  const smdp=document.createElement("input");smdp.maxLength=512;smdp.placeholder="smdp.example.com（与 Activation code 二选一）";
  const matching=document.createElement("input");matching.maxLength=1024;matching.placeholder="Matching ID（手动模式可选）";
  const confirmation=document.createElement("input");confirmation.type="password";confirmation.maxLength=128;confirmation.autocomplete="off";
  const imei=document.createElement("input");imei.inputMode="numeric";imei.pattern="[0-9]{15}";imei.maxLength=15;imei.required=true;imei.value=defaultEUICCIMEI(entry);
  grid.append(labelField("Activation code",activation,true),labelField("SM-DP+ 地址",smdp),labelField("Matching ID",matching),labelField("Confirmation code（可选）",confirmation),labelField("IMEI（15 位）",imei));fieldset.append(grid);form.append(fieldset);
  const note=document.createElement("p");note.className="muted";note.textContent="下载码可能只能使用一次。MDD 不会记录明文，也不会在断线或结果不确定时自动重试。";form.append(note);
  const qrControls=document.createElement("div"),fileInput=document.createElement("input"),qrButton=document.createElement("button"),qrHint=document.createElement("span");qrControls.className="toolbar";fileInput.type="file";fileInput.accept="image/*";fileInput.hidden=true;qrButton.type="button";qrButton.className="secondary";qrButton.textContent="上传二维码图片";qrHint.className="muted";qrHint.textContent="也可把二维码截图粘贴或拖入当前表单；图片只在浏览器内解析";qrControls.append(fileInput,qrButton,qrHint);form.append(qrControls);
  const controls=document.createElement("div");controls.className="toolbar";const submit=document.createElement("button"),cancel=document.createElement("button");submit.type="submit";submit.textContent="确认并开始";cancel.type="button";cancel.className="secondary";cancel.textContent="取消";cancel.addEventListener("click",()=>form.remove());controls.append(submit,cancel);form.append(controls);
  const error=document.createElement("div");error.className="result hidden";form.append(error);
  let qrBusy=false;
  const showQRFailure=message=>{error.classList.remove("hidden");error.style.color="#b42318";error.textContent=message};
  const readQR=async file=>{if(!file||qrBusy)return;qrBusy=true;qrButton.disabled=true;qrButton.textContent="正在识别二维码…";error.classList.add("hidden");try{const text=await decodeEUICCQRImage(file);if(!text){showQRFailure("未在图片中找到二维码");return}const parsed=parseEUICCActivationCode(text);if(!parsed){showQRFailure("二维码内容不是 eSIM 激活码");return}activation.value=parsed.value;smdp.value="";matching.value="";error.classList.remove("hidden");error.style.color="#067647";error.textContent="已识别 eSIM 激活码；内容仅保留在当前表单中"}catch(problem){showQRFailure(problem.message==="qr_image_too_large"?"二维码图片过大（最大 16 MiB、20000 像素边长）":"无法读取该二维码图片")}finally{qrBusy=false;qrButton.disabled=false;qrButton.textContent="上传二维码图片"}};
  qrButton.addEventListener("click",()=>fileInput.click());fileInput.addEventListener("change",()=>{readQR(fileInput.files?.[0]);fileInput.value=""});
  form.addEventListener("paste",event=>{const item=Array.from(event.clipboardData?.items||[]).find(candidate=>String(candidate.type||"").startsWith("image/"));if(item){event.preventDefault();readQR(item.getAsFile())}});
  form.addEventListener("dragover",event=>event.preventDefault());
  form.addEventListener("drop",event=>{const file=Array.from(event.dataTransfer?.files||[]).find(candidate=>String(candidate.type||"").startsWith("image/"));if(file){event.preventDefault();readQR(file)}});
  form.addEventListener("submit",async event=>{
    event.preventDefault();error.classList.add("hidden");
    if(!form.reportValidity())return;
    if(!confirm(`确认把一个新 Profile 下载到 EID ${entry.euicc.eid}？\n\n下载码不会自动重试；如果结果不确定，请先查询状态。`))return;
    submit.disabled=true;cancel.disabled=true;
    let code=activation.value.trim(),server=smdp.value.trim(),matchingID=matching.value.trim();
    if((code?1:0)+(server?1:0)!==1){error.classList.remove("hidden");error.style.color="#b42318";error.textContent="请填写 Activation code，或填写 SM-DP+ 地址（二选一）";return}
    if(server){if(server.includes("$")||matchingID.includes("$")){error.classList.remove("hidden");error.style.color="#b42318";error.textContent="SM-DP+ 地址或 Matching ID 不能包含 $";return}code=`LPA:1$${server}$${matchingID}`}
    const operation=operationID("ui-euicc-download"),body={operation_id:operation,activation_code:code,confirmation_code:confirmation.value.trim(),imei:imei.value.trim()};
    try{
      await stopEUICCVoWiFiLines(entry);
      const payload=await jsonRequest(`/v1/euiccs/${encodeURIComponent(entry.euicc.eid)}/downloads`,{method:"POST",body:JSON.stringify(body)});
      activation.value="";smdp.value="";matching.value="";confirmation.value="";state.euiccDownloads.set(entry.euicc.eid,{operation_id:operation,job:payload.job});saveEUICCDownloads();form.remove();renderEUICCs();pollEUICCDownload(entry.euicc.eid,operation)
    }catch(problem){error.classList.remove("hidden");error.style.color="#b42318";error.textContent=`下载未开始：${problem.code||problem.message}`;submit.disabled=false;cancel.disabled=false}
  });
  card.insertBefore(form,card.querySelector(".readers"))
}

function labelField(text,input,wide=false){const label=document.createElement("label");if(wide)label.className="form-wide";label.append(document.createTextNode(text),input);return label}

function defaultEUICCIMEI(entry){
  const ids=new Set((entry.euicc.profiles||[]).map(profile=>profile.iccid));
  const line=(state.lineCatalog?.lines||[]).find(candidate=>ids.has(candidate.card_id)&&/^\d{15}$/.test(candidate.sim?.imei||""));
  return line?.sim?.imei||""
}

async function stopEUICCVoWiFiLines(entry){
  const ids=new Set((entry.euicc.profiles||[]).map(profile=>profile.iccid));
  const lines=(state.lineCatalog?.lines||[]).filter(line=>line.enabled&&ids.has(line.card_id));
  for(const line of lines){
    const status=await jsonRequest(`/v1/lines/${encodeURIComponent(line.id)}/vowifi/status`);
    if(status.active_call)throw new Error(`线路 ${line.name||line.id} 正在通话，不能开始下载`);
    if(status.runtime?.condition==="stopped")continue;
    if(!confirm(`线路 ${line.name||line.id} 当前为 ${status.runtime?.condition||"unknown"}。下载需要独占卡片，是否先停止该线路？`))throw new Error("已取消下载");
    const stopped=await jsonRequest(`/v1/lines/${encodeURIComponent(line.id)}/vowifi/runtime/stop`,{method:"POST",body:JSON.stringify({operation_id:operationID("ui-euicc-stop")})});
    if(stopped.status?.runtime?.condition!=="stopped")throw new Error(`线路 ${line.name||line.id} 未确认停止`)
  }
}

function renderEUICCDownloadStatus(card,entry){
  const fact=state.euiccDownloads.get(entry.euicc.eid);if(!fact?.job)return;
  const status=document.createElement("div");status.className="result";status.dataset.downloadStatus="";
  const job=fact.job,detail=[`下载 ${job.state}`,`阶段 ${job.stage}`,job.code,job.metadata?.profile_name||job.metadata?.service_provider_name,job.metadata?.iccid,fact.status_error&&`状态暂不可读：${fact.status_error}`].filter(Boolean).join(" · ");status.textContent=detail;
  if(["failed","uncertain"].includes(job.state))status.style.color="#b42318";else if(job.state==="completed")status.style.color="#067647";
  if(["queued","running","cancelling"].includes(job.state)){
    const cancel=document.createElement("button");cancel.type="button";cancel.className="secondary";cancel.textContent=job.state==="cancelling"?"正在取消":"取消下载";cancel.disabled=job.state==="cancelling";cancel.addEventListener("click",()=>cancelEUICCDownload(entry.euicc.eid,fact.operation_id,cancel));status.append(document.createTextNode(" "),cancel);pollEUICCDownload(entry.euicc.eid,fact.operation_id)
  }
  card.append(status)
}

async function pollEUICCDownload(eid,operation){
  const current=state.euiccDownloads.get(eid);if(!current||current.operation_id!==operation||current.polling)return;current.polling=true;
  try{const payload=await jsonRequest(`/v1/euiccs/${encodeURIComponent(eid)}/downloads/${encodeURIComponent(operation)}`);current.job=payload.job;delete current.status_error;saveEUICCDownloads();renderEUICCs();if(["queued","running","cancelling"].includes(payload.job?.state))setTimeout(()=>pollEUICCDownload(eid,operation),1500);else if(payload.job?.state==="completed")setTimeout(loadEUICCs,1000)}
  catch(problem){current.status_error=problem.code||"status_unavailable";saveEUICCDownloads();renderEUICCs();setTimeout(()=>pollEUICCDownload(eid,operation),3000)}
  finally{current.polling=false}
}

async function cancelEUICCDownload(eid,operation,button){button.disabled=true;try{const payload=await jsonRequest(`/v1/euiccs/${encodeURIComponent(eid)}/downloads/${encodeURIComponent(operation)}/cancel`,{method:"POST",body:"{}"});state.euiccDownloads.set(eid,{operation_id:operation,job:payload.job});saveEUICCDownloads();renderEUICCs()}catch(problem){button.disabled=false;showNotice(`取消下载失败：${problem.code||problem.message}`)}}

function saveEUICCDownloads(){const value=[];for(const [eid,fact] of state.euiccDownloads){if(fact?.operation_id)value.push({eid,operation_id:fact.operation_id,job:fact.job})}localStorage.setItem("mdd-euicc-downloads",JSON.stringify(value))}
function restoreEUICCDownloads(){try{const value=JSON.parse(localStorage.getItem("mdd-euicc-downloads")||"[]");for(const fact of value){if(/^\d{32}$/.test(fact.eid)&&fact.operation_id)state.euiccDownloads.set(fact.eid,{operation_id:fact.operation_id,job:fact.job})}}catch{localStorage.removeItem("mdd-euicc-downloads")}}

async function loadRuntime(){try{state.runtime=await jsonRequest("/v1/system/runtime");renderRuntime()}catch(error){renderRuntimeError(error)}}
async function loadLineCatalog(){const refresh=el("refresh-line-config"),save=el("save-line-config"),selected=el("line-config-line").value;refresh.disabled=true;save.disabled=true;try{state.lineCatalog=await jsonRequest("/v1/catalog/lines");renderLineSelector(selected);renderLineEditor();if(state.snapshot)renderAgents(state.snapshot.agents||[])}catch(error){state.lineCatalog=null;el("line-config-line").replaceChildren();disableLineEditor(true);showLineConfigResult(`线路配置读取失败：${error.code||error.message}`,true)}finally{refresh.disabled=false}}
async function saveLineConfig(event){event.preventDefault();const catalog=state.lineCatalog,line=currentEditedLine(),button=el("save-line-config"),stored=(catalog?.lines||[]).find(candidate=>candidate.id===line?.id);if(!catalog||!line)return;if(stored&&JSON.stringify(line)===JSON.stringify(catalogLinePayload(stored))){showLineConfigResult(`线路 ${line.name||line.id} 没有变化；catalog revision ${catalog.revision} 未更新。`);return}button.disabled=true;showLineConfigResult(`正在保存 ${line.id} 到 catalog revision ${catalog.revision}；不会应用或操作 Provider…`);try{const result=await jsonRequest(`/v1/catalog/lines/${encodeURIComponent(line.id)}`,{method:"PUT",headers:{"If-Match":`"${catalog.revision}"`},body:JSON.stringify(line)});showLineConfigResult(`已保存 ${result.line?.name||line.id} · catalog revision ${result.revision} · 尚未应用到 Provider`);await loadLineCatalog();await loadProviderConfig()}catch(error){if(error.status===412){showLineConfigResult("保存被拒绝：catalog 已被其他操作更新，已刷新最新配置；你的旧版本没有覆盖新数据。",true);await loadLineCatalog();await loadProviderConfig()}else{showLineConfigResult(`保存失败：${lineConfigError(error)}`,true);button.disabled=false}}}
async function loadProviderConfig(){const refresh=el("refresh-provider-config"),apply=el("apply-provider-config");refresh.disabled=true;try{state.providerConfig=await jsonRequest("/v1/system/provider-config");renderProviderConfig()}catch(error){state.providerConfig=null;el("provider-config-status").replaceChildren(errorCard(`配置应用服务不可用：${error.code||error.message}`));apply.disabled=true}finally{refresh.disabled=false}}
async function applyProviderConfig(){const status=state.providerConfig,button=el("apply-provider-config"),result=el("provider-config-result");if(!status||status.applying)return;button.disabled=true;result.classList.remove("hidden");result.style.color="#344054";result.textContent=`正在应用 catalog revision ${status.catalog_revision}；只处理实际变化的线路…`;try{const applied=await jsonRequest("/v1/system/provider-config",{method:"POST",body:JSON.stringify({schema_version:1,catalog_revision:status.catalog_revision})});result.textContent=`${applied.state} · revision ${applied.catalog_revision} · 新增 ${applied.added} / 变更 ${applied.changed} / 移除 ${applied.removed}`;await loadProviderConfig()}catch(error){result.style.color="#b42318";result.textContent=`应用失败：${[error.code,error.detail].filter(Boolean).join(" · ")||error.message}`;await loadProviderConfig()}finally{button.disabled=false}}

async function loadEgressConfig(){const refresh=el("refresh-egress-config"),save=el("save-egress-config"),apply=el("apply-egress-config");refresh.disabled=true;save.disabled=true;apply.disabled=true;try{const config=await jsonRequest("/v1/egress/config");state.egressConfig=config;state.egressDraft=JSON.parse(JSON.stringify(config.config));let applyError=null;try{state.egressApply=await jsonRequest("/v1/egress/config/apply")}catch(error){state.egressApply=null;applyError=error}renderEgressConfig();if(applyError){el("egress-apply-status").replaceChildren(errorCard(`出口应用服务不可用：${applyError.code||applyError.message}`));showEgressConfigResult("配置仍可保存；应用服务恢复前不会改变运行网络。",true)}}catch(error){state.egressConfig=null;state.egressDraft=null;state.egressApply=null;el("egress-config-profiles").replaceChildren(errorCard(`国家出口配置读取失败：${error.code||error.message}`));el("egress-config-exits").replaceChildren();el("egress-apply-status").replaceChildren();showEgressConfigResult(`国家出口配置不可用：${[error.code,error.detail].filter(Boolean).join(" · ")||error.message}`,true)}finally{refresh.disabled=false}}

function renderEgressConfig(){const config=state.egressDraft,status=state.egressApply;if(!config)return;el("egress-config-enabled").checked=Boolean(config.enabled);renderEgressProfiles();renderEgressExits();const root=el("egress-apply-status");root.replaceChildren();if(status)root.append(keyValueCard("出口 desired 与运行确认",[["配置 revision",status.config_revision],["catalog revision",status.catalog_revision],["已应用配置",status.applied_config_revision],["已应用 catalog",status.applied_catalog_revision],["运行代际已确认",status.runtime_confirmed?"是":"否"],["待应用",status.pending?"是":"否"]]));el("save-egress-config").disabled=false;const apply=el("apply-egress-config");apply.disabled=!status||status.applying||!status.pending;apply.textContent=status?.applying?"应用进行中":status?.pending?"应用已保存配置":"出口配置已同步"}

function renderEgressProfiles(){const root=el("egress-config-profiles"),profiles=state.egressDraft?.profiles||{};root.replaceChildren();const ids=Object.keys(profiles).sort();if(!ids.length){root.append(empty("代理库为空"));return}for(const id of ids){const profile=profiles[id],card=document.createElement("article"),head=document.createElement("div"),title=document.createElement("h3"),remove=document.createElement("button"),grid=document.createElement("div");card.className="card";head.className="card-head";title.textContent=id;remove.type="button";remove.className="secondary";remove.textContent="移除";remove.addEventListener("click",()=>removeEgressProfile(id));head.append(title,remove);grid.className="form-grid";grid.append(egressTextField("名称",profile.name||"",value=>profile.name=value),egressSelectField("类型",profile.type||"node",[["node","单节点"],["subscription","订阅"],["socks5","SOCKS5"],["existing","现有 outbound"],["cellular_sim","蜂窝 SIM 出口"]],value=>{profile.type=value;renderEgressProfiles()}));if(profile.type==="node")grid.append(egressTextField("节点分享链接 / 线性链",profile.value||"",value=>profile.value=value,true));if(profile.type==="subscription")grid.append(egressTextField("订阅 URL",profile.url||"",value=>profile.url=value,true),egressNumberField("刷新分钟",profile.refresh_minutes||30,1,10080,value=>profile.refresh_minutes=value));if(profile.type==="socks5")grid.append(egressTextField("服务器",profile.server||"",value=>profile.server=value),egressNumberField("端口",profile.port||1080,1,65535,value=>profile.port=value),egressTextField("用户名",profile.username||"",value=>profile.username=value),egressSecretField("密码",profile.password||"",value=>profile.password=value));if(profile.type==="existing")grid.append(egressTextField("sing-box outbound tag",profile.outbound_tag||"",value=>profile.outbound_tag=value));if(profile.type==="cellular_sim")grid.append(egressTextField("SIM ICCID",profile.sim_iccid||"",value=>profile.sim_iccid=value));card.append(head,grid);root.append(card)}}

function renderEgressExits(){const root=el("egress-config-exits"),config=state.egressDraft,exits=config?.exits||{},profiles=config?.profiles||{};root.replaceChildren();const countries=Object.keys(exits).sort();if(!countries.length){root.append(empty("尚未配置国家出口"));return}for(const country of countries){const exit=exits[country],card=document.createElement("article"),head=document.createElement("div"),title=document.createElement("h3"),remove=document.createElement("button"),grid=document.createElement("div"),enabled=document.createElement("input"),enabledLabel=document.createElement("label");card.className="card";head.className="card-head";title.textContent=country.toUpperCase();remove.type="button";remove.className="secondary";remove.textContent="移除";remove.addEventListener("click",()=>{delete exits[country];renderEgressExits()});head.append(title,remove);enabled.type="checkbox";enabled.checked=Boolean(exit.enabled);enabled.addEventListener("change",event=>exit.enabled=event.target.checked);enabledLabel.className="check-label";enabledLabel.append(enabled,document.createTextNode("启用此出口"));grid.className="form-grid";const choices=[["__direct","明确直连"],...Object.keys(profiles).sort().map(id=>[id,`${profiles[id].name||id} · ${profiles[id].type}`])],selected=exit.mode==="direct"?"__direct":exit.profile_id||"";grid.append(enabledLabel,egressSelectField("出口代理",selected,[["","选择代理"],...choices],value=>{if(value==="__direct"){exit.mode="direct";exit.profile_id=""}else{exit.mode="";exit.profile_id=value}}),egressTextField("订阅节点关键词（每行一个）",(exit.keywords||[]).join("\n"),value=>exit.keywords=value.split(/\r?\n/).map(item=>item.trim()).filter(Boolean),true),egressTextField("固定/首选节点名",exit.pinned_node||"",value=>exit.pinned_node=value),egressSelectField("节点策略",exit.pin_mode||"lock",[["lock","锁定"],["prefer","优先，故障可切换"]],value=>exit.pin_mode=value));card.append(head,grid);root.append(card)}}

function egressTextField(labelText,value,onChange,wide=false){const label=document.createElement("label"),input=wide?document.createElement("textarea"):document.createElement("input");label.textContent=labelText;if(wide)label.className="form-wide";input.value=value;input.addEventListener("input",event=>onChange(event.target.value));label.append(input);return label}
function egressSecretField(labelText,value,onChange){const field=egressTextField(labelText,value,onChange);field.querySelector("input").type="password";field.querySelector("input").autocomplete="new-password";return field}
function egressNumberField(labelText,value,min,max,onChange){const field=egressTextField(labelText,value,value=>onChange(Number(value)));const input=field.querySelector("input");input.type="number";input.min=min;input.max=max;return field}
function egressSelectField(labelText,value,choices,onChange){const label=document.createElement("label"),select=document.createElement("select");label.textContent=labelText;for(const [id,name] of choices){const option=document.createElement("option");option.value=id;option.textContent=name;select.append(option)}select.value=value;select.addEventListener("change",event=>onChange(event.target.value));label.append(select);return label}

function addEgressProfile(){if(!state.egressDraft)return;const id=el("egress-profile-id").value.trim(),name=el("egress-profile-name").value.trim(),type=el("egress-profile-type").value;if(!/^[A-Za-z0-9_.-]{1,80}$/.test(id)||!name){showEgressConfigResult("请填写有效且未占用的代理 ID 与名称。",true);return}if(state.egressDraft.profiles[id]){showEgressConfigResult("该代理 ID 已存在。",true);return}const profile={name,type};if(type==="socks5")profile.port=1080;if(type==="subscription")profile.refresh_minutes=30;state.egressDraft.profiles[id]=profile;el("egress-profile-id").value="";el("egress-profile-name").value="";renderEgressProfiles();renderEgressExits()}
function removeEgressProfile(id){const used=Object.entries(state.egressDraft?.exits||{}).filter(([,exit])=>exit.profile_id===id).map(([country])=>country.toUpperCase());if(used.length){showEgressConfigResult(`不能移除：${id} 正由 ${used.join("、")} 使用。`,true);return}delete state.egressDraft.profiles[id];renderEgressProfiles();renderEgressExits()}
function addEgressExit(){if(!state.egressDraft)return;const country=el("egress-exit-country").value.trim().toLowerCase();if(!/^[a-z]{2}$/.test(country)){showEgressConfigResult("国家代码必须是两位 ISO 字母。",true);return}if(state.egressDraft.exits[country]){showEgressConfigResult("该国家出口已经存在。",true);return}const first=Object.keys(state.egressDraft.profiles||{}).sort()[0]||"";state.egressDraft.exits[country]={enabled:true,mode:first?"":"direct",profile_id:first,keywords:[]};el("egress-exit-country").value="";renderEgressExits()}

async function saveEgressConfig(){const saved=state.egressConfig,draft=state.egressDraft,button=el("save-egress-config");if(!saved||!draft)return;if(JSON.stringify(saved.config)===JSON.stringify(draft)){showEgressConfigResult(`配置没有变化；revision ${saved.revision} 未更新。`);return}button.disabled=true;showEgressConfigResult(`正在保存 revision ${saved.revision}；不会改变 sing-box、路由或 Provider…`);try{const result=await jsonRequest("/v1/egress/config",{method:"PUT",headers:{"If-Match":`"${saved.revision}"`},body:JSON.stringify(draft)});showEgressConfigResult(`已保存国家出口配置 revision ${result.revision}；尚未应用到运行网络。`);await loadEgressConfig()}catch(error){if(error.status===412){showEgressConfigResult("保存被拒绝：配置已由其他管理员更新；旧页面没有覆盖新数据。",true);await loadEgressConfig()}else{showEgressConfigResult(`保存失败：${[error.code,error.detail].filter(Boolean).join(" · ")||error.message}`,true);button.disabled=false}}}
async function applyEgressConfig(){const status=state.egressApply,button=el("apply-egress-config");if(!status||status.applying||!status.pending)return;button.disabled=true;showEgressConfigResult(`正在应用出口配置 revision ${status.config_revision} 与 catalog revision ${status.catalog_revision}；相同代际不会重建 sing-box…`);try{const result=await jsonRequest("/v1/egress/config/apply",{method:"POST",body:JSON.stringify({schema_version:2,config_revision:status.config_revision,catalog_revision:status.catalog_revision})});showEgressConfigResult(`${result.state} · 运行代际已确认 · ${result.generation.slice(0,12)}`);await Promise.all([loadEgressConfig(),loadEgressDiagnostics()])}catch(error){showEgressConfigResult(`应用未确认：${[error.code,error.detail].filter(Boolean).join(" · ")||error.message}`,true);await loadEgressConfig()}finally{button.disabled=false}}
function showEgressConfigResult(message,isError=false){const result=el("egress-config-result");result.classList.remove("hidden");result.style.color=isError?"#b42318":"#344054";result.textContent=message}
async function loadDiagnostics(){const button=el("refresh-diagnostics");button.disabled=true;await Promise.all([loadCoreDiagnostics(),loadEgressDiagnostics()]);button.disabled=false;renderClientChecks()}
async function loadCoreDiagnostics(){try{state.diagnosticSnapshot=await jsonRequest("/v1/diagnostics");renderDiagnostics()}catch(error){el("diagnostic-checks").replaceChildren(errorCard(`诊断采样失败：${error.message}`))}}
async function loadEgressDiagnostics(){const button=el("refresh-egress-diagnostics");button.disabled=true;try{state.egressDiagnosticSnapshot=await jsonRequest("/v1/egress/exits");renderEgressDiagnostics()}catch(error){state.egressDiagnosticSnapshot=null;el("egress-diagnostic-exits").replaceChildren(errorCard(`出口状态读取失败：${error.code||error.message}`))}finally{button.disabled=false}}

function renderRuntime(){const root=el("runtime-settings");root.replaceChildren();const info=state.runtime;if(!info)return;root.append(keyValueCard("单一公开入口",[
  ["监听",info.public?.listen],["传输",info.public?.transport],["公开监听器数量",info.public?.listener_count],["复用方式",info.public?.multiplexing],["浏览器状态",info.public?.browser_state_path],["Agent 控制",info.public?.agent_control_path],["VoWiFi 浏览器媒体",info.public?.browser_media_path],["蜂窝浏览器媒体",info.public?.cellular_browser_media_path],["Agent 蜂窝媒体",info.public?.agent_media_path]
]),keyValueCard("TLS 与进程",[["证书 SHA-256",info.public?.tls_fingerprint_sha256],["组件",info.component],["构建版本",info.build_version],["Go",info.go_version],["状态 TTL",`${info.state_ttl_seconds}s`]]),keyValueCard("本机 Provider IPC",[["范围",info.local?.scope],["传输",info.local?.transport],["说明","仅进程间控制，不是部署入口"]]))}
function renderRuntimeError(error){el("runtime-settings").replaceChildren(errorCard(`运行配置读取失败：${error.message}`))}

function renderLineSelector(previous){const select=el("line-config-line"),lines=state.lineCatalog?.lines||[];select.replaceChildren();for(const line of lines){const option=document.createElement("option");option.value=line.id;option.textContent=`${line.name||line.id} · ${line.sim?.msisdn||line.card_id}`;select.append(option)}select.value=lines.some(line=>line.id===previous)?previous:(lines[0]?.id||"");select.disabled=!lines.length;disableLineEditor(!lines.length);if(!lines.length)showLineConfigResult("catalog 中没有可编辑线路。",true)}
function renderLineEditor(){const line=(state.lineCatalog?.lines||[]).find(candidate=>candidate.id===el("line-config-line").value);if(!line){disableLineEditor(true);return}disableLineEditor(false);setLineField("id",line.id);setLineField("name",line.name);setLineField("card-id",line.card_id);el("line-config-enabled").checked=Boolean(line.enabled);setLineField("imsi",line.sim?.imsi);setLineField("mcc",line.sim?.mcc);setLineField("mnc",line.sim?.mnc);setLineField("imei",line.sim?.imei);setLineField("msisdn",line.sim?.msisdn);setLineField("smsc",line.sim?.smsc);setLineField("country",line.network?.egress_country);setLineField("epdg",line.network?.epdg_address);setLineField("pcscf",(line.network?.pcscf||[]).join("\n"));setLineField("impi",line.ims?.impi);setLineField("impu",line.ims?.impu);setLineField("domain",line.ims?.domain);setLineField("aka",line.ims?.aka_app_preference);setLineField("network",line.ims?.network);setLineField("server",line.ims?.server);setLineField("expires",line.ims?.expires||"");syncLineIdentityRequirements()}
function currentEditedLine(){const id=fieldValue("id");if(!id)return null;const selectedID=el("line-config-line").value,stored=(state.lineCatalog?.lines||[]).find(line=>line.id===selectedID),presentation={access_network_info:stored?.ims?.access_network_info||"",visited_network_id:stored?.ims?.visited_network_id||"",access_type:stored?.ims?.access_type||"",user_equals_phone:Boolean(stored?.ims?.user_equals_phone)};return{schema_version:1,id,name:fieldValue("name"),enabled:el("line-config-enabled").checked,card_id:fieldValue("card-id"),sim:{imsi:fieldValue("imsi"),mcc:fieldValue("mcc"),mnc:fieldValue("mnc"),imei:fieldValue("imei"),msisdn:fieldValue("msisdn"),smsc:fieldValue("smsc")},network:{epdg_address:fieldValue("epdg"),pcscf:fieldValue("pcscf").split(/\r?\n/).map(value=>value.trim()).filter(Boolean),egress_country:fieldValue("country")},ims:{impi:fieldValue("impi"),impu:fieldValue("impu"),domain:fieldValue("domain"),...presentation,aka_app_preference:fieldValue("aka"),network:fieldValue("network"),server:fieldValue("server"),expires:Number(fieldValue("expires"))||0}}}
function catalogLinePayload(line){return{schema_version:1,id:line.id,name:line.name||"",enabled:Boolean(line.enabled),card_id:line.card_id||"",sim:{imsi:line.sim?.imsi||"",mcc:line.sim?.mcc||"",mnc:line.sim?.mnc||"",imei:line.sim?.imei||"",msisdn:line.sim?.msisdn||"",smsc:line.sim?.smsc||""},network:{epdg_address:line.network?.epdg_address||"",pcscf:line.network?.pcscf||[],egress_country:line.network?.egress_country||""},ims:{impi:line.ims?.impi||"",impu:line.ims?.impu||"",domain:line.ims?.domain||"",access_network_info:line.ims?.access_network_info||"",visited_network_id:line.ims?.visited_network_id||"",access_type:line.ims?.access_type||"",user_equals_phone:Boolean(line.ims?.user_equals_phone),aka_app_preference:line.ims?.aka_app_preference||"",network:line.ims?.network||"",server:line.ims?.server||"",expires:Number(line.ims?.expires)||0}}}
function syncLineIdentityRequirements(){const required=el("line-config-enabled").checked;for(const name of ["imsi","mcc","mnc"])el(`line-config-${name}`).required=required}
function fieldValue(name){return el(`line-config-${name}`).value.trim()}
function setLineField(name,value){el(`line-config-${name}`).value=value??""}
function disableLineEditor(disabled){for(const control of el("line-config-form").elements)control.disabled=disabled;el("save-line-config").disabled=disabled}
function showLineConfigResult(message,isError=false){const result=el("line-config-result");result.classList.remove("hidden");result.style.color=isError?"#b42318":"#344054";result.textContent=message}
function lineConfigError(error){if(error.code==="card_identity_in_use")return"该卡片 ID 已由其他线路使用";if(error.code==="invalid_line")return"字段不符合服务端线路契约";return[error.code,error.detail].filter(Boolean).join(" · ")||error.message}

function renderProviderConfig(){const root=el("provider-config-status"),status=state.providerConfig,button=el("apply-provider-config");root.replaceChildren();if(!status)return;root.append(keyValueCard("期望与已应用版本",[["catalog revision",status.catalog_revision],["已应用 revision",status.applied_revision],["待应用",status.pending?"是":"否"],["正在应用",status.applying?"是":"否"]]),keyValueCard("最近一次应用",[["apply ID",status.last_apply_id],["状态",status.last_state],["代码",status.last_code]]));button.disabled=status.applying||!status.pending;button.textContent=status.applying?"应用进行中":status.pending?"应用当前配置":"配置已同步"}

function keyValueCard(title,rows){const card=document.createElement("article");card.className="card";const heading=document.createElement("h3");heading.textContent=title;card.append(heading);const list=document.createElement("dl");list.className="key-values";for(const [key,value] of rows){const term=document.createElement("dt"),description=document.createElement("dd");term.textContent=key;description.textContent=value??"—";list.append(term,description)}card.append(list);return card}
function errorCard(message){const card=document.createElement("article");card.className="card error";card.textContent=message;return card}

function renderClientChecks(){const root=el("client-checks");root.replaceChildren();const api=state.diagnosticSnapshot?{status:"pass",code:"browser_api_response",detail:`采样 ${fmtTime(state.diagnosticSnapshot.generated_at)}`}:{status:"not_run",code:"browser_api_not_sampled",detail:"尚未取得诊断 API 响应"};const ws=state.socket?.readyState===WebSocket.OPEN&&state.snapshot?{status:"pass",code:"browser_state_wss_current",detail:`状态采样 ${fmtTime(state.snapshot.at)}`}:{status:"fail",code:"browser_state_wss_unavailable",detail:"浏览器状态 WSS 未连接或尚无快照"};for(const check of [api,ws])root.append(checkNode(check,"浏览器"))}
function renderDiagnostics(){const root=el("diagnostic-checks");root.replaceChildren();const checks=state.diagnosticSnapshot?.checks||[];if(!checks.length){root.append(empty("没有服务端诊断事实"));return}for(const check of checks)root.append(checkNode(check,check.scope))}
function renderEgressDiagnostics(){const root=el("egress-diagnostic-exits"),exits=state.egressDiagnosticSnapshot?.exits||[];root.replaceChildren();if(!exits.length){root.append(empty("当前没有已应用的国家出口"));return}for(const exit of exits){const card=document.createElement("article"),title=document.createElement("h3"),badges=document.createElement("div"),runtime=document.createElement("span"),meta=document.createElement("div"),actions=document.createElement("div"),button=document.createElement("button"),result=document.createElement("div"),prior=state.egressDiagnosticResults.get(exit.country);card.className="card";title.textContent=exit.country.toUpperCase();badges.className="badges";runtime.className=`badge ${exit.ready?"good":"bad"}`;runtime.textContent=exit.ready?"已应用":"未就绪";badges.append(runtime);meta.className="muted";meta.textContent=[exit.mode&&`模式 ${exit.mode}`,exit.node&&`节点 ${exit.node}`,exit.candidate_count&&`候选 ${exit.candidate_count}`,exit.error].filter(Boolean).join(" · ")||"没有运行详情";actions.className="toolbar";button.textContent=prior?.loading?"测试中…":"测试实际 UDP 出口";button.disabled=!exit.testable||prior?.loading===true;button.addEventListener("click",()=>testEgressDiagnostic(exit.country));actions.append(button);result.className=`result ${prior?"":"hidden"}`;if(prior){result.textContent=prior.text;result.style.color=prior.failed?"#b42318":"#067647"}card.append(title,badges,meta,actions,result);root.append(card)}}
async function testEgressDiagnostic(country){if(state.egressDiagnosticResults.get(country)?.loading)return;state.egressDiagnosticResults.set(country,{text:"正在通过已应用出口发送两个 UDP DNS 探测…",failed:false,loading:true});renderEgressDiagnostics();try{const tested=await jsonRequest(`/v1/egress/exits/${encodeURIComponent(country)}/test`,{method:"POST",body:"{}"});state.egressDiagnosticResults.set(country,{text:`PASS：端到端 UDP 已通过 ${tested.target} 返回合法 DNS answer · ${tested.latency_ms} ms（任一目标通过即成功）`,failed:false,loading:false})}catch(error){state.egressDiagnosticResults.set(country,{text:`FAIL [${error.layer||"country_egress_udp"}]：${error.detail||error.code||error.message}`,failed:true,loading:false})}finally{renderEgressDiagnostics()}}
function checkNode(check,scope){const row=document.createElement("article");row.className="diagnostic-row";const badge=document.createElement("span");badge.className=`badge ${check.status==="pass"?"good":check.status==="not_run"?"neutral":"bad"}`;badge.textContent=check.status;const body=document.createElement("div"),title=document.createElement("strong"),detail=document.createElement("div");title.textContent=`${scope||"—"} · ${check.code}`;detail.className="muted";detail.textContent=[check.kind,check.detail,check.observed_at?fmtTime(check.observed_at):""].filter(Boolean).join(" · ");body.append(title,detail);row.append(badge,body);return row}

function render(snapshot){
  showNotice("");el("snapshot-time").textContent=`采样 ${fmtTime(snapshot.at)}`;
  const agents=Array.isArray(snapshot.agents)?snapshot.agents:[];const catalog=snapshot.catalog?.lines||[];const lines=Array.isArray(snapshot.lines)?snapshot.lines:[];
  const readers=agents.flatMap(agent=>agent.topology?.readers||[]);
  el("agent-count").textContent=agents.length;el("reader-count").textContent=readers.length;el("card-count").textContent=readers.filter(reader=>reader.identity_state==="identified").length;el("line-count").textContent=catalog.length;
  renderLines(catalog,lines);renderAgents(agents);renderCallLines(catalog);renderMessageLines(catalog);renderMessages(Array.isArray(snapshot.messages)?snapshot.messages:[]);restorePendingMessage();refreshCallStatuses();
}

function renderCallLines(lines){
  const select=el("call-line"),selected=select.value;select.replaceChildren();
  if(!lines.length){const option=document.createElement("option");option.value="";option.textContent="尚无线路配置";select.append(option);select.disabled=true;return}
  for(const line of lines){
    const vowifi=document.createElement("option");vowifi.value=callRouteValue("vowifi",line.id);vowifi.textContent=`${line.name||line.id} · VoWiFi · ${line.sim?.msisdn||"无号码"}`;select.append(vowifi);
    if(operationReadyForLine(line.id,"cellular_call")){const cellular=document.createElement("option");cellular.value=callRouteValue("cellular",line.id);cellular.textContent=`${line.name||line.id} · 蜂窝 Modem · ${line.sim?.msisdn||"无号码"}`;select.append(cellular)}
  }
  const locked=state.currentCall?callRouteValue(state.currentCall.mode,state.currentCall.line_id):"";const values=[...select.options].map(option=>option.value);select.value=values.includes(locked||selected)?(locked||selected):values[0];select.disabled=Boolean(state.currentCall);
}

function callRouteValue(mode,lineID){return `${mode}:${lineID}`}
function selectedCallRoute(){const value=el("call-line").value,separator=value.indexOf(":");if(separator<1)return null;const lineID=value.slice(separator+1),line=(state.snapshot?.catalog?.lines||[]).find(candidate=>candidate.id===lineID);if(!line?.card_id)return null;return{mode:value.slice(0,separator),line_id:lineID,expected_card_id:line.card_id}}
function operationReadyForLine(lineID,name){const projection=(state.snapshot?.lines||[]).find(candidate=>candidate.line_id===lineID);return projection?.operations?.[name]?.ready===true}

async function refreshCallStatuses(){
  if(state.callStatusLoading)return;const lines=state.snapshot?.catalog?.lines||[];state.callStatusLoading=true;el("refresh-call-status").disabled=true;
  await Promise.all(lines.map(async line=>{
    const [provider,cellular]=await Promise.allSettled([jsonRequest(`/v1/lines/${encodeURIComponent(line.id)}/vowifi/status`),jsonRequest(`/v1/lines/${encodeURIComponent(line.id)}/cellular/calls/status`)]);
    state.providerStatuses.set(line.id,provider.status==="fulfilled"?{status:provider.value}:{error:provider.reason});state.cellularStatuses.set(line.id,cellular.status==="fulfilled"?{status:cellular.value}:{error:cellular.reason});
    const call=state.currentCall;if(call?.line_id!==line.id)return;
    if(call.mode==="vowifi"){const status=provider.status==="fulfilled"?provider.value:null;if(call.phase==="start_unknown"&&status?.active_call?.call_id===call.call_id){call.phase="active";call.media.markActive();showCallResult("服务器确认该幂等呼叫已经接通；已恢复为通话中。")}else if(["active","media_failed"].includes(call.phase)&&status&&!status.active_call){showCallResult("Provider 已确认通话结束，线路已空闲。");await releaseCurrentCall()}return}
    const sessions=cellular.status==="fulfilled"?cellular.value.sessions||[]:[];const live=sessions.find(session=>session.session_id===call.lease?.session_id);if(call.phase==="start_unknown"&&live?.phase==="active"){call.phase="active";call.media.markActive();showCallResult("Agent 确认同一蜂窝呼叫已经建立；已恢复为通话中。")}else if(call.phase==="start_unknown"&&live?.phase==="uncertain"){showCallResult("蜂窝呼叫结果仍不明确；不会再次拨号，请挂断或等待 10 秒守卫。",true)}else if(["active","media_failed"].includes(call.phase)&&cellular.status==="fulfilled"&&!live){showCallResult("Agent 已确认蜂窝通话结束，线路已空闲。");await releaseCurrentCall()}
  }));
  state.callStatusLoading=false;el("refresh-call-status").disabled=false;renderCallStatuses(lines);
}

function renderCallStatuses(lines){const root=el("call-statuses");root.replaceChildren();renderIncomingCalls(lines);if(!lines.length){root.append(empty("尚无线路配置"));return}for(const line of lines){const entry=state.providerStatuses.get(line.id),cellular=state.cellularStatuses.get(line.id),card=document.createElement("article");card.className="card";const title=document.createElement("h3");title.textContent=line.name||line.id;card.append(title);const badges=document.createElement("div");badges.className="badges";if(entry?.status){const status=entry.status,runtime=document.createElement("span"),voice=document.createElement("span"),occupied=document.createElement("span"),pending=status.pending_incoming_call;runtime.className=`badge ${status.runtime?.condition==="running"?"good":"warn"}`;runtime.textContent=`VoWiFi ${status.runtime?.condition||"unknown"}`;voice.className=`badge ${status.voice?.available?"good":"bad"}`;voice.textContent=`语音 ${status.voice?.code||status.voice?.condition||"unknown"}`;occupied.className=`badge ${status.active_call||pending?"warn":"neutral"}`;occupied.textContent=status.active_call?`VoWiFi 占用 ${status.active_call.condition}`:pending?`呼入等待 ${pending.caller}`:"VoWiFi 空闲";badges.append(runtime,voice,occupied)}else{const failed=document.createElement("span");failed.className="badge bad";failed.textContent=`VoWiFi 状态不可用 ${entry?.error?.code||entry?.error?.message||"未采样"}`;badges.append(failed)}const modem=document.createElement("span"),ready=operationReadyForLine(line.id,"cellular_call"),sessions=cellular?.status?.sessions||[],active=sessions.find(session=>!["ended","expired"].includes(session.phase));modem.className=`badge ${active?"warn":ready?"good":"neutral"}`;modem.textContent=active?`蜂窝占用 ${active.phase}`:ready?"蜂窝语音就绪":"蜂窝语音不可用";badges.append(modem);if(cellular?.error){const failed=document.createElement("span");failed.className="badge bad";failed.textContent=`蜂窝状态不可用 ${cellular.error.code||cellular.error.message}`;badges.append(failed)}card.append(badges);root.append(card)}}

function renderIncomingCalls(lines){const root=el("incoming-calls");root.replaceChildren();const pending=[];for(const line of lines){const call=state.providerStatuses.get(line.id)?.status?.pending_incoming_call;if(call)pending.push({line,call})}if(!pending.length){root.append(empty("当前没有呼入电话"));return}for(const {line,call} of pending){const card=document.createElement("article");card.className="card";const title=document.createElement("h3");title.textContent=call.caller||"未知来电";const detail=document.createElement("div");detail.className="muted";detail.textContent=`${line.name||line.id} · ${fmtTime(call.received_at)} · ${call.call_id}`;const toolbar=document.createElement("div");toolbar.className="toolbar";const answer=actionButton("接听",()=>beginIncomingCall(line,call));const reject=actionButton("拒接",()=>rejectIncomingCall(line,call));reject.classList.add("danger");answer.disabled=Boolean(state.currentCall);reject.disabled=Boolean(state.currentCall);toolbar.append(answer,reject);card.append(title,detail,toolbar);root.append(card)}}

async function beginIncomingCall(line,pending){if(state.currentCall)return;const bufferMS=Number(el("call-buffer").value)||500;const call={mode:"vowifi",line_id:line.id,callee:pending.caller||"呼入",buffer_ms:bufferMS,call_id:pending.call_id,start_operation_id:callIdentity("ui-incoming-answer"),end_operation_id:"",lease:null,media:null,phase:"preparing",ending:false,muted:false,direction:"incoming"};try{call.media=new CallMedia(bufferMS,(type,detail)=>onCallMediaEvent(call,type,detail));call.media.openAudioFromGesture()}catch(error){showCallResult(error.message,true);return}state.currentCall=call;updateCallControls();showCallResult("正在建立呼入双向音频；成功前不会向运营商确认接听…");try{call.lease=await jsonRequest("/v1/media/leases",{method:"POST",body:JSON.stringify({line_id:call.line_id,call_id:call.call_id})});await call.media.prepare(call.lease,call.call_id);call.phase="ready";await submitCallStart()}catch(error){if(state.currentCall===call&&call.phase!=="start_unknown"&&call.phase!=="active"){showCallResult(`接听失败：${callErrorDetail(error)}`,true);await releaseCurrentCall();await refreshCallStatuses()}}}

async function rejectIncomingCall(line,pending){try{await jsonRequest(`/v1/lines/${encodeURIComponent(line.id)}/vowifi/calls/incoming/reject`,{method:"POST",body:JSON.stringify({operation_id:callIdentity("ui-incoming-reject"),call_id:pending.call_id,reason_code:"user_rejected"})});showCallResult(`已拒接 ${pending.caller||"呼入电话"}`);await refreshCallStatuses();await loadCallHistory()}catch(error){showCallResult(`拒接失败：${callErrorDetail(error)}`,true)}}

function showCallResult(message,isError=false){const result=el("call-result");result.classList.remove("hidden");result.textContent=message;result.style.color=isError?"#b42318":"#344054"}
function updateCallControls(){const call=state.currentCall,retry=call?.phase==="start_unknown"&&call.mode!=="cellular",active=call?.phase==="active";el("call-line").disabled=Boolean(call)||!el("call-line").value;el("call-number").disabled=Boolean(call);el("call-buffer").disabled=Boolean(call);el("call-start").classList.toggle("hidden",Boolean(call)&&!retry);el("call-start").textContent=retry?"重试同一呼叫请求":"呼叫";el("call-mute").classList.toggle("hidden",!active);el("call-end").classList.toggle("hidden",!call);el("call-end").disabled=call?.ending===true;el("call-dialpad").classList.toggle("hidden",Boolean(call));el("call-backspace").classList.toggle("hidden",Boolean(call));el("call-active-keypad").classList.toggle("hidden",!active);if(!active){state.dtmfEcho="";el("call-dtmf-echo").textContent="按键会发送给当前通话"}}
function callIdentity(prefix){return globalThis.crypto?.randomUUID?`${prefix}-${crypto.randomUUID()}`:operationID(prefix)}
function callErrorDetail(error){return[error.kind,error.code,error.layer,error.detail].filter(Boolean).join(" · ")||error.message}
function startResultIsAmbiguous(error){return!error.status||["operation_timeout","provider_transport_failed","invalid_provider_response","cellular_call_start_uncertain"].includes(error.code)}
function endResultIsAmbiguous(error){return!error.status||["operation_timeout","provider_transport_failed","invalid_provider_response"].includes(error.code)}

async function beginCall(event){
  event.preventDefault();if(state.currentCall){if(state.currentCall.phase==="start_unknown"&&state.currentCall.mode!=="cellular")await submitCallStart();return}
  let callee,bufferMS;try{callee=normalizeDialTarget(el("call-number").value);bufferMS=Number(el("call-buffer").value);if(!Number.isInteger(bufferMS)||bufferMS<100||bufferMS>2000)throw new Error("音频排队上限必须是 100–2000 ms 的整数")}
  catch(error){showCallResult(error.message,true);return}
  const route=selectedCallRoute();if(!route){showCallResult("请选择有效线路",true);return}
  const call={mode:route.mode,line_id:route.line_id,expected_card_id:route.expected_card_id,callee,buffer_ms:bufferMS,call_id:callIdentity("browser-call"),start_operation_id:callIdentity("ui-call-start"),end_operation_id:"",lease:null,media:null,phase:"preparing",ending:false,muted:false,direction:"outgoing"};
  try{call.media=new CallMedia(bufferMS,(type,detail)=>onCallMediaEvent(call,type,detail));call.media.openAudioFromGesture()}catch(error){showCallResult(error.message,true);return}
  state.currentCall=call;updateCallControls();showCallResult("正在申请麦克风并建立零费用双向音频探测；请说话以确认采集链路…");
  try{call.lease=await jsonRequest(call.mode==="cellular"?"/v1/cellular/media/leases":"/v1/media/leases",{method:"POST",body:JSON.stringify(call.mode==="cellular"?{line_id:call.line_id,call_id:call.call_id,expected_card_id:call.expected_card_id}:{line_id:call.line_id,call_id:call.call_id})});await call.media.prepare(call.lease,call.call_id);call.phase="ready";showCallResult("双向音频探测通过，正在向运营商提交呼叫…");await submitCallStart()}
  catch(error){if(state.currentCall===call&&call.phase!=="start_unknown"&&call.phase!=="active"){showCallResult(`呼叫前检查失败：${callErrorDetail(error)}。未确认运营商呼叫。`,true);await releaseCurrentCall()}}
}

async function submitCallStart(){const call=state.currentCall;if(!call||!["ready","start_unknown"].includes(call.phase))return;el("call-start").disabled=true;const incoming=call.direction==="incoming",cellular=call.mode==="cellular",path=cellular?`/v1/lines/${encodeURIComponent(call.line_id)}/cellular/calls/start`:`/v1/lines/${encodeURIComponent(call.line_id)}/vowifi/${incoming?"calls/incoming/answer":"calls/start"}`,body=cellular?{operation_id:call.start_operation_id,session_id:call.lease?.session_id,callee:call.callee,expected_card_id:call.expected_card_id}:incoming?{operation_id:call.start_operation_id,call_id:call.call_id,media_session_id:call.lease?.session_id,media_buffer_ms:call.buffer_ms}:{operation_id:call.start_operation_id,call_id:call.call_id,media_session_id:call.lease?.session_id,callee:call.callee,media_buffer_ms:call.buffer_ms,expected_card_id:call.expected_card_id};try{const result=await jsonRequest(path,{method:"POST",body:JSON.stringify(body)});call.phase="active";call.media.markActive();showCallResult(`通话中 · ${call.callee} · ${result.code||"active"}`);await refreshCallStatuses()}
catch(error){if(startResultIsAmbiguous(error)){call.phase="start_unknown";showCallResult(call.mode==="cellular"?`蜂窝呼叫结果不明确：${callErrorDetail(error)}。不会再次拨号；请挂断或等待 10 秒守卫。`:`呼叫结果不明确：${callErrorDetail(error)}。请重试同一请求或挂断；不会创建第二次呼叫。`,true);updateCallControls()}else{showCallResult(`呼叫失败：${callErrorDetail(error)}`,true);await releaseCurrentCall();await refreshCallStatuses()}}
finally{el("call-start").disabled=false;updateCallControls();void loadCallHistory()}}

async function sendCallDTMF(signal){const call=state.currentCall;if(!call||call.phase!=="active"||state.dtmfSending||!/^[0-9*#]$/.test(signal))return;state.dtmfSending=true;for(const button of document.querySelectorAll("[data-dtmf-key]"))button.disabled=true;const cellular=call.mode==="cellular",path=cellular?`/v1/lines/${encodeURIComponent(call.line_id)}/cellular/calls/dtmf`:`/v1/lines/${encodeURIComponent(call.line_id)}/vowifi/calls/dtmf`,body=cellular?{operation_id:callIdentity("ui-cellular-dtmf"),session_id:call.lease?.session_id,signal}:{operation_id:callIdentity("ui-vowifi-dtmf"),call_id:call.call_id,signal,duration_ms:160};try{const result=await jsonRequest(path,{method:"POST",body:JSON.stringify(body)});state.dtmfEcho=(state.dtmfEcho+signal).slice(-32);el("call-dtmf-echo").textContent=`已发送 ${state.dtmfEcho} · ${result.code||"DTMF"}`}catch(error){showCallResult(`按键 ${signal} 发送失败：${callErrorDetail(error)}`,true)}finally{state.dtmfSending=false;for(const button of document.querySelectorAll("[data-dtmf-key]"))button.disabled=state.currentCall?.phase!=="active"}}

function onCallMediaEvent(call,type,detail){if(state.currentCall!==call)return;if(type==="reconnecting")showCallResult(detail||"媒体链路短暂中断，正在恢复同一通话…",true);else if(type==="reconnected")showCallResult(`通话中 · ${call.callee} · 媒体链路已恢复`);else if(type==="ended"){showCallResult(`通话已结束 · ${detail||"后端已关闭媒体"}`);void releaseCurrentCall().then(refreshCallStatuses).then(loadCallHistory)}else if(type==="failed"&&call.phase==="active"){call.phase="media_failed";call.media.close();showCallResult(`媒体链路超过恢复窗口：${detail}。10 秒精确通话守卫将停止该通话。`,true);updateCallControls()}}

function toggleCallMute(){const call=state.currentCall;if(!call||call.phase!=="active")return;call.muted=!call.muted;call.media.setMuted(call.muted);el("call-mute").textContent=call.muted?"取消静音":"静音";showCallResult(`通话中 · ${call.callee}${call.muted?" · 已静音":""}`)}

async function hangupCall(){const call=state.currentCall;if(!call||call.ending)return;call.ending=true;updateCallControls();call.media?.close();if(!call.end_operation_id)call.end_operation_id=callIdentity("ui-call-end");showCallResult("正在发送精确挂断请求；媒体心跳已停止，若请求失败，10 秒守卫仍会继续挂断…");const cellular=call.mode==="cellular",path=cellular?`/v1/lines/${encodeURIComponent(call.line_id)}/cellular/calls/hangup`:`/v1/lines/${encodeURIComponent(call.line_id)}/vowifi/calls/end`,body=cellular?{operation_id:call.end_operation_id,session_id:call.lease?.session_id}:{operation_id:call.end_operation_id,call_id:call.call_id,reason_code:"user_hangup"};try{const result=await jsonRequest(path,{method:"POST",body:JSON.stringify(body)});showCallResult(`通话已由${cellular?" Agent":" Provider"}确认结束 · ${result.code||"ended"}`);await releaseCurrentCall();await refreshCallStatuses();await loadCallHistory();return}
catch(error){if(["call_not_found","cellular_call_not_found"].includes(error.code)){showCallResult("后端已确认没有该通话；线路未被本页继续占用。");await releaseCurrentCall();await refreshCallStatuses();return}if(!endResultIsAmbiguous(error))call.end_operation_id="";showCallResult(`挂断尚未确认：${callErrorDetail(error)}。媒体已断开，服务端精确守卫会继续处理；也可再次点击挂断。`,true)}finally{if(state.currentCall===call){call.ending=false;updateCallControls()}}}

async function releaseCurrentCall(){const call=state.currentCall;if(!call)return;call.media?.close();state.currentCall=null;updateCallControls();if(call.lease?.session_id){try{await jsonRequest(call.mode==="cellular"?"/v1/cellular/media/leases":"/v1/media/leases",{method:"DELETE",body:JSON.stringify({session_id:call.lease.session_id})})}catch{}}}

async function loadCallHistory(){if(state.callHistoryLoading)return;state.callHistoryLoading=true;el("refresh-call-history").disabled=true;try{const payload=await jsonRequest("/v1/calls?limit=100");state.callHistory=Array.isArray(payload.calls)?payload.calls:[];renderCallHistory()}catch(error){el("call-history").replaceChildren(errorCard(`通话记录读取失败：${error.code||error.message}`))}finally{state.callHistoryLoading=false;el("refresh-call-history").disabled=false}}

function renderCallHistory(){const root=el("call-history");root.replaceChildren();const calls=state.callHistory||[];if(!calls.length){root.append(empty("尚无通话记录"));el("clear-call-history").disabled=true;return}el("clear-call-history").disabled=!calls.some(call=>call.ended_at);for(const call of calls){const row=document.createElement("article");row.className="call-history-row";const detail=document.createElement("div"),peer=document.createElement("div"),meta=document.createElement("div");peer.className="call-history-peer";peer.textContent=call.peer||"未知号码";meta.className="muted";const line=(state.snapshot?.catalog?.lines||[]).find(candidate=>candidate.id===call.line_id);const direction=call.direction==="in"?"↙ 呼入":call.direction==="out"?"↗ 呼出":"通话",transport=call.transport==="cellular"?"蜂窝 Modem":"VoWiFi",duration=callDuration(call);meta.textContent=`${direction} · ${transport} · ${line?.name||call.line_id} · ${fmtTime(call.started_at)}${duration?` · ${duration}`:""}`;detail.append(peer,meta);const actions=document.createElement("div");actions.className="call-history-actions";const status=document.createElement("span");status.className=`call-history-status ${call.status||""}`;status.textContent=callStatusLabel(call.status);actions.append(status);if(call.peer){const redial=document.createElement("button");redial.type="button";redial.className="secondary";redial.textContent="回拨";redial.addEventListener("click",()=>selectHistoryCall(call));actions.append(redial)}if(call.ended_at){const remove=document.createElement("button");remove.type="button";remove.className="secondary";remove.textContent="删除";remove.addEventListener("click",()=>deleteCallHistory([call.id]));actions.append(remove)}row.append(detail,actions);root.append(row)}}

function callDuration(call){if(!call.answered_at||!call.ended_at)return"";const seconds=Math.max(0,Math.round((new Date(call.ended_at)-new Date(call.answered_at))/1000));return`${Math.floor(seconds/60)}:${String(seconds%60).padStart(2,"0")}`}
function callStatusLabel(status){return({dialing:"拨号中",ringing:"响铃中",answered:"已接通",ended:"已结束",failed:"失败",missed:"未接",rejected:"已拒接",interrupted:"已中断"})[status]||status||"未知"}
function selectHistoryCall(call){const route=callRouteValue(call.transport==="cellular"?"cellular":"vowifi",call.line_id),select=el("call-line"),available=[...select.options].some(option=>option.value===route);if(!available){showCallResult("该历史线路当前不可用于呼叫；没有自动切换到其他 SIM。",true);return}select.value=route;el("call-number").value=call.peer;showCallResult("已从历史记录填入号码；确认线路后点击呼叫。")}
async function deleteCallHistory(ids){if(!ids.length||!confirm(`删除 ${ids.length} 条已结束通话记录？`))return;try{await jsonRequest("/v1/calls",{method:"DELETE",body:JSON.stringify({ids})});await loadCallHistory()}catch(error){showCallResult(`删除通话记录失败：${error.code||error.message}`,true)}}
async function clearCallHistory(){const ids=(state.callHistory||[]).filter(call=>call.ended_at).map(call=>call.id);if(!ids.length)return;if(!confirm(`清空 ${ids.length} 条已结束通话记录？正在进行的通话不会被删除。`))return;try{await jsonRequest("/v1/calls",{method:"DELETE",body:JSON.stringify({ids})});await loadCallHistory()}catch(error){showCallResult(`清空通话记录失败：${error.code||error.message}`,true)}}

function renderMessageLines(lines){
  const select=el("message-line"),selected=select.value;select.replaceChildren();
  if(!lines.length){const option=document.createElement("option");option.value="";option.textContent="尚无线路配置";select.append(option);select.disabled=true;return}
  for(const line of lines){const vowifi=document.createElement("option");vowifi.value=messageRouteValue("vowifi",line.id);vowifi.textContent=`${line.name||line.id} · VoWiFi · ${line.sim?.msisdn||"无号码"}`;select.append(vowifi);if(cellularSMSTargetForLine(line)){const cellular=document.createElement("option");cellular.value=messageRouteValue("cellular",line.id);cellular.textContent=`${line.name||line.id} · 蜂窝 Modem · ${line.sim?.msisdn||"无号码"}`;select.append(cellular)}}
  const pending=state.pendingMessage,locked=pending?messageRouteValue(pending.mode||"vowifi",pending.line_id):"",values=[...select.options].map(option=>option.value);select.value=values.includes(locked||selected)?(locked||selected):values[0];select.disabled=Boolean(pending);
}

function messageRouteValue(mode,lineID){return`${mode}:${lineID}`}
function selectedMessageRoute(){const value=el("message-line").value,separator=value.indexOf(":");if(separator<1)return null;return{mode:value.slice(0,separator),line_id:value.slice(separator+1)}}
function cellularSMSTargetForLine(line){const matches=[];for(const agent of state.snapshot?.agents||[]){if(agent.topology?.modem_condition!=="ready")continue;for(const modem of agent.topology?.modems||[]){if(modem.equipment_id===line.sim?.imei&&modem.sim?.iccid===line.card_id&&modem.at_control?.state==="ready"&&modem.at_control?.sms)matches.push({agent,modem})}}return matches.length===1?matches[0]:null}
async function refreshSelectedCellularMessages(){const route=selectedMessageRoute(),button=el("message-refresh");if(!route||route.mode!=="cellular"){button.disabled=false;return}button.disabled=true;showMessageResult("正在从精确蜂窝 Modem 读取短信和送达报告；不会发送短信…");try{const payload=await jsonRequest(`/v1/lines/${encodeURIComponent(route.line_id)}/cellular/messages`);renderMessages(Array.isArray(payload.messages)?payload.messages:[]);showMessageResult(`蜂窝短信已刷新 · ${payload.messages?.length||0} 条事实`)}catch(error){showMessageResult(`蜂窝短信刷新失败：${[error.code,error.detail].filter(Boolean).join(" · ")||error.message}`,true)}finally{button.disabled=false}}

function renderMessages(messages){
  const root=el("messages");root.replaceChildren();el("message-count").textContent=`${messages.length} 条事实`;
  if(!messages.length){root.append(empty("尚无短信事实"));return}
  for(const message of [...messages].reverse()){
    const card=document.createElement("article");card.className="card message-card";card.dataset.kind=message.kind||"unknown";
    const head=document.createElement("div");head.className="card-head";const title=document.createElement("div"),heading=document.createElement("h3"),meta=document.createElement("div"),kind=document.createElement("span");
    heading.textContent=messageKind(message.kind);meta.className="muted";meta.textContent=`${message.line_id||"未知线路"} · ${fmtTime(message.observed_at||message.received_at)}`;kind.className=`badge ${message.kind==="received"?"good":message.kind==="delivery"?"warn":"neutral"}`;kind.textContent=message.state||message.kind||"unknown";title.append(heading,meta);head.append(title,kind);card.append(head);
    const body=document.createElement("div");body.className="message-body";body.textContent=message.body||"（无正文）";card.append(body);
    const details=document.createElement("div");details.className="muted";details.textContent=[message.sender?`发件 ${message.sender}`:"",message.recipient?`收件 ${message.recipient}`:"",message.message_id?`消息 ${message.message_id}`:"",message.part?`分段 ${message.part}`:"",message.sip_code?`SIP ${message.sip_code}`:"",message.error?`错误 ${message.error}`:""].filter(Boolean).join(" · ");card.append(details);root.append(card)
  }
}

function messageKind(kind){return kind==="received"?"收到短信":kind==="submitted"?"短信已提交":kind==="delivery"?"送达报告":"未知短信事实"}
function messageIdentity(prefix){if(globalThis.crypto?.randomUUID)return`${prefix}-${crypto.randomUUID()}`;return operationID(prefix)}
function savePendingMessage(){try{if(state.pendingMessage)sessionStorage.setItem("mdd.pendingMessage",JSON.stringify(state.pendingMessage));else sessionStorage.removeItem("mdd.pendingMessage")}catch{}}
function restorePendingMessage(){
  let restored=false;if(!state.pendingMessage){try{const saved=JSON.parse(sessionStorage.getItem("mdd.pendingMessage")||"null");if(saved&&typeof saved.line_id==="string"&&typeof saved.operation_id==="string"&&typeof saved.message_id==="string"&&typeof saved.recipient==="string"&&typeof saved.body==="string"){saved.mode=saved.mode==="cellular"?"cellular":"vowifi";state.pendingMessage=saved;restored=true}}catch{}}
  const pending=state.pendingMessage;if(!pending)return setMessageDraftLocked(false);
  el("message-line").value=messageRouteValue(pending.mode||"vowifi",pending.line_id);el("message-recipient").value=pending.recipient;el("message-body").value=pending.body;setMessageDraftLocked(true);if(restored)showMessageResult("上次发送尚未取得明确成功结果。可重试同一幂等请求，不会生成新的发送身份。",true);
}
function setMessageDraftLocked(locked){el("message-line").disabled=locked||!el("message-line").value;el("message-recipient").disabled=locked;el("message-body").disabled=locked;el("message-discard").classList.toggle("hidden",!locked);el("message-send").textContent=locked?"重试同一请求":"发送"}
function showMessageResult(message,isError=false){const result=el("message-result");result.classList.remove("hidden");result.textContent=message;result.style.color=isError?"#b42318":"#344054"}
async function sendMessage(event){
  event.preventDefault();if(state.messageSending)return;
  if(!state.pendingMessage){const recipient=el("message-recipient").value.trim(),body=el("message-body").value,route=selectedMessageRoute();if(!route){showMessageResult("请选择有效短信线路",true);return}if(new TextEncoder().encode(recipient).byteLength>128||new TextEncoder().encode(body).byteLength>8192){showMessageResult("收件号码不能超过 128 字节，正文不能超过 8192 字节。",true);return}state.pendingMessage={mode:route.mode,line_id:route.line_id,operation_id:messageIdentity("ui-message-send"),message_id:messageIdentity("message"),recipient,body};savePendingMessage();setMessageDraftLocked(true)}
  const pending=state.pendingMessage;if(!pending.line_id||!pending.recipient||!pending.body){state.pendingMessage=null;savePendingMessage();setMessageDraftLocked(false);showMessageResult("线路、收件号码和正文不能为空。",true);return}
  state.messageSending=true;el("message-send").disabled=true;showMessageResult("发送处理中；在服务器明确确认前将保留同一幂等请求…");
  try{const path=pending.mode==="cellular"?`/v1/lines/${encodeURIComponent(pending.line_id)}/cellular/messages`:`/v1/lines/${encodeURIComponent(pending.line_id)}/vowifi/messages/send`,payload=await jsonRequest(path,{method:"POST",body:JSON.stringify({operation_id:pending.operation_id,message_id:pending.message_id,recipient:pending.recipient,body:pending.body})});state.pendingMessage=null;savePendingMessage();setMessageDraftLocked(false);el("message-body").value="";showMessageResult(`服务器已接受：${payload.code||"sent"} · ${payload.message_id||pending.message_id}`)}
  catch(error){const detail=[error.kind,error.code,error.layer,error.detail].filter(Boolean).join(" · ");showMessageResult(`发送未取得成功确认：${detail||error.message}。草稿已锁定；重试将复用同一请求，不会静默创建第二次付费发送。`,true)}
  finally{state.messageSending=false;el("message-send").disabled=false}
}
function discardPendingMessage(){if(!state.pendingMessage)return;if(!confirm("这只会放弃本页保存的幂等重试身份，不会撤回已经可能提交的短信。继续？"))return;state.pendingMessage=null;savePendingMessage();setMessageDraftLocked(false);showMessageResult("已放弃本页的幂等重试身份；未自动发送任何新短信。")}

function renderLines(catalog,projections){
  const root=el("lines");root.replaceChildren();
  if(!catalog.length){root.append(empty("尚无线路配置"));return}
  for(const line of catalog){const projection=projections.find(item=>item.line_id===line.id);root.append(lineCard(line,projection))}
}

function lineCard(line,projection){
  const card=document.createElement("article");card.className="card";card.dataset.line=line.id;
  const head=document.createElement("div");head.className="card-head";
  const title=document.createElement("div"),h3=document.createElement("h3"),meta=document.createElement("div");h3.textContent=line.name||line.id;meta.className="muted";meta.textContent=`${line.id} · ${line.sim?.msisdn||"无号码"} · 出口 ${line.network?.egress_country||"未配置"}`;title.append(h3,meta);
  const enabled=document.createElement("span");enabled.className=`badge ${line.enabled?"good":"neutral"}`;enabled.textContent=line.enabled?"已启用":"已停用";head.append(title,enabled);card.append(head);
  const operations=document.createElement("div");operations.className="operation-grid";
  for(const [name,value] of Object.entries(projection?.operations||{})){const item=document.createElement("span");item.className=`badge ${badgeClass(value.ready)}`;item.textContent=`${name}: ${value.ready?"就绪":`阻塞 ${value.blocked?.join(",")||"未知"}`}`;operations.append(item)}card.append(operations);
  const toolbar=document.createElement("div");toolbar.className="toolbar";
  toolbar.append(actionButton("启动 VoWiFi",()=>runtimeAction(line.id,"start")),actionButton("停止 VoWiFi",()=>runtimeAction(line.id,"stop")),actionButton("PCM 回环诊断",()=>mediaDiagnostic(line.id)));
  card.append(toolbar);
  const result=document.createElement("div");result.className="result hidden";result.dataset.result="";card.append(result);
  const facts=projection?.facts||[];if(facts.length)card.append(factTable(facts));else{const missing=document.createElement("p");missing.className="muted";missing.textContent="尚无该线路的实时权威事实。";card.append(missing)}
  return card;
}

function actionButton(label,action){const button=document.createElement("button");button.className="secondary";button.textContent=label;button.addEventListener("click",async()=>{button.disabled=true;try{await action(button)}finally{button.disabled=false}});return button}
function resultFor(lineID,message,isError=false){const result=document.querySelector(`[data-line="${CSS.escape(lineID)}"] [data-result]`);if(!result)return;result.classList.remove("hidden");result.textContent=message;result.style.color=isError?"#b42318":"#344054"}

async function runtimeAction(lineID,action){
  resultFor(lineID,`${action==="start"?"启动":"停止"}请求处理中…`);
  try{const payload=await jsonRequest(`/v1/lines/${encodeURIComponent(lineID)}/vowifi/runtime/${action}`,{method:"POST",body:JSON.stringify({operation_id:operationID(`ui-${action}`)})});resultFor(lineID,JSON.stringify({code:payload.code,status:payload.status},null,2))}
  catch(error){resultFor(lineID,`请求失败：${error.message}`,true)}
}

async function mediaDiagnostic(lineID){
  if(state.diagnostics.has(lineID)){resultFor(lineID,"该线路已有诊断进行中",true);return}
  const callID=operationID("pcm-diagnostic");state.diagnostics.set(lineID,callID);let lease=null,socket=null;
  resultFor(lineID,"正在创建零费用 PCM 回环诊断；不会拨号，也不检查麦克风/扬声器…");
  try{
    lease=await jsonRequest("/v1/media/leases",{method:"POST",body:JSON.stringify({line_id:lineID,call_id:callID})});
    await runPCMCanary(lease,callID,progress=>resultFor(lineID,progress));
    resultFor(lineID,"PASS：同一 WSS 已完成 2 帧非静音 PCM 精确双向回环；未发起电话。")
  }catch(error){resultFor(lineID,`FAIL：${error.message}`,true)}
  finally{if(socket)socket.close();if(lease?.session_id){try{await jsonRequest("/v1/media/leases",{method:"DELETE",body:JSON.stringify({session_id:lease.session_id})})}catch{}}state.diagnostics.delete(lineID)}
}

function runPCMCanary(lease,ticket,progress){return new Promise((resolve,reject)=>{
  const scheme=location.protocol==="https:"?"wss":"ws";const socket=new WebSocket(`${scheme}://${location.host}${lease.ws_path}`);socket.binaryType="arraybuffer";
  let challenge="",echoes=0,readyStatus=false,settled=false;const timer=setTimeout(()=>finish(new Error("PCM 诊断 15 秒超时")),15000);
  function finish(error){if(settled)return;settled=true;clearTimeout(timer);socket.close(1000,"diagnostic complete");error?reject(error):resolve()}
  socket.onerror=()=>finish(new Error("媒体 WSS 连接失败"));socket.onclose=event=>{if(!settled)finish(new Error(`媒体 WSS 已关闭 (${event.code})`))};
  socket.onopen=()=>socket.send(JSON.stringify({type:"browser.media.hello",version:1,session_id:lease.session_id,ticket}));
  socket.onmessage=event=>{
    if(event.data instanceof ArrayBuffer){const frame=new Uint8Array(event.data);if(frame.byteLength!==320)return finish(new Error("回环 PCM 帧长度错误"));echoes++;if(echoes===2){socket.send(JSON.stringify({type:"browser.media.evidence",version:1,challenge,capture_callbacks:2,playback_callbacks:2,played_frames:2}));progress("PCM 已精确回环，正在核对 Provider 证据…")}return}
    let message;try{message=JSON.parse(event.data)}catch{return finish(new Error("媒体控制消息不是 JSON"))}
    if(message.type==="browser.media.claimed"){challenge=message.challenge||"";if(!challenge)return finish(new Error("媒体 challenge 缺失"))}
    else if(message.type==="browser.media.started"){if(message.purpose!=="canary")return finish(new Error("媒体会话不是零费用诊断"));for(let index=0;index<2;index++){const frame=new ArrayBuffer(320),view=new DataView(frame);for(let offset=0;offset<320;offset+=2)view.setInt16(offset,1000+index,true);socket.send(frame)}}
    else if(message.type==="browser.media.status"){readyStatus=message.ready===true}
    else if(message.type==="browser.media.ready"){if(!readyStatus||echoes!==2)return finish(new Error("PCM 证据不完整"));finish()}
  };
})}

function factTable(facts){const table=document.createElement("table");table.className="fact-table";const head=document.createElement("thead"),row=document.createElement("tr");for(const text of["层","状态","可用/新鲜","代码","观测时间"]){const th=document.createElement("th");th.textContent=text;row.append(th)}head.append(row);table.append(head);const body=document.createElement("tbody");for(const fact of facts){const tr=document.createElement("tr");for(const value of[fact.layer,fact.condition,`${fact.available?"可用":"不可用"} / ${fact.fresh?"新鲜":"过期"}`,fact.code||"—",fmtTime(fact.observed_at)]){const td=document.createElement("td");td.textContent=value;tr.append(td)}body.append(tr)}table.append(body);return table}

function modemDataLine(modem){return(state.lineCatalog?.lines||[]).find(line=>line.card_id===modem.sim?.iccid&&line.sim?.imei===modem.equipment_id)}

async function loadCellularDataStatus(lineID){
  const prior=state.cellularData.get(lineID);if(prior?.loading)return;
  state.cellularData.set(lineID,{loading:true});
  try{state.cellularData.set(lineID,await jsonRequest(`/v1/lines/${encodeURIComponent(lineID)}/cellular/data/sessions`))}
  catch(error){state.cellularData.set(lineID,{state:"error",detail:error.code||error.message})}
  if(state.snapshot)renderAgents(state.snapshot.agents||[])
}

function appendCellularDataControls(item,modem){
  const line=modemDataLine(modem),protectedData=modem.capabilities?.cellular_data===true&&modem.network?.data_guard==="protected";
  if(!line||!protectedData)return;
  let status=state.cellularData.get(line.id);if(!status){loadCellularDataStatus(line.id);status={loading:true}}
  const panel=document.createElement("div"),summary=document.createElement("div"),toolbar=document.createElement("div");panel.className="panel";summary.className="muted";toolbar.className="toolbar";
  if(status.loading){summary.textContent="数据借用状态读取中…";panel.append(summary);item.append(panel);return}
  if(status.state==="ready"){
    summary.textContent=`数据借用中 · MBN ${status.profile} · SOCKS5 端口 ${status.listen_port} · ${status.used_bytes}/${status.max_bytes} bytes · 到期 ${fmtTime(status.expires_at)}`;
    if(status.username&&status.password){const credential=document.createElement("code");credential.textContent=`${status.username}:${status.password}`;panel.append(summary,credential)}else{panel.append(summary)}
    const stop=document.createElement("button");stop.className="secondary";stop.textContent="停止并撤销借用";stop.addEventListener("click",async()=>{stop.disabled=true;try{await jsonRequest(`/v1/lines/${encodeURIComponent(line.id)}/cellular/data/sessions/${encodeURIComponent(status.session_id)}`,{method:"DELETE",body:"{}"});state.cellularData.set(line.id,{state:"stopped",line_id:line.id});renderAgents(state.snapshot?.agents||[])}catch(error){showNotice(`停止数据借用失败：${error.code||error.message}`);stop.disabled=false}});toolbar.append(stop);panel.append(toolbar);item.append(panel);return
  }
  summary.textContent=status.state==="error"?`数据借用状态失败：${status.detail}`:"默认隔离；只有显式会话可借用流量。飞行模式或数据未开启时，正常连接会直接返回失败。";
  const ttl=document.createElement("input"),quota=document.createElement("input"),start=document.createElement("button");ttl.type="number";ttl.min="60";ttl.max="86400";ttl.value="900";ttl.title="借用秒数";ttl.style.maxWidth="9rem";quota.type="number";quota.min="1024";quota.max=String(1024*1024*1024*1024);quota.value=String(100*1024*1024);quota.title="最大字节数";quota.style.maxWidth="12rem";start.textContent="开始数据借用";
  start.addEventListener("click",async()=>{start.disabled=true;try{const created=await jsonRequest(`/v1/lines/${encodeURIComponent(line.id)}/cellular/data/sessions`,{method:"POST",body:JSON.stringify({ttl_seconds:Number(ttl.value),max_bytes:Number(quota.value)})});state.cellularData.set(line.id,created);renderAgents(state.snapshot?.agents||[])}catch(error){showNotice(`数据借用失败：${error.detail||error.code||error.message}`);start.disabled=false}});
  toolbar.append(ttl,quota,start);panel.append(summary,toolbar);item.append(panel)
}

function renderAgents(agents){const root=el("agents");root.replaceChildren();if(!agents.length){root.append(empty("当前没有 Agent 连接"));return}for(const agent of agents){const card=document.createElement("article");card.className="card";const title=document.createElement("h3");title.textContent=agent.agent_id;const meta=document.createElement("div");meta.className="muted";meta.textContent=`进程世代 ${agent.process_generation} · 心跳 ${fmtTime(agent.last_seen)} · 拓扑 ${fmtTime(agent.last_report)}`;card.append(title,meta);const readerCondition=document.createElement("span"),modemCondition=document.createElement("span");readerCondition.className=`badge ${agent.topology?.reader_condition==="ready"?"good":"warn"}`;readerCondition.textContent=`读卡器 ${agent.topology?.reader_condition||"未上报"}`;modemCondition.className=`badge ${agent.topology?.modem_condition==="ready"?"good":"neutral"}`;modemCondition.textContent=`Modem ${agent.topology?.modem_condition||"未上报"}`;card.append(readerCondition,modemCondition);const readers=document.createElement("div");readers.className="readers";for(const reader of agent.topology?.readers||[]){const item=document.createElement("div");item.className="reader";const slots=reader.secure_elements?.length?reader.secure_elements:reader.euicc?[{euicc:reader.euicc}]:[];const identity=reader.card_id||slots.map(slot=>`${slot.label?`${slot.label} `:""}${slot.euicc.eid}`).join(" · ")||"无卡/身份未就绪";item.textContent=`${reader.reader_name} · ${reader.identity_state} · ${identity}`;for(const slot of slots){const profiles=document.createElement("div");profiles.className="muted";profiles.textContent=`${slot.label?`${slot.label}: `:""}${slot.euicc.profiles_available?`profiles: ${slot.euicc.profiles.map(profile=>`${profile.iccid} (${profile.state})`).join(", ")||"空白"}`:"profiles 查询不可用"}`;item.append(profiles)}readers.append(item)}for(const modem of agent.topology?.modems||[]){const item=document.createElement("div");item.className="reader";item.textContent=`${modem.model||modem.manufacturer||"蜂窝 Modem"} · ${modem.equipment_id||"无 IMEI"} · ICCID ${modem.sim?.iccid||"未就绪"}`;const detail=document.createElement("div");detail.className="muted";const guard={protected:"已隔离",failed:"隔离失败",unmanaged:"未接管"}[modem.network?.data_guard]||"未上报";detail.textContent=`AT ${modem.at_control?.state||"unknown"} · 语音控制 ${modem.at_control?.call_signalling?"可用":"不可用"} · 网络 ${modem.network?.registration||"unknown"} · 主机数据 ${guard}${modem.network?.data_guard_detail?`（${modem.network.data_guard_detail}）`:""}`;item.append(detail);appendCellularDataControls(item,modem);readers.append(item)}card.append(readers);root.append(card)}}
function empty(text){const node=document.createElement("p");node.className="muted";node.textContent=text;return node}

restoreEUICCDownloads();
initialize();
