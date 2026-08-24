import assert from 'node:assert/strict'

globalThis.window = { location: { pathname: '/' } }
globalThis.location = { hostname: 'gateway.test' }
globalThis.sessionStorage = { getItem: () => '', setItem: () => {}, removeItem: () => {} }

const { CellularIncomingController } = await import('../src/cellularIncomingCoordinator.js')

const tick = () => new Promise((resolve) => setTimeout(resolve, 0))

class FakePhone {
  constructor(onEvent, options = {}) {
    this.onEvent = onEvent
    this.options = options
    this.started = 0
    this.answerCount = 0
    this.stopped = 0
    this.hungup = 0
  }
  start() { this.started += 1 }
  unlockAudio() {}
  answer() {
    this.answerCount += 1
    this.onEvent('active')
  }
  hangup() { this.hungup += 1 }
  stop() { this.stopped += 1 }
  waitForBidirectionalMedia() {
    if (this.options.waitForBidirectionalMedia) return this.options.waitForBidirectionalMedia()
    return Promise.resolve({ bidirectional: true, bytes_in: 1600, bytes_out: 1600 })
  }
}

function controllerFixture(overrides = {}) {
  const calls = {
    prepare: 0, cancel: 0, ring: 0, submit: 0, answer: 0, hangup: 0, release: 0,
  }
  const phones = []
  const states = []
  const api = {
    prepareIncomingCellularCall: async () => {
      calls.prepare += 1
      return { call_id: `prep-${calls.prepare}`, browser_nonce: 'nonce',
        softphone: { enabled: true, host: 'gateway.test' } }
    },
    cancelCellularCall: async () => { calls.cancel += 1; return { ok: true } },
    ringIncomingCellularCall: async () => { calls.ring += 1; return { ok: true } },
    submitCellularMediaEvidence: async () => { calls.submit += 1; return { ok: true } },
    answerIncomingCellularCall: async () => { calls.answer += 1; return { ok: true } },
    cellularCallHangup: async () => { calls.hangup += 1; return { ok: true } },
    releaseCellularCall: async () => { calls.release += 1; return { released: true } },
    cellularCallStatus: async () => ({ status: 'active', media: { phase: 'active' } }),
    ...overrides.api,
  }
  const controller = new CellularIncomingController({
    api,
    createMediaPhone: (onEvent) => {
      const phone = new FakePhone(onEvent, overrides.phone)
      phones.push(phone)
      return phone
    },
    onStateChange: (state) => states.push(state),
    showToast: () => {},
    t: (value) => value,
    host: () => 'gateway.test',
    delay: () => Promise.resolve(),
    ...overrides.options,
  })
  return { controller, calls, phones, states }
}

function incoming(instance = '5', id = 10, peer = '+123') {
  return { type: 'call', instance, call: {
    id, direction: 'in', status: 'ringing', transport: 'cellular', peer,
  } }
}

{
  const { controller, calls, phones } = controllerFixture()
  controller.handleMessage(incoming('5', 10))
  controller.handleMessage(incoming('5', 10))
  await tick()
  assert.equal(calls.prepare, 1)
  assert.equal(phones.length, 1)
  phones[0].onEvent('registered', true)
  await tick()
  phones[0].onEvent('incoming', { from: '+123' })
  assert.equal(calls.ring, 1)
  assert.equal(controller.state.mediaReady, true)
  controller.stop({ release: false })
}

{
  let resolvePrepare
  const { controller, calls } = controllerFixture({
    api: {
      prepareIncomingCellularCall: () => new Promise((resolve) => {
        calls.prepare += 1
        resolvePrepare = () => resolve({ call_id: 'late-prep', browser_nonce: 'nonce',
          softphone: { enabled: true, host: 'gateway.test' } })
      }),
    },
  })
  controller.handleMessage(incoming('5', 11))
  controller.decline()
  resolvePrepare()
  await tick()
  await tick()
  assert.equal(calls.hangup, 1)
  assert.equal(calls.cancel, 1)
  assert.equal(calls.ring, 0)
  assert.equal(calls.answer, 0)
  controller.stop({ release: false })
}

{
  const { controller, calls, phones } = controllerFixture()
  controller.handleMessage(incoming('5', 12))
  await tick()
  phones[0].onEvent('registered', true)
  await tick()
  phones[0].onEvent('incoming', { from: '+123' })
  assert.equal(controller.answer(), true)
  assert.equal(controller.answer(), true)
  await tick()
  await tick()
  assert.equal(phones[0].answerCount, 1)
  assert.equal(calls.submit, 1)
  assert.equal(calls.answer, 1)
  assert.equal(controller.state.state, 'active')
  controller.stop({ release: false })
}

{
  const { controller, calls, phones } = controllerFixture()
  controller.handleMessage(incoming('5', 16))
  await tick()
  controller.decline()
  phones[0].onEvent('registered', true)
  phones[0].onEvent('incoming', { from: '+123' })
  await tick()
  assert.equal(calls.hangup, 1)
  assert.equal(calls.ring, 0)
  assert.equal(calls.answer, 0)
  controller.stop({ release: false })
}

{
  const { controller, calls, phones } = controllerFixture()
  controller.handleMessage(incoming('5', 17))
  await tick()
  phones[0].onEvent('registered', true)
  await tick()
  phones[0].onEvent('incoming', { from: '+123' })
  assert.equal(controller.answer(), true)
  await tick()
  await tick()
  assert.equal(controller.state.state, 'active')
  controller.stop({ release: true })
  assert.equal(calls.hangup, 1)
  assert.equal(calls.release, 0)
}

{
  let resolveEvidence
  const { controller, calls, phones } = controllerFixture({
    phone: {
      waitForBidirectionalMedia: () => new Promise((resolve) => {
        resolveEvidence = () => resolve({ bidirectional: true, bytes_in: 1600, bytes_out: 1600 })
      }),
    },
  })
  controller.handleMessage(incoming('5', 13))
  await tick()
  phones[0].onEvent('registered', true)
  await tick()
  phones[0].onEvent('incoming', { from: '+123' })
  assert.equal(controller.answer(), true)
  controller.decline()
  resolveEvidence()
  await tick()
  await tick()
  assert.equal(calls.hangup, 1)
  assert.equal(calls.submit, 0)
  assert.equal(calls.answer, 0)
  controller.stop({ release: false })
}

{
  const { controller, calls } = controllerFixture({ options: { clearMs: 0 } })
  controller.handleMessage(incoming('5', 14))
  await tick()
  controller.decline()
  controller.handleMessage(incoming('5', 14))
  assert.equal(calls.prepare, 1)
  await tick()
  controller.handleMessage(incoming('5', 14))
  assert.equal(calls.prepare, 1)
  controller.handleMessage(incoming('5', 15))
  assert.equal(calls.prepare, 2)
  assert.equal(controller.state.sourceKey, '5:15')
  controller.stop({ release: false })
}

{
  const { controller } = controllerFixture()
  controller.handleMessage(incoming('5', 20, '+111'))
  controller._endSoon('Ended')
  controller.handleMessage(incoming('5', 21, '+222'))
  assert.equal(controller.state.sourceKey, '5:21')
  controller.handleMessage({ type: 'call', instance: '5', call: {
    id: 20, direction: 'in', status: 'answered', transport: 'cellular', peer: '+111',
  } })
  assert.equal(controller.state.sourceKey, '5:21')
  assert.notEqual(controller.state.state, 'active')
  controller.stop({ release: false })
}

console.log('Cellular incoming coordinator tests passed')
