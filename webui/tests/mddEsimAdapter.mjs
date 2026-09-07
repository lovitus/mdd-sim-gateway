import assert from 'node:assert/strict'
globalThis.window = {location:{pathname:'/'}}
const {euiccReaderKey, readerEuiccs, secureElementView, profileRequest, downloadView, notificationEntries, esimAPI} = await import('../src/mdd/esimAdapter.js')
const {api:go} = await import('../src/api.js')
const entries = [
  {agent_id:'agent-a',reader_name:'Same reader',euicc:{eid:'eid-a',profiles_available:true,profiles:[]}},
  {agent_id:'agent-b',reader_name:'Same reader',euicc:{eid:'eid-b',profiles_available:false,profiles:[]}},
]
assert.equal(readerEuiccs(entries,euiccReaderKey({agent_id:'agent-b',name:'Same reader'}))[0].euicc.eid,'eid-b')
assert.equal(secureElementView(entries[1]).error,'euicc_profile_inventory_unavailable')
assert.equal(secureElementView(entries[0]).error,'')
const target = {eid:'eid-a',profile:{iccid:'profile-a',profileState:'disabled',profileNickname:'before'}}
const rename = profileRequest('nickname',target,'after')
assert.equal(rename.expected_nickname,'before')
assert.equal(rename.nickname,'after')
assert.equal(Object.hasOwn(rename,'expected_state'),false)
assert.equal(profileRequest('enable',target).expected_state,'disabled')
assert.throws(() => profileRequest('delete',target), /euicc_profile_action_unavailable/)
const calls=[]
go.mutateEuiccProfile=async (...args) => {calls.push(args);return {outcome:'refresh_pending'}}
assert.equal((await esimAPI.esimEnable('profile-a',target)).outcome,'refresh_pending')
assert.equal(calls[0][0],'eid-a')
await assert.rejects(esimAPI.esimEnable('other-profile',target), /euicc_profile_identity_changed/)
assert.equal(calls.length,1)
go.mutateEuiccProfile=async () => ({outcome:'uncertain'})
await assert.rejects(esimAPI.esimEnable('profile-a',target), /euicc_profile_uncertain/)
assert.equal(downloadView({state:'uncertain'}).done,false)
assert.equal(downloadView({state:'uncertain'}).terminal,true)
assert.equal(downloadView({state:'running'}).terminal,false)
assert.equal(downloadView({state:'completed'}).done,true)
assert.throws(() => notificationEntries({eid:'eid-a',confirmed:true,notifications:[{event:'delete',sequence_number:0}]}), /euicc_deletion_customization_deferred/)
assert.throws(() => esimAPI.esimNotificationRemove(0,{eid:'eid-a',confirmed:true,notifications:[{event:'enable',sequence_number:0}]}), /euicc_notification_acknowledgement_required/)
await assert.rejects(esimAPI.esimReplayNotification({}), /euicc_deletion_customization_deferred/)
go.euiccs=async () => ({euiccs:[]})
go.devices=async () => ({devices:[{observed_only:true,last_observed_at:'2026-09-07T01:00:00Z',go_device:{agent_id:'agent-a',reader:{reader_name:'Same reader',euicc:{eid:'eid-a',profiles_available:true,profiles:[{iccid:'profile-a',state:'enabled',nickname:'remembered'}]}}}}]})
const cached=await esimAPI.esimChipCached(euiccReaderKey({agent_id:'agent-a',name:'Same reader'}))
assert.equal(cached.ses[0].profiles[0].profileNickname,'remembered')
assert.equal(cached.ses[0].capabilities.profile_management,false)
assert.equal(cached.ses[0].capabilities.profile_download,false)
assert.equal(cached.ts,Date.parse('2026-09-07T01:00:00Z')/1000)
console.log('Customized MDD eSIM identity, typed outcomes and deferred-deletion contracts passed')
