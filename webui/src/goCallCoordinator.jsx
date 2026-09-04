import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api } from './api.js'
import { getCallAudioBufferMS } from './browserPreferences.js'
import { CallMedia, normalizeDialTarget } from './goCallMedia.js'
import { operationID } from './goV1Adapter.js'
import { useI18n } from './i18n.jsx'

function routesFor(instances) {
  const result = []
  for (const line of instances || []) {
    if (line.operations?.vowifi_call?.ready) result.push({ line, mode: 'vowifi' })
    if (line.operations?.cellular_call?.ready) result.push({ line, mode: 'cellular' })
  }
  return result
}
function ambiguousStart(error) {
  const code = error?.code || error?.data?.code || error?.data?.detail?.code
  return !error?.status || error.status >= 500 || [
    'operation_timeout', 'provider_transport_failed', 'invalid_provider_response',
    'cellular_call_start_uncertain', 'cellular_incoming_answer_uncertain',
  ].includes(code)
}

function terminalMissing(error) {
  const code = error?.code || error?.data?.code || error?.data?.detail?.code
  return code === 'call_not_found' || code === 'cellular_call_not_found'
}

function activeStatus(status, call) {
  if (!status || !call?.lease?.session_id) return false
  if (call.mode === 'cellular')
    return (status.sessions || []).some(session => session.session_id === call.lease.session_id && session.phase === 'active')
  return status.active_call?.call_id === call.call_id
}

function presentStatus(status, call) {
  if (!status || !call) return null
  if (call.mode === 'cellular')
    return (status.sessions || []).some(session => session.session_id === call.lease?.session_id)
  return status.active_call?.call_id === call.call_id
}

export function useGoCallCoordinator({ enabled, instances, subscribe, showToast }) {
  const routes = useMemo(() => routesFor(instances), [instances])
  const [current, setCurrent] = useState(null)
  const currentRef = useRef(null)
  const [statuses, setStatuses] = useState({})
  const [cellularIncoming, setCellularIncoming] = useState([])
  const [history, setHistory] = useState([])
  const [historyLoading, setHistoryLoading] = useState(false)
  const showToastRef = useRef(showToast)
  showToastRef.current = showToast

  const update = useCallback((call, patch) => {
    if (currentRef.current !== call) return
    Object.assign(call, patch)
    setCurrent({ ...call })
  }, [])

  const release = useCallback(async call => {
    if (!call) return
    call.cancelled = true
    call.media?.close()
    if (currentRef.current === call) {
      currentRef.current = null
      setCurrent(null)
    }
    if (call.lease?.session_id) {
      try { await api.releaseCallMediaLease(call.mode, call.lease.session_id) } catch {}
    }
  }, [])

  const loadHistory = useCallback(async () => {
    setHistoryLoading(true)
    try { setHistory((await api.callHistoryV1()).calls || []) }
    catch (error) { showToastRef.current?.(error.message) }
    finally { setHistoryLoading(false) }
  }, [])

  const refreshStatuses = useCallback(async () => {
    if (!enabled) return
    const lines = instances || []
    const values = await Promise.all(lines.flatMap(line => ['vowifi', 'cellular'].map(async mode => {
      try { return [`${mode}:${line.id}`, { status: await api.callTransportStatus(line.id, mode) }] }
      catch (error) { return [`${mode}:${line.id}`, { error }] }
    })))
    const next = Object.fromEntries(values)
    setStatuses(next)
    const call = currentRef.current
    if (!call || !call.lease?.session_id) return
    const entry = next[`${call.mode}:${call.line_id}`]
    if (!entry?.status) return
    if (call.phase === 'start_unknown' && activeStatus(entry.status, call)) {
      call.media?.markActive()
      update(call, { phase: 'active', message: 'The same call is confirmed active' })
    } else if (['active', 'media_failed', 'ending'].includes(call.phase) && presentStatus(entry.status, call) === false) {
      showToastRef.current?.('The backend confirmed the call ended')
      await release(call)
      await loadHistory()
    }
  }, [enabled, instances, loadHistory, release, update])

  useEffect(() => {
    if (!enabled) return undefined
    void refreshStatuses(); void loadHistory()
    const timer = setInterval(refreshStatuses, 5000)
    return () => clearInterval(timer)
  }, [enabled, refreshStatuses, loadHistory])

  useEffect(() => subscribe?.(message => {
    if (message.type !== 'go.snapshot') return
    setCellularIncoming((message.raw?.cellular_calls || []).filter(call => call.state === 'ringing_in'))
    void refreshStatuses()
  }), [subscribe, refreshStatuses])

  useEffect(() => {
    const stopEvidence = () => currentRef.current?.media?.close()
    window.addEventListener('pagehide', stopEvidence)
    return () => window.removeEventListener('pagehide', stopEvidence)
  }, [])

  useEffect(() => {
    if (enabled) return
    currentRef.current?.media?.close()
  }, [enabled])

  const mediaEvent = useCallback((call, type, detail) => {
    if (currentRef.current !== call) return
    if (type === 'reconnecting') update(call, { message: detail, media_state: 'reconnecting' })
    else if (type === 'reconnected') update(call, { message: 'Media reconnected', media_state: 'ready' })
    else if (type === 'ended') {
      update(call, { message: detail || 'Call ended', phase: 'ended' })
      void release(call).then(loadHistory)
    } else if (type === 'failed' && call.phase === 'active') {
      call.media.close()
      update(call, { phase: 'media_failed', media_state: 'failed',
        message: `${detail}. The 10-second exact call guard will terminate this call.` })
    }
  }, [loadHistory, release, update])

  const submitStart = useCallback(async call => {
    if (currentRef.current !== call || call.cancelled || !['ready', 'start_unknown'].includes(call.phase)) return
    update(call, { phase: 'signalling', message: 'Bidirectional audio passed; submitting the carrier call' })
    try {
      const result = await api.startCallV1(call, call.direction === 'incoming')
      if (currentRef.current !== call || call.cancelled) return
      call.media.markActive()
      update(call, { phase: 'active', media_state: 'ready', message: result.code || 'Call active', started_at: Date.now() })
      void refreshStatuses(); void loadHistory()
    } catch (error) {
      if (currentRef.current !== call || call.cancelled) return
      if (ambiguousStart(error)) {
        update(call, { phase: 'start_unknown', message: call.mode === 'cellular'
          ? `${error.message}. The call will not be submitted again; hang up or wait for the 10-second guard.`
          : `${error.message}. Retry will reuse the same call identity.` })
      } else {
        showToastRef.current?.(`Call failed: ${error.message}`)
        await release(call)
        void refreshStatuses(); void loadHistory()
      }
    }
  }, [loadHistory, refreshStatuses, release, update])

  const begin = useCallback(async ({ line, mode, callee, bufferMS, incoming }) => {
    if (currentRef.current) throw new Error('another call is already owned by this browser')
    const value = incoming ? (mode === 'cellular' ? incoming.number : incoming.caller) : normalizeDialTarget(callee)
    const call = {
      mode, line_id: String(line.id), expected_card_id: String(line.iccid || line.card_id || ''),
      callee: value || 'Incoming call', buffer_ms: Number(bufferMS),
      call_id: mode === 'cellular' && incoming ? incoming.incoming_event_id : operationID('browser-call'),
      start_operation_id: operationID(incoming ? 'react-call-answer' : 'react-call-start'),
      end_operation_id: '', lease: null, phase: 'preparing', media_state: 'opening',
      message: 'Requesting microphone and bidirectional audio evidence',
      direction: incoming ? 'incoming' : 'outgoing', incoming: incoming || null,
      muted: false, cancelled: false,
    }
    const media = new CallMedia(call.buffer_ms, (type, detail) => mediaEvent(call, type, detail))
    call.media = media
    media.openAudioFromGesture()
    currentRef.current = call
    setCurrent({ ...call })
    try {
      const leaseBody = mode === 'cellular' ? {
        line_id: call.line_id, call_id: call.call_id, expected_card_id: call.expected_card_id,
        ...(incoming ? {
          operation_id: call.start_operation_id, incoming_event_id: incoming.incoming_event_id,
          sim_session_generation: incoming.sim_session_generation,
          native_call_index: incoming.native_call_index, call_occurrence: incoming.occurrence,
        } : {}),
      } : { line_id: call.line_id, call_id: call.call_id }
      call.lease = await api.createCallMediaLease(mode, leaseBody)
      if (currentRef.current !== call || call.cancelled) {
        await api.releaseCallMediaLease(mode, call.lease.session_id).catch(() => {})
        return null
      }
      await media.prepare(call.lease, call.call_id)
      if (currentRef.current !== call || call.cancelled) return null
      update(call, { phase: 'ready', media_state: 'ready', message: 'Bidirectional audio evidence passed' })
      await submitStart(call)
      return call
    } catch (error) {
      if (currentRef.current === call && !call.cancelled && !['active', 'start_unknown'].includes(call.phase)) {
        showToastRef.current?.(`Pre-call check failed: ${error.message}. No carrier call was confirmed.`)
        await release(call)
      }
      throw error
    }
  }, [mediaEvent, release, submitStart, update])

  const startOutgoing = useCallback((lineID, mode, callee, bufferMS) => {
    const route = routes.find(value => value.mode === mode && String(value.line.id) === String(lineID))
    if (!route) return Promise.reject(new Error('the selected call route is not ready'))
    return begin({ line: route.line, mode, callee, bufferMS })
  }, [begin, routes])

  const answerIncoming = useCallback((lineID, mode, incoming, bufferMS) => {
    const line = (instances || []).find(value => String(value.id) === String(lineID))
    if (!line) return Promise.reject(new Error('incoming line is no longer configured'))
    if (mode === 'cellular' && incoming.actionable !== true)
      return Promise.reject(new Error(incoming.blocked || 'incoming call is not actionable'))
    return begin({ line, mode, incoming, bufferMS })
  }, [begin, instances])

  const rejectIncoming = useCallback(async (lineID, mode, incoming) => {
    try {
      const result = await api.rejectIncomingCallV1(lineID, mode, incoming)
      showToastRef.current?.(result.code || 'Incoming call rejected')
    } catch (error) {
      showToastRef.current?.(mode === 'cellular'
        ? `${error.message}. MDD will not blindly repeat CHUP; wait for the authoritative Agent snapshot.`
        : error.message)
      throw error
    } finally { void refreshStatuses(); void loadHistory() }
  }, [loadHistory, refreshStatuses])

  const hangup = useCallback(async () => {
    const call = currentRef.current
    if (!call || call.ending) return
    call.ending = true
    call.media?.close()
    if (!call.end_operation_id) call.end_operation_id = operationID('react-call-end')
    update(call, { phase: 'ending', message: 'Sending the exact hangup; media evidence has stopped' })
    if (!call.lease?.session_id) { await release(call); return }
    try {
      const result = await api.hangupCallV1(call)
      showToastRef.current?.(result.code || 'Call ended')
      await release(call); await refreshStatuses(); await loadHistory()
    } catch (error) {
      if (terminalMissing(error)) {
        await release(call); await refreshStatuses(); await loadHistory(); return
      }
      call.ending = false
      update(call, { phase: 'ending', message: `${error.message}. Hangup is unconfirmed; the 10-second guard remains active.` })
    }
  }, [loadHistory, refreshStatuses, release, update])

  const retryStart = useCallback(() => {
    const call = currentRef.current
    if (call?.phase !== 'start_unknown' || call.mode === 'cellular') return Promise.resolve()
    return submitStart(call)
  }, [submitStart])

  const sendDTMF = useCallback(signal => {
    const call = currentRef.current
    if (!call || call.phase !== 'active' || !/^[0-9*#]$/.test(signal)) return Promise.resolve()
    return api.callDTMFV1(call, signal)
  }, [])

  const toggleMute = useCallback(() => {
    const call = currentRef.current
    if (!call || call.phase !== 'active') return
    call.muted = !call.muted
    call.media?.setMuted(call.muted)
    update(call, { muted: call.muted })
  }, [update])

  const deleteHistory = useCallback(async ids => {
    await api.deleteCallHistoryV1(ids)
    await loadHistory()
  }, [loadHistory])

  const incoming = useMemo(() => {
    const values = []
    for (const line of instances || []) {
      const call = statuses[`vowifi:${line.id}`]?.status?.pending_incoming_call
      if (call) values.push({ line, mode: 'vowifi', call })
    }
    for (const call of cellularIncoming) {
      const line = (instances || []).find(value => String(value.id) === String(call.line_id))
      if (line) values.push({ line, mode: 'cellular', call })
    }
    return values
  }, [instances, statuses, cellularIncoming])

  const line = useCallback(id => {
    const instance = (instances || []).find(value => String(value.id) === String(id))
    const call = current && String(current.line_id) === String(id) ? current : null
    return {
      reg: instance?.operations?.vowifi_call?.ready ? 'native' : 'unavailable',
      prov: { browser_media: {
        outbound: instance?.operations?.vowifi_call?.ready === true,
        inbound: instance?.operations?.vowifi_call?.ready === true,
      } }, call,
    }
  }, [instances, current])

  const verifyMedia = useCallback(async lineID => {
    if (currentRef.current) throw new Error('a call is already active')
    const lineValue = (instances || []).find(value => String(value.id) === String(lineID))
    if (!lineValue) throw new Error('line not found')
    const callID = operationID('react-media-canary')
    const media = new CallMedia(500)
    media.openAudioFromGesture()
    let lease
    try {
      lease = await api.createCallMediaLease('vowifi', { line_id: lineID, call_id: callID })
      await media.prepare(lease, callID)
    } finally {
      media.close()
      if (lease?.session_id) await api.releaseCallMediaLease('vowifi', lease.session_id).catch(() => {})
    }
  }, [instances])

  return {
    routes, current, incoming, statuses, history, historyLoading,
    refresh: refreshStatuses, loadHistory, startOutgoing, answerIncoming, rejectIncoming,
    hangup, retryStart, sendDTMF, toggleMute, deleteHistory, line, lines: {}, verifyMedia,
  }
}

export function GlobalGoCallOverlay({ coordinator }) {
  const { t } = useI18n()
  const call = coordinator?.current
  const incoming = coordinator?.incoming?.[0]
  const [, tick] = useState(0)
  useEffect(() => {
    if (!call?.started_at) return undefined
    const timer = setInterval(() => tick(value => value + 1), 1000)
    return () => clearInterval(timer)
  }, [call?.started_at])
  if (!call && !incoming) return null
  if (!call && incoming) return <div className="u-global-call"><div><b>{t('Incoming call')}</b><span>{incoming.call.number || incoming.call.caller || t('Unknown')}</span><small>{incoming.line.name || incoming.line.id} · {incoming.mode}</small></div><div className="u-inline"><button className="btn btn-primary" disabled={incoming.mode === 'cellular' && incoming.call.actionable !== true} onClick={() => coordinator.answerIncoming(incoming.line.id, incoming.mode, incoming.call, getCallAudioBufferMS()).catch(() => {})}>{t('Answer')}</button><button className="btn btn-danger" onClick={() => coordinator.rejectIncoming(incoming.line.id, incoming.mode, incoming.call).catch(() => {})}>{t('Reject')}</button></div></div>
  const seconds = call.started_at ? Math.max(0, Math.floor((Date.now() - call.started_at) / 1000)) : 0
  return <div className="u-global-call"><div><b>{call.callee}</b><span>{call.mode} · {call.phase} · {Math.floor(seconds / 60)}:{String(seconds % 60).padStart(2, '0')}</span><small>{call.message}</small></div><div className="u-inline">{call.phase === 'active' && <button className="btn btn-ghost" onClick={coordinator.toggleMute}>{t(call.muted ? 'Unmute' : 'Mute')}</button>}<button className="btn btn-danger" onClick={coordinator.hangup}>{t('Hang up')}</button></div></div>
}
