import React, { useCallback, useEffect, useMemo, useState } from 'react'
import { api } from '../api.js'
import { useI18n } from '../i18n.jsx'
import SimConfig from './SimConfig.jsx'
import Logs from './Logs.jsx'
import VowifiHistory from './VowifiHistory.jsx'
import AllowancePanel from './AllowancePanel.jsx'
import { compactReaderName, lineCallReadinessStatus } from '../linePresentation.js'
import { agentHealthPresentation, agentHeartbeatAge, agentHealthEnumLabel } from '../agentHealthPresentation.js'


const CAP_STATES = ['off', 'starting', 'on', 'stopping', 'degraded', 'error', 'unsupported']

function normalizeState(value, desired) {
  const raw = typeof value === 'object' ? (value.actual || value.state) : value
  const state = String(raw || (desired ? 'starting' : 'off')).toLowerCase()
  return CAP_STATES.includes(state) ? state : (['ok', 'working', 'registered', 'connected', 'active'].includes(state) ? 'on' : 'off')
}

function capability(device, key) {
  const cap = device?.capabilities?.[key] || device?.[key] || {}
  const desired = typeof cap === 'object' ? !!(cap.desired ?? cap.enabled) : !!cap
  return { desired, actual: normalizeState(cap, desired), reason: cap.reason || cap.error || '', available: cap.available !== false }
}

function supportsCellular(device) {
  return device?.device_type !== 'reader' && capability(device, 'cellular').actual !== 'unsupported'
}

function exitNodeLabel(device, t) {
  // The node picker lives on the settings page. Showing the running node here without saying
  // it disagrees with the pinned one reads as "my setting was ignored".
  const exit = device?.egress || {}
  if (!exit.node) return t('Not connected')
  if (!exit.pinned_node || exit.pinned_node === exit.node) return exit.node
  return t('{node} (not your pinned {pinned})', { node: exit.node, pinned: exit.pinned_node })
}

const REGIONAL_FLAG_PAIR = /([\u{1F1E6}-\u{1F1FF}]{2})/gu

function isRegionalFlag(value) {
  const points = [...String(value || '')]
  if (points.length !== 2) return false
  const codes = points.map(point => point.codePointAt(0))
  return codes.every(code => code >= 0x1F1E6 && code <= 0x1F1FF)
}

function ProxyNodeName({ text }) {
  return <>{String(text || '').split(REGIONAL_FLAG_PAIR).map((part, index) => {
    return isRegionalFlag(part)
      ? <span key={`flag-${index}`} className="u-proxy-node-flag">{part}</span>
      : <React.Fragment key={`text-${index}`}>{part}</React.Fragment>
  })}</>
}

// Stating the mismatch without the cause reads as an unexplained override, so give the event
// that actually moved the exit: when it happened, and what the line was failing with.
function exitChangeReason(exit, t, language) {
  const change = exit?.last_change
  if (!change?.ts) return ''
  const at = new Date(change.ts * 1000).toLocaleString(language === 'zh' ? 'zh-CN' : 'en-GB',
    { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
  const why = String(change.reason || '').startsWith('health-freeze:')
    ? t('the line failed ({code})', { code: String(change.reason).split(':')[1] })
    : (change.reason || t('an automatic selection'))
  const cooldown = exit.pinned_cooldown_seconds
  return t('Moved at {at}: {why}.', { at, why })
    + (cooldown ? ' ' + t('Your pinned node is held back for another {minutes} min.',
      { minutes: Math.ceil(cooldown / 60) }) : '')
}

function Badge({ state = 'off', children }) {
  const { t } = useI18n()
  return <span className={`u-badge cap-${state}`}><span className="u-dot" />{children || t(`cap.${state}`)}</span>
}

function LineVerificationPanel({ instances, callCoordinator, setSelected, setView, showToast }) {
  const { t, language } = useI18n()
  const usable = (instances || []).filter(item => item?.id != null)
  const [selectedId, setSelectedId] = useState('')
  const [facts, setFacts] = useState(null)
  const [running, setRunning] = useState('')
  const [error, setError] = useState('')
  const [stabilityTarget, setStabilityTarget] = useState('')
  const [stabilitySeconds, setStabilitySeconds] = useState(50)
  const [stabilityResult, setStabilityResult] = useState(null)
  useEffect(() => {
    if (!selectedId && usable[0]) setSelectedId(String(usable[0].id))
  }, [selectedId, usable])
  const selected = usable.find(item => String(item.id) === String(selectedId))
  const loadFacts = async (passive = false) => {
    if (!selectedId) return
    setError(''); setRunning(passive ? 'passive' : 'refresh')
    try { setFacts(passive ? await api.verifyLinePassive(selectedId) : await api.lineFacts(selectedId)) }
    catch (e) { setError(e.message) } finally { setRunning('') }
  }
  useEffect(() => { if (selectedId) void loadFacts(false) }, [selectedId])
  const testMedia = async () => {
    if (!selectedId) return
    setError(''); setRunning('media')
    try {
      if (!callCoordinator?.verifyMedia) throw new Error('Browser media coordinator is unavailable')
      await callCoordinator.verifyMedia(selectedId)
      await loadFacts(false)
      showToast(language === 'zh' ? '无收费浏览器 WSS 双向 PCM 测试通过。' : 'No-charge browser WSS two-way PCM test passed.')
    } catch (e) { setError(e.message) } finally { setRunning('') }
  }
  const testEgress = async () => {
    const country = String(selected?.proxy_country || facts?.egress?.country || '').toLowerCase()
    if (!country) { setError(language === 'zh' ? '该线路未配置国家出口。' : 'This line has no country exit.') ; return }
    setError(''); setRunning('egress')
    try {
      const result = await api.testEgress(country)
      setFacts(current => ({ ...current, egress: result }))
      showToast(language === 'zh' ? '出口 UDP 探测完成；请同时查看下方原始结果。' : 'Egress UDP probe completed; inspect the raw result below.')
    } catch (e) { setError(e.message) } finally { setRunning('') }
  }
  const openCalls = () => {
    if (!selected) return
    setSelected?.(String(selected.id)); setView?.('calls')
  }
  const manualRegister = async () => {
    if (!selectedId) return
    const text = language === 'zh'
      ? '仅在没有活动通话时发送一次 IMS REGISTER；不会拨号或发短信。继续吗？'
      : 'Send one IMS REGISTER only when the line is proven idle? This does not dial or send SMS.'
    if (!window.confirm(text)) return
    setError(''); setRunning('register')
    try {
      await api.register(selectedId)
      await loadFacts(true)
      showToast(language === 'zh' ? '已提交一次人工 IMS REGISTER。' : 'One manual IMS REGISTER was submitted.')
    } catch (e) { setError(e.message) } finally { setRunning('') }
  }
  const testStability = async () => {
    if (!selectedId) return
    const target = String(stabilityTarget || '').replace(/[\s().-]/g, '')
    if (!/^(?:\d{2,6}|\+[1-9]\d{6,14})$/.test(target)) {
      setError(language === 'zh' ? '请输入短号或国际号码，例如 +448001076285。' : 'Enter a service short code or an international number.'); return
    }
    const seconds = Math.max(10, Math.min(300, Number(stabilitySeconds) || 50))
    const confirmText = language === 'zh'
      ? `将通过当前线路拨打 ${target}，接通后最多测试 ${seconds} 秒，可能产生费用。系统会用该通话自己的 WSS 会话挂断，并核验 Engine 零通道。继续吗？`
      : `Call ${target} on the selected line for up to ${seconds}s after answer? Charges may apply. The exact WSS call session will hang up and Engine idle will be verified. Continue?`
    if (!window.confirm(confirmText)) return
    setError(''); setStabilityResult(null); setRunning('stability')
    try {
      if (!callCoordinator?.runStabilityTest) throw new Error('Browser call coordinator is unavailable')
      const result = await callCoordinator.runStabilityTest(selectedId, target, seconds)
      setStabilityResult(result)
      setFacts(result.facts || null)
      if (result.passed) showToast(language === 'zh' ? '通话稳定测试通过，已核验零活动通道。' : 'Call stability test passed; zero active channels verified.')
      else setError(result.reason || (language === 'zh' ? '通话未达到请求的稳定时长，但已核验零活动通道。' : 'Call did not reach the requested stability duration, but zero active channels was verified.'))
    } catch (e) { setError(e.message) } finally { setRunning('') }
  }
  const entries = Object.entries(facts?.facts || {})
  const stateText = { ready: language === 'zh' ? '就绪' : 'Ready', degraded: language === 'zh' ? '异常' : 'Degraded', blocked: language === 'zh' ? '被阻断' : 'Blocked', unknown: language === 'zh' ? '未知' : 'Unknown' }
  return <>
    <h2>{language === 'zh' ? '线路验证与排障' : 'Line verification & troubleshooting'}</h2>
    <p className="u-note">{language === 'zh'
      ? '状态不是“已注册”的同义词。此页按同一 Engine 世代展示卡路由、隧道、IMS、动作门槛和媒体证据。所有按钮均为手动触发；不会自动拨号或发送短信。'
      : 'Registered is not a health verdict. This view keeps card route, tunnel, IMS, action boundary, and media evidence on one Engine generation. Every action is manual; it never auto-dials or sends SMS.'}</p>
    <div className="u-form-grid"><div><label>{language === 'zh' ? '线路' : 'Line'}</label><select value={selectedId} onChange={e => setSelectedId(e.target.value)}>{usable.map(item => <option key={item.id} value={item.id}>{item.name || item.msisdn || `Line ${item.id}`}</option>)}</select></div></div>
    <div className="u-action-grid" style={{ marginTop: 12 }}>
      <button className="btn btn-ghost" disabled={!selectedId || !!running} onClick={() => loadFacts(false)}>{running === 'refresh' ? (language === 'zh' ? '读取中…' : 'Reading…') : (language === 'zh' ? '刷新事实快照' : 'Refresh facts')}</button>
      <button className="btn btn-ghost" disabled={!selectedId || !!running} onClick={() => loadFacts(true)}>{running === 'passive' ? (language === 'zh' ? '采样中…' : 'Sampling…') : (language === 'zh' ? '无收费端到端采样' : 'No-charge passive sample')}</button>
      <button className="btn btn-ghost" disabled={!selectedId || !!running} onClick={testMedia}>{running === 'media' ? (language === 'zh' ? '媒体测试中…' : 'Testing media…') : (language === 'zh' ? '浏览器 WSS 双向 PCM 测试' : 'Browser WSS PCM test')}</button>
      <button className="btn btn-ghost" disabled={!selectedId || !!running} onClick={testEgress}>{running === 'egress' ? (language === 'zh' ? '检测出口…' : 'Testing egress…') : (language === 'zh' ? '出口 UDP 诊断' : 'Egress UDP diagnostic')}</button>
      <button className="btn btn-ghost" disabled={!selectedId || !!running} onClick={manualRegister}>{running === 'register' ? (language === 'zh' ? '提交中…' : 'Submitting…') : (language === 'zh' ? '人工 IMS 重新注册（空闲线路）' : 'Manual IMS re-register (idle line)')}</button>
      <button className="btn btn-ghost" disabled={!selectedId} onClick={openCalls}>{language === 'zh' ? '打开普通通话页' : 'Open regular Calls page'}</button>
    </div>
    <p className="u-hint">{language === 'zh'
      ? '通话稳定测试由用户明确输入号码并确认后才开始；它复用正常浏览器 WSS 外呼，接通后按绝对时钟挂断，并以独立被动采样核验 Engine 零活动通道。健康轮询永不自动拨号。'
      : 'The stability test starts only after you enter and confirm a target. It reuses normal browser WSS calling, hangs up on an absolute timer after answer, then verifies Engine idle through an independent passive sample. Health polling never dials.'}</p>
    <div className="u-form-grid"><div><label>{language === 'zh' ? '稳定测试号码（收费）' : 'Stability-test number (chargeable)'}</label><input value={stabilityTarget} onChange={e => setStabilityTarget(e.target.value)} placeholder="+448001076285" /></div><div><label>{language === 'zh' ? '接通后测试秒数（10–300）' : 'Seconds after answer (10–300)'}</label><input type="number" min="10" max="300" value={stabilitySeconds} onChange={e => setStabilitySeconds(e.target.value)} /></div></div>
    <button className="btn btn-primary" disabled={!selectedId || !!running} onClick={testStability}>{running === 'stability' ? (language === 'zh' ? '通话稳定测试中…' : 'Running stability test…') : (language === 'zh' ? '开始人工通话稳定测试' : 'Start manual call stability test')}</button>
    <p className="u-hint">{language === 'zh' ? '人工 IMS 重新注册会先由服务端核实当前线路没有活动通话；恢复记录身份不明、当前世代恢复中或有通话时会拒绝，不提供“清空 fence”按钮。' : 'Manual IMS re-registration first proves that the line has no active call. It is refused for an unknown/current recovery owner or a live call; there is intentionally no clear-fence button.'}</p>
    {error && <p className="u-error">{error}</p>}
    {facts && <>
      <div className="u-detail"><span>{language === 'zh' ? '汇总结论' : 'Summary'}</span><b>{stateText[facts.summary?.state] || facts.summary?.state || '—'} · {facts.summary?.code || '—'}</b></div>
      <div className="u-detail"><span>{language === 'zh' ? 'Engine 世代' : 'Engine generation'}</span><b className="mono">{facts.generation?.engine_run_id || '—'}</b></div>
      <div className="u-detail"><span>{language === 'zh' ? '状态样本年龄' : 'Status sample age'}</span><b>{facts.status_source?.age_seconds == null ? '—' : `${facts.status_source.age_seconds}s`}</b></div>
      {entries.map(([name, fact]) => <div className="u-detail" key={name}><span>{name}</span><b><Badge state={fact.state === 'ready' ? 'on' : fact.state === 'blocked' ? 'error' : fact.state === 'degraded' ? 'degraded' : 'off'}>{stateText[fact.state] || fact.state}</Badge> <code>{fact.code}</code></b></div>)}
      {!!facts.summary?.blockers?.length && <p className="u-error">{language === 'zh' ? '当前阻断来源：' : 'Current action blockers: '}{facts.summary.blockers.join(', ')}</p>}
      {!!facts.summary?.unknown?.length && <p className="u-note">{language === 'zh' ? '尚未取得证据：' : 'Evidence not yet collected: '}{facts.summary.unknown.join(', ')}</p>}
      <details><summary>{language === 'zh' ? '完整线路证据（用于人工排障）' : 'Complete line evidence (manual troubleshooting)'}</summary><pre className="mono" style={{ whiteSpace: 'pre-wrap' }}>{JSON.stringify(facts, null, 2)}</pre></details>
      {facts.egress && <details><summary>{language === 'zh' ? '出口探测原始结果' : 'Raw egress result'}</summary><pre className="mono" style={{ whiteSpace: 'pre-wrap' }}>{JSON.stringify(facts.egress, null, 2)}</pre></details>}
    </>}
    {stabilityResult && <details open><summary>{language === 'zh' ? '最近通话稳定测试证据' : 'Latest call stability evidence'}</summary><pre className="mono" style={{ whiteSpace: 'pre-wrap' }}>{JSON.stringify(stabilityResult, null, 2)}</pre></details>}
  </>
}

// A modem can be registered, expose every command and still be unable to submit an SMS
// because its firmware baseline never enabled IMS. Without this the operator only sees an
// unspecified send failure and has no way to tell a firmware precondition from a defect.
function FirmwareAdvice({ advice }) {
  const { language } = useI18n()
  const isZh = language === 'zh'
  const state = String(advice?.state || '')
  if (!state || state === 'verified') return null
  const label = state === 'action_required' ? (isZh ? '固件基线需要处理' : 'Firmware baseline needs attention')
    : state === 'unknown' ? (isZh ? '固件基线未收录' : 'Firmware baseline not recorded')
      : (isZh ? '未上报固件版本' : 'Firmware version not reported')
  return <div className="u-note" style={{ marginTop: 12 }}>
    <Badge state={state === 'action_required' ? 'degraded' : 'unsupported'}>{label}</Badge>
    <p style={{ margin: '6px 0 0' }}>{advice.reason}</p>
    {!!advice.recommended && <p style={{ margin: '4px 0 0' }}>{isZh ? '已验收基线' : 'Accepted baseline'}: {advice.recommended}</p>}
    {!!advice.doc && <p style={{ margin: '4px 0 0' }}>{isZh ? '升级手册' : 'Upgrade procedure'}: {advice.doc}</p>}
    {!!advice.requires_service && <p style={{ margin: '4px 0 0' }}>{isZh
      ? '本网关只检测固件，不会下载或刷写。跨基线升级必须按手册人工停机执行，或更换硬件。'
      : 'This gateway only detects firmware; it never downloads or flashes. A cross-baseline upgrade must follow the documented attended procedure, or the hardware must be replaced.'}</p>}
  </div>
}

// The SMS centre and a known-deficient baseline are the two submit preconditions the page
// cannot infer. Keep them visible even when the capability itself reports ready, because a
// driver can report `sms_ready` while every submit is still rejected.
function SmsAdvisory({ device, refreshDevices, showToast }) {
  const { language } = useI18n()
  const isZh = language === 'zh'
  const [busy, setBusy] = useState('')
  const diagnostics = device?.sms_diagnostics
  const advisory = diagnostics?.advisory || []
  const recovery = diagnostics?.recovery || {}
  const refresh = recovery.refresh || {}
  const restart = recovery.soft_restart || {}
  if (!diagnostics || (!advisory.length && !diagnostics.service_center && !refresh.recommended && !restart.recommended)) return null
  const run = async (kind) => {
    if (kind === 'restart' && !window.confirm(isZh
      ? '软重启会短暂中断该模块的数据、短信和通话。继续吗？'
      : 'Soft restart briefly interrupts this modem\'s data, SMS and calls. Continue?')) return
    setBusy(kind)
    try {
      const result = kind === 'restart'
        ? await api.softRestartDevice(device.id)
        : await api.refreshDeviceSms(device.id)
      showToast?.(kind === 'restart'
        ? (isZh ? '模块软重启已开始，Agent 会自动恢复原配置' : 'Soft restart started; the Agent will restore desired state')
        : (isZh ? `短信配置已刷新${result.service_center ? `：${result.service_center}` : ''}` : 'SMS configuration refreshed'))
      setTimeout(() => refreshDevices?.(), kind === 'restart' ? 5000 : 500)
    } catch (error) { showToast?.(error.message) } finally { setBusy('') }
  }
  return <div className="u-note" style={{ marginTop: 8 }}>
    <div>{isZh ? '短信中心' : 'SMS centre'}: <b>{diagnostics.service_center || (isZh ? '未上报' : 'not reported')}</b></div>
    {advisory.map((item, index) => <p key={index} style={{ margin: '4px 0 0' }}>{item}</p>)}
    {!!refresh.recommended && <><p style={{ margin: '4px 0 0' }}>{refresh.reason}</p>
      <button className="btn btn-ghost" disabled={!!busy} onClick={() => run('refresh')}>{busy === 'refresh' ? (isZh ? '刷新中…' : 'Refreshing…') : (isZh ? '刷新短信配置' : 'Refresh SMS configuration')}</button></>}
    {!!restart.available && !!restart.recommended && <button className="btn btn-ghost" disabled={!!busy} onClick={() => run('restart')} style={{ marginLeft: 8 }}>{busy === 'restart' ? (isZh ? '正在重启…' : 'Restarting…') : (isZh ? '软重启模块' : 'Soft restart modem')}</button>}
  </div>
}

function Empty({ title, detail }) {
  return <div className="u-empty"><div className="u-empty-icon">◇</div><h3>{title}</h3><p>{detail}</p></div>
}

function LineActivity({ device, compact = false }) {
  const { t } = useI18n()
  const status = device?.status
  const factSummary = device?.facts?.summary || null
  if (!status) return null
  // ``status`` is the VoWiFi engine state, not an overall modem state. Windows MBN owns the
  // EC20 SIM, so VoWiFi is unsupported while 4G data and SMS remain fully usable. Hiding this
  // unrelated STOPPED panel prevents a healthy cellular modem from looking globally stopped.
  if (device?.remote_modem && capability(device, 'vowifi').actual === 'unsupported') return null
  const activity = status.activity || {}
  const factState = String(factSummary?.state || '')
  const factCode = String(factSummary?.code || '')
  const current = factCode || activity.current || status.label || t('Checking line status')
  const next = activity.next || ''
  const actual = factState === 'ready' ? 'on'
    : factState === 'blocked' ? 'error'
      : factState === 'degraded' ? 'degraded'
        : factState === 'unknown' ? 'starting' : capability(device, 'vowifi').actual
  const retryCount = Number(activity.retry_count || status.retry?.count || 0)
  const retryMax = Number(activity.retry_max || status.retry?.max || 0)
  return <div className={`u-line-activity ${compact ? 'compact' : ''}`}>
    <div className="u-line-activity-head"><b>{t('Backend activity')}</b><Badge state={actual}>{factState ? `${factState} · ${factCode}` : t(status.label || `cap.${actual}`)}</Badge></div>
    {!compact && factSummary && <p className="u-line-reason"><b>{t('Reason')}:</b> {factCode}</p>}
    {!compact && !factSummary && status.reason && status.state !== 'OK' && <p className="u-line-reason"><b>{t('Reason')}:</b> {t(status.reason)}</p>}
    <div className="u-line-step"><span>{t('Now')}</span><b>{t(current)}</b></div>
    {next && <div className="u-line-step"><span>{t('Next')}</span><b>{t(next, { seconds: activity.seconds || status.automatic_retry_in || 0 })}</b></div>}
    {!factSummary && retryMax > 0 && status.state !== 'OK' && <div className="u-line-retry">
      <div><span>{t('Recovery progress')}</span><b>{retryCount} / {retryMax}</b></div>
      <i><span style={{ width: `${Math.min(100, (retryCount / retryMax) * 100)}%` }} /></i>
    </div>}
  </div>
}

function BrowserVoiceStatus({ device, instances = [], callCoordinator, compact = false }) {
  const { t } = useI18n()
  const iid = String(device?.instance_id || '')
  if (!iid) return null
  const line = instances.find(item => String(item.id || '') === iid)
  if (!line) return null
  const readiness = lineCallReadinessStatus(line, [device], {
    coordinatorLine: callCoordinator?.line?.(iid),
  }, t)
  return <div className={`u-line-activity ${compact ? 'compact' : ''}`}>
    <div className="u-line-activity-head">
      <b>{t('Browser voice')}</b>
      <Badge state={readiness.browserVoiceReady ? 'on' : 'degraded'}>{readiness.browserVoiceLabel}</Badge>
    </div>
    {!compact && <div className="u-line-step"><span>{t('VoWiFi backend')}</span><b>{readiness.imsLabel}</b></div>}
  </div>
}

function LogicalChannels({ value }) {
  const { t } = useI18n()
  if (!value) return null
  return <><div className="u-detail"><span>{t('SIM logical channels')}</span><b>{t('{used} / {total} allocated', { used: value.allocated ?? 0, total: value.capacity ?? 3 })} · {t(`channel.status.${value.status || 'stopped'}`)}</b></div>{(value.items || []).map(item => <div className="u-detail" key={`${item.slot}-${item.channel}`}><span>{t('Logical channel {channel}', { channel: item.channel })}</span><b>{t(`channel.role.${item.role}`)}</b></div>)}{value.error && <p className="u-error">{value.error}</p>}</>
}

export function CapabilitySwitch({ device, kind, onChanged, showToast, compact = false, onNavigateToHardware, onNavigateToSim }) {
  const { t, language } = useI18n()
  const isZh = language === 'zh'
  const [submitting, setSubmitting] = useState(false)
  const [pendingTarget, setPendingTarget] = useState(null)
  const c = capability(device, kind)
  const isReader = device.device_type === 'reader'
  const isImeiMissing = isReader && !device.bound_imei?.is_bound && !device.imei
  const pending = submitting || c.actual === 'starting' || c.actual === 'stopping'
  const unavailable = (!c.available && !isImeiMissing) || c.actual === 'unsupported' || device.compatibilityOnly ||
    device.present === false || (kind === 'cellular' && capability(device, 'flight').desired)
  const title = kind === 'cellular' ? t('4G network') : kind === 'flight' ? t('Flight mode') : kind === 'roaming' ? t('Allow data roaming') : t('VoWiFi / WiFi Calling')
  const canRetry = kind === 'vowifi' && c.desired && ['off', 'degraded', 'error'].includes(c.actual) && !unavailable
  const change = async (next, retry = false) => {
    if (kind === 'vowifi' && next && isImeiMissing) {
      showToast?.(isZh ? '该读卡器尚未设置 IMEI，请在「硬件」标签页添加或选择已保存的 IMEI' : 'Please configure an IMEI in the Hardware tab before enabling VoWiFi')
      onNavigateToHardware?.(device.id)
      return
    }
    const other = capability(device, kind === 'cellular' ? 'vowifi' : 'cellular')
    const impact = retry
      ? t('Restart the VoWiFi line now? The SIM, ePDG and IMS connection will be rebuilt.')
      : kind === 'cellular' && other.desired
      ? t('Changing 4G rebuilds SIM access. VoWiFi may reconnect for 20–60 seconds. Continue?')
      : t('{action} {name}? The UI will wait for the real device state.', { action: next ? t('Enable') : t('Disable'), name: title })
    if (!window.confirm(impact)) return
    setPendingTarget(next)
    setSubmitting(true)
    try {
      const field = kind === 'flight' ? 'flight_mode' : `${kind}_enabled`
      await api.patchDeviceCapabilities(device.id, { [field]: next })
      showToast?.(t('Request accepted; waiting for device state'))
      await onChanged?.()
    } catch (e) {
      const errorDetail = e.data?.detail || {}
      if (errorDetail.code === 'imei_binding_required') {
        showToast?.(isZh ? '该读卡器尚未设置 IMEI，请在「硬件」标签页添加或选择已保存的 IMEI' : 'Please configure an IMEI in the Hardware tab before enabling VoWiFi')
        onNavigateToHardware?.(device.id)
        return
      }
      if (errorDetail.code === 'pin_required' || errorDetail.code === 'pin_invalid') {
        const tries = errorDetail.tries == null ? '' : (isZh ? `，剩余 ${errorDetail.tries} 次尝试` : `; ${errorDetail.tries} attempts remain`)
        showToast?.(isZh ? `需要先在「SIM」标签页输入正确的 SIM PIN${tries}` : `Enter the correct SIM PIN in the SIM tab first${tries}`)
        onNavigateToSim?.(device.id)
        return
      }
      if (errorDetail.code === 'no_card') {
        showToast?.(isZh ? '当前没有检测到 SIM 卡，请检查读卡器连接' : 'No SIM card is currently detected; check the reader connection')
        return
      }
      if (errorDetail.code === 'port_conflict') {
        showToast?.(isZh ? '线路端口被其他程序占用，请使用自动端口；系统会自动选择其它可用端口' : 'A line port is in use; use Automatic port mapping so another block can be selected')
        return
      }
      showToast?.(`${t('Capability change failed')}: ${e.status === 404 ? t('Unified device control is not available on this backend') : e.message}`)
    } finally { setSubmitting(false); setPendingTarget(null) }
  }
  const toggle = () => change(!c.desired)
  const displayedDesired = pendingTarget == null ? c.desired : pendingTarget
  const displayedState = pendingTarget == null ? c.actual : (pendingTarget ? 'starting' : 'stopping')
  const mismatch = displayedDesired && displayedState === 'off'
    ? t('Not applied')
    : !displayedDesired && displayedState === 'on' ? t('Still running') : null
  const detail = c.actual === 'on'
    ? t(kind === 'cellular' ? 'Mobile data is connected.' : kind === 'flight' ? 'Modem RF is disabled.' : kind === 'roaming' ? 'Mobile data may connect while roaming.' : 'Working — connected to the carrier over Wi-Fi.')
    : (c.reason ? t(c.reason) : t(`cap.help.${c.actual}`))
  return <div className={`u-capability ${compact ? 'compact' : ''}`}>
    <div><b>{title}</b><div className="u-cap-detail">{detail}</div></div>
    <div className="u-cap-actions">{canRetry && <button className="btn btn-ghost" disabled={submitting} onClick={() => change(true, true)}>{t('Restart line')}</button>}<Badge state={displayedState}>{device.present === false ? t('Offline') : mismatch}</Badge><button className={`u-switch ${displayedDesired ? 'on' : ''}`} role="switch" aria-checked={displayedDesired}
      aria-label={title} disabled={pending || unavailable} onClick={toggle}><span /></button></div>
  </div>
}

function deviceTitle(d, index) { return compactReaderName(d.name || d.label || d.model || `Device ${index + 1}`) }
function simName(d, t) {
  if (d.sim?.presence === 'unknown') return t('SIM state unknown')
  if (d.present === false) return t('Device not connected')
  return d.sim?.present === false ? t('No SIM inserted') : compactReaderName(d.sim?.name || d.carrier || d.operator || 'SIM')
}
function carrierLabel(d, t) {
  const carrier = d.sim?.carrier || {}
  const names = []
  if (carrier.name) names.push(carrier.name)
  if (carrier.home_network && !names.some(value => value.toLowerCase() === carrier.home_network.toLowerCase())) names.push(carrier.home_network)
  const current = carrier.current_network || d.cellular?.operator || d.operator || ''
  if (current && !['--', 'unknown', 'none', 'n/a'].includes(String(current).toLowerCase()) &&
      !names.some(value => value.toLowerCase() === String(current).toLowerCase())) names.push(current)
  const name = names.join(' · ')
  return `${name || t('Unknown carrier')}${carrier.plmn ? ` (${carrier.plmn})` : ''}`
}
function deviceTypeName(d, t) { return d.device_type === 'reader' ? t('Smart-card reader') : t('Cellular modem') }
function stablePathName(d, t) {
  const path = d.stable_path || d.reader
  return path ? `USB ${compactReaderName(path)}` : t('Stable hardware path unavailable')
}
function deviceSimLine(d, t, language) {
  const name = simName(d, t)
  if (d.present === false || d.sim?.present === false || d.sim?.presence === 'unknown') return name
  const country = d.egress?.detected_country || d.egress?.country
  return country ? `${name} · ${countryName(country, language)}` : name
}
function deviceIdentityLine(d, t) {
  if (d.sim?.presence === 'unknown') return t('SIM state unknown')
  if (d.present === false) return t('Device not connected')
  if (d.sim?.present === false) return t('No SIM inserted')
  const number = d.sim?.number || d.number
  return `${simName(d, t)} · ${number || t('SIM detected')}`
}

const EMPTY_IMEI_DRAFT = { id: '', name: '', imei: '', notes: '' }

export function ImeiPoolPanel({ devices, instances, refreshDevices, showToast }) {
  const { t, language } = useI18n()
  const isZh = language === 'zh'
  const [pool, setPool] = useState([])
  const [bindings, setBindings] = useState({})
  const [draft, setDraft] = useState(EMPTY_IMEI_DRAFT)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState('')

  const load = useCallback(() => {
    setLoading(true)
    return api.imeiPool()
      .then(data => {
        setPool(data?.pool || [])
        setBindings(data?.bindings || {})
      })
      .catch(error => showToast?.(`${t('Error')}: ${error.message}`))
      .finally(() => setLoading(false))
  }, [showToast, t])

  useEffect(() => { load() }, [load])

  const rows = useMemo(() => {
    const found = new Map()
    for (const inst of instances || []) {
      if (!inst.iccid) continue
      found.set(String(inst.iccid), {
        iccid: String(inst.iccid),
        label: inst.name || `Line ${inst.id}`,
        number: inst.msisdn || '',
        state: inst.status?.state || 'STOPPED',
      })
    }
    for (const device of devices || []) {
      const iccid = String(device.sim?.iccid || '')
      if (!iccid) continue
      const previous = found.get(iccid) || { iccid }
      found.set(iccid, {
        ...previous,
        label: previous.label || device.sim?.name || device.name,
        number: previous.number || device.sim?.number || '',
        present: device.present === true,
      })
    }
    for (const iccid of Object.keys(bindings)) {
      if (!found.has(iccid)) found.set(iccid, { iccid, label: isZh ? '历史绑定' : 'Saved binding', state: 'STOPPED' })
    }
    return [...found.values()].sort((a, b) => String(a.label || a.iccid).localeCompare(String(b.label || b.iccid)))
  }, [devices, instances, bindings, isZh])

  const usedIccids = (entryId) => Object.entries(bindings)
    .filter(([, binding]) => binding.imei_id === entryId)
    .map(([iccid]) => iccid)
  const editingUsedBy = draft.id ? usedIccids(draft.id) : []
  const cleanImei = String(draft.imei || '').replace(/\D/g, '')

  const saveEntry = async () => {
    if (!draft.name.trim() || cleanImei.length !== 15) {
      showToast?.(isZh ? '请输入名称和完整的 15 位 IMEI' : 'Enter a name and a complete 15-digit IMEI')
      return
    }
    setBusy('save')
    try {
      await api.saveImeiPoolEntry({ ...draft, name: draft.name.trim(), imei: cleanImei, notes: draft.notes.trim() })
      setDraft(EMPTY_IMEI_DRAFT)
      await load()
      showToast?.(isZh ? 'IMEI 条目已保存' : 'IMEI entry saved')
    } catch (error) { showToast?.(`${t('Error')}: ${error.message}`) }
    finally { setBusy('') }
  }

  const deleteEntry = async (entry) => {
    const used = usedIccids(entry.id)
    if (used.length) {
      showToast?.(isZh ? `该 IMEI 仍绑定 ${used.length} 张 SIM，请先解绑` : `This IMEI is still bound to ${used.length} SIM(s); unbind them first`)
      return
    }
    if (!window.confirm(isZh ? `删除 IMEI“${entry.name}”？` : `Delete IMEI “${entry.name}”?`)) return
    setBusy(`delete:${entry.id}`)
    try {
      await api.deleteImeiPoolEntry(entry.id)
      if (draft.id === entry.id) setDraft(EMPTY_IMEI_DRAFT)
      await load()
      showToast?.(isZh ? 'IMEI 条目已删除' : 'IMEI entry deleted')
    } catch (error) { showToast?.(`${t('Error')}: ${error.message}`) }
    finally { setBusy('') }
  }

  const changeBinding = async (row, entryId) => {
    const current = bindings[row.iccid]
    if (!entryId || current?.imei_id === entryId) return
    const entry = pool.find(item => item.id === entryId)
    if (!entry) return
    const running = row.state && row.state !== 'STOPPED'
    const warning = running
      ? (isZh ? '该线路正在运行。换绑会立即保存，但需要重启线路后才使用新 IMEI。继续？' : 'This line is running. The binding is saved now but takes effect after a line restart. Continue?')
      : (isZh ? `将此 SIM 绑定到“${entry.name}”？` : `Bind this SIM to “${entry.name}”?`)
    if (!window.confirm(warning)) return
    setBusy(`bind:${row.iccid}`)
    try {
      await api.bindImeiToIccid({ iccid: row.iccid, imei_id: entry.id })
      await Promise.all([load(), refreshDevices?.()])
      showToast?.(running
        ? (isZh ? '绑定已保存；请重启该线路使新 IMEI 生效' : 'Binding saved; restart the line to apply the new IMEI')
        : (isZh ? 'IMEI 绑定已保存' : 'IMEI binding saved'))
    } catch (error) { showToast?.(`${t('Error')}: ${error.message}`) }
    finally { setBusy('') }
  }

  const unbind = async (row) => {
    if (!bindings[row.iccid]) return
    if (!window.confirm(isZh ? '解除此 ICCID 的 IMEI 绑定？线路下次启动可能要求重新绑定。' : 'Unbind this ICCID? The line may require a new binding on its next start.')) return
    setBusy(`unbind:${row.iccid}`)
    try {
      await api.unbindImeiFromIccid(row.iccid)
      await Promise.all([load(), refreshDevices?.()])
      showToast?.(isZh ? 'IMEI 绑定已解除' : 'IMEI binding removed')
    } catch (error) { showToast?.(`${t('Error')}: ${error.message}`) }
    finally { setBusy('') }
  }

  return <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
    <div className="card u-panel">
      <div className="u-hardware-intro">
        <h3>{isZh ? 'IMEI 池' : 'IMEI Pool'}</h3>
        <p>{isZh ? '集中保存设备身份，并按 ICCID 绑定。读卡器和槽位变化不会改变 SIM 的绑定。' : 'Save device identities centrally and bind them by ICCID. Reader and slot changes do not alter SIM bindings.'}</p>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(260px, 1fr) minmax(320px, 1.4fr)', gap: 16 }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          <input value={draft.name} onChange={e => setDraft({ ...draft, name: e.target.value })} placeholder={isZh ? '设备名称，例如 Pixel 7 Pro' : 'Device name, e.g. Pixel 7 Pro'} />
          <input className="mono" inputMode="numeric" maxLength={18} disabled={editingUsedBy.length > 0}
            value={draft.imei} onChange={e => setDraft({ ...draft, imei: e.target.value.replace(/\D/g, '') })}
            placeholder={isZh ? '15 位 IMEI' : '15-digit IMEI'} />
          {editingUsedBy.length > 0 && <p className="u-note" style={{ margin: 0 }}>{isZh ? `正被 ${editingUsedBy.length} 张 SIM 使用；解绑前不能修改数字。` : `Used by ${editingUsedBy.length} SIM(s); unbind before changing its digits.`}</p>}
          <textarea rows={2} value={draft.notes} onChange={e => setDraft({ ...draft, notes: e.target.value })} placeholder={isZh ? '备注（可选）' : 'Notes (optional)'} />
          <div style={{ display: 'flex', gap: 8 }}>
            <button className="btn btn-primary" disabled={busy === 'save' || cleanImei.length !== 15 || !draft.name.trim()} onClick={saveEntry}>{busy === 'save' ? (isZh ? '保存中…' : 'Saving…') : (draft.id ? (isZh ? '保存修改' : 'Save changes') : (isZh ? '添加到池' : 'Add to pool'))}</button>
            {draft.id && <button className="btn btn-ghost" onClick={() => setDraft(EMPTY_IMEI_DRAFT)}>{t('Cancel')}</button>}
          </div>
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          {loading ? <p>{t('Reading…')}</p> : pool.length === 0 ? <Empty title={isZh ? 'IMEI 池为空' : 'IMEI pool is empty'} detail={isZh ? '在左侧添加第一个设备身份。' : 'Add the first device identity on the left.'} /> : pool.map(entry => {
            const used = usedIccids(entry.id)
            return <div key={entry.id} style={{ border: '1px solid var(--border)', borderRadius: 10, padding: '10px 12px', display: 'flex', justifyContent: 'space-between', gap: 12, alignItems: 'center' }}>
              <div style={{ minWidth: 0 }}><b>{entry.name}</b><div className="mono" style={{ fontSize: 12, color: 'var(--text-dim)' }}>{entry.imei_masked} · {isZh ? `绑定 ${used.length} 张 SIM` : `${used.length} SIM binding(s)`}</div>{entry.notes && <div style={{ fontSize: 12, color: 'var(--text-mute)' }}>{entry.notes}</div>}</div>
              <div style={{ display: 'flex', gap: 6 }}><button className="btn btn-ghost" onClick={() => setDraft({ id: entry.id, name: entry.name, imei: entry.imei, notes: entry.notes || '' })}>{isZh ? '编辑' : 'Edit'}</button><button className="btn btn-danger-outline" disabled={used.length > 0 || busy === `delete:${entry.id}`} title={used.length ? (isZh ? '请先解除全部 SIM 绑定' : 'Unbind all SIMs first') : ''} onClick={() => deleteEntry(entry)}>{isZh ? '删除' : 'Delete'}</button></div>
            </div>
          })}
        </div>
      </div>
    </div>

    <div className="card u-panel">
      <h3>{isZh ? 'SIM / ICCID 绑定' : 'SIM / ICCID Bindings'}</h3>
      <p className="u-note">{isZh ? '绑定跟随 ICCID，不跟随槽位。运行中的线路换绑后需要重启才会使用新 IMEI。' : 'Bindings follow ICCID, not slots. Restart a running line after changing its binding.'}</p>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        {rows.map(row => {
          const binding = bindings[row.iccid]
          return <div key={row.iccid} style={{ display: 'grid', gridTemplateColumns: 'minmax(170px, 1fr) minmax(230px, 1.2fr) auto', gap: 10, alignItems: 'center', border: '1px solid var(--border)', borderRadius: 10, padding: '10px 12px' }}>
            <div><b>{row.label || 'SIM'}</b>{row.number && <div style={{ fontSize: 12, color: 'var(--text-dim)' }}>{row.number}</div>}<div className="mono" style={{ fontSize: 11, color: 'var(--text-mute)' }}>ICCID {row.iccid}</div></div>
            <select value={binding?.imei_id || ''} disabled={busy === `bind:${row.iccid}`} onChange={e => changeBinding(row, e.target.value)}><option value="">{isZh ? '— 未绑定 —' : '— Unbound —'}</option>{pool.map(entry => <option key={entry.id} value={entry.id}>{entry.name} ({entry.imei_masked})</option>)}</select>
            <button className="btn btn-ghost" disabled={!binding || busy === `unbind:${row.iccid}`} onClick={() => unbind(row)}>{isZh ? '解绑' : 'Unbind'}</button>
          </div>
        })}
        {!rows.length && <Empty title={isZh ? '没有 SIM 记录' : 'No SIM records'} detail={isZh ? '检测到 ICCID 后会显示在这里。' : 'SIMs appear here after an ICCID is detected.'} />}
      </div>
    </div>
  </div>
}

function HardwarePanel({ device, refreshDevices, showToast }) {
  const { t, language } = useI18n()
  const isZh = language === 'zh'
  const isReader = device.device_type === 'reader'
  const [imei, setImei] = useState(device.imei || device.bound_imei?.imei || '')
  const [name, setName] = useState(device.bound_imei?.name || '')
  const [pool, setPool] = useState([])
  const [selectedPoolId, setSelectedPoolId] = useState(device.bound_imei?.imei_id || '')
  const [saving, setSaving] = useState(false)

  const loadPool = () => {
    api.imeiPool().then(data => {
      setPool(data?.pool || [])
    }).catch(() => {})
  }

  useEffect(() => {
    setImei(device.imei || device.bound_imei?.imei || '')
    setName(device.bound_imei?.name || '')
    setSelectedPoolId(device.bound_imei?.imei_id || '')
    loadPool()
  }, [device.id, device.imei, device.bound_imei])

  const handleSelectPool = (poolId) => {
    setSelectedPoolId(poolId)
    if (!poolId) return
    const chosen = pool.find(item => item.id === poolId)
    if (chosen) {
      setImei(chosen.imei)
      setName(chosen.name)
    }
  }

  const save = async () => {
    const digits = String(imei || '').replace(/\D/g, '')
    if (digits.length !== 15) {
      showToast(t('IMEI must contain exactly 15 digits'))
      return
    }
    setSaving(true)
    try {
      const result = await api.saveDeviceHardware(device.id, { imei: digits, name: name.trim() })
      await refreshDevices()
      loadPool()
      showToast(isZh ? '硬件 IMEI 已保存' : t(result.applied ? 'Hardware IMEI saved and the active line was restarted' : 'Hardware IMEI saved'))
    } catch (error) {
      showToast(`${t('Error')}: ${error.message}`)
    } finally {
      setSaving(false)
    }
  }

  const forget = async () => {
    if (device.present) { showToast(t('Disconnect this device before hiding it')); return }
    if (!window.confirm(t('Hide this offline device? All matching data is preserved, and a normal heartbeat will show it again.'))) return
    try {
      await api.deleteDevice(device.id)
      await refreshDevices()
      showToast(t('Offline device hidden; it will reappear after a normal heartbeat'))
    } catch (error) { showToast(`${t('Error')}: ${error.message}`) }
  }

  const imeiClean = String(imei || '').replace(/\D/g, '')

  return (
    <div className="card u-panel u-hardware-panel">
      <div className="u-hardware-intro">
        <h3>{t('Hardware')}</h3>
        <p>{t('The device name identifies this hardware in the interface. Model and firmware appear only when the hardware reports them.')}</p>
      </div>
      <div className="u-details cols u-hardware-facts">
        <div className="u-detail"><span>{t('Device name')}</span><b>{deviceTitle(device, 0)}</b></div>
        <div className="u-detail"><span>{t('Device type')}</span><b>{deviceTypeName(device, t)}</b></div>
        {device.model && <div className="u-detail"><span>{t('Model')}</span><b>{device.model}</b></div>}
        {device.firmware && <div className="u-detail"><span>{t('Firmware version')}</span><b>{device.firmware}</b></div>}
        <div className="u-detail"><span>{t('Stable path')}</span><b>{stablePathName(device, t)}</b></div>
        <LogicalChannels value={device.logical_channels}/>
        {!isReader && <div className="u-detail"><span>IMEI</span><b>{device.imei_masked || t('Hardware did not report')}</b></div>}
        {isReader && (
          <div className="u-detail">
            <span>{isZh ? '当前 IMEI' : 'Current IMEI'}</span>
            <b>{device.bound_imei?.is_bound ? `${device.bound_imei.name} (${device.bound_imei.imei_masked})` : (device.imei_masked || (isZh ? '未设置' : 'Not set'))}</b>
          </div>
        )}
      </div>
      <FirmwareAdvice advice={device.firmware_advice} />

      {isReader && (
        <div className="u-hardware-action u-hardware-imei">
          <div className="u-hardware-action-copy">
            <h4>{isZh ? '读卡器硬件 / 设备 IMEI' : t('Hardware IMEI')}</h4>
            <p>{isZh ? '该 IMEI 将持久绑定当前 SIM 卡。可直接输入 15 位数字，或从已保存的列表中选择。' : t('This IMEI belongs to the physical reader. Any SIM inserted here uses it automatically.')}</p>
          </div>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 10, maxWidth: 420, width: '100%' }}>
            {pool.length > 0 && (
              <div>
                <label style={{ display: 'block', marginBottom: 4, fontSize: 13, color: 'var(--text-muted)' }}>
                  {isZh ? '从已保存的列表中选择' : 'Select from saved IMEIs'}
                </label>
                <select
                  value={selectedPoolId}
                  onChange={e => handleSelectPool(e.target.value)}
                  style={{ width: '100%', padding: '8px 10px', borderRadius: 6, border: '1px solid var(--border)', background: 'var(--bg-card)', color: 'inherit' }}
                >
                  <option value="">{isZh ? '-- 手动输入或选择预存设备 --' : '-- Enter manually or select saved --'}</option>
                  {pool.map(item => (
                    <option key={item.id} value={item.id}>
                      {item.name} ({item.imei_masked})
                    </option>
                  ))}
                </select>
              </div>
            )}

            <div>
              <label style={{ display: 'block', marginBottom: 4, fontSize: 13, color: 'var(--text-muted)' }}>
                {isZh ? '15 位 IMEI' : '15-digit IMEI'}
              </label>
              <input
                className="mono"
                inputMode="numeric"
                maxLength={18}
                value={imei}
                onChange={e => {
                  setImei(e.target.value.replace(/[^0-9 -]/g, ''))
                  setSelectedPoolId('')
                }}
                placeholder={t('15-digit IMEI required for VoWiFi')}
                style={{ width: '100%' }}
              />
            </div>

            <div>
              <label style={{ display: 'block', marginBottom: 4, fontSize: 13, color: 'var(--text-muted)' }}>
                {isZh ? '设备型号名称 (可选，保存至常用列表)' : 'Device Name (Optional, saved for reuse)'}
              </label>
              <input
                type="text"
                value={name}
                onChange={e => setName(e.target.value)}
                placeholder={isZh ? '例如：Pixel 7 Pro / iPhone 15' : 'e.g. Pixel 7 Pro'}
                style={{ width: '100%' }}
              />
            </div>

            <div style={{ marginTop: 4 }}>
              <button
                className="btn btn-primary"
                disabled={saving || imeiClean.length !== 15}
                onClick={save}
              >
                {saving ? (isZh ? '保存中…' : 'Saving…') : t('Save')}
              </button>
            </div>
          </div>
        </div>
      )}

      <div className="u-hardware-action u-hardware-danger">
        <div className="u-hardware-action-copy">
          <h4>{t('Hide offline device')}</h4>
          <p>{t('This only hides the offline entry. All matching data is retained, and the device reappears after a normal heartbeat.')}</p>
        </div>
        <button className="btn btn-danger-outline" disabled={device.present} onClick={forget}>{t('Hide device')}</button>
      </div>
    </div>
  )
}

function Discovering({ t }) {
  return <div className="u-empty"><div className="u-empty-icon u-empty-spinner">◌</div>
    <h3>{t('Detecting devices…')}</h3>
    <p>{t('The gateway is reading the connected readers and modems. This takes a few seconds after a restart.')}</p></div>
}

function ImsCapabilityBadges({ device }) {
  const { t } = useI18n()
  const values = device?.ims_capabilities || {}
  return <div className="u-details cols" style={{ marginTop: 10 }}>
    {[["voice", t('Voice')], ["sms", t('SMS')], ["rcs", 'RCS']].map(([key, label]) => {
      const item = values[key] || { actual: 'off' }
      return <div className="u-detail" key={key} title={item.reason || ''}>
        <span>{label}</span><Badge state={normalizeState(item, false)} />
      </div>
    })}
  </div>
}

function CellularProfilePanel({ device, showToast, refreshDevices }) {
  const { t } = useI18n()
  const defaultName = `MDD-${String(device?.sim?.iccid || '').slice(-4) || 'SIM'}`
  const [profiles, setProfiles] = useState([])
  const [suggestedProfiles, setSuggestedProfiles] = useState([])
  const [candidateId, setCandidateId] = useState('custom')
  const [supported, setSupported] = useState(true)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [draft, setDraft] = useState({ name: defaultName, apn: '', auth: 'NONE', username: '', password: '' })
  const load = useCallback(() => {
    setLoading(true); setError('')
    api.deviceCellularProfiles(device.id)
      .then(result => {
        const suggestions = result.suggested_profiles || (result.suggested_apns || []).map((apn, index) => ({ id: `apn-${index}`, name: apn, apn, auth: 'NONE', username: '' }))
        setProfiles(result.profiles || []); setSuggestedProfiles(suggestions)
        setSupported(result.supported !== false); if (result.error) setError(result.error)
        if (suggestions.length === 1) setDraft(value => ({ ...value, apn: value.apn || suggestions[0].apn, auth: suggestions[0].auth || 'NONE', username: suggestions[0].username || '' }))
      })
      .catch(e => setError(e.message))
      .finally(() => setLoading(false))
  }, [device.id])
  useEffect(() => {
    setDraft({ name: defaultName, apn: '', auth: 'NONE', username: '', password: '' })
    load()
  }, [defaultName, load])
  const save = async e => {
    e.preventDefault()
    if (!draft.name.trim() || !draft.apn.trim()) return
    setSaving(true); setError('')
    try {
      const result = await api.saveDeviceCellularProfile(device.id, draft)
      setDraft(value => ({ ...value, name: result.name || value.name, apn: result.apn || value.apn, password: '' }))
      showToast(t('Mobile broadband profile saved on the Agent host.'))
      load(); await refreshDevices?.()
    } catch (e) { setError(e.message) } finally { setSaving(false) }
  }
  const chooseCandidate = id => {
    setCandidateId(id)
    if (id === 'custom') return
    const candidate = suggestedProfiles.find(item => item.id === id)
    if (!candidate) return
    const suffix = String(device?.sim?.iccid || '').slice(-4) || 'SIM'
    setDraft({ name: `MDD-${suffix}-${candidate.apn}`.slice(0, 100), apn: candidate.apn || '',
      auth: candidate.auth || 'NONE', username: candidate.username || '', password: '' })
  }
  return <div className="u-profile-editor">
    <h3>{t('Mobile broadband profile')}</h3>
    <p className="u-note">{t('The Agent saves this profile in the operating system on the modem host. The gateway never stores or returns the password.')}</p>
    {loading ? <p>{t('Loading…')}</p> : profiles.length ? <div className="u-detail"><span>{t('System profiles')}</span><b>{profiles.map(item => item.name).join(', ')}</b></div> : <p className="u-note">{t('No system profile is configured. MDD will automatically use one unambiguous data APN reported by the modem; otherwise choose a candidate below.')}</p>}
    {error && <p className="u-error">{error}</p>}
    {supported && <form onSubmit={save}>
      {suggestedProfiles.length > 0 && <div className="u-profile-source"><label>{t('Configuration source')}</label><select value={candidateId} onChange={e => chooseCandidate(e.target.value)}><option value="custom">{t('Custom configuration')}</option>{suggestedProfiles.map(candidate => <option key={candidate.id} value={candidate.id}>{candidate.name || candidate.apn} · {candidate.pdp_type || 'IP'} · {candidate.auth || 'NONE'}</option>)}</select><p>{t('Selecting a modem candidate fills every available field. Passwords are never read back and remain empty.')}</p></div>}
      <div className="u-form-grid">
        <div><label>{t('Profile name')}</label><input value={draft.name} maxLength={100} onChange={e => setDraft({ ...draft, name: e.target.value })} required /></div>
        <div><label>APN</label><input value={draft.apn} maxLength={100} autoComplete="off" onChange={e => { setCandidateId('custom'); setDraft({ ...draft, apn: e.target.value }) }} placeholder={t('Provided by the carrier')} required /></div>
        <div><label>{t('Authentication')}</label><select value={draft.auth} onChange={e => setDraft({ ...draft, auth: e.target.value })}><option value="NONE">{t('None')}</option><option value="PAP">PAP</option><option value="CHAP">CHAP</option><option value="MSCHAPV2">MSCHAPv2</option></select></div>
        <div><label>{t('Username')}</label><input value={draft.username} maxLength={200} autoComplete="off" onChange={e => setDraft({ ...draft, username: e.target.value })} /></div>
        <div><label>{t('Password')}</label><input type="password" value={draft.password} maxLength={500} autoComplete="new-password" onChange={e => setDraft({ ...draft, password: e.target.value })} /></div>
      </div>
      <div className="u-inline"><span className="u-muted">{t('Save the profile, then enable 4G or allow roaming to retry the connection.')}</span><button className="btn btn-primary" disabled={saving || !draft.name.trim() || !draft.apn.trim()}>{t(saving ? 'Saving…' : 'Save profile')}</button></div>
    </form>}
  </div>
}

export function UnifiedOverview({
  devices,
  discovering,
  refreshDevices,
  setView,
  showToast,
  instances,
  setSelectedDeviceId,
  setSelected,
  subscribe,
  callCoordinator,
}) {
  const { t } = useI18n()
  const pending = discovering && !devices.length
  const counts = useMemo(() => ({
    devices: devices.length,
    cellular: devices.filter(d => capability(d, 'cellular').actual === 'on').length,
    vowifi: devices.filter(d => capability(d, 'vowifi').actual === 'on').length,
    attention: devices.filter(d => ['error', 'degraded'].includes(capability(d, 'cellular').actual) || ['error', 'degraded'].includes(capability(d, 'vowifi').actual)).length,
  }), [devices])
  return <div className="u-page">
    <div className="u-metrics">
      {[[t('Devices'), counts.devices], [t('4G online'), counts.cellular], [t('VoWiFi online'), counts.vowifi], [t('Needs attention'), counts.attention]].map(([l,v]) => <div className="u-metric" key={l}><span>{l}</span><strong>{pending ? '—' : v}</strong></div>)}
    </div>
    {pending ? <Discovering t={t} /> :
      !devices.length ? <Empty title={t('No communication devices found')} detail={t('Connect a modem or smart-card reader. Discovery updates automatically.')} /> :
      <div className="u-device-grid">{devices.map((d, i) => <div className="card u-device-card" key={d.id}>
        <div className="u-card-head"><div><h2>{deviceTitle(d, i)}</h2><p>{deviceIdentityLine(d, t)}</p></div><Badge state={d.present === false ? 'error' : 'on'}>{d.present === false ? t('Offline') : t('Detected')}</Badge></div>
        <div className="u-card-body">{supportsCellular(d) && <><CapabilitySwitch device={d} kind="cellular" compact onChanged={refreshDevices} showToast={showToast} /><CapabilitySwitch device={d} kind="roaming" compact onChanged={refreshDevices} showToast={showToast} /></>}<CapabilitySwitch device={d} kind="vowifi" compact onChanged={refreshDevices} showToast={showToast} onNavigateToHardware={() => { setSelectedDeviceId(d.id); setView('devices') }} onNavigateToSim={() => { setSelectedDeviceId(d.id); setView('devices') }} /><LineActivity device={d} compact /><BrowserVoiceStatus device={d} instances={instances} callCoordinator={callCoordinator} compact />{capability(d, 'vowifi').desired && <VowifiHistory instanceId={d.instance_id} subscribe={subscribe} compact />}
          <div className="u-details"><div className="u-detail"><span>{t('Carrier')}</span><b>{carrierLabel(d, t)}</b></div><div className="u-detail"><span>{t('Country exit')}</span><b className="u-proxy-node-text"><ProxyNodeName text={exitNodeLabel(d, t) || d.proxy_node || t('Not connected')} /></b></div></div>
          <ImsCapabilityBadges device={d} />
          {d.instance_id && <AllowancePanel instanceId={String(d.instance_id)} showToast={showToast} />}
        </div><div className="u-card-foot"><button className="btn btn-ghost" onClick={() => { if (d.instance_id) setSelected(String(d.instance_id)); setView('calls') }}>{t('Call')}</button><button className="btn btn-ghost" onClick={() => { if (d.instance_id) setSelected(String(d.instance_id)); setView('messages') }}>{t('Message')}</button><button className="btn btn-primary" onClick={() => { setSelectedDeviceId(d.id); setView('devices') }}>{t('Details')}</button></div>
      </div>)}</div>}
  </div>
}

export function DevicesPage({
  devices,
  discovering,
  refreshDevices,
  instances,
  cards,
  selected,
  setSelected,
  refresh,
  showToast,
  selectedDeviceId,
  setSelectedDeviceId,
  subscribe,
  callCoordinator,
}) {
  const { t, language } = useI18n(); const [tab, setTab] = useState('status')
  const active = devices.some(device => device.id === selectedDeviceId) ? selectedDeviceId : devices[0]?.id
  useEffect(() => { if (active && active !== selectedDeviceId) setSelectedDeviceId(active) }, [active, selectedDeviceId, setSelectedDeviceId])
  const d = devices.find(x => x.id === active)
  useEffect(() => { if (d && !supportsCellular(d) && tab === 'cellular') setTab('status') }, [d, tab])
  if (!d) return discovering ? <Discovering t={t} /> : <Empty title={t('No communication devices found')} detail={t('Connect a modem or smart-card reader. Discovery updates automatically.')} />
  const isZh = language === 'zh'
  const tabs = [['status',t('Status')],['sim','SIM'],...(supportsCellular(d) ? [['cellular',t('4G network / APN')]] : []),['vowifi','VoWiFi'],['hardware',t('Hardware')],['imeis', isZh ? 'IMEI 池' : 'IMEI Pool'],['trash', isZh ? '回收站' : 'Recycle Bin']]
  return <div className="u-split"><aside className="card u-device-list">{devices.map((x,i)=><button key={x.id} className={`u-device-option ${x.id===active?'active':''}`} onClick={()=>setSelectedDeviceId(x.id)}><b className="u-device-option-name">{deviceTitle(x,i)}</b><span className="u-device-option-sim">{deviceSimLine(x, t, language)}</span><span className="u-device-option-status"><Badge state={x.present === false ? 'error' : 'on'}>{x.present === false ? t('Offline') : t('Online')}</Badge></span></button>)}</aside>
    <section className="u-page"><div className="u-page-heading"><div><h2>{deviceTitle(d, devices.indexOf(d))}</h2><p>{deviceTypeName(d, t)} · {stablePathName(d, t)}</p></div></div><div className="u-tabs">{tabs.map(([k,l])=><button key={k} className={tab===k?'active':''} onClick={()=>setTab(k)}>{l}</button>)}</div>
      {tab==='status' && <div className="card u-panel">{supportsCellular(d) ? <><CapabilitySwitch device={d} kind="cellular" onChanged={refreshDevices} showToast={showToast}/><CapabilitySwitch device={d} kind="roaming" onChanged={refreshDevices} showToast={showToast}/><CapabilitySwitch device={d} kind="flight" onChanged={refreshDevices} showToast={showToast}/></> : <p className="u-note">{t('This is a smart-card reader. It provides SIM access for VoWiFi and has no 4G radio.')}</p>}<CapabilitySwitch device={d} kind="vowifi" onChanged={refreshDevices} showToast={showToast} onNavigateToHardware={() => setTab('hardware')} onNavigateToSim={() => setTab('sim')} /><LineActivity device={d}/><BrowserVoiceStatus device={d} instances={instances} callCoordinator={callCoordinator}/><ImsCapabilityBadges device={d}/><SmsAdvisory device={d} refreshDevices={refreshDevices} showToast={showToast}/><FirmwareAdvice advice={d.firmware_advice}/><p className="u-note">{t('Cellular data, flight mode and VoWiFi are independent controls. Flight mode disables modem RF; the 4G switch only connects or disconnects mobile data.')}</p><p className="u-note">{t('Software support means the technical path is implemented. Actual availability still depends on the SIM plan, carrier, region, modem firmware and device-identity policy.')}</p></div>}
      {tab==='sim' && <div className="card u-panel"><SimConfig instances={instances} selected={selected} refresh={refresh} cards={cards} setSelected={setSelected} targetDevice={d} devices={devices}/></div>}
      {tab==='cellular' && <div className="card u-panel"><h3>{t('4G network')}</h3><CapabilitySwitch device={d} kind="cellular" onChanged={refreshDevices} showToast={showToast}/><CapabilitySwitch device={d} kind="roaming" onChanged={refreshDevices} showToast={showToast}/><CapabilitySwitch device={d} kind="flight" onChanged={refreshDevices} showToast={showToast}/>{d.cellular ? <div className="u-details cols"><div className="u-detail"><span>{t('Registration')}</span><b>{d.cellular.registration || t('Not connected')}</b></div><div className="u-detail"><span>{t('Operator')}</span><b>{d.cellular.operator || t('Not connected')}</b></div><div className="u-detail"><span>APN</span><b>{d.cellular.apn || t('Automatic')}</b></div><div className="u-detail"><span>{t('IP address')}</span><b>{d.cellular.ip || t('Waiting')}</b></div><div className="u-detail"><span>{t('Signal')}</span><b>{d.cellular.signal == null ? t('Waiting') : `${d.cellular.signal}%`}</b></div><div className="u-detail"><span>{t('Traffic')}</span><b>↓ {formatBytes(d.cellular.rx_bytes)} · ↑ {formatBytes(d.cellular.tx_bytes)}</b></div><div className="u-detail"><span>{t('Data profile')}</span><b>{d.cellular.profile || t('Automatic')}</b></div><div className="u-detail"><span>{t('Network interface')}</span><b>{d.cellular.interface || t('Waiting')}</b></div></div>:<Empty title={t('Cellular data not connected')} detail={t('Turn on 4G to let the per-device ModemManager backend establish a data bearer.')} />}<CellularProfilePanel device={d} showToast={showToast} refreshDevices={refreshDevices}/></div>}
      {tab==='vowifi' && <div className="card u-panel"><h3>VoWiFi</h3><CountryExitControl device={d} refresh={refresh} showToast={showToast}/><LineActivity device={d}/><BrowserVoiceStatus device={d} instances={instances} callCoordinator={callCoordinator}/><ImsCapabilityBadges device={d}/><VowifiHistory instanceId={d.instance_id} subscribe={subscribe}/><div className="u-details cols"><div className="u-detail"><span>ePDG / IKE</span><b>{d.facts?.facts?.tunnel?.code || (typeof d.vowifi?.epdg === 'object' ? (d.vowifi.epdg.ike_reason || (d.vowifi.epdg.pcscf ? t('Tunnel connected') : t('Waiting'))) : (d.vowifi?.epdg || d.status?.state || t('Not connected')))}</b></div><div className="u-detail"><span>IMS / SIP</span><b>{d.facts?.facts?.ims?.code || d.vowifi?.ims || d.status?.label || t('Not connected')}</b></div><div className="u-detail"><span>{t('Country exit')}</span><b className="u-proxy-node-text"><ProxyNodeName text={exitNodeLabel(d, t)} /></b></div><div className="u-detail"><span>{t('Rekey')}</span><b>{d.vowifi?.rekey_minutes ?? 30} {t('minutes')}</b></div></div>{!!d.egress?.pinned_node && d.egress.pinned_node !== d.egress.node && !!exitChangeReason(d.egress, t, language) && <p className="u-note u-proxy-node-text"><ProxyNodeName text={exitChangeReason(d.egress, t, language)} /></p>}<p className="u-note">{t('Software support means the technical path is implemented. Actual availability still depends on the SIM plan, carrier, region, modem firmware and device-identity policy.')}</p></div>}
      {tab==='hardware' && <HardwarePanel device={d} refreshDevices={refreshDevices} showToast={showToast}/>}
      {tab==='imeis' && <ImeiPoolPanel devices={devices} instances={instances} refreshDevices={refreshDevices} showToast={showToast}/>}
      {tab==='trash' && <RecycleBinPanel refresh={refresh} showToast={showToast}/>}
    </section>
  </div>
}


function RecycleBinPanel({ refresh, showToast }) {
  const { t, language } = useI18n()
  const isZh = language === 'zh'
  const [items, setItems] = useState([])
  const [loading, setLoading] = useState(true)
  const [restoring, setRestoring] = useState('')

  const load = useCallback(() => {
    setLoading(true)
    api.softDeletedInstances()
      .then(res => setItems(res.instances || []))
      .catch(() => setItems([]))
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => { load() }, [load])

  const restore = async (id) => {
    setRestoring(id)
    try {
      await api.restoreInstance(id)
      showToast(isZh ? `已成功恢复卡片 ${id}！` : `Successfully restored line ${id}!`)
      await refresh?.()
      load()
    } catch (e) {
      showToast(`${t('Error')}: ${e.message}`)
    } finally {
      setRestoring('')
    }
  }

  if (loading) return <div className="card u-panel"><p>{t('Reading…')}</p></div>
  if (!items.length) return <div className="card u-panel"><Empty title={isZh ? '回收站为空' : 'Recycle Bin is empty'} detail={isZh ? '没有已软删除的 SIM 卡。软删除的卡片会保存在这里，可随时无损恢复。' : 'No soft-deleted SIM lines. Soft-deleted lines are kept here and can be restored anytime.'} /></div>

  return <div className="card u-panel">
    <h3>{isZh ? '回收站 (已软删除卡片)' : 'Recycle Bin (Soft-deleted lines)'}</h3>
    <p className="u-note">{isZh ? '这些卡片处于软删除状态（已暂停且在主界面隐藏），所有短信、通话记录及配置均已完整保留。点击“恢复”即可将其重新激活。' : 'These lines are soft-deleted (stopped and hidden). All settings and history are preserved. Click Restore to reactivate.'}</p>
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10, marginTop: 14 }}>
      {items.map(item => (
        <div key={item.id} style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '12px 14px', border: '1px solid var(--border)', borderRadius: 10 }}>
          <div>
            <div style={{ fontWeight: 700, fontSize: 14 }}>{item.name || `SIM-${item.id}`} <span style={{ fontSize: 12, color: 'var(--text-mute)', fontWeight: 400 }}>({item.id})</span></div>
            <div style={{ fontSize: 12, color: 'var(--text-dim)', fontFamily: 'monospace', marginTop: 3 }}>ICCID: {item.iccid || '—'} · IMSI: {item.imsi || '—'}</div>
          </div>
          <button className="btn btn-primary" disabled={restoring === item.id} onClick={() => restore(item.id)}>
            {restoring === item.id ? (isZh ? '恢复中…' : 'Restoring…') : (isZh ? '恢复' : 'Restore')}
          </button>
        </div>
      ))}
    </div>
  </div>
}

function CountryExitControl({ device, refresh, showToast }) {

  const { t, language } = useI18n()
  const [saving, setSaving] = useState(false)
  const route = device.egress || {}
  const countries = [...new Set([...(route.available_countries || []), route.detected_country, route.country].filter(Boolean))]
    .sort((a, b) => countryLabel(a, language).localeCompare(countryLabel(b, language)))
  const select = async (country) => {
    if (!device.instance_id) return
    setSaving(true)
    try {
      const result = await api.setLineCountry(device.instance_id, country)
      await refresh()
      showToast(t(country ? 'Country exit changed to {country}' : 'Country exit returned to automatic detection', {
        country: country ? countryLabel(result.effective_country || country, language) : '' }))
    } catch (error) { showToast(`${t('Error')}: ${error.message}`) }
    finally { setSaving(false) }
  }
  return <div className="u-note" style={{ marginBottom: 16 }}>
    <label>{t('Country exit selection')}</label>
    <select value={route.override || ''} disabled={saving || !device.instance_id} onChange={event => select(event.target.value)}>
      <option value="">{route.detected_country
        ? t('Automatic — detected {country}', { country: countryLabel(route.detected_country, language) })
        : t('Automatic — country not detected')}</option>
      {countries.map(country => <option value={country} key={country}>{countryLabel(country, language)}</option>)}
    </select>
    <div style={{ fontSize: 12, color: 'var(--text-mute)', marginTop: 6 }}>
      {device.instance_id
        ? t('The SIM country is detected automatically. Select a country only when the detected route is wrong.')
        : t('Waiting for the SIM identity before a country exit can be selected.')}
    </div>
  </div>
}

const COUNTRY_CODES = `ad ae af ag ai al am ao ar at au az ba bb bd be bf bg bh bi bj bn bo br bs bt bw by bz ca cd cf cg ch ci ck cl cm cn co cr cu cv cy cz de dj dk dm do dz ec ee eg er es et fi fj fm fr ga gb gd ge gh gm gn gq gr gt gw gy hk hn hr ht hu id ie in iq ir is it jm jo jp ke kg kh ki km kn kp kr kw ky kz la lb lc li lk lr ls lt lu lv ly ma mc md me mg mk ml mm mn mo mr mt mu mv mw mx my mz na ne ng ni nl no np nz om pa pe pg ph pk pl pr ps pt pw py qa ro rs ru rw sa sb sc sd se sg si sk sl sm sn so sr ss st sv sy sz td tg th tm tn to tr tt tv tw tz ua ug us uy uz vc ve vg vi vn vu ws ye za zm zw`.split(' ')

function countryLabel(code, language) {
  try { return `${new Intl.DisplayNames([language === 'zh' ? 'zh-CN' : 'en'], { type: 'region' }).of(code.toUpperCase())} (${code.toUpperCase()})` }
  catch { return code.toUpperCase() }
}

function countryName(code, language) {
  try { return new Intl.DisplayNames([language === 'zh' ? 'zh-CN' : 'en'], { type: 'region' }).of(code.toUpperCase()) }
  catch { return code.toUpperCase() }
}

function countryKeywords(code) {
  const values = [code.toUpperCase()]
  for (const locale of [navigator.language || 'zh-CN', 'en']) {
    try { values.push(new Intl.DisplayNames([locale], { type: 'region' }).of(code.toUpperCase())) } catch { /* ISO code remains */ }
  }
  return [...new Set(values.filter(Boolean))]
}

function formatBytes(value) {
  const n = Number(value || 0)
  if (n < 1024) return `${n} B`
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(1)} KiB`
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MiB`
  return `${(n / 1024 ** 3).toFixed(1)} GiB`
}

function EyeIcon({ open }) {
  return <svg className="u-eye-icon" viewBox="0 0 24 24" aria-hidden="true"><path d="M2.4 12s3.5-6 9.6-6 9.6 6 9.6 6-3.5 6-9.6 6-9.6-6-9.6-6Z"/><circle cx="12" cy="12" r="2.7"/>{!open && <path d="M4 4 20 20"/>}</svg>
}

export function EgressPage({ showToast }) {
  const { t, language } = useI18n()
  const [s, setS] = useState(null)
  const [live, setLive] = useState(null)
  const [newCountry, setNewCountry] = useState('')
  const [profileDraft, setProfileDraft] = useState(null)
  const [revealSensitive, setRevealSensitive] = useState(false)
  const [saving, setSaving] = useState(false)
  const [profileTests, setProfileTests] = useState({})
  const [remoteModems, setRemoteModems] = useState([])
  const loadLive = () => api.egressStatus().then(setLive).catch(() => setLive(null))
  useEffect(() => {
    api.settings().then(setS).catch(() => setS({ proxy: {} }))
    api.cellularSims().then(result => setRemoteModems(result.sims || [])).catch(() => setRemoteModems([]))
    loadLive()
    // The exit node changes on its own when a line fails, so a snapshot taken at mount goes
    // stale with nothing on screen admitting it — the page would still show the node that
    // was in use when it was opened.
    const timer = setInterval(loadLive, 5000)
    return () => clearInterval(timer)
  }, [])
  useEffect(() => {
    if (!profileDraft) return undefined
    const closeOnEscape = event => { if (event.key === 'Escape') setProfileDraft(null) }
    window.addEventListener('keydown', closeOnEscape)
    return () => window.removeEventListener('keydown', closeOnEscape)
  }, [profileDraft])
  if (!s) return <p>{t('Loading')}…</p>
  const proxy = s.proxy || { profiles: {}, exits: {} }
  const patch = p => setS(x => ({ ...x, proxy: { ...x.proxy, ...p } }))
  const profiles = proxy.profiles || {}
  const profileTypeLabel = profile => profile.type === 'subscription' ? t('Subscription link') : profile.type === 'node' ? t('Individual node') : profile.type === 'existing' ? t('Imported outbound') : profile.type === 'cellular_sim' ? t('Data SIM') : 'SOCKS5'
  const patchExit = (country, p) => patch({ exits: { ...(proxy.exits || {}), [country]: { ...(proxy.exits?.[country] || {}), ...p } } })
  const patchProfile = (id, p) => patch({ profiles: { ...profiles, [id]: { ...profiles[id], ...p } } })
  const removeExit = country => { const exits = { ...(proxy.exits || {}) }; delete exits[country]; patch({ exits }) }
  const openAddProfile = () => setProfileDraft({ type: 'subscription', name: '', url: '', refresh_minutes: 30, value: '', server: '', port: 1080, username: '', password: '', iccid: '' })
  const confirmAddProfile = () => {
    if (!profileDraft) return
    const id = `proxy-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 7)}`
    const name = profileDraft.name.trim() || t(profileDraft.type === 'subscription' ? 'New subscription' : profileDraft.type === 'node' ? 'New node' : profileDraft.type === 'cellular_sim' ? 'Data SIM' : 'New SOCKS5 proxy')
    const detail = profileDraft.type === 'subscription'
      ? { url: profileDraft.url.trim(), refresh_minutes: profileDraft.refresh_minutes || 30 }
      : profileDraft.type === 'node'
        ? { value: profileDraft.value.trim() }
        : profileDraft.type === 'cellular_sim'
          ? { sim_iccid: profileDraft.iccid }
          : { server: profileDraft.server.trim(), port: profileDraft.port || 1080, username: profileDraft.username, password: profileDraft.password }
    patch({ profiles: { ...profiles, [id]: { name, type: profileDraft.type, ...detail } } })
    setProfileDraft(null)
  }
  const draftReady = !!profileDraft && (profileDraft.type === 'subscription' ? !!profileDraft.url.trim() : profileDraft.type === 'node' ? !!profileDraft.value.trim() : profileDraft.type === 'cellular_sim' ? /^\d{18,22}$/.test(profileDraft.iccid) : !!profileDraft.server.trim() && +profileDraft.port > 0 && +profileDraft.port <= 65535)
  const removeProfile = id => {
    const countries = Object.entries(proxy.exits || {}).filter(([, ex]) => ex.profile_id === id).map(([country]) => country.toUpperCase())
    if (countries.length) { showToast(t('This proxy is used by: {countries}', { countries: countries.join(', ') })); return }
    const next = { ...profiles }; delete next[id]
    setS(current => ({ ...current, proxy: { ...current.proxy, profiles: next },
      updates: current.updates?.proxy_profile_id === id
        ? { proxy_mode: 'auto', proxy_profile_id: '' } : current.updates }))
  }
  const testProfile = async id => {
    setProfileTests(x => ({ ...x, [id]: { busy: true } }))
    try {
      const result = await api.testProxyProfile(id, profiles[id])
      setProfileTests(x => ({ ...x, [id]: { ok: true, latency: result.latency_ms, target: result.target } }))
      showToast(t('Node UDP test passed ({latency} ms via {target})', {
        latency: result.latency_ms, target: result.target || '—' }))
    } catch (error) {
      const translated = t(error.message)
      const safeProbeDetail = /^(UDP probes (failed|timed out):|UDP test failed:|SOCKS5 proxy returned an invalid UDP response|UDP DNS response did not match)/.test(error.message)
      const message = translated !== error.message || safeProbeDetail
        ? translated
        : t('UDP test failed. Check the proxy address, credentials, protocol and UDP support.')
      setProfileTests(x => ({ ...x, [id]: { ok: false, error: message } }))
      showToast(message)
    }
  }
  const addExit = () => { if (!newCountry) return; patchExit(newCountry, { enabled: true, profile_id: '', keywords: countryKeywords(newCountry) }); setNewCountry('') }
  const available = COUNTRY_CODES.filter(code => !proxy.exits?.[code]).sort((a, b) => countryLabel(a, language).localeCompare(countryLabel(b, language)))
  const save = async () => { setSaving(true); try { await api.saveSettings(s); await api.refreshEgress(); showToast(t('Saved')); setTimeout(loadLive, 1000) } catch (e) { showToast(`${t('Error')}: ${e.message}`) } finally { setSaving(false) } }
  return <div className="u-page">
    <div className="card u-panel u-routing-policy"><div className="u-card-head"><div><h2>{t('Country proxy routing')}</h2><p>{t('When enabled, VoWiFi uses the proxy assigned to its SIM country and never falls back to the default network if that exit fails.')}</p></div><div className="u-head-actions"><Badge state={proxy.enabled && live ? 'on' : 'off'}>{proxy.enabled ? (live ? t('Enabled') : t('Status unavailable')) : t('Disabled')}</Badge><label className="u-title-toggle"><span>{t('Enable country proxy exits')}</span><input type="checkbox" className="u-toggle" checked={!!proxy.enabled} onChange={e => patch({ enabled: e.target.checked })} /></label></div></div><p className="u-routing-impact">{proxy.enabled ? t('On: each line uses its country exit. If the proxy or UDP validation fails, only that line’s VoWiFi stops; it will not leak through the host’s default network.') : t('Off: country exits are bypassed and VoWiFi uses the host’s default network. Country assignments and proxy settings are kept for later.')}</p>{Object.values(profiles).some(profile => profile.type === 'existing') && <><label>{t('Existing sing-box config')}</label><input className="mono" value={proxy.existing_singbox_config || ''} onChange={e => patch({ existing_singbox_config: e.target.value })} placeholder="/etc/sing-box/config.json" /></>}</div>
    <div className="u-section-title u-proxy-library-head"><div><h2>{t('Proxy library')}</h2><p>{t('Add reusable subscriptions, individual nodes, or SOCKS5 proxies, then assign them to country exits below.')}</p></div><div className="u-proxy-toolbar"><button className="u-icon-button" type="button" aria-pressed={revealSensitive} onClick={() => setRevealSensitive(x => !x)} title={t(revealSensitive ? 'Hide sensitive information' : 'Show sensitive information')}><EyeIcon open={revealSensitive}/><span>{t('Sensitive information')}</span></button><button className="btn btn-primary" onClick={openAddProfile}>{t('+ Add proxy')}</button></div></div>
    {!Object.keys(profiles).length ? <Empty title={t('No proxies configured')} detail={t('Add a subscription, individual node, or SOCKS5 proxy above.')} /> : <div className="u-proxy-list">{Object.entries(profiles).map(([id, profile]) => {
      const usedBy = Object.entries(proxy.exits || {}).filter(([, ex]) => ex.profile_id === id).map(([country]) => countryLabel(country, language))
      return <div className="card u-proxy-row" key={id}>
        <div className="u-proxy-identity"><span className="u-proxy-kind">{profileTypeLabel(profile)}</span><input aria-label={t('Name')} value={profile.name || ''} onChange={e => patchProfile(id, { name: e.target.value })} />{usedBy.length ? <small>{t('Used by {countries}', { countries: usedBy.join(', ') })}</small> : <small>{t('Not assigned to a country exit')}</small>}</div>
        <div className="u-proxy-primary">
          {profile.type === 'subscription' && <><label>{t('Subscription URL')}</label><input className="mono" type={revealSensitive ? 'text' : 'password'} autoComplete="off" value={profile.url || ''} onChange={e => patchProfile(id, { url: e.target.value })} placeholder="https://…" /></>}
          {profile.type === 'node' && <><label>{t('Node chain (one hop per line)')}</label><textarea className={`mono ${!revealSensitive && profile.value ? 'u-secret-text' : ''}`} rows="3" spellCheck="false" autoComplete="off" value={profile.value || ''} onChange={e => patchProfile(id, { value: e.target.value })} placeholder={t('vless://first-hop…\nsocks5://final-exit…')} /><small>{t('Enter hops in traffic order. The first line is reached first; the last line is the public exit.')}</small></>}
          {profile.type === 'socks5' && <><label>{t('Server')}</label><input className="mono" type={revealSensitive ? 'text' : 'password'} value={profile.server || ''} onChange={e => patchProfile(id, { server: e.target.value })} /></>}
          {profile.type === 'cellular_sim' && <><label>SIM</label><select value={profile.sim_iccid || ''} onChange={e => patchProfile(id, { sim_iccid: e.target.value })}><option value="">{t('Select a SIM…')}</option>{remoteModems.map(modem => <option key={modem.iccid} value={modem.iccid}>{modem.phone || modem.line_name || `•••• ${modem.iccid.slice(-4)}`} · {modem.online ? t('Online') : t('Offline')}</option>)}</select></>}
          {profile.type === 'existing' && <><label>{t('Existing outbound tag')}</label><input value={profile.outbound_tag || ''} onChange={e => patchProfile(id, { outbound_tag: e.target.value })} /></>}
        </div>
        <div className="u-proxy-secondary">
          {profile.type === 'subscription' && <><label>{t('Refresh interval')}</label><div className="u-number-suffix"><input type="number" min="1" value={profile.refresh_minutes || 30} onChange={e => patchProfile(id, { refresh_minutes: +e.target.value })} /><span>{t('minutes')}</span></div></>}
          {profile.type === 'node' && <small>{t('Up to 4 UDP-capable hops; a single line keeps the existing behavior.')}</small>}
          {profile.type === 'socks5' && <><label>{t('Port')}</label><input type="number" min="1" max="65535" value={profile.port || 1080} onChange={e => patchProfile(id, { port: +e.target.value })} /></>}
          {profile.type === 'existing' && <small>{t('Compatibility entry')}</small>}
          {profile.type === 'cellular_sim' && <small>{t('The binding follows the ICCID when the SIM moves to another modem or agent.')}</small>}
        </div>
        {profile.type === 'socks5' && <div className="u-proxy-auth"><div><label>{t('Username')}</label><input type={revealSensitive ? 'text' : 'password'} autoComplete="off" value={profile.username || ''} onChange={e => patchProfile(id, { username: e.target.value })} /></div><div><label>{t('Password')}</label><input type={revealSensitive ? 'text' : 'password'} autoComplete="new-password" value={profile.password || ''} onChange={e => patchProfile(id, { password: e.target.value })} /></div></div>}
        <div className="u-proxy-actions">{['node', 'socks5', 'cellular_sim'].includes(profile.type) && <><button className="btn btn-ghost" disabled={profileTests[id]?.busy} onClick={() => testProfile(id)}>{t(profileTests[id]?.busy ? 'Testing…' : 'Test node UDP')}</button>{profileTests[id]?.ok && <small className="u-test-ok">{t('Passed')} · {profileTests[id].latency} ms</small>}{profileTests[id] && !profileTests[id].busy && !profileTests[id].ok && <small className="u-test-error" title={profileTests[id].error}>{t('Failed')}</small>}</>}<button className="btn btn-ghost u-proxy-remove" onClick={() => removeProfile(id)}>{t('Remove')}</button></div>
      </div>
    })}</div>}
    <div className="u-section-title"><div><h2>{t('Country exits')}</h2><p>{t('If no healthy UDP exit exists, only that SIM’s VoWiFi stops; 4G remains available.')}</p></div><div className="u-inline u-add-exit"><select value={newCountry} onChange={e => setNewCountry(e.target.value)}><option value="">{t('Select a country/region…')}</option>{available.map(code => <option key={code} value={code}>{countryLabel(code, language)}</option>)}</select><button className="btn btn-primary" disabled={!newCountry} onClick={addExit}>{t('+ Add')}</button></div></div>
    {!Object.keys(proxy.exits || {}).length ? <Empty title={t('No country exits configured')} detail={t('Choose a country above, then configure its node source and keywords.')} /> : <div className="u-device-grid">{Object.entries(proxy.exits).map(([country, ex]) => {
      const st = live?.exits?.[country]
      const selected = profiles[ex.profile_id]
      const subscription = selected?.type === 'subscription'
      return <div className="card u-panel" key={country}><div className="u-card-head"><h3>{countryLabel(country, language)}</h3><div className="u-head-actions"><Badge state={st?.ready ? 'on' : st ? 'error' : 'off'}>{st?.ready ? t('Exit running') : t('Not connected')}</Badge><label className="u-title-toggle"><span>{t('Enabled')}</span><input type="checkbox" className="u-toggle" checked={ex.enabled !== false} onChange={e => patchExit(country, { enabled: e.target.checked })} /></label></div></div>
        <label>{t('Exit proxy')}</label><select value={ex.mode === 'direct' ? '__direct' : ex.profile_id || ''} onChange={e => patchExit(country, e.target.value === '__direct' ? { mode: 'direct', profile_id: '' } : { mode: '', profile_id: e.target.value })}><option value="">{t('Select a proxy…')}</option>{Object.entries(profiles).map(([id, item]) => <option key={id} value={id}>{item.name || t('Unnamed proxy')} · {profileTypeLabel(item)}</option>)}<option value="__direct">{t('Explicit direct connection')}</option></select>
        {subscription && <><label>{t('Node-name keywords (comma-separated)')}</label><input value={(ex.keywords || []).join(', ')} onChange={e => patchExit(country, { keywords: e.target.value.split(',').map(x => x.trim()).filter(Boolean) })} /></>}
        {subscription
          ? <><label>{t('Current node')}</label>
            {/* The pinned name is kept in the list even when the live status is missing, so
                opening this page before the orchestrator answers cannot silently drop it. */}
            <select className="u-proxy-node-select" value={ex.pinned_node || ''} onChange={e => patchExit(country, { pinned_node: e.target.value })}>
              <option value="">{st?.node ? t('Automatic — changes only when a line fails ({node})', { node: st.node }) : t('Automatic')}</option>
              {[...new Set([...(st?.candidates || []), ...(ex.pinned_node ? [ex.pinned_node] : [])])].map(name => <option key={name} value={name}>{name}</option>)}
            </select>
            {st?.pinned_missing && <p className="u-error u-proxy-node-text"><ProxyNodeName text={t('Pinned node “{node}” is no longer offered by the subscription; automatic selection is in use.', { node: ex.pinned_node })} /></p>}
            {/* Without this the picker shows the chosen node while the exit quietly runs on
                another one, which reads as "my setting did nothing". */}
            {!!ex.pinned_node && !!st?.node && st.node !== ex.pinned_node && !st?.pinned_missing
              && ((ex.pin_mode || 'lock') === 'lock'
                ? <p className="u-error u-proxy-node-text"><ProxyNodeName text={t('Not in use: the exit is running on “{node}”. Check whether the locked node is reachable.', { node: st.node })} /></p>
                : <p className="u-note u-proxy-node-text"><ProxyNodeName text={`${t('The preferred node is not in use; the exit is running on “{node}”. It returns to your preferred node the next time the exit has to change.', { node: st.node })} ${exitChangeReason(st, t, language)}`} /></p>)}
            {!!ex.pinned_node && <><label>{t('If that node stops working')}</label>
              <select value={ex.pin_mode || 'lock'} onChange={e => patchExit(country, { pin_mode: e.target.value })}>
                <option value="lock">{t('Keep using it — never switch automatically')}</option>
                <option value="prefer">{t('Move to another node, and come back to this one later')}</option>
              </select>
              <p className="u-note">{(ex.pin_mode || 'lock') === 'lock'
                ? t('Locked: the line stays down until you change this. Use it for a controlled comparison.')
                : t('Preferred: a failing line moves to another node, and returns to this one the next time the exit has to change anyway.')}</p></>}</>
          : <div className="u-detail"><span>{t('Current node')}</span><b className="u-proxy-node-text"><ProxyNodeName text={st?.node || '—'} /></b></div>}
        {st?.error && <p className="u-error">{st.error}</p>}
        <div className="u-inline"><button className="btn btn-ghost" onClick={async () => { try { const result = await api.testEgress(country); await loadLive(); showToast(t('Applied exit UDP DNS probe passed ({latency} ms via {target})', { latency: result.latency_ms, target: result.target || '—' })) } catch (e) { showToast(e.message) } }}>{t('Test applied exit')}</button><button className="btn btn-ghost" onClick={() => removeExit(country)}>{t('Remove')}</button></div>
      </div>
    })}</div>}
    <button className="btn btn-primary" disabled={saving} onClick={save}>{t('Save and apply')}</button>
    {profileDraft && <div className="u-modal-backdrop" onClick={() => setProfileDraft(null)}>
      <div className="card u-proxy-modal" role="dialog" aria-modal="true" aria-labelledby="add-proxy-title" onClick={e => e.stopPropagation()}>
        <div className="u-proxy-modal-head"><div><h2 id="add-proxy-title">{t('Add proxy')}</h2><p>{t('Choose a source type. You can change the details before adding it to the library.')}</p></div><button className="u-modal-close" type="button" onClick={() => setProfileDraft(null)} aria-label={t('Cancel')}>×</button></div>
        <div className="u-proxy-type-grid">
          {[
            ['subscription', t('Subscription link'), t('Paste a Clash subscription URL. The gateway fetches it automatically, extracts compatible nodes, and refreshes it on schedule.'), '📡'],
            ['node', t('Individual node'), t('Paste one or more share links, one hop per line. The last line is the public exit.'), '🔗'],
            ['socks5', 'SOCKS5', t('Connect to a SOCKS5 server directly. It must support UDP ASSOCIATE for VoWiFi.'), '🧦'],
            ['cellular_sim', t('Data SIM'), t('Borrow mobile data from an online remote modem. The mapping follows ICCID.'), '📶'],
          ].map(([type, title, detail, icon]) => <button type="button" key={type} className={`u-proxy-type ${profileDraft.type === type ? 'active' : ''}`} onClick={() => setProfileDraft({ ...profileDraft, type })}><span className="u-proxy-type-icon" aria-hidden="true">{icon}</span><b>{title}</b><small>{detail}</small></button>)}
        </div>
        <div className="u-proxy-modal-form">
          <label>{t('Name')} <span>{t('optional')}</span></label><input autoFocus value={profileDraft.name} onChange={e => setProfileDraft({ ...profileDraft, name: e.target.value })} placeholder={t(profileDraft.type === 'subscription' ? 'New subscription' : profileDraft.type === 'node' ? 'New node' : 'New SOCKS5 proxy')} />
          {profileDraft.type === 'subscription' && <><label>{t('Subscription URL')}</label><input className="mono" type={revealSensitive ? 'text' : 'password'} autoComplete="off" value={profileDraft.url} onChange={e => setProfileDraft({ ...profileDraft, url: e.target.value })} placeholder="https://…" /><label>{t('Refresh interval (minutes)')}</label><input type="number" min="1" value={profileDraft.refresh_minutes} onChange={e => setProfileDraft({ ...profileDraft, refresh_minutes: +e.target.value })} /></>}
          {profileDraft.type === 'node' && <><label>{t('Node chain (one hop per line)')}</label><textarea className="mono" rows="5" spellCheck="false" value={profileDraft.value} onChange={e => setProfileDraft({ ...profileDraft, value: e.target.value })} placeholder={t('vless://first-hop…\nsocks5://final-exit…')} /><p className="u-note">{t('Enter hops in traffic order. The first line is reached first; the last line is the public exit.')}</p></>}
          {profileDraft.type === 'socks5' && <div className="u-form-grid"><div><label>{t('Server')}</label><input className="mono" value={profileDraft.server} onChange={e => setProfileDraft({ ...profileDraft, server: e.target.value })} placeholder="proxy.example.com" /></div><div><label>{t('Port')}</label><input type="number" min="1" max="65535" value={profileDraft.port} onChange={e => setProfileDraft({ ...profileDraft, port: +e.target.value })} /></div><div><label>{t('Username (optional)')}</label><input value={profileDraft.username} onChange={e => setProfileDraft({ ...profileDraft, username: e.target.value })} /></div><div><label>{t('Password (optional)')}</label><input type={revealSensitive ? 'text' : 'password'} autoComplete="new-password" value={profileDraft.password} onChange={e => setProfileDraft({ ...profileDraft, password: e.target.value })} /></div></div>}
          {profileDraft.type === 'cellular_sim' && <><label>SIM</label><select value={profileDraft.iccid} onChange={e => setProfileDraft({ ...profileDraft, iccid: e.target.value })}><option value="">{t('Select a SIM…')}</option>{remoteModems.map(modem => <option key={modem.iccid} value={modem.iccid}>{modem.phone || modem.line_name || `•••• ${modem.iccid.slice(-4)}`} · {modem.online ? t('Online') : t('Offline')}</option>)}</select><p className="u-note">{t('If the SIM is unplugged, the exit fails closed. Reinsert the same ICCID anywhere and it can recover without editing this profile.')}</p></>}
        </div>
        <div className="u-modal-actions"><button className="btn btn-ghost" onClick={() => setProfileDraft(null)}>{t('Cancel')}</button><button className="btn btn-primary" disabled={!draftReady} onClick={confirmAddProfile}>{t('Add to proxy library')}</button></div>
      </div>
    </div>}
  </div>
}

export function NotificationsPage({ showToast }) {
  const { t } = useI18n(); const [s, setS] = useState(null); const [tab, setTab] = useState('channels'); const [deliveries, setDeliveries] = useState({ pending: [], history: [] })
  const loadDeliveries = () => api.notificationDeliveries().then(setDeliveries).catch(() => setDeliveries({ pending: [], history: [] }))
  useEffect(() => { api.settings().then(setS).catch(() => setS({ webhook: {}, telegram: {}, pushplus: {} })); loadDeliveries() }, [])
  useEffect(() => { if (tab === 'delivery') loadDeliveries() }, [tab])
  if (!s) return <p>{t('Loading')}…</p>
  const wh = s.webhook || {}, tg = s.telegram || {}, pp = s.pushplus || {}
  const setChannel = (key, patch) => setS(x => ({ ...x, [key]: { ...(x[key] || {}), ...patch } }))
  const setEvent = (key, cfg, event, checked) => setChannel(key, { events: { ...(cfg.events || {}), [event]: checked } })
  const eventOptions = (key, cfg) => <div className="u-event-options"><label>{t('Forward these events')}</label><div className="u-inline">{[['incoming_call', t('Incoming call')], ['incoming_sms', t('Incoming SMS')], ['host_alert', t('Host alert')], ['number_changed', t('Line number changed')], ['line_unrecoverable', t('Line cannot recover')], ['activation_reminder', t('Activation reminder')]].map(([event, label]) => <label key={event}><input type="checkbox" className="u-toggle" checked={cfg.events?.[event] !== false} onChange={e => setEvent(key, cfg, event, e.target.checked)} />{label}</label>)}</div></div>
  const save = async () => { try { await api.saveSettings(s); showToast(t('Saved')) } catch (e) { showToast(e.message) } }
  return <div className="u-page"><div className="u-tabs"><button className={tab === 'channels' ? 'active' : ''} onClick={() => setTab('channels')}>{t('Channels')}</button><button className={tab === 'delivery' ? 'active' : ''} onClick={() => setTab('delivery')}>{t('Delivery log')}</button></div>
    {tab === 'channels' && <div className="u-device-grid"><div className="card u-panel"><div className="u-card-head"><div><h2>Webhook</h2><p>{t('Standard GET or POST webhook with optional custom fields.')}</p></div><input type="checkbox" className="u-toggle" checked={!!wh.enabled} onChange={e => setChannel('webhook', { enabled: e.target.checked })} /></div><label>{t('Payload format')}</label><select value={wh.format || 'generic'} onChange={e => setChannel('webhook', { format: e.target.value })}><option value="generic">{t('Standard event fields')}</option><option value="custom">{t('Custom template')}</option></select><label>{t('Webhook URL')}</label><input value={wh.url || ''} onChange={e => setChannel('webhook', { url: e.target.value })} /><div className="u-form-grid"><div><label>{t('Method')}</label><select value={wh.method || 'POST'} onChange={e => setChannel('webhook', { method: e.target.value })}><option>POST</option><option>GET</option></select></div><div><label>{t('Body format')}</label><select value={wh.body_mode || 'json'} onChange={e => setChannel('webhook', { body_mode: e.target.value })}><option value="json">JSON</option><option value="form">Form</option><option value="raw">Raw</option></select></div></div>{wh.format === 'custom' && <><label>{t('Payload template')}</label><textarea rows="5" value={wh.payload_template || ''} onChange={e => setChannel('webhook', { payload_template: e.target.value })} placeholder={'{"title":"{{title}}","text":"{{text}}"}'} /></>}<label>{t('Custom headers (JSON)')}</label><textarea rows="3" value={wh.headers_json || '{}'} onChange={e => setChannel('webhook', { headers_json: e.target.value })} /><label><input type="checkbox" className="u-toggle" checked={wh.verify_tls !== false} onChange={e => setChannel('webhook', { verify_tls: e.target.checked })} />{t('Verify remote TLS certificate')}</label>{eventOptions('webhook', wh)}<button className="btn btn-ghost" onClick={async () => { try { await api.testWebhook(wh); showToast(t('Test succeeded')) } catch (e) { showToast(e.message) } }}>{t('Test')}</button></div>
      <div className="card u-panel"><div className="u-card-head"><div><h2>Telegram</h2><p>{t('Direct, manual proxy, or an existing country exit.')}</p></div><input type="checkbox" className="u-toggle" checked={!!tg.enabled} onChange={e => setChannel('telegram', { enabled: e.target.checked })} /></div><label>{t('Bot token')}</label><input type="password" value={tg.bot_token || ''} onChange={e => setChannel('telegram', { bot_token: e.target.value })} /><label>{t('Chat / Channel ID')}</label><input value={tg.chat_id || ''} onChange={e => setChannel('telegram', { chat_id: e.target.value })} /><label>{t('Connection')}</label><select value={tg.proxy_mode || 'direct'} onChange={e => setChannel('telegram', { proxy_mode: e.target.value })}><option value="direct">{t('Direct')}</option><option value="manual">{t('Manual HTTP/SOCKS proxy')}</option><option value="country">{t('Use country exit')}</option></select>{tg.proxy_mode === 'manual' && <><label>{t('Proxy URL')}</label><input value={tg.proxy_url || ''} onChange={e => setChannel('telegram', { proxy_url: e.target.value })} /></>}{tg.proxy_mode === 'country' && <><label>{t('Country exit')}</label><select value={tg.proxy_country || ''} onChange={e => setChannel('telegram', { proxy_country: e.target.value })}><option value="">{t('Select a country/region…')}</option>{Object.keys(s.proxy?.exits || {}).map(country => <option key={country} value={country}>{country.toUpperCase()}</option>)}</select></>}{eventOptions('telegram', tg)}<button className="btn btn-ghost" onClick={async () => { try { await api.testTelegram(tg); showToast(t('Test succeeded')) } catch (e) { showToast(e.message) } }}>{t('Test')}</button>
      </div>
      <div className="card u-panel"><div className="u-card-head"><div><h2>PushPlus</h2><p>{t('Push through the official PushPlus service.')}</p></div><input type="checkbox" className="u-toggle" checked={!!pp.enabled} onChange={e => setChannel('pushplus', { enabled: e.target.checked })} /></div><label>{t('PushPlus token')}</label><input type="password" value={pp.token || ''} onChange={e => setChannel('pushplus', { token: e.target.value })} /><label>{t('Topic code (optional)')}</label><input value={pp.topic || ''} onChange={e => setChannel('pushplus', { topic: e.target.value })} /><div className="u-form-grid"><div><label>{t('Message template')}</label><select value={pp.template || 'html'} onChange={e => setChannel('pushplus', { template: e.target.value })}><option value="html">HTML</option><option value="txt">{t('Plain text')}</option><option value="markdown">Markdown</option><option value="json">JSON</option></select></div><div><label>{t('PushPlus channel')}</label><select value={pp.channel || 'wechat'} onChange={e => setChannel('pushplus', { channel: e.target.value })}><option value="wechat">{t('WeChat')}</option><option value="app">App</option><option value="mail">{t('Email')}</option><option value="webhook">Webhook</option><option value="cp">{t('WeCom')}</option><option value="clawbot">ClawBot</option></select></div></div>{eventOptions('pushplus', pp)}<button className="btn btn-ghost" onClick={async () => { try { await api.testPushPlus(pp); showToast(t('Test succeeded')) } catch (e) { showToast(e.message) } }}>{t('Test')}</button></div></div>}
    {tab === 'delivery' && <div className="card u-panel"><div className="u-card-head"><div><h2>{t('Delivery log')}</h2><p>{t('Failed deliveries retry automatically up to three times.')}</p></div><div className="u-inline"><button className="btn btn-ghost" onClick={loadDeliveries}>{t('Refresh')}</button><button className="btn btn-ghost" onClick={async () => { await api.clearNotificationDeliveries(); loadDeliveries() }}>{t('Clear')}</button></div></div>{deliveries.pending.map(row => <div className="u-detail" key={row.id}><span>{row.channel} · {row.event}</span><b>{t('Retrying')} ({row.attempts}/3)</b></div>)}{deliveries.history.map(row => <div className="u-detail" key={row.id}><span>{new Date(row.finished_at * 1000).toLocaleString()} · {row.channel} · {row.event}</span><b>{row.status} · {row.attempts}</b></div>)}{!deliveries.pending.length && !deliveries.history.length && <p className="u-muted">{t('No delivery records')}</p>}</div>}
    {tab !== 'delivery' && <button className="btn btn-primary" onClick={save}>{t('Save')}</button>}
  </div>
}

export function SystemPage({ showToast, openUpdateDialog, instances, callCoordinator, setSelected, setView }) {
  const { t, language, setLanguage } = useI18n(); const [s, setS] = useState(null); const [tab, setTab] = useState('general'); const [status, setStatus] = useState(null); const [update,setUpdate]=useState(null); const [checking,setChecking]=useState(false); const [passwordForm,setPasswordForm]=useState({current:'',next:'',confirm:''})
  const [tokenInput, setTokenInput] = useState('')
  const [showToken, setShowToken] = useState(false)
  const [savingToken, setSavingToken] = useState(false)
  const loadStatus = () => api.systemStatus().then(st => {
    setStatus(st)
    if (st?.security?.agent_token && !tokenInput) {
      setTokenInput(st.security.agent_token)
    }
  }).catch(() => setStatus(null))
  useEffect(() => { api.settings().then(setS).catch(() => setS({ tls: {}, retry: {}, rekey: {}, security: {}, device_defaults: {}, updates: { proxy_mode: 'auto' }, proxy: { profiles: {}, exits: {} } })); loadStatus() }, [])
  if (!s) return <p>{t('Loading')}…</p>
  const tabs = [['general', t('General')], ['web', t('Web access')], ['voice', t('Calls & VoWiFi')], ['verification', language === 'zh' ? '验证与排障' : 'Verification'], ['security', t('Security')], ['backup', t('Backup & updates')], ['maintenance', t('Maintenance')]]
  const save = async () => { try { const saved = await api.saveSettings(s); setS(saved); showToast(t('Saved')) } catch (e) { showToast(t(e.message)) } }
  const action = async name => { try { const result = name === 'backup' ? await api.createBackup() : await api.maintenance(name); showToast(result.ok ? t('Operation completed') : t('Operation completed with errors')); loadStatus() } catch (e) { showToast(e.message) } }
  const checkUpdate=async()=>{setChecking(true);try{const result=await api.checkUpdate(true);setUpdate(result);showToast(result.update_available?t('Update available'):(result.ok?t('Already up to date'):t(result.error_code||result.error)))}catch(e){showToast(e.message)}finally{setChecking(false)}}
  const changePassword=async()=>{if(passwordForm.next!==passwordForm.confirm){showToast(t('Passwords do not match'));return}try{await api.authPassword(passwordForm.current,passwordForm.next);window.location.reload()}catch(e){showToast(e.message)}}
  return <div className="u-page"><div className="u-tabs">{tabs.map(([k, l]) => <button key={k} className={tab === k ? 'active' : ''} onClick={() => setTab(k)}>{l}</button>)}</div><div className="card u-panel">
    {tab === 'general' && <><h2>{t('General')}</h2><div className="u-form-grid"><div><label>{t('Language')}</label><select value={language} onChange={e => setLanguage(e.target.value)}><option value="zh">中文</option><option value="en">English</option></select></div><div><label>{t('Timezone')}</label><input list="timezones" value={s.timezone || ''} onChange={e => setS({ ...s, timezone: e.target.value })} /><datalist id="timezones"><option>Asia/Shanghai</option><option>Europe/London</option><option>America/New_York</option><option>America/Los_Angeles</option><option>Asia/Tokyo</option><option>UTC</option></datalist></div></div><h3>{t('New device defaults')}</h3><label><input type="checkbox" className="u-toggle" checked={!!s.device_defaults?.cellular_enabled} onChange={e => setS({ ...s, device_defaults: { ...s.device_defaults, cellular_enabled: e.target.checked } })} />{t('Enable 4G for newly detected modems')}</label><label><input type="checkbox" className="u-toggle" checked={s.device_defaults?.vowifi_enabled !== false} onChange={e => setS({ ...s, device_defaults: { ...s.device_defaults, vowifi_enabled: e.target.checked } })} />{t('Enable VoWiFi for newly detected modems')}</label><h3>{t('Hardware')}</h3><label><input type="checkbox" className="u-toggle" checked={s.hardware?.modem_backend === 'serial'} onChange={e => {
      const serial = e.target.checked
      if (!window.confirm(serial ? t('serialModeEnableConfirm') : t('serialModeDisableConfirm'))) return
      setS({ ...s, hardware: { ...s.hardware, modem_backend: serial ? 'serial' : 'auto' } })
    }} />{t('VoWiFi-only mode (do not run ModemManager)')}</label><p className="u-hint">{t('serialModeHint')}</p></>}
    {tab === 'web' && <><h2>{t('Web access')}</h2><label><input type="checkbox" className="u-toggle" checked={!!s.tls?.self_signed} onChange={e => setS({ ...s, tls: { ...s.tls, self_signed: e.target.checked } })} />{t('Use self-signed certificate')}</label><div className="u-form-grid"><div><label>{t('Bind address')}</label><input value={s.bind || ''} onChange={e => setS({ ...s, bind: e.target.value })} /></div><div><label>{t('HTTPS port')}</label><input type="number" value={s.http_port || 8443} onChange={e => setS({ ...s, http_port: +e.target.value })} /></div><div><label>{t('Domain')}</label><input value={s.tls?.domain || ''} onChange={e => setS({ ...s, tls: { ...s.tls, domain: e.target.value } })} /></div><div><label>{t('Certificate path')}</label><input value={s.tls?.cert_path || ''} onChange={e => setS({ ...s, tls: { ...s.tls, cert_path: e.target.value } })} /></div><div><label>{t('Private key path')}</label><input value={s.tls?.key_path || ''} onChange={e => setS({ ...s, tls: { ...s.tls, key_path: e.target.value } })} /></div></div></>}
    {tab === 'voice' && <><h2>{t('Calls & VoWiFi')}</h2><div className="u-form-grid"><div><label>{t('Ring timeout (seconds)')}</label><input type="number" value={s.ring_timeout ?? 35} onChange={e => setS({ ...s, ring_timeout: +e.target.value })} /></div><div><label>{t('Max retries')}</label><input type="number" value={s.retry?.max ?? 3} onChange={e => setS({ ...s, retry: { ...s.retry, max: +e.target.value } })} /></div><div><label>{t('Seconds per attempt')}</label><input type="number" value={s.retry?.interval ?? 30} onChange={e => setS({ ...s, retry: { ...s.retry, interval: +e.target.value } })} /></div><div><label>{t('Rekey minutes')}</label><input type="number" value={s.rekey?.minutes ?? 30} onChange={e => setS({ ...s, rekey: { ...s.rekey, minutes: +e.target.value } })} /></div>
      <div><label htmlFor="cellular-audio-buffer-ms">{t('Call audio buffer limit (ms)')}</label><input id="cellular-audio-buffer-ms" type="number" min="100" max="2000" step="1" value={s.cellular_audio_buffer_ms ?? 500} onChange={e => setS({ ...s, cellular_audio_buffer_ms: +e.target.value })} /><p className="u-hint">{t('Call audio buffer hint')}</p></div>
    </div></>}
    {tab === 'verification' && <LineVerificationPanel instances={instances} callCoordinator={callCoordinator} setSelected={setSelected} setView={setView} showToast={showToast} />}
    {tab === 'security' && <>
      <h2>{t('Security')}</h2>
      <div className="u-detail"><span>{t('HTTPS')}</span><b>{status?.security?.https ? t('Enabled') : t('Disabled')}</b></div>
      <div className="u-detail"><span>{t('Certificate mode')}</span><b>{status?.security?.certificate_mode ? t(status.security.certificate_mode) : '—'}</b></div>
      {status?.security?.cert_fingerprint && <div className="u-detail"><span>{t('TLS 证书 SHA-256 指纹')}</span><div className="u-inline"><code style={{fontSize:'12px',wordBreak:'break-all'}}>{status.security.cert_fingerprint}</code><button className="btn btn-ghost" style={{padding:'2px 8px',fontSize:'12px'}} onClick={() => { navigator.clipboard.writeText(status.security.cert_fingerprint); showToast(t('Copied')) }}>{t('Copy')}</button></div></div>}

      <h3>{t('SIM 卡转发 Agent Token')}</h3>
      <p className="u-hint">{t('此 Token 为共享认证密钥，多个 Android 客户端、Go 客户端可配置相同 Token 同时连接网关。')}</p>
      <div className="u-form-grid">
        <div style={{gridColumn: '1 / -1'}}>
          <label>{t('Agent Token')}</label>
          <div style={{display:'flex', gap:'8px', alignItems:'center', flexWrap:'wrap'}}>
            <input
              type={showToken ? "text" : "password"}
              value={tokenInput || ''}
              placeholder={t('请输入 6-256 位 Token')}
              onChange={e => setTokenInput(e.target.value)}
              style={{flex: '1 1 240px'}}
            />
            <button className="btn btn-ghost" onClick={() => setShowToken(!showToken)}>{showToken ? t('Hide') : t('Show')}</button>
            <button className="btn btn-ghost" onClick={() => { if (tokenInput) { navigator.clipboard.writeText(tokenInput); showToast(t('Copied')) } }}>{t('Copy')}</button>
            <button className="btn btn-ghost" onClick={async () => {
              try {
                const res = await api.generateAgentToken();
                if (res.agent_token) {
                  setTokenInput(res.agent_token);
                  setShowToken(true);
                  showToast(t('已生成随机 Token (请点击保存生效)'));
                }
              } catch (e) { showToast(e.message) }
            }}>{t('Random')}</button>
            <button className="btn btn-primary" disabled={savingToken || !tokenInput || tokenInput.length < 6} onClick={async () => {
              setSavingToken(true);
              try {
                const res = await api.setAgentToken(tokenInput);
                showToast(t('Agent Token 已更新保存'));
                loadStatus();
              } catch (e) { showToast(e.message) } finally { setSavingToken(false) }
            }}>{savingToken ? t('Saving…') : t('Save Token')}</button>
          </div>
        </div>
      </div>

      <h3>{t('Change administrator password')}</h3><div className="u-form-grid"><div><label>{t('Current password')}</label><input type="password" autoComplete="current-password" value={passwordForm.current} onChange={e=>setPasswordForm({...passwordForm,current:e.target.value})}/></div><div><label>{t('New password (at least 10 characters)')}</label><input type="password" autoComplete="new-password" minLength="10" value={passwordForm.next} onChange={e=>setPasswordForm({...passwordForm,next:e.target.value})}/></div><div><label>{t('Confirm password')}</label><input type="password" autoComplete="new-password" minLength="10" value={passwordForm.confirm} onChange={e=>setPasswordForm({...passwordForm,confirm:e.target.value})}/></div></div><button className="btn btn-ghost" disabled={!passwordForm.current||passwordForm.next.length<10||!passwordForm.confirm} onClick={changePassword}>{t('Change password')}</button><label><input type="checkbox" className="u-toggle" checked={s.security?.audit_enabled !== false} onChange={e => setS({ ...s, security: { ...s.security, audit_enabled: e.target.checked } })} />{t('Record administrative operations')}</label><label>{t('Trusted reverse proxies (comma-separated)')}</label><input value={(s.security?.trusted_proxies || []).join(', ')} onChange={e => setS({ ...s, security: { ...s.security, trusted_proxies: e.target.value.split(',').map(x => x.trim()).filter(Boolean) } })} /></>}

    {tab === 'backup' && <>
      <div className="u-card-head"><div><h2>{t('Backup & updates')}</h2><p>{t('Backups stay encrypted by host permissions and are not downloaded through the browser.')}</p></div><button className="btn btn-primary" onClick={() => action('backup')}>{t('Create local backup')}</button></div>
      <div className="u-detail"><span>{t('Running version')}</span><b>{status?.version || '—'}</b></div>
      <h3>{t('Update connection')}</h3>
      <p className="u-note">{t('Automatic tries a direct connection first, then the available proxy library entries. The route that passes the check is reused for the download.')}</p>
      <div className="u-form-grid">
        <div><label>{t('Connection')}</label><select value={s.updates?.proxy_mode || 'auto'} onChange={e => setS({ ...s, updates: { proxy_mode: e.target.value, proxy_profile_id: e.target.value === 'library' ? (s.updates?.proxy_profile_id || '') : '' } })}><option value="auto">{t('Automatic — direct, then proxy library')}</option><option value="direct">{t('Direct only')}</option><option value="library">{t('Specified proxy')}</option></select></div>
        {s.updates?.proxy_mode === 'library' && <div><label>{t('Proxy')}</label><select value={s.updates?.proxy_profile_id || ''} onChange={e => setS({ ...s, updates: { proxy_mode: 'library', proxy_profile_id: e.target.value } })}><option value="">{t('Select a proxy…')}</option>{Object.entries(s.proxy?.profiles || {}).map(([id, profile]) => <option key={id} value={id}>{profile.name || t('Unnamed proxy')}</option>)}</select></div>}
      </div>
      {s.updates?.proxy_mode === 'library' && <p className="u-note">{t('SOCKS5 entries connect directly. Subscription and node entries reuse a ready country exit assigned to that proxy.')}</p>}
      <button className="btn btn-ghost" onClick={save}>{t('Save update connection')}</button>
      <div className="u-detail"><span>{t('Software updates')}</span><div className="u-inline"><b>{update?.latest ? `v${update.latest}` : t(update ? 'Update check failed' : 'Not checked')}</b><button className="btn btn-ghost" disabled={checking} onClick={checkUpdate}>{t(checking?'Checking…':'Check for updates')}</button></div></div>
      {update?.update_available&&<div className="u-note u-update-note"><span>{t('A new version is available. Review the release notes before updating.')}</span><button className="btn btn-primary" onClick={()=>openUpdateDialog(update)}>{t('Review update')}</button></div>}
      {update&&!update.ok&&<p className="u-error">{t(update.error_code||update.error)}</p>}
      {(status?.backups || []).map(item => <div className="u-detail" key={item.name}><span>{item.name}</span><b>{formatBytes(item.size)} · {new Date(item.created_at * 1000).toLocaleString()}</b></div>)}
    </>}
    {tab === 'maintenance' && <><h2>{t('Maintenance')}</h2><div className="u-action-grid"><button className="btn btn-ghost" onClick={() => action('restart_lines')}>{t('Restart all VoWiFi lines')}</button><button className="btn btn-ghost" onClick={() => action('refresh_egress')}>{t('Refresh country exits')}</button><button className="btn btn-ghost" onClick={() => action('clear_notification_history')}>{t('Clear notification history')}</button></div></>}
  </div>{!['backup', 'maintenance', 'verification'].includes(tab) && <button className="btn btn-primary" onClick={save}>{t('Save')}</button>}</div>
}

const HOST_ALERT_TEXT = {
  undervoltage_now: 'Power is browning out right now. The network port, cellular module and card reader share this supply rail, so every line drops at the same instant.',
  undervoltage_seen: 'Under-voltage has been detected on this host. Every line drops at the moment it happens.',
  throttled_now: 'The CPU is being throttled or frequency-capped.',
  temperature_high: 'Host temperature is high enough to throttle.',
  disk_critical: 'The disk is nearly full; history and runtime state may fail to write.',
  disk_low: 'Disk space is running low.',
  swap_pressure: 'Swap is being paged actively; on an SD card this slows every operation and times out status reads.',
  default_route_changed: 'The default route moved between uplinks, changing the source address every outbound connection uses.',
}

function formatDuration(seconds, t) {
  const days = Math.floor(seconds / 86400), hours = Math.floor((seconds % 86400) / 3600)
  return days ? t('{days}d {hours}h', { days, hours }) : t('{hours}h {minutes}m', { hours, minutes: Math.floor((seconds % 3600) / 60) })
}

function Row({ label, children }) {
  return <div className="u-detail"><span>{label}</span><b>{children}</b></div>
}

// The host is the layer that takes every line down at once and the one nothing else reports:
// the NIC shows no errors, the link stays up, and a brown-out only surfaces minutes later as
// unexplained packet loss. This is where that evidence lives.
function HostPanel({ host, alerts, loading, clearing, onClear, t }) {
  if (loading) return <Empty title={t('Reading host information…')} detail={t('Collecting power, storage, memory and network status.')} />
  if (!host?.model && !host?.memory) return <Empty title={t('Host information unavailable')} detail={t('The control plane has not sampled the host yet.')} />
  const mem = host.memory || {}, disk = host.disk || {}, load = host.load || {}, net = host.network || {}
  const throttle = host.throttling || {}
  const sticky = throttle.since_boot || [], now = throttle.now || []
  return <div className="u-device-grid">
    {!!alerts.length && <div className="card u-panel" style={{ gridColumn: '1 / -1' }}>
      <div className="u-card-head"><h3>{t('Needs attention')}</h3><button className="btn btn-ghost" disabled={clearing} onClick={onClear}>{t(clearing ? 'Clearing…' : 'Clear')}</button></div>
      {alerts.map(a => <p key={a.code} className={a.severity === 'critical' ? 'u-error' : 'u-note'} style={{ marginTop: 8 }}>
        {t(HOST_ALERT_TEXT[a.code] || a.code)}
        {a.detail?.events ? ' ' + t('({count} events, last {last})', { count: a.detail.events, last: a.detail.last || '—' }) : ''}
      </p>)}
    </div>}

    <div className="card u-panel">
      <h3>{t('Machine')}</h3>
      <Row label={t('Model')}>{host.model || '—'}</Row>
      <Row label={t('Uptime')}>{host.uptime_seconds ? formatDuration(host.uptime_seconds, t) : '—'}</Row>
      <Row label={t('Temperature')}>{host.temperature_c != null ? `${host.temperature_c} °C` : '—'}</Row>
      <Row label={t('CPU frequency')}>{host.cpu_mhz ? `${host.cpu_mhz} MHz` : '—'}</Row>
      <Row label={t('Load (1m / per core)')}>{load['1m'] != null ? `${load['1m']} / ${load.per_core} (${load.cores} ${t('cores')})` : '—'}</Row>
    </div>

    <div className="card u-panel">
      <h3>{t('Memory and storage')}</h3>
      <Row label={t('Memory')}>{mem.total_mb ? t('{used}% of {total} MB used', { used: mem.used_percent, total: mem.total_mb }) : '—'}</Row>
      <Row label={t('Swap')}>{mem.swap_total_mb ? t('{used} MB of {total} MB ({percent}%)', { used: mem.swap_used_mb, total: mem.swap_total_mb, percent: mem.swap_used_percent }) : '—'}</Row>
      <Row label={t('Disk')}>{disk.total_mb ? t('{used}% used · {free} MB free', { used: disk.used_percent, free: disk.free_mb }) : '—'}</Row>
    </div>

    <div className="card u-panel">
      <h3>{t('Network')}</h3>
      {(net.addresses || []).map(a => <Row key={`${a.interface}-${a.address}`} label={a.interface}>{a.address}</Row>)}
      {!net.addresses?.length && <Row label={t('Addresses')}>—</Row>}
      <Row label={t('Default route')}>{(net.default_interfaces || []).join(', ') || '—'}</Row>
      {net.usb_attached && <Row label={t('Uplink attachment')}>{t('USB — shares its bus and power with the modem and reader')}</Row>}
      {!!net.counters && <Row label={t('Interface errors / dropped')}>{`${(net.counters.rx_errors ?? 0) + (net.counters.tx_errors ?? 0)} / ${(net.counters.rx_dropped ?? 0) + (net.counters.tx_dropped ?? 0)}`}</Row>}
    </div>

    {(!!throttle.raw || !!host.undervoltage) && <div className="card u-panel">
      <h3>{t('Power and throttling')}</h3>
      <Row label={t('Right now')}>{now.length ? now.map(x => t(`throttle.${x}`)).join(', ') : t('Normal')}</Row>
      <Row label={t('Since boot')}>{sticky.length ? sticky.map(x => t(`throttle.${x}`)).join(', ') : t('Nothing recorded')}</Row>
      {!!host.undervoltage?.count && <Row label={t('Under-voltage events')}>{t('{count} · last {last}', { count: host.undervoltage.count, last: host.undervoltage.last || '—' })}</Row>}
      <p className="u-note">{t('Under-voltage is invisible everywhere else: the link stays up and the interface reports no errors, so it surfaces minutes later as packet loss on every line at once.')}</p>
    </div>}

    {!!host.usb_devices?.length && <div className="card u-panel">
      <h3>{t('USB devices')}</h3>
      {host.usb_devices.map((d, i) => <Row key={`${d}-${i}`} label={`#${i + 1}`}>{d}</Row>)}
    </div>}
  </div>
}

function AgentHostsPanel({ agents, loading, now, language }) {
  const isZh = language === 'zh'
  if (loading) return <div className="card u-panel"><p>{isZh ? '正在读取 Agent 状态…' : 'Loading Agent health…'}</p></div>
  if (!agents.length) return <Empty title={isZh ? '尚无 Agent 健康上报' : 'No Agent health reports'} detail={isZh ? '新版 Windows、macOS Agent 接入后会显示在这里；旧版 Agent 的设备功能不受影响。' : 'Updated Windows and macOS Agents appear here. Older Agent device functions are unaffected.'} />
  return <>
    <div className="u-section-title"><div><h2>{isZh ? 'Agent 主机' : 'Agent hosts'}</h2><p>{isZh ? '这是管理程序自身的健康状态；不代表 SIM、4G、短信或通话一定可用。' : 'This is the management process health, not proof that SIM, cellular, SMS or calling is available.'}</p></div></div>
    <div className="u-device-grid">{agents.map(agent => {
      const view = agentHealthPresentation(agent, language)
      const meta = agent.meta || {}; const snapshot = agent.snapshot || {}
      const manager = snapshot.manager || {}; const runtime = snapshot.runtime || {}
      const inventory = snapshot.inventory || {}; const attachments = agent.attachments || {}
      const storage = snapshot.resources?.storage || {}
      const platform = { windows: 'Windows', macos: 'macOS', linux: 'Linux' }[meta.platform] || (isZh ? '旧版' : 'Legacy')
      return <div className="card u-panel" key={agent.id}>
        <div className="u-section-title"><div><h3>{platform} Agent · {agent.display_id}</h3><p>{meta.arch || '—'}{meta.agent_version ? ` · v${meta.agent_version}` : ''}</p></div><Badge state={view.state}>{view.label}</Badge></div>
        <div className="u-detail"><span>{isZh ? '运行状态' : 'Runtime'}</span><b>{agentHealthEnumLabel('runtime', runtime.state || (isZh ? '未上报' : 'not reported'), language)}</b></div>
        <div className="u-detail"><span>{isZh ? '宿主方式' : 'Host mode'}</span><b>{agentHealthEnumLabel('manager', manager.kind || meta.manager, language)}</b></div>
        <div className="u-detail"><span>{isZh ? '最后心跳' : 'Last heartbeat'}</span><b>{agentHeartbeatAge(agent.seen_at, now, language)}</b></div>
        <div className="u-detail"><span>{isZh ? '当前硬件连接' : 'Current attachments'}</span><b>{isZh ? `${attachments.modems_online ?? inventory.modems_connected ?? 0} 个模块 · ${attachments.readers_online ?? 0} 个读卡器` : `${attachments.modems_online ?? inventory.modems_connected ?? 0} modem(s) · ${attachments.readers_online ?? 0} reader(s)`}</b></div>
        {!!snapshot.isolation?.state && <div className="u-detail"><span>{isZh ? '宿主流量隔离' : 'Host traffic isolation'}</span><b>{agentHealthEnumLabel('isolation', snapshot.isolation.state, language)}{snapshot.isolation.backend ? ` · ${snapshot.isolation.backend}` : ''}</b></div>}
        {!!storage.state && <div className="u-detail"><span>{isZh ? 'Agent 数据磁盘' : 'Agent data storage'}</span><b>{agentHealthEnumLabel('storage', storage.state, language)}{Number.isFinite(storage.used_percent) ? ` · ${storage.used_percent}%` : ''}</b></div>}
        {!!runtime.last_error_code && <p className="u-error">{runtime.last_error_code}</p>}
        {!!snapshot.isolation?.reason_code && <p className="u-error">{snapshot.isolation.reason_code}</p>}
      </div>
    })}</div>
  </>
}

export function DiagnosticsPage(props) {
  const { t, language } = useI18n(); const [tab, setTab] = useState('health'); const [results, setResults] = useState({}); const { devices } = props
  const [system, setSystem] = useState(null)
  const [hostLoading, setHostLoading] = useState(true)
  const [clearingAlerts, setClearingAlerts] = useState(false)
  const [agents, setAgents] = useState([])
  const [agentsLoading, setAgentsLoading] = useState(true)
  const [agentNow, setAgentNow] = useState(Date.now())
  // The host is where an outage that hits every line at once comes from, so this refreshes
  // on its own rather than showing whatever was true when the page was opened.
  useEffect(() => {
    const load = () => api.systemStatus().then(value => { setSystem(value); props.setSystemMeta?.(value) }).catch(() => {}).finally(() => setHostLoading(false))
    load(); const timer = setInterval(load, 30 * 1000); return () => clearInterval(timer)
  }, [])
  useEffect(() => {
    let active = true
    const load = () => api.agentHealth().then(value => { if (active) setAgents(value.agents || []) }).catch(() => {}).finally(() => { if (active) setAgentsLoading(false) })
    load()
    // Relative age is local presentation state. Do not replace the Agent array every heartbeat:
    // only semantic server events do that. The slow fallback repairs a missed browser event.
    const clock = setInterval(() => setAgentNow(Date.now()), 10 * 1000)
    const fallback = setInterval(load, 60 * 1000)
    const unsubscribe = props.subscribe?.(message => { if (message.type === 'agent-health') load() })
    return () => { active = false; clearInterval(clock); clearInterval(fallback); unsubscribe?.() }
  }, [props.subscribe])
  const host = system?.host || {}
  const hostAlerts = system?.host_alerts || []
  const issueUrl = `${(system?.repository_url || 'https://github.com/MddIdd/mdd-sim-gateway').replace(/\/$/, '')}/issues/new/choose`
  const clearHostAlerts = async () => { try { setClearingAlerts(true); await api.clearHostAlerts(); const next = { ...(system || {}), host_alerts: [] }; setSystem(next); props.setSystemMeta?.(s => ({ ...s, host_alerts: [] })); props.showToast(t('Host alerts cleared')) } catch (e) { props.showToast(e.message) } finally { setClearingAlerts(false) } }
  const run = async d => { try { const result = await api.deviceDiagnostics(d.id); setResults(x => ({ ...x, [d.id]: result })); props.showToast(result.ok ? t('Diagnostics passed') : t('Diagnostics found problems')) } catch (e) { props.showToast(e.message) } }
  return <div className="u-page"><div className="u-tabs"><button className={tab === 'health' ? 'active' : ''} onClick={() => setTab('health')}>{t('Health')}</button><button className={tab === 'agents' ? 'active' : ''} onClick={() => setTab('agents')}>{language === 'zh' ? 'Agent 主机' : 'Agent hosts'}</button><button className={tab === 'host' ? 'active' : ''} onClick={() => setTab('host')}>{language === 'zh' ? '网关主机' : 'Gateway host'}{!!hostAlerts.length && <i className={`u-nav-dot ${hostAlerts.some(a => a.severity === 'critical') ? 'critical' : 'warning'}`} />}</button><button className={tab === 'logs' ? 'active' : ''} onClick={() => setTab('logs')}>{t('Live logs')}</button><button className={tab === 'bundle' ? 'active' : ''} onClick={() => setTab('bundle')}>{t('Support bundle')}</button></div>
    {tab === 'health' && <div className="u-device-grid">{devices.map((d, i) => <div className="card u-panel" key={d.id}><h3>{deviceTitle(d, i)}</h3><div className="u-detail"><span>{t('4G network')}</span><Badge state={capability(d, 'cellular').actual} /></div><div className="u-detail"><span>VoWiFi / IMS</span><Badge state={capability(d, 'vowifi').actual} /></div><button className="btn btn-ghost" onClick={() => run(d)}>{t('Run diagnostics')}</button>{results[d.id]?.checks?.map(check => <div className="u-detail" key={check.name}><span>{check.name}</span><b>{check.ok ? '✓' : '✕'} {check.detail}</b></div>)}</div>)}</div>}
    {tab === 'agents' && <AgentHostsPanel agents={agents} loading={agentsLoading} now={agentNow} language={language} />}
    {tab === 'host' && <HostPanel host={host} alerts={hostAlerts} loading={hostLoading} clearing={clearingAlerts} onClear={clearHostAlerts} t={t} />}
    {tab === 'logs' && <Logs {...props} />}
    {tab === 'bundle' && <div className="card u-panel"><h2>{t('Redacted support bundle')}</h2><p>{t('Contains status, configuration shape and bounded logs. SIM identities, phone numbers, credentials and cryptographic material are removed.')}</p><div className="u-support-actions"><a className="btn btn-primary" href={api.supportBundleUrl}>{t('Download support bundle')}</a><div><b>{t('Found a problem or have a suggestion?')}</b><p>{t('Open a GitHub Issue. For faults, attach the redacted support bundle when appropriate.')}</p><a href={issueUrl} target="_blank" rel="noreferrer">{t('Submit an Issue')} ↗</a></div></div></div>}
  </div>
}
