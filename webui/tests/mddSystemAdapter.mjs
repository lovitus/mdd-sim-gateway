import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
globalThis.window = {location:{pathname:'/'}}
const {maintenanceLeases, maintenanceRequest, systemAPI, hostView, systemSettingsView,agentCredentialChange} = await import('../src/mdd/systemAdapter.js')
const {api:go} = await import('../src/api.js')
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
const preferenceWrites=[]
go.saveSystemPreferences=async (revision,patch)=>{preferenceWrites.push({revision,patch});return {revision:4,preferences:patch}}
const audioSaved=await systemAPI.saveSettings({...settings,cellular_audio_buffer_ms:700},'voice')
assert.deepEqual(preferenceWrites,[{revision:3,patch:{call_audio_buffer_ms:700}}])
assert.equal(audioSaved.__preference_revision,4)
let notificationPatch
go.saveNotificationConfig=async patch=>{notificationPatch=patch;return {...notificationConfig,revision:9,timezone:patch.timezone}}
const timezoneSaved=await systemAPI.saveSettings({...settings,timezone:'Asia/Shanghai'},'general')
assert.equal(notificationPatch.expected_revision,8)
assert.equal(notificationPatch.telegram.enabled,true)
assert.equal(Object.hasOwn(notificationPatch.telegram,'bot_token'),false)
assert.equal(Object.hasOwn(notificationPatch.webhook,'headers_json'),false)
assert.equal(timezoneSaved.__notifications.revision,9)
await assert.rejects(systemAPI.saveSettings(settings,'web'),/system_setting_not_writable/)
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
console.log('Customized MDD maintenance lease and registration identity adapter contracts passed')
