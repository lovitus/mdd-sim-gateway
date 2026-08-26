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

// The native VoWiFi and cellular transports share the exact PCM clock, bounded send queue
// and AudioWorklet playback queue. Only their call ownership/signalling protocols differ.
export function connectPcmAudio(context, stream, { socket, started, muted = () => false, stats }) {
  const source = context.createMediaStreamSource(stream)
  const node = new AudioWorkletNode(context, 'mdd-pcm-duplex', {
    numberOfInputs: 1, numberOfOutputs: 1, outputChannelCount: [1],
  })
  let limitBytes = 0
  const setBufferLimit = (ms = 500) => {
    if (!Number.isInteger(ms) || ms < 100 || ms > 2000)
      throw new RangeError('Browser PCM buffer limit must be an integer between 100 and 2000 ms')
    const maxFrames = Math.ceil(ms / 20)
    node.port.postMessage({ type: 'configure', maxFrames })
    limitBytes = maxFrames * FRAME_BYTES
    return limitBytes
  }
  setBufferLimit()
  source.connect(node)
  node.connect(context.destination)
  const downsampler = new Downsampler(context.sampleRate)
  const silence = new ArrayBuffer(FRAME_BYTES)
  node.port.onmessage = event => {
    if (event.data?.type === 'stats') {
      stats({
        capture_callbacks: Number(event.data.capture_callbacks || 0),
        playback_callbacks: Number(event.data.playback_callbacks || 0),
        played_frames: Number(event.data.played_frames || 0),
      })
      return
    }
    if (event.data?.type !== 'capture' || !(event.data.samples instanceof Float32Array)) return
    for (const frame of downsampler.push(event.data.samples)) {
      const transport = socket()
      if (started() && transport?.readyState === WebSocket.OPEN &&
          transport.bufferedAmount + FRAME_BYTES <= limitBytes)
        transport.send(muted() ? silence.slice(0) : frame)
    }
  }
  return { source, node, setBufferLimit }
}

export function playPcmFrame(node, frame) {
  if (!(frame instanceof ArrayBuffer) || frame.byteLength !== FRAME_BYTES)
    throw new Error('Server returned an invalid PCM frame')
  const view = new DataView(frame)
  const samples = new Float32Array(FRAME_SAMPLES)
  for (let index = 0; index < samples.length; index += 1)
    samples[index] = view.getInt16(index * 2, true) / 32768
  node.port.postMessage({ type: 'play', samples }, [samples.buffer])
}

export class NativeBrowserCall {
  constructor(instanceId, destination, onEvent = () => {}, options = {}) {
    this.instanceId = String(instanceId || '')
    this.destination = String(destination || '')
    this.direction = options.direction === 'inbound' ? 'inbound' : 'outbound'
    this.backendCall = options.backendCall ? {
      id: String(options.backendCall.id ?? ''),
      source_call_id: String(options.backendCall.source_call_id || ''),
      engine_run_id: String(options.backendCall.engine_run_id || ''),
      browser_revision: Number(options.backendCall.browser_revision),
      peer: String(options.backendCall.peer || options.backendCall.number || 'Unknown'),
    } : null
    this.onEvent = onEvent
    this.socket = null
    this.stream = null
    this.context = null
    this.source = null
    this.node = null
    this.evidenceTimer = null
    this.warmupTimer = null
    this.hangupTimer = null
    this.terminationTimer = null
    this.started = false
    this.finished = false
    this.ending = false
    this.muted = false
    this.challenge = ''
    this.operationId = ''
    this.mediaEpoch = ''
    this.lastCallRevision = 0
    this.callPhase = 'allocated'
    this.answerSent = false
    this.sessionId = ''
    this.audioGestureResolve = null
    this.stats = { capture_callbacks: 0, playback_callbacks: 0, played_frames: 0 }
  }

  _emit(type, data = {}) {
    try { this.onEvent(type, data) } catch {}
  }

  start() {
    this._emit(this.direction === 'inbound' ? 'preparing' : 'mediacheck',
      this.direction === 'inbound' ? { call: this.backendCall } : { to: this.destination })
    void this._run()
    return this
  }

  _ensureContext() {
    if (!this.context) {
      const Context = window.AudioContext || window.webkitAudioContext
      if (!Context) throw new Error('This browser does not support Web Audio')
      this.context = new Context()
    }
    return this.context
  }

  enableAudioFromGesture() {
    if (this.finished || this.ending) return Promise.resolve(false)
    let resumed
    try {
      // Both create and resume are invoked synchronously in the click stack, before any await.
      resumed = this._ensureContext().resume()
    } catch (error) {
      this._fail(error)
      return Promise.reject(error)
    }
    return Promise.resolve(resumed).then(() => {
      if (this.context?.state === 'running') {
        const resolve = this.audioGestureResolve
        this.audioGestureResolve = null
        resolve?.()
        return true
      }
      return false
    }).catch(error => {
      const resolve = this.audioGestureResolve
      this.audioGestureResolve = null
      resolve?.()
      this._fail(error)
      throw error
    })
  }

  async _cleanup({ preserveTermination = false } = {}) {
    this.finished = true
    clearInterval(this.evidenceTimer)
    clearTimeout(this.warmupTimer)
    clearTimeout(this.hangupTimer)
    if (!preserveTermination) clearTimeout(this.terminationTimer)
    const gesture = this.audioGestureResolve
    this.audioGestureResolve = null
    gesture?.()
    if (this.socket && this.socket.readyState < WebSocket.CLOSING) {
      try { this.socket.close(1000) } catch {}
    }
    try { this.source?.disconnect() } catch {}
    try { this.node?.disconnect() } catch {}
    for (const track of this.stream?.getTracks?.() || []) try { track.stop() } catch {}
    if (this.context && this.context.state !== 'closed') try { await this.context.close() } catch {}
  }

  _fail(error) {
    if (this.finished || this.ending) return
    this._emit('failed', {
      cause: error?.message || String(error || 'Browser call failed'),
      category: error?.mddCategory || 'audio-failed',
      status: Number(error?.status || 0), detail: error?.data?.detail,
    })
    void this._cleanup()
  }

  _identity(message) {
    return message?.operation_id === this.operationId && message?.media_epoch === this.mediaEpoch
  }

  _armTerminationWatchdog() {
    if (this.direction !== 'inbound' || this.terminationTimer) return
    this.terminationTimer = setTimeout(() => {
      this._emit('termination-unconfirmed', { call: this.backendCall })
      void this._cleanup()
    }, 10000)
  }

  _handleCallPhase(message) {
    if (!this._identity(message)) {
      this._fail(new Error('Browser call identity changed'))
      return
    }
    const revision = Number(message.revision)
    if (!Number.isSafeInteger(revision) || revision <= 0) {
      this._fail(new Error('Browser call revision is invalid'))
      return
    }
    if (revision <= this.lastCallRevision) return
    this.lastCallRevision = revision
    this.callPhase = String(message.phase || '')
    if (this.direction === 'inbound') {
      if (['ready', 'claiming', 'attach_submitted_unknown',
        'answer_submitted_unknown', 'active'].includes(message.phase))
        clearTimeout(this.warmupTimer)
      if (message.phase === 'ready') this._emit('ready', { call: this.backendCall })
      else if (['claiming', 'attach_submitted_unknown', 'answer_submitted_unknown'].includes(message.phase))
        this._emit('answering', { phase: message.phase })
      else if (message.phase === 'active') this._emit('active', { call: this.backendCall })
      else if (message.phase === 'answered_elsewhere') {
        this._emit('answered-elsewhere', { call: this.backendCall })
        void this._cleanup()
      } else if (message.phase === 'ending') {
        this.ending = true
        clearTimeout(this.warmupTimer)
        this._armTerminationWatchdog()
        this._emit('ending', { call: this.backendCall })
      }
      else if (message.phase === 'terminal') {
        this._emit('ended', { cause: message.disposition || 'Ended' })
        void this._cleanup()
      }
      return
    }
    if (message.phase === 'calling') {
      clearTimeout(this.warmupTimer)
      this._emit('calling', { to: this.destination })
    } else if (message.phase === 'active') {
      clearTimeout(this.warmupTimer)
      this._emit('active', { to: this.destination })
    } else if (message.phase === 'terminal') {
      this._emit('ended', { cause: message.disposition || 'Ended' })
      void this._cleanup()
    }
  }

  async _stopIfEnding() {
    if (!this.ending && !this.finished) return false
    await this._cleanup()
    return true
  }

  async _run() {
    try {
      if (!window.isSecureContext && location.hostname !== 'localhost' && location.hostname !== '127.0.0.1')
        throw new Error('Browser audio requires HTTPS or localhost')
      if (!navigator.mediaDevices?.getUserMedia || !window.AudioWorkletNode)
        throw new Error('This browser does not support microphone AudioWorklet')
      this._ensureContext()
      this.stream = await navigator.mediaDevices.getUserMedia({
        audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true,
          autoGainControl: true }, video: false,
      })
      if (await this._stopIfEnding()) return
      await this.context.audioWorklet.addModule(new URL('./browserMediaWorklet.js', import.meta.url))
      if (await this._stopIfEnding()) return
      try { await this.context.resume() } catch {}
      if (this.context.state !== 'running') {
        this._emit('needs-user-gesture', { call: this.backendCall })
        await new Promise(resolve => { this.audioGestureResolve = resolve })
        if (this.context.state !== 'running')
          throw new Error('Browser audio still requires a user gesture')
      }
      if (await this._stopIfEnding()) return
      Object.assign(this, connectPcmAudio(this.context, this.stream, {
        socket: () => this.socket, started: () => this.started, muted: () => this.muted,
        stats: value => { this.stats = value },
      }))
      let prepared
      try {
        prepared = this.direction === 'inbound'
          ? await api.prepareBrowserIncoming(
            this.instanceId, this.backendCall?.id, this.backendCall?.source_call_id,
            this.backendCall?.engine_run_id)
          : await api.prepareBrowserOutbound(this.instanceId, this.destination)
      } catch (error) {
        if (this.direction === 'inbound') {
          const code = String(error?.data?.detail?.code || '')
          if (error?.status === 404) error.mddCategory = 'terminal'
          else if (code === 'answered_elsewhere') error.mddCategory = 'answered-elsewhere'
          else if (code === 'incoming_owner_unavailable') error.mddCategory = 'owner-unavailable'
          else if (code === 'incoming_owner_conflict') error.mddCategory = 'ending'
          else if (error?.status === 503) error.mddCategory = 'capacity'
          else if (error?.status === 409) error.mddCategory = 'owner-unavailable'
        }
        throw error
      }
      if (await this._stopIfEnding()) return
      if (!prepared?.session_id || !prepared?.ticket || !prepared?.operation_id ||
          !prepared?.media_epoch || prepared?.purpose !== this.direction)
        throw new Error(`Server did not allocate an ${this.direction} browser media session`)
      if (this.direction === 'inbound') {
        const call = prepared.call || {}
        if (String(prepared.backend_call_id ?? '') !== this.backendCall?.id ||
            Number(prepared.backend_revision) !== this.backendCall?.browser_revision ||
            String(call.id ?? '') !== this.backendCall?.id ||
            String(call.source_call_id || '') !== this.backendCall?.source_call_id ||
            String(call.engine_run_id || '') !== this.backendCall?.engine_run_id ||
            String(call.browser_state || '') !== 'ringing' ||
            Number(call.browser_revision) !== this.backendCall?.browser_revision) {
          const error = new Error('Incoming call identity changed during media preparation')
          error.mddCategory = 'owner-unavailable'
          throw error
        }
      }
      this.sessionId = String(prepared.session_id)
      this.operationId = prepared.operation_id
      this.mediaEpoch = prepared.media_epoch
      this.setBufferLimit(prepared.buffer_limit_ms)
      this.socket = new WebSocket(wsUrl(this.instanceId))
      this.socket.binaryType = 'arraybuffer'
      this.socket.onopen = () => this.socket.send(JSON.stringify({
        type: 'browser.media.hello', version: 1,
        session_id: prepared.session_id, ticket: prepared.ticket,
      }))
      this.socket.onmessage = event => {
        if (event.data instanceof ArrayBuffer) {
          try { playPcmFrame(this.node, event.data) } catch (error) { this._fail(error) }
          return
        }
        let message
        try { message = JSON.parse(event.data) } catch {
          this._fail(new Error('Server returned invalid media control data')); return
        }
        if (message.type === 'browser.media.claimed' || message.type === 'browser.media.challenge')
          this.challenge = message.challenge || ''
        else if (message.type === 'browser.media.started') this.started = true
        else if (message.type === 'browser.media.ready' && this.direction === 'inbound') {
          clearTimeout(this.warmupTimer)
          this._emit('media-ready', { call: this.backendCall })
        }
        else if (message.type === 'browser.call.phase') this._handleCallPhase(message)
        else if (message.type === 'browser.media.error')
          this._fail(new Error(message.error || 'Browser media transport failed'))
      }
      this.socket.onerror = () => this._fail(new Error('Browser media WebSocket failed'))
      this.socket.onclose = event => {
        if (!this.finished && !this.ending)
          this._fail(new Error(event.reason || 'Browser media WebSocket closed'))
        else if (!this.finished && this.ending)
          void this._cleanup({ preserveTermination: true })
      }
      this.evidenceTimer = setInterval(() => {
        if (!this.started || !this.challenge || this.socket?.readyState !== WebSocket.OPEN) return
        this.socket.send(JSON.stringify({
          type: 'browser.media.evidence', version: 1,
          challenge: this.challenge, ...this.stats,
        }))
      }, 250)
      this.warmupTimer = setTimeout(() => {
        this._fail(new Error('Browser call media warmup timed out'))
      }, 15000)
    } catch (error) {
      this._fail(error)
    }
  }

  hangup() {
    if (this.finished || this.ending) return false
    this.ending = true
    try {
      if (this.socket?.readyState === WebSocket.OPEN) this.socket.send(JSON.stringify({
        type: 'browser.call.hangup', version: 1,
        operation_id: this.operationId, media_epoch: this.mediaEpoch,
      }))
    } catch {}
    finally {
      // Closing WSS is itself a server-side hangup signal; timers are armed even when send throws.
      this.hangupTimer = setTimeout(() => {
        void this._cleanup({ preserveTermination: this.direction === 'inbound' })
      }, 500)
      this._armTerminationWatchdog()
    }
    return true
  }

  answer() {
    if (this.direction !== 'inbound' || this.finished || this.ending || this.answerSent ||
        this.callPhase !== 'ready' || this.socket?.readyState !== WebSocket.OPEN) return false
    this.answerSent = true
    try {
      this.socket.send(JSON.stringify({
        type: 'browser.call.answer', version: 1,
        operation_id: this.operationId, media_epoch: this.mediaEpoch,
      }))
      return true
    } catch (error) {
      this.answerSent = false
      this._fail(error)
      return false
    }
  }

  closeLocal() {
    if (this.finished) return false
    this.ending = true
    void this._cleanup()
    return true
  }

  matchesBackendCall(call) {
    return Boolean(this.backendCall &&
      String(call?.id ?? '') === this.backendCall.id &&
      String(call?.source_call_id || '') === this.backendCall.source_call_id &&
      String(call?.engine_run_id || '') === this.backendCall.engine_run_id)
  }

  ownsBackendCall(call) {
    if (!this.matchesBackendCall(call)) return false
    const state = String(call?.browser_state || '')
    if (state === 'ringing') return true
    return Boolean(this.sessionId &&
      String(call?.browser_owner_session || '') === this.sessionId &&
      String(call?.browser_operation || '') === this.operationId &&
      String(call?.browser_epoch || '') === this.mediaEpoch)
  }

  sendDTMF(digit) {
    const phaseAllowed = this.direction === 'inbound'
      ? this.callPhase === 'active' : ['calling', 'active'].includes(this.callPhase)
    if (this.finished || !phaseAllowed || !/^[0-9*#]$/.test(String(digit || '')) ||
        this.socket?.readyState !== WebSocket.OPEN) return false
    this.socket.send(JSON.stringify({
      type: 'browser.call.dtmf', version: 1, digit,
      operation_id: this.operationId, media_epoch: this.mediaEpoch,
    }))
    return true
  }

  setMuted(muted) {
    this.muted = Boolean(muted)
    return true
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
    const pcm = connectPcmAudio(context, stream, {
      socket: () => socket, started: () => started, stats: value => { stats = value },
    })
    source = pcm.source
    node = pcm.node

    const prepared = await api.prepareBrowserMedia(instanceId)
    if (!prepared?.session_id || !prepared?.ticket)
      throw new Error('Server did not allocate a browser media session')
    pcm.setBufferLimit(prepared.buffer_limit_ms)

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
      socket.onopen = () => socket.send(JSON.stringify({
        type: 'browser.media.hello', version: 1,
        session_id: prepared.session_id, ticket: prepared.ticket,
      }))
      socket.onmessage = event => {
        if (event.data instanceof ArrayBuffer) {
          try { playPcmFrame(node, event.data) } catch (error) { void finish(error) }
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
