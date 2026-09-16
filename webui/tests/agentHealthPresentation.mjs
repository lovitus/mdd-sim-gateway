import assert from 'node:assert/strict'
import { agentHealthPresentation, agentHeartbeatAge, agentHealthEnumLabel, normalizeCoreAgentHealth } from '../src/agentHealthPresentation.js'

const location = normalizeCoreAgentHealth({ agent_id: 'agent-a', topology: { host: {
  platform: 'linux', hostname: 'modem-host', os_name: 'Ubuntu', os_version: '24.04',
  kernel_version: '6.8', addresses: ['192.0.2.10'], build_version: 'revision-a',
} } })
assert.equal(location.meta.hostname, 'modem-host')
assert.equal(location.meta.os_version, '24.04')
assert.deepEqual(location.meta.addresses, ['192.0.2.10'])
assert.deepEqual(normalizeCoreAgentHealth({agent_id:'legacy'}).meta.addresses, [])

const agent = (connection, overall = 'healthy', reporting = true) => ({
  reporting, connection, snapshot: { overall },
})

assert.deepEqual(agentHealthPresentation(agent('fresh'), 'zh'), {
  state: 'on', label: '在线 · 正常',
})
assert.equal(agentHealthPresentation(agent('delayed'), 'en').state, 'degraded')
assert.equal(agentHealthPresentation(agent('offline'), 'en').state, 'error')
assert.equal(agentHealthPresentation({ reporting: false, connection: 'offline', snapshot: {} }, 'en').state, 'error')
assert.equal(agentHealthPresentation(agent('stopped'), 'en').state, 'off')
assert.equal(agentHealthPresentation(agent('fresh', 'unsupported'), 'zh').label,
	'在线 · 此版本未上报主机健康')
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
		host: { schema_version: 1, platform: 'macos', architecture: 'arm64', build_version: 'revision-1',
			host_mode: 'gui', manager: 'gui', session_scope: 'user', config_state: 'ok',
			token_configured: true, modem_enabled: true,
			storage: { state: 'ok', total_bytes: 1000, free_bytes: 400, used_percent: 60 } },
    reader_condition: 'ready',
    readers: [{ reader_name: 'Reader A', card_present: true, card_id: '89440001', identity_state: 'identified' }],
		modem_condition: 'ready', modems: [{ network: { data_guard: 'protected' } }],
  },
}, '2026-08-28T12:00:10Z')
assert.equal(core.id, 'agent-1')
assert.equal(core.connection, 'fresh')
assert.equal(core.snapshot.overall, 'healthy')
assert.equal(core.attachments.readers_online, 1)
assert.equal(core.attachments.modems_online, 1)
assert.equal(core.meta.platform, 'macos')
assert.equal(core.snapshot.resources.storage.used_percent, 60)
assert.equal(core.topology.readers[0].card_id, '89440001')

const legacy = normalizeCoreAgentHealth({
	agent_id: 'legacy', last_seen: '2026-08-28T12:00:10Z', last_report: '2026-08-28T12:00:08Z',
	topology: { reader_condition: 'ready', readers: [] },
}, '2026-08-28T12:00:10Z')
assert.equal(legacy.reporting, false)
assert.equal(legacy.connection, 'unreported')
assert.equal(legacy.snapshot.overall, 'unsupported')
assert.equal(agentHealthPresentation(legacy, 'en').label, 'Health not reported by this version')

const delayed = normalizeCoreAgentHealth({
  agent_id: 'agent-2',
  last_seen: '2026-08-28T12:00:10Z',
  last_report: '2026-08-28T11:59:00Z',
	topology: { host: { schema_version: 1, platform: 'linux', architecture: 'amd64', build_version: 'revision-2',
		host_mode: 'service', manager: 'systemd', session_scope: 'machine', config_state: 'ok',
		token_configured: true, modem_enabled: false,
		storage: { state: 'ok', total_bytes: 1000, free_bytes: 500, used_percent: 50 } },
		reader_condition: 'recovering', reader_detail: 'PC/SC unavailable', readers: [], modem_condition: 'disabled', modems: [] },
}, '2026-08-28T12:00:10Z')
assert.equal(delayed.connection, 'delayed')
assert.equal(delayed.snapshot.overall, 'degraded')
assert.equal(delayed.snapshot.runtime.last_error_code, 'PC/SC unavailable')

console.log('Agent health presentation tests passed')

const withoutModem = { ...core, agent_id: 'reader-host', last_report: '2026-08-28T12:00:08Z',
  topology: { ...core.topology, modems: [] } }
const noModemBefore = JSON.stringify(withoutModem)
const present = snapshot => normalizeCoreAgentHealth(snapshot, '2026-08-28T12:00:10Z')
assert.equal(present(withoutModem).snapshot.isolation.state, 'not_applicable')
assert.equal(present(withoutModem).snapshot.overall, 'healthy')
assert.equal(JSON.stringify(withoutModem), noModemBefore, 'presentation must never change retained modem policy')
assert.equal(withoutModem.topology.host.modem_enabled, true, 'removing hardware must not disable future isolation')
for (const [guard, expected] of [['protected', 'healthy'], ['failed', 'degraded'], ['unmanaged', 'degraded'], ['', 'degraded']]) {
  const reinserted = {...withoutModem, topology: {...withoutModem.topology, modems: [{network:{data_guard:guard}}]}}
  assert.equal(present(reinserted).snapshot.overall, expected, `reinserted ${guard} must be re-evaluated`)
}
const mixed = {...withoutModem, topology: {...withoutModem.topology, modems: [{network:{data_guard:'protected'}},{network:{data_guard:'unmanaged'}}]}}
assert.equal(present(mixed).snapshot.overall, 'degraded', 'one protected modem cannot hide an unprotected modem')
const diskWarning = {...withoutModem, topology:{...withoutModem.topology, host:{...core.topology.host, storage:{state:'warning',used_percent:88,free_bytes:30*1024**3}}}}
assert.deepEqual(present(diskWarning).snapshot.issues.map(issue=>issue.code), ['storage_warning'])
assert.equal(present(diskWarning).snapshot.issues[0].automatic,false)
const unknownDiscovery = {...withoutModem, topology:{...withoutModem.topology, modem_condition:'recovering',modem_detail:'probe failed'}}
assert.equal(present(unknownDiscovery).snapshot.overall,'degraded', 'failed discovery is not proof of hardware absence')
const {agentHealthPresentation: copiedPresentation} = await import('../src/mdd/agentHealthPresentation.js')
assert.equal(copiedPresentation, agentHealthPresentation, 'both UIs must use the same health logic')
