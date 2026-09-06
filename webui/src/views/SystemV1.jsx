import React, { useCallback, useEffect, useState } from 'react'
import { api, downloadSystemBackup } from '../api.js'
import { CALL_AUDIO_BUFFER_MAX_MS, CALL_AUDIO_BUFFER_MIN_MS, cacheCallAudioBufferMS, getCallAudioBufferMS, normalizeCallAudioBufferMS } from '../browserPreferences.js'
import { useI18n } from '../i18n.jsx'
import { agentHealthEnumLabel, agentHealthPresentation, agentHeartbeatAge } from '../agentHealthPresentation.js'

function fmtBytes(value) {
  const number = Number(value || 0)
  if (!number) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  const index = Math.min(units.length - 1, Math.floor(Math.log(number) / Math.log(1024)))
  return `${(number / 1024 ** index).toFixed(index ? 1 : 0)} ${units[index]}`
}

function Value({ label, children }) {
  return <div className="u-detail"><span>{label}</span><b>{children ?? '—'}</b></div>
}

export default function SystemV1({ showToast, setSystemMeta, setCallAudioBufferMS: setGlobalCallAudioBufferMS }) {
  const { t, language, setLanguage } = useI18n()
  const [value, setValue] = useState(null)
  const [password, setPassword] = useState({ current: '', next: '', confirm: '' })
  const [callAudioBufferMS, setCallAudioBufferMS] = useState(getCallAudioBufferMS)
	const [preferenceRevision, setPreferenceRevision] = useState(0)
  const [busy, setBusy] = useState(false)
	const [maintenance, setMaintenance] = useState(null)
	const [agentCredentials, setAgentCredentials] = useState(null)
	const [connectedAgents, setConnectedAgents] = useState([])
	const [agentID, setAgentID] = useState('')
	const [issuedCredential, setIssuedCredential] = useState(null)
  const load = useCallback(() => api.systemStatus().then(result => {
    setValue(result); setSystemMeta?.(result)
  }).catch(error => showToast(error.message)), [setSystemMeta, showToast])
	const loadAgentState = useCallback(() => Promise.all([api.authAgentCredentials(), api.agentHealth()]).then(([credentials, health]) => {
		setAgentCredentials(credentials); setConnectedAgents(health.agents || [])
	}).catch(error => showToast(error.message)), [showToast])
	useEffect(() => {
		void load()
		void loadAgentState()
		void api.systemMaintenanceStatus().then(setMaintenance).catch(() => {})
		void api.systemPreferences().then(result => {
			const buffer = cacheCallAudioBufferMS(result.preferences?.call_audio_buffer_ms)
			setCallAudioBufferMS(buffer); setGlobalCallAudioBufferMS?.(buffer); setPreferenceRevision(Number(result.revision || 0))
		}).catch(error => showToast(error.message))
	}, [load, loadAgentState, showToast, setGlobalCallAudioBufferMS])
  const changePassword = async () => {
    if (!password.next || password.next !== password.confirm) { showToast(t('Passwords do not match')); return }
    setBusy(true)
    try { await api.authPassword(password.current, password.next); window.location.reload() }
    catch (error) { showToast(error.message) } finally { setBusy(false) }
  }
	const saveAudio = async () => {
		if (!preferenceRevision || busy) return
		setBusy(true)
		try {
			const wanted = normalizeCallAudioBufferMS(callAudioBufferMS)
			const updated = await api.saveSystemPreferences(preferenceRevision, { call_audio_buffer_ms: wanted })
			const saved = cacheCallAudioBufferMS(updated.preferences?.call_audio_buffer_ms)
			setCallAudioBufferMS(saved); setGlobalCallAudioBufferMS?.(saved); setPreferenceRevision(Number(updated.revision || 0)); showToast(t('Saved'))
		} catch (error) { showToast(error.message) } finally { setBusy(false) }
  }
  const downloadBackup = async () => {
    try {
      const blob = await downloadSystemBackup(); const url = URL.createObjectURL(blob); const link = document.createElement('a'); link.href = url; link.download = 'mdd-state-backup.zip'; link.click(); URL.revokeObjectURL(url); showToast(t('Durable state backup downloaded'))
    } catch (error) { showToast(error.message) }
  }
	const updateAgentCredential = async (action, selectedAgentID = '') => {
		const normalized = String(selectedAgentID || agentID).trim()
		if (action !== 'set_mode' && !normalized) { showToast(t('Enter an Agent ID')); return }
		if (action === 'revoke' && !window.confirm(t('Revoke this Agent credential and disconnect its active sessions?'))) return
		if (action === 'unenroll' && !window.confirm(t('Return this Agent to the legacy shared fallback?'))) return
		setBusy(true)
		try {
			const payload = action === 'set_mode'
				? { action, mode: agentCredentials?.mode === 'scoped' ? 'transition' : 'scoped' }
				: { action, agent_id: normalized }
			if (action === 'set_mode' && !window.confirm(t(payload.mode === 'scoped'
				? 'Disable the shared fallback? Agents without an active scoped credential will disconnect.'
				: 'Re-enable the legacy shared fallback? Unknown Agent IDs will be able to authenticate with the shared token.'))) return
			const result = await api.updateAgentCredentials(payload)
			setAgentCredentials(result.credentials || await api.authAgentCredentials())
			setConnectedAgents(current => action === 'set_mode' ? [] : current.filter(agent => agent.agent_id !== normalized))
			if (result.agent_token) setIssuedCredential({ agentID: normalized, token: result.agent_token })
			showToast(t(action === 'issue' ? 'Agent credential issued. Restart that Agent with the new token.' : action === 'revoke' ? 'Agent credential revoked.' : action === 'unenroll' ? 'Agent returned to the transition fallback.' : 'Agent credential mode updated.'))
		} catch (error) { showToast(error.message) } finally { setBusy(false) }
	}
	const copyIssuedCredential = async () => {
		try { await navigator.clipboard.writeText(issuedCredential?.token || ''); showToast(t('Copied')) }
		catch { showToast(t('Clipboard unavailable')) }
	}
  const runMaintenance = async action => {
    if (!window.confirm(t(action === 'begin' ? 'Drain all active VoWiFi providers for maintenance?' : 'Resume all drained VoWiFi providers?'))) return
    setBusy(true)
    try {
      const catalog = await api.catalogLines()
      const lines = (catalog.lines || []).filter(line => line.enabled).map(line => line.id)
      if (!lines.length) throw new Error(t('No enabled lines are available for maintenance.'))
      const leaseID = maintenance?.lease_id || `browser-maintenance-${Date.now()}`
      const result = await api.systemMaintenance(action, { schema_version: 1, catalog_revision: Number(catalog.revision), lease_id: leaseID, line_ids: lines })
      setMaintenance(result); showToast(t(action === 'begin' ? 'Provider maintenance drain requested' : 'Provider maintenance resume requested'))
    } catch (error) { showToast(error.message) } finally { setBusy(false) }
  }
  if (!value) return <p>{t('Loading…')}</p>
  const provenance = value.provenance || {}
  const host = value.host?.value || {}
  const memory = value.memory?.value || {}
  const disk = value.disk?.value || {}
  const systemd = value.systemd?.value || {}
  const interfaces = value.network?.value?.interfaces || []
  const credentialIDs = [...new Set([...(agentCredentials?.active || []), ...(agentCredentials?.revoked || []), ...connectedAgents.map(agent => agent.agent_id)])].sort()
	const connectedByID = new Map(connectedAgents.map(agent => [agent.agent_id, agent]))
	const agentHealthRows = credentialIDs.map(id => connectedByID.get(id) || {
		agent_id: id, reporting: true, connection: 'offline', seen_at: null,
		meta: {}, snapshot: { overall: 'failed', resources: { storage: { state: 'unknown' } } },
	})
	return <div className="u-page"><div className="u-card-head"><div><h2>{t('System settings')}</h2><p>{t('This page shows the running Go runtime and host facts. It does not expose dead Python maintenance controls.')}</p></div><div className="u-inline"><button className="btn btn-ghost" disabled={busy} onClick={downloadBackup}>{t('Download durable state backup')}</button><button className="btn btn-ghost" disabled={busy} onClick={() => runMaintenance('begin')}>{t('Drain for maintenance')}</button><button className="btn btn-ghost" disabled={busy || !maintenance?.lease_id} onClick={() => runMaintenance('resume')}>{t('Resume maintenance')}</button><button className="btn btn-ghost" onClick={() => { void load(); void loadAgentState() }}>{t('Refresh')}</button></div></div>
    <div className="u-device-grid"><div className="card u-panel"><h3>{t('Runtime')}</h3><Value label={t('Version')}>{value.build_version}</Value><Value label="VCS">{value.vcs_revision}</Value><Value label="Go">{value.go_version}</Value><Value label={t('Public listener')}>{value.public?.listen}</Value><Value label={t('Transport')}>{value.public?.transport} · {value.public?.multiplexing}</Value><Value label="TLS SHA-256">{value.public?.tls_fingerprint_sha256}</Value></div>
      <div className="card u-panel"><h3>{t('Release provenance')}</h3><Value label={t('State')}>{provenance.state}</Value><Value label={t('Verified')}>{provenance.verified ? t('Yes') : t('No')}</Value><Value label="Release ID">{provenance.release_id}</Value><Value label="Core SHA-256">{provenance.core_sha256}</Value><Value label={t('Source revision')}>{provenance.source_revision || provenance.vcs_revision}</Value></div>
      <div className="card u-panel"><h3>{t('Host')}</h3><Value label={t('Platform')}>{host.platform} {host.platform_version}</Value><Value label={t('Kernel')}>{host.kernel_version} · {host.kernel_arch}</Value><Value label={t('Uptime')}>{host.uptime_seconds ? `${Math.floor(host.uptime_seconds / 3600)} h` : '—'}</Value><Value label={t('Memory')}>{memory.total_bytes ? `${fmtBytes(memory.used_bytes)} / ${fmtBytes(memory.total_bytes)} · ${memory.used_percent}%` : value.memory?.code}</Value><Value label={t('Disk')}>{disk.total_bytes ? `${fmtBytes(disk.used_bytes)} / ${fmtBytes(disk.total_bytes)} · ${disk.used_percent}%` : value.disk?.code}</Value></div></div>
    <div className="card u-panel"><h3>systemd</h3>{[...(systemd.fixed || []), ...(systemd.providers || [])].map(unit => <Value label={unit.name} key={unit.name}>{unit.active_state} · {unit.sub_state || unit.load_state} · NRestarts {unit.n_restarts ?? '—'}</Value>)}{maintenance && <p className="u-note">{maintenance.code} · {maintenance.ready ? t('Ready') : t('Blocked')} · lease {maintenance.lease_id}</p>}</div>
    <div className="card u-panel"><div className="u-card-head"><div><h3>{t('Agent credentials')}</h3><p>{t(agentCredentials?.mode === 'scoped' ? 'Scoped mode rejects unknown Agent IDs.' : 'Transition mode keeps the legacy shared fallback for Agents not migrated yet.')}</p></div><button className="btn btn-ghost" disabled={busy || !agentCredentials} onClick={() => updateAgentCredential('set_mode')}>{t(agentCredentials?.mode === 'scoped' ? 'Enable transition mode' : 'Disable shared fallback')}</button></div>
		<div className="u-form-grid"><div><label>{t('Agent ID')}</label><input value={agentID} onChange={event => setAgentID(event.target.value)} list="mdd-agent-credential-ids"/><datalist id="mdd-agent-credential-ids">{connectedAgents.map(agent => <option key={agent.agent_id} value={agent.agent_id}/>)}</datalist></div></div>
		<div className="u-inline"><button className="btn btn-primary" disabled={busy || !agentID.trim()} onClick={() => updateAgentCredential('issue')}>{t('Issue or rotate credential')}</button></div>
		{issuedCredential && <div className="u-note u-credential-secret"><Value label={t('New credential for')}>{issuedCredential.agentID}</Value><label>{t('Shown once')}</label><input className="mono" type="password" readOnly value={issuedCredential.token}/><div className="u-inline"><button className="btn btn-ghost" onClick={copyIssuedCredential}>{t('Copy')}</button><button className="btn btn-ghost" onClick={() => setIssuedCredential(null)}>{t('Clear')}</button></div></div>}
		{credentialIDs.map(id => { const active = (agentCredentials?.active || []).includes(id); const revoked = (agentCredentials?.revoked || []).includes(id); const connected = connectedAgents.some(agent => agent.agent_id === id); const credentialState = active ? 'Scoped' : revoked ? 'Revoked' : agentCredentials?.mode === 'scoped' ? 'Unenrolled' : 'Legacy fallback'; return <div className="u-detail u-credential-row" key={id}><span className="mono">{id}</span><span className="u-inline"><b>{t(credentialState)} · {t(connected ? 'Connected' : 'Offline')}</b><button className="btn btn-ghost" disabled={busy} onClick={() => updateAgentCredential('issue', id)}>{t(active ? 'Rotate' : 'Issue')}</button>{agentCredentials?.mode === 'transition' && (active || revoked) && <button className="btn btn-ghost" disabled={busy} onClick={() => updateAgentCredential('unenroll', id)}>{t('Use fallback')}</button>}<button className="btn btn-danger-outline" disabled={busy || revoked} onClick={() => updateAgentCredential('revoke', id)}>{t('Revoke')}</button></span></div> })}
		{!credentialIDs.length && <p className="u-muted">{t('No Agent credentials have been enrolled.')}</p>}
	</div>
	<div className="card u-panel"><div className="u-card-head"><div><h3>{t('Agent host health')}</h3><p>{t('Host facts share the authenticated Agent connection and never probe hardware.')}</p></div></div>
		{agentHealthRows.map(agent => { const health = agentHealthPresentation(agent, language); const snapshot = agent.snapshot || {}; const storage = snapshot.resources?.storage || {}; return <section className="u-agent-health" key={agent.agent_id}><div className="u-detail"><span className="mono">{agent.agent_id}</span><b className={health.state === 'error' ? 'u-error' : ''}>{health.label}</b></div><div className="u-details cols"><Value label={t('Platform')}>{agent.meta?.platform || '—'} · {agent.meta?.arch || '—'}</Value><Value label={t('Version')}>{agent.meta?.agent_version || '—'}</Value><Value label={t('Manager')}>{agentHealthEnumLabel('manager', snapshot.manager?.kind, language)} · {snapshot.manager?.host_mode || '—'}</Value><Value label={t('Hardware')}>{snapshot.inventory?.modems_total ?? 0} modem · {snapshot.inventory?.pcsc?.readers?.length ?? 0} reader</Value><Value label={t('Isolation')}>{agentHealthEnumLabel('isolation', snapshot.isolation?.state, language)}{snapshot.isolation?.backend ? ` · ${snapshot.isolation.backend}` : ''}</Value><Value label={t('Disk')}>{storage.total_bytes ? `${fmtBytes(storage.total_bytes - storage.free_bytes)} / ${fmtBytes(storage.total_bytes)} · ${storage.used_percent}%` : agentHealthEnumLabel('storage', storage.state, language)}</Value><Value label={t('Last report')}>{agentHeartbeatAge(agent.seen_at, Date.now(), language)}</Value></div></section> })}
		{!agentHealthRows.length && <p className="u-muted">{t('No connected Agents.')}</p>}
	</div>
    <div className="card u-panel"><h3>{t('Network interfaces')}</h3>{interfaces.map(item => <Value label={item.name} key={item.name}>{(item.addresses || []).join(', ')} · RX {fmtBytes(item.rx_bytes)} · TX {fmtBytes(item.tx_bytes)}</Value>)}{!interfaces.length && <p className="u-muted">{value.network?.code || t('Unavailable')}</p>}</div>
    <div className="card u-panel"><h3>{t('Console')}</h3><div className="u-form-grid"><div><label>{t('Language')}</label><select value={language} onChange={event => setLanguage(event.target.value)}><option value="zh">中文</option><option value="en">English</option></select></div><div><label>{t('Call audio buffer limit (ms)')}</label><input type="number" min={CALL_AUDIO_BUFFER_MIN_MS} max={CALL_AUDIO_BUFFER_MAX_MS} step="100" value={callAudioBufferMS} onChange={event => setCallAudioBufferMS(event.target.value)}/><p className="u-hint">{t('Used by new calls in this browser. Existing calls are unchanged.')}</p><button className="btn btn-ghost" disabled={busy || !preferenceRevision} onClick={saveAudio}>{t('Save audio settings')}</button></div></div><h3>{t('Change password')}</h3><div className="u-form-grid"><div><label>{t('Current password')}</label><input type="password" value={password.current} onChange={event => setPassword(current => ({ ...current, current: event.target.value }))}/></div><div><label>{t('New password')}</label><input type="password" value={password.next} onChange={event => setPassword(current => ({ ...current, next: event.target.value }))}/></div><div><label>{t('Confirm password')}</label><input type="password" value={password.confirm} onChange={event => setPassword(current => ({ ...current, confirm: event.target.value }))}/></div></div><button className="btn btn-primary" disabled={busy} onClick={changePassword}>{t('Change password')}</button></div>
  </div>
}
