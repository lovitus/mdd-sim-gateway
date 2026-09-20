import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { LocalCallRecording, recordingSupported, requestLocalRecording } from '../src/callRecording.js'
import { CallMedia } from '../src/goCallMedia.js'

function fixture(options = {}) {
  const edges = [], disconnected = [], timers = new Map()
  let clock = 1000, id = 0, trackStops = 0, recorder
  const node = name => ({ name, gain: { value: 1 }, connect(...args) { edges.push([name, ...args]) },
    disconnect(...args) { disconnected.push([name, ...args]) } })
  const mic = node('microphone'), remote = node('playback'), graph = []
  const create = name => { const n = node(name); graph.push(n); return n }
  const context = { state: 'running', createMediaStreamDestination() {
    const n = create('recording-destination'); n.stream = { getTracks: () => [{ stop() { trackStops++ } }] }; return n
  }, createChannelMerger: () => create('merger'), createGain: () => create('gain') }
  class Encoder {
    static isTypeSupported(type) { return type === 'audio/webm;codecs=opus' }
    constructor(stream, opts) { recorder = this; this.stream = stream; this.mimeType = opts?.mimeType; this.state = 'inactive'; this.stops = 0 }
    start(timeslice) { assert.equal(timeslice, 1000); this.state = 'recording' }
    stop() { this.stops++; this.state = 'inactive' }
    chunk(data) { this.ondataavailable?.({ data: new Blob([data], { type: this.mimeType }) }) }
    ended() { this.onstop?.() }
  }
  const changes = []
  const owned = new LocalCallRecording({ context, microphone: mic, playback: remote,
    Recorder: Encoder, now: () => clock, onChange: x => changes.push(x),
    setTimer: (fn, delay) => { const key = ++id; timers.set(key, { fn, delay }); return key },
    clearTimer: key => timers.delete(key), ...options })
  return { owned, mic, remote, context, graph, edges, disconnected, timers, changes,
    recorder: () => recorder, tick(ms) { clock += ms }, stops: () => trackStops,
    fire(delay) { const [key, timer] = [...timers].find(([, t]) => t.delay === delay); timers.delete(key); timer.fn() } }
}
// DOM timer methods require the global receiver. Node and injected arrow
// timers otherwise conceal Illegal invocation errors on real browsers.
{
  const savedSet = globalThis.setTimeout, savedClear = globalThis.clearTimeout
  const pending = new Set()
  let next = 0
  globalThis.setTimeout = function (callback, delay) {
    assert.equal(this, globalThis, 'browser timer must keep its global receiver')
    assert.equal(typeof callback, 'function'); assert.ok(delay > 0)
    const id = ++next; pending.add(id); return id
  }
  globalThis.clearTimeout = function (id) {
    assert.equal(this, globalThis, 'browser clearTimer must keep its global receiver')
    pending.delete(id)
  }
  try {
    const realDefaults = fixture({ setTimer: undefined, clearTimer: undefined })
    realDefaults.owned.start(true)
    realDefaults.recorder().chunk('encoded')
    const encoder = realDefaults.recorder()
    realDefaults.owned.stop(); encoder.ended(); realDefaults.owned.discard()
    assert.equal(pending.size, 0, 'default timer resources must be released')
  } finally { globalThis.setTimeout = savedSet; globalThis.clearTimeout = savedClear }
}
assert.equal(recordingSupported(undefined), false)
assert.throws(() => new LocalCallRecording({ maxBytes: Infinity }), /Invalid/)
const denied = fixture()
assert.throws(() => denied.owned.start(false), /consent/)
assert.equal(denied.graph.length, 0)
const recorded = fixture()
recorded.owned.start(true)
assert.equal(recorded.owned.state, 'recording')
assert.throws(() => recorded.owned.start(true), /already/)
const encoder = recorded.recorder()
encoder.chunk('first')
assert.equal(recorded.owned.micGain.gain.value, 1)
recorded.owned.setMuted(true)
assert.equal(recorded.owned.micGain.gain.value, 0)
recorded.owned.setMuted(false)
assert.equal(recorded.owned.micGain.gain.value, 1)
recorded.tick(100)
recorded.owned.stop()
recorded.owned.stop()
assert.equal(encoder.stops, 1)
assert.equal(recorded.owned.state, 'finalizing')
encoder.chunk('final') // stop() is asynchronous; never omit its last data event.
encoder.ended()
assert.equal(await recorded.owned.clip.text(), 'firstfinal')
assert.equal(recorded.owned.snapshot().durationMS, 100)
assert.equal(recorded.owned.state, 'ready')
assert.equal(recorded.timers.size, 0)
assert.equal(recorded.stops(), 1)
for (const source of ['microphone', 'playback']) {
  const calls = recorded.disconnected.filter(([name]) => name === source)
  assert.ok(calls.length > 0)
  assert.ok(calls.every(call => call.length === 2), 'never disconnect the live media source globally')
}
let clicked = 0, removed = 0, revoked = 0
const anchor = { click() { clicked++ }, remove() { removed++ } }
const file = recorded.owned.save({ document: { createElement: () => anchor, body: { appendChild() {} } },
  URL: { createObjectURL: () => 'blob:private', revokeObjectURL() { revoked++ } } })
assert.match(file, /^mdd-recording-[\dTZ-]+\.webm$/)
assert.equal(clicked, 1); assert.equal(removed, 1)
recorded.owned.discard(); recorded.owned.discard()
assert.equal(revoked, 1)
assert.equal(recorded.owned.clip, null)
assert.equal(recorded.owned.bytes, 0)
assert.equal(recorded.timers.size, 0)
assert.throws(() => recorded.owned.save(), /No completed/)

const muted = fixture({ muted: true }); muted.owned.start(true)
assert.equal(muted.owned.micGain.gain.value, 0); muted.owned.discard()
const oversized = fixture({ maxBytes: 5 }); oversized.owned.start(true)
oversized.recorder().chunk('123456')
assert.equal(oversized.owned.state, 'failed'); assert.equal(oversized.owned.clip, null)
assert.equal(oversized.owned.bytes, 0); assert.equal(oversized.stops(), 1)
assert.equal(oversized.timers.size, 0)
const timed = fixture({ maxMS: 50 }); timed.owned.start(true); const timedEncoder = timed.recorder()
timedEncoder.chunk('data'); timed.tick(50); timed.fire(50)
assert.equal(timed.owned.state, 'finalizing'); timedEncoder.ended()
assert.equal(timed.owned.reason, 'time_limit'); assert.equal(timed.owned.state, 'ready')
const wedged = fixture(); wedged.owned.start(true); wedged.owned.stop(); wedged.fire(3000)
assert.equal(wedged.owned.state, 'failed'); assert.equal(wedged.owned.reason, 'finalize_timeout')
assert.equal(wedged.owned.clip, null)
const late = fixture(); late.owned.start(true)
const callback = late.recorder().ondataavailable; const end = late.recorder().onstop
late.owned.discard(); callback({ data: new Blob(['private']) }); end()
assert.equal(late.owned.state, 'discarded'); assert.equal(late.owned.bytes, 0)
const broken = fixture(); broken.owned.start(true); broken.recorder().onerror()
assert.equal(broken.owned.state, 'failed'); assert.equal(broken.timers.size, 0)
const empty = fixture(); empty.owned.start(true); empty.recorder().ended()
assert.equal(empty.owned.reason, 'empty_recording')
// CallMedia owns recorder lifecycle, but recording teardown never submits calls.
const media = new CallMedia(500); const stops = []
media.recording = { stop: reason => stops.push(reason), setMuted: value => stops.push(value) }
media.setMuted(true); media.close(); media.close()
assert.deepEqual(stops, [true, 'call_ended'])
assert.throws(() => media.startRecording({ consent: true }), /active call/)
const source = readFileSync(new URL('../src/goCallCoordinator.jsx', import.meta.url), 'utf8')
assert.ok(source.includes('currentRef.current !== call') && source.includes('confirm: async () => await dialogs.confirm'))
assert.ok(source.includes('recordingRef.current === owned'))
assert.ok(source.includes('discardRecordingNow()'))
assert.ok(!readFileSync(new URL('../src/callRecording.js', import.meta.url), 'utf8').match(/\b(fetch|localStorage|indexedDB|XMLHttpRequest)\s*[.(]/))
console.log('Local recording: consent, stereo graph, mute, encoder finalization, bounded storage, discard, exact cleanup and call lifetime passed')

let starts = 0, answer
const call = { phase: 'active', media: { started: true, closed: false, startRecording() { starts++; return { state: 'recording' } } } }
let current = call
const pending = requestLocalRecording(call, { isCurrent: x => x === current, confirm: () => new Promise(resolve => { answer = resolve }) })
current = { ...call } // Same properties and IDs do not imply the same call lifetime.
answer(true)
assert.equal(await pending, null); assert.equal(starts, 0)
current = call
const closedDuringPrompt = requestLocalRecording(call, { isCurrent: x => x === current, confirm: async () => { call.media.closed = true; return true } })
assert.equal(await closedDuringPrompt, null); assert.equal(starts, 0)
call.media.closed = false
assert.equal(await requestLocalRecording(call, { isCurrent: () => true, confirm: async () => false }), null)
assert.ok(await requestLocalRecording(call, { isCurrent: () => true, confirm: async () => true }))
assert.equal(starts, 1)
console.log('Mounted recording admission: delayed consent cannot cross call replacement, close or cancellation')
