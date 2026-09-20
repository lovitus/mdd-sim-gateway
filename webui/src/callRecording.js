// One opt-in, browser-local recording. No upload, persistent browser storage,
// backend call, or ownership of the microphone/playback graph is introduced.
export const RECORDING_LIMIT_BYTES = 32 * 1024 * 1024
export const RECORDING_LIMIT_MS = 30 * 60 * 1000
const FINALIZE_MS = 3000
const formats = ['audio/webm;codecs=opus', 'audio/mp4', 'audio/ogg;codecs=opus']

export function recordingSupported(Recorder = globalThis.MediaRecorder) {
  return typeof Recorder === 'function'
}

export class LocalCallRecording {
  constructor({ context, microphone, playback, muted = false, onChange = () => {},
    Recorder = globalThis.MediaRecorder, maxBytes = RECORDING_LIMIT_BYTES, maxMS = RECORDING_LIMIT_MS,
    now = () => Date.now(), setTimer = (fn, ms) => globalThis.setTimeout(fn, ms),
    clearTimer = id => globalThis.clearTimeout(id) }) {
    if (!Number.isSafeInteger(maxBytes) || maxBytes <= 0 || maxBytes > RECORDING_LIMIT_BYTES ||
        !Number.isSafeInteger(maxMS) || maxMS <= 0 || maxMS > RECORDING_LIMIT_MS)
      throw new RangeError('Invalid local recording limit')
    Object.assign(this, { context, microphone, playback, muted, onChange, Recorder, maxBytes, maxMS, now, setTimer, clearTimer })
    this.state = 'idle'
    this.chunks = []
    this.bytes = 0
    this.clip = null
    this.reason = ''
    this.startedAt = 0
    this.stoppedAt = 0
    this.urls = new Map()
  }

  snapshot() {
    return { state: this.state, bytes: this.bytes, reason: this.reason,
      durationMS: this.startedAt ? Math.max(0, (this.stoppedAt || this.now()) - this.startedAt) : 0 }
  }
  notify() { try { this.onChange(this.snapshot()) } catch { /* UI cannot terminate a call. */ } }

  start(consent) {
    if (consent !== true) throw new Error('Explicit recording consent is required')
    if (this.state !== 'idle') throw new Error('This recording has already been used')
    if (!recordingSupported(this.Recorder) || this.context?.state !== 'running' || !this.microphone || !this.playback)
      throw new Error('Local recording is unavailable')
    try {
      // Record two channels, not two competing call owners. Mute removes the
      // local microphone from the recording too. Never monitor it in speakers.
      this.destination = this.context.createMediaStreamDestination()
      this.merger = this.context.createChannelMerger(2)
      this.micGain = this.context.createGain()
      this.remoteGain = this.context.createGain()
      this.micGain.channelCount = this.remoteGain.channelCount = 1
      this.micGain.channelCountMode = this.remoteGain.channelCountMode = 'explicit'
      this.micGain.gain.value = this.muted ? 0 : 1
      this.microphone.connect(this.micGain)
      this.playback.connect(this.remoteGain)
      this.micGain.connect(this.merger, 0, 0)
      this.remoteGain.connect(this.merger, 0, 1)
      this.merger.connect(this.destination)
      const mimeType = formats.find(type => this.Recorder.isTypeSupported?.(type))
      this.recorder = new this.Recorder(this.destination.stream, mimeType ? { mimeType } : undefined)
      this.recorder.ondataavailable = event => {
        if (!['recording', 'finalizing'].includes(this.state) || !event.data?.size) return
        if (!(event.data instanceof Blob) || this.bytes + event.data.size > this.maxBytes) {
          // An arbitrary truncated codec container is not a successful recording.
          this.fail('size_limit')
          return
        }
        this.bytes += event.data.size
        this.chunks.push(event.data)
        this.notify()
      }
      this.recorder.onerror = () => this.fail('encoding_failed')
      this.recorder.onstop = () => this.finish()
      this.state = 'recording'
      this.startedAt = this.now()
      this.recorder.start(1000) // Drain encoded data regularly rather than at hangup only.
      this.limitTimer = this.setTimer(() => this.stop('time_limit'), this.maxMS)
      this.notify()
      return this
    } catch (error) {
      this.fail('start_failed')
      throw error
    }
  }

  setMuted(value) {
    this.muted = Boolean(value)
    if (this.micGain) this.micGain.gain.value = this.muted ? 0 : 1
  }

  detachInputs() {
    // Disconnect only our own edges. disconnect() on the live source would
    // silently cut the carrier's microphone/playback path.
    try { if (this.micGain) this.microphone.disconnect(this.micGain) } catch {}
    try { if (this.remoteGain) this.playback.disconnect(this.remoteGain) } catch {}
  }

  stop(reason = 'manual') {
    if (this.state !== 'recording') return
    this.state = 'finalizing'
    this.reason = reason
    this.stoppedAt = this.now()
    this.clearTimer(this.limitTimer)
    this.detachInputs()
    this.finalTimer = this.setTimer(() => this.fail('finalize_timeout'), FINALIZE_MS)
    this.notify()
    try { this.recorder.stop() } catch { this.fail('encoding_failed') }
  }

  finish() {
    if (!['recording', 'finalizing'].includes(this.state)) return
    if (!this.chunks.length) { this.fail('empty_recording'); return }
    this.stoppedAt ||= this.now()
    this.reason ||= 'media_ended'
    this.clip = new Blob(this.chunks, { type: this.recorder.mimeType || this.chunks[0].type || 'application/octet-stream' })
    this.chunks = []
    this.releaseResources()
    this.state = 'ready'
    this.notify()
  }

  fail(reason) {
    if (['discarded', 'ready', 'failed'].includes(this.state)) return
    this.reason = reason
    this.state = 'failed'
    this.stoppedAt ||= this.now()
    this.chunks = []
    this.clip = null
    this.bytes = 0
    this.releaseResources()
    this.notify()
  }

  releaseResources() {
    this.clearTimer(this.limitTimer)
    this.clearTimer(this.finalTimer)
    this.detachInputs()
    if (this.recorder) {
      this.recorder.ondataavailable = this.recorder.onstop = this.recorder.onerror = null
      try { if (this.recorder.state !== 'inactive') this.recorder.stop() } catch {}
    }
    for (const node of [this.micGain, this.remoteGain, this.merger]) try { node?.disconnect() } catch {}
    // These are synthetic recording tracks, never the microphone's tracks.
    for (const track of this.destination?.stream.getTracks() || []) try { track.stop() } catch {}
    this.destination = this.merger = this.micGain = this.remoteGain = null
    this.microphone = this.playback = this.context = this.recorder = null
  }

  save({ document = globalThis.document, URL = globalThis.URL } = {}) {
    if (this.state !== 'ready' || !this.clip) throw new Error('No completed local recording to save')
    const extension = this.clip.type.includes('mp4') ? 'm4a' : this.clip.type.includes('ogg') ? 'ogg' : this.clip.type.includes('webm') ? 'webm' : 'bin'
    // No phone number, SIM identity, bearer token or call ID in the filename.
    const filename = `mdd-recording-${new Date(this.startedAt).toISOString().replace(/[:.]/g, '-')}.${extension}`
    const url = URL.createObjectURL(this.clip)
    let timer
    const revoke = () => { URL.revokeObjectURL(url); this.urls.delete(url) }
    try {
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = filename
      anchor.hidden = true
      document.body.appendChild(anchor)
      try { anchor.click() } finally { anchor.remove() }
      timer = this.setTimer(revoke, 1000)
      this.urls.set(url, { revoke, timer })
    } catch (error) { revoke(); throw error }
    return filename
  }

  discard() {
    if (this.state === 'discarded') return
    this.state = 'discarded' // Ignore late encoder callbacks after logout/unmount.
    this.releaseResources()
    for (const entry of this.urls.values()) { this.clearTimer(entry.timer); entry.revoke() }
    this.urls.clear()
    this.chunks = []
    this.clip = null
    this.bytes = 0
    this.notify()
  }
}

// Shared by the mounted owner and deterministic dialog-race tests. The owner
// supplies identity/admission, not a string call ID that a later call can reuse.
export async function requestLocalRecording(call, { isCurrent, confirm, onChange }) {
  const admitted = () => isCurrent(call) && call?.phase === 'active' && !call.ending &&
    !call.cancelled && !call.media?.closed && call.media?.started === true
  if (!admitted() || await confirm() !== true || !admitted()) return null
  return call.media.startRecording({ consent: true, onChange })
}
