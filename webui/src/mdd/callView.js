// Presentation only. The Go coordinator retains call identity, media and teardown ownership.
export function historyCallDraft(record, lines) {
  const line = lines.find(item => String(item.id) === String(record.line_id))
  if (!line || !['cellular', 'vowifi'].includes(record.transport) || !record.peer) return null
  return { lineID:String(line.id), transport:record.transport, number:record.peer }
}

export function originalCallView(call) {
  if (!call) return null
  const state = { preparing:'checking', ready:'checking', signalling:'calling',
    start_unknown:'start_unknown', active:'active', media_failed:'media_failed',
    ending:'ending', ended:'ended' }[call.phase] || 'start_unknown'
  return { state, number:call.callee, transport:call.mode,
    dir:call.direction === 'incoming' ? 'in' : 'out', startedAt:call.started_at,
    muted:call.muted === true, message:call.message || '', mediaPhase:call.media_state }
}
