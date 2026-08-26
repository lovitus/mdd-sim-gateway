import assert from 'node:assert/strict'
import { webcrypto } from 'node:crypto'

Object.defineProperty(globalThis, 'crypto', { value: webcrypto, configurable: true })
globalThis.location = { hostname: 'gateway.test', host: 'gateway.test:8443', protocol: 'https:', pathname: '/mdd/' }
const sockets = []
class FakeWebSocket {
  static OPEN = 1
  static CLOSING = 2
  constructor(url) { this.url = url; this.readyState = 0; this.bufferedAmount = 0; this.sent = []; sockets.push(this) }
  send(value) { this.sent.push(value) }
  close() { this.readyState = 3 }
  open() { this.readyState = 1; this.onopen() }
  message(value) { this.onmessage({ data: JSON.stringify(value) }) }
}
class FakeContext {
  constructor() {
    this.sampleRate = 48000; this.state = 'running'; this.destination = {}
    this.audioWorklet = { addModule: async () => {} }
  }
  resume() { return Promise.resolve() }
  createMediaStreamSource() { return { connect() {}, disconnect() {} } }
  close() { this.state = 'closed'; return Promise.resolve() }
}
class FakeNode {
  constructor() { this.played = []; this.port = { postMessage: value => this.played.push(value) } }
  connect() {}
  disconnect() {}
}
globalThis.WebSocket = FakeWebSocket
globalThis.AudioWorkletNode = FakeNode
globalThis.window = { location, isSecureContext: true, AudioContext: FakeContext, AudioWorkletNode: FakeNode }
let stopped = 0
let getMedia = async () => ({ getTracks: () => [{ stop() { stopped += 1 } }] })
Object.defineProperty(globalThis, 'navigator', { configurable: true,
  value: { mediaDevices: { getUserMedia: () => getMedia() } } })
const { CellularBrowserCall, cellularMediaUrl } = await import('../src/cellularBrowserCall.js')
const tick = () => new Promise(resolve => setTimeout(resolve, 0))
const settle = async () => { await tick(); await tick(); await tick() }

function fixture(options = {}) {
  const requests = []
  const events = []
  const prepared = (owner) => ({ ok: true, call_id: `owned-${requests.length}`,
    owner_token: owner, audio: { transport: 'same-origin-wss-pcm-v1', frame_bytes: 320 } })
  const api = {
    prepareCellularCall: async (...args) => { requests.push(['prepare', ...args]); return prepared(args[2]) },
    prepareIncomingCellularCall: async (...args) => { requests.push(['prepare-in', ...args]); return prepared(args[2]) },
    commitCellularCall: async (...args) => { requests.push(['commit', ...args]); return { ok: true } },
    answerIncomingCellularCall: async (...args) => { requests.push(['answer', ...args]); return { ok: true } },
    releaseCellularCall: async (...args) => { requests.push(['release', ...args]); return { released: true } },
    cellularCallStatus: async () => ({ status: 'active' }),
    cellularCallHangup: async () => { throw new Error('line-wide hangup is forbidden') },
    ...options.api,
  }
  const call = new CellularBrowserCall('7', '+441234567890', (type, data) => events.push([type, data]),
    { ...options, api })
  return { call, api, requests, events }
}
function ready(call) {
  const socket = call.socket
  socket.open()
  socket.message({ type: 'cellular.media.started', version: 1, call_id: call.callId, challenge: 'fresh', frame_bytes: 320 })
  socket.message({ type: 'cellular.media.ready', version: 1, call_id: call.callId, media: { ready: true, phase: 'ready' } })
}

assert.equal(cellularMediaUrl('7', 'a/b'), 'wss://gateway.test:8443/mdd/api/instances/7/cellular-call/a%2Fb/ws')
location.protocol = 'http:'; location.host = 'localhost:3000'
assert.equal(cellularMediaUrl('7', 'call'), 'ws://localhost:3000/mdd/api/instances/7/cellular-call/call/ws')
location.protocol = 'https:'; location.host = 'gateway.test:8443'

{
  const { call, requests } = fixture()
  call.start(); await settle()
  assert.match(call.ownerToken, /^[a-f0-9]{64}$/)
  assert.equal(requests.filter(([type]) => type === 'commit').length, 0)
  ready(call); ready(call); await settle()
  assert.equal(requests.filter(([type]) => type === 'commit').length, 1, 'never repeat a paid commit')
  assert.deepEqual(JSON.parse(call.socket.sent[0]), { type: 'cellular.media.hello', version: 1, owner_token: call.ownerToken })
  assert.deepEqual(requests.find(([type]) => type === 'commit').slice(1), ['7', call.callId, call.ownerToken])
  call.node.port.onmessage({ data: { type: 'capture', samples: new Float32Array(960).fill(0.5) } })
  assert.equal(call.socket.sent.filter(value => value instanceof ArrayBuffer).length, 1)
  call.socket.bufferedAmount = 1280
  call.node.port.onmessage({ data: { type: 'capture', samples: new Float32Array(960) } })
  assert.equal(call.socket.sent.filter(value => value instanceof ArrayBuffer).length, 1, 'congestion drops stale PCM instead of growing a queue')
  call.socket.onmessage({ data: new ArrayBuffer(320) })
  assert.equal(call.node.played.length, 1)
  await call.hangup()
  assert.equal(call.finished, true)
  assert.deepEqual(requests.find(([type]) => type === 'release').slice(1), ['7', call.callId, call.ownerToken])
}
{
  const { call, requests } = fixture({ direction: 'inbound', sourceCallId: 41 })
  call.start(); await settle()
  assert.deepEqual(requests[0].slice(0, 3), ['prepare-in', '7', 41])
  assert.equal(requests.some(([type]) => type === 'answer'), false)
  ready(call); await settle()
  assert.equal(requests.filter(([type]) => type === 'answer').length, 1)
  await call.closeLocal()
}
{
  const previous = getMedia
  getMedia = () => Promise.reject(new Error('Microphone denied'))
  const { call, requests, events } = fixture({ direction: 'inbound', sourceCallId: 42 })
  call.start(); await settle()
  assert.equal(requests.length, 0, 'denied microphone must not prepare or hang up the carrier call')
  assert.equal(events.find(([type]) => type === 'failed')[1].committed, false)
  getMedia = previous
}
{
  let resolvePrepare
  const { call, requests } = fixture({ api: {
    prepareCellularCall: (...args) => new Promise(resolve => {
      resolvePrepare = () => resolve({ call_id: 'late', owner_token: args[2],
        audio: { transport: 'same-origin-wss-pcm-v1', frame_bytes: 320 } })
    }),
  } })
  const previousSockets = sockets.length
  call.start(); await settle(); await call.hangup(); resolvePrepare(); await settle()
  assert.equal(sockets.length, previousSockets)
  assert.deepEqual(requests, [['release', '7', 'late', call.ownerToken]])
}
{
  const { call, requests, events } = fixture({ api: {
    prepareIncomingCellularCall: async () => { throw Object.assign(new Error('occupied'), { status: 409 }) },
  }, direction: 'inbound', sourceCallId: 43 })
  call.start(); await settle()
  assert.equal(requests.length, 0, 'losing claimant has no owner to release')
  assert.equal(events.find(([type]) => type === 'failed')[1].status, 409)
}
{
  const { call, requests } = fixture()
  call.start(); await settle(); ready(call); await settle()
  call.socket.onclose({ reason: 'network failed' }); await settle()
  assert.equal(requests.filter(([type]) => type === 'release').length, 1)
  assert.equal(call.context.state, 'closed')
}
{
  let resolveCommit
  const { call, requests } = fixture({ api: {
    commitCellularCall: () => new Promise(resolve => { resolveCommit = () => resolve({ ok: true }) }),
  } })
  call.start(); await settle(); ready(call); await tick()
  await call.hangup(); resolveCommit(); await settle()
  assert.equal(call.finished, true)
  assert.equal(call.committed, false, 'late commit result must not resurrect a cancelled call')
  assert.equal(requests.filter(([type]) => type === 'release').length, 1)
}
{
  const { call, requests } = fixture({ api: {
    cellularCallStatus: async () => ({ status: 'failed', unavailable: true, error: 'Agent unavailable' }),
  } })
  call.start(); await settle(); ready(call); await settle()
  await call._poll(); await call._poll(); await settle()
  assert.equal(requests.filter(([type]) => type === 'release').length, 1,
    'three unavailable status samples must end the owner instead of resetting its failure count forever')
  assert.equal(call.finished, true)
}
{
  const { call, events } = fixture({ api: {
    releaseCellularCall: async () => ({ termination_pending: true }),
  } })
  call.start(); await settle(); ready(call); await settle(); await call.hangup()
  assert.equal(call.finished, false)
  assert.equal(events.some(([type]) => type === 'ended'), false, 'accepted Hangup is not a confirmed terminal call')
  call.api.cellularCallStatus = async () => ({ status: 'idle', terminal_confirmed: true })
  await settle(); await call._poll(); await settle()
  assert.equal(call.finished, true)
}
assert.ok(stopped >= 7)
console.log('Cellular browser PCM ownership, cancellation and termination tests passed')
