import React, { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api.js'
import { maintenanceLeases, maintenanceRequest } from '../systemAdapter.js'
import { useI18n } from '../i18n.jsx'

export default function Maintenance({showToast}) {
  const {t} = useI18n()
  const [snapshot,setSnapshot] = useState(null)
  const [busy,setBusy] = useState(false)
  const [error,setError] = useState('')
  const inFlight = useRef(false)
  const load = useCallback(async () => {
    try { setSnapshot(await api.systemMaintenanceStatus()); setError('') }
    catch (failure) { setError(failure.message) }
  }, [])
  useEffect(() => { void load() }, [load])
  const run = async (action, leaseID = '') => {
    if (inFlight.current) return
    if (!window.confirm(t(action === 'begin' ? 'Drain active providers for maintenance?' : 'Resume this maintenance lease?'))) return
    inFlight.current = true; setBusy(true); setError('')
    try {
      const current = await api.systemMaintenanceStatus()
      const request = maintenanceRequest(current, action, leaseID || crypto.randomUUID())
      const result = await api.systemMaintenance(action, request)
      if (result.ready !== true) throw new Error(result.code || 'maintenance_not_ready')
      showToast?.(result.code)
      setSnapshot(await api.systemMaintenanceStatus())
    } catch (failure) { setError(failure.message) }
    finally { inFlight.current = false; setBusy(false) }
  }
  const tool = async action => {
    if (inFlight.current || !window.confirm(t(action === 'egress' ? 'Refresh country exits?' : 'Clear notification history?'))) return
    inFlight.current = true; setBusy(true); setError('')
    try {
      if (action === 'egress') {
        const saved = await api.egressConfig()
        const result = await api.applyEgress(saved.revision)
        showToast?.(result.code || t('Request submitted'))
      } else {
        await api.clearNotificationDeliveries()
        showToast?.(t('Notification history cleared'))
      }
    } catch (failure) { setError(failure.message) }
    finally { inFlight.current = false; setBusy(false) }
  }
  return <section><h2>{t('Maintenance')}</h2>
    {error && <p className="u-error" role="alert">{error}</p>}
    <div className="u-action-grid"><button className="btn btn-ghost" disabled={busy || !snapshot} onClick={() => run('begin')}>{t('Drain for maintenance')}</button>
      <button className="btn btn-ghost" disabled={busy} onClick={load}>{t('Refresh')}</button>
      <button className="btn btn-ghost" disabled={busy} onClick={() => tool('egress')}>{t('Refresh country exits')}</button>
      <button className="btn btn-ghost" disabled={busy} onClick={() => tool('notifications')}>{t('Clear notification history')}</button></div>
    {maintenanceLeases(snapshot).map(lease => <div className="u-detail" key={lease.lease_id}><span>{lease.line_ids.join(', ')}</span>
      <button className="btn btn-primary" disabled={busy} onClick={() => run('resume',lease.lease_id)}>{t('Resume maintenance')}</button></div>)}
    {(snapshot?.lines || []).map(line => <div className="u-detail" key={line.line_id}><span>{line.line_id}</span><b>{line.maintenance?.code || line.code || t('Unknown')}</b></div>)}
  </section>
}
