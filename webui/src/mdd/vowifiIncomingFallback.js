const TERMINAL_STATUSES = new Set([
  'answered', 'ended', 'missed', 'failed', 'rejected', 'cancelled',
  'canceled', 'busy', 'no answer', 'no_answer',
])

export function shouldSurfaceIncomingSyncFailure(failures, retryCount = 3) {
  return Number(failures) === Number(retryCount) + 1
}

export function incomingSyncWarningExpected(instance, line, error = null) {
  const call = line?.call
  if (call?.transport === 'vowifi' && call?.state && call.state !== 'ended') return true
  if (error && !Number(error?.status)) return false
  if (!instance) return false
  if (instance?.enabled === false) return false
  const state = String(instance?.status?.state || '').toUpperCase()
  return state === 'OK'
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
  if (call.browser_state !== null && call.browser_state !== undefined)
    return String(call.browser_state) === 'terminal'
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
  const browserState = String(backendCall?.browser_state || '')
  const state = browserState === 'active' ? 'active_elsewhere'
    : ['claiming', 'attach_submitted_unknown', 'answer_submitted_unknown'].includes(browserState)
      ? 'answering_elsewhere'
      : browserState === 'ending' ? 'ending'
        : browserState === 'unknown' ? 'termination_unconfirmed' : 'incoming'
  return {
    dir: 'in',
    number: backendCall?.peer || backendCall?.number || 'Unknown',
    state,
    transport: 'vowifi',
    source: 'backend',
    answerable: false,
    instanceId: String(instanceId || ''),
    backendCallId: String(backendCall?.id ?? ''),
    engineRunId: String(backendCall?.engine_run_id || ''),
    sourceCallId: String(backendCall?.source_call_id || ''),
    browserRevision: Number(backendCall?.browser_revision),
    browserOwnerSession: String(backendCall?.browser_owner_session || ''),
    browserOperation: String(backendCall?.browser_operation || ''),
    browserEpoch: String(backendCall?.browser_epoch || ''),
    exactIdentity: exact,
    backendState: browserState,
    reason: exact ? (browserState === 'unknown' ? 'browser_call_recovery_required'
      : state.endsWith('_elsewhere') ? 'browser_call_owned_elsewhere'
        : 'browser_audio_unavailable')
      : 'missing_exact_call_identity',
  }
}

export function nativeIncomingCall(instanceId, backendCall, state = 'preparing', patch = {}) {
  return {
    ...backendFallbackCall(instanceId, backendCall),
    state,
    source: 'native-wss-incoming',
    localOwner: true,
    answerable: state === 'incoming',
    reason: '',
    ...patch,
  }
}

export function boundedIdentityMapSet(map, identity, value = true, limit = 256) {
  if (!(map instanceof Map) || !identity) return map
  map.delete(identity)
  map.set(identity, value)
  while (map.size > limit) map.delete(map.keys().next().value)
  return map
}

export function stopNativeCall(call) {
  if (!call) return 'missing'
  const ownerPhase = ['claiming', 'attach_submitted_unknown',
    'answer_submitted_unknown', 'active'].includes(call.callPhase)
  if (call.direction === 'inbound' && !call.answerSent && !ownerPhase) {
    call.closeLocal()
    return 'local'
  }
  call.hangup()
  return 'hangup'
}

export function nativeCallbackCurrent(calls, identities, key, call, identity) {
  return calls?.get(String(key || '')) === call &&
    identities?.get(String(key || '')) === identity
}

export function nativeDeclineEligible(call) {
  return Boolean(call?.source === 'native-wss-incoming' && call?.localOwner === true &&
    ['ringing', 'claiming', 'attach_submitted_unknown'].includes(
      String(call?.backendState || 'ringing')) &&
    !['active', 'active_elsewhere', 'ending', 'termination_unconfirmed'].includes(call?.state))
}

export function routeNativeHangup(call, exactHangup) {
  if (call?.hangup?.()) return { route: 'wss', result: true }
  return { route: 'exact', result: exactHangup?.() }
}

export function selectIncomingOverlayEntry(lines) {
  const incoming = Object.entries(lines || {}).filter(([, line]) =>
    ['preparing', 'needs-user-gesture', 'incoming', 'answering', 'answering_elsewhere',
      'active_elsewhere', 'ending', 'termination_unconfirmed'].includes(line.call?.state) &&
    line.call?.transport === 'vowifi')
  return incoming.find(([, line]) =>
    line.call?.answerable !== false &&
      line.call?.source === 'native-wss-incoming') ||
    incoming[0] || null
}
