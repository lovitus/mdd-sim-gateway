import { api as go, downloadSystemBackup, getBasePrefix } from '../api.js'
import {notificationSettingsView, notificationSettingsPatch} from './notificationAdapter.js'
import {cacheCallAudioBufferMS} from '../browserPreferences.js'

export function agentCredentialChange(action, agentID, mode) {
  if (!['scoped','transition'].includes(mode)) throw new Error('agent_credential_mode_unavailable')
  if (action === 'set_mode') return {payload:{action,mode:mode === 'scoped' ? 'transition' : 'scoped'},
    confirmation:mode === 'scoped'
      ? 'Re-enable the legacy shared fallback? Unknown Agent IDs will be able to authenticate with the shared token.'
      : 'Disable the shared fallback? Agents without an active scoped credential will disconnect.'}
  const id = String(agentID || '').trim()
  if (!id || !['issue','revoke','unenroll'].includes(action)) throw new Error('agent_credential_action_invalid')
  if (action === 'unenroll' && mode !== 'transition') throw new Error('agent_fallback_requires_transition_mode')
  return {payload:{action,agent_id:id},confirmation:action === 'revoke'
    ? 'Revoke credential for {agent}? Active sessions will disconnect.'
    : action === 'unenroll' ? 'Return Agent {agent} to the legacy shared fallback?'
    : 'Issue or rotate credential for {agent}? Its Agent configuration must be updated.'}
}

export function systemSettingsView(preferences = {}, notifications = {}, status = {}, catalog = {}) {
  const audio = preferences.preferences?.call_audio_buffer_ms
  let bind = '', port
  if (status.public?.listen) {
    try {
      const listen = status.public.listen
      const address = new URL(`https://${listen.startsWith(':') ? '0.0.0.0' + listen : listen}`)
      bind = listen.startsWith(':') ? '' : address.hostname
      port = Number(address.port || 443)
    } catch { /* An unknown listener is not a fabricated address or port. */ }
  }
  return {timezone:notifications.timezone,cellular_audio_buffer_ms:audio,
    security:{audit_enabled:preferences.preferences?.audit_enabled,trusted_proxies:preferences.preferences?.trusted_proxies || []},
    __security_supported:preferences.revision>0 && typeof preferences.preferences?.audit_enabled==='boolean' && Array.isArray(preferences.preferences?.trusted_proxies),
    __general_supported:notifications.revision > 0 && typeof notifications.timezone === 'string',
    __voice_supported:preferences.revision > 0 && Number.isInteger(audio),
    ring_timeout:preferences.preferences?.ring_timeout_seconds,
    __ring_timeout_supported:Number.isInteger(preferences.preferences?.ring_timeout_seconds),
    rekey:{minutes:catalog.defaults?.rekey_minutes},__catalog_revision:catalog.revision,
    __saved_rekey_minutes:catalog.defaults?.rekey_minutes,
    __rekey_supported:Number.isInteger(catalog.defaults?.rekey_minutes),
    bind,http_port:port,tls:{fingerprint:status.public?.tls_fingerprint_sha256 || ''},
    __preference_revision:preferences.revision,__notifications:notifications,
  }
}

export function hostView(snapshot) {
  const value = key => snapshot[key]?.state === 'available' ? snapshot[key].value || {} : {}
  const host = value('host'), cpu = value('cpu'), load = value('load'), memory = value('memory'), swap = value('swap'), disk = value('disk')
  const mb = bytes => Number.isFinite(bytes) ? bytes / (1024 * 1024) : undefined
  return { model:cpu.model, uptime_seconds:host.uptime_seconds, cpu_mhz:cpu.mhz,
    load:{'1m':load.one_minute,per_core:load.one_minute_per_core,cores:cpu.logical_cores},
    memory:{total_mb:mb(memory.total_bytes),used_percent:memory.used_percent,swap_total_mb:mb(swap.total_bytes),swap_used_mb:mb(swap.used_bytes),swap_used_percent:swap.used_percent},
    disk:{total_mb:mb(disk.total_bytes),free_mb:mb(disk.free_bytes),used_percent:disk.used_percent},
    network:{addresses:(value('network').interfaces || []).flatMap(item => (item.addresses || []).map(address => ({interface:item.name,address})))},
    source:snapshot,
  }
}

export function maintenanceLeases(snapshot) {
  const leases = new Map()
  for (const line of snapshot?.lines || []) {
    const lease = line.maintenance?.lease_id
    if (!line.maintenance?.draining || !lease) continue
    if (!leases.has(lease)) leases.set(lease, [])
    leases.get(lease).push(line.line_id)
  }
  return [...leases].map(([lease_id, line_ids]) => ({lease_id,line_ids:line_ids.sort()}))
}

export function maintenanceRequest(snapshot, action, leaseID) {
  if (!['begin', 'resume'].includes(action) || !leaseID || !snapshot?.catalog_revision) throw new Error('maintenance_identity_required')
  const lineIDs = action === 'resume'
    ? maintenanceLeases(snapshot).find(lease => lease.lease_id === leaseID)?.line_ids || []
    : (snapshot.lines || []).filter(line => line.provider_present && !line.maintenance?.draining).map(line => line.line_id)
  if (!lineIDs.length) throw new Error('maintenance_lines_unavailable')
  return {schema_version:1,catalog_revision:snapshot.catalog_revision,lease_id:leaseID,line_ids:lineIDs}
}

export const systemAPI = {
  async settings() {
    const sources = ['Audio settings', 'Notification settings', 'Runtime information', 'Line defaults']
    const results = await Promise.allSettled([go.systemPreferences(),go.notificationConfig(),go.systemRuntime(),go.catalogLines()])
    const unauthorized = results.find(result => result.status === 'rejected' && [401,403].includes(result.reason?.status))
    if (unauthorized) throw unauthorized.reason
    const view = systemSettingsView(...results.map(result => result.status === 'fulfilled' ? result.value : {}))
    if (view.__voice_supported) cacheCallAudioBufferMS(view.cellular_audio_buffer_ms)
    view.__load_errors = results.flatMap((result,index) => result.status === 'rejected'
      ? [{source:sources[index],code:result.reason?.code || result.reason?.message || 'settings_unavailable'}] : [])
    return view
  },
  async saveRekeySettings(draft) {
    const minutes=Number(draft.rekey?.minutes)
    if (!draft.__rekey_supported || !draft.__catalog_revision || !Number.isInteger(minutes) || minutes<0 || minutes>1440) throw new Error('invalid_rekey_default')
    const result=await go.saveProviderDefaults({rekey_minutes:minutes},draft.__catalog_revision)
    return {...draft,__catalog_revision:result.revision,rekey:{minutes:result.defaults.rekey_minutes},__saved_rekey_minutes:result.defaults.rekey_minutes}
  },
  async saveSettings(draft, domain) {
    if (domain === 'security') {
      if(!draft.__security_supported || typeof draft.security?.audit_enabled!=='boolean' || !Array.isArray(draft.security?.trusted_proxies)) throw new Error('audit_settings_unavailable')
      const saved=await go.saveSystemPreferences(draft.__preference_revision,{audit_enabled:draft.security.audit_enabled,trusted_proxies:draft.security.trusted_proxies})
      return {...draft,__preference_revision:saved.revision,security:{audit_enabled:saved.preferences.audit_enabled,trusted_proxies:saved.preferences.trusted_proxies}}
    }
    if (domain === 'general') {
      if (!draft.__general_supported) throw new Error('notification_settings_unavailable')
      const patch = notificationSettingsPatch({...notificationSettingsView(draft.__notifications),timezone:draft.timezone})
      const notifications = await go.saveNotificationConfig(patch)
      return {...draft,timezone:notifications.timezone,__notifications:notifications}
    }
    if (domain === 'voice') {
      if (!draft.__voice_supported) throw new Error('audio_settings_unavailable')
      const value = Number(draft.cellular_audio_buffer_ms)
      if (!Number.isInteger(value) || value < 100 || value > 2000) throw new Error('invalid_call_audio_buffer_ms')
      const patch={call_audio_buffer_ms:value}
      if (draft.__ring_timeout_supported) {
        const seconds=Number(draft.ring_timeout)
        if (!Number.isInteger(seconds) || seconds<5 || seconds>180) throw new Error('invalid_ring_timeout_seconds')
        patch.ring_timeout_seconds=seconds
      }
      const saved = await go.saveSystemPreferences(draft.__preference_revision,patch)
      cacheCallAudioBufferMS(saved.preferences.call_audio_buffer_ms)
      return {...draft,__preference_revision:saved.revision,cellular_audio_buffer_ms:saved.preferences.call_audio_buffer_ms,
        ring_timeout:saved.preferences.ring_timeout_seconds,__ring_timeout_supported:Number.isInteger(saved.preferences.ring_timeout_seconds)}
    }
    throw new Error('system_setting_not_writable')
  },
  supportBundleUrl: getBasePrefix() + '/v1/diagnostics/support-bundle',
  async diagnosticSystemStatus() {
    const [status, alerts] = await Promise.all([go.systemStatus(),go.hostAlerts()])
    return {...status,host:hostView(status),host_alerts:(alerts.alerts || []).filter(alert => !alert.acknowledged)}
  },
  async clearHostAlerts(alerts) {
    for (const alert of alerts) {
      if (!alert.key || !alert.occurrence) throw new Error('host_alert_identity_required')
    }
    await Promise.all(alerts.map(alert => go.acknowledgeHostAlert(alert)))
    return systemAPI.diagnosticSystemStatus()
  },
  async register(lineID) {
    const catalog = await go.catalogLines()
    const line = (catalog.lines || []).find(value => String(value.id) === String(lineID))
    if (!line?.card_id) throw new Error('line_card_identity_unavailable')
    return go.registerV1(lineID, line.card_id)
  },
  async createBackup() {
    const blob = await downloadSystemBackup()
    const url = URL.createObjectURL(blob)
    try {
      const link = document.createElement('a')
      link.href = url; link.download = 'mdd-state-backup.zip'; link.click()
    } finally { setTimeout(() => URL.revokeObjectURL(url), 1000) }
    return { ok:true }
  },
}
