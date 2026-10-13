import assert from 'node:assert/strict'
globalThis.window={location:{pathname:'/'}}
globalThis.sessionStorage={getItem:()=>null}
const {api}=await import('../src/api.js')
const {snapshotObservation,snapshotIsFresh}=await import('../src/mdd/snapshotFreshness.js')
const {simHomeCountry}=await import('../src/simCountry.js')
const {editedCatalogLine,lineForm}=await import('../src/mdd/lineAdapter.js')

const requests=[]
globalThis.fetch=async(path,options)=>{
  requests.push({path,...options,body:JSON.parse(options.body)})
  return new Response(JSON.stringify({state:'unknown',operation_id:JSON.parse(options.body).operation_id}),{status:202})
}
const confirmed={id:'modem',instance_id:'line-confirmed',sim:{iccid:'8944100000000002420'},policy_revision:3,
  go_device:{agent_id:'agent-confirmed',process_generation:'process-confirmed',modem:{equipment_id:'862547055201716',
    attachment_id:'attachment-confirmed',sim:{iccid:'8944100000000002420',session_generation:'session-confirmed'}}}}
await api.patchDeviceCapabilities(confirmed.id,{vowifi_enabled:false},{...confirmed,present:false,stale:true})
assert.equal(requests.length,1,'offline stop must not retarget through device GET')
assert.equal(requests[0].path,'/v1/lines/line-confirmed/vowifi/runtime/stop')
assert.equal(requests[0].body.expected_card_id,confirmed.sim.iccid)
await api.patchDeviceCapabilities(confirmed.id,{connection_enabled:true},confirmed)
assert.equal(requests[1].headers['If-Match'],'"3"')
assert.deepEqual(requests[1].body.expected_target,{agent_id:'agent-confirmed',process_generation:'process-confirmed',
  equipment_id:'862547055201716',attachment_id:'attachment-confirmed',card_id:'8944100000000002420',sim_session_generation:'session-confirmed'})
await api.saveDeviceCellularProfile(confirmed.id,{name:'MDD',apn:'internet'},confirmed,2)
assert.equal(requests[2].headers['If-Match'],'"2"')
assert.deepEqual(requests[2].body.expected_target,requests[1].body.expected_target)
const outcome=await api.softRestartDevice(confirmed.id,confirmed,'original-recovery')
assert.equal(outcome.operation_id,'original-recovery')
assert.equal(outcome.state,'unknown')
await assert.rejects(api.patchDeviceCapabilities('modem',{connection_enabled:true},{...confirmed,go_device:{}}),/identity_required/)
assert.equal(requests.length,4)

assert.equal(simHomeCountry('234'),'gb')
assert.equal(simHomeCountry('1'),simHomeCountry('001'))
assert.equal(simHomeCountry('999'),'')
const line={schema_version:1,id:'line',card_id:'card',sim:{mcc:'234'},network:{egress_country:'fr'}}
assert.equal(editedCatalogLine({...lineForm(line,3),proxy_country:''}).network.egress_country,'gb')
assert.equal(editedCatalogLine({...lineForm(line,3),mcc:'999',proxy_country:''}).network.egress_country,'fr')
const now=Date.parse('2026-10-13T00:00:00Z')
const fresh=snapshotObservation({at:new Date(now).toISOString(),devices_available:true,devices:[]},now)
assert.equal(snapshotIsFresh(fresh,now+30000),true)
assert.equal(snapshotIsFresh(fresh,now+76000),false)
assert.equal(snapshotObservation({devices_available:false,devices:[]},now),null)
assert.equal(snapshotObservation({devices_available:true,devices:[]},now),null)
assert.equal(snapshotIsFresh(snapshotObservation({at:new Date(now-80000).toISOString(),devices_available:true,devices:[]},now),now),false,'receipt cannot wash old server observation')
assert.equal(snapshotIsFresh(snapshotObservation({at:new Date(now+6000).toISOString(),devices_available:true,devices:[]},now),now),false)
console.log('PASS frozen control/API identity, original unknown operation, concrete country and snapshot freshness')
