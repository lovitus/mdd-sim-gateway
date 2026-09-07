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
