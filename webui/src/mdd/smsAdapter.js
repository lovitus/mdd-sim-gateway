import { api as go } from '../api.js'

export function smsRequest(lineID, to, body, transport, operationID, cardID) {
  if (!['cellular', 'vowifi'].includes(transport)) throw new Error('sms_transport_required')
  if (!lineID || !cardID || !operationID || !to.trim() || !body.trim()) throw new Error('sms_identity_required')
  return { operation_id: operationID, message_id: operationID,
    expected_card_id: String(cardID), recipient: to.trim(), body }
}

export const smsAPI = {
  sendSms(lineID, to, body, transport, operationID, cardID) {
    return go.sendMessageV1(lineID, transport, smsRequest(lineID, to, body, transport, operationID, cardID))
  },
}
