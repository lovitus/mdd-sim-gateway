import assert from 'node:assert/strict'
import {
  runNotificationTest,
} from '../src/notificationTestTracker.js'
import {
  callRouteOptions, messageRouteOptions, retainOrDefaultRoute, routeForExactLine, routeKey,
} from '../src/routeSelection.js'
import {
  CALL_AUDIO_BUFFER_DEFAULT_MS, getCallAudioBufferMS, normalizeCallAudioBufferMS, saveCallAudioBufferMS,
} from '../src/browserPreferences.js'

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
  { id: 'line-a', operations: { vowifi_call: { ready: true }, cellular_call: { ready: false, blocked: ['radio_off'] }, vowifi_sms: { ready: true }, cellular_sms: { ready: false, blocked: ['radio_off'] } } },
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

const storage = new Map()
const store = { getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value) }
assert.equal(getCallAudioBufferMS(store), CALL_AUDIO_BUFFER_DEFAULT_MS)
assert.equal(saveCallAudioBufferMS(1500, store), 1500)
assert.equal(getCallAudioBufferMS(store), 1500)
assert.equal(normalizeCallAudioBufferMS(50), 100)
assert.equal(normalizeCallAudioBufferMS(5000), 2000)

console.log('UI migration contract tests passed')
