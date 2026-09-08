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

export function systemSettingsView(preferences = {}, notifications = {}, status = {}, catalog = {}, web = {}, host = {}) {
  const audio = preferences.preferences?.call_audio_buffer_ms
  let bind = '', port
  if (web.settings?.listen || status.public?.listen) {
    try {
      const listen = web.settings?.listen || status.public.listen
      const address = new URL(`https://${listen.startsWith(':') ? '0.0.0.0' + listen : listen}`)
      bind = listen.startsWith(':') ? '' : address.hostname
      port = Number(address.port || 443)
    } catch { /* An unknown listener is not a fabricated address or port. */ }
  }
  return {timezone:notifications.timezone,cellular_audio_buffer_ms:audio,
	__hardware_supported:/^[a-f0-9]{64}$/.test(host.revision || '') && ['auto','serial'].includes(host.settings?.modem_backend),
	__hardware_revision:host.revision,__hardware_runtime:host.runtime_state || 'not_observed',
	hardware:{modem_backend:host.settings?.modem_backend || '',modem_profiles:structuredClone(host.settings?.modem_profiles || [])},
	__device_defaults_supported:preferences.new_device_defaults_supported===true,
	device_defaults:{cellular_enabled:preferences.preferences?.new_device_defaults?.connection_enabled ?? false,
		vowifi_enabled:preferences.preferences?.new_device_defaults?.vowifi_enabled ?? true,
		flight_mode:preferences.preferences?.new_device_defaults?.flight_mode ?? false,
		roaming_enabled:preferences.preferences?.new_device_defaults?.roaming_enabled ?? false},
    __web_supported:web.schema_version===1 && /^[a-f0-9]{64}$/.test(web.revision || ''),
    __web_revision:web.revision,__web_restart_required:web.restart_required===true,
    updates:preferences.preferences?.updates ? {...preferences.preferences.updates} : undefined,
    __saved_updates:preferences.preferences?.updates ? {...preferences.preferences.updates} : undefined,
    __updates_supported:preferences.revision>0 && ['auto','direct','library'].includes(preferences.preferences?.updates?.proxy_mode),
    security:{audit_enabled:preferences.preferences?.audit_enabled,trusted_proxies:preferences.preferences?.trusted_proxies || []},
    __security_supported:preferences.revision>0 && typeof preferences.preferences?.audit_enabled==='boolean' && Array.isArray(preferences.preferences?.trusted_proxies),
    __general_supported:notifications.revision > 0 && typeof notifications.timezone === 'string',
    __voice_supported:preferences.revision > 0 && Number.isInteger(audio),
    ring_timeout:preferences.preferences?.ring_timeout_seconds,
    __ring_timeout_supported:Number.isInteger(preferences.preferences?.ring_timeout_seconds),
    retry:preferences.preferences?.retry ? {...preferences.preferences.retry} : undefined,
    __retry_supported:Number.isSafeInteger(preferences.preferences?.retry?.max) && preferences.preferences.retry.max>=1 && Number.isSafeInteger(preferences.preferences?.retry?.interval) && preferences.preferences.retry.interval>=5,
    rekey:{minutes:catalog.defaults?.rekey_minutes},__catalog_revision:catalog.revision,
    __saved_rekey_minutes:catalog.defaults?.rekey_minutes,
    __rekey_supported:Number.isInteger(catalog.defaults?.rekey_minutes),
    bind,http_port:port,tls:{fingerprint:status.public?.tls_fingerprint_sha256 || '',self_signed:status.public?.certificate?.self_signed,
      domain:(status.public?.certificate?.dns_names || []).join(', '),not_after:status.public?.certificate?.not_after,
      cert_path:web.settings?.tls_cert || '',key_path:web.settings?.tls_key || ''},
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
    const sources = ['Audio settings', 'Notification settings', 'Runtime information', 'Line defaults', 'Proxy library','Web access']
    const results = await Promise.allSettled([go.systemPreferences(),go.notificationConfig(),go.systemRuntime(),go.catalogLines(),go.egressConfig(),go.webSettings(),go.hostModemSettings()])
    const unauthorized = results.find(result => result.status === 'rejected' && [401,403].includes(result.reason?.status))
    if (unauthorized) throw unauthorized.reason
    const view = systemSettingsView(...results.slice(0,4).map(result => result.status === 'fulfilled' ? result.value : {}),results[5].status==='fulfilled'?results[5].value:{},results[6].status==='fulfilled'?results[6].value:{})
    view.__hardware_error=results[6].status==='rejected' ? results[6].reason?.code || 'host_modem_unavailable' : ''
    view.proxy={profiles:Object.fromEntries(Object.entries(results[4].status==='fulfilled' ? results[4].value.config?.profiles || {} : {})
      .filter(([,profile])=>['socks5','node','subscription','existing'].includes(profile.type)).map(([id,profile])=>[id,{name:profile.name,type:profile.type}]))}
    const savedProfile=view.updates?.proxy_profile_id
    if(savedProfile&&!view.proxy.profiles[savedProfile])view.proxy.profiles[savedProfile]={name:savedProfile,unavailable:true}
    if (view.__voice_supported) cacheCallAudioBufferMS(view.cellular_audio_buffer_ms)
    view.__load_errors = results.flatMap((result,index) => result.status === 'rejected' && index!==6 && !(index===5 && result.reason?.status===404)
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
	if(domain==='hardware'){
		if(!draft.__hardware_supported || !['auto','serial'].includes(draft.hardware?.modem_backend))throw new Error('host_modem_unavailable')
		const result=await go.saveHostModemSettings({expected_revision:draft.__hardware_revision,settings:{modem_backend:draft.hardware.modem_backend,modem_profiles:structuredClone(draft.hardware.modem_profiles)}})
		return {...draft,hardware:structuredClone(result.settings),__hardware_revision:result.revision,__hardware_runtime:result.runtime_state || 'not_observed'}
	}
	if(domain==='device-defaults'){
		if(!draft.__device_defaults_supported)throw new Error('device_defaults_unavailable')
		const value=draft.device_defaults
		if(!value || ['cellular_enabled','vowifi_enabled','flight_mode','roaming_enabled'].some(key=>typeof value[key]!=='boolean'))throw new Error('invalid_device_defaults')
		const result=await go.saveSystemPreferences(draft.__preference_revision,{new_device_defaults:{connection_enabled:value.cellular_enabled,vowifi_enabled:value.vowifi_enabled,flight_mode:value.flight_mode,roaming_enabled:value.roaming_enabled}})
		return {...draft,__preference_revision:result.revision,device_defaults:{cellular_enabled:result.preferences.new_device_defaults.connection_enabled,vowifi_enabled:result.preferences.new_device_defaults.vowifi_enabled,flight_mode:result.preferences.new_device_defaults.flight_mode,roaming_enabled:result.preferences.new_device_defaults.roaming_enabled}}
	}
    if(domain==='web'){
      if(!draft.__web_supported)throw new Error('web_settings_unavailable')
      const port=Number(draft.http_port),host=String(draft.bind || '').trim()
      if(!Number.isInteger(port)||port<1||port>65535||!draft.tls?.cert_path||!draft.tls?.key_path)throw new Error('invalid_web_settings')
      const listen=`${host.includes(':')&&!host.startsWith('[')?`[${host}]`:host}:${port}`
      const result=await go.saveWebSettings({schema_version:1,expected_revision:draft.__web_revision,settings:{listen,tls_cert:draft.tls.cert_path.trim(),tls_key:draft.tls.key_path.trim()}})
      return {...draft,__web_revision:result.revision,__web_restart_required:result.restart_required===true}
    }
    if(domain==='backup'){
      const selection=draft.updates
      if(!draft.__updates_supported||!['auto','direct','library'].includes(selection?.proxy_mode))throw new Error('update_network_settings_unavailable')
      if(selection.proxy_mode==='library'&&(!selection.proxy_profile_id||!draft.proxy?.profiles?.[selection.proxy_profile_id]||draft.proxy.profiles[selection.proxy_profile_id].unavailable))throw new Error('selected_update_proxy_unavailable')
      const updates={proxy_mode:selection.proxy_mode,proxy_profile_id:selection.proxy_mode==='library'?selection.proxy_profile_id:''}
      const saved=await go.saveSystemPreferences(draft.__preference_revision,{updates})
      return {...draft,__preference_revision:saved.revision,updates:{...saved.preferences.updates},__saved_updates:{...saved.preferences.updates}}
    }
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
      if (draft.__retry_supported) {
        if(!Number.isSafeInteger(draft.retry?.max)||draft.retry.max<1||!Number.isSafeInteger(draft.retry?.interval)||draft.retry.interval<5)throw new Error('invalid_retry_window')
        patch.retry={max:draft.retry.max,interval:draft.retry.interval}
      }
      if (draft.__ring_timeout_supported) {
        const seconds=Number(draft.ring_timeout)
        if (!Number.isInteger(seconds) || seconds<5 || seconds>180) throw new Error('invalid_ring_timeout_seconds')
        patch.ring_timeout_seconds=seconds
      }
      const saved = await go.saveSystemPreferences(draft.__preference_revision,patch)
      cacheCallAudioBufferMS(saved.preferences.call_audio_buffer_ms)
      return {...draft,__preference_revision:saved.revision,cellular_audio_buffer_ms:saved.preferences.call_audio_buffer_ms,
        retry:saved.preferences.retry ? {...saved.preferences.retry} : draft.retry,
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
