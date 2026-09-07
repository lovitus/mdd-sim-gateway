import {api as go} from '../api.js'

export const hardwareAPI = {deleteDevice:device => go.hideOfflineDevice(device)}

export async function saveReaderIMEI(device, input, snapshot) {
  const lineID = device?.instance_id
  const cardID = device?.sim?.iccid
  if (device?.device_type !== 'reader' || !lineID || !cardID || device.present !== true) throw new Error('reader_imei_binding_identity_required')
  if (!snapshot?.revision || !snapshot.catalog_revision || !/^\d{15}$/.test(input.imei)) throw new Error('imei_binding_revision_required')
  const existing = input.id ? snapshot.pool.find(entry => entry.id === input.id) : snapshot.pool.find(entry => entry.imei === input.imei)
  if (input.id && (!existing || existing.imei !== input.imei)) throw new Error('imei_pool_selection_changed')
  let revision = snapshot.revision
  let entryID = existing?.id
  if (!existing || existing.name !== input.name) {
    entryID ||= crypto.randomUUID()
    const saved = await go.saveIMEIEntryExpected({schema_version:1,id:entryID,imei:input.imei,name:input.name,notes:existing?.notes || ''},revision)
    revision = saved.revision
  }
  try {
    return await go.bindIMEIExpected(entryID,String(lineID),cardID,revision,snapshot.catalog_revision)
  } catch (error) {
    if (revision !== snapshot.revision) error.message = `IMEI pool entry saved; SIM binding failed: ${error.message}`
    throw error
  }
}
