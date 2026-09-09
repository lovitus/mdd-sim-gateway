import assert from 'node:assert/strict'
import {readFile} from 'node:fs/promises'
const simPage=await readFile(new URL('../src/mdd/views/SimConfig.jsx',import.meta.url),'utf8')
assert.ok(!simPage.includes(String.raw`/^\\d{4,8}$/`),'PIN handlers must accept numeric PINs, not a literal backslash')
assert.ok(simPage.includes('creating && targetDevice?.present === true && targetDevice?.sim?.iccid'),'new SIM Save must reach the existing claim-and-save adapter')
assert.ok(!simPage.includes('if (pin) body.pin = pin'),'catalog saves must not submit the separate PIN input')
assert.ok(simPage.includes('className="mdd-sim-layout"'),'SIM panels must use the responsive layout')
assert.ok(simPage.includes('readOnly value={targetDevice.go_device?.reader?.reader_name || targetDevice.reader || targetDevice.name'), 'bound hardware must display its actual name, not the first enumerated reader')
assert.ok(simPage.includes("setPinMsg(''); setCard(null)"), 'changing hardware or card identity must clear the previous readback')
assert.ok(simPage.includes('disabled={pinBusy || !targetDevice?.sim?.iccid || targetDevice?.sim?.present === false'), 'an empty reader cannot offer a PIN request')
assert.ok(simPage.includes('if (pinEpoch.current === epoch) setPinMsg(error.message)'), 'late PIN errors must not replace the new target state')
globalThis.window = {location:{pathname:'/'}}
const {lineForm,editedCatalogLine,lineAPI,simPINIdentity,pinProof,candidateForDevice,savedCatalogLine,modemProvisionIntent,runtimeNetworkSelection} = await import('../src/mdd/lineAdapter.js')
const {mapGoSnapshot} = await import('../src/goV1Adapter.js')
const {api:go} = await import('../src/api.js')
const {lineEndpointLabel}=await import('../src/mdd/linePresentation.js')
assert.equal(lineEndpointLabel(null,{name:'EC20F'}, {},x=>x),'EC20F')
assert.equal(lineEndpointLabel({name:'Actual reader',index:0},null,{},x=>x),'[Slot 0] Actual reader')
assert.equal(lineEndpointLabel(null,null,{},x=>x),'Device not reported')
const source = {schema_version:1,id:'a',card_id:'fixture-card',enabled:false,
  sim:{imsi:'fixture-imsi',mcc:'001',mnc:'01',imei:'fixture-imei'},
  network:{ims_apn:'ims',apn_profiles:[{id:'saved-profile',password_set:true}],epdg_address:'saved-epdg'},
  ims:{impi:'saved-impi',access_type:'saved-access'}}
const form = lineForm(source,7)
assert.equal(form.provisioning_state,'ready')
assert.equal(mapGoSnapshot({catalog:{lines:[source]}}).instances[0].provisioning_state,'ready')
assert.equal(lineForm({...source,hardware_provision_state:'draft'},7).provisioning_state,'draft')
form.smsc='+12025550123';form.apn='updated-ims';form.sip.pani='updated-pani';form.imei='do-not-write'
const next = editedCatalogLine(form)
assert.equal(next.sim.smsc,form.smsc)
assert.equal(next.sim.imei,'fixture-imei')
assert.equal(next.network.epdg_address,'saved-epdg')
assert.deepEqual(next.network.apn_profiles,source.network.apn_profiles)
assert.equal(next.ims.impi,'saved-impi')
assert.equal(next.ims.access_network_info,'updated-pani')
assert.equal(source.sim.smsc,undefined)
assert.throws(() => editedCatalogLine({...form,id:'other'}),/line_catalog_identity_required/)
assert.throws(() => editedCatalogLine({...form,pin:'fixture-secret'}),/use_explicit_agent_pin_action/)
let revision
go.saveCatalogLine = async (line,value) => {revision=value;return {line,revision:8}}
assert.equal((await lineAPI.saveInstance(form)).__catalog_revision,8)
assert.equal(revision,7)
assert.throws(() => lineAPI.restoreInstance('a',undefined),/line_catalog_revision_required/)
const target = simPINIdentity({sim:{iccid:'fixture-card'},reader:'reader-a'},'fixture-card')
assert.deepEqual(target,{card_id:'fixture-card',reader_name:'reader-a'})
assert.throws(() => simPINIdentity({sim:{iccid:'other'},reader:'reader-a'},'fixture-card'),/sim_pin_card_identity_changed/)
for (const attempts of [undefined,0,1,2,'3']) assert.equal(pinProof({state:'pin_required',attempts_remaining:attempts},'op',target),null)
assert.equal(pinProof({state:'pin_required',attempts_remaining:3},'op',target).operation,'op')
assert.equal(pinProof({state:'unknown',attempts_remaining:3},'op',target),null)
const device = {device_type:'modem',sim:{iccid:'fixture-card'},go_device:{agent_id:'agent',process_generation:'process',modem:{equipment_id:'equipment',attachment_id:'attachment',sim_session_generation:'session'}}}
const originalDevices=go.devices
const detectDevice={...device,id:'modem-a',present:true}
go.devices=async()=>({devices:[{...detectDevice,sim:{iccid:'fixture-card',present:true,imsi:'fresh-imsi'},sms_diagnostics:{service_center:'+12025550123'}}]})
const detected=await lineAPI.detect(detectDevice)
assert.equal(detected.imsi,'fresh-imsi')
assert.equal(detected.smsc,'+12025550123')
assert.equal(detected.pin_enabled,null)
go.devices=async()=>({devices:[{...detectDevice,stale:true}]})
await assert.rejects(lineAPI.detect(detectDevice),/device_snapshot_unavailable/)
go.devices=async()=>({devices:[{...detectDevice,sim:{iccid:'replacement'}}]})
await assert.rejects(lineAPI.detect(detectDevice),/sim_pin_card_identity_changed/)
go.devices=originalDevices
const candidate = {card_id:'fixture-card',agent_id:'agent',process_generation:'process',equipment_id:'equipment',attachment_id:'attachment',session_generation:'session'}
assert.equal(candidateForDevice([candidate],device),candidate)
assert.equal(candidateForDevice([candidate],{...device,stale:true}),null)
assert.throws(() => simPINIdentity({...device,stale:true}),/device_snapshot_unavailable/)
assert.equal(candidateForDevice([candidate,candidate],device),null)
assert.equal(candidateForDevice([{...candidate,session_generation:'old-session'}],device),null)
const claimedSource={...source,id:'new-line',card_id:'fixture-card',hardware_provision_state:'draft'}
const previousCatalogSave=go.saveCatalogLine
let claimCalls=[]
go.lineCandidates=async()=>({catalog_revision:7,candidates:[{...candidate,candidate_id:'candidate-new',can_claim:true,observed:{}}]})
go.claimLineCandidate=async(...args)=>{claimCalls.push(args);return {line:claimedSource,revision:8}}
let newlySaved
go.saveCatalogLine=async(line,revision)=>{newlySaved={line,revision};return {line,revision:9}}
const claimState={}
const created=await lineAPI.saveInstance({name:'Edited name',mnc:'12',smsc:'+12025550123',enabled:true},device,claimState)
assert.equal(created.id,'new-line')
assert.equal(newlySaved.line.enabled,false)
assert.equal(newlySaved.line.sim.mnc,'12')
assert.equal(newlySaved.line.sim.smsc,'+12025550123')
assert.equal(newlySaved.line.sim.imei,'fixture-imei')
assert.equal(newlySaved.revision,8)
assert.equal(claimCalls.length,1)
go.saveCatalogLine=async()=>{throw new Error('save failed')}
await assert.rejects(lineAPI.saveInstance({name:'Keep edit'},device,claimState),error=>error.createdDraft?.id==='new-line'&&error.createdDraft.name==='Keep edit')
assert.equal(claimCalls[0][3],claimCalls[1][3])
await assert.rejects(lineAPI.saveInstance({pin:'1234'},device,{}),/use_explicit_agent_pin_action/)
go.saveCatalogLine=previousCatalogSave
const saved = lineForm(source,7)
assert.equal(savedCatalogLine(saved).id,'a')
assert.throws(() => savedCatalogLine({...saved,smsc:'changed'}),/save_catalog_before_provisioning/)
const intent = modemProvisionIntent(saved,device,'operation')
assert.equal(intent.attachment_id,'attachment')
assert.equal(intent.sim_session_generation,'session')
assert.equal(intent.card_id,'fixture-card')
assert.throws(() => modemProvisionIntent(saved,{...device,sim:{iccid:'changed'}},'operation'),/provision_modem_identity_unavailable/)
assert.deepEqual(runtimeNetworkSelection([{id:'a',facts:{facts:{vowifi_runtime:{detail:'pdn_family=ipv6; idr=ims'}}}}],'a'),{pdnFamily:'ipv6',responderID:'ims'})
assert.deepEqual(runtimeNetworkSelection([],'a'),{pdnFamily:'',responderID:''})
console.log('Customized MDD catalog preservation, stopped-line and PIN preflight contracts passed')
