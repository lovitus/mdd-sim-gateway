import assert from 'node:assert/strict'
import fs from 'node:fs'

const coordinator = fs.readFileSync(new URL('../src/goCallCoordinator.jsx', import.meta.url), 'utf8')
const api = fs.readFileSync(new URL('../src/api.js', import.meta.url), 'utf8')
const app = fs.readFileSync(new URL('../src/mdd/App.jsx', import.meta.url), 'utf8')

assert.ok(coordinator.includes("message.raw?.cellular_calls"),
  'cellular ringing state must come from the authenticated browser snapshot')
assert.ok(coordinator.includes("call.state === 'ringing_in'"))
assert.ok(coordinator.includes("incoming.actionable !== true"))
for (const fence of [
  'incoming_event_id: incoming.incoming_event_id',
  'sim_session_generation: incoming.sim_session_generation',
  'native_call_index: incoming.native_call_index',
  'call_occurrence: incoming.occurrence',
]) assert.ok(coordinator.includes(fence), `missing incoming fence ${fence}`)
assert.ok(coordinator.includes("if (currentRef.current || mediaProbeRef.current) throw new Error"),
  'one browser cannot prepare another call or media probe while it owns one')
assert.ok(coordinator.includes("mode === 'cellular' && incoming.actionable !== true"))
assert.ok(coordinator.includes('MDD will not blindly repeat CHUP'))
assert.ok(api.includes('/cellular/calls/reject'))
assert.ok(api.includes('/vowifi/calls/incoming/reject'))
assert.ok(app.includes('<GlobalGoCallOverlay coordinator={callCoordinator}'))
assert.ok(coordinator.includes("incoming.call.actionable !== true"))
assert.ok(!coordinator.includes('canConfirmRoute'))
assert.ok(!coordinator.includes('mediaIngress'))

console.log('Go multi-browser incoming-call exact-claim and fail-closed reject contracts passed')
