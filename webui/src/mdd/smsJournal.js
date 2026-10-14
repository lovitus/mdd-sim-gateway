// Same ownership/receipt rules as android-agent MessageJournal: this is not a send queue.
const PREFIX = 'mdd_sms_receipt_'
const key = id => PREFIX + encodeURIComponent(id)

export function smsOperations(storage, lineID) {
  const legacy = JSON.parse(storage.getItem(`mdd_sms_operation_${lineID}`) || 'null')
  if (legacy?.id && legacy.payload && !storage.getItem(key(legacy.id))) {
    storage.setItem(key(legacy.id), JSON.stringify({...legacy, lineID:String(lineID), state:'unknown', created:0}))
  }
  if (legacy?.id && storage.getItem(key(legacy.id))) storage.removeItem(`mdd_sms_operation_${lineID}`)
  const records = []
  for (let i = 0; i < storage.length; i++) {
    const name = storage.key(i)
    if (!name?.startsWith(PREFIX)) continue
    const row = JSON.parse(storage.getItem(name))
    if (!row?.id || !row.payload || !row.lineID) throw new Error('Invalid stored SMS identity')
    if (!lineID || row.lineID === String(lineID)) records.push(row)
  }
  return records.sort((a,b) => b.created - a.created)
}

export function beginSMS(storage, lineID, payload) {
  const records = smsOperations(storage, lineID)
  if (records.length >= 128) {
    const resolved = records.findLast(row => row.state === 'submitted')
    if (!resolved) throw new Error('SMS receipt capacity reached; preserve unresolved records before continuing')
    storage.removeItem(key(resolved.id))
  }
  const row = {id:crypto.randomUUID(), lineID:String(lineID), payload:{...payload}, state:'unknown', created:Date.now()}
  storage.setItem(key(row.id), JSON.stringify(row))
  return row
}

export function recordSMSResult(storage, operation, result, error = '') {
  const row = JSON.parse(storage.getItem(key(operation.id)) || 'null')
  if (!row || row.lineID !== operation.lineID || JSON.stringify(row.payload) !== JSON.stringify(operation.payload))
    throw new Error('Stored SMS owner changed')
  const confirmed = result?.message_id === row.id && (row.payload.transport === 'cellular'
    ? result.code === 'cellular_sms_submitted'
    : result.operation_id === row.id && result.accepted === true && result.code === 'sent')
  if (confirmed) row.state = 'submitted'
  // A late error cannot erase a confirmed receipt or another operation.
  if (error && row.state !== 'submitted') row.error = String(error)
  storage.setItem(key(row.id), JSON.stringify(row))
  return row
}

export function replyLine(instances, conversation) {
  if (!conversation?.card_id) return null
  const matches = instances.filter(line => String(line.iccid || line.card_id || '') === conversation.card_id)
  return matches.length === 1 ? matches[0] : null
}
