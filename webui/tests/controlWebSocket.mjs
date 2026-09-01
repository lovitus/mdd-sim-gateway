import assert from 'node:assert/strict'

globalThis.window = { location: { pathname: '/mdd/devices' } }
globalThis.location = { protocol: 'https:', host: 'gateway.example', pathname: '/mdd/devices' }
globalThis.sessionStorage = {
  value: '',
  setItem(_key, value) { this.value = value },
  getItem() { return this.value },
  removeItem() { this.value = '' },
}

const { controlWsUrl, setAuthToken } = await import('../src/api.js')
setAuthToken('secret-session-token')
const url = controlWsUrl()
assert.equal(url, 'wss://gateway.example/mdd/v1/browser/ws?auth_close=1')
assert.equal(url.includes('secret-session-token'), false)
