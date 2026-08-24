import assert from 'node:assert/strict'
import {
  boundedCellularRelease,
  refreshCellularMediaState,
} from '../src/cellularMediaMonitor.js'

const order = []
let terminations = 0
const refreshed = await refreshCellularMediaState({
  refreshEvidence: async () => { order.push('evidence'); throw new Error('RTP stopped') },
  getStatus: async () => { order.push('status'); return { media: { phase: 'degraded' } } },
  terminate: async () => { order.push('terminate'); terminations += 1 },
})
assert.equal(refreshed.mediaRefreshError.message, 'RTP stopped')
assert.deepEqual(order, ['evidence', 'status', 'terminate'])
assert.equal(terminations, 1)

let attempts = 0
const release = await boundedCellularRelease({
  callId: 'call-1',
  release: async (callId) => {
    assert.equal(callId, 'call-1')
    attempts += 1
    if (attempts < 2) throw new Error('temporary transport loss')
    return { termination_pending: true }
  },
  delay: async () => {},
})
assert.equal(release.termination_pending, true)
assert.equal(attempts, 2)

let boundedAttempts = 0
await assert.rejects(() => boundedCellularRelease({
  callId: 'call-2', attempts: 3,
  release: async () => { boundedAttempts += 1; throw new Error('offline') },
  delay: async () => {},
}), /offline/)
assert.equal(boundedAttempts, 3)

console.log('cellular media monitor tests passed')
