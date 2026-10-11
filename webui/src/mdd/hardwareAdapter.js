import {api as go} from '../api.js'

export const hardwareAPI = {deleteDevice:device => go.hideOfflineDevice(device)}

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
