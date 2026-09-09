import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
globalThis.window = {location:{pathname:'/'}}
const {mergeReportedProfiles}=await import('../src/mdd/esimAdapter.js')
const priorProfiles=[{eid:'eid-a',profiles:[],notifications:[{sequence_number:1}],notifications_read:true}]
const reportedCard={present:true,euicc:{eid:'eid-a',profiles_available:true,profiles:[{iccid:'card-a',state:'disabled',nickname:'reported'}]}}
assert.equal(mergeReportedProfiles(priorProfiles,{...reportedCard,stale:true}),priorProfiles)
const reportedView=mergeReportedProfiles(priorProfiles,reportedCard)
assert.equal(reportedView[0].profiles[0].profileNickname,'reported')
assert.equal(reportedView[0].notifications_read,false)
assert.equal(mergeReportedProfiles(reportedView,reportedCard),reportedView)
assert.equal(mergeReportedProfiles(priorProfiles,{...reportedCard,euicc:{...reportedCard.euicc,eid:'other'}}),priorProfiles)
const {euiccReaderKey, readerEuiccs, secureElementView, profileRequest, downloadView, notificationEntries, esimAPI,rememberDownload,rememberedDownload,forgetDownload,cachedDownloadReceipt,downloadRejectedBeforeDispatch,profileInventoryAvailable} = await import('../src/mdd/esimAdapter.js')
const {api:go} = await import('../src/api.js')
const entries = [
  {agent_id:'agent-a',reader_name:'Same reader',euicc:{eid:'eid-a',profiles_available:true,profiles:[]}},
  {agent_id:'agent-b',reader_name:'Same reader',euicc:{eid:'eid-b',profiles_available:false,profiles:[]}},
]
assert.equal(readerEuiccs(entries,euiccReaderKey({agent_id:'agent-b',name:'Same reader'}))[0].euicc.eid,'eid-b')
assert.equal(secureElementView(entries[1]).error,'euicc_profile_inventory_unavailable')
assert.equal(secureElementView(entries[0]).error,'')
assert.equal(secureElementView(entries[0]).notifications_read,false)
assert.equal(profileInventoryAvailable([secureElementView(entries[0])]),true)
assert.equal(profileInventoryAvailable([secureElementView(entries[1])]),false)
assert.equal(profileInventoryAvailable(entries.map(secureElementView)),false)
assert.equal(profileInventoryAvailable([]),false)
assert.equal(secureElementView(entries[0]).freeSpace,undefined)
const chip={...entries[0],euicc:{...entries[0].euicc,info:{addresses_available:true,default_smdp_address:'rsp.example',memory_available:true,free_nvm_bytes:0}}}
assert.equal(secureElementView(chip).freeSpace,0)
assert.equal(secureElementView(chip).defaultDpAddress,'rsp.example')
chip.euicc.info.memory_available=false
assert.equal(secureElementView(chip).freeSpace,undefined)
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
let refreshCalls=0
const freshEID='89049032000000000000000000000001'
go.euiccs=async()=>({euiccs:[{agent_id:'agent-a',reader_name:'Same reader',euicc:{eid:freshEID,inventory_refresh:true,profiles_available:true,profiles:[]}}]})
go.refreshEuiccInventory=async eid=>{refreshCalls++;return {outcome:'refreshed',inventory:{eid,profiles_available:true,profiles:[{iccid:'8944000000000000001',state:'disabled',nickname:'fresh'}]}}}
const liveRead=await esimAPI.esimChip(euiccReaderKey({agent_id:'agent-a',name:'Same reader'}))
assert.equal(liveRead.cached,false)
assert.equal(liveRead.ses[0].profiles[0].profileNickname,'fresh')
await esimAPI.esimChipCached(euiccReaderKey({agent_id:'agent-a',name:'Same reader'}))
assert.equal(refreshCalls,1)
go.euiccs=async()=>({euiccs:[{agent_id:'agent-a',reader_name:'Same reader',euicc:{eid:freshEID,inventory_refresh:true,notification_inventory:true,profiles_available:true,profiles:[]}}]})
go.euiccNotifications=async()=>{throw new Error('notification read failed')}
const partialRead=await esimAPI.esimChip(euiccReaderKey({agent_id:'agent-a',name:'Same reader'}))
assert.equal(partialRead.ses[0].profiles[0].profileNickname,'fresh')
assert.equal(partialRead.ses[0].notification_error,'notification read failed')
assert.equal(partialRead.ses[0].notifications_read,false)
let notificationReads=0
go.euiccNotifications=async()=>{notificationReads++;return {entries:[]}}
const emptyNotifications=await esimAPI.esimChip(euiccReaderKey({agent_id:'agent-a',name:'Same reader'}))
assert.equal(emptyNotifications.ses[0].notifications_read,true)
assert.deepEqual(emptyNotifications.ses[0].notifications,[])
const beforeCachedRefresh=refreshCalls
const reportedInventory=await esimAPI.esimChipCached(euiccReaderKey({agent_id:'agent-a',name:'Same reader'}))
assert.equal(reportedInventory.ses[0].notifications_read,false)
assert.equal(notificationReads,1,'cached inventory must not query card notifications')
assert.equal(refreshCalls,beforeCachedRefresh,'cached inventory must not trigger an APDU refresh')
console.log('Customized MDD eSIM identity, typed outcomes and deferred-deletion contracts passed')
const page=readFileSync(new URL('../src/mdd/views/Esim.jsx',import.meta.url),'utf8')
assert.ok(page.includes('cancelled || readGeneration.current !== generation || !r?.cached'))
assert.ok(page.includes("setErr(error.message || t('eUICC load failed'))"))
assert.ok(page.includes('downloadSE?.capabilities?.profile_download === true'))
assert.ok(page.includes("if (!downloadAvailable) return setErr(t('Unavailable'))"))
assert.ok(page.includes('disabled={busy || !downloadAvailable'))
assert.equal((page.match(/disabled=\{!!busyOp \|\| !readerOnline \|\| !se\.capabilities\?\.profile_management\}/g)||[]).length,3,'enable, disable and rename must all respect Agent management capability')
assert.ok(page.includes('if (current()) await loadAll(true)'))
assert.ok(page.includes("if (current()) await loadAll(label === 'Nickname' || label === 'Disable')"),'notification actions retain their explicit readback')
assert.ok(page.includes('cached ? api.esimChipCached(reader) : api.esimChip(reader)'))
assert.ok(page.includes('ses.some(se => se.notifications_read !== true)'))
assert.ok(page.includes("'(optional)'"))
assert.equal(page.includes('(lpac default TAC)'),true)
assert.ok(page.includes('profilesAvailable ? t(\'{count} profile(s)\''))
assert.ok(page.includes('role="dialog" aria-modal="true" aria-labelledby="esim-download-title"'))
assert.ok(page.includes("maxHeight: 'calc(100dvh - 32px)', overflowY: 'auto'"))
const storage=new Map()
globalThis.localStorage={setItem:(key,value)=>storage.set(key,value),getItem:key=>storage.get(key)||null,removeItem:key=>storage.delete(key)}
const pointer={eid:'89049032000000000000000000000001',operation_id:'download-one'}
rememberDownload('reader-a',{...pointer,activation_code:'never-store',confirmation_code:'never-store'})
assert.deepEqual(rememberedDownload('reader-a'),pointer)
assert.throws(()=>rememberDownload('reader-a',{...pointer,operation_id:'second-download'}),/earlier download is still tracked/)
assert.equal([...storage.values()].join('').includes('never-store'),false)
const unknown=cachedDownloadReceipt([{eid:pointer.eid,download:{operation_id:'older',job:{state:'completed'}}}],pointer)
assert.equal(unknown.operation_id,'download-one')
assert.equal(unknown.job,null)
assert.equal(downloadView(unknown.job).done,false)
forgetDownload('reader-a',{...pointer,operation_id:'older'})
assert.deepEqual(rememberedDownload('reader-a'),pointer)
forgetDownload('reader-a',pointer)
assert.equal(rememberedDownload('reader-a'),null)
assert.equal(downloadRejectedBeforeDispatch({status:400,code:'invalid_euicc_download_request'}),true)
assert.equal(downloadRejectedBeforeDispatch({status:502,code:'invalid_euicc_download_request'}),false)
assert.equal(downloadRejectedBeforeDispatch({status:409,code:'euicc_download_conflict'}),false)
assert.ok(page.indexOf('rememberDownload(reader,body)')<page.indexOf('await api.esimDownload(body)'))
assert.ok(page.includes('if(receipt.reader && receipt.reader!==activeReader.current) return'))
assert.ok(page.includes('if(!activeEIDs.current.has(receipt.eid)) return'))
assert.ok(page.includes('reader:body.reader,eid:body.eid'))
assert.ok(page.includes('if (!reader || activeReader.current !== reader) return'))
globalThis.localStorage.setItem=()=>{throw new Error('storage unavailable')}
assert.throws(()=>rememberDownload('reader-a',pointer),/no request was sent/)
delete globalThis.localStorage

const downloads=[]
go.startEuiccDownload=async (...args)=>{downloads.push(args);return {accepted:true}}
await esimAPI.esimDownload({eid:'test-eid',operation_id:'without-imei',activation_code:'LPA:1$example.com$test'})
assert.equal(downloads[0][1].imei,undefined)
assert.throws(()=>esimAPI.esimDownload({eid:'test-eid',operation_id:'invalid-imei',imei:'123'}),/euicc_download_identity_required/)
assert.equal(downloads.length,1)

const {downloadFailureLabel}=await import('../src/mdd/esimAdapter.js')
assert.match(downloadFailureLabel('euicc_rsp_8.8.2_3.1'),/CI public keys/)
assert.match(downloadFailureLabel('euicc_rsp_8.8.2_3.1'),/euicc_rsp_8\.8\.2_3\.1/)
assert.equal(downloadFailureLabel('euicc_download_failed'),'euicc_download_failed')
