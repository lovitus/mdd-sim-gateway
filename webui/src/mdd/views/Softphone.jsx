import React, { useEffect, useRef, useState, useCallback } from 'react'
import { api } from '../api.js'
import { getCallAudioBufferMS } from '../../browserPreferences.js'
import { originalCallView, historyCallDraft } from '../callView.js'
import SimSelector from './SimSelector.jsx'
import { useI18n } from '../i18n.jsx'

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
}) {
  const { t } = useI18n()
  const id = selected?.id
  const coordinatorLine = callCoordinator?.line?.(id) || {}
  const reg = coordinatorLine.reg || 'unavailable'
  const [callTransport, setCallTransport] = useState('vowifi')
  const [num, setNum] = useState('')
  const [dur, setDur] = useState(0)
  const [keypad, setKeypad] = useState(false)
  const [dtmfSeq, setDtmfSeq] = useState('')
  const [calls, setCalls] = useState([])
  const [historyScope, setHistoryScope] = useState('all')
  const [callSelMode, setCallSelMode] = useState(false)
  const [callSel, setCallSel] = useState(() => new Set())
  const [mediaTest, setMediaTest] = useState('idle')
	const [mediaTestError,setMediaTestError]=useState('')
	const mediaTestEpoch=useRef(0)
  const currentIdRef = useRef(id)
  const historyRequest = useRef(0)
  const historyPending = useRef(new Map())
  const historyDraft = useRef(null)
  currentIdRef.current = id
  const owned = callCoordinator?.current
  const call = owned && String(owned.line_id) === String(id) ? originalCallView(owned) : null
  const muted = Boolean(call?.muted)
  const cellularBusy = Boolean(owned)
  const vowifiReady = selected?.operations?.vowifi_call?.ready === true
  const cellularReady = selected?.operations?.cellular_call?.ready === true
  const cellularAvailable = cellularReady || devices.some(device => device.present === true &&
    device.device_type === 'modem' && String(device.instance_id) === String(id))
  const browserMediaAvailable = vowifiReady
  const vowifiReason = t(vowifiReady ? 'Ready' : selected?.operations?.vowifi_call?.code || 'Voice unavailable')
  const vowifiDetail = ''
  const toast = message => showToast?.(message)
  const historyLineID = historyScope === 'all' ? '' : String(id || '')

  const loadCalls = useCallback(async () => {
    const key = historyLineID
    const request = ++historyRequest.current
    let pending = historyPending.current.get(key)
    if (!pending) {
      pending = api.calls(key)
      historyPending.current.set(key, pending)
    }
    try {
      const result = await pending
      if (request === historyRequest.current) {
        setCalls(previous => JSON.stringify(previous) === JSON.stringify(result.calls || []) ? previous : result.calls || [])
      }
    } catch (error) {
      if (request === historyRequest.current) showToast?.(error.message)
    } finally {
      if (historyPending.current.get(key) === pending) historyPending.current.delete(key)
    }
  }, [historyLineID, showToast])
  useEffect(() => {
    ++historyRequest.current
    setCalls([]); setCallSelMode(false); setCallSel(new Set())
    void loadCalls()
  }, [loadCalls])
  useEffect(() => {
    setKeypad(false); setDtmfSeq('')
    const draft = historyDraft.current
    setCallTransport(draft?.lineID === String(id) ? draft.transport : !vowifiReady && cellularReady ? 'cellular' : 'vowifi')
    historyDraft.current = null
    setMediaTest('idle')
	setMediaTestError('')
	++mediaTestEpoch.current
	callCoordinator.cancelMediaTest?.()
  }, [id])
  useEffect(() => {
    if (call?.transport) setCallTransport(call.transport)
  }, [call?.transport])
  useEffect(() => {
    if(callTransport==='vowifi')return
    ++mediaTestEpoch.current
    callCoordinator.cancelMediaTest?.()
    setMediaTest('idle');setMediaTestError('')
  }, [callTransport])
  useEffect(() => () => {
    ++mediaTestEpoch.current
    callCoordinator.cancelMediaTest?.()
  }, [callCoordinator.cancelMediaTest])
  useEffect(() => subscribe?.(message => {
    if (message.type === 'go.snapshot' || (message.type === 'call' && String(message.instance) === String(id))) void loadCalls()
  }), [subscribe, id, loadCalls])
  useEffect(() => { if (!calls.length) { setCallSelMode(false); setCallSel(new Set()) } }, [calls.length])
  const toggleCallSel = cid => setCallSel(previous => {
    const next = new Set(previous); next.has(cid) ? next.delete(cid) : next.add(cid); return next
  })
  const removeCalls = async (request, forId = id) => {
    try {
      await api.deleteCalls(forId, request)
      if (historyScope === 'all' || String(currentIdRef.current) === String(forId)) {
        setCallSelMode(false); setCallSel(new Set()); await loadCalls()
      }
    } catch (error) { toast(error.message) }
  }
  const deleteSelectedCalls = () => {
    if (callSel.size && window.confirm(t('Delete selected calls?'))) void removeCalls({ ids:[...callSel] })
  }
  const deleteOneCall = (record, event) => { event?.stopPropagation(); void removeCalls({ ids:[record.id] }, record.line_id) }
  const clearAllCalls = () => {
    if (calls.length && window.confirm(t('Clear the entire call history for this line?'))) void removeCalls({ all:true })
  }
  const verifyMedia = async () => {
    if (!browserMediaAvailable || owned || mediaTest === 'running') return
    const forId = id
    const epoch=++mediaTestEpoch.current
    const current=()=>String(currentIdRef.current)===String(forId) && mediaTestEpoch.current===epoch
    setMediaTest('running')
	setMediaTestError('')
    try {
      const result=await callCoordinator.verifyMedia(id)
      if(result?.cancelled){if(current())setMediaTest('idle');return}
      if (current()) {
		setMediaTest('passed')
		toast(t('No-charge media test passed. This browser route is ready.'))
	  }
    } catch (error) {
      if (current()) {
		setMediaTest('failed');setMediaTestError(error.message || String(error))
		toast(error.message)
	  }
    }
  }
  const cancelMediaVerification=()=>{
    ++mediaTestEpoch.current
    callCoordinator.cancelMediaTest?.()
    setMediaTest('idle');setMediaTestError('')
  }
  useEffect(() => {
    if (call?.state !== 'active' || !call.startedAt) { setDur(0); return }
    const timer = setInterval(() => setDur(Math.max(0, Math.floor((Date.now() - call.startedAt) / 1000))), 1000)
    return () => clearInterval(timer)
  }, [call?.state, call?.startedAt])
  const pressDTMF = useCallback(key => {
    if (!/^[0-9*#]$/.test(key)) return
    Promise.resolve(callCoordinator.sendDTMF(key)).catch(error => showToast?.(error.message))
    setDtmfSeq(value => (value + key).slice(-32))
  }, [callCoordinator.sendDTMF, showToast])
  useEffect(() => {
    if (!keypad || call?.state !== 'active') return
    setDtmfSeq('')
    const onKey = event => {
      if (event.metaKey || event.ctrlKey || event.altKey || event.repeat) return
      if (/^[0-9*#]$/.test(event.key)) { event.preventDefault(); pressDTMF(event.key) }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [keypad, call?.state, pressDTMF])
  const dialKey = key => setNum(value => key === '+' ? (value.startsWith('+') ? value : '+' + value) : value + key)
  const prepareHistoryCall = record => {
    const draft = historyCallDraft(record, instances)
    if (!draft || owned) return
    historyDraft.current = draft.lineID === String(id) ? null : draft
    setSelected(draft.lineID)
    setCallTransport(draft.transport)
    setNum(draft.number)
  }
  const placeCall = async (number = num) => {
    if (owned) { toast(t('This line is already in use')); return }
    const target = normalizeDialTarget(number)
    if (!target) { toast(t('Use a service short code or international format, for example +8613800138000.')); return }
    if (callTransport === 'vowifi' ? !vowifiReady : !cellularReady) { toast(t('Voice unavailable')); return }
    if (callTransport === 'cellular' && !window.confirm(t('Place this call through the cellular modem? Normal call charges may apply.'))) return
    try {
      await callCoordinator.startOutgoing(id, callTransport, target, getCallAudioBufferMS())
    } catch (error) { toast(error.message) }
  }
  const hangup = () => callCoordinator.hangup()
  const toggleMute = () => callCoordinator.toggleMute()
  const vowifiColor = vowifiReady ? GREEN : '#eab308'
  const inCall = Boolean(call)
  const endLabel = (c) => t(c === 'Rejected' ? 'Call declined' : c === 'Busy' ? 'Busy' : c === 'Canceled' || c === 'Canceled/Rejected' ? 'Call cancelled' : 'Call ended')
  const globalIncoming = callCoordinator.incoming?.find(value => String(value.line.id) === String(id))

  return (
    <div style={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      <div style={{ flexShrink: 0 }}>
        <SimSelector instances={instances} cards={cards} devices={devices} selected={selected}
          setSelected={setSelected} callCoordinator={callCoordinator}
          showVoiceReadiness />
      </div>
      <div className="mdd-phone-layout">
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
            {coordinatorLine.provisionError && <button className="btn btn-ghost"
              disabled={Boolean(call)} onClick={() => callCoordinator?.refresh()}>
              {t('Retry')}
            </button>}
            <div style={{ color: 'var(--text-mute)', marginTop: 4 }}>
              {t('VoWiFi backend')}: {reg || t('Stopped')}
              {vowifiDetail ? ` · ${vowifiDetail}` : ''}
            </div>
          </div>
        )}
        {callTransport === 'vowifi' && (browserMediaAvailable || mediaTest==='running' || mediaTest==='failed') && <div className="u-note" style={{ margin: '8px 0 12px' }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 10 }}>
            <span>{t(mediaTest === 'passed' ? 'Browser media route verified without placing a carrier call.' : mediaTest === 'failed' ? 'The last browser media test failed.' : 'Verify microphone and bidirectional audio through the same-origin WebSocket without placing a carrier call.')}{mediaTestError && <span role="alert" className="u-error" style={{display:'block'}}>{mediaTestError}</span>}</span>
            <button className="btn btn-ghost" disabled={mediaTest!=='running' && (!browserMediaAvailable || Boolean(call))} onClick={mediaTest==='running'?cancelMediaVerification:verifyMedia}>{t(mediaTest === 'running' ? 'Cancel' : 'Test media')}</button>
          </div>
        </div>}
        {callTransport === 'cellular' && <div className="u-note" style={{ margin: '8px 0 12px', color: cellularReady ? GREEN : '#f59e0b' }}>
          {t(cellularReady ? 'Modem voice hardware passed; browser and Agent audio are verified over the same-origin WebSocket before signalling.' : 'Cellular voice is unavailable until signalling and audio self-tests pass.')}
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

        {(call?.state === 'ending' || call?.state === 'termination_unconfirmed') && <div role="status" className="u-note">
          {call.message || t('Call termination could not be confirmed')}
          <button className="btn btn-ghost" onClick={hangup}>{t('Hangup')}</button>
        </div>}
        {(call?.state === 'start_unknown' || call?.state === 'media_failed') && <div role="alert" className="u-note">
          {call.message}
          <button className="btn btn-danger" onClick={hangup}>{t('Hangup')}</button>
        </div>}

        {/* ===== IN CALL ===== */}
        {call?.state === 'active' && (
          <div style={{ flex: 1, display: 'flex', flexDirection: 'column', justifyContent: 'center', textAlign: 'center', gap: 14 }}>
            <Avatar label={call.number} color={GREEN} size={84} />
            <div>
              <div className="mono" style={{ fontSize: 20, fontWeight: 700 }}>{call.number || 'Unknown'}</div>
              <div style={{ fontSize: 15, color: GREEN, marginTop: 4, fontVariantNumeric: 'tabular-nums' }}>{fmtDur(dur)}</div>
              {call.message && <div role="status" style={{ fontSize:12, marginTop:5 }}>{call.message}</div>}
            </div>
            {keypad && (
              <div style={{ maxWidth: 220, margin: '0 auto', display: 'flex', flexDirection: 'column', gap: 8 }}>
                {/* Echo strip: shows every digit/symbol entered via click or physical keyboard */}
                <div className="mono" style={{ minHeight: 40, padding: '8px 12px', borderRadius: 8,
                  background: 'var(--surface-2, rgba(255,255,255,0.06))', border: '1px solid var(--border, rgba(255,255,255,0.12))',
                  fontSize: 20, letterSpacing: 0, textAlign: 'center', overflow: 'hidden', whiteSpace: 'nowrap',
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
              <RoundBtn icon={muted ? '🔇' : '🎙'} label={t(muted ? 'Unmute' : 'Mute')} color="#60a5fa" onClick={toggleMute} active={muted} />
              <RoundBtn icon="⌨" label={t('Keypad')} color="#a78bfa" onClick={() => setKeypad((v) => !v)} active={keypad} />
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
              className="mono" style={{ fontSize: 24, textAlign: 'center', margin: '10px 0 16px', letterSpacing: 0, border: 'none', background: 'transparent' }} />
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3,1fr)', gap: 10 }}>
              {KEYS.map(([k, sub]) => (
                <button key={k} onClick={() => dialKey(k)} style={{
                  padding: '10px 0', borderRadius: 12, cursor: 'pointer', background: 'var(--hover)',
                  border: '1px solid var(--border)', color: 'var(--text)', display: 'flex', flexDirection: 'column', alignItems: 'center',
                }}>
                  <span style={{ fontSize: 22, fontWeight: 600 }}>{k}</span>
                  <span style={{ fontSize: 9, color: 'var(--text-mute)', letterSpacing: 0, height: 10 }}>{sub}</span>
                </button>
              ))}
            </div>
            <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', gap: 24, marginTop: 16 }}>
              <button className="btn btn-ghost" aria-label={t('International prefix')} title={t('International prefix')} onClick={() => dialKey('+')} style={{ width:58, height:58, fontSize:26 }}>+</button>
              <button aria-label={t('Call')} onClick={() => placeCall()} disabled={cellularBusy || !normalizeDialTarget(num) || (callTransport === 'vowifi' ? !vowifiReady : !cellularReady)} style={{
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
          <select aria-label={t('Call history scope')} value={historyScope} onChange={event => setHistoryScope(event.target.value)} style={{ width:'auto' }}>
            <option value="all">{t('All lines')}</option><option value="line">{t('Selected line')}</option>
          </select>
          {calls.length > 0 && historyScope === 'line' && (
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
                  {historyScope === 'all' && <div>{instances.find(line => String(line.id) === String(c.line_id))?.name || c.line_id}</div>}
                  <div style={{ fontSize: 11, color: 'var(--text-mute)' }}>{dlabel} · {new Date(c.start_ts * 1000).toLocaleString()}{c.transport === 'cellular' ? ` · ${t('Cellular modem')}` : ''}</div>
                </div>
                <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                  <span style={{ color, fontWeight: 600, textTransform: 'capitalize' }}>{c.status || 'ringing'}</span>
                  {!callSelMode && <>
                    <button className="btn btn-ghost" style={{ padding: '5px 10px' }}
                      disabled={Boolean(owned) || !historyCallDraft(c, instances)}
                      onClick={(e) => { e.stopPropagation(); prepareHistoryCall(c) }}>{t('Prepare call')}</button>
                    <button className="row-del" title={t('Delete this call')} aria-label={t('Delete this call')}
                      onClick={(e) => deleteOneCall(c, e)}>🗑</button>
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
