import {CallMedia,normalizeDialTarget} from "/assets/call-audio.js";

const state={csrf:"",socket:null,snapshot:null,diagnostics:new Map(),runtime:null,lineCatalog:null,providerConfig:null,diagnosticSnapshot:null,view:"overview",pendingMessage:null,messageSending:false,currentCall:null,providerStatuses:new Map(),cellularStatuses:new Map(),callStatusLoading:false};
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

function openConsole(){loginPanel.classList.add("hidden");consolePanel.classList.remove("hidden");el("logout").classList.remove("hidden");restorePendingMessage();connectState();loadRuntime();loadLineCatalog();loadProviderConfig();loadDiagnostics()}
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
el("refresh-call-status").addEventListener("click",refreshCallStatuses);
el("call-form").addEventListener("submit",beginCall);
el("call-end").addEventListener("click",hangupCall);
el("call-mute").addEventListener("click",toggleCallMute);
el("message-form").addEventListener("submit",sendMessage);
el("message-discard").addEventListener("click",discardPendingMessage);
el("refresh-line-config").addEventListener("click",loadLineCatalog);
el("line-config-line").addEventListener("change",renderLineEditor);
el("line-config-enabled").addEventListener("change",syncLineIdentityRequirements);
el("line-config-form").addEventListener("submit",saveLineConfig);
el("refresh-provider-config").addEventListener("click",loadProviderConfig);
el("apply-provider-config").addEventListener("click",applyProviderConfig);
window.addEventListener("pagehide",()=>state.currentCall?.media?.close());

function selectView(view){state.view=view;for(const button of document.querySelectorAll("[data-view]"))button.classList.toggle("active",button.dataset.view===view);for(const section of document.querySelectorAll(".view"))section.classList.toggle("hidden",section.id!==`view-${view}`);if(view==="settings"){if(!state.runtime)loadRuntime();loadLineCatalog();loadProviderConfig()}if(view==="diagnostics")loadDiagnostics();if(view==="calls")refreshCallStatuses()}

async function loadRuntime(){try{state.runtime=await jsonRequest("/v1/system/runtime");renderRuntime()}catch(error){renderRuntimeError(error)}}
async function loadLineCatalog(){const refresh=el("refresh-line-config"),save=el("save-line-config"),selected=el("line-config-line").value;refresh.disabled=true;save.disabled=true;try{state.lineCatalog=await jsonRequest("/v1/catalog/lines");renderLineSelector(selected);renderLineEditor()}catch(error){state.lineCatalog=null;el("line-config-line").replaceChildren();disableLineEditor(true);showLineConfigResult(`线路配置读取失败：${error.code||error.message}`,true)}finally{refresh.disabled=false}}
async function saveLineConfig(event){event.preventDefault();const catalog=state.lineCatalog,line=currentEditedLine(),button=el("save-line-config"),stored=(catalog?.lines||[]).find(candidate=>candidate.id===line?.id);if(!catalog||!line)return;if(stored&&JSON.stringify(line)===JSON.stringify(catalogLinePayload(stored))){showLineConfigResult(`线路 ${line.name||line.id} 没有变化；catalog revision ${catalog.revision} 未更新。`);return}button.disabled=true;showLineConfigResult(`正在保存 ${line.id} 到 catalog revision ${catalog.revision}；不会应用或操作 Provider…`);try{const result=await jsonRequest(`/v1/catalog/lines/${encodeURIComponent(line.id)}`,{method:"PUT",headers:{"If-Match":`"${catalog.revision}"`},body:JSON.stringify(line)});showLineConfigResult(`已保存 ${result.line?.name||line.id} · catalog revision ${result.revision} · 尚未应用到 Provider`);await loadLineCatalog();await loadProviderConfig()}catch(error){if(error.status===412){showLineConfigResult("保存被拒绝：catalog 已被其他操作更新，已刷新最新配置；你的旧版本没有覆盖新数据。",true);await loadLineCatalog();await loadProviderConfig()}else{showLineConfigResult(`保存失败：${lineConfigError(error)}`,true);button.disabled=false}}}
async function loadProviderConfig(){const refresh=el("refresh-provider-config"),apply=el("apply-provider-config");refresh.disabled=true;try{state.providerConfig=await jsonRequest("/v1/system/provider-config");renderProviderConfig()}catch(error){state.providerConfig=null;el("provider-config-status").replaceChildren(errorCard(`配置应用服务不可用：${error.code||error.message}`));apply.disabled=true}finally{refresh.disabled=false}}
async function applyProviderConfig(){const status=state.providerConfig,button=el("apply-provider-config"),result=el("provider-config-result");if(!status||status.applying)return;button.disabled=true;result.classList.remove("hidden");result.style.color="#344054";result.textContent=`正在应用 catalog revision ${status.catalog_revision}；只处理实际变化的线路…`;try{const applied=await jsonRequest("/v1/system/provider-config",{method:"POST",body:JSON.stringify({schema_version:1,catalog_revision:status.catalog_revision})});result.textContent=`${applied.state} · revision ${applied.catalog_revision} · 新增 ${applied.added} / 变更 ${applied.changed} / 移除 ${applied.removed}`;await loadProviderConfig()}catch(error){result.style.color="#b42318";result.textContent=`应用失败：${[error.code,error.detail].filter(Boolean).join(" · ")||error.message}`;await loadProviderConfig()}finally{button.disabled=false}}
async function loadDiagnostics(){const button=el("refresh-diagnostics");button.disabled=true;try{state.diagnosticSnapshot=await jsonRequest("/v1/diagnostics");renderDiagnostics()}catch(error){el("diagnostic-checks").replaceChildren(errorCard(`诊断采样失败：${error.message}`))}finally{button.disabled=false;renderClientChecks()}}

function renderRuntime(){const root=el("runtime-settings");root.replaceChildren();const info=state.runtime;if(!info)return;root.append(keyValueCard("单一公开入口",[
  ["监听",info.public?.listen],["传输",info.public?.transport],["公开监听器数量",info.public?.listener_count],["复用方式",info.public?.multiplexing],["浏览器状态",info.public?.browser_state_path],["Agent 控制",info.public?.agent_control_path],["VoWiFi 浏览器媒体",info.public?.browser_media_path],["蜂窝浏览器媒体",info.public?.cellular_browser_media_path],["Agent 蜂窝媒体",info.public?.agent_media_path]
]),keyValueCard("TLS 与进程",[["证书 SHA-256",info.public?.tls_fingerprint_sha256],["组件",info.component],["构建版本",info.build_version],["Go",info.go_version],["状态 TTL",`${info.state_ttl_seconds}s`]]),keyValueCard("本机 Provider IPC",[["范围",info.local?.scope],["传输",info.local?.transport],["说明","仅进程间控制，不是部署入口"]]))}
function renderRuntimeError(error){el("runtime-settings").replaceChildren(errorCard(`运行配置读取失败：${error.message}`))}

function renderLineSelector(previous){const select=el("line-config-line"),lines=state.lineCatalog?.lines||[];select.replaceChildren();for(const line of lines){const option=document.createElement("option");option.value=line.id;option.textContent=`${line.name||line.id} · ${line.sim?.msisdn||line.card_id}`;select.append(option)}select.value=lines.some(line=>line.id===previous)?previous:(lines[0]?.id||"");select.disabled=!lines.length;disableLineEditor(!lines.length);if(!lines.length)showLineConfigResult("catalog 中没有可编辑线路。",true)}
function renderLineEditor(){const line=(state.lineCatalog?.lines||[]).find(candidate=>candidate.id===el("line-config-line").value);if(!line){disableLineEditor(true);return}disableLineEditor(false);setLineField("id",line.id);setLineField("name",line.name);setLineField("card-id",line.card_id);el("line-config-enabled").checked=Boolean(line.enabled);setLineField("imsi",line.sim?.imsi);setLineField("mcc",line.sim?.mcc);setLineField("mnc",line.sim?.mnc);setLineField("imei",line.sim?.imei);setLineField("msisdn",line.sim?.msisdn);setLineField("smsc",line.sim?.smsc);setLineField("country",line.network?.egress_country);setLineField("epdg",line.network?.epdg_address);setLineField("pcscf",(line.network?.pcscf||[]).join("\n"));setLineField("impi",line.ims?.impi);setLineField("impu",line.ims?.impu);setLineField("domain",line.ims?.domain);setLineField("aka",line.ims?.aka_app_preference);setLineField("network",line.ims?.network);setLineField("server",line.ims?.server);setLineField("expires",line.ims?.expires||"");syncLineIdentityRequirements()}
function currentEditedLine(){const id=fieldValue("id");if(!id)return null;return{schema_version:1,id,name:fieldValue("name"),enabled:el("line-config-enabled").checked,card_id:fieldValue("card-id"),sim:{imsi:fieldValue("imsi"),mcc:fieldValue("mcc"),mnc:fieldValue("mnc"),imei:fieldValue("imei"),msisdn:fieldValue("msisdn"),smsc:fieldValue("smsc")},network:{epdg_address:fieldValue("epdg"),pcscf:fieldValue("pcscf").split(/\r?\n/).map(value=>value.trim()).filter(Boolean),egress_country:fieldValue("country")},ims:{impi:fieldValue("impi"),impu:fieldValue("impu"),domain:fieldValue("domain"),aka_app_preference:fieldValue("aka"),network:fieldValue("network"),server:fieldValue("server"),expires:Number(fieldValue("expires"))||0}}}
function catalogLinePayload(line){return{schema_version:1,id:line.id,name:line.name||"",enabled:Boolean(line.enabled),card_id:line.card_id||"",sim:{imsi:line.sim?.imsi||"",mcc:line.sim?.mcc||"",mnc:line.sim?.mnc||"",imei:line.sim?.imei||"",msisdn:line.sim?.msisdn||"",smsc:line.sim?.smsc||""},network:{epdg_address:line.network?.epdg_address||"",pcscf:line.network?.pcscf||[],egress_country:line.network?.egress_country||""},ims:{impi:line.ims?.impi||"",impu:line.ims?.impu||"",domain:line.ims?.domain||"",aka_app_preference:line.ims?.aka_app_preference||"",network:line.ims?.network||"",server:line.ims?.server||"",expires:Number(line.ims?.expires)||0}}}
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
    if(line.enabled&&cellularTargetForLine(line)){const cellular=document.createElement("option");cellular.value=callRouteValue("cellular",line.id);cellular.textContent=`${line.name||line.id} · 蜂窝 Modem · ${line.sim?.msisdn||"无号码"}`;select.append(cellular)}
  }
  const locked=state.currentCall?callRouteValue(state.currentCall.mode,state.currentCall.line_id):"";const values=[...select.options].map(option=>option.value);select.value=values.includes(locked||selected)?(locked||selected):values[0];select.disabled=Boolean(state.currentCall);
}

function callRouteValue(mode,lineID){return `${mode}:${lineID}`}
function selectedCallRoute(){const value=el("call-line").value,separator=value.indexOf(":");if(separator<1)return null;return{mode:value.slice(0,separator),line_id:value.slice(separator+1)}}
function cellularTargetForLine(line){const matches=[];for(const agent of state.snapshot?.agents||[]){if(agent.topology?.modem_condition!=="ready")continue;for(const modem of agent.topology?.modems||[]){if(modem.equipment_id===line.sim?.imei&&modem.sim?.iccid===line.card_id&&modem.at_control?.state==="ready"&&modem.at_control?.call_signalling)matches.push({agent,modem})}}return matches.length===1?matches[0]:null}

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

function renderCallStatuses(lines){const root=el("call-statuses");root.replaceChildren();renderIncomingCalls(lines);if(!lines.length){root.append(empty("尚无线路配置"));return}for(const line of lines){const entry=state.providerStatuses.get(line.id),cellular=state.cellularStatuses.get(line.id),card=document.createElement("article");card.className="card";const title=document.createElement("h3");title.textContent=line.name||line.id;card.append(title);const badges=document.createElement("div");badges.className="badges";if(entry?.status){const status=entry.status,runtime=document.createElement("span"),voice=document.createElement("span"),occupied=document.createElement("span"),pending=status.pending_incoming_call;runtime.className=`badge ${status.runtime?.condition==="running"?"good":"warn"}`;runtime.textContent=`VoWiFi ${status.runtime?.condition||"unknown"}`;voice.className=`badge ${status.voice?.available?"good":"bad"}`;voice.textContent=`语音 ${status.voice?.code||status.voice?.condition||"unknown"}`;occupied.className=`badge ${status.active_call||pending?"warn":"neutral"}`;occupied.textContent=status.active_call?`VoWiFi 占用 ${status.active_call.condition}`:pending?`呼入等待 ${pending.caller}`:"VoWiFi 空闲";badges.append(runtime,voice,occupied)}else{const failed=document.createElement("span");failed.className="badge bad";failed.textContent=`VoWiFi 状态不可用 ${entry?.error?.code||entry?.error?.message||"未采样"}`;badges.append(failed)}const modem=document.createElement("span"),target=cellularTargetForLine(line),sessions=cellular?.status?.sessions||[],active=sessions.find(session=>!["ended","expired"].includes(session.phase));modem.className=`badge ${active?"warn":target?"good":"neutral"}`;modem.textContent=active?`蜂窝占用 ${active.phase}`:target?"蜂窝语音就绪":"蜂窝语音不可用";badges.append(modem);if(cellular?.error){const failed=document.createElement("span");failed.className="badge bad";failed.textContent=`蜂窝状态不可用 ${cellular.error.code||cellular.error.message}`;badges.append(failed)}card.append(badges);root.append(card)}}

function renderIncomingCalls(lines){const root=el("incoming-calls");root.replaceChildren();const pending=[];for(const line of lines){const call=state.providerStatuses.get(line.id)?.status?.pending_incoming_call;if(call)pending.push({line,call})}if(!pending.length){root.append(empty("当前没有呼入电话"));return}for(const {line,call} of pending){const card=document.createElement("article");card.className="card";const title=document.createElement("h3");title.textContent=call.caller||"未知来电";const detail=document.createElement("div");detail.className="muted";detail.textContent=`${line.name||line.id} · ${fmtTime(call.received_at)} · ${call.call_id}`;const toolbar=document.createElement("div");toolbar.className="toolbar";const answer=actionButton("接听",()=>beginIncomingCall(line,call));const reject=actionButton("拒接",()=>rejectIncomingCall(line,call));reject.classList.add("danger");answer.disabled=Boolean(state.currentCall);reject.disabled=Boolean(state.currentCall);toolbar.append(answer,reject);card.append(title,detail,toolbar);root.append(card)}}

async function beginIncomingCall(line,pending){if(state.currentCall)return;const bufferMS=Number(el("call-buffer").value)||500;const call={mode:"vowifi",line_id:line.id,callee:pending.caller||"呼入",buffer_ms:bufferMS,call_id:pending.call_id,start_operation_id:callIdentity("ui-incoming-answer"),end_operation_id:"",lease:null,media:null,phase:"preparing",ending:false,muted:false,direction:"incoming"};try{call.media=new CallMedia(bufferMS,(type,detail)=>onCallMediaEvent(call,type,detail));call.media.openAudioFromGesture()}catch(error){showCallResult(error.message,true);return}state.currentCall=call;updateCallControls();showCallResult("正在建立呼入双向音频；成功前不会向运营商确认接听…");try{call.lease=await jsonRequest("/v1/media/leases",{method:"POST",body:JSON.stringify({line_id:call.line_id,call_id:call.call_id})});await call.media.prepare(call.lease,call.call_id);call.phase="ready";await submitCallStart()}catch(error){if(state.currentCall===call&&call.phase!=="start_unknown"&&call.phase!=="active"){showCallResult(`接听失败：${callErrorDetail(error)}`,true);await releaseCurrentCall();await refreshCallStatuses()}}}

async function rejectIncomingCall(line,pending){try{await jsonRequest(`/v1/lines/${encodeURIComponent(line.id)}/vowifi/calls/incoming/reject`,{method:"POST",body:JSON.stringify({operation_id:callIdentity("ui-incoming-reject"),call_id:pending.call_id,reason_code:"user_rejected"})});showCallResult(`已拒接 ${pending.caller||"呼入电话"}`);await refreshCallStatuses()}catch(error){showCallResult(`拒接失败：${callErrorDetail(error)}`,true)}}

function showCallResult(message,isError=false){const result=el("call-result");result.classList.remove("hidden");result.textContent=message;result.style.color=isError?"#b42318":"#344054"}
function updateCallControls(){const call=state.currentCall,retry=call?.phase==="start_unknown"&&call.mode!=="cellular";el("call-line").disabled=Boolean(call)||!el("call-line").value;el("call-number").disabled=Boolean(call);el("call-buffer").disabled=Boolean(call);el("call-start").classList.toggle("hidden",Boolean(call)&&!retry);el("call-start").textContent=retry?"重试同一呼叫请求":"呼叫";el("call-mute").classList.toggle("hidden",call?.phase!=="active");el("call-end").classList.toggle("hidden",!call);el("call-end").disabled=call?.ending===true}
function callIdentity(prefix){return globalThis.crypto?.randomUUID?`${prefix}-${crypto.randomUUID()}`:operationID(prefix)}
function callErrorDetail(error){return[error.kind,error.code,error.layer,error.detail].filter(Boolean).join(" · ")||error.message}
function startResultIsAmbiguous(error){return!error.status||["operation_timeout","provider_transport_failed","invalid_provider_response","cellular_call_start_uncertain"].includes(error.code)}
function endResultIsAmbiguous(error){return!error.status||["operation_timeout","provider_transport_failed","invalid_provider_response"].includes(error.code)}

async function beginCall(event){
  event.preventDefault();if(state.currentCall){if(state.currentCall.phase==="start_unknown"&&state.currentCall.mode!=="cellular")await submitCallStart();return}
  let callee,bufferMS;try{callee=normalizeDialTarget(el("call-number").value);bufferMS=Number(el("call-buffer").value);if(!Number.isInteger(bufferMS)||bufferMS<100||bufferMS>2000)throw new Error("音频排队上限必须是 100–2000 ms 的整数")}
  catch(error){showCallResult(error.message,true);return}
  const route=selectedCallRoute();if(!route){showCallResult("请选择有效线路",true);return}
  const call={mode:route.mode,line_id:route.line_id,callee,buffer_ms:bufferMS,call_id:callIdentity("browser-call"),start_operation_id:callIdentity("ui-call-start"),end_operation_id:"",lease:null,media:null,phase:"preparing",ending:false,muted:false,direction:"outgoing"};
  try{call.media=new CallMedia(bufferMS,(type,detail)=>onCallMediaEvent(call,type,detail));call.media.openAudioFromGesture()}catch(error){showCallResult(error.message,true);return}
  state.currentCall=call;updateCallControls();showCallResult("正在申请麦克风并建立零费用双向音频探测；请说话以确认采集链路…");
  try{call.lease=await jsonRequest(call.mode==="cellular"?"/v1/cellular/media/leases":"/v1/media/leases",{method:"POST",body:JSON.stringify({line_id:call.line_id,call_id:call.call_id})});await call.media.prepare(call.lease,call.call_id);call.phase="ready";showCallResult("双向音频探测通过，正在向运营商提交呼叫…");await submitCallStart()}
  catch(error){if(state.currentCall===call&&call.phase!=="start_unknown"&&call.phase!=="active"){showCallResult(`呼叫前检查失败：${callErrorDetail(error)}。未确认运营商呼叫。`,true);await releaseCurrentCall()}}
}

async function submitCallStart(){const call=state.currentCall;if(!call||!["ready","start_unknown"].includes(call.phase))return;el("call-start").disabled=true;const incoming=call.direction==="incoming",cellular=call.mode==="cellular",path=cellular?`/v1/lines/${encodeURIComponent(call.line_id)}/cellular/calls/start`:`/v1/lines/${encodeURIComponent(call.line_id)}/vowifi/${incoming?"calls/incoming/answer":"calls/start"}`,body=cellular?{operation_id:call.start_operation_id,session_id:call.lease?.session_id,callee:call.callee}:incoming?{operation_id:call.start_operation_id,call_id:call.call_id,media_session_id:call.lease?.session_id,media_buffer_ms:call.buffer_ms}:{operation_id:call.start_operation_id,call_id:call.call_id,media_session_id:call.lease?.session_id,callee:call.callee,media_buffer_ms:call.buffer_ms};try{const result=await jsonRequest(path,{method:"POST",body:JSON.stringify(body)});call.phase="active";call.media.markActive();showCallResult(`通话中 · ${call.callee} · ${result.code||"active"}`);await refreshCallStatuses()}
catch(error){if(startResultIsAmbiguous(error)){call.phase="start_unknown";showCallResult(call.mode==="cellular"?`蜂窝呼叫结果不明确：${callErrorDetail(error)}。不会再次拨号；请挂断或等待 10 秒守卫。`:`呼叫结果不明确：${callErrorDetail(error)}。请重试同一请求或挂断；不会创建第二次呼叫。`,true);updateCallControls()}else{showCallResult(`呼叫失败：${callErrorDetail(error)}`,true);await releaseCurrentCall();await refreshCallStatuses()}}
finally{el("call-start").disabled=false}}

function onCallMediaEvent(call,type,detail){if(state.currentCall!==call)return;if(type==="reconnecting")showCallResult(detail||"媒体链路短暂中断，正在恢复同一通话…",true);else if(type==="reconnected")showCallResult(`通话中 · ${call.callee} · 媒体链路已恢复`);else if(type==="ended"){showCallResult(`通话已结束 · ${detail||"后端已关闭媒体"}`);void releaseCurrentCall().then(refreshCallStatuses)}else if(type==="failed"&&call.phase==="active"){call.phase="media_failed";call.media.close();showCallResult(`媒体链路超过恢复窗口：${detail}。10 秒精确通话守卫将停止该通话。`,true);updateCallControls()}}

function toggleCallMute(){const call=state.currentCall;if(!call||call.phase!=="active")return;call.muted=!call.muted;call.media.setMuted(call.muted);el("call-mute").textContent=call.muted?"取消静音":"静音";showCallResult(`通话中 · ${call.callee}${call.muted?" · 已静音":""}`)}

async function hangupCall(){const call=state.currentCall;if(!call||call.ending)return;call.ending=true;updateCallControls();call.media?.close();if(!call.end_operation_id)call.end_operation_id=callIdentity("ui-call-end");showCallResult("正在发送精确挂断请求；媒体心跳已停止，若请求失败，10 秒守卫仍会继续挂断…");const cellular=call.mode==="cellular",path=cellular?`/v1/lines/${encodeURIComponent(call.line_id)}/cellular/calls/hangup`:`/v1/lines/${encodeURIComponent(call.line_id)}/vowifi/calls/end`,body=cellular?{operation_id:call.end_operation_id,session_id:call.lease?.session_id}:{operation_id:call.end_operation_id,call_id:call.call_id,reason_code:"user_hangup"};try{const result=await jsonRequest(path,{method:"POST",body:JSON.stringify(body)});showCallResult(`通话已由${cellular?" Agent":" Provider"}确认结束 · ${result.code||"ended"}`);await releaseCurrentCall();await refreshCallStatuses();return}
catch(error){if(["call_not_found","cellular_call_not_found"].includes(error.code)){showCallResult("后端已确认没有该通话；线路未被本页继续占用。");await releaseCurrentCall();await refreshCallStatuses();return}if(!endResultIsAmbiguous(error))call.end_operation_id="";showCallResult(`挂断尚未确认：${callErrorDetail(error)}。媒体已断开，服务端精确守卫会继续处理；也可再次点击挂断。`,true)}finally{if(state.currentCall===call){call.ending=false;updateCallControls()}}}

async function releaseCurrentCall(){const call=state.currentCall;if(!call)return;call.media?.close();state.currentCall=null;updateCallControls();if(call.lease?.session_id){try{await jsonRequest(call.mode==="cellular"?"/v1/cellular/media/leases":"/v1/media/leases",{method:"DELETE",body:JSON.stringify({session_id:call.lease.session_id})})}catch{}}}

function renderMessageLines(lines){
  const select=el("message-line"),selected=select.value;select.replaceChildren();
  if(!lines.length){const option=document.createElement("option");option.value="";option.textContent="尚无线路配置";select.append(option);select.disabled=true;return}
  for(const line of lines){const option=document.createElement("option");option.value=line.id;option.textContent=`${line.name||line.id} · ${line.sim?.msisdn||"无号码"}`;select.append(option)}
  select.value=lines.some(line=>line.id===selected)?selected:lines[0].id;select.disabled=Boolean(state.pendingMessage);
}

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
  let restored=false;if(!state.pendingMessage){try{const saved=JSON.parse(sessionStorage.getItem("mdd.pendingMessage")||"null");if(saved&&typeof saved.line_id==="string"&&typeof saved.operation_id==="string"&&typeof saved.message_id==="string"&&typeof saved.recipient==="string"&&typeof saved.body==="string"){state.pendingMessage=saved;restored=true}}catch{}}
  const pending=state.pendingMessage;if(!pending)return setMessageDraftLocked(false);
  el("message-line").value=pending.line_id;el("message-recipient").value=pending.recipient;el("message-body").value=pending.body;setMessageDraftLocked(true);if(restored)showMessageResult("上次发送尚未取得明确成功结果。可重试同一幂等请求，不会生成新的发送身份。",true);
}
function setMessageDraftLocked(locked){el("message-line").disabled=locked||!el("message-line").value;el("message-recipient").disabled=locked;el("message-body").disabled=locked;el("message-discard").classList.toggle("hidden",!locked);el("message-send").textContent=locked?"重试同一请求":"发送"}
function showMessageResult(message,isError=false){const result=el("message-result");result.classList.remove("hidden");result.textContent=message;result.style.color=isError?"#b42318":"#344054"}
async function sendMessage(event){
  event.preventDefault();if(state.messageSending)return;
  if(!state.pendingMessage){const recipient=el("message-recipient").value.trim(),body=el("message-body").value;if(new TextEncoder().encode(recipient).byteLength>128||new TextEncoder().encode(body).byteLength>8192){showMessageResult("收件号码不能超过 128 字节，正文不能超过 8192 字节。",true);return}state.pendingMessage={line_id:el("message-line").value,operation_id:messageIdentity("ui-message-send"),message_id:messageIdentity("message"),recipient,body};savePendingMessage();setMessageDraftLocked(true)}
  const pending=state.pendingMessage;if(!pending.line_id||!pending.recipient||!pending.body){state.pendingMessage=null;savePendingMessage();setMessageDraftLocked(false);showMessageResult("线路、收件号码和正文不能为空。",true);return}
  state.messageSending=true;el("message-send").disabled=true;showMessageResult("发送处理中；在服务器明确确认前将保留同一幂等请求…");
  try{const payload=await jsonRequest(`/v1/lines/${encodeURIComponent(pending.line_id)}/vowifi/messages/send`,{method:"POST",body:JSON.stringify({operation_id:pending.operation_id,message_id:pending.message_id,recipient:pending.recipient,body:pending.body})});state.pendingMessage=null;savePendingMessage();setMessageDraftLocked(false);el("message-body").value="";showMessageResult(`服务器已接受：${payload.code||"sent"} · ${payload.message_id||pending.message_id}`)}
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

function renderAgents(agents){const root=el("agents");root.replaceChildren();if(!agents.length){root.append(empty("当前没有 Agent 连接"));return}for(const agent of agents){const card=document.createElement("article");card.className="card";const title=document.createElement("h3");title.textContent=agent.agent_id;const meta=document.createElement("div");meta.className="muted";meta.textContent=`进程世代 ${agent.process_generation} · 心跳 ${fmtTime(agent.last_seen)} · 拓扑 ${fmtTime(agent.last_report)}`;card.append(title,meta);const readerCondition=document.createElement("span"),modemCondition=document.createElement("span");readerCondition.className=`badge ${agent.topology?.reader_condition==="ready"?"good":"warn"}`;readerCondition.textContent=`读卡器 ${agent.topology?.reader_condition||"未上报"}`;modemCondition.className=`badge ${agent.topology?.modem_condition==="ready"?"good":"neutral"}`;modemCondition.textContent=`Modem ${agent.topology?.modem_condition||"未上报"}`;card.append(readerCondition,modemCondition);const readers=document.createElement("div");readers.className="readers";for(const reader of agent.topology?.readers||[]){const item=document.createElement("div");item.className="reader";const identity=reader.card_id||reader.euicc?.eid||"无卡/身份未就绪";item.textContent=`${reader.reader_name} · ${reader.identity_state} · ${identity}`;if(reader.euicc){const profiles=document.createElement("div");profiles.className="muted";profiles.textContent=reader.euicc.profiles_available?`profiles: ${reader.euicc.profiles.map(profile=>`${profile.iccid} (${profile.state})`).join(", ")||"空白"}`:"profiles 查询不可用";item.append(profiles)}readers.append(item)}for(const modem of agent.topology?.modems||[]){const item=document.createElement("div");item.className="reader";item.textContent=`${modem.model||modem.manufacturer||"蜂窝 Modem"} · ${modem.equipment_id||"无 IMEI"} · ICCID ${modem.sim?.iccid||"未就绪"}`;const detail=document.createElement("div");detail.className="muted";detail.textContent=`AT ${modem.at_control?.state||"unknown"} · 语音控制 ${modem.at_control?.call_signalling?"可用":"不可用"} · 网络 ${modem.network?.registration||"unknown"}`;item.append(detail);readers.append(item)}card.append(readers);root.append(card)}}
function empty(text){const node=document.createElement("p");node.className="muted";node.textContent=text;return node}

initialize();
