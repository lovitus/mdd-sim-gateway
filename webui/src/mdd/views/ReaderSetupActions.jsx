import React, { useEffect, useRef, useState } from 'react'
import { dialogs } from '../../dialogs.js'
import { api } from '../api.js'
import { lineForm, savedCatalogLine } from '../lineAdapter.js'
import { rememberProvision, recalledProvision, forgetProvision, provisionTerminal } from '../provisionRecovery.js'
import { useI18n } from '../i18n.jsx'
import {pendingProviderApply, rememberProviderApply, archiveProviderApply, providerApplyRejected, currentProviderReady, providerApplyObserved} from '../providerApplyRecovery.js'

const baselineKey = form => JSON.stringify([form.id, form.__catalog_revision, form.__catalog_line])

// A finite user action over the existing owners, never a background reconciler.
export default function ReaderSetupActions({form, device, save, saveConfiguration, editorVersion, onSaved, refresh,
  operationLock, blocked, imeiReady, onNavigateToHardware, onReadIdentity, onFocusField}) {
  const {t} = useI18n()
  const [working,setWorking] = useState(false)
  const [progress,setProgress] = useState('')
  const [message,setMessage] = useState('')
  const [failure,setFailure] = useState('')
  const [receipt,setReceipt] = useState(null)
  const [receiptState,setReceiptState] = useState('')
  const [pendingApply,setPendingApply] = useState(null)
  const [applyState,setApplyState] = useState(null)
  const [widerApply,setWiderApply] = useState(false)
  const [facts,setFacts] = useState(null)
  const [observationCurrent,setObservationCurrent] = useState(false)
  const [loaded,setLoaded] = useState(false)
  const [recoveryError,setRecoveryError] = useState('')
  const current = useRef(null)
  const active = useRef(null)
  const mounted = useRef(true)
  const raw = device?.go_device || {}
  const physical = JSON.stringify([device?.id,device?.sim?.iccid,device?.present,device?.stale,device?.observed_only,
    raw.agent_id,raw.process_generation,raw.reader?.reader_name,raw.reader?.session_generation])
  current.current = {physical,editorVersion,form}
  useEffect(()=>{mounted.current=true;return()=>{mounted.current=false}},[])
  useEffect(()=>{
    if(active.current?.physical===physical)return
    setReceipt(null);setReceiptState('');setPendingApply(null);setRecoveryError('');setLoaded(false)
    setFailure('');setMessage('');setApplyState(null);setWiderApply(false);setFacts(null);setObservationCurrent(false)
    let cancelled=false
    try {setReceipt(form.id ? recalledProvision(form.id) : null);setPendingApply(pendingProviderApply())}
    catch(error){setRecoveryError(error.message)}
    if(!form.id){setLoaded(true);return}
    Promise.all([api.providerApplyStatus(),api.lineFacts(form.id)]).then(([status,value])=>{
      if(!cancelled){setApplyState(status);setFacts(value);setObservationCurrent(true)}
    }).catch(error=>{if(!cancelled)setFailure(error.message)}).finally(()=>{if(!cancelled)setLoaded(true)})
    return()=>{cancelled=true}
  },[physical,form.id,form.__catalog_revision])
  const baseline=form.__catalog_line
  let saved=null
  try {saved=savedCatalogLine(form)} catch {}
  const dirty=!!baseline && !saved
  const exact=!!device && device.present===true && !device.stale && !device.observed_only &&
    !!device.sim?.iccid && !!raw.agent_id && !!raw.process_generation && !!raw.reader?.session_generation &&
    !!(raw.reader?.reader_name || device.reader) && (!baseline || baseline.card_id===device.sim.iccid)
  const disabled=baseline?.enabled!==true
  const draft=baseline?.hardware_provision_state==='draft'
  const upgradeRequired=device?.reader_sim_identity_supported===false
  const verificationBlocked=upgradeRequired && (!baseline || draft)
  const intent=facts?.facts?.vowifi_intent
  const intentOn=observationCurrent && intent?.fresh===true && intent.code==='vowifi_enabled'
  const intentOff=observationCurrent && intent?.fresh===true && intent.code==='vowifi_disabled'
  const applied=observationCurrent && currentProviderReady(facts,form.id)
  const runtime=facts?.facts?.vowifi_runtime
  const running=applied && runtime?.fresh===true && runtime.available===true
  const missing=[]
  if(!/^\d{5,18}$/.test(form.imsi || ''))missing.push(['imsi','IMSI / PIN'])
  if(!/^\d{3}$/.test(form.mcc || ''))missing.push(['mcc','MCC'])
  if(!/^\d{2,3}$/.test(form.mnc || ''))missing.push(['mnc','MNC'])
  if(!/^\+?\d{3,32}$/.test(form.smsc || ''))missing.push(['smsc',t('SMS centre (SMSC)')])
  if(!/^[a-z]{2}$/.test(form.proxy_country || ''))missing.push(['country',t('Country exit')])
  const busy=working || blocked || !loaded || !!recoveryError
  const check=(context,allowCancelled=false)=>{
    const now=current.current
    if(!mounted.current || (context.cancelled && !allowCancelled) || now.physical!==context.physical || now.editorVersion!==context.editorVersion ||
      !context.baselines.has(baselineKey(now.form)))throw new Error(t('SIM or configuration changed; review again.'))
  }
  const run=async action=>{
    if(operationLock.current || busy)return
    const context={physical,editorVersion,device:structuredClone(device),form,baselines:new Set([baselineKey(form)]),cancelled:false,
      intentCode:observationCurrent && intent?.fresh===true ? intent.code : ''}
    operationLock.current=true;active.current=context;setWorking(true);setMessage('');setFailure('');setProgress('')
    try{await action(context)}catch(error){
      if(mounted.current && current.current.physical===context.physical)setFailure(context.cancelled ? t('Remaining steps cancelled. Completed operations are retained.') : error.message)
    }finally{
      operationLock.current=false;active.current=null
      if(mounted.current){setWorking(false);setProgress('');setLoaded(true)}
    }
  }
  // Admit only this invocation's exact server responses before publishing them
  // into React. User edits, replacement attachments and other revisions stop it.
  const accept=context=>value=>{
    // An admitted write can complete after Cancel. Keep its exact result in
    // the original editor, without granting permission for another dispatch.
    check(context,true)
    if(!value.id || value.iccid!==context.device.sim.iccid || (context.form.id && value.id!==context.form.id) ||
      !Number.isSafeInteger(value.__catalog_revision))throw new Error('setup_configuration_identity_changed')
    context.baselines.add(baselineKey(value));context.form=value
  }
  const publish=(context,value)=>{accept(context)(value);onSaved(value)}
  const unchanged=async context=>{
    check(context)
    const value=await api.lineConfiguration(context.form.id)
    check(context)
    if(baselineKey(value)!==baselineKey(context.form))throw new Error(t('Configuration changed elsewhere. Reload and review before continuing.'))
  }
  const observe=async context=>{
    check(context);setObservationCurrent(false)
    const value=await api.lineFacts(context.form.id)
    check(context)
    const status=await api.providerApplyStatus()
    check(context);setFacts(value);setApplyState(status);setObservationCurrent(true)
    const pending=pendingProviderApply()
    setPendingApply(pending)
    if(pending){
      const original=pending.line_id===context.form.id ? value : await api.lineFacts(pending.line_id)
      check(context)
      const configuration=await api.lineConfiguration(pending.line_id)
      check(context)
      if(providerApplyObserved(pending,status,original,configuration)){
        archiveProviderApply(pending,{observed_applied:true});setWiderApply(false)
      }
      setPendingApply(pendingProviderApply())
    }
    const code=value.facts?.vowifi_intent?.fresh===true ? value.facts.vowifi_intent.code : ''
    if(context.activating && code==='vowifi_disabled' && context.intentCode && context.intentCode!=='vowifi_disabled'){
      throw new Error(t('A newer stop request was observed. Activation stopped.'))
    }
    if(code)context.intentCode=code
    return {value,status}
  }
  const archiveInitialization=(request,result)=>{
    sessionStorage.setItem(`mdd_provision_recovery_${request.line_id}:${request.operation_id}`,JSON.stringify({...request,result}))
    forgetProvision(request)
  }
  const readOriginal=()=>run(async context=>{
    const result=await api.operationStatus(receipt.operation_id)
    if(result.operation_id!==receipt.operation_id || result.line_id!==receipt.line_id)throw new Error('provision_receipt_identity_changed')
    if(provisionTerminal(result.state))archiveInitialization(receipt,result)
    check(context)
    if(provisionTerminal(result.state))setReceipt(null)
    setMessage(`${result.state || 'unknown'} · ${result.error_code || ''} · ${receipt.operation_id}`)
    setReceiptState(result.state || 'unknown')
    if(result.state==='succeeded'){
      const value=await api.lineConfiguration(context.form.id)
      check(context);publish(context,value)
    }
    await refresh?.()
  })
  const initialize=async(context,newVerification=false)=>{
    const line=savedCatalogLine(context.form)
    const prior=recalledProvision(line.id)
    if(!exact || line.enabled || line.hardware_provision_state!=='draft' ||
      (prior && !(newVerification && prior.kind==='reader' && receiptState==='unknown')))throw new Error(t('Current reader identity or saved configuration is incomplete.'))
    check(context)
    if(prior)archiveInitialization(prior,{state:'unknown',superseded_by_explicit_verification:true})
    const reader=context.device.go_device
    const request={kind:'reader',scope:'sim_identity',operation_id:crypto.randomUUID(),line_id:line.id,card_id:line.card_id,
      agent_id:reader.agent_id,process_generation:reader.process_generation,reader_name:reader.reader?.reader_name || context.device.reader,
      sim_session_generation:reader.reader?.session_generation,catalog_revision:String(context.form.__catalog_revision)}
    rememberProvision(request);setReceipt(request);setProgress('Checking SIM...')
    let result
    try {result=await api.readerProvisionV1(context.device,line.id,context.form.__catalog_revision,request.operation_id,{scope:request.scope})}
    catch(error){
      const unsupported=error.code==='reader_sim_identity_unsupported' && error.status===409 && error.data?.not_dispatched===true
      if(unsupported || ['reader_provision_target_unavailable','reader_provision_requires_disabled_draft','reader_provision_smsc_required','invalid_reader_provision_request'].includes(error.code)){
        archiveInitialization(request,{rejected:error.code})
        if(mounted.current && current.current.physical===context.physical){setReceipt(null);setReceiptState('')}
      }
      if(unsupported)error.message=t('Update the Agent before verifying this SIM. No card request was sent.')
      throw error
    }
    if(result.operation_id!==request.operation_id || (result.line_id && result.line_id!==line.id) ||
      (result.line && (result.line.id!==line.id || result.line.card_id!==line.card_id)))throw new Error('provision_receipt_identity_changed')
    if(result.state==='succeeded' && (!result.line || !Number.isSafeInteger(result.catalog_revision)))throw new Error('provision_configuration_receipt_missing')
    if(provisionTerminal(result.state))archiveInitialization(request,result)
    check(context);setReceiptState(result.state || 'unknown')
    if(provisionTerminal(result.state))setReceipt(null)
    if(result.state!=='succeeded')throw new Error(`${result.state || 'unknown'} · ${result.error_code || ''}`)
    publish(context,lineForm(result.line,result.catalog_revision));await refresh?.();check(context)
  }
  const apply=async(context,wide,status)=>{
    check(context)
    if(pendingProviderApply())throw new Error(t('Application is not confirmed; read status before any further action.'))
    const line=savedCatalogLine(context.form),revision=context.form.__catalog_revision
    const attempt={id:crypto.randomUUID(),line_id:line.id,card_id:line.card_id,revision,wide,enabled:line.enabled,previous_apply_id:status?.last_apply_id || ''}
    rememberProviderApply(attempt);setPendingApply(attempt);setProgress('Preparing VoWiFi...')
    try{
      const result=await api.applyProviderConfig(revision,wide ? '' : line.id)
      if(result.state!=='applied')throw new Error(t('Application is not confirmed; read status before any further action.'))
      archiveProviderApply(attempt,{result})
      check(context,true);setPendingApply(pendingProviderApply());setWiderApply(false)
      check(context)
    }catch(error){
      if(providerApplyRejected(error))archiveProviderApply(attempt,{rejected:error.code})
      check(context)
      if(error.code==='provider_apply_blocked')setWiderApply(true)
      setPendingApply(pendingProviderApply());throw error
    }
  }
  const writeIntent=async(context,enabled)=>{
    check(context)
    const target={...context.device,instance_id:context.form.id,runtime_intent_target:{line_id:context.form.id,card_id:context.form.iccid}}
    let error
    try{await api.patchDeviceCapabilities(target.id,{vowifi_enabled:enabled},target)}catch(value){error=value}
    check(context)
    const observed=await observe(context)
    await refresh?.();check(context)
    if(error)throw error
    return observed.value
  }
  const activate=(wide=false)=>run(async context=>{
    if(verificationBlocked)throw new Error(t('Update the Agent before verifying this SIM. No card request was sent.'))
    if(!exact || !imeiReady || missing.length || recalledProvision(form.id) || pendingProviderApply())throw new Error(t('Current reader identity or saved configuration is incomplete.'))
    // A revision-wide consent never covers a revision created after that consent.
    if(wide && (!baseline?.enabled || draft || dirty))throw new Error(t('Save configuration before reviewing its application.'))
    if(!(await dialogs.confirm(t(wide
      ? 'Apply ALL pending Provider configuration at revision {revision} and enable VoWiFi for ICCID {card}? This can change or restart other Providers. No automatic retry will occur.'
      : 'Enable VoWiFi for ICCID {card}? Save these settings, verify this SIM, and prepare its connection. A retained start request may become active during preparation. Changes to other Providers require separate confirmation. No call or SMS will be sent.',
    {revision:form.__catalog_revision,card:form.iccid || device.sim.iccid}))))return
    check(context);context.activating=true
    if(baseline)await unchanged(context)
    let changed=false
    if(!baseline || dirty){
      setProgress('Saving configuration...')
      await saveConfiguration({beforePublish:accept(context),beforeDispatch:()=>check(context)});check(context);changed=true
    }
    let observed=await observe(context)
    if(context.form.__catalog_line.hardware_provision_state==='draft'){
      await initialize(context);changed=true;observed=await observe(context)
    }
    await unchanged(context)
    if(!context.form.__catalog_line.enabled){
      setProgress('Preparing VoWiFi...')
      const result=await api.saveCatalogLine({...savedCatalogLine(context.form),enabled:true},context.form.__catalog_revision)
      check(context);publish(context,lineForm(result.line,result.revision));changed=true
      await refresh?.();check(context);observed=await observe(context)
    }
    const actualBeforeApply=observed.value.facts?.vowifi_runtime
    const alreadyRunning=actualBeforeApply?.fresh===true && actualBeforeApply.available===true
    if(wide || !currentProviderReady(observed.value,context.form.id) ||
      (observed.status.applied_revision!==context.form.__catalog_revision && (changed || !alreadyRunning))){
      await apply(context,wide,observed.status);observed=await observe(context)
    }
    await unchanged(context)
    if(pendingProviderApply() || !currentProviderReady(observed.value,context.form.id))throw new Error(t('Provider configuration not confirmed for this SIM'))
    const actual=observed.value.facts?.vowifi_runtime,intention=observed.value.facts?.vowifi_intent
    // Reissuing Start resets recovery state. A retained/current request already owns it.
    if(!(actual?.fresh && actual.available) && !(intention?.fresh && intention.code==='vowifi_enabled')){
      setProgress('Connecting VoWiFi...');observed.value=await writeIntent(context,true)
    }
    setMessage(t(observed.value.facts?.vowifi_runtime?.available ? 'VoWiFi runtime is running; check carrier registration below.' : 'Intent saved; waiting for the observed runtime state.'))
  })
  const stop=()=>run(async context=>{
    if(!(await dialogs.confirm(t('Cancel this SIM’s saved start request and stop VoWiFi?'))))return
    check(context);await writeIntent(context,false)
  })
  const disable=()=>run(async context=>{
    const line=savedCatalogLine(context.form)
    if(!(await dialogs.confirm(t('Disable this saved line? Its saved start intent is retained. Stop VoWiFi before archiving.'))))return
    await unchanged(context)
    const result=await api.saveCatalogLine({...line,enabled:false},context.form.__catalog_revision)
    check(context);publish(context,lineForm(result.line,result.revision));await refresh?.()
  })
  const reload=()=>run(async context=>{
    if(dirty && !(await dialogs.confirm(t('Discard unsaved edits and reload this SIM configuration?'))))return
    check(context)
    const value=await api.lineConfiguration(context.form.id)
    check(context);publish(context,value);await observe(context)
  })
  const openHardware=()=>run(async context=>{
    if(!baseline || dirty)await saveConfiguration({beforePublish:accept(context),beforeDispatch:()=>check(context)})
    check(context);onNavigateToHardware?.()
  })
  let stage=!baseline ? 'Configuration not saved' : running ? 'VoWiFi runtime running' : intentOn && !disabled && applied ? 'Start requested; waiting for runtime' : 'Ready to configure VoWiFi'
  if(!exact)stage='Current reader identity is not confirmed'
  if(verificationBlocked)stage='Agent update required for SIM verification'
  if(receipt)stage='Initialization result requires confirmation'
  return <section className="u-setup" aria-label={t('Current SIM setup')}>
    <strong>{t(progress || stage)}</strong>
    <div>{form.name || t('SIM')} · ICCID {form.iccid || device?.sim?.iccid}</div>
    {upgradeRequired && <p role="status" className="u-error">{t('This Agent cannot verify the current SIM. Update it before detecting or initializing this card. Saved configuration is retained.')} · {raw.agent_id}</p>}
    {!!missing.length && <div className="u-inline">{t('Missing information')}: {missing.map(([field,label])=><button key={field} className="btn btn-ghost" onClick={()=>onFocusField?.(field)}>{label}</button>)}<button className="btn btn-ghost" disabled={busy || !exact} onClick={onReadIdentity}>{t('Detect card')}</button></div>}
    {!imeiReady && <p>{t('Set the current SIM IMEI in the Hardware tab before enabling VoWiFi.')} <button className="btn btn-ghost" disabled={busy || !exact} onClick={openHardware}>{t(!baseline || dirty ? 'Save configuration and set IMEI' : 'Set SIM IMEI')}</button></p>}
    <div className="u-inline">
      <button className="btn btn-ghost" disabled={busy || !exact} onClick={save}>{t('Save configuration')}</button>
      {receipt ? <button className="btn btn-primary" disabled={busy} onClick={readOriginal}>{t('Check original result')}</button>
        : pendingApply || (intentOn && applied && !disabled && !dirty && !running) ? <button className="btn btn-primary" disabled={busy} onClick={()=>run(observe)}>{t('Read current state')}</button>
        : !running && <button className="btn btn-primary" disabled={busy || !exact || verificationBlocked || !imeiReady || !!missing.length || (widerApply && dirty)} onClick={()=>activate(widerApply)}>{t(widerApply ? 'Review pending configuration and enable' : !baseline || dirty ? 'Save and enable VoWiFi' : 'Enable VoWiFi')}</button>}
      {working && <button className="btn btn-ghost" onClick={()=>{if(active.current)active.current.cancelled=true;setProgress('Cancelling remaining steps...')}}>{t('Cancel remaining steps')}</button>}
    </div>
    {running && dirty && <p>{t('Save configuration without interrupting this call path. Review application separately after saving.')}</p>}
    {receipt?.kind==='reader' && receiptState==='unknown' && <button className="btn btn-ghost" disabled={busy || dirty || !exact || verificationBlocked || !!missing.length} onClick={()=>run(async context=>{
      if(!(await dialogs.confirm(t('Verify this exact SIM and complete initialization? The line remains disabled; no PIN or SIM write is submitted.'))))return
      check(context);await initialize(context,true)
    })}>{t('New read-only SIM verification')}</button>}
    {widerApply && <p>{t('Restricted application was rejected. Review the actual blocker before considering a separately confirmed revision-wide application.')}</p>}
    {pendingApply && <p className="u-error">{t('Application is not confirmed; read status before any further action.')} · {pendingApply.revision}</p>}
    {message && <p role="status">{message}</p>}
    {failure && <p role="alert" className="u-error">{failure}</p>}
    {recoveryError && <p role="alert" className="u-error">{recoveryError}</p>}
    <details><summary>{t('Technical details')}</summary>
      {baseline && <p className="mono">{form.id} · {form.__catalog_revision} · {disabled ? 'disabled' : 'enabled'} · {draft ? 'draft' : baseline.hardware_provision_state}</p>}
      {intentOn && disabled && <p>{t('An earlier start request is saved. Cancel it before preparing a stopped line, or explicitly enable with that request retained.')}</p>}
      {facts?.provider_error && <p className="u-error">{facts.provider_error}</p>}
      {!observationCurrent && facts?.provider && <p>{t('Previous observation is historical; read current state before starting.')}</p>}
      {baseline?.enabled && !running && runtime?.code && <p>{t(runtime.code)}</p>}
      {baseline && <div className="u-inline">
        <button className="btn btn-ghost" disabled={busy} onClick={()=>run(observe)}>{t('Read current state')}</button>
        <button className="btn btn-ghost" disabled={busy} onClick={reload}>{t('Reload configuration')}</button>
        {(intentOn || !intentOff) && <button className="btn btn-ghost" disabled={busy} onClick={stop}>{t('Cancel pending start')}</button>}
        {baseline.enabled && <button className="btn btn-ghost" disabled={busy || dirty} onClick={disable}>{t('Disable line')}</button>}
        {running && applyState?.pending && <button className="btn btn-ghost" disabled={busy || dirty || !!pendingApply} onClick={()=>activate(true)}>{t('Review pending Provider configuration')}</button>}
      </div>}
    </details>
  </section>
}
