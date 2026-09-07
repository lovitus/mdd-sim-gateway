import React, { useEffect, useState } from 'react'
import { api } from '../api.js'
import { useI18n } from '../i18n.jsx'

// The old global host-alert banner, using the existing durable Go notification
// owner rather than a second browser or Core alert state machine.
export default function HostAlertsV1() {
  const { t } = useI18n()
  const [alerts, setAlerts] = useState([])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const [refresh, setRefresh] = useState(0)
  useEffect(() => {
    let stopped = false, timer, delay = 30000
    const load = async () => {
      try {
        const result = await api.hostAlerts()
        if (!stopped) { setAlerts(result.alerts || []); setError(''); delay = 30000 }
      } catch (err) {
        if (!stopped) { setError(err.message); delay = Math.min(240000, delay * 2) }
      } finally { if (!stopped) timer = setTimeout(load, delay) }
    }
    load()
    return () => { stopped = true; clearTimeout(timer) }
  }, [refresh])
  const acknowledge = async alert => {
    if (busy) return
    setBusy(alert.key)
    try { await api.acknowledgeHostAlert(alert); setRefresh(value => value + 1) }
    catch (err) { setError(err.message) }
    finally { setBusy('') }
  }
  const visible = alerts.filter(alert => !alert.acknowledged)
  if (!visible.length && !error) return null
  return <section className="u-host-alerts" aria-label={t('Host alerts')}>
    {error && <div className="u-inline"><span>{t('Host alert status unavailable')}: {error}</span><button className="btn btn-ghost" onClick={() => setRefresh(value => value + 1)}>{t('Refresh')}</button></div>}
    {visible.map(alert => <div className="u-host-alert-row" key={alert.key}>
      <div><strong>{alert.severity} · {alert.code}</strong><span>{alert.scope} · {alert.recovering ? t('Recovery under observation') : t('Alert recorded')}</span><span>{alert.last_observed && !alert.last_observed.startsWith('0001-') ? new Date(alert.last_observed).toLocaleString() : t('Not recorded')}</span></div>
      <button className="btn btn-ghost" disabled={!!busy} onClick={() => acknowledge(alert)}>{t('Acknowledge')}</button>
    </div>)}
  </section>
}
