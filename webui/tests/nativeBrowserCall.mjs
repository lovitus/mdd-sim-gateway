import assert from 'node:assert/strict'

const tick = () => new Promise(resolve => setTimeout(resolve, 0))

let websocketCreates = 0
class FakeWebSocket {
  static OPEN = 1
  static CLOSING = 2
  constructor() { websocketCreates += 1 }
}
globalThis.WebSocket = FakeWebSocket
globalThis.location = { hostname: 'gateway.test', protocol: 'https:', host: 'gateway.test' }
globalThis.window = { isSecureContext: true, AudioWorkletNode: null, location }
const { api } = await import('../src/api.js')
const { NativeBrowserCall } = await import('../src/browserMedia.js')

let resolveMedia
const pendingMedia = new Promise(resolve => { resolveMedia = resolve })
Object.defineProperty(globalThis, 'navigator', {
  configurable: true,
  value: { mediaDevices: { getUserMedia: () => pendingMedia } },
})
window.AudioWorkletNode = class {}
let prepareCalls = 0
const originalPrepare = api.prepareBrowserOutbound
api.prepareBrowserOutbound = async () => { prepareCalls += 1; return {} }

const beforePermission = new NativeBrowserCall('7', '+447700900123')
beforePermission.start()
beforePermission.hangup()
resolveMedia({ getTracks: () => [{ stop() {} }] })
await tick(); await tick()
assert.equal(prepareCalls, 0)
assert.equal(websocketCreates, 0)

class FakeNode {
  constructor() { this.port = {} }
  connect() {}
  disconnect() {}
}
class FakeContext {
  constructor() {
    this.sampleRate = 48000
    this.state = 'running'
    this.destination = {}
    this.audioWorklet = { addModule: async () => {} }
  }
  async resume() {}
  createMediaStreamSource() { return { connect() {}, disconnect() {} } }
  async close() { this.state = 'closed' }
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

let resolvePrepare
const pendingPrepare = new Promise(resolve => { resolvePrepare = resolve })
api.prepareBrowserOutbound = () => { prepareCalls += 1; return pendingPrepare }
const duringPrepare = new NativeBrowserCall('7', '+447700900123')
duringPrepare.start()
for (let index = 0; index < 10 && prepareCalls < 1; index += 1) await tick()
assert.equal(prepareCalls, 1)
duringPrepare.hangup()
resolvePrepare({
  purpose: 'outbound', session_id: 'session', ticket: 'ticket',
  operation_id: 'operation', media_epoch: 'epoch',
})
await tick(); await tick()
assert.equal(websocketCreates, 0)

const phases = []
const reordered = new NativeBrowserCall(
  '7', '+447700900123', type => phases.push(type))
reordered.operationId = 'operation'
reordered.mediaEpoch = 'epoch'
reordered._handleCallPhase({
  type: 'browser.call.phase', operation_id: 'operation', media_epoch: 'epoch',
  phase: 'active', revision: 4,
})
reordered._handleCallPhase({
  type: 'browser.call.phase', operation_id: 'operation', media_epoch: 'epoch',
  phase: 'calling', revision: 3,
})
assert.deepEqual(phases, ['active'])
assert.equal(reordered.lastCallRevision, 4)

api.prepareBrowserOutbound = originalPrepare
console.log('Native browser call cancellation tests passed')
