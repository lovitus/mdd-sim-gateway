// Thin REST + WebSocket client for the manager API (same origin).
export function getBasePrefix() {
  const pathname = window.location.pathname || '/'
  if (pathname === '/mdd' || pathname.startsWith('/mdd/')) {
    return '/mdd'
  }
  return ''
}

const base = getBasePrefix()
let csrfToken = ''
let authToken = ''

export function setCsrf(token) { csrfToken = token || '' }

export function setAuthToken(token) {
  authToken = token || ''
  if (token) {
    try { sessionStorage.setItem('mdd_token', token) } catch {}
  } else {
    try { sessionStorage.removeItem('mdd_token') } catch {}
  }
}

export function getAuthToken() {
  if (!authToken) {
    try { authToken = sessionStorage.getItem('mdd_token') || '' } catch {}
  }
  return authToken
}

async function j(method, path, body, headers = {}, timeoutMs = 0) {
  const opt = { method, headers: { ...headers }, credentials: 'same-origin' }
  const token = getAuthToken()
  if (token) {
    opt.headers['X-MDD-Session'] = token
    opt.headers['Authorization'] = `Bearer ${token}`
  }
  if (csrfToken && !['GET', 'HEAD', 'OPTIONS'].includes(method)) opt.headers['X-MDD-CSRF-Token'] = csrfToken
  if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body) }
  const controller = timeoutMs ? new AbortController() : null
  if (controller) opt.signal = controller.signal
  const timer = controller ? setTimeout(() => controller.abort(), timeoutMs) : null
  let r, text
  try {
    r = await fetch(base + path, opt)
    text = await r.text()
  } finally { clearTimeout(timer) }
  let data
  try { data = text ? JSON.parse(text) : {} } catch { data = { raw: text } }
  // A non-empty CSRF or auth token means this tab previously had an authenticated session.
  // Sessions are intentionally memory-only and disappear when the control plane restarts;
  // notify the app once so it can stop all polling and return to the login screen.
  if (r.status === 401 && (csrfToken || authToken)) {
    csrfToken = ''
    setAuthToken('')
    window.dispatchEvent(new CustomEvent('mdd-auth-expired'))
  }
  // detail may be a structured dict (e.g. {code, message}); prefer its message so
  // alerts show readable text instead of "[object Object]".
  const detailMsg = data.detail && typeof data.detail === 'object' ? (data.detail.message || data.detail.code) : data.detail
  if (!r.ok) throw Object.assign(new Error(detailMsg || data.error || r.statusText), { status: r.status, data })
  return data
}

/** Build query string. Prefer reader NAME (stable); index is optional fallback. */
function readerQuery(readerOrIndex, maybeName) {
  const q = new URLSearchParams()
  if (typeof readerOrIndex === 'string' && readerOrIndex) {
    q.set('reader', readerOrIndex)
  } else if (typeof readerOrIndex === 'number') {
    q.set('reader_index', String(readerOrIndex))
    if (maybeName) q.set('reader', maybeName)
  } else if (maybeName) {
    q.set('reader', maybeName)
  } else {
    q.set('reader_index', '0')
  }
  return q
}

function readerBody(readerOrIndex, extra = {}) {
  if (typeof readerOrIndex === 'string' && readerOrIndex) {
    return { reader: readerOrIndex, ...extra }
  }
  if (typeof readerOrIndex === 'number') {
    return { reader_index: readerOrIndex, ...extra }
  }
  if (readerOrIndex && typeof readerOrIndex === 'object') {
    return { ...readerOrIndex, ...extra }
  }
  return { reader_index: 0, ...extra }
}

export const api = {
  authStatus: () => j('GET', '/api/auth/status'),
  authSetup: (username, password) => j('POST', '/api/auth/setup', { username, password }),
  authLogin: (username, password) => j('POST', '/api/auth/login', { username, password }),
  authLogout: () => j('POST', '/api/auth/logout', {}),
  authPassword: (current_password, new_password) => j('POST', '/api/auth/password', { current_password, new_password }),
  // Unified physical-device control plane. Older deployments may return 404;
  // App.jsx then derives read-only device cards from /api/instances + /api/cards.
  devices: () => j('GET', '/api/devices'),
  snapshot: () => j('GET', '/api/snapshot'),
  patchDeviceCapabilities: (id, patch) => j('PATCH', `/api/devices/${encodeURIComponent(id)}/capabilities`, patch),
  deviceCellular: (id) => j('GET', `/api/devices/${encodeURIComponent(id)}/cellular`),
  deviceCellularProfiles: (id) => j('GET', `/api/devices/${encodeURIComponent(id)}/cellular/profiles`),
  saveDeviceCellularProfile: (id, profile) => j('PUT', `/api/devices/${encodeURIComponent(id)}/cellular/profile`, profile),
  deviceDiagnostics: (id) => j('POST', `/api/devices/${encodeURIComponent(id)}/diagnostics`, {}),
  refreshDeviceSms: (id) => j('POST', `/api/devices/${encodeURIComponent(id)}/sms/refresh`, {}),
  softRestartDevice: (id) => j('POST', `/api/devices/${encodeURIComponent(id)}/soft-restart`, {}),
  cellularSims: () => j('GET', '/api/cellular-sims'),
  saveDeviceHardware: (id, patch) => j('PUT', `/api/devices/${encodeURIComponent(id)}/hardware`, patch),
  deleteDevice: (id) => j('DELETE', `/api/devices/${encodeURIComponent(id)}`),
  readers: () => j('GET', '/api/readers'),
  detect: (i = 0) => j('GET', `/api/sim/detect?reader_index=${i}`),
  // `reader` (PC/SC reader NAME) lets the backend re-resolve the index at request time —
  // indices shift when another reader is unplugged, and a stale index could address the
  // wrong physical SIM.
  verifyPin: (pin, reader_index = 0, reader) => j('POST', '/api/sim/verify-pin', { pin, reader_index, reader }),
  changePin: (oldp, newp, reader_index = 0) => j('POST', '/api/sim/change-pin', { old: oldp, new: newp, reader_index }),
  setPinEnabled: (pin, enabled, reader_index = 0) => j('POST', '/api/sim/pin-enabled', { pin, enabled, reader_index }),

  settings: () => j('GET', '/api/settings'),
  saveSettings: (patch) => j('PUT', '/api/settings', patch),
  egressStatus: () => j('GET', '/api/egress/status'),
  testEgress: (country) => j('POST', `/api/egress/${encodeURIComponent(country)}/test`, {}),
  testProxyProfile: (profileId, profile) => j('POST', `/api/egress/profile/${encodeURIComponent(profileId)}/test`, profile || {}),
  refreshEgress: () => j('POST', '/api/egress/refresh', {}),
  testWebhook: (config) => j('POST', '/api/notifications/webhook/test', config || {}),
  testTelegram: (config) => j('POST', '/api/notifications/telegram/test', config || {}),
  testPushPlus: (config) => j('POST', '/api/notifications/pushplus/test', config || {}),
  notificationDeliveries: (limit = 100) => j('GET', `/api/notifications/deliveries?limit=${limit}`),
  clearNotificationDeliveries: () => j('DELETE', '/api/notifications/deliveries'),
  systemStatus: () => j('GET', '/api/system/status'),
  clearHostAlerts: () => j('DELETE', '/api/system/host-alerts'),
  agentHealth: () => j('GET', '/api/agents/health'),
  checkUpdate: (force = false) => j('GET', `/api/system/update/check${force ? '?force=true' : ''}`),
  applyUpdate: () => j('POST', '/api/system/update/apply', {}),
  updateProgress: () => j('GET', '/api/system/update/progress'),
  createBackup: () => j('POST', '/api/system/backups', {}),
  supportBundleUrl: '/api/diagnostics/support-bundle',

  imeiPool: () => j('GET', '/api/imei-pool'),
  saveImeiPoolEntry: (entry) => j('POST', '/api/imei-pool', entry),
  deleteImeiPoolEntry: (id) => j('DELETE', `/api/imei-pool/${encodeURIComponent(id)}`),
  bindImeiToIccid: (body) => j('POST', '/api/imei-pool/bind', body),
  unbindImeiFromIccid: (iccid) => j('DELETE', `/api/imei-pool/binding/${encodeURIComponent(iccid)}`),


  instances: (includeDeleted = false) => j('GET', `/api/instances${includeDeleted ? '?include_deleted=true' : ''}`),
  softDeletedInstances: () => j('GET', '/api/instances/soft-deleted'),
  softDeleteInstance: (id) => j('POST', `/api/instances/${id}/soft-delete`),
  restoreInstance: (id) => j('POST', `/api/instances/${id}/restore`),
  cards: () => j('GET', '/api/cards'),
  portsSuggest: () => j('GET', '/api/ports/suggest'),
  provision: (body) => j('POST', '/api/provision', body),
  saveInstance: (inst) => j('POST', '/api/instances', inst),
  setLineCountry: (id, country) => j('PUT', `/api/instances/${id}/country`, { country }),
  deleteInstance: (id, deleteHistory = true) => j('DELETE', `/api/instances/${id}?delete_history=${deleteHistory ? 'true' : 'false'}&confirm_id=${encodeURIComponent(id)}`),
  start: (id, body) => j('POST', `/api/instances/${id}/start`, body || {}),
  stop: (id) => j('POST', `/api/instances/${id}/stop`),
  reprovision: (id, body) => j('POST', `/api/instances/${id}/reprovision`, body || {}),
  clearPin: (id) => j('POST', `/api/instances/${id}/pin/clear`),
  status: (id) => j('GET', `/api/instances/${id}/status`),

  // Recorded VoWiFi up/down timeline; the window follows the accumulated history (max 2 days).
  lineAvailability: (id) => j('GET', `/api/instances/${id}/availability`),
  logs: (id, tail = 300) => j('GET', `/api/instances/${id}/logs?tail=${tail}`),
  register: (id) => j('POST', `/api/instances/${id}/register`),

  threads: (id) => j('GET', `/api/instances/${id}/messages/threads`),
  messages: (id, peer) => j('GET', `/api/instances/${id}/messages/${encodeURIComponent(peer)}`),
  sendSms: (id, to, body, transport = 'auto', operationId = '') => j(
    'POST',
    `/api/instances/${id}/sms/send`,
    { to, body, transport, operation_id: operationId },
  ),
  ackSmsSubmission: (id, operationId) => j(
    'POST', `/api/instances/${id}/sms/submissions/${encodeURIComponent(operationId)}/ack`, {}),
  allowance: (id) => j('GET', `/api/instances/${id}/allowance`),
  saveAllowance: (id, body) => j('PUT', `/api/instances/${id}/allowance`, body),
  allowanceQueryRule: (id) => j('GET', `/api/instances/${id}/allowance/query-rule`),
  saveAllowanceQueryRule: (id, body) => j('PUT', `/api/instances/${id}/allowance/query-rule`, body),
  resetAllowanceQueryRule: (id) => j('DELETE', `/api/instances/${id}/allowance/query-rule`),
  queryAllowance: (id, transport = 'auto') => j(
    'POST', `/api/instances/${id}/allowance/query`, { transport }),
  // delete messages: { ids:[...] } | { peer } (whole conversation) | { all:true }
  deleteMessages: (id, sel) => j('POST', `/api/instances/${id}/messages/delete`, sel),

  calls: (id) => j('GET', `/api/instances/${id}/calls`),
  openIncomingCalls: (id) => j('GET', `/api/instances/${id}/calls/open-incoming`),
  // delete call-log entries: { ids:[...] } | { all:true }
  deleteCalls: (id, sel) => j('POST', `/api/instances/${id}/calls/delete`, sel),
  hangup: (id) => j('POST', `/api/instances/${id}/hangup`),
  hangupIncomingVowifiCall: (
    id, callId, sourceCallId, engineRunId, disposition = 'hangup',
  ) => j(
    'POST',
    `/api/instances/${id}/calls/${encodeURIComponent(callId)}/hangup`,
    { source_call_id: sourceCallId, engine_run_id: engineRunId, disposition },
  ),
  prepareCellularCall: (id, to, ownerToken) => j('POST', `/api/instances/${id}/cellular-call/prepare`, { to, owner_token: ownerToken }, {}, 15000),
  commitCellularCall: (id, callId, ownerToken) => j('POST', `/api/instances/${id}/cellular-call/${encodeURIComponent(callId)}/commit`, { owner_token: ownerToken }, {}, 10000),
  cellularMediaStatus: (id, callId, ownerToken) => j(
    'GET', `/api/instances/${id}/cellular-call/${encodeURIComponent(callId)}/media`, undefined,
    { 'X-MDD-Call-Owner': ownerToken }, 5000),
  cancelCellularCall: (id, callId, ownerToken) => j('POST', `/api/instances/${id}/cellular-call/${encodeURIComponent(callId)}/cancel`, { owner_token: ownerToken }, {}, 5000),
  releaseCellularCall: (id, callId, ownerToken) => j('POST', `/api/instances/${id}/cellular-call/${encodeURIComponent(callId)}/release`, { owner_token: ownerToken }, {}, 5000),
  cellularCallAlerts: () => j('GET', '/api/cellular-call-alerts'),
  dismissCellularCallAlert: (callId) => j('DELETE', `/api/cellular-call-alerts/${encodeURIComponent(callId)}`),
  prepareIncomingCellularCall: (id, sourceCallId, ownerToken) => j('POST', `/api/instances/${id}/cellular-call/incoming/prepare`, { source_call_id: sourceCallId, owner_token: ownerToken }, {}, 15000),
  answerIncomingCellularCall: (id, callId, ownerToken) => j('POST', `/api/instances/${id}/cellular-call/${encodeURIComponent(callId)}/answer`, { owner_token: ownerToken }, {}, 10000),
  cellularCallStatus: (id) => j('GET', `/api/instances/${id}/cellular-call/status`, undefined, {}, 5000),
  cellularCallHangup: (id) => j('POST', `/api/instances/${id}/cellular-call/hangup`, {}, {}, 5000),
  cellularCallDtmf: (id, digits) => j('POST', `/api/instances/${id}/cellular-call/dtmf`, { digits }),
  softphone: (id) => j('GET', `/api/instances/${id}/softphone`),
  prepareBrowserMedia: (id) => j(
    'POST', `/api/instances/${encodeURIComponent(id)}/browser-media/prepare`, {}),
  prepareBrowserOutbound: (id, to) => j(
    'POST', `/api/instances/${encodeURIComponent(id)}/browser-media/outbound/prepare`, { to }),
  prepareBrowserIncoming: (id, callId, sourceCallId, engineRunId) => j(
    'POST',
    `/api/instances/${encodeURIComponent(id)}/calls/${encodeURIComponent(callId)}/browser-media/prepare`,
    { source_call_id: sourceCallId, engine_run_id: engineRunId }),

  // eSIM / LPA (lpac) — first arg is usually the PC/SC reader NAME (string).
  // Optional se_id / aid target a specific Secure Element on dual-SE cards.
  esimStatus: () => j('GET', '/api/esim/status'),
  esimChip: (readerOrIndex, maybeName) => j('GET', `/api/esim/chip?${readerQuery(readerOrIndex, maybeName)}`),
  esimChipCached: (readerOrIndex, maybeName) => j('GET', `/api/esim/chip/cached?${readerQuery(readerOrIndex, maybeName)}`),
  esimProfiles: (readerOrIndex, maybeName) => j('GET', `/api/esim/profiles?${readerQuery(readerOrIndex, maybeName)}`),
  esimEnable: (iccid, readerOrBody) => j(
    'POST',
    `/api/esim/profiles/${encodeURIComponent(iccid)}/enable`,
    readerBody(readerOrBody),
  ),
  esimDisable: (iccid, readerOrBody) => j(
    'POST',
    `/api/esim/profiles/${encodeURIComponent(iccid)}/disable`,
    readerBody(readerOrBody),
  ),
  esimDelete: (iccid, readerOrBody) => {
    if (readerOrBody && typeof readerOrBody === 'object') {
      const q = readerQuery(readerOrBody.reader ?? readerOrBody.reader_index)
      if (readerOrBody.se_id || readerOrBody.seId) q.set('se_id', readerOrBody.se_id || readerOrBody.seId)
      if (readerOrBody.aid) q.set('aid', readerOrBody.aid)
      return j('DELETE', `/api/esim/profiles/${encodeURIComponent(iccid)}?${q}`)
    }
    return j(
      'DELETE',
      `/api/esim/profiles/${encodeURIComponent(iccid)}?${readerQuery(readerOrBody)}`,
    )
  },
  esimNickname: (iccid, nickname, readerOrBody) => j(
    'POST',
    `/api/esim/profiles/${encodeURIComponent(iccid)}/nickname`,
    readerBody(readerOrBody, { nickname }),
  ),
  esimDownload: (body) => j('POST', '/api/esim/download', body),
  esimDownloadCancel: (readerOrBody) => j('POST', '/api/esim/download/cancel', readerBody(readerOrBody)),
  esimDiscovery: (body) => j('POST', '/api/esim/discovery', body || {}),
  esimNotifications: (readerOrIndex, maybeName) => j(
    'GET',
    `/api/esim/notifications?${readerQuery(readerOrIndex, maybeName)}`,
  ),
  // Aliases used by Esim.jsx
  esimProcessNotifications: (readerOrIndex, seq) => j(
    'POST',
    '/api/esim/notifications/process',
    readerBody(readerOrIndex, seq == null ? {} : { seq }),
  ),
  esimNotificationsProcess: (body) => j('POST', '/api/esim/notifications/process', body || {}),
  esimRemoveNotification: (seq, readerOrBody) => {
    if (readerOrBody && typeof readerOrBody === 'object') {
      const q = readerQuery(readerOrBody.reader ?? readerOrBody.reader_index)
      if (readerOrBody.se_id || readerOrBody.seId) q.set('se_id', readerOrBody.se_id || readerOrBody.seId)
      if (readerOrBody.aid) q.set('aid', readerOrBody.aid)
      return j('DELETE', `/api/esim/notifications/${seq}?${q}`)
    }
    return j(
      'DELETE',
      `/api/esim/notifications/${seq}?${readerQuery(readerOrBody)}`,
    )
  },
  esimNotificationRemove: (seq, readerOrBody) => {
    if (readerOrBody && typeof readerOrBody === 'object') {
      const q = readerQuery(readerOrBody.reader ?? readerOrBody.reader_index)
      if (readerOrBody.se_id || readerOrBody.seId) q.set('se_id', readerOrBody.se_id || readerOrBody.seId)
      if (readerOrBody.aid) q.set('aid', readerOrBody.aid)
      return j('DELETE', `/api/esim/notifications/${seq}?${q}`)
    }
    return j(
      'DELETE',
      `/api/esim/notifications/${seq}?${readerQuery(readerOrBody)}`,
    )
  },
  esimReplayNotification: (body) => j('POST', '/api/esim/notifications/replay', body || {}),
  externalDeps: () => j('GET', '/api/system/external-deps'),
  setAgentToken: (agent_token) => j('POST', '/api/system/agent-token', { agent_token }),
  generateAgentToken: () => j('POST', '/api/system/agent-token/generate', {}),
}


export function connectWs(onMsg, onAuthLost) {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  let ws, alive = true
  const prefix = getBasePrefix()
  const token = getAuthToken()
  const tokenParam = token ? `&token=${encodeURIComponent(token)}` : ''
  let connectionGeneration = 0
  const open = () => {
    // The marker lets the server distinguish clients that understand the 4401 close code
    // from an already-open pre-upgrade tab that would otherwise reconnect forever.
    ws = new WebSocket(`${proto}://${location.host}${prefix}/ws?auth_close=1${tokenParam}`)
    ws.onopen = () => {
      connectionGeneration += 1
      onMsg({ type: 'ws-lifecycle', event: 'open', connection_generation: connectionGeneration })
    }
    ws.onmessage = (e) => { try { onMsg(JSON.parse(e.data)) } catch {} }
    ws.onclose = (event) => {
      if (event.code === 4401) {
        alive = false
        onAuthLost?.()
        return
      }
      if (alive) setTimeout(open, 2000)
    }
    ws.onerror = () => { try { ws.close() } catch {} }
  }
  open()
  return () => { alive = false; try { ws.close() } catch {} }
}
