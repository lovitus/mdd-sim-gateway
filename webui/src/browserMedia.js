import { api, getBasePrefix } from './api.js'

const FRAME_SAMPLES = 160
const FRAME_BYTES = 320

function wsUrl(instanceId) {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${protocol}//${window.location.host}${getBasePrefix()}/api/instances/${encodeURIComponent(instanceId)}/browser-media/ws`
}

class Downsampler {
  constructor(inputRate, outputRate = 8000) {
    this.ratio = inputRate / outputRate
    this.buffer = new Float32Array(0)
    this.position = 0
    this.packet = []
  }

  push(samples) {
    const joined = new Float32Array(this.buffer.length + samples.length)
    joined.set(this.buffer)
    joined.set(samples, this.buffer.length)
    this.buffer = joined
    const frames = []
    while (this.position + 1 < this.buffer.length) {
      const left = Math.floor(this.position)
      const fraction = this.position - left
      this.packet.push(this.buffer[left] * (1 - fraction) + this.buffer[left + 1] * fraction)
      this.position += this.ratio
      if (this.packet.length === FRAME_SAMPLES) {
        const pcm = new ArrayBuffer(FRAME_BYTES)
        const view = new DataView(pcm)
        this.packet.forEach((sample, index) => view.setInt16(
          index * 2, Math.max(-32768, Math.min(32767, Math.round(sample * 32767))), true))
        frames.push(pcm)
        this.packet = []
      }
    }
    // Retain the last real input sample when the next output position crosses this callback's
    // boundary.  AudioWorklet normally supplies 128 samples; dropping past that boundary would
    // restart from the next block's index 0 and turn 48 kHz into about 8.16 kHz instead of 8 kHz.
    const drop = Math.min(Math.floor(this.position), Math.max(0, this.buffer.length - 1))
    if (drop) {
      this.buffer = this.buffer.slice(drop)
      this.position -= drop
    }
    return frames
  }
}

export async function verifyBrowserMedia(instanceId) {
  if (!window.isSecureContext && location.hostname !== 'localhost' && location.hostname !== '127.0.0.1')
    throw new Error('Browser audio requires HTTPS or localhost')
  if (!navigator.mediaDevices?.getUserMedia || !window.AudioWorkletNode)
    throw new Error('This browser does not support microphone AudioWorklet')

  let stream = null
  let context = null
  let source = null
  let node = null
  let socket = null
  let timer = null
  let evidenceTimer = null
  let settled = false
  let started = false
  let challenge = ''
  let stats = { capture_callbacks: 0, playback_callbacks: 0, played_frames: 0 }

  const cleanup = async () => {
    clearTimeout(timer)
    clearInterval(evidenceTimer)
    if (socket && socket.readyState < WebSocket.CLOSING) try { socket.close(1000) } catch {}
    try { source?.disconnect() } catch {}
    try { node?.disconnect() } catch {}
    for (const track of stream?.getTracks?.() || []) try { track.stop() } catch {}
    if (context && context.state !== 'closed') try { await context.close() } catch {}
  }

  try {
    // Permission and the complete audio graph are ready before the server allocates its 10-second
    // canary.  A slow permission prompt therefore cannot consume the Asterisk safety timeout.
    stream = await navigator.mediaDevices.getUserMedia({
      audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true,
        autoGainControl: true }, video: false,
    })
    const Context = window.AudioContext || window.webkitAudioContext
    context = new Context()
    await context.audioWorklet.addModule(new URL('./browserMediaWorklet.js', import.meta.url))
    await context.resume()
    source = context.createMediaStreamSource(stream)
    node = new AudioWorkletNode(context, 'mdd-pcm-duplex', {
      numberOfInputs: 1, numberOfOutputs: 1, outputChannelCount: [1],
    })
    source.connect(node)
    node.connect(context.destination)
    const downsampler = new Downsampler(context.sampleRate)

    const prepared = await api.prepareBrowserMedia(instanceId)
    if (!prepared?.session_id || !prepared?.ticket)
      throw new Error('Server did not allocate a browser media session')

    return await new Promise((resolve, reject) => {
      const finish = async (error, value = true) => {
        if (settled) return
        settled = true
        await cleanup()
        if (error) reject(error)
        else resolve(value)
      }
      socket = new WebSocket(wsUrl(instanceId))
      socket.binaryType = 'arraybuffer'
      node.port.onmessage = event => {
        if (event.data?.type === 'stats') {
          stats = {
            capture_callbacks: Number(event.data.capture_callbacks || 0),
            playback_callbacks: Number(event.data.playback_callbacks || 0),
            played_frames: Number(event.data.played_frames || 0),
          }
          return
        }
        if (event.data?.type !== 'capture' || !(event.data.samples instanceof Float32Array)) return
        for (const frame of downsampler.push(event.data.samples)) {
          if (started && socket.readyState === WebSocket.OPEN && socket.bufferedAmount < FRAME_BYTES * 4)
            socket.send(frame)
        }
      }
      socket.onopen = () => socket.send(JSON.stringify({
        type: 'browser.media.hello', version: 1,
        session_id: prepared.session_id, ticket: prepared.ticket,
      }))
      socket.onmessage = event => {
        if (event.data instanceof ArrayBuffer) {
          if (event.data.byteLength !== FRAME_BYTES) {
            void finish(new Error('Server returned an invalid PCM frame'))
            return
          }
          const view = new DataView(event.data)
          const samples = new Float32Array(FRAME_SAMPLES)
          for (let index = 0; index < samples.length; index += 1)
            samples[index] = view.getInt16(index * 2, true) / 32768
          node.port.postMessage({ type: 'play', samples }, [samples.buffer])
          return
        }
        let message
        try { message = JSON.parse(event.data) } catch {
          void finish(new Error('Server returned invalid media control data')); return
        }
        if (message.type === 'browser.media.claimed' || message.type === 'browser.media.challenge')
          challenge = message.challenge || ''
        else if (message.type === 'browser.media.started') started = true
        else if (message.type === 'browser.media.ready') void finish(null, true)
        else if (message.type === 'browser.media.error')
          void finish(new Error(message.error || 'Browser media test failed'))
      }
      socket.onerror = () => { void finish(new Error('Browser media WebSocket failed')) }
      socket.onclose = event => {
        if (!settled) void finish(new Error(event.reason || 'Browser media WebSocket closed'))
      }
      evidenceTimer = setInterval(() => {
        if (!started || !challenge || socket.readyState !== WebSocket.OPEN) return
        socket.send(JSON.stringify({
          type: 'browser.media.evidence', version: 1, challenge, ...stats,
        }))
      }, 250)
      timer = setTimeout(() => { void finish(new Error('Browser media test timed out')) }, 14000)
    })
  } catch (error) {
    await cleanup()
    throw error
  }
}

export { Downsampler, FRAME_BYTES, FRAME_SAMPLES }
