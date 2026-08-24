import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  backendCallIdentity,
  backendFallbackCall,
  incomingReconcileActive,
  isTerminalBackendCall,
  sameBackendCall,
  selectIncomingOverlayEntry,
  shouldSurfaceIncomingSyncFailure,
  shouldShowBackendFallback,
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

assert.ok(api.includes('hangupIncomingVowifiCall'))
assert.ok(api.includes('/calls/${encodeURIComponent(callId)}/hangup'))
assert.ok(coordinator.includes('api.hangupIncomingVowifiCall'))
assert.ok(coordinator.includes('capturedBackendCall'))
assert.ok(coordinator.includes('sameBackendCall(linesRef.current[key]?.call, capturedBackendCall)'))
assert.ok(coordinator.includes('selectIncomingOverlayEntry(coordinator?.lines || {})'))
assert.ok(!coordinator.includes('api.hangup(key)'))
assert.ok(coordinator.includes("answerable === false"))
assert.ok(coordinator.includes("source: 'jssip'"))
assert.ok(coordinator.includes('backendTerminalCalls'))
assert.ok(coordinator.includes('Confirm media route'))
assert.ok(coordinator.includes('Open Calls to test'))
assert.ok(main.includes('hangup_incoming_vowifi_call'))
assert.ok(main.includes('hangup_channels_by_linkedid'))
assert.ok(!main.slice(main.indexOf('async def hangup_incoming_vowifi_call'),
  main.indexOf('async def _webrtc_port_open')).includes('hangup_all'))

console.log('VoWiFi incoming fallback tests passed')
