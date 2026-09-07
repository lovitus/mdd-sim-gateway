import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
globalThis.window={location:{pathname:'/'}}
const {networkProfileView,networkProfileWire,networkAPI}=await import('../src/mdd/networkAdapter.js')
const {api:go}=await import('../src/api.js')
const readEgressConfig=go.egressConfig
const originalFetch=globalThis.fetch
try {
  for (const code of ['cellular_connection_disabled','egress_apply_revision_changed']) {
    globalThis.fetch=async ()=>new Response(JSON.stringify({code,layer:'country_egress'}),{status:409,statusText:'Conflict'})
    await assert.rejects(readEgressConfig(),error=>error.message===code && error.code===code && error.status===409 && error.layer==='country_egress')
  }
  globalThis.fetch=async ()=>new Response(JSON.stringify({code:'egress_status_unavailable',detail:'runtime status read failed'}),{status:503,statusText:'Service Unavailable'})
  await assert.rejects(readEgressConfig(),error=>error.message==='runtime status read failed' && error.code==='egress_status_unavailable')
} finally {globalThis.fetch=originalFetch}
const source={type:'cellular_sim',name:'fixture',sim_iccid:'saved-card'}
assert.deepEqual(networkProfileWire(networkProfileView(source)),source)
assert.equal(networkProfileWire({...source,sim_iccid:''}).sim_iccid,'')
const calls=[]
go.egressConfig=async ()=>({revision:7,config:{profiles:{data:source},exits:{}}})
go.testEgressProfile=async (...args)=>{calls.push(args);throw new Error('cellular_connection_disabled')}
const draft=await networkAPI.networkSettings()
assert.equal(draft.proxy.profiles.data.sim_iccid,'saved-card')
draft.proxy.profiles.data.sim_iccid='edited-card'
await assert.rejects(networkAPI.testProxyProfile('data',draft.proxy.profiles.data),/cellular_connection_disabled/)
assert.deepEqual(calls,[['data',7,{type:'cellular_sim',name:'fixture',sim_iccid:'edited-card'}]])
assert.equal(source.sim_iccid,'saved-card')
let savedWire
go.saveEgressConfig=async (config,revision)=>{savedWire={config,revision};return {config,revision:8}}
await networkAPI.saveNetworkSettings(draft)
assert.equal(savedWire.config.profiles.data.sim_iccid,'edited-card')
assert.equal(savedWire.revision,7)
const applied=[]
go.applyEgress=async revision=>{applied.push(revision);throw new Error('apply_unconfirmed')}
await assert.rejects(networkAPI.refreshEgress(8),/apply_unconfirmed/)
assert.deepEqual(applied,[8])
await assert.rejects(networkAPI.refreshEgress(0),/egress_revision_missing/)
assert.deepEqual(applied,[8])
go.applyEgress=async ()=>({config_revision:8,state:'pending',code:'runtime_confirmed'})
await assert.rejects(networkAPI.refreshEgress(8),/egress_runtime_unconfirmed/)
go.applyEgress=async ()=>({config_revision:9,state:'applied',code:'runtime_confirmed'})
await assert.rejects(networkAPI.refreshEgress(8),/egress_runtime_unconfirmed/)
go.applyEgress=async ()=>({config_revision:8,state:'unchanged',code:'runtime_confirmed'})
assert.equal((await networkAPI.refreshEgress(8)).state,'unchanged')
assert.equal(Object.hasOwn(savedWire.config.profiles.data,'iccid'),false)
const page=readFileSync(new URL('../src/mdd/views/UnifiedPages.jsx',import.meta.url),'utf8')
assert.ok(page.includes('value={profile.sim_iccid'))
assert.ok(page.includes('Saved SIM (not in current inventory)'))
assert.ok(page.includes('setSimError(error.message)'))
assert.equal(page.includes('setInterval(loadLive, 5000)'),false)
assert.equal(page.includes('loadLive('),false)
assert.ok(page.includes('api.refreshEgress(saved.__revision)'))
assert.ok(page.includes("let stage = 'save'"))
assert.ok(page.includes('Configuration saved; application failed or is unconfirmed'))
assert.ok(page.includes('if (networkOperationBusy.current) return'))
assert.ok(page.includes('<fieldset disabled={saving}'))
assert.ok(page.includes("['existing', t('Imported outbound')"))
assert.ok(page.includes("outbound_tag: (profileDraft.outbound_tag || '').trim()"))
const apiSource=readFileSync(new URL('../src/api.js',import.meta.url),'utf8')
assert.ok(apiSource.includes("if (device.device_type !== 'modem') continue"))
console.log('Customized MDD data profile mapping and single-request failure contracts passed')
