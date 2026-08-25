import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  backendCallIdentity,
  backendFallbackCall,
  boundedIdentityMapSet,
  incomingReconcileActive,
  isTerminalBackendCall,
  sameBackendCall,
  selectIncomingOverlayEntry,
  shouldSurfaceIncomingSyncFailure,
  shouldShowBackendFallback,
  stopNativeCall,
  nativeCallbackCurrent,
  nativeDeclineEligible,
  routeNativeHangup,
} from '../src/vowifiIncomingFallback.js'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..')
const coordinator = fs.readFileSync(path.join(root, 'webui/src/callCoordinator.jsx'), 'utf8')
const api = fs.readFileSync(path.join(root, 'webui/src/api.js'), 'utf8')
const main = fs.readFileSync(path.join(root, 'control/app/main.py'), 'utf8')

const incoming = {
  id: 42,
  direction: 'in',
  peer: '+441234',
  status: 'ringing',
  engine_run_id: 'run-7',
  source_call_id: 'run-7:171.42',
}

assert.equal(backendCallIdentity(incoming), '42:run-7:run-7:171.42')
assert.equal(isTerminalBackendCall(incoming), false)
assert.equal(isTerminalBackendCall({ ...incoming, end_ts: 1 }), true)
assert.equal(isTerminalBackendCall({ ...incoming, status: 'missed' }), true)
for (const state of ['ringing', 'claiming', 'attach_submitted_unknown',
  'answer_submitted_unknown', 'active', 'ending', 'unknown'])
  assert.equal(isTerminalBackendCall({
    ...incoming, browser_state: state, status: state === 'active' ? 'answered' : 'ringing',
  }), false, state)
assert.equal(isTerminalBackendCall({ ...incoming, browser_state: 'terminal' }), true)
assert.equal(shouldShowBackendFallback(null, incoming), true)
assert.equal(shouldShowBackendFallback({ state: 'ended' }, incoming), true)
assert.equal(shouldShowBackendFallback(
  { source: 'jssip', state: 'incoming' }, incoming), false)
assert.equal(shouldShowBackendFallback(
  { source: 'jssip', state: 'active' }, incoming), false)
assert.equal(shouldShowBackendFallback(
  { source: 'jssip', state: 'checking' }, incoming), false)
assert.equal(shouldShowBackendFallback(
  { source: 'backend', backendCallId: '42', engineRunId: 'run-7',
    sourceCallId: 'run-7:171.42',
    state: 'incoming' }, incoming), true)
assert.equal(shouldShowBackendFallback(
  null, incoming, new Set(['42:run-7:run-7:171.42'])), false)

const fallback = backendFallbackCall('7', incoming)
assert.equal(fallback.source, 'backend')
assert.equal(fallback.answerable, false)
assert.equal(fallback.backendCallId, '42')
assert.equal(fallback.sourceCallId, 'run-7:171.42')
assert.equal(sameBackendCall(fallback, incoming), true)
assert.equal(sameBackendCall(fallback, { ...incoming, source_call_id: 'different' }), false)
assert.deepEqual(selectIncomingOverlayEntry({
  '1': { call: fallback },
  '7': { call: {
    source: 'jssip', answerable: true, transport: 'vowifi', state: 'incoming',
    number: '+337',
  } },
})?.[0], '7')
assert.deepEqual(selectIncomingOverlayEntry({
  '1': { call: fallback },
})?.[0], '1')
assert.equal(shouldSurfaceIncomingSyncFailure(1, 3), false)
assert.equal(shouldSurfaceIncomingSyncFailure(3, 3), false)
assert.equal(shouldSurfaceIncomingSyncFailure(4, 3), true)
assert.equal(shouldSurfaceIncomingSyncFailure(5, 3), false)
assert.equal(incomingReconcileActive(true, true, ['7'], '7'), true)
assert.equal(incomingReconcileActive(false, true, ['7'], '7'), false)
assert.equal(incomingReconcileActive(true, false, ['7'], '7'), false)
assert.equal(incomingReconcileActive(true, true, [], '7'), false)
const bounded = new Map()
for (let index = 0; index < 512; index += 1)
  boundedIdentityMapSet(bounded, `call-${index}`, index)
assert.equal(bounded.size, 256)
assert.equal(bounded.has('call-0'), false)
assert.equal(bounded.get('call-511'), 511)
const stopped = []
assert.equal(stopNativeCall({ direction: 'inbound', answerSent: false, callPhase: 'ready',
  closeLocal: () => stopped.push('local'), hangup: () => stopped.push('hangup') }), 'local')
assert.equal(stopNativeCall({ direction: 'inbound', answerSent: true, callPhase: 'claiming',
  closeLocal: () => stopped.push('local'), hangup: () => stopped.push('hangup') }), 'hangup')
assert.deepEqual(stopped, ['local', 'hangup'])
const calls = new Map(); const identities = new Map(); const quickCall = {}
calls.set('7', quickCall); identities.set('7', 'identity-7')
assert.equal(nativeCallbackCurrent(calls, identities, '7', quickCall, 'identity-7'), true)
assert.equal(nativeCallbackCurrent(calls, identities, '7', {}, 'identity-7'), false)
assert.equal(nativeCallbackCurrent(calls, identities, '7', quickCall, 'stale'), false)
const localRinging = { source: 'native-wss-incoming', localOwner: true,
  backendState: 'ringing', state: 'incoming' }
assert.equal(nativeDeclineEligible(localRinging), true)
assert.equal(nativeDeclineEligible({ ...localRinging, backendState: 'answer_submitted_unknown',
  state: 'answering' }), false)
assert.equal(nativeDeclineEligible({ ...localRinging, state: 'active' }), false)
assert.equal(nativeDeclineEligible({ ...localRinging, state: 'ending' }), false)
assert.equal(nativeDeclineEligible({ ...localRinging, source: 'backend' }), false)
let exactHangups = 0
assert.deepEqual(routeNativeHangup({ hangup: () => true }, () => { exactHangups += 1 }),
  { route: 'wss', result: true })
assert.deepEqual(routeNativeHangup({ hangup: () => false }, () => {
  exactHangups += 1; return 'submitted'
}), { route: 'exact', result: 'submitted' })
assert.equal(exactHangups, 1)

assert.ok(api.includes('hangupIncomingVowifiCall'))
assert.ok(api.includes('/calls/${encodeURIComponent(callId)}/hangup'))
assert.ok(coordinator.includes('api.hangupIncomingVowifiCall'))
assert.ok(coordinator.includes('terminateExactIncoming'))
assert.ok(coordinator.includes("disposition = 'hangup'"))
assert.ok(coordinator.includes("localDecline ? 'decline' : 'hangup'"))
assert.ok(coordinator.includes('sameUiCall'))
assert.ok(coordinator.includes('selectIncomingOverlayEntry(coordinator?.lines || {})'))
assert.ok(!coordinator.includes('api.hangup(key)'))
assert.ok(coordinator.includes("answerable === false"))
assert.ok(coordinator.includes("source: 'jssip'"))
assert.ok(coordinator.includes('backendTerminalCalls'))
assert.ok(coordinator.includes('Confirm media route'))
assert.ok(coordinator.includes('Open Calls to test'))
assert.ok(coordinator.includes("call.source === 'native-wss-incoming'"))
assert.ok(coordinator.includes('enableIncomingAudio'))
assert.ok(main.includes('hangup_incoming_vowifi_call'))
assert.ok(main.includes('hangup_channels_by_linkedid'))
assert.ok(!main.slice(main.indexOf('async def hangup_incoming_vowifi_call'),
  main.indexOf('async def _webrtc_port_open')).includes('hangup_all'))

console.log('VoWiFi incoming fallback tests passed')
