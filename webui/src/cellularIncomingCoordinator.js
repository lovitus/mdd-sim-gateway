import { api as defaultApi } from './api.js'
import { boundedCellularRelease, refreshCellularMediaState } from './cellularMediaMonitor.js'

const TERMINAL_STATUSES = new Set(['ended', 'terminated', 'idle', 'failed'])

const sleep = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds))

function callStatus(item) {
  return String(item?.status || '').toLowerCase()
}

function callDirection(item) {
  return String(item?.direction || item?.dir || '').toLowerCase()
}

function callPeer(item) {
  return item?.peer || item?.from || item?.number || 'Unknown'
}

function callSourceKey(instanceId, item) {
  return `${instanceId}:${item?.id || item?.call_id || item?.lease_id || callPeer(item)}`
}

export class CellularIncomingController {
  constructor(options = {}) {
    this.options = { api: defaultApi, delay: sleep, ...options }
    this.state = null
    this.epoch = 0
    this.mediaPhone = null
    this.clearTimer = null
    this.pollTimer = null
    this.preparedCallId = ''
    this.browserNonce = ''
    this.mediaDialed = false
    this.answerToken = 0
    this.answerRequested = false
    this.answered = false
    this.committed = false
    this.releaseRequested = false
    this.termination = { key: '', promise: null }
    this.terminalSources = new Map()
  }

  updateOptions(options = {}) {
    this.options = { ...this.options, ...options }
  }

  _emit(next) {
    this.state = next
    this.options.onStateChange?.(next ? { ...next } : null)
  }

  _patch(patch) {
    if (!this.state) return
    this._emit({ ...this.state, ...patch })
  }

  _toast(message) {
    if (message) this.options.showToast?.(message)
  }

  _t(key) {
    return this.options.t ? this.options.t(key) : key
  }

  _host() {
    return this.options.host?.() || globalThis.location?.hostname || 'localhost'
  }

  _now() {
    return this.options.now?.() || Date.now()
  }

  _terminalSourceTtlMs() {
    const configured = Number(this.options.terminalSourceTtlMs)
    return Number.isFinite(configured) && configured > 0 ? configured : 30000
  }

  _clearMs() {
    const configured = Number(this.options.clearMs)
    return Number.isFinite(configured) && configured >= 0 ? configured : 2500
  }

  _pruneTerminalSources(now = this._now()) {
    for (const [key, expiresAt] of this.terminalSources.entries()) {
      if (expiresAt <= now) this.terminalSources.delete(key)
    }
  }

  _rememberTerminalSource(sourceKey) {
    if (!sourceKey) return
    const now = this._now()
    this._pruneTerminalSources(now)
    this.terminalSources.set(sourceKey, now + this._terminalSourceTtlMs())
    while (this.terminalSources.size > 64) {
      this.terminalSources.delete(this.terminalSources.keys().next().value)
    }
  }

  _isTerminalSource(sourceKey) {
    if (!sourceKey) return false
    const now = this._now()
    this._pruneTerminalSources(now)
    return this.terminalSources.has(sourceKey)
  }

  _answerStillCurrent(epoch, token) {
    return this.epoch === epoch &&
      this.answerToken === token &&
      !this.releaseRequested &&
      this.state &&
      ['incoming', 'answering'].includes(this.state.state)
  }

  handleMessage(message) {
    if (message?.type !== 'call') return
    const item = message.call || {}
    if (item.transport !== 'cellular') return
    const instanceId = String(message.instance || item.instance_id || item.instance || '')
    if (!instanceId) return
    const direction = callDirection(item)
    const status = callStatus(item)
    const current = this.state
    const sourceKey = callSourceKey(instanceId, item)
    if (direction === 'in' && status === 'ringing') {
      if (this._isTerminalSource(sourceKey)) return
      const currentTerminal = current && ['ended', 'failed'].includes(current.state)
      if (current && current.sourceKey === sourceKey) {
        if (currentTerminal) {
          this._rememberTerminalSource(sourceKey)
          return
        }
        this._patch({ peer: callPeer(item), sourceCallId: item.id || current.sourceCallId })
        return
      }
      if (current && !currentTerminal && current.sourceKey !== sourceKey) return
      this._startIncoming(instanceId, item, sourceKey)
      return
    }
    if (!current || current.instanceId !== instanceId) return
    if (current.sourceKey && sourceKey !== current.sourceKey) return
    if (status === 'answered') {
      this._patch({ state: 'active', busy: false, startedAt: current.startedAt || Date.now() })
    } else if (item.end_ts || TERMINAL_STATUSES.has(status)) {
      this._endSoon(status === 'failed' ? 'Failed' : 'Ended')
    }
  }

  _startIncoming(instanceId, item, sourceKey) {
    this.stop({ release: false })
    this.epoch += 1
    const epoch = this.epoch
    this.preparedCallId = ''
    this.browserNonce = ''
    this.mediaDialed = false
    this.answerToken = 0
    this.answerRequested = false
    this.answered = false
    this.committed = false
    this.releaseRequested = false
    this.termination = { key: '', promise: null }
    this._emit({
      transport: 'cellular',
      state: 'incoming',
      phase: 'preparing',
      instanceId,
      sourceKey,
      sourceCallId: item.id || item.call_id || '',
      peer: callPeer(item),
      mediaReady: false,
      busy: false,
      startedAt: 0,
    })
    this._prepare(epoch, instanceId)
  }

  async _prepare(epoch, instanceId) {
    try {
      const prepared = await this.options.api.prepareIncomingCellularCall(instanceId)
      if (this.epoch !== epoch || !this.state) {
        this.options.api.cancelCellularCall(instanceId, prepared.call_id).catch(() => {})
        return
      }
      if (this.releaseRequested) {
        this.options.api.cancelCellularCall(instanceId, prepared.call_id).catch(() => {})
        return
      }
      this.preparedCallId = prepared.call_id
      this.browserNonce = prepared.browser_nonce
      this._patch({ phase: 'registering_browser', preparedCallId: prepared.call_id })
      const mediaPhone = this.options.createMediaPhone?.(
        (type, data) => this._handlePhoneEvent(epoch, type, data, prepared))
      if (!mediaPhone) throw new Error('browser media is unavailable')
      this.mediaPhone = mediaPhone
      mediaPhone.start(prepared.softphone, prepared.softphone?.host || this._host())
    } catch (error) {
      if (this.epoch !== epoch || !this.state) return
      this._toast(`${this._t('Cellular call failed')}: ${error.message}`)
      this._endSoon('Failed')
    }
  }

  _handlePhoneEvent(epoch, type, data, prepared) {
    if (this.epoch !== epoch || !this.state) return
    if (type === 'registered' && data && !this.releaseRequested &&
        this.state.state === 'incoming' && !this.mediaDialed) {
      this.mediaDialed = true
      this.options.api.ringIncomingCellularCall(
        this.state.instanceId, prepared.call_id).catch((error) => {
          if (this.epoch !== epoch) return
          this._toast(`${this._t('Cellular call failed')}: ${error.message}`)
          this._requestTermination().catch(() => {})
          this._endSoon('Failed')
        })
      return
    }
    if (type === 'incoming') {
      if (this.releaseRequested || this.state.state !== 'incoming') return
      this._patch({
        phase: 'ready',
        mediaReady: true,
        peer: data?.from || this.state.peer,
      })
      return
    }
    if (type === 'active' && this.answerRequested && !this.releaseRequested && !this.answered) {
      this.answered = true
      const token = this.answerToken
      this._answerAfterMedia(epoch, token, prepared).catch((error) => {
        if (this.epoch !== epoch) return
        this._toast(`${this._t('Cellular call failed')}: ${error.message}`)
        this._requestTermination().catch(() => {})
        this._endSoon('Failed')
      })
      return
    }
    if ((type === 'failed' || type === 'ended') && !this.releaseRequested) {
      this._requestTermination().catch(() => {})
      this._endSoon(data?.cause || 'Media ended')
      return
    }
    if (type === 'retryexhausted' && !this.releaseRequested) {
      this._toast(`${this._t('Cellular call failed')}: ${this._t('Media connection retry limit reached')}`)
      this._requestTermination().catch(() => {})
      this._endSoon('Media retry limit reached')
    }
  }

  async _answerAfterMedia(epoch, token, prepared) {
    if (!this._answerStillCurrent(epoch, token)) return
    this._patch({ state: 'answering', busy: true, phase: 'verifying' })
    const evidence = await this.mediaPhone.waitForBidirectionalMedia()
    if (!this._answerStillCurrent(epoch, token)) return
    await this.options.api.submitCellularMediaEvidence(this.state.instanceId, prepared.call_id, {
      nonce: prepared.browser_nonce,
      ...evidence,
    })
    if (!this._answerStillCurrent(epoch, token)) return
    const result = await this.options.api.answerIncomingCellularCall(
      this.state.instanceId, prepared.call_id)
    if (!this._answerStillCurrent(epoch, token)) return
    if (!result.ok && !result.uncertain) throw new Error(result.error || this._t('Unknown'))
    this.committed = true
    this._patch({
      state: 'active',
      phase: result.uncertain ? 'uncertain' : 'active',
      busy: false,
      mediaReady: true,
      startedAt: this.state.startedAt || Date.now(),
    })
    if (result.uncertain) {
      this._toast(this._t('Call answer is uncertain. Use Hang up before trying again.'))
    }
    this._startStatusPoll(epoch)
  }

  answer() {
    if (!this.state) return false
    if (this.answerRequested && ['incoming', 'answering'].includes(this.state.state)) return true
    if (this.state.state !== 'incoming') return false
    if (!this.state.mediaReady || !this.mediaPhone || !this.preparedCallId) {
      this._toast(this._t('Incoming call audio is still preparing.'))
      return false
    }
    this.answerRequested = true
    this.answerToken += 1
    this._patch({ busy: true, phase: 'answering' })
    try {
      this.mediaPhone.unlockAudio()
      this.mediaPhone.answer()
    } catch (error) {
      this._toast(`${this._t('Cellular call failed')}: ${error.message}`)
      this._requestTermination().catch(() => {})
      this._endSoon('Failed')
    }
    return true
  }

  decline() {
    return this._userTerminate('Rejected')
  }

  hangup() {
    return this._userTerminate('Ended')
  }

  _userTerminate(reason) {
    if (!this.state) return false
    this.releaseRequested = true
    this.answerToken += 1
    this._rememberTerminalSource(this.state.sourceKey)
    this._patch({ busy: true, phase: 'ending' })
    this._requestTermination().catch((error) =>
      this._toast(`${this._t('Cellular hangup failed')}: ${error.message}`))
    this._endSoon(reason)
    return true
  }

  _requestTermination() {
    if (!this.state) return Promise.resolve({ ok: true, missing: true })
    const key = this.preparedCallId || `instance:${this.state.instanceId}`
    if (this.termination.key === key && this.termination.promise) {
      return this.termination.promise
    }
    const promise = this.options.api.cellularCallHangup(this.state.instanceId)
      .then((result) => {
        if (!result?.ok && !result?.termination_pending && this.termination.key === key) {
          this.termination = { key: '', promise: null }
        }
        return result
      }).catch((error) => {
        if (this.termination.key === key) this.termination = { key: '', promise: null }
        throw error
      })
    this.termination = { key, promise }
    return promise
  }

  _startStatusPoll(epoch) {
    if (this.pollTimer) clearTimeout(this.pollTimer)
    const poll = async () => {
      if (this.epoch !== epoch || !this.state || this.state.state !== 'active') return
      try {
        const refreshed = await refreshCellularMediaState({
          refreshEvidence: async () => {
            if (!this.mediaPhone || !this.preparedCallId || !this.browserNonce) return
            const evidence = await this.mediaPhone.waitForBidirectionalMedia(3000)
            await this.options.api.submitCellularMediaEvidence(
              this.state.instanceId, this.preparedCallId,
              { nonce: this.browserNonce, ...evidence })
          },
          getStatus: () => this.options.api.cellularCallStatus(this.state.instanceId),
          terminate: () => boundedCellularRelease({
            callId: this.preparedCallId,
            release: (callId) => this.options.api.releaseCellularCall(this.state.instanceId, callId),
            delay: this.options.delay,
          }),
        })
        const result = refreshed.status
        const status = callStatus(result)
        const mediaPhase = result?.media?.phase || ''
        if (mediaPhase) this._patch({ phase: mediaPhase })
        if (result?.unavailable || status === 'failed') {
          this._toast(`${this._t('Cellular call ended')}: ${result.error || this._t('Cellular modem is unavailable')}`)
          this._endSoon('Failed')
          return
        }
        if (TERMINAL_STATUSES.has(status) &&
            (result.terminal_confirmed || Number(result.terminal_samples || 0) >= 2)) {
          this._endSoon(result.call?.reason || 'Ended')
          return
        }
      } catch (error) {
        this._toast(`${this._t('Could not read cellular call status')}: ${error.message}`)
      }
      if (this.epoch === epoch && this.state?.state === 'active') {
        this.pollTimer = setTimeout(poll, 2000)
      }
    }
    this.pollTimer = setTimeout(poll, 2000)
  }

  _stopPhone() {
    const phone = this.mediaPhone
    this.mediaPhone = null
    if (phone) {
      try { phone.hangup() } catch {}
      try { phone.stop() } catch {}
    }
  }

  _endSoon(endCause) {
    if (!this.state) return
    this._rememberTerminalSource(this.state.sourceKey)
    this.answerToken += 1
    if (this.clearTimer) clearTimeout(this.clearTimer)
    if (this.pollTimer) clearTimeout(this.pollTimer)
    this.pollTimer = null
    this._stopPhone()
    this._patch({ state: 'ended', endCause, busy: false, phase: 'ended' })
    const epoch = this.epoch
    this.clearTimer = setTimeout(() => {
      if (this.epoch !== epoch) return
      this.clearTimer = null
      this._emit(null)
    }, this._clearMs())
  }

  stop({ release = true } = {}) {
    if (this.clearTimer) clearTimeout(this.clearTimer)
    if (this.pollTimer) clearTimeout(this.pollTimer)
    this.clearTimer = null
    this.pollTimer = null
    if (release && this.state) {
      this.releaseRequested = true
      this.answerToken += 1
      this._rememberTerminalSource(this.state.sourceKey)
    }
    const shouldTerminate = release && this.state &&
      (this.committed || this.answerRequested ||
        ['answering', 'active'].includes(this.state.state))
    if (shouldTerminate) {
      this._requestTermination().catch((error) =>
        this._toast(`${this._t('Cellular hangup failed')}: ${error.message}`))
    } else if (release && this.state && this.preparedCallId && !this.committed) {
      this.options.api.releaseCellularCall(this.state.instanceId, this.preparedCallId)
        .catch(() => {})
    }
    this._stopPhone()
    this._emit(null)
  }
}
