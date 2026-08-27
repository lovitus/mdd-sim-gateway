import assert from 'node:assert/strict'

const tick = () => new Promise(resolve => setTimeout(resolve, 0))

let websocketCreates = 0
const bufferOrder = []
class FakeWebSocket {
  static OPEN = 1
  static CLOSING = 2
  constructor() { websocketCreates += 1; this.readyState = 0; bufferOrder.push(['socket']) }
  close() { this.readyState = 3 }
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
  constructor() {
    this.configurations = []
    this.port = { postMessage: message => {
      if (message.type === 'configure') {
        this.configurations.push(message.maxFrames)
        bufferOrder.push(['configure', message.maxFrames])
      }
    } }
  }
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
const originalIncoming = api.prepareBrowserIncoming
const backend = { id: 42, source_call_id: 'run-7:42', engine_run_id: 'run-7',
  browser_revision: 0, browser_state: 'ringing', peer: '+447700900123' }
try {
  for (const direction of ['outbound', 'inbound']) {
    for (const limit of [undefined, 500, 1000, 1500]) {
      const prepare = async () => ({ purpose: direction, session_id: 'buffer-session', ticket: 'ticket',
        operation_id: 'operation', media_epoch: 'epoch', buffer_limit_ms: limit,
        backend_call_id: 42, backend_revision: 0, call: backend })
      api.prepareBrowserOutbound = prepare; api.prepareBrowserIncoming = prepare
      const before = websocketCreates, orderedFrom = bufferOrder.length
      const call = new NativeBrowserCall('7', backend.peer, () => {},
        { direction, backendCall: backend })
      await call._run()
      assert.equal(websocketCreates, before + 1)
      assert.equal(call.node.configurations.at(-1), Math.ceil((limit ?? 500) / 20))
      assert.deepEqual(bufferOrder.slice(orderedFrom).slice(-2),
        [['configure', Math.ceil((limit ?? 500) / 20)], ['socket']], 'snapshot must configure PCM before WS starts')
      await call._cleanup()
    }
    for (const bad of [null, NaN, 99, 2001, '1000']) {
      const prepare = async () => ({ purpose: direction, session_id: 'buffer-session', ticket: 'ticket',
        operation_id: 'operation', media_epoch: 'epoch', buffer_limit_ms: bad,
        backend_call_id: 42, backend_revision: 0, call: backend })
      api.prepareBrowserOutbound = prepare; api.prepareBrowserIncoming = prepare
      const before = websocketCreates, failures = []
      const call = new NativeBrowserCall('7', backend.peer, (type, value) => failures.push([type, value]),
        { direction, backendCall: backend })
      await call._run()
      assert.equal(websocketCreates, before, 'invalid prepared limit cannot open WS')
      assert.ok(failures.some(([type, value]) => type === 'failed' && /buffer limit/i.test(value.cause)))
      await call._cleanup()
    }
  }
} finally { api.prepareBrowserOutbound = originalPrepare; api.prepareBrowserIncoming = originalIncoming }
console.log('Native browser call tests passed: cancellation and prepared buffer snapshots for both directions')
