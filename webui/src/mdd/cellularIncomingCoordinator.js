import { api as defaultApi } from './api.js'
import { CellularBrowserCall } from './cellularBrowserCall.js'

const TERMINAL_STATUSES = new Set(['ended', 'terminated', 'idle', 'failed'])
const sourceKey = (instanceId, item) => `${instanceId}:${item?.id || item?.call_id || ''}`

export class CellularIncomingController {
  constructor(options = {}) {
    this.options = { api: defaultApi, ...options }
    this.state = null
    this.epoch = 0
    this.mediaPhone = null
    this.clearTimer = null
    this.terminationTimer = null
    this.terminalSources = new Map()
  }

  updateOptions(options = {}) { this.options = { ...this.options, ...options } }
  _emit(state) {
    this.state = state
    this.options.onStateChange?.(state ? { ...state } : null)
  }
  _patch(patch) { if (this.state) this._emit({ ...this.state, ...patch }) }
  _t(key) { return this.options.t ? this.options.t(key) : key }
  _toast(message) { this.options.showToast?.(message) }
  _rememberTerminalSource(key) {
    const now = this.options.now?.() || Date.now()
    for (const [source, expiry] of this.terminalSources) if (expiry <= now) this.terminalSources.delete(source)
    this.terminalSources.set(key, now + (this.options.terminalSourceTtlMs || 30000))
    while (this.terminalSources.size > 64) this.terminalSources.delete(this.terminalSources.keys().next().value)
  }

  handleMessage(message) {
    const item = message?.call
    if (message?.type !== 'call' || item?.transport !== 'cellular') return
    const instanceId = String(message.instance || item.instance_id || '')
    const key = sourceKey(instanceId, item)
    if (!instanceId || !(item.id || item.call_id)) return
    if (item.direction === 'in' && item.status === 'ringing') {
      const now = this.options.now?.() || Date.now()
      if ((this.terminalSources.get(key) || 0) > now) return
      if (this.state?.sourceKey === key) {
        if (this.state.state !== 'ended') this._patch({ peer: item.peer || item.from || this.state.peer })
        return
      }
      if (this.state && this.state.state !== 'ended') return
      this.stop()
      this._emit({ transport: 'cellular', state: 'incoming', phase: 'ringing',
        instanceId, sourceKey: key, sourceCallId: item.id || item.call_id,
        peer: item.peer || item.from || item.number || 'Unknown',
        mediaReady: false, busy: false, startedAt: 0 })
      // Every tab shows ringing, but only the user clicking Answer opens audio and claims it.
      return
    }
    if (this.state?.sourceKey !== key) return
    if (item.status === 'answered') {
      if (this.mediaPhone?.callId && item.cellular_owner_call_id === this.mediaPhone.callId) {
        this._patch({ state: 'active', busy: false, startedAt: this.state.startedAt || Date.now() })
      } else this._endSoon('Answered elsewhere')
    } else if (item.end_ts || TERMINAL_STATUSES.has(item.status)) this._endSoon(item.status)
  }

  answer() {
    if (!this.state || this.state.state !== 'incoming') return false
    if (this.state.busy || this.mediaPhone) return true
    const epoch = this.epoch
    const Factory = this.options.Call || CellularBrowserCall
    const call = new Factory(this.state.instanceId, this.state.peer, (type, data = {}) => {
      if (this.epoch !== epoch || this.mediaPhone !== call || !this.state) return
      if (type === 'prepared') this._patch({ preparedCallId: data.callId })
      else if (type === 'media') this._patch({ mediaReady: data.ready === true, phase: data.phase })
      else if (type === 'answering') this._patch({ phase: 'answering' })
      else if (type === 'calling' && this.state.state !== 'active')
        this._patch({ state: 'answering', phase: data.uncertain ? 'uncertain' : 'answering' })
      else if (type === 'active') this._patch({ state: 'active', busy: false,
        startedAt: this.state.startedAt || Date.now() })
      else if (type === 'ending') this._patch({ state: 'ending', busy: true })
      else if (type === 'termination-unconfirmed') this._patch({ state: 'termination_unconfirmed',
        busy: false, error: data.cause || this._t('Call termination could not be confirmed') })
      else if (type === 'failed') {
        this._toast(`${this._t('Cellular call failed')}: ${data.cause}`)
        if (data.committed) this._patch({ state: 'ending', busy: true, error: data.cause })
        else {
          // Denied microphone or losing claim leaves the physical incoming call available.
          this.mediaPhone = null
          this._patch({ state: 'incoming', busy: false, mediaReady: false,
            phase: data.status === 409 ? 'occupied' : 'audio_failed', error: data.cause })
        }
      } else if (type === 'ended') this._endSoon(data.cause)
    }, { direction: 'inbound', sourceCallId: this.state.sourceCallId, api: this.options.api })
    this.mediaPhone = call
    this._patch({ busy: true, phase: 'preparing', error: '' })
    call.start()
    return true
  }

  decline() {
    if (!this.state || this.state.state === 'ended' || this.state.state === 'ending') return false
    const epoch = this.epoch
    const instanceId = this.state.instanceId
    this._patch({ state: 'ending', busy: true })
    // Only this explicit carrier-call decline invokes global Hangup. Automatic cleanup never does.
    this.options.api.cellularCallHangup(instanceId).then(result => {
      if (this.epoch !== epoch || !this.state) return
      if (!result?.ok && !result?.termination_pending) throw new Error(result.error || 'Hangup failed')
    }).catch(error => {
      if (this.epoch === epoch) this._patch({ state: 'termination_unconfirmed', busy: false, error: error.message })
    })
    if (this.mediaPhone) { const call = this.mediaPhone; this.mediaPhone = null; void call.closeLocal() }
    clearTimeout(this.terminationTimer)
    this.terminationTimer = setTimeout(() => {
      if (this.epoch === epoch && this.state?.state === 'ending') this._patch({
        state: 'termination_unconfirmed', busy: false,
        error: this._t('Call termination could not be confirmed'),
      })
    }, 10000)
    return true
  }

  hangup() {
    if (!this.state) return false
    if (this.mediaPhone) { void this.mediaPhone.hangup(); return true }
    return this.decline()
  }

  _endSoon(endCause) {
    if (!this.state) return
    this._rememberTerminalSource(this.state.sourceKey)
    const call = this.mediaPhone
    this.mediaPhone = null
    if (call) void call.closeLocal()
    clearTimeout(this.clearTimer)
    clearTimeout(this.terminationTimer)
    this._patch({ state: 'ended', endCause, busy: false, phase: 'ended' })
    const epoch = this.epoch
    this.clearTimer = setTimeout(() => { if (this.epoch === epoch) this._emit(null) },
      this.options.clearMs ?? 2500)
  }

  stop() {
    this.epoch += 1
    clearTimeout(this.clearTimer)
    clearTimeout(this.terminationTimer)
    const call = this.mediaPhone
    this.mediaPhone = null
    if (call) void call.closeLocal()
    this._emit(null)
  }
}
