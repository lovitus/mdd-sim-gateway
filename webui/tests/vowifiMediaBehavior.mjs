import assert from 'node:assert/strict'
globalThis.location = { hostname: 'gateway.test', protocol: 'https:', host: 'gateway.test', pathname: '/' }
const sockets = []
class Socket {
  static OPEN = 1
  static CLOSING = 2
  constructor(url) { this.url = url; this.readyState = 1; this.sent = []; sockets.push(this) }
  send(value) { this.sent.push(value) }
  close() { this.readyState = 3 }
}
class Context {
  constructor() {
    this.sampleRate = 48000; this.state = 'running'; this.destination = {}
    this.audioWorklet = { addModule: async () => {} }
  }
  resume() { return Promise.resolve() }
  createMediaStreamSource() { return { connect() {}, disconnect() {} } }
  close() { this.state = 'closed'; return Promise.resolve() }
}
class Node {
  constructor() { this.port = { postMessage() {} } }
  connect() {}
  disconnect() {}
}
globalThis.window = { location, isSecureContext: true, AudioContext: Context, AudioWorkletNode: Node }
globalThis.WebSocket = Socket; globalThis.AudioWorkletNode = Node
Object.defineProperty(globalThis, 'navigator', { configurable: true, value: {
  mediaDevices: { getUserMedia: async () => ({ getTracks: () => [{ stop() {} }] }) },
} })
const { NativeBrowserCall, verifyBrowserMedia } = await import('../src/browserMedia.js')
const { api } = await import('../src/api.js')
const tick = () => new Promise(resolve => setTimeout(resolve, 0))
const prepared = id => ({ purpose: 'outbound', session_id: id, ticket: `${id}-ticket`,
  operation_id: `${id}-operation`, media_epoch: `${id}-epoch` })
let resolveOld
const requests = []
api.prepareBrowserOutbound = (id, number) => {
  requests.push(number)
  return number === '111' ? new Promise(resolve => { resolveOld = resolve })
    : Promise.resolve(prepared('new'))
}
const old = new NativeBrowserCall('7', '111').start()
for (let i = 0; i < 10 && !resolveOld; i += 1) await tick()
old.hangup()
const current = new NativeBrowserCall('7', '222').start()
for (let i = 0; i < 10 && !current.socket; i += 1) await tick()
assert.deepEqual(requests, ['111', '222'])
assert.equal(sockets.length, 1)
resolveOld(prepared('old')); await tick(); await tick()
assert.equal(sockets.length, 1, 'late cancelled preparation must not create its old transport')
assert.equal(current.context.state, 'running', 'old cleanup must not close another call audio graph')
current.hangup(); await current._cleanup()

let canaryPrepares = 0
api.prepareBrowserMedia = async () => { canaryPrepares += 1; return { session_id: 'canary', ticket: 'canary-ticket' } }
const canary = verifyBrowserMedia('7')
for (let i = 0; i < 10 && sockets.length < 2; i += 1) await tick()
const socket = sockets.at(-1)
socket.onopen()
assert.equal(JSON.parse(socket.sent[0]).session_id, 'canary')
socket.onmessage({ data: JSON.stringify({ type: 'browser.media.ready' }) })
assert.equal(await canary, true)
assert.equal(canaryPrepares, 1)
assert.deepEqual(requests, ['111', '222'], 'no-charge verification never prepares a carrier call')
console.log('Native browser stale-prepare, isolated-audio and no-charge canary tests passed')
