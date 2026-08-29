"use strict";

const state={csrf:"",socket:null,snapshot:null,diagnostics:new Map(),runtime:null,diagnosticSnapshot:null,view:"overview"};
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
  if(!response.ok)throw new Error(payload.code||payload.detail||`HTTP ${response.status}`);
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

function openConsole(){loginPanel.classList.add("hidden");consolePanel.classList.remove("hidden");el("logout").classList.remove("hidden");connectState();loadRuntime();loadDiagnostics()}
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

function selectView(view){state.view=view;for(const button of document.querySelectorAll("[data-view]"))button.classList.toggle("active",button.dataset.view===view);for(const section of document.querySelectorAll(".view"))section.classList.toggle("hidden",section.id!==`view-${view}`);if(view==="settings"&&!state.runtime)loadRuntime();if(view==="diagnostics")loadDiagnostics()}

async function loadRuntime(){try{state.runtime=await jsonRequest("/v1/system/runtime");renderRuntime()}catch(error){renderRuntimeError(error)}}
async function loadDiagnostics(){const button=el("refresh-diagnostics");button.disabled=true;try{state.diagnosticSnapshot=await jsonRequest("/v1/diagnostics");renderDiagnostics()}catch(error){el("diagnostic-checks").replaceChildren(errorCard(`诊断采样失败：${error.message}`))}finally{button.disabled=false;renderClientChecks()}}

function renderRuntime(){const root=el("runtime-settings");root.replaceChildren();const info=state.runtime;if(!info)return;root.append(keyValueCard("单一公开入口",[
  ["监听",info.public?.listen],["传输",info.public?.transport],["公开监听器数量",info.public?.listener_count],["复用方式",info.public?.multiplexing],["浏览器状态",info.public?.browser_state_path],["Agent 控制",info.public?.agent_control_path],["浏览器媒体",info.public?.browser_media_path]
]),keyValueCard("TLS 与进程",[["证书 SHA-256",info.public?.tls_fingerprint_sha256],["组件",info.component],["构建版本",info.build_version],["Go",info.go_version],["状态 TTL",`${info.state_ttl_seconds}s`]]),keyValueCard("本机 Provider IPC",[["范围",info.local?.scope],["传输",info.local?.transport],["说明","仅进程间控制，不是部署入口"]]))}
function renderRuntimeError(error){el("runtime-settings").replaceChildren(errorCard(`运行配置读取失败：${error.message}`))}

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
  renderLines(catalog,lines);renderAgents(agents);
}

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

function renderAgents(agents){const root=el("agents");root.replaceChildren();if(!agents.length){root.append(empty("当前没有 Agent 连接"));return}for(const agent of agents){const card=document.createElement("article");card.className="card";const title=document.createElement("h3");title.textContent=agent.agent_id;const meta=document.createElement("div");meta.className="muted";meta.textContent=`进程世代 ${agent.process_generation} · 心跳 ${fmtTime(agent.last_seen)} · 拓扑 ${fmtTime(agent.last_report)}`;card.append(title,meta);const condition=document.createElement("span");condition.className=`badge ${agent.topology?.reader_condition==="ready"?"good":"warn"}`;condition.textContent=`读卡器 ${agent.topology?.reader_condition||"未上报"}`;card.append(condition);const readers=document.createElement("div");readers.className="readers";for(const reader of agent.topology?.readers||[]){const item=document.createElement("div");item.className="reader";const identity=reader.card_id||reader.euicc?.eid||"无卡/身份未就绪";item.textContent=`${reader.reader_name} · ${reader.identity_state} · ${identity}`;if(reader.euicc){const profiles=document.createElement("div");profiles.className="muted";profiles.textContent=reader.euicc.profiles_available?`profiles: ${reader.euicc.profiles.map(profile=>`${profile.iccid} (${profile.state})`).join(", ")||"空白"}`:"profiles 查询不可用";item.append(profiles)}readers.append(item)}card.append(readers);root.append(card)}}
function empty(text){const node=document.createElement("p");node.className="muted";node.textContent=text;return node}

initialize();
