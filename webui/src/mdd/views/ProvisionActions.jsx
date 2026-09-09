import React, { useEffect, useRef, useState } from 'react'
import { api } from '../api.js'
import { candidateForDevice, claimedDraftForm, savedCatalogLine, modemProvisionIntent } from '../lineAdapter.js'
import { useI18n } from '../i18n.jsx'
import {rememberProvision,recalledProvision,forgetProvision,provisionTerminal} from '../provisionRecovery.js'

// The existing Go preflight/provision/apply workflow, within the original SIM form.
export default function ProvisionActions({form,device,onSaved,refresh,operationLock,blocked = false}) {
  const {t} = useI18n()
  const [candidates,setCandidates] = useState(null)
  const [proof,setProof] = useState(null)
  const [reconcile,setReconcile] = useState(null)
  const [working,setBusy] = useState(false)
  const [recovering,setRecovering] = useState(true)
  const [recoveryError,setRecoveryError] = useState('')
  const busy = working || blocked || recovering || Boolean(recoveryError)
  const unresolved = Boolean(reconcile && String(reconcile.line_id) === String(form.id))
  const [message,setMessage] = useState('')
  const [candidateError,setCandidateError] = useState('')
  const [candidateRead,setCandidateRead] = useState(0)
  const localLock = useRef(false)
  const inFlight = operationLock || localLock
  const claimOperation = useRef(null)
  const generation = useRef(0)
  const raw = device?.go_device || {}
  useEffect(()=>{
    let stopped=false
    setRecovering(true);setRecoveryError('');setReconcile(null)
    try {
      const stored=form.id ? recalledProvision(String(form.id)) : null
      if(stored){
        setReconcile(stored)
        api.operationStatus(stored.operation_id).then(result=>{
          if(stopped)return
          if(result.operation_id!==stored.operation_id || String(result.line_id)!==String(form.id))throw new Error('provision_receipt_identity_changed')
          if(provisionTerminal(result.state)){forgetProvision(stored);setReconcile(null)}
          setMessage(result.state || 'unknown')
        }).catch(error=>{if(!stopped)setMessage(error.message)}).finally(()=>{if(!stopped)setRecovering(false)})
      }else setRecovering(false)
    }catch(error){setRecoveryError(error.message);setRecovering(false)}
    return ()=>{stopped=true}
  },[form.id])
  const endpointIdentity = JSON.stringify([device?.id,device?.sim?.iccid,raw.agent_id,raw.process_generation,
    raw.reader?.reader_name,raw.reader?.session_generation,raw.modem?.equipment_id,
    raw.modem?.attachment_id,raw.modem?.sim_session_generation || raw.modem?.sim?.session_generation])
  const identity = JSON.stringify([form,device?.id,device?.sim?.iccid,raw.agent_id,raw.process_generation,
    raw.reader?.reader_name,raw.reader?.session_generation,raw.modem?.equipment_id,
    raw.modem?.attachment_id,raw.modem?.sim_session_generation || raw.modem?.sim?.session_generation])
  useEffect(() => {
    ++generation.current; setProof(null)
    setCandidates(null);setCandidateError('');setMessage('')
    let stopped = false
    api.lineCandidates().then(value => {if (!stopped) setCandidates(value)}).catch(error => {if (!stopped) setCandidateError(error.message)})
    return () => {stopped = true}
  }, [endpointIdentity,candidateRead])
  useEffect(() => {++generation.current;setProof(null)}, [identity])
  const candidate = candidateForDevice(candidates?.candidates || [],device)
  let saved = null
  try {saved=savedCatalogLine(form)} catch {}
  const savedReady = !!saved && /^\d{5,18}$/.test(saved.sim.imsi || '') && /^\d{3}$/.test(saved.sim.mcc || '') &&
    /^\d{2,3}$/.test(saved.sim.mnc || '') && /^\+?\d{3,32}$/.test(saved.sim.smsc || '') &&
    (device?.device_type === 'reader' || /^\d{15}$/.test(saved.sim.imei || ''))
  const run = async action => {
    if (inFlight.current || busy) return
    inFlight.current = true; setBusy(true); setMessage('')
    const epoch = generation.current
    try { await action(epoch) }
    catch (error) {if(epoch === generation.current)setMessage(error.message)}
    finally {inFlight.current = false;setBusy(false)}
  }
  const reload = async epoch => {
    const value = await api.lineConfiguration(form.id)
    if (epoch === generation.current) onSaved(value)
    await refresh?.()
  }
  const claim = () => run(async epoch => {
    if (!candidate?.can_claim || candidate.configured_line_id) throw new Error('line_candidate_unavailable')
    if (!window.confirm(t('Create a disabled draft for this exact SIM?'))) return
    const name = form.name || candidate.observed?.msisdn || ''
    const key = JSON.stringify([candidate.candidate_id,name])
    if (!claimOperation.current || claimOperation.current.key !== key) claimOperation.current = {key,id:crypto.randomUUID()}
    const result = await api.claimLineCandidate(candidate.candidate_id,name,candidates.catalog_revision,claimOperation.current.id)
    if (epoch === generation.current) onSaved(claimedDraftForm(result,form))
    if (epoch === generation.current) setMessage(t('Disabled draft created. Hardware provisioning is still required.'))
    await refresh?.()
  })
  const readback = () => run(async epoch => {
    if (unresolved) throw new Error('provision_reconcile_required')
    const request = modemProvisionIntent(form,device,crypto.randomUUID())
    setProof(null)
    const result = await api.provisionReadbackV1(request)
    if (epoch === generation.current && result.state === 'succeeded') setProof({identity,request:{...request,sim_session_generation:result.sim_session_generation || request.sim_session_generation}})
    if (epoch === generation.current) setMessage(result.state || result.code || 'unknown')
  })
  const provision = () => run(async epoch => {
    if (unresolved) throw new Error('provision_reconcile_required')
    savedCatalogLine(form)
    if (!window.confirm(t('Provision this exact saved SIM configuration? The line will not start automatically.'))) return
    let result
    if (device?.device_type === 'reader') {
      if (device.sim?.iccid !== form.iccid) throw new Error('reader_card_identity_changed')
      result = await api.readerProvisionV1(device,form.id,form.__catalog_revision)
    } else {
      if (!proof || proof.identity !== identity) throw new Error('provision_preflight_required')
      const request = {...proof.request,operation_id:crypto.randomUUID(),preflight_operation_id:proof.request.operation_id}
      rememberProvision(request)
      setProof(null); setReconcile(request)
      result = await (form.provisioning_state === 'draft' ? api.provisionV1(request) : api.reprovisionV1(request))
      if (provisionTerminal(result.state)) {forgetProvision(request);setReconcile(null)}
    }
    if (epoch === generation.current) setMessage([result.state || 'unknown',result.error_code || ''].filter(Boolean).join(' · '))
    await reload(epoch)
  })
  const apply = () => run(async epoch => {
    savedCatalogLine(form)
    if (!window.confirm(t('Apply this exact catalog revision to VoWiFi Providers? Changed lines may restart.'))) return
    const result = await api.applyProviderConfig(form.__catalog_revision)
    if (epoch === generation.current) setMessage([result.state || 'unknown',result.code || ''].filter(Boolean).join(' · '))
    await refresh?.()
  })
  return <section>
    <div className="u-inline">
      {!form.__catalog_revision && <button className="btn btn-primary" disabled={busy || !candidate?.can_claim} onClick={claim}>{t('Create disabled draft')}</button>}
      {form.__catalog_revision && <>
        {device?.device_type !== 'reader' && <button className="btn btn-ghost" disabled={busy || unresolved || !device || !savedReady} onClick={readback}>{t('Verify hardware state')}</button>}
        <button className="btn btn-ghost" disabled={busy || unresolved || !device || !savedReady || (device.device_type !== 'reader' && (!proof || proof.identity !== identity))} onClick={provision}>{t('Provision hardware')}</button>
        <button className="btn btn-ghost" disabled={busy} onClick={apply}>{t('Apply saved catalog')}</button>
      </>}
      {reconcile && reconcile.line_id === form.id && <button className="btn btn-ghost" disabled={busy} onClick={() => run(async epoch => {
        const status = await api.operationStatus(reconcile.operation_id)
        if (status.operation_id !== reconcile.operation_id || String(status.line_id) !== String(form.id)) throw new Error('provision_receipt_identity_changed')
        if (provisionTerminal(status.state)) {forgetProvision(reconcile);setReconcile(null);await reload(epoch);return}
        if (status.state !== 'unknown') {if(epoch===generation.current)setMessage(status.state || 'unknown');return}
        const result = await api.reconcileProvisionV1(reconcile)
        if (epoch === generation.current) setMessage(result.state || 'unknown'); if (provisionTerminal(result.state)) {forgetProvision(reconcile);setReconcile(null)}
        await reload(epoch)
      })}>{t('Read back and reconcile')}</button>}
    </div>
    {message && <p role="status" className="u-note">{message}</p>}
    {recoveryError && <p role="alert" className="u-error">{recoveryError}</p>}
    {candidateError && <p role="alert" className="u-error">{candidateError} <button className="btn btn-ghost" disabled={busy} onClick={()=>setCandidateRead(value=>value+1)}>{t('Retry')}</button></p>}
    {!form.__catalog_revision && candidate?.provision_blockers?.length > 0 && <p>{candidate.provision_blockers.join(', ')}</p>}
  </section>
}
