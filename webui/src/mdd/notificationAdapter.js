import { api as go } from '../api.js'
import { recordedUnixSeconds } from '../goV1Adapter.js'
import { runNotificationTest } from '../notificationTestTracker.js'

const secretNames = {
  webhook: { url:'url', headers_json:'headers', payload_template:'payload_template' },
  telegram: { bot_token:'bot_token', chat_id:'chat_id', proxy_url:'proxy_url' },
  pushplus: { token:'token', topic:'topic' },
}

export function notificationSettingsView(view, exits = {}) {
  const result = { timezone:view.timezone, proxy:{exits}, __revision:view.revision, __supported_events:view.supported_events || [] }
  for (const channel of Object.keys(secretNames)) {
    const source = view[channel] || {}
    const value = {...source, events:{...source.events}, __configured:{}, __clear:{}}
    for (const [field, wire] of Object.entries(secretNames[channel])) {
      delete value[wire]
      value[field] = ''
      value.__configured[field] = source[wire]?.configured === true
    }
    result[channel] = value
  }
  return result
}

export function notificationSettingsPatch(draft) {
  const result = { expected_revision:draft.__revision, timezone:draft.timezone }
  const fields = {webhook:['enabled','format','method','body_mode','tls_cert_sha256'],telegram:['enabled','proxy_mode','proxy_country'],pushplus:['enabled','template','channel']}
  for (const channel of Object.keys(secretNames)) {
    const source = draft[channel]
    const value = {}
    for (const field of fields[channel]) if (source[field] !== undefined) value[field] = source[field]
    value.events = Object.fromEntries((draft.__supported_events || []).filter(event => Object.hasOwn(source.events || {}, event)).map(event => [event, source.events[event] === true]))
    for (const field of Object.keys(secretNames[channel])) {
      if (source.__clear?.[field]) value[field] = ''
      else if (typeof source[field] === 'string' && source[field] !== '') value[field] = source[field]
    }
    result[channel] = value
  }
  return result
}

let saved = null
async function test(channel, draft) {
  if (!saved || JSON.stringify(draft) !== JSON.stringify(saved[channel])) throw new Error('Save notification changes before testing.')
  const result = await runNotificationTest({channel,enqueue:go.testNotification,listDeliveries:go.notificationDeliveries})
  if (result.timed_out || result.delivery?.state !== 'delivered') throw new Error(result.delivery?.code || 'notification_delivery_unconfirmed')
  return result
}

export const notificationAPI = {
  async notificationSettings() {
    const view = await go.notificationConfig()
    let exits = {}, egressError = ''
    try { exits = (await go.egressConfig()).config?.exits || {} }
    catch (error) { egressError = error.message }
    saved = {...notificationSettingsView(view,exits),__egress_error:egressError}
    return structuredClone(saved)
  },
  async saveNotificationSettings(draft) {
    const view = await go.saveNotificationConfig(notificationSettingsPatch(draft))
    saved = notificationSettingsView(view,draft.proxy?.exits || {})
    return structuredClone(saved)
  },
  async notificationDeliveries() {
    const result = await go.notificationDeliveries()
    const rows = (result.deliveries || []).map(item => ({...item,id:item.delivery_id,event:item.event_type,status:item.state,finished_at:recordedUnixSeconds(item.finished_at)}))
    return {pending:rows.filter(item=>['pending','sending'].includes(item.state)),history:rows.filter(item=>!['pending','sending'].includes(item.state))}
  },
  testTelegram: draft => test('telegram',draft),
  testWebhook: draft => test('webhook',draft),
  testPushPlus: draft => test('pushplus',draft),
}
