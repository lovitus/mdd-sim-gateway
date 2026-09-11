import React, { useEffect } from 'react'
import { useI18n } from '../i18n.jsx'
import { deviceForLine, lineEndpointLabel, lineCompositeStatus, lineServiceStatus } from '../linePresentation.js'

// Per-page SIM/line picker for multi-SIM setups.
// Clearly labels each option with:
// [Slot #] Reader Name · Profile/Carrier Name · (MSISDN / ICCID tail) — Status
export default function SimSelector({
  instances = [],
  cards = [],
  devices = [],
  selected,
  setSelected,
  label = 'Active SIM / line',
  callCoordinator,
  showVoiceReadiness = false,
  service,
}) {
  const { t } = useI18n()

  const options = []
  const seenIds = new Set()

  // A PC/SC index is only a current transport slot. Match a line by its persisted id/ICCID;
  // reconnecting another card into a reused slot must never borrow this line's label.
  for (const inst of instances) {
    const card = cards.find((c) => c.present && (
      String(c.matched) === String(inst.id) ||
      (c.iccid && inst.iccid && String(c.iccid) === String(inst.iccid))
    ))
    const device=deviceForLine(inst, devices)
    const isOnline = !!card || device?.present === true
    const endpoint=lineEndpointLabel(card,device,inst,t)
    const profileName = (card ? (card.spn || card.profile_name || card.carrier) : '') ||
      inst.carrier || inst.profile_name || inst.name ||
      (inst.mcc && inst.mnc ? `${inst.mcc}-${inst.mnc}` : '') || t('SIM')
    const tail = inst.msisdn ? ` · ${inst.msisdn}` : (inst.iccid ? ` · ICCID: ••••${String(inst.iccid).slice(-4)}` : '')
    const statusText = ` — ${service ? lineServiceStatus(inst, service, t) : lineCompositeStatus(inst, devices, t, {
      includeBrowserVoice: showVoiceReadiness,
      coordinatorLine: callCoordinator?.line?.(inst.id),
    })}`

    seenIds.add(String(inst.id))
    options.push({
      id: String(inst.id),
      label: `${endpoint} · ${profileName}${tail}${statusText}`,
      raw: inst,
      isOnline,
    })
  }

  // 2. Physical cards detected in readers that might not yet have explicit instance configs
  for (const card of cards) {
    if (!card.present) continue
    const matchedId = card.matched ? String(card.matched) : null
    if (matchedId && seenIds.has(matchedId)) continue
    if (card.iccid && instances.some((inst) => String(inst.iccid) === String(card.iccid))) continue
    const cardId = `unconfigured:${JSON.stringify([card.agent_id || '',card.name || '',card.iccid || '',card.index ?? null])}`
    if (seenIds.has(cardId)) continue
    seenIds.add(cardId)

    const endpoint=lineEndpointLabel(card,null,null,t)
    const profileName = card.spn || card.profile_name || card.carrier || (card.mcc && card.mnc ? `${card.mcc}-${card.mnc}` : '') || t('SIM')
    const tail = card.iccid ? ` · ICCID: ••••${String(card.iccid).slice(-4)}` : (card.imsi ? ` · IMSI: ••••${String(card.imsi).slice(-4)}` : '')

    options.push({
      id: cardId,
      label: `${endpoint} · ${profileName}${tail} — ${t('Configure this SIM first')}`,
      raw: card,
      isOnline: true,
      disabled: true,
    })
  }

  const selectedId = selected?.id ? String(selected.id) : (typeof selected === 'string' ? selected : '')
  const selectable=options.filter(option=>!option.disabled)

  useEffect(() => {
    if (selectable.length > 0 && (!selectedId || !selectable.some((o) => o.id === selectedId))) {
      setSelected((selectable.find((option) => option.isOnline) || selectable[0]).id)
    }
  }, [selectedId, options.map((o) => o.id).join(',')]) // eslint-disable-line react-hooks/exhaustive-deps

  if (!options.length) return null

  return (
    <div className="card" style={{ padding: '10px 14px', marginBottom: 14, display: 'flex', alignItems: 'center', gap: 12 }}>
      <span style={{ fontSize: 12, color: 'var(--text-mute)', whiteSpace: 'nowrap', fontWeight: 600 }}>{t(label)}</span>
      <select
        value={selectedId || selectable[0]?.id || ''}
        disabled={!selectable.length}
        onChange={(e) => setSelected(e.target.value)}
        style={{ flex: 1, maxWidth: 580, fontWeight: 500 }}
      >
        {!selectable.length && <option value="">{t('Configure this SIM first')}</option>}
        {options.map((opt) => (
          <option key={opt.id} value={opt.id} disabled={opt.disabled}>{opt.label}</option>
        ))}
      </select>
      {selectable.length === 1 && <span style={{ fontSize: 11, color: 'var(--text-faint)' }}>{t('only line')}</span>}
    </div>
  )
}
