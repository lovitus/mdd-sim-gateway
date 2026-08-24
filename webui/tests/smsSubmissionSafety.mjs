import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const messages = fs.readFileSync(path.join(root, 'src/views/Messages.jsx'), 'utf8')
const api = fs.readFileSync(path.join(root, 'src/api.js'), 'utf8')
const send = messages.slice(messages.indexOf('  const send = async () => {'),
  messages.indexOf('  const toast ='))

assert.ok(api.includes('operation_id: operationId'))
assert.ok(api.includes('/sms/submissions/${encodeURIComponent(operationId)}/ack'))
assert.ok(send.includes('mdd_sms_operation_${forId}'))
assert.ok(send.includes('crypto.randomUUID()'))
assert.ok(send.includes('Boolean(res.submission_acknowledged)'))
const unknownCheck = send.indexOf('res.ok === false && res.uncertain')
const firstAck = send.indexOf('await api.ackSmsSubmission')
assert.ok(unknownCheck >= 0 && unknownCheck < firstAck)
assert.ok(send.includes("if (acknowledged && activeId.current === forId)"))
assert.ok(send.includes('The SMS outcome is unknown. Acknowledge it'))

console.log('SMS submission safety tests passed')
