import assert from 'node:assert/strict'
import fs from 'node:fs'
import {
  runNotificationTest,
} from '../src/notificationTestTracker.js'
import {
  callRouteOptions, messageRouteOptions, retainOrDefaultRoute, routeForExactLine, routeKey,
} from '../src/routeSelection.js'
import {
  CALL_AUDIO_BUFFER_DEFAULT_MS, getCallAudioBufferMS, normalizeCallAudioBufferMS, saveCallAudioBufferMS,
} from '../src/browserPreferences.js'

const simConfigV1 = fs.readFileSync(new URL('../src/views/SimConfigV1.jsx', import.meta.url), 'utf8')
const apiSource = fs.readFileSync(new URL('../src/api.js', import.meta.url), 'utf8')
const appSource = fs.readFileSync(new URL('../src/App.jsx', import.meta.url), 'utf8')
assert.doesNotMatch(apiSource, /\/api\/auth\/(setup|agent-token)/,
  'the active Go UI must not expose legacy first-run setup or global shared-token mutation')
assert.match(appSource, /AuthScreen[\s\S]*api\.authLogin\(username,password\)/,
  'the active login screen must use only host-bootstrapped administrator login')
assert.doesNotMatch(appSource, /api\.authSetup|Create the administrator account|Create account/,
  'the browser must not advertise an unsupported administrator setup flow')
assert.match(simConfigV1, /function SimConfigV1\(\{[^}]*\bdevices\s*=\s*\[\]/s,
  'the active SIM configuration page must receive typed device inventory explicitly')
assert.match(simConfigV1, /disabled=\{firstProvision \|\| \(!identityReady && !draft\.enabled\)\}/,
  'hardware drafts and identity-incomplete lines must remain disabled in the active SIM configuration page')
assert.match(apiSource, /provisionReadbackV1:\s*\(body\)\s*=>\s*j\('POST', '\/v1\/provision\/readback', body\)/,
  'the active API adapter must expose the independent read-only provision readback endpoint')
assert.match(simConfigV1, /onClick=\{readbackProvision\}[^>]*>\{t\([^)]*'Verify hardware state'/s,
  'the active SIM page must expose a distinct hardware readback action')
assert.match(simConfigV1, /api\.provisionReadbackV1\(request\)/,
  'hardware verification must call the read-only endpoint instead of reprovision')
assert.match(simConfigV1, /request\.preflight_operation_id\s*=\s*provisionProof\.operationID/,
  'reprovision must submit the exact successful readback as its write precondition')
assert.match(simConfigV1, /sim_session_generation:\s*result\.sim_session_generation\s*\|\|\s*request\.sim_session_generation/,
  'a read-only session rebind must gate writes on the Agent-confirmed current session')
assert.match(simConfigV1, /request\.sim_session_generation\s*=\s*provisionProof\.sessionGeneration/,
  'reprovision must send the proof-bound session without polling for a delayed health projection')
assert.match(simConfigV1, /firstProvision\s*\?\s*api\.provisionV1\(request\)\s*:\s*api\.reprovisionV1\(request\)/,
  'a claimed draft must use first-time provision before the page exposes reprovision semantics')
assert.match(simConfigV1, /disabled=\{!!busy \|\| !!runtimeBusy \|\| !provisionProofReady\}[^>]*onClick=\{reprovision\}/,
  'the active SIM page must keep hardware reprovision disabled until exact readback succeeds')
assert.doesNotMatch(simConfigV1, /observeProvision|setTimeout\(resolve,\s*delay\)/,
  'the active SIM page must not poll operation status after a synchronous provision response')
assert.match(simConfigV1, /api\.simPIN\(\{ operation_id: operationID, \.\.\.target, action: 'status' \}\)/,
  'the active SIM page must read PIN status without sending a credential')
assert.match(simConfigV1, /preflight_operation_id:\s*preflightOperationID/,
  'PIN verify must consume the exact safe status precondition')
assert.match(simConfigV1, /\['pin_required', 'retry_counter'\]\.includes\(result\.state\)/,
  'reader retry counters and modem PIN-required states must remain distinct typed outcomes')
assert.doesNotMatch(simConfigV1, /new_pin|set_enabled|api\.operationStatus\(operationID\)/,
  'the active SIM page must not expose PIN change/enable or poll a credential operation')
for (const field of ['imeisv', 'ims_apn', 'idr_mode', 'cp_mode']) {
  assert.match(simConfigV1, new RegExp(`${field}: draft\\.(?:sim|network)\\.${field}`),
    `provision must preserve durable ${field}`)
}
assert.match(simConfigV1, /Actual IMS network[^\n]*actualNetwork\.responderID[^\n]*actualNetwork\.pdnFamily/,
  'the SIM page must present Provider-selected IDr and PDN family separately from desired state')
const unifiedPages = fs.readFileSync(new URL('../src/views/UnifiedPages.jsx', import.meta.url), 'utf8')
const diagnosticsV1 = fs.readFileSync(new URL('../src/views/DiagnosticsV1.jsx', import.meta.url), 'utf8')
const systemV1 = fs.readFileSync(new URL('../src/views/SystemV1.jsx', import.meta.url), 'utf8')
assert.match(diagnosticsV1, /manual_register=true[\s\S]*api\.registerV1\(lineID, selectedLine\.iccid \|\| selectedLine\.card_id\)/,
  'the active diagnostics page must negotiate manual registration and use the typed exact-card Go route')
assert.match(apiSource, /softRestartGoDevice[\s\S]*\/cellular\/soft-restart[\s\S]*expected_card_id:[^\n]*modem\.sim\.iccid/,
  'modem soft restart must resolve a fresh exact line, card, and equipment target')
assert.doesNotMatch(unifiedPages, /Go Agent soft-restart contract is not migrated/,
  'the active device page must not leave negotiated Go soft restart disabled')
assert.match(apiSource, /authAgentCredentials:\s*\(\)\s*=>\s*j\('GET', '\/api\/auth\/agent-credentials'\)/,
  'the active API adapter must read redacted per-Agent credential status')
assert.match(systemV1, /action === 'set_mode'[\s\S]*api\.updateAgentCredentials\(payload\)/,
  'the system page must expose transition/scoped cutover and per-Agent issuance')
assert.match(systemV1, /Issue or rotate credential/,
  'the system page must expose per-Agent credential issuance and rotation')
assert.match(systemV1, /action === 'set_mode' \? \[\][\s\S]*credentialState = active \? 'Scoped'[\s\S]*'Unenrolled'/,
  'credential mutation must clear stale connection state and scoped mode must not claim a shared fallback')
assert.match(systemV1, /result\.agent_token[\s\S]*Shown once[\s\S]*type="password"/,
  'a newly issued Agent token must remain an explicit one-time secret instead of entering status state')
assert.match(systemV1, /action === 'revoke'[\s\S]*Revoke this Agent credential and disconnect its active sessions/,
  'credential revocation must require an explicit destructive confirmation')
assert.match(systemV1, /action === 'unenroll'[\s\S]*Return this Agent to the legacy shared fallback[\s\S]*mode === 'transition'/,
  'transition mode must expose an explicit rollback while scoped mode forbids fallback deletion')
assert.doesNotMatch(systemV1, /api\.authAgentToken\(/,
  'the active system page must not rotate one shared token for every Agent')

let enqueues = 0
let accepted = 0
let reads = 0
let clock = 0
const notification = await runNotificationTest({
  channel: 'telegram',
  enqueue: async channel => {
    enqueues++
    assert.equal(channel, 'telegram')
    return { delivery: { delivery_id: 'exact-delivery', state: 'pending' } }
  },
  onAccepted: async result => { accepted++; assert.equal(result.delivery.state, 'pending') },
  listDeliveries: async () => {
    reads++
    return { deliveries: [
      { delivery_id: 'another-delivery', state: 'delivered' },
      { delivery_id: 'exact-delivery', state: reads < 2 ? 'pending' : 'delivered' },
    ] }
  },
  now: () => clock,
  sleep: async milliseconds => { clock += milliseconds },
})
assert.equal(enqueues, 1, 'a notification test POST is made exactly once')
assert.equal(accepted, 1, 'the queued state is published once before polling')
assert.equal(notification.delivery.delivery_id, 'exact-delivery')
assert.equal(notification.delivery.state, 'delivered')
assert.equal(notification.timed_out, false)

let transientReads = 0
const afterReadFailure = await runNotificationTest({
  channel: 'pushplus',
  enqueue: async () => ({ delivery: { delivery_id: 'eventual-delivery', state: 'pending' } }),
  listDeliveries: async () => {
    transientReads++
    if (transientReads === 1) throw new Error('temporary read failure')
    return { deliveries: [{ delivery_id: 'eventual-delivery', state: 'delivered' }] }
  },
  now: () => clock,
  sleep: async milliseconds => { clock += milliseconds },
})
assert.equal(afterReadFailure.delivery.state, 'delivered', 'a transient status read never repeats the real notification POST')

clock = 0
const timedOut = await runNotificationTest({
  channel: 'webhook',
  enqueue: async () => ({ delivery: { delivery_id: 'slow-delivery', state: 'pending' } }),
  listDeliveries: async () => ({ deliveries: [{ delivery_id: 'slow-delivery', state: 'pending' }] }),
  timeoutMS: 1000,
  pollMS: 400,
  now: () => clock,
  sleep: async milliseconds => { clock += milliseconds },
})
assert.equal(timedOut.timed_out, true)
assert.equal(timedOut.delivery.state, 'pending')

const instances = [
  { id: 'line-a', operations: { vowifi_call: { ready: true }, cellular_call: { ready: false, blocked: ['radio_off'] }, vowifi_sms: { ready: true }, cellular_sms: { ready: false, blocked: ['cellular_sms'], facts: [{ layer: 'cellular_sms', fresh: true, available: false, code: 'cellular_sms_smsc_mismatch' }] } } },
  { id: 'line-b', operations: { vowifi_call: { ready: false, blocked: ['ims_offline'] }, cellular_call: { ready: false, blocked: ['modem_offline'] }, vowifi_sms: { ready: false, blocked: ['ims_offline'] }, cellular_sms: { ready: false, blocked: ['modem_offline'] } } },
]
const calls = callRouteOptions(instances)
const exactUnavailableCall = routeForExactLine(calls, 'line-b')
assert.equal(exactUnavailableCall.line.id, 'line-b', 'exact device selection never falls through to another SIM')
assert.equal(exactUnavailableCall.ready, false)
assert.equal(routeKey(retainOrDefaultRoute(calls, 'cellular:line-b')), 'cellular:line-b', 'an unavailable route remains selected for history and diagnostics')
const messages = messageRouteOptions(instances)
assert.equal(messages.filter(route => route.line.id === 'line-b').length, 2, 'unavailable lines remain available for message history')
assert.equal(routeForExactLine(messages, 'line-b').ready, false)
assert.equal(messages.find(route => route.line.id === 'line-a' && route.transport === 'cellular').blocked,
  'cellular_sms_smsc_mismatch', 'message routes preserve the typed SMSC blocker instead of reducing it to a layer name')

const storage = new Map()
const store = { getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value) }
assert.equal(getCallAudioBufferMS(store), CALL_AUDIO_BUFFER_DEFAULT_MS)
assert.equal(saveCallAudioBufferMS(1500, store), 1500)
assert.equal(getCallAudioBufferMS(store), 1500)
assert.equal(normalizeCallAudioBufferMS(50), 100)
assert.equal(normalizeCallAudioBufferMS(5000), 2000)

console.log('UI migration contract tests passed')
