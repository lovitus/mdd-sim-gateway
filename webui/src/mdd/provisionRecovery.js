// Browser continuity only; Go's durable operation receipt remains authoritative.
const fields = ['operation_id','line_id','line_name','equipment_id','card_id','attachment_id','sim_session_generation','imsi','mcc','mnc','imei','imeisv','msisdn','smsc','reader_port','apn','ims_apn','egress_country','idr_mode','cp_mode','preflight_operation_id']
const key = lineID => `mdd_provision_recovery_${lineID}`
export const provisionTerminal = state => ['succeeded','failed','reconciled'].includes(state)
export function rememberProvision(request, storage = sessionStorage) {
  if (!request.operation_id || !request.line_id || !request.card_id) throw new Error('provision_recovery_identity_required')
  const existing = recalledProvision(request.line_id,storage)
  if (existing && existing.operation_id !== request.operation_id) throw new Error('provision_reconcile_required')
  const safe = Object.fromEntries(fields.filter(field=>request[field] !== undefined).map(field=>[field,request[field]]))
  if (request.enabled !== undefined) safe.enabled = request.enabled
  if (existing) {
    if (fields.some(field=>existing[field] !== safe[field]) || existing.enabled !== safe.enabled) throw new Error('provision_recovery_request_changed')
    return existing
  }
  storage.setItem(key(request.line_id),JSON.stringify(safe))
  return safe
}
export function recalledProvision(lineID, storage = sessionStorage) {
  const raw = storage.getItem(key(lineID))
  if (!raw) return null
  const value = JSON.parse(raw)
  if (!value || value.line_id !== lineID || !value.operation_id || !value.card_id) throw new Error('provision_recovery_invalid')
  if (Object.keys(value).some(field=>!fields.includes(field)&&field!=='enabled') || fields.some(field=>value[field]!==undefined&&typeof value[field]!=='string') || (value.enabled!==undefined&&typeof value.enabled!=='boolean')) throw new Error('provision_recovery_invalid')
  return value
}
export function forgetProvision(request, storage = sessionStorage) {
  if (recalledProvision(request.line_id,storage)?.operation_id === request.operation_id) storage.removeItem(key(request.line_id))
}
