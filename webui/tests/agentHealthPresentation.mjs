import assert from 'node:assert/strict'
import { agentHealthPresentation, agentHeartbeatAge, agentHealthEnumLabel } from '../src/agentHealthPresentation.js'

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

console.log('Agent health presentation tests passed')
