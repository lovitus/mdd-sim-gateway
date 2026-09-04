// Thin REST + WebSocket client for the manager API (same origin).
import { mapBrowserSnapshot, mapDeviceProfilesResponse, mapGoSnapshot, operationID } from './goV1Adapter.js'
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

export async function downloadSystemBackup() {
  const headers = {}
  const token = getAuthToken()
  if (token) { headers['X-MDD-Session'] = token; headers.Authorization = `Bearer ${token}` }
  if (csrfToken) headers['X-MDD-CSRF-Token'] = csrfToken
  const response = await fetch(base + '/v1/system/backups', { method: 'POST', headers, credentials: 'same-origin' })
  if (!response.ok) throw new Error(`backup failed (${response.status})`)
  return response.blob()
}

async function requestJSON(method, path, body, headers = {}, timeoutMs = 0) {
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
  if (!r.ok) throw Object.assign(new Error(detailMsg || data.error || r.statusText), {
    status: r.status,
    data,
    code: data.code || data.detail?.code || '',
    kind: data.kind || data.detail?.kind || '',
    layer: data.layer || data.detail?.layer || '',
    detail: typeof data.detail === 'string' ? data.detail : data.detail?.message || '',
  })
  return { data, response: r }
}

async function j(method, path, body, headers = {}, timeoutMs = 0) {
  return (await requestJSON(method, path, body, headers, timeoutMs)).data
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
	authAgentToken: () => j('POST', '/api/auth/agent-token', {}),
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
  checkUpdate: (force = false) => j('GET', `/v1/system/update/check${force ? '?force=true' : ''}`),
  applyUpdate: () => j('POST', '/v1/system/update/apply', {}),
  updateProgress: () => j('GET', '/v1/system/update/progress'),
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
  lineFacts: (id) => j('GET', `/api/instances/${id}/facts`),
  verifyLinePassive: (id) => j('POST', `/api/instances/${id}/verification/passive`, {}),

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
  releaseCellularCall: (id, callId, ownerToken, reason = 'unknown') => j('POST', `/api/instances/${id}/cellular-call/${encodeURIComponent(callId)}/release`, { owner_token: ownerToken, reason }, {}, 5000),
  cellularCallAlerts: () => j('GET', '/api/cellular-call-alerts'),
  dismissCellularCallAlert: (callId) => j('DELETE', `/api/cellular-call-alerts/${encodeURIComponent(callId)}`),
  prepareIncomingCellularCall: (id, sourceCallId, ownerToken) => j('POST', `/api/instances/${id}/cellular-call/incoming/prepare`, { source_call_id: sourceCallId, owner_token: ownerToken }, {}, 15000),
  answerIncomingCellularCall: (id, callId, ownerToken) => j('POST', `/api/instances/${id}/cellular-call/${encodeURIComponent(callId)}/answer`, { owner_token: ownerToken }, {}, 10000),
  cellularCallStatus: (id) => j('GET', `/api/instances/${id}/cellular-call/status`, undefined, {}, 5000),
  cellularCallHangup: (id) => j('POST', `/api/instances/${id}/cellular-call/hangup`, {}, {}, 5000),
  cellularCallDtmf: (id, digits) => j('POST', `/api/instances/${id}/cellular-call/dtmf`, { digits }),
  softphone: (id) => j('GET', `/api/instances/${id}/softphone`, undefined, {}, 8000),
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

let latestGoSnapshot = null

async function optionalGet(path, fallback) {
  try { return await j('GET', path) } catch { return fallback }
}

async function loadGoSnapshot() {
  const [lines, catalog, devices, agents, egress] = await Promise.all([
    j('GET', '/v1/lines'),
    j('GET', '/v1/catalog/lines'),
    j('GET', '/v1/devices'),
    j('GET', '/v1/agents'),
    optionalGet('/v1/egress/exits', { schema_version: 1, exits: [] }),
  ])
  latestGoSnapshot = mapGoSnapshot({
    at: lines.at || devices.at,
    lines: lines.lines || [], catalog,
    devices: devices.devices || [], agents: agents.agents || [], egress,
  })
  return latestGoSnapshot
}

async function freshDevice(id) {
  const payload = await j('GET', '/v1/devices')
  const device = (payload.devices || []).find(item => String(item.id) === String(id))
  if (!device) throw Object.assign(new Error('device_offline'), {
    status: 404, data: { code: 'device_offline' },
  })
  return device
}

function exactDeviceLine(device) {
  const endpoints = (device?.endpoints || []).filter(endpoint =>
    endpoint?.association === 'exact' && endpoint?.operation_candidate === true && endpoint?.line?.id)
  const ids = [...new Set(endpoints.map(endpoint => String(endpoint.line.id)))]
  if (ids.length !== 1) throw Object.assign(new Error(ids.length ? 'device_ambiguous' : 'device_not_configured'), {
    status: 409, data: { code: ids.length ? 'device_ambiguous' : 'device_not_configured' },
  })
  return ids[0]
}

// Device diagnostics are a read-only projection of the typed Go diagnostics
// stream.  The old Python endpoint performed an implicit modem probe and is
// intentionally not used by the Go-only UI: a stale or ambiguous attachment
// must remain visibly blocked instead of being reported as healthy.
async function goDeviceDiagnostics(id) {
  const result = await j('GET', `/v1/devices/${encodeURIComponent(id)}/diagnostics`)
  const checks = (result.checks || []).map(fact => ({
    name: fact.id || fact.layer || 'unknown',
    ok: ['ready', 'pass', 'connected', 'active'].includes(String(fact.status || '').toLowerCase()),
    detail: fact.code || fact.detail || fact.status || 'unknown',
  }))
  return { ...result, ok: checks.length > 0 && checks.every(check => check.ok), checks }
}

async function patchGoDevice(id, patch) {
  const keys = Object.keys(patch || {}).filter(key => patch[key] !== undefined)
	if (keys.length !== 1 || !['cellular_enabled', 'connection_enabled', 'flight_mode', 'roaming_enabled', 'selected_profile', 'vowifi_enabled'].includes(keys[0]))
    throw new Error('exactly one supported device policy field is required')
  const field = keys[0]
  if (field === 'vowifi_enabled') {
    const lineID = exactDeviceLine(await freshDevice(id))
    const action = patch[field] ? 'start' : 'stop'
    return j('POST', `/v1/lines/${encodeURIComponent(lineID)}/vowifi/runtime/${action}`,
      { operation_id: operationID(`react-vowifi-${action}`) })
  }
  const current = await requestJSON('GET', `/v1/devices/${encodeURIComponent(id)}/policy`)
  const etag = current.response.headers.get('ETag')
  if (!etag) throw new Error('device policy revision is unavailable')
  return j('PATCH', `/v1/devices/${encodeURIComponent(id)}/policy`, {
    operation_id: operationID('react-device-policy'), [field]: field === 'selected_profile' ? String(patch[field] || '') : patch[field] === true,
  }, { 'If-Match': etag })
}

async function goDeviceProfiles(id) {
  const result = await requestJSON('GET', `/v1/devices/${encodeURIComponent(id)}/profiles`)
  return mapDeviceProfilesResponse(result.data)
}

async function saveGoDeviceProfile(id, profile) {
  const current = await requestJSON('GET', `/v1/devices/${encodeURIComponent(id)}/profiles`)
  const etag = current.response.headers.get('ETag')
  if (!etag) throw new Error('device profile revision is unavailable')
  const payload = await j('PUT', `/v1/devices/${encodeURIComponent(id)}/profiles`, {
    operation_id: operationID('react-device-profile'),
    name: String(profile?.name || '').trim(),
    apn: String(profile?.apn || '').trim(),
    auth: String(profile?.auth || 'NONE').trim().toUpperCase(),
    username: String(profile?.username || '').trim(),
    password: String(profile?.password || ''),
    password_set: String(profile?.password || '') !== '',
  }, { 'If-Match': etag })
  return (payload.profiles || []).find(item => item.name === String(profile?.name || '').trim()) || profile
}

async function goLineFacts(id) {
  const projection = await j('GET', `/v1/lines/${encodeURIComponent(id)}`)
  return {
    facts: Object.fromEntries((projection.facts || []).map(fact =>
      [fact.layer, { ...fact, state: fact.condition }])),
    summary: {
      state: Object.values(projection.operations || {}).some(value => value?.ready) ? 'ready' : 'unknown',
      code: Object.values(projection.operations || {}).some(value => value?.ready)
        ? 'one_or_more_operations_ready' : 'operations_not_ready',
      blockers: [...new Set(Object.values(projection.operations || {}).flatMap(value => value?.blocked || []))],
    },
    raw: projection,
  }
}

async function goEgressStatus() {
  const value = await j('GET', '/v1/egress/exits')
  return {
    schema_version: value.schema_version,
    exits: Object.fromEntries((value.exits || []).map(exit => [String(exit.country).toLowerCase(), exit])),
  }
}

async function goEgressConfig() {
  const payload = await j('GET', '/v1/egress/config')
  return { revision: Number(payload.revision || 0), config: payload.config || {} }
}

async function saveGoEgressConfig(config, revision) {
  if (!Number.isSafeInteger(Number(revision)) || Number(revision) < 1)
    throw new Error('country exit configuration revision is unavailable')
  return j('PUT', '/v1/egress/config', config, { 'If-Match': `"${Number(revision)}"` })
}

async function applyGoEgress(configRevision) {
  const catalog = await j('GET', '/v1/catalog/lines')
  return j('POST', '/v1/egress/config/apply', {
    schema_version: 2,
    config_revision: Number(configRevision),
    catalog_revision: Number(catalog.revision),
  })
}

async function goCellularSIMs() {
  const snapshot = await loadGoSnapshot()
  const byLine = new Map(snapshot.instances.map(line => [String(line.id), line]))
  const seen = new Set()
  const sims = []
  for (const device of snapshot.devices) {
    const iccid = String(device?.sim?.iccid || '')
    if (!/^\d{18,22}$/.test(iccid) || seen.has(iccid)) continue
    seen.add(iccid)
    const line = byLine.get(String(device.instance_id || ''))
    sims.push({
      iccid,
      phone: device.sim?.number || line?.msisdn || '',
      line_name: line?.name || device.sim?.name || '',
      line_id: line?.id || '',
      online: device.present !== false && device.mode === 'adapted' &&
        device.capabilities?.cellular?.available === true,
      allowed: device.capabilities?.cellular?.desired === true,
      flight_mode: device.capabilities?.flight?.desired === true,
      data_state: device.cellular?.data_state || '',
      borrow: device.cellular?.data_lease || null,
    })
  }
  return { sims }
}

async function goNotificationConfig() {
  return j('GET', '/v1/notifications/config')
}

async function saveGoNotificationConfig(patch) {
  return j('PUT', '/v1/notifications/config', patch)
}

function oldIMEIPool(snapshot) {
  return {
    revision: snapshot.revision,
    catalog_revision: snapshot.catalog_revision,
    pool: (snapshot.entries || []).map(entry => ({ ...entry,
      imei_masked: entry.imei ? `•••• ${entry.imei.slice(-4)}` : '—' })),
    bindings: Object.fromEntries((snapshot.bindings || []).map(binding => [binding.card_id, {
      imei_id: binding.entry_id, line_id: binding.line_id, imei: binding.imei,
    }])),
    unpooled: snapshot.unpooled || [],
  }
}

async function goIMEIPool() {
  return oldIMEIPool(await j('GET', '/v1/imei-pool'))
}

async function saveGoIMEIEntry(entry) {
  const snapshot = await j('GET', '/v1/imei-pool')
  const id = String(entry?.id || operationID('imei')).slice(0, 128)
  return j('PUT', `/v1/imei-pool/${encodeURIComponent(id)}`, {
    schema_version: 1, id, name: String(entry?.name || '').trim(),
    imei: String(entry?.imei || '').trim(), notes: String(entry?.notes || '').trim(),
  }, { 'If-Match': `"${snapshot.revision}"` })
}

async function deleteGoIMEIEntry(id) {
  const snapshot = await j('GET', '/v1/imei-pool')
  return j('DELETE', `/v1/imei-pool/${encodeURIComponent(id)}`, {},
    { 'If-Match': `"${snapshot.revision}"` })
}

async function bindGoIMEI(input) {
  const [snapshot, catalog] = await Promise.all([j('GET', '/v1/imei-pool'), j('GET', '/v1/catalog/lines')])
  const line = (catalog.lines || []).find(value => String(value.card_id) === String(input.iccid))
  if (!line) throw new Error('no configured line owns this ICCID')
  return j('PUT', `/v1/imei-pool/${encodeURIComponent(input.imei_id)}/bindings/${encodeURIComponent(line.id)}`, {
    expected_catalog_revision: snapshot.catalog_revision,
    expected_card_id: line.card_id,
  }, { 'If-Match': `"${snapshot.revision}"` })
}

async function unbindGoIMEI(iccid) {
  const snapshot = await j('GET', '/v1/imei-pool')
  const binding = (snapshot.bindings || []).find(value => String(value.card_id) === String(iccid))
  if (!binding) throw new Error('IMEI binding not found')
  return j('DELETE', `/v1/imei-pool/${encodeURIComponent(binding.entry_id)}/bindings/${encodeURIComponent(binding.line_id)}`, {
    expected_catalog_revision: snapshot.catalog_revision,
    expected_card_id: binding.card_id,
  }, { 'If-Match': `"${snapshot.revision}"` })
}

function oldAllowance(snapshot) {
  const value = snapshot?.snapshot || snapshot
  return { allowance: { ...(value?.values || {}), source: value?.source || '',
    revision: Number(value?.revision || 0),
    updated_ts: value?.updated_at ? Math.floor(new Date(value.updated_at).getTime() / 1000) : 0 } }
}

async function goAllowance(lineID) {
  return oldAllowance(await j('GET', `/v1/lines/${encodeURIComponent(lineID)}/allowance`))
}

async function saveGoAllowance(lineID, values) {
  const current = await j('GET', `/v1/lines/${encodeURIComponent(lineID)}/allowance`)
  const result = await j('PUT', `/v1/lines/${encodeURIComponent(lineID)}/allowance`, {
    balance: values.balance || '', sms_remaining: values.sms_remaining || '',
    data_remaining: values.data_remaining || '', voice_remaining: values.voice_remaining || '',
    valid_until: values.valid_until || '', activated_at: values.activated_at || '',
  }, { 'If-Match': `"${current.snapshot.revision}"` })
  return oldAllowance(result)
}

function oldAllowanceRule(value) {
  const rule = value?.rule || value
  return { rule: { ...rule, effective: { recipient: rule?.recipient || '', body: rule?.body || '' },
    known: false, custom: Boolean(rule?.recipient || rule?.body) } }
}

async function saveGoAllowanceRule(lineID, input) {
  const current = await j('GET', `/v1/lines/${encodeURIComponent(lineID)}/allowance/query-rule`)
  const result = await j('PUT', `/v1/lines/${encodeURIComponent(lineID)}/allowance/query-rule`, {
    schema_version: 1, line_id: String(lineID), recipient: input.recipient,
    body: input.body, parser: current.rule?.parser || 'none',
  }, { 'If-Match': `"${current.rule.revision}"` })
  return oldAllowanceRule(result)
}

async function resetGoAllowanceRule(lineID) {
  const current = await j('GET', `/v1/lines/${encodeURIComponent(lineID)}/allowance/query-rule`)
  return oldAllowanceRule(await j('DELETE', `/v1/lines/${encodeURIComponent(lineID)}/allowance/query-rule`, {},
    { 'If-Match': `"${current.rule.revision}"` }))
}

async function setGoLineCountry(lineID, country) {
  const catalog = await j('GET', '/v1/catalog/lines')
  const line = (catalog.lines || []).find(value => String(value.id) === String(lineID))
  if (!line) throw new Error('line not found')
  const next = { ...line, network: { ...(line.network || {}), egress_country: String(country || '').toLowerCase() } }
  const result = await j('PUT', `/v1/catalog/lines/${encodeURIComponent(lineID)}`, next,
    { 'If-Match': `"${catalog.revision}"` })
  return { effective_country: result.line?.network?.egress_country || '', line: result.line, revision: result.revision }
}

async function queryGoAllowance(lineID, transport) {
  if (!['cellular', 'vowifi'].includes(transport)) throw new Error('choose an explicit SMS transport in Messages')
  const catalog = await j('GET', '/v1/catalog/lines')
  const line = (catalog.lines || []).find(value => String(value.id) === String(lineID))
  if (!line?.card_id) throw new Error('line SIM identity is unavailable')
  const query = await j('POST', `/v1/lines/${encodeURIComponent(lineID)}/allowance/query`, {
    query_id: operationID('react-allowance-query'), expected_card_id: line.card_id, transport,
  })
  const dispatch = query.dispatch
  const expectedPath = transport === 'cellular'
    ? `/v1/lines/${encodeURIComponent(lineID)}/cellular/messages`
    : `/v1/lines/${encodeURIComponent(lineID)}/vowifi/messages/send`
  const body = dispatch?.body || {}
  if (dispatch?.method !== 'POST' || dispatch.path !== expectedPath ||
      body.allowance_query_id !== query.query?.query_id || body.expected_card_id !== line.card_id)
    throw new Error('allowance dispatch identity mismatch')
  const result = await api.sendMessageV1(lineID, transport, body)
  return { ...result, ok: true, query }
}

// The React shell is retained, but its data and mutations are now sourced from the typed Go
// contracts. Legacy methods remain only until their corresponding React view is replaced;
// none of these overrides recreates the Python aggregate API on the server.
Object.assign(api, {
  snapshot: loadGoSnapshot,
  instances: async () => ({ instances: (await loadGoSnapshot()).instances }),
  cards: async () => ({ cards: (await loadGoSnapshot()).cards }),
  devices: async () => {
    const snapshot = await loadGoSnapshot()
    return { devices: snapshot.devices, discovering: snapshot.discovering }
  },
  patchDeviceCapabilities: patchGoDevice,
  deviceDiagnostics: goDeviceDiagnostics,
  deviceCellularProfiles: goDeviceProfiles,
  saveDeviceCellularProfile: saveGoDeviceProfile,
  refreshDeviceSms: async id => {
    return j('POST', `/v1/devices/${encodeURIComponent(id)}/sms/refresh`, {}, {}, 40000)
  },
  lineFacts: goLineFacts,
  verifyLinePassive: goLineFacts,
  agentHealth: async () => {
    const payload = await j('GET', '/v1/agents')
    return { at: payload.at, agents: payload.agents || [] }
  },
  egressStatus: goEgressStatus,
  egressConfig: goEgressConfig,
  saveEgressConfig: saveGoEgressConfig,
  applyEgress: applyGoEgress,
	testEgressProfile: (profileID, revision) => j('POST', `/v1/egress/profiles/${encodeURIComponent(profileID)}/test`, {},
		{ 'If-Match': `"${Number(revision)}"` }, 15000),
  cellularSims: goCellularSIMs,
  setLineCountry: async (lineID, country) => {
    const catalog = await j('GET', '/v1/catalog/lines')
    const line = (catalog.lines || []).find(value => String(value.id) === String(lineID))
    if (!line) throw Object.assign(new Error('line_not_found'), { status: 404, code: 'line_not_found' })
    const updated = { ...line, network: { ...(line.network || {}), egress_country: String(country || '').trim().toLowerCase() } }
    const result = await j('PUT', `/v1/catalog/lines/${encodeURIComponent(lineID)}`, updated,
      { 'If-Match': `"${Number(catalog.revision)}"` })
    return { ...result.line, effective_country: updated.network.egress_country }
  },
  setLineRuntime: (lineID, action) => {
    if (!['start', 'stop'].includes(action)) throw new Error('invalid_runtime_action')
    return j('POST', `/v1/lines/${encodeURIComponent(lineID)}/vowifi/runtime/${action}`, {
      operation_id: operationID(`react-line-runtime-${action}`),
    })
  },
  notificationConfig: goNotificationConfig,
	systemPreferences: () => j('GET', '/v1/system/preferences'),
	systemBackup: () => j('POST', '/v1/system/backups', undefined, {}, 60000),
	systemMaintenance: (action, request) => j('POST', '/v1/system/maintenance', { action, request }),
	systemMaintenanceStatus: () => j('GET', '/v1/system/maintenance'),
	saveSystemPreferences: (revision, patch) => j('PATCH', '/v1/system/preferences', patch,
		{ 'If-Match': `"${Number(revision)}"` }),
  saveNotificationConfig: saveGoNotificationConfig,
  notificationDeliveries: () => j('GET', '/v1/notifications/deliveries'),
  clearNotificationDeliveries: () => j('DELETE', '/v1/notifications/deliveries', {}),
  testNotification: channel => j('POST', `/v1/notifications/tests/${encodeURIComponent(channel)}`,
    { operation_id: operationID(`react-notification-${channel}`) }),
  imeiPool: goIMEIPool,
  saveImeiPoolEntry: saveGoIMEIEntry,
  deleteImeiPoolEntry: deleteGoIMEIEntry,
  bindImeiToIccid: bindGoIMEI,
  unbindImeiFromIccid: unbindGoIMEI,
  allowance: goAllowance,
  saveAllowance: saveGoAllowance,
  allowanceQueryRule: async lineID => oldAllowanceRule(await j('GET',
    `/v1/lines/${encodeURIComponent(lineID)}/allowance/query-rule`)),
  saveAllowanceQueryRule: saveGoAllowanceRule,
  resetAllowanceQueryRule: resetGoAllowanceRule,
  queryAllowance: queryGoAllowance,
  setLineCountry: setGoLineCountry,
  listMessagesV1: (lineID, transport) => transport === 'cellular'
    ? j('GET', `/v1/lines/${encodeURIComponent(lineID)}/cellular/messages`, undefined, {}, 40000)
    : j('GET', `/v1/messages?line_id=${encodeURIComponent(lineID)}&limit=100`),
	messageHistoryV1: (lineID, transport) => j('GET', `/v1/messages?line_id=${encodeURIComponent(lineID)}&transport=${encodeURIComponent(transport)}&limit=100`),
	deleteMessageHistoryV1: body => j('DELETE', '/v1/messages', body),
  sendMessageV1: (lineID, transport, body) => transport === 'cellular'
    ? j('POST', `/v1/lines/${encodeURIComponent(lineID)}/cellular/messages`, body, {}, 140000)
    : j('POST', `/v1/lines/${encodeURIComponent(lineID)}/vowifi/messages/send`, body, {}, 140000),
  euiccs: () => j('GET', '/v1/euiccs'),
  mutateEuiccProfile: (eid, iccid, action, body) => j('POST',
    `/v1/euiccs/${encodeURIComponent(eid)}/profiles/${encodeURIComponent(iccid)}/${encodeURIComponent(action)}`,
    body, {}, 130000),
  startEuiccDownload: (eid, body) => j('POST', `/v1/euiccs/${encodeURIComponent(eid)}/downloads`, body, {}, 130000),
  euiccDownloadStatus: (eid, operationIDValue) => j('GET',
    `/v1/euiccs/${encodeURIComponent(eid)}/downloads/${encodeURIComponent(operationIDValue)}`),
  cancelEuiccDownload: (eid, operationIDValue) => j('POST',
    `/v1/euiccs/${encodeURIComponent(eid)}/downloads/${encodeURIComponent(operationIDValue)}/cancel`, {}),
  discoverEuicc: (eid, body) => j('POST', `/v1/euiccs/${encodeURIComponent(eid)}/discovery`, body, {}, 130000),
  euiccNotifications: eid => j('GET', `/v1/euiccs/${encodeURIComponent(eid)}/notifications`, undefined, {}, 130000),
  deliverEuiccNotification: (eid, sequence, body) => j('POST',
    `/v1/euiccs/${encodeURIComponent(eid)}/notifications/${encodeURIComponent(sequence)}/deliver`, body, {}, 130000),
  removeEuiccNotification: (eid, sequence, body) => j('POST',
    `/v1/euiccs/${encodeURIComponent(eid)}/notifications/${encodeURIComponent(sequence)}/remove`, body, {}, 130000),
  createCallMediaLease: (mode, body) => j('POST',
    mode === 'cellular' ? '/v1/cellular/media/leases' : '/v1/media/leases', body),
  releaseCallMediaLease: (mode, sessionID) => j('DELETE',
    mode === 'cellular' ? '/v1/cellular/media/leases' : '/v1/media/leases', { session_id: sessionID }),
  callTransportStatus: (lineID, mode) => j('GET', mode === 'cellular'
    ? `/v1/lines/${encodeURIComponent(lineID)}/cellular/calls/status`
    : `/v1/lines/${encodeURIComponent(lineID)}/vowifi/status`),
  startCallV1: (call, incoming = false) => {
    const cellular = call.mode === 'cellular'
    const path = cellular
      ? `/v1/lines/${encodeURIComponent(call.line_id)}/cellular/calls/${incoming ? 'answer' : 'start'}`
      : `/v1/lines/${encodeURIComponent(call.line_id)}/vowifi/${incoming ? 'calls/incoming/answer' : 'calls/start'}`
    let body
    if (cellular && incoming) body = {
      operation_id: call.start_operation_id, session_id: call.lease?.session_id,
      incoming_event_id: call.incoming.incoming_event_id, expected_card_id: call.expected_card_id,
      sim_session_generation: call.incoming.sim_session_generation,
      native_call_index: call.incoming.native_call_index, call_occurrence: call.incoming.occurrence,
    }
    else if (cellular) body = {
      operation_id: call.start_operation_id, session_id: call.lease?.session_id,
      callee: call.callee, expected_card_id: call.expected_card_id,
    }
    else if (incoming) body = {
      operation_id: call.start_operation_id, call_id: call.call_id,
      media_session_id: call.lease?.session_id, media_buffer_ms: call.buffer_ms,
    }
    else body = {
      operation_id: call.start_operation_id, call_id: call.call_id,
      media_session_id: call.lease?.session_id, callee: call.callee,
      media_buffer_ms: call.buffer_ms, expected_card_id: call.expected_card_id,
    }
    return j('POST', path, body, {}, 45000)
  },
  hangupCallV1: call => j('POST', call.mode === 'cellular'
    ? `/v1/lines/${encodeURIComponent(call.line_id)}/cellular/calls/hangup`
    : `/v1/lines/${encodeURIComponent(call.line_id)}/vowifi/calls/end`,
  call.mode === 'cellular'
    ? { operation_id: call.end_operation_id, session_id: call.lease?.session_id }
    : { operation_id: call.end_operation_id, call_id: call.call_id, reason_code: 'user_hangup' }, {}, 45000),
  callDTMFV1: (call, signal) => j('POST', call.mode === 'cellular'
    ? `/v1/lines/${encodeURIComponent(call.line_id)}/cellular/calls/dtmf`
    : `/v1/lines/${encodeURIComponent(call.line_id)}/vowifi/calls/dtmf`,
  call.mode === 'cellular'
    ? { operation_id: operationID('react-cellular-dtmf'), session_id: call.lease?.session_id, signal }
    : { operation_id: operationID('react-vowifi-dtmf'), call_id: call.call_id, signal, duration_ms: 160 }),
  rejectIncomingCallV1: (lineID, mode, call) => j('POST', mode === 'cellular'
    ? `/v1/lines/${encodeURIComponent(lineID)}/cellular/calls/reject`
    : `/v1/lines/${encodeURIComponent(lineID)}/vowifi/calls/incoming/reject`,
  mode === 'cellular' ? {
    operation_id: operationID('react-cellular-incoming-reject'),
    incoming_event_id: call.incoming_event_id, expected_card_id: call.card_id,
    sim_session_generation: call.sim_session_generation,
    native_call_index: call.native_call_index, call_occurrence: call.occurrence,
  } : { operation_id: operationID('react-vowifi-incoming-reject'), call_id: call.call_id, reason_code: 'user_rejected' }),
  callHistoryV1: () => j('GET', '/v1/calls?limit=100'),
  deleteCallHistoryV1: ids => j('DELETE', '/v1/calls', { ids }),
  catalogLines: (includeDeleted = false) => j('GET', `/v1/catalog/lines${includeDeleted ? '?include_deleted=true' : ''}`),
  saveCatalogLine: (line, revision) => j('PUT', `/v1/catalog/lines/${encodeURIComponent(line.id)}`,
    line, { 'If-Match': `"${Number(revision)}"` }),
  softDeleteCatalogLine: (lineID, revision) => j('POST', `/v1/catalog/lines/${encodeURIComponent(lineID)}/soft-delete`,
    {}, { 'If-Match': `"${Number(revision)}"` }),
  restoreCatalogLine: (lineID, revision) => j('POST', `/v1/catalog/lines/${encodeURIComponent(lineID)}/restore`,
    {}, { 'If-Match': `"${Number(revision)}"` }),
  lineCandidates: () => j('GET', '/v1/line-candidates'),
  simPIN: command => j('POST', '/v1/sim-pin', command),
  operationStatus: operationIDValue => j('GET', `/v1/operations/${encodeURIComponent(operationIDValue)}`),
  claimLineCandidate: (candidateID, name, revision, operationIDValue = operationID('react-line-claim')) => j('POST',
    `/v1/line-candidates/${encodeURIComponent(candidateID)}/claim`,
    { schema_version: 1, operation_id: operationIDValue, name: String(name || '').trim() }, { 'If-Match': `"${Number(revision)}"` }),
  providerApplyStatus: () => j('GET', '/v1/system/provider-config'),
  applyProviderConfig: revision => j('POST', '/v1/system/provider-config', {
    schema_version: 1, catalog_revision: Number(revision),
  }, {}, 140000),
  rawModemBinding: lineID => j('GET', `/v1/lines/${encodeURIComponent(lineID)}/raw-modem`),
  saveRawModemBinding: (lineID, body) => j('PUT', `/v1/lines/${encodeURIComponent(lineID)}/raw-modem`, body),
  testEgress: country => j('POST', `/v1/egress/exits/${encodeURIComponent(String(country).toLowerCase())}/test`, {}),
  systemStatus: async () => {
    const [status, runtime] = await Promise.all([
      j('GET', '/v1/system/status'), j('GET', '/v1/system/runtime'),
    ])
    return { ...status, ...runtime, version: runtime.build_version || runtime.module_version || '',
      repository_url: 'https://github.com/MddIdd/mdd-sim-gateway', runtime }
  },
  diagnosticsV1: () => j('GET', '/v1/diagnostics'),
  lineFactsV1: async lineID => {
    const snapshot = await j('GET', '/v1/diagnostics')
    const projection = (snapshot.lines || []).find(item => String(item.line_id) === String(lineID))
    if (!projection) throw Object.assign(new Error('line_diagnostics_not_found'), { status: 404, code: 'line_diagnostics_not_found' })
    const facts = Object.fromEntries((projection.facts || []).map(fact => [fact.layer, { ...fact, state: fact.condition }]))
    const blockers = Object.values(facts).filter(fact => ['blocked', 'failed'].includes(fact.state)).map(fact => fact.layer)
    const unknown = Object.values(facts).filter(fact => !fact.fresh || fact.state === 'unknown').map(fact => fact.layer)
    const firstProblem = Object.values(facts).find(fact => ['blocked', 'failed'].includes(fact.state))
    return { facts, summary: { state: firstProblem ? 'blocked' : unknown.length ? 'unknown' : 'ready', code: firstProblem?.code || (unknown.length ? 'facts_incomplete' : 'typed_diagnostics_ready'), blockers, unknown }, raw: projection }
  },
})


export function connectWs(onMsg, onAuthLost) {
  let ws, alive = true
  let connectionGeneration = 0
  const open = () => {
    // The marker lets the server distinguish clients that understand the 4401 close code
    // from an already-open pre-upgrade tab that would otherwise reconnect forever.
    // Authentication is the existing same-origin HttpOnly cookie. Never place the session
    // token in a URL where proxies, access logs and browser history can retain it.
    ws = new WebSocket(controlWsUrl())
    ws.onopen = () => {
      connectionGeneration += 1
      onMsg({ type: 'ws-lifecycle', event: 'open', connection_generation: connectionGeneration })
    }
    ws.onmessage = (e) => {
      try {
        const message = JSON.parse(e.data)
        if (message?.type === 'browser.snapshot') {
          latestGoSnapshot = mapBrowserSnapshot(message, latestGoSnapshot)
          onMsg({ type: 'go.snapshot', snapshot: latestGoSnapshot, raw: message })
          return
        }
        onMsg(message)
      } catch {}
    }
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

export function controlWsUrl() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  return `${proto}://${location.host}${getBasePrefix()}/v1/browser/ws?auth_close=1`
}
