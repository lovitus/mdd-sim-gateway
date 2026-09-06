import assert from 'node:assert/strict'
import { CallMedia, Downsampler, FRAME_BYTES, normalizeDialTarget } from '../src/goCallMedia.js'

const downsampler = new Downsampler(8000)
const samples = new Float32Array(161).fill(0.5)
const frames = downsampler.push(samples)
assert.equal(frames.length, 1)
assert.equal(frames[0].byteLength, FRAME_BYTES)
assert.equal(new DataView(frames[0]).getInt16(0, true), 16384)

assert.equal(normalizeDialTarget('00 44 (800) 123-4567'), '+448001234567')
assert.equal(normalizeDialTarget('1234'), '1234')
assert.throws(() => normalizeDialTarget('12;ATH'))

let socketCloses = 0
let sourceDisconnects = 0
let nodeDisconnects = 0
let trackStops = 0
let contextCloses = 0
const media = new CallMedia(500)
media.socket = { close: () => { socketCloses += 1 } }
media.source = { disconnect: () => { sourceDisconnects += 1 } }
media.node = { disconnect: () => { nodeDisconnects += 1 } }
media.stream = { getTracks: () => [{ stop: () => { trackStops += 1 } }] }
media.context = { state: 'running', close: () => { contextCloses += 1 } }
media.close()
media.close()
assert.deepEqual({ socketCloses, sourceDisconnects, nodeDisconnects, trackStops, contextCloses },
  { socketCloses: 1, sourceDisconnects: 1, nodeDisconnects: 1, trackStops: 1, contextCloses: 1 })

console.log('Go call media behavior tests passed')
