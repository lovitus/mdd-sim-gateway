import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { originalCallView, historyCallDraft } from '../src/mdd/callView.js'
import { lineCallReadinessStatus } from '../src/mdd/linePresentation.js'

const historyRecord = {line_id:'2', transport:'cellular', peer:'+441234567890'}
const lines = [{id:'1', operations:{}}, {id:'2', operations:{cellular_call:{ready:true}}}]
assert.deepEqual(historyCallDraft(historyRecord, lines), {lineID:'2', transport:'cellular', number:historyRecord.peer})
assert.equal(historyCallDraft({...historyRecord,line_id:'missing'}, lines), null)
assert.equal(historyCallDraft({...historyRecord,transport:'unknown'}, lines), null)
assert.equal(historyCallDraft({...historyRecord,peer:''}, lines), null)
assert.equal(lineCallReadinessStatus({id:'2'}, [{instance_id:'2',present:true,capabilities:{call:{actual:'on',available:true}}}]).browserVoiceLabel,
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
