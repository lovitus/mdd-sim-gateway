import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import vm from 'node:vm'

globalThis.window = { location: { protocol: 'https:', host: 'gateway.test', pathname: '/mdd/' } }
globalThis.location = { hostname: 'gateway.test' }

const { Downsampler, FRAME_BYTES, FRAME_SAMPLES, connectPcmAudio, playPcmFrame,
  NativeBrowserCall, nativeRebufferFrames, verifyBrowserMedia } = await import('../src/browserMedia.js')
const downsampler = new Downsampler(48000)
const frames = downsampler.push(new Float32Array(960).fill(0.5))
assert.equal(frames.length, 1)
assert.equal(frames[0].byteLength, FRAME_BYTES)
assert.equal(FRAME_BYTES, FRAME_SAMPLES * 2)
const view = new DataView(frames[0])
assert.ok(view.getInt16(0, true) > 16000 && view.getInt16(0, true) < 16500)

// The browser's AudioWorklet render quantum is normally 128 samples.  Streaming one second in
// those real callback-sized chunks must preserve the exact 48 kHz -> 8 kHz ratio across blocks.
const streaming = new Downsampler(48000)
let streamedSamples = 0
for (let offset = 0; offset < 48000; offset += 128) {
  const length = Math.min(128, 48000 - offset)
  for (const frame of streaming.push(new Float32Array(length)))
    streamedSamples += frame.byteLength / 2
}
assert.equal(streamedSamples, 8000)

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..')
const client = fs.readFileSync(path.join(root, 'webui/src/browserMedia.js'), 'utf8')
const worklet = fs.readFileSync(path.join(root, 'webui/src/browserMediaWorklet.js'), 'utf8')
assert.ok(client.indexOf('getUserMedia') < client.indexOf('api.prepareBrowserMedia'))

let Processor
vm.runInNewContext(worklet, {
  Float32Array, sampleRate: 48000,
  AudioWorkletProcessor: class { constructor() { this.port = { postMessage() {} } } },
  registerProcessor(name, value) { assert.equal(name, 'mdd-pcm-duplex'); Processor = value },
})
assert.equal(typeof Processor, 'function')

const nodes = [], sockets = [], events = []
class FakeNode {
  constructor() {
    this.processor = new Processor()
    this.configurations = []
    this.rebufferConfigurations = []
    this.port = { postMessage: data => {
      if (data.type === 'configure') {
        this.configurations.push(data.maxFrames)
        this.rebufferConfigurations.push(data.rebufferFrames)
        events.push(['configure', data.maxFrames, data.rebufferFrames])
      }
      this.processor.port.onmessage({ data })
    } }
    this.processor.port.postMessage = data => this.port.onmessage?.({ data })
    nodes.push(this)
  }
  connect() {}
  disconnect() {}
}
class FakeSocket {
  static OPEN = 1
  static CLOSING = 2
  constructor() { this.readyState = 1; this.bufferedAmount = 0; this.sent = []; sockets.push(this); events.push(['socket']) }
  send(data) { this.sent.push(data); this.bufferedAmount += typeof data === 'string' ? data.length : data.byteLength }
  close() { this.readyState = 3 }
}
class FakeContext {
  constructor() { this.sampleRate = 48000; this.state = 'running'; this.destination = {}; this.audioWorklet = { addModule: async () => {} } }
  createMediaStreamSource() { return { connect() {}, disconnect() {} } }
  async resume() {}
  async close() { this.state = 'closed' }
}
globalThis.AudioWorkletNode = FakeNode
globalThis.WebSocket = FakeSocket

function pcmFrame(base = 1) {
  const frame = new ArrayBuffer(FRAME_BYTES), view = new DataView(frame)
  for (let i = 0; i < FRAME_SAMPLES; i++) view.setInt16(i * 2, base + i, true)
  return frame
}
function bridge(limit) {
  const socket = new FakeSocket()
  let started = true
  const observed = []
  const audio = connectPcmAudio(new FakeContext(), {}, {
    socket: () => socket, started: () => started, stats: value => observed.push(value),
  })
  if (limit !== undefined) audio.setBufferLimit?.(limit)
  return { audio, socket, observed, setStarted: value => { started = value },
    capture: count => audio.node.port.onmessage({ data: {
      type: 'capture', samples: new Float32Array(count * 960 + 1).fill(.5),
    } }),
    play: base => playPcmFrame(audio.node, pcmFrame(base)),
  }
}

// A normal 1000ms queued burst used to stop sending at four frames and keep only six for playback.
const regression = bridge(1000)
regression.capture(50)
for (let i = 1; i <= 50; i++) regression.play(i)
assert.deepEqual({ sent: regression.socket.sent.length, queued: regression.audio.node.processor.playQueue.length },
  { sent: 50, queued: 50 }, '1000ms must reach both real production PCM queues')

for (const ms of [100, 500, 501, 1000, 1500, 2000]) {
  const maxFrames = Math.ceil(ms / 20), sample = bridge(ms), processor = sample.audio.node.processor
  assert.equal(typeof sample.audio.setBufferLimit, 'function')
  assert.equal(sample.audio.setBufferLimit(ms), maxFrames * FRAME_BYTES)
  assert.equal(sample.audio.node.configurations.at(-1), maxFrames)
  sample.capture(maxFrames)
  assert.equal(sample.socket.sent.length, maxFrames)
  assert.equal(sample.socket.bufferedAmount, maxFrames * FRAME_BYTES)
  sample.capture(1)
  assert.equal(sample.socket.sent.length, maxFrames, 'full send queue drops without claiming a forwarded frame')
  sample.socket.bufferedAmount = maxFrames * FRAME_BYTES - FRAME_BYTES + 1
  sample.capture(1)
  assert.equal(sample.socket.sent.length, maxFrames, 'projected buffered amount must include the new frame')
  sample.socket.bufferedAmount = maxFrames * FRAME_BYTES - FRAME_BYTES
  sample.capture(1)
  assert.equal(sample.socket.sent.length, maxFrames + 1, 'exactly one complete remaining frame fits')
  for (let i = 1; i <= maxFrames; i++) sample.play(i)
  assert.equal(processor.playQueue.length, maxFrames)
  assert.equal(processor.playQueue[0][0], 1 / 32768)
  sample.play(maxFrames + 1)
  assert.equal(processor.playQueue.length, maxFrames)
  assert.equal(processor.playQueue[0][0], 2 / 32768, 'overflow drops the oldest frame')
  assert.equal(processor.playedFrames, 0, 'discarded frames were not played')
  sample.setStarted(false); sample.socket.bufferedAmount = 0; sample.capture(1)
  assert.equal(sample.socket.sent.length, maxFrames + 1, 'capture cannot send before media started')
}

const defaults = bridge()
assert.equal(defaults.audio.node.configurations.at(-1), 25, 'missing setting defaults to 500ms')
assert.equal(defaults.audio.node.rebufferConfigurations.at(-1), 0, 'shared/cellular default stays immediate')
assert.deepEqual([200, 500, 1000, 1500, 2000].map(nativeRebufferFrames), [3, 5, 10, 10, 10])
for (const bad of [null, NaN, Infinity, -1, 0, 99, 2001, 500.5, '1000', true]) {
  const configurations = defaults.audio.node.configurations.length
  assert.throws(() => defaults.audio.setBufferLimit(bad), /buffer limit/i)
  assert.equal(defaults.audio.node.configurations.length, configurations, 'invalid input cannot partially apply')
}
assert.throws(() => playPcmFrame(defaults.audio.node, new ArrayBuffer(319)), /invalid PCM frame/)

const partial = bridge(100), processor = partial.audio.node.processor
partial.play(1000)
for (let i = 0; i < 12; i++) processor._nextPlaybackSample()
assert.equal(processor.playOffset, 12)
for (let i = 2; i <= 6; i++) partial.play(i * 1000)
assert.equal(processor.playQueue.length, 5)
assert.equal(processor.playOffset, 0, 'dropping the current frame must reset its playback offset')
assert.equal(processor._nextPlaybackSample(), 2000 / 32768)
assert.equal(processor.playedFrames, 0)
for (let i = 1; i < 160; i++) processor._nextPlaybackSample()
processor._nextPlaybackSample()
assert.equal(processor.playedFrames, 1, 'only a fully consumed remaining frame is counted')

// Native jitter buffering outputs silence until its target, re-enters buffering on every true
// underflow, and never counts silence or evicted frames as playback evidence.
const jitter = bridge(1000), jitterProcessor = jitter.audio.node.processor
jitter.audio.setBufferLimit(1000, 3)
const render = () => {
  const output = new Float32Array(960)
  jitterProcessor.process([], [[output]])
  return output
}
jitter.play(100); jitter.play(200)
assert.ok(render().every(value => value === 0))
assert.deepEqual({ callbacks: jitterProcessor.playbackCallbacks, played: jitterProcessor.playedFrames },
  { callbacks: 0, played: 0 })
jitter.play(300)
assert.ok(render().some(value => value !== 0), 'target resumes native playback')
render(); render(); const callbacksBeforeUnderflow = jitterProcessor.playbackCallbacks
assert.ok(render().every(value => value === 0), 'drained queue enters buffering')
assert.equal(jitterProcessor.playedFrames, 3)
assert.equal(jitterProcessor.playbackCallbacks, callbacksBeforeUnderflow,
  'underflow silence is not playback evidence')
jitter.play(400); jitter.play(500)
assert.ok(render().every(value => value === 0), 'partial rebuffer remains silent')
jitter.play(600)
assert.ok(render().some(value => value !== 0), 'full rebuffer resumes in order')

const jitterOverflow = bridge(100), jitterOverflowProcessor = jitterOverflow.audio.node.processor
jitterOverflow.audio.setBufferLimit(100, 3)
for (let i = 1; i <= 6; i++) jitterOverflow.play(i * 1000)
assert.equal(jitterOverflowProcessor.playQueue.length, 5)
assert.equal(jitterOverflowProcessor.playQueue[0][0], 2000 / 32768)
assert.equal(jitterOverflowProcessor.playedFrames, 0,
  'buffering overflow drops oldest without claiming playback')
const overflowOutput = new Float32Array(960)
jitterOverflowProcessor.process([], [[overflowOutput]])
assert.ok(overflowOutput.some(value => value !== 0))
assert.equal(overflowOutput.find(value => value !== 0), 2000 / 32768)

for (const badTarget of [-1, 1, 2, 11, 51, 3.5, '3', null, NaN, Infinity]) {
  const configurations = defaults.audio.node.configurations.length
  assert.throws(() => defaults.audio.setBufferLimit(500, badTarget), /rebuffer target/i)
  assert.equal(defaults.audio.node.configurations.length, configurations,
    'invalid rebuffer target cannot partially apply capacity')
}
const shrink = bridge(1000)
for (let i = 1; i <= 40; i++) shrink.play(i * 100)
shrink.audio.node.processor._nextPlaybackSample()
shrink.audio.setBufferLimit(100)
assert.equal(shrink.audio.node.processor.playQueue.length, 5)
assert.equal(shrink.audio.node.processor.playOffset, 0)
assert.equal(shrink.audio.node.processor._nextPlaybackSample(), 3600 / 32768)
assert.equal(shrink.audio.node.processor.playedFrames, 0)
for (const bad of [0, 4, 101, NaN, Infinity, 5.5, '50', null]) {
  shrink.audio.node.port.postMessage({ type: 'configure', maxFrames: bad })
  assert.equal(shrink.audio.node.processor.maxFrames, 5, 'Worklet refuses an invalid or unbounded capacity')
}
for (const badTarget of [-1, 1, 2, 11, 6, 3.5, '3', null, NaN, Infinity]) {
  shrink.audio.node.port.postMessage({ type: 'configure', maxFrames: 5, rebufferFrames: badTarget })
  assert.equal(shrink.audio.node.processor.maxFrames, 5)
  assert.equal(shrink.audio.node.processor.rebufferFrames, 0,
    'Worklet rejects invalid or capacity-exceeding rebuffer target')
}

// Exercise the real canary lifecycle too; no real microphone, socket, or API is used.
const { api } = await import('../src/api.js')
const originalPrepare = api.prepareBrowserMedia
window.isSecureContext = true; window.AudioContext = FakeContext; window.AudioWorkletNode = FakeNode
Object.defineProperty(globalThis, 'navigator', { configurable: true,
  value: { mediaDevices: { getUserMedia: async () => ({ getTracks: () => [{ stop() {} }] }) } } })
const tick = () => new Promise(resolve => setTimeout(resolve, 0))
try {
  for (const ms of [undefined, 500, 1000, 1500]) {
    api.prepareBrowserMedia = async () => ({ session_id: 'canary', ticket: 'ticket', buffer_limit_ms: ms })
    const before = sockets.length, eventStart = events.length
    const done = verifyBrowserMedia('7')
    for (let i = 0; i < 20 && sockets.length === before; i++) await tick()
    assert.equal(sockets.length, before + 1)
    const socket = sockets.at(-1), node = nodes.at(-1), maxFrames = Math.ceil((ms ?? 500) / 20)
    assert.deepEqual(events.slice(eventStart).slice(-2),
      [['configure', maxFrames, nativeRebufferFrames(ms ?? 500)], ['socket']])
    socket.onmessage({ data: JSON.stringify({ type: 'browser.media.started' }) })
    node.port.onmessage({ data: { type: 'capture', samples: new Float32Array(maxFrames * 960 + 1) } })
    assert.equal(socket.sent.length, maxFrames)
    socket.onmessage({ data: JSON.stringify({ type: 'browser.media.ready' }) })
    assert.equal(await done, true)
    assert.equal(socket.readyState, 3)
  }
  for (const bad of [null, NaN, 99, 2001, '1000']) {
    api.prepareBrowserMedia = async () => ({ session_id: 'canary', ticket: 'ticket', buffer_limit_ms: bad })
    const before = sockets.length
    await assert.rejects(verifyBrowserMedia('7'), /buffer limit/i)
    assert.equal(sockets.length, before, 'invalid snapshot cannot connect a WebSocket')
  }
} finally { api.prepareBrowserMedia = originalPrepare }

// A healthy native browser leg keeps its AudioContext and rotates the exact resume ticket.
const originalOutbound = api.prepareBrowserOutbound
try {
  api.prepareBrowserOutbound = async () => ({
    session_id: 'native-session', ticket: 'initial-ticket', operation_id: 'operation',
    media_epoch: 'epoch', purpose: 'outbound', buffer_limit_ms: 500,
  })
  const before = sockets.length
  const call = new NativeBrowserCall('7', '+44123456789')
  call.start()
  for (let i = 0; i < 20 && sockets.length === before; i++) await tick()
  const old = call.socket
  old.onopen()
  old.onmessage({ data: JSON.stringify({ type: 'browser.media.claimed', challenge: 'one',
    resume_ticket: 'resume-one', connection_epoch: 1 }) })
  old.onmessage({ data: JSON.stringify({ type: 'browser.media.started' }) })
  old.onmessage({ data: JSON.stringify({ type: 'browser.media.ready' }) })
  old.onclose({ code: 1006, reason: 'brief network loss' })
  assert.equal(call.finished, false)
  assert.equal(call.context.state, 'running')
  clearTimeout(call.reconnectTimer); call._openSocket(call.prepared, true)
  const resumed = call.socket
  resumed.onopen()
  assert.deepEqual(JSON.parse(resumed.sent[0]), {
    type: 'browser.media.resume', version: 1, session_id: 'native-session',
    resume_ticket: 'resume-one', connection_epoch: 1,
  })
  resumed.onmessage({ data: JSON.stringify({ type: 'browser.media.resumed', challenge: 'two',
    resume_ticket: 'resume-two', connection_epoch: 2 }) })
  old.onclose({ code: 1006, reason: 'late close' })
  assert.equal(call.socket, resumed)
  assert.equal(call.resumeTicket, 'resume-two')
  await call._cleanup()
} finally { api.prepareBrowserOutbound = originalOutbound }

for (const code of [1000, 1001, 4401, 4403, 4409]) {
  api.prepareBrowserOutbound = async () => ({
    session_id: `native-${code}`, ticket: 'initial-ticket', operation_id: 'operation',
    media_epoch: 'epoch', purpose: 'outbound', buffer_limit_ms: 500,
  })
  const before = sockets.length
  const call = new NativeBrowserCall('7', '+44123456789')
  call.start()
  for (let i = 0; i < 20 && sockets.length === before; i++) await tick()
  const socket = call.socket
  socket.onopen()
  socket.onmessage({ data: JSON.stringify({ type: 'browser.media.claimed', challenge: 'one',
    resume_ticket: 'resume', connection_epoch: 1 }) })
  socket.onmessage({ data: JSON.stringify({ type: 'browser.media.ready' }) })
  socket.onclose({ code, reason: 'not resumable' })
  await tick()
  assert.equal(sockets.length, before + 1, `${code}: native no reconnect socket`)
  assert.equal(call.finished, true)
}
api.prepareBrowserOutbound = originalOutbound

console.log('Browser WSS PCM tests passed: configured send/play bounds, partial-frame eviction, counters, and canary snapshot')
