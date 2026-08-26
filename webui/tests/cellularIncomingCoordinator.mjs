import assert from 'node:assert/strict'
globalThis.window = { location: { pathname: '/' } }
const { CellularIncomingController } = await import('../src/cellularIncomingCoordinator.js')
const tick = () => new Promise(resolve => setTimeout(resolve, 0))

function fixture(options = {}) {
  const calls = []
  let globalHangups = 0
  class FakeCall {
    constructor(instanceId, peer, event, spec) {
      Object.assign(this, { instanceId, peer, event, spec, started: 0, closed: 0, hungup: 0, callId: '' })
      calls.push(this)
    }
    start() { this.started += 1 }
    closeLocal() { this.closed += 1; return Promise.resolve() }
    hangup() { this.hungup += 1; return Promise.resolve() }
  }
  const controller = new CellularIncomingController({
    Call: FakeCall, clearMs: 0,
    api: { cellularCallHangup: async () => { globalHangups += 1; return { termination_pending: true } } },
    ...options,
  })
  return { controller, calls, globalHangups: () => globalHangups }
}
function incoming(id = 10, peer = '+123') {
  return { type: 'call', instance: '5', call: {
    id, direction: 'in', status: 'ringing', transport: 'cellular', peer,
  } }
}
function answered(id, owner = '') {
  return { ...incoming(id), call: { ...incoming(id).call, status: 'answered', cellular_owner_call_id: owner } }
}

{
  const { controller, calls } = fixture()
  controller.handleMessage(incoming())
  controller.handleMessage(incoming())
  assert.equal(calls.length, 0, 'ringing never prepares Agent audio or opens the microphone')
  assert.equal(controller.answer(), true)
  assert.equal(controller.answer(), true)
  assert.equal(calls.length, 1)
  assert.equal(calls[0].started, 1)
  assert.deepEqual(calls[0].spec.sourceCallId, 10)
  assert.equal(calls[0].spec.direction, 'inbound')
  assert.equal(controller.state.state, 'incoming', 'click is not proof that ATA succeeded')
  calls[0].event('calling')
  assert.equal(controller.state.state, 'answering')
  calls[0].callId = 'owner-a'
  controller.handleMessage(answered(10, 'owner-a'))
  assert.equal(controller.state.state, 'active')
  calls[0].event('calling')
  assert.equal(controller.state.state, 'active', 'late HTTP answer response cannot demote an answered broadcast')
  controller.stop()
}
{
  const a = fixture(), b = fixture()
  a.controller.handleMessage(incoming(11)); b.controller.handleMessage(incoming(11))
  a.controller.answer(); b.controller.answer()
  a.calls[0].callId = 'winner'; b.calls[0].callId = 'loser'
  a.controller.handleMessage(answered(11, 'winner')); b.controller.handleMessage(answered(11, 'winner'))
  assert.equal(a.controller.state.state, 'active')
  assert.equal(b.controller.state.state, 'ended')
  assert.equal(b.controller.state.endCause, 'Answered elsewhere')
  assert.equal(b.calls[0].closed, 1)
  assert.equal(a.globalHangups() + b.globalHangups(), 0, 'loser must not send physical Hangup')
  a.controller.stop(); b.controller.stop()
}
{
  const { controller, calls, globalHangups } = fixture()
  controller.handleMessage(incoming(12)); controller.answer()
  calls[0].event('failed', { cause: 'Microphone denied', committed: false })
  assert.equal(controller.state.state, 'incoming')
  assert.equal(controller.state.busy, false)
  assert.equal(globalHangups(), 0)
  controller.answer()
  assert.equal(calls.length, 2, 'explicit local retry gets a new per-call owner')
  calls[0].event('active')
  assert.notEqual(controller.state.state, 'active', 'stale audio callback cannot claim a retried call')
  controller.stop()
  assert.equal(calls[1].closed, 1)
  assert.equal(globalHangups(), 0)
}
{
  const { controller, calls, globalHangups } = fixture()
  controller.handleMessage(incoming(13)); controller.answer()
  calls[0].event('failed', { cause: 'occupied', status: 409, committed: false })
  assert.equal(controller.state.phase, 'occupied')
  assert.equal(globalHangups(), 0)
  controller.handleMessage(answered(13, 'another'))
  assert.equal(controller.state.state, 'ended')
  controller.stop()
}
{
  const { controller, globalHangups } = fixture()
  controller.handleMessage(incoming(14)); controller.decline(); controller.decline()
  assert.equal(globalHangups(), 1)
  await tick()
  assert.equal(controller.state.state, 'ending', 'accepted physical decline is not a confirmed terminal call')
  controller.handleMessage({ ...incoming(14), call: { ...incoming(14).call, status: 'ended', end_ts: 1 } })
  await tick()
  controller.handleMessage(incoming(14))
  assert.equal(controller.state, null, 'late same-source ringing cannot resurrect an ended call')
  controller.handleMessage(incoming(15))
  assert.equal(controller.state.sourceCallId, 15)
  controller.handleMessage(answered(14, 'stale'))
  assert.equal(controller.state.sourceCallId, 15)
  controller.stop()
}
{
  const { controller, calls, globalHangups } = fixture()
  controller.handleMessage(incoming(16)); controller.answer()
  calls[0].event('calling'); controller.stop()
  assert.equal(calls[0].closed, 1)
  assert.equal(globalHangups(), 0, 'unmount only closes its owned call')
  calls[0].event('active')
  assert.equal(controller.state, null)
}
console.log('Cellular incoming ownership, multi-tab and teardown tests passed')
