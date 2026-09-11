import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { originalCallView, historyCallDraft } from '../src/mdd/callView.js'
import { lineCallReadinessStatus, intentionalLineStop, unavailableCellularLabel, lineFailureReasons } from '../src/mdd/linePresentation.js'

const reasonDevice={facts:{summary:{state:'blocked'},raw:{operations:{vowifi_call:{facts:[
  {layer:'card',condition:'blocked',fresh:true,code:'card_not_present'},
  {layer:'card_route',condition:'blocked',fresh:true,code:'card_not_present'},
  {layer:'tunnel',condition:'failed',fresh:false,code:'old_failure'},
  {layer:'ims',condition:'ready',fresh:true,code:'ims_registered'},
]}}}}}
assert.deepEqual(lineFailureReasons(reasonDevice),[
  {code:'card_not_present',layers:['card','card_route']},
  {code:'facts_stale',layers:['tunnel']},
])
assert.deepEqual(lineFailureReasons({facts:{summary:{state:'ready'}}}),[])
assert.deepEqual(lineFailureReasons({facts:{summary:{state:'blocked',code:'vowifi_disabled'},facts:{vowifi_intent:{available:false}}}}),[])

assert.equal(unavailableCellularLabel({device_type:'modem',present:false}),'Device not connected')
assert.equal(unavailableCellularLabel({device_type:'modem',present:true}),'Cellular modem is unavailable')
assert.match(unavailableCellularLabel({device_type:'reader'}),/smart-card reader/)

assert.equal(intentionalLineStop({summary:{state:'blocked',code:'vowifi_disabled'},facts:{vowifi_intent:{available:false}}}),true)
assert.equal(intentionalLineStop({summary:{state:'blocked',code:'line_disabled'},facts:{intent:{available:false}}}),true)
assert.equal(intentionalLineStop({summary:{state:'blocked',code:'hardware_not_found'},facts:{vowifi_intent:{available:false}}}),false)
assert.equal(intentionalLineStop({summary:{state:'blocked',code:'vowifi_disabled'},facts:{}}),false)

const historyRecord = {line_id:'2', transport:'cellular', peer:'+441234567890'}
const lines = [{id:'1', operations:{}}, {id:'2', operations:{cellular_call:{ready:true}}}]
assert.deepEqual(historyCallDraft(historyRecord, lines), {lineID:'2', transport:'cellular', number:historyRecord.peer})
assert.equal(historyCallDraft({...historyRecord,line_id:'missing'}, lines), null)
assert.equal(historyCallDraft({...historyRecord,transport:'unknown'}, lines), null)
assert.equal(historyCallDraft({...historyRecord,peer:''}, lines), null)
assert.equal(lineCallReadinessStatus({id:'2',operations:{cellular_call:{ready:true}}}, [{instance_id:'2',present:true,capabilities:{call:{actual:'on',available:true}}}]).browserVoiceLabel,
  'Modem voice hardware ready; browser audio is checked per call.')

const source = { phase:'start_unknown', mode:'cellular', line_id:'fixture-line',
  callee:'fixture-peer', direction:'outgoing', muted:false,
  message:'cellular_call_start_uncertain', started_at:1234 }
assert.equal(originalCallView(source).state, 'start_unknown')
assert.equal(originalCallView(source).message, source.message)
assert.equal(originalCallView({ ...source, phase:'ending' }).state, 'ending')
assert.equal(originalCallView({ ...source, phase:'media_failed' }).state, 'media_failed')
assert.equal(originalCallView({ ...source, phase:'unrecognized' }).state, 'start_unknown')
assert.equal(originalCallView({ ...source, phase:'active', muted:true }).muted, true)
assert.equal(source.phase, 'start_unknown')
assert.equal(originalCallView(null), null)
const phone = readFileSync(new URL('../src/mdd/views/Softphone.jsx', import.meta.url), 'utf8')
assert.ok(phone.includes('setMediaTestError(error.message || String(error))'))
assert.ok(phone.includes('{mediaTestError && <span role="alert"'))
const app = readFileSync(new URL('../src/mdd/App.jsx', import.meta.url), 'utf8')
assert.equal(phone.includes('new CellularBrowserCall'), false)
assert.equal(phone.includes('closeLocal'), false)
assert.equal(phone.includes('callCoordinator.startOutgoing('), true)
assert.equal(phone.includes("dialKey('+')"), true)
assert.ok(phone.includes('historyDraft.current = draft.lineID === String(id) ? null : draft'))
assert.ok(phone.includes("draft?.lineID === String(id) ? draft.transport"))
assert.ok(phone.includes('disabled={Boolean(owned) || !historyCallDraft(c, instances)}'))
assert.ok(phone.includes('!normalizeDialTarget(num)'))
assert.equal(app.includes('useCellularIncomingCoordinator'), false)
assert.equal(app.includes('useGoCallCoordinator({'), true)
assert.equal(app.includes('snapshotEpoch.current !== epoch'), true)
console.log('Customized MDD call projection and single-owner wiring contracts passed')
