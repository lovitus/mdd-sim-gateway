const CORE_AGENT_REPORT_STALE_MS = 30_000

export function agentHealthPresentation(agent, language = 'en') {
  const isZh = language === 'zh'
  const connection = String(agent?.connection || 'unreported')
  const overall = String(agent?.snapshot?.overall || 'unsupported')
  if (!agent?.reporting || connection === 'unreported') return {
    state: 'unsupported',
    label: isZh ? '此版本未上报健康状态' : 'Health not reported by this version',
  }
  if (connection === 'delayed') return {
    state: 'degraded', label: isZh ? '心跳延迟' : 'Heartbeat delayed',
  }
  if (connection === 'offline') return {
    state: 'error', label: isZh ? '离线' : 'Offline',
  }
  if (connection === 'stopped' || overall === 'stopped') return {
    state: 'off', label: isZh ? '已停止' : 'Stopped',
  }
  if (overall === 'unsupported') return {
    state: 'unsupported',
    label: isZh
      ? '在线 · Linux 健康采集尚未实现'
      : 'Online · Linux health collection not implemented',
  }
  if (overall === 'healthy') return {
    state: 'on', label: isZh ? '在线 · 正常' : 'Online · Healthy',
  }
  if (overall === 'starting' || overall === 'stopping') return {
    state: 'starting',
    label: isZh
      ? `在线 · ${overall === 'starting' ? '正在启动' : '正在停止'}`
      : `Online · ${overall}`,
  }
  return {
    state: overall === 'failed' ? 'error' : 'degraded',
    label: isZh ? '在线 · 需要处理' : 'Online · Needs attention',
  }
}

export function normalizeCoreAgentHealth(agent, snapshotAt) {
  if (!agent?.agent_id) return agent
  const topology = agent.topology || null
  const readers = Array.isArray(topology?.readers) ? topology.readers : []
  const atMs = Date.parse(snapshotAt || '')
  const reportMs = Date.parse(agent.last_report || '')
  const reportAge = Number.isFinite(atMs) && Number.isFinite(reportMs)
    ? Math.max(0, atMs - reportMs) : Number.POSITIVE_INFINITY
  const reporting = !!topology && Number.isFinite(reportMs)
  const connection = reporting && reportAge <= CORE_AGENT_REPORT_STALE_MS ? 'fresh' : 'delayed'
  const readerCondition = String(topology?.reader_condition || '')
  const overall = readerCondition === 'ready' ? 'healthy'
    : readerCondition === 'recovering' ? 'degraded'
      : readerCondition === 'starting' ? 'starting' : 'unsupported'
  const seenMs = Date.parse(agent.last_seen || '')
  return {
    ...agent,
    id: agent.agent_id,
    display_id: agent.agent_id,
    reporting,
    connection,
    seen_at: Number.isFinite(seenMs) ? seenMs / 1000 : null,
    meta: { platform: 'go' },
    attachments: { modems_online: 0, readers_online: readers.length },
    topology,
    snapshot: {
      overall,
      runtime: {
        state: 'online',
        ...(topology?.reader_detail ? { last_error_code: topology.reader_detail } : {}),
      },
    },
  }
}

export function agentHeartbeatAge(seenAt, now, language = 'en') {
  if (!seenAt) return '—'
  const isZh = language === 'zh'
  const seconds = Math.max(0, Math.floor(Number(now) / 1000 - Number(seenAt)))
  if (seconds < 15) return isZh ? '刚刚' : 'just now'
  if (seconds < 60) return isZh ? `${seconds} 秒前` : `${seconds}s ago`
  const minutes = Math.floor(seconds / 60)
  return isZh ? `${minutes} 分钟前` : `${minutes}m ago`
}

export function agentHealthEnumLabel(kind, value, language = 'en') {
  const raw = String(value || '')
  if (language !== 'zh') return raw || '—'
  const labels = {
    runtime: {
      starting: '正在启动', ready: '就绪', online: '运行中', stopping: '正在停止',
      stopped: '已停止', failed: '失败', cleanup_blocked: '正在安全收敛通话',
    },
    storage: { ok: '正常', warning: '空间偏低', critical: '空间严重不足', unknown: '未知' },
    isolation: { ok: '正常', error: '异常', unsupported: '尚未实现' },
    manager: { scm: 'Windows 服务', gui: '图形应用', cli: '命令行' },
  }
  return labels[kind]?.[raw] || raw || '—'
}
