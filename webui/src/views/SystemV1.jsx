import React, { useCallback, useEffect, useState } from 'react'
import { api } from '../api.js'
import { CALL_AUDIO_BUFFER_MAX_MS, CALL_AUDIO_BUFFER_MIN_MS, cacheCallAudioBufferMS, getCallAudioBufferMS, normalizeCallAudioBufferMS } from '../browserPreferences.js'
import { useI18n } from '../i18n.jsx'

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
  const load = useCallback(() => api.systemStatus().then(result => {
    setValue(result); setSystemMeta?.(result)
  }).catch(error => showToast(error.message)), [setSystemMeta, showToast])
	useEffect(() => {
		void load()
		void api.systemPreferences().then(result => {
			const buffer = cacheCallAudioBufferMS(result.preferences?.call_audio_buffer_ms)
			setCallAudioBufferMS(buffer); setGlobalCallAudioBufferMS?.(buffer); setPreferenceRevision(Number(result.revision || 0))
		}).catch(error => showToast(error.message))
	}, [load, showToast, setGlobalCallAudioBufferMS])
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
  if (!value) return <p>{t('Loading…')}</p>
  const provenance = value.provenance || {}
  const host = value.host?.value || {}
  const memory = value.memory?.value || {}
  const disk = value.disk?.value || {}
  const systemd = value.systemd?.value || {}
  const interfaces = value.network?.value?.interfaces || []
  return <div className="u-page"><div className="u-card-head"><div><h2>{t('System settings')}</h2><p>{t('This page shows the running Go runtime and host facts. It does not expose dead Python maintenance controls.')}</p></div><button className="btn btn-ghost" onClick={load}>{t('Refresh')}</button></div>
    <div className="u-device-grid"><div className="card u-panel"><h3>{t('Runtime')}</h3><Value label={t('Version')}>{value.build_version}</Value><Value label="VCS">{value.vcs_revision}</Value><Value label="Go">{value.go_version}</Value><Value label={t('Public listener')}>{value.public?.listen}</Value><Value label={t('Transport')}>{value.public?.transport} · {value.public?.multiplexing}</Value><Value label="TLS SHA-256">{value.public?.tls_fingerprint_sha256}</Value></div>
      <div className="card u-panel"><h3>{t('Release provenance')}</h3><Value label={t('State')}>{provenance.state}</Value><Value label={t('Verified')}>{provenance.verified ? t('Yes') : t('No')}</Value><Value label="Release ID">{provenance.release_id}</Value><Value label="Core SHA-256">{provenance.core_sha256}</Value><Value label={t('Source revision')}>{provenance.source_revision || provenance.vcs_revision}</Value></div>
      <div className="card u-panel"><h3>{t('Host')}</h3><Value label={t('Platform')}>{host.platform} {host.platform_version}</Value><Value label={t('Kernel')}>{host.kernel_version} · {host.kernel_arch}</Value><Value label={t('Uptime')}>{host.uptime_seconds ? `${Math.floor(host.uptime_seconds / 3600)} h` : '—'}</Value><Value label={t('Memory')}>{memory.total_bytes ? `${fmtBytes(memory.used_bytes)} / ${fmtBytes(memory.total_bytes)} · ${memory.used_percent}%` : value.memory?.code}</Value><Value label={t('Disk')}>{disk.total_bytes ? `${fmtBytes(disk.used_bytes)} / ${fmtBytes(disk.total_bytes)} · ${disk.used_percent}%` : value.disk?.code}</Value></div></div>
    <div className="card u-panel"><h3>systemd</h3>{[...(systemd.fixed || []), ...(systemd.providers || [])].map(unit => <Value label={unit.name} key={unit.name}>{unit.active_state} · {unit.sub_state || unit.load_state} · NRestarts {unit.n_restarts ?? '—'}</Value>)}</div>
    <div className="card u-panel"><h3>{t('Network interfaces')}</h3>{interfaces.map(item => <Value label={item.name} key={item.name}>{(item.addresses || []).join(', ')} · RX {fmtBytes(item.rx_bytes)} · TX {fmtBytes(item.tx_bytes)}</Value>)}{!interfaces.length && <p className="u-muted">{value.network?.code || t('Unavailable')}</p>}</div>
    <div className="card u-panel"><h3>{t('Console')}</h3><div className="u-form-grid"><div><label>{t('Language')}</label><select value={language} onChange={event => setLanguage(event.target.value)}><option value="zh">中文</option><option value="en">English</option></select></div><div><label>{t('Call audio buffer limit (ms)')}</label><input type="number" min={CALL_AUDIO_BUFFER_MIN_MS} max={CALL_AUDIO_BUFFER_MAX_MS} step="100" value={callAudioBufferMS} onChange={event => setCallAudioBufferMS(event.target.value)}/><p className="u-hint">{t('Used by new calls in this browser. Existing calls are unchanged.')}</p><button className="btn btn-ghost" disabled={busy || !preferenceRevision} onClick={saveAudio}>{t('Save audio settings')}</button></div></div><h3>{t('Change password')}</h3><div className="u-form-grid"><div><label>{t('Current password')}</label><input type="password" value={password.current} onChange={event => setPassword(current => ({ ...current, current: event.target.value }))}/></div><div><label>{t('New password')}</label><input type="password" value={password.next} onChange={event => setPassword(current => ({ ...current, next: event.target.value }))}/></div><div><label>{t('Confirm password')}</label><input type="password" value={password.confirm} onChange={event => setPassword(current => ({ ...current, confirm: event.target.value }))}/></div></div><button className="btn btn-primary" disabled={busy} onClick={changePassword}>{t('Change password')}</button></div>
  </div>
}
