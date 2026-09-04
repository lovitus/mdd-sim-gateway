import React, { useCallback, useEffect, useState } from 'react'
import { api } from '../api.js'
import { useI18n } from '../i18n.jsx'

export default function DiagnosticsV1({ instances, devices, callCoordinator, showToast }) {
  const { t } = useI18n()
  const [snapshot, setSnapshot] = useState(null)
  const [lineID, setLineID] = useState('')
  const [media, setMedia] = useState('')
  const [facts, setFacts] = useState(null)
  const load = useCallback(() => api.diagnosticsV1().then(setSnapshot).catch(error => showToast(error.message)), [showToast])
  useEffect(() => { void load() }, [load])
  useEffect(() => { if (!lineID && instances?.[0]) setLineID(String(instances[0].id)) }, [instances, lineID])
  const readFacts = async () => {
    const line = (snapshot?.lines || []).find(item => String(item.line_id || item.id) === String(lineID))
    if (line) { setFacts(line); return }
    showToast(t('The selected line is not present in the current typed diagnostic snapshot.'))
  }
  const verifyMedia = async () => {
    setMedia('running')
    try { await callCoordinator.verifyMedia(lineID); setMedia('pass'); showToast(t('Bidirectional WSS PCM passed without placing a call.')) }
    catch (error) { setMedia(`fail: ${error.message}`); showToast(error.message) }
  }
  if (!snapshot) return <p>{t('Loading…')}</p>
  return <div className="u-page"><div className="u-card-head"><div><h2>{t('Diagnostics')}</h2><p>{t('These are explicit read-only or no-charge tests. No automatic carrier calls or SMS are generated.')}</p></div><div className="u-inline"><button className="btn btn-ghost" onClick={load}>{t('Refresh')}</button><a className="btn btn-ghost" href="/v1/diagnostics/support-bundle">{t('Download redacted support bundle')}</a></div></div>
    <div className="u-metrics"><div className="u-metric"><span>{t('Checks')}</span><strong>{snapshot.checks?.length || 0}</strong></div><div className="u-metric"><span>{t('Agents')}</span><strong>{snapshot.agents?.length || 0}</strong></div><div className="u-metric"><span>{t('Lines')}</span><strong>{snapshot.lines?.length || 0}</strong></div><div className="u-metric"><span>{t('Devices')}</span><strong>{devices?.length || 0}</strong></div></div>
    <div className="card u-panel"><h3>{t('Core and Agent checks')}</h3>{(snapshot.checks || []).map(check => <div className="u-detail" key={check.id}><span>{check.id}<small>{check.scope} · {check.kind}</small></span><b className={check.status === 'fail' ? 'u-error' : ''}>{check.status} · {check.code}</b></div>)}</div>
    <div className="card u-panel"><h3>{t('Line end-to-end evidence')}</h3><div className="u-form-grid"><div><label>{t('Line')}</label><select value={lineID} onChange={event => { setLineID(event.target.value); setFacts(null); setMedia('') }}>{(instances || []).map(line => <option value={line.id} key={line.id}>{line.name || line.id}</option>)}</select></div></div><div className="u-inline"><button className="btn btn-ghost" disabled={!lineID} onClick={readFacts}>{t('Read complete fact snapshot')}</button><button className="btn btn-ghost" disabled={!lineID || media === 'running'} onClick={verifyMedia}>{t(media === 'running' ? 'Testing…' : 'No-charge browser WSS PCM test')}</button></div>{media && <p className={media.startsWith('fail') ? 'u-error' : 'u-note'}>{media}</p>}{facts && <pre className="mono" style={{ whiteSpace: 'pre-wrap' }}>{JSON.stringify(facts, null, 2)}</pre>}</div>
    <div className="card u-panel"><h3>{t('Raw diagnostic snapshot')}</h3><details><summary>{t('Show typed JSON')}</summary><pre className="mono" style={{ whiteSpace: 'pre-wrap' }}>{JSON.stringify(snapshot, null, 2)}</pre></details></div>
  </div>
}
