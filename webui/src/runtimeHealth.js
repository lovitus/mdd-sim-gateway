// Shared view of typed Provider/Core facts. Never infer failure from silence,
// expired UI timers, hardware presence, or untrusted raw error text.
function values(fact) {
  if (!fact?.fresh) return null
  return Object.fromEntries(String(fact.detail || '').split(';').map(v => v.split('=', 2)).filter(v => v.length === 2))
}
function count(value) { return /^\d{1,9}$/.test(value || '') ? Number(value) : null }
function timestamp(value) { return value && Number.isFinite(Date.parse(value)) ? value : null }
export function runtimeHealthView(projection) {
  const facts = Object.fromEntries((projection?.facts || []).map(f => [f.layer, f]))
  const stopped = facts.vowifi_intent?.fresh && facts.vowifi_intent?.code === 'vowifi_disabled'
  const data = values(facts.vowifi_runtime)
  const core = values(facts.admission) || {}
  if (!data || !['true', 'false'].includes(data.dpd_enabled) || !['true', 'false'].includes(data.ims_registered)) {
    return stopped ? { state:'off', telemetry_available:false } : null
  }
  const ready = projection?.operations?.vowifi_call?.ready === true || projection?.operations?.vowifi_sms?.ready === true
  const failed = data.dpd_dead === 'true' || count(data.ims_failures) > 0
  const recovering = data.ims_recovering === 'true' || core.core_recovering === 'true'
  return {
    telemetry_available:true,
    state: stopped ? 'off' : ready ? 'ready' : recovering ? 'recovering' : failed ? 'failed' : 'not_ready',
    dpd_enabled: data.dpd_enabled === 'true', dpd_dead: data.dpd_dead === 'true',
    dpd_missed: count(data.dpd_missed), last_inbound_at: timestamp(data.last_inbound_at),
    last_dpd_success_at: timestamp(data.last_dpd_success_at), ims_registered: data.ims_registered === 'true',
    ims_expires_at: timestamp(data.ims_expires_at), ims_failures: count(data.ims_failures),
    ims_status: count(data.ims_status), ims_failure: /^[a-z0-9_]{1,64}$/.test(data.ims_failure || '') ? data.ims_failure : '',
    ims_next_attempt_at: timestamp(data.ims_next_attempt_at), ims_retry_after_until: timestamp(data.ims_retry_after_until),
    core_recovery_due_at: timestamp(core.core_recovery_due_at), core_failure_count: count(core.core_failure_count), core_failure_max: count(core.core_failure_max),
  }
}
export function registrationOutcomeMessage(result, language = 'en') {
  const zh = language === 'zh'
  switch (result?.code) {
    case 'ims_recovering': return zh ? 'IMS 正在恢复；未重复提交 REGISTER。' : 'IMS recovery is already in progress; no duplicate REGISTER was sent.'
    case 'ims_retry_wait': return zh ? 'IMS 正在等待重试；请查看下一次重试时间。' : 'IMS is waiting for its scheduled retry; check the next retry time.'
    case 'ims_registered': return zh ? 'IMS 注册已刷新。' : 'IMS registration refreshed.'
    default: return zh ? '操作已返回；请查看当前 IMS 状态。' : 'The operation returned; check the current IMS state.'
  }
}
