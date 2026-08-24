import React, { useEffect } from 'react'
import { useI18n } from '../i18n.jsx'
import { compactReaderName, lineCompositeStatus } from '../linePresentation.js'

const virtualReaderName = (slot) =>
  `Virtual PCD 00 ${Math.max(0, Number(slot) || 0).toString(16).toUpperCase().padStart(2, '0')}`

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
  mediaIngress,
  callCoordinator,
  showVoiceReadiness = false,
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
    const isOnline = !!card || devices.some((d) => d.present && String(d.instance_id || '') === String(inst.id))
    const slotIdx = card?.vpcd_slot ?? card?.index ?? inst.reader_index ?? 0
    const slotName = compactReaderName(card?.name || inst.reader_name || virtualReaderName(slotIdx))
    const profileName = (card ? (card.spn || card.profile_name || card.carrier) : '') ||
      inst.carrier || inst.profile_name || inst.name ||
      (inst.mcc && inst.mnc ? `${inst.mcc}-${inst.mnc}` : '') || t('SIM')
    const tail = inst.msisdn ? ` · ${inst.msisdn}` : (inst.iccid ? ` · ICCID: ••••${String(inst.iccid).slice(-4)}` : '')
    const statusText = ` — ${lineCompositeStatus(inst, devices, t, {
      includeBrowserVoice: showVoiceReadiness,
      mediaIngress,
      coordinatorLine: callCoordinator?.line?.(inst.id),
    })}`

    seenIds.add(String(inst.id))
    options.push({
      id: String(inst.id),
      label: `[${t('Slot')} ${slotIdx}] ${slotName} · ${profileName}${tail}${statusText}`,
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
    const cardId = matchedId || `card-${card.index ?? 0}`
    if (seenIds.has(cardId)) continue
    seenIds.add(cardId)

    const slotIdx = card.vpcd_slot ?? card.index ?? 0
    const slotName = compactReaderName(card.name || virtualReaderName(slotIdx))
    const profileName = card.spn || card.profile_name || card.carrier || (card.mcc && card.mnc ? `${card.mcc}-${card.mnc}` : '') || t('SIM')
    const tail = card.iccid ? ` · ICCID: ••••${String(card.iccid).slice(-4)}` : (card.imsi ? ` · IMSI: ••••${String(card.imsi).slice(-4)}` : '')

    options.push({
      id: cardId,
      label: `[${t('Slot')} ${slotIdx}] ${slotName} · ${profileName}${tail}`,
      raw: card,
      isOnline: true,
    })
  }

  const selectedId = selected?.id ? String(selected.id) : (typeof selected === 'string' ? selected : '')

  useEffect(() => {
    if (options.length > 0 && (!selectedId || !options.some((o) => o.id === selectedId))) {
      setSelected((options.find((option) => option.isOnline) || options[0]).id)
    }
  }, [selectedId, options.map((o) => o.id).join(',')]) // eslint-disable-line react-hooks/exhaustive-deps

  if (!options.length) return null

  return (
    <div className="card" style={{ padding: '10px 14px', marginBottom: 14, display: 'flex', alignItems: 'center', gap: 12 }}>
      <span style={{ fontSize: 12, color: 'var(--text-mute)', whiteSpace: 'nowrap', fontWeight: 600 }}>{t(label)}</span>
      <select
        value={selectedId || options[0]?.id || ''}
        onChange={(e) => setSelected(e.target.value)}
        style={{ flex: 1, maxWidth: 580, fontWeight: 500 }}
      >
        {options.map((opt) => (
          <option key={opt.id} value={opt.id}>{opt.label}</option>
        ))}
      </select>
      {options.length === 1 && <span style={{ fontSize: 11, color: 'var(--text-faint)' }}>{t('only line')}</span>}
    </div>
  )
}
