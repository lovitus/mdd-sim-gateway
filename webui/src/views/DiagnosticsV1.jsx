import React, { useCallback, useEffect, useState } from 'react'
import { api } from '../api.js'
import { useI18n } from '../i18n.jsx'

export default function DiagnosticsV1({ instances, devices, callCoordinator, showToast }) {
  const { t } = useI18n()
  const [snapshot, setSnapshot] = useState(null)
  const [lineID, setLineID] = useState('')
  const [media, setMedia] = useState('')
  const [facts, setFacts] = useState(null)
	const [logs, setLogs] = useState(null)
	const [logSource, setLogSource] = useState('all')
	const [logsLoading, setLogsLoading] = useState(false)
	const [registering, setRegistering] = useState(false)
  const load = useCallback(() => api.diagnosticsV1().then(setSnapshot).catch(error => showToast(error.message)), [showToast])
  useEffect(() => { void load() }, [load])
  useEffect(() => { if (!lineID && instances?.[0]) setLineID(String(instances[0].id)) }, [instances, lineID])
  const readFacts = async () => {
    const line = (snapshot?.lines || []).find(item => String(item.line_id || item.id) === String(lineID))
    if (line) { setFacts(line); return }
    showToast(t('The selected line is not present in the current typed diagnostic snapshot.'))
  }
	const readLogs = async () => {
		if (!lineID || logsLoading) return
		setLogsLoading(true)
		try { setLogs(await api.lineDiagnosticLogs(lineID, 200)) }
		catch (error) { showToast(error.message) }
		finally { setLogsLoading(false) }
	}
  const verifyMedia = async () => {
    setMedia('running')
    try { await callCoordinator.verifyMedia(lineID); setMedia('pass'); showToast(t('Bidirectional WSS PCM passed without placing a call.')) }
    catch (error) { setMedia(`fail: ${error.message}`); showToast(error.message) }
  }
	const selectedLine = (instances || []).find(line => String(line.id) === String(lineID))
	const selectedFacts = (snapshot?.lines || []).find(line => String(line.line_id) === String(lineID))
	const visibleLogs = (logs?.entries || []).filter(entry => logSource === 'all' || entry.source === logSource)
	const runtimeFact = (selectedFacts?.facts || []).find(fact => fact.layer === 'vowifi_runtime')
	const registerSupported = String(runtimeFact?.detail || '').split(';').includes('manual_register=true')
	const manualRegister = async () => {
		if (!selectedLine?.iccid || !registerSupported || registering ||
			!window.confirm(t('Send one IMS REGISTER on the selected line? This does not place a call or send SMS.'))) return
		setRegistering(true)
		try {
			await api.registerV1(lineID, selectedLine.iccid || selectedLine.card_id)
			await load(); showToast(t('IMS registration refreshed'))
		} catch (error) { showToast(error.message) } finally { setRegistering(false) }
	}
  if (!snapshot) return <p>{t('Loading…')}</p>
  return <div className="u-page"><div className="u-card-head"><div><h2>{t('Diagnostics')}</h2><p>{t('These are explicit read-only or no-charge tests. No automatic carrier calls or SMS are generated.')}</p></div><div className="u-inline"><button className="btn btn-ghost" onClick={load}>{t('Refresh')}</button><a className="btn btn-ghost" href="/v1/diagnostics/support-bundle">{t('Download redacted support bundle')}</a></div></div>
    <div className="u-metrics"><div className="u-metric"><span>{t('Checks')}</span><strong>{snapshot.checks?.length || 0}</strong></div><div className="u-metric"><span>{t('Agents')}</span><strong>{snapshot.agents?.length || 0}</strong></div><div className="u-metric"><span>{t('Lines')}</span><strong>{snapshot.lines?.length || 0}</strong></div><div className="u-metric"><span>{t('Devices')}</span><strong>{devices?.length || 0}</strong></div></div>
    <div className="card u-panel"><h3>{t('Core and Agent checks')}</h3>{(snapshot.checks || []).map(check => <div className="u-detail" key={check.id}><span>{check.id}<small>{check.scope} · {check.kind}</small></span><b className={check.status === 'fail' ? 'u-error' : ''}>{check.status} · {check.code}</b></div>)}</div>
    <div className="card u-panel"><h3>{t('Line end-to-end evidence')}</h3><div className="u-form-grid"><div><label>{t('Line')}</label><select value={lineID} onChange={event => { setLineID(event.target.value); setFacts(null); setLogs(null); setMedia('') }}>{(instances || []).map(line => <option value={line.id} key={line.id}>{line.name || line.id}</option>)}</select></div></div><div className="u-inline"><button className="btn btn-ghost" disabled={!lineID} onClick={readFacts}>{t('Read complete fact snapshot')}</button><button className="btn btn-ghost" disabled={!lineID || media === 'running'} onClick={verifyMedia}>{t(media === 'running' ? 'Testing…' : 'No-charge browser WSS PCM test')}</button><button className="btn btn-ghost" disabled={!lineID || !selectedLine?.iccid || !registerSupported || registering} onClick={manualRegister} title={!registerSupported ? t('The current Provider has not negotiated manual registration') : ''}>{t(registering ? 'Registering…' : 'Manual IMS re-register')}</button></div>{media && <p className={media.startsWith('fail') ? 'u-error' : 'u-note'}>{media}</p>}{facts && <pre className="mono" style={{ whiteSpace: 'pre-wrap' }}>{JSON.stringify(facts, null, 2)}</pre>}</div>
    <section className="card u-panel"><div className="u-card-head"><div><h3>{t('Line diagnostic logs')}</h3><p>{t('Bounded typed events only. Subscriber identities, credentials, network addresses and local paths are redacted.')}</p></div><div className="u-inline"><button className="btn btn-ghost" disabled={!lineID || logsLoading} onClick={readLogs}>{t(logsLoading ? 'Loading…' : 'Refresh')}</button>{lineID && <a className="btn btn-ghost" href={api.lineDiagnosticExportURL(lineID, 500)}>{t('Download redacted logs')}</a>}</div></div><div className="u-log-tabs" role="group" aria-label={t('Log source')}>{['all','agent','provider','core'].map(source => <button key={source} className={logSource === source ? 'active' : ''} onClick={() => setLogSource(source)}>{t(source === 'all' ? 'All sources' : source === 'agent' ? 'Agent' : source === 'provider' ? 'Provider' : 'Core')}</button>)}</div>{logs && <div className="u-line-logs">{visibleLogs.map((entry, index) => <div className="u-line-log" key={`${entry.received_at}-${entry.source}-${entry.layer}-${index}`}><div><span className="u-badge">{t(entry.source === 'agent' ? 'Agent' : entry.source === 'provider' ? 'Provider' : 'Core')}</span><b>{entry.layer} · {entry.condition}</b><time>{new Date(entry.received_at).toLocaleString()}</time></div><code>{entry.code || '—'}</code>{entry.detail && <pre>{entry.detail}</pre>}</div>)}{!visibleLogs.length && <p className="u-muted">{t('No matching diagnostic events.')}</p>}</div>}{!logs && <p className="u-muted">{t('Select a line and load its recent redacted Agent and Provider events.')}</p>}</section>
    <div className="card u-panel"><h3>{t('Raw diagnostic snapshot')}</h3><details><summary>{t('Show typed JSON')}</summary><pre className="mono" style={{ whiteSpace: 'pre-wrap' }}>{JSON.stringify(snapshot, null, 2)}</pre></details></div>
  </div>
}
