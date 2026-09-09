import assert from 'node:assert/strict'
globalThis.window = { location: { pathname: '/' } }
const { smsRequest, smsAPI, canComposeSMS, isSMSSubmitKey } = await import('../src/mdd/smsAdapter.js')
assert.equal(isSMSSubmitKey({key:'Enter',nativeEvent:{isComposing:true}}),false)
assert.equal(isSMSSubmitKey({key:'Enter',nativeEvent:{isComposing:false,keyCode:229}}),false)
assert.equal(isSMSSubmitKey({key:'Enter',repeat:true}),false)
assert.equal(isSMSSubmitKey({key:'a'}),false)
assert.equal(isSMSSubmitKey({key:'Enter',nativeEvent:{keyCode:13}}),true)
const sender={id:'line',iccid:'fixture-card',operations:{cellular_sms:{ready:true},vowifi_sms:{ready:false}}}
assert.equal(canComposeSMS(sender,'cellular',' +12025550123 ','body'),true)
assert.equal(canComposeSMS(sender,'cellular','   ','body'),false)
assert.equal(canComposeSMS(sender,'cellular','+12025550123','   '),false)
assert.equal(canComposeSMS(sender,'vowifi','+12025550123','body'),false)
assert.equal(canComposeSMS({...sender,iccid:''},'cellular','+12025550123','body'),false)
assert.equal(canComposeSMS({...sender,iccid:'',card_id:'fixture-card'},'cellular','+12025550123','body'),true)
assert.equal(canComposeSMS({...sender,operations:{}},'cellular','+12025550123','body'),false)
assert.equal(canComposeSMS(sender,'auto','+12025550123','body'),false)
const { api: go } = await import('../src/api.js')
const expected = { operation_id:'fixture-operation', message_id:'fixture-operation',
  expected_card_id:'fixture-card', recipient:'+12025550123', body:'fixture body' }
assert.deepEqual(smsRequest('line', ' +12025550123 ', 'fixture body', 'cellular', 'fixture-operation', 'fixture-card'), expected)
assert.throws(() => smsRequest('line', '+12025550123', 'body', 'auto', 'op', 'card'), /sms_transport_required/)
assert.throws(() => smsRequest('line', '+12025550123', 'body', 'cellular', 'op', ''), /sms_identity_required/)
const calls = []
const failure = Object.assign(new Error('modem_sms_submit_uncertain'), { status:409 })
go.sendMessageV1 = async (...args) => { calls.push(args); throw failure }
await assert.rejects(smsAPI.sendSms('line', '+12025550123', 'fixture body', 'cellular', 'fixture-operation', 'fixture-card'), error => error === failure)
assert.deepEqual(calls, [['line', 'cellular', expected]])
go.sendMessageV1=async (line,transport,body)=>({line,transport,body})
const receipt=await smsAPI.readSmsReceipt('line',{id:'fixture-operation',payload:{to:'+12025550123',body:'fixture body',transport:'cellular',cardID:'fixture-card'}})
assert.deepEqual(receipt.body,{...expected,reconcile_only:true})
assert.throws(()=>smsAPI.readSmsReceipt('line',{id:'fixture-operation',payload:{transport:'vowifi'}}),/cellular_sms_receipt_required/)
console.log('Customized MDD SMS identity and uncertain-result adapter contracts passed')
