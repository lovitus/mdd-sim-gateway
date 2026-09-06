import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../api.js'
import { useI18n } from '../i18n.jsx'

function cloneLine(line) {
  return {
    schema_version: 1,
    id: line.id,
    name: line.name || '',
    enabled: line.enabled === true,
    hardware_provision_state: line.hardware_provision_state || '',
    card_id: line.card_id || '',
    sim: { imsi: '', mcc: '', mnc: '', imei: '', imeisv: '', msisdn: '', smsc: '', ...(line.sim || {}) },
    network: { epdg_address: '', pcscf: [], egress_country: '', apn_profiles: [], active_apn: '',
      ims_apn: 'ims', idr_mode: 'apn', cp_mode: 'auto', ...(line.network || {}) },
    ims: { ...(line.ims || {}) },
  }
}

function runtimeNetworkSelection(instances, lineID) {
  const line = (instances || []).find(item => String(item.id) === String(lineID || ''))
  const detail = String(line?.facts?.facts?.vowifi_runtime?.detail || '')
  const values = Object.fromEntries(detail.split(';').map(item => item.split('=', 2)).filter(item => item.length === 2))
  return { pdnFamily: values.pdn_family || '', responderID: values.idr || '' }
}

function Field({ label, children }) {
  return <div><label>{label}</label>{children}</div>
}

function provisionFingerprint(request) {
  if (!request) return ''
  const { operation_id: _operationID, preflight_operation_id: _preflightID, ...intent } = request
  return JSON.stringify(intent)
}

export default function SimConfigV1({ instances, selected, targetDevice, setSelected, refresh, devices = [] }) {
  const { t } = useI18n()
  const [catalog, setCatalog] = useState(null)
  const [deletedCatalog, setDeletedCatalog] = useState(null)
  const [candidates, setCandidates] = useState(null)
  const [apply, setApply] = useState(null)
  const [draft, setDraft] = useState(null)
  const [busy, setBusy] = useState('')
	const deletionOperations = useRef(new Map())
  const [retainHistory, setRetainHistory] = useState({})
  const [runtimeBusy, setRuntimeBusy] = useState('')
  const [message, setMessage] = useState('')
  const [reconcileRequest, setReconcileRequest] = useState(null)
  const [provisionProof, setProvisionProof] = useState(null)
	const [pinStatusProof, setPinStatusProof] = useState(null)
  const [pinConfiguration, setPinConfiguration] = useState(null)
  const [pin, setPin] = useState('')
  const load = useCallback(async () => {
    const [nextCatalog, nextDeletedCatalog, nextCandidates, nextApply] = await Promise.all([
      api.catalogLines(), api.catalogLines(true), api.lineCandidates(), api.providerApplyStatus(),
    ])
    setCatalog(nextCatalog); setDeletedCatalog(nextDeletedCatalog); setCandidates(nextCandidates); setApply(nextApply)
    return nextCatalog
  }, [])
  useEffect(() => { load().catch(error => setMessage(error.message)) }, [load])
  const targetLineID = String(targetDevice?.instance_id || selected?.id || '')
  const line = useMemo(() => (catalog?.lines || []).find(item => String(item.id) === targetLineID), [catalog, targetLineID])
  useEffect(() => { setDraft(line ? cloneLine(line) : null) }, [line])
  useEffect(() => { setPinStatusProof(null); setPinConfiguration(null); setPin('') }, [targetLineID])
  const identityReady = !!draft && /^\d{5,18}$/.test(String(draft.sim?.imsi || '')) &&
    /^\d{3}$/.test(String(draft.sim?.mcc || '')) && /^\d{2,3}$/.test(String(draft.sim?.mnc || ''))
	const smscReady = /^\+?\d{3,32}$/.test(String(draft?.sim?.smsc || ''))
  const firstProvision = draft?.hardware_provision_state === 'draft'
	const readerTarget = targetDevice?.device_type === 'reader'
	const readerSIM = targetDevice?.go_device?.reader?.sim || targetDevice?.sim || {}
	const readerIdentityReady = readerTarget && readerSIM.identity_state === 'ready' &&
		/^\d{5,18}$/.test(String(readerSIM.imsi || '')) && /^\d{3}$/.test(String(readerSIM.mcc || '')) &&
		/^\d{2,3}$/.test(String(readerSIM.mnc || ''))
	const savedDraft = !!line && !!draft && JSON.stringify(cloneLine(line)) === JSON.stringify(draft)
  const choose = id => {
    const value = (catalog?.lines || []).find(item => String(item.id) === String(id))
    setSelected?.(value ? String(value.id) : null)
    setDraft(value ? cloneLine(value) : null)
	setProvisionProof(null); setReconcileRequest(null); setPinStatusProof(null); setPinConfiguration(null); setPin('')
  }
  const patchSIM = value => setDraft(current => ({ ...current, sim: { ...current.sim, ...value } }))
  const patchNetwork = value => setDraft(current => ({ ...current, network: { ...current.network, ...value } }))
  const patchAPN = (index, value) => setDraft(current => ({ ...current, network: { ...current.network, apn_profiles: (current.network.apn_profiles || []).map((profile, position) => position === index ? { ...profile, ...value } : profile) } }))
  const addAPN = () => setDraft(current => ({ ...current, network: { ...current.network, apn_profiles: [...(current.network.apn_profiles || []), { id: `custom-${Date.now()}`, name: '', apn: '', auth: 'NONE', username: '', password: '', password_set: false }] } }))
  const removeAPN = index => setDraft(current => ({ ...current, network: { ...current.network, apn_profiles: (current.network.apn_profiles || []).filter((_, position) => position !== index), active_apn: current.network.active_apn === current.network.apn_profiles?.[index]?.id ? '' : current.network.active_apn } }))
  const patchIMS = value => setDraft(current => ({ ...current, ims: { ...current.ims, ...value } }))
	const useCurrentReaderIdentity = () => {
		if (!draft || !readerIdentityReady || !window.confirm(t('Use the current reader identity in this draft? Save is still required.'))) return
		patchSIM({ imsi: readerSIM.imsi, mcc: readerSIM.mcc, mnc: readerSIM.mnc,
			smsc: readerSIM.smsc || draft.sim.smsc })
		setProvisionProof(null)
		setMessage(t('Current reader identity copied into the draft. Review and save it before provisioning.'))
	}
  const save = async () => {
    if (!draft || !catalog) return
    setBusy('save'); setMessage('')
    try {
      const result = await api.saveCatalogLine(draft, catalog.revision)
      setCatalog(current => ({ ...current, revision: result.revision,
        lines: current.lines.map(value => value.id === result.line.id ? result.line : value) }))
      setDraft(cloneLine(result.line))
      setProvisionProof(null); setReconcileRequest(null)
      setMessage(t('Saved to the catalog. Running Providers are unchanged until explicit Apply.'))
      await refresh?.()
    } catch (error) {
      setMessage(error.message)
      if (error.status === 412) await load()
    } finally { setBusy('') }
  }
  const claim = async candidate => {
    if (!candidates || !candidate.can_claim) return
    const name = window.prompt(t('Line name'), candidate.observed?.msisdn || `SIM-${candidate.card_id.slice(-4)}`)
    if (name === null) return
    setBusy(`claim:${candidate.candidate_id}`); setMessage('')
    try {
      const result = await api.claimLineCandidate(candidate.candidate_id, name, candidates.catalog_revision,
        `react-line-claim-${candidate.candidate_id}-${Date.now()}`)
      await load(); setSelected?.(String(result.line.id)); await refresh?.()
      setMessage(t('A disabled line draft was created. Review it before enabling or applying.'))
    } catch (error) {
      setMessage(error.message)
      if (error.status === 412 || error.status === 409) await load()
    } finally { setBusy('') }
  }
  const softDelete = async () => {
    if (!draft || draft.enabled || !catalog || !window.confirm(t('Move this disabled line to the recycle bin? History and card identity are retained.'))) return
    setBusy('delete'); setMessage('')
    try {
      await api.softDeleteCatalogLine(draft.id, catalog.revision)
      setSelected?.(null); await load(); await refresh?.(); setMessage(t('Line moved to the recycle bin. It was not hard-deleted and was not started.'))
    } catch (error) { setMessage(error.message); if (error.status === 412) await load() }
    finally { setBusy('') }
  }
  const restore = async lineID => {
    if (!deletedCatalog || !window.confirm(t('Restore this line as disabled? It will not start automatically.'))) return
    setBusy(`restore:${lineID}`); setMessage('')
    try { await api.restoreCatalogLine(lineID, deletedCatalog.revision); await load(); setMessage(t('Line restored as disabled. Review it before enabling or applying.')) }
    catch (error) { setMessage(error.message); if (error.status === 412) await load() }
    finally { setBusy('') }
  }
  const permanentlyDelete = async lineID => {
		const warning = retainHistory[lineID]
			? 'Permanently delete this recycled line while retaining ended message and call history? The line cannot be restored.'
			: 'Permanently delete this recycled line and its history? This cannot be undone.'
		if (!deletedCatalog || !window.confirm(t(warning))) return
		if (window.prompt(t('Type the exact line ID to confirm permanent deletion.')) !== lineID) {
			setMessage(t('Permanent deletion was not confirmed.'))
			return
		}
		let operationID = deletionOperations.current.get(lineID)
		if (!operationID) {
			operationID = `react-line-delete-${Date.now()}-${Math.random().toString(16).slice(2)}`
			deletionOperations.current.set(lineID, operationID)
		}
		setBusy(`purge:${lineID}`); setMessage('')
		try {
			await api.permanentlyDeleteCatalogLine(lineID, deletedCatalog.revision, operationID, !retainHistory[lineID])
			deletionOperations.current.delete(lineID)
			await load(); await refresh?.(); setMessage(t(retainHistory[lineID] ? 'Line was permanently deleted; ended message and call history was retained.' : 'Line and retained history were permanently deleted.'))
		} catch (error) {
			if (error.code === 'line_deletion_conflict' && error.data?.operation?.operation_id) {
				deletionOperations.current.set(lineID, error.data.operation.operation_id)
				setRetainHistory(current => ({ ...current, [lineID]: error.data.operation.delete_history === false }))
				setMessage(t('An earlier permanent deletion is incomplete. Confirm again to resume the same operation.'))
			} else if (error.code === 'line_deletion_incomplete') {
				setMessage(t('Permanent deletion is incomplete. Retry to resume; the line cannot be restored meanwhile.'))
			} else {
				setMessage(error.message)
			}
			if (error.status === 412) await load()
		} finally { setBusy('') }
	}
  const setRuntime = async action => {
    const lineID = String(draft?.id || targetDevice?.instance_id || '')
    if (!lineID || !draft?.enabled || !window.confirm(t(action === 'start' ? 'Start this line VoWiFi runtime now?' : 'Stop this line VoWiFi runtime now?'))) return
    setRuntimeBusy(action); setMessage('')
    try { await api.setLineRuntime(lineID, action); await refresh?.(); setMessage(t(action === 'start' ? 'Start requested; review the live typed state.' : 'Stop requested; review the live typed state.')) }
    catch (error) { setMessage(error.message) }
    finally { setRuntimeBusy('') }
  }
	const pinTarget = () => {
    const card = String(targetDevice?.sim?.iccid || draft?.card_id || '')
    const equipment = String(targetDevice?.go_device?.equipment_id || targetDevice?.equipment_id || '')
    const reader = String(targetDevice?.reader || targetDevice?.go_device?.reader_name || '')
		return card && (equipment || reader) ? { card_id: card, ...(equipment ? { equipment_id: equipment } : { reader_name: reader }) } : null
	}
	const checkPINStatus = async () => {
		const target = pinTarget()
		if (!target) { setMessage(t('Select one exact current card session.')); return }
		setPinStatusProof(null); setPinConfiguration(null); setBusy('pin-status'); setMessage('')
		const operationID = `react-sim-pin-status-${Date.now()}`
		try {
			const result = await api.simPIN({ operation_id: operationID, ...target, action: 'status' })
			const attempts = Number(result.attempts_remaining)
			setPinStatusProof(['pin_required', 'retry_counter'].includes(result.state) && Number.isInteger(attempts) && attempts > 2
				? { operationID, target: JSON.stringify(target), attempts }
				: null)
			setPinConfiguration(result.configuration || null)
			const saved = result.configuration ? t(result.configuration.configured ? 'Saved on Agent' : 'Not saved on Agent') : t('Agent version does not support PIN storage')
			setMessage(`${t('SIM PIN status')}: ${result.state}${Number.isInteger(attempts) ? ` · ${attempts} ${t('attempts remaining')}` : ''} · ${saved}`)
		} catch (error) { setMessage(error.message) }
		finally { setBusy('') }
	}
	const mutatePIN = async (saveOnAgent = false) => {
		const target = pinTarget()
		if (!target || !pinStatusProof || pinStatusProof.target !== JSON.stringify(target) ||
			pinStatusProof.attempts <= 2 || !/^\d{4,8}$/.test(pin)) {
			setMessage(t('Check the retry counter before entering a 4–8 digit PIN.'))
			return
		}
    setBusy('pin'); setMessage('')
		if (saveOnAgent && !pinConfiguration) {
			setMessage(t('This Agent version does not support local PIN storage.'))
			return
		}
		const operationID = `react-sim-pin-${saveOnAgent ? 'save' : 'verify'}-${Date.now()}`
		const preflightOperationID = pinStatusProof.operationID
		setPinStatusProof(null)
    try {
			const result = await api.simPIN({ operation_id: operationID, ...target, action: saveOnAgent ? 'verify_save' : 'verify', pin,
				preflight_operation_id: preflightOperationID,
				...(saveOnAgent ? { expected_config_revision: pinConfiguration.revision || '' } : {}) })
			setPin(''); setPinConfiguration(result.configuration || pinConfiguration)
			setMessage(t(saveOnAgent ? 'SIM PIN verified and saved on the Agent.' : 'SIM PIN verified once; it was not saved.'))
			await refresh?.(); await load()
    } catch (error) {
			setMessage(error.message); if (error.status === 409 || error.status === 503) await refresh?.()
    }
    finally { setBusy('') }
  }
  const applyNow = async () => {
    if (!catalog || !apply?.pending || !window.confirm(t('Apply this exact catalog revision to VoWiFi Providers? Changed lines may restart.'))) return
    setBusy('apply'); setMessage('')
    try {
      const result = await api.applyProviderConfig(catalog.revision)
      setMessage(`${result.state} · ${result.code || 'applied'} · +${result.added} / ~${result.changed} / -${result.removed}`)
      await load(); await refresh?.()
    } catch (error) { setMessage(error.message); await load() }
    finally { setBusy('') }
  }
  const buildProvisionRequest = operationID => {
    const modem = targetDevice?.go_device?.modem || {}
    const session = String(targetDevice?.go_device?.modem?.sim_session_generation || modem?.sim?.session_generation || targetDevice?.go_device?.reader?.session_generation || '')
    const equipment = String(modem.equipment_id || targetDevice?.equipment_id || '')
    const attachment = String(modem.attachment_id || '')
    if (!draft || !equipment || !attachment || !session || !draft.card_id || !draft.sim.imsi || !draft.sim.mcc || !draft.sim.mnc || !draft.sim.imei || !draft.sim.smsc) {
      return null
    }
    return {
        operation_id: operationID, line_id: draft.id, line_name: draft.name,
        equipment_id: equipment, card_id: draft.card_id, attachment_id: attachment,
        sim_session_generation: session, imsi: draft.sim.imsi, mcc: draft.sim.mcc,
		mnc: draft.sim.mnc, imei: draft.sim.imei, imeisv: draft.sim.imeisv, msisdn: draft.sim.msisdn,
		smsc: draft.sim.smsc, apn: draft.network.active_apn, ims_apn: draft.network.ims_apn,
		idr_mode: draft.network.idr_mode, cp_mode: draft.network.cp_mode,
        egress_country: draft.network.egress_country,
      }
  }
  const readbackProvision = async () => {
    const request = buildProvisionRequest(`react-provision-readback-${draft?.id || 'line'}-${Date.now()}`)
    if (!request) {
      setMessage(t('Provisioning requires a current exact modem attachment and SIM session.'))
      return
    }
    setProvisionProof(null)
    setBusy('provision-readback'); setMessage('')
    try {
      const result = await api.provisionReadbackV1(request)
      const verifiedRequest = { ...request,
        sim_session_generation: result.sim_session_generation || request.sim_session_generation }
      setProvisionProof(result.state === 'succeeded'
        ? { operationID: request.operation_id, sessionGeneration: verifiedRequest.sim_session_generation,
          fingerprint: provisionFingerprint(verifiedRequest) }
        : null)
      setMessage(`${t('Hardware readback')}: ${result.state || 'accepted'}${result.error_code ? ` · ${result.error_code}` : ''}`)
      await load(); await refresh?.()
    } catch (error) { setMessage(error.message); await load() }
    finally { setBusy('') }
  }
	const provisionReader = async () => {
		if (!draft || !catalog || !targetDevice || !readerTarget || !identityReady || !smscReady) {
			setMessage(t('Reader provisioning requires a saved complete line and one exact current reader session.'))
			return
		}
		if (!savedDraft) {
			setMessage(t('Save catalog changes before provisioning this reader line.'))
			return
		}
		if (!window.confirm(t('Verify this exact reader and promote the disabled draft? The line will remain stopped.'))) return
		setBusy('reader-provision'); setMessage('')
		try {
			const result = await api.readerProvisionV1(targetDevice, draft.id, catalog.revision)
			setMessage(`${t('Reader provision operation')}: ${result.state || 'accepted'}${result.error_code ? ` · ${result.error_code}` : ''}`)
			await load(); await refresh?.()
		} catch (error) {
			setMessage(error.message)
			if (error.status === 409 || error.status === 412) await load()
		} finally { setBusy('') }
	}
	const removeSavedPIN = async () => {
		const target = pinTarget()
		if (!target || !pinConfiguration?.configured || !pinConfiguration.revision ||
			!window.confirm(t('Remove the saved PIN from this Agent? The SIM card itself will not be changed.'))) return
		setBusy('pin-remove'); setMessage('')
		try {
			const result = await api.simPIN({ operation_id: `react-sim-pin-remove-${Date.now()}`, ...target,
				action: 'remove_saved', pin: '', expected_config_revision: pinConfiguration.revision })
			setPinConfiguration(result.configuration || { configured: false })
			setMessage(t('Saved SIM PIN removed from the Agent.'))
		} catch (error) { setMessage(error.message); if (error.status === 409) await checkPINStatus() }
		finally { setBusy('') }
	}
  const reprovision = async () => {
    const operationID = `react-${firstProvision ? 'provision' : 'reprovision'}-${draft?.id || 'line'}-${Date.now()}`
    const request = buildProvisionRequest(operationID)
    if (!request) {
      setMessage(t('Provisioning requires a current exact modem attachment and SIM session.'))
      return
    }
    if (provisionProof?.sessionGeneration) request.sim_session_generation = provisionProof.sessionGeneration
    if (!provisionProof || provisionProof.fingerprint !== provisionFingerprint(request)) {
      setMessage(t('Verify the current hardware state before reprovisioning.'))
      return
    }
    request.preflight_operation_id = provisionProof.operationID
    setProvisionProof(null)
    setBusy('provision'); setMessage('')
    try {
      const result = await (firstProvision ? api.provisionV1(request) : api.reprovisionV1(request))
      const state = result.state || 'accepted'
      const detail = result.error_code || result.error_detail || ''
      setReconcileRequest(state === 'unknown' ? request : null)
      setMessage(`${t(firstProvision ? 'Provision operation' : 'Reprovision operation')}: ${state}${detail ? ` · ${detail}` : ''}`)
      await load(); await refresh?.()
    } catch (error) { setMessage(error.message); await load() }
    finally { setBusy('') }
  }
  const reconcile = async () => {
    if (!reconcileRequest) return
    setBusy('reconcile'); setMessage('')
    try {
      const result = await api.reconcileProvisionV1(reconcileRequest)
      const state = result.state || 'accepted'
      setReconcileRequest(state === 'unknown' ? reconcileRequest : null)
      setMessage(`${t('Reconcile operation')}: ${state}`)
      await load(); await refresh?.()
    } catch (error) { setMessage(error.message); await load() }
    finally { setBusy('') }
  }
  const currentProvisionRequest = buildProvisionRequest('current-intent')
  const proofBoundRequest = currentProvisionRequest && provisionProof?.sessionGeneration
    ? { ...currentProvisionRequest, sim_session_generation: provisionProof.sessionGeneration }
    : currentProvisionRequest
	const provisionProofReady = !!provisionProof && provisionProof.fingerprint === provisionFingerprint(proofBoundRequest)
	const actualNetwork = runtimeNetworkSelection(instances, draft?.id)
  if (!catalog || !candidates) return <p>{t('Loading…')}</p>
  return <div className="u-page">
    <div className="card u-panel"><div className="u-card-head"><div><h3>{t('Saved SIM lines')}</h3><p>{t('Line IDs and ICCIDs are immutable operation identities. Reader or modem movement does not change them.')}</p></div><button className="btn btn-ghost" onClick={() => load()}>{t('Refresh')}</button></div>
      <select value={draft?.id || ''} disabled={!!targetDevice?.instance_id} onChange={event => choose(event.target.value)}><option value="">{t('Choose a saved line')}</option>{(catalog.lines || []).map(item => <option value={item.id} key={item.id}>{item.name || item.id} · {item.card_id}</option>)}</select>
    </div>
    <div className="card u-panel"><div className="u-card-head"><div><h3>{t('Detected readers and cards')}</h3><p>{t('Inventory and PIN actions use exact reader/card/session identity. Reader order is never used as card identity.')}</p></div><span className="u-badge cap-on">{(devices || []).filter(item => item.device_type === 'reader').length}</span></div>{(devices || []).filter(item => item.device_type === 'reader').map(item => <div className="u-detail" key={item.id}><span><b>{item.reader || item.name || item.id}</b><small>{item.go_device?.agent_id || item.agent_id || t('Agent unavailable')}</small></span><b>{item.sim?.present ? (item.sim.iccid || item.sim.pin_state || t('Card identity unavailable')) : t('No card')} · {item.sim?.pin_state || t('PIN state unavailable')}</b></div>)}{!(devices || []).some(item => item.device_type === 'reader') && <p className="u-muted">{t('No typed reader inventory is currently reported.')}</p>}<p className="u-note">{t('Select a current reader/card device to perform PIN actions. Modem PIN changes remain unavailable until its adapter exposes the same typed primitive.')}</p></div>
    {!!(deletedCatalog?.lines || []).filter(item => item.deleted).length && <div className="card u-panel"><div className="u-card-head"><div><h3>{t('Recycle bin')}</h3><p>{t('Soft-deleted lines retain history and card identity; restore is always disabled.')}</p></div></div>{deletedCatalog.lines.filter(item => item.deleted).map(item => <div className="u-detail" key={item.id}><span><b>{item.name || item.id}</b><small>{item.id} · {item.card_id}</small></span><span className="u-inline"><label className="u-title-toggle"><input type="checkbox" checked={retainHistory[item.id] === true} disabled={!!busy || deletionOperations.current.has(item.id)} onChange={event => setRetainHistory(current => ({ ...current, [item.id]: event.target.checked }))}/><span>{t('Retain ended message and call history')}</span></label><button className="btn btn-ghost" disabled={!!busy || deletionOperations.current.has(item.id)} onClick={() => restore(item.id)}>{t(busy === `restore:${item.id}` ? 'Restoring…' : 'Restore')}</button><button className="btn btn-danger-outline" disabled={!!busy} onClick={() => permanentlyDelete(item.id)}>{t(busy === `purge:${item.id}` ? 'Deleting…' : deletionOperations.current.has(item.id) ? 'Resume permanent deletion' : 'Delete permanently')}</button></span></div>)}</div>}
    {(candidates.candidates || []).some(item => !item.configured_line_id) && <div className="card u-panel"><h3>{t('Detected unconfigured SIMs')}</h3><p className="u-note">{t('Claiming creates only a disabled draft. Hardware provisioning, PIN verification and runtime start are separate guarded steps.')}</p>{candidates.candidates.filter(item => !item.configured_line_id).map(item => <div className="u-detail" key={item.candidate_id}><span><b>{item.kind} · {item.mode}</b><small>ICCID {item.card_id} · {item.observed?.msisdn || t('No number')} · {item.condition} · {item.provision_state}{item.provision_blockers?.length ? ` · ${item.provision_blockers.join(', ')}` : ''}</small></span><button className="btn btn-primary" disabled={!item.can_claim || !!busy} onClick={() => claim(item)}>{t('Create disabled draft')}</button></div>)}</div>}
    {draft ? <div className="card u-panel"><div className="u-card-head"><div><h3>{t('Line configuration')}</h3><p>{t('This edits durable desired configuration only; it never chooses an Agent attachment.')}</p></div><label className="u-title-toggle"><span>{t('Enabled in Provider catalog')}</span><input type="checkbox" className="u-toggle" checked={draft.enabled} disabled={firstProvision || (!identityReady && !draft.enabled)} onChange={event => setDraft(current => ({ ...current, enabled: !firstProvision && identityReady && event.target.checked }))}/></label></div>
      {!identityReady && <p className="u-note">{t('SIM identity is incomplete. Keep this line disabled until fresh IMSI, MCC and MNC facts are available; enabling it would be rejected by the Go catalog.')}</p>}
      {identityReady && <p className="u-note">{t('Identity is complete for catalog editing only. Provisioning and SIM PIN readiness still require their own exact Agent/session evidence.')}</p>}
      <div className="u-form-grid"><Field label={t('Instance ID')}><input className="mono" value={draft.id} readOnly/></Field><Field label="ICCID"><input className="mono" value={draft.card_id} readOnly/></Field><Field label={t('Name')}><input value={draft.name} onChange={event => setDraft(current => ({ ...current, name: event.target.value }))}/></Field><Field label={t('Phone number (MSISDN)')}><input value={draft.sim.msisdn} onChange={event => patchSIM({ msisdn: event.target.value })}/></Field><Field label="IMSI"><input className="mono" value={draft.sim.imsi} onChange={event => patchSIM({ imsi: event.target.value.replace(/\D/g, '') })}/></Field><Field label="MCC"><input value={draft.sim.mcc} maxLength="3" onChange={event => patchSIM({ mcc: event.target.value.replace(/\D/g, '') })}/></Field><Field label="MNC"><input value={draft.sim.mnc} maxLength="3" onChange={event => patchSIM({ mnc: event.target.value.replace(/\D/g, '') })}/></Field><Field label="IMEI"><input value={draft.sim.imei} maxLength="15" onChange={event => patchSIM({ imei: event.target.value.replace(/\D/g, '') })}/></Field><Field label="IMEISV"><input value={draft.sim.imeisv} maxLength="16" onChange={event => patchSIM({ imeisv: event.target.value.replace(/\D/g, '') })}/></Field><Field label="SMSC"><input value={draft.sim.smsc} onChange={event => patchSIM({ smsc: event.target.value })}/></Field><Field label={t('Country exit')}><input value={draft.network.egress_country} maxLength="2" onChange={event => patchNetwork({ egress_country: event.target.value.replace(/[^a-z]/gi, '').toLowerCase() })}/></Field><Field label="ePDG"><input value={draft.network.epdg_address} onChange={event => patchNetwork({ epdg_address: event.target.value })}/></Field><Field label={t('IMS APN')}><input value={draft.network.ims_apn} onChange={event => patchNetwork({ ims_apn: event.target.value.toLowerCase() })}/></Field><Field label={t('ePDG identity (IDr)')}><select value={draft.network.idr_mode} onChange={event => patchNetwork({ idr_mode: event.target.value })}><option value="apn">{t('Bare APN (default)')}</option><option value="fqdn">APN-FQDN</option></select></Field><Field label={t('IMS address family (CP)')}><select value={draft.network.cp_mode} onChange={event => patchNetwork({ cp_mode: event.target.value })}><option value="auto">{t('Automatic')}</option><option value="v6">IPv6</option><option value="dual">IPv4 + IPv6</option><option value="v4">IPv4</option></select></Field></div>
      <p className="u-note">{t('Desired IMS network')}: {draft.network.ims_apn || 'ims'} · {draft.network.idr_mode || 'apn'} · {draft.network.cp_mode || 'auto'} | {t('Actual IMS network')}: {actualNetwork.responderID || '—'} · {actualNetwork.pdnFamily || '—'}</p>
      <label>P-CSCF ({t('one per line')})</label><textarea rows="3" value={(draft.network.pcscf || []).join('\n')} onChange={event => patchNetwork({ pcscf: event.target.value.split(/\r?\n/).map(value => value.trim()).filter(Boolean) })}/>
      <div className="u-card-head" style={{ marginTop: 16, padding: 0 }}><div><h3>{t('MDD APN profiles')}</h3><p>{t('MDD is the durable source. SIM/modem observations are suggestions only; select one profile explicitly before apply.')}</p></div><button className="btn btn-ghost" onClick={addAPN}>{t('Add custom APN')}</button></div>
      {(draft.network.apn_profiles || []).map((profile, index) => <div className="u-detail" key={profile.id || index}><span><input className="mono" value={profile.id || ''} onChange={event => patchAPN(index, { id: event.target.value })} placeholder={t('Stable profile ID')}/><small><input value={profile.name || ''} onChange={event => patchAPN(index, { name: event.target.value })} placeholder={t('Display name')}/></small></span><span className="u-inline"><input value={profile.apn || ''} onChange={event => patchAPN(index, { apn: event.target.value })} placeholder="APN"/><select value={profile.auth || 'NONE'} onChange={event => patchAPN(index, { auth: event.target.value })}><option>NONE</option><option>PAP</option><option>CHAP</option><option>MSCHAPV2</option></select><input type="text" value={profile.username || ''} onChange={event => patchAPN(index, { username: event.target.value })} placeholder={t('Username')}/><input type="password" value={profile.password || ''} onChange={event => patchAPN(index, { password: event.target.value, password_set: true })} placeholder={t('Password (optional)')}/><label className="u-title-toggle"><span>{t('Active')}</span><input type="radio" name="mdd-active-apn" checked={draft.network.active_apn === profile.id} onChange={() => patchNetwork({ active_apn: profile.id })}/></label><button className="btn btn-danger-outline" onClick={() => removeAPN(index)}>{t('Remove')}</button></span></div>)}
      <details><summary>{t('Advanced IMS identity')}</summary><div className="u-form-grid"><Field label="IMPI"><input value={draft.ims.impi || ''} onChange={event => patchIMS({ impi: event.target.value })}/></Field><Field label="IMPU"><input value={draft.ims.impu || ''} onChange={event => patchIMS({ impu: event.target.value })}/></Field><Field label={t('Domain')}><input value={draft.ims.domain || ''} onChange={event => patchIMS({ domain: event.target.value })}/></Field><Field label="User-Agent"><input value={draft.ims.user_agent || ''} onChange={event => patchIMS({ user_agent: event.target.value })}/></Field><Field label={t('Access network info')}><input value={draft.ims.access_network_info || ''} onChange={event => patchIMS({ access_network_info: event.target.value })}/></Field><Field label={t('Visited network ID')}><input value={draft.ims.visited_network_id || ''} onChange={event => patchIMS({ visited_network_id: event.target.value })}/></Field><Field label={t('AKA application')}><select value={draft.ims.aka_app_preference || ''} onChange={event => patchIMS({ aka_app_preference: event.target.value })}><option value="">{t('Automatic')}</option><option value="usim">USIM</option><option value="isim">ISIM</option></select></Field><Field label={t('Transport')}><select value={draft.ims.network || ''} onChange={event => patchIMS({ network: event.target.value })}><option value="">{t('Automatic')}</option><option value="udp">UDP</option><option value="tcp">TCP</option></select></Field></div></details>
		{pinTarget() && <div className="u-pin-actions"><button className="btn btn-ghost" disabled={!!busy} onClick={checkPINStatus}>{t(busy === 'pin-status' ? 'Checking…' : 'Check PIN status')}</button><input type="password" inputMode="numeric" maxLength="8" autoComplete="off" value={pin} disabled={!pinStatusProof} placeholder={t('Current SIM PIN')} onChange={event => setPin(event.target.value.replace(/\D/g, ''))}/><button className="btn btn-ghost" disabled={!!busy || !pinStatusProof || !/^\d{4,8}$/.test(pin)} onClick={() => mutatePIN(false)}>{t(busy === 'pin' ? 'Working…' : 'Verify once')}</button><button className="btn btn-primary" disabled={!!busy || !pinConfiguration || !pinStatusProof || !/^\d{4,8}$/.test(pin)} onClick={() => mutatePIN(true)}>{t(busy === 'pin' ? 'Working…' : 'Verify and save on Agent')}</button><button className="btn btn-danger-outline" disabled={!!busy || !pinConfiguration?.configured} onClick={removeSavedPIN}>{t(busy === 'pin-remove' ? 'Removing…' : 'Remove saved PIN')}</button></div>}
		<div className="u-inline">{readerTarget && <button className="btn btn-ghost" disabled={!!busy || !readerIdentityReady} onClick={useCurrentReaderIdentity}>{t('Use current reader identity')}</button>}<button className="btn btn-primary" disabled={!!busy} onClick={save}>{t(busy === 'save' ? 'Saving…' : 'Save catalog')}</button>{firstProvision && readerTarget ? <button className="btn btn-ghost" disabled={!!busy || !!runtimeBusy || !identityReady || !smscReady || !savedDraft} onClick={provisionReader}>{t(busy === 'reader-provision' ? 'Provisioning…' : 'Verify reader and provision')}</button> : <><button className="btn btn-ghost" disabled={!!busy || !!runtimeBusy} onClick={readbackProvision}>{t(busy === 'provision-readback' ? 'Reading…' : 'Verify hardware state')}</button><button className="btn btn-ghost" disabled={!!busy || !!runtimeBusy || !provisionProofReady} onClick={reprovision}>{t(busy === 'provision' ? (firstProvision ? 'Provisioning…' : 'Reprovisioning…') : (firstProvision ? 'Provision hardware' : 'Reprovision hardware'))}</button></>}{reconcileRequest && <button className="btn btn-ghost" disabled={!!busy || !!runtimeBusy} onClick={reconcile}>{t(busy === 'reconcile' ? 'Reconciling…' : 'Read back and reconcile')}</button>}{draft.enabled && <><button className="btn btn-ghost" disabled={!!busy || !!runtimeBusy} onClick={() => setRuntime('start')}>{t(runtimeBusy === 'start' ? 'Starting…' : 'Start runtime')}</button><button className="btn btn-ghost" disabled={!!busy || !!runtimeBusy} onClick={() => setRuntime('stop')}>{t(runtimeBusy === 'stop' ? 'Stopping…' : 'Stop runtime')}</button></>}{!draft.enabled && <button className="btn btn-ghost" disabled={!!busy || !!runtimeBusy} onClick={softDelete}>{t(busy === 'delete' ? 'Moving…' : 'Move to recycle bin')}</button>}</div>
    </div> : <div className="u-empty"><h3>{t('No line selected')}</h3><p>{t('Select a saved line or claim one current unconfigured SIM as a disabled draft.')}</p></div>}
    <div className="card u-panel"><div className="u-card-head"><div><h3>{t('Provider apply')}</h3><p>{t('Saving catalog data never restarts a Provider. Apply is a separate explicit action.')}</p></div><span className={`u-badge ${apply?.pending ? 'cap-degraded' : 'cap-on'}`}>{apply?.pending ? t('Pending') : t('Applied')}</span></div><div className="u-detail"><span>{t('Catalog revision')}</span><b>{apply?.catalog_revision}</b></div><div className="u-detail"><span>{t('Applied revision')}</span><b>{apply?.applied_revision}</b></div><button className="btn btn-primary" disabled={!apply?.pending || !!busy} onClick={applyNow}>{t(busy === 'apply' ? 'Applying…' : 'Review and apply current revision')}</button></div>
    {message && <p className="u-note">{message}</p>}
  </div>
}
