import React, { useEffect, useRef, useState, useCallback } from 'react'
import { api } from '../api.js'
import { boundedCellularRelease, refreshCellularMediaState } from '../cellularMediaMonitor.js'
import SimSelector from './SimSelector.jsx'
import { lineCallReadinessStatus } from '../linePresentation.js'
import { useI18n } from '../i18n.jsx'
import { shouldRefreshRemoteSim } from '../remoteSimRefresh.js'

const GREEN = '#22c55e', RED = '#ef4444'
const KEYS = [['1', ''], ['2', 'ABC'], ['3', 'DEF'], ['4', 'GHI'], ['5', 'JKL'],
  ['6', 'MNO'], ['7', 'PQRS'], ['8', 'TUV'], ['9', 'WXYZ'], ['*', ''], ['0', '+'], ['#', '']]

const fmtDur = (s) => `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`

export const normalizeDialTarget = (value) => {
  let number = String(value || '').replace(/[\s().-]/g, '')
  // Carrier service short codes (balance, voicemail, support, etc.) are intentionally
  // dialled as-is and are not E.164 numbers. Keep the bound tight so a normal national
  // number is not accidentally sent without its country code.
  if (/^\d{2,6}$/.test(number)) return number
  if (number.startsWith('00')) number = `+${number.slice(2)}`
  return /^\+[1-9]\d{6,14}$/.test(number) ? number : ''
}

function Avatar({ label, color = 'var(--primary)', size = 96 }) {
  return (
    <div style={{ width: size, height: size, borderRadius: '50%', background: color + '22',
      border: `2px solid ${color}55`, display: 'flex', alignItems: 'center', justifyContent: 'center',
      fontSize: size * 0.42, color, margin: '0 auto' }}>☎</div>
  )
}

function RoundBtn({ icon, label, color, bg, onClick, active }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 6 }}>
      <button onClick={onClick} style={{
        width: 58, height: 58, borderRadius: '50%', cursor: 'pointer', fontSize: 22,
        border: '1px solid ' + (active ? color : 'var(--border-strong)'),
        background: bg || (active ? color + '22' : 'var(--hover)'),
        color: active ? color : 'var(--text-soft)', display: 'flex', alignItems: 'center', justifyContent: 'center',
      }}>{icon}</button>
      <span style={{ fontSize: 11, color: 'var(--text-mute)' }}>{label}</span>
    </div>
  )
}

export default function Softphone({
  selected,
  subscribe,
  instances,
  cards,
  devices,
  setSelected,
  showToast,
  callCoordinator,
  cellularIncoming,
  mediaIngress,
}) {
  const { t } = useI18n()
  const id = selected?.id
  const coordinatorLine = callCoordinator?.line?.(id) || {}
  const prov = coordinatorLine.prov || null
  const reg = coordinatorLine.reg || 'idle'
  const vowifiCall = coordinatorLine.call || null
  const [cellularCall, setCellularCall] = useState(null)     // {dir, number, state, startedAt, endCause}
  const [callTransport, setCallTransport] = useState('vowifi')
  const [cellularBusy, setCellularBusy] = useState(false)
  const [remoteSim, setRemoteSim] = useState(null)
  const [num, setNum] = useState('')
  const [dur, setDur] = useState(0)
  const [muted, setMuted] = useState(false)
  const [keypad, setKeypad] = useState(false)
  const [dtmfSeq, setDtmfSeq] = useState('')   // digits/symbols entered since the keypad opened
  const [recording, setRecording] = useState(false)
  const [calls, setCalls] = useState([])
  const [callSelMode, setCallSelMode] = useState(false)
  const [callSel, setCallSel] = useState(() => new Set())
  const remoteSimRequest = useRef(0)
  const currentIdRef = useRef(id)
  currentIdRef.current = id
  const cellularPhone = useRef(null)
  const cellularCallId = useRef('')
  const cellularBrowserNonce = useRef('')
  const cellularMediaDialed = useRef(false)
  const cellularCommitted = useRef(false)
  const cellularClosing = useRef(false)
  const cellularMediaFailureHandled = useRef(false)
  const cellularTermination = useRef({ key: '', promise: null })
  const call = callTransport === 'cellular' ? cellularCall : vowifiCall
  const setCall = setCellularCall
  const mediaTest = coordinatorLine.mediaTest || 'idle'
  const voiceReadiness = lineCallReadinessStatus(
    selected,
    devices,
    { mediaIngress, coordinatorLine },
    t)
  const vowifiReady = voiceReadiness.browserVoiceReady
  const browserMediaAvailable = prov?.browser_media?.available === true
  const vowifiReason = voiceReadiness.browserVoiceLabel
  const vowifiDetail = !vowifiReady && prov?.media_error ? t(prov.media_error) : ''
  const requestCellularTermination = useCallback(() => {
    const key = cellularCallId.current || `instance:${id}`
    if (cellularTermination.current.key === key && cellularTermination.current.promise) {
      return cellularTermination.current.promise
    }
    const promise = api.cellularCallHangup(id).then((result) => {
      if (!result?.ok && !result?.termination_pending &&
          cellularTermination.current.key === key) {
        cellularTermination.current = { key: '', promise: null }
      }
      return result
    }).catch((error) => {
      // A dropped HTTP request is not evidence that the Gateway accepted termination. Allow a
      // later user/event retry while the Agent-side paid-call lease remains the final backstop.
      if (cellularTermination.current.key === key) {
        cellularTermination.current = { key: '', promise: null }
      }
      throw error
    })
    cellularTermination.current = { key, promise }
    return promise
  }, [id])

  // VoWiFi provisioning and the persistent audio sink are owned by App's CallCoordinator so
  // incoming calls work from every authenticated page, not only while this view is mounted.
  const selectedDevice = devices.find((device) => device.present === true
    && device.device_type === 'modem'
    && String(device.instance_id || '') === String(id || ''))
  const selectedDeviceIccidKey = String(selectedDevice?.iccid ||
    selectedDevice?.sim?.iccid || '')
  const cellularAvailable = Boolean(selectedDevice || remoteSim)
  const remoteCallReady = Boolean(remoteSim?.online
    && remoteSim?.capabilities?.call_signalling
    && remoteSim?.capabilities?.call_audio
    && remoteSim?.status?.call_audio_ready
    && remoteSim?.status?.call_ready)
  // A cellular data bearer proves neither voice registration nor UAC audio readiness.
  // Remote calls are selectable only after the Agent publishes both capabilities and both
  // bounded self-tests. Browser/Asterisk media is checked again by prepare/commit.
  const cellularReady = remoteCallReady

  const loadCalls = useCallback(() => { if (id) api.calls(id).then((r) => setCalls(r.calls || [])).catch(() => {}) }, [id])
  useEffect(() => { loadCalls() }, [loadCalls])
  const refreshRemoteSim = useCallback(() => {
    const requestId = ++remoteSimRequest.current
    const requestedLine = String(id || '')
    if (!requestedLine) { setRemoteSim(null); return }
    api.cellularSims().then(result => {
      if (requestId !== remoteSimRequest.current ||
          requestedLine !== String(currentIdRef.current || '')) return
      setRemoteSim((result.sims || []).find(
        sim => String(sim.instance_id || '') === requestedLine) || null)
    }).catch(() => {
      if (requestId === remoteSimRequest.current &&
          requestedLine === String(currentIdRef.current || '')) setRemoteSim(null)
    })
  }, [id])
  useEffect(() => { refreshRemoteSim() }, [refreshRemoteSim, selectedDeviceIccidKey])
  useEffect(() => { setCallSelMode(false); setCallSel(new Set()); setCallTransport('vowifi') }, [id])
  useEffect(() => {
    if (!cellularReady && callTransport === 'cellular' && !cellularCall) setCallTransport('vowifi')
  }, [cellularReady, callTransport, cellularCall])
  // if the list empties (own delete, or another client's clear-all over WS), leave select
  // mode so the toolbar/checkbox UI can't get stranded on an empty list.
  useEffect(() => { if (!calls.length) { setCallSelMode(false); setCallSel(new Set()) } }, [calls.length])
  useEffect(() => subscribe && subscribe((m) => {
    if (shouldRefreshRemoteSim(m, id)) refreshRemoteSim()
    if (String(m.instance || '') !== String(id || '')) return
    if (m.type !== 'call') return
    loadCalls()
    const item = m.call
    if (!item || item.transport !== 'cellular') return
    if (item.direction === 'in') return
    if (item.status === 'answered') {
      setCall(current => current?.transport === 'cellular'
        ? { ...current, state: 'active', startedAt: current.startedAt || Date.now() } : current)
    } else if (item.end_ts || item.status === 'ended') {
      setCall(current => current?.transport === 'cellular' ? { ...current, state: 'ended' } : current)
    }
  }), [subscribe, id, loadCalls, refreshRemoteSim])

  const toast = (m) => (showToast ? showToast(m) : null)
  const toggleCallSel = (cid) => setCallSel((s) => { const n = new Set(s); n.has(cid) ? n.delete(cid) : n.add(cid); return n })
  // Reload only if still on the same line (a delete may resolve after the user switched SIMs).
  const reloadIfSame = (forId) => { if (forId === id) loadCalls() }

  const deleteSelectedCalls = async () => {
    if (!callSel.size) return
    if (!confirm(`Delete ${callSel.size} selected call${callSel.size > 1 ? 's' : ''}?`)) return
    const forId = id
    try {
      await api.deleteCalls(forId, { ids: [...callSel] })
      setCallSelMode(false); setCallSel(new Set()); reloadIfSame(forId); toast('Calls deleted')
    } catch (e) { toast('Delete failed: ' + e.message) }
  }
  const deleteOneCall = async (cid, e) => {
    if (e) e.stopPropagation()
    const forId = id
    try { await api.deleteCalls(forId, { ids: [cid] }); reloadIfSame(forId) } catch (e2) { toast('Delete failed: ' + e2.message) }
  }
  const clearAllCalls = async () => {
    if (!calls.length) return
    if (!confirm('Clear the entire call history for this line?')) return
    const forId = id
    try { await api.deleteCalls(forId, { all: true }); setCallSelMode(false); setCallSel(new Set()); reloadIfSame(forId); toast('Call history cleared') }
    catch (e) { toast('Delete failed: ' + e.message) }
  }

  // Page-local cleanup only. VoWiFi signalling is owned by App's CallCoordinator and must not
  // disappear when the user leaves the Calls route.
  useEffect(() => {
    if (!id) return
    currentIdRef.current = id
    setCall(null)
    return () => {
      if (String(currentIdRef.current || '') === String(id || '')) currentIdRef.current = ''
      const preparedCallId = cellularCallId.current
      if (preparedCallId) api.releaseCellularCall(id, preparedCallId).catch(() => {})
      if (cellularPhone.current) { cellularPhone.current.stop(); cellularPhone.current = null }
      cellularCallId.current = ''; cellularBrowserNonce.current = ''
      cellularMediaDialed.current = false
      cellularCommitted.current = false; cellularClosing.current = false
      cellularTermination.current = { key: '', promise: null }
    }
  }, [id])

  const stopCellularMediaPhone = () => {
    cellularClosing.current = true
    const current = cellularPhone.current
    cellularPhone.current = null
    if (current) {
      try { current.hangup() } catch {}
      try { current.stop() } catch {}
    }
    cellularCallId.current = ''
    cellularBrowserNonce.current = ''
    cellularMediaDialed.current = false
    cellularCommitted.current = false
    cellularMediaFailureHandled.current = false
    setTimeout(() => { cellularClosing.current = false }, 1000)
  }

  const clearCallSoon = (endCause) => {
    setCall((c) => c ? { ...c, state: 'ended', endCause } : null)
    setKeypad(false); setMuted(false); setRecording(false)
    setTimeout(() => setCall(null), 2500)
    loadCalls()
  }

  const verifyMedia = () => {
    if (!browserMediaAvailable || call) {
      if (!browserMediaAvailable) toast(t('Browser WSS media is unavailable'))
      return
    }
    callCoordinator.verifyMedia(id).then(() => {
      toast(t('No-charge media test passed. This browser route is ready.'))
    }).catch((error) => {
      toast(`${t('Media test failed')}: ${error.message}`)
    })
  }

  // in-call duration timer
  useEffect(() => {
    if (call?.state !== 'active' || !call.startedAt) { setDur(0); return }
    const t = setInterval(() => setDur(Math.floor((Date.now() - call.startedAt) / 1000)), 500)
    return () => clearInterval(t)
  }, [call?.state, call?.startedAt])

  // ModemManager owns the cellular call object and exposes its signalling state. Poll only
  // while our experimental cellular dial UI is active; no microphone/audio path is implied.
  useEffect(() => {
    if (!id || call?.transport !== 'cellular' || !call?.committed || call.state === 'ended') return
    let alive = true
    let timer = null

    const terminateDegraded = async () => {
      if (!alive || cellularMediaFailureHandled.current) return
      cellularMediaFailureHandled.current = true
      const callId = cellularCallId.current
      if (!callId) return
      toast(t('Cellular media stopped; the call is being ended safely.'))
      try {
        await boundedCellularRelease({
          callId,
          release: (value) => api.releaseCellularCall(id, value),
          delay: (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds)),
        })
      } catch (error) {
        if (alive) toast(`${t('Cellular hangup failed')}: ${error.message}`)
      }
    }

    const poll = async () => {
      let mediaRefreshError = null
      try {
        const refreshed = await refreshCellularMediaState({
          refreshEvidence: async () => {
            if (!cellularPhone.current || !cellularCallId.current ||
                !cellularBrowserNonce.current) return
            const evidence = await cellularPhone.current.waitForBidirectionalMedia(3000)
            await api.submitCellularMediaEvidence(id, cellularCallId.current, {
              nonce: cellularBrowserNonce.current, ...evidence,
            })
          },
          getStatus: () => api.cellularCallStatus(id),
          terminate: terminateDegraded,
        })
        const result = refreshed.status
        mediaRefreshError = refreshed.mediaRefreshError
        if (!alive) return
        const state = result.status
        const mediaPhase = result.media?.phase || ''
        setCall((current) => current?.transport === 'cellular'
          ? { ...current, mediaPhase } : current)
        if (result.unavailable || state === 'failed') {
          toast(`${t('Cellular call ended')}: ${result.error || t('Cellular modem is unavailable')}`)
          clearCallSoon('Failed')
          return
        }
        if (state === 'active') {
          setCall((current) => current?.transport === 'cellular'
            ? { ...current, state: 'active', startedAt: current.startedAt || Date.now() } : current)
        } else if (state === 'dialing' || state === 'ringing-out') {
          setCall((current) => current?.transport === 'cellular' ? { ...current, state: 'ringing' } : current)
        } else if (state === 'terminated' || state === 'idle' || state === 'ended') {
          if (result.terminal_confirmed || Number(result.terminal_samples || 0) >= 2) {
            stopCellularMediaPhone()
            clearCallSoon(result.call?.reason || 'Ended')
          } else {
            setCall((current) => current?.transport === 'cellular'
              ? { ...current, mediaPhase: 'confirming-termination' } : current)
          }
        }
      } catch (error) {
        if (alive) toast(`${t('Could not read cellular call status')}: ${error.message}`)
      } finally {
        if (alive) {
          // Surface a persistent browser-side failure only after the authoritative status read;
          // do not let it suppress degradation handling above.
          if (mediaRefreshError && !cellularMediaFailureHandled.current) {
            setCall((current) => current?.transport === 'cellular'
              ? { ...current, mediaPhase: 'verifying' } : current)
          }
          timer = setTimeout(poll, 2000)
        }
      }
    }
    poll()
    return () => { alive = false; if (timer) clearTimeout(timer) }
  }, [id, call?.transport, call?.state])

  // Physical-keyboard DTMF: while the in-call keypad is open, let the user type 0-9 * #
  // directly instead of only clicking. Clear the echo strip each time the keypad opens.
  useEffect(() => {
    if (!(keypad && call?.state === 'active')) return
    setDtmfSeq('')
    const onKey = (e) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return
      // Shift+3 produces '#'; a bare '3' should stay '3'. e.key already reflects the shifted
      // character, so match on the resulting character directly.
      const k = e.key
      if (/^[0-9*#]$/.test(k)) { e.preventDefault(); pressDTMF(k) }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [keypad, call?.state])

  // Watchdog: a call parked in a non-terminal setup phase (calling/ringing/incoming) that
  // never advances to 'active' or 'ended' — e.g. a BYE/terminal JsSIP event was dropped —
  // would otherwise strand the UI forever. Force it back to idle after a timeout. 'active'
  // has no timeout (calls can be long); 'ended' clears on its own via clearCallSoon.
  useEffect(() => {
    if (!call || call.state === 'active' || call.state === 'ended') return
    const ms = call.state === 'incoming' ? 60000 : 65000
    const t = setTimeout(() => {
      // Ask the phone to tear down whatever it thinks it has, then reset the UI.
      try {
        if (call.transport === 'cellular') {
          requestCellularTermination().catch(() => {})
          stopCellularMediaPhone()
        }
        else callCoordinator.hangup(id)
      } catch {}
      setCall(null); setKeypad(false); setMuted(false); setRecording(false)
    }, ms)
    return () => clearTimeout(t)
  }, [call?.state])

  const dialKey = (k) => {
    if (call?.state === 'active') { callCoordinator.sendDTMF(id, k); setNum((n) => n + k) }
    else setNum((n) => n + k)
  }
  // In-call DTMF: send the tone and echo it into the keypad's display strip.
  const pressDTMF = (k) => {
    if (call?.transport === 'cellular') api.cellularCallDtmf(id, k).catch(error => toast(error.message))
    else callCoordinator.sendDTMF(id, k)
    setDtmfSeq((s) => (s + k).slice(-32))
  }
  const placeCall = async (number = num) => {
    if (!number) return
    const target = normalizeDialTarget(number)
    if (!target) { toast(t('Use a service short code or international format, for example +8613800138000.')); return }
    if (callTransport === 'cellular') {
      if (!cellularReady) { toast(t('Turn on 4G and wait for the cellular modem to become ready first.')); return }
      if (!window.confirm(t('Place this call through the cellular modem? Normal call charges may apply.'))) return
      // Prime the shared audio element synchronously inside the click gesture, before any
      // network await. The dedicated cellular Phone created below reuses this exact element.
      callCoordinator.createMediaPhone(() => {}).unlockAudio()
      setCellularBusy(true)
      try {
        const prepared = await api.prepareCellularCall(id, target)
        if (String(currentIdRef.current || '') !== String(id || '')) {
          api.cancelCellularCall(id, prepared.call_id).catch(() => {})
          return
        }
        stopCellularMediaPhone()
        cellularClosing.current = false
        cellularCallId.current = prepared.call_id
        cellularTermination.current = { key: '', promise: null }
        cellularBrowserNonce.current = prepared.browser_nonce
        setCall({ dir: 'out', number: target, state: 'calling', transport: 'cellular', committed: false })
        const mediaPhone = callCoordinator.createMediaPhone((type, data) => {
          if (type === 'registered' && data && !cellularMediaDialed.current) {
            cellularMediaDialed.current = true
            mediaPhone.unlockAudio()
            mediaPhone.call(prepared.media_target)
          } else if (type === 'active' && !cellularCommitted.current) {
            cellularCommitted.current = true
            mediaPhone.waitForBidirectionalMedia().then((evidence) =>
              api.submitCellularMediaEvidence(id, prepared.call_id, {
                nonce: prepared.browser_nonce, ...evidence,
              })
            ).then(() => api.commitCellularCall(id, prepared.call_id)).then((result) => {
              if (!result.ok && !result.uncertain) throw new Error(result.error || t('Unknown'))
              setCall((current) => current?.transport === 'cellular'
                ? { ...current, state: 'ringing', committed: true } : current)
              toast(result.uncertain
                ? t('Call start is uncertain. Use Hang up before trying again.')
                : t('Cellular call started with browser audio.'))
            }).catch((error) => {
              toast(`${t('Cellular call failed')}: ${error.message}`)
              stopCellularMediaPhone()
              clearCallSoon('Failed')
            }).finally(() => setCellularBusy(false))
          } else if ((type === 'failed' || type === 'ended') && !cellularClosing.current) {
              requestCellularTermination().catch(() => {})
            stopCellularMediaPhone()
            clearCallSoon(data?.cause || 'Media ended')
          } else if (type === 'retryexhausted' && !cellularClosing.current) {
            toast(`${t('Cellular call failed')}: ${t('Media connection retry limit reached')}`)
            requestCellularTermination().catch(() => {})
            stopCellularMediaPhone()
            clearCallSoon('Media retry limit reached')
          }
        })
        cellularPhone.current = mediaPhone
        mediaPhone.start(prepared.softphone, prepared.softphone.host || location.hostname)
        setNum('')
      } catch (error) { toast(`${t('Cellular call failed')}: ${error.message}`) }
      finally {
        // Once media is prepared, commit's finally owns the busy flag. Preparation failures
        // have no Phone/call id and can release it immediately.
        if (!cellularCallId.current) setCellularBusy(false)
      }
      return
    }
    if (!vowifiReady) { toast(vowifiReason); return }
    if (!callCoordinator.call(id, target)) return
    setNum('')
  }
  const answer = async () => {
    if (!vowifiReady) { toast(vowifiReason); return }
    callCoordinator.answer(id)
  }
  // Optimistically move to 'ended' on a local hangup. JsSIP will still fire 'ended'
  // (→ clearCallSoon), but if that event is delayed or missed the UI has already left the
  // active/ringing screen instead of stranding on it.
  const hangup = async () => {
    if (call?.transport === 'cellular') {
      setCellularBusy(true)
      try {
        const result = await requestCellularTermination()
        if (!result.ok && !result.termination_pending) {
          toast(`${t('Cellular hangup failed')}: ${result.error || t('Call state is unknown')}`)
          return
        }
      }
      catch (error) { toast(`${t('Cellular hangup failed')}: ${error.message}`); return }
      finally { setCellularBusy(false) }
      stopCellularMediaPhone()
    } else callCoordinator.hangup(id)
    setCall((c) => (c && c.state !== 'ended') ? { ...c, state: 'ended', endCause: c.endCause } : c)
    setKeypad(false); setMuted(false); setRecording(false)
    setTimeout(() => setCall((c) => (c && c.state === 'ended') ? null : c), 2500)
  }
  // Declining a ringing incoming call must send 603 (→ "declined"), not a bare hangup
  // (→ "missed"). reject() picks the right signalling for an un-answered incoming session.
  const decline = () => {
    if (call?.transport === 'cellular') {
      requestCellularTermination().catch(error => toast(error.message))
      stopCellularMediaPhone()
    }
    else callCoordinator.decline(id)
    setCall((c) => (c && c.state !== 'ended') ? { ...c, state: 'ended', endCause: 'Rejected' } : c)
    setKeypad(false); setMuted(false); setRecording(false)
    setTimeout(() => setCall((c) => (c && c.state === 'ended') ? null : c), 2500)
  }
  const toggleMute = () => {
    const m = !muted; setMuted(m)
    if (call?.transport === 'cellular') cellularPhone.current?.setMuted(m)
    else callCoordinator.setMuted(id, m)
  }
  const toggleRecord = async () => {
    if (recording) {
      const blob = call?.transport === 'cellular'
        ? await cellularPhone.current?.stopRecording()
        : await callCoordinator.stopRecording(id)
      setRecording(false)
      if (blob) {
        const url = URL.createObjectURL(blob)
        const a = document.createElement('a')
        a.href = url; a.download = `call-${call?.number || 'rec'}-${Date.now()}.webm`; a.click()
        setTimeout(() => URL.revokeObjectURL(url), 10000)
      }
    } else {
      const ok = call?.transport === 'cellular'
        ? await cellularPhone.current?.startRecording()
        : await callCoordinator.startRecording(id)
      setRecording(ok)
    }
  }

  if (!id) return (
    <div>
      <SimSelector instances={instances} cards={cards} devices={devices} selected={selected}
        setSelected={setSelected} mediaIngress={mediaIngress} callCoordinator={callCoordinator}
        showVoiceReadiness />
      <div style={{ color: 'var(--text-dim)' }}>{t('Select a SIM / line to use the softphone.')}</div>
    </div>
  )

  const vowifiColor = vowifiReady ? GREEN : '#eab308'
  const inCall = call && (call.state === 'active' || call.state === 'checking' || call.state === 'calling' || call.state === 'ringing' || call.state === 'incoming' || call.state === 'ended')
  const endLabel = (c) => t(c === 'Rejected' ? 'Call declined' : c === 'Busy' ? 'Busy' : c === 'Canceled' || c === 'Canceled/Rejected' ? 'Call cancelled' : 'Call ended')
  const globalIncoming = cellularIncoming?.line?.(id)

  return (
    <div style={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      <div style={{ flexShrink: 0 }}>
        <SimSelector instances={instances} cards={cards} devices={devices} selected={selected}
          setSelected={setSelected} mediaIngress={mediaIngress} callCoordinator={callCoordinator}
          showVoiceReadiness />
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: '380px 1fr', gridTemplateRows: 'minmax(0, 1fr)', gap: 16, flex: 1, minHeight: 0 }}>
      {globalIncoming && <div className="u-note" style={{ margin: '0 0 8px', color: GREEN }}>
        {t('Incoming cellular call is controlled by the global overlay.')}
      </div>}
      <style>{`@keyframes ringpulse{0%{box-shadow:0 0 0 0 ${GREEN}88}70%{box-shadow:0 0 0 16px ${GREEN}00}100%{box-shadow:0 0 0 0 ${GREEN}00}}`}</style>
      {/* ---- Phone panel (Google-Voice style) ---- */}
      <div className="card" style={{ padding: 24, minHeight: 520, display: 'flex', flexDirection: 'column', overflow: 'auto' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
          {cellularAvailable ? <label style={{ display: 'flex', alignItems: 'center', gap: 7, fontSize: 12, color: 'var(--text-dim)' }}>
            {t('Call via')}
            <select value={callTransport} disabled={Boolean(inCall)} onChange={(event) => setCallTransport(event.target.value)} style={{ width: 'auto' }}>
              <option value="vowifi">VoWiFi</option>
              <option value="cellular" disabled={!cellularReady}>{t('Cellular modem')}{!cellularReady ? ` — ${t('Voice unavailable')}` : ''}</option>
            </select>
          </label> : <div style={{ fontSize: 13, color: 'var(--text-dim)' }}>{t('Softphone')}</div>}
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12, color: callTransport === 'cellular' ? (cellularReady ? GREEN : RED) : vowifiColor }}>
            <span style={{ width: 8, height: 8, borderRadius: 999, background: callTransport === 'cellular' ? (cellularReady ? GREEN : RED) : vowifiColor }} />
            {callTransport === 'cellular' ? t(cellularReady ? 'Modem voice hardware ready' : 'Voice unavailable') : vowifiReason}
          </div>
        </div>

        {callTransport === 'vowifi' && !vowifiReady && (
          <div style={{ color: '#f97316', fontSize: 13, margin: '12px 0' }}>
            {vowifiReason}
            <div style={{ color: 'var(--text-mute)', marginTop: 4 }}>
              {t('VoWiFi backend')}: {reg || t('Stopped')}
              {vowifiDetail ? ` · ${vowifiDetail}` : ''}
            </div>
          </div>
        )}
        {callTransport === 'vowifi' && browserMediaAvailable && <div className="u-note" style={{ margin: '8px 0 12px' }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 10 }}>
            <span>{t(mediaTest === 'passed' ? 'Browser media route verified without placing a carrier call.' : mediaTest === 'failed' ? 'The last browser media test failed.' : 'Verify microphone and bidirectional audio through the same-origin WebSocket without placing a carrier call.')}</span>
            <button className="btn btn-ghost" disabled={!browserMediaAvailable || Boolean(call) || mediaTest === 'running'} onClick={verifyMedia}>{t(mediaTest === 'running' ? 'Testing…' : 'Test media')}</button>
          </div>
        </div>}
        {callTransport === 'cellular' && <div className="u-note" style={{ margin: '8px 0 12px', color: cellularReady ? GREEN : '#f59e0b' }}>
          {t(cellularReady ? 'Modem voice hardware passed; browser, Asterisk and Agent media are verified for each call before signalling.' : 'Cellular voice is unavailable until signalling and audio self-tests pass.')}
        </div>}

        {/* ===== INCOMING handled by full-screen overlay above ===== */}

        {/* ===== OUTGOING RINGING ===== */}
        {(call?.state === 'checking' || call?.state === 'calling' || call?.state === 'ringing') && (
          <div style={{ flex: 1, display: 'flex', flexDirection: 'column', justifyContent: 'center', textAlign: 'center', gap: 16 }}>
            <Avatar label={call.number} />
            <div>
              <div className="mono" style={{ fontSize: 22, fontWeight: 700 }}>{call.number}</div>
              <div style={{ fontSize: 13, color: 'var(--text-mute)', marginTop: 4 }}>{call.state === 'checking' ? t('Checking browser audio…') : call.state === 'ringing' ? 'Ringing…' : 'Calling…'}</div>
            </div>
            <div style={{ display: 'flex', justifyContent: 'center', marginTop: 10 }}>
              <RoundBtn icon="✕" label={t('End')} color="#fff" bg={RED} onClick={hangup} />
            </div>
          </div>
        )}

        {/* ===== IN CALL ===== */}
        {call?.state === 'active' && (
          <div style={{ flex: 1, display: 'flex', flexDirection: 'column', justifyContent: 'center', textAlign: 'center', gap: 14 }}>
            <Avatar label={call.number} color={GREEN} size={84} />
            <div>
              <div className="mono" style={{ fontSize: 20, fontWeight: 700 }}>{call.number || 'Unknown'}</div>
              <div style={{ fontSize: 15, color: GREEN, marginTop: 4, fontVariantNumeric: 'tabular-nums' }}>{fmtDur(dur)}</div>
              {call.transport === 'cellular' && <div style={{ fontSize: 12, color: call.mediaPhase === 'media_flowing' ? GREEN : '#f59e0b', marginTop: 5 }}>
                {t(call.mediaPhase === 'media_flowing' ? 'Cellular call · bidirectional audio flow verified' : 'Cellular call · verifying continuous audio flow')}
              </div>}
              {recording && <div style={{ fontSize: 12, color: RED, marginTop: 2 }}>● Recording</div>}
            </div>
            {keypad && (
              <div style={{ maxWidth: 220, margin: '0 auto', display: 'flex', flexDirection: 'column', gap: 8 }}>
                {/* Echo strip: shows every digit/symbol entered via click or physical keyboard */}
                <div className="mono" style={{ minHeight: 40, padding: '8px 12px', borderRadius: 8,
                  background: 'var(--surface-2, rgba(255,255,255,0.06))', border: '1px solid var(--border, rgba(255,255,255,0.12))',
                  fontSize: 20, letterSpacing: 2, textAlign: 'center', overflow: 'hidden', whiteSpace: 'nowrap',
                  direction: 'rtl', color: dtmfSeq ? 'var(--text)' : 'var(--text-mute)' }}>
                  {dtmfSeq || 'Type or tap keys'}
                </div>
                <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3,1fr)', gap: 8 }}>
                  {KEYS.map(([k]) => (
                    <button key={k} className="btn btn-ghost" style={{ padding: 12, fontSize: 18 }}
                      onClick={() => pressDTMF(k)}>{k}</button>
                  ))}
                </div>
              </div>
            )}
            <div style={{ display: 'flex', justifyContent: 'center', gap: 22, marginTop: 8 }}>
              {call.transport !== 'cellular' && <RoundBtn icon={muted ? '🔇' : '🎙'} label={t(muted ? 'Unmute' : 'Mute')} color="#60a5fa" onClick={toggleMute} active={muted} />}
              <RoundBtn icon="⌨" label={t('Keypad')} color="#a78bfa" onClick={() => setKeypad((v) => !v)} active={keypad} />
              {call.transport !== 'cellular' && <RoundBtn icon="⏺" label={t(recording ? 'Stop' : 'Record')} color={RED} onClick={toggleRecord} active={recording} />}
            </div>
            <div style={{ display: 'flex', justifyContent: 'center', marginTop: 6 }}>
              <RoundBtn icon="✕" label={t('Hangup')} color="#fff" bg={RED} onClick={hangup} />
            </div>
          </div>
        )}

        {/* ===== ENDED (brief) ===== */}
        {call?.state === 'ended' && (
          <div style={{ flex: 1, display: 'flex', flexDirection: 'column', justifyContent: 'center', textAlign: 'center', gap: 12 }}>
            <Avatar label={call.number} color={call.endCause === 'Rejected' ? RED : 'var(--text-mute)'} />
            <div className="mono" style={{ fontSize: 20, fontWeight: 700 }}>{call.number || 'Unknown'}</div>
            <div style={{ fontSize: 14, color: call.endCause === 'Rejected' ? RED : 'var(--text-mute)' }}>{endLabel(call.endCause)}</div>
          </div>
        )}

        {/* ===== DIALER (idle) ===== */}
        {!inCall && (
          <div style={{ flex: 1, display: 'flex', flexDirection: 'column' }}>
            <input value={num} onChange={(e) => setNum(e.target.value)} placeholder={t('Enter a number')}
              className="mono" style={{ fontSize: 24, textAlign: 'center', margin: '10px 0 16px', letterSpacing: 1, border: 'none', background: 'transparent' }} />
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3,1fr)', gap: 10 }}>
              {KEYS.map(([k, sub]) => (
                <button key={k} onClick={() => dialKey(k)} style={{
                  padding: '10px 0', borderRadius: 12, cursor: 'pointer', background: 'var(--hover)',
                  border: '1px solid var(--border)', color: 'var(--text)', display: 'flex', flexDirection: 'column', alignItems: 'center',
                }}>
                  <span style={{ fontSize: 22, fontWeight: 600 }}>{k}</span>
                  <span style={{ fontSize: 9, color: 'var(--text-mute)', letterSpacing: 1, height: 10 }}>{sub}</span>
                </button>
              ))}
            </div>
            <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', gap: 24, marginTop: 16 }}>
              <div style={{ width: 58 }} />
              <button onClick={() => placeCall()} disabled={cellularBusy || !num || (callTransport === 'vowifi' ? !vowifiReady : !cellularReady)} style={{
                width: 64, height: 64, borderRadius: '50%', border: 'none', cursor: 'pointer', fontSize: 26,
                background: (num && (callTransport === 'cellular' ? cellularReady : vowifiReady)) ? GREEN : 'var(--border-strong)', color: '#fff',
              }}>✆</button>
              <button onClick={() => setNum((n) => n.slice(0, -1))} style={{
                width: 58, height: 58, borderRadius: '50%', border: 'none', background: 'transparent',
                color: 'var(--text-mute)', cursor: 'pointer', fontSize: 22, visibility: num ? 'visible' : 'hidden',
              }}>⌫</button>
            </div>
          </div>
        )}
      </div>

      {/* ---- Recent calls ---- */}
      <div className="card" style={{ padding: 20, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12, flexShrink: 0 }}>
          <div style={{ fontSize: 15, fontWeight: 600 }}>{t('Recent calls')}</div>
          {calls.length > 0 && (
            callSelMode ? (
              <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <span style={{ fontSize: 12, color: 'var(--text-mute)' }}>{callSel.size} selected</span>
                <button className="btn btn-ghost" style={{ padding: '4px 10px', fontSize: 12, color: RED }}
                  disabled={!callSel.size} onClick={deleteSelectedCalls}>{t('Delete')}</button>
                <button className="btn btn-ghost" style={{ padding: '4px 10px', fontSize: 12 }}
                  onClick={() => { setCallSelMode(false); setCallSel(new Set()) }}>{t('Cancel')}</button>
              </div>
            ) : (
              <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <button className="btn btn-ghost" style={{ padding: '4px 10px', fontSize: 12 }}
                  onClick={() => setCallSelMode(true)}>{t('Select')}</button>
                <button className="btn btn-ghost" style={{ padding: '4px 10px', fontSize: 12, color: RED }}
                  onClick={clearAllCalls}>{t('Clear all')}</button>
              </div>
            )
          )}
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, flex: 1, minHeight: 0, overflow: 'auto' }}>
          {calls.length === 0 && <div style={{ fontSize: 13, color: 'var(--text-mute)' }}>{t('No calls yet.')}</div>}
          {calls.map((c) => {
            const s = (c.status || '').toLowerCase()
            const color = s === 'answered' ? GREEN : (s === 'rejected' || s === 'busy' || s === 'failed') ? RED
              : (s === 'no answer' || s === 'cancelled' || s === 'missed') ? '#eab308' : 'var(--text-dim)'
            const dlabel = c.direction === 'in' ? '↙ Incoming' : '↗ Outgoing'
            const checked = callSel.has(c.id)
            return (
              <div key={c.id} onClick={() => callSelMode && toggleCallSel(c.id)} className="hover-row"
                style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8,
                  fontSize: 13.5, padding: '10px 12px', borderRadius: 10, cursor: callSelMode ? 'pointer' : 'default',
                  background: checked ? 'var(--active)' : 'var(--input-bg)' }}>
                {callSelMode && <input type="checkbox" readOnly checked={checked} style={{ width: 'auto', flexShrink: 0 }} />}
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div className="mono" style={{ fontWeight: 600 }}>{c.peer}</div>
                  <div style={{ fontSize: 11, color: 'var(--text-mute)' }}>{dlabel} · {new Date(c.start_ts * 1000).toLocaleString()}{c.transport === 'cellular' ? ` · ${t('Cellular modem')}` : ''}</div>
                </div>
                <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                  <span style={{ color, fontWeight: 600, textTransform: 'capitalize' }}>{c.status || 'ringing'}</span>
                  {!callSelMode && <>
                    <button className="btn btn-ghost" style={{ padding: '5px 10px' }}
                      disabled={callTransport === 'vowifi' ? !vowifiReady : !cellularReady}
                      onClick={(e) => { e.stopPropagation(); setNum(c.peer); placeCall(c.peer) }}>{t('Call')}</button>
                    <button className="row-del" title={t('Delete this call')} aria-label={t('Delete this call')}
                      onClick={(e) => deleteOneCall(c.id, e)}>🗑</button>
                  </>}
                </div>
              </div>
            )
          })}
        </div>
      </div>
      </div>
    </div>
  )
}
