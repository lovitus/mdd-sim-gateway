import React, { useEffect, useRef, useState } from 'react'
import { api } from '../api.js'
import { useI18n } from '../i18n.jsx'
import { matchUpdateProgress } from '../updateProgress.js'

const active = status => ['requested', 'running'].includes(status?.state)

// Retains the old UpdateModal's confirmation and resumable progress, adapted
// to the Go worker's durable operation identity and terminal states.
export default function UpdateV1() {
  const { t } = useI18n()
  const [release, setRelease] = useState(null)
  const [status, setStatus] = useState(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [watch, setWatch] = useState(0)
  const [observationStopped, setObservationStopped] = useState(false)
  const expected = useRef('')
  const uncertain = useRef(null)
  const mounted = useRef(false)
  const requestBusy = useRef(false)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  useEffect(() => {
    let stopped = false, timer
    const deadline = Date.now() + 600000
    setObservationStopped(false)
    let delay = 30000
    const tick = async () => {
      let continuing = false
      try {
        const result = await api.updateProgress()
        if (stopped) return
        const decision = matchUpdateProgress(result, expected.current, uncertain.current)
        if (!decision.accepted) { setStatus({ state: 'unknown' }); setError(decision.code); setObservationStopped(true); return }
        if (uncertain.current) { expected.current = decision.operation; uncertain.current = null }
        setError('')
        setStatus(result)
        continuing = active(result)
      } catch (err) {
        if (stopped) return
        setError(err.message)
        continuing = err.status !== 401
      }
      if (!stopped && continuing) {
        if (Date.now() + delay > deadline) { setError('update_observation_timeout'); setObservationStopped(true); return }
        timer = setTimeout(tick, delay)
        delay = Math.min(delay * 2, 240000)
      }
    }
    tick()
    return () => { stopped = true; clearTimeout(timer) }
  }, [watch])
  const check = async () => {
    if (requestBusy.current) return
    requestBusy.current = true; setBusy(true); setError('')
    try {
      const result = await api.checkUpdate(true)
      if (mounted.current) setRelease(result)
    } catch (err) { if (mounted.current) setError(err.message) }
    finally { requestBusy.current = false; if (mounted.current) setBusy(false) }
  }
  const begin = async () => {
    if (requestBusy.current || active(status) || !release?.comparison_known || !release?.update_available || status?.state === 'unknown') return
    if (!window.confirm(t('Install version {version}? Services may be interrupted; sign in again after restart.', { version: release.latest }))) return
    requestBusy.current = true; setBusy(true); setError('')
    const priorOperation = status?.operation_id || ''
    try {
      const result = await api.applyUpdate(release.latest)
      expected.current = result.operation_id
      if (mounted.current) { setStatus({ state: 'requested', target: result.target, operation_id: result.operation_id }); setWatch(value => value + 1) }
    } catch (err) {
      if (mounted.current) {
        setError(err.message)
        // An uncertain POST is observed, never repeated automatically.
        if (!err.status || err.status >= 500) uncertain.current = { previous: priorOperation, target: release.latest }
        setStatus({ state: 'unknown' }); setWatch(value => value + 1)
      }
    } finally { requestBusy.current = false; if (mounted.current) setBusy(false) }
  }
  return <section className="card u-panel">
    <div className="u-card-head"><h3>{t('Software update')}</h3><button className="btn btn-ghost" disabled={busy || active(status)} onClick={check}>{t('Check for updates')}</button></div>
    {release && <div className="u-details cols"><div className="u-detail"><span>{t('Current version')}</span><b>{release.current}</b></div><div className="u-detail"><span>{t('Latest version')}</span><b>{release.latest || '—'}</b></div></div>}
    {release?.comparison_known === false && <p className="u-note">{t('This build has no comparable release version. Automatic upgrade is unavailable.')}</p>}
    {release?.notes && <details><summary>{t('Release notes')}</summary><p style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{release.notes}</p></details>}
    {status && <p role="status">{t('Update status')}: {status.state} {status.phase || ''} {status.target || ''}</p>}
    {(error || status?.error_code) && <p className="u-error">{error || status.error_code}</p>}
    <div className="u-inline"><button className="btn btn-primary" disabled={busy || !status || active(status) || status.state === 'unknown' || !release?.comparison_known || !release?.update_available} onClick={begin}>{t('Install update')}</button><button className="btn btn-ghost" disabled={busy || active(status) && !observationStopped} onClick={() => { expected.current = ''; setError(''); setWatch(value => value + 1) }}>{t('Refresh status')}</button></div>
  </section>
}
