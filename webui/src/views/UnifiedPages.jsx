import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../api.js'
import { useI18n } from '../i18n.jsx'
import SimConfig from './SimConfigV1.jsx'
import HardwarePanelV1 from './HardwareV1.jsx'
import AllowancePanel from './AllowancePanel.jsx'
import VowifiHistory from './VowifiHistoryV1.jsx'
import { compactReaderName, lineCallReadinessStatus } from '../linePresentation.js'


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
        : (isZh ? '短信历史已刷新' : 'SMS history refreshed'))
      setTimeout(() => refreshDevices?.(), kind === 'restart' ? 5000 : 500)
    } catch (error) { showToast?.(error.message) } finally { setBusy('') }
  }
  return <div className="u-note" style={{ marginTop: 8 }}>
    <div>{isZh ? '短信中心' : 'SMS centre'}: <b>{diagnostics.service_center || (isZh ? '未上报' : 'not reported')}</b></div>
    {advisory.map((item, index) => <p key={index} style={{ margin: '4px 0 0' }}>{item}</p>)}
    {!!refresh.recommended && <><p style={{ margin: '4px 0 0' }}>{refresh.reason}</p>
      <button className="btn btn-ghost" disabled={busy === 'refresh'} onClick={() => run('refresh')}>{busy === 'refresh' ? (isZh ? '刷新中…' : 'Refreshing…') : (isZh ? '刷新短信历史' : 'Refresh SMS history')}</button></>}
	{!!restart.available && !!restart.recommended && <button className="btn btn-ghost" disabled={busy === 'restart'} onClick={() => run('restart')} style={{ marginLeft: 8 }}>{busy === 'restart' ? (isZh ? '重启中…' : 'Restarting…') : (isZh ? '软重启模块' : 'Soft restart modem')}</button>}
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
  const unavailable = (!c.available && !c.requestable && !isImeiMissing) || c.actual === 'unsupported' || device.compatibilityOnly ||
	device.present === false || ((kind === 'cellular' || kind === 'connection') && capability(device, 'flight').desired)
	const title = kind === 'cellular' ? t('Allow cellular data borrowing') : kind === 'connection' ? t('4G data connection') : kind === 'flight' ? t('Flight mode') : kind === 'roaming' ? t('Allow data roaming') : t('VoWiFi / WiFi Calling')
  const canRetry = kind === 'vowifi' && c.desired && ['off', 'degraded', 'error'].includes(c.actual) && !unavailable
  const change = async (next, retry = false) => {
    if (kind === 'vowifi' && next && isImeiMissing) {
      showToast?.(isZh ? '该读卡器尚未设置 IMEI，请在「硬件」标签页添加或选择已保存的 IMEI' : 'Please configure an IMEI in the Hardware tab before enabling VoWiFi')
      onNavigateToHardware?.(device.id)
      return
    }
    const impact = retry
      ? t('Restart the VoWiFi line now? The SIM, ePDG and IMS connection will be rebuilt.')
	  : kind === 'cellular'
	  ? t('Change permission for MDD to borrow this SIM’s data? This does not connect a bearer by itself; an active lease must be stopped first.')
	  : kind === 'connection'
	  ? t('Change the persistent 4G data connection? This may use metered or roaming data; only MDD sockets can use the guarded bearer.')
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
      const errorCode = e.code || errorDetail.code || ''
      if (kind === 'vowifi' && (errorCode === 'sim_apdu_data_active' || errorCode === 'flight_mode_enabled')) {
        await onChanged?.()
        showToast?.(errorCode === 'flight_mode_enabled'
          ? (isZh ? 'VoWiFi 请求已保存；关闭飞行模式后会自动准备 SIM 并启动' : 'VoWiFi intent was saved; turn off flight mode to prepare the SIM and start automatically')
          : (isZh ? 'VoWiFi 请求已保存；先关闭持续 4G 数据连接，释放 SIM 后会自动启动' : 'VoWiFi intent was saved; turn off the persistent 4G data connection and it will start after SIM ownership is released'))
        return
      }
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
	const detail = kind === 'cellular'
	  ? t(c.desired
		? 'Borrowing is allowed; a guarded bearer may connect only for an explicit MDD consumer.'
		: 'Borrowing is disabled; MDD will not open a cellular bearer.')
	  : kind === 'connection'
	  ? t(c.desired
		? (c.actual === 'on' ? 'The guarded 4G bearer is connected; host applications still cannot use it.' : 'The guarded 4G bearer is requested and is converging.')
		: 'The persistent 4G bearer is off; explicit borrowing may still connect when allowed.')
    : c.actual === 'on'
      ? t(kind === 'flight' ? 'Modem RF is disabled.' : kind === 'roaming' ? 'Mobile data may connect while roaming.' : 'Working — connected to the carrier over Wi-Fi.')
      : (c.reason ? t(c.reason) : t(`cap.help.${c.actual}`))
  return <div className={`u-capability ${compact ? 'compact' : ''}`}>
    <div><b>{title}</b><div className="u-cap-detail">{detail}</div></div>
    <div className="u-cap-actions">{canRetry && <button className="btn btn-ghost" disabled={submitting} onClick={() => change(true, true)}>{t('Restart line')}</button>}<Badge state={displayedState}>{device.present === false ? t('Offline') : mismatch}</Badge><button className={`u-switch ${displayedDesired ? 'on' : ''}`} role="switch" aria-checked={displayedDesired}
      aria-label={title} disabled={pending || (unavailable && !c.desired)} onClick={toggle}><span /></button></div>
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
    Promise.all([api.deviceCellularProfiles(device.id), api.catalogLines()])
      .then(([result, catalog]) => {
        const line = (catalog.lines || []).find(item => String(item.id) === String(device.instance_id))
        setCatalogLine(line || null); setCatalogRevision(Number(catalog.revision || 0))
        const suggestions = result.suggested_profiles || (result.suggested_apns || []).map((apn, index) => ({ id: `apn-${index}`, name: apn, apn, auth: 'NONE', username: '' }))
        const durable = (line?.network?.apn_profiles || []).map(item => ({ ...item, password_configured: item.password_set === true, source: 'mdd' }))
        setProfiles(durable); setSuggestedProfiles(suggestions)
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
      if (!catalogLine || !catalogRevision) throw new Error(t('No durable MDD line is associated with this modem.'))
      const profile = { id: draft.name.trim().toLowerCase().replace(/[^a-z0-9_.:-]+/g, '-').slice(0, 100), name: draft.name.trim(), apn: draft.apn.trim(), auth: draft.auth, username: draft.username, password: draft.password, password_set: !!draft.password }
      const nextLine = { ...catalogLine, network: { ...catalogLine.network, apn_profiles: [...(catalogLine.network?.apn_profiles || []).filter(item => item.id !== profile.id), profile], active_apn: profile.id } }
      const result = await api.saveCatalogLine(nextLine, catalogRevision)
      setCatalogLine(result.line); setCatalogRevision(Number(result.revision || 0)); setProfiles((result.line.network?.apn_profiles || []).map(item => ({ ...item, password_configured: item.password_set === true, source: 'mdd' })))
      setDraft(value => ({ ...value, name: profile.name, apn: profile.apn, password: '' }))
      showToast(t('MDD APN profile saved as durable desired state. Apply it explicitly before starting data.'))
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
  const applyMDDProfile = async () => {
    const active = profiles.find(item => item.id === catalogLine?.network?.active_apn)
    if (!active) { setError(t('Select and save one active MDD APN profile first.')); return }
    setSaving(true); setError('')
    try {
      await api.saveDeviceCellularProfile(device.id, { name: active.name, apn: active.apn, auth: active.auth, username: active.username || '', password: active.password || '' })
      await api.patchDeviceCapabilities(device.id, { selected_profile: active.name })
      showToast(t('Active MDD APN profile applied to the modem projection.'))
      load(); await refreshDevices?.()
    } catch (e) { setError(e.message) } finally { setSaving(false) }
  }
  return <div className="u-profile-editor">
    <h3>{t('Mobile broadband profile')}</h3>
    <p className="u-note">{t(supported
      ? 'MDD stores this profile as the durable desired source. The modem/Agent profile is only an observed or applied projection; apply it explicitly before starting data.'
      : 'Mobile-broadband profiles are managed by macOS and are read-only in MDD.')}</p>
    {loading ? <p>{t('Loading…')}</p> : profiles.length ? <><div className="u-detail"><span>{t('MDD durable profiles')}</span><b>{profiles.map(item => `${item.name}${item.id === catalogLine?.network?.active_apn ? ' · active' : ''}`).join(', ')}</b></div><div className="u-detail"><span>{t('Applied / actual')}</span><b>{device.cellular?.profile || t('Not reported')} · {device.cellular?.data_state || t('Bearer inactive')}</b></div><button className="btn btn-ghost" disabled={saving || !catalogLine?.network?.active_apn} onClick={applyMDDProfile}>{t(saving ? 'Applying…' : 'Apply active MDD APN to modem')}</button></> : <p className="u-note">{t('No MDD APN profile is configured. Modem-reported candidates below are suggestions only until explicitly saved.')}</p>}
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
      <div className="u-inline"><span className="u-muted">{t('Save the profile, then allow data borrowing and roaming as needed. A bearer starts only when an exit or explicit session borrows it.')}</span><button className="btn btn-primary" disabled={saving || !draft.name.trim() || !draft.apn.trim()}>{t(saving ? 'Saving…' : 'Save profile')}</button></div>
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
	cellular: devices.filter(d => ['connected', 'ready', 'up'].includes(String(d.cellular?.data_state || '').toLowerCase())).length,
    vowifi: devices.filter(d => capability(d, 'vowifi').actual === 'on').length,
	attention: devices.filter(d => ['error', 'degraded'].includes(capability(d, 'cellular').actual) || ['error', 'degraded'].includes(capability(d, 'connection').actual) || ['error', 'degraded'].includes(capability(d, 'vowifi').actual)).length,
  }), [devices])
  return <div className="u-page">
    <div className="u-metrics">
      {[[t('Devices'), counts.devices], [t('Active data bearers'), counts.cellular], [t('VoWiFi online'), counts.vowifi], [t('Needs attention'), counts.attention]].map(([l,v]) => <div className="u-metric" key={l}><span>{l}</span><strong>{pending ? '—' : v}</strong></div>)}
    </div>
    {pending ? <Discovering t={t} /> :
      !devices.length ? <Empty title={t('No communication devices found')} detail={t('Connect a modem or smart-card reader. Discovery updates automatically.')} /> :
      <div className="u-device-grid">{devices.map((d, i) => <div className="card u-device-card" key={d.id}>
        <div className="u-card-head"><div><h2>{deviceTitle(d, i)}</h2><p>{deviceIdentityLine(d, t)}</p></div><Badge state={d.present === false ? 'error' : 'on'}>{d.present === false ? t('Offline') : t('Detected')}</Badge></div>
		<div className="u-card-body">{supportsCellular(d) && <><CapabilitySwitch device={d} kind="connection" compact onChanged={refreshDevices} showToast={showToast} /><CapabilitySwitch device={d} kind="cellular" compact onChanged={refreshDevices} showToast={showToast} /><CapabilitySwitch device={d} kind="roaming" compact onChanged={refreshDevices} showToast={showToast} /></>}<CapabilitySwitch device={d} kind="vowifi" compact onChanged={refreshDevices} showToast={showToast} onNavigateToHardware={() => { setSelectedDeviceId(d.id); setView('devices') }} onNavigateToSim={() => { setSelectedDeviceId(d.id); setView('devices') }} /><LineActivity device={d} compact /><BrowserVoiceStatus device={d} instances={instances} callCoordinator={callCoordinator} compact />
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
  const tabs = [['status',t('Status')],['sim','SIM'],...(supportsCellular(d) ? [['cellular',t('Data borrowing / APN')]] : []),['vowifi','VoWiFi'],['hardware',t('Hardware')],['imeis', isZh ? 'IMEI 池' : 'IMEI Pool']]
  return <div className="u-split"><aside className="card u-device-list">{devices.map((x,i)=><button key={x.id} className={`u-device-option ${x.id===active?'active':''}`} onClick={()=>setSelectedDeviceId(x.id)}><b className="u-device-option-name">{deviceTitle(x,i)}</b><span className="u-device-option-sim">{deviceSimLine(x, t, language)}</span><span className="u-device-option-status"><Badge state={x.present === false ? 'error' : 'on'}>{x.present === false ? t('Offline') : t('Online')}</Badge></span></button>)}</aside>
    <section className="u-page"><div className="u-page-heading"><div><h2>{deviceTitle(d, devices.indexOf(d))}</h2><p>{deviceTypeName(d, t)} · {stablePathName(d, t)}</p></div></div><div className="u-tabs">{tabs.map(([k,l])=><button key={k} className={tab===k?'active':''} onClick={()=>setTab(k)}>{l}</button>)}</div>
      {tab==='status' && supportsCellular(d) && <div className="card u-panel"><CapabilitySwitch device={d} kind="connection" onChanged={refreshDevices} showToast={showToast}/></div>}
      {tab==='status' && <div className="card u-panel">{supportsCellular(d) ? <><CapabilitySwitch device={d} kind="cellular" onChanged={refreshDevices} showToast={showToast}/><CapabilitySwitch device={d} kind="roaming" onChanged={refreshDevices} showToast={showToast}/><CapabilitySwitch device={d} kind="flight" onChanged={refreshDevices} showToast={showToast}/></> : <p className="u-note">{t('This is a smart-card reader. It provides SIM access for VoWiFi and has no 4G radio.')}</p>}<CapabilitySwitch device={d} kind="vowifi" onChanged={refreshDevices} showToast={showToast} onNavigateToHardware={() => setTab('hardware')} onNavigateToSim={() => setTab('sim')} /><LineActivity device={d}/><BrowserVoiceStatus device={d} instances={instances} callCoordinator={callCoordinator}/><ImsCapabilityBadges device={d}/><SmsAdvisory device={d} refreshDevices={refreshDevices} showToast={showToast}/><FirmwareAdvice advice={d.firmware_advice}/><p className="u-note">{t('Data-borrow permission, flight mode and VoWiFi are independent. Permission does not connect a bearer; it only allows an explicit exit/session to borrow one.')}</p><p className="u-note">{t('Software support means the technical path is implemented. Actual availability still depends on the SIM plan, carrier, region, modem firmware and device-identity policy.')}</p></div>}
      {tab==='sim' && <div className="card u-panel"><SimConfig instances={instances} selected={selected} refresh={refresh} cards={cards} setSelected={setSelected} targetDevice={d} devices={devices}/></div>}
	  {tab==='cellular' && <div className="card u-panel"><h3>{t('Cellular data and borrowing')}</h3><CapabilitySwitch device={d} kind="connection" onChanged={refreshDevices} showToast={showToast}/><CapabilitySwitch device={d} kind="cellular" onChanged={refreshDevices} showToast={showToast}/><CapabilitySwitch device={d} kind="roaming" onChanged={refreshDevices} showToast={showToast}/><CapabilitySwitch device={d} kind="flight" onChanged={refreshDevices} showToast={showToast}/>{d.cellular ? <div className="u-details cols"><div className="u-detail"><span>{t('Registration')}</span><b>{d.cellular.registration || t('Not connected')}</b></div><div className="u-detail"><span>{t('Operator')}</span><b>{d.cellular.operator || t('Not connected')}</b></div><div className="u-detail"><span>{t('Actual bearer')}</span><b>{d.cellular.data_state || t('Not connected')}</b></div><div className="u-detail"><span>{t('Data profile')}</span><b>{d.cellular.profile || t('Automatic')}</b></div><div className="u-detail"><span>{t('Borrow owner')}</span><b>{d.cellular.data_lease ? `${d.cellular.data_lease.purpose} · ${d.cellular.data_lease.state}` : t('None')}</b></div><div className="u-detail"><span>{t('Host isolation')}</span><b>{d.cellular.data_guard || '—'}{d.cellular.data_guard_detail ? ` · ${d.cellular.data_guard_detail}` : ''}</b></div><div className="u-detail"><span>{t('Signal')}</span><b>{d.cellular.signal == null ? t('Waiting') : `${d.cellular.signal}%`}</b></div></div>:<Empty title={t('Cellular data unavailable')} detail={t('The current modem does not expose a protected data-borrow path.')} />}<CellularProfilePanel key={d.id} device={d} showToast={showToast} refreshDevices={refreshDevices}/></div>}
      {tab==='vowifi' && <div className="card u-panel"><h3>VoWiFi</h3><CountryExitControl device={d} refresh={refresh} showToast={showToast}/><LineActivity device={d}/><BrowserVoiceStatus device={d} instances={instances} callCoordinator={callCoordinator}/><ImsCapabilityBadges device={d}/><div className="u-details cols"><div className="u-detail"><span>ePDG / IKE</span><b>{d.facts?.facts?.tunnel?.code || (typeof d.vowifi?.epdg === 'object' ? (d.vowifi.epdg.ike_reason || (d.vowifi.epdg.pcscf ? t('Tunnel connected') : t('Waiting'))) : (d.vowifi?.epdg || d.status?.state || t('Not connected')))}</b></div><div className="u-detail"><span>IMS / SIP</span><b>{d.facts?.facts?.ims?.code || d.vowifi?.ims || d.status?.label || t('Not connected')}</b></div><div className="u-detail"><span>{t('Country exit')}</span><b className="u-proxy-node-text"><ProxyNodeName text={exitNodeLabel(d, t)} /></b></div></div><VowifiHistory instanceId={d.instance_id}/><p className="u-note">{t('Software support means the technical path is implemented. Actual availability still depends on the SIM plan, carrier, region, modem firmware and device-identity policy.')}</p></div>}
      {tab==='hardware' && <HardwarePanelV1 device={d} refreshDevices={refreshDevices} showToast={showToast}/>}
      {tab==='imeis' && <ImeiPoolPanel devices={devices} instances={instances} refreshDevices={refreshDevices} showToast={showToast}/>}
    </section>
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
      showToast(t(country ? 'Country exit saved as {country}; apply the catalog explicitly to activate it' : 'Country exit returned to automatic detection; apply the catalog explicitly to activate it', {
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
	const [savedProxy, setSavedProxy] = useState(null)
  const [live, setLive] = useState(null)
  const [newCountry, setNewCountry] = useState('')
  const [profileDraft, setProfileDraft] = useState(null)
  const [revealSensitive, setRevealSensitive] = useState(false)
  const [saving, setSaving] = useState(false)
	const [applying, setApplying] = useState(false)
	const [profileTests, setProfileTests] = useState({})
	const profileStateRef = useRef({ revision: 0, profiles: {}, dirty: false, tests: {} })
  const [remoteModems, setRemoteModems] = useState([])
  const loadLive = () => api.egressStatus().then(setLive).catch(() => setLive(null))
  useEffect(() => {
	api.egressConfig().then(result => { setS({ proxy: result.config, revision: result.revision }); setSavedProxy(result.config) })
      .catch(() => setS({ proxy: {}, revision: 0 }))
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
	const dirty = JSON.stringify(proxy) !== JSON.stringify(savedProxy)
  const patch = p => setS(x => ({ ...x, proxy: { ...x.proxy, ...p } }))
	const profiles = proxy.profiles || {}
	profileStateRef.current = { revision: s.revision, profiles, dirty, tests: profileTests }
  const profileTypeLabel = profile => profile.type === 'subscription' ? t('Subscription link') : profile.type === 'node' ? t('Individual node') : profile.type === 'existing' ? t('Imported outbound') : profile.type === 'cellular_sim' ? t('Data SIM') : 'SOCKS5'
  const patchExit = (country, p) => patch({ exits: { ...(proxy.exits || {}), [country]: { ...(proxy.exits?.[country] || {}), ...p } } })
	const patchProfile = (id, p) => {
		patch({ profiles: { ...profiles, [id]: { ...profiles[id], ...p } } })
		setProfileTests(current => { const next = { ...current }; delete next[id]; return next })
	}
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
	setProfileTests(current => { const tests = { ...current }; delete tests[id]; return tests })
    setS(current => ({ ...current, proxy: { ...current.proxy, profiles: next },
      updates: current.updates?.proxy_profile_id === id
        ? { proxy_mode: 'auto', proxy_profile_id: '' } : current.updates }))
  }
  const addExit = () => { if (!newCountry) return; patchExit(newCountry, { enabled: true, profile_id: '', keywords: countryKeywords(newCountry) }); setNewCountry('') }
  const available = COUNTRY_CODES.filter(code => !proxy.exits?.[code]).sort((a, b) => countryLabel(a, language).localeCompare(countryLabel(b, language)))
	const save = async () => { setSaving(true); try {
		const saved = await api.saveEgressConfig(proxy, s.revision)
		setS({ proxy: saved.config, revision: saved.revision })
		setSavedProxy(saved.config); showToast(t('Saved'))
	} catch (e) { showToast(`${t('Error')}: ${e.message}`) } finally { setSaving(false) } }
	const apply = async () => { if (dirty || !s.revision) return; setApplying(true); try {
		await api.applyEgress(s.revision); showToast(t('Applied')); setTimeout(loadLive, 1000)
	} catch (e) { showToast(`${t('Error')}: ${e.message}`) } finally { setApplying(false) } }
	const testProfile = async id => { if (dirty || profileTests[id]?.state === 'testing') return
		const expectedProfile = JSON.stringify(profiles[id] || {})
		setProfileTests(current => ({ ...current, [id]: { state: 'testing', revision: s.revision, profile: expectedProfile } }))
		try {
			const response = await api.testEgressProfile(id, s.revision); const result = response.result || {}
			const latest = await api.egressConfig()
			const stillExact = Number(response.config_revision) === Number(s.revision) && Number(latest.revision) === Number(s.revision) &&
				JSON.stringify(latest.config?.profiles?.[id] || {}) === expectedProfile
			if (!stillExact) throw new Error(t('The saved proxy changed while its test was running; the result was discarded.'))
			const currentState = profileStateRef.current
			const pending = currentState.tests[id]
			if (currentState.dirty || Number(currentState.revision) !== Number(s.revision) ||
				JSON.stringify(currentState.profiles[id] || {}) !== expectedProfile || pending?.state !== 'testing' ||
				pending.revision !== s.revision || pending.profile !== expectedProfile) {
				throw new Error(t('The saved proxy changed while its test was running; the result was discarded.'))
			}
			setProfileTests(current => ({ ...current, [id]: { state: 'passed', revision: s.revision, profile: expectedProfile, ...result } }))
			showToast(t('Node UDP test passed ({latency} ms via {target})', { latency: result.latency_ms, target: result.target || '—' }))
		} catch (error) {
			setProfileTests(current => {
				const pending = current[id]
				if (pending?.state !== 'testing' || pending.revision !== s.revision || pending.profile !== expectedProfile) return current
				return { ...current, [id]: { state: 'failed', revision: s.revision, profile: expectedProfile, error: error.message } }
			})
			showToast(error.message)
		}
	}
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
		<div className="u-proxy-actions"><small className="u-muted">{dirty ? t('Save this proxy before testing it.') : profileTests[id]?.state === 'passed' ? `${t('UDP test passed')} · ${profileTests[id].latency_ms} ms · ${profileTests[id].target}` : profileTests[id]?.state === 'failed' ? profileTests[id].error : t('Profile tests are isolated and do not apply or reload a country exit.')}</small>{['node', 'socks5'].includes(profile.type) && <button className="btn btn-ghost" disabled={dirty || profileTests[id]?.state === 'testing'} onClick={() => testProfile(id)}>{t(profileTests[id]?.state === 'testing' ? 'Testing…' : 'Test node UDP')}</button>}<button className="btn btn-ghost u-proxy-remove" onClick={() => removeProfile(id)}>{t('Remove')}</button></div>
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
	<div className="u-inline"><button className="btn btn-primary" disabled={saving || !dirty} onClick={save}>{t(saving ? 'Saving…' : 'Save')}</button><button className="btn btn-ghost" disabled={applying || dirty || !s.revision} onClick={apply}>{t(applying ? 'Applying…' : 'Apply saved configuration')}</button></div>
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
