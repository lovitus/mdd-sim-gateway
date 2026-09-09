import assert from 'node:assert/strict'
globalThis.window={location:{pathname:'/'}}
const {saveReaderIMEI}=await import('../src/mdd/hardwareAdapter.js')
const {api:go}=await import('../src/api.js')
assert.throws(() => go.hideOfflineDevice({id:'device',present:null,observed_only:true,observation_version:'version'}),/device_not_proven_offline/)
assert.throws(() => go.hideOfflineDevice({id:'device',present:true,observed_only:false,observation_version:'version'}),/device_not_proven_offline/)
const device={device_type:'reader',instance_id:'line-a',present:true,sim:{iccid:'fixture-card'}}
const input={id:'entry',imei:'123456789012347',name:'fixture'}
const snapshot={revision:3,catalog_revision:7,pool:[input]}
const calls=[]
go.bindIMEIExpected=async (...args)=>{calls.push(args);return {changed:true}}
assert.equal((await saveReaderIMEI(device,input,snapshot)).changed,true)
assert.deepEqual(calls,[['entry','line-a','fixture-card',3,7]])
await assert.rejects(saveReaderIMEI({...device,device_type:'modem'},input,snapshot),/reader_imei_binding_identity_required/)
assert.equal(calls.length,1)
go.saveIMEIEntryExpected=async (entry,revision)=>{assert.equal(revision,3);return {entry,revision:4}}
go.bindIMEIExpected=async ()=>{throw new Error('catalog changed')}
await assert.rejects(saveReaderIMEI(device,{...input,name:'renamed'},snapshot),/IMEI pool entry saved; SIM binding failed: catalog changed/)
console.log('Customized MDD reader IMEI scope and partial-save reporting contracts passed')

const poolRequests=[]
globalThis.fetch=async (url,options={})=>{
  poolRequests.push({url,options})
  if(options.method==='GET'){
    assert.equal(url,'/v1/catalog/lines')
    return new Response(JSON.stringify({revision:99,lines:[{id:'line-a',card_id:'fixture-card'}]}),{status:200})
  }
  assert.equal(options.headers['If-Match'],'"3"')
  return new Response(JSON.stringify({code:'revision_conflict'}),{status:409})
}
const observed={...snapshot,bindings:{'fixture-card':{imei_id:'entry',line_id:'line-a'}}}
for(const mutate of [
  ()=>go.saveImeiPoolEntry(input,observed),
  ()=>go.deleteImeiPoolEntry('entry',observed),
  ()=>go.bindImeiToIccid({iccid:'fixture-card',imei_id:'entry'},observed),
  ()=>go.unbindImeiFromIccid('fixture-card',observed),
])await assert.rejects(mutate,error=>error.status===409)
assert.equal(poolRequests.filter(x=>x.options.method!=='GET').length,4)
for(const request of poolRequests.filter(x=>x.url.includes('/bindings/'))){
  assert.equal(JSON.parse(request.options.body).expected_catalog_revision,7)
  assert.equal(JSON.parse(request.options.body).expected_card_id,'fixture-card')
}
const count=poolRequests.length
await assert.rejects(go.saveImeiPoolEntry(input),/imei_pool_revision_missing/)
assert.equal(poolRequests.length,count)
