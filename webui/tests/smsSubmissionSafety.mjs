import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const messages = fs.readFileSync(path.join(root, 'src/views/MessagesV1.jsx'), 'utf8')
const api = fs.readFileSync(path.join(root, 'src/api.js'), 'utf8')
const send = messages.slice(messages.indexOf('  const dispatch = async value => {'),
  messages.indexOf('  const discard ='))

assert.ok(api.includes('/v1/lines/${encodeURIComponent(lineID)}/cellular/messages'))
assert.ok(messages.includes("const pendingKey = 'mdd.go.pendingMessage'"))
assert.ok(messages.includes('sessionStorage.setItem(pendingKey, JSON.stringify(value))'))
assert.ok(send.includes('operation_id: value.operation_id'))
assert.ok(send.includes('message_id: value.message_id'))
assert.ok(send.includes('expected_card_id: value.expected_card_id'))
assert.ok(messages.includes("if (pending) { if (route?.ready) await dispatch(pending); return }"))
assert.ok(messages.includes('Retry uses the same request identity; do not create a second send.'))
assert.ok(messages.includes('This cannot retract a message that may already have been submitted.'))

console.log('SMS submission safety tests passed')
