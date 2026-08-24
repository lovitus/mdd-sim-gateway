import assert from 'node:assert/strict'

globalThis.window = { location: { pathname: '/' }, isSecureContext: true }
globalThis.location = { hostname: 'localhost' }
globalThis.sessionStorage = { getItem: () => '', setItem: () => {}, removeItem: () => {} }
globalThis.localStorage = { getItem: () => '' }
Object.defineProperty(globalThis, 'navigator', {
  value: { mediaDevices: { getUserMedia: () => Promise.resolve({}) } }, configurable: true,
})

const [{ Softphone }, { api }] = await Promise.all([
  import('../src/softphone.js'), import('../src/api.js'),
])

const issued = ['old-token', 'new-token']
api.issueSoftphoneMediaAdmission = async () => ({ token: issued.shift() })

let releaseOld
const oldCanary = new Promise((resolve) => { releaseOld = resolve })
const canaryTokens = []
const calls = []
const session = () => ({
  direction: 'outgoing',
  connection: null,
  on: () => {},
  terminate: () => {},
})
const phone = new Softphone(() => {}, null)
phone._instanceId = '7'
phone.ua = {
  configuration: { uri: { host: 'gateway.test' } },
  call: (target, options) => {
    calls.push({ target, headers: options.extraHeaders })
    return session()
  },
}
phone._runMediaCanary = (attempt, token) => {
  canaryTokens.push({ attempt, token })
  return token === 'old-token' ? oldCanary : Promise.resolve()
}

phone.call('111')
await new Promise((resolve) => setTimeout(resolve, 0))
phone.hangup() // cancel the first attempt while its canary is still pending
phone.call('222')
await new Promise((resolve) => setTimeout(resolve, 0))
await new Promise((resolve) => setTimeout(resolve, 0))

assert.deepEqual(canaryTokens.map((item) => item.token), ['old-token', 'new-token'])
assert.equal(calls.length, 1)
assert.equal(calls[0].target, 'sip:222@gateway.test')
assert.deepEqual(calls[0].headers, ['X-MDD-Media-Token: new-token'])

releaseOld()
await new Promise((resolve) => setTimeout(resolve, 0))
assert.equal(calls.length, 1, 'a stale canary must not originate its old destination')

const oldSession = session()
const realSession = session()
const audio = { srcObject: 'real-stream', muted: false, volume: 1, play: () => Promise.resolve() }
phone.remoteAudio = audio
phone.session = realSession
phone.attachRemote('late-old-canary-stream', oldSession)
assert.equal(audio.srcObject, 'real-stream', 'late canary media must not replace real call audio')

const lateEvents = []
const stoppedPhone = new Softphone((type) => lateEvents.push(type), null)
stoppedPhone.stop()
stoppedPhone.emit('registered', true)
stoppedPhone.emit('ended', { cause: 'late' })
assert.deepEqual(lateEvents, [], 'stopped phones must swallow late events')

let sharedRemoved = false
const sharedAudio = {
  srcObject: 'active-line-stream',
  remove: () => { sharedRemoved = true },
}
const idlePhone = new Softphone(() => {}, sharedAudio)
idlePhone.stop()
assert.equal(sharedRemoved, false, 'stopping an idle line must not remove the shared App audio element')
assert.equal(sharedAudio.srcObject, 'active-line-stream',
  'stopping an idle line must not clear another line using the shared App audio element')

console.log('VoWiFi media behavior tests passed')
