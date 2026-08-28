import assert from 'node:assert/strict'
import { agentHealthPresentation, agentHeartbeatAge, agentHealthEnumLabel, normalizeCoreAgentHealth } from '../src/agentHealthPresentation.js'

const agent = (connection, overall = 'healthy', reporting = true) => ({
  reporting, connection, snapshot: { overall },
})

assert.deepEqual(agentHealthPresentation(agent('fresh'), 'zh'), {
  state: 'on', label: '在线 · 正常',
})
assert.equal(agentHealthPresentation(agent('delayed'), 'en').state, 'degraded')
assert.equal(agentHealthPresentation(agent('offline'), 'en').state, 'error')
assert.equal(agentHealthPresentation(agent('stopped'), 'en').state, 'off')
assert.equal(agentHealthPresentation(agent('fresh', 'unsupported'), 'zh').label,
  '在线 · Linux 健康采集尚未实现')
assert.equal(agentHealthPresentation(agent('unreported', 'healthy', false), 'en').state,
  'unsupported')

const now = 1_000_000
assert.equal(agentHeartbeatAge(995, now, 'zh'), '刚刚')
assert.equal(agentHeartbeatAge(970, now, 'en'), '30s ago')
assert.equal(agentHeartbeatAge(800, now, 'zh'), '3 分钟前')
assert.equal(agentHeartbeatAge(null, now, 'en'), '—')
assert.equal(agentHealthEnumLabel('runtime', 'cleanup_blocked', 'zh'), '正在安全收敛通话')
assert.equal(agentHealthEnumLabel('storage', 'critical', 'zh'), '空间严重不足')
assert.equal(agentHealthEnumLabel('isolation', 'ok', 'zh'), '正常')
assert.equal(agentHealthEnumLabel('manager', 'scm', 'zh'), 'Windows 服务')

const core = normalizeCoreAgentHealth({
  agent_id: 'agent-1',
  process_generation: 'process-1',
  last_seen: '2026-08-28T12:00:10Z',
  last_report: '2026-08-28T12:00:08Z',
  topology: {
    reader_condition: 'ready',
    readers: [{ reader_name: 'Reader A', card_present: true, card_id: '89440001', identity_state: 'identified' }],
  },
}, '2026-08-28T12:00:10Z')
assert.equal(core.id, 'agent-1')
assert.equal(core.connection, 'fresh')
assert.equal(core.snapshot.overall, 'healthy')
assert.equal(core.attachments.readers_online, 1)
assert.equal(core.topology.readers[0].card_id, '89440001')

const delayed = normalizeCoreAgentHealth({
  agent_id: 'agent-2',
  last_seen: '2026-08-28T12:00:10Z',
  last_report: '2026-08-28T11:59:00Z',
  topology: { reader_condition: 'recovering', reader_detail: 'PC/SC unavailable', readers: [] },
}, '2026-08-28T12:00:10Z')
assert.equal(delayed.connection, 'delayed')
assert.equal(delayed.snapshot.overall, 'degraded')
assert.equal(delayed.snapshot.runtime.last_error_code, 'PC/SC unavailable')

console.log('Agent health presentation tests passed')
