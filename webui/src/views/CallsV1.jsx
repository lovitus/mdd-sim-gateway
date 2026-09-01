import React, { useEffect, useMemo, useState } from 'react'
import { useI18n } from '../i18n.jsx'

const KEYS = ['1', '2', '3', '4', '5', '6', '7', '8', '9', '*', '0', '#']

function blocked(value) {
  return (value?.blocked || ['state_unavailable']).join(', ')
}
function optionsFor(instances) {
  const values = []
  for (const line of instances || []) {
    for (const [mode, key] of [['vowifi', 'vowifi_call'], ['cellular', 'cellular_call']]) {
      const operation = line.operations?.[key]
      values.push({ line, mode, ready: operation?.ready === true, blocked: blocked(operation) })
    }
  }
  return values
}

export default function CallsV1({ instances, callCoordinator: coordinator, showToast }) {
  const { t } = useI18n()
  const options = useMemo(() => optionsFor(instances), [instances])
  const [selected, setSelected] = useState('')
  const [number, setNumber] = useState('')
  const [bufferMS, setBufferMS] = useState(500)
  const [busy, setBusy] = useState('')
  const current = coordinator?.current
  useEffect(() => {
    if (current) { setSelected(`${current.mode}:${current.line_id}`); return }
    if (options.some(value => `${value.mode}:${value.line.id}` === selected && value.ready)) return
    const first = options.find(value => value.ready)
    setSelected(first ? `${first.mode}:${first.line.id}` : '')
  }, [current, options, selected])
  const route = options.find(value => `${value.mode}:${value.line.id}` === selected)
  const start = async () => {
    if (!route?.ready || current) return
    setBusy('start')
    try { await coordinator.startOutgoing(route.line.id, route.mode, number, Number(bufferMS)) }
    catch (error) { if (!coordinator.current) showToast(error.message) }
    finally { setBusy('') }
  }
  const answer = async item => {
    setBusy(`answer:${item.mode}:${item.line.id}`)
    try { await coordinator.answerIncoming(item.line.id, item.mode, item.call, Number(bufferMS)) }
    catch (error) { if (!coordinator.current) showToast(error.message) }
    finally { setBusy('') }
  }
  const reject = async item => {
    setBusy(`reject:${item.mode}:${item.line.id}`)
    try { await coordinator.rejectIncoming(item.line.id, item.mode, item.call) }
    finally { setBusy('') }
  }
  const history = coordinator?.history || []
  return <div className="u-page">
    {!!coordinator?.incoming?.length && <div className="u-section"><div className="u-section-title"><div><h2>{t('Incoming calls')}</h2><p>{t('One successful answer claims the call; other browsers become read-only.')}</p></div></div><div className="u-device-grid">{coordinator.incoming.map(item => <article className="card u-panel" key={`${item.mode}:${item.line.id}:${item.call.incoming_event_id || item.call.call_id}`}><div className="u-card-head"><div><h2>{item.call.number || item.call.caller || t('Unknown')}</h2><p>{item.line.name || item.line.id} · {item.mode}</p></div><span className="u-badge cap-starting">{t('Ringing')}</span></div>
      {item.mode === 'cellular' && item.call.actionable !== true && <p className="u-error">{item.call.blocked || 'incoming_state_stale'}</p>}
      <div className="u-inline"><button className="btn btn-primary" disabled={!!busy || !!current || item.mode === 'cellular' && item.call.actionable !== true} onClick={() => answer(item)}>{t('Answer')}</button><button className="btn btn-danger" disabled={!!busy || !!current} onClick={() => reject(item)}>{t('Reject')}</button></div></article>)}</div></div>}
    <div className="u-split"><section className="card u-panel"><h2>{current ? t('Current call') : t('New call')}</h2>
      <label>{t('Line and transport')}</label><select value={selected} disabled={!!current} onChange={event => setSelected(event.target.value)}>
        {!options.length && <option value="">{t('No lines configured')}</option>}
        {options.map(value => <option key={`${value.mode}:${value.line.id}`} value={`${value.mode}:${value.line.id}`} disabled={!value.ready}>{value.line.name || value.line.id} · {value.mode === 'cellular' ? t('Cellular modem') : 'VoWiFi'}{value.ready ? '' : ` · ${t('Unavailable')} (${value.blocked})`}</option>)}</select>
      <div className="u-form-grid"><div><label>{t('Number')}</label><input value={current?.callee || number} disabled={!!current} onChange={event => setNumber(event.target.value)} placeholder="+448001076285"/></div><div><label>{t('Audio queue limit')}</label><div className="u-number-suffix"><input type="number" min="100" max="2000" step="100" value={bufferMS} disabled={!!current} onChange={event => setBufferMS(event.target.value)}/><span>ms</span></div></div></div>
      {!current && <div className="u-dialpad">{KEYS.map(key => <button type="button" key={key} onClick={() => setNumber(value => `${value}${key}`)}>{key}</button>)}</div>}
      {!current && <div className="u-inline"><button className="btn btn-primary" disabled={!!busy || !route?.ready || !number.trim()} onClick={start}>{t(busy === 'start' ? 'Preparing audio…' : 'Call')}</button><button className="btn btn-ghost" onClick={() => setNumber(value => value.slice(0, -1))}>⌫</button></div>}
      {current && <><div className="u-details cols"><div className="u-detail"><span>{t('State')}</span><b>{current.phase}</b></div><div className="u-detail"><span>{t('Transport')}</span><b>{current.mode}</b></div><div className="u-detail"><span>{t('Media')}</span><b>{current.media_state}</b></div><div className="u-detail"><span>{t('Line')}</span><b>{current.line_id}</b></div></div><p className={current.phase === 'media_failed' || current.phase === 'start_unknown' || current.phase === 'ending' ? 'u-error' : 'u-note'}>{current.message}</p>
        {current.phase === 'active' && <div className="u-dialpad">{KEYS.map(key => <button type="button" key={key} onClick={() => coordinator.sendDTMF(key).catch(error => showToast(error.message))}>{key}</button>)}</div>}
        <div className="u-inline">{current.phase === 'active' && <button className="btn btn-ghost" onClick={coordinator.toggleMute}>{t(current.muted ? 'Unmute' : 'Mute')}</button>}{current.phase === 'start_unknown' && current.mode === 'vowifi' && <button className="btn btn-ghost" onClick={() => coordinator.retryStart()}>{t('Retry the same request')}</button>}<button className="btn btn-danger" onClick={coordinator.hangup}>{t('Hang up')}</button></div></>}
      <p className="u-note">{t('Closing the page or losing media heartbeats stops evidence immediately; the server terminates the exact call after the 10-second guard. Temporary jitter does not end a call.')}</p>
    </section>
    <aside className="card u-panel"><div className="u-card-head"><h2>{t('Line occupancy')}</h2><button className="btn btn-ghost" onClick={coordinator.refresh}>{t('Refresh')}</button></div>{(instances || []).map(line => <div className="u-detail" key={line.id}><span>{line.name || line.id}</span><b>{['vowifi', 'cellular'].map(mode => { const entry = coordinator.statuses?.[`${mode}:${line.id}`]; const occupied = mode === 'cellular' ? (entry?.status?.sessions || []).some(session => !['ended', 'expired'].includes(session.phase)) : !!entry?.status?.active_call; return `${mode}: ${entry?.error ? t('Status unavailable') : occupied ? t('Occupied') : t('Idle')}` }).join(' · ')}</b></div>)}</aside></div>
    <div className="card u-panel"><div className="u-card-head"><h2>{t('Call history')}</h2><div className="u-inline"><button className="btn btn-ghost" onClick={coordinator.loadHistory}>{t('Refresh')}</button><button className="btn btn-ghost" disabled={!history.some(call => call.ended_at)} onClick={() => { const ids = history.filter(call => call.ended_at).map(call => call.id); if (window.confirm(t('Delete all ended call records?'))) coordinator.deleteHistory(ids).catch(error => showToast(error.message)) }}>{t('Clear ended')}</button></div></div>
      {coordinator.historyLoading ? <p>{t('Loading…')}</p> : !history.length ? <p className="u-muted">{t('No call history')}</p> : history.map(call => <div className="u-detail" key={call.id}><span><b>{call.peer || t('Unknown')}</b><small>{call.direction} · {call.transport} · {call.line_id} · {new Date(call.started_at).toLocaleString()}</small></span><span className="u-inline"><b>{call.status}</b>{call.ended_at && <button className="btn btn-ghost" onClick={() => coordinator.deleteHistory([call.id]).catch(error => showToast(error.message))}>{t('Delete')}</button>}</span></div>)}</div>
  </div>
}
