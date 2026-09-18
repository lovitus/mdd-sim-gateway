import React from 'react'
import { useI18n } from '../mdd/i18n.jsx'

// Both list and detail render the same adapted fact, not separate status polls.
export default function RuntimeHealth({ health, compact = false }) {
  const { language } = useI18n()
  const zh = language === 'zh'
  if (!health) return <span className="u-muted">{zh ? 'VoWiFi 状态未确认' : 'VoWiFi state unconfirmed'}</span>
  const names = zh ? { off:'VoWiFi 已关闭', ready:'VoWiFi 就绪', recovering:'VoWiFi 恢复中', failed:'VoWiFi 故障', not_ready:'VoWiFi 未就绪' }
    : { off:'VoWiFi off', ready:'VoWiFi ready', recovering:'VoWiFi recovering', failed:'VoWiFi failed', not_ready:'VoWiFi not ready' }
  const time = value => value ? new Date(value).toLocaleString() : '—'
  const label = <span role="status">{names[health.state] || names.not_ready}</span>
  if (compact || health.telemetry_available === false) return label
  const rows = [
    [zh?'最近认证入站':'Last authenticated inbound', time(health.last_inbound_at)],
    [zh?'最近 DPD 成功':'Last DPD success', time(health.last_dpd_success_at)],
    [zh?'DPD 失败次数':'Missed DPD probes', health.dpd_enabled ? health.dpd_missed ?? '—' : (zh?'未启用':'Disabled')],
    [zh?'IMS 注册到期':'IMS registration expiry', time(health.ims_expires_at)],
    [zh?'IMS 连续失败':'Consecutive IMS failures', health.ims_failures ?? '—'],
    [zh?'IMS 失败原因':'IMS failure', health.ims_failure ? `${health.ims_failure} (${health.ims_status ?? '—'})` : '—'],
    [zh?'下次 IMS 重试':'Next IMS retry', time(health.ims_next_attempt_at)],
    [zh?'运营商重试限制至':'Carrier retry prohibited until', time(health.ims_retry_after_until)],
    [zh?'Core 安全恢复计划':'Core idle-recovery schedule', time(health.core_recovery_due_at)],
  ]
  return <div className="u-runtime-health">{label}<dl>{rows.map(([key,value])=><div className="u-detail" key={key}><dt>{key}</dt><dd>{value}</dd></div>)}</dl></div>
}
