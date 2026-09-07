import assert from 'node:assert/strict'
globalThis.window={location:{pathname:'/'}}
const {networkProfileView,networkProfileWire,networkAPI}=await import('../src/mdd/networkAdapter.js')
const {api:go}=await import('../src/api.js')
const source={type:'cellular_sim',name:'fixture',sim_iccid:'saved-card'}
assert.deepEqual(networkProfileWire(networkProfileView(source)),source)
assert.equal(networkProfileWire({...source,iccid:''}).sim_iccid,'')
const calls=[]
go.egressConfig=async ()=>({revision:7,config:{profiles:{data:source},exits:{}}})
go.testEgressProfile=async (...args)=>{calls.push(args);throw new Error('cellular_connection_disabled')}
const draft=await networkAPI.networkSettings()
assert.equal(draft.proxy.profiles.data.iccid,'saved-card')
draft.proxy.profiles.data.iccid='edited-card'
await assert.rejects(networkAPI.testProxyProfile('data',draft.proxy.profiles.data),/cellular_connection_disabled/)
assert.deepEqual(calls,[['data',7,{type:'cellular_sim',name:'fixture',sim_iccid:'edited-card'}]])
assert.equal(source.sim_iccid,'saved-card')
console.log('Customized MDD data profile mapping and single-request failure contracts passed')
