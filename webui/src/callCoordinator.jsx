import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api } from './api.js'
import { NativeBrowserCall, verifyBrowserMedia } from './browserMedia.js'
import { KeyedTrailingRequests } from './keyedTrailingRequests.js'
import { useI18n } from './i18n.jsx'
import {
  backendCallIdentity,
  backendFallbackCall,
  backendPresentationIdentity,
  boundedIdentityMapSet,
  incomingReconcileActive,
  incomingSyncWarningExpected,
  isTerminalBackendCall,
  sameBackendPresentationCall,
  nativeIncomingCall,
  nativeCallbackCurrent,
  nativeDeclineEligible,
  routeNativeHangup,
  selectIncomingOverlayEntry,
  shouldSurfaceIncomingSyncFailure,
  shouldShowBackendFallback,
  stopNativeCall,
} from './vowifiIncomingFallback.js'

const GREEN = '#22c55e'
const RED = '#ef4444'
const INCOMING_RETRY_DELAYS_MS = [1000, 3000, 8000]
const INCOMING_RETRY_COOLDOWN_MS = 30000

const emptyLine = () => ({
  prov: null,
  provisionError: '',
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

export function useCallCoordinator({ enabled, instances, subscribe, showToast }) {
  const mountedRef = useRef(true)
  const enabledRef = useRef(enabled)
  enabledRef.current = enabled
  const nativeCalls = useRef(new Map())
  const nativeBackendIdentities = useRef(new Map())
  const incomingSuppressions = useRef(new Map())
  const incomingAudioFailures = useRef(new Map())
  const incomingCapacityFailures = useRef(new Map())
  const incomingOwnerDiagnostics = useRef(new Map())
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
  const linesRef = useRef({})
  const instancesRef = useRef(instances)
  instancesRef.current = instances
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
    nativeBackendIdentities.current.delete(key)
    if (nativeCall) {
      try { stopNativeCall(nativeCall) } catch {}
    }
    updateLine(key, line => ({
      prov: forgetProvision ? null : line.prov,
      reg: 'idle',
      retryExhausted: false,
      call: line.call?.state === 'ended' ? line.call : null,
    }))
  }, [updateLine])

  const ensureNative = useCallback((id, prov) => {
    const key = String(id || '')
    if (!enabled || !key) return
    updateLine(key, { reg: prov?.browser_media?.inbound || prov?.browser_media?.outbound
      ? 'native' : 'unavailable' })
  }, [enabled, updateLine])

  provisioningHandlers.current = {
    active: key => Boolean(mountedRef.current && enabledRef.current &&
      instanceIdsRef.current.includes(key)),
    run: key => api.softphone(key),
    commit: (key, prov) => {
      const current = linesRef.current[key]
      // Same-generation admission changes affect new calls, not an existing media owner.
      const changed = !current?.prov || current.prov.generation !== prov.generation
      if (changed) stopLine(key, { forgetProvision: true })
      updateLine(key, { prov, provisionError: '', retryExhausted: false, refreshPending: false })
      if (prov?.enabled || prov?.browser_media?.inbound === true) ensureNative(key, prov)
      return prov
    },
  }
  if (!provisioningRequests.current) {
    provisioningRequests.current = new KeyedTrailingRequests({
      active: key => provisioningHandlers.current.active(key),
      run: key => provisioningHandlers.current.run(key),
      commit: (key, value) => provisioningHandlers.current.commit(key, value),
      retryDelaysMs: [1000, 3000, 8000],
      shouldRetry: error => !error?.status || error.status === 408 || error.status >= 500,
      onError: key => updateLine(key, { provisionError: 'Browser voice capability check failed' }),
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
    updateLine(key, { reg: 'idle', provisionError: '', retryExhausted: false, mediaTest: 'idle' })
    loadProvision(key, { fresh: true })
  }, [loadProvision, stopLine, updateLine])

  const startNativeIncoming = useCallback((id, backendCall, { force = false } = {}) => {
    const key = String(id || '')
    const identity = backendCallIdentity(backendCall)
    if (!key || !identity || backendCall?.browser_state !== 'ringing' ||
        linesRef.current[key]?.prov?.browser_media?.inbound !== true) return false
    if (!force && (incomingSuppressions.current.has(identity) ||
        incomingAudioFailures.current.has(identity) ||
        incomingCapacityFailures.current.has(identity) ||
        incomingOwnerDiagnostics.current.has(identity))) return false
    const prior = nativeCalls.current.get(key)
    if (prior?.direction === 'inbound' && prior.matchesBackendCall(backendCall)) return true
    if (prior) {
      try {
        if (prior.direction === 'inbound') prior.closeLocal()
        else prior.hangup()
      } catch {}
      nativeCalls.current.delete(key)
      nativeBackendIdentities.current.delete(key)
    }
    let call = null
    const stillCurrent = () => nativeCallbackCurrent(
      nativeCalls.current, nativeBackendIdentities.current, key, call, identity)
    call = new NativeBrowserCall(
      key, backendCall.peer || backendCall.number || 'Unknown', (type, data) => {
        if (!stillCurrent()) return
        if (type === 'needs-user-gesture') updateLine(key, line => ({
          call: line.call ? { ...line.call, state: 'needs-user-gesture', answerable: false } : null,
        }))
        else if (type === 'ready') updateLine(key, line => ({
          call: line.call ? { ...line.call, state: 'incoming', answerable: true,
            audioError: '' } : null,
        }))
        else if (type === 'answering') updateLine(key, line => ({
          call: line.call ? { ...line.call, state: 'answering', answerable: false } : null,
        }))
        else if (type === 'active') updateLine(key, line => ({
          call: line.call ? { ...line.call, state: 'active', answerable: false,
            startedAt: Date.now() } : null,
        }))
        else if (type === 'ending') updateLine(key, line => ({
          call: line.call ? { ...line.call, state: 'ending', answerable: false } : null,
        }))
        else if (type === 'termination-unconfirmed') updateLine(key, line => ({
          call: line.call ? { ...line.call, state: 'termination_unconfirmed',
            answerable: false, hangupError: 'Call termination could not be confirmed' } : null,
        }))
        else if (type === 'answered-elsewhere') {
          boundedIdentityMapSet(incomingSuppressions.current, identity)
          nativeCalls.current.delete(key)
          nativeBackendIdentities.current.delete(key)
          updateLine(key, {
            call: { ...backendFallbackCall(key, backendCall), state: 'answering_elsewhere',
              backendState: 'claiming' },
          })
        } else if (type === 'ended') {
          nativeCalls.current.delete(key)
          nativeBackendIdentities.current.delete(key)
          rememberBackendTerminalCall(identity)
          clearCallSoon(key, data?.cause)
        } else if (type === 'failed') {
          nativeCalls.current.delete(key)
          nativeBackendIdentities.current.delete(key)
          const category = String(data?.category || 'audio-failed')
          if (category === 'terminal') {
            rememberBackendTerminalCall(identity); clearCallSoon(key, data?.cause); return
          }
          if (category === 'answered-elsewhere' || category === 'occupied')
            boundedIdentityMapSet(incomingSuppressions.current, identity, category)
          else if (category === 'capacity')
            boundedIdentityMapSet(incomingCapacityFailures.current, identity, data?.cause)
          else if (category === 'owner-unavailable')
            boundedIdentityMapSet(incomingOwnerDiagnostics.current, identity, data?.cause)
          else if (category !== 'ending')
            boundedIdentityMapSet(incomingAudioFailures.current, identity, data?.cause)
          const fallback = backendFallbackCall(key, backendCall)
          updateLine(key, { call: {
            ...fallback,
            state: category === 'ending' ? 'ending'
              : category === 'owner-unavailable' ? 'termination_unconfirmed'
                : category === 'answered-elsewhere' || category === 'occupied' ||
                  category === 'capacity' ? 'answering_elsewhere' : 'incoming',
            audioError: data?.cause || '', retryableAudio: category === 'audio-failed',
            reason: category,
          } })
        }
      }, { direction: 'inbound', backendCall })
    nativeCalls.current.set(key, call)
    nativeBackendIdentities.current.set(key, identity)
    updateLine(key, { call: nativeIncomingCall(key, backendCall, 'preparing') })
    call.start()
    return true
  }, [clearCallSoon, rememberBackendTerminalCall, updateLine])

  const applyBackendIncoming = useCallback((id, call, { authoritative = false } = {}) => {
    const key = String(id || '')
    if (!key || !call || call.direction !== 'in') return
    const identity = backendCallIdentity(call)
    if (isTerminalBackendCall(call)) {
      const native = nativeCalls.current.get(key)
      if (native?.direction === 'inbound' && native.matchesBackendCall(call)) {
        nativeCalls.current.delete(key)
        nativeBackendIdentities.current.delete(key)
        try { native.closeLocal() } catch {}
      }
      for (const map of [incomingSuppressions.current, incomingAudioFailures.current,
        incomingCapacityFailures.current, incomingOwnerDiagnostics.current]) map.delete(identity)
      rememberBackendTerminalCall(identity || backendPresentationIdentity(call))
      const current = linesRef.current[key]?.call
      if (sameBackendPresentationCall(current, call) ||
          String(current?.backendCallId || '') === String(call.id ?? ''))
        clearCallSoon(key, call.status || 'ended')
      return
    }
    const provision = linesRef.current[key]?.prov
    const browserState = String(call.browser_state || '')
    if (authoritative && identity && provision?.browser_media?.inbound === true) {
      const native = nativeCalls.current.get(key)
      if (native?.direction === 'inbound') {
        if (!native.matchesBackendCall(call)) {
          nativeCalls.current.delete(key); nativeBackendIdentities.current.delete(key)
          try { native.closeLocal() } catch {}
        } else if (browserState === 'ringing') return
        else if (browserState === 'unknown') {
          nativeCalls.current.delete(key); nativeBackendIdentities.current.delete(key)
          try { native.closeLocal() } catch {}
          boundedIdentityMapSet(incomingOwnerDiagnostics.current, identity,
            'Incoming call recovery is required')
        } else if (native.ownsBackendCall(call)) {
          updateLine(key, line => ({ call: line.call ? {
            ...line.call, backendState: browserState,
            browserRevision: Number(call.browser_revision),
            browserOwnerSession: String(call.browser_owner_session || ''),
            browserOperation: String(call.browser_operation || ''),
            browserEpoch: String(call.browser_epoch || ''),
          } : null }))
          if (browserState === 'ending') updateLine(key, line => ({
            call: line.call ? { ...line.call, state: line.call.state === 'termination_unconfirmed'
              ? line.call.state : 'ending', answerable: false } : null,
          }))
          else if (browserState === 'active') updateLine(key, line => ({
            call: line.call ? { ...line.call, state: 'active', answerable: false } : null,
          }))
          return
        } else {
          nativeCalls.current.delete(key); nativeBackendIdentities.current.delete(key)
          try { native.closeLocal() } catch {}
          boundedIdentityMapSet(incomingSuppressions.current, identity, 'owned-elsewhere')
        }
      }
      const currentUiCall = linesRef.current[key]?.call
      const mayStart = !currentUiCall || currentUiCall.state === 'ended' ||
        currentUiCall.source === 'backend'
      if (browserState === 'ringing' && mayStart && startNativeIncoming(key, call)) return
      updateLine(key, { call: backendFallbackCall(key, call) })
      return
    }
    updateLine(key, line => {
      if (!shouldShowBackendFallback(
        line.call, call, backendTerminalCalls.current, authoritative)) return {}
      return { call: backendFallbackCall(key, call) }
    })
  }, [clearCallSoon, rememberBackendTerminalCall, startNativeIncoming, updateLine])

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
      else {
        const native = nativeCalls.current.get(key)
        if (native?.direction === 'inbound') {
          nativeCalls.current.delete(key)
          nativeBackendIdentities.current.delete(key)
          try { native.closeLocal() } catch {}
        }
        updateLine(key, line => ({
          call: ['backend', 'native-wss-incoming'].includes(line.call?.source)
            ? null : line.call,
          incomingSyncError: '',
        }))
      }
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
          const instance = (instancesRef.current || []).find(
            item => String(item.id) === key)
          if (incomingSyncWarningExpected(instance, linesRef.current[key])) {
            updateLine(key, {
              incomingSyncError: requestError?.message || 'Incoming-call sync failed',
            })
            showToastRef.current?.(
              'Incoming-call status could not be verified; automatic retry is paused')
          } else {
            updateLine(key, { incomingSyncError: '' })
          }
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

  useEffect(() => {
    if (!enabled) return
    for (const [key, line] of Object.entries(lines)) {
      const call = line.call
      if (line.prov?.browser_media?.inbound !== true || call?.source !== 'backend' ||
          call.backendState !== 'ringing') continue
      const backend = {
        id: call.backendCallId, source_call_id: call.sourceCallId,
        engine_run_id: call.engineRunId, browser_revision: call.browserRevision,
        browser_state: call.backendState, peer: call.number,
      }
      const identity = backendCallIdentity(backend)
      if (!identity || incomingSuppressions.current.has(identity) ||
          incomingAudioFailures.current.has(identity) ||
          incomingCapacityFailures.current.has(identity) ||
          incomingOwnerDiagnostics.current.has(identity)) continue
      startNativeIncoming(key, backend)
    }
  }, [enabled, lines, startNativeIncoming])

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
      const keys = new Set([...nativeCalls.current.keys(),
        ...Object.keys(linesRef.current)])
      for (const key of keys) stopLine(key, { forgetProvision: true })
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
      else if ((line.prov.enabled || line.prov?.browser_media?.inbound === true) &&
        !line.retryExhausted && line.reg !== 'native')
        ensureNative(id, line.prov)
    })
  }, [cancelReconcile, enabled, ensureNative, instanceIds, instanceIdsKey, loadProvision, stopLine])

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
      if (message.type === 'ws-lifecycle' && message.event === 'open') {
        instanceIdsRef.current.forEach(key => {
          reconcileOpenIncoming(key)
          const line = linesRef.current[key]
          if (!line?.prov && !line?.call) {
            updateLine(key, { provisionError: '' })
            loadProvision(key, { fresh: true })
          }
        })
      }
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
  }, [enabled, instanceIdsKey, loadProvision, reconcileOpenIncoming, reloadLine, subscribe, updateLine])

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
      for (const call of nativeCalls.current.values()) {
        try { stopNativeCall(call) } catch {}
      }
      nativeCalls.current.clear()
      nativeBackendIdentities.current.clear()
      incomingSuppressions.current.clear()
      incomingAudioFailures.current.clear()
      incomingCapacityFailures.current.clear()
      incomingOwnerDiagnostics.current.clear()
    }
  }, [])

  const terminateExactIncoming = useCallback((id, current, disposition = 'hangup') => {
    const key = String(id || '')
    if (!current?.exactIdentity || !current.backendCallId || !current.sourceCallId ||
        current.state === 'ending') return false
    const captured = {
      id: current.backendCallId, source_call_id: current.sourceCallId,
      engine_run_id: current.engineRunId,
    }
    const sameUiCall = () => {
      const value = linesRef.current[key]?.call
      return Boolean(value && String(value.backendCallId || '') === String(captured.id) &&
        String(value.sourceCallId || '') === String(captured.source_call_id) &&
        String(value.engineRunId || '') === String(captured.engine_run_id))
    }
    updateLine(key, line => ({ call: line.call ? {
      ...line.call, state: 'ending', answerable: false, hangupError: '',
    } : null }))
    api.hangupIncomingVowifiCall(
      key, captured.id, captured.source_call_id, captured.engine_run_id, disposition,
    ).then(result => {
      if (!mountedRef.current || !sameUiCall()) return
      if (!result?.terminal_confirmed) {
        updateLine(key, line => ({ call: line.call ? {
          ...line.call, state: 'termination_unconfirmed',
          hangupError: 'Call termination could not be confirmed',
        } : null }))
        return
      }
      const native = nativeCalls.current.get(key)
      if (native?.direction === 'inbound' && native.matchesBackendCall(captured)) {
        nativeCalls.current.delete(key); nativeBackendIdentities.current.delete(key)
        try { native.closeLocal() } catch {}
      }
      rememberBackendTerminalCall(backendCallIdentity(captured))
      clearCallSoon(key, disposition === 'decline' &&
        result.decline_disposition_confirmed ? 'Rejected' : 'Call ended')
    }).catch(error => {
      if (!mountedRef.current || !sameUiCall()) return
      const detail = error?.data?.detail || {}
      updateLine(key, line => ({ call: line.call ? {
        ...line.call, state: 'termination_unconfirmed',
        hangupError: detail.message || error?.message ||
          'Call termination could not be confirmed',
        declineDispositionUnconfirmed: detail.decline_disposition_unconfirmed === true,
      } : null }))
      showToastRef.current?.(detail.message || error?.message || 'Hangup failed')
    })
    return true
  }, [clearCallSoon, rememberBackendTerminalCall, updateLine])

  const actions = {
    call: (id, number, testObserver = null) => {
      const key = String(id || '')
      const existing = linesRef.current[key]?.call
      if (existing && existing.state !== 'ended') {
        showToastRef.current?.('This line is already in use')
        return false
      }
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
            nativeBackendIdentities.current.delete(key)
            clearCallSoon(key, data?.cause)
          }
          // The manual stability tool observes this exact call; it does not create a parallel
          // dialling path or attach itself to health polling.
          try { testObserver?.(type, { ...data, stats: { ...call.stats } }) } catch {}
        })
        nativeCalls.current.set(key, call)
        call.start()
        return true
      }
      showToastRef.current?.('Native outbound calling is unavailable on this Engine')
      return false
    },
    runStabilityTest: (id, number, activeSeconds = 50) => {
      const key = String(id || '')
      const duration = Math.max(10, Math.min(300, Number(activeSeconds) || 50))
      return new Promise((resolve, reject) => {
        let settled = false
        let activeAt = 0
        let activeTimer = null
        let setupTimer = null
        let terminalTimer = null
        const cleanup = () => {
          clearTimeout(activeTimer); clearTimeout(setupTimer); clearTimeout(terminalTimer)
        }
        const finish = (error, value) => {
          if (settled) return
          settled = true; cleanup()
          if (error) reject(error)
          else resolve(value)
        }
        const exactHangup = () => {
          const native = nativeCalls.current.get(key)
          if (native) native.hangup()
        }
        const verifyTerminal = async (stats) => {
          try {
            const facts = await api.verifyLinePassive(key)
            if (facts?.facts?.work?.code !== 'idle') {
              throw new Error('Call termination was not verified: active channel remains')
            }
            const measured = activeAt ? Math.max(0, (Date.now() - activeAt) / 1000) : 0
            finish(null, {
              passed: Boolean(activeAt && measured >= duration * 0.9),
              reason: !activeAt ? 'Call ended before becoming active'
                : measured < duration * 0.9 ? 'Call ended before the requested stability duration'
                  : '',
              active_seconds: measured, requested_active_seconds: duration, stats, facts,
            })
          } catch (error) { finish(error) }
        }
        const requestTerminalVerification = (stats = {}) => {
          exactHangup()
          clearTimeout(terminalTimer)
          // If the browser never receives the terminal event, the server-side 10s heartbeat
          // still owns billing protection.  Wait beyond that boundary, then require a fresh
          // Engine idle sample rather than reporting success from a closed browser socket.
          terminalTimer = setTimeout(() => { void verifyTerminal(stats) }, 12000)
        }
        const started = actions.call(key, number, (type, data = {}) => {
          if (type === 'active' && !activeAt) {
            activeAt = Date.now()
            activeTimer = setTimeout(() => requestTerminalVerification(data.stats || {}),
              duration * 1000)
            return
          }
          if (type === 'failed') {
            finish(new Error(data.cause || 'Call stability test failed'))
            return
          }
          if (type === 'ended') {
            // Let the terminal event propagate before using the independent passive channel
            // snapshot. This is evidence that billing-sensitive work really reached zero.
            clearTimeout(terminalTimer)
            terminalTimer = setTimeout(() => { void verifyTerminal(data.stats || {}) }, 750)
          }
        })
        if (!started) {
          finish(new Error('This line is already in use or browser calling is unavailable'))
          return
        }
        // A ringing/failed setup is not a stability result. Stop the exact local call session
        // after a bounded window; the existing server-side 10s media heartbeat remains the
        // separate lost-browser billing protection.
        setupTimer = setTimeout(() => {
          if (!activeAt) {
            requestTerminalVerification()
            finish(new Error('Call did not become active before the stability-test setup deadline'))
          }
        }, 75000)
      })
    },
    answer: (id) => {
      const native = nativeCalls.current.get(String(id || ''))
      return native?.direction === 'inbound' ? native.answer() : false
    },
    decline: (id) => {
      const key = String(id || '')
      const current = linesRef.current[key]?.call
      if (['backend', 'native-wss-incoming'].includes(current?.source)) {
        const native = nativeCalls.current.get(key)
        const localDecline = nativeDeclineEligible(current)
        if (localDecline && native?.direction === 'inbound') try { native.closeLocal() } catch {}
        return terminateExactIncoming(key, current, localDecline ? 'decline' : 'hangup')
      }
      return false
    },
    hangup: (id) => {
      const key = String(id || '')
      const nativeCall = nativeCalls.current.get(key)
      if (nativeCall) {
        if (nativeCall.direction === 'inbound') {
          const routed = routeNativeHangup(nativeCall, () => {
            nativeCalls.current.delete(key)
            nativeBackendIdentities.current.delete(key)
            const current = linesRef.current[key]?.call
            return current?.exactIdentity
              ? terminateExactIncoming(key, current, 'hangup') : false
          })
          if (routed.route === 'wss') updateLine(key, line => ({ call: line.call ? {
            ...line.call, state: 'ending', answerable: false,
          } : null }))
          return Boolean(routed.result)
        }
        nativeCalls.current.delete(key); nativeCall.hangup()
        nativeBackendIdentities.current.delete(key)
        updateLine(key, line => ({ call: line.call ? {
          ...line.call, state: 'ended', endCause: line.call.endCause,
        } : null }))
        clearCallSoon(key, undefined)
        return true
      }
      const current = linesRef.current[key]?.call
      if (current?.source === 'backend' && current.exactIdentity)
        return terminateExactIncoming(key, current, 'hangup')
      return false
    },
    sendDTMF: (id, tone) => nativeCalls.current.get(String(id || ''))?.sendDTMF(tone) || false,
    setMuted: (id, muted) => nativeCalls.current.get(String(id || ''))?.setMuted(muted) || false,
    enableIncomingAudio: (id) => {
      const native = nativeCalls.current.get(String(id || ''))
      return native?.direction === 'inbound'
        ? native.enableAudioFromGesture() : Promise.resolve(false)
    },
    retryIncomingAudio: (id) => {
      const key = String(id || '')
      const current = linesRef.current[key]?.call
      const identity = current ? backendCallIdentity({
        id: current.backendCallId, source_call_id: current.sourceCallId,
        engine_run_id: current.engineRunId,
      }) : null
      if (!identity || !current?.retryableAudio) return false
      incomingAudioFailures.current.delete(identity)
      const prior = nativeCalls.current.get(key)
      if (prior?.direction === 'inbound') try { prior.closeLocal() } catch {}
      nativeCalls.current.delete(key)
      nativeBackendIdentities.current.delete(key)
      return startNativeIncoming(key, {
        id: current.backendCallId, source_call_id: current.sourceCallId,
        engine_run_id: current.engineRunId, browser_revision: current.browserRevision,
        browser_state: current.backendState || 'ringing', peer: current.number,
      }, { force: true })
    },
    recheckIncoming: (id) => {
      const key = String(id || '')
      const current = linesRef.current[key]?.call
      const identity = current ? backendCallIdentity({
        id: current.backendCallId, source_call_id: current.sourceCallId,
        engine_run_id: current.engineRunId,
      }) : null
      if (identity) incomingOwnerDiagnostics.current.delete(identity)
      return reconcileOpenIncoming(key)
    },
    startRecording: () => Promise.resolve(false),
    stopRecording: () => Promise.resolve(null),
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
  }

  return {
    lines,
    line: (id) => lines[String(id || '')] || emptyLine(),
    ...actions,
    showToast,
  }
}

export function GlobalCallOverlay({ coordinator, instances }) {
  const { t } = useI18n()
  const incoming = selectIncomingOverlayEntry(coordinator?.lines || {})
  const live = incoming || Object.entries(coordinator?.lines || {}).find(
    ([, line]) => line.call?.transport === 'vowifi' &&
      ['checking', 'calling', 'ringing', 'active', 'active_elsewhere'].includes(line.call?.state))
  const syncIssue = Object.entries(coordinator?.lines || {}).find(
    ([id, line]) => Boolean(line.incomingSyncError) && incomingSyncWarningExpected(
      (instances || []).find(item => String(item.id) === String(id)), line))
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
  const nativeInbound = call.source === 'native-wss-incoming' ||
    line.prov?.browser_media?.inbound === true
  const answerable = call.source === 'native-wss-incoming' && call.state === 'incoming' &&
    call.answerable !== false && call.exactIdentity
  const nativeDeclineStage = nativeDeclineEligible(call)
  const declineLabel = nativeDeclineStage ? 'Decline' : 'Hang up'
  const terminateIncoming = () => nativeDeclineStage
    ? coordinator.decline(id) : coordinator.hangup(id)
  const backendDiagnosticOnly = call.source === 'backend' && !call.exactIdentity
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
            : nativeInbound
              ? call.state === 'needs-user-gesture' ? 'Enable browser audio, then wait for the no-charge Echo proof before answering.'
                : call.state === 'preparing' ? 'Preparing this browser audio without answering the carrier call…'
                  : ['answering_elsewhere', 'active_elsewhere'].includes(call.state)
                    ? 'This line is being answered or used by another browser.'
                    : call.state === 'answering' ? 'Answering after exact media and call ownership checks…'
                      : call.reason === 'owner-unavailable' ? 'The live call owner could not be verified. Recheck the call state or hang up the exact call.'
                        : call.retryableAudio ? 'This browser could not prepare microphone and audio. You can retry locally or hang up the exact call.'
                          : 'This browser cannot answer this call; the exact backend call remains visible for safe hangup.'
              : 'This Engine does not provide same-origin browser voice. The exact call remains available for hangup.')}
          {call.hangupError && <div style={{ color: RED, marginTop: 8 }}>{call.hangupError}</div>}
        </div>}
        {!answerable && <div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginTop: 18 }}>
          {nativeInbound && call.state === 'needs-user-gesture' &&
            <button className="btn btn-primary" disabled={call.state === 'ending'}
              onClick={() => { coordinator.enableIncomingAudio(id)?.catch(error =>
                coordinator.showToast?.(error?.message || 'Failed to enable browser audio')) }}>
              {t('Enable audio')}
            </button>}
          {nativeInbound && call.retryableAudio &&
            <button className="btn btn-primary" disabled={call.state === 'ending'}
              onClick={() => coordinator.retryIncomingAudio(id)}>{t('Retry browser audio')}</button>}
          {nativeInbound && call.reason === 'owner-unavailable' &&
            <button className="btn btn-primary" disabled={call.state === 'ending'}
              onClick={() => coordinator.recheckIncoming(id)}>{t('Recheck call state')}</button>}
        </div>}
        <div style={{ display: 'flex', justifyContent: 'center', gap: answerable ? 56 : 24, marginTop: 34 }}>
          {!backendDiagnosticOnly && <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 8 }}>
            <button onClick={terminateIncoming} style={{ width: 68, height: 68, borderRadius: '50%', border: 'none',
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
