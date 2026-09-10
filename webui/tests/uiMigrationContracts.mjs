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

const read = file => fs.readFileSync(new URL('../src/' + file, import.meta.url), 'utf8')
const entry = read('main.jsx')
const apiSource = read('api.js')
const appSource = read('mdd/App.jsx')
const sim = read('mdd/views/SimConfig.jsx')
const provision = read('mdd/views/ProvisionActions.jsx')
const lineAdapter = read('mdd/lineAdapter.js')
const unified = read('mdd/views/UnifiedPages.jsx')
const credentials = read('mdd/views/AgentCredentials.jsx')
const systemAdapter = read('mdd/systemAdapter.js')
assert.match(entry, /import App from '.\/mdd\/App.jsx'/, 'contracts must inspect the actually mounted App')
assert.match(entry, /mdd\/index.css/, 'the customized stylesheet must be mounted')
assert.match(appSource, /api\.authLogin\(username,password\)/)
assert.doesNotMatch(appSource, /api\.authSetup|Create the administrator account/)
assert.doesNotMatch(apiSource, /\/api\/auth\/(setup|agent-token)/)
assert.match(sim, /form\.provisioning_state === 'draft'/, 'hardware drafts cannot be enabled')
assert.match(sim, /api\.lineConfiguration\(managedSelected.id\)/, 'forms use a catalog revision, not observed UI values')
assert.match(lineAdapter, /go\.saveCatalogLine\(editedCatalogLine\(form\),expectedRevision\(form.__catalog_revision\)\)/)
assert.match(provision, /savedReady[\s\S]*!savedReady/, 'incomplete or unsaved provisioning is not actionable')
assert.match(provision, /api\.readerProvisionV1\(device,form.id,form.__catalog_revision\)/)
assert.match(provision, /api\.provisionReadbackV1\(request\)/)
assert.match(provision, /preflight_operation_id:proof.request.operation_id/)
assert.match(provision, /sim_session_generation:result.sim_session_generation \|\| request.sim_session_generation/)
assert.match(provision, /api\.provisionV1\(request\).*api\.reprovisionV1\(request\)/)
assert.match(provision, /proof.identity !== identity/)
assert.match(provision, /api\.reconcileProvisionV1\(reconcile\)/)
assert.doesNotMatch(provision, /setInterval|setTimeout/, 'synchronous provision must not gain a polling loop')
assert.match(sim, /prompt\(t\('Type the line ID/)
assert.match(sim, /deletionRequests.current.get\(forId\)/)
assert.match(sim, /request.deleteHistory !== deleteHistory/)
assert.match(sim, /api\.deleteInstance\(forId, deleteHistory, form.__catalog_revision, request.operation\)/)
assert.match(sim, /preflight_operation_id:preflight/)
assert.match(sim, /action:saveOnAgent \? 'verify_save' : 'verify'/)
assert.match(sim, /action:'remove_saved'[\s\S]*expected_config_revision:pinConfiguration.revision/)
assert.match(lineAdapter, /attempts > 2/)
assert.doesNotMatch(sim, /new_pin|set_enabled|api\.operationStatus/)
assert.match(sim, /Actual IMS network[\s\S]*actualNetwork.responderID[\s\S]*actualNetwork.pdnFamily/)
for (const field of ['imeisv','ims_apn','idr_mode','cp_mode']) assert.ok(lineAdapter.includes(field+':'),field+' must reach provision')
assert.match(apiSource, /softRestartGoDevice[\s\S]*expected_card_id:[^\n]*modem.sim.iccid/)
assert.match(systemAdapter, /go\.registerV1\(lineID, line.card_id\)/)
assert.match(unified, /sim_apdu_data_active[\s\S]*VoWiFi intent was saved[\s\S]*persistent 4G data connection/)
assert.match(sim, /providerOnly && targetDevice\?\.go_device\?\.modem\?\.at_control\?\.sim_apdu_on_demand/)
assert.match(unified, /kind="connection"/)
assert.match(unified, /kind="cellular"/)
assert.match(credentials, /api\.updateAgentCredentials\(command.payload\)/)
assert.match(systemAdapter, /action === 'set_mode'/)
assert.match(systemAdapter, /action === 'unenroll' && mode !== 'transition'/)
assert.match(systemAdapter, /Revoke credential for.*Active sessions will disconnect/)
assert.match(credentials, /result.agent_token[\s\S]*Shown once[\s\S]*type="password"/)
assert.match(credentials, /action === 'set_mode' \? \[\]/)
assert.doesNotMatch(credentials, /authAgentToken|generateAgentToken|setAgentToken/)
assert.match(appSource, /<HostAlerts\/>/)
const logs = read('mdd/views/Logs.jsx')
assert.match(logs, /lineDiagnosticExportURL\(id,500\)/)
assert.match(logs, /\['all', 'agent', 'provider', 'core'\]/)
assert.doesNotMatch(logs, /setInterval|journalctl|api\/instances/)
assert.match(apiSource, /agentHealth:[\s\S]*normalizeCoreAgentHealth\(agent, payload.at\)/)

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
