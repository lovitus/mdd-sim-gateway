import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api } from './api.js'
import { Softphone as BrowserPhone } from './softphone.js'
import { NativeBrowserCall, verifyBrowserMedia } from './browserMedia.js'
import { KeyedTrailingRequests } from './keyedTrailingRequests.js'
import { useI18n } from './i18n.jsx'
import {
  backendCallIdentity,
  backendFallbackCall,
  backendPresentationIdentity,
  incomingReconcileActive,
  isTerminalBackendCall,
  sameBackendCall,
  sameBackendPresentationCall,
  selectIncomingOverlayEntry,
  shouldSurfaceIncomingSyncFailure,
  shouldShowBackendFallback,
} from './vowifiIncomingFallback.js'

const GREEN = '#22c55e'
const RED = '#ef4444'
const INCOMING_RETRY_DELAYS_MS = [1000, 3000, 8000]
const INCOMING_RETRY_COOLDOWN_MS = 30000

const emptyLine = () => ({
  prov: null,
  reg: 'idle',
  call: null,
  mediaTest: 'idle',
  retryExhausted: false,
  refreshPending: false,
  incomingSyncError: '',
})

function Avatar({ color = GREEN, size = 110 }) {
  return (
    <div style={{ width: size, height: size, borderRadius: '50%', background: color + '22',
      border: `2px solid ${color}55`, display: 'flex', alignItems: 'center', justifyContent: 'center',
      fontSize: size * 0.42, color, margin: '0 auto' }}>☎</div>
  )
}

export function useCallCoordinator({ enabled, instances, subscribe, showToast, mediaIngressRevision }) {
  const mountedRef = useRef(true)
  const enabledRef = useRef(enabled)
  enabledRef.current = enabled
  const audioRef = useRef(null)
  const phones = useRef(new Map())
  const nativeCalls = useRef(new Map())
  const provisioningRequests = useRef(null)
  const provisioningHandlers = useRef({})
  const clearTimers = useRef(new Map())
  const backendTerminalCalls = useRef(new Set())
  const backendEventRevisions = useRef(new Map())
  const reconcileRequests = useRef(new Map())
  const reconcileInFlight = useRef(new Set())
  const reconcileFollowupPending = useRef(new Set())
  const reconcileRetryTimers = useRef(new Map())
  const reconcileFailures = useRef(new Map())
  const reconcileCooldownUntil = useRef(new Map())
  const reconcileRuntimeGenerations = useRef(new Map())
  const reconcileEpochs = useRef(new Map())
  const reconcileHintKeys = useRef(new Map())
  const reconcileDirtyEpochs = useRef(new Map())
  const reconcileCooldownProbeEpochs = useRef(new Map())
  const mediaIngressSeen = useRef(mediaIngressRevision)
  const linesRef = useRef({})
  const showToastRef = useRef(showToast)
  showToastRef.current = showToast
  const [lines, setLines] = useState({})
  const instanceIdsKey = (instances || []).map(item => String(item.id || ''))
    .filter(Boolean).sort().join('\u0000')
  const instanceIds = useMemo(() => instanceIdsKey ? instanceIdsKey.split('\u0000') : [],
    [instanceIdsKey])
  const instanceIdsRef = useRef(instanceIds)
  instanceIdsRef.current = instanceIds

  const rememberBackendTerminalCall = useCallback((identity) => {
    if (!identity) return
    backendTerminalCalls.current.add(identity)
    if (backendTerminalCalls.current.size > 256)
      backendTerminalCalls.current = new Set([...backendTerminalCalls.current].slice(-128))
  }, [])

  const updateLine = useCallback((id, updater) => {
    const key = String(id || '')
    if (!mountedRef.current || !key) return
    setLines(current => {
      const previous = current[key] || emptyLine()
      const patch = typeof updater === 'function' ? updater(previous) : updater
      const next = { ...previous, ...(patch || {}) }
      const all = { ...current, [key]: next }
      linesRef.current = all
      return all
    })
  }, [])

  const clearCallSoon = useCallback((id, endCause) => {
    const key = String(id || '')
    if (!key) return
    const existing = clearTimers.current.get(key)
    if (existing) clearTimeout(existing)
    updateLine(key, line => ({ call: line.call ? { ...line.call, state: 'ended', endCause } : null }))
    const timer = setTimeout(() => {
      clearTimers.current.delete(key)
      if (!mountedRef.current) return
      updateLine(key, line => ({
        call: line.call?.state === 'ended' ? null : line.call,
      }))
    }, 2500)
    clearTimers.current.set(key, timer)
  }, [updateLine])

  const stopLine = useCallback((id, { forgetProvision = false } = {}) => {
    const key = String(id || '')
    const nativeCall = nativeCalls.current.get(key)
    nativeCalls.current.delete(key)
    if (nativeCall) {
      try { nativeCall.hangup() } catch {}
    }
    const phone = phones.current.get(key)
    phones.current.delete(key)
    if (phone) {
      try { phone.stop() } catch {}
    }
    updateLine(key, line => ({
      prov: forgetProvision ? null : line.prov,
      reg: 'idle',
      retryExhausted: false,
      call: line.call?.state === 'ended' ? line.call : null,
    }))
  }, [updateLine])

  const ensurePhone = useCallback((id, prov) => {
    const key = String(id || '')
    if (!enabled || !key || !prov?.enabled || phones.current.has(key)) return
    let phone = null
    phone = new BrowserPhone((type, data) => {
      if (phones.current.get(key) !== phone) return
      if (type === 'registered') updateLine(key, { reg: data ? 'registered' : 'unregistered' })
      else if (type === 'ws') updateLine(key, line => ({
        reg: data === 'connected' ? (line.reg === 'registered' ? line.reg : 'connecting') : 'disconnected',
      }))
      else if (type === 'regfail') updateLine(key, { reg: 'failed' })
      else if (type === 'retryexhausted') {
        phones.current.delete(key)
        updateLine(key, { reg: 'failed', retryExhausted: true })
        try { phone.stop() } catch {}
      }
      else if (type === 'mediacheck') updateLine(key, {
        call: { dir: 'out', number: data.to, state: 'checking', transport: 'vowifi', instanceId: key },
      })
      else if (type === 'incoming') updateLine(key, {
        call: { dir: 'in', number: data.from || 'Unknown', state: 'incoming',
          transport: 'vowifi', source: 'jssip', answerable: true, instanceId: key },
      })
      else if (type === 'calling') updateLine(key, {
        call: { dir: 'out', number: data.to, state: 'calling',
          transport: 'vowifi', source: 'jssip', instanceId: key },
      })
      else if (type === 'progress') updateLine(key, line => ({
        call: (line.call && line.call.dir === 'out' &&
          (line.call.state === 'calling' || line.call.state === 'ringing'))
          ? { ...line.call, state: 'ringing' } : line.call,
      }))
      else if (type === 'active') updateLine(key, line => ({
        call: line.call ? { ...line.call, state: 'active', startedAt: Date.now() } : line.call,
      }))
      else if (type === 'ended') clearCallSoon(key, data && data.cause)
      else if (type === 'failed') clearCallSoon(key, data && data.cause)
    }, audioRef.current)
    phones.current.set(key, phone)
    phone.start(prov, prov.host || location.hostname)
    updateLine(key, { reg: 'connecting', retryExhausted: false })
  }, [clearCallSoon, enabled, updateLine])

  provisioningHandlers.current = {
    active: key => Boolean(mountedRef.current && enabledRef.current &&
      instanceIdsRef.current.includes(key)),
    run: key => api.softphone(key),
    commit: (key, prov) => {
      const current = linesRef.current[key]
      const changed = !current?.prov || current.prov.generation !== prov.generation ||
        current.prov.enabled !== prov.enabled
      if (changed) stopLine(key, { forgetProvision: true })
      updateLine(key, { prov, retryExhausted: false, refreshPending: false })
      if (prov?.enabled) ensurePhone(key, prov)
      return prov
    },
  }
  if (!provisioningRequests.current) {
    provisioningRequests.current = new KeyedTrailingRequests({
      active: key => provisioningHandlers.current.active(key),
      run: key => provisioningHandlers.current.run(key),
      commit: (key, value) => provisioningHandlers.current.commit(key, value),
    })
  }

  const loadProvision = useCallback((id, options = {}) => {
    const key = String(id || '')
    return key ? provisioningRequests.current.request(key, options) : Promise.resolve(null)
  }, [])

  const reloadLine = useCallback((id) => {
    const key = String(id || '')
    if (!key) return
    stopLine(key, { forgetProvision: true })
    updateLine(key, { reg: 'idle', retryExhausted: false, mediaTest: 'idle' })
    loadProvision(key, { fresh: true })
  }, [loadProvision, stopLine, updateLine])

  const applyBackendIncoming = useCallback((id, call, { authoritative = false } = {}) => {
    const key = String(id || '')
    if (!key || !call || call.direction !== 'in') return
    const identity = backendCallIdentity(call)
    if (isTerminalBackendCall(call)) {
      rememberBackendTerminalCall(identity || backendPresentationIdentity(call))
      if (sameBackendPresentationCall(linesRef.current[key]?.call, call))
        clearCallSoon(key, call.status || 'ended')
      return
    }
    updateLine(key, line => {
      if (!shouldShowBackendFallback(
        line.call, call, backendTerminalCalls.current, authoritative)) return {}
      return { call: backendFallbackCall(key, call) }
    })
  }, [clearCallSoon, rememberBackendTerminalCall, updateLine])

  const reconcileActive = useCallback(key => incomingReconcileActive(
    mountedRef.current, enabledRef.current, instanceIdsRef.current, key), [])

  const reconcileOpenIncoming = useCallback((id) => {
    const key = String(id || '')
    if (!key || !reconcileActive(key)) return Promise.resolve()
    const requestEpoch = reconcileEpochs.current.get(key) || 0
    const scheduleCooldownProbe = (cooldownUntil) => {
      if (reconcileDirtyEpochs.current.get(key) !== requestEpoch ||
          reconcileCooldownProbeEpochs.current.get(key) === requestEpoch ||
          reconcileRetryTimers.current.has(key)) return
      // Consume this epoch atomically with timer creation. A failed probe cannot schedule itself
      // again; only a semantically new call hint or trusted runtime identity opens a new epoch.
      reconcileCooldownProbeEpochs.current.set(key, requestEpoch)
      const timer = setTimeout(() => {
        reconcileRetryTimers.current.delete(key)
        if (!reconcileActive(key) ||
            (reconcileEpochs.current.get(key) || 0) !== requestEpoch) return
        void reconcileOpenIncoming(key)
      }, Math.max(0, cooldownUntil - Date.now()))
      reconcileRetryTimers.current.set(key, timer)
    }
    const cooldownUntil = reconcileCooldownUntil.current.get(key) || 0
    if (Date.now() < cooldownUntil) {
      scheduleCooldownProbe(cooldownUntil)
      return Promise.resolve()
    }
    if (reconcileInFlight.current.has(key)) {
      reconcileFollowupPending.current.add(key)
      return Promise.resolve()
    }
    const scheduledRetry = reconcileRetryTimers.current.get(key)
    if (scheduledRetry) clearTimeout(scheduledRetry)
    reconcileRetryTimers.current.delete(key)
    reconcileInFlight.current.add(key)
    const request = (reconcileRequests.current.get(key) || 0) + 1
    const revision = backendEventRevisions.current.get(key) || 0
    reconcileRequests.current.set(key, request)
    let requestError = null
    return api.openIncomingCalls(key).then(result => {
      if (reconcileRequests.current.get(key) !== request) return
      if (!reconcileActive(key)) return
      if ((backendEventRevisions.current.get(key) || 0) !== revision ||
          (reconcileEpochs.current.get(key) || 0) !== requestEpoch) return
      const retryTimer = reconcileRetryTimers.current.get(key)
      if (retryTimer) clearTimeout(retryTimer)
      reconcileRetryTimers.current.delete(key)
      reconcileFailures.current.delete(key)
      reconcileCooldownUntil.current.delete(key)
      if (reconcileDirtyEpochs.current.get(key) === requestEpoch)
        reconcileDirtyEpochs.current.delete(key)
      if (reconcileCooldownProbeEpochs.current.get(key) === requestEpoch)
        reconcileCooldownProbeEpochs.current.delete(key)
      const open = (result?.calls || []).find(call => !isTerminalBackendCall(call))
      if (open) applyBackendIncoming(key, open, { authoritative: true })
      else updateLine(key, line => ({
        call: line.call?.source === 'backend' ? null : line.call,
        incomingSyncError: '',
      }))
      if (open) updateLine(key, { incomingSyncError: '' })
    }).catch(error => {
      requestError = error
      if (reconcileRequests.current.get(key) !== request ||
          !reconcileActive(key) ||
          (reconcileEpochs.current.get(key) || 0) !== requestEpoch) return
    }).finally(() => {
      if (reconcileRequests.current.get(key) !== request) return
      reconcileInFlight.current.delete(key)
      const pending = reconcileFollowupPending.current.delete(key)
      if (!reconcileActive(key)) return
      if ((reconcileEpochs.current.get(key) || 0) !== requestEpoch) {
        if (pending && reconcileActive(key)) void reconcileOpenIncoming(key)
        return
      }
      let failures = reconcileFailures.current.get(key) || 0
      if (requestError) {
        failures += 1
        reconcileFailures.current.set(key, failures)
        if (shouldSurfaceIncomingSyncFailure(failures, INCOMING_RETRY_DELAYS_MS.length)) {
          updateLine(key, {
            incomingSyncError: requestError?.message || 'Incoming-call sync failed',
          })
          showToastRef.current?.(
            'Incoming-call status could not be verified; automatic retry is paused')
        }
      }
      if (pending && (!requestError || failures <= INCOMING_RETRY_DELAYS_MS.length)) {
        void reconcileOpenIncoming(key)
        return
      }
      if (!requestError || !reconcileActive(key)) return
      if (failures <= INCOMING_RETRY_DELAYS_MS.length) {
        const timer = setTimeout(() => {
          reconcileRetryTimers.current.delete(key)
          if (!reconcileActive(key) ||
              (reconcileEpochs.current.get(key) || 0) !== requestEpoch) return
          void reconcileOpenIncoming(key)
        }, INCOMING_RETRY_DELAYS_MS[failures - 1])
        const prior = reconcileRetryTimers.current.get(key)
        if (prior) clearTimeout(prior)
        reconcileRetryTimers.current.set(key, timer)
      } else {
        const nextCooldown = Date.now() + INCOMING_RETRY_COOLDOWN_MS
        reconcileCooldownUntil.current.set(key, nextCooldown)
        scheduleCooldownProbe(nextCooldown)
      }
    })
  }, [applyBackendIncoming, reconcileActive, updateLine])

  const cancelReconcile = useCallback((key) => {
    key = String(key || '')
    if (!key) return
    reconcileRequests.current.set(key, (reconcileRequests.current.get(key) || 0) + 1)
    reconcileInFlight.current.delete(key)
    reconcileFollowupPending.current.delete(key)
    const retryTimer = reconcileRetryTimers.current.get(key)
    if (retryTimer) clearTimeout(retryTimer)
    reconcileRetryTimers.current.delete(key)
    reconcileFailures.current.delete(key)
    reconcileCooldownUntil.current.delete(key)
    reconcileRuntimeGenerations.current.delete(key)
    reconcileEpochs.current.delete(key)
    reconcileHintKeys.current.delete(key)
    reconcileDirtyEpochs.current.delete(key)
    reconcileCooldownProbeEpochs.current.delete(key)
  }, [])

  useEffect(() => {
    if (!enabled) {
      for (const key of [...reconcileRequests.current.keys()]) cancelReconcile(key)
      provisioningRequests.current.clear()
      for (const key of phones.current.keys()) stopLine(key, { forgetProvision: true })
      linesRef.current = {}
      setLines({})
      return
    }
    const ids = new Set(instanceIds)
    // Pending first loads have no linesRef entry yet. Cancel them before walking rendered lines
    // so a remove/re-add of the same instance cannot accept the old response.
    provisioningRequests.current.cancelExcept(ids)
    for (const key of [...reconcileRequests.current.keys()]) {
      if (!ids.has(key)) cancelReconcile(key)
    }
    for (const key of Object.keys(linesRef.current)) {
      if (!ids.has(key)) {
        provisioningRequests.current.cancel(key)
        cancelReconcile(key)
        stopLine(key, { forgetProvision: true })
        setLines(current => {
          const next = { ...current }
          delete next[key]
          linesRef.current = next
          return next
        })
      }
    }
    ids.forEach(id => {
      const line = linesRef.current[id]
      if (!line?.prov) loadProvision(id)
      else if (line.prov.enabled && !line.retryExhausted && !phones.current.has(id))
        ensurePhone(id, line.prov)
    })
  }, [cancelReconcile, enabled, ensurePhone, instanceIds, instanceIdsKey, loadProvision, stopLine])

  useEffect(() => {
    if (!enabled || !subscribe) return undefined
    const unsubscribe = subscribe(message => {
      const id = String(message.instance || '')
      const currentLine = id && instanceIdsRef.current.includes(id)
      if (message.type === 'call' && currentLine && message.call?.direction === 'in') {
        const exactHint = backendCallIdentity(message.call)
        const semanticHint = exactHint
          ? `${exactHint}:${isTerminalBackendCall(message.call) ? 'terminal' : 'open'}` : ''
        if (semanticHint) {
          if (reconcileHintKeys.current.get(id) !== semanticHint) {
            reconcileHintKeys.current.set(id, semanticHint)
            const epoch = (reconcileEpochs.current.get(id) || 0) + 1
            reconcileEpochs.current.set(id, epoch)
            reconcileFailures.current.delete(id)
            reconcileCooldownUntil.current.delete(id)
            reconcileCooldownProbeEpochs.current.delete(id)
            const retryTimer = reconcileRetryTimers.current.get(id)
            if (retryTimer) clearTimeout(retryTimer)
            reconcileRetryTimers.current.delete(id)
          }
          reconcileDirtyEpochs.current.set(id, reconcileEpochs.current.get(id) || 0)
        }
        backendEventRevisions.current.set(id, (backendEventRevisions.current.get(id) || 0) + 1)
        if (reconcileInFlight.current.has(id)) reconcileFollowupPending.current.add(id)
        // A WS event is only a hint. It can be delayed from an old Engine generation, so only a
        // complete /open-incoming snapshot may create, update or clear the backend fallback UI.
        void reconcileOpenIncoming(id)
        return
      }
      if (message.type === 'ws-lifecycle' && message.event === 'open')
        instanceIdsRef.current.forEach(reconcileOpenIncoming)
      if (message.type === 'engine' && currentLine) {
        if (message.event === 'runtime_changed') {
          const runtimeIdentity = `${message.running ? 'running' : 'stopped'}:` +
            `${String(message.generation || '')}:${String(message.engine_run_id || '')}`
          if (reconcileRuntimeGenerations.current.get(id) !== runtimeIdentity) {
            reconcileRuntimeGenerations.current.set(id, runtimeIdentity)
            const epoch = (reconcileEpochs.current.get(id) || 0) + 1
            reconcileEpochs.current.set(id, epoch)
            reconcileDirtyEpochs.current.set(id, epoch)
            reconcileHintKeys.current.delete(id)
            reconcileFailures.current.delete(id)
            reconcileCooldownUntil.current.delete(id)
            reconcileCooldownProbeEpochs.current.delete(id)
            const retryTimer = reconcileRetryTimers.current.get(id)
            if (retryTimer) clearTimeout(retryTimer)
            reconcileRetryTimers.current.delete(id)
          }
          void reconcileOpenIncoming(id)
        }
        const line = linesRef.current[id]
        if (line?.call) updateLine(id, { refreshPending: true })
        else reloadLine(id)
      }
    })
    // Subscribe first, then reconcile. A call event during the request invalidates its response.
    instanceIdsRef.current.forEach(reconcileOpenIncoming)
    return unsubscribe
  }, [enabled, instanceIdsKey, reconcileOpenIncoming, reloadLine, subscribe, updateLine])

  useEffect(() => {
    if (mediaIngressSeen.current === mediaIngressRevision) return
    mediaIngressSeen.current = mediaIngressRevision
    for (const [id, line] of Object.entries(linesRef.current)) {
      if (line.call) updateLine(id, { refreshPending: true })
      else reloadLine(id)
    }
  }, [mediaIngressRevision, reloadLine, updateLine])

  useEffect(() => {
    for (const [id, line] of Object.entries(lines)) {
      if (line.refreshPending && !line.call) reloadLine(id)
    }
  }, [lines, reloadLine])

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      provisioningRequests.current.clear()
      for (const [key, request] of reconcileRequests.current)
        reconcileRequests.current.set(key, request + 1)
      reconcileInFlight.current.clear()
      reconcileFollowupPending.current.clear()
      for (const timer of reconcileRetryTimers.current.values()) clearTimeout(timer)
      reconcileRetryTimers.current.clear()
      reconcileFailures.current.clear()
      reconcileCooldownUntil.current.clear()
      reconcileRuntimeGenerations.current.clear()
      reconcileEpochs.current.clear()
      reconcileHintKeys.current.clear()
      reconcileDirtyEpochs.current.clear()
      reconcileCooldownProbeEpochs.current.clear()
      for (const timer of clearTimers.current.values()) clearTimeout(timer)
      clearTimers.current.clear()
      for (const phone of phones.current.values()) {
        try { phone.stop() } catch {}
      }
      phones.current.clear()
      for (const call of nativeCalls.current.values()) {
        try { call.hangup() } catch {}
      }
      nativeCalls.current.clear()
    }
  }, [])

  const getPhone = useCallback((id) => phones.current.get(String(id || '')), [])
  const createMediaPhone = useCallback((onEvent) => new BrowserPhone(onEvent, audioRef.current), [])

  const actions = {
    call: (id, number) => {
      const key = String(id || '')
      const provision = linesRef.current[key]?.prov
      if (provision?.browser_media?.outbound === true) {
        if (nativeCalls.current.has(key)) return false
        let call = null
        call = new NativeBrowserCall(key, number, (type, data) => {
          if (nativeCalls.current.get(key) !== call) return
          if (type === 'mediacheck') updateLine(key, {
            call: { dir: 'out', number: data.to, state: 'checking',
              transport: 'vowifi', source: 'native-wss', instanceId: key },
          })
          else if (type === 'calling') updateLine(key, line => ({
            call: line.call ? { ...line.call, state: 'calling' } : line.call,
          }))
          else if (type === 'active') updateLine(key, line => ({
            call: line.call ? { ...line.call, state: 'active', startedAt: Date.now() } : line.call,
          }))
          else if (type === 'ended' || type === 'failed') {
            nativeCalls.current.delete(key)
            clearCallSoon(key, data?.cause)
          }
        })
        nativeCalls.current.set(key, call)
        call.start()
        return true
      }
      const phone = getPhone(id)
      if (!phone) return false
      phone.unlockAudio()
      phone.call(number)
      return true
    },
    answer: (id) => {
      const phone = getPhone(id)
      if (!phone) return false
      if (linesRef.current[String(id || '')]?.call?.answerable === false) return false
      phone.unlockAudio()
      phone.answer()
      return true
    },
    decline: (id) => {
      const key = String(id || '')
      const current = linesRef.current[key]?.call
      if (current?.source === 'backend') {
        if (current.state === 'ending' || !current.exactIdentity ||
            !current.backendCallId || !current.sourceCallId)
          return false
        const capturedBackendCall = {
          id: current.backendCallId,
          engine_run_id: current.engineRunId,
          source_call_id: current.sourceCallId,
        }
        updateLine(key, line => ({ call: line.call ? {
          ...line.call, state: 'ending', hangupError: '',
        } : line.call }))
        api.hangupIncomingVowifiCall(
          key, current.backendCallId, current.sourceCallId, current.engineRunId).then(result => {
          if (!mountedRef.current) return
          if (!sameBackendCall(linesRef.current[key]?.call, capturedBackendCall)) return
          if (!result?.terminal_confirmed) {
            updateLine(key, line => ({ call: line.call ? {
              ...line.call, state: 'termination_unconfirmed',
              hangupError: 'Call termination could not be confirmed',
            } : line.call }))
            return
          }
          rememberBackendTerminalCall(backendCallIdentity(capturedBackendCall))
          clearCallSoon(key, 'Call ended')
        }).catch(error => {
          if (!mountedRef.current) return
          if (!sameBackendCall(linesRef.current[key]?.call, capturedBackendCall)) return
          updateLine(key, line => ({ call: line.call ? {
            ...line.call, state: 'termination_unconfirmed',
            hangupError: error?.message || 'Call termination could not be confirmed',
          } : line.call }))
          showToast?.(error?.message || 'Hangup failed')
        })
        return true
      }
      const phone = getPhone(key)
      if (!phone) return false
      phone.reject()
      updateLine(key, line => ({ call: line.call ? { ...line.call, state: 'ended', endCause: 'Rejected' } : null }))
      clearCallSoon(key, 'Rejected')
      return true
    },
    hangup: (id) => {
      const key = String(id || '')
      const nativeCall = nativeCalls.current.get(key)
      if (nativeCall) {
        nativeCalls.current.delete(key)
        nativeCall.hangup()
        updateLine(key, line => ({ call: line.call ? {
          ...line.call, state: 'ended', endCause: line.call.endCause,
        } : null }))
        clearCallSoon(key, undefined)
        return true
      }
      const phone = getPhone(id)
      if (!phone) return false
      phone.hangup()
      updateLine(id, line => ({ call: line.call ? { ...line.call, state: 'ended', endCause: line.call.endCause } : null }))
      clearCallSoon(id, undefined)
      return true
    },
    sendDTMF: (id, tone) => nativeCalls.current.get(String(id || ''))?.sendDTMF(tone)
      ?? getPhone(id)?.sendDTMF(tone),
    setMuted: (id, muted) => nativeCalls.current.get(String(id || ''))?.setMuted(muted)
      ?? getPhone(id)?.setMuted(muted),
    startRecording: (id) => getPhone(id)?.startRecording() || Promise.resolve(false),
    stopRecording: (id) => getPhone(id)?.stopRecording() || Promise.resolve(null),
    verifyMedia: (id) => {
      const key = String(id || '')
      if (!linesRef.current[key]?.prov?.browser_media?.available)
        return Promise.reject(new Error('Browser WSS media is unavailable'))
      updateLine(key, { mediaTest: 'running' })
      return verifyBrowserMedia(key).then(result => {
        updateLine(key, { mediaTest: 'passed' })
        return result
      }).catch(error => {
        updateLine(key, { mediaTest: 'failed' })
        throw error
      })
    },
    reloadLine,
    createMediaPhone,
  }

  return {
    lines,
    line: (id) => lines[String(id || '')] || emptyLine(),
    audioRef,
    ...actions,
    showToast,
  }
}

export function GlobalCallOverlay({
  coordinator, instances, mediaIngress, onMediaIngressConfirmed, onRequestMediaSetup,
}) {
  const { t } = useI18n()
  const [mediaBusy, setMediaBusy] = useState(false)
  const incoming = selectIncomingOverlayEntry(coordinator?.lines || {})
  const live = incoming || Object.entries(coordinator?.lines || {}).find(
    ([, line]) => line.call?.transport === 'vowifi' &&
      ['checking', 'calling', 'ringing', 'active'].includes(line.call?.state))
  const syncIssue = Object.entries(coordinator?.lines || {}).find(
    ([, line]) => Boolean(line.incomingSyncError))
  const syncNotice = syncIssue ? (() => {
    const [syncId, syncLine] = syncIssue
    const syncSelected = (instances || []).find(
      item => String(item.id) === String(syncId))
    return <div role="status" className="card" style={{ position: 'fixed', right: 20, top: 20,
      zIndex: 1001, padding: 12, maxWidth: 340, borderColor: '#f59e0b88',
      boxShadow: '0 8px 28px rgba(0,0,0,.3)' }}>
      <div style={{ color: '#f59e0b', fontWeight: 700, fontSize: 13 }}>
        {t('Incoming call status could not be verified')}
      </div>
      <div style={{ color: 'var(--text-mute)', fontSize: 12, marginTop: 4 }}>
        {syncSelected?.name || syncSelected?.id || syncId}: {syncLine.incomingSyncError}
      </div>
    </div>
  })() : null
  if (!live) return syncNotice
  if (!incoming) {
    const [id, line] = live
    const selected = (instances || []).find(item => String(item.id) === String(id))
    const call = line.call
    return (<>
      {syncNotice}
      <div className="card" style={{ position: 'fixed', right: 20, bottom: 20, zIndex: 1000,
        padding: 16, minWidth: 260, boxShadow: '0 12px 40px rgba(0,0,0,.35)' }}>
        <div style={{ fontSize: 12, color: 'var(--text-mute)' }}>{selected?.name || selected?.id || id}</div>
        <div className="mono" style={{ fontSize: 16, fontWeight: 700, marginTop: 4 }}>{call.number || 'Unknown'}</div>
        <div style={{ fontSize: 13, color: call.state === 'active' ? GREEN : '#eab308', marginTop: 4 }}>
          {t(call.state === 'active' ? 'Call active' : call.state === 'checking' ? 'Checking browser audio…' : call.state === 'ringing' ? 'Ringing…' : 'Calling…')}
        </div>
        <button className="btn btn-ghost" style={{ marginTop: 10, color: RED }}
          onClick={() => coordinator.hangup(id)}>{t('Hangup')}</button>
      </div>
    </>)
  }
  const [id, line] = incoming
  const selected = (instances || []).find(item => String(item.id) === String(id))
  const call = line.call
  const browserRouteConfirmed = mediaIngress?.confirmed === true
  const answerable = call.answerable !== false && call.source !== 'backend' &&
    browserRouteConfirmed
  const canConfirmRoute = mediaIngress?.candidate && mediaIngress.confirmed === false
  const declineLabel = call.source === 'backend' ? 'Hang up' : 'Decline'
  const backendDiagnosticOnly = call.source === 'backend' && !call.exactIdentity
  const confirmRoute = async () => {
    if (!canConfirmRoute || mediaBusy) return
    setMediaBusy(true)
    try {
      const next = await api.confirmMediaIngress(
        mediaIngress.candidate.id, mediaIngress.inventory_generation)
      onMediaIngressConfirmed?.(next, id)
      coordinator.reloadLine?.(id)
    } catch (error) {
      coordinator.showToast?.(error?.message || 'Failed to confirm media route')
    } finally {
      setMediaBusy(false)
    }
  }
  return (
    <div style={{ position: 'fixed', inset: 0, zIndex: 1000, background: 'rgba(6,10,20,0.82)',
      backdropFilter: 'blur(3px)', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
      <div className="card" style={{ padding: 40, width: 380, textAlign: 'center',
        boxShadow: '0 20px 60px rgba(0,0,0,.6)', animation: 'none' }}>
        <div style={{ fontSize: 13, color: 'var(--text-mute)', letterSpacing: 1, textTransform: 'uppercase' }}>{t(call.state === 'ending' ? 'Ending incoming call…' : 'Incoming call')}</div>
        <div style={{ margin: '22px 0' }}><Avatar /></div>
        <div className="mono" style={{ fontSize: 26, fontWeight: 800 }}>{call.number || 'Unknown'}</div>
        <div style={{ fontSize: 13, color: 'var(--text-mute)', marginTop: 6 }}>{selected?.name || selected?.id || id}</div>
        {line.incomingSyncError && <div role="status" style={{ fontSize: 12, color: '#f59e0b',
          marginTop: 12, lineHeight: 1.4 }}>
          {t('Incoming call status could not be verified')}: {line.incomingSyncError}
        </div>}
        {!answerable && <div style={{ fontSize: 13, color: '#f59e0b', marginTop: 14, lineHeight: 1.45 }}>
          {t(backendDiagnosticOnly
            ? 'This incoming call is missing its exact Engine identity. It is shown for diagnosis, but cannot be safely answered or hung up from this page.'
            : 'This browser cannot answer yet because its VoWiFi softphone is not registered or the voice route is not confirmed. You can hang up this incoming call, then confirm and test the route before the next call.')}
          {call.hangupError && <div style={{ color: RED, marginTop: 8 }}>{call.hangupError}</div>}
        </div>}
        {!answerable && <div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginTop: 18 }}>
          {canConfirmRoute && <button className="btn btn-primary" disabled={mediaBusy || call.state === 'ending'}
            onClick={confirmRoute}>{t(mediaBusy ? 'Confirming…' : 'Confirm media route')}</button>}
          <button className="btn btn-ghost" onClick={() => onRequestMediaSetup?.(id)}
            disabled={call.state === 'ending'}>{t('Open Calls to test')}</button>
        </div>}
        <div style={{ display: 'flex', justifyContent: 'center', gap: answerable ? 56 : 24, marginTop: 34 }}>
          {!backendDiagnosticOnly && <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 8 }}>
            <button onClick={() => coordinator.decline(id)} style={{ width: 68, height: 68, borderRadius: '50%', border: 'none',
              cursor: call.state === 'ending' ? 'not-allowed' : 'pointer', fontSize: 26, background: RED,
              opacity: call.state === 'ending' ? .55 : 1, color: '#fff' }} disabled={call.state === 'ending'}>✕</button>
            <span style={{ fontSize: 13, color: 'var(--text-soft)' }}>{t(answerable ? 'Decline' : declineLabel)}</span>
          </div>}
          {answerable && <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 8 }}>
            <button onClick={() => coordinator.answer(id)} style={{ width: 68, height: 68, borderRadius: '50%', border: 'none',
              cursor: 'pointer', fontSize: 26, background: GREEN, color: '#fff',
              boxShadow: `0 0 0 0 ${GREEN}`, animation: 'ringpulse 1.4s infinite' }}>✆</button>
            <span style={{ fontSize: 13, color: 'var(--text-soft)' }}>{t('Answer')}</span>
          </div>}
        </div>
      </div>
      <style>{`@keyframes ringpulse{0%{box-shadow:0 0 0 0 ${GREEN}88}70%{box-shadow:0 0 0 16px ${GREEN}00}100%{box-shadow:0 0 0 0 ${GREEN}00}}`}</style>
    </div>
  )
}
