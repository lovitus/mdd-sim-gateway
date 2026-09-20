import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const api = fs.readFileSync(path.join(root, 'src/api.js'), 'utf8')
const mounted = fs.readFileSync(path.join(root, 'src/mdd/views/Messages.jsx'), 'utf8')
assert.ok(mounted.includes('if (!isSMSSubmitKey(e)) return'))
assert.ok(mounted.includes('if (!canComposeSMS(senderLine,sendTransport,to,text)) return'))
assert.ok(mounted.includes('operationId = saved.id'))
assert.ok(mounted.includes('sms_ready: [item.operations?.cellular_sms?.ready, item.operations?.vowifi_sms?.ready]'))
assert.ok(mounted.includes("to:String(saved.payload?.to || '').trim()"))
const adapter = fs.readFileSync(path.join(root, 'src/mdd/smsAdapter.js'), 'utf8')
assert.ok(api.includes('/v1/lines/${encodeURIComponent(lineID)}/cellular/messages'))
assert.ok(mounted.includes('localStorage.setItem(operationKey, JSON.stringify({ id: operationId, payload }))'))
assert.ok(mounted.indexOf('localStorage.setItem(operationKey') < mounted.indexOf('await api.sendSms(forId'),
  'durable retry identity is saved before a paid submission')
assert.ok(mounted.includes('await api.sendSms(forId, to, text, payload.transport, operationId, payload.cardID)'))
assert.ok(adapter.includes('operation_id: operationID, message_id: operationID'))
assert.ok(adapter.includes('expected_card_id: String(cardID)'))
assert.ok(mounted.includes('Retry uses the same request identity; do not create a second send.'))
assert.ok(mounted.includes('This cannot retract a message that may already have been submitted.'))
console.log('Mounted SMS submission retains operation, message and SIM identity before dispatch')
