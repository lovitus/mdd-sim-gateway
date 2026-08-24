import assert from 'node:assert/strict'
import { liveStatusFromWsMessage } from '../src/liveStatus.js'

const status = liveStatusFromWsMessage({
  type: 'status',
  instance: '5',
  state: 'OK',
  label: 'Working',
  reason_code: 'ok',
})
assert.deepEqual(status, {
  state: 'OK',
  label: 'Working',
  reason_code: 'ok',
})

const engine = {
  type: 'engine',
  instance: '5',
  event: 'runtime_changed',
  status_transition: {
    state: 'STOPPED',
    label: 'Stopped',
    reason_code: 'engine_stopped',
    reason: 'The VoWiFi engine stopped; refreshing line status.',
    detail: { engine_generation: 'abc' },
  },
}
const transition = liveStatusFromWsMessage(engine)
assert.equal(transition.state, 'STOPPED')
assert.equal(transition.reason_code, 'engine_stopped')
assert.notEqual(transition, engine.status_transition)

assert.equal(liveStatusFromWsMessage({
  type: 'engine',
  instance: '5',
  event: 'runtime_changed',
}), null)
assert.equal(liveStatusFromWsMessage({
  type: 'engine',
  status_transition: { state: 'STOPPED' },
}), null)
