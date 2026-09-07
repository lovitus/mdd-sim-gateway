import React, { useCallback, useEffect, useRef, useState } from 'react'
import { api, connectWs, setCsrf, setAuthToken } from './api.js'
import Softphone from './views/Softphone.jsx'
import Messages from './views/Messages.jsx'
import HostAlerts from './views/HostAlerts.jsx'
import Esim from './views/Esim.jsx'
import { UnifiedOverview, DevicesPage, ImeiPoolPanel, EgressPage, NotificationsPage, SystemPage, DiagnosticsPage } from './views/UnifiedPages.jsx'
import { useI18n } from './i18n.jsx'
import { GlobalGoCallOverlay, useGoCallCoordinator } from '../goCallCoordinator.jsx'
import { liveStatusFromWsMessage, mergeLiveLineStatus } from './liveStatus.js'
import { consumeUpdateCompletion, updateProgressOutcome, matchUpdateProgress } from './updateProgress.js'

const NAV = [
  ['overview', 'Overview', '⌂'], ['devices', 'Devices', '▣'], ['imeis', 'IMEI Pool', '◈'], ['calls', 'Calls', '☎'],
  ['messages', 'Messages', '✉'], ['esim', 'eSIM', '◎'], ['egress', 'Network exits', '⇄'],
  ['notifications', 'Notifications', '◉'], ['settings', 'System settings', '⚙'], ['diagnostics', 'Diagnostics', '≣'],
]

// Each page is addressable as #/<key>, so a refresh (or a bookmark) lands on the same page
// instead of falling back to the overview. An unknown hash means the overview.
const viewFromHash = () => {
  const key = window.location.hash.replace(/^#\/?/, '')
  return NAV.some(([k]) => k === key) ? key : 'overview'
}

// GitHub's own abbreviation, so the console reads the same as the repository page: exact
// below a thousand, one decimal above it, and the decimal dropped once it stops adding
// precision (1000 -> 1k, 1250 -> 1.3k, 13300 -> 13.3k).
function starCount(value) {
  // An unreadable count is absent, not zero: Number(null) is 0, and rendering that would
  // claim the repository has no stars whenever GitHub could not be reached.
  if (value === null || value === undefined || value === '') return ''
  const count = Number(value)
  if (!Number.isFinite(count) || count < 0) return ''
  if (count < 1000) return String(count)
  const thousands = count / 1000
  return `${thousands >= 100 ? Math.round(thousands) : Number(thousands.toFixed(1))}k`
}

function legacyDevices(instances, cards) {
  const used = new Set()
  const fromInstances = instances.map((inst, i) => {
    const reader = inst.reader || inst.reader_name || inst.config?.reader
    if (reader) used.add(reader)
    const state = String(inst.status?.state || '').toUpperCase()
    const running = ['OK', 'WORKING', 'REGISTERED'].includes(state) || inst.status?.label === 'Working'
    return {
      id: inst.device_id || inst.id,
      name: inst.name || inst.id,
      reader,
      model: inst.modem_name || inst.modem,
      sim: { name: inst.carrier || inst.name, number: inst.number || inst.msisdn },
      status: inst.status,
      compatibilityOnly: true,
      capabilities: {
        cellular: { desired: false, actual: 'unsupported', reason: 'Unified cellular status has not been exposed by this backend.' },
        vowifi: { desired: running, actual: running ? 'on' : (state === 'ERROR' ? 'error' : 'off'), reason: inst.status?.reason || '' },
      },
    }
  })
  const readers = cards.filter(c => !used.has(c.reader || c.name)).map((c, i) => ({
    id: `reader:${c.reader || c.name || i}`, name: c.modem_name || c.reader || c.name || `Reader ${i + 1}`,
    reader: c.reader || c.name, present: c.present !== false, sim: { name: c.carrier || 'SIM' }, compatibilityOnly: true,
    capabilities: { cellular: { desired: false, actual: 'unsupported' }, vowifi: { desired: false, actual: 'off' } },
  }))
  return [...fromInstances, ...readers]
}

export default function App() {
  const { t } = useI18n()
  const [view, setView] = useState(viewFromHash); const [menuOpen, setMenuOpen] = useState(false)
  const [instances, setInstances] = useState([]); const [cards, setCards] = useState([]); const [devices, setDevices] = useState([])
  // Sessions live in memory, so signing in normally happens seconds after the control plane
  // restarted — while its first card scan is still running. Until that scan has answered,
  // an empty list means "not known yet", not "no devices".
  const [discovering, setDiscovering] = useState(true)
  const [selected, setSelected] = useState(null); const [toast, setToast] = useState(null)
  const snapshotEpoch = useRef(0)
  const [selectedDeviceId, setSelectedDeviceId] = useState(null)
  const [theme, setTheme] = useState(() => localStorage.getItem('theme') || 'auto')
  const [systemMeta, setSystemMeta] = useState({ version: '', repository_url: '' })
  const [updateOpen, setUpdateOpen] = useState(false)
  const [updateDialog, setUpdateDialog] = useState(null)
  const [authState, setAuthState] = useState(null)
  const wsEvents = useRef({ handlers: new Set() }); const toastTimer = useRef(null); const unifiedAvailable = useRef(false)
  const updateCompletionSeen = useRef('')
  const refreshInFlight = useRef(false)
  const refreshDebounce = useRef(null)

  useEffect(() => { document.documentElement.dataset.theme = theme; localStorage.setItem('theme', theme) }, [theme])
  // Keep the address bar on the current page without growing history, and follow the hash
  // when the user edits it or navigates back/forward (replaceState never fires hashchange,
  // so the two effects cannot feed each other).
  useEffect(() => {
    const wanted = `#/${view}`
    if (window.location.hash !== wanted) window.history.replaceState(null, '', wanted)
  }, [view])
  useEffect(() => {
    const onHash = () => setView(viewFromHash())
    window.addEventListener('hashchange', onHash)
    return () => window.removeEventListener('hashchange', onHash)
  }, [])
  const dismissToast = useCallback(() => {
    clearTimeout(toastTimer.current)
    toastTimer.current = null
    setToast(null)
  }, [])
  const showToast = useCallback((message) => {
    clearTimeout(toastTimer.current)
    setToast({ message, id: Date.now() })
    toastTimer.current = setTimeout(dismissToast, 15000)
  }, [dismissToast])
  useEffect(() => () => clearTimeout(toastTimer.current), [])
  const openUpdateDialog=useCallback(update=>{setSystemMeta(s=>({...s,update}));setUpdateDialog(update);setUpdateOpen(true)},[])
  const closeUpdateDialog=useCallback(()=>setUpdateOpen(false),[])
  const handleUpdateCompleted=useCallback(status=>{
    const completion=consumeUpdateCompletion(updateCompletionSeen.current,status)
    updateCompletionSeen.current=completion.key
    if(completion.notify)showToast(t('Updated to v{version}',{version:status.target||''}))
  },[showToast,t])
  const expireAuth=useCallback(()=>{
    setCsrf('')
    setAuthState(s=>({...s,configured:true,authenticated:false,csrf:''}))
  },[])

  const refresh = useCallback(async () => {
    if (refreshInFlight.current) return
    refreshInFlight.current = true
    try {
      const epoch = snapshotEpoch.current
      const snapshot = await api.snapshot()
      if (snapshotEpoch.current !== epoch) return
      unifiedAvailable.current = true
      setInstances(snapshot.instances || [])
      setCards(snapshot.cards || [])
      setDevices(snapshot.devices || [])
      setDiscovering(!!snapshot.discovering)
      setSelected(value => value && (snapshot.instances || []).some(item => String(item.id) === String(value)) ? value : null)
    } catch (error) {
      // Retain the last visible snapshot on a failed read; do not invent empty inventory.
      if (error?.status !== 401) showToast(error.message)
    } finally {
      refreshInFlight.current = false
    }
  }, [])
  const scheduleRefresh = useCallback(() => {
    clearTimeout(refreshDebounce.current)
    refreshDebounce.current = setTimeout(refresh, 750)
  }, [refresh])
  useEffect(() => () => clearTimeout(refreshDebounce.current), [])
  useEffect(()=>{
    window.addEventListener('mdd-auth-expired',expireAuth)
    return()=>window.removeEventListener('mdd-auth-expired',expireAuth)
  },[expireAuth])
  useEffect(()=>{ api.authStatus().then(s=>{ if(s.csrf) setCsrf(s.csrf); if(s.token) setAuthToken(s.token); setAuthState(s) }).catch(()=>setAuthState({configured:true,authenticated:false})) },[])
  useEffect(()=>{ if(authState?.authenticated) refresh() },[authState?.authenticated]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(()=>{ if(!authState?.authenticated)return;
    const load=()=>api.systemStatus().then(setSystemMeta).catch(()=>{})
    load(); const timer=setInterval(load,60*1000); return()=>clearInterval(timer) },[authState?.authenticated])
  useEffect(()=>{if(!authState?.authenticated)return;const check=()=>api.checkUpdate().then(update=>setSystemMeta(s=>({...s,update}))).catch(()=>{});check();const timer=setInterval(check,6*60*60*1000);return()=>clearInterval(timer)},[authState?.authenticated])
  // The self-update restarts the control plane, which drops the session mid-update — surface
  // the final outcome on the next sign-in instead.
  useEffect(()=>{if(!authState?.authenticated)return;api.updateProgress().then(s=>{
    const age=(Date.now()-Date.parse(s.updated_at || ''))/1000
    const outcome=updateProgressOutcome(s)
    if(outcome==='failed'&&age>=0&&age<1800)showToast(t('Update failed: {error}',{error:String(s.error_code||s.error||'').split('\n')[0].slice(0,160)}))
    else if(outcome==='complete'&&age>=0&&age<900)handleUpdateCompleted(s)
  }).catch(()=>{})},[authState?.authenticated,handleUpdateCompleted,showToast,t])
  useEffect(()=>{ if(!authState?.authenticated)return; const timer=setInterval(refresh,30000); return()=>clearInterval(timer) },[refresh,authState?.authenticated])

  useEffect(()=>{ if(!authState?.authenticated)return; return connectWs(msg=>{
    if (msg.type === 'go.snapshot' && msg.snapshot) {
      unifiedAvailable.current = true
      ++snapshotEpoch.current
      setInstances(msg.snapshot.instances || [])
      setCards(msg.snapshot.cards || [])
      setDevices(msg.snapshot.devices || [])
      setDiscovering(!!msg.snapshot.discovering)
    }
    const liveStatus=liveStatusFromWsMessage(msg)
    if(liveStatus){
      const { facts, ...status } = liveStatus
      setInstances(list=>list.map(i=>String(i.id)===String(msg.instance)
        ? {...i, status, ...(facts ? { facts } : {})} : i))
      setDevices(list=>list.map(d=>String(d.instance_id)===String(msg.instance)
        ? mergeLiveLineStatus(d, status, facts) : d))
    }
    // The card scan is what makes readers (and their lines) appear. Rebuild the device list
    // from it immediately instead of leaving the page empty until the next 10s poll.
    if(msg.type==='cards'){setCards(msg.cards||[]);scheduleRefresh()}
    if(msg.type==='engine'&&['card_removed','reader_lost','reader_added','reader_removed'].includes(msg.event)){
      const name=msg.args?.[0]
      showToast({card_removed:t('SIM removed — line stopped'),reader_lost:t('Reader unplugged — line stopped'),reader_added:`${t('Card reader connected')}${name?`: ${name}`:''}`,reader_removed:`${t('Card reader disconnected')}${name?`: ${name}`:''}`}[msg.event])
    }
    if(['device','capability','cellular','engine','remote-modem'].includes(msg.type)) scheduleRefresh()
    wsEvents.current.handlers.forEach(h=>h(msg))
    if(msg.type==='sms'&&msg.message?.direction==='in')showToast(t('SMS from {peer}',{peer:msg.message.peer}))
    if(msg.type==='call'&&msg.call?.direction==='in')showToast(t('Incoming call from {peer}',{peer:msg.call.peer}))
  },expireAuth)},[scheduleRefresh,showToast,t,authState?.authenticated,expireAuth])
  const subscribe=useCallback(h=>{wsEvents.current.handlers.add(h);return()=>wsEvents.current.handlers.delete(h)},[])
  const callCoordinator = useGoCallCoordinator({
    enabled: !!authState?.authenticated,
    instances,
    subscribe,
    showToast,
  })
  if (!authState) return <div className="auth-shell"><div className="auth-card"><h1>MDD Sim Gateway</h1><p>{t('Loading…')}</p></div></div>
  if (!authState.authenticated) return <AuthScreen configured={authState.configured} accountUsername={authState.username} t={t} onDone={result=>{if(result.csrf) setCsrf(result.csrf); if(result.token) setAuthToken(result.token); setAuthState(s=>({...s,configured:true,authenticated:true,csrf:result.csrf,token:result.token}))}} />
  const sel=instances.find(i=>i.id===selected)
  const common={devices,discovering,refreshDevices:refresh,instances,cards,selected:sel,setSelected,refresh,subscribe,showToast,setView,selectedDeviceId,setSelectedDeviceId,openUpdateDialog,setSystemMeta,callCoordinator}
  const content={
    overview:<UnifiedOverview {...common}/>, devices:<DevicesPage {...common}/>, imeis:<ImeiPoolPanel {...common}/>, calls:<Softphone {...common}/>,
    messages:<Messages {...common}/>, esim:<Esim {...common}/>, egress:<EgressPage {...common}/>,
    notifications:<NotificationsPage {...common}/>, settings:<SystemPage {...common}/>, diagnostics:<DiagnosticsPage {...common}/>,
  }[view]
  const issueUrl = `${(systemMeta.repository_url || 'https://github.com/MddIdd/mdd-sim-gateway').replace(/\/$/, '')}/issues/new/choose`
  return <div className="u-shell">
    <GlobalGoCallOverlay coordinator={callCoordinator} translate={t} />
    <aside className={`u-sidebar ${menuOpen?'open':''}`}>
      <div className="u-brand"><img src="/logo.svg" alt="" /><div>MDD Sim Gateway<small>{t('4G + VoWiFi unified')}</small></div></div>
      <nav>{NAV.map(([key,label,icon])=><button key={key} className={view===key?'active':''} onClick={()=>{setView(key);setMenuOpen(false)}}><span>{icon}</span>{t(label)}{key==='diagnostics'&&!!systemMeta.host_alerts?.length&&<i className={`u-nav-dot ${systemMeta.host_alerts.some(a=>a.severity==='critical')?'critical':'warning'}`} title={t('The gateway host needs attention')}/>}</button>)}</nav>
      <div className="u-sidebar-foot"><div className="u-theme">{[['auto','◐'],['light','☀'],['dark','☾']].map(([k,x])=><button key={k} className={theme===k?'active':''} onClick={()=>setTheme(k)} title={t(k)}>{x}</button>)}</div><small>{discovering&&!devices.length?t('Detecting devices…'):`${devices.length} ${t(devices.length === 1 ? 'device' : 'devices')}`}</small><a className="u-feedback-link" href={issueUrl} target="_blank" rel="noreferrer"><span>◉</span>{t('Issues and suggestions')}<b>↗</b></a><div className="u-project-meta">{systemMeta.update?.update_available&&systemMeta.update?.release_url?<a className="u-version has-update" href={systemMeta.update.release_url} onClick={e=>{e.preventDefault();setUpdateOpen(true)}} title={t('New version available: v{version}',{version:systemMeta.update.latest})}><i />v{systemMeta.version}</a>:<span className="u-version">{systemMeta.version ? `v${systemMeta.version}` : '—'}</span>}<span className="u-repo-actions">{systemMeta.repository_url&&<><a href={systemMeta.repository_url} target="_blank" rel="noreferrer" aria-label="GitHub" title="GitHub"><svg viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M12 .7a11.5 11.5 0 0 0-3.64 22.4c.58.1.79-.25.79-.56v-2.23c-3.22.7-3.9-1.37-3.9-1.37-.52-1.34-1.29-1.69-1.29-1.69-1.05-.72.08-.71.08-.71 1.17.08 1.78 1.2 1.78 1.2 1.04 1.78 2.72 1.27 3.38.97.1-.75.4-1.27.74-1.56-2.57-.29-5.27-1.29-5.27-5.69 0-1.26.45-2.29 1.19-3.1-.12-.29-.52-1.47.11-3.06 0 0 .97-.31 3.16 1.18a10.9 10.9 0 0 1 5.75 0c2.19-1.49 3.16-1.18 3.16-1.18.63 1.59.23 2.77.11 3.06.74.81 1.19 1.84 1.19 3.1 0 4.42-2.71 5.39-5.29 5.68.42.36.79 1.07.79 2.16v3.2c0 .31.21.67.8.56A11.5 11.5 0 0 0 12 .7Z"/></svg></a><a className="u-star-link" href={systemMeta.repository_url} target="_blank" rel="noreferrer" aria-label={t('Star on GitHub')} title={t('Star on GitHub')}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="m12 2.7 2.75 5.58 6.16.9-4.46 4.34 1.05 6.13L12 16.76l-5.5 2.89 1.05-6.13-4.46-4.34 6.16-.9L12 2.7Z" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinejoin="round"/></svg><b>{starCount(systemMeta.update?.stars) || '—'}</b></a></>}</span></div><button className="btn btn-ghost" onClick={async()=>{try{await api.authLogout()}finally{setCsrf('');setAuthToken('');setAuthState(s=>({...s,configured:true,authenticated:false,csrf:'',token:''}))}}}>{t('Sign out')}</button></div>
    </aside>
    <button className="u-menu" onClick={()=>setMenuOpen(!menuOpen)}>☰</button>
    {menuOpen&&<button className="u-scrim" aria-label={t('Close menu')} onClick={()=>setMenuOpen(false)}/>}
    <main className="u-main"><header><div><h1>{t(NAV.find(x=>x[0]===view)?.[1]||view)}</h1><p>{t(`page.${view}.subtitle`)}</p></div><div className="u-live"><span className="u-dot" />{unifiedAvailable.current?t('Live device control'):t('Compatibility view')}</div></header>
      <div className="u-content"><HostAlerts/>{content}</div></main>
    {toast&&<div className="u-toast" key={toast.id} role="status" style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
      <span style={{ minWidth: 0, overflowWrap: 'anywhere' }}>{toast.message}</span>
      <button type="button" className="btn btn-ghost" aria-label={t('Dismiss')} title={t('Dismiss')} onClick={dismissToast} style={{ flexShrink: 0, background: 'transparent', color: 'inherit' }}>×</button>
    </div>}
    {updateOpen&&updateDialog&&<UpdateModal update={updateDialog} current={systemMeta.version} t={t} onClose={closeUpdateDialog} onCompleted={handleUpdateCompleted}/>}
  </div>
}

function UpdateModal({ update, current, t, onClose, onCompleted }) {
  const [mode,setMode] = useState('checking')
  const [phase,setPhase] = useState('')
  const [target,setTarget] = useState(update.latest)
  const [error,setError] = useState('')
  const [watch,setWatch] = useState(0)
  const [observing,setObserving] = useState(false)
  const primaryAction = useRef(null)
  const expected = useRef('')
  const uncertain = useRef(null)
  const previous = useRef('')
  const submitting = useRef(false)
  const mounted = useRef(true)
  const completed = useRef(onCompleted)
  completed.current = onCompleted
  const canClose = !submitting.current
  useEffect(() => {mounted.current=true;return () => {mounted.current=false}}, [])
  useEffect(() => {if (mode === 'confirm') primaryAction.current?.focus()}, [mode])
  useEffect(() => {
    const key = event => {if (event.key === 'Escape' && canClose) onClose()}
    window.addEventListener('keydown',key)
    return () => window.removeEventListener('keydown',key)
  }, [onClose,canClose])
  useEffect(() => {
    let stopped=false,timer,delay=30000
    const deadline=Date.now()+600000
    setObserving(true)
    const tick=async () => {
      if (stopped) return
      let continuing=false
      try {
        const status=await api.updateProgress()
        if (stopped) return
        const decision=matchUpdateProgress(status,expected.current,uncertain.current)
        if (!decision.accepted) {
          setMode('unknown');setError(decision.code)
          continuing=Boolean(uncertain.current)
        } else {
          previous.current=status.operation_id || ''
          if (uncertain.current) {expected.current=decision.operation;uncertain.current=null}
          const active=['requested','running'].includes(status.state)
          if (active || expected.current || !update.update_available && status.operation_id) {
            if (!expected.current) expected.current=status.operation_id
            setTarget(status.target || update.latest);setPhase(status.phase || status.state)
            if (decision.outcome === 'complete') {setMode('complete');completed.current?.(status)}
            else if (decision.outcome === 'failed') {setMode('failed');setError(status.error_code || status.error || '')}
            else if (active) {setMode('working');setError('');continuing=true}
            else {setMode('unknown');setError('update_state_unknown')}
          } else {setMode('confirm');setError('')}
        }
      } catch (failure) {
        if (stopped) return
        setError(failure.message)
        setMode(failure.status === 401 ? 'restarting' : 'unknown')
        continuing=failure.status !== 401
      }
      if (!stopped && continuing && Date.now()+delay <= deadline) {
        timer=setTimeout(tick,delay);delay=Math.min(delay*2,240000)
      } else if (!stopped) {
        setObserving(false)
        if (continuing) {setMode('unknown');setError('update_observation_timeout')}
      }
    }
    void tick()
    return () => {stopped=true;clearTimeout(timer)}
  }, [watch,update.latest])
  const begin=async () => {
    if (submitting.current || mode !== 'confirm' || !update.comparison_known || !update.update_available) return
    submitting.current=true;setError('');setPhase('requested');setMode('working')
    try {
      const result=await api.applyUpdate(update.latest)
      if (!result.operation_id) throw new Error('update_operation_identity_missing')
      expected.current=result.operation_id
      if (mounted.current) setWatch(value=>value+1)
    } catch (failure) {
      if (!mounted.current) return
      setError(failure.message)
      if (!failure.status || failure.status>=500) {
        uncertain.current={previous:previous.current,target:update.latest}
        setMode('unknown');setWatch(value=>value+1)
      } else setMode('failed')
    } finally {submitting.current=false}
  }
  const mute = { fontSize: 12, color: 'var(--text-mute)' }
  return (
    <div className="u-modal-backdrop" onClick={canClose ? onClose : undefined}>
      <div className="card u-update-modal" role="dialog" aria-modal="true" aria-labelledby="update-dialog-title" onClick={(e) => e.stopPropagation()}>
        {mode === 'confirm' && <>
          <div id="update-dialog-title" style={{ fontWeight: 700, fontSize: 16, marginBottom: 6 }}>{update.update_available ? t('New version available: v{version}', { version: update.latest }) : t('Software update')}</div>
          <div style={{ ...mute, marginBottom: 12 }}>{current} {update.latest ? `→ ${update.latest}` : ''}</div>
          {update.notes && <>
            <div style={{ ...mute, marginBottom: 4 }}>{t('Release notes')}</div>
            <div style={{ maxHeight: '40vh', overflowY: 'auto', whiteSpace: 'pre-wrap', fontSize: 13, lineHeight: 1.5, border: '1px solid var(--border, #8883)', borderRadius: 8, padding: '8px 10px', marginBottom: 12 }}>{update.notes}</div>
          </>}
          <p style={{ ...mute, margin: '0 0 14px' }}>{t('Install the verified release? Services may restart and require signing in again.')}</p>
          <div className="u-modal-actions">
            <button className="btn btn-ghost" onClick={onClose}>{t('Cancel')}</button>
            <a className="btn btn-ghost" href={update.release_url} target="_blank" rel="noreferrer">{t('Release page')}</a>
            <button ref={primaryAction} className="btn btn-primary" disabled={!update.comparison_known || !update.update_available} onClick={begin}>{t('Update now')}</button>
          </div>
        </>}
        {(mode === 'working' || mode === 'restarting' || mode === 'checking') && <>
          <div style={{ fontWeight: 700, fontSize: 16, marginBottom: 10 }}>{t('Updating to v{version}…', { version: target })}</div>
          <p style={{ fontSize: 13, margin: '0 0 6px' }}>
            {mode === 'restarting' ? t('Sign in again to read the update result.') : phase || t('Checking…')}
          </p>
          <p style={{ ...mute, margin: 0 }}>{t('Keep the gateway powered on. This can take a few minutes.')}</p>
        </>}
        {mode === 'unknown' && <div role="alert"><h3>{t('Update outcome unknown')}</h3><p>{error}</p><button className="btn btn-ghost" disabled={observing} onClick={() => setWatch(value=>value+1)}>{t('Refresh status')}</button><button className="btn btn-ghost" onClick={onClose}>{t('Close')}</button></div>}
        {mode === 'complete' && <>
          <div style={{ fontWeight: 700, fontSize: 16, marginBottom: 10 }}>{t('Updated to v{version}', { version: target })}</div>
          <div className="u-modal-actions"><button className="btn btn-primary" onClick={onClose}>{t('Close')}</button></div>
        </>}
        {mode === 'failed' && <>
          <div style={{ fontWeight: 700, fontSize: 16, marginBottom: 10 }}>{t('Update failed')}</div>
          {error && <div style={{ maxHeight: '30vh', overflowY: 'auto', whiteSpace: 'pre-wrap', fontSize: 12, lineHeight: 1.5, border: '1px solid var(--border, #8883)', borderRadius: 8, padding: '8px 10px', marginBottom: 12, wordBreak: 'break-all' }}>{error}</div>}
          <div className="u-modal-actions">
            <button className="btn btn-ghost" onClick={onClose}>{t('Cancel')}</button>
            <button className="btn btn-primary" onClick={() => {expected.current='';uncertain.current=null;setWatch(value=>value+1)}}>{t('Review update')}</button>
          </div>
        </>}
      </div>
    </div>
  )
}

function AuthScreen({ accountUsername, t, onDone }) {
  const [username]=useState(accountUsername || 'admin'); const [password,setPassword]=useState(''); const [error,setError]=useState(''); const [busy,setBusy]=useState(false); const [retry,setRetry]=useState(0)
  useEffect(()=>{if(!retry)return;const timer=setInterval(()=>setRetry(v=>Math.max(0,v-1)),1000);return()=>clearInterval(timer)},[retry])
  const submit=async()=>{if(busy||retry||!password)return;setError('');setBusy(true);try{onDone(await api.authLogin(username,password))}catch(err){if(err.status===429){const seconds=Math.max(1,Number(err.data?.retry_after)||60);setRetry(seconds);setError(t('Too many attempts. Try again in {seconds} seconds.',{seconds}))}else setError(err.message)}finally{setBusy(false)}}
  return <div className="auth-shell"><form className="auth-card" onSubmit={e=>{e.preventDefault();submit()}}><div className="auth-brand"><div className="auth-mark">M</div><h1>MDD Sim Gateway</h1></div><p>{t('Sign in to manage the gateway')}</p><label>{t('Username')}<input value={username} readOnly autoComplete="username" required /></label><label>{t('Password')}<input type="password" value={password} onChange={e=>setPassword(e.target.value)} autoComplete="current-password" minLength="1" required /></label>{error&&<p className="auth-error">{retry?t('Too many attempts. Try again in {seconds} seconds.',{seconds:retry}):error}</p>}<button type="submit" className="primary" disabled={busy||retry>0||!password}>{retry?t('Try again in {seconds}s',{seconds:retry}):t(busy?'Please wait…':'Sign in')}</button></form></div>
}
