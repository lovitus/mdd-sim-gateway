import React, {useEffect, useRef, useState} from 'react'
import {dialogs} from '../../dialogs.js'
import {api} from '../api.js'
import {useI18n} from '../i18n.jsx'
import {connectionPackage, loadPendingEnrollment, newAgentEnrollment, savePendingEnrollment} from '../agentEnrollment.js'

export default function AgentEnrollment({fingerprint, onCreated, showToast, operationLock, disabled}) {
  const {t} = useI18n()
  const [open,setOpen] = useState(false)
  const [server,setServer] = useState(location.origin)
  const [mode,setMode] = useState('system')
  const [pin,setPin] = useState('')
  const [confirmed,setConfirmed] = useState(false)
  const [busy,setBusy] = useState(false)
  const [pending,setPending] = useState(null)
  const [bundle,setBundle] = useState('')
  const [error,setError] = useState('')
  const [invalidPending,setInvalidPending] = useState(false)
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    try { const saved=loadPendingEnrollment(); setPending(saved); if(saved) setOpen(true) }
    catch (failure) { setError(failure.message); setInvalidPending(true) }
    return () => { mounted.current=false }
  }, [])
  const create = async () => {
    if (operationLock.current) return
    operationLock.current=true; setBusy(true); setError('')
    try {
      const frozen = pending || newAgentEnrollment({server,mode,pin,directCore:confirmed})
      if (!(await dialogs.confirm(t('Add a new Agent credential for {agent}? Existing Agents are unchanged.',{agent:frozen.agent_id})))) return
      if (!mounted.current) return
      savePendingEnrollment(frozen)
      setPending(frozen)
      const result = await api.updateAgentCredentials({action:'create',agent_id:frozen.agent_id,enrollment_id:frozen.enrollment_id})
      const exported = connectionPackage(frozen,result)
      // The pending identity contains no token and survives an interrupted response.
      // Keep it until the operator explicitly closes the successfully shown result.
      if (!mounted.current) return
      setBundle(exported)
      onCreated?.(result.credentials)
    } catch (failure) { if(mounted.current) setError(failure.message) }
    finally { operationLock.current=false; if(mounted.current) setBusy(false) }
  }
  const copy = async () => {
    try { await navigator.clipboard.writeText(bundle); showToast?.(t('Copied')) }
    catch { setError(t('Clipboard unavailable')) }
  }
  return <div className="u-agent-enrollment">
    {!open && <button className="btn btn-primary" disabled={disabled || busy} onClick={()=>setOpen(true)}>{t('Add Agent')}</button>}
    {error && <p role="alert" className="u-error">{t(error)}</p>}
    {open && <>
      <h4>{t('Add Agent')}</h4>
      {!pending && !bundle && <div className="u-form-grid">
        <label className="u-field">{t('Agent connection URL')}<input aria-label={t('Agent connection URL')} value={server} disabled={busy} onChange={event=>{setServer(event.target.value);setConfirmed(false)}}/></label>
        <label className="u-field">{t('Certificate trust')}<select aria-label={t('Certificate trust')} value={mode} disabled={busy} onChange={event=>{setMode(event.target.value);setConfirmed(false)}}>
          <option value="system">{t('System CA validation')}</option><option value="pin">{t('Fixed certificate fingerprint')}</option>
        </select></label>
        {mode === 'pin' && <>
          <label className="u-field">{t('Trusted leaf SHA-256')}<input aria-label={t('Trusted leaf SHA-256')} value={pin} disabled={busy} onChange={event=>{setPin(event.target.value);setConfirmed(false)}}/></label>
          {fingerprint && <button className="btn btn-ghost" disabled={busy} onClick={()=>{setPin(fingerprint);setConfirmed(false)}}>{t('Use this Core certificate')}</button>}
          <label><input type="checkbox" checked={confirmed} disabled={busy} onChange={event=>setConfirmed(event.target.checked)}/>{t('This trusted fingerprint belongs to the connection URL, not a different proxy endpoint.')}</label>
        </>}
      </div>}
      {pending && <div className="u-note"><b>{pending.agent_id}</b><div style={{overflowWrap:'anywhere'}}>{pending.server_url}</div><div>{t(bundle ? 'Credential created; Agent connection not yet verified' : 'Enrollment pending; retry preserves this identity')}</div></div>}
      {bundle ? <>
        <p className="u-muted">{t('Sensitive connection package. This token remains valid until revoked or rotated.')}</p>
        <input aria-label={t('Agent connection package')} type="password" readOnly value={bundle} autoComplete="off" style={{width:'100%'}}/>
        <div className="u-inline"><button className="btn btn-primary" onClick={copy}>{t('Copy connection package')}</button>
          <button className="btn btn-ghost" onClick={()=>{savePendingEnrollment(null);setBundle('');setPending(null);setOpen(false)}}>{t('Close')}</button></div>
      </> : <div className="u-inline">
        <button className="btn btn-primary" disabled={busy || disabled || invalidPending} onClick={create}>{t(busy ? 'Saving...' : pending ? 'Retry this enrollment' : 'Create credential')}</button>
        <button className="btn btn-ghost" disabled={busy} onClick={()=>setOpen(false)}>{t('Close')}</button>
      </div>}
    </>}
  </div>
}
