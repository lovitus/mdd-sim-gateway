import React, { useCallback, useEffect, useState } from 'react'
import { api } from '../api.js'
import { useI18n } from '../i18n.jsx'

export default function HardwareV1({ device, showToast, refreshDevices }) {
  const { t } = useI18n()
  const [raw, setRaw] = useState(null)
  const [candidate, setCandidate] = useState('')
  const [importer, setImporter] = useState('')
  const [busy, setBusy] = useState(false)
  const lineID = String(device?.instance_id || '')
  const load = useCallback(() => {
    if (!lineID || device.device_type !== 'modem') { setRaw(null); return Promise.resolve() }
    return api.rawModemBinding(lineID).then(value => {
      setRaw(value)
      if (!candidate && value.candidates?.[0]) setCandidate(value.candidates[0].candidate_id)
      if (!importer && value.importers?.[0]) setImporter(value.importers[0])
    }).catch(error => showToast(error.message))
  }, [lineID, device.device_type, showToast, candidate, importer])
  useEffect(() => { void load() }, [load])
  const enable = async () => {
    const selected = raw?.candidates?.find(value => value.candidate_id === candidate)
    if (!selected || !importer || !window.confirm(t('Persist whole-modem passthrough for this exact ICCID and equipment? The source OS will remain fenced until you explicitly disable it.'))) return
    setBusy(true)
    try {
      await api.saveRawModemBinding(lineID, {
        expected_revision: raw.revision, expected_equipment_id: selected.equipment_id,
        expected_card_id: raw.card_id, enabled: true,
        source_candidate_id: selected.candidate_id, source_agent_id: selected.source_agent_id,
        importer_agent_id: importer,
      })
      await load(); await refreshDevices?.()
    } catch (error) { showToast(error.message); await load() } finally { setBusy(false) }
  }
  const disable = async () => {
    if (!raw?.binding || !window.confirm(t('Disable persistent passthrough and return this modem/SIM pair to adapted mode?'))) return
    setBusy(true)
    try {
      await api.saveRawModemBinding(lineID, {
        expected_revision: raw.revision, expected_equipment_id: raw.binding.equipment_id,
        expected_card_id: raw.binding.card_id, enabled: false,
      })
      await load(); await refreshDevices?.()
    } catch (error) { showToast(error.message); await load() } finally { setBusy(false) }
  }
  const modem = device.go_device?.modem || device.go_device?.raw?.imported_modem || {}
  return <div className="card u-panel"><h3>{t('Hardware')}</h3>
    <div className="u-details cols"><div className="u-detail"><span>{t('Mode')}</span><b>{device.mode}</b></div><div className="u-detail"><span>{t('Agent')}</span><b>{device.go_device?.agent_id || '—'}</b></div><div className="u-detail"><span>{t('Attachment')}</span><b>{modem.attachment_id || device.reader || '—'}</b></div><div className="u-detail"><span>IMEI / Equipment ID</span><b>{modem.equipment_id || '—'}</b></div><div className="u-detail"><span>{t('Manufacturer')}</span><b>{modem.manufacturer || '—'}</b></div><div className="u-detail"><span>{t('Model')}</span><b>{modem.model || '—'}</b></div><div className="u-detail"><span>{t('Firmware')}</span><b>{modem.firmware || '—'}</b></div><div className="u-detail"><span>{t('Condition')}</span><b>{device.condition} · {device.condition_code || '—'}</b></div></div>
    <h3>{t('Card endpoints')}</h3>{(device.endpoints || []).map(endpoint => <div className="u-detail" key={endpoint.id}><span>{endpoint.slot_label || endpoint.kind} · {(endpoint.card_ids || []).join(', ') || endpoint.eid || t('No identity')}</span><b>{endpoint.association} · {endpoint.operation_candidate ? t('Operation candidate') : t('Read-only')}</b></div>)}
    {device.device_type === 'reader' && <p className="u-note">{t('Reader presentation IMEI is managed by ICCID in the IMEI Pool tab; reader enumeration order is never persisted as card identity.')}</p>}
    {device.device_type === 'modem' && lineID && <div className="u-note"><div className="u-card-head"><div><h3>{t('Persistent whole-modem passthrough')}</h3><p>{t('Disconnects, crashes, Agent exit, and reboots retain the source fence. Only this explicit control releases it.')}</p></div><span className={`u-badge ${raw?.binding?.enabled ? 'cap-on' : 'cap-off'}`}>{raw?.binding?.enabled ? t('Enabled') : t('Adapted mode')}</span></div>
      {raw?.binding?.enabled ? <><div className="u-detail"><span>{t('Source Agent')}</span><b>{raw.binding.source_agent_id}</b></div><div className="u-detail"><span>{t('Importer Agent')}</span><b>{raw.binding.importer_agent_id}</b></div><button className="btn btn-danger" disabled={busy} onClick={disable}>{t('Disable passthrough')}</button></> : <><div className="u-form-grid"><div><label>{t('Exact source modem')}</label><select value={candidate} onChange={event => setCandidate(event.target.value)}><option value="">{t('No isolated source candidate')}</option>{(raw?.candidates || []).map(value => <option value={value.candidate_id} key={value.candidate_id}>{value.model || value.manufacturer || value.equipment_id} · {value.source_agent_id}</option>)}</select></div><div><label>{t('Importer Agent')}</label><select value={importer} onChange={event => setImporter(event.target.value)}><option value="">{t('No ready importer')}</option>{(raw?.importers || []).map(value => <option value={value} key={value}>{value}</option>)}</select></div></div><button className="btn btn-primary" disabled={busy || !candidate || !importer} onClick={enable}>{t('Enable persistent passthrough')}</button>{(!raw?.candidates?.length || !raw?.importers?.length) && <p className="u-error">{t('Raw passthrough remains fail-closed until one exact isolated source and one distinct ready importer exist.')}</p>}</>}
    </div>}
  </div>
}
