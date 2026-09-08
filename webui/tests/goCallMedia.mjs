import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
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

const globals=['navigator','AudioContext','AudioWorkletNode','isSecureContext']
const descriptors=new Map(globals.map(key=>[key,Object.getOwnPropertyDescriptor(globalThis,key)]))
let resolveMicrophone
let lateTrackStops=0
const stream={getTracks:()=>[{stop:()=>{lateTrackStops++}}]}
class AudioContextFixture {
  constructor(){this.state='running'}
  resume(){return Promise.resolve()}
  close(){this.state='closed';return Promise.resolve()}
}
try {
  Object.defineProperty(globalThis,'navigator',{configurable:true,value:{mediaDevices:{getUserMedia:()=>new Promise(resolve=>{resolveMicrophone=resolve})}}})
  Object.defineProperty(globalThis,'AudioContext',{configurable:true,value:AudioContextFixture})
  Object.defineProperty(globalThis,'AudioWorkletNode',{configurable:true,value:class {}})
  Object.defineProperty(globalThis,'isSecureContext',{configurable:true,value:true})
  const cancelled=new CallMedia(500)
  const pending=cancelled.openAudioFromGesture()
  cancelled.close()
  await assert.rejects(pending,/cancelled/)
  assert.equal(lateTrackStops,0)
  resolveMicrophone(stream)
  await Promise.resolve()
  assert.equal(lateTrackStops,1,'permission granted after cancellation must release the microphone')
  const ready=new CallMedia(500)
  const acquisition=ready.openAudioFromGesture()
  resolveMicrophone(stream)
  await acquisition
  ready.close()
  assert.equal(lateTrackStops,2,'acquired microphone must be owned before media lease preparation')
  navigator.mediaDevices.getUserMedia=()=>Promise.reject(new Error('Requested device not found'))
  const unavailable=new CallMedia(500)
  await assert.rejects(unavailable.openAudioFromGesture(),/Requested device not found/)
  unavailable.close()
  assert.equal(unavailable.context.state,'closed')
  const preparing=new CallMedia(500)
  const readiness=new Promise((_,reject)=>{preparing.readyReject=reject})
  preparing.close()
  await assert.rejects(readiness,/cancelled/)
} finally {
  for(const key of globals){const descriptor=descriptors.get(key);if(descriptor)Object.defineProperty(globalThis,key,descriptor);else delete globalThis[key]}
}
const coordinator=readFileSync(new URL('../src/goCallCoordinator.jsx',import.meta.url),'utf8')
assert.equal((coordinator.match(/await media\.openAudioFromGesture\(\)/g)||[]).length,2,'call and canary must await the microphone before creating a lease')
console.log('Go call media behavior tests passed')
