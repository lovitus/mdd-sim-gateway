import assert from 'node:assert/strict'
globalThis.window = { location: { pathname: '/' } }
const { notificationSettingsView, notificationSettingsPatch } = await import('../src/mdd/notificationAdapter.js')

const source = { revision:7,timezone:'Asia/Shanghai',supported_events:['incoming_sms','incoming_call'],
  webhook:{enabled:false,events:{incoming_sms:true},url:{configured:true},headers:{configured:true},payload_template:{configured:false}},
  telegram:{enabled:true,events:{incoming_sms:true,incoming_call:true},proxy_mode:'country',proxy_country:'gb',bot_token:{configured:true},chat_id:{configured:true},proxy_url:{configured:false}},
  pushplus:{enabled:false,events:{incoming_sms:true},token:{configured:false},topic:{configured:false}},
}
const draft = notificationSettingsView(source,{gb:{enabled:true}})
const explained=notificationSettingsView({...source,unsupported_reasons:{number_changed:'no_authoritative_ims_number_source'}})
assert.equal(explained.__unsupported_reasons.number_changed,'no_authoritative_ims_number_source')
assert.equal(Object.hasOwn(notificationSettingsPatch(explained),'__unsupported_reasons'),false)
assert.equal(draft.telegram.bot_token,'')
assert.equal(draft.telegram.__configured.bot_token,true)
assert.equal(draft.telegram.__configured.chat_id,true)
const unchanged = notificationSettingsPatch(draft)
assert.equal(unchanged.expected_revision,7)
assert.equal(Object.hasOwn(unchanged.telegram,'bot_token'),false)
assert.equal(Object.hasOwn(unchanged.telegram,'chat_id'),false)
assert.equal(Object.hasOwn(unchanged.webhook,'headers_json'),false)
draft.telegram.bot_token='replacement-fixture'
assert.equal(notificationSettingsPatch(draft).telegram.bot_token,'replacement-fixture')
draft.telegram.__clear.chat_id=true
assert.equal(notificationSettingsPatch(draft).telegram.chat_id,'')
assert.equal(source.telegram.bot_token.configured,true)
const custom = notificationSettingsView(source)
assert.equal(Object.hasOwn(notificationSettingsPatch(custom).webhook,'payload_template'),false)
custom.webhook.__clear.headers_json=true
assert.equal(notificationSettingsPatch(custom).webhook.headers_json,'')
custom.webhook.payload_template='{"text":"fixture"}'
assert.equal(notificationSettingsPatch(custom).webhook.payload_template,'{"text":"fixture"}')
assert.equal(Object.hasOwn(notificationSettingsPatch(custom).webhook,'verify_tls'),false)
const {api:go} = await import('../src/api.js')
const {notificationAPI} = await import('../src/mdd/notificationAdapter.js')
go.notificationConfig=async () => source
go.egressConfig=async () => {throw new Error('egress unavailable')}
const preserved=await notificationAPI.notificationSettings()
assert.equal(preserved.telegram.__configured.bot_token,true)
assert.equal(preserved.telegram.proxy_country,'gb')
assert.equal(preserved.__egress_error,'egress unavailable')
const recoveryView=notificationSettingsView({...source,supported_events:[...source.supported_events,'line_unrecoverable'],telegram:{...source.telegram,events:{...source.telegram.events,line_unrecoverable:false}}})
assert.equal(notificationSettingsPatch(recoveryView).telegram.events.line_unrecoverable,false)
recoveryView.telegram.events.line_unrecoverable=true
assert.equal(notificationSettingsPatch(recoveryView).telegram.events.line_unrecoverable,true)
assert.equal(Object.hasOwn(notificationSettingsPatch(recoveryView).telegram,'bot_token'),false)
console.log('Customized MDD notification credential-preservation adapter contracts passed')
