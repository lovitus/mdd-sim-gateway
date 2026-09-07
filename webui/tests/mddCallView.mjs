import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { originalCallView } from '../src/mdd/callView.js'

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
assert.equal(app.includes('useCellularIncomingCoordinator'), false)
assert.equal(app.includes('useGoCallCoordinator({'), true)
assert.equal(app.includes('snapshotEpoch.current !== epoch'), true)
console.log('Customized MDD call projection and single-owner wiring contracts passed')
