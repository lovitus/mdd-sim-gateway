import React, { useEffect, useState, useCallback, useMemo, useRef } from 'react'
import { api } from '../api.js'
import SimSelector from './SimSelector.jsx'
import { useI18n } from '../i18n.jsx'

// Map ANSI SGR color codes to on-theme colors.
const FG = {
  30: '#475569', 31: '#ef4444', 32: '#22c55e', 33: '#eab308', 34: '#3b82f6',
  35: '#a855f7', 36: '#06b6d4', 37: 'var(--text)',
  90: '#64748b', 91: '#f87171', 92: '#4ade80', 93: '#facc15', 94: '#60a5fa',
  95: '#c084fc', 96: '#22d3ee', 97: 'var(--text)',
}

// Parse a string containing ANSI SGR escapes (e.g. "\x1b[1;32mDEBUG\x1b[0m[153]: ")
// into an array of styled React spans. Tolerates a missing ESC byte.
function ansi(text) {
  const out = []
  let style = {}
  let key = 0
  const re = /\x1b?\[([0-9;]*)m/g
  let last = 0
  let m
  const push = (t) => { if (t) out.push(<span key={key++} style={{ ...style }}>{t}</span>) }
  while ((m = re.exec(text)) !== null) {
    push(text.slice(last, m.index))
    const codes = m[1].split(';').filter((s) => s !== '').map(Number)
    if (codes.length === 0) style = {}
    for (const c of codes) {
      if (c === 0) style = {}
      else if (c === 1) style = { ...style, fontWeight: 700 }
      else if (c === 3) style = { ...style, fontStyle: 'italic' }
      else if (c === 4) style = { ...style, textDecoration: 'underline' }
      else if (FG[c]) style = { ...style, color: FG[c] }
    }
    last = re.lastIndex
  }
  push(text.slice(last))
  return out
}

export default function Logs({ selected, instances, cards, devices, setSelected, callCoordinator }) {
  const { t } = useI18n()
  const id = selected?.id
  const [logs, setLogs] = useState({})
  const [tab, setTab] = useState('all')
  const [auto, setAuto] = useState(false)
  const [error,setError] = useState('')
  const [loading,setLoading] = useState(false)
  const active = useRef(id)
  const request = useRef(0)
  const pending = useRef(new Map())
  active.current = id

  const load = useCallback(async () => {
    if (!id) return
    const sequence = ++request.current
    let promise = pending.current.get(id)
    if (!promise) {promise=api.logs(id,400);pending.current.set(id,promise)}
    setLoading(true)
    try {
      const result=await promise
      if (request.current === sequence && active.current === id) {
        setLogs(previous => JSON.stringify(previous) === JSON.stringify(result) ? previous : result)
        setError('')
      }
    } catch (failure) {if(request.current === sequence && active.current === id)setError(failure.message)}
    finally {
      if(pending.current.get(id) === promise)pending.current.delete(id)
      if(request.current === sequence && active.current === id)setLoading(false)
    }
  }, [id])

  useEffect(() => { ++request.current;setLogs({});setError('');setLoading(false);void load() }, [load])
  useEffect(() => {
    if (!auto) return
    let stopped=false,timer,delay=30000
    const deadline=Date.now()+600000
    const tick=async () => {
      if(stopped)return
      if(Date.now()>=deadline){setAuto(false);return}
      await load()
      delay=Math.min(delay*2,240000)
      if(!stopped && Date.now()+delay<=deadline)timer=setTimeout(tick,delay)
      else if(!stopped)setAuto(false)
    }
    timer=setTimeout(tick,delay)
    return () => {stopped=true;clearTimeout(timer)}
  }, [auto, load])

  const rendered = useMemo(() => ansi(logs[tab] || t('(empty)')), [logs, tab, t])

  if (!id) return (
    <div>
      <SimSelector instances={instances} cards={cards} devices={devices} selected={selected}
        setSelected={setSelected} label={t('Show logs for')}
        callCoordinator={callCoordinator} showVoiceReadiness />
      <div style={{ color: 'var(--text-dim)' }}>{t('Select a line to view diagnostic logs.')}</div>
    </div>
  )

  return (
    <div>
      <SimSelector instances={instances} cards={cards} devices={devices} selected={selected}
        setSelected={setSelected} label={t('Show logs for')}
        callCoordinator={callCoordinator} showVoiceReadiness />
      <div style={{ display: 'flex', flexWrap:'wrap', gap: 8, marginBottom: 12, alignItems: 'center' }}>
        {['all', 'agent', 'provider', 'core'].map((source) => (
          <button key={source} className={`btn ${tab === source ? 'btn-primary' : 'btn-ghost'}`} onClick={() => setTab(source)}>
            {t(source === 'all' ? 'All sources' : source === 'agent' ? 'Agent' : source === 'provider' ? 'Provider' : 'Core')}
          </button>
        ))}
        <button className="btn btn-ghost" disabled={loading} onClick={load}>{t(loading ? 'Loading…' : 'Refresh')}</button>
        <a className="btn btn-ghost" href={api.lineDiagnosticExportURL(id,500)}>{t('Download redacted logs')}</a>
        <label style={{ margin: 0, display: 'flex', alignItems: 'center', gap: 6 }}>
          <input type="checkbox" style={{ width: 'auto' }} checked={auto} onChange={(e) => setAuto(e.target.checked)} /> {t('Auto refresh')}
        </label>
      </div>
      {error && <p role="alert" className="u-error">{error}</p>}
      <pre className="mono card" style={{
        padding: 16, fontSize: 12, lineHeight: 1.5, overflow: 'auto',
        minHeight:260, maxHeight:'70vh', whiteSpace: 'pre-wrap', overflowWrap:'anywhere', color: 'var(--text-soft)',
        background: 'var(--input-bg)',
      }}>
        {rendered}
      </pre>
    </div>
  )
}
