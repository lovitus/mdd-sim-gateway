import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
globalThis.window = {location:{pathname:'/'}}
const {maintenanceLeases, maintenanceRequest, systemAPI, hostView, systemSettingsView,agentCredentialChange} = await import('../src/mdd/systemAdapter.js')
const {api:go} = await import('../src/api.js')
go.hostModemSettings=async()=>{throw Object.assign(new Error('unavailable'),{status:404})}
const hostViewSettings=systemSettingsView({}, {}, {}, {}, {}, {revision:'a'.repeat(64),settings:{modem_backend:'auto',modem_profiles:[{vid:'2c7c',pid:'0125',at_interface:2}]},runtime_state:'not_observed'})
assert.equal(hostViewSettings.__hardware_supported,true)
const oldHostSave=go.saveHostModemSettings
let hostRequest
go.saveHostModemSettings=async input=>{hostRequest=input;return {revision:'b'.repeat(64),settings:input.settings,runtime_state:'not_observed'}}
const hostSaved=await systemAPI.saveSettings({...hostViewSettings,hardware:{...hostViewSettings.hardware,modem_backend:'serial'}},'hardware')
assert.deepEqual(hostRequest.settings.modem_profiles,[{vid:'2c7c',pid:'0125',at_interface:2}])
assert.equal(hostRequest.expected_revision,'a'.repeat(64))
assert.equal(hostSaved.__hardware_runtime,'not_observed')
assert.equal(hostSaved.__hardware_revision,'b'.repeat(64))
go.saveHostModemSettings=oldHostSave
const defaultView=systemSettingsView({revision:3,new_device_defaults_supported:true,preferences:{new_device_defaults:{connection_enabled:false,vowifi_enabled:false,flight_mode:true,roaming_enabled:true}}})
assert.equal(defaultView.__device_defaults_supported,true)
assert.deepEqual(defaultView.device_defaults,{cellular_enabled:false,vowifi_enabled:false,flight_mode:true,roaming_enabled:true})
assert.equal(systemSettingsView().__device_defaults_supported,false)
const originalPreferenceSave=go.saveSystemPreferences
let defaultPatch
go.saveSystemPreferences=async(revision,patch)=>{defaultPatch={revision,patch};return {revision:4,preferences:patch}}
const defaultSaved=await systemAPI.saveSettings(defaultView,'device-defaults')
assert.deepEqual(defaultPatch,{revision:3,patch:{new_device_defaults:{connection_enabled:false,vowifi_enabled:false,flight_mode:true,roaming_enabled:true}}})
assert.equal(defaultSaved.__preference_revision,4)
await assert.rejects(systemAPI.saveSettings({...defaultView,__device_defaults_supported:false},'device-defaults'),/device_defaults_unavailable/)
go.saveSystemPreferences=originalPreferenceSave
go.webSettings=async()=>{throw Object.assign(new Error('unsupported'),{status:404})}
const snapshot = {catalog_revision:7,lines:[
  {line_id:'a',provider_present:true,maintenance:{draining:true,lease_id:'lease-a'}},
  {line_id:'b',provider_present:true,maintenance:{draining:true,lease_id:'lease-b'}},
  {line_id:'c',provider_present:true,maintenance:{draining:false}},
  {line_id:'d',provider_present:false,maintenance:{draining:false}},
]}
assert.deepEqual(maintenanceLeases(snapshot),[
  {lease_id:'lease-a',line_ids:['a']},{lease_id:'lease-b',line_ids:['b']},
])
assert.deepEqual(maintenanceRequest(snapshot,'resume','lease-a'),{
  schema_version:1,catalog_revision:7,lease_id:'lease-a',line_ids:['a'],
})
assert.deepEqual(maintenanceRequest(snapshot,'begin','lease-new').line_ids,['c'])
assert.throws(() => maintenanceRequest(snapshot,'resume','unknown'), /maintenance_lines_unavailable/)
assert.throws(() => maintenanceRequest(snapshot,'restart_lines','lease-a'), /maintenance_identity_required/)
const calls = []
go.catalogLines = async () => ({lines:[{id:'a',card_id:'fixture-card'}]})
go.registerV1 = async (...args) => { calls.push(args); return {code:'fixture-result'} }
assert.equal((await systemAPI.register('a')).code,'fixture-result')
assert.deepEqual(calls,[['a','fixture-card']])
await assert.rejects(systemAPI.register('b'), /line_card_identity_unavailable/)
assert.equal(calls.length,1)
const host = hostView({memory:{state:'available',value:{total_bytes:1048576,used_percent:0}},
  cpu:{state:'unavailable',value:{mhz:9999}},network:{state:'available',value:{interfaces:[{name:'eth0',addresses:['fixture-address']}]}}})
assert.equal(host.memory.total_mb,1)
assert.equal(host.memory.used_percent,0)
assert.equal(host.cpu_mhz,undefined)
assert.deepEqual(host.network.addresses,[{interface:'eth0',address:'fixture-address'}])
assert.equal(hostView({}).disk.free_mb,undefined)
assert.equal(systemAPI.supportBundleUrl,'/v1/diagnostics/support-bundle')
const acknowledgements = []
go.acknowledgeHostAlert = async alert => acknowledgements.push({key:alert.key,occurrence:alert.occurrence})
go.systemStatus = async () => ({})
go.hostAlerts = async () => ({alerts:[{key:'new-alert',occurrence:2,acknowledged:false}]})
const refreshed = await systemAPI.clearHostAlerts([{key:'seen-alert',occurrence:1}])
assert.deepEqual(acknowledgements,[{key:'seen-alert',occurrence:1}])
assert.equal(refreshed.host_alerts[0].key,'new-alert')
await assert.rejects(systemAPI.clearHostAlerts([{code:'unscoped'}]), /host_alert_identity_required/)
const notificationConfig={revision:8,timezone:'UTC',supported_events:['incoming_sms'],
  telegram:{enabled:true,events:{incoming_sms:true},proxy_mode:'direct',bot_token:{configured:true},chat_id:{configured:true}},
  webhook:{enabled:false,events:{},headers:{configured:true},url:{configured:false},payload_template:{configured:false}},
  pushplus:{enabled:false,events:{},token:{configured:false},topic:{configured:false}}}
const settings=systemSettingsView({revision:3,preferences:{call_audio_buffer_ms:500}},notificationConfig,{public:{listen:'127.0.0.1:8443'}})
assert.equal(settings.bind,'127.0.0.1')
assert.equal(settings.http_port,8443)
assert.equal(systemSettingsView({}, {}, {public:{listen:'[::1]:9443'}}).http_port,9443)
assert.equal(systemSettingsView({}, {}, {public:{listen:':8443'}}).http_port,8443)
assert.equal(systemSettingsView().http_port,undefined)
const certView=systemSettingsView({}, {}, {public:{certificate:{self_signed:false,dns_names:['gateway.example'],not_after:'2027-01-01T00:00:00Z'}}})
assert.equal(certView.tls.self_signed,false)
assert.equal(certView.tls.domain,'gateway.example')
assert.equal(systemSettingsView().tls.self_signed,undefined)
assert.equal(systemSettingsView().__voice_supported,false)
const preferenceWrites=[]
go.saveSystemPreferences=async (revision,patch)=>{preferenceWrites.push({revision,patch});return {revision:4,preferences:patch}}
const audioSaved=await systemAPI.saveSettings({...settings,cellular_audio_buffer_ms:700},'voice')
assert.deepEqual(preferenceWrites,[{revision:3,patch:{call_audio_buffer_ms:700}}])
assert.equal(audioSaved.__preference_revision,4)
const auditSettings=systemSettingsView({revision:4,preferences:{call_audio_buffer_ms:700,ring_timeout_seconds:35,audit_enabled:true,trusted_proxies:[]}},notificationConfig,{})
assert.equal(auditSettings.__security_supported,true)
const auditSaved=await systemAPI.saveSettings({...auditSettings,security:{audit_enabled:false,trusted_proxies:['127.0.0.1/32']}},'security')
assert.deepEqual(preferenceWrites.at(-1),{revision:4,patch:{audit_enabled:false,trusted_proxies:['127.0.0.1/32']}})
assert.equal(auditSaved.security.audit_enabled,false)
assert.equal(auditSaved.cellular_audio_buffer_ms,700)
await assert.rejects(systemAPI.saveSettings(settings,'security'),/audit_settings_unavailable/)
const updateSettings=systemSettingsView({revision:4,preferences:{call_audio_buffer_ms:700,ring_timeout_seconds:35,updates:{proxy_mode:'direct'}}},notificationConfig,{})
updateSettings.proxy={profiles:{'proxy-a':{name:'Proxy A',type:'socks5'}}}
const routeSaved=await systemAPI.saveSettings({...updateSettings,updates:{proxy_mode:'library',proxy_profile_id:'proxy-a'}},'backup')
assert.deepEqual(preferenceWrites.at(-1),{revision:4,patch:{updates:{proxy_mode:'library',proxy_profile_id:'proxy-a'}}})
assert.equal(routeSaved.__saved_updates.proxy_profile_id,'proxy-a')
assert.equal(routeSaved.cellular_audio_buffer_ms,700)
await assert.rejects(systemAPI.saveSettings({...updateSettings,updates:{proxy_mode:'library',proxy_profile_id:'missing'}},'backup'),/selected_update_proxy_unavailable/)
let notificationPatch
go.saveNotificationConfig=async patch=>{notificationPatch=patch;return {...notificationConfig,revision:9,timezone:patch.timezone}}
const timezoneSaved=await systemAPI.saveSettings({...settings,timezone:'Asia/Shanghai'},'general')
assert.equal(notificationPatch.expected_revision,8)
assert.equal(notificationPatch.telegram.enabled,true)
assert.equal(Object.hasOwn(notificationPatch.telegram,'bot_token'),false)
assert.equal(Object.hasOwn(notificationPatch.webhook,'headers_json'),false)
assert.equal(timezoneSaved.__notifications.revision,9)
await assert.rejects(systemAPI.saveSettings(settings,'web'),/web_settings_unavailable/)
const web=systemSettingsView({}, {}, {public:{listen:'127.0.0.1:8443'}}, {}, {schema_version:1,revision:'a'.repeat(64),settings:{listen:'[::1]:9443',tls_cert:'/cert',tls_key:'/key'},restart_required:true})
assert.equal(web.http_port,9443)
assert.equal(web.__web_supported,true)
assert.equal(web.tls.cert_path,'/cert')
let webWrite
go.saveWebSettings=async input=>{webWrite=input;return {revision:'b'.repeat(64),restart_required:true}}
const webSaved=await systemAPI.saveSettings({...web,bind:'::1'},'web')
assert.deepEqual(webWrite,{schema_version:1,expected_revision:'a'.repeat(64),settings:{listen:'[::1]:9443',tls_cert:'/cert',tls_key:'/key'}})
assert.equal(webSaved.__web_revision,'b'.repeat(64))
assert.equal(webSaved.__web_restart_required,true)
await assert.rejects(systemAPI.saveSettings({...web,http_port:0},'web'),/invalid_web_settings/)
await assert.rejects(systemAPI.saveSettings({...settings,cellular_audio_buffer_ms:99},'voice'),/invalid_call_audio_buffer_ms/)
const app=readFileSync(new URL('../src/mdd/App.jsx',import.meta.url),'utf8')
assert.equal(app.includes('api.authSetup'),false)
assert.equal(app.includes('autoComplete="current-password" minLength="1"'),true)
assert.deepEqual(agentCredentialChange('set_mode','','transition').payload,{action:'set_mode',mode:'scoped'})
assert.deepEqual(agentCredentialChange('unenroll','agent-a','transition').payload,{action:'unenroll',agent_id:'agent-a'})
assert.throws(() => agentCredentialChange('unenroll','agent-a','scoped'),/agent_fallback_requires_transition_mode/)
assert.match(agentCredentialChange('set_mode','','scoped').confirmation,/Unknown Agent IDs/)
assert.match(agentCredentialChange('revoke','agent-a','scoped').confirmation,/disconnect/)
const timedSettings=systemSettingsView({revision:4,preferences:{call_audio_buffer_ms:700,ring_timeout_seconds:35}},notificationConfig,{})
assert.equal(timedSettings.__retry_supported,false)
const retrySettings=systemSettingsView({revision:4,preferences:{call_audio_buffer_ms:700,ring_timeout_seconds:35,retry:{max:3,interval:40}}},notificationConfig,{})
assert.equal(retrySettings.__retry_supported,true)
await systemAPI.saveSettings({...retrySettings,retry:{max:4,interval:30}},'voice')
assert.deepEqual(preferenceWrites.at(-1),{revision:4,patch:{call_audio_buffer_ms:700,retry:{max:4,interval:30},ring_timeout_seconds:35}})
await assert.rejects(systemAPI.saveSettings({...retrySettings,retry:{max:0,interval:40}},'voice'),/invalid_retry_window/)
assert.equal(timedSettings.__ring_timeout_supported,true)
const timedSaved=await systemAPI.saveSettings({...timedSettings,ring_timeout:60},'voice')
assert.deepEqual(preferenceWrites.at(-1),{revision:4,patch:{call_audio_buffer_ms:700,ring_timeout_seconds:60}})
assert.equal(timedSaved.ring_timeout,60)
await assert.rejects(systemAPI.saveSettings({...timedSettings,ring_timeout:4},'voice'),/invalid_ring_timeout_seconds/)
const rekeySettings=systemSettingsView({revision:4,preferences:{call_audio_buffer_ms:700,ring_timeout_seconds:35}},notificationConfig,{}, {revision:9,defaults:{rekey_minutes:0}})
let rekeyWrite
go.saveProviderDefaults=async (defaults,revision)=>{rekeyWrite={defaults,revision};return {defaults,revision:10}}
const rekeySaved=await systemAPI.saveRekeySettings({...rekeySettings,rekey:{minutes:30}})
assert.deepEqual(rekeyWrite,{defaults:{rekey_minutes:30},revision:9})
assert.equal(rekeySaved.__saved_rekey_minutes,30)
assert.equal(rekeySaved.__catalog_revision,10)
assert.equal(rekeySaved.__preference_revision,4)
await assert.rejects(systemAPI.saveRekeySettings({...rekeySettings,rekey:{minutes:1441}}),/invalid_rekey_default/)
go.systemPreferences=async()=>({revision:4,preferences:{call_audio_buffer_ms:700,ring_timeout_seconds:35}})
go.notificationConfig=async()=>notificationConfig
go.systemRuntime=async()=>({public:{listen:'127.0.0.1:8443'}})
go.egressConfig=async()=>({revision:1,config:{profiles:{}}})
go.catalogLines=async()=>({revision:9,defaults:{rekey_minutes:0}})
go.systemStatus=async()=>{throw new Error('host sampling must not block settings')}
const complete=await systemAPI.settings()
assert.equal(complete.__general_supported,true)
assert.equal(complete.__voice_supported,true)
assert.equal(complete.__rekey_supported,true)
assert.deepEqual(complete.__load_errors,[])
go.notificationConfig=async()=>{throw Object.assign(new Error('request failed'),{code:'notification_config_unavailable',status:503})}
const notificationUnavailable=await systemAPI.settings()
assert.equal(notificationUnavailable.cellular_audio_buffer_ms,700)
assert.equal(notificationUnavailable.__rekey_supported,true)
assert.equal(notificationUnavailable.timezone,undefined)
assert.equal(notificationUnavailable.__general_supported,false)
assert.deepEqual(notificationUnavailable.__load_errors,[{source:'Notification settings',code:'notification_config_unavailable'}])
await assert.rejects(systemAPI.saveSettings({...notificationUnavailable,timezone:'UTC'},'general'),/notification_settings_unavailable/)
go.notificationConfig=async()=>notificationConfig
go.systemPreferences=async()=>{throw new Error('preferences unavailable')}
go.catalogLines=async()=>{throw new Error('catalog unavailable')}
const audioUnavailable=await systemAPI.settings()
assert.equal(audioUnavailable.timezone,'UTC')
assert.equal(audioUnavailable.__voice_supported,false)
assert.equal(audioUnavailable.cellular_audio_buffer_ms,undefined)
assert.equal(audioUnavailable.__rekey_supported,false)
const writesBefore=preferenceWrites.length
await assert.rejects(systemAPI.saveSettings({...audioUnavailable,cellular_audio_buffer_ms:500},'voice'),/audio_settings_unavailable/)
assert.equal(preferenceWrites.length,writesBefore)
const unauthorized=Object.assign(new Error('authentication expired'),{status:401})
go.systemRuntime=async()=>{throw unauthorized}
await assert.rejects(systemAPI.settings(),error=>error===unauthorized)
const originalFetch = globalThis.fetch
const allowanceWrites = []
try {
  globalThis.fetch = async (url, options) => {
    allowanceWrites.push({url,method:options.method,revision:options.headers['If-Match'],body:JSON.parse(options.body)})
    return new Response(JSON.stringify({snapshot:{revision:8,values:{balance:'10'}},rule:{revision:8,recipient:'100',body:'BAL',parser:'none'}}),{status:200})
  }
  await go.saveAllowance('line-a',{revision:7,balance:'10'})
  await go.saveAllowanceQueryRule('line-a',{revision:6,recipient:'100',body:'BAL',parser:'none'})
  await go.resetAllowanceQueryRule('line-a',5)
  assert.deepEqual(allowanceWrites.map(value=>[value.method,value.revision]),[['PUT','"7"'],['PUT','"6"'],['DELETE','"5"']])
  assert.equal(allowanceWrites[1].body.parser,'none')
  await assert.rejects(go.saveAllowance('line-a',{balance:'10'}),/allowance_revision_missing/)
  await assert.rejects(go.saveAllowanceQueryRule('line-a',{recipient:'100',body:'BAL'}),/allowance_rule_revision_missing/)
  assert.equal(allowanceWrites.length,3)
  globalThis.fetch = async () => new Response(JSON.stringify({code:'allowance_revision_changed'}),{status:412})
  await assert.rejects(go.saveAllowance('line-a',{revision:7,balance:'10'}),error=>error.status===412 && error.code==='allowance_revision_changed')
  const provider={line_id:'line-a',process_generation:'process-a',runtime:{condition:'failed',ike:{requests_sent:2,response_datagrams:1,response_timeouts:1}}}
  const reads=[]
  globalThis.fetch=async url=>{
    reads.push(url)
    return new Response(JSON.stringify(url.endsWith('/vowifi/status') ? provider : url.endsWith('/recovery') ? {line_id:'line-a',revision:1,failures:0} : {facts:[],operations:{}}),{status:200})
  }
  const facts=await go.lineFacts('line-a')
  assert.deepEqual(reads,['/v1/lines/line-a','/v1/lines/line-a/vowifi/status','/v1/lines/line-a/recovery'])
  assert.equal(facts.recovery.failures,0)
  assert.equal(facts.provider.runtime.ike.response_timeouts,1)
  assert.equal(facts.generation.engine_run_id,'process-a')
  assert.equal(facts.summary.state,'unknown')
  provider.line_id='other-line'
  const wrong=await go.lineFacts('line-a')
  assert.equal(wrong.provider,null)
  assert.equal(wrong.provider_error,'provider_identity_mismatch')
  globalThis.fetch=async url=>new Response(JSON.stringify(url.endsWith('/vowifi/status') ? {code:'provider_unavailable'} : {facts:[],operations:{}}),{status:url.endsWith('/vowifi/status')?412:200})
  const absent=await go.lineFacts('line-a')
  assert.equal(absent.provider,null)
  assert.equal(absent.provider_error,'provider_unavailable')
  assert.equal(absent.generation,undefined)
} finally {globalThis.fetch=originalFetch}
console.log('Customized MDD maintenance lease, allowance revision and registration identity adapter contracts passed')
