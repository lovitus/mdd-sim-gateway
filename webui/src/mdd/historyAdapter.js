import { api as go } from '../api.js'
import { recordedUnixSeconds } from '../goV1Adapter.js'

// Shape adapters only: persistence, deletion fences and submission ownership
// remain in the Go APIs. No legacy HTTP endpoint is called here.
export function messageRows(records) {
  const reports = new Map()
  const reportKey = item => JSON.stringify([item.line_id, item.transport, item.message_id, item.part || 0])
  for (const item of records) if (item.kind === 'delivery' && item.message_id) {
    const prior = reports.get(reportKey(item))
    if (!prior || recordedUnixSeconds(item.received_at) >= recordedUnixSeconds(prior.received_at)) reports.set(reportKey(item), item)
  }
  return records.filter(item => item.kind !== 'delivery').map(item => {
    const report = reports.get(reportKey(item))
    const status = report?.state || item.state || (item.kind === 'received' ? 'received' : item.kind === 'submitted' ? 'submitted' : 'unknown')
    return { ...item, id: item.event_id, instance: item.line_id,
      direction: item.kind === 'received' ? 'in' : 'out',
      peer: item.sender || item.recipient || '', body: item.body || '',
      status: status === 'submitted' ? 'sent' : status,
      ts: recordedUnixSeconds(item.observed_at || item.received_at),
      error: report?.error || item.error || item.error_code || '',
    }
  }).sort((left, right) => left.ts - right.ts || String(left.id).localeCompare(String(right.id)))
}

export function conversationRows(conversations, lineID = '') {
  return conversations.filter(item => !lineID || String(item.line_id) === String(lineID)).map(item => ({
    key: JSON.stringify([String(item.line_id), item.transport, item.peer]),
    line_id:String(item.line_id), transport:item.transport, peer:item.peer,
    n:item.count, last_ts:recordedUnixSeconds(item.last?.received_at), last_body:item.last?.body || '',
  })).sort((left, right) => right.last_ts - left.last_ts || left.key.localeCompare(right.key))
}

export function mergeMessagePages(pages) {
  const records = new Map()
  for (const page of pages) for (const record of page.messages || []) records.set(record.event_id, record)
  return messageRows([...records.values()])
}

export function diagnosticLogView(entries) {
  const format = item => [item.received_at || '',item.source || '',item.layer || '',item.condition || '',item.code || '',item.detail || ''].filter(Boolean).join(' ')
  return Object.fromEntries(['all','agent','provider','core'].map(source => [source,
    entries.filter(item => source === 'all' || item.source === source).map(format).join('\n')]))
}

export const historyAPI = {
  async threads(lineID) {
    const result = await go.allMessageConversationsV1()
    return { threads: conversationRows(result.conversations || [], lineID) }
  },
  messages(lineID, peer, transport, before = '') {
    if (!lineID || !['cellular', 'vowifi'].includes(transport)) throw new Error('message_history_scope_required')
    return go.messagePageV1(lineID, transport, peer, before)
  },
  deleteMessages(lineID, request) {
    if (!lineID) throw new Error('message_history_line_required')
    return go.deleteMessageHistoryV1({ line_id: String(lineID),
      ...(request.transport ? { transport:request.transport } : {}),
      ...(request.all ? { all:true } : request.peer !== undefined ? {peer:request.peer} : {event_ids:request.ids || []}) })
  },
  async calls(lineID) {
    const result = await go.callHistoryV1(lineID)
    return { calls: (result.calls || []).map(item => ({ ...item,
      start_ts: recordedUnixSeconds(item.started_at), end_ts: recordedUnixSeconds(item.ended_at),
      duration: item.answered_at && item.ended_at ? Math.max(0,(Date.parse(item.ended_at)-Date.parse(item.answered_at))/1000) : 0,
    })) }
  },
  async deleteCalls(lineID, request) {
    if (request.all) return go.clearCallHistoryV1(lineID)
    const result = await go.callHistoryV1(lineID)
    const allowed = new Set((result.calls || []).filter(item => item.ended_at).map(item => item.id))
    const ids = request.ids || []
    if (ids.some(id => !allowed.has(id))) throw new Error('call_history_selection_changed')
    return go.deleteCallHistoryV1(ids)
  },
  async logs(lineID, limit = 200) {
    const result = await go.lineDiagnosticLogs(lineID, Math.min(500,limit))
    return diagnosticLogView(result.entries || [])
  },
}
