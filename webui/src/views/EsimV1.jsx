import React, { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api.js'
import { euiccProfileInventory, operationID } from '../goV1Adapter.js'
import { useI18n } from '../i18n.jsx'

function parseActivationCode(raw) {
  let value = String(raw || '').trim()
  if (/^LPA:/i.test(value)) value = value.slice(4)
  const parts = value.split('$')
  return parts.length >= 3 && parts[0] === '1' && parts[1] ? `LPA:${value}` : ''
}

async function decodeQR(file) {
  const jsQR = (await import('jsqr')).default
  const bitmap = await createImageBitmap(file)
  try {
    if (file.size > 16 * 1024 * 1024 || bitmap.width > 20000 || bitmap.height > 20000)
      throw new Error('qr_image_too_large')
    for (const maximum of [1500, 800, 3000]) {
      const scale = Math.min(1, maximum / Math.max(bitmap.width, bitmap.height))
      const width = Math.max(1, Math.round(bitmap.width * scale))
      const height = Math.max(1, Math.round(bitmap.height * scale))
      const canvas = document.createElement('canvas'); canvas.width = width; canvas.height = height
      const context = canvas.getContext('2d', { willReadFrequently: true })
      context.drawImage(bitmap, 0, 0, width, height)
      const pixels = context.getImageData(0, 0, width, height)
      const found = jsQR(pixels.data, width, height, { inversionAttempts: 'attemptBoth' })
      if (found?.data) return found.data
      if (scale === 1) break
    }
    return ''
  } finally { bitmap.close?.() }
}

function DownloadForm({ entry, onStarted, onCancel, showToast }) {
  const { t } = useI18n()
  const [activation, setActivation] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [imei, setIMEI] = useState('')
  const [busy, setBusy] = useState(false)
  const input = useRef(null)
  const read = async file => {
    try {
      const value = parseActivationCode(await decodeQR(file))
      if (!value) throw new Error(t('The image does not contain an eSIM activation code.'))
      setActivation(value)
    } catch (error) { showToast(error.message) }
  }
  const submit = async event => {
    event.preventDefault()
    const code = parseActivationCode(activation)
    if (!code || !/^\d{15}$/.test(imei)) { showToast(t('A valid activation code and 15-digit IMEI are required.')); return }
    if (!window.confirm(t('Start this one-time profile download on the exact EID? It will not be retried automatically.'))) return
    setBusy(true)
    try {
      const operation = operationID('react-euicc-download')
      const result = await api.startEuiccDownload(entry.euicc.eid, {
        operation_id: operation, activation_code: code,
        confirmation_code: confirmation.trim(), imei,
      })
      onStarted(operation, result.job)
    } catch (error) { showToast(error.message) } finally { setBusy(false) }
  }
  return <form className="u-note" onSubmit={submit} onDragOver={event => event.preventDefault()}
    onDrop={event => { event.preventDefault(); const file = event.dataTransfer?.files?.[0]; if (file) void read(file) }}>
    <h3>{t('Download eSIM')}</h3><p>{t('The activation code stays in this form and is never persisted by the browser adapter.')}</p>
    <label>{t('Activation code')}</label><textarea rows="3" value={activation} onChange={event => setActivation(event.target.value)} placeholder="LPA:1$smdp.example$MATCHING-ID"/>
    <div className="u-form-grid"><div><label>IMEI</label><input inputMode="numeric" maxLength="15" value={imei} onChange={event => setIMEI(event.target.value.replace(/\D/g, ''))}/></div>
      <div><label>{t('Confirmation code (optional)')}</label><input type="password" value={confirmation} onChange={event => setConfirmation(event.target.value)}/></div></div>
    <input ref={input} hidden type="file" accept="image/*" onChange={event => { const file = event.target.files?.[0]; if (file) void read(file) }}/>
    <div className="u-inline"><button type="button" className="btn btn-ghost" onClick={() => input.current?.click()}>{t('Read QR image')}</button>
      <button type="button" className="btn btn-ghost" onClick={onCancel}>{t('Cancel')}</button><button className="btn btn-primary" disabled={busy}>{t(busy ? 'Starting…' : 'Start download')}</button></div>
  </form>
}

function EUICCCard({ entry, reload, showToast }) {
  const { t } = useI18n()
  const [download, setDownload] = useState(null)
  const [showDownload, setShowDownload] = useState(false)
  const [notifications, setNotifications] = useState(null)
  const [discovery, setDiscovery] = useState(null)
  const [busy, setBusy] = useState('')
  const eid = entry.euicc.eid
  const inventory = euiccProfileInventory(entry.euicc)
  useEffect(() => {
    if (!download?.operation || !['queued', 'running', 'cancelling'].includes(download.job?.state)) return
    let stopped = false
    const timer = setTimeout(async () => {
      try {
        const result = await api.euiccDownloadStatus(eid, download.operation)
        if (!stopped) setDownload({ operation: download.operation, job: result.job })
      } catch (error) { if (!stopped) showToast(error.message) }
    }, 1500)
    return () => { stopped = true; clearTimeout(timer) }
  }, [download, eid, showToast])
  const mutate = async (profile, action, nickname) => {
    const verb = action === 'nickname' ? t('change the nickname of') : action === 'enable' ? t('enable') : t('disable')
    if (!window.confirm(`${verb} ${profile.iccid}?`)) return
    setBusy(`${action}:${profile.iccid}`)
    try {
      const body = { operation_id: operationID(`react-euicc-${action}`), expected_state: profile.state }
      if (action === 'nickname') {
        body.nickname = nickname; body.expected_nickname = profile.nickname || ''
      }
      const result = await api.mutateEuiccProfile(eid, profile.iccid, action, body)
      showToast(`${result.outcome || 'accepted'}${result.outcome === 'uncertain' ? ` · ${t('Do not retry until inventory is refreshed.')}` : ''}`)
      if (result.outcome !== 'uncertain') setTimeout(reload, 1200)
    } catch (error) { showToast(error.message) } finally { setBusy('') }
  }
  const loadNotifications = async () => {
    setBusy('notifications')
    try { setNotifications(await api.euiccNotifications(eid)) }
    catch (error) { showToast(error.message) } finally { setBusy('') }
  }
  const deliver = async item => {
    if (!window.confirm(t('Send this exact stored notification to the carrier server once?'))) return
    setBusy(`deliver:${item.sequence_number}`)
    try {
      await api.deliverEuiccNotification(eid, item.sequence_number, {
        confirmed: true, event: item.event, iccid: item.iccid || '', address: item.address,
      })
      await loadNotifications()
    } catch (error) { showToast(error.message) } finally { setBusy('') }
  }
  const remove = async item => {
    if (!window.confirm(t('Remove only this already acknowledged notification from the card?'))) return
    setBusy(`remove:${item.sequence_number}`)
    try {
      await api.removeEuiccNotification(eid, item.sequence_number, {
        confirmed: true, receiver_acknowledged: true,
        event: item.event, iccid: item.iccid || '', address: item.address,
      })
      await loadNotifications()
    } catch (error) { showToast(error.message) } finally { setBusy('') }
  }
  const discover = async () => {
    if (!window.confirm(t('Query SM-DS for this EID? This does not download or install a profile.'))) return
    setBusy('discovery')
    try { setDiscovery(await api.discoverEuicc(eid, { operation_id: operationID('react-euicc-discovery'), smds: '', imei: '' })) }
    catch (error) { showToast(error.message) } finally { setBusy('') }
  }
  return <article className="card u-panel"><div className="u-card-head"><div><h2>{entry.slot_label || entry.reader_name}</h2><p>{entry.reader_name} · Agent {entry.agent_id}</p></div><span className="u-badge cap-on">{t('Detected')}</span></div>
    <div className="u-detail"><span>EID</span><b className="mono">{eid}</b></div><div className="u-detail"><span>{t('Profiles')}</span><b>{inventory.available ? inventory.count : t('Inventory unavailable')}</b></div>
    <div className="u-profile-list">{inventory.profiles.map(profile => <div className="u-detail" key={profile.iccid}><span><b>{profile.nickname || profile.profile_name || profile.service_provider_name || profile.iccid}</b><small className="mono">{profile.iccid}</small></span><span className="u-inline"><span className={`u-badge ${profile.state === 'enabled' ? 'cap-on' : 'cap-off'}`}>{profile.state}</span>
      <button className="btn btn-ghost" disabled={!!busy || !entry.euicc.profile_management} onClick={() => mutate(profile, profile.state === 'enabled' ? 'disable' : 'enable')}>{t(profile.state === 'enabled' ? 'Disable' : 'Enable')}</button>
      <button className="btn btn-ghost" disabled={!!busy || !entry.euicc.profile_management} onClick={() => { const name = window.prompt(t('Nickname'), profile.nickname || ''); if (name !== null) void mutate(profile, 'nickname', name.trim()) }}>{t('Rename')}</button></span></div>)}</div>
    {!inventory.profiles.length && <p className="u-muted">{inventory.available ? t('This eUICC has no profiles.') : t('Profile inventory is not available.')}</p>}
    <div className="u-inline"><button className="btn btn-ghost" disabled={!entry.euicc.profile_download || !!busy} onClick={() => setShowDownload(value => !value)}>{t('Download eSIM')}</button>
      <button className="btn btn-ghost" disabled={!entry.euicc.profile_discovery || !!busy} onClick={discover}>{t('SM-DS discovery')}</button>
      <button className="btn btn-ghost" disabled={!entry.euicc.notification_inventory || !!busy} onClick={loadNotifications}>{t('Notifications')}</button></div>
    {showDownload && (
      <DownloadForm entry={entry} showToast={showToast} onCancel={() => setShowDownload(false)}
        onStarted={(operation, job) => { setDownload({ operation, job }); setShowDownload(false) }}/>
    )}
    {download && <div className="u-note"><b>{t('Download')}: {download.job?.state || 'unknown'}</b><p>{download.job?.stage || download.job?.code || ''}</p>{['queued', 'running'].includes(download.job?.state) && <button className="btn btn-ghost" onClick={async () => { const result = await api.cancelEuiccDownload(eid, download.operation); setDownload({ operation: download.operation, job: result.job }) }}>{t('Cancel download')}</button>}</div>}
    {discovery && <details open><summary>{t('SM-DS results')}</summary><pre className="mono">{JSON.stringify(discovery, null, 2)}</pre></details>}
    {notifications && <details open><summary>{t('eUICC notifications')}</summary>{(notifications.entries || []).map(item => <div className="u-detail" key={item.sequence_number}><span>#{item.sequence_number} · {item.event} · {item.iccid || '—'}</span><span className="u-inline"><button className="btn btn-ghost" disabled={!!busy || !entry.euicc.notification_delivery} onClick={() => deliver(item)}>{t('Deliver once')}</button><button className="btn btn-ghost" disabled={!!busy || !entry.euicc.notification_removal} onClick={() => remove(item)}>{t('Remove acknowledged')}</button></span></div>)}</details>}
  </article>
}

export default function EsimV1({ showToast }) {
  const { t } = useI18n()
  const [entries, setEntries] = useState(null)
  const load = useCallback(() => api.euiccs().then(result => setEntries(result.euiccs || [])).catch(error => { setEntries([]); showToast(error.message) }), [showToast])
  useEffect(() => { void load() }, [load])
  return <div className="u-page"><div className="u-section-title"><div><h2>eSIM / eUICC</h2><p>{t('Each EID is operated independently of its reader name; moving the card does not change profile identity.')}</p></div><button className="btn btn-ghost" onClick={load}>{t('Refresh')}</button></div>
    {entries === null ? <p>{t('Loading…')}</p> : !entries.length ? <div className="u-empty"><h3>{t('No eUICC detected')}</h3><p>{t('Insert an eUICC into any connected PC/SC reader.')}</p></div> : <div className="u-device-grid">{entries.map(entry => <EUICCCard key={`${entry.euicc.eid}:${entry.agent_id}:${entry.reader_name}:${entry.slot_id}`} entry={entry} reload={load} showToast={showToast}/>)}</div>}
  </div>
}
