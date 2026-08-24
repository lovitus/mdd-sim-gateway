const TERMINAL_STATUSES = new Set([
  'answered', 'ended', 'missed', 'failed', 'rejected', 'cancelled',
  'canceled', 'busy', 'no answer', 'no_answer',
])

export function shouldSurfaceIncomingSyncFailure(failures, retryCount = 3) {
  return Number(failures) === Number(retryCount) + 1
}

export function incomingReconcileActive(mounted, enabled, instanceIds, key) {
  return Boolean(mounted && enabled && (instanceIds || []).includes(String(key || '')))
}

export function backendCallIdentity(call) {
  const backendCallId = call?.id === undefined || call?.id === null ? '' : String(call.id)
  const sourceCallId = String(call?.source_call_id || '')
  const engineRunId = String(call?.engine_run_id || '')
  const prefix = `${engineRunId}:`
  const linkedid = sourceCallId.startsWith(prefix) ? sourceCallId.slice(prefix.length) : ''
  if (!backendCallId || !sourceCallId ||
      !/^[A-Za-z0-9_.:-]{1,128}$/.test(engineRunId) ||
      !/^[A-Za-z0-9_.-]{1,160}$/.test(linkedid)) return null
  return `${backendCallId}:${engineRunId}:${sourceCallId}`
}

export function backendPresentationIdentity(call) {
  if (call?.id === undefined || call?.id === null) return null
  return `diagnostic:${String(call.id)}:${String(call?.engine_run_id || '')}:${String(call?.source_call_id || '')}`
}

export function isTerminalBackendCall(call) {
  if (!call || call.end_ts !== null && call.end_ts !== undefined) return true
  return TERMINAL_STATUSES.has(String(call.status || '').toLowerCase())
}

export function sameBackendCall(uiCall, backendCall) {
  if (!uiCall || uiCall.source !== 'backend') return false
  const identity = backendCallIdentity(backendCall)
  return Boolean(identity &&
    String(uiCall.backendCallId || '') === String(backendCall?.id ?? '') &&
    String(uiCall.engineRunId || '') === String(backendCall?.engine_run_id || '') &&
    String(uiCall.sourceCallId || '') === String(backendCall?.source_call_id || ''))
}

export function sameBackendPresentationCall(uiCall, backendCall) {
  return Boolean(uiCall && uiCall.source === 'backend' &&
    String(uiCall.backendCallId || '') === String(backendCall?.id ?? '') &&
    String(uiCall.engineRunId || '') === String(backendCall?.engine_run_id || '') &&
    String(uiCall.sourceCallId || '') === String(backendCall?.source_call_id || ''))
}

export function shouldShowBackendFallback(
  currentCall, backendCall, terminalKeys = new Set(), authoritative = false,
) {
  const identity = backendCallIdentity(backendCall)
  const diagnosticIdentity = backendPresentationIdentity(backendCall)
  if ((!identity && !diagnosticIdentity) || isTerminalBackendCall(backendCall) ||
      (identity && terminalKeys.has(identity)) ||
      (!identity && terminalKeys.has(diagnosticIdentity))) return false
  if (!currentCall || currentCall.state === 'ended') return true
  if (currentCall.source === 'backend') {
    return authoritative || (identity ? sameBackendCall(currentCall, backendCall)
      : sameBackendPresentationCall(currentCall, backendCall))
  }
  return false
}

export function backendFallbackCall(instanceId, backendCall) {
  const exact = Boolean(backendCallIdentity(backendCall))
  return {
    dir: 'in',
    number: backendCall?.peer || backendCall?.number || 'Unknown',
    state: 'incoming',
    transport: 'vowifi',
    source: 'backend',
    answerable: false,
    instanceId: String(instanceId || ''),
    backendCallId: String(backendCall?.id ?? ''),
    engineRunId: String(backendCall?.engine_run_id || ''),
    sourceCallId: String(backendCall?.source_call_id || ''),
    exactIdentity: exact,
    reason: exact ? 'browser_softphone_unregistered_or_media_unconfirmed'
      : 'missing_exact_call_identity',
  }
}

export function selectIncomingOverlayEntry(lines) {
  const incoming = Object.entries(lines || {}).filter(([, line]) =>
    ['incoming', 'ending', 'termination_unconfirmed'].includes(line.call?.state) &&
    line.call?.transport === 'vowifi')
  return incoming.find(([, line]) =>
    line.call?.answerable !== false && line.call?.source === 'jssip') ||
    incoming[0] || null
}
