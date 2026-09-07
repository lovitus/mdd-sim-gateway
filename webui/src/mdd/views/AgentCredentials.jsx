import React, { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api.js'
import { useI18n } from '../i18n.jsx'
import {agentCredentialChange} from '../systemAdapter.js'

// Adapted from SystemV1's scoped credential workflow; never read or set a shared token.
export default function AgentCredentials({ showToast }) {
  const { t } = useI18n()
  const [credentials, setCredentials] = useState(null)
  const [agents, setAgents] = useState([])
  const [agentID, setAgentID] = useState('')
  const [issued, setIssued] = useState(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const inFlight = useRef(false)
  const load = useCallback(async () => {
    try {
      const [value, health] = await Promise.all([api.authAgentCredentials(), api.agentHealth()])
      setCredentials(value); setAgents(health.agents || []); setError('')
    } catch (failure) { setError(failure.message) }
  }, [])
  useEffect(() => { void load() }, [load])
  const change = async (action, id = '') => {
    if (inFlight.current) return
    let command
    try { command=agentCredentialChange(action,id,credentials?.mode) }
    catch (failure) {setError(failure.message);return}
    if (!window.confirm(t(command.confirmation,{agent:id}))) return
    inFlight.current = true; setBusy(true); setError(''); setIssued(null)
    try {
      const result = await api.updateAgentCredentials(command.payload)
      setAgents(current => action === 'set_mode' ? [] : current.filter(agent => agent.agent_id !== id.trim()))
      if (result.agent_token) setIssued({id:id.trim(),token:result.agent_token})
      if (result.credentials) setCredentials(result.credentials)
      await load()
      showToast?.(t('Saved'))
    } catch (failure) { setError(failure.message) }
    finally { inFlight.current = false; setBusy(false) }
  }
  const ids = [...new Set([...(credentials?.active || []), ...(credentials?.revoked || []), ...agents.map(agent => agent.agent_id)])].sort()
  return <section>
    <h3>{t('Agent credentials')}</h3>
    {error && <p role="alert" className="u-error">{error}</p>}
    <div className="u-detail"><span>{t('Authentication mode')}</span><b>{credentials?.mode || t('Unknown')}</b></div>
    <button className="btn btn-ghost" disabled={busy || !credentials} onClick={() => change('set_mode')}>{t(credentials?.mode === 'scoped' ? 'Enable transition mode' : 'Disable shared fallback')}</button>
    <div className="u-inline"><input aria-label={t('Agent ID')} value={agentID} onChange={event => setAgentID(event.target.value)} disabled={busy}/>
      <button className="btn btn-primary" disabled={busy || !credentials || !agentID.trim()} onClick={() => change('issue', agentID)}>{t('Issue or rotate credential')}</button>
      <button className="btn btn-ghost" disabled={busy} onClick={load}>{t('Refresh')}</button></div>
    {issued && <div className="u-note"><label>{issued.id} · {t('Shown once')}</label><input type="password" readOnly value={issued.token} autoComplete="off"/>
      <div className="u-inline"><button className="btn btn-ghost" onClick={async () => {
        try { await navigator.clipboard.writeText(issued.token); showToast?.(t('Copied')) }
        catch { setError(t('Clipboard unavailable')) }
      }}>{t('Copy')}</button><button className="btn btn-ghost" onClick={() => setIssued(null)}>{t('Clear')}</button></div></div>}
    {ids.map(id => <div className="u-detail" key={id}><span>{id}</span><div className="u-inline">
      <b>{t((credentials?.active || []).includes(id) ? 'Scoped' : (credentials?.revoked || []).includes(id) ? 'Revoked' : 'Unenrolled')}</b>
      <button className="btn btn-ghost" disabled={busy} onClick={() => change('issue', id)}>{t('Rotate')}</button>
      {credentials?.mode === 'transition' && ((credentials.active || []).includes(id) || (credentials.revoked || []).includes(id)) && <button className="btn btn-ghost" disabled={busy} onClick={() => change('unenroll',id)}>{t('Use fallback')}</button>}
      <button className="btn btn-danger" disabled={busy || (credentials?.revoked || []).includes(id)} onClick={() => change('revoke', id)}>{t('Revoke')}</button>
    </div></div>)}
  </section>
}
