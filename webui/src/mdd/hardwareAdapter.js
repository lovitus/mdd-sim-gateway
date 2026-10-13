import {api as go} from '../api.js'

export const hardwareAPI = {deleteDevice:device => go.hideOfflineDevice(device)}

// Shared by Hardware, SIM setup and VoWiFi. A pending binding follows the card,
// not the last reader or a cached line projection.
export function readerIMEIBinding(data, cardID) {
  if (!Number.isSafeInteger(data?.revision) || data.revision < 0 ||
      !Number.isSafeInteger(data.catalog_revision) || data.catalog_revision <= 0 ||
      !Array.isArray(data.pool) || !Array.isArray(data.unpooled) ||
      !data.bindings || typeof data.bindings !== 'object' || Array.isArray(data.bindings)) {
    throw new Error('imei_pool_snapshot_invalid')
  }
  const binding = data.bindings[cardID]
  const entry = binding && data.pool.find(item => item.id === binding.imei_id)
  if (binding && (!entry || entry.imei !== binding.imei)) throw new Error('imei_binding_entry_mismatch')
  const saved = entry || data.unpooled.find(item => item.card_id === cardID) || null
  const validIMEI = entry ? /^\d{15}$/ : /^\d{14,16}$/
  if (saved && (typeof saved.imei !== 'string' || !validIMEI.test(saved.imei))) throw new Error('imei_binding_value_invalid')
  return { saved, entry }
}

export async function saveReaderIMEI(device, input, snapshot) {
  const cardID = device?.sim?.iccid
  if (device?.device_type !== 'reader' || !cardID || device.present !== true) throw new Error('reader_imei_binding_identity_required')
  if (!Number.isSafeInteger(snapshot?.revision) || snapshot.revision < 0 || !Number.isSafeInteger(snapshot.catalog_revision) || snapshot.catalog_revision <= 0 || !/^\d{15}$/.test(input.imei)) throw new Error('imei_binding_revision_required')
  const {revision:expectedRevision,catalog_revision:catalogRevision} = snapshot
  const existing = input.id ? snapshot.pool.find(entry => entry.id === input.id) : snapshot.pool.find(entry => entry.imei === input.imei)
  if (input.id && (!existing || existing.imei !== input.imei)) throw new Error('imei_pool_selection_changed')
  let revision = expectedRevision
  let entryID = existing?.id
  if (!existing || existing.name !== input.name) {
    entryID ||= crypto.randomUUID()
    const saved = await go.saveIMEIEntryExpected({schema_version:1,id:entryID,imei:input.imei,name:input.name,notes:existing?.notes || ''},revision)
    revision = saved.revision
  }
  try {
    return await go.bindImeiToIccid({imei_id:entryID,iccid:cardID},{revision,catalog_revision:catalogRevision})
  } catch (error) {
    if (revision !== expectedRevision) error.message = `IMEI pool entry saved; SIM binding failed: ${error.message}`
    throw error
  }
}
