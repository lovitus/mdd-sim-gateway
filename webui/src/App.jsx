import React, { useCallback, useEffect, useRef, useState } from 'react'
import { api, connectWs, setCsrf, setAuthToken } from './api.js'
import Softphone from './views/CallsV1.jsx'
import Messages from './views/MessagesV1.jsx'
import Esim from './views/EsimV1.jsx'
import { UnifiedOverview, DevicesPage, ImeiPoolPanel, EgressPage } from './views/UnifiedPages.jsx'
import NotificationsPage from './views/NotificationsV1.jsx'
import SystemPage from './views/SystemV1.jsx'
import DiagnosticsPage from './views/DiagnosticsV1.jsx'
import HostAlertsV1 from './views/HostAlertsV1.jsx'
import { useI18n } from './i18n.jsx'
import { GlobalGoCallOverlay, useGoCallCoordinator } from './goCallCoordinator.jsx'
import { createToastLifecycle } from './toastLifecycle.js'
import { cacheCallAudioBufferMS, getCallAudioBufferMS } from './browserPreferences.js'

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

export default function App() {
  const { t } = useI18n()
  const [view, setView] = useState(viewFromHash); const [menuOpen, setMenuOpen] = useState(false)
  const [instances, setInstances] = useState([]); const [cards, setCards] = useState([]); const [devices, setDevices] = useState([])
  // Sessions live in memory, so signing in normally happens seconds after the control plane
  // restarted — while its first card scan is still running. Until that scan has answered,
  // an empty list means "not known yet", not "no devices".
  const [discovering, setDiscovering] = useState(true)
  const [selected, setSelected] = useState(null); const [toast, setToast] = useState(null)
  const [selectedDeviceId, setSelectedDeviceId] = useState(null)
  const [theme, setTheme] = useState(() => localStorage.getItem('theme') || 'auto')
  const [systemMeta, setSystemMeta] = useState({ version: '', repository_url: '' })
	const [callAudioBufferMS, setCallAudioBufferMS] = useState(getCallAudioBufferMS)
  const [authState, setAuthState] = useState(null)
  const wsEvents = useRef({ handlers: new Set() }); const toastTimer = useRef(null)
  const refreshInFlight = useRef(false)

  const toastLifecycle = useRef(null)
  if (!toastLifecycle.current) toastLifecycle.current = createToastLifecycle({ setToast, timerRef: toastTimer })

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
  const dismissToast = useCallback(() => toastLifecycle.current.dismiss(), [])
  const showToast = useCallback(message => toastLifecycle.current.show(message), [])
  useEffect(() => () => toastLifecycle.current.cleanup(), [])
  const expireAuth=useCallback(()=>{
    setCsrf('')
    setAuthState(s=>({...s,configured:true,authenticated:false,csrf:''}))
  },[])

  const refresh = useCallback(async () => {
    if (refreshInFlight.current) return
    refreshInFlight.current = true
    try {
      const snapshot = await api.snapshot()
      setInstances(snapshot.instances || [])
      setCards(snapshot.cards || [])
      setDevices(snapshot.devices || [])
      setDiscovering(false)
      setSelected(current => current && (snapshot.instances || []).some(item =>
        String(item.id) === String(current)) ? current : null)
    } finally {
      refreshInFlight.current = false
    }
  }, [])
  useEffect(()=>{
    window.addEventListener('mdd-auth-expired',expireAuth)
    return()=>window.removeEventListener('mdd-auth-expired',expireAuth)
  },[expireAuth])
  useEffect(()=>{ api.authStatus().then(s=>{ if(s.csrf) setCsrf(s.csrf); if(s.token) setAuthToken(s.token); setAuthState(s) }).catch(()=>setAuthState({configured:true,authenticated:false})) },[])
  useEffect(()=>{ if(authState?.authenticated) refresh() },[authState?.authenticated]) // eslint-disable-line react-hooks/exhaustive-deps
	useEffect(()=>{ if(!authState?.authenticated)return
		let stopped=false, timer
		const loadPreferences=()=>api.systemPreferences().then(value=>{if(!stopped){
			const buffer=cacheCallAudioBufferMS(value.preferences?.call_audio_buffer_ms);setCallAudioBufferMS(buffer)
		}}).catch(()=>{})
		const visible=()=>{if(document.visibilityState==='visible')loadPreferences()}
		loadPreferences();timer=setInterval(loadPreferences,30000)
		window.addEventListener('focus',loadPreferences);document.addEventListener('visibilitychange',visible)
		return()=>{stopped=true;clearInterval(timer);window.removeEventListener('focus',loadPreferences);document.removeEventListener('visibilitychange',visible)}
	},[authState?.authenticated])
  useEffect(()=>{ if(!authState?.authenticated)return;
    const load=()=>api.systemStatus().then(setSystemMeta).catch(()=>{})
    load(); const timer=setInterval(load,60*1000); return()=>clearInterval(timer) },[authState?.authenticated])
  useEffect(()=>{ if(!authState?.authenticated)return; const timer=setInterval(refresh,30000); return()=>clearInterval(timer) },[refresh,authState?.authenticated])

  useEffect(()=>{ if(!authState?.authenticated)return; return connectWs(msg=>{
    if(msg.type==='go.snapshot'&&msg.snapshot){
      const next=msg.snapshot
      setInstances(next.instances||[])
      setCards(next.cards||[])
      setDevices(next.devices||[])
      setDiscovering(false)
      setSelected(current=>current&&(next.instances||[]).some(item=>String(item.id)===String(current))?current:null)
    }
    wsEvents.current.handlers.forEach(h=>h(msg))
  },expireAuth)},[authState?.authenticated,expireAuth])
  const subscribe=useCallback(h=>{wsEvents.current.handlers.add(h);return()=>wsEvents.current.handlers.delete(h)},[])
  const callCoordinator = useGoCallCoordinator({
    enabled: !!authState?.authenticated,
    instances,
    subscribe,
    showToast,
  })
  if (!authState) return <div className="auth-shell"><div className="auth-card"><h1>MDD Sim Gateway</h1><p>{t('Loading…')}</p></div></div>
  if (!authState.authenticated) return <AuthScreen accountUsername={authState.username} t={t} onDone={result=>{if(result.csrf) setCsrf(result.csrf); if(result.token) setAuthToken(result.token); setAuthState(s=>({...s,configured:true,authenticated:true,csrf:result.csrf,token:result.token}))}} />
  const sel=instances.find(i=>i.id===selected)
  const common={devices,discovering,refreshDevices:refresh,instances,cards,selected:sel,setSelected,refresh,subscribe,showToast,setView,selectedDeviceId,setSelectedDeviceId,setSystemMeta,callAudioBufferMS,setCallAudioBufferMS,callCoordinator,cellularIncoming:callCoordinator}
  const content={
    overview:<UnifiedOverview {...common}/>, devices:<DevicesPage {...common}/>, imeis:<ImeiPoolPanel {...common}/>, calls:<Softphone {...common}/>,
    messages:<Messages {...common}/>, esim:<Esim {...common}/>, egress:<EgressPage {...common}/>,
    notifications:<NotificationsPage {...common}/>, settings:<SystemPage {...common}/>, diagnostics:<DiagnosticsPage {...common}/>,
  }[view]
  const issueUrl = `${(systemMeta.repository_url || 'https://github.com/MddIdd/mdd-sim-gateway').replace(/\/$/, '')}/issues/new/choose`
  const fullVersion = String(systemMeta.version || '')
  const sidebarVersion = /^[0-9a-f]{40}$/i.test(fullVersion) ? fullVersion.slice(0, 12) : fullVersion
  return <div className="u-shell">
    <GlobalGoCallOverlay coordinator={callCoordinator} />
    <aside className={`u-sidebar ${menuOpen?'open':''}`}>
      <div className="u-brand"><img src="/logo.svg" alt="" /><div>MDD Sim Gateway<small>{t('4G + VoWiFi unified')}</small></div></div>
      <nav>{NAV.map(([key,label,icon])=><button key={key} className={view===key?'active':''} onClick={()=>{setView(key);setMenuOpen(false)}}><span>{icon}</span>{t(label)}{key==='diagnostics'&&!!systemMeta.host_alerts?.length&&<i className={`u-nav-dot ${systemMeta.host_alerts.some(a=>a.severity==='critical')?'critical':'warning'}`} title={t('The gateway host needs attention')}/>}</button>)}</nav>
      <div className="u-sidebar-foot"><div className="u-theme">{[['auto','◐'],['light','☀'],['dark','☾']].map(([k,x])=><button key={k} className={theme===k?'active':''} onClick={()=>setTheme(k)} title={t(k)}>{x}</button>)}</div><small>{discovering&&!devices.length?t('Detecting devices…'):`${devices.length} ${t(devices.length === 1 ? 'device' : 'devices')}`}</small><a className="u-feedback-link" href={issueUrl} target="_blank" rel="noreferrer"><span>◉</span>{t('Issues and suggestions')}<b>↗</b></a><div className="u-project-meta"><span className="u-version" title={fullVersion ? `v${fullVersion}` : undefined}>{fullVersion ? `v${sidebarVersion}` : '—'}</span><span className="u-repo-actions">{systemMeta.repository_url&&<><a href={systemMeta.repository_url} target="_blank" rel="noreferrer" aria-label="GitHub" title="GitHub"><svg viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M12 .7a11.5 11.5 0 0 0-3.64 22.4c.58.1.79-.25.79-.56v-2.23c-3.22.7-3.9-1.37-3.9-1.37-.52-1.34-1.29-1.69-1.29-1.69-1.05-.72.08-.71.08-.71 1.17.08 1.78 1.2 1.78 1.2 1.04 1.78 2.72 1.27 3.38.97.1-.75.4-1.27.74-1.56-2.57-.29-5.27-1.29-5.27-5.69 0-1.26.45-2.29 1.19-3.1-.12-.29-.52-1.47.11-3.06 0 0 .97-.31 3.16 1.18a10.9 10.9 0 0 1 5.75 0c2.19-1.49 3.16-1.18 3.16-1.18.63 1.59.23 2.77.11 3.06.74.81 1.19 1.84 1.19 3.1 0 4.42-2.71 5.39-5.29 5.68.42.36.79 1.07.79 2.16v3.2c0 .31.21.67.8.56A11.5 11.5 0 0 0 12 .7Z"/></svg></a></>}</span></div><button className="btn btn-ghost" onClick={async()=>{try{await api.authLogout()}finally{setCsrf('');setAuthToken('');setAuthState(s=>({...s,configured:true,authenticated:false,csrf:'',token:''}))}}}>{t('Sign out')}</button></div>
    </aside>
    <button className="u-menu" onClick={()=>setMenuOpen(!menuOpen)}>☰</button>
    {menuOpen&&<button className="u-scrim" aria-label={t('Close menu')} onClick={()=>setMenuOpen(false)}/>}
    <main className="u-main"><header><div><h1>{t(NAV.find(x=>x[0]===view)?.[1]||view)}</h1><p>{t(`page.${view}.subtitle`)}</p></div><div className="u-live"><span className="u-dot" />{t('Live Go control')}</div></header>
      <div className="u-content"><HostAlertsV1/>{content}</div></main>
    {toast&&<div className="u-toast" key={toast.id} role="status" style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
      <span style={{ minWidth: 0, overflowWrap: 'anywhere' }}>{toast.message}</span>
      <button type="button" className="btn btn-ghost" aria-label={t('Dismiss')} title={t('Dismiss')} onClick={dismissToast} style={{ flexShrink: 0, background: 'transparent', color: 'inherit' }}>×</button>
    </div>}
  </div>
}


function AuthScreen({ accountUsername, t, onDone }) {
  const [username]=useState(accountUsername || 'admin'); const [password,setPassword]=useState(''); const [error,setError]=useState(''); const [busy,setBusy]=useState(false); const [retry,setRetry]=useState(0)
  useEffect(()=>{if(!retry)return;const timer=setInterval(()=>setRetry(v=>Math.max(0,v-1)),1000);return()=>clearInterval(timer)},[retry])
  const submit=async()=>{if(busy||retry||!password)return;setError('');setBusy(true);try{onDone(await api.authLogin(username,password))}catch(err){if(err.status===429){const seconds=Math.max(1,Number(err.data?.retry_after)||60);setRetry(seconds);setError(t('Too many attempts. Try again in {seconds} seconds.',{seconds}))}else setError(err.message)}finally{setBusy(false)}}
  return <div className="auth-shell"><form className="auth-card" onSubmit={e=>{e.preventDefault();submit()}}><div className="auth-brand"><div className="auth-mark">M</div><h1>MDD Sim Gateway</h1></div><p>{t('Sign in to manage the gateway')}</p><label>{t('Username')}<input value={username} readOnly autoComplete="username" required /></label><label>{t('Password')}<input type="password" value={password} onChange={e=>setPassword(e.target.value)} autoComplete="current-password" minLength="1" required /></label>{error&&<p className="auth-error">{retry?t('Too many attempts. Try again in {seconds} seconds.',{seconds:retry}):error}</p>}<button type="submit" className="primary" disabled={busy||retry>0||!password}>{retry?t('Try again in {seconds}s',{seconds:retry}):t(busy?'Please wait…':'Sign in')}</button></form></div>
}
