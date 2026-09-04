import React, { useCallback, useEffect, useState } from 'react'
import { api } from '../api.js'
import { runNotificationTest } from '../notificationTestTracker.js'
import { useI18n } from '../i18n.jsx'

const channels = ['webhook', 'telegram', 'pushplus']

function secret(view) {
  return { configured: view?.configured === true, value: '', clear: false }
}

function editable(view) {
  return {
    revision: Number(view.revision || 0),
    timezone: view.timezone || 'UTC',
    supported_events: view.supported_events || [],
    unsupported_reasons: view.unsupported_reasons || {},
    webhook: {
      enabled: view.webhook?.enabled === true, events: view.webhook?.events || {},
      format: view.webhook?.format || 'generic', method: view.webhook?.method || 'POST',
      body_mode: view.webhook?.body_mode || 'json', tls_cert_sha256: view.webhook?.tls_cert_sha256 || '',
      url: secret(view.webhook?.url), headers_json: secret(view.webhook?.headers),
      payload_template: secret(view.webhook?.payload_template),
    },
    telegram: {
      enabled: view.telegram?.enabled === true, events: view.telegram?.events || {},
      proxy_mode: view.telegram?.proxy_mode || 'direct', proxy_country: view.telegram?.proxy_country || '',
      bot_token: secret(view.telegram?.bot_token), chat_id: secret(view.telegram?.chat_id),
      proxy_url: secret(view.telegram?.proxy_url),
    },
    pushplus: {
      enabled: view.pushplus?.enabled === true, events: view.pushplus?.events || {},
      template: view.pushplus?.template || 'html', channel: view.pushplus?.channel || 'wechat',
      token: secret(view.pushplus?.token), topic: secret(view.pushplus?.topic),
    },
  }
}

function secretPatch(target, key, value) {
  if (value.clear) target[key] = ''
  else if (value.value !== '') target[key] = value.value
}

function requestFrom(draft) {
  const webhook = {
    enabled: draft.webhook.enabled, events: draft.webhook.events,
    format: draft.webhook.format, method: draft.webhook.method,
    body_mode: draft.webhook.body_mode, tls_cert_sha256: draft.webhook.tls_cert_sha256,
  }
  secretPatch(webhook, 'url', draft.webhook.url)
  secretPatch(webhook, 'headers_json', draft.webhook.headers_json)
  secretPatch(webhook, 'payload_template', draft.webhook.payload_template)
  const telegram = {
    enabled: draft.telegram.enabled, events: draft.telegram.events,
    proxy_mode: draft.telegram.proxy_mode, proxy_country: draft.telegram.proxy_country,
  }
  secretPatch(telegram, 'bot_token', draft.telegram.bot_token)
  secretPatch(telegram, 'chat_id', draft.telegram.chat_id)
  secretPatch(telegram, 'proxy_url', draft.telegram.proxy_url)
  const pushplus = {
    enabled: draft.pushplus.enabled, events: draft.pushplus.events,
    template: draft.pushplus.template, channel: draft.pushplus.channel,
  }
  secretPatch(pushplus, 'token', draft.pushplus.token)
  secretPatch(pushplus, 'topic', draft.pushplus.topic)
  return { expected_revision: draft.revision, timezone: draft.timezone, webhook, telegram, pushplus }
}

function SecretField({ label, value, onChange }) {
  const { t } = useI18n()
  const [revealed, setRevealed] = useState(false)
  return <div>
    <label>{label}</label>
    <div className="u-inline"><input type={revealed ? 'text' : 'password'} autoComplete="new-password" value={value.value}
      placeholder={value.configured ? t('Configured; leave blank to keep') : t('Not configured')}
      disabled={value.clear} onChange={event => onChange({ ...value, value: event.target.value })}/>
	<button type="button" className="btn btn-ghost" aria-pressed={revealed}
	  aria-label={t(revealed ? 'Hide current input' : 'Show current input')}
        title={t(revealed ? 'Hide current input' : 'Show current input')}
        onClick={() => setRevealed(current => !current)}>{revealed ? '◉' : '◎'}</button></div>
    <label className="u-inline"><input type="checkbox" checked={value.clear}
      onChange={event => onChange({ ...value, clear: event.target.checked, value: '' })}/>
      {t('Clear saved value')}</label>
  </div>
}

function EventOptions({ draft, channel, setDraft }) {
  const { t } = useI18n()
  const supported = new Set(draft.supported_events)
  const labels = {
    incoming_call: t('Incoming call'), incoming_sms: t('Incoming SMS'),
    host_alert: t('Host alert'), activation_reminder: t('Activation reminder'),
  }
  return <div className="u-event-options"><label>{t('Forward these events')}</label>
    <div className="u-inline">{Object.entries(labels).map(([event, label]) =>
      <label key={event}><input type="checkbox" className="u-toggle" disabled={!supported.has(event)}
        checked={draft[channel].events?.[event] === true}
        onChange={input => setDraft(current => ({ ...current, [channel]: { ...current[channel],
          events: { ...current[channel].events, [event]: input.target.checked } } }))}/>{label}</label>)}</div>
  </div>
}

export default function NotificationsV1({ showToast }) {
  const { t } = useI18n()
  const [draft, setDraft] = useState(null)
	const [savedDraft, setSavedDraft] = useState(null)
  const [deliveries, setDeliveries] = useState([])
  const [egressCountries, setEgressCountries] = useState([])
  const [tab, setTab] = useState('channels')
  const [busy, setBusy] = useState('')
  const loadDeliveries = useCallback(() => api.notificationDeliveries()
    .then(result => setDeliveries(result.deliveries || [])), [])
	const load = useCallback(() => api.notificationConfig().then(value => {
		const next = editable(value); setDraft(next); setSavedDraft(next)
	}), [])
  useEffect(() => {
    void load(); void loadDeliveries()
    void api.egressConfig().then(result => setEgressCountries(
      Object.keys(result.config?.exits || {}).sort())).catch(() => setEgressCountries([]))
  }, [load, loadDeliveries])
  if (!draft) return <p>{t('Loading…')}</p>
	const dirty = !!savedDraft && JSON.stringify(requestFrom(draft)) !== JSON.stringify(requestFrom(savedDraft))
  const patch = (channel, value) => setDraft(current => ({ ...current,
    [channel]: { ...current[channel], ...value } }))
  const save = async () => {
    setBusy('save')
    try {
      const updated = await api.saveNotificationConfig(requestFrom(draft))
	  const next = editable(updated); setDraft(next); setSavedDraft(next); showToast(t('Saved'))
    } catch (error) { showToast(error.message) } finally { setBusy('') }
  }
  const test = async channel => {
	if (dirty) { showToast(t('Save notification changes before testing.')); return }
    if (!window.confirm(t('Send one real test through this configured channel?'))) return
    setBusy(`test-${channel}`)
    try {
      const result = await runNotificationTest({
        channel,
        enqueue: api.testNotification,
        listDeliveries: api.notificationDeliveries,
		onAccepted: accepted => {
		  showToast(`${t('Test queued')}: ${accepted.delivery.state || 'pending'}`)
		  void loadDeliveries().catch(() => {})
        },
      })
      showToast(result.timed_out
        ? `${t('Notification test timed out')}: ${result.delivery?.state || 'pending'}`
        : `${t('Notification test finished')}: ${result.delivery.state}`)
	  void loadDeliveries().catch(() => {})
    } catch (error) { showToast(error.message) } finally { setBusy('') }
  }
  return <div className="u-page">
    <div className="u-tabs"><button className={tab === 'channels' ? 'active' : ''} onClick={() => setTab('channels')}>{t('Channels')}</button>
      <button className={tab === 'delivery' ? 'active' : ''} onClick={() => { setTab('delivery'); void loadDeliveries() }}>{t('Delivery log')}</button></div>
    {tab === 'channels' && <>
      <div className="u-form-grid"><div><label>{t('Timezone')}</label><input value={draft.timezone}
        onChange={event => setDraft(current => ({ ...current, timezone: event.target.value }))}/></div></div>
      <div className="u-device-grid">
        <div className="card u-panel"><div className="u-card-head"><h2>Webhook</h2><input type="checkbox" className="u-toggle"
          checked={draft.webhook.enabled} onChange={event => patch('webhook', { enabled: event.target.checked })}/></div>
          <div className="u-form-grid"><div><label>{t('Payload format')}</label><select value={draft.webhook.format}
            onChange={event => patch('webhook', { format: event.target.value })}><option value="generic">{t('Standard event fields')}</option><option value="custom">{t('Custom template')}</option></select></div>
            <div><label>{t('Method')}</label><select value={draft.webhook.method} onChange={event => patch('webhook', { method: event.target.value })}><option>POST</option><option>GET</option></select></div>
            <div><label>{t('Body format')}</label><select value={draft.webhook.body_mode} onChange={event => patch('webhook', { body_mode: event.target.value })}><option value="json">JSON</option><option value="form">Form</option><option value="raw">Raw</option></select></div>
            <div><label>{t('Remote TLS certificate SHA-256 (optional)')}</label><input className="mono" value={draft.webhook.tls_cert_sha256} onChange={event => patch('webhook', { tls_cert_sha256: event.target.value })}/></div></div>
          <SecretField label={t('Webhook URL')} value={draft.webhook.url} onChange={value => patch('webhook', { url: value })}/>
          <SecretField label={t('Custom headers (JSON)')} value={draft.webhook.headers_json} onChange={value => patch('webhook', { headers_json: value })}/>
          {draft.webhook.format === 'custom' && (
            <SecretField label={t('Payload template')} value={draft.webhook.payload_template}
              onChange={value => patch('webhook', { payload_template: value })}/>
          )}
		  <EventOptions draft={draft} channel="webhook" setDraft={setDraft}/><button className="btn btn-ghost" disabled={!!busy || dirty} onClick={() => test('webhook')}>{t('Test')}</button>
        </div>
        <div className="card u-panel"><div className="u-card-head"><h2>Telegram</h2><input type="checkbox" className="u-toggle"
          checked={draft.telegram.enabled} onChange={event => patch('telegram', { enabled: event.target.checked })}/></div>
          <SecretField label={t('Bot token')} value={draft.telegram.bot_token} onChange={value => patch('telegram', { bot_token: value })}/>
          <SecretField label={t('Chat / Channel ID')} value={draft.telegram.chat_id} onChange={value => patch('telegram', { chat_id: value })}/>
          <label>{t('Connection')}</label><select value={draft.telegram.proxy_mode} onChange={event => patch('telegram', { proxy_mode: event.target.value })}><option value="direct">{t('Direct')}</option><option value="manual">{t('Manual HTTP/SOCKS proxy')}</option><option value="country">{t('Use country exit')}</option></select>
          {draft.telegram.proxy_mode === 'manual' && (
            <SecretField label={t('Proxy URL')} value={draft.telegram.proxy_url}
              onChange={value => patch('telegram', { proxy_url: value })}/>
          )}
          {draft.telegram.proxy_mode === 'country' && <><label>{t('Country exit')}</label><select value={draft.telegram.proxy_country} onChange={event => patch('telegram', { proxy_country: event.target.value })}><option value="">{t('Select a country/region…')}</option>{[...new Set([...egressCountries, ...(draft.telegram.proxy_country ? [draft.telegram.proxy_country] : [])])].sort().map(country => <option value={country} key={country}>{country.toUpperCase()}</option>)}</select></>}
		  <EventOptions draft={draft} channel="telegram" setDraft={setDraft}/><button className="btn btn-ghost" disabled={!!busy || dirty} onClick={() => test('telegram')}>{t('Test')}</button>
        </div>
        <div className="card u-panel"><div className="u-card-head"><h2>PushPlus</h2><input type="checkbox" className="u-toggle"
          checked={draft.pushplus.enabled} onChange={event => patch('pushplus', { enabled: event.target.checked })}/></div>
          <SecretField label={t('PushPlus token')} value={draft.pushplus.token} onChange={value => patch('pushplus', { token: value })}/>
          <SecretField label={t('Topic code (optional)')} value={draft.pushplus.topic} onChange={value => patch('pushplus', { topic: value })}/>
          <div className="u-form-grid"><div><label>{t('Message template')}</label><select value={draft.pushplus.template} onChange={event => patch('pushplus', { template: event.target.value })}><option value="html">HTML</option><option value="txt">{t('Plain text')}</option><option value="markdown">Markdown</option><option value="json">JSON</option></select></div><div><label>{t('PushPlus channel')}</label><select value={draft.pushplus.channel} onChange={event => patch('pushplus', { channel: event.target.value })}><option value="wechat">{t('WeChat')}</option><option value="app">App</option><option value="mail">{t('Email')}</option><option value="webhook">Webhook</option><option value="cp">{t('WeCom')}</option><option value="clawbot">ClawBot</option></select></div></div>
		  <EventOptions draft={draft} channel="pushplus" setDraft={setDraft}/><button className="btn btn-ghost" disabled={!!busy || dirty} onClick={() => test('pushplus')}>{t('Test')}</button>
        </div>
      </div>
      <button className="btn btn-primary" disabled={!!busy || draft.revision < 1} onClick={save}>{t(busy === 'save' ? 'Saving…' : 'Save')}</button>
    </>}
    {tab === 'delivery' && <div className="card u-panel"><div className="u-card-head"><h2>{t('Delivery log')}</h2><div className="u-inline"><button className="btn btn-ghost" onClick={loadDeliveries}>{t('Refresh')}</button><button className="btn btn-ghost" onClick={async () => { await api.clearNotificationDeliveries(); await loadDeliveries() }}>{t('Clear')}</button></div></div>
      {!deliveries.length ? <p className="u-muted">{t('No delivery records')}</p> : deliveries.map(item => <div className="u-detail" key={item.delivery_id}><span>{item.channel} · {item.event_type || item.event}</span><b>{item.state} · {item.attempts || 0}</b></div>)}</div>}
  </div>
}

export { editable as notificationEditable, requestFrom as notificationRequest }
