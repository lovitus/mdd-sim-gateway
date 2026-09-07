import { api as go } from '../api.js'

export function simPINIdentity(device, expectedCardID = '') {
  if (device?.stale || device?.observed_only) throw new Error('device_snapshot_unavailable')
  const raw = device?.go_device || {}
  const cardID = device?.sim?.iccid || raw.reader?.card_id || raw.modem?.sim?.iccid || ''
  if (!cardID || (expectedCardID && expectedCardID !== cardID)) throw new Error('sim_pin_card_identity_changed')
  const equipment = raw.modem?.equipment_id || raw.equipment_id || device?.equipment_id
  const reader = raw.reader?.reader_name || device?.reader
  if (!equipment && !reader) throw new Error('sim_pin_endpoint_required')
  return {card_id:cardID,...(equipment ? {equipment_id:equipment} : {reader_name:reader})}
}

export function runtimeNetworkSelection(instances, lineID) {
  const line = instances.find(item => String(item.id) === String(lineID || ''))
  const detail = String(line?.facts?.facts?.vowifi_runtime?.detail || '')
  const values = Object.fromEntries(detail.split(';').map(item => item.split('=',2).map(value => value.trim())).filter(item => item.length === 2))
  return {pdnFamily:values.pdn_family || '',responderID:values.idr || ''}
}

export function pinProof(result, operation, target) {
  const attempts = result.attempts_remaining
  return ['pin_required','retry_counter'].includes(result.state) && Number.isInteger(attempts) && attempts > 2
    ? {operation,target:JSON.stringify(target)} : null
}

export function candidateForDevice(candidates, device) {
  if (device?.stale || device?.observed_only) return null
  const raw = device?.go_device || {}
  const card = device?.sim?.iccid
  const agent = raw.agent_id || device?.agent_id
  const generation = raw.process_generation || raw.reader?.process_generation
  if (!card || !agent || !generation) return null
  const matches = candidates.filter(candidate => candidate.card_id === card && candidate.agent_id === agent &&
    candidate.process_generation === generation && (device.device_type === 'reader'
      ? candidate.reader_name === (raw.reader?.reader_name || device.reader) && candidate.session_generation === raw.reader?.session_generation
      : candidate.equipment_id === (raw.modem?.equipment_id || device.equipment_id) && candidate.attachment_id === raw.modem?.attachment_id && candidate.session_generation === (raw.modem?.sim_session_generation || raw.modem?.sim?.session_generation)))
  return matches.length === 1 ? matches[0] : null
}

export function savedCatalogLine(form) {
  const edited = editedCatalogLine(form)
  const baseline = editedCatalogLine(lineForm(form.__catalog_line,form.__catalog_revision))
  if (JSON.stringify(edited) !== JSON.stringify(baseline)) throw new Error('save_catalog_before_provisioning')
  return form.__catalog_line
}

export function modemProvisionIntent(form, device, operation) {
  if (device?.stale || device?.observed_only) throw new Error('device_snapshot_unavailable')
  const line = savedCatalogLine(form)
  const modem = device?.go_device?.modem || {}
  const session = modem.sim_session_generation || modem.sim?.session_generation
  if (device?.sim?.iccid !== line.card_id || !modem.equipment_id || !modem.attachment_id || !session) throw new Error('provision_modem_identity_unavailable')
  return {operation_id:operation,line_id:line.id,line_name:line.name || '',equipment_id:modem.equipment_id,
    card_id:line.card_id,attachment_id:modem.attachment_id,sim_session_generation:session,
    imsi:line.sim.imsi,mcc:line.sim.mcc,mnc:line.sim.mnc,imei:line.sim.imei,imeisv:line.sim.imeisv,
    msisdn:line.sim.msisdn,smsc:line.sim.smsc,apn:line.network?.active_apn,ims_apn:line.network?.ims_apn,
    idr_mode:line.network?.idr_mode,cp_mode:line.network?.cp_mode,egress_country:line.network?.egress_country}
}

export function lineForm(line, revision) {
  return {id:line.id,name:line.name || '',enabled:line.enabled,iccid:line.card_id,
    imsi:line.sim?.imsi || '',mcc:line.sim?.mcc || '',mnc:line.sim?.mnc || '',
    imei:line.sim?.imei || '',imeisv:line.sim?.imeisv || '',msisdn:line.sim?.msisdn || '',smsc:line.sim?.smsc || '',
    proxy_country:line.network?.egress_country || '',apn:line.network?.ims_apn || 'ims',
    idr_mode:line.network?.idr_mode || 'apn',cp_mode:line.network?.cp_mode || 'auto',
    sip:{pani:line.ims?.access_network_info || '',access_type:line.ims?.access_type || '',user_eq_phone:line.ims?.user_equals_phone === true},
    provisioning_state:line.hardware_provision_state === 'draft' ? 'draft' : 'ready',
    __catalog_revision:revision,__catalog_line:structuredClone(line),
  }
}

export function editedCatalogLine(form) {
  const base = form.__catalog_line
  if (!base || !form.__catalog_revision || String(base.id) !== String(form.id) || (form.original_id && String(form.original_id) !== String(base.id))) throw new Error('line_catalog_identity_required')
  if (form.iccid !== base.card_id) throw new Error('line_card_identity_changed')
  if (form.pin) throw new Error('use_explicit_agent_pin_action')
  const next = structuredClone(base)
  next.name = form.name
  next.enabled = form.enabled === true
  next.sim = {...next.sim,imsi:form.imsi,mcc:form.mcc,mnc:form.mnc,msisdn:form.msisdn,smsc:form.smsc}
  next.network = {...next.network,egress_country:form.proxy_country,ims_apn:form.apn,idr_mode:form.idr_mode,cp_mode:form.cp_mode}
  next.ims = {...next.ims,access_network_info:form.sip?.pani || '',access_type:form.sip?.access_type || '',user_equals_phone:form.sip?.user_eq_phone === true}
  return next
}

function expectedRevision(revision) {
  if (!Number.isSafeInteger(revision) || revision < 1) throw new Error('line_catalog_revision_required')
  return revision
}

export const lineAPI = {
  async detect(device) {
    if (!device || device.present !== true) throw new Error('current_sim_device_required')
    let sim = device.sim || {}
    if (device.device_type === 'reader') {
      const result = await go.readerReadback(device)
      if (result.state !== 'applied' || !result.reader) throw new Error(result.error_code || 'reader_readback_unconfirmed')
      sim = {...result.reader.sim,iccid:result.reader.card_id,present:result.reader.card_present}
    }
    return {...sim,present:sim.present === true,iccid:sim.iccid || '',pin_enabled:null}
  },
  async lineConfiguration(lineID) {
    const snapshot = await go.catalogLines(true)
    const line = (snapshot.lines || []).find(value => String(value.id) === String(lineID))
    if (!line) throw new Error('line_not_found')
    return lineForm(line,snapshot.revision)
  },
  async saveInstance(form) {
    const result = await go.saveCatalogLine(editedCatalogLine(form),expectedRevision(form.__catalog_revision))
    return lineForm(result.line,result.revision)
  },
  async softDeletedInstances() {
    const snapshot = await go.catalogLines(true)
    return {instances:(snapshot.lines || []).filter(line => line.deleted).map(line => lineForm(line,snapshot.revision))}
  },
  restoreInstance: (id,revision) => go.restoreCatalogLine(id,expectedRevision(revision)),
  softDeleteInstance: (id,revision) => go.softDeleteCatalogLine(id,expectedRevision(revision)),
  deleteInstance(id,deleteHistory,revision,operation) {
    if (!operation) throw new Error('line_deletion_operation_required')
    return go.permanentlyDeleteCatalogLine(id,expectedRevision(revision),operation,deleteHistory)
  },
}
