import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

globalThis.window = { location: { protocol: 'https:', host: 'gateway.test', pathname: '/mdd/' } }
globalThis.location = { hostname: 'gateway.test' }

const { Downsampler, FRAME_BYTES, FRAME_SAMPLES } = await import('../src/browserMedia.js')
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
assert.ok(client.includes('socket.bufferedAmount < FRAME_BYTES * 4'))
assert.ok(client.includes("event.data.byteLength !== FRAME_BYTES"))
assert.ok(worklet.includes("registerProcessor('mdd-pcm-duplex'"))
assert.ok(worklet.includes('if (this.playQueue.length > 12)'))

console.log('Browser WSS PCM tests passed')
