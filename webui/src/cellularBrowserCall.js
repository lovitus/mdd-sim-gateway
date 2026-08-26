import { api as defaultApi, getBasePrefix } from './api.js'
import { connectPcmAudio, playPcmFrame, FRAME_BYTES } from './browserMedia.js'
import { boundedCellularRelease } from './cellularMediaMonitor.js'

export function cellularMediaUrl(instanceId, callId) {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${protocol}//${window.location.host}${getBasePrefix()}/api/instances/${encodeURIComponent(instanceId)}/cellular-call/${encodeURIComponent(callId)}/ws`
}

// One object/owner per user action, never a persisted tab-wide token. It owns only its exact
// preparation; closing a losing tab must never invoke the line-wide physical Hangup endpoint.
export class CellularBrowserCall {
  constructor(instanceId, destination, onEvent = () => {}, options = {}) {
    this.instanceId = String(instanceId)
    this.destination = String(destination || '')
    this.direction = options.direction === 'inbound' ? 'inbound' : 'outbound'
    this.sourceCallId = options.sourceCallId
    this.api = options.api || defaultApi
    this.onEvent = onEvent
    this.ownerToken = ''
    this.callId = ''
    this.socket = null
    this.context = null
    this.stream = null
    this.source = null
    this.node = null
    this.started = false
    this.finished = false
    this.ending = false
    this.muted = false
    this.commitRequested = false
    this.committed = false
    this.challenge = ''
    this.stats = { capture_callbacks: 0, playback_callbacks: 0, played_frames: 0 }
    this.releasePromise = null
    this.releaseReason = ''
    this.pollFailures = 0
    this.pollInFlight = false
    this.terminationDeadline = 0
    this.evidenceTimer = null
    this.warmupTimer = null
    this.pollTimer = null
    this.terminationTimer = null
  }

  _emit(type, data = {}) {
    try { this.onEvent(type, data) } catch {}
  }

  start() {
    if (this.ownerToken || this.finished) return this
    this._emit('checking')
    try {
      if (!window.isSecureContext && !['localhost', '127.0.0.1'].includes(location.hostname))
        throw new Error('Browser audio requires HTTPS or localhost')
      if (!navigator.mediaDevices?.getUserMedia || !window.AudioWorkletNode)
        throw new Error('This browser does not support microphone AudioWorklet')
      this.ownerToken = Array.from(crypto.getRandomValues(new Uint8Array(32)),
        byte => byte.toString(16).padStart(2, '0')).join('')
      const Context = window.AudioContext || window.webkitAudioContext
      this.context = new Context()
      // Resume synchronously inside the user's Place/Answer gesture.
      const resumed = this.context.resume()
      void this._run(resumed)
    } catch (error) { void this._fail(error) }
    return this
  }

  async _closeAudio() {
    clearInterval(this.evidenceTimer)
    clearTimeout(this.warmupTimer)
    if (this.socket && this.socket.readyState < WebSocket.CLOSING)
      try { this.socket.close(1000) } catch {}
    try { this.source?.disconnect() } catch {}
    try { this.node?.disconnect() } catch {}
    for (const track of this.stream?.getTracks?.() || []) try { track.stop() } catch {}
    if (this.context && this.context.state !== 'closed') try { await this.context.close() } catch {}
  }

  async _run(resumed) {
    try {
      await resumed
      this.stream = await navigator.mediaDevices.getUserMedia({
        audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true,
          autoGainControl: true }, video: false,
      })
      if (this.ending || this.finished) { await this._closeAudio(); return }
      await this.context.audioWorklet.addModule(new URL('./browserMediaWorklet.js', import.meta.url))
      if (this.ending || this.finished) { await this._closeAudio(); return }
      if (this.context.state !== 'running') throw new Error('Browser audio requires a user gesture')
      Object.assign(this, connectPcmAudio(this.context, this.stream, {
        socket: () => this.socket, started: () => this.started, muted: () => this.muted,
        stats: value => { this.stats = value },
      }))
      const prepared = this.direction === 'inbound'
        ? await this.api.prepareIncomingCellularCall(this.instanceId, this.sourceCallId, this.ownerToken)
        : await this.api.prepareCellularCall(this.instanceId, this.destination, this.ownerToken)
      // A cancelled prepare may finish late. Retain the identity solely to release this owner.
      this.callId = String(prepared?.call_id || '')
      if (this.ending || this.finished) { await this._releaseOwner(); return }
      if (!this.callId || prepared.owner_token !== this.ownerToken ||
          prepared.audio?.transport !== 'same-origin-wss-pcm-v1' ||
          prepared.audio?.frame_bytes !== FRAME_BYTES)
        throw new Error('Server did not allocate an owned cellular PCM session')
      this.mediaBufferLimitBytes = this.setBufferLimit(prepared.audio.buffer_limit_ms)
      this._emit('prepared', { callId: this.callId })
      this.socket = new WebSocket(cellularMediaUrl(this.instanceId, this.callId))
      this.socket.binaryType = 'arraybuffer'
      this.socket.onopen = () => {
        if (this.ending || this.finished) return
        this.socket.send(JSON.stringify({ type: 'cellular.media.hello', version: 1,
          owner_token: this.ownerToken }))
      }
      this.socket.onmessage = event => this._message(event)
      this.socket.onerror = () => { void this._fail(new Error('Cellular media WebSocket failed')) }
      this.socket.onclose = event => {
        if (!this.ending && !this.finished)
          void this._fail(new Error(event.reason || 'Cellular media WebSocket closed'))
      }
      this.evidenceTimer = setInterval(() => {
        if (this.started && this.challenge && this.socket?.readyState === WebSocket.OPEN) {
          const evidence = JSON.stringify({ type: 'cellular.media.evidence', version: 1,
            challenge: this.challenge, ...this.stats })
          // Reserve bounded control headroom: allowed audio backlog must not starve
          // the liveness evidence and turn recoverable congestion into a hangup.
          if (this.socket.bufferedAmount + evidence.length <= this.mediaBufferLimitBytes + FRAME_BYTES * 4)
            this.socket.send(evidence)
        }
      }, 250)
      this.warmupTimer = setTimeout(() => {
        void this._fail(new Error('Cellular media warmup timed out'))
      }, 15000)
    } catch (error) { void this._fail(error) }
  }

  _message(event) {
    if (this.ending || this.finished) return
    try {
      if (event.data instanceof ArrayBuffer) { playPcmFrame(this.node, event.data); return }
      const message = JSON.parse(event.data)
      if (message.call_id !== this.callId || message.version !== 1)
        throw new Error('Cellular media identity changed')
      if (message.type === 'cellular.media.started') {
        if (message.frame_bytes !== FRAME_BYTES) throw new Error('Invalid cellular PCM format')
        this.started = true
        this.challenge = String(message.challenge || '')
      } else if (message.type === 'cellular.media.challenge') {
        this.challenge = String(message.challenge || '')
      } else if (message.type === 'cellular.media.ready' || message.type === 'cellular.media.status') {
        this._emit('media', message.media || {})
        if (message.media?.ready && !this.commitRequested) void this._commit()
        // A transient degraded sample is not a disconnect. The server/Agent lease owns
        // the bounded recovery window; ready must never submit this call a second time.
      } else if (message.type === 'cellular.media.error') {
        throw new Error(message.error || 'Cellular media failed')
      }
    } catch (error) { void this._fail(error) }
  }

  async _commit() {
    if (this.commitRequested || this.ending || this.finished) return
    this.commitRequested = true
    clearTimeout(this.warmupTimer)
    this._emit('answering')
    try {
      const result = await (this.direction === 'inbound'
        ? this.api.answerIncomingCellularCall(this.instanceId, this.callId, this.ownerToken)
        : this.api.commitCellularCall(this.instanceId, this.callId, this.ownerToken))
      if (this.ending || this.finished) return
      if (!result.ok && !result.uncertain) throw new Error(result.error || 'Cellular call failed')
      this.committed = true
      this._emit('calling', { uncertain: result.uncertain === true })
      this._poll()
    } catch (error) { void this._fail(error) }
  }

  _releaseOwner() {
    if (!this.callId) return Promise.resolve({ missing: true })
    if (!this.releasePromise) this.releasePromise = boundedCellularRelease({
      callId: this.callId,
      release: callId => this.api.releaseCellularCall(this.instanceId, callId, this.ownerToken, this.releaseReason || 'unknown'),
      delay: milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds)),
    }).catch(error => { this.releasePromise = null; throw error })
    return this.releasePromise
  }

  async _fail(error) {
    if (this.ending || this.finished) return
    const submitted = this.commitRequested
    this._emit('failed', { cause: error?.message || String(error),
      status: Number(error?.status || 0), committed: submitted })
    await this.hangup('media_error')
  }

  async hangup(reason = 'user') {
    if (this.finished) return { missing: true }
    if (!this.releaseReason) this.releaseReason = reason
    this.ending = true
    this._emit('ending', { committed: this.commitRequested })
    void this._closeAudio()
    if (this.commitRequested && (!this.terminationDeadline || Date.now() >= this.terminationDeadline)) {
      clearTimeout(this.terminationTimer)
      this.terminationDeadline = Date.now() + 10000
      this.terminationTimer = setTimeout(() => {
        clearTimeout(this.pollTimer)
        this._emit('termination-unconfirmed')
      }, 10000)
    }
    try {
      const result = await this._releaseOwner()
      if (!this.commitRequested || result?.released || result?.missing) this._finish()
      else {
        this._poll()
      }
      return result
    } catch (error) {
      this._emit('termination-unconfirmed', { cause: error.message })
      return { error: error.message }
    }
  }

  async _poll() {
    clearTimeout(this.pollTimer)
    if (this.finished || this.pollInFlight ||
        (this.ending && this.terminationDeadline && Date.now() >= this.terminationDeadline)) return
    this.pollInFlight = true
    try {
      const result = await this.api.cellularCallStatus(this.instanceId)
      if (this.finished) return
      if (['idle', 'terminated', 'ended', 'failed'].includes(result.status) &&
          (result.terminal_confirmed || Number(result.terminal_samples || 0) >= 2)) {
        this._finish(result.call?.reason || 'Ended'); return
      }
      if (!this.ending) {
        if (result.unavailable || result.status === 'failed')
          throw new Error(result.error || 'Cellular modem is unavailable')
        if (result.status === 'active') this._emit('active')
        else if (['dialing', 'ringing-out'].includes(result.status)) this._emit('ringing')
      }
      this.pollFailures = 0
    } catch (error) {
      this.pollFailures += 1
      if (!this.ending && [401, 403].includes(Number(error?.status))) {
        void this._fail(error)
        return
      }
      if (this.pollFailures >= 3 && this.ending) {
        this._emit('termination-unconfirmed', { cause: error.message })
        return
      }
      // A management GET failure is not loss of the live audio WebSocket. Keep the
      // ordinary status poll; media ownership/lease decides when a call must stop.
      if (this.pollFailures === 3)
        this._emit('status-unavailable', { cause: error.message })
    } finally { this.pollInFlight = false }
    if (!this.finished && (!this.ending || !this.terminationDeadline || Date.now() < this.terminationDeadline))
      this.pollTimer = setTimeout(() => this._poll(), 2000)
  }

  _finish(cause = 'Ended') {
    if (this.finished) return
    this.finished = true
    clearTimeout(this.pollTimer)
    clearTimeout(this.terminationTimer)
    void this._closeAudio()
    this._emit('ended', { cause })
  }

  closeLocal() { return this.hangup('page_closed') }
  setMuted(muted) { this.muted = Boolean(muted); return true }
  sendDTMF(digit) {
    if (!this.committed || this.ending || this.finished || !/^[0-9*#]$/.test(digit)) return false
    return this.api.cellularCallDtmf(this.instanceId, digit)
  }
}
