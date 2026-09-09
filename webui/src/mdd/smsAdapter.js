import { api as go } from '../api.js'

// MDN keydown/IME guidance: composition can end before Enter, while keyCode is still 229.
export function isSMSSubmitKey(event) {
  const native = event.nativeEvent || event
  return event.key === 'Enter' && !event.repeat && !native.isComposing && native.keyCode !== 229
}

export function canComposeSMS(line, transport, recipient, body) {
  return Boolean(line?.id && (line.iccid || line.card_id) && ['cellular','vowifi'].includes(transport) &&
    line.operations?.[`${transport}_sms`]?.ready === true && String(recipient || '').trim() && String(body || '').trim())
}

export function smsRequest(lineID, to, body, transport, operationID, cardID) {
  if (!['cellular', 'vowifi'].includes(transport)) throw new Error('sms_transport_required')
  if (!lineID || !cardID || !operationID || !to.trim() || !body.trim()) throw new Error('sms_identity_required')
  return { operation_id: operationID, message_id: operationID,
    expected_card_id: String(cardID), recipient: to.trim(), body }
}

export const smsAPI = {
  readSmsReceipt(lineID, stored) {
    if (!stored?.id || stored.payload?.transport !== 'cellular') throw new Error('cellular_sms_receipt_required')
    const p=stored.payload
    return go.sendMessageV1(lineID,'cellular',{...smsRequest(lineID,p.to,p.body,p.transport,stored.id,p.cardID),reconcile_only:true})
  },
  sendSms(lineID, to, body, transport, operationID, cardID) {
    return go.sendMessageV1(lineID, transport, smsRequest(lineID, to, body, transport, operationID, cardID))
  },
}
