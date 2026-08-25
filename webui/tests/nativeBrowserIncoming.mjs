import assert from 'node:assert/strict'

const tick = () => new Promise(resolve => setTimeout(resolve, 0))
const backend = {
  id: 42, source_call_id: 'run-7:171.42', engine_run_id: 'run-7',
  browser_revision: 0, browser_state: 'ringing', peer: '+441234',
}

let socketCreates = 0
class FakeWebSocket {
  static OPEN = 1
  static CLOSING = 2
  constructor() { socketCreates += 1; this.readyState = 0; this.sent = [] }
  send(value) { this.sent.push(value) }
  close() { this.readyState = 3 }
}
globalThis.WebSocket = FakeWebSocket
globalThis.location = { hostname: 'gateway.test', protocol: 'https:', host: 'gateway.test' }
globalThis.window = { isSecureContext: true, location }

let userGesture = false
let resumeCalls = 0
class FakeContext {
  constructor() {
    this.sampleRate = 48000
    this.state = 'suspended'
    this.destination = {}
    this.audioWorklet = { addModule: async () => {} }
  }
  resume() {
    resumeCalls += 1
    if (userGesture) this.state = 'running'
    return Promise.resolve()
  }
  createMediaStreamSource() { return { connect() {}, disconnect() {} } }
  async close() { this.state = 'closed' }
}
class FakeNode {
  constructor() { this.port = { postMessage() {} } }
  connect() {}
  disconnect() {}
}
window.AudioContext = FakeContext
window.AudioWorkletNode = FakeNode
globalThis.AudioWorkletNode = FakeNode
Object.defineProperty(globalThis, 'navigator', {
  configurable: true,
  value: { mediaDevices: { getUserMedia: async () => ({
    getTracks: () => [{ stop() {} }],
  }) } },
})

const { api } = await import('../src/api.js')
const { NativeBrowserCall } = await import('../src/browserMedia.js')
const originalPrepare = api.prepareBrowserIncoming

let prepareCalls = 0
let resolvePrepare
const pendingPrepare = new Promise(resolve => { resolvePrepare = resolve })
api.prepareBrowserIncoming = () => { prepareCalls += 1; return pendingPrepare }
const events = []
const gestureCall = new NativeBrowserCall(
  '7', backend.peer, (type, data) => events.push([type, data]),
  { direction: 'inbound', backendCall: backend })
gestureCall.start()
for (let index = 0; index < 10 && !events.some(([type]) =>
  type === 'needs-user-gesture'); index += 1) await tick()
assert.equal(events.some(([type]) => type === 'needs-user-gesture'), true)
assert.equal(prepareCalls, 0)
assert.equal(socketCreates, 0)
userGesture = true
const priorResumeCalls = resumeCalls
const gesturePromise = gestureCall.enableAudioFromGesture()
assert.equal(resumeCalls, priorResumeCalls + 1)
await gesturePromise
for (let index = 0; index < 10 && prepareCalls < 1; index += 1) await tick()
assert.equal(prepareCalls, 1)
gestureCall.closeLocal()
resolvePrepare({
  purpose: 'inbound', session_id: 'session', ticket: 'ticket',
  operation_id: 'a'.repeat(32), media_epoch: 'B'.repeat(24),
  backend_call_id: 42, backend_revision: 0, call: backend,
})
await tick(); await tick()
assert.equal(socketCreates, 0)

const ownerEvents = []
const owner = new NativeBrowserCall(
  '7', backend.peer, type => ownerEvents.push(type),
  { direction: 'inbound', backendCall: backend })
owner.operationId = 'a'.repeat(32)
owner.mediaEpoch = 'B'.repeat(24)
owner.sessionId = 'session-owner'
owner.socket = { readyState: WebSocket.OPEN, sent: [], send(value) { this.sent.push(value) } }
assert.equal(owner.answer(), false)
owner.callPhase = 'ready'
assert.equal(owner.answer(), true)
assert.equal(owner.answer(), false)
assert.equal(owner.socket.sent.length, 1)
assert.equal(JSON.parse(owner.socket.sent[0]).type, 'browser.call.answer')
assert.equal(owner.ownsBackendCall({ ...backend, browser_state: 'ringing' }), true)
const claimed = {
  ...backend, browser_state: 'claiming', browser_owner_session: 'session-owner',
  browser_operation: owner.operationId, browser_epoch: owner.mediaEpoch,
}
assert.equal(owner.ownsBackendCall(claimed), true)
assert.equal(owner.ownsBackendCall({ ...claimed, browser_owner_session: 'other' }), false)
assert.equal(owner.ownsBackendCall({ ...claimed, browser_state: 'ending' }), true)
let warmupFired = false
owner.warmupTimer = setTimeout(() => { warmupFired = true }, 5)
owner._handleCallPhase({
  operation_id: owner.operationId, media_epoch: owner.mediaEpoch,
  phase: 'ready', revision: 2,
})
await new Promise(resolve => setTimeout(resolve, 10))
assert.equal(warmupFired, false)
owner._handleCallPhase({
  operation_id: owner.operationId, media_epoch: owner.mediaEpoch,
  phase: 'active', revision: 4,
})
owner._handleCallPhase({
  operation_id: owner.operationId, media_epoch: owner.mediaEpoch,
  phase: 'ready', revision: 3,
})
assert.deepEqual(ownerEvents, ['ready', 'active'])
owner.callPhase = 'ready'
assert.equal(owner.sendDTMF('5'), false)
owner.callPhase = 'active'
assert.equal(owner.sendDTMF('5'), true)

const loserEvents = []
const loser = new NativeBrowserCall(
  '7', backend.peer, type => loserEvents.push(type),
  { direction: 'inbound', backendCall: backend })
loser.operationId = 'e'.repeat(32)
loser.mediaEpoch = 'F'.repeat(24)
loser.socket = { readyState: WebSocket.OPEN, sent: [], send(value) { this.sent.push(value) },
  close() { this.readyState = 3 } }
loser._handleCallPhase({
  operation_id: loser.operationId, media_epoch: loser.mediaEpoch,
  phase: 'ready', revision: 2,
})
loser._handleCallPhase({
  operation_id: loser.operationId, media_epoch: loser.mediaEpoch,
  phase: 'answered_elsewhere', revision: 3,
})
assert.deepEqual(loserEvents, ['ready', 'answered-elsewhere'])
const local = new NativeBrowserCall(
  '7', backend.peer, () => {}, { direction: 'inbound', backendCall: backend })
local.operationId = '1'.repeat(32); local.mediaEpoch = 'G'.repeat(24)
local.socket = { readyState: WebSocket.OPEN, sent: [], send(value) { this.sent.push(value) },
  close() { this.readyState = 3 } }
assert.equal(local.closeLocal(), true)
await tick()
assert.equal(local.socket.sent.length, 0)

const sendFailureEvents = []
const sendFailure = new NativeBrowserCall(
  '7', backend.peer, (type, data) => sendFailureEvents.push([type, data]),
  { direction: 'inbound', backendCall: backend })
sendFailure.operationId = '2'.repeat(32); sendFailure.mediaEpoch = 'H'.repeat(24)
sendFailure.callPhase = 'active'
let forceClosed = false
sendFailure.socket = { readyState: WebSocket.OPEN,
  send() { throw new Error('injected send failure') }, close() { forceClosed = true } }
const originalSetTimeout = globalThis.setTimeout
globalThis.setTimeout = (fn, delay) => {
  if (delay === 500) fn()
  if (delay === 500 || delay === 10000) return { delay }
  return originalSetTimeout(fn, delay)
}
assert.equal(sendFailure.hangup(), true)
await tick()
globalThis.setTimeout = originalSetTimeout
assert.equal(forceClosed, true)
assert.equal(sendFailure.hangup(), false)

const answerFailureEvents = []
const answerFailure = new NativeBrowserCall(
  '7', backend.peer, (type, data) => answerFailureEvents.push([type, data]),
  { direction: 'inbound', backendCall: backend })
answerFailure.operationId = '3'.repeat(32); answerFailure.mediaEpoch = 'I'.repeat(24)
answerFailure.callPhase = 'ready'
answerFailure.socket = { readyState: WebSocket.OPEN,
  send() { throw new Error('injected Answer send failure') }, close() {} }
assert.equal(answerFailure.answer(), false)
assert.equal(answerFailure.answerSent, false)
assert.equal(answerFailureEvents.some(([type]) => type === 'failed'), true)

const resumeFailureEvents = []
const resumeFailure = new NativeBrowserCall(
  '7', backend.peer, (type, data) => resumeFailureEvents.push([type, data]),
  { direction: 'inbound', backendCall: backend })
let waiterReleased = false
resumeFailure.context = { state: 'suspended', resume: () => Promise.reject(
  new Error('injected resume failure')), close: async () => {} }
resumeFailure.audioGestureResolve = () => { waiterReleased = true }
await assert.rejects(resumeFailure.enableAudioFromGesture(), /injected resume failure/)
await tick()
assert.equal(waiterReleased, true)
assert.equal(resumeFailureEvents.some(([type, data]) =>
  type === 'failed' && data.category === 'audio-failed'), true)

let mismatchEvents = []
api.prepareBrowserIncoming = async () => ({
  purpose: 'inbound', session_id: 'session-mismatch', ticket: 'ticket',
  operation_id: 'c'.repeat(32), media_epoch: 'D'.repeat(24),
  backend_call_id: 42, backend_revision: 0,
  call: { ...backend, source_call_id: 'run-7:different' },
})
const mismatch = new NativeBrowserCall(
  '7', backend.peer, (type, data) => mismatchEvents.push([type, data]),
  { direction: 'inbound', backendCall: backend })
mismatch.start()
for (let index = 0; index < 20 && !mismatchEvents.some(([type]) => type === 'failed');
  index += 1) await tick()
const mismatchFailure = mismatchEvents.find(([type]) => type === 'failed')?.[1]
assert.equal(mismatchFailure?.category, 'owner-unavailable')

let capacityEvents = []
api.prepareBrowserIncoming = async () => {
  throw Object.assign(new Error('capacity'), { status: 503, data: {} })
}
const capacity = new NativeBrowserCall(
  '7', backend.peer, (type, data) => capacityEvents.push([type, data]),
  { direction: 'inbound', backendCall: backend })
capacity.start()
for (let index = 0; index < 20 && !capacityEvents.some(([type]) => type === 'failed');
  index += 1) await tick()
assert.equal(capacityEvents.find(([type]) => type === 'failed')?.[1]?.category, 'capacity')

let multiPrepare = 0
const multiEvents = [[], [], [], []]
const socketsBeforeMulti = socketCreates
api.prepareBrowserIncoming = async () => {
  multiPrepare += 1
  if (multiPrepare === 4)
    throw Object.assign(new Error('claimant capacity'), { status: 503, data: {} })
  return {
    purpose: 'inbound', session_id: `session-${multiPrepare}`, ticket: 'ticket',
    operation_id: String(multiPrepare).repeat(32), media_epoch: 'M'.repeat(24),
    backend_call_id: 42, backend_revision: 0, call: backend,
  }
}
const pages = multiEvents.map((pageEvents, index) => new NativeBrowserCall(
  '7', backend.peer, (type, data) => pageEvents.push([type, data]),
  { direction: 'inbound', backendCall: backend }))
pages.forEach(page => page.start())
for (let index = 0; index < 30 && multiPrepare < 4; index += 1) await tick()
assert.equal(multiPrepare, 4)
assert.equal(socketCreates - socketsBeforeMulti, 3)
assert.equal(multiEvents[3].find(([type]) => type === 'failed')?.[1]?.category, 'capacity')
const endingPage = pages[0]
let watchdog = null
const timerBeforeEnding = globalThis.setTimeout
globalThis.setTimeout = (fn, delay) => {
  if (delay === 10000) { watchdog = fn; return { delay } }
  return timerBeforeEnding(fn, delay)
}
endingPage._handleCallPhase({
  operation_id: endingPage.operationId, media_epoch: endingPage.mediaEpoch,
  phase: 'ready', revision: 2,
})
endingPage._handleCallPhase({
  operation_id: endingPage.operationId, media_epoch: endingPage.mediaEpoch,
  phase: 'ending', revision: 3,
})
endingPage.socket.onclose({ reason: 'server cleanup closed WSS' })
await tick()
assert.equal(multiEvents[0].some(([type]) => type === 'failed'), false)
assert.equal(typeof watchdog, 'function')
watchdog()
await tick()
assert.equal(multiEvents[0].some(([type]) => type === 'termination-unconfirmed'), true)
globalThis.setTimeout = timerBeforeEnding
pages.slice(0, 3).forEach(page => page.closeLocal())
await tick()

api.prepareBrowserIncoming = originalPrepare
console.log('Native browser incoming tests passed')
