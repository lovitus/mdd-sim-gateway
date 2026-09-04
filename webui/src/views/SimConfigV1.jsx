import React, { useCallback, useEffect, useMemo, useState } from 'react'
import { api } from '../api.js'
import { useI18n } from '../i18n.jsx'

function cloneLine(line) {
  return {
    schema_version: 1,
    id: line.id,
    name: line.name || '',
    enabled: line.enabled === true,
    card_id: line.card_id || '',
    sim: { imsi: '', mcc: '', mnc: '', imei: '', msisdn: '', smsc: '', ...(line.sim || {}) },
    network: { epdg_address: '', pcscf: [], egress_country: '', ...(line.network || {}) },
    ims: { ...(line.ims || {}) },
  }
}

function Field({ label, children }) {
  return <div><label>{label}</label>{children}</div>
}

export default function SimConfigV1({ instances, selected, targetDevice, setSelected, refresh }) {
  const { t } = useI18n()
  const [catalog, setCatalog] = useState(null)
  const [deletedCatalog, setDeletedCatalog] = useState(null)
  const [candidates, setCandidates] = useState(null)
  const [apply, setApply] = useState(null)
  const [draft, setDraft] = useState(null)
  const [busy, setBusy] = useState('')
  const [runtimeBusy, setRuntimeBusy] = useState('')
  const [message, setMessage] = useState('')
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
  const choose = id => {
    const value = (catalog?.lines || []).find(item => String(item.id) === String(id))
    setSelected?.(value ? String(value.id) : null)
    setDraft(value ? cloneLine(value) : null)
  }
  const patchSIM = value => setDraft(current => ({ ...current, sim: { ...current.sim, ...value } }))
  const patchNetwork = value => setDraft(current => ({ ...current, network: { ...current.network, ...value } }))
  const patchIMS = value => setDraft(current => ({ ...current, ims: { ...current.ims, ...value } }))
  const save = async () => {
    if (!draft || !catalog) return
    setBusy('save'); setMessage('')
    try {
      const result = await api.saveCatalogLine(draft, catalog.revision)
      setCatalog(current => ({ ...current, revision: result.revision,
        lines: current.lines.map(value => value.id === result.line.id ? result.line : value) }))
      setDraft(cloneLine(result.line))
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
      const result = await api.claimLineCandidate(candidate.candidate_id, name, candidates.catalog_revision)
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
  const setRuntime = async action => {
    const lineID = String(draft?.id || targetDevice?.instance_id || '')
    if (!lineID || !draft?.enabled || !window.confirm(t(action === 'start' ? 'Start this line VoWiFi runtime now?' : 'Stop this line VoWiFi runtime now?'))) return
    setRuntimeBusy(action); setMessage('')
    try { await api.setLineRuntime(lineID, action); await refresh?.(); setMessage(t(action === 'start' ? 'Start requested; review the live typed state.' : 'Stop requested; review the live typed state.')) }
    catch (error) { setMessage(error.message) }
    finally { setRuntimeBusy('') }
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
  if (!catalog || !candidates) return <p>{t('Loading…')}</p>
  return <div className="u-page">
    <div className="card u-panel"><div className="u-card-head"><div><h3>{t('Saved SIM lines')}</h3><p>{t('Line IDs and ICCIDs are immutable operation identities. Reader or modem movement does not change them.')}</p></div><button className="btn btn-ghost" onClick={() => load()}>{t('Refresh')}</button></div>
      <select value={draft?.id || ''} disabled={!!targetDevice?.instance_id} onChange={event => choose(event.target.value)}><option value="">{t('Choose a saved line')}</option>{(catalog.lines || []).map(item => <option value={item.id} key={item.id}>{item.name || item.id} · {item.card_id}</option>)}</select>
    </div>
    <div className="card u-panel"><div className="u-card-head"><div><h3>{t('Detected readers and cards')}</h3><p>{t('Read-only inventory from the typed Go device projection. Reader order is never used as card identity.')}</p></div><span className="u-badge cap-on">{(devices || []).filter(item => item.device_type === 'reader').length}</span></div>{(devices || []).filter(item => item.device_type === 'reader').map(item => <div className="u-detail" key={item.id}><span><b>{item.reader || item.name || item.id}</b><small>{item.go_device?.agent_id || item.agent_id || t('Agent unavailable')}</small></span><b>{item.sim?.present ? (item.sim.iccid || item.sim.pin_state || t('Card identity unavailable')) : t('No card')} · {item.sim?.pin_state || t('PIN state unavailable')}</b></div>)}{!(devices || []).some(item => item.device_type === 'reader') && <p className="u-muted">{t('No typed reader inventory is currently reported.')}</p>}<p className="u-note">{t('SIM PIN verification, change and enable/disable actions require a dedicated exact reader/session contract and remain pending.')}</p></div>
    {!!(deletedCatalog?.lines || []).filter(item => item.deleted).length && <div className="card u-panel"><div className="u-card-head"><div><h3>{t('Recycle bin')}</h3><p>{t('Soft-deleted lines retain history and card identity; restore is always disabled.')}</p></div></div>{deletedCatalog.lines.filter(item => item.deleted).map(item => <div className="u-detail" key={item.id}><span><b>{item.name || item.id}</b><small>{item.id} · {item.card_id}</small></span><button className="btn btn-ghost" disabled={!!busy} onClick={() => restore(item.id)}>{t(busy === `restore:${item.id}` ? 'Restoring…' : 'Restore')}</button></div>)}</div>}
    {(candidates.candidates || []).some(item => !item.configured_line_id) && <div className="card u-panel"><h3>{t('Detected unconfigured SIMs')}</h3>{candidates.candidates.filter(item => !item.configured_line_id).map(item => <div className="u-detail" key={item.candidate_id}><span><b>{item.kind} · {item.mode}</b><small>ICCID {item.card_id} · {item.observed?.msisdn || t('No number')} · {item.condition}</small></span><button className="btn btn-primary" disabled={!item.can_claim || !!busy} onClick={() => claim(item)}>{t('Create disabled draft')}</button></div>)}</div>}
    {draft ? <div className="card u-panel"><div className="u-card-head"><div><h3>{t('Line configuration')}</h3><p>{t('This edits durable desired configuration only; it never chooses an Agent attachment.')}</p></div><label className="u-title-toggle"><span>{t('Enabled in Provider catalog')}</span><input type="checkbox" className="u-toggle" checked={draft.enabled} onChange={event => setDraft(current => ({ ...current, enabled: event.target.checked }))}/></label></div>
      <div className="u-form-grid"><Field label={t('Instance ID')}><input className="mono" value={draft.id} readOnly/></Field><Field label="ICCID"><input className="mono" value={draft.card_id} readOnly/></Field><Field label={t('Name')}><input value={draft.name} onChange={event => setDraft(current => ({ ...current, name: event.target.value }))}/></Field><Field label={t('Phone number (MSISDN)')}><input value={draft.sim.msisdn} onChange={event => patchSIM({ msisdn: event.target.value })}/></Field><Field label="IMSI"><input className="mono" value={draft.sim.imsi} onChange={event => patchSIM({ imsi: event.target.value.replace(/\D/g, '') })}/></Field><Field label="MCC"><input value={draft.sim.mcc} maxLength="3" onChange={event => patchSIM({ mcc: event.target.value.replace(/\D/g, '') })}/></Field><Field label="MNC"><input value={draft.sim.mnc} maxLength="3" onChange={event => patchSIM({ mnc: event.target.value.replace(/\D/g, '') })}/></Field><Field label="SMSC"><input value={draft.sim.smsc} onChange={event => patchSIM({ smsc: event.target.value })}/></Field><Field label={t('Country exit')}><input value={draft.network.egress_country} maxLength="2" onChange={event => patchNetwork({ egress_country: event.target.value.replace(/[^a-z]/gi, '').toLowerCase() })}/></Field><Field label="ePDG"><input value={draft.network.epdg_address} onChange={event => patchNetwork({ epdg_address: event.target.value })}/></Field></div>
      <label>P-CSCF ({t('one per line')})</label><textarea rows="3" value={(draft.network.pcscf || []).join('\n')} onChange={event => patchNetwork({ pcscf: event.target.value.split(/\r?\n/).map(value => value.trim()).filter(Boolean) })}/>
      <details><summary>{t('Advanced IMS identity')}</summary><div className="u-form-grid"><Field label="IMPI"><input value={draft.ims.impi || ''} onChange={event => patchIMS({ impi: event.target.value })}/></Field><Field label="IMPU"><input value={draft.ims.impu || ''} onChange={event => patchIMS({ impu: event.target.value })}/></Field><Field label={t('Domain')}><input value={draft.ims.domain || ''} onChange={event => patchIMS({ domain: event.target.value })}/></Field><Field label="User-Agent"><input value={draft.ims.user_agent || ''} onChange={event => patchIMS({ user_agent: event.target.value })}/></Field><Field label={t('Access network info')}><input value={draft.ims.access_network_info || ''} onChange={event => patchIMS({ access_network_info: event.target.value })}/></Field><Field label={t('Visited network ID')}><input value={draft.ims.visited_network_id || ''} onChange={event => patchIMS({ visited_network_id: event.target.value })}/></Field><Field label={t('AKA application')}><select value={draft.ims.aka_app_preference || ''} onChange={event => patchIMS({ aka_app_preference: event.target.value })}><option value="">{t('Automatic')}</option><option value="usim">USIM</option><option value="isim">ISIM</option></select></Field><Field label={t('Transport')}><select value={draft.ims.network || ''} onChange={event => patchIMS({ network: event.target.value })}><option value="">{t('Automatic')}</option><option value="udp">UDP</option><option value="tcp">TCP</option></select></Field></div></details>
      {targetDevice?.sim?.pin_state && <p className="u-note">SIM PIN: {targetDevice.sim.pin_state} · {targetDevice.sim.pin_configured ? t('Configured locally on the Agent') : t('Not configured on the Agent')}{targetDevice.sim.pin_attempts_remaining != null ? ` · ${targetDevice.sim.pin_attempts_remaining} ${t('attempts remaining')}` : ''}</p>}
      <div className="u-inline"><button className="btn btn-primary" disabled={!!busy} onClick={save}>{t(busy === 'save' ? 'Saving…' : 'Save catalog')}</button>{draft.enabled && <><button className="btn btn-ghost" disabled={!!busy || !!runtimeBusy} onClick={() => setRuntime('start')}>{t(runtimeBusy === 'start' ? 'Starting…' : 'Start runtime')}</button><button className="btn btn-ghost" disabled={!!busy || !!runtimeBusy} onClick={() => setRuntime('stop')}>{t(runtimeBusy === 'stop' ? 'Stopping…' : 'Stop runtime')}</button></>}{!draft.enabled && <button className="btn btn-ghost" disabled={!!busy || !!runtimeBusy} onClick={softDelete}>{t(busy === 'delete' ? 'Moving…' : 'Move to recycle bin')}</button>}</div>
    </div> : <div className="u-empty"><h3>{t('No line selected')}</h3><p>{t('Select a saved line or claim one current unconfigured SIM as a disabled draft.')}</p></div>}
    <div className="card u-panel"><div className="u-card-head"><div><h3>{t('Provider apply')}</h3><p>{t('Saving catalog data never restarts a Provider. Apply is a separate explicit action.')}</p></div><span className={`u-badge ${apply?.pending ? 'cap-degraded' : 'cap-on'}`}>{apply?.pending ? t('Pending') : t('Applied')}</span></div><div className="u-detail"><span>{t('Catalog revision')}</span><b>{apply?.catalog_revision}</b></div><div className="u-detail"><span>{t('Applied revision')}</span><b>{apply?.applied_revision}</b></div><button className="btn btn-primary" disabled={!apply?.pending || !!busy} onClick={applyNow}>{t(busy === 'apply' ? 'Applying…' : 'Review and apply current revision')}</button></div>
    {message && <p className="u-note">{message}</p>}
  </div>
}
