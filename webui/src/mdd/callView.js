// Presentation only. The Go coordinator retains call identity, media and teardown ownership.
export function originalCallView(call) {
  if (!call) return null
  const state = { preparing:'checking', ready:'checking', signalling:'calling',
    start_unknown:'start_unknown', active:'active', media_failed:'media_failed',
    ending:'ending', ended:'ended' }[call.phase] || 'start_unknown'
  return { state, number:call.callee, transport:call.mode,
    dir:call.direction === 'incoming' ? 'in' : 'out', startedAt:call.started_at,
    muted:call.muted === true, message:call.message || '', mediaPhase:call.media_state }
}
