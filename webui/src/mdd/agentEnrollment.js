const pendingKey = 'mdd-agent-enrollment-v1'

export function enrollmentEndpoint(value) {
  const url = new URL(value)
  if (!['https:', 'wss:'].includes(url.protocol) || url.username || url.password || url.search || url.hash || !url.hostname) throw new Error('Invalid Agent endpoint')
  if (url.protocol === 'https:') {
    if (url.pathname !== '/' && url.pathname !== '') throw new Error('Enter the Core origin, not a page URL')
    url.pathname = '/v1/agent/ws'
  }
  if (url.pathname !== '/v1/agent/ws') throw new Error('Invalid Agent endpoint')
  url.protocol = 'wss:'
  return `wss://${url.hostname.includes(':') && !url.hostname.startsWith('[') ? `[${url.hostname}]` : url.hostname}:${url.port || '443'}${url.pathname}`
}

export function newAgentEnrollment({server, mode, pin, directCore}) {
  const server_url = enrollmentEndpoint(server)
  const tls_sha256 = String(pin || '').replaceAll(':', '').trim().toLowerCase()
  if (mode !== 'system' && mode !== 'pin') throw new Error('Invalid TLS mode')
  if (mode === 'pin' && (!directCore || !/^[a-f0-9]{64}$/.test(tls_sha256))) throw new Error('Confirm the endpoint and its trusted certificate fingerprint')
  return {agent_id:`agent-${crypto.randomUUID().replaceAll('-', '')}`, enrollment_id:crypto.randomUUID().replaceAll('-', ''),
    server_url, tls_mode:mode, ...(mode === 'pin' ? {tls_sha256} : {})}
}

export function connectionPackage(pending, result) {
  if (result?.agent_id !== pending.agent_id || typeof result.agent_token !== 'string' || result.agent_token.length < 32) throw new Error('Invalid Agent enrollment response')
  return JSON.stringify({kind:'mdd-agent-connection',version:1,agent_id:pending.agent_id,
    server_url:pending.server_url,server_token:result.agent_token,tls_mode:pending.tls_mode,
    ...(pending.tls_mode === 'pin' ? {tls_sha256:pending.tls_sha256} : {})})
}

export function loadPendingEnrollment(storage = sessionStorage) {
  const raw = storage.getItem(pendingKey)
  if (!raw) return null
  const value = JSON.parse(raw)
  if (!/^agent-[a-f0-9]{32}$/.test(value.agent_id) || !/^[a-f0-9]{32}$/.test(value.enrollment_id) ||
      enrollmentEndpoint(value.server_url) !== value.server_url || !['pin','system'].includes(value.tls_mode) ||
      value.tls_mode === 'pin' && !/^[a-f0-9]{64}$/.test(value.tls_sha256)) throw new Error('Invalid pending Agent enrollment')
  return value
}

export function savePendingEnrollment(value, storage = sessionStorage) {
  if (value) storage.setItem(pendingKey, JSON.stringify(value))
  else storage.removeItem(pendingKey)
}
