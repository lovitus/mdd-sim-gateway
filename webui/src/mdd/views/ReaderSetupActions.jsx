import React, { useEffect, useRef, useState } from 'react'
import { dialogs } from '../../dialogs.js'
import { api } from '../api.js'
import { lineForm, savedCatalogLine } from '../lineAdapter.js'
import { rememberProvision, recalledProvision, forgetProvision, provisionTerminal } from '../provisionRecovery.js'
import { useI18n } from '../i18n.jsx'

// A view of existing catalog/operation/runtime owners, not another setup controller.
export default function ReaderSetupActions({form, device, save, onSaved, refresh, operationLock, blocked, imeiReady}) {
  const {t} = useI18n()
  const [working,setWorking] = useState(false)
  const [message,setMessage] = useState('')
  const [failure,setFailure] = useState('')
  const [receipt,setReceipt] = useState(null)
  const [receiptState,setReceiptState] = useState('')
  const [pendingApply,setPendingApply] = useState(null)
  const [applyState,setApplyState] = useState(null)
  const [widerApply,setWiderApply] = useState(false)
  const [facts,setFacts] = useState(null)
  const [loaded,setLoaded] = useState(false)
  const current = useRef('')
  const mounted = useRef(true)
  const raw = device?.go_device || {}
  const applyKey=`mdd_reader_setup_apply_${form.id}`
  const identity = JSON.stringify([form,device?.id,device?.sim?.iccid,device?.present,device?.stale,
    raw.agent_id,raw.process_generation,raw.reader?.reader_name,raw.reader?.session_generation])
  current.current = identity
  useEffect(()=>{mounted.current=true;return()=>{mounted.current=false}},[])
  useEffect(()=>{
    setFacts(device?.facts || null)
  },[device?.facts])
  useEffect(()=>{
    setReceipt(null);setLoaded(false);setFailure('');setMessage('');setApplyState(null);setWiderApply(false)
    let cancelled=false
    try {
      setReceipt(form.id ? recalledProvision(form.id) : null)
      const retained=JSON.parse(sessionStorage.getItem(applyKey) || 'null')
      if(retained && (retained.line_id!==form.id || retained.card_id!==form.iccid || !Number.isSafeInteger(retained.revision)))throw new Error('apply_recovery_identity_changed')
      setPendingApply(retained)
    }
    catch(error){setFailure(error.message)}
    if(!form.id || !form.__catalog_line?.enabled){setLoaded(true);return}
    api.providerApplyStatus().then(value=>{if(!cancelled)setApplyState(value)})
      .catch(error=>{if(!cancelled)setFailure(error.message)})
      .finally(()=>{if(!cancelled)setLoaded(true)})
    return()=>{cancelled=true}
  },[form.id,form.__catalog_revision])
  const baseline=form.__catalog_line
  let saved=null
  try {saved=savedCatalogLine(form)} catch {}
  const dirty=!!baseline && !saved
  const exact=!!device && device.present===true && !device.stale && !device.observed_only &&
    !!device.sim?.iccid && !!raw.agent_id && !!raw.process_generation && !!raw.reader?.session_generation &&
    !!(raw.reader?.reader_name || device.reader) && (!baseline || baseline.card_id===device.sim.iccid)
  const disabled=baseline?.enabled!==true
  const draft=baseline?.hardware_provision_state==='draft'
  const intent=facts?.facts?.vowifi_intent
  const intentOn=intent?.fresh===true && intent.code==='vowifi_enabled'
  const intentOff=intent?.fresh===true && intent.code==='vowifi_disabled'
  const engine=facts?.facts?.engine_process
  const applied=engine?.fresh===true && engine.available===true && engine.code==='provider_reachable'
  const runtime=facts?.facts?.vowifi_runtime
  const running=runtime?.fresh===true && runtime.available===true
  const missing=[]
  if(!/^\d{5,18}$/.test(form.imsi || ''))missing.push('IMSI / PIN')
  if(!/^\d{3}$/.test(form.mcc || '') || !/^\d{2,3}$/.test(form.mnc || ''))missing.push('MCC / MNC')
  if(!/^\+?\d{3,32}$/.test(form.smsc || ''))missing.push(t('SMS centre (SMSC)'))
  const busy=working || blocked || !loaded
  const check=context=>{
    if(!mounted.current || current.current!==context)throw new Error(t('SIM or configuration changed; review again.'))
  }
  const run=async action=>{
    if(operationLock.current || busy)return
    operationLock.current=true;setWorking(true);setMessage('');setFailure('')
    const context=identity
    try{await action(context)}catch(error){if(mounted.current && current.current===context)setFailure(error.message)}
    finally{operationLock.current=false;if(mounted.current)setWorking(false)}
  }
  const observe=async context=>{
    const value=await api.lineFacts(form.id)
    const status=await api.providerApplyStatus()
    check(context);setFacts(value);setApplyState(status)
    if(pendingApply && status.applied_revision===pendingApply.revision && !status.applying &&
      value.facts?.engine_process?.fresh && value.facts.engine_process.code==='provider_reachable'){
      // Evidence establishes the current target, not the fate of a lost response.
      sessionStorage.setItem(`${applyKey}:${pendingApply.id}`,JSON.stringify({...pendingApply,observed_applied:true}))
      sessionStorage.removeItem(applyKey);setPendingApply(null)
    }
    await refresh?.()
    return value
  }
  const readOriginal=()=>run(async context=>{
    const result=await api.operationStatus(receipt.operation_id)
    if(result.operation_id!==receipt.operation_id || result.line_id!==receipt.line_id)throw new Error('provision_receipt_identity_changed')
    if(provisionTerminal(result.state))forgetProvision(receipt)
    check(context)
    if(provisionTerminal(result.state))setReceipt(null)
    setMessage(`${result.state || 'unknown'} · ${result.error_code || ''} · ${receipt.operation_id}`)
    setReceiptState(result.state || 'unknown')
    if(result.state==='succeeded'){const value=await api.lineConfiguration(form.id);check(context);onSaved(value)}
    await refresh?.()
  })
  const initialize=(newVerification=false)=>run(async context=>{
    savedCatalogLine(form)
    if(!exact || (receipt && !(newVerification && receipt.kind==='reader' && receiptState==='unknown')) || missing.length)throw new Error(t('Current reader identity or saved configuration is incomplete.'))
    if(!(await dialogs.confirm(t('Verify this exact SIM and complete initialization? The line remains disabled; no PIN or SIM write is submitted.'))))return
    check(context)
    if(receipt){
      sessionStorage.setItem(`mdd_provision_recovery_${form.id}:${receipt.operation_id}`,JSON.stringify(receipt))
      forgetProvision(receipt)
    }
    const request={kind:'reader',operation_id:crypto.randomUUID(),line_id:form.id,card_id:form.iccid,
      agent_id:raw.agent_id,process_generation:raw.process_generation,reader_name:raw.reader?.reader_name || device.reader,
      sim_session_generation:raw.reader?.session_generation,catalog_revision:String(form.__catalog_revision)}
    rememberProvision(request);setReceipt(request)
    const result=await api.readerProvisionV1(device,form.id,form.__catalog_revision,request.operation_id)
    if(result.operation_id!==request.operation_id || (result.line_id && result.line_id!==request.line_id) || (result.line && result.line.id!==request.line_id))throw new Error('provision_receipt_identity_changed')
    if(provisionTerminal(result.state))forgetProvision(request)
    check(context)
    if(provisionTerminal(result.state))setReceipt(null)
    setMessage(`${result.state || 'unknown'} · ${result.error_code || ''}`)
    if(result.state==='succeeded'){
      const value=await api.lineConfiguration(form.id);check(context);onSaved(value)
    }
    await refresh?.()
  })
  const setIntent=enabled=>run(async context=>{
    if(!(await dialogs.confirm(t(enabled ? 'Start VoWiFi for this SIM? No call or SMS will be sent.' : 'Cancel this SIM’s saved start request and stop VoWiFi?'))))return
    check(context)
    const target={...device,instance_id:form.id,runtime_intent_target:{line_id:form.id,card_id:form.iccid}}
    let error
    try{await api.patchDeviceCapabilities(device.id,{vowifi_enabled:enabled},target)}catch(value){error=value}
    const value=await observe(context)
    check(context)
    if(!dirty){const latest=await api.lineConfiguration(form.id);check(context);onSaved(latest)}
    if(error)throw error
    setMessage(t(value.facts?.vowifi_runtime?.available ? 'VoWiFi runtime is running; check carrier registration below.' : 'Intent saved; waiting for the observed runtime state.'))
  })
  const enable=()=>run(async context=>{
    const line=savedCatalogLine(form)
    if(!(await dialogs.confirm(t(line.enabled
      ? 'Disable this saved line? Its saved start intent is retained. Stop VoWiFi before archiving.'
      : 'Enable this saved line? Any retained or concurrent start request may become active. Other SIM switches remain unchanged.'))))return
    check(context)
    const result=await api.saveCatalogLine({...line,enabled:!line.enabled},form.__catalog_revision)
    check(context);onSaved(lineForm(result.line,result.revision));await refresh?.()
  })
  const apply=wide=>run(async context=>{
    if(pendingApply)throw new Error(t('Application is not confirmed; read status before any further action.'))
    savedCatalogLine(form)
    const revision=form.__catalog_revision
    if(!(await dialogs.confirm(t(wide
      ? 'Apply ALL pending Provider configuration at revision {revision}? This can add, change or remove other lines and restart affected Providers. No automatic retry will occur.'
      : 'Apply only this new Provider at revision {revision}? Any plan changing other Providers will be rejected.',{revision}))))return
    check(context)
    const attempt={id:crypto.randomUUID(),line_id:form.id,card_id:form.iccid,revision,wide}
    sessionStorage.setItem(applyKey,JSON.stringify(attempt));setPendingApply(attempt)
    try{
      const result=await api.applyProviderConfig(revision,wide ? '' : form.id)
      check(context)
      setMessage(`${result.state || 'unknown'} · ${result.code || ''}`)
      if(result.state!=='applied')throw new Error(t('Application is not confirmed; read status before any further action.'))
      sessionStorage.setItem(`${applyKey}:${attempt.id}`,JSON.stringify({...attempt,result}))
      sessionStorage.removeItem(applyKey);setPendingApply(null)
      await observe(context)
    }catch(error){
      check(context)
      // A scoped rejection is not permission to publish the whole revision.
      if(error.code==='provider_apply_blocked')setWiderApply(true)
      if(['provider_apply_blocked','catalog_revision_changed','provider_render_failed','egress_status_invalid','provider_apply_in_progress','configuration_apply_in_progress'].includes(error.code)){
        sessionStorage.setItem(`${applyKey}:${attempt.id}`,JSON.stringify({...attempt,rejected:error.code}))
        sessionStorage.removeItem(applyKey);setPendingApply(null)
      }
      throw error
    }
  })
  let stage=!baseline ? 'Configuration not saved' : draft ? 'Configuration saved; initialization required' : disabled ? 'Initialization complete; line disabled' :
    !applied ? 'Provider configuration not confirmed for this SIM' : running ? 'VoWiFi runtime running' : intentOn ? 'Start requested; waiting for runtime' : 'Ready to start VoWiFi'
  if(!exact)stage='Current reader identity is not confirmed'
  if(receipt)stage='Initialization result requires confirmation'
  return <section className="u-setup" aria-label={t('Current SIM setup')}>
    <strong>{t(stage)}</strong>
    {baseline && <div className="mono">{form.name || form.id} · ICCID {form.iccid}</div>}
    {intentOn && disabled && <p className="u-error">{t('An earlier start request is saved. Cancel it before preparing a stopped line, or explicitly enable with that request retained.')}</p>}
    {!intentOff && disabled && baseline && <p>{t('Stopped intent is not confirmed. Enable may activate a saved start request; Save and initialization will not enable the line.')}</p>}
    {!!missing.length && (!baseline || draft) && <p>{t('Missing information')}: {missing.join(' · ')}</p>}
    {dirty && <p>{t('Save the edited configuration before continuing.')}</p>}
    {pendingApply && <p className="u-error">{t('Application is not confirmed; read status before any further action.')} · {pendingApply.revision}</p>}
    {!imeiReady && <p>{t('Set the current SIM IMEI in the Hardware tab before enabling VoWiFi.')}</p>}
    <div className="u-inline">
      {(!baseline || dirty) && <button className="btn btn-primary" disabled={busy || !exact} onClick={save}>{t(baseline ? 'Save' : 'Save draft')}</button>}
      {receipt ? <button className="btn btn-primary" disabled={busy} onClick={readOriginal}>{t('Check original result')}</button> : baseline && !dirty && <>
        {draft ? <button className="btn btn-primary" disabled={busy || !exact || !!missing.length} onClick={()=>initialize()}>{t('Complete initialization')}</button>
          : disabled ? <button className="btn btn-primary" disabled={busy || !exact || !imeiReady} onClick={enable}>{t('Enable line')}</button>
          : !running && (!applied || applyState?.pending) ? <button className="btn btn-primary" disabled={busy || !exact || !applyState || applyState.applying || !!pendingApply} onClick={()=>apply(applied || widerApply)}>{t(applied || widerApply ? 'Review pending Provider configuration' : 'Apply this new line')}</button>
          : !intentOn && <button className="btn btn-primary" disabled={busy || !exact || !imeiReady} onClick={()=>setIntent(true)}>{t('Start VoWiFi')}</button>}
      </>}
      {receipt?.kind==='reader' && receiptState==='unknown' && <button className="btn btn-ghost" disabled={busy || dirty || !exact} onClick={()=>initialize(true)}>{t('New read-only SIM verification')}</button>}
      {baseline && (intentOn || !intentOff) && <button className="btn btn-ghost" disabled={busy} onClick={()=>setIntent(false)}>{t('Cancel pending start')}</button>}
      {baseline?.enabled && <button className="btn btn-ghost" disabled={busy || dirty} onClick={enable}>{t('Disable line')}</button>}
      {baseline && <button className="btn btn-ghost" disabled={busy} onClick={()=>run(observe)}>{t('Read current state')}</button>}
    </div>
    {widerApply && <p>{t('Restricted application was rejected. Review the actual blocker before considering a separately confirmed revision-wide application.')}</p>}
    {message && <p role="status">{message}</p>}
    {failure && <p role="alert" className="u-error">{failure}</p>}
    {baseline?.enabled && !running && runtime?.code && <p>{t(runtime.code)}</p>}
  </section>
}
