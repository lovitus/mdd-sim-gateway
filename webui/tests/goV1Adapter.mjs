import assert from 'node:assert/strict'
import { euiccProfileInventory, mapBrowserSnapshot, mapDeviceProfilesResponse, mapGoSnapshot, recordedUnixSeconds } from '../src/goV1Adapter.js'

for (const value of [undefined, null, '', '0001-01-01T00:00:00Z', 'invalid', '1970-01-01T00:00:00Z']) assert.equal(recordedUnixSeconds(value), 0)
assert.equal(recordedUnixSeconds('2026-09-07T00:00:00Z'), Date.parse('2026-09-07T00:00:00Z') / 1000)

const cardID = '8944100000000000001'
const policy = {
  revision: 7,
  desired: { cellular_enabled: true, flight_mode: false, roaming_enabled: true, selected_profile: 'carrier' },
  state: 'ready',
  data_lease: { session_id: 'lease-a', purpose: 'egress:gb', state: 'ready' },
}
const projection = {
  line_id: 'line-a',
  facts: [
    { layer: 'vowifi_intent', condition: 'ready', available: true, fresh: true, code: 'vowifi_enabled' },
    { layer: 'vowifi_runtime', condition: 'ready', available: true, fresh: true, code: 'runtime_running' },
    { layer: 'ims', condition: 'ready', available: true, fresh: true, code: 'ims_registered' },
  ],
  operations: {
    cellular_data: { ready: true }, cellular_call: { ready: true }, cellular_sms: { ready: true },
    vowifi_call: { ready: true }, vowifi_sms: { ready: true },
  },
}
const device = {
  id: 'modem:agent-a:imei-a', kind: 'modem', mode: 'adapted', agent_id: 'agent-a',
  process_generation: 'process-a', condition: 'ready',
  modem: {
    attachment_id: 'attachment-a', equipment_id: '862547055201716', model: 'EC20',
    at_control: { call_signalling: true, sms: true, sim_apdu: true },
    sim: { state: 'ready', iccid: cardID, imsi: '234100000000001', msisdns: ['+441234567890'],
      pin_state: 'not_required', sms_error: 'refresh_failed' },
    network: { registration: 'home', operator_name: 'Example', signal_percent: 77,
      software_radio: 'on', data: 'connected', profile: 'carrier', data_guard: 'protected' },
    policy,
  },
  endpoints: [{ association: 'exact', operation_candidate: true, card_ids: [cardID],
    line: { id: 'line-a', name: 'UK line', enabled: true, operations: projection.operations } }],
}

const mapped = mapGoSnapshot({
  lines: [projection],
  catalog: { schema_version: 1, revision: 4, lines: [{ schema_version: 1, id: 'line-a', name: 'UK line',
    enabled: true, card_id: cardID, sim: { imsi: '234100000000001', mcc: '234', mnc: '10', msisdn: '+441234567890' },
    network: { egress_country: 'gb' }, ims: {} }] },
  devices: [device],
  agents: [{ agent_id: 'agent-a', process_generation: 'process-a', capabilities: ['modem-recovery-v1'] }],
  egress: { exits: [{ country: 'gb', ready: true, node: 'London' }] },
})

assert.equal(mapped.instances.length, 1)
assert.equal(mapped.instances[0].id, 'line-a')
assert.equal(mapped.instances[0].status.state, 'OK')
assert.equal(mapped.devices.length, 1)
assert.equal(mapped.devices[0].instance_id, 'line-a')
assert.equal(mapped.devices[0].capabilities.cellular.desired, true)
assert.equal(mapped.devices[0].capabilities.cellular.actual, 'on')
assert.equal(mapped.devices[0].capabilities.roaming.desired, true)
assert.equal(mapped.devices[0].capabilities.vowifi.actual, 'on')
assert.equal(mapped.devices[0].cellular.data_lease.purpose, 'egress:gb')
assert.equal(mapped.devices[0].egress.node, 'London')
assert.equal(mapped.devices[0].sms_diagnostics.recovery.soft_restart.available, true)
assert.equal(mapped.devices[0].sms_diagnostics.recovery.soft_restart.recommended, true)

const readerCardID = '8944100000000000002'
const readerMapped = mapGoSnapshot({ lines: [], catalog: { schema_version: 1, revision: 1, lines: [] },
  devices: [{ id: 'reader:agent-r:reader-a', kind: 'reader', mode: 'remote_card', agent_id: 'agent-r',
    process_generation: 'process-r', reader: { reader_name: 'reader-a', card_present: true,
      session_generation: 'session-r', card_id: readerCardID, identity_state: 'identified',
      sim: { identity_state: 'ready', imsi: '234100000000002', mcc: '234', mnc: '10', smsc: '+447785016005' } },
    endpoints: [{ association: 'unmatched', operation_candidate: true, card_ids: [readerCardID] }] }], agents: [], egress: { exits: [] } })
assert.equal(readerMapped.devices[0].sim.imsi, '234100000000002')
assert.equal(readerMapped.devices[0].sim.mcc, '234')
assert.equal(readerMapped.devices[0].sim.mnc, '10')
assert.equal(readerMapped.devices[0].sim.smsc, '+447785016005')
assert.equal(readerMapped.cards[0].sim.identity_state, 'ready')

const ambiguous = structuredClone(device)
ambiguous.endpoints.push({ association: 'exact', operation_candidate: true, card_ids: ['8944100000000000002'],
  line: { id: 'line-b', name: 'Other', enabled: true, operations: {} } })
const ambiguousMapped = mapGoSnapshot({
  lines: [projection], catalog: mapped.go.catalog, devices: [ambiguous], egress: { exits: [] },
})
assert.equal(ambiguousMapped.devices[0].instance_id, '')
assert.equal(ambiguousMapped.devices[0].endpoint_ambiguous, true)

const pushed = mapBrowserSnapshot({ lines: [projection], catalog: mapped.go.catalog,
  devices: [device], agents: [] }, mapped)
assert.equal(pushed.devices[0].egress.node, 'London',
  'browser snapshots retain the last independently sampled egress projection')

const systemManaged = mapDeviceProfilesResponse({ device: { policy: {
  revision: 4, profile_mode: 'system_managed',
} }, profiles: [] })
assert.equal(systemManaged.supported, false)
assert.match(systemManaged.error, /managed by macOS/)

const blankEUICC = euiccProfileInventory({ profiles_available: true, profiles: null })
assert.equal(blankEUICC.available, true)
assert.equal(blankEUICC.count, 0)
assert.deepEqual(blankEUICC.profiles, [])

const unavailableEUICC = euiccProfileInventory({ profiles_available: false, profiles: null })
assert.equal(unavailableEUICC.available, false)
assert.equal(unavailableEUICC.count, null)
assert.deepEqual(unavailableEUICC.profiles, [])

console.log('Go v1 React adapter tests passed')
