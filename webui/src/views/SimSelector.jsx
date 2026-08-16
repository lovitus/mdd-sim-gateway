import React, { useEffect } from 'react'
import { useI18n } from '../i18n.jsx'

// Per-page SIM/line picker for multi-SIM setups.
// Clearly labels each option with:
// [Slot #] Reader Name · Profile/Carrier Name · (MSISDN / ICCID tail) — Status
export default function SimSelector({ instances = [], cards = [], devices = [], selected, setSelected, label = 'Active SIM / line' }) {
  const { t } = useI18n()

  const options = []
  const seenIds = new Set()

  // 1. Configured instances matched to physical cards or modems
  for (const inst of instances) {
    const card = cards.find((c) => c.present && (
      String(c.matched) === String(inst.id) ||
      (c.iccid && c.iccid === inst.iccid) ||
      (c.index !== undefined && String(c.index) === String(inst.reader_index))
    ))
    const dev = devices.find((d) => d.present && String(d.instance_id || '') === String(inst.id))
    const slotIdx = card?.index ?? inst.reader_index ?? 0
    const slotName = card?.name || inst.reader_name || `Virtual PCD 00 0${slotIdx}`
    const profileName = card?.spn || card?.profile_name || inst.carrier || card?.carrier || inst.name || (inst.mcc && inst.mnc ? `${inst.mcc}-${inst.mnc}` : '') || t('SIM')
    const tail = inst.msisdn ? ` · ${inst.msisdn}` : (card?.iccid ? ` · ICCID: ••••${String(card.iccid).slice(-4)}` : '')
    const statusText = inst.status?.label ? ` — ${t(inst.status.label)}` : ''

    seenIds.add(String(inst.id))
    options.push({
      id: String(inst.id),
      label: `[${t('Slot')} ${slotIdx}] ${slotName} · ${profileName}${tail}${statusText}`,
      raw: inst,
    })
  }

  // 2. Physical cards detected in readers that might not yet have explicit instance configs
  for (const card of cards) {
    if (!card.present) continue
    const matchedId = card.matched ? String(card.matched) : null
    if (matchedId && seenIds.has(matchedId)) continue
    const cardId = matchedId || `card-${card.index ?? 0}`
    if (seenIds.has(cardId)) continue
    seenIds.add(cardId)

    const slotIdx = card.index ?? 0
    const slotName = card.name || `Virtual PCD 00 0${slotIdx}`
    const profileName = card.spn || card.profile_name || card.carrier || (card.mcc && card.mnc ? `${card.mcc}-${card.mnc}` : '') || t('SIM')
    const tail = card.iccid ? ` · ICCID: ••••${String(card.iccid).slice(-4)}` : (card.imsi ? ` · IMSI: ••••${String(card.imsi).slice(-4)}` : '')

    options.push({
      id: cardId,
      label: `[${t('Slot')} ${slotIdx}] ${slotName} · ${profileName}${tail}`,
      raw: card,
    })
  }

  const selectedId = selected?.id ? String(selected.id) : (typeof selected === 'string' ? selected : '')

  useEffect(() => {
    if (options.length > 0 && (!selectedId || !options.some((o) => o.id === selectedId))) {
      setSelected(options[0].id)
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

