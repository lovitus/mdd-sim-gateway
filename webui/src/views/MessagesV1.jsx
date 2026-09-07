import React, { useCallback, useEffect, useMemo, useState } from 'react'
import { api } from '../api.js'
import { operationID } from '../goV1Adapter.js'
import { messageRouteOptions, retainOrDefaultRoute, routeForExactLine, routeKey } from '../routeSelection.js'
import { useI18n } from '../i18n.jsx'
import AllowancePanel from './AllowancePanel.jsx'
import { createLatestRequestGate } from '../latestRequestGate.js'

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

function directMessagePeer(item) {
  return item.sender || item.recipient || ''
}

function translatedBlockers(value, t) {
  return String(value || '').split(', ').filter(Boolean).map(code => t(code)).join(', ')
}

export default function MessagesV1({ instances, selected: selectedLine, setSelected, subscribe, showToast }) {
  const { t } = useI18n()
  const routes = useMemo(() => messageRouteOptions(instances), [instances])
  const [selectedRoute, setSelectedRoute] = useState('')
  const [messages, setMessages] = useState([])
	const [conversations, setConversations] = useState([])
	const [nextBefore, setNextBefore] = useState('')
	const pageGeneration = React.useRef(0)
	const [loadingOlder, setLoadingOlder] = useState(false)
	const [selectedPeer, setSelectedPeer] = useState('')
	const [selectedEvents, setSelectedEvents] = useState(() => new Set())
  const [loading, setLoading] = useState(false)
  const [recipient, setRecipient] = useState('')
  const [body, setBody] = useState('')
  const [pending, setPending] = useState(loadPending)
  const [sending, setSending] = useState(false)
  const appliedExternalLine = React.useRef('')
	const loadGate = React.useRef(createLatestRequestGate())
  useEffect(() => {
    const pendingRoute = pending ? routes.find(value => routeKey(value) === `${pending.transport}:${pending.line_id}`) : null
    if (pending) {
      setSelectedRoute(routeKey(pendingRoute))
      return
    }
    const requestedLineID = selectedLine?.id
    if (requestedLineID && appliedExternalLine.current !== String(requestedLineID)) {
      const exact = routeForExactLine(routes, requestedLineID)
      setSelectedRoute(routeKey(exact)); appliedExternalLine.current = String(requestedLineID)
      return
    }
    setSelectedRoute(routeKey(retainOrDefaultRoute(routes, selectedRoute)))
  }, [routes, pending, selectedLine?.id, selectedRoute])
  const route = routes.find(value => routeKey(value) === selectedRoute)
	useEffect(() => {
		loadGate.current.select(selectedRoute)
		pageGeneration.current++
		setMessages([]); setConversations([]); setNextBefore(''); setSelectedPeer(''); setSelectedEvents(new Set()); setLoading(false)
	}, [selectedRoute])
  const selectRoute = event => {
    const next = routes.find(value => routeKey(value) === event.target.value)
    setSelectedRoute(event.target.value)
    if (next) { appliedExternalLine.current = String(next.line.id); setSelected?.(String(next.line.id)) }
  }
  const load = useCallback(async () => {
	if (!route) { setMessages([]); setLoading(false); return }
	const expectedRoute = routeKey(route)
	const token = loadGate.current.begin(expectedRoute)
    setLoading(true)
    try {
	  const result = await api.messageConversationsV1(route.line.id, route.transport)
	  if (loadGate.current.accepts(token)) setConversations(previous => JSON.stringify(previous) === JSON.stringify(result.conversations || []) ? previous : result.conversations || [])
	} catch (error) {
	  if (loadGate.current.accepts(token)) showToast(error.message)
	} finally {
	  if (loadGate.current.accepts(token)) setLoading(false)
	}
  }, [route?.line?.id, route?.transport, showToast])
  useEffect(() => { void load() }, [load])
	useEffect(() => {
		const generation = ++pageGeneration.current
		setMessages([]); setNextBefore(''); setSelectedEvents(new Set()); setLoadingOlder(false)
		if (!route || !selectedPeer) return
		let stopped = false
		api.messagePageV1(route.line.id, route.transport, selectedPeer).then(result => {
			if (!stopped && generation === pageGeneration.current) { setMessages(result.messages || []); setNextBefore(result.next_before || '') }
		}).catch(error => { if (!stopped && generation === pageGeneration.current) showToast(error.message) })
		return () => { stopped = true; pageGeneration.current++ }
	}, [route?.line?.id, route?.transport, selectedPeer,
		conversations.find(item => item.peer === selectedPeer)?.last?.event_id,
		conversations.find(item => item.peer === selectedPeer)?.count])
	const loadOlder = async () => {
		if (!route || !selectedPeer || !nextBefore || loadingOlder) return
		const generation = pageGeneration.current
		setLoadingOlder(true)
		try {
			const result = await api.messagePageV1(route.line.id, route.transport, selectedPeer, nextBefore)
			if (generation === pageGeneration.current) {
				setMessages(previous => { const ids = new Set(previous.map(item => item.event_id)); return [...(result.messages || []).filter(item => !ids.has(item.event_id)), ...previous] })
				setNextBefore(result.next_before || '')
			}
		} catch (error) { if (generation === pageGeneration.current) showToast(error.message) }
		finally { if (generation === pageGeneration.current) setLoadingOlder(false) }
	}
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
    if (pending) { if (route?.ready) await dispatch(pending); return }
    if (!route?.ready || !recipient.trim() || !body.trim()) return
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
	useEffect(() => {
		if (selectedPeer && conversations.some(item => item.peer === selectedPeer)) return
		setSelectedPeer(conversations[0]?.peer || '')
	}, [conversations, selectedPeer])
	const selectedConversation = conversations.find(item => item.peer === selectedPeer)
	const visibleMessages = messages
	const deleteHistory = async (scope) => {
		if (!route || loading || sending || pending || (scope === 'conversation' && !selectedConversation) ||
			!window.confirm(t(scope === 'all' ? 'Delete all history for this line and transport?' : 'Delete this conversation history?'))) return
		const token = loadGate.current.begin(routeKey(route))
		try {
			await api.deleteMessageHistoryV1({ line_id: String(route.line.id), transport: route.transport,
				...(scope === 'all' ? { all: true } : { peer: selectedPeer }) })
			if (loadGate.current.accepts(token)) await load()
		} catch (error) { showToast(error.message) }
	}
	const deleteSelected = async () => {
		if (!route || loading || sending || pending || !selectedEvents.size ||
			!window.confirm(t('Delete selected message records?'))) return
		const token = loadGate.current.begin(routeKey(route))
		try {
			await api.deleteMessageHistoryV1({ line_id: String(route.line.id), transport: route.transport, event_ids: [...selectedEvents] })
			if (loadGate.current.accepts(token)) { setSelectedEvents(new Set()); await load() }
		} catch (error) { showToast(error.message) }
	}
  return <div className="u-page">
    <div className="card u-panel"><div className="u-card-head"><div><h2>{t('Messages')}</h2><p>{t('Choose an explicitly ready transport. MDD never falls through to another SIM or transport.')}</p></div><button className="btn btn-ghost" disabled={loading} onClick={load}>{t('Refresh')}</button></div>
      <label>{t('Line and transport')}</label><select value={selectedRoute} disabled={!!pending} onChange={selectRoute}>
        {!routes.length && <option value="">{t('No lines configured')}</option>}
        {routes.map(value => <option key={`${value.transport}:${value.line.id}`} value={`${value.transport}:${value.line.id}`}>
          {value.line.name || value.line.id} · {value.transport === 'cellular' ? t('Cellular modem') : 'VoWiFi'}{value.ready ? '' : ` · ${t('Unavailable')} (${translatedBlockers(value.blocked, t)})`}
        </option>)}</select>
      {route && <p className="u-note">ICCID {route.line.iccid || '—'} · {route.transport === 'cellular' ? `SMSC ${route.line.smsc || '—'} · ` : ''}{route.ready ? (route.transport === 'cellular' ? t('Fresh modem SMS route') : t('Fresh IMS messaging route')) : `${t('History remains available; sending is blocked')}: ${translatedBlockers(route.blocked, t)}`}</p>}
    </div>
	<div className="u-split"><aside className="card u-panel"><div className="u-card-head"><h2>{t('Conversations')}</h2><button className="btn btn-ghost" disabled={!messages.length || loading || sending || !!pending} onClick={() => deleteHistory('all')}>{t('Clear all')}</button></div>{loading ? <p>{t('Loading…')}</p> : !conversations.length ? <p className="u-muted">{t('No messages')}</p> : <div className="u-message-list">{conversations.map(item => <button type="button" className={`u-message ${item.peer === selectedPeer ? 'active' : ''}`} key={item.peer} onClick={() => setSelectedPeer(item.peer)}><div><b>{item.peer}</b><span>{item.count}</span></div><p>{item.last.body || item.last.state || item.last.kind}</p></button>)}</div>}</aside>
	<div className="card u-panel"><div className="u-card-head"><h2>{selectedPeer || t('Conversation history')}</h2><div className="u-inline"><button className="btn btn-ghost" disabled={!selectedConversation || loading || sending || !!pending} onClick={() => deleteHistory('conversation')}>{t('Delete conversation')}</button><button className="btn btn-danger-outline" disabled={!selectedEvents.size || loading || sending || !!pending} onClick={deleteSelected}>{t('Delete selected')}</button></div></div>
	  {loading ? <p>{t('Loading…')}</p> : !visibleMessages.length ? <p className="u-muted">{t('No messages')}</p> :
		<div className="u-message-list">{visibleMessages.map((item, index) => <div className={`u-message ${item.kind === 'received' ? 'incoming' : 'outgoing'}`} key={`${item.event_id || index}`}>
		  <div><span className="u-inline">{item.event_id && <input type="checkbox" checked={selectedEvents.has(item.event_id)} onChange={event => setSelectedEvents(previous => { const next = new Set(previous); if (event.target.checked) next.add(item.event_id); else next.delete(item.event_id); return next })}/>}<b>{directMessagePeer(item) || selectedPeer || '—'}</b></span><span>{new Date(item.observed_at || item.received_at).toLocaleString()}</span></div>
          <p>{item.body || `${item.kind || 'event'} · ${item.state || ''}`}</p>
		  <small>{item.provider_id || route?.transport} · {item.state || item.status || 'unknown'}{item.error_code ? ` · ${item.error_code}` : ''} · {item.event_id || ''}</small>
		</div>)}</div>}
	{nextBefore && <button className="btn btn-ghost" disabled={loadingOlder} onClick={loadOlder}>{t(loadingOlder ? 'Loading…' : 'Load earlier messages')}</button>}
	</div></div>
    <div className="card u-panel"><h2>{pending ? t('Unresolved send request') : t('New message')}</h2>
      {pending && <p className="u-note">{t('The prior outcome is not confirmed. Fields are locked and Retry reuses exactly the same operation and message IDs.')}</p>}
      <div className="u-form-grid"><div><label>{t('Recipient')}</label><input value={recipient} disabled={!!pending} onChange={event => setRecipient(event.target.value)}/></div></div>
      <label>{t('Message')}</label><textarea rows="5" value={body} disabled={!!pending} onChange={event => setBody(event.target.value)}/>
      <div className="u-inline"><button className="btn btn-primary" disabled={sending || !route?.ready || (!pending && (!recipient.trim() || !body.trim()))} onClick={send}>{t(sending ? 'Sending…' : pending ? 'Retry the same request' : 'Send')}</button>
        {pending && <button className="btn btn-ghost" disabled={sending} onClick={discard}>{t('Discard retry identity')}</button>}</div>
    </div>
    {route && (
      <AllowancePanel instanceId={String(route.line.id)} mode="messages" transport={route.transport} showToast={showToast}/>
    )}
  </div>
}
