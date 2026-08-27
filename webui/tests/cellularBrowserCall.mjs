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
  constructor() { this.played = []; this.configured = []; this.port = { postMessage: value => {
    if (value.type === 'play') this.played.push(value)
    else if (value.type === 'configure') this.configured.push(value)
  } } }
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
  socket.message({ type: 'cellular.media.started', version: 1, call_id: call.callId,
    challenge: 'fresh', frame_bytes: 320, resume_ticket: 'resume-one', connection_epoch: 1 })
  socket.message({ type: 'cellular.media.ready', version: 1, call_id: call.callId, media: { ready: true, phase: 'ready' } })
}

assert.equal(cellularMediaUrl('7', 'a/b'), 'wss://gateway.test:8443/mdd/api/instances/7/cellular-call/a%2Fb/ws')
location.protocol = 'http:'; location.host = 'localhost:3000'
assert.equal(cellularMediaUrl('7', 'call'), 'ws://localhost:3000/mdd/api/instances/7/cellular-call/call/ws')
location.protocol = 'https:'; location.host = 'gateway.test:8443'

// A configured audio backlog must not starve the evidence heartbeat. Its own
// reserved headroom is bounded, rather than allowing unlimited control messages.
{
  const originalSetInterval = globalThis.setInterval
  const originalClearInterval = globalThis.clearInterval
  const intervals = new Map()
  let sequence = 0
  globalThis.setInterval = (fn, ms) => { intervals.set(++sequence, { fn, ms }); return sequence }
  globalThis.clearInterval = id => intervals.delete(id)
  const { call, requests } = fixture({ api: {
    prepareCellularCall: async (_iid, _number, owner) => ({ ok: true, call_id: 'buffered-call',
      owner_token: owner, audio: { transport: 'same-origin-wss-pcm-v1', frame_bytes: 320,
        buffer_limit_ms: 1000 } }),
  } })
  try {
    call.start(); await settle(); ready(call); await settle()
    assert.equal(call.mediaBufferLimitBytes, 16000)
    assert.equal(call.node.configured.at(-1).maxFrames, 50)
    assert.equal(call.node.configured.at(-1).rebufferFrames, 0,
      'cellular playback keeps immediate behavior until separately proven')
    const pulse = [...intervals.values()].find(timer => timer.ms === 250).fn
    call.socket.bufferedAmount = 16000
    pulse()
    const count = () => call.socket.sent.filter(value => typeof value === 'string' &&
      JSON.parse(value).type === 'cellular.media.evidence').length
    assert.equal(count(), 1, 'a permitted 1000ms audio backlog still sends heartbeat')
    call.socket.bufferedAmount = 16000 + 1280
    pulse(); assert.equal(count(), 1, 'control headroom cannot grow without bound')
    await call.hangup()
    assert.equal(requests.filter(([type]) => type === 'commit').length, 1)
    assert.equal(intervals.size, 0)
  } finally {
    await call.hangup()
    globalThis.setInterval = originalSetInterval
    globalThis.clearInterval = originalClearInterval
  }
}

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
  call.socket.bufferedAmount = 500 / 20 * 320
  call.node.port.onmessage({ data: { type: 'capture', samples: new Float32Array(960) } })
  assert.equal(call.socket.sent.filter(value => value instanceof ArrayBuffer).length, 1, 'congestion drops stale PCM instead of growing a queue')
  call.socket.onmessage({ data: new ArrayBuffer(320) })
  assert.equal(call.node.played.length, 1)
  await call.hangup()
  assert.equal(call.finished, true)
  assert.deepEqual(requests.find(([type]) => type === 'release').slice(1), ['7', call.callId, call.ownerToken, 'user'])
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
  assert.deepEqual(requests, [['release', '7', 'late', call.ownerToken, 'user']])
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
  const old = call.socket
  old.onclose({ code: 1006, reason: 'network failed' }); await settle()
  assert.equal(requests.filter(([type]) => type === 'release').length, 0)
  assert.equal(call.context.state, 'running')
  clearTimeout(call.reconnectTimer); call._openSocket(true)
  const resumed = call.socket
  resumed.open()
  assert.deepEqual(JSON.parse(resumed.sent[0]), {
    type: 'cellular.media.resume', version: 1, owner_token: call.ownerToken,
    resume_ticket: 'resume-one', connection_epoch: 1,
  })
  resumed.message({ type: 'cellular.media.resumed', version: 1, call_id: call.callId,
    challenge: 'fresh-two', frame_bytes: 320, resume_ticket: 'resume-two', connection_epoch: 2 })
  old.onclose({ code: 1006, reason: 'late old close' }); await settle()
  assert.equal(call.socket, resumed)
  assert.equal(call.resumeTicket, 'resume-two')
  await call.hangup()
}
for (const code of [1000, 1001, 4401, 4403, 4409]) {
  const { call, requests } = fixture()
  const before = sockets.length
  call.start(); await settle(); ready(call); await settle()
  call.socket.onclose({ code, reason: 'not resumable' }); await settle()
  assert.equal(sockets.length, before + 1, `${code}: no reconnect socket`)
  assert.equal(requests.filter(([type]) => type === 'release').length, 1)
  assert.equal(call.finished, true)
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
  const { call, requests, events } = fixture({ api: {
    cellularCallStatus: async () => ({ status: 'failed', unavailable: true, error: 'Agent unavailable' }),
  } })
  call.start(); await settle(); ready(call); await settle()
  await call._poll(); await call._poll(); await settle()
  assert.equal(requests.filter(([type]) => type === 'release').length, 0,
    'management status failure must not cut an otherwise live media connection')
  assert.equal(call.finished, false)
  call.api.cellularCallStatus = async () => { throw Object.assign(new Error('temporary'), { status: 503 }) }
  for (let n = 0; n < 5; n++) await call._poll()
  assert.equal(events.filter(([type]) => type === 'status-unavailable').length, 1)
  assert.equal(requests.filter(([type]) => type === 'commit').length, 1)
  assert.equal(requests.filter(([type]) => type === 'release').length, 0)
  call.api.cellularCallStatus = async () => ({ status: 'active' })
  await call._poll()
  assert.equal(call.pollFailures, 0)
  assert.equal(events.at(-1)[0], 'active')
  call.socket.message({ type: 'cellular.media.error', version: 1, call_id: call.callId,
    error: 'actual media connection ended' }); await settle()
  assert.equal(requests.filter(([type]) => type === 'release').length, 1)
}
for (const status of [401, 403]) {
  const { call, requests } = fixture()
  call.start(); await settle(); ready(call); await settle()
  call.api.cellularCallStatus = async () => { throw Object.assign(new Error('not authorized'), { status }) }
  await call._poll(); await settle()
  assert.equal(requests.filter(([type]) => type === 'release').length, 1)
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

// A real transport/protocol failure still ends this exact owner once.
{
  const { call, requests, events } = fixture()
  call.start(); await settle(); ready(call); await settle()
  const socket = call.socket
  socket.message({ type: 'cellular.media.error', version: 1, call_id: call.callId,
    error: 'real media transport failure' })
  socket.onclose({ reason: 'transport closed after error' })
  await settle()
  assert.equal(requests.filter(([type]) => type === 'release').length, 1)
  assert.equal(events.filter(([type]) => type === 'failed').length, 1)
  assert.equal(call.finished, true)
}
console.log('Real cellular media error still releases exactly once')

// A temporary freshness observation is not a transport failure. The existing server/Agent
// lease owners decide sustained media loss; recovery must not submit a second dial/answer.
for (const direction of ['outbound', 'inbound']) {
  const { call, requests, events } = fixture({ direction, sourceCallId: 44 })
  call.start(); await settle(); ready(call); await settle()
  try {
    call.socket.message({ type: 'cellular.media.status', version: 1, call_id: call.callId,
      media: { ready: false, phase: 'degraded' } })
    await settle()
    assert.equal(requests.filter(([type]) => type === 'release').length, 0,
      `${direction}: transient degraded must leave sustained-loss cleanup to the server`)
    assert.equal(call.ending, false)
    assert.equal(call.finished, false)
    assert.equal(call.socket.readyState, WebSocket.OPEN)
    assert.equal(events.some(([type]) => type === 'failed'), false)
    assert.ok(events.some(([type, data]) => type === 'media' && data.phase === 'degraded'))
    call.socket.message({ type: 'cellular.media.status', version: 1, call_id: call.callId,
      media: { ready: true, phase: 'media_flowing' } })
    await settle()
    const paidMethod = direction === 'inbound' ? 'answer' : 'commit'
    assert.equal(requests.filter(([type]) => type === paidMethod).length, 1)
    assert.equal(requests.filter(([type]) => type === 'release').length, 0)
    assert.ok(events.some(([type, data]) => type === 'media' && data.phase === 'media_flowing'))
  } finally {
    await call.closeLocal()
  }
  assert.equal(requests.filter(([type]) => type === 'release').length, 1,
    'explicit page close still releases its owner')
}
console.log('Cellular browser PCM ownership, cancellation and termination tests passed')
