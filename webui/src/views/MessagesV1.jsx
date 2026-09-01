import React, { useCallback, useEffect, useMemo, useState } from 'react'
import { api } from '../api.js'
import { operationID } from '../goV1Adapter.js'
import { useI18n } from '../i18n.jsx'
import AllowancePanel from './AllowancePanel.jsx'

const pendingKey = 'mdd.go.pendingMessage'

function loadPending() {
  try {
    const value = JSON.parse(sessionStorage.getItem(pendingKey) || 'null')
    return value?.operation_id && value?.message_id ? value : null
  } catch { return null }
}

function savePending(value) {
  try {
    if (value) sessionStorage.setItem(pendingKey, JSON.stringify(value))
    else sessionStorage.removeItem(pendingKey)
  } catch {}
}

function routesFor(instances) {
  const result = []
  for (const line of instances || []) {
    if (line.operations?.vowifi_sms?.ready) result.push({ line, transport: 'vowifi' })
    if (line.operations?.cellular_sms?.ready) result.push({ line, transport: 'cellular' })
  }
  return result
}

function messagePeer(item) {
  return item.sender || item.recipient || '—'
}

export default function MessagesV1({ instances, subscribe, showToast }) {
  const { t } = useI18n()
  const routes = useMemo(() => routesFor(instances), [instances])
  const [selected, setSelected] = useState('')
  const [messages, setMessages] = useState([])
  const [loading, setLoading] = useState(false)
  const [recipient, setRecipient] = useState('')
  const [body, setBody] = useState('')
  const [pending, setPending] = useState(loadPending)
  const [sending, setSending] = useState(false)
  useEffect(() => {
    const wanted = pending ? `${pending.transport}:${pending.line_id}` : selected
    if (wanted && routes.some(route => `${route.transport}:${route.line.id}` === wanted)) {
      if (selected !== wanted) setSelected(wanted)
      return
    }
    const first = routes[0]
    setSelected(first ? `${first.transport}:${first.line.id}` : '')
  }, [routes, pending, selected])
  const route = routes.find(value => `${value.transport}:${value.line.id}` === selected)
  const load = useCallback(async () => {
    if (!route) { setMessages([]); return }
    setLoading(true)
    try {
      const result = await api.listMessagesV1(route.line.id, route.transport)
      setMessages(result.messages || [])
    } catch (error) { showToast(error.message) } finally { setLoading(false) }
  }, [route?.line?.id, route?.transport, showToast])
  useEffect(() => { void load() }, [load])
  useEffect(() => subscribe?.(message => {
    if (message.type === 'go.snapshot') void load()
  }), [subscribe, load])
  useEffect(() => {
    if (!pending) return
    setRecipient(pending.recipient || '')
    setBody(pending.body || '')
  }, [pending])
  const dispatch = async value => {
    setSending(true)
    try {
      const result = await api.sendMessageV1(value.line_id, value.transport, {
        operation_id: value.operation_id,
        message_id: value.message_id,
        recipient: value.recipient,
        body: value.body,
        expected_card_id: value.expected_card_id,
      })
      savePending(null); setPending(null); setBody('')
      showToast(`${t('Server accepted the message')}: ${result.code || 'submitted'}`)
      await load()
    } catch (error) {
      showToast(`${error.message}. ${t('Retry uses the same request identity; do not create a second send.')}`)
    } finally { setSending(false) }
  }
  const send = async () => {
    if (pending) { await dispatch(pending); return }
    if (!route || !recipient.trim() || !body.trim()) return
    const value = {
      transport: route.transport,
      line_id: String(route.line.id),
      expected_card_id: String(route.line.iccid || route.line.card_id || ''),
      operation_id: operationID('react-sms'),
      message_id: operationID('message'),
      recipient: recipient.trim(), body,
    }
    if (!value.expected_card_id) { showToast(t('The exact SIM identity is unavailable.')); return }
    setPending(value); savePending(value)
    await dispatch(value)
  }
  const discard = () => {
    if (!window.confirm(t('Discard only this browser retry identity? This cannot retract a message that may already have been submitted.'))) return
    savePending(null); setPending(null); setRecipient(''); setBody('')
  }
  return <div className="u-page">
    <div className="card u-panel"><div className="u-card-head"><div><h2>{t('Messages')}</h2><p>{t('Choose an explicitly ready transport. MDD never falls through to another SIM or transport.')}</p></div><button className="btn btn-ghost" disabled={loading} onClick={load}>{t('Refresh')}</button></div>
      <label>{t('Line and transport')}</label><select value={selected} disabled={!!pending} onChange={event => setSelected(event.target.value)}>
        {!routes.length && <option value="">{t('No SMS route is ready')}</option>}
        {routes.map(value => <option key={`${value.transport}:${value.line.id}`} value={`${value.transport}:${value.line.id}`}>
          {value.line.name || value.line.id} · {value.transport === 'cellular' ? t('Cellular modem') : 'VoWiFi'}
        </option>)}</select>
      {route && <p className="u-note">ICCID {route.line.iccid || '—'} · {route.transport === 'cellular' ? t('Fresh modem SMS route') : t('Fresh IMS messaging route')}</p>}
    </div>
    <div className="card u-panel"><h2>{t('Conversation history')}</h2>
      {loading ? <p>{t('Loading…')}</p> : !messages.length ? <p className="u-muted">{t('No messages')}</p> :
        <div className="u-message-list">{messages.map((item, index) => <div className={`u-message ${item.kind === 'received' ? 'incoming' : 'outgoing'}`} key={`${item.event_id || index}`}>
          <div><b>{messagePeer(item)}</b><span>{new Date(item.observed_at || item.received_at).toLocaleString()}</span></div>
          <p>{item.body || `${item.kind || 'event'} · ${item.state || ''}`}</p>
          <small>{item.provider_id || route?.transport} · {item.event_id || ''}</small>
        </div>)}</div>}
    </div>
    <div className="card u-panel"><h2>{pending ? t('Unresolved send request') : t('New message')}</h2>
      {pending && <p className="u-note">{t('The prior outcome is not confirmed. Fields are locked and Retry reuses exactly the same operation and message IDs.')}</p>}
      <div className="u-form-grid"><div><label>{t('Recipient')}</label><input value={recipient} disabled={!!pending} onChange={event => setRecipient(event.target.value)}/></div></div>
      <label>{t('Message')}</label><textarea rows="5" value={body} disabled={!!pending} onChange={event => setBody(event.target.value)}/>
      <div className="u-inline"><button className="btn btn-primary" disabled={sending || (!pending && (!route || !recipient.trim() || !body.trim()))} onClick={send}>{t(sending ? 'Sending…' : pending ? 'Retry the same request' : 'Send')}</button>
        {pending && <button className="btn btn-ghost" disabled={sending} onClick={discard}>{t('Discard retry identity')}</button>}</div>
    </div>
    {route && (
      <AllowancePanel instanceId={String(route.line.id)} mode="messages" transport={route.transport} showToast={showToast}/>
    )}
  </div>
}
