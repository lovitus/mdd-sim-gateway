import assert from 'node:assert/strict'
globalThis.window={location:{pathname:'/'}}
const {saveReaderIMEI}=await import('../src/mdd/hardwareAdapter.js')
const {api:go}=await import('../src/api.js')
assert.throws(() => go.hideOfflineDevice({id:'device',present:null,observed_only:true,observation_version:'version'}),/device_not_proven_offline/)
assert.throws(() => go.hideOfflineDevice({id:'device',present:true,observed_only:false,observation_version:'version'}),/device_not_proven_offline/)
const cardID='8944100000000002420'
const device={device_type:'reader',present:true,sim:{iccid:cardID}}
const input={id:'entry',imei:'123456789012347',name:'fixture'}
const snapshot={revision:3,catalog_revision:7,pool:[input]}
const calls=[]
globalThis.fetch=async (url,options)=>{
  calls.push({url,options})
  assert.equal(url,`/v1/imei-pool/entry/cards/${cardID}`)
  assert.equal(options.method,'PUT')
  assert.equal(options.headers['If-Match'],'"3"')
  assert.deepEqual(JSON.parse(options.body),{expected_catalog_revision:7,expected_card_id:cardID})
  return Response.json({changed:true,revision:4,catalog_revision:8})
}
// Hardware-page saving must work before the user creates a line.
assert.equal((await saveReaderIMEI(device,input,snapshot)).changed,true)
assert.equal((await saveReaderIMEI({...device,instance_id:'line-a'},input,snapshot)).changed,true)
assert.equal(calls.length,2)
for(const invalid of [
  {...device,device_type:'modem'},
  {...device,present:false},
  {...device,present:null},
  {...device,present:undefined},
  {...device,sim:{}},
  {...device,sim:undefined},
])await assert.rejects(saveReaderIMEI(invalid,input,snapshot),/reader_imei_binding_identity_required/)
await assert.rejects(saveReaderIMEI(device,{...input,id:'missing'},snapshot),/imei_pool_selection_changed/)
await assert.rejects(saveReaderIMEI(device,{...input,imei:'123456789012354'},snapshot),/imei_pool_selection_changed/)
for(const invalid of [undefined,{...snapshot,revision:-1},{...snapshot,revision:'3'},{...snapshot,catalog_revision:NaN}]){
  await assert.rejects(saveReaderIMEI(device,input,invalid),/imei_binding_revision_required/)
}
assert.equal(calls.length,2,'rejected identities and revisions must not issue requests')

// Exercise the actual adapter/API boundary, including both CAS values after a pool write.
for(const create of [false,true]){
  for(const failAt of ['', 'pool', 'binding']){
    const requests=[]
    const edited={...input,name:'renamed',...(create?{id:undefined}:{})}
    const before={...snapshot,...(create?{revision:0,pool:[]}:{})}
    let savedID
    globalThis.fetch=async (url,options)=>{
      requests.push({url,options})
      assert.equal(options.method,'PUT')
      if(requests.length===1){
        const entry=JSON.parse(options.body)
        savedID=entry.id
        assert.ok(savedID)
        assert.equal(url,`/v1/imei-pool/${savedID}`)
        assert.equal(options.headers['If-Match'],`"${before.revision}"`)
        assert.deepEqual(entry,{schema_version:1,id:savedID,imei:input.imei,name:'renamed',notes:''})
        return failAt==='pool' ? Response.json({code:'pool_conflict'},{status:409}) : Response.json({revision:before.revision+1})
      }
      assert.equal(requests.length,2,'no automatic conflict retries or implicit provisioning')
      assert.equal(url,`/v1/imei-pool/${savedID}/cards/${cardID}`)
      assert.equal(options.headers['If-Match'],`"${before.revision+1}"`)
      assert.deepEqual(JSON.parse(options.body),{expected_catalog_revision:7,expected_card_id:cardID})
      return failAt==='binding' ? Response.json({code:'catalog_changed'},{status:409}) : Response.json({changed:true})
    }
    if(failAt){
      await assert.rejects(saveReaderIMEI(device,edited,before),error=>{
        assert.equal(error.status,409)
        assert.equal(error.code,failAt==='pool'?'pool_conflict':'catalog_changed')
        assert.equal(error.message,failAt==='pool'?'pool_conflict':'IMEI pool entry saved; SIM binding failed: catalog_changed')
        return true
      })
    }else assert.equal((await saveReaderIMEI(device,edited,before)).changed,true)
    assert.equal(requests.length,failAt==='pool'?1:2)
  }
}
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
for(const request of poolRequests.filter(x=>x.url.includes('/cards/'))){
  assert.equal(JSON.parse(request.options.body).expected_catalog_revision,7)
  assert.equal(JSON.parse(request.options.body).expected_card_id,'fixture-card')
}
const count=poolRequests.length
await assert.rejects(go.saveImeiPoolEntry(input),/imei_pool_revision_missing/)
assert.equal(poolRequests.length,count)

// Newly observed SIMs can be bound without creating or starting a line.
const newCard='8944100000000002420'
const pendingRequests=[]
globalThis.fetch=async (url,options={})=>{
  pendingRequests.push({url,options})
  if(url==='/v1/catalog/lines') return new Response(JSON.stringify({revision:7,lines:[]}),{status:200})
  assert.equal(url,`/v1/imei-pool/entry/cards/${newCard}`)
  assert.equal(options.headers['If-Match'],'"3"')
  assert.deepEqual(JSON.parse(options.body),{expected_catalog_revision:7,expected_card_id:newCard})
  return new Response(JSON.stringify({changed:true,revision:4,catalog_revision:8}),{status:200})
}
assert.equal((await go.bindImeiToIccid({iccid:newCard,imei_id:'entry'},observed)).changed,true)
await go.unbindImeiFromIccid(newCard,{...observed,bindings:{[newCard]:{imei_id:'entry'}}})
assert.deepEqual(pendingRequests.map(x=>x.options.method),['PUT','DELETE'])
console.log('New-card IMEI binding and unbinding use exact ICCID without provisioning')
