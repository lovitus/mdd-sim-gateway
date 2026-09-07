import React, { useEffect, useRef, useState } from 'react'
import { api } from '../api.js'
import { candidateForDevice, lineForm, savedCatalogLine, modemProvisionIntent } from '../lineAdapter.js'
import { useI18n } from '../i18n.jsx'

// The existing Go preflight/provision/apply workflow, within the original SIM form.
export default function ProvisionActions({form,device,onSaved,refresh,operationLock,blocked = false}) {
  const {t} = useI18n()
  const [candidates,setCandidates] = useState(null)
  const [proof,setProof] = useState(null)
  const [reconcile,setReconcile] = useState(null)
  const [working,setBusy] = useState(false)
  const busy = working || blocked
  const [message,setMessage] = useState('')
  const localLock = useRef(false)
  const inFlight = operationLock || localLock
  const claimOperation = useRef(null)
  const generation = useRef(0)
  const raw = device?.go_device || {}
  const identity = JSON.stringify([form,device?.id,device?.sim?.iccid,raw.agent_id,raw.process_generation,
    raw.reader?.reader_name,raw.reader?.session_generation,raw.modem?.equipment_id,
    raw.modem?.attachment_id,raw.modem?.sim_session_generation || raw.modem?.sim?.session_generation])
  useEffect(() => {
    ++generation.current; setProof(null)
    let stopped = false
    api.lineCandidates().then(value => {if (!stopped) setCandidates(value)}).catch(error => {if (!stopped) setMessage(error.message)})
    return () => {stopped = true}
  }, [device?.id,device?.sim?.iccid])
  useEffect(() => {++generation.current;setProof(null)}, [identity])
  const candidate = candidateForDevice(candidates?.candidates || [],device)
  let saved = null
  try {saved=savedCatalogLine(form)} catch {}
  const savedReady = !!saved && /^\d{5,18}$/.test(saved.sim.imsi || '') && /^\d{3}$/.test(saved.sim.mcc || '') &&
    /^\d{2,3}$/.test(saved.sim.mnc || '') && /^\+?\d{3,32}$/.test(saved.sim.smsc || '') &&
    (device?.device_type === 'reader' || /^\d{15}$/.test(saved.sim.imei || ''))
  const run = async action => {
    if (inFlight.current || blocked) return
    inFlight.current = true; setBusy(true); setMessage('')
    const epoch = generation.current
    try { await action(epoch) }
    catch (error) {setMessage(error.message)}
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
    if (epoch === generation.current) onSaved(lineForm(result.line,result.revision))
    setMessage(t('Disabled draft created. Hardware provisioning is still required.'))
    await refresh?.()
  })
  const readback = () => run(async epoch => {
    const request = modemProvisionIntent(form,device,crypto.randomUUID())
    setProof(null)
    const result = await api.provisionReadbackV1(request)
    if (epoch === generation.current && result.state === 'succeeded') setProof({identity,request:{...request,sim_session_generation:result.sim_session_generation || request.sim_session_generation}})
    setMessage(result.state || result.code || 'unknown')
  })
  const provision = () => run(async epoch => {
    savedCatalogLine(form)
    if (!window.confirm(t('Provision this exact saved SIM configuration? The line will not start automatically.'))) return
    let result
    if (device?.device_type === 'reader') {
      if (device.sim?.iccid !== form.iccid) throw new Error('reader_card_identity_changed')
      result = await api.readerProvisionV1(device,form.id,form.__catalog_revision)
    } else {
      if (!proof || proof.identity !== identity) throw new Error('provision_preflight_required')
      const request = {...proof.request,operation_id:crypto.randomUUID(),preflight_operation_id:proof.request.operation_id}
      setProof(null); setReconcile(request)
      result = await (form.provisioning_state === 'draft' ? api.provisionV1(request) : api.reprovisionV1(request))
      if (result.state !== 'unknown') setReconcile(null)
    }
    setMessage([result.state || 'unknown',result.error_code || ''].filter(Boolean).join(' · '))
    await reload(epoch)
  })
  const apply = () => run(async () => {
    savedCatalogLine(form)
    if (!window.confirm(t('Apply this exact catalog revision to VoWiFi Providers? Changed lines may restart.'))) return
    const result = await api.applyProviderConfig(form.__catalog_revision)
    setMessage([result.state || 'unknown',result.code || ''].filter(Boolean).join(' · '))
    await refresh?.()
  })
  return <section>
    <div className="u-inline">
      {!form.__catalog_revision && <button className="btn btn-primary" disabled={busy || !candidate?.can_claim} onClick={claim}>{t('Create disabled draft')}</button>}
      {form.__catalog_revision && <>
        {device?.device_type !== 'reader' && <button className="btn btn-ghost" disabled={busy || !device || !savedReady} onClick={readback}>{t('Verify hardware state')}</button>}
        <button className="btn btn-ghost" disabled={busy || !device || !savedReady || (device.device_type !== 'reader' && (!proof || proof.identity !== identity))} onClick={provision}>{t('Provision hardware')}</button>
        <button className="btn btn-ghost" disabled={busy} onClick={apply}>{t('Apply saved catalog')}</button>
      </>}
      {reconcile && reconcile.line_id === form.id && <button className="btn btn-ghost" disabled={busy} onClick={() => run(async epoch => {
        const result = await api.reconcileProvisionV1(reconcile)
        setMessage(result.state || 'unknown'); if (result.state !== 'unknown') setReconcile(null)
        await reload(epoch)
      })}>{t('Read back and reconcile')}</button>}
    </div>
    {message && <p role="status" className="u-note">{message}</p>}
    {!form.__catalog_revision && candidate?.provision_blockers?.length > 0 && <p>{candidate.provision_blockers.join(', ')}</p>}
  </section>
}
