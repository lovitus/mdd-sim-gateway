import React, { useEffect, useState, useCallback, useRef } from 'react'
import { api } from '../api.js'
import { mergeMessagePages } from '../historyAdapter.js'
import SimSelector from './SimSelector.jsx'
import { useI18n } from '../i18n.jsx'
import AllowancePanel from './AllowancePanel.jsx'

// Keep the existing array reference when a duplicate WebSocket event returns the same rows.
// Besides avoiding needless work, this prevents the browser from re-anchoring scrollable chat
// panels while the Agent is sending frequent status heartbeats.
const sameRows = (left, right) => JSON.stringify(left) === JSON.stringify(right)

function Messages({
  selected,
  subscribe,
  showToast,
  instances,
  cards,
  devices,
  setSelected,
  callCoordinator,
}) {
  const { t: tr } = useI18n()
  const id = selected?.id
  const [threads, setThreads] = useState([])
  const [threadsLoading, setThreadsLoading] = useState(false)
  const [threadError,setThreadError] = useState('')
  const [messageError,setMessageError] = useState('')
  const failedReads = useRef(new Map())
  const [historyScope, setHistoryScope] = useState('all')
  const [conversation, setConversation] = useState(null)
  const peer = conversation?.peer || null
  const [msgs, setMsgs] = useState([])
  const [messagesLoading, setMessagesLoading] = useState(false)
  const [olderLoading, setOlderLoading] = useState(false)
  const [nextBefore, setNextBefore] = useState('')
  const [text, setText] = useState('')
  const [newTo, setNewTo] = useState('')
  const [transport, setTransport] = useState('')
  const [sending, setSending] = useState(false)
  const [selMode, setSelMode] = useState(false)
  const [selIds, setSelIds] = useState(() => new Set())
  const activeId = useRef(id)
  const activeConversation = useRef(conversation)
  const threadsRequest = useRef(0)
  const messagesRequest = useRef(0)
  const sendingRef = useRef(false)
  const threadPending = useRef(new Map())
  const messagePending = useRef(new Map())
  const loadedCursors = useRef([''])
  const toastRef = useRef(showToast)
  toastRef.current = showToast
  activeId.current = id
  activeConversation.current = conversation
  const historyLineID = historyScope === 'all' ? '' : String(id || '')
  const senderLine = conversation
    ? instances.find(line => String(line.id) === conversation.line_id) : selected
  const senderID = senderLine?.id
  const sendTransport = conversation?.transport || transport
  const cellularAvailable = senderLine?.operations?.cellular_sms?.ready === true
  const cellularPreferred = cellularAvailable && senderLine?.operations?.vowifi_sms?.ready !== true

  const loadThreads = useCallback(async (showLoading = false) => {
    const failureKey = `threads:${historyLineID}`
    if (!showLoading && Date.now() < (failedReads.current.get(failureKey)?.at || 0)) return
    const request = ++threadsRequest.current
    if (showLoading) setThreadsLoading(true)
    let pending = threadPending.current.get(historyLineID)
    if (!pending) { pending = api.threads(historyLineID); threadPending.current.set(historyLineID, pending) }
    try {
      const result = await pending
      if (request === threadsRequest.current) {
        setThreads(current => sameRows(current, result.threads || []) ? current : result.threads || [])
        setThreadError('');failedReads.current.delete(failureKey)
      }
    } catch (error) {
      if (request === threadsRequest.current) {
        const delay=Math.min(240000,(failedReads.current.get(failureKey)?.delay || 15000)*2)
        failedReads.current.set(failureKey,{delay,at:Date.now()+delay})
        setThreadError([error.code,error.message].filter(Boolean).join(' · '))
      }
    }
    finally {
      if (threadPending.current.get(historyLineID) === pending) threadPending.current.delete(historyLineID)
      if (request === threadsRequest.current) setThreadsLoading(false)
    }
  }, [historyLineID])

  const loadMsgs = useCallback(async (_peer, showLoading = false, older = '') => {
    const current = activeConversation.current
    if (!current) return
    const key = current.key
    if (!showLoading && Date.now() < (failedReads.current.get(key)?.at || 0)) return
    if (messagePending.current.get(key)?.request === messagesRequest.current) return
    const request = ++messagesRequest.current
    const cursors = older ? [...loadedCursors.current, older] : [...loadedCursors.current]
    const uniqueCursors = [...new Set(cursors)]
    if (showLoading) setMessagesLoading(true)
    if (older) setOlderLoading(true)
    const pending = Promise.all(uniqueCursors.map(before => api.messages(current.line_id, current.peer, current.transport, before)))
    messagePending.current.set(key, { request, promise:pending })
    try {
      const pages = await pending
      if (request !== messagesRequest.current || activeConversation.current?.key !== key) return
      loadedCursors.current = uniqueCursors
      const next = mergeMessagePages(pages)
      setMsgs(previous => sameRows(previous, next) ? previous : next)
      setNextBefore(pages[pages.length - 1]?.next_before || '')
      setMessageError('');failedReads.current.delete(key)
    } catch (error) {
      if (request === messagesRequest.current) {
        const delay=Math.min(240000,(failedReads.current.get(key)?.delay || 15000)*2)
        failedReads.current.set(key,{delay,at:Date.now()+delay})
        setMessageError([error.code,error.message].filter(Boolean).join(' · '))
      }
    } finally {
      if (messagePending.current.get(key)?.promise === pending) messagePending.current.delete(key)
      if (request === messagesRequest.current) { setMessagesLoading(false); setOlderLoading(false) }
    }
  }, [])
  useEffect(() => {
    ++threadsRequest.current
    setThreads([]);setThreadError('')
    void loadThreads(true)
  }, [loadThreads])
  useEffect(() => {
    setConversation(null); setText(''); setNewTo(''); setTransport('')
    try {
      const saved = JSON.parse(localStorage.getItem(`mdd_sms_operation_${id}`) || 'null')
      if (saved?.id && saved.payload) {
        setText(saved.payload.body || ''); setNewTo(saved.payload.to || '')
        setTransport(['vowifi', 'cellular'].includes(saved.payload.transport) ? saved.payload.transport : '')
      }
    } catch {}
  }, [id])
  useEffect(() => {
    ++messagesRequest.current
    loadedCursors.current = ['']
    setMsgs([]); setNextBefore(''); setOlderLoading(false);setMessageError('')
    setMessagesLoading(Boolean(conversation)); setSelMode(false); setSelIds(new Set())
    if (conversation) void loadMsgs(undefined, true)
  }, [conversation?.key, loadMsgs])
  useEffect(() => {
    if (!msgs.length) { setSelMode(false); setSelIds(new Set()) }
  }, [msgs.length])
  useEffect(() => subscribe(message => {
    if (message.type === 'go.snapshot' || message.type === 'sms') {
      void loadThreads()
      void loadMsgs()
    }
  }), [subscribe, loadThreads, loadMsgs])
  const send = async () => {
    // React state is updated asynchronously, so `sending` alone leaves a short window where
    // a double click or a repeating Enter key can submit the same billable SMS twice.
    if (sendingRef.current) return
    const to = peer || newTo
    if (!to || !text || !sendTransport || !senderID) return
    const forId = senderID
    const composeKey = activeConversation.current?.key || ''
    const selectedID = activeId.current
    const operationKey = `mdd_sms_operation_${forId}`
    const payload = { to, body: text, transport:sendTransport, cardID: String(senderLine?.iccid || senderLine?.card_id || '') }
    if (!payload.cardID) { showToast?.(tr('The exact SIM identity is unavailable.')); return }
    let operationId = ''
    try {
      const saved = JSON.parse(localStorage.getItem(operationKey) || 'null')
      if (saved) {
        if (JSON.stringify(saved.payload) !== JSON.stringify(payload)) {
          if (window.confirm(tr('Discard only this browser retry identity? This cannot retract a message that may already have been submitted.'))) {
            localStorage.removeItem(operationKey)
          }
          return
        }
        operationId = saved.id || ''
      }
    } catch { showToast?.(tr('Cannot preserve the SMS request identity.')); return }
    if (!operationId) operationId = crypto.randomUUID()
    try { localStorage.setItem(operationKey, JSON.stringify({ id: operationId, payload })) }
    catch { showToast?.(tr('Cannot preserve the SMS request identity.')); return }
    sendingRef.current = true
    setSending(true)
    try {
      await api.sendSms(forId, to, text, payload.transport, operationId, payload.cardID)
      try { localStorage.removeItem(operationKey) } catch {}
      // A slow modem submit may finish after the operator selected another line. Never erase
      // that line's draft or replace its open conversation with the old line's recipient.
      if (activeId.current === selectedID && (activeConversation.current?.key || '') === composeKey) {
        setText(''); setNewTo('')
        setConversation({ key:JSON.stringify([String(forId), payload.transport, to]), line_id:String(forId), transport:payload.transport, peer:to })
        await loadThreads(); await loadMsgs()
      }
      showToast?.(tr('Server accepted the message'))
    } catch (e) {
      const msg = 'SMS failed: ' + e.message + '. ' + tr('Retry uses the same request identity; do not create a second send.')
      showToast ? showToast(msg) : alert(msg)
    } finally {
      sendingRef.current = false
      setSending(false)
    }
  }

  const toast = (m) => (showToast ? showToast(m) : null)

  const toggleSel = (mid) => setSelIds((s) => {
    const n = new Set(s); n.has(mid) ? n.delete(mid) : n.add(mid); return n
  })
  const removeHistory = async (target, request) => {
    if (!target?.line_id) return
    try {
      await api.deleteMessages(target.line_id, { ...request, transport:target.transport })
      if (activeConversation.current?.key === target.key) {
        setSelMode(false); setSelIds(new Set())
        if (request.peer !== undefined) { setConversation(null); setMsgs([]) }
        else {
          setMsgs(current => current.filter(row => !request.ids.includes(row.id)))
          await messagePending.current.get(target.key)?.promise.catch(() => {})
          await loadMsgs()
        }
      }
      await loadThreads()
    } catch (error) { toast(error.message) }
  }
  const deleteSelected = () => {
    if (!conversation || !selIds.size) return
    if (window.confirm(tr('Delete selected messages?'))) void removeHistory(conversation, { ids:[...selIds] })
  }
  const deleteThread = (target, event) => {
    event?.stopPropagation()
    if (!target) return
    if (window.confirm(tr('Delete the entire conversation with {peer}?', { peer:target.peer }))) void removeHistory(target, { peer:target.peer })
  }
  const clearAll = async () => {
    if (historyScope !== 'line' || !id) return
    if (!window.confirm(tr('Delete ALL messages on this line? This cannot be undone.'))) return
    const forId = id
    try {
      await api.deleteMessages(forId, { all:true })
      if (String(activeConversation.current?.line_id) === String(forId)) { setConversation(null); setMsgs([]) }
      await loadThreads()
    } catch (error) { toast(error.message) }
  }
  return (
    <div style={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      <div style={{ flexShrink: 0 }}>
        <SimSelector instances={instances} cards={cards} devices={devices} selected={selected}
          setSelected={setSelected} callCoordinator={callCoordinator}
          showVoiceReadiness />
      </div>
      {senderID && <AllowancePanel instanceId={String(senderID)} mode="messages" transport={sendTransport} showToast={showToast} />}
      {cellularPreferred && <div className="u-note" style={{ marginBottom: 12 }}>{tr('Cellular SMS is ready. VoWiFi may remain stopped because the host operating system owns this SIM.')}</div>}
      <div className="mdd-message-layout">
      <div className="card" style={{ padding: 12, overflow: 'auto', minHeight: 0 }}>
        <button className="btn btn-primary" style={{ width: '100%', marginBottom: 8 }} onClick={() => { setConversation(null); setMsgs([]); setMessagesLoading(false); setText('') }}>+ {tr('New message')}</button>
        <select aria-label={tr('Message history scope')} value={historyScope} onChange={event => { setHistoryScope(event.target.value); setConversation(null) }}>
          <option value="all">{tr('All lines')}</option><option value="line" disabled={!id}>{tr('Selected line')}</option>
        </select>
        {threads.length > 0 && historyScope === 'line' &&
          <button className="btn btn-ghost" style={{ width: '100%', marginBottom: 10, color: '#ef4444', fontSize: 12 }}
            onClick={clearAll}>{tr('Clear all conversations')}</button>}
        {threads.map((t) => (
          <div key={t.key} onClick={() => { if (conversation?.key !== t.key) { setConversation(t); setText('') } }} className="hover-row"
            style={{ padding: 10, borderRadius: 10, cursor: 'pointer', marginBottom: 4, display: 'flex', alignItems: 'center', gap: 8,
              background: conversation?.key === t.key ? 'var(--active)' : 'transparent' }}>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ fontWeight: 600, fontSize: 14 }} className="mono">{t.peer}</div>
              <div style={{ fontSize:12 }}>{instances.find(line => String(line.id) === t.line_id)?.name || t.line_id} · {t.transport}</div>
              <div style={{ fontSize: 12, color: 'var(--text-mute)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{t.last_body}</div>
            </div>
            <button className="row-del" title="Delete conversation" aria-label={`Delete conversation with ${t.peer}`}
              onClick={(e) => deleteThread(t, e)}>🗑</button>
          </div>
        ))}
        {threadsLoading && <div aria-live="polite" style={{ color: 'var(--text-mute)', fontSize: 13, padding: 8 }}>{tr('Loading conversations…')}</div>}
        {threadError && <div role="alert" className="u-error">{threadError}<button className="btn btn-ghost" disabled={threadsLoading} onClick={() => loadThreads(true)}>{tr('Retry')}</button></div>}
        {!threadsLoading && !threadError && threads.length === 0 && <div style={{ color: 'var(--text-mute)', fontSize: 13, padding: 8 }}>{tr('No conversations yet.')}</div>}
      </div>

      <div className="card" style={{ display: 'flex', flexDirection: 'column', padding: 0, minHeight: 0 }}>
        <div style={{ padding: 14, borderBottom: '1px solid var(--border)', display: 'flex', alignItems: 'center', gap: 10, flexShrink: 0 }}>
          {peer ? <span className="mono" style={{ fontWeight: 600, flex: 1 }}>{peer}<small style={{ display:'block' }}>{senderLine?.name || conversation.line_id} · {conversation.transport}</small></span>
            : <input placeholder={tr('Recipient number e.g. +1...')} value={newTo} onChange={(e) => setNewTo(e.target.value)} style={{ maxWidth: 300, flex: 1 }} />}
          {peer && msgs.length > 0 && (
            selMode ? (
              <>
                <span style={{ fontSize: 12, color: 'var(--text-mute)' }}>{selIds.size} {tr('selected')}</span>
                <button className="btn btn-ghost" style={{ padding: '4px 10px', fontSize: 12, color: '#ef4444' }}
                  disabled={!selIds.size} onClick={deleteSelected}>{tr('Delete')}</button>
                <button className="btn btn-ghost" style={{ padding: '4px 10px', fontSize: 12 }}
                  onClick={() => { setSelMode(false); setSelIds(new Set()) }}>{tr('Cancel')}</button>
              </>
            ) : (
              <>
                <button className="btn btn-ghost" style={{ padding: '4px 10px', fontSize: 12 }}
                  onClick={() => setSelMode(true)}>{tr('Select')}</button>
                <button className="btn btn-ghost" title="Delete conversation" style={{ padding: '4px 10px', fontSize: 12, color: '#ef4444' }}
                  onClick={() => deleteThread(conversation)}>{tr('Delete all')}</button>
              </>
            )
          )}
        </div>
        <div style={{ flex: 1, minHeight: 0, overflow: 'auto', padding: 16, display: 'flex', flexDirection: 'column', gap: 8 }}>
          {nextBefore && <button className="btn btn-ghost" disabled={olderLoading || messagesLoading} onClick={() => loadMsgs(undefined, false, nextBefore)}>{tr(olderLoading ? 'Loading messages…' : 'Older messages')}</button>}
          {messagesLoading && <div aria-live="polite" style={{ color: 'var(--text-mute)', fontSize: 13 }}>{tr('Loading messages…')}</div>}
          {messageError && <div role="alert" className="u-error">{messageError}<button className="btn btn-ghost" disabled={messagesLoading} onClick={() => loadMsgs(undefined,true)}>{tr('Retry')}</button></div>}
          {!messagesLoading && !messageError && peer && msgs.length === 0 && <div style={{ color: 'var(--text-mute)', fontSize: 13 }}>{tr('No messages in this conversation.')}</div>}
          {msgs.map((m) => {
            const failed = m.status === 'failed'
            // Outbound delivery lifecycle: pending -> sent (IMS accepted) -> delivered | failed.
            // 'delivered' is confirmed by the network's SMS submit report; 'sent' means accepted
            // but delivery not yet confirmed.
            const delivered = m.status === 'delivered'
            const sent = m.status === 'sent'
            const uncertain = m.status === 'unknown'
            const statusText = failed ? ` · ${tr('Failed to deliver')}`
              : m.status === 'pending' ? ` · ${tr('sending…')}`
              : sent ? ` · ${tr('Sent')}`
              : delivered ? ` · ${tr('Delivered ✓')}`
              : uncertain ? ` · ${tr('Delivery unknown')}`
              : ''
            const statusColor = failed ? '#ef4444' : uncertain ? '#f59e0b' : delivered ? '#22c55e' : 'var(--text-mute)'
            const checked = selIds.has(m.id)
            return (
              <div key={m.id} onClick={() => selMode && toggleSel(m.id)}
                style={{ alignSelf: m.direction === 'out' ? 'flex-end' : 'flex-start', maxWidth: '74%',
                  cursor: selMode ? 'pointer' : 'default', display: 'flex', alignItems: 'center', gap: 8,
                  flexDirection: m.direction === 'out' ? 'row-reverse' : 'row' }}>
                {selMode && <input type="checkbox" readOnly checked={checked} style={{ width: 'auto', flexShrink: 0 }} />}
                <div style={{ minWidth: 0 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6,
                    flexDirection: m.direction === 'out' ? 'row' : 'row-reverse' }}>
                    {failed && <span title={m.error || 'Delivery failed'}
                      style={{ color: '#ef4444', fontWeight: 800, cursor: 'help', fontSize: 15 }}>❗</span>}
                    {uncertain && <span title={m.error || tr('Delivery unknown')}
                      style={{ color: '#f59e0b', fontWeight: 800, cursor: 'help', fontSize: 15 }}>⚠</span>}
                    <div style={{
                      background: checked ? 'var(--active)' : failed ? 'rgba(239,68,68,.15)' : uncertain ? 'rgba(245,158,11,.14)' : (m.direction === 'out' ? 'var(--primary)' : 'var(--hover)'),
                      border: failed ? '1px solid rgba(239,68,68,.55)' : uncertain ? '1px solid rgba(245,158,11,.55)' : '1px solid transparent',
                      padding: '8px 12px', borderRadius: 12, fontSize: 14, whiteSpace:'pre-wrap', overflowWrap:'anywhere',
                    }}>{m.body}</div>
                  </div>
                  <div style={{ fontSize: 10, color: statusColor,
                    textAlign: m.direction === 'out' ? 'right' : 'left', marginTop: 2 }}>
                    {new Date(m.ts * 1000).toLocaleString()}
                    {m.transport === 'cellular' ? ` · ${tr('4G SMS')}` : ''}
                    {m.part > 0 ? ` · ${tr('Part {part}', {part:m.part})}` : ''}
                    {statusText}
                  </div>
                  {failed && m.error && (
                    <div style={{ fontSize: 10.5, color: '#ef4444', marginTop: 1,
                      textAlign: m.direction === 'out' ? 'right' : 'left', maxWidth: 280 }}>{m.error}</div>
                  )}
                  {uncertain && m.error && (
                    <div style={{ fontSize: 10.5, color: '#f59e0b', marginTop: 1,
                      textAlign: m.direction === 'out' ? 'right' : 'left', maxWidth: 280 }}>{m.error}</div>
                  )}
                </div>
              </div>
            )
          })}
        </div>
        <div style={{ display: 'flex', gap: 8, padding: 12, borderTop: '1px solid var(--border)', flexShrink: 0, flexWrap: 'wrap', alignItems: 'center' }}>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12, color: 'var(--text-mute)', whiteSpace: 'nowrap' }}>
            {tr('Send via')}
            <select value={sendTransport} disabled={sending || Boolean(conversation)}
              onChange={(e) => setTransport(e.target.value)}
              aria-label={tr('Send via')}
              title={!cellularAvailable ? tr('This line does not have an available cellular modem.') : ''}
              style={{ width: 'auto', minWidth: 150 }}>
              <option value="" disabled>{tr('Send via')}</option>
              <option value="vowifi">VoWiFi</option>
              <option value="cellular" disabled={!cellularAvailable}>
                {tr('Cellular network (Modem)')}{!cellularAvailable ? ` — ${tr('Unavailable')}` : ''}
              </option>
            </select>
          </label>
          <input placeholder={tr('Type a message…')} value={text} disabled={sending}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key !== 'Enter') return
              e.preventDefault()
              if (!e.repeat) send()
            }} style={{ flex: '1 1 220px' }} />
          <button className="btn btn-primary" disabled={sending || !senderID || !sendTransport || !text.trim() || (!peer && !newTo)} onClick={send}>{tr('Send')}</button>
        </div>
      </div>
      </div>
    </div>
  )
}

// The application receives live line/modem status objects several times per second. Most of
// those objects differ only in diagnostics/timestamps that this page never renders. Prevent
// those parent updates from repainting every select option and chat bubble; SMS WebSocket
// events still update the component through its own subscription above.
const visibleProps = (props) => ({
  selected: String(props.selected?.id || props.selected || ''),
  instances: (props.instances || []).map((item) => ({
    id: String(item.id || ''), name: item.name || '', carrier: item.carrier || '',
    profile_name: item.profile_name || '', mcc: item.mcc || '', mnc: item.mnc || '',
    msisdn: item.msisdn || '', iccid: item.iccid || '', reader: item.reader || '',
    reader_name: item.reader_name || '', reader_index: item.reader_index ?? null,
    status_state: item.status?.state ?? null,
    status_label: item.status?.label || '',
  })),
  cards: (props.cards || []).map((item) => ({
    index: item.index ?? null, vpcd_slot: item.vpcd_slot ?? null,
    name: item.name || '', present: item.present !== false, matched: String(item.matched || ''),
    iccid: item.iccid || '', imsi: item.imsi || '', spn: item.spn || '',
    profile_name: item.profile_name || '', carrier: item.carrier || '',
    mcc: item.mcc || '', mnc: item.mnc || '',
  })),
  devices: (props.devices || []).map((item) => ({
    id: String(item.id || ''), present: item.present !== false,
    device_type: item.device_type || '', instance_id: String(item.instance_id || ''),
    remote_modem: Boolean(item.remote_modem), name: item.name || '', reader: item.reader || '',
    sim_name: item.sim?.name || '', sim_number: item.sim?.number || '',
    sms_actual: item.capabilities?.sms?.actual || '',
    sms_available: item.capabilities?.sms?.available,
    cellular_actual: item.capabilities?.cellular?.actual || '',
    cellular_registration: item.cellular?.registration || '',
    cellular_data_active: Boolean(item.cellular?.data_active),
    vowifi_actual: item.capabilities?.vowifi?.actual || '',
  })),
  callLines: Object.fromEntries(Object.entries(props.callCoordinator?.lines || {}).map(([id, line]) => [id, {
    reg: line.reg || '',
    native_outbound: line.prov?.browser_media?.outbound === true,
    prov_generation: line.prov?.generation || '',
    mediaTest: line.mediaTest || '',
    retryExhausted: line.retryExhausted === true,
  }])),
})

const sameVisibleProps = (previous, next) =>
  previous.subscribe === next.subscribe && previous.showToast === next.showToast &&
  previous.setSelected === next.setSelected &&
  JSON.stringify(visibleProps(previous)) === JSON.stringify(visibleProps(next))

export default React.memo(Messages, sameVisibleProps)
